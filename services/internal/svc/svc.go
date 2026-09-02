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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/dbconf"
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
		case errors.Is(err, ErrForbidden):
			writeErr(w, http.StatusForbidden, err.Error())
		case errors.Is(err, ErrConflict):
			writeErr(w, http.StatusConflict, err.Error())
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

// ErrConflict は業務ルール上その操作ができないことを表す。
// 「キャンセルできない状態の注文」など、判断はサービス側の責務。
var ErrConflict = errors.New("この操作は許可されていません")

// ErrForbidden は権限が足りないことを表す。
var ErrForbidden = errors.New("権限がありません")

// Conflict は ErrConflict をメッセージ付きで返す。
func Conflict(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, a...))
}

// Forbidden は ErrForbidden をメッセージ付きで返す。
func Forbidden(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, a...))
}

// Body はリクエストボディを JSON として読む。
func Body(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("リクエストボディを解釈できません: %w", err)
	}
	return nil
}

// RequireWrite は更新権限を確かめる。
func RequireWrite(id authctx.Identity, service string) error {
	if !id.CanWrite(service) {
		return Forbidden("ロール %s は %s を更新できません", id.Role, service)
	}
	return nil
}

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

// DSN は接続文字列を返す。解決の順序は pkg/dbconf を参照。
func DSN() string { return dbconf.DSN() }

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

// DefaultLimit / MaxLimit は一覧 API が 1 回で返す行数の上限。
//
// これが無いと 5 万件の注文をまるごと返してしまう (実測 6.98MB)。
// LLM へ渡すのは Projection がさらに絞るが、そこへ至る前の転送量と
// メモリを抑えるのはサービス側の責務。
const (
	DefaultLimit = 100
	MaxLimit     = 1000

	// MaxOffset は offset の上限。
	//
	// offset は指定分の行を読み飛ばしてから返すので、深いページほど遅くなる。
	// 際限なく受け付けると 1 リクエストで DB を舐めさせられるため上限を置く。
	// これを超える範囲を見たい要求は、ページ送りではなく絞り込みで解く。
	MaxOffset = 100000
)

// Page は 1 ページ分の指定。
//
// **LLM にはこの概念を見せない。** Tool の引数に offset を出すと、
// 「未払いの請求書を全部見せて」に対して 108 ページ分の往復を始めてしまう。
// LLM には総件数と先頭 N 件と truncated を渡し、足りなければ
// 絞り込ませる。ページ送りは人間が画面で行う操作。
type Page struct {
	Limit  int
	Offset int
	Cursor string // keyset ページング。offset より優先する

	// NoCount が true のとき総件数を数えない。
	//
	// count は絞り込みの有無に関わらず全走査で、**ページごとに毎回払う**。
	// cursor で順に辿るときは総件数が要らないことが多いので外せるようにする。
	// 既定は数える (「何件ありますか」に答えられなくなるほうが困る)。
	NoCount bool
}

// Pg はクエリパラメータからページ指定を読む。
func Pg(r *http.Request) Page {
	n := QInt(r, "limit", DefaultLimit)
	if n <= 0 {
		n = DefaultLimit
	}
	if n > MaxLimit {
		n = MaxLimit
	}
	off := QInt(r, "offset", 0)
	if off < 0 {
		off = 0
	}
	if off > MaxOffset {
		off = MaxOffset
	}
	return Page{Limit: n, Offset: off, Cursor: Q(r, "cursor"), NoCount: Q(r, "count") == "none"}
}

// ListPage は件数を数えたうえで 1 ページ分だけ返す。
//
// count には**該当する総件数**を入れる。返した行数ではない。
// ここを取り違えると「何件ありますか」に対して LLM がページサイズを
// 答えてしまう。returned と has_more で実際に返した量を伝える。
func ListPage(ctx context.Context, pool *pgxpool.Pool, entity,
	selectCols, fromWhere string, order Order, pg Page, args ...any) (map[string]any, error) {

	// count は絞り込みの有無に関わらず全走査になる (実測: 5 万行で 464 buffers)。
	// **ページごとに毎回払う**ので、総件数を返す設計はここにコストを固定している。
	total := -1 // -1 は「数えていない」
	if !pg.NoCount {
		if err := pool.QueryRow(ctx, "SELECT count(*) "+fromWhere, args...).Scan(&total); err != nil {
			return nil, fmt.Errorf("件数取得: %w", err)
		}
	}

	keyset := false
	offset := pg.Offset
	sql := fmt.Sprintf("SELECT %s %s %s LIMIT %d OFFSET %d",
		selectCols, fromWhere, order.SQL(), pg.Limit, offset)

	if cur := DecodeCursor(pg.Cursor, sortKeyOf(order)); cur != nil {
		// cursor があるときは読み飛ばさない。ここが offset との違い。
		// offset は無視する (両方指定されたら cursor を優先する)。
		kw := &W{args: append([]any{}, args...)}
		order.Keyset(kw, cur)
		sql = fmt.Sprintf("SELECT %s %s %s LIMIT %d",
			selectCols, joinWhere(fromWhere, kw), order.SQL(), pg.Limit)
		args = kw.Args()
		offset = 0
		keyset = true
	}

	rows, err := Rows(ctx, pool, sql, args...)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i] = Enrich(entity, rows[i])
	}
	// has_more は「この後にまだ行があるか」。総件数と返却数の比較では、
	// 2 ページ目以降で常に true になってしまう。
	hasMore := offset+len(rows) < total
	if pg.NoCount {
		// 総件数が無いので「ちょうど 1 ページ分返ったか」で判断するしかない。
		hasMore = len(rows) == pg.Limit
	}
	if keyset {
		// cursor では「何件目まで来たか」が分からないので、
		// 1 ページ分ちょうど返ったかどうかで判断する。
		hasMore = len(rows) == pg.Limit
	}
	out := map[string]any{
		"items":    rows,
		"returned": len(rows),
		"offset":   offset,
		"has_more": hasMore,
		"limit":    pg.Limit,
		"meta":     pageMeta(total, pg, len(rows)),
	}
	// count は「数えていない」ことを表せないので、数えたときだけ入れる。
	// 0 を入れると「該当なし」と区別がつかない。
	if total >= 0 {
		out["count"] = total
	}
	if hasMore && len(rows) > 0 {
		out["next_cursor"] = nextCursor(order, rows[len(rows)-1])
	}
	return out, nil
}

// sortKeyOf は cursor の照合に使う並び順の識別子。
// 並び順が変わった cursor を無効にするために使う。
func sortKeyOf(o Order) string {
	if o.Sort.Desc {
		return o.Sort.Col + "_desc"
	}
	return o.Sort.Col + "_asc"
}

// nextCursor は最後の行から次の cursor を作る。
func nextCursor(o Order, last map[string]any) string {
	return Cursor{
		Sort:  sortKeyOf(o),
		Value: last[colName(o.Sort.Col)],
		ID:    last[colName(o.Tiebreak)],
	}.Encode()
}

// colName は "s.quantity" のような修飾を落として結果の列名に合わせる。
func colName(expr string) string {
	if i := strings.LastIndex(expr, "."); i >= 0 {
		return expr[i+1:]
	}
	return expr
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

// pageMeta はページングした一覧用の meta。
// responseMeta は has_next を常に false で返すので、ページングした
// 応答にそのまま使うと「続きは無い」と嘘をつくことになる。
func pageMeta(total int, pg Page, returned int) map[string]any {
	m := responseMeta(total)
	m["per_page"] = pg.Limit
	m["page"] = pg.Offset/max(pg.Limit, 1) + 1
	m["has_next"] = pg.Offset+returned < total
	return m
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

// QList は同じキーで繰り返し指定されたクエリパラメータを返す。
// 空要素は落とし、暴走を防ぐため上限を設ける。
func QList(r *http.Request, key string, max int) []string {
	vs := r.URL.Query()[key]
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}
