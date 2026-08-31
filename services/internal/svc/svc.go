// Package svc はモックサービス共通の骨組み。
//
// 各サービスは自分の schema のみを参照し、authctx の identity に基づいて
// 絞り込むか 403 を返す。この 2 点をフレームワーク側で強制する。
package svc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mktkhr/nlops/pkg/authctx"
)

// Server は 1 つのモックサービス。
type Server struct {
	Name string
	Pool *pgxpool.Pool
	Log  *slog.Logger
	mux  *http.ServeMux
}

// Handler は認証済みリクエストを処理する。
type Handler func(ctx context.Context, id authctx.Identity, r *http.Request) (any, error)

// New はサービスを作る。
func New(ctx context.Context, name, dsn string) (*Server, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("DB 接続: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("DB 疎通: %w", err)
	}
	return &Server{
		Name: name,
		Pool: pool,
		Log:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", name),
		mux:  http.NewServeMux(),
	}, nil
}

// Handle はルートを登録する。認証情報の取り出しとサービス単位のアクセス判定は
// ここで一括して行うので、個々のハンドラは絞り込みだけに集中する。
func (s *Server) Handle(pattern string, h Handler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id, err := authctx.FromRequest(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if id.AccessTo(s.Name) == authctx.AccessDeny {
			s.Log.Info("denied", "user", id.UserID, "role", id.Role, "path", r.URL.Path)
			writeErr(w, http.StatusForbidden,
				fmt.Sprintf("ロール %s は %s サービスを参照できません", id.Role, s.Name))
			return
		}
		out, err := h(r.Context(), id, r)
		switch {
		case errors.Is(err, ErrNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case err != nil:
			s.Log.Error("handler", "path", r.URL.Path, "err", err)
			writeErr(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusOK, out)
		}
		s.Log.Info("req", "user", id.UserID, "path", r.URL.Path, "ms", time.Since(start).Milliseconds())
	})
}

// ErrNotFound は該当なしを表す。
var ErrNotFound = errors.New("該当するデータがありません")

// NotFound は ErrNotFound をメッセージ付きで返す。
func NotFound(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, a...))
}

// Run はサーバを起動し、シグナルで停止する。
func (s *Server) Run(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		s.Log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Pool.Close()
		return srv.Shutdown(shutCtx)
	}
}

// ---- クエリヘルパ ----

// Rows はクエリ結果を map のスライスへ変換する。
// PoC ではカラム名をそのまま JSON キーにする。
func Rows(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]map[string]any, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("クエリ: %w", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[string(f.Name)] = normalize(vals[i])
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

// Row は 1 行だけ取得する。0 行なら ErrNotFound。
func Row(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (map[string]any, error) {
	rs, err := Rows(ctx, pool, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNotFound
	}
	return rs[0], nil
}

// normalize は pgx の型を JSON で扱いやすい形へ寄せる。
func normalize(v any) any {
	switch x := v.(type) {
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format(time.RFC3339)
	case pgx.Rows:
		return nil
	}
	return v
}

// ---- リクエストヘルパ ----

// Q はクエリパラメータを返す。
func Q(r *http.Request, key string) string { return r.URL.Query().Get(key) }

// QInt はクエリパラメータを整数で返す。未指定・不正なら def。
func QInt(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// P はパスパラメータを返す。
func P(r *http.Request, key string) string { return r.PathValue(key) }

// List は一覧レスポンスの共通形。
func List(items []map[string]any) map[string]any {
	return map[string]any{"items": items, "count": len(items)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// DSN は環境変数から接続文字列を組み立てる。
func DSN() string {
	if v := os.Getenv("NLOPS_DSN"); v != "" {
		return v
	}
	return "postgres://nlops:nlops@127.0.0.1:5432/nlops?sslmode=disable"
}

// ---- 実 API の冗長性の模擬 ----
//
// Response Projection の効果は「実 API が LLM に不要なフィールドを大量に返す」
// ことが前提になっている。モックが投影先と同じフィールドしか返さないと
// Projection が素通しになり、検証にならない。
// そこで実際の業務 API が持つ冗長性 (監査カラム / HATEOAS / 内部メタ) を模擬する。

// Enrich は 1 行へ監査カラムとリンクを付与する。
func Enrich(entity string, row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	key := firstID(row)
	row["created_at"] = "2026-04-01T09:00:00+09:00"
	row["updated_at"] = "2026-08-30T18:42:11+09:00"
	row["created_by"] = "batch-import@internal"
	row["updated_by"] = "svc-" + entity + "@internal"
	row["version"] = 3
	row["deleted"] = false
	row["tenant_id"] = "tenant-0001"
	row["_etag"] = "W/\"" + entity + "-" + key + "-v3\""
	row["_links"] = map[string]any{
		"self":  "/" + entity + "/" + key,
		"audit": "/" + entity + "/" + key + "/audit-log",
	}
	return row
}

// Detail は単一オブジェクトのレスポンスを組み立てる。
func Detail(entity string, row map[string]any) map[string]any {
	if row == nil {
		return nil
	}
	return map[string]any{
		"data": Enrich(entity, row),
		"meta": responseMeta(1),
	}
}

// ListOf は一覧レスポンスを組み立てる。
func ListOf(entity string, items []map[string]any) map[string]any {
	for i := range items {
		items[i] = Enrich(entity, items[i])
	}
	out := map[string]any{"items": items, "count": len(items)}
	out["meta"] = responseMeta(len(items))
	return out
}

func responseMeta(n int) map[string]any {
	return map[string]any{
		"api_version":  "2026-05-01",
		"generated_at": "2026-08-31T00:00:00+09:00",
		"page":         1,
		"per_page":     100,
		"total":        n,
		"has_next":     false,
		"trace_id":     "00000000-0000-4000-8000-000000000000",
		"deprecations": []any{},
	}
}

func firstID(row map[string]any) string {
	for _, k := range []string{"customer_id", "order_id", "product_id", "shipment_id", "invoice_id", "contact_id", "warehouse_id"} {
		if v, ok := row[k].(string); ok {
			return v
		}
	}
	return "unknown"
}
