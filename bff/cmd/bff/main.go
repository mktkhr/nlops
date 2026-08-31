// Command bff は Frontend 向けの Backend For Frontend を起動する。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mktkhr/nlops/bff/internal/server"
	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/command"
	"github.com/mktkhr/nlops/pkg/llm"
	"github.com/mktkhr/nlops/pkg/toolschema"
	"github.com/mktkhr/nlops/pkg/uiroute"
)

func main() {
	var (
		addr    = flag.String("addr", ":8080", "listen address")
		base    = flag.String("base", "http://127.0.0.1:11435", "OpenAI 互換サーバ")
		model   = flag.String("model", "gemma4-12b", "モデル ID")
		mode    = flag.String("mode", "one_stage", "one_stage / two_stage")
		reason  = flag.String("reasoning", "none", "reasoning_effort")
		catPath = flag.String("catalog", "catalog/services.json", "カタログ")
		rolPath = flag.String("roles", "catalog/roles.json", "ロール定義")
		cmdPath = flag.String("commands", "catalog/commands.json", "更新操作の定義。空文字で無効化")
		rtPath  = flag.String("routes", "catalog/routes.json", "画面定義。空文字で画面遷移を無効化")
		steps   = flag.Int("max-steps", 6, "Tool Loop の最大反復数")
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

	client := llm.New(*base)
	client.ReasoningEffort = *reason
	runner := loop.New(cat, client)
	if *rtPath != "" {
		routes, err := uiroute.Load(*rtPath)
		if err != nil {
			die(err)
		}
		runner.Routes = routes
	}
	var cmds *command.Catalog
	if *cmdPath != "" {
		cmds, err = command.Load(*cmdPath)
		if err != nil {
			die(err)
		}
		runner.Commands = cmds
	}
	srv := server.New(runner, dir, cat)
	srv.Commands = cmds
	srv.Model = *model
	srv.Mode = loop.Mode(*mode)
	srv.MaxSteps = *steps

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// SSE で数分ストリームするので書き込みタイムアウトは設けない。
		WriteTimeout: 0,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		srv.Log.Info("listening", "addr", *addr, "model", *model, "mode", *mode)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		die(err)
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "エラー:", err)
	os.Exit(1)
}
