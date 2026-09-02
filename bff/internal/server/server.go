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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
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

	// Thinking は要求ごとにモデルの思考を有効にする。
	// 既定は false。有効にすると遅くなり、制約デコードの JSON が
	// 出てこない失敗が増える (docs/decisions.md)。画面から切り替えて
	// 違いを見られるようにするための口。
	Thinking bool `json:"thinking"`
}

// thinkingMaxTokens は思考を有効にしたときの 1 反復あたりの上限。
//
// 既定の 512 のままだと reasoning が枠を使い切って content が空になり、
// 「切り替えたら必ず壊れる」ように見える。実測に基づく値。
const thinkingMaxTokens = 8192

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
		"thinking": req.Thinking,
	})

	maxTok := 512
	if req.Thinking {
		maxTok = thinkingMaxTokens
	}

	// Tool Loop は 1 要求あたり数秒かかる。何も返さないと壊れて見えるので、
	// ステップ完了ごとに逐次流す。OnStep は Run と同じ goroutine から呼ばれる。
	tr := s.Runner.Run(r.Context(), id, req.Query, loop.Options{
		Model: s.Model, Mode: s.Mode, StrictArgs: true,
		MaxSteps: s.MaxSteps, MaxTokens: maxTok, Answer: true, StopGuard: true, IntentGate: true,
		Thinking: req.Thinking,
		OnStep:   func(st loop.Step) { send("step", toStepDTO(st)) },
	})

	if tr.Err != "" {
		send("error", map[string]any{"message": tr.Err})
	}
	if tr.Proposal != nil {
		// 提案は流すだけ。実行は人間が確認してから /api/commands/execute を叩く。
		send("proposal", tr.Proposal)
	}
	if tr.Navigate != nil {
		nav := map[string]any{
			"route":   tr.Navigate.Route,
			"filters": tr.Navigate.Filters,
			"reason":  tr.Navigate.Reason,
		}
		// 遷移するかを決めるための材料。読めなければ入れない
		// (0 を入れると「該当なし」と区別がつかない)。
		if sum, ok := s.screenSummary(r.Context(), id, tr.Navigate.Route, tr.Navigate.Filters); ok {
			nav["summary"] = sum
		}
		send("navigate", nav)
	}
	send("answer", map[string]any{"answer": tr.Answer})
	send("done", map[string]any{
		// filters は実際に適用された絞り込み条件。利用者が頼んでいない条件が
		// 付いていないかを人間が確かめられるようにするため。
		"filters":    tr.Filters,
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

// idempotencyKey は「同じ承認を 2 回実行しない」ための鍵を決める。
//
// 優先順:
//  1. Idempotency-Key ヘッダ (呼び出し側が明示した場合)
//  2. traceId + 操作 + 引数のハッシュ
//
// **traceId が無ければ鍵を作らない。** 会話が無い直接呼び出しでは、
// 二重送信と「同じ操作をもう一度やりたい」を区別できない。
// 区別できないものを勝手に止めるほうが害が大きい。
//
// 引数までハッシュに含めるのは、同じ会話で対象や値を変えた 2 回目を
// 別の操作として通すため。
func idempotencyKey(r *http.Request, req executeRequest, args map[string]any) string {
	if k := strings.TrimSpace(r.Header.Get("Idempotency-Key")); k != "" {
		return k
	}
	if req.TraceID == "" {
		return ""
	}
	// map の JSON 化は Go ではキー順が安定する (encoding/json はキーをソートする)。
	canon, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(req.TraceID+"\x00"+req.Command+"\x00"), canon...))
	return hex.EncodeToString(sum[:])
}

// readBefore は更新直前の対象の状態を読む。
//
// 読み取り先はコマンド定義の before で宣言する (推測しない)。
// 実行ユーザーの権限で読むので、**その人が見られない情報は監査にも残らない**。
// 監査だけ権限を越えて読むと、監査ログが情報漏洩の経路になる。
func (s *Server) readBefore(ctx context.Context, id authctx.Identity,
	cmd command.Command, args map[string]any) any {

	if cmd.Before == nil {
		return nil
	}
	base := s.baseURL(cmd.Service)
	if base == "" {
		return nil
	}
	path := cmd.Before.Path
	for k, v := range args {
		path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(fmt.Sprint(v)))
	}
	if strings.Contains(path, "{") {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, cmd.Before.Method, base+path, nil)
	if err != nil {
		return nil
	}
	id.Apply(req)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		s.Log.Warn("更新前の状態を読めなかった", "command", cmd.Name, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.Log.Warn("更新前の状態を読めなかった", "command", cmd.Name, "status", resp.StatusCode)
		return nil
	}
	var out any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	return unwrapEnvelope(out)
}

// unwrapEnvelope は API の封筒 (data / meta / _links) を剥がす。
//
// モックサービスは実 API らしく冗長な応答を返す。監査に必要なのは
// **業務データの中身**であって、その場限りの trace_id や _links ではない。
// 残す量が増えるほど、後から読むときに変化を見つけにくくなる。
func unwrapEnvelope(v any) any {
	obj, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if inner, ok := obj["data"]; ok {
		return inner
	}
	out := make(map[string]any, len(obj))
	for k, val := range obj {
		if k == "meta" || k == "_links" {
			continue
		}
		out[k] = val
	}
	return out
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

	// ここまでの拒否は「実行していない」ので、枠は取らずに記録するだけでよい。
	// 実際にサービスを叩く手前で枠を確保する。
	//
	// **サービス側の業務ルールに二重実行の防止を任せない。**
	// order.cancel は 2 回目を 409 で弾くが (状態が変わるため)、
	// customer.updateContact は 2 回とも 200 を返す (実測)。
	// 弾けるかどうかが操作ごとに違うのは、防いでいるとは言えない。
	key := idempotencyKey(r, req, args)
	claimed, prev, execID, err := s.Audit.Claim(r.Context(), key, audit.Execution{
		TraceID: req.TraceID, Identity: id, Command: cmd.Name, Arguments: args,
	})
	if err != nil {
		// 枠が取れないなら実行しない。記録できないまま更新を通すと、
		// 二重実行を防ぐ根拠そのものが無くなる。
		writeErr(w, http.StatusServiceUnavailable, "監査記録に書き込めないため実行を中止しました")
		return
	}
	if !claimed {
		s.Log.Info("execute skipped (duplicate)", "user", id.UserID, "command", cmd.Name)
		if prev != nil && prev.StatusCode == 0 {
			writeErr(w, http.StatusConflict, "この操作は現在実行中です。しばらく待ってから確認してください。")
			return
		}
		if prev != nil && prev.StatusCode != http.StatusOK {
			// 前回が失敗しているなら、その理由をそのまま返す。
			writeErr(w, prev.StatusCode, prev.Error)
			return
		}
		// 前回成功している。もう一度実行はせず、済んでいることを伝える。
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "command": cmd.Name, "alreadyExecuted": true,
		})
		return
	}

	base := s.baseURL(cmd.Service)
	if base == "" {
		writeErr(w, http.StatusInternalServerError, "サービスの接続先が不明です")
		return
	}

	// 更新前の状態を読む。**失敗しても実行は止めない。**
	// 監査を厚くするための読み取りであって、更新の前提条件ではない。
	// ここで止めると「記録を良くしたら操作ができなくなった」ことになる。
	before := s.readBefore(r.Context(), id, cmd, args)

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
		s.Audit.Complete(r.Context(), execID, audit.Execution{
			TraceID: req.TraceID, Identity: id, Command: cmd.Name, Arguments: args,
			StatusCode: http.StatusBadRequest, Error: "必須のパスパラメータが不足しています",
		})
		writeErr(w, http.StatusBadRequest, "必須のパスパラメータが不足しています")
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
		msg := fmt.Sprintf("%s サービスへ接続できません", cmd.Service)
		s.Audit.Complete(r.Context(), execID, audit.Execution{
			TraceID: req.TraceID, Identity: id, Command: cmd.Name, Arguments: args,
			StatusCode: http.StatusBadGateway, Error: msg,
		})
		writeErr(w, http.StatusBadGateway, msg)
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
		s.Audit.Complete(r.Context(), execID, audit.Execution{
			TraceID: req.TraceID, Identity: id, Command: cmd.Name,
			Arguments: args, StatusCode: resp.StatusCode, Error: msg, Before: before,
		})
		writeErr(w, resp.StatusCode, msg)
		return
	}
	var after any
	_ = json.Unmarshal(raw, &after)
	after = unwrapEnvelope(after)
	s.Audit.Complete(r.Context(), execID, audit.Execution{
		TraceID: req.TraceID, Identity: id, Command: cmd.Name,
		Arguments: args, StatusCode: http.StatusOK, Before: before, Result: after,
	})
	// 何がどう変わったかを返す。「実行しました」だけでは、
	// 承認した内容がそのまま反映されたのかを画面で確かめられない。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "command": cmd.Name, "changes": changedFields(before, after),
	})
}

// changedFields は更新前後で値が変わった項目を返す。
//
// 更新のたびに動く管理項目 (version / updated_at など) は除く。
// 混ぜると「何を変えたか」が埋もれる。
func changedFields(before, after any) []map[string]any {
	b, ok1 := before.(map[string]any)
	a, ok2 := after.(map[string]any)
	if !ok1 || !ok2 {
		return nil
	}
	noise := map[string]bool{
		"version": true, "updated_at": true, "updated_by": true, "_etag": true,
	}
	out := []map[string]any{}
	for _, k := range sortedKeys(a) {
		if noise[k] {
			continue
		}
		if fmt.Sprint(b[k]) == fmt.Sprint(a[k]) {
			continue
		}
		out = append(out, map[string]any{"field": k, "before": b[k], "after": a[k]})
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// pageParams は画面からのページ指定と並べ替えをサービスへそのまま渡す。
//
// 上限や既定値、並べ替えられる列の判断はサービス側 (svc.Pg / svc.OrderBy) が持つ。
// BFF が独自の上限や許可リストを持つと二重管理になってずれる。
func pageParams(r *http.Request, q url.Values) {
	for _, k := range []string{"limit", "offset", "sort"} {
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

// previewRows は画面遷移の要約に載せる行数。
//
// 「どんなデータがあるか」が分かればよいので少なくてよい。
// 増やすほど画面と同じものを 2 か所で描くことになり、
// 遷移するかどうかの判断材料という役割から離れる。
const previewRows = 5

// screenSummary は画面 (route + filters) の要約を返す。件数と先頭の数行。
//
// 画面遷移は Tool を 1 つも実行しないので、そのままではアシスタント画面に
// 「どの画面を開くか」しか出せない。**それだけではボタンを 1 つ増やしただけ**に
// なるので、遷移せずに済むだけの中身をここで足す。
//
// **要約はサービスの応答から機械的に作る。** LLM に書かせると、
// 遷移経路の速さ (Tool 実行なしで 2.6 秒) を失ううえ、
// 見ていないものを書く余地を増やすことになる。
//
// 実行ユーザーの権限で読む。見えない範囲を教えると、
// 画面には出ない情報を要約として漏らすことになる。
// 読めなければ何も返さず、**黙って 0 件と見せない**。
func (s *Server) screenSummary(ctx context.Context, id authctx.Identity,
	route string, filters map[string]string) (map[string]any, bool) {

	q := url.Values{}
	for k, v := range filters {
		if v != "" {
			q.Set(k, v)
		}
	}
	q.Set("limit", strconv.Itoa(previewRows))

	switch route {
	case "/orders":
		ids, ok := s.resolveCustomerIDs(ctx, id, q.Get("customer_name"))
		if !ok {
			return nil, false
		}
		q.Del("customer_name")
		for _, cid := range ids {
			q.Add("customer_ids", cid)
		}
		p, _, err := s.fetchPage(ctx, id, "order", "/orders", q)
		if err != nil {
			return nil, false
		}
		names := s.customerNames(ctx, id, p.Items)
		rows := make([]map[string]any, 0, len(p.Items))
		for _, o := range p.Items {
			cid := str(o["customer_id"])
			rows = append(rows, map[string]any{
				"key":      str(o["order_id"]),
				"title":    nameOr(names[cid], cid),
				"detail":   str(o["ordered_at"]) + " / " + str(o["status"]),
				"trailing": num(o["total_amount"]),
			})
		}
		return map[string]any{"count": p.Count, "rows": rows, "unit": "件"}, true
	case "/customers":
		p, _, err := s.fetchPage(ctx, id, "customer", "/customers", q)
		if err != nil {
			return nil, false
		}
		rows := make([]map[string]any, 0, len(p.Items))
		for _, c := range p.Items {
			rows = append(rows, map[string]any{
				"key":    str(c["customer_id"]),
				"title":  str(c["name"]),
				"detail": str(c["region"]) + " / " + str(c["status"]),
			})
		}
		return map[string]any{"count": p.Count, "rows": rows, "unit": "件"}, true
	}
	return nil, false
}

// nameOr は氏名が引けなかったときに ID で代替する。
// 空欄にすると「名前の無い顧客」に見える。
func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

// resolveCustomerIDs は氏名を顧客 ID の集合へ解決する。
// 氏名の指定が無ければ空の集合を返す (絞り込まない)。
// 絞り込めない (候補が多すぎる / 読めない) ときは ok=false。
func (s *Server) resolveCustomerIDs(ctx context.Context, id authctx.Identity,
	name string) ([]string, bool) {

	if name == "" {
		return nil, true
	}
	q := url.Values{}
	q.Set("name", name)
	q.Set("limit", strconv.Itoa(maxResolve))
	cust, _, err := s.fetchPage(ctx, id, "customer", "/customers", q)
	if err != nil || cust.HasMore {
		return nil, false
	}
	out := make([]string, 0, len(cust.Items))
	for _, c := range cust.Items {
		if cid, ok := c["customer_id"].(string); ok {
			out = append(out, cid)
		}
	}
	return out, true
}
