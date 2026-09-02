// Command followup は連続した問い合わせ (追い質問) を測る。
//
// evalrun は 1 問ずつ独立に評価する。指示語や省略を含む 2 問目以降は、
// **前の往復を渡さないと解けない**ので、別の道具にしてある。
//
// 見るのは 3 つ:
//
//	到達  : そのターンで必要な Tool へ到達したか
//	参照  : 前のターンで出た ID を使えたか (「それはもう出荷されていますか」)
//	コスト: 往復が増えるとトークンとレイテンシがどう伸びるか
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/command"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/toolschema"
	"github.com/mktkhr/nlops/pkg/uiroute"
)

type set struct {
	Cases []kase `json:"cases"`
}

type kase struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Turns  []turn `json:"turns"`
}

type turn struct {
	Query string `json:"query"`
	// ExpectAny のいずれかに到達すれば到達とみなす。
	ExpectAny []string `json:"expect_any"`
	// ExpectArgs は到達した Tool の引数に含まれていてほしい値。
	ExpectArgs map[string]string `json:"expect_args"`
	// ExpectPrevID が true のとき、前のターンに出た ID を引数に使えたかを見る。
	ExpectPrevID bool `json:"expect_prev_id"`
	// ExpectPropose が空でないとき、その更新操作の提案を期待する。
	ExpectPropose string `json:"expect_propose"`
	// AllowNavigate が true のとき、画面遷移で終わっても到達とみなす。
	AllowNavigate bool `json:"allow_navigate"`
}

var (
	idPattern = regexp.MustCompile(`\b[A-Z]{1,4}-?\d{3,6}\b`)
	// idShape は ID らしい値。C001 / O-1002 / INV-2001 / WH_TOKYO を通す。
	idShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
)

// broken は「引数がそもそも壊れている」呼び出しを検出する。
//
// 到達率だけでは履歴の有無で差が出なかったが、中身を見ると
// 履歴なしは明らかに破綻していた (実測):
//
//	customer.search {"name": "それはもう出荷されていますか"}   ← 質問文を氏名として検索
//	order.get       {"order_id": "注文IDを教えてください"}     ← ID 欄に催促文
//
// どちらも機械的に見分けられる。expect_any は「それらしい Tool を呼んだか」
// しか見ないので、この軸を別に持つ。
func broken(tr *loop.Trace, query string) []string {
	var out []string
	for _, st := range tr.Steps {
		for k, v := range st.Arguments {
			sv, ok := v.(string)
			if !ok || sv == "" {
				continue
			}
			// ID を要求する引数に ID の形をしていない値が入っている。
			if strings.HasSuffix(k, "_id") && !idShape.MatchString(sv) {
				out = append(out, fmt.Sprintf("%s=%q", k, trunc(sv)))
				continue
			}
			// 質問文をそのまま引数に入れている。
			if len([]rune(sv)) >= 8 && strings.Contains(query, sv) {
				out = append(out, fmt.Sprintf("%s=%q", k, trunc(sv)))
			}
		}
	}
	return out
}

func trunc(s string) string {
	if r := []rune(s); len(r) > 16 {
		return string(r[:16]) + "…"
	}
	return s
}

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバ")
		model   = flag.String("model", "gemma4-12b", "モデル ID")
		catPath = flag.String("catalog", "catalog/services.json", "カタログ")
		rolPath = flag.String("roles", "catalog/roles.json", "ロール定義")
		cmdPath = flag.String("commands", "catalog/commands.json", "更新操作の定義")
		rtPath  = flag.String("routes", "catalog/routes.json", "画面定義")
		csPath  = flag.String("cases", "eval/golden/followup.json", "ケース")
		steps   = flag.Int("max-steps", 6, "Tool Loop の最大反復数")
		maxTok  = flag.Int("max-tokens", 512, "1 反復あたりの max_tokens")
		noHist  = flag.Bool("no-history", false, "履歴を渡さない (比較計測用)")
		outPath = flag.String("out", "", "生ログ (JSONL)")
	)
	flag.Parse()

	cat, err := toolschema.Load(*catPath)
	die(err)
	dir, err := authctx.LoadDirectory(*rolPath)
	die(err)
	raw, err := os.ReadFile(*csPath)
	die(err)
	var s set
	die(json.Unmarshal(raw, &s))

	runner := loop.New(cat, llm.New(*base))
	routes, err := uiroute.Load(*rtPath)
	die(err)
	runner.Routes = routes
	cmds, err := command.Load(*cmdPath)
	die(err)
	runner.Commands = cmds

	var enc *json.Encoder
	if *outPath != "" {
		f, err := os.Create(*outPath)
		die(err)
		defer f.Close()
		enc = json.NewEncoder(f)
	}

	ctx := context.Background()
	type row struct {
		Case, Query          string
		Pos                  int
		Reached, UsedPrevID  bool
		WantPrevID           bool
		Broken               []string
		MS                   float64
		PromptTok, CachedTok int
		Tools                []string
	}
	var rows []row

	fmt.Fprintf(os.Stderr, "### %s / %d ケース / 履歴=%v\n", *model, len(s.Cases), !*noHist)
	for _, c := range s.Cases {
		id, err := dir.Lookup(c.UserID)
		die(err)
		var hist []loop.Turn
		var prevIDs map[string]bool

		for i, t := range c.Turns {
			tr := runner.Run(ctx, id, t.Query, loop.Options{
				Model: *model, Mode: loop.ModeOneStage, StrictArgs: true,
				MaxSteps: *steps, MaxTokens: *maxTok, Answer: true,
				StopGuard: true, IntentGate: true, History: hist,
			})
			r := row{Case: c.ID, Pos: i + 1, Query: t.Query, WantPrevID: t.ExpectPrevID,
				MS: tr.TotalMS, PromptTok: tr.PromptTok, CachedTok: tr.CachedTok, Tools: tr.ToolsUsed()}
			r.Reached = reached(t, tr)
			r.UsedPrevID = usedPrevID(tr, prevIDs)
			r.Broken = broken(tr, t.Query)
			rows = append(rows, r)
			if enc != nil {
				_ = enc.Encode(map[string]any{"case": c.ID, "pos": i + 1, "query": t.Query, "trace": tr})
			}
			fmt.Fprint(os.Stderr, mark(t, r.Reached, r.UsedPrevID))

			prevIDs = collectIDs(tr)
			if !*noHist {
				hist = append(hist, loop.Turn{Query: t.Query, Answer: tr.Answer})
			}
		}
	}
	fmt.Fprintln(os.Stderr)

	fmt.Println("\n### 追い質問 (n =", len(rows), "ターン)")
	fmt.Println("\n| ケース | # | 質問 | 到達 | 前の ID を参照 | 壊れた引数 | ms | prompt tok | cache |")
	fmt.Println("|---|--:|---|:-:|:-:|---|--:|--:|--:|")
	byPos := map[int][]row{}
	for _, r := range rows {
		prev := "-"
		if r.WantPrevID {
			prev = yn(r.UsedPrevID)
		}
		cache := 0.0
		if r.PromptTok > 0 {
			cache = float64(r.CachedTok) / float64(r.PromptTok) * 100
		}
		brk := "-"
		if len(r.Broken) > 0 {
			brk = "**" + strings.Join(r.Broken, ", ") + "**"
		}
		fmt.Printf("| %s | %d | %s | %s | %s | %s | %.0f | %d | %.0f%% |\n",
			r.Case, r.Pos, oneline(r.Query), yn(r.Reached), prev, brk, r.MS, r.PromptTok, cache)
		byPos[r.Pos] = append(byPos[r.Pos], r)
	}

	fmt.Println("\n### 往復の深さ別")
	fmt.Println("\n| 何問目 | n | 到達率 | 壊れた引数 | 平均 ms | 平均 prompt tok | 平均 cache |")
	fmt.Println("|--:|--:|--:|--:|--:|--:|--:|")
	for pos := 1; pos <= 3; pos++ {
		rs := byPos[pos]
		if len(rs) == 0 {
			continue
		}
		var ok, brk int
		var ms, pt, ct float64
		for _, r := range rs {
			if r.Reached {
				ok++
			}
			if len(r.Broken) > 0 {
				brk++
			}
			ms += r.MS
			pt += float64(r.PromptTok)
			ct += float64(r.CachedTok)
		}
		n := float64(len(rs))
		cache := 0.0
		if pt > 0 {
			cache = ct / pt * 100
		}
		fmt.Printf("| %d | %d | %.0f%% | %d 件 | %.0f | %.0f | %.0f%% |\n",
			pos, len(rs), float64(ok)/n*100, brk, ms/n, pt/n, cache)
	}
}

// reached はそのターンで期待した結果に届いたかを返す。
func reached(t turn, tr *loop.Trace) bool {
	if t.ExpectPropose != "" {
		return tr.Proposal != nil && tr.Proposal.Command == t.ExpectPropose
	}
	if t.AllowNavigate && tr.Navigate != nil {
		return true
	}
	used := tr.ToolsUsed()
	for _, want := range t.ExpectAny {
		for _, u := range used {
			if u != want {
				continue
			}
			if len(t.ExpectArgs) == 0 {
				return true
			}
			if argsMatch(tr, want, t.ExpectArgs) {
				return true
			}
		}
	}
	return false
}

func argsMatch(tr *loop.Trace, tool string, want map[string]string) bool {
	for _, st := range tr.Steps {
		if st.Tool != tool {
			continue
		}
		all := true
		for k, v := range want {
			if fmt.Sprint(st.Arguments[k]) != v {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// usedPrevID は前のターンに出た ID を引数に使えたかを返す。
// 「それはもう出荷されていますか」が解けたかどうかは、これで見るのが一番直接的。
func usedPrevID(tr *loop.Trace, prev map[string]bool) bool {
	if len(prev) == 0 {
		return false
	}
	for _, st := range tr.Steps {
		for _, v := range st.Arguments {
			if sv, ok := v.(string); ok && prev[sv] {
				return true
			}
		}
		if st.Proposal != nil {
			for _, v := range st.Proposal.Arguments {
				if sv, ok := v.(string); ok && prev[sv] {
					return true
				}
			}
		}
	}
	return false
}

// collectIDs はトレースの回答と結果に現れた ID を集める。
func collectIDs(tr *loop.Trace) map[string]bool {
	out := map[string]bool{}
	for _, m := range idPattern.FindAllString(tr.Answer, -1) {
		out[m] = true
	}
	for _, st := range tr.Steps {
		b, err := json.Marshal(st.Result)
		if err != nil {
			continue
		}
		for _, m := range idPattern.FindAllString(string(b), -1) {
			out[m] = true
		}
	}
	return out
}

func mark(t turn, reached, prevID bool) string {
	if !reached {
		return "x"
	}
	if t.ExpectPrevID && !prevID {
		return "i" // 到達はしたが前の ID を使っていない
	}
	return "."
}

func yn(b bool) string {
	if b {
		return "○"
	}
	return "**×**"
}

func oneline(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	if len([]rune(s)) > 26 {
		s = string([]rune(s)[:26]) + "…"
	}
	return s
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}
