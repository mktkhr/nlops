// Command evalrun はゴールデンセットを Tool Loop 全体に対して実行し採点する。
//
// spike が「初手だけ」を測るのに対し、evalrun は
//   - 必要な Tool 列に到達したか
//   - 踏んではいけない Tool を踏まなかったか
//   - 権限差が結果に正しく反映されたか
//
// までを見る。モックサービスが起動している必要がある。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mktkhr/nlops/eval/internal/golden"
	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

type outcome struct {
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	CaseID   string `json:"case_id"`
	Category string `json:"category"`
	Query    string `json:"query"`
	UserID   string `json:"user_id"`

	SvcRecall  float64 `json:"svc_recall"`
	RequiredOK bool    `json:"required_ok"`
	ForbidOK   bool    `json:"forbid_ok"`
	PermOK     bool    `json:"perm_ok"`
	Pass       bool    `json:"pass"`

	MissingTools []string `json:"missing_tools,omitempty"`
	HitForbidden []string `json:"hit_forbidden,omitempty"`
	PermNote     string   `json:"perm_note,omitempty"`

	Trace *loop.Trace `json:"trace"`
}

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバ")
		models  = flag.String("models", "gemma4-12b", "カンマ区切りのモデル ID")
		modes   = flag.String("modes", "one_stage", "one_stage / two_stage")
		catPath = flag.String("catalog", "catalog/services.json", "カタログ")
		rolPath = flag.String("roles", "catalog/roles.json", "ロール定義")
		csPath  = flag.String("cases", "eval/golden/cases.json", "ゴールデンセット")
		outDir  = flag.String("out", "docs/spike-raw", "生ログ出力先")
		filter  = flag.String("category", "", "カテゴリで絞る")
		only    = flag.String("id", "", "ケース ID で絞る")
		steps   = flag.Int("max-steps", 6, "Tool Loop の最大反復数")
		noGuard = flag.Bool("no-guard", false, "未解決 ID の差し戻しを無効化する")
		reason  = flag.String("reasoning", "none", "reasoning_effort (gpt-oss 系は low)")
		maxTok  = flag.Int("max-tokens", 512, "1 反復あたりの max_tokens")
		noStop  = flag.Bool("no-stop-guard", false, "空振り連続時の finish 強制を無効化する (比較計測用)")
		noProj  = flag.Bool("no-projection", false, "Response Projection を無効化する (比較計測用)")
		answer  = flag.Bool("answer", false, "最終回答の生成まで行う (遅くなる)")
	)
	flag.Parse()

	cat, err := toolschema.Load(*catPath)
	if err != nil {
		die(err)
	}
	dir, err := authctx.LoadDirectory(*rolPath)
	if err != nil {
		die(err)
	}
	set, err := golden.Load(*csPath)
	if err != nil {
		die(err)
	}

	var cases []golden.Case
	for _, c := range set.Cases {
		if *filter != "" && !contains(split(*filter), c.Category) {
			continue
		}
		if *only != "" && !contains(split(*only), c.ID) {
			continue
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		die(fmt.Errorf("実行対象のケースがありません"))
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die(err)
	}
	rawPath := filepath.Join(*outDir, "eval-"+time.Now().Format("20060102-150405")+".jsonl")
	raw, err := os.Create(rawPath)
	if err != nil {
		die(err)
	}
	defer raw.Close()
	enc := json.NewEncoder(raw)

	lc := llm.New(*base)
	lc.ReasoningEffort = *reason
	runner := loop.New(cat, lc)
	runner.Executor.GuardUnresolvedIDs = !*noGuard
	runner.Executor.DisableProjection = *noProj
	ctx := context.Background()

	var all []outcome
	for _, model := range split(*models) {
		for _, mode := range split(*modes) {
			fmt.Fprintf(os.Stderr, "\n### %s / %s (%d 件)\n", model, mode, len(cases))
			for i, c := range cases {
				id, err := dir.Lookup(c.UserID)
				if err != nil {
					die(err)
				}
				tr := runner.Run(ctx, id, c.Query, loop.Options{
					Model: model, Mode: loop.Mode(mode), StrictArgs: true,
					MaxSteps: *steps, MaxTokens: *maxTok, Answer: *answer, StopGuard: !*noStop,
				})
				o := grade(c, tr, model, mode)
				all = append(all, o)
				_ = enc.Encode(o)
				fmt.Fprint(os.Stderr, mark(o))
				if (i+1)%40 == 0 {
					fmt.Fprintln(os.Stderr)
				}
			}
			fmt.Fprintln(os.Stderr)
		}
	}

	fmt.Printf("\n生ログ: %s\n", rawPath)
	summarize(all)
	failures(all)
}

func grade(c golden.Case, tr *loop.Trace, model, mode string) outcome {
	o := outcome{Model: model, Mode: mode, CaseID: c.ID, Category: c.Category,
		Query: c.Query, UserID: c.UserID, Trace: tr}

	used := tr.ToolsUsed()
	usedSet := map[string]bool{}
	for _, t := range used {
		usedSet[t] = true
	}

	o.SvcRecall, _ = golden.ServiceScore(c.ExpectedServices, servicesOf(used))

	o.RequiredOK = true
	for _, t := range c.RequiredTools {
		if !usedSet[t] {
			o.RequiredOK = false
			o.MissingTools = append(o.MissingTools, t)
		}
	}
	o.ForbidOK = true
	for _, t := range c.ForbiddenTools {
		if usedSet[t] {
			o.ForbidOK = false
			o.HitForbidden = append(o.HitForbidden, t)
		}
	}
	o.PermOK, o.PermNote = gradePermission(c, tr)
	o.Pass = o.RequiredOK && o.ForbidOK && o.PermOK && tr.Err == ""
	return o
}

// gradePermission は権限差が結果へ正しく反映されたかを見る。
func gradePermission(c golden.Case, tr *loop.Trace) (bool, string) {
	if c.Permission == nil {
		return true, ""
	}
	p := c.Permission
	if p.ExpectDenied {
		if tr.Denied {
			return true, "denied を観測"
		}
		return false, "403 を期待したが観測されなかった"
	}
	seen := collectStrings(tr)
	for _, want := range p.MustIncludeIDs {
		if !seen[want] {
			return false, fmt.Sprintf("%s が結果に含まれていない", want)
		}
	}
	for _, ng := range p.MustExcludeIDs {
		if seen[ng] {
			return false, fmt.Sprintf("%s が漏れている (権限違反)", ng)
		}
	}
	if tr.Denied {
		return false, "予期しない 403"
	}
	return true, ""
}

// collectStrings はトレース内の Projection 済み結果に現れた文字列値を集める。
func collectStrings(tr *loop.Trace) map[string]bool {
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out[x] = true
		case map[string]any:
			for _, val := range x {
				walk(val)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	for _, s := range tr.Steps {
		if s.Result != nil {
			walk(s.Result.Projected)
		}
	}
	return out
}

func servicesOf(tools []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tools {
		if i := strings.Index(t, "."); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	return out
}

type stat struct {
	n, pass, req, forbid, perm, errs int
	svcRecall                        float64
	totalMS, promptTok, cachedTok    float64
	rawB, projB, stepsN              float64
}

func summarize(os_ []outcome) {
	byKey := map[string]*stat{}
	byCat := map[string]*stat{}
	var order, cats []string
	for _, o := range os_ {
		k := o.Model + " | " + o.Mode
		s, ok := byKey[k]
		if !ok {
			s = &stat{}
			byKey[k] = s
			order = append(order, k)
		}
		cs, ok := byCat[o.Category]
		if !ok {
			cs = &stat{}
			byCat[o.Category] = cs
			cats = append(cats, o.Category)
		}
		for _, t := range []*stat{s, cs} {
			t.n++
			if o.Pass {
				t.pass++
			}
			if o.RequiredOK {
				t.req++
			}
			if o.ForbidOK {
				t.forbid++
			}
			if o.PermOK {
				t.perm++
			}
			if o.Trace.Err != "" {
				t.errs++
			}
			t.svcRecall += o.SvcRecall
			t.totalMS += o.Trace.TotalMS
			t.promptTok += float64(o.Trace.PromptTok)
			t.cachedTok += float64(o.Trace.CachedTok)
			t.rawB += float64(o.Trace.RawBytes)
			t.projB += float64(o.Trace.ProjBytes)
			t.stepsN += float64(len(o.Trace.Steps))
		}
	}
	sort.Strings(order)
	sort.Strings(cats)

	fmt.Println("\n### 構成別")
	fmt.Println("| 構成 | n | 総合 | 必須Tool到達 | 禁止Tool回避 | 権限 | svc recall | 平均step | 平均ms | prompt tok | cache率 | raw->proj 削減 |")
	fmt.Println("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
	for _, k := range order {
		printStat(k, byKey[k])
	}
	fmt.Println("\n### カテゴリ別")
	fmt.Println("| カテゴリ | n | 総合 | 必須Tool到達 | 禁止Tool回避 | 権限 | svc recall | 平均step | 平均ms | prompt tok | cache率 | raw->proj 削減 |")
	fmt.Println("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
	for _, c := range cats {
		printStat(c, byCat[c])
	}
}

func printStat(label string, s *stat) {
	n := float64(s.n)
	cache := 0.0
	if s.promptTok > 0 {
		cache = s.cachedTok / s.promptTok * 100
	}
	red := 0.0
	if s.rawB > 0 {
		red = (1 - s.projB/s.rawB) * 100
	}
	fmt.Printf("| %s | %d | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.1f | %.0f | %.0f | %.0f%% | %.0f%% |\n",
		label, s.n,
		float64(s.pass)/n*100, float64(s.req)/n*100, float64(s.forbid)/n*100,
		float64(s.perm)/n*100, s.svcRecall/n*100,
		s.stepsN/n, s.totalMS/n, s.promptTok/n, cache, red)
}

func failures(os_ []outcome) {
	var bad []outcome
	for _, o := range os_ {
		if !o.Pass {
			bad = append(bad, o)
		}
	}
	if len(bad) == 0 {
		fmt.Println("\n失敗ケースなし")
		return
	}
	fmt.Printf("\n### 失敗ケース (%d 件)\n\n", len(bad))
	for _, o := range bad {
		fmt.Printf("- [%s/%s] %s (%s, %s) %q\n", o.Model, o.Mode, o.CaseID, o.Category, o.UserID, o.Query)
		fmt.Printf("    実行: %v\n", o.Trace.ToolsUsed())
		if len(o.MissingTools) > 0 {
			fmt.Printf("    未到達: %v\n", o.MissingTools)
		}
		if len(o.HitForbidden) > 0 {
			fmt.Printf("    禁止Tool: %v\n", o.HitForbidden)
		}
		if o.PermNote != "" && !o.PermOK {
			fmt.Printf("    権限: %s\n", o.PermNote)
		}
		if o.Trace.Err != "" {
			fmt.Printf("    err: %s\n", o.Trace.Err)
		}
	}
}

func mark(o outcome) string {
	switch {
	case o.Trace.Err != "":
		return "X"
	case !o.PermOK:
		return "p"
	case !o.ForbidOK:
		return "f"
	case !o.RequiredOK:
		return "r"
	default:
		return "."
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "エラー:", err)
	os.Exit(1)
}
