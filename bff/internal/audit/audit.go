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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	// Before / Result は更新の前後。何がどう変わったかを後から追えるように
	// 両方残す。Result だけだと「元が何だったか」が永久に分からない。
	Before any
	Result any
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
			status_code, ok, error, before, result)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		uuid.NewString(), nullableUUID(e.TraceID), e.Identity.UserID, string(e.Identity.Role),
		e.Command, jsonb(e.Arguments), e.StatusCode, e.StatusCode == 200,
		nullable(e.Error), jsonb(e.Before), jsonb(e.Result))
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

// staleClaim は「実行中」のまま放置された枠を引き継ぐまでの時間。
//
// 実行の途中でプロセスが落ちると status_code = 0 の行が残る。これを永久に
// 「実行中」とみなすと、その操作は二度と実行できなくなる。人手で消させるより、
// 一定時間が過ぎたら引き継げるようにする。サービス呼び出しのタイムアウトより
// 十分長くとる。
const staleClaim = 2 * time.Minute

// Claim は実行前に枠を確保する。
//
//   - claimed=true  : 自分が実行してよい。完了したら Complete を呼ぶ
//   - claimed=false : 同じ鍵の実行が既にある。prev がその記録 (実行中なら StatusCode=0)
//
// **止めているのは一意制約であって、事前の SELECT ではない。**
// 「無いことを確認してから INSERT」では、同時に来た 2 本が両方とも
// 「無い」を見て両方実行してしまう。
//
// key が空、または監査が無効なときは常に claimed=true になる。
// 二重実行を防げるのは監査 DB がある構成だけ、という前提をここに置く。
func (r *Recorder) Claim(ctx context.Context, key string, e Execution) (claimed bool, prev *Execution, execID string, err error) {
	if !r.Enabled() || key == "" {
		return true, nil, "", nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	id := uuid.NewString()
	// 実行中のまま古くなった枠は引き継ぐ。それ以外は何もしない。
	row := r.Pool.QueryRow(ctx, `
		INSERT INTO audit.command_executions (
			execution_id, trace_id, user_id, role, command, arguments,
			status_code, ok, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,0,false,$7)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO UPDATE
			SET execution_id = EXCLUDED.execution_id,
			    created_at   = now(),
			    user_id      = EXCLUDED.user_id,
			    role         = EXCLUDED.role
			WHERE audit.command_executions.status_code = 0
			  AND audit.command_executions.created_at < now() - $8::interval
		RETURNING execution_id`,
		id, nullableUUID(e.TraceID), e.Identity.UserID, string(e.Identity.Role),
		e.Command, jsonb(e.Arguments), key, staleClaim.String())

	var got string
	scanErr := row.Scan(&got)
	if scanErr == nil {
		return true, nil, got, nil
	}
	if !errors.Is(scanErr, pgx.ErrNoRows) {
		// 監査が書けないときに更新を止めるかは設計判断。ここでは止める。
		// 「記録できないが実行はする」は、二重実行を防ぐ根拠を失った状態で
		// 更新を通すことになる。
		return false, nil, "", fmt.Errorf("実行枠の確保: %w", scanErr)
	}

	// 既に同じ鍵の実行がある。その結果をそのまま返せるように読み出す。
	var p Execution
	var errMsg *string
	if qErr := r.Pool.QueryRow(ctx, `
		SELECT command, status_code, error
		FROM audit.command_executions WHERE idempotency_key = $1`, key,
	).Scan(&p.Command, &p.StatusCode, &errMsg); qErr != nil {
		return false, nil, "", fmt.Errorf("既存の実行記録の取得: %w", qErr)
	}
	if errMsg != nil {
		p.Error = *errMsg
	}
	return false, &p, "", nil
}

// Complete は Claim で確保した枠を結果で埋める。
func (r *Recorder) Complete(ctx context.Context, execID string, e Execution) {
	if !r.Enabled() || execID == "" {
		// 枠を取っていない構成 (監査無効) では、従来どおり後から 1 行入れる。
		r.RecordExecution(ctx, e)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	_, err := r.Pool.Exec(ctx, `
		UPDATE audit.command_executions
		   SET status_code = $2, ok = $3, error = $4, before = $5, result = $6
		 WHERE execution_id = $1`,
		execID, e.StatusCode, e.StatusCode == 200, nullable(e.Error),
		jsonb(e.Before), jsonb(e.Result))
	if err != nil {
		r.fail("更新操作の記録", err)
	}
}

// Prune は保持期間を過ぎた記録を消す。days が 0 以下なら何もしない。
//
// **消す対象は 2 つで、扱いが違う。**
//   - traces / trace_steps: 会話の記録。量が多く、ゴールデンセットの材料として
//     しか使わないので、期限で消してよい
//   - command_executions: 更新の記録。**業務データを変えた事実**なので、
//     会話より長く残す (execRetentionFactor 倍)
//
// 消したことはログに残す。黙って消えると「記録が無い」のか
// 「消した」のか区別がつかない。
func (r *Recorder) Prune(ctx context.Context, days int) {
	if !r.Enabled() || days <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	cutoff := fmt.Sprintf("%d days", days)
	// trace_steps は traces への ON DELETE CASCADE で一緒に消える。
	tag, err := r.Pool.Exec(ctx,
		`DELETE FROM audit.traces WHERE created_at < now() - $1::interval`, cutoff)
	if err != nil {
		r.fail("トレースの整理", err)
		return
	}
	traces := tag.RowsAffected()

	execCutoff := fmt.Sprintf("%d days", days*execRetentionFactor)
	tag, err = r.Pool.Exec(ctx,
		`DELETE FROM audit.command_executions WHERE created_at < now() - $1::interval`, execCutoff)
	if err != nil {
		r.fail("更新記録の整理", err)
		return
	}
	if traces > 0 || tag.RowsAffected() > 0 {
		r.Log.Info("監査記録を整理した",
			"traces", traces, "traces_days", days,
			"executions", tag.RowsAffected(), "executions_days", days*execRetentionFactor)
	}
}

// execRetentionFactor は更新記録をトレースの何倍長く残すか。
// 更新は業務データを変えた事実なので、会話の記録より長く要る。
const execRetentionFactor = 12
