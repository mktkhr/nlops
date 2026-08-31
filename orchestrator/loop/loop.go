// Package loop は Tool Execution Loop を実装する。
//
// 1 回につき 1 Tool を選び、結果を Projection してから次の判断へ渡す。
// 履歴は append のみで、途中の要約圧縮は行わない。llama.cpp の
// --cache-reuse を効かせるには prompt prefix がバイト単位で安定している
// 必要があり、履歴を書き換えると全再 prefill になるため。
// context 削減は Tool 結果を追加する時点の Projection だけで行う。
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mktkhr/nlops/orchestrator/executor"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/prompt"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

// Mode はルーティングの段数。
type Mode string

const (
	// ModeOneStage は全 Tool を毎回提示する。prompt prefix が固定なので
	// prefix cache が最大限効く。
	ModeOneStage Mode = "one_stage"
	// ModeTwoStage は先にサービスを選び、そのサービスの Tool だけを提示する。
	// context は減るが Stage 2 の prefix が要求ごとに変わるためキャッシュは効きにくい。
	ModeTwoStage Mode = "two_stage"
)

// Options は 1 回の実行設定。
type Options struct {
	Model      string
	Mode       Mode
	StrictArgs bool
	MaxSteps   int
	MaxTokens  int
	Answer     bool // 最終回答の自然言語生成まで行うか

	// StopGuard が true のとき、空振りが 2 回続いた時点で Tool の選択肢を外し
	// finish しか選べなくする。過剰探索でレイテンシが伸びる失敗を実測したため。
	StopGuard bool

	// OnStep は 1 ステップ完了ごとに呼ばれる。BFF が進捗をストリームするために使う。
	OnStep func(Step)
}

// Step は Tool Loop の 1 反復。
type Step struct {
	Iteration int              `json:"iteration"`
	Tool      string           `json:"tool,omitempty"`
	Arguments map[string]any   `json:"arguments,omitempty"`
	Finish    bool             `json:"finish,omitempty"`
	Forced    bool             `json:"forced,omitempty"` // 空振り連続で finish を強制した
	Result    *executor.Result `json:"result,omitempty"`
	LLMms     float64          `json:"llm_ms"`
	PromptTok int              `json:"prompt_tokens"`
	CachedTok int              `json:"cached_tokens"`
	CompTok   int              `json:"completion_tokens"`
}

// Trace は 1 要求の実行記録。Observability の最小形。
type Trace struct {
	Query    string   `json:"query"`
	UserID   string   `json:"user_id"`
	Role     string   `json:"role"`
	Mode     string   `json:"mode"`
	Model    string   `json:"model"`
	Services []string `json:"services,omitempty"`
	Steps    []Step   `json:"steps"`
	Answer   string   `json:"answer,omitempty"`

	RouteMS    float64 `json:"route_ms"`
	AnswerMS   float64 `json:"answer_ms"`
	TotalMS    float64 `json:"total_ms"`
	PromptTok  int     `json:"prompt_tokens"`
	CachedTok  int     `json:"cached_tokens"`
	CompTok    int     `json:"completion_tokens"`
	RawBytes   int     `json:"raw_bytes"`  // Projection 前の API レスポンス合計
	ProjBytes  int     `json:"proj_bytes"` // LLM へ渡した合計
	Denied     bool    `json:"denied"`
	Incomplete bool    `json:"incomplete"` // MaxSteps に到達して打ち切った
	Err        string  `json:"error,omitempty"`
}

// ToolsUsed は実行された Tool 名を順に返す。
func (t *Trace) ToolsUsed() []string {
	var out []string
	for _, s := range t.Steps {
		if s.Tool != "" && s.Result != nil {
			out = append(out, s.Tool)
		}
	}
	return out
}

// Runner は Tool Loop を回す。
type Runner struct {
	Catalog  *toolschema.Catalog
	LLM      *llm.Client
	Executor *executor.Executor
}

// New は Runner を作る。
func New(cat *toolschema.Catalog, client *llm.Client) *Runner {
	return &Runner{Catalog: cat, LLM: client, Executor: executor.New(cat)}
}

// Run は 1 つの自然言語要求を処理する。
func (r *Runner) Run(ctx context.Context, id authctx.Identity, query string, opt Options) *Trace {
	if opt.MaxSteps == 0 {
		opt.MaxSteps = 6
	}
	if opt.MaxTokens == 0 {
		opt.MaxTokens = 512
	}
	start := time.Now()
	tr := &Trace{Query: query, UserID: id.UserID, Role: string(id.Role),
		Mode: string(opt.Mode), Model: opt.Model}
	r.Executor.Reset(query)

	tools := r.Catalog.Tools()
	if opt.Mode == ModeTwoStage {
		svcs, resp, err := r.routeServices(ctx, query, opt)
		if resp != nil {
			tr.RouteMS = ms(resp.Wall)
			tr.PromptTok += resp.Usage.PromptTokens
			tr.CachedTok += resp.Usage.PromptTokensDetails.CachedTokens
			tr.CompTok += resp.Usage.CompletionTokens
		}
		if err != nil {
			tr.Err = "service routing: " + err.Error()
			tr.TotalMS = ms(time.Since(start))
			return tr
		}
		tr.Services = svcs
		tools = r.Catalog.Tools(svcs...)
		if len(tools) == 0 {
			tr.Err = "有効なサービスが選ばれませんでした"
			tr.TotalMS = ms(time.Since(start))
			return tr
		}
	}

	// 履歴は append のみ。prefix を壊さないため書き換えない。
	msgs := []llm.Message{
		{Role: "system", Content: prompt.LoopSystem(tools)},
		{Role: "user", Content: query},
	}
	// executed は Tool 実行が 1 回でも成立したか。成立するまで finish を許さない。
	executed := false
	// barren は「収穫のない結果」が連続した回数。
	barren := 0
	// seenCalls は実行済みの (Tool, 引数) の組。小型モデルが同じ呼び出しを
	// 繰り返して進捗しない失敗を実測したため、2 回目以降は実行せず差し戻す。
	seenCalls := map[string]bool{}

	for i := 1; i <= opt.MaxSteps; i++ {
		step := Step{Iteration: i}
		schema := prompt.LoopSchema(tools, opt.StrictArgs, executed)
		// 空振りが続いたら Tool の選択肢自体を外す。プロンプトでの依頼より確実。
		forced := opt.StopGuard && executed && barren >= 2
		if forced {
			schema = prompt.FinishOnlySchema()
		}
		resp, err := r.LLM.Chat(ctx, llm.Request{
			Model: opt.Model, Temperature: 0, MaxTokens: opt.MaxTokens, Messages: msgs,
			ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: schema},
		})
		if resp != nil {
			step.LLMms = ms(resp.Wall)
			step.PromptTok = resp.Usage.PromptTokens
			step.CachedTok = resp.Usage.PromptTokensDetails.CachedTokens
			step.CompTok = resp.Usage.CompletionTokens
			tr.PromptTok += step.PromptTok
			tr.CachedTok += step.CachedTok
			tr.CompTok += step.CompTok
		}
		if err != nil {
			tr.Err = fmt.Sprintf("step %d: %v", i, err)
			tr.Steps = append(tr.Steps, step)
			break
		}

		if resp.FinishReason() == "length" {
			tr.Err = fmt.Sprintf("step %d: max_tokens 到達で content が空 (thinking が止まっていない可能性)", i)
			tr.Steps = append(tr.Steps, step)
			break
		}
		var decision struct {
			Next      string         `json:"next"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(resp.Text()), &decision); err != nil {
			tr.Err = fmt.Sprintf("step %d: JSON 不正: %v", i, err)
			tr.Steps = append(tr.Steps, step)
			break
		}
		if decision.Next == "finish" {
			step.Finish = true
			step.Forced = forced
			tr.Steps = append(tr.Steps, step)
			emit(opt, step)
			goto answer
		}

		step.Tool = decision.Tool
		step.Arguments = decision.Arguments

		sig := callSignature(decision.Tool, decision.Arguments)
		var res executor.Result
		if seenCalls[sig] {
			res = executor.Result{Tool: decision.Tool, Error: ErrDuplicateCall +
				": この Tool と引数の組み合わせは実行済みで、結果は上にあります。" +
				"別の Tool を選ぶか、情報が揃っているなら finish してください。"}
		} else {
			seenCalls[sig] = true
			res = r.Executor.Execute(ctx, id, executor.Call{Tool: decision.Tool, Arguments: decision.Arguments})
		}
		step.Result = &res
		tr.RawBytes += res.RawBytes
		tr.ProjBytes += res.ProjBytes
		if res.Denied {
			tr.Denied = true
		}
		if res.Error == "" {
			executed = true
		}
		if barrenResult(res) {
			barren++
		} else {
			barren = 0
		}
		tr.Steps = append(tr.Steps, step)
		emit(opt, step)

		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: resp.Text()},
			llm.Message{Role: "user", Content: renderResult(decision.Tool, res, i, opt.MaxSteps)})

		if i == opt.MaxSteps {
			tr.Incomplete = true
		}
	}

answer:
	if opt.Answer && tr.Err == "" {
		ans, resp, err := r.finalAnswer(ctx, msgs, query, opt)
		if resp != nil {
			tr.AnswerMS = ms(resp.Wall)
			tr.PromptTok += resp.Usage.PromptTokens
			tr.CachedTok += resp.Usage.PromptTokensDetails.CachedTokens
			tr.CompTok += resp.Usage.CompletionTokens
		}
		if err != nil {
			tr.Err = "final answer: " + err.Error()
		} else {
			tr.Answer = ans
		}
	}
	tr.TotalMS = ms(time.Since(start))
	return tr
}

func (r *Runner) routeServices(ctx context.Context, query string, opt Options) ([]string, *llm.Response, error) {
	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model: opt.Model, Temperature: 0, MaxTokens: opt.MaxTokens,
		Messages: []llm.Message{
			{Role: "system", Content: prompt.ServiceRouterSystem(r.Catalog)},
			{Role: "user", Content: query},
		},
		ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: prompt.ServiceRouterSchema(r.Catalog)},
	})
	if err != nil {
		return nil, resp, err
	}
	var out struct {
		Services []string `json:"services"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil {
		return nil, resp, fmt.Errorf("JSON 不正: %w", err)
	}
	sel := map[string]bool{}
	for _, s := range out.Services {
		sel[s] = true
	}
	var ordered []string
	for _, n := range r.Catalog.ServiceNames() {
		if sel[n] {
			ordered = append(ordered, n)
		}
	}
	return ordered, resp, nil
}

// finalAnswer は集めた Tool 結果から自然言語の回答を作る。
// 制御判断と違い、ここはスキーマ制約をかけない。
func (r *Runner) finalAnswer(ctx context.Context, history []llm.Message, query string, opt Options) (string, *llm.Response, error) {
	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.Message{Role: "system", Content: prompt.AnswerSystem()})
	// system と最初の user はそのまま流用せず、Tool 結果だけを引き継ぐ。
	msgs = append(msgs, llm.Message{Role: "user", Content: query})
	for _, m := range history[2:] {
		msgs = append(msgs, m)
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: "以上の Tool 結果を根拠に、最初の要求へ答えてください。"})

	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model: opt.Model, Temperature: 0, MaxTokens: 512, Messages: msgs,
	})
	if err != nil {
		return "", resp, err
	}
	return resp.Text(), resp, nil
}

// renderResult は Tool 結果を LLM へ返す形に整える。
// Projection 済みのデータのみを載せる。生レスポンスはここへ来ない。
// 残りステップ数を添えるのは、打ち切りが近いことをモデルへ伝えて
// 過剰探索を抑えるため。system prompt には入れないので prefix は壊れない。
func renderResult(tool string, res executor.Result, step, maxSteps int) string {
	head := fmt.Sprintf("[tool_result] %s (step %d/%d)", tool, step, maxSteps)
	if res.Error != "" {
		return fmt.Sprintf("%s\n{\"error\":%q}", head, res.Error)
	}
	b, err := json.Marshal(res.Projected)
	if err != nil {
		return fmt.Sprintf("%s\n{\"error\":\"整形失敗\"}", head)
	}
	return fmt.Sprintf("%s\n%s", head, b)
}

// barrenResult は「次の判断材料が増えなかった結果」かどうかを返す。
func barrenResult(res executor.Result) bool {
	if res.Error != "" || res.Denied || (res.Status != 0 && res.Status != 200) {
		return true
	}
	m, ok := res.Projected.(map[string]any)
	if !ok {
		return false
	}
	if items, ok := m["items"].([]any); ok {
		return len(items) == 0
	}
	return len(m) == 0
}

func emit(opt Options, s Step) {
	if opt.OnStep != nil {
		opt.OnStep(s)
	}
}

// ErrDuplicateCall は同一の Tool 呼び出しの繰り返しを差し戻すときの印。
const ErrDuplicateCall = "duplicate_call"

// callSignature は (Tool, 引数) の同一性を判定するキーを返す。
// encoding/json は map のキーをソートして出すので安定する。
func callSignature(tool string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return tool
	}
	return tool + "|" + string(b)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
