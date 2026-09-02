// Command orchctl は Orchestrator の CLI。PoC のエントリポイント。
//
//	orchctl -user u_sales_e "田中さんの未発送注文を確認したい"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/command"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/toolschema"
	"github.com/mktkhr/nlops/pkg/uiroute"
)

func main() {
	var (
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバの base URL")
		model   = flag.String("model", "gemma4-12b", "モデル ID")
		mode    = flag.String("mode", "one_stage", "one_stage / two_stage")
		user    = flag.String("user", "u_admin", "実行ユーザー ID")
		catPath = flag.String("catalog", "catalog/services.json", "カタログ")
		rolPath = flag.String("roles", "catalog/roles.json", "ロール定義")
		cmdPath = flag.String("commands", "catalog/commands.json", "更新操作の定義。空文字で提案を無効化")
		rtPath  = flag.String("routes", "catalog/routes.json", "画面定義。空文字で画面遷移を無効化")
		strict  = flag.Bool("strict", true, "Tool ごとの引数スキーマで制約する")
		steps   = flag.Int("max-steps", 6, "Tool Loop の最大反復数")
		noGuard = flag.Bool("no-guard", false, "未解決 ID の差し戻しを無効化する")
		noAmbig = flag.Bool("no-ambiguity-guard", false, "曖昧な ID での読み取りの差し戻しを無効化する (比較計測用)")
		reason  = flag.String("reasoning", "none", "reasoning_effort (gpt-oss 系は low)")
		maxTok  = flag.Int("max-tokens", 512, "1 反復あたりの max_tokens")
		gate    = flag.Bool("intent-gate", true, "Loop の前に navigate / tool を2択で判定する")
		noStop  = flag.Bool("no-stop-guard", false, "空振り連続時の finish 強制を無効化する (比較計測用)")
		noProj  = flag.Bool("no-projection", false, "Response Projection を無効化する (比較計測用)")
		asJSON  = flag.Bool("json", false, "トレースを JSON で出力する")
		quiet   = flag.Bool("quiet", false, "最終回答だけを出力する")
	)
	flag.Parse()

	query := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if query == "" {
		fmt.Fprintln(os.Stderr, "使い方: orchctl [flags] \"<自然言語の要求>\"")
		os.Exit(2)
	}

	cat, err := toolschema.Load(*catPath)
	if err != nil {
		die(err)
	}
	dir, err := authctx.LoadDirectory(*rolPath)
	if err != nil {
		die(err)
	}
	id, err := dir.Lookup(*user)
	if err != nil {
		die(err)
	}

	lc := llm.New(*base)
	lc.ReasoningEffort = *reason
	runner := loop.New(cat, lc)
	if *rtPath != "" {
		routes, err := uiroute.Load(*rtPath)
		if err != nil {
			die(err)
		}
		runner.Routes = routes
	}
	if *cmdPath != "" {
		cmds, err := command.Load(*cmdPath)
		if err != nil {
			die(err)
		}
		runner.Commands = cmds
	}
	runner.Executor.GuardUnresolvedIDs = !*noGuard
	runner.Executor.GuardAmbiguousReads = !*noAmbig
	runner.Executor.DisableProjection = *noProj

	tr := runner.Run(context.Background(), id, query, loop.Options{
		Model: *model, Mode: loop.Mode(*mode), StrictArgs: *strict,
		MaxSteps: *steps, MaxTokens: *maxTok, Answer: true, StopGuard: !*noStop, IntentGate: *gate,
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(tr)
		return
	}
	if *quiet {
		fmt.Println(tr.Answer)
		return
	}
	render(tr)
}

func render(tr *loop.Trace) {
	fmt.Printf("要求 : %s\n", tr.Query)
	fmt.Printf("実行者: %s (%s)\n", tr.UserID, tr.Role)
	fmt.Printf("構成 : %s / %s\n", tr.Model, tr.Mode)
	if len(tr.Services) > 0 {
		fmt.Printf("選定 : %s\n", strings.Join(tr.Services, ", "))
	}
	fmt.Println()
	for _, s := range tr.Steps {
		if s.Finish {
			fmt.Printf("  %d. finish (%.0fms)\n", s.Iteration, s.LLMms)
			continue
		}
		if s.Proposal != nil {
			a, _ := json.Marshal(s.Proposal.Arguments)
			fmt.Printf("  %d. propose %s %s (%.0fms)\n", s.Iteration, s.Proposal.Command, a, s.LLMms)
			continue
		}
		if s.Navigate != nil {
			f, _ := json.Marshal(s.Navigate.Filters)
			fmt.Printf("  %d. navigate %s %s (%.0fms)\n", s.Iteration, s.Navigate.Route, f, s.LLMms)
			continue
		}
		args, _ := json.Marshal(s.Arguments)
		fmt.Printf("  %d. %s %s\n", s.Iteration, s.Tool, args)
		if s.Result != nil {
			out, _ := json.Marshal(s.Result.Projected)
			status := fmt.Sprintf("%d", s.Result.Status)
			if s.Result.Error != "" {
				status = "拒否"
				out = []byte(s.Result.Error)
			}
			fmt.Printf("     -> [%s] %s\n", status, trunc(string(out), 240))
			if s.Result.RawBytes > 0 {
				fmt.Printf("        raw %dB -> proj %dB (%.0f%% 削減)\n",
					s.Result.RawBytes, s.Result.ProjBytes,
					100*(1-float64(s.Result.ProjBytes)/float64(s.Result.RawBytes)))
			}
		}
	}
	if tr.Err != "" {
		fmt.Printf("\nエラー: %s\n", tr.Err)
	}
	if tr.Incomplete {
		fmt.Printf("\n注意: 最大反復数に到達したため打ち切りました\n")
	}
	if tr.Proposal != nil {
		a, _ := json.Marshal(tr.Proposal.Arguments)
		fmt.Printf("\n--- 操作の提案 (未実行) ---\n%s (%s)\n引数: %s\n確認: %s\n",
			tr.Proposal.Title, tr.Proposal.Command, a, tr.Proposal.Confirm)
	}
	if tr.Navigate != nil {
		fmt.Printf("\n--- 画面遷移 ---\n%s", tr.Navigate.Route)
		if len(tr.Navigate.Filters) > 0 {
			f, _ := json.Marshal(tr.Navigate.Filters)
			fmt.Printf(" %s", f)
		}
		fmt.Println()
	}
	if tr.Answer != "" {
		fmt.Printf("\n--- 回答 ---\n%s\n", strings.TrimSpace(tr.Answer))
	}
	cacheRate := 0.0
	if tr.PromptTok > 0 {
		cacheRate = float64(tr.CachedTok) / float64(tr.PromptTok) * 100
	}
	fmt.Printf("\n計測: 合計 %.0fms (routing %.0f / answer %.0f) | prompt %d tok (cache %.0f%%) | completion %d tok | raw %dB -> proj %dB\n",
		tr.TotalMS, tr.RouteMS, tr.AnswerMS, tr.PromptTok, cacheRate, tr.CompTok, tr.RawBytes, tr.ProjBytes)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "エラー:", err)
	os.Exit(1)
}
