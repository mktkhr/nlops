// Package llm は OpenAI 互換の推論サーバへの薄いクライアント。
//
// モデル差し替えの唯一の接点をこのパッケージに閉じる。ローカルの llama.cpp /
// llama-swap でも、切り分けが必要になった場合のホスト型モデルでも、
// 呼び出し側のコードは変えずに済むようにする。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Message は 1 つの会話メッセージ。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// Request は chat completion のリクエスト。
type Request struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`

	// ReasoningEffort="none" と ChatTemplateKwargs{"enable_thinking":false} は
	// どちらも Qwen3.6 系の thinking を止める。llama.cpp 側の実装差を吸収するため
	// 既定では両方送る (DisableThinking を参照)。
	ReasoningEffort    string         `json:"reasoning_effort,omitempty"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`

	// Stream は ChatStream が立てる。呼び出し側で指定するものではない。
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions はストリーミング時の追加指定。
// usage は既定では最終チャンクに乗らないので明示的に要求する。
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ResponseFormat は structured output の指定。
type ResponseFormat struct {
	Type       string      `json:"type"` // "json_schema"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema は response_format.json_schema の中身。
type JSONSchema struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

// Usage はトークンの使用量。ストリーミングでも同じ形で返るので型にしてある。
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Response は chat completion のレスポンス。llama.cpp 独自の timings も拾う。
type Response struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage   Usage `json:"usage"`
	Timings struct {
		CacheN          int     `json:"cache_n"`
		PromptN         int     `json:"prompt_n"`
		PromptMS        float64 `json:"prompt_ms"`
		PromptPerSecond float64 `json:"prompt_per_second"`
		PredictedN      int     `json:"predicted_n"`
		PredictedMS     float64 `json:"predicted_ms"`
		PredictedPerSec float64 `json:"predicted_per_second"`
	} `json:"timings"`

	// 呼び出し側で計測した実測値。
	Wall time.Duration `json:"-"`
}

// Text は content を返す。
func (r *Response) Text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// FinishReason は終了理由を返す。
// setText / setFinish はストリーミングで組み立てた内容を Response に載せる。
// Chat と同じ形にしておくと、呼び出し側が両者を区別せずに扱える。
func (r *Response) setText(s string) {
	if len(r.Choices) == 0 {
		r.Choices = make([]struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		}, 1)
	}
	r.Choices[0].Message.Content = s
}

func (r *Response) setFinish(s string) {
	r.setText(r.Text())
	r.Choices[0].FinishReason = s
}

func (r *Response) FinishReason() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].FinishReason
}

// Client は推論サーバのクライアント。
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// DisableThinking が true のとき、thinking を止めるパラメータを付与する。
	// Qwen3.6 系では `/no_think` をメッセージに入れる方法は効かないことを
	// 実測で確認済みのため、必ずこの経路を使う。
	DisableThinking bool

	// ReasoningEffort は reasoning_effort に載せる値。
	// Qwen3.6 系は "none" で思考が止まるが、harmony 形式の gpt-oss では
	// "none" が解釈されず思考が止まらないため "low" を指定する必要がある。
	// モデルごとに変わるので外から差し替えられるようにしてある。
	ReasoningEffort string
}

// New は既定設定のクライアントを作る。
func New(baseURL string) *Client {
	return &Client{
		BaseURL:         baseURL,
		HTTP:            &http.Client{Timeout: 10 * time.Minute},
		DisableThinking: true,
	}
}

// Chat は 1 回の chat completion を実行する。
// applyThinkingDefaults は思考を止める指定を埋める。
// Chat と ChatStream で同じ扱いにするために切り出してある。
func (c *Client) applyThinkingDefaults(req *Request) {
	if !c.DisableThinking {
		return
	}
	if req.ReasoningEffort == "" {
		req.ReasoningEffort = c.ReasoningEffort
		if req.ReasoningEffort == "" {
			req.ReasoningEffort = "none"
		}
	}
	if req.ChatTemplateKwargs == nil {
		req.ChatTemplateKwargs = map[string]any{}
	}
	if _, ok := req.ChatTemplateKwargs["enable_thinking"]; !ok {
		req.ChatTemplateKwargs["enable_thinking"] = false
	}
}

func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	c.applyThinkingDefaults(&req)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("リクエスト整形: %w", err)
	}

	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("推論サーバ呼び出し: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("推論サーバが %d を返した: %s", resp.StatusCode, truncate(string(raw), 400))
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("レスポンス解析: %w (body=%s)", err, truncate(string(raw), 400))
	}
	out.Wall = time.Since(start)
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
