// Package server は Frontend 向けの BFF を実装する。
//
// 責務は Presentation / Orchestration に限る。
//   - Frontend 向け API と DTO 変換
//   - ユーザー識別情報の解決と下流への伝播
//   - 複数 Microservice の Aggregation
//   - LLM Orchestrator への Entry Point (進捗を SSE でストリームする)
//
// 業務ルール・Validation・データ整合性は Microservice 側の責務であり、
// ここには置かない。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"bytes"

	"github.com/mktkhr/nlops/bff/internal/audit"
	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/command"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

// Server は BFF。
type Server struct {
	Runner  *loop.Runner
	Dir     *authctx.Directory
	Catalog *toolschema.Catalog
	Log     *slog.Logger
	HTTP    *http.Client

	// Commands は更新操作の定義。実行の入口はここだけで、
	// LLM の経路 (Executor) はこの定義を持たない。
	Commands *command.Catalog

	// Audit はトレースと更新操作の記録先。無効なら記録しない。
	Audit *audit.Recorder

	Model    string
	Mode     loop.Mode
	MaxSteps int

	mux *http.ServeMux
}

// New は BFF を組み立てる。
func New(runner *loop.Runner, dir *authctx.Directory, cat *toolschema.Catalog) *Server {
	s := &Server{
		Runner: runner, Dir: dir, Catalog: cat,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		HTTP:     &http.Client{Timeout: 15 * time.Second},
		Model:    "gemma4-12b",
		Mode:     loop.ModeOneStage,
		MaxSteps: 6,
		Audit:    &audit.Recorder{},
		mux:      http.NewServeMux(),
	}
	s.routes()
	return s
}

// Handler は HTTP ハンドラを返す。
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/users", s.handleUsers)
	s.mux.HandleFunc("POST /api/ask", s.handleAsk)
	s.mux.HandleFunc("GET /api/orders", s.handleOrders)
	s.mux.HandleFunc("GET /api/customers", s.handleCustomers)
	s.mux.HandleFunc("POST /api/commands/execute", s.handleExecute)
	s.mux.HandleFunc("GET /api/audit/traces", s.handleAuditTraces)
	s.mux.HandleFunc("GET /api/audit/traces/{trace_id}", s.handleAuditTrace)
	s.mux.HandleFunc("GET /api/audit/executions", s.handleAuditExecutions)
	s.mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

// ---- ユーザー ----

func (s *Server) handleUsers(w http.ResponseWriter, _ *http.Request) {
	type dto struct {
		UserID string `json:"userId"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Region string `json:"region,omitempty"`
	}
	out := make([]dto, 0, len(s.Dir.Users))
	for _, u := range s.Dir.Users {
		out = append(out, dto{UserID: u.UserID, Name: u.Name, Role: string(u.Role), Region: u.Region})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// identity はリクエストから実行ユーザーを解決する。
// PoC なのでヘッダの user id をそのまま信頼する。実運用ではここが認証になる。
func (s *Server) identity(r *http.Request) (authctx.Identity, error) {
	uid := r.Header.Get(authctx.HeaderUserID)
	if uid == "" {
		uid = r.URL.Query().Get("user")
	}
	if uid == "" {
		return authctx.Identity{}, fmt.Errorf("ユーザーが指定されていません")
	}
	return s.Dir.Lookup(uid)
}

// ---- AI への問い合わせ (SSE) ----

type askRequest struct {
	Query string `json:"query"`
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		writeErr(w, http.StatusBadRequest, "query が必要です")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "ストリーミングに対応していません")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Nginx を挟んでもバッファさせない
	w.WriteHeader(http.StatusOK)

	send := func(event string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	traceID := audit.NewTraceID()
	send("start", map[string]any{
		"query": req.Query, "user": id.UserID, "model": s.Model, "traceId": traceID,
	})

	// Tool Loop は 1 要求あたり数秒かかる。何も返さないと壊れて見えるので、
	// ステップ完了ごとに逐次流す。OnStep は Run と同じ goroutine から呼ばれる。
	tr := s.Runner.Run(r.Context(), id, req.Query, loop.Options{
		Model: s.Model, Mode: s.Mode, StrictArgs: true,
		MaxSteps: s.MaxSteps, MaxTokens: 512, Answer: true, StopGuard: true, IntentGate: true,
		OnStep: func(st loop.Step) { send("step", toStepDTO(st)) },
	})

	if tr.Err != "" {
		send("error", map[string]any{"message": tr.Err})
	}
	if tr.Proposal != nil {
		// 提案は流すだけ。実行は人間が確認してから /api/commands/execute を叩く。
		send("proposal", tr.Proposal)
	}
	if tr.Navigate != nil {
		send("navigate", map[string]any{
			"route":   tr.Navigate.Route,
			"filters": tr.Navigate.Filters,
			"reason":  tr.Navigate.Reason,
		})
	}
	send("answer", map[string]any{"answer": tr.Answer})
	send("done", map[string]any{
		"totalMs":    tr.TotalMS,
		"promptTok":  tr.PromptTok,
		"cachedTok":  tr.CachedTok,
		"compTok":    tr.CompTok,
		"rawBytes":   tr.RawBytes,
		"projBytes":  tr.ProjBytes,
		"denied":     tr.Denied,
		"incomplete": tr.Incomplete,
		"toolsUsed":  tr.ToolsUsed(),
		"navigated":  tr.Navigate != nil,
		"proposed":   tr.Proposal != nil,
	})
	s.Audit.RecordTrace(r.Context(), traceID, id, tr)
	s.Log.Info("ask", "user", id.UserID, "steps", len(tr.Steps), "ms", int(tr.TotalMS), "trace", traceID)
}

type stepDTO struct {
	Iteration int              `json:"iteration"`
	Tool      string           `json:"tool,omitempty"`
	Arguments map[string]any   `json:"arguments,omitempty"`
	Finish    bool             `json:"finish,omitempty"`
	Forced    bool             `json:"forced,omitempty"`
	Status    int              `json:"status,omitempty"`
	Denied    bool             `json:"denied,omitempty"`
	Error     string           `json:"error,omitempty"`
	Result    any              `json:"result,omitempty"`
	Navigate  *loop.Navigation `json:"navigate,omitempty"`
	Proposal  *loop.Proposal   `json:"proposal,omitempty"`
	LLMms     float64          `json:"llmMs"`
}

func toStepDTO(st loop.Step) stepDTO {
	d := stepDTO{
		Iteration: st.Iteration, Tool: st.Tool, Arguments: st.Arguments,
		Finish: st.Finish, Forced: st.Forced, Navigate: st.Navigate, Proposal: st.Proposal, LLMms: st.LLMms,
	}
	if st.Result != nil {
		d.Status = st.Result.Status
		d.Denied = st.Result.Denied
		d.Error = st.Result.Error
		d.Result = st.Result.Projected // Projection 済みのものだけを返す
	}
	return d
}

// ---- 更新操作の実行 ----

type executeRequest struct {
	Command   string         `json:"command"`
	Arguments map[string]any `json:"arguments"`
	TraceID   string         `json:"traceId"`
}

// handleExecute は人間が確認した更新操作を実行する。
//
// LLM はこの経路を呼べない。呼ぶのは画面からの明示的な操作だけ。
// 受け取った内容はカタログに対して検証し直す。クライアントの言い分は信用しない。
// 実行可否の業務判断はサービス側が行うので、ここでは判断しない。
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.Commands == nil {
		writeErr(w, http.StatusNotFound, "更新操作は無効化されています")
		return
	}
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "リクエストを解釈できません")
		return
	}
	// 拒否された試行も記録する。何をやろうとしたかが監査の本体。
	reject := func(code int, msg string) {
		s.Audit.RecordExecution(r.Context(), audit.Execution{
			TraceID: req.TraceID, Identity: id, Command: req.Command,
			Arguments: req.Arguments, StatusCode: code, Error: msg,
		})
		writeErr(w, code, msg)
	}

	cmd, ok := s.Commands.ByName(req.Command)
	if !ok {
		reject(http.StatusBadRequest, fmt.Sprintf("操作 %q は存在しません", req.Command))
		return
	}
	args, err := cmd.Validate(req.Arguments)
	if err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}
	if !id.CanWrite(cmd.Service) {
		reject(http.StatusForbidden,
			fmt.Sprintf("ロール %s は %s を更新できません", id.Role, cmd.Service))
		return
	}

	base := s.baseURL(cmd.Service)
	if base == "" {
		writeErr(w, http.StatusInternalServerError, "サービスの接続先が不明です")
		return
	}
	path := cmd.HTTP.Path
	body := map[string]any{}
	for k, v := range args {
		ph := "{" + k + "}"
		if strings.Contains(path, ph) {
			path = strings.ReplaceAll(path, ph, url.PathEscape(fmt.Sprint(v)))
			continue
		}
		body[k] = v
	}
	if strings.Contains(path, "{") {
		reject(http.StatusBadRequest, "必須のパスパラメータが不足しています")
		return
	}
	payload, _ := json.Marshal(body)

	req2, err := http.NewRequestWithContext(r.Context(), cmd.HTTP.Method, base+path, bytes.NewReader(payload))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req2.Header.Set("Content-Type", "application/json")
	id.Apply(req2)

	resp, err := s.HTTP.Do(req2)
	if err != nil {
		reject(http.StatusBadGateway, fmt.Sprintf("%s サービスへ接続できません", cmd.Service))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	s.Log.Info("execute", "user", id.UserID, "command", cmd.Name, "status", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		// 業務ルールによる拒否 (409) も含め、サービスの判断をそのまま返す。
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if msg == "" {
			msg = fmt.Sprintf("%s サービスが %d を返しました", cmd.Service, resp.StatusCode)
		}
		s.Audit.RecordExecution(r.Context(), audit.Execution{
			TraceID: req.TraceID, Identity: id, Command: cmd.Name,
			Arguments: args, StatusCode: resp.StatusCode, Error: msg,
		})
		writeErr(w, resp.StatusCode, msg)
		return
	}
	var after any
	_ = json.Unmarshal(raw, &after)
	s.Audit.RecordExecution(r.Context(), audit.Execution{
		TraceID: req.TraceID, Identity: id, Command: cmd.Name,
		Arguments: args, StatusCode: http.StatusOK, Result: after,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command": cmd.Name})
}

// ---- 画面向けの読み取り API ----

// handleOrders は注文一覧を返す。order サービスの結果に customer サービスの
// 顧客名を合成する。この Aggregation が BFF を置く理由。
func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := url.Values{}
	for _, k := range []string{"status", "customer_id", "ordered_from", "ordered_to"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	pageParams(r, q)

	// 顧客名は customer サービスが持つ。氏名でフィルタする場合はここで ID へ
	// 解決し、**その ID 集合を注文サービスへ渡す**。
	//
	// 以前は顧客一覧と注文一覧をそれぞれ 1 ページ取ってメモリ上で突き合わせていた。
	// データが 11 件のうちは動くが、5 万件では「新しい順 100 件」と
	// 「顧客の先頭 100 件」が重ならず、実在する注文に対して 0 件を返す。
	nameFilter := r.URL.Query().Get("customer_name")
	if nameFilter != "" {
		custQ := url.Values{}
		custQ.Set("name", nameFilter)
		custQ.Set("limit", strconv.Itoa(maxResolve))
		cust, ccode, cerr := s.fetchPage(r.Context(), id, "customer", "/customers", custQ)
		if cerr != nil {
			// 403 を 502 へ潰さない。権限がないことは呼び出し側へそのまま伝える。
			writeErr(w, ccode, cerr.Error())
			return
		}
		if cust.HasMore {
			// 一部だけ絞り込んで「これが全部です」と見せるより、
			// 絞り込めなかったことを伝えるほうが安全。
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(
				"「%s」に一致する顧客が %d 件あります。氏名をもう少し具体的に指定してください。",
				nameFilter, cust.Count))
			return
		}
		if len(cust.Items) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"items": []any{}, "count": 0, "hasMore": false, "offset": 0, "limit": 0})
			return
		}
		for _, c := range cust.Items {
			if cid, ok := c["customer_id"].(string); ok {
				q.Add("customer_ids", cid)
			}
		}
	}

	op, code, err := s.fetchPage(r.Context(), id, "order", "/orders", q)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	orders := op.Items

	// 表示用の顧客名は、返ってきた注文の顧客だけを引く。
	names := s.customerNames(r.Context(), id, orders)

	type dto struct {
		OrderID      string `json:"orderId"`
		CustomerID   string `json:"customerId"`
		CustomerName string `json:"customerName"`
		Status       string `json:"status"`
		OrderedAt    string `json:"orderedAt"`
		TotalAmount  int64  `json:"totalAmount"`
	}
	items := make([]dto, 0, len(orders))
	for _, o := range orders {
		cid, _ := o["customer_id"].(string)
		items = append(items, dto{
			OrderID:      str(o["order_id"]),
			CustomerID:   cid,
			CustomerName: names[cid],
			Status:       str(o["status"]),
			OrderedAt:    str(o["ordered_at"]),
			TotalAmount:  num(o["total_amount"]),
		})
	}
	// count は該当総件数。items はその 1 ページ分でしかない。
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": op.Count, "hasMore": op.HasMore,
		"offset": op.Offset, "limit": op.Limit})
}

// pageParams は画面からのページ指定をサービスへそのまま渡す形にする。
//
// 上限や既定値の判断はサービス側 (svc.Pg) が持つ。BFF が独自の上限を
// 持つと、サービスの制約と二重管理になってずれる。
func pageParams(r *http.Request, q url.Values) {
	for _, k := range []string{"limit", "offset"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
}

// maxResolve は氏名から顧客 ID を引くときの上限。
// これを超える曖昧な氏名は、黙って一部だけ返さずエラーにする。
const maxResolve = 200

// customerNames は注文行に出てきた顧客の氏名だけを引く。
// 顧客一覧の先頭 1 ページを取って突き合わせる実装は、
// 顧客が増えた時点で当たらなくなる。
func (s *Server) customerNames(ctx context.Context, id authctx.Identity,
	rows []map[string]any) map[string]string {

	seen := map[string]bool{}
	q := url.Values{}
	for _, o := range rows {
		cid, _ := o["customer_id"].(string)
		if cid == "" || seen[cid] {
			continue
		}
		seen[cid] = true
		q.Add("customer_ids", cid)
	}
	names := map[string]string{}
	if len(seen) == 0 {
		return names
	}
	q.Set("limit", strconv.Itoa(len(seen)))
	// 氏名が引けなくても注文一覧そのものは出す。名前欄が空になるだけ。
	cust, _, err := s.fetchPage(ctx, id, "customer", "/customers", q)
	if err != nil {
		return names
	}
	for _, c := range cust.Items {
		if cid, ok := c["customer_id"].(string); ok {
			names[cid], _ = c["name"].(string)
		}
	}
	return names
}

func (s *Server) handleCustomers(w http.ResponseWriter, r *http.Request) {
	id, err := s.identity(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := url.Values{}
	for _, k := range []string{"name", "email", "status", "region"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	pageParams(r, q)
	cp, code, err := s.fetchPage(r.Context(), id, "customer", "/customers", q)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	rows := cp.Items
	type dto struct {
		CustomerID string `json:"customerId"`
		Name       string `json:"name"`
		Region     string `json:"region"`
		Status     string `json:"status"`
	}
	items := make([]dto, 0, len(rows))
	for _, c := range rows {
		items = append(items, dto{
			CustomerID: str(c["customer_id"]), Name: str(c["name"]),
			Region: str(c["region"]), Status: str(c["status"]),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "count": cp.Count, "hasMore": cp.HasMore,
		"offset": cp.Offset, "limit": cp.Limit})
}

// page は一覧 API の 1 ページ分。Count は返した行数ではなく該当総件数。
type page struct {
	Items   []map[string]any `json:"items"`
	Count   int              `json:"count"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
	HasMore bool             `json:"has_more"`
}

// fetchPage は Microservice の一覧 API を呼ぶ。認証情報の付与はここで行う。
// 総件数と続きの有無も返す。「50,011 件中 100 件を表示」を画面に出すために要る。
func (s *Server) fetchPage(ctx context.Context, id authctx.Identity,
	service, path string, q url.Values) (page, int, error) {

	base := s.baseURL(service)
	if base == "" {
		return page{}, http.StatusInternalServerError, fmt.Errorf("サービス %s の接続先が不明です", service)
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return page{}, http.StatusInternalServerError, err
	}
	id.Apply(req)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return page{}, http.StatusBadGateway, fmt.Errorf("%s サービスへ接続できません", service)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		return page{}, http.StatusForbidden, fmt.Errorf("%s を参照する権限がありません", service)
	}
	if resp.StatusCode != http.StatusOK {
		return page{}, resp.StatusCode, fmt.Errorf("%s サービスが %d を返しました", service, resp.StatusCode)
	}
	var out page
	if err := json.Unmarshal(body, &out); err != nil {
		return page{}, http.StatusBadGateway, fmt.Errorf("%s サービスの応答を解釈できません", service)
	}
	return out, http.StatusOK, nil
}

// baseURL はカタログからサービスの接続先を引く。
// Tool Registry と同じ定義を使うことで接続先の二重管理を避ける。
func (s *Server) baseURL(service string) string {
	for _, svc := range s.Catalog.Services {
		if svc.Name == service {
			return svc.BaseURL
		}
	}
	return ""
}

// ---- ヘルパ ----

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
