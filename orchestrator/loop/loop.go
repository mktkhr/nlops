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
	"strings"
	"time"

	"github.com/mktkhr/nlops/orchestrator/executor"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/command"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/prompt"
	"github.com/mktkhr/nlops/pkg/toolschema"
	"github.com/mktkhr/nlops/pkg/uiroute"
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

	// IntentGate が true のとき、Loop に入る前に「画面を開くだけで済むか」を
	// 2 択で判定し、以降の選択肢をその側だけに絞る。
	// 27 分岐の中からモードを選ばせると安定しなかったため設けた。
	IntentGate bool

	// OnStep は 1 ステップ完了ごとに呼ばれる。BFF が進捗をストリームするために使う。
	OnStep func(Step)

	// History は同じ会話の過去のやり取り。古いものから順に並べる。
	//
	// **Tool の結果は入れない。質問と最終回答だけを入れる。**
	// 途中の結果まで積むと、3 往復で context が数万トークンになり、
	// この PoC が拠り所にしている prefix cache の利点を失う。
	//
	// system prompt の後ろへ append するだけなので、
	// 会話が伸びても prefix は壊れない (`--cache-reuse` が効き続ける)。
	History []Turn

	// Thinking が true のとき、モデルの思考を有効にする。
	//
	// **要求ごとに切り替える**必要があるので、llm.Client の DisableThinking では
	// なく Request 側で指定する (Client は 1 つを共有しているため)。
	// 既定は false。実測では精度が 98% → 78% に落ち、失敗の大半は
	// 「制約デコードの JSON が出てこない」という最悪の壊れ方をする
	// (docs/decisions.md「モデルの thinking を有効にするとどうなるか」)。
	Thinking bool

	// ForceFirst が設定されていると、その Tool 呼び出しをモデルが選んだことにして
	// step 1 として実行し、以降は通常どおり Loop を回す。
	//
	// 「間違った Tool を選んでしまった後に戻れるか」を測るための計測器。
	// 選定精度が高いと誤選択が自然発生しないので、回復挙動だけを切り離して
	// 観測できない。**検証専用であり、通常運用では使わない。**
	ForceFirst *executor.Call
}

// Turn は 1 往復。連続した問い合わせで前の文脈を渡すために使う。
type Turn struct {
	Query  string `json:"query"`
	Answer string `json:"answer"`
}

// Navigation は LLM が生成した画面の状態。
// 元案 §14 の「WRITE API を直接実行せず Frontend の状態を返す」経路。
type Navigation struct {
	Route   string            `json:"route"`
	Filters map[string]string `json:"filters,omitempty"`
	Reason  string            `json:"reason,omitempty"`
}

// Proposal は LLM が生成した更新操作の提案。
//
// **これは実行されない。** 人間が画面で確認し、承認して初めて
// BFF が該当サービスの更新 API を呼ぶ。実行可否の業務判断もサービス側にある。
type Proposal struct {
	Command   string         `json:"command"`
	Title     string         `json:"title"`
	Arguments map[string]any `json:"arguments"`
	Reason    string         `json:"reason,omitempty"`
	Confirm   string         `json:"confirm,omitempty"`
}

// Step は Tool Loop の 1 反復。
type Step struct {
	Iteration int              `json:"iteration"`
	Tool      string           `json:"tool,omitempty"`
	Arguments map[string]any   `json:"arguments,omitempty"`
	Finish    bool             `json:"finish,omitempty"`
	Forced    bool             `json:"forced,omitempty"` // 空振り連続で finish を強制した
	Navigate  *Navigation      `json:"navigate,omitempty"`
	Proposal  *Proposal        `json:"proposal,omitempty"`
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

	// Navigate は「画面を開いて絞り込めば済む」と判断された場合の遷移先。
	Navigate *Navigation `json:"navigate,omitempty"`

	// Proposal は更新操作の提案。実行はされていない。
	Proposal *Proposal `json:"proposal,omitempty"`

	// Filters は Loop 全体で実際に適用された絞り込み条件。
	//
	// モデルが利用者の求めていない条件を勝手に足す失敗があり
	// (「高橋みどりさんの一番古い注文」に status=PLACED)、
	// **それを構造で防ぐ方法は見つかっていない** (decisions.md 参照)。
	// 防げないので、代わりに**何で絞ったかを利用者へ見せる**。
	// 「頼んでいない条件が付いている」ことは人間なら一目で分かる。
	Filters map[string]string `json:"filters,omitempty"`

	Intent     string  `json:"intent,omitempty"` // IntentGate 使用時の判定結果
	IntentMS   float64 `json:"intent_ms"`
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

	// Routes が設定されているとき、LLM は画面遷移を選べるようになる。
	Routes *uiroute.Catalog

	// Commands が設定されているとき、LLM は更新操作を「提案」できるようになる。
	// 提案するだけで、Loop は決して実行しない。
	Commands *command.Catalog
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
	// 前のやり取りに出た ID も「利用者が知っている値」として扱う。
	// そうしないと「その注文の出荷は？」で未解決 ID の差し戻しに当たる。
	r.Executor.Reset(historyText(opt.History) + query)

	// モード判定。navigate 側と決まったら Tool の選択肢を渡さない。
	routes := r.Routes
	navigateOnly := false
	writeMode := false
	if opt.IntentGate && (routes != nil || r.Commands != nil) {
		mode, resp, err := r.classifyIntent(ctx, query, opt)
		if resp != nil {
			tr.IntentMS = ms(resp.Wall)
			tr.PromptTok += resp.Usage.PromptTokens
			tr.CachedTok += resp.Usage.PromptTokensDetails.CachedTokens
			tr.CompTok += resp.Usage.CompletionTokens
		}
		if err == nil {
			tr.Intent = mode
			switch mode {
			case "navigate":
				navigateOnly = true
			case "write":
				writeMode = true
				routes = nil
			default:
				routes = nil
			}
		}
	}

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
	system := prompt.LoopSystem(tools, routes)
	switch {
	case navigateOnly:
		system = prompt.NavigateOnlySystem(routes)
	case writeMode:
		system = prompt.WriteSystem(tools, r.Commands)
	}
	// system → 過去のやり取り → 今回の質問。この順なら会話が伸びても
	// 前半は変わらないので、prefix cache が効き続ける。
	msgs := make([]llm.Message, 0, 2+2*len(opt.History))
	msgs = append(msgs, llm.Message{Role: "system", Content: system})
	for _, t := range opt.History {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: t.Query},
			llm.Message{Role: "assistant", Content: t.Answer})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: query})
	// 履歴の分だけ、Loop 中に書き換えてはいけない先頭が伸びる。
	head := len(msgs)
	// executed は Tool 実行が 1 回でも成立したか。成立するまで finish を許さない。
	executed := false
	// barren は「収穫のない結果」が連続した回数。
	barren := 0
	// navigated は画面遷移で終わったか。終わっていれば最終回答は作らない。
	navigated := false
	// seenCalls は実行済みの (Tool, 引数) の組。小型モデルが同じ呼び出しを
	// 繰り返して進捗しない失敗を実測したため、2 回目以降は実行せず差し戻す。
	seenCalls := map[string]bool{}

	// 検証用: 間違った Tool を踏んだ状態から Loop を始める。
	// モデルが自分で選んだときと履歴の形を揃えないと、回復挙動が変わってしまう。
	startAt := 1
	if opt.ForceFirst != nil {
		call := *opt.ForceFirst
		res := r.Executor.Execute(ctx, id, call)
		step := Step{Iteration: 1, Tool: call.Tool, Arguments: call.Arguments, Result: &res, Forced: true}
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
		}
		seenCalls[callSignature(call.Tool, call.Arguments)] = true
		tr.Steps = append(tr.Steps, step)
		emit(opt, step)
		decision, _ := json.Marshal(map[string]any{
			"next": "tool", "tool": call.Tool, "arguments": call.Arguments,
			"reason": "対象を確認します。",
		})
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: string(decision)},
			llm.Message{Role: "user", Content: renderResult(call.Tool, res, 1, opt.MaxSteps)})
		startAt = 2
	}

	for i := startAt; i <= opt.MaxSteps; i++ {
		step := Step{Iteration: i}
		schema := prompt.LoopSchema(tools, routes, opt.StrictArgs, executed)
		switch {
		case navigateOnly:
			schema = prompt.NavigateOnlySchema(routes)
		case writeMode:
			// 対象を1回特定したら Tool を取り上げ、提案か終了だけを残す。
			writeTools := tools
			if executed {
				writeTools = nil
			}
			schema = prompt.WriteSchema(writeTools, r.Commands, opt.StrictArgs, executed)
		}
		// 空振りが続いたら Tool の選択肢自体を外す。プロンプトでの依頼より確実。
		forced := opt.StopGuard && executed && barren >= 2
		if forced {
			schema = prompt.FinishOnlySchema()
		}
		effort, kwargs := think(opt)
		resp, err := r.LLM.Chat(ctx, llm.Request{
			Model: opt.Model, Temperature: 0, MaxTokens: opt.MaxTokens, Messages: msgs,
			ResponseFormat:  &llm.ResponseFormat{Type: "json_schema", JSONSchema: schema},
			ReasoningEffort: effort, ChatTemplateKwargs: kwargs,
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
			// 何を吐いて打ち切られたかが分からないと原因を特定できない。
			head := resp.Text()
			if len(head) > 300 {
				head = head[:300] + "…"
			}
			tr.Err = fmt.Sprintf("step %d: max_tokens 到達 (%d tok)。生成: %q", i, step.CompTok, head)
			tr.Steps = append(tr.Steps, step)
			break
		}
		var decision struct {
			Next      string            `json:"next"`
			Tool      string            `json:"tool"`
			Arguments map[string]any    `json:"arguments"`
			Route     string            `json:"route"`
			Command   string            `json:"command"`
			Filters   map[string]string `json:"filters"`
			Reason    string            `json:"reason"`
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

		if decision.Next == "propose" {
			prop, retry := r.resolveProposal(decision.Command, decision.Arguments, decision.Reason)
			if retry != "" {
				step.Tool = "propose"
				step.Result = &executor.Result{Tool: "propose", Error: retry}
				barren++
				tr.Steps = append(tr.Steps, step)
				emit(opt, step)
				msgs = append(msgs,
					llm.Message{Role: "assistant", Content: resp.Text()},
					llm.Message{Role: "user", Content: fmt.Sprintf("[tool_result] propose (step %d/%d)\n{\"error\":%q}", i, opt.MaxSteps, retry)})
				continue
			}
			step.Proposal = prop
			tr.Proposal = prop
			tr.Steps = append(tr.Steps, step)
			emit(opt, step)
			// 提案を作るのが答え。実行はしないし、最終回答も作らない。
			tr.Answer = prop.Reason
			navigated = true
			goto answer
		}

		if decision.Next == "navigate" {
			nav, retry := r.resolveNavigation(decision.Route, decision.Filters, decision.Reason)
			if retry != "" {
				// フィルタの ID が未解決。遷移させずに差し戻して選び直させる。
				step.Tool = "navigate"
				step.Result = &executor.Result{Tool: "navigate", Error: retry}
				barren++
				tr.Steps = append(tr.Steps, step)
				emit(opt, step)
				msgs = append(msgs,
					llm.Message{Role: "assistant", Content: resp.Text()},
					llm.Message{Role: "user", Content: fmt.Sprintf("[tool_result] navigate (step %d/%d)\n{\"error\":%q}", i, opt.MaxSteps, retry)})
				continue
			}
			step.Navigate = nav
			tr.Navigate = nav
			tr.Steps = append(tr.Steps, step)
			emit(opt, step)
			// 画面を開くのが答えなので、最終回答の生成は行わない。
			tr.Answer = nav.Reason
			navigated = true
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
		if res.Error == "" {
			// 差し戻された呼び出しは実際には絞り込んでいないので数えない。
			recordFilters(tr, decision.Arguments)
		}
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
	if opt.Answer && tr.Err == "" && !navigated {
		ans, resp, err := r.finalAnswer(ctx, msgs, head, query, opt)
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
	effort, kwargs := think(opt)
	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model: opt.Model, Temperature: 0, MaxTokens: opt.MaxTokens,
		ReasoningEffort: effort, ChatTemplateKwargs: kwargs,
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
//
// head は Loop の履歴のうち「Tool 結果より前」の長さ。
// system prompt と過去のやり取りと今回の質問がそこに入っており、
// 長さは会話の往復数で変わる。決め打ちで読み飛ばすと、
// **前のやり取りを Tool 結果として渡してしまう**。
func (r *Runner) finalAnswer(ctx context.Context, history []llm.Message, head int,
	query string, opt Options) (string, *llm.Response, error) {

	msgs := make([]llm.Message, 0, len(history)+2)
	msgs = append(msgs, llm.Message{Role: "system", Content: prompt.AnswerSystem()})
	// Loop の system prompt は流用しない。過去のやり取りは、
	// 「その注文の…」のような指示語を解けるようにするため引き継ぐ。
	for _, t := range opt.History {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: t.Query},
			llm.Message{Role: "assistant", Content: t.Answer})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: query})
	for _, m := range history[head:] {
		msgs = append(msgs, m)
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: "以上の Tool 結果を根拠に、最初の要求へ答えてください。"})

	// 最終回答は短くてよいので 512 を下限にするが、**上限は Loop と揃える**。
	// 512 を直書きしていたため、思考を有効にすると reasoning が枠を食い切り
	// content が空のまま返っていた (回答だけが消えるので気づきにくい)。
	maxTok := 512
	if opt.MaxTokens > maxTok {
		maxTok = opt.MaxTokens
	}
	// ここは response_format を使わないので、制約デコードと思考が干渉しない
	// 唯一の呼び出し。思考を入れるなら本来ここが一番安全。
	effort, kwargs := think(opt)
	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model: opt.Model, Temperature: 0, MaxTokens: maxTok, Messages: msgs,
		ReasoningEffort: effort, ChatTemplateKwargs: kwargs,
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
// think は 1 リクエスト分の思考設定を返す。
//
// llm.Client は値が空のときだけ既定 (思考オフ) を埋めるので、
// ここで明示すればクライアント共有のままでも要求ごとに切り替えられる。
func think(opt Options) (string, map[string]any) {
	if !opt.Thinking {
		return "", nil // クライアント側の既定 (思考オフ) に任せる
	}
	return "high", map[string]any{"enable_thinking": true}
}

// recordFilters は実際に適用された絞り込み条件を集める。
// 呼び出しが成功したときだけ呼ぶ (差し戻された条件を見せても混乱するだけ)。
func recordFilters(tr *Trace, args map[string]any) {
	for k, v := range args {
		if !prompt.NarrowingParams[k] {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if tr.Filters == nil {
			tr.Filters = map[string]string{}
		}
		tr.Filters[k] = s
	}
}

// historyText は過去のやり取りを 1 つの文字列にする。
// Executor が「利用者の入力に現れた語」を拾うために使う。
func historyText(hist []Turn) string {
	var b strings.Builder
	for _, t := range hist {
		b.WriteString(t.Query)
		b.WriteByte('\n')
		b.WriteString(t.Answer)
		b.WriteByte('\n')
	}
	return b.String()
}

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

// resolveNavigation は LLM が出した遷移先を検証する。
// 定義外のフィルタは落とし、未解決の ID は差し戻す。
// 戻り値の 2 つ目が空でなければ遷移させず、その内容を LLM へ返す。
// classifyIntent は「画面を開くだけで済むか」を 2 択で判定する。
func (r *Runner) classifyIntent(ctx context.Context, query string, opt Options) (string, *llm.Response, error) {
	// **Intent Gate には思考を入れない。** 出力は 1 文字 (n/t/w) で
	// max_tokens 16 しかないため、思考を有効にすると必ず枠を使い切って空になる。
	msgs := []llm.Message{{Role: "system", Content: prompt.IntentSystem(r.Routes, r.Commands)}}
	// 直前の 1 往復だけ渡す。「その注文の出荷は？」のような指示語は、
	// 前を見ないと画面遷移か Tool かの判断がつかない。
	// 全部渡さないのは、1 文字を返すだけの判定にしては重くなるため。
	if n := len(opt.History); n > 0 {
		last := opt.History[n-1]
		msgs = append(msgs,
			llm.Message{Role: "user", Content: last.Query},
			llm.Message{Role: "assistant", Content: last.Answer})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: query})

	resp, err := r.LLM.Chat(ctx, llm.Request{
		Model: opt.Model, Temperature: 0, MaxTokens: 16, Messages: msgs,
		ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: prompt.IntentSchema()},
	})
	if err != nil {
		return "", resp, err
	}
	var out struct {
		M string `json:"m"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil {
		return "", resp, err
	}
	switch out.M {
	case "n":
		return "navigate", resp, nil
	case "w":
		return "write", resp, nil
	}
	return "tool", resp, nil
}

func (r *Runner) resolveNavigation(route string, filters map[string]string, reason string) (*Navigation, string) {
	if r.Routes == nil {
		return nil, "画面遷移は利用できません。Tool を使ってください。"
	}
	def, ok := r.Routes.ByPath(route)
	if !ok {
		return nil, fmt.Sprintf("画面 %q は存在しません。", route)
	}
	clean := def.Sanitize(filters)

	// Tool 引数と同じ基準で ID の出所を確かめる。
	asAny := make(map[string]any, len(clean))
	for k, v := range clean {
		asAny[k] = v
	}
	if bad := r.Executor.UnresolvedIDs(asAny, enumFilters(def)); len(bad) > 0 {
		return nil, fmt.Sprintf("%s: フィルタ %s の値は未解決です。先に検索系の Tool で ID を取得してください。",
			executor.ErrUnresolvedID, strings.Join(bad, ", "))
	}
	return &Navigation{Route: def.Path, Filters: clean, Reason: reason}, ""
}

// resolveProposal は提案されたコマンドと引数を検証する。
// **ここでは実行しない。** 実行は人間の確認を経て BFF が行う。
func (r *Runner) resolveProposal(name string, args map[string]any, reason string) (*Proposal, string) {
	if r.Commands == nil {
		return nil, "更新操作は利用できません。"
	}
	cmd, ok := r.Commands.ByName(name)
	if !ok {
		return nil, fmt.Sprintf("操作 %q は存在しません。", name)
	}
	clean, err := cmd.Validate(args)
	if err != nil {
		return nil, err.Error()
	}
	// Tool 引数と同じ基準で ID の出所を確かめる。捏造した ID で更新を提案させない。
	if bad := r.Executor.UnresolvedIDs(clean, enumCommandParams(cmd)); len(bad) > 0 {
		return nil, fmt.Sprintf("%s: 引数 %s の値は未解決です。先に検索系の Tool で対象を特定してください。",
			executor.ErrUnresolvedID, strings.Join(bad, ", "))
	}
	// 候補が複数あった一覧から拾っただけの ID で更新を提案させない。
	// 承認画面には 1 件しか出ないので、他に候補があったことが承認者から消える。
	if bad := r.Executor.AmbiguousIDs(clean, enumCommandParams(cmd)); len(bad) > 0 {
		return nil, fmt.Sprintf("%s: 引数 %s。"+
			"更新対象は 1 件に絞り込めていなければなりません。"+
			"検索条件を狭めるか、対象が特定できないことを利用者に伝えて finish してください。",
			executor.ErrAmbiguousID, strings.Join(bad, ", "))
	}
	return &Proposal{
		Command: cmd.Name, Title: cmd.Title, Arguments: clean,
		Reason: reason, Confirm: cmd.Confirm,
	}, ""
}

// enumFilters は enum で候補が固定されている画面フィルタ名を返す。
func enumFilters(r uiroute.Route) map[string]bool {
	out := map[string]bool{}
	for k, f := range r.Filters {
		if len(f.Enum) > 0 {
			out[k] = true
		}
	}
	return out
}

// enumCommandParams は enum で候補が固定されているコマンド引数名を返す。
func enumCommandParams(c command.Command) map[string]bool {
	out := map[string]bool{}
	for k, p := range c.Parameters {
		if len(p.Enum) > 0 {
			out[k] = true
		}
	}
	return out
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
