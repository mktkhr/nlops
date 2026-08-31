// Command spike は、モックサービスを実装する前に「LLM が初手の Tool と引数を
// 正しく出せるか」だけを測る。コード投資前の関門となる計測。
//
// 測るもの:
//   - JSON Schema 追従率 (制約デコードが壊れないか)
//   - Stage 1 のサービス選定精度 (recall / precision)
//   - 初手 Tool の的中率
//   - 初手引数の一致率
//   - 1 段階 / 2 段階のレイテンシとトークン差
//   - prefix cache のヒット率
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
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/prompt"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

type result struct {
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	Schema   string `json:"schema"`
	CaseID   string `json:"case_id"`
	Category string `json:"category"`
	Query    string `json:"query"`

	JSONValid bool   `json:"json_valid"`
	Err       string `json:"err,omitempty"`

	GotServices  []string `json:"got_services,omitempty"`
	SvcRecall    float64  `json:"svc_recall"`
	SvcPrecision float64  `json:"svc_precision"`

	GotTool string         `json:"got_tool"`
	ToolOK  bool           `json:"tool_ok"`
	GotArgs map[string]any `json:"got_args"`
	ArgsHit float64        `json:"args_hit"`
	ArgsMis []string       `json:"args_miss,omitempty"`

	Stage1MS  float64 `json:"stage1_ms"`
	Stage2MS  float64 `json:"stage2_ms"`
	TotalMS   float64 `json:"total_ms"`
	PromptTok int     `json:"prompt_tokens"`
	CachedTok int     `json:"cached_tokens"`
	CompTok   int     `json:"completion_tokens"`
}

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバの base URL")
		models  = flag.String("models", "gemma4-12b", "カンマ区切りのモデル ID")
		modes   = flag.String("modes", "two_stage,one_stage", "two_stage / one_stage")
		schemas = flag.String("schemas", "strict,loose", "strict / loose")
		catPath = flag.String("catalog", "catalog/services.json", "カタログ")
		csPath  = flag.String("cases", "eval/golden/cases.json", "ゴールデンセット")
		outDir  = flag.String("out", "docs/spike-raw", "生ログ出力先")
		filter  = flag.String("category", "", "カテゴリで絞る (カンマ区切り)")
		limit   = flag.Int("limit", 0, "先頭 N 件のみ実行 (0 = 全件)")
		reason  = flag.String("reasoning", "none", "reasoning_effort (gpt-oss 系は low)")
		maxTok  = flag.Int("max-tokens", 256, "max_tokens")
	)
	flag.Parse()

	cat, err := toolschema.Load(*catPath)
	if err != nil {
		die(err)
	}
	set, err := golden.Load(*csPath)
	if err != nil {
		die(err)
	}

	cases := filterCases(set.Cases, *filter, *limit)
	if len(cases) == 0 {
		die(fmt.Errorf("実行対象のケースがありません"))
	}

	client := llm.New(*base)
	client.ReasoningEffort = *reason
	ctx := context.Background()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die(err)
	}
	stamp := time.Now().Format("20060102-150405")
	rawPath := filepath.Join(*outDir, "spike-"+stamp+".jsonl")
	raw, err := os.Create(rawPath)
	if err != nil {
		die(err)
	}
	defer raw.Close()
	enc := json.NewEncoder(raw)

	var all []result
	for _, model := range split(*models) {
		for _, mode := range split(*modes) {
			for _, sch := range split(*schemas) {
				fmt.Fprintf(os.Stderr, "\n### %s / %s / schema=%s (%d 件)\n", model, mode, sch, len(cases))
				for i, c := range cases {
					r := runCase(ctx, client, cat, c, model, mode, sch == "strict", *maxTok)
					all = append(all, r)
					_ = enc.Encode(r)
					mark := "."
					if !r.JSONValid {
						mark = "X"
					} else if !r.ToolOK {
						mark = "t"
					} else if r.ArgsHit < 1 {
						mark = "a"
					}
					fmt.Fprint(os.Stderr, mark)
					if (i+1)%40 == 0 {
						fmt.Fprintln(os.Stderr)
					}
				}
				fmt.Fprintln(os.Stderr)
			}
		}
	}

	fmt.Printf("\n生ログ: %s\n", rawPath)
	printSummary(all)
	printFailures(all)
}

func runCase(ctx context.Context, c *llm.Client, cat *toolschema.Catalog, gc golden.Case,
	model, mode string, strictArgs bool, maxTok int) result {

	r := result{Model: model, Mode: mode, Schema: schemaLabel(strictArgs),
		CaseID: gc.ID, Category: gc.Category, Query: gc.Query}
	start := time.Now()

	tools := cat.Tools()
	if mode == "two_stage" {
		svcs, resp, err := routeServices(ctx, c, cat, gc.Query, model, maxTok)
		if resp != nil {
			r.Stage1MS = float64(resp.Wall.Microseconds()) / 1000
			r.PromptTok += resp.Usage.PromptTokens
			r.CachedTok += resp.Usage.PromptTokensDetails.CachedTokens
			r.CompTok += resp.Usage.CompletionTokens
		}
		if err != nil {
			r.Err = "stage1: " + err.Error()
			r.TotalMS = float64(time.Since(start).Microseconds()) / 1000
			return r
		}
		r.GotServices = svcs
		r.SvcRecall, r.SvcPrecision = golden.ServiceScore(gc.ExpectedServices, svcs)
		tools = cat.Tools(svcs...)
		if len(tools) == 0 {
			r.Err = "stage1 が有効なサービスを返さなかった"
			r.TotalMS = float64(time.Since(start).Microseconds()) / 1000
			return r
		}
	}

	call, resp, err := selectTool(ctx, c, tools, gc.Query, model, strictArgs, maxTok)
	if resp != nil {
		r.Stage2MS = float64(resp.Wall.Microseconds()) / 1000
		r.PromptTok += resp.Usage.PromptTokens
		r.CachedTok += resp.Usage.PromptTokensDetails.CachedTokens
		r.CompTok += resp.Usage.CompletionTokens
	}
	r.TotalMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		r.Err = "stage2: " + err.Error()
		return r
	}

	r.JSONValid = true
	r.GotTool = call.Tool
	r.GotArgs = call.Arguments
	r.ToolOK = gc.FirstCall.ToolOK(call.Tool)
	r.ArgsHit, r.ArgsMis = gc.FirstCall.ArgsScore(call.Arguments)
	return r
}

func routeServices(ctx context.Context, c *llm.Client, cat *toolschema.Catalog,
	query, model string, maxTok int) ([]string, *llm.Response, error) {

	resp, err := c.Chat(ctx, llm.Request{
		Model:       model,
		Temperature: 0,
		MaxTokens:   maxTok,
		Messages: []llm.Message{
			{Role: "system", Content: prompt.ServiceRouterSystem(cat)},
			{Role: "user", Content: query},
		},
		ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: prompt.ServiceRouterSchema(cat)},
	})
	if err != nil {
		return nil, resp, err
	}
	if resp.FinishReason() == "length" {
		return nil, resp, fmt.Errorf("max_tokens 到達 (content 空)")
	}
	var out struct {
		Services []string `json:"services"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil {
		return nil, resp, fmt.Errorf("JSON 不正: %w (%q)", err, trunc(resp.Text(), 120))
	}
	// カタログ順に正規化して prefix を安定させる。
	valid := map[string]bool{}
	for _, s := range out.Services {
		valid[s] = true
	}
	var ordered []string
	for _, n := range cat.ServiceNames() {
		if valid[n] {
			ordered = append(ordered, n)
		}
	}
	return ordered, resp, nil
}

type toolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func selectTool(ctx context.Context, c *llm.Client, tools []toolschema.Tool,
	query, model string, strictArgs bool, maxTok int) (toolCall, *llm.Response, error) {

	var call toolCall
	resp, err := c.Chat(ctx, llm.Request{
		Model:       model,
		Temperature: 0,
		MaxTokens:   maxTok,
		Messages: []llm.Message{
			{Role: "system", Content: prompt.ToolSelectorSystem(tools)},
			{Role: "user", Content: query},
		},
		ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: prompt.ToolSelectorSchema(tools, strictArgs)},
	})
	if err != nil {
		return call, resp, err
	}
	if resp.FinishReason() == "length" {
		return call, resp, fmt.Errorf("max_tokens 到達 (content 空)")
	}
	if err := json.Unmarshal([]byte(resp.Text()), &call); err != nil {
		return call, resp, fmt.Errorf("JSON 不正: %w (%q)", err, trunc(resp.Text(), 120))
	}
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	return call, resp, nil
}

// ---- 集計 ----

type agg struct {
	n                        int
	jsonOK, toolOK           int
	svcRecall, svcPrec, args float64
	stage1, stage2, total    float64
	prompt, cached, comp     int
}

func printSummary(rs []result) {
	groups := map[string]*agg{}
	var order []string
	for _, r := range rs {
		k := fmt.Sprintf("%s | %s | %s", r.Model, r.Mode, r.Schema)
		a, ok := groups[k]
		if !ok {
			a = &agg{}
			groups[k] = a
			order = append(order, k)
		}
		a.n++
		if r.JSONValid {
			a.jsonOK++
			if r.ToolOK {
				a.toolOK++
			}
			a.args += r.ArgsHit
		}
		a.svcRecall += r.SvcRecall
		a.svcPrec += r.SvcPrecision
		a.stage1 += r.Stage1MS
		a.stage2 += r.Stage2MS
		a.total += r.TotalMS
		a.prompt += r.PromptTok
		a.cached += r.CachedTok
		a.comp += r.CompTok
	}
	sort.Strings(order)

	fmt.Println()
	fmt.Println("| 構成 | n | JSON | svc recall | svc prec | tool的中 | args一致 | s1 ms | s2 ms | 合計 ms | prompt tok | cache率 |")
	fmt.Println("|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
	for _, k := range order {
		a := groups[k]
		n := float64(a.n)
		cacheRate := 0.0
		if a.prompt > 0 {
			cacheRate = float64(a.cached) / float64(a.prompt) * 100
		}
		argsRate := 0.0
		if a.jsonOK > 0 {
			argsRate = a.args / float64(a.jsonOK) * 100
		}
		fmt.Printf("| %s | %d | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.0f%% | %.0f | %.0f | %.0f | %d | %.0f%% |\n",
			k, a.n,
			float64(a.jsonOK)/n*100,
			a.svcRecall/n*100, a.svcPrec/n*100,
			float64(a.toolOK)/n*100, argsRate,
			a.stage1/n, a.stage2/n, a.total/n,
			a.prompt/a.n, cacheRate)
	}
}

func printFailures(rs []result) {
	var bad []result
	for _, r := range rs {
		if !r.JSONValid || !r.ToolOK || r.ArgsHit < 1 {
			bad = append(bad, r)
		}
	}
	if len(bad) == 0 {
		fmt.Println("\n失敗ケースなし")
		return
	}
	fmt.Printf("\n## 失敗ケース (%d 件)\n\n", len(bad))
	for _, r := range bad {
		args, _ := json.Marshal(r.GotArgs)
		fmt.Printf("- [%s/%s/%s] %s (%s) %q\n", r.Model, r.Mode, r.Schema, r.CaseID, r.Category, r.Query)
		if r.Err != "" {
			fmt.Printf("    err: %s\n", r.Err)
			continue
		}
		if len(r.GotServices) > 0 {
			fmt.Printf("    svc: %v\n", r.GotServices)
		}
		fmt.Printf("    tool: %s (ok=%v)  args: %s  miss=%v\n", r.GotTool, r.ToolOK, args, r.ArgsMis)
	}
}

// ---- ヘルパ ----

func filterCases(cs []golden.Case, filter string, limit int) []golden.Case {
	want := map[string]bool{}
	for _, f := range split(filter) {
		want[f] = true
	}
	var out []golden.Case
	for _, c := range cs {
		if len(want) > 0 && !want[c.Category] {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
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

func schemaLabel(strict bool) string {
	if strict {
		return "strict"
	}
	return "loose"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "エラー:", err)
	os.Exit(1)
}
