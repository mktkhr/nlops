package server

import (
	"context"
	"net/http"
	"strconv"
)

// 監査の閲覧 API。
//
// 誰が何を見られるかは業務データとは別の判断になる。ここでは admin だけに開く。
// 自分のトレースだけを見せる、監査ロールを作るといった設計は運用要件次第。

func (s *Server) requireAuditReader(w http.ResponseWriter, r *http.Request) (bool, string) {
	id, err := s.identity(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return false, ""
	}
	if id.Role != "admin" {
		writeErr(w, http.StatusForbidden, "監査ログを参照できるのは管理者だけです")
		return false, ""
	}
	if !s.Audit.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "監査ログは無効化されています")
		return false, ""
	}
	return true, id.UserID
}

func limitOf(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// handleAuditTraces は直近のトレース一覧を返す。
func (s *Server) handleAuditTraces(w http.ResponseWriter, r *http.Request) {
	ok, _ := s.requireAuditReader(w, r)
	if !ok {
		return
	}
	rows, err := s.queryJSON(r.Context(), `
		SELECT trace_id, created_at, user_id, role, query, outcome, intent,
		       denied, incomplete, error, step_count, total_ms, prompt_tok, cached_tok
		FROM audit.traces
		WHERE ($1 = '' OR user_id = $1)
		ORDER BY created_at DESC
		LIMIT $2`, r.URL.Query().Get("user"), limitOf(r, 50, 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "count": len(rows)})
}

// handleAuditTrace は 1 件のトレースをステップ付きで返す。
func (s *Server) handleAuditTrace(w http.ResponseWriter, r *http.Request) {
	ok, _ := s.requireAuditReader(w, r)
	if !ok {
		return
	}
	id := r.PathValue("trace_id")
	traces, err := s.queryJSON(r.Context(), `SELECT * FROM audit.traces WHERE trace_id = $1::uuid`, id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(traces) == 0 {
		writeErr(w, http.StatusNotFound, "該当するトレースがありません")
		return
	}
	steps, err := s.queryJSON(r.Context(),
		`SELECT * FROM audit.trace_steps WHERE trace_id = $1::uuid ORDER BY iteration`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	execs, err := s.queryJSON(r.Context(),
		`SELECT * FROM audit.command_executions WHERE trace_id = $1::uuid ORDER BY created_at`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"trace": traces[0], "steps": steps, "executions": execs,
	})
}

// handleAuditExecutions は更新操作の実行履歴を返す。拒否された試行も含む。
func (s *Server) handleAuditExecutions(w http.ResponseWriter, r *http.Request) {
	ok, _ := s.requireAuditReader(w, r)
	if !ok {
		return
	}
	rows, err := s.queryJSON(r.Context(), `
		SELECT execution_id, created_at, trace_id, user_id, role, command,
		       arguments, status_code, ok, error, before, result
		FROM audit.command_executions
		WHERE ($1 = '' OR command = $1)
		ORDER BY created_at DESC
		LIMIT $2`, r.URL.Query().Get("command"), limitOf(r, 50, 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "count": len(rows)})
}

// queryJSON はクエリ結果をカラム名そのままの map として返す。
func (s *Server) queryJSON(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	rows, err := s.Audit.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	out := []map[string]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[string(f.Name)] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
