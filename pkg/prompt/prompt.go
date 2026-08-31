// Package prompt は Orchestrator が LLM へ渡すプロンプトとスキーマを組み立てる。
//
// 重要な制約: llama.cpp の --cache-reuse が効くよう、prompt prefix は
// バイト単位で安定していなければならない。したがってこのパッケージは
//   - map の iteration 順に依存しない (必ずソートするかカタログ順を使う)
//   - 現在時刻・ランダム ID・リクエスト ID を prefix に入れない
//
// を厳守する。
package prompt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/toolschema"
	"github.com/mktkhr/nlops/pkg/uiroute"
)

// ServiceRouterSystem は Stage 1 (Service Router) の system prompt を返す。
// 各サービスの name / description / responsibility のみを渡し、
// API スキーマは一切渡さない。
func ServiceRouterSystem(cat *toolschema.Catalog) string {
	var b strings.Builder
	b.WriteString("あなたは業務システムのサービスルーターです。\n")
	b.WriteString("ユーザーの要求に答えるために参照が必要な業務サービスを選びます。\n\n")
	b.WriteString("# 利用可能なサービス\n\n")
	for _, s := range cat.Services {
		fmt.Fprintf(&b, "## %s (%s)\n%s\n責務: %s\n\n", s.Name, s.Title, s.Description, s.Responsibility)
	}
	b.WriteString("# 指示\n")
	b.WriteString("- 要求を満たすために本当に必要なサービスだけを選びます。\n")
	b.WriteString("- 人名や商品名からIDを解決する必要がある場合は、そのIDを保持しているサービスも選びます。\n")
	b.WriteString("- 迷った場合は、責務の記述に照らして最も直接的に該当するサービスを選びます。\n")
	b.WriteString("- JSON のみを出力します。\n")
	return b.String()
}

// ServiceRouterSchema は Stage 1 の出力スキーマを返す。
func ServiceRouterSchema(cat *toolschema.Catalog) *llm.JSONSchema {
	return &llm.JSONSchema{
		Name:   "service_selection",
		Strict: true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"services": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type": "string",
						"enum": cat.ServiceNames(),
					},
				},
			},
			"required":             []string{"services"},
			"additionalProperties": false,
		},
	}
}

// ToolSelectorSystem は Tool 選定の system prompt を返す。
// Stage 2 では Stage 1 が選んだサービスの Tool だけを、
// 1 段階構成では全 Tool を渡す。
func ToolSelectorSystem(tools []toolschema.Tool) string {
	var b strings.Builder
	b.WriteString("あなたは業務システムの Tool 選定器です。\n")
	b.WriteString("ユーザーの要求とこれまでの実行結果から、次に実行すべき Tool を1つだけ選びます。\n\n")
	b.WriteString("# 利用可能な Tool\n\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "## %s\n%s\n", t.Name, t.Description)
		b.WriteString(renderParams(t.Parameters))
		b.WriteString("\n")
	}
	b.WriteString("# 指示\n")
	b.WriteString("- 1回につき Tool を1つだけ選びます。複数の Tool をまとめて計画しません。\n")
	b.WriteString("- ID を推測してはいけません。ID が分からない場合は、まず検索系の Tool で ID を解決します。\n")
	b.WriteString("- 引数はユーザーの要求から読み取れる値だけを入れます。分からない引数は省略します。\n")
	b.WriteString("- enum が定義された引数は、必ずその候補のいずれかを使います。\n")
	b.WriteString("- JSON のみを出力します。\n")
	return b.String()
}

func renderParams(s toolschema.Schema) string {
	if len(s.Properties) == 0 {
		return "引数: なし\n"
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	// map の iteration 順に依存しないよう必ずソートする。
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("引数:\n")
	for _, n := range names {
		p := s.Properties[n]
		typ := p.Type
		if len(p.Enum) > 0 {
			typ = typ + ": " + strings.Join(p.Enum, " | ")
		}
		mark := "任意"
		if required[n] {
			mark = "必須"
		}
		fmt.Fprintf(&b, "- %s (%s, %s) %s\n", n, typ, mark, p.Description)
	}
	return b.String()
}

// ToolSelectorSchema は Tool 選定の出力スキーマを返す。
//
// strictArgs=true のとき、Tool ごとに引数スキーマを固定した anyOf を生成する。
// これにより「存在しない引数名」「enum 外の値」を生成側で構造的に禁止できる。
// false のときは tool 名のみ enum で拘束し、arguments は自由なオブジェクトにする。
func ToolSelectorSchema(tools []toolschema.Tool, strictArgs bool) *llm.JSONSchema {
	if !strictArgs {
		return &llm.JSONSchema{
			Name:   "tool_call",
			Strict: true,
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tool":      map[string]any{"type": "string", "enum": toolschema.ToolNames(tools)},
					"arguments": map[string]any{"type": "object"},
				},
				"required":             []string{"tool", "arguments"},
				"additionalProperties": false,
			},
		}
	}

	variants := make([]any, 0, len(tools))
	for _, t := range tools {
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool":      map[string]any{"const": t.Name},
				"arguments": argsSchema(t.Parameters),
			},
			"required":             []string{"tool", "arguments"},
			"additionalProperties": false,
		})
	}
	return &llm.JSONSchema{
		Name:   "tool_call",
		Strict: true,
		Schema: map[string]any{"anyOf": variants},
	}
}

func argsSchema(s toolschema.Schema) map[string]any {
	props := map[string]any{}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		p := s.Properties[n]
		m := map[string]any{"type": p.Type}
		if len(p.Enum) > 0 {
			m["enum"] = p.Enum
		}
		props[n] = m
	}
	out := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(s.Required) > 0 {
		req := append([]string(nil), s.Required...)
		sort.Strings(req)
		out["required"] = req
	}
	return out
}

// LoopSystem は Tool Execution Loop の system prompt を返す。
// ToolSelectorSystem との差分は、終了の宣言と画面遷移を選べること。
// routes が nil のときは画面遷移の説明を出さない。
func LoopSystem(tools []toolschema.Tool, routes *uiroute.Catalog) string {
	hasRoutes := routes != nil && len(routes.Routes) > 0

	var b strings.Builder
	b.WriteString("あなたは業務システムの操作エージェントです。\n")
	if hasRoutes {
		b.WriteString("ユーザーの要求に対して、次の2つのどちらかで応えます。\n\n")
		b.WriteString("A. 画面を開く (navigate) — 絞り込んだ一覧を見れば済む要求。これを優先します。\n")
		b.WriteString("B. Tool を実行する — 件数・金額・状態など、特定の値を答える必要がある要求。\n\n")

		b.WriteString("# A: 遷移できる画面\n\n")
		for _, r := range routes.Routes {
			fmt.Fprintf(&b, "## %s (%s)\n%s\n", r.Path, r.Title, r.Description)
			b.WriteString(renderFilters(r))
			b.WriteString("\n")
		}
		b.WriteString("上に挙げた画面がすべてです。商品・在庫・配送・請求・与信の一覧画面はありません。\n")
		b.WriteString("これらに関する要求は、一覧を求めるものであっても navigate せず Tool を実行します。\n\n")
		b.WriteString("# B: 利用可能な Tool\n\n")
	} else {
		b.WriteString("ユーザーの要求に答えるため、Tool を1回に1つずつ実行して情報を集めます。\n\n")
		b.WriteString("# 利用可能な Tool\n\n")
	}

	for _, t := range tools {
		fmt.Fprintf(&b, "## %s\n%s\n", t.Name, t.Description)
		b.WriteString(renderParams(t.Parameters))
		b.WriteString("\n")
	}

	if hasRoutes {
		b.WriteString("# 判断の例\n")
		b.WriteString("- 「東日本の顧客の一覧を見せて」→ navigate。Tool は使いません。\n")
		b.WriteString("- 「未発送の注文を画面で見たい」→ navigate。Tool は使いません。\n")
		b.WriteString("- 「田中さんの注文を画面で見せて」→ navigate。氏名を受け取るフィルタがあるので\n")
		b.WriteString("  Tool で ID を解決する必要はありません。\n")
		b.WriteString("- 「田中さんの未払い残高はいくらですか」→ 特定の値を答えるので Tool を実行します。\n")
		b.WriteString("- 「田中さんの注文は何件ありますか」→ 件数を答えるので Tool を実行します。\n")
		b.WriteString("- 「使える配送業者を教えて」→ 配送業者の画面が無いので Tool を実行します。\n")
		b.WriteString("- 「周辺機器の商品を見せて」→ 商品の画面が無いので Tool を実行します。\n")
		b.WriteString("- 「利用停止の顧客はいますか」→ 顧客画面は取引状態で絞り込めないので Tool を実行します。\n\n")
	}

	b.WriteString("# 指示\n")
	if hasRoutes {
		b.WriteString("- 「一覧」「見せて」「表示して」「画面」を含む要求は、まず navigate で足りるか考えます。\n")
		b.WriteString("  ただし navigate できるのは、要求が画面のフィルタだけで完全に表現できる場合に限ります。\n")
		b.WriteString("  対応する画面が無い、または必要な絞り込みがフィルタに無いなら Tool を実行します。\n")
		b.WriteString("- navigate のフィルタに ID を入れる場合は、先に検索系の Tool で解決した ID だけを使います。\n")
		b.WriteString("  氏名しか分からない状態で customer_id を書いてはいけません。\n")
		b.WriteString("- 画面のフィルタで表現できる条件は、Tool を使わずそのまま navigate に書きます。\n")
		b.WriteString("  一覧の中身を Tool で取りに行ってはいけません。中身は画面が表示します。\n")
		b.WriteString("- navigate は最後の行動です。reason には画面を開く理由を1文で書きます。\n")
	}
	b.WriteString("- 1回につき Tool を1つだけ実行します。複数の Tool をまとめて計画しません。\n")
	b.WriteString("- ID を推測してはいけません。ID が分からない場合は、まず検索系の Tool で ID を解決します。\n")
	b.WriteString("- 直前の Tool 結果に含まれる値だけを次の引数に使います。\n")
	b.WriteString("- 引数はユーザーの要求から読み取れる値だけを入れます。分からない引数は省略します。\n")
	b.WriteString("- enum が定義された引数は、必ずその候補のいずれかを使います。\n")
	b.WriteString("- 情報が足りないうちは必ず Tool を実行します。まだ何も実行していない状態で finish を選んではいけません。\n")
	b.WriteString("- 検索結果が 0 件だったときは、条件を広げた再検索を 1 回だけ試します。それでも 0 件なら finish します。\n")
	b.WriteString("- 同じ Tool を条件だけ変えて 3 回以上呼んではいけません。\n")
	b.WriteString("- ユーザーの要求に直接答えるのに必要のない Tool は呼びません。関連情報を集めて回る必要はありません。\n")
	b.WriteString("- Tool の結果が denied のときは、権限がないという事実をそのまま受け入れて終了します。同じ Tool を再試行しません。\n")
	b.WriteString("- ユーザーの要求に答えるだけの情報が集まったら next=\"finish\" を返します。\n")
	b.WriteString("- JSON のみを出力します。\n")
	return b.String()
}

func renderFilters(r uiroute.Route) string {
	if len(r.Filters) == 0 {
		return "フィルタ: なし\n"
	}
	var b strings.Builder
	b.WriteString("フィルタ:\n")
	for _, n := range r.FilterNames() {
		f := r.Filters[n]
		typ := f.Type
		if len(f.Enum) > 0 {
			typ = typ + ": " + strings.Join(f.Enum, " | ")
		}
		fmt.Fprintf(&b, "- %s (%s) %s\n", n, typ, f.Description)
	}
	return b.String()
}

// LoopSchema は Tool Loop 1 反復の出力スキーマを返す。
// 「次の Tool を実行する」か「終了する」かの判断を構造的に拘束する。
//
// allowFinish=false のとき finish の選択肢自体をスキーマから外す。
// Tool を 1 つも実行していないうちに finish を選ぶ失敗を実測したため、
// プロンプトでの依頼ではなく文法で禁止する。
func LoopSchema(tools []toolschema.Tool, routes *uiroute.Catalog, strictArgs, allowFinish bool) *llm.JSONSchema {
	var variants []any
	if !strictArgs {
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"next":      map[string]any{"const": "tool"},
				"tool":      map[string]any{"type": "string", "enum": toolschema.ToolNames(tools)},
				"arguments": map[string]any{"type": "object"},
			},
			"required":             []string{"next", "tool", "arguments"},
			"additionalProperties": false,
		})
	} else {
		for _, t := range tools {
			variants = append(variants, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"next":      map[string]any{"const": "tool"},
					"tool":      map[string]any{"const": t.Name},
					"arguments": argsSchema(t.Parameters),
				},
				"required":             []string{"next", "tool", "arguments"},
				"additionalProperties": false,
			})
		}
	}
	// 画面遷移。定義された画面とフィルタしか書けないよう固定する。
	if routes != nil {
		for _, r := range routes.Routes {
			props := map[string]any{}
			for _, n := range r.FilterNames() {
				f := r.Filters[n]
				m := map[string]any{"type": "string"}
				if len(f.Enum) > 0 {
					m["enum"] = f.Enum
				}
				props[n] = m
			}
			variants = append(variants, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"next":  map[string]any{"const": "navigate"},
					"route": map[string]any{"const": r.Path},
					"filters": map[string]any{
						"type": "object", "properties": props, "additionalProperties": false,
					},
					"reason": map[string]any{"type": "string"},
				},
				"required":             []string{"next", "route", "filters", "reason"},
				"additionalProperties": false,
			})
		}
	}

	// finish は最後に置く。先頭に置くと最も単純な分岐へ流れやすい。
	if allowFinish {
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"next":   map[string]any{"const": "finish"},
				"reason": map[string]any{"type": "string"},
			},
			"required":             []string{"next"},
			"additionalProperties": false,
		})
	}
	return &llm.JSONSchema{Name: "loop_step", Strict: true, Schema: map[string]any{"anyOf": variants}}
}

// FinishOnlySchema は finish しか選べないスキーマを返す。
// 空振りが続いたときに、Tool を呼び続ける選択肢そのものを外すために使う。
func FinishOnlySchema() *llm.JSONSchema {
	return &llm.JSONSchema{Name: "loop_step", Strict: true, Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"next":   map[string]any{"const": "finish"},
			"reason": map[string]any{"type": "string"},
		},
		"required":             []string{"next"},
		"additionalProperties": false,
	}}
}

// IntentSystem はモード判定の system prompt を返す。
//
// Tool 定義を一切見せず、画面だけを見せて「画面を開くだけで満たせるか」を
// 判定させる。27 個の分岐から選ばせるより、2 択の方が安定するという想定。
func IntentSystem(routes *uiroute.Catalog) string {
	var b strings.Builder
	b.WriteString("ユーザーの要求が、次の画面を開いて絞り込むだけで満たせるかを判定します。\n\n")
	b.WriteString("# 画面\n\n")
	for _, r := range routes.Routes {
		fmt.Fprintf(&b, "## %s (%s)\n%s\n", r.Path, r.Title, r.Description)
		b.WriteString(renderFilters(r))
		b.WriteString("\n")
	}
	b.WriteString("# 判定\n")
	b.WriteString("- \"n\" (navigate): 上の画面のどれかと、そのフィルタだけで要求を完全に表現できる。\n")
	b.WriteString("  氏名での絞り込みはフィルタで直接できるので、IDへの変換は不要です。\n")
	b.WriteString("- \"t\" (tool): それ以外。対応する画面が無い、必要な絞り込みがフィルタに無い、\n")
	b.WriteString("  または件数・金額・状態など特定の値を答える必要がある。\n\n")
	b.WriteString("# 例\n")
	b.WriteString("- 「西日本の顧客の一覧を開いて」→ n\n")
	b.WriteString("- 「田中さんの注文を画面で見せて」→ n (customer_name で絞れる)\n")
	b.WriteString("- 「高橋みどりさんの注文一覧を開いて」→ n\n")
	b.WriteString("- 「担当している顧客の一覧を開いて」→ n (フィルタなしで開く)\n")
	b.WriteString("- 「利用停止の顧客はいますか」→ t (取引状態のフィルタが無い)\n")
	b.WriteString("- 「使える配送業者を教えて」→ t (配送業者の画面が無い)\n")
	b.WriteString("- 「田中さんの未払い残高はいくら」→ t (特定の値を答える)\n\n")
	b.WriteString("{\"m\":\"n\"} または {\"m\":\"t\"} だけを出力します。\n")
	return b.String()
}

// IntentSchema はモード判定の出力スキーマを返す。
//
// キー名と値を最短にしてある。制約デコードでは 1 トークンあたりのコストが
// 非制約時の約 2.5 倍になり、この判定は 1 要求ごとに必ず走るため、
// 出力トークン数がそのままレイテンシに乗る。
func IntentSchema() *llm.JSONSchema {
	return &llm.JSONSchema{Name: "intent", Strict: true, Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"m": map[string]any{"type": "string", "enum": []string{"n", "t"}},
		},
		"required":             []string{"m"},
		"additionalProperties": false,
	}}
}

// NavigateOnlySystem は画面遷移だけを行うときの system prompt を返す。
// Tool 定義を見せないので、要求を画面のフィルタへ写すことに集中できる。
func NavigateOnlySystem(routes *uiroute.Catalog) string {
	var b strings.Builder
	b.WriteString("ユーザーの要求を、業務画面の絞り込み状態へ変換します。\n\n")
	b.WriteString("# 画面\n\n")
	for _, r := range routes.Routes {
		fmt.Fprintf(&b, "## %s (%s)\n%s\n", r.Path, r.Title, r.Description)
		b.WriteString(renderFilters(r))
		b.WriteString("\n")
	}
	b.WriteString("# 指示\n")
	b.WriteString("- 要求に最も合う画面を1つ選びます。\n")
	b.WriteString("- 要求から読み取れる条件だけをフィルタに入れます。読み取れないものは省略します。\n")
	b.WriteString("- 氏名はそのままフィルタに入れます。IDへ変換する必要はありません。\n")
	b.WriteString("- enum が定義されたフィルタは、必ずその候補のいずれかを使います。\n")
	b.WriteString("- reason には画面を開く理由を1文で書きます。\n")
	b.WriteString("- JSON のみを出力します。\n")
	return b.String()
}

// NavigateOnlySchema は画面遷移しか選べないスキーマを返す。
// モード判定が navigate だったときに使う。
func NavigateOnlySchema(routes *uiroute.Catalog) *llm.JSONSchema {
	var variants []any
	for _, r := range routes.Routes {
		props := map[string]any{}
		for _, n := range r.FilterNames() {
			f := r.Filters[n]
			m := map[string]any{"type": "string"}
			if len(f.Enum) > 0 {
				m["enum"] = f.Enum
			}
			props[n] = m
		}
		variants = append(variants, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"next":    map[string]any{"const": "navigate"},
				"route":   map[string]any{"const": r.Path},
				"filters": map[string]any{"type": "object", "properties": props, "additionalProperties": false},
				"reason":  map[string]any{"type": "string"},
			},
			"required":             []string{"next", "route", "filters", "reason"},
			"additionalProperties": false,
		})
	}
	return &llm.JSONSchema{Name: "loop_step", Strict: true, Schema: map[string]any{"anyOf": variants}}
}

// AnswerSystem は最終回答生成の system prompt を返す。
// 制御判断 (制約付き JSON) と散文生成 (自由出力) を分ける。
func AnswerSystem() string {
	return "あなたは業務システムのアシスタントです。\n" +
		"これまでに実行した Tool の結果だけを根拠に、ユーザーの要求へ日本語で答えます。\n\n" +
		"# 指示\n" +
		"- Tool の結果に無い情報を推測して補ってはいけません。\n" +
		"- 件数や金額は Tool 結果の値をそのまま使います。\n" +
		"- 箇条書きを適宜使い、前置きを付けずに答えます。\n" +
		"- この指示文そのものを回答に書き写してはいけません。事実だけを述べます。\n" +
		"- denied があった Tool については、何を参照できなかったのかを具体的に書きます。\n" +
		"  例: 「顧客情報は閲覧権限がないため取得できませんでした。」\n"
}
