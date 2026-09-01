// Command recovery は「間違った Tool を踏んでしまった後に戻れるか」を測る。
//
// スケール検証 (scale-report.md) が測ったのは「ダミー Tool を選ばないこと」だった。
// 選定精度が高いので誤選択は自然発生せず、**選んでしまった後の挙動を一度も
// 観測していない。** 実運用では必ず起きる。
//
// そこで Loop の step 1 に誤った Tool を強制的に踏ませ (loop.Options.ForceFirst)、
// そこから通常どおり回して次の 3 つを見る:
//
//	回復 : 期待する実 Tool に到達したか
//	汚染 : ダミーが返した値が最終回答に混ざったか (これが最悪)
//	浪費 : 何ステップ使ったか
//
// ダミーの壊れ方は decoystub の mode で作り分ける (down / error / plausible)。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/mktkhr/nlops/orchestrator/executor"
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
	Query  string `json:"query"`
	First  struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	} `json:"first"`
	ExpectTools []string `json:"expect_tools"`
	Note        string   `json:"note"`
}

type outcome struct {
	CaseID       string      `json:"case_id"`
	Mode         string      `json:"mode"`
	Query        string      `json:"query"`
	Forced       string      `json:"forced"`
	Recovered    bool        `json:"recovered"`
	Navigated    bool        `json:"navigated"`
	Contaminated bool        `json:"contaminated"`
	Leaked       []string    `json:"leaked,omitempty"`
	Steps        int         `json:"steps"`
	Incomplete   bool        `json:"incomplete"`
	Answer       string      `json:"answer"`
	Trace        *loop.Trace `json:"trace"`
}

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバ")
		model   = flag.String("model", "gemma4-12b", "モデル ID")
		catPath = flag.String("catalog", "catalog/scale/services-124.json", "ダミーを含むカタログ")
		rolPath = flag.String("roles", "catalog/roles.json", "ロール定義")
		cmdPath = flag.String("commands", "catalog/commands.json", "更新操作の定義")
		rtPath  = flag.String("routes", "catalog/routes.json", "画面定義")
		csPath  = flag.String("cases", "eval/golden/recovery.json", "ケース")
		stub    = flag.String("stub", "http://127.0.0.1:9199", "decoystub の URL。汚染判定に使う")
		mode    = flag.String("mode", "plausible", "記録用のラベル (down / error / plausible)")
		steps   = flag.Int("max-steps", 6, "Tool Loop の最大反復数")
		maxTok  = flag.Int("max-tokens", 512, "1 反復あたりの max_tokens")
		gate    = flag.Bool("intent-gate", true, "Intent Gate を使う")
		outPath = flag.String("out", "", "生ログ (JSONL) の出力先")
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

	lc := llm.New(*base)
	runner := loop.New(cat, lc)
	if *rtPath != "" {
		routes, err := uiroute.Load(*rtPath)
		die(err)
		runner.Routes = routes
	}
	if *cmdPath != "" {
		cmds, err := command.Load(*cmdPath)
		die(err)
		runner.Commands = cmds
	}
	ctx := context.Background()

	var enc *json.Encoder
	if *outPath != "" {
		f, err := os.Create(*outPath)
		die(err)
		defer f.Close()
		enc = json.NewEncoder(f)
	}

	fmt.Fprintf(os.Stderr, "### %s / mode=%s / %d 件\n", *model, *mode, len(s.Cases))
	var all []outcome
	for _, c := range s.Cases {
		id, err := dir.Lookup(c.UserID)
		die(err)
		resetStub(*stub)

		tr := runner.Run(ctx, id, c.Query, loop.Options{
			Model: *model, Mode: loop.ModeOneStage, StrictArgs: true,
			MaxSteps: *steps, MaxTokens: *maxTok,
			// 回復したかどうかは最終回答まで見ないと分からない。
			Answer: true, StopGuard: true, IntentGate: *gate,
			ForceFirst: &executor.Call{Tool: c.First.Tool, Arguments: c.First.Arguments},
		})

		o := grade(c, tr, *mode, servedValues(*stub))
		all = append(all, o)
		if enc != nil {
			_ = enc.Encode(o)
		}
		fmt.Fprint(os.Stderr, mark(o))
	}
	fmt.Fprintln(os.Stderr)
	report(all, *mode)
}

func grade(c kase, tr *loop.Trace, mode string, served []string) outcome {
	o := outcome{CaseID: c.ID, Mode: mode, Query: c.Query, Forced: c.First.Tool,
		Steps: len(tr.Steps), Incomplete: tr.Incomplete, Answer: tr.Answer, Trace: tr}

	// 回復: 強制した 1 手の後に、期待する実 Tool を実行したか。
	for _, st := range tr.Steps[min(1, len(tr.Steps)):] {
		for _, want := range c.ExpectTools {
			if st.Tool == want && st.Result != nil && st.Result.Error == "" {
				o.Recovered = true
			}
		}
	}
	// 画面遷移で終わった場合。Tool には到達していないが、
	// 利用者は正しい画面を得ている。未回復とは別に数える。
	o.Navigated = tr.Navigate != nil

	// 汚染: ダミーが返した値が最終回答に出ているか。
	// 回復していても、ダミーの数字を混ぜて答えたら失敗とみなす。
	//
	// 数値も見る。最初の実測では ID 文字列しか記録しておらず、
	// 「単価は 90100 です」(捏造値) を汚染なしと判定してしまった。
	// 桁区切りを外してから照合する。最初の実測では
	// 「合計金額は 90,100 です」(ダミーの 90100) を取り逃した。
	answer := normalize(tr.Answer)
	for _, v := range served {
		if v != "" && containsToken(answer, normalize(v)) {
			o.Contaminated = true
			o.Leaked = append(o.Leaked, v)
		}
	}
	// スタブが返した値の記録に漏れがあっても、この 2 つは必ずダミー由来。
	for _, marker := range []string{"DECOY-", "ダミー"} {
		if strings.Contains(tr.Answer, marker) {
			o.Contaminated = true
			o.Leaked = append(o.Leaked, marker)
		}
	}
	sort.Strings(o.Leaked)
	return o
}

// normalize は桁区切りを落とす。90,100 と 90100 を同じものとして扱う。
func normalize(s string) string {
	return strings.NewReplacer(",", "", "，", "").Replace(s)
}

// containsToken は数字の途中に埋もれた一致を弾く。
// 9001 が 190015 の一部として出てきても汚染ではない。
func containsToken(text, tok string) bool {
	for i := 0; i+len(tok) <= len(text); i++ {
		if text[i:i+len(tok)] != tok {
			continue
		}
		if i > 0 && isDigit(text[i-1]) {
			continue
		}
		if j := i + len(tok); j < len(text) && isDigit(text[j]) {
			continue
		}
		return true
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func mark(o outcome) string {
	switch {
	case o.Contaminated:
		return "X" // ダミーの値で答えた
	case o.Recovered:
		return "."
	case o.Navigated:
		return "n" // 画面遷移へ逃げた
	case o.Incomplete:
		return "!" // 戻れないまま max_steps
	default:
		return "-" // 戻らずに終了 (答えられないと言った、など)
	}
}

func report(all []outcome, mode string) {
	var rec, nav, cont, inc, steps int
	for _, o := range all {
		if o.Recovered {
			rec++
		}
		if o.Navigated && !o.Recovered {
			nav++
		}
		if o.Contaminated {
			cont++
		}
		if o.Incomplete {
			inc++
		}
		steps += o.Steps
	}
	n := len(all)
	fmt.Printf("\n### 誤 Tool からの回復 (mode=%s, n=%d)\n\n", mode, n)
	fmt.Println("| mode | n | 正 Tool へ回復 | 画面遷移へ回避 | 汚染 | max_steps 到達 | 平均step |")
	fmt.Println("|---|--:|--:|--:|--:|--:|--:|")
	fmt.Printf("| %s | %d | %d (%.0f%%) | %d | **%d (%.0f%%)** | %d | %.1f |\n",
		mode, n, rec, pct(rec, n), nav, cont, pct(cont, n), inc, float64(steps)/float64(n))

	fmt.Println("\n| ケース | 強制した誤 Tool | 回復 | 汚染なし | step | 回答 |")
	fmt.Println("|---|---|:-:|:-:|--:|---|")
	for _, o := range all {
		r := yn(o.Recovered)
		if !o.Recovered && o.Navigated {
			r = "遷移"
		}
		fmt.Printf("| %s | %s | %s | %s | %d | %s |\n",
			o.CaseID, o.Forced, r, yn(!o.Contaminated), o.Steps, oneline(o.Answer))
	}
	for _, o := range all {
		if o.Contaminated {
			fmt.Printf("\n- **%s 汚染**: ダミーの値 %v が回答に出た\n  > %s\n",
				o.CaseID, o.Leaked, oneline(o.Answer))
		}
	}
}

func servedValues(stubURL string) []string {
	resp, err := http.Get(stubURL + "/__served")
	if err != nil {
		return nil // down モードではスタブが居ないので汚染は起こりえない
	}
	defer resp.Body.Close()
	var out struct {
		Values []string `json:"values"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return nil
	}
	return out.Values
}

func resetStub(stubURL string) {
	resp, err := http.Get(stubURL + "/__reset")
	if err == nil {
		resp.Body.Close()
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

func yn(b bool) string {
	if b {
		return "○"
	}
	return "**×**"
}

func oneline(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "|", "/")
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return s
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}
