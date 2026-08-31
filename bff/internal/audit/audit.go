// Package audit はリクエストのトレースと更新操作の承認記録を永続化する。
//
// 置き場所を BFF にしているのは、記録する対象が「システムが何をしたか」であり、
// リクエストの入口を持つのが BFF だから。業務データはマイクロサービスが持ち続け、
// この schema をサービス側が参照することはない。
//
// 記録は best-effort とする。監査の書き込みに失敗しても、ユーザーの操作は止めない。
// ただし失敗はログに残す (静かに落とすと監査が欠けたことに気づけない)。
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
)

// Recorder は監査記録を書く。Pool が nil なら何もしない。
type Recorder struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// New は Recorder を作る。dsn が空なら記録を無効にする。
func New(ctx context.Context, dsn string, log *slog.Logger) (*Recorder, error) {
	if dsn == "" {
		return &Recorder{Log: log}, nil
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("監査 DB 接続: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("監査 DB 疎通: %w", err)
	}
	return &Recorder{Pool: pool, Log: log}, nil
}

// Enabled は記録が有効かを返す。
func (r *Recorder) Enabled() bool { return r != nil && r.Pool != nil }

// Close は接続を閉じる。
func (r *Recorder) Close() {
	if r.Enabled() {
		r.Pool.Close()
	}
}

// NewTraceID は新しいトレース ID を発行する。
func NewTraceID() string { return uuid.NewString() }

// RecordTrace は 1 リクエスト分のトレースを保存する。
func (r *Recorder) RecordTrace(ctx context.Context, traceID string, id authctx.Identity, tr *loop.Trace) {
	if !r.Enabled() {
		return
	}
	// リクエストの context は応答済みで cancel されている可能性があるので使わない。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		r.fail("トレース記録の開始", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO audit.traces (
			trace_id, user_id, role, query, model, mode, intent, outcome, answer,
			denied, incomplete, error, step_count,
			total_ms, intent_ms, answer_ms, prompt_tok, cached_tok, comp_tok, raw_bytes, proj_bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		traceID, id.UserID, string(id.Role), tr.Query, tr.Model, tr.Mode,
		nullable(tr.Intent), outcomeOf(tr), nullable(tr.Answer),
		tr.Denied, tr.Incomplete, nullable(tr.Err), len(tr.Steps),
		tr.TotalMS, tr.IntentMS, tr.AnswerMS,
		tr.PromptTok, tr.CachedTok, tr.CompTok, tr.RawBytes, tr.ProjBytes)
	if err != nil {
		r.fail("トレース記録", err)
		return
	}

	for _, s := range tr.Steps {
		kind, tool, status, denied, errMsg := "tool", s.Tool, 0, false, ""
		var result any
		switch {
		case s.Navigate != nil:
			kind = "navigate"
			result = s.Navigate
		case s.Proposal != nil:
			kind = "propose"
			result = s.Proposal
		case s.Finish:
			kind = "finish"
		}
		if s.Result != nil {
			status, denied, errMsg = s.Result.Status, s.Result.Denied, s.Result.Error
			if result == nil {
				result = s.Result.Projected
			}
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO audit.trace_steps (
				trace_id, iteration, kind, tool, arguments, status, denied, error, result,
				llm_ms, prompt_tok, cached_tok, comp_tok)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			traceID, s.Iteration, kind, nullable(tool), jsonb(s.Arguments),
			nullInt(status), denied, nullable(errMsg), jsonb(result),
			s.LLMms, s.PromptTok, s.CachedTok, s.CompTok)
		if err != nil {
			r.fail("ステップ記録", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		r.fail("トレース記録の確定", err)
	}
}

// Execution は更新操作の実行 1 回分。承認されて実行されたものも、
// 権限や業務ルールで拒否されたものも同じ形で残す。
type Execution struct {
	TraceID    string
	Identity   authctx.Identity
	Command    string
	Arguments  map[string]any
	StatusCode int
	Error      string
	Result     any
}

// RecordExecution は更新操作の実行を保存する。
func (r *Recorder) RecordExecution(ctx context.Context, e Execution) {
	if !r.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	_, err := r.Pool.Exec(ctx, `
		INSERT INTO audit.command_executions (
			execution_id, trace_id, user_id, role, command, arguments,
			status_code, ok, error, result)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		uuid.NewString(), nullableUUID(e.TraceID), e.Identity.UserID, string(e.Identity.Role),
		e.Command, jsonb(e.Arguments), e.StatusCode, e.StatusCode == 200,
		nullable(e.Error), jsonb(e.Result))
	if err != nil {
		r.fail("更新操作の記録", err)
	}
}

func (r *Recorder) fail(what string, err error) {
	if r.Log != nil {
		r.Log.Error("監査記録に失敗", "what", what, "err", err)
	}
}

func outcomeOf(tr *loop.Trace) string {
	switch {
	case tr.Err != "":
		return "error"
	case tr.Proposal != nil:
		return "propose"
	case tr.Navigate != nil:
		return "navigate"
	default:
		return "answer"
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// nullableUUID は空文字や UUID でない値を NULL にする。
// トレース ID を伴わない実行 (API を直接叩いた場合など) も記録できるようにする。
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		return nil
	}
	return s
}

func jsonb(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
