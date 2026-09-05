package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatStream は生成しながらトークンを流す。
//
// **使うのは最終回答だけ。** 制御判断 (次に何をするか) は制約デコードで
// 一気に確定させるので、途中を見せる意味がない。回答は書く量に比例して
// 時間がかかる (実測: 1 行あたり約 440ms、20 行で 8 秒) ので、
// ここだけ体感を変える価値がある。
//
// onDelta が空文字でない値を返したら、そこで生成を打ち切る。
// 回答にシステムプロンプトが混ざったときに、**流し切る前に止める**ために使う。
func (c *Client) ChatStream(ctx context.Context, req Request, onDelta func(string) bool) (*Response, error) {
	// CLI 経路は逐次出力を取らない。--output-format stream-json で取れるが、
	// **測定用の経路なので体感速度に意味がない。** 生成し終えてから 1 回で渡す。
	// 呼び出し側 (回答へのシステムプロンプト混入検査) は同じように動く。
	if IsCLIModel(req.Model) {
		resp, err := c.chatCLI(ctx, req)
		if err != nil {
			return nil, err
		}
		if onDelta != nil && resp.Text() != "" {
			onDelta(resp.Text())
		}
		return resp, nil
	}
	c.applyThinkingDefaults(&req)
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("リクエスト整形: %w", err)
	}
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
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
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("推論サーバが %d を返した: %s", resp.StatusCode, truncate(string(raw), 400))
	}

	var text strings.Builder
	out := &Response{}
	sc := bufio.NewScanner(resp.Body)
	// 1 行が既定の 64KB を超えることがある (usage を含む最終チャンクなど)。
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage = *chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				out.setFinish(ch.FinishReason)
			}
			if ch.Delta.Content == "" {
				continue
			}
			text.WriteString(ch.Delta.Content)
			if onDelta != nil && onDelta(ch.Delta.Content) {
				// 呼び出し側が打ち切りを求めた。読み残しは捨てる。
				out.setText(text.String())
				out.Wall = time.Since(start)
				return out, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ストリーム読み取り: %w", err)
	}
	out.setText(text.String())
	out.Wall = time.Since(start)
	return out, nil
}

type streamChunk struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}
