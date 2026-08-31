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
	"time"

	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

// Server は BFF。
type Server struct {
	Runner  *loop.Runner
	Dir     *authctx.Directory
	Catalog *toolschema.Catalog
	Log     *slog.Logger
	HTTP    *http.Client

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
	send("start", map[string]any{"query": req.Query, "user": id.UserID, "model": s.Model})

	// Tool Loop は 1 要求あたり数秒かかる。何も返さないと壊れて見えるので、
	// ステップ完了ごとに逐次流す。OnStep は Run と同じ goroutine から呼ばれる。
	tr := s.Runner.Run(r.Context(), id, req.Query, loop.Options{
		Model: s.Model, Mode: s.Mode, StrictArgs: true,
		MaxSteps: s.MaxSteps, MaxTokens: 512, Answer: true, StopGuard: true,
		OnStep: func(st loop.Step) { send("step", toStepDTO(st)) },
	})

	if tr.Err != "" {
		send("error", map[string]any{"message": tr.Err})
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
	})
	s.Log.Info("ask", "user", id.UserID, "steps", len(tr.Steps), "ms", int(tr.TotalMS))
}

type stepDTO struct {
	Iteration int            `json:"iteration"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Finish    bool           `json:"finish,omitempty"`
	Forced    bool           `json:"forced,omitempty"`
	Status    int            `json:"status,omitempty"`
	Denied    bool           `json:"denied,omitempty"`
	Error     string         `json:"error,omitempty"`
	Result    any            `json:"result,omitempty"`
	LLMms     float64        `json:"llmMs"`
}

func toStepDTO(st loop.Step) stepDTO {
	d := stepDTO{
		Iteration: st.Iteration, Tool: st.Tool, Arguments: st.Arguments,
		Finish: st.Finish, Forced: st.Forced, LLMms: st.LLMms,
	}
	if st.Result != nil {
		d.Status = st.Result.Status
		d.Denied = st.Result.Denied
		d.Error = st.Result.Error
		d.Result = st.Result.Projected // Projection 済みのものだけを返す
	}
	return d
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
	orders, code, err := s.fetchList(r.Context(), id, "order", "/orders", q)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}

	// 顧客名は customer サービスが持つ。必要な分だけ引く。
	names := map[string]string{}
	if customers, _, err := s.fetchList(r.Context(), id, "customer", "/customers", nil); err == nil {
		for _, c := range customers {
			if cid, ok := c["customer_id"].(string); ok {
				names[cid], _ = c["name"].(string)
			}
		}
	}

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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
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
	rows, code, err := s.fetchList(r.Context(), id, "customer", "/customers", q)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// fetchList は Microservice の一覧 API を呼ぶ。認証情報の付与はここで行う。
func (s *Server) fetchList(ctx context.Context, id authctx.Identity,
	service, path string, q url.Values) ([]map[string]any, int, error) {

	base := s.baseURL(service)
	if base == "" {
		return nil, http.StatusInternalServerError, fmt.Errorf("サービス %s の接続先が不明です", service)
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	id.Apply(req)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("%s サービスへ接続できません", service)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		return nil, http.StatusForbidden, fmt.Errorf("%s を参照する権限がありません", service)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("%s サービスが %d を返しました", service, resp.StatusCode)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("%s サービスの応答を解釈できません", service)
	}
	return out.Items, http.StatusOK, nil
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
