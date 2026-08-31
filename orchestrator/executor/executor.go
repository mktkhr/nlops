// Package executor は Tool 呼び出しを実際の HTTP リクエストへ変換して実行する。
//
// 責務の境界:
//   - LLM は Tool 名と引数だけを出す。URL・認証情報・ヘッダは一切見ない。
//   - Executor が base_url / パス展開 / 認証情報付与を行う。
//   - API レスポンスは必ず Projection を通してから LLM へ返す。
//     生レスポンスを context へ戻すと小型モデルは破綻するため、ここは迂回禁止。
package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mktkhr/nlops/pkg/authctx"
	"github.com/mktkhr/nlops/pkg/toolschema"
)

// Call は LLM が出した Tool 呼び出し。
type Call struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// Result は Tool 実行の結果。
type Result struct {
	Tool      string `json:"tool"`
	URL       string `json:"url"` // トレース用。LLM へは返さない。
	Status    int    `json:"status"`
	Denied    bool   `json:"denied"`
	Error     string `json:"error,omitempty"`
	RawBytes  int    `json:"raw_bytes"`
	ProjBytes int    `json:"proj_bytes"`
	Elapsed   string `json:"elapsed"`

	// Projected は LLM へ返す整形済みデータ。
	Projected any `json:"projected"`
}

// Executor は Tool を実行する。
type Executor struct {
	Catalog *toolschema.Catalog
	HTTP    *http.Client

	// GuardUnresolvedIDs が true のとき、直前までの Tool 結果にもユーザー入力にも
	// 出現しない ID 値を引数に使おうとしたら実行せずに差し戻す。
	// スパイクで「モデルが customer_id を捏造する」失敗を実測したため設けた防御。
	GuardUnresolvedIDs bool

	// DisableProjection が true のとき生レスポンスをそのまま LLM へ返す。
	// Projection の効果を測るための比較用スイッチであり、通常は false。
	DisableProjection bool

	seenIDs map[string]bool
}

// New は Executor を作る。
func New(cat *toolschema.Catalog) *Executor {
	return &Executor{
		Catalog:            cat,
		HTTP:               &http.Client{Timeout: 15 * time.Second},
		GuardUnresolvedIDs: true,
		seenIDs:            map[string]bool{},
	}
}

// Reset は 1 会話分の状態を初期化する。query に含まれる語は既知として扱う。
func (e *Executor) Reset(query string) {
	e.seenIDs = map[string]bool{}
	for _, tok := range tokenPattern.FindAllString(query, -1) {
		e.seenIDs[strings.ToUpper(tok)] = true
	}
}

var (
	tokenPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)
	idParamName  = regexp.MustCompile(`(^|_)id$`)
)

// ErrUnresolvedID は未解決 ID を差し戻すときのメッセージ。
const ErrUnresolvedID = "unresolved_id"

// ErrInvalidEnum は enum 外の値を差し戻すときのメッセージ。
const ErrInvalidEnum = "invalid_enum"

// Execute は 1 つの Tool 呼び出しを実行する。
func (e *Executor) Execute(ctx context.Context, id authctx.Identity, call Call) Result {
	start := time.Now()
	r := Result{Tool: call.Tool}

	tool, ok := e.Catalog.ToolByName(call.Tool)
	if !ok {
		r.Error = fmt.Sprintf("Tool %q は存在しません", call.Tool)
		return r
	}
	svcDef := e.serviceOf(tool)
	if svcDef == nil {
		r.Error = fmt.Sprintf("Tool %q のサービス定義が見つかりません", call.Tool)
		return r
	}

	args := e.sanitizeArgs(tool, call.Arguments)

	if bad := invalidEnums(tool, args); len(bad) > 0 {
		r.Error = fmt.Sprintf("%s: %s", ErrInvalidEnum, strings.Join(bad, " / "))
		r.Elapsed = time.Since(start).String()
		return r
	}

	if e.GuardUnresolvedIDs {
		if bad := e.unresolvedIDs(args, enumParams(tool)); len(bad) > 0 {
			r.Error = fmt.Sprintf("%s: 引数 %s の値は未解決です。先に検索系の Tool で ID を取得してください。",
				ErrUnresolvedID, strings.Join(bad, ", "))
			r.Elapsed = time.Since(start).String()
			return r
		}
	}

	reqURL, err := buildURL(svcDef.BaseURL, tool, args)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.URL = reqURL

	req, err := http.NewRequestWithContext(ctx, tool.HTTP.Method, reqURL, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	id.Apply(req) // 認証情報を載せるのは Executor の責務。LLM は関与しない。

	resp, err := e.HTTP.Do(req)
	if err != nil {
		r.Error = fmt.Sprintf("サービス呼び出し失敗: %v", err)
		r.Elapsed = time.Since(start).String()
		return r
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	r.Status = resp.StatusCode
	r.RawBytes = len(raw)
	r.Elapsed = time.Since(start).String()

	if resp.StatusCode == http.StatusForbidden {
		r.Denied = true
		r.Projected = map[string]any{"denied": true, "reason": errMessage(raw)}
		r.ProjBytes = jsonLen(r.Projected)
		return r
	}
	if resp.StatusCode != http.StatusOK {
		r.Projected = map[string]any{"error": errMessage(raw), "status": resp.StatusCode}
		r.ProjBytes = jsonLen(r.Projected)
		return r
	}

	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		r.Error = fmt.Sprintf("レスポンス解析: %v", err)
		return r
	}
	if e.DisableProjection {
		r.Projected = body // 比較計測用。通常運用では使わない。
	} else {
		r.Projected = project(body, tool.Projection)
	}
	r.ProjBytes = jsonLen(r.Projected)
	e.recordIDs(r.Projected)
	return r
}

func (e *Executor) serviceOf(t toolschema.Tool) *toolschema.Service {
	for i := range e.Catalog.Services {
		if e.Catalog.Services[i].Name == t.Service() {
			return &e.Catalog.Services[i]
		}
	}
	return nil
}

// sanitizeArgs はスキーマに定義されていない引数を落とす。
// LLM が勝手な引数名を出しても API へは渡さない。
func (e *Executor) sanitizeArgs(t toolschema.Tool, in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if _, ok := t.Parameters.Properties[k]; !ok {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue
		}
		if v == nil {
			continue
		}
		out[k] = v
	}
	return out
}

// invalidEnums は enum が定義された引数に候補外の値が入っているものを返す。
//
// strict スキーマ (Tool ごとの anyOf) を使えば文法レベルで防げるが、
// その文法コストは Tool 数に比例し 124 Tool で約 30% になることを実測した。
// Tool 数が増えたら loose スキーマ + この検査へ切り替えられるようにしてある。
func invalidEnums(t toolschema.Tool, args map[string]any) []string {
	var bad []string
	for _, k := range toolschema.SortedKeys(args) {
		p, ok := t.Parameters.Properties[k]
		if !ok || p == nil || len(p.Enum) == 0 {
			continue
		}
		got := fmt.Sprint(args[k])
		if slices.Contains(p.Enum, got) {
			continue
		}
		bad = append(bad, fmt.Sprintf("引数 %s の値 %q は候補外です。%s のいずれかを使ってください",
			k, got, strings.Join(p.Enum, " / ")))
	}
	return bad
}

// UnresolvedIDs は未解決の ID 引数を返す。画面遷移のフィルタや更新提案にも
// 同じ検証をかけるため公開している。
//
// skip に入れた引数は検査しない。enum で候補が固定されている引数は
// 捏造しようがないので、呼び出し側が skip に入れる。
func (e *Executor) UnresolvedIDs(args map[string]any, skip map[string]bool) []string {
	if !e.GuardUnresolvedIDs {
		return nil
	}
	return e.unresolvedIDs(args, skip)
}

// unresolvedIDs は「ID を要求する引数なのに、まだどこにも現れていない値」を返す。
func (e *Executor) unresolvedIDs(args map[string]any, skip map[string]bool) []string {
	var bad []string
	for _, k := range toolschema.SortedKeys(args) {
		if !idParamName.MatchString(k) || skip[k] {
			continue
		}
		s, ok := args[k].(string)
		if !ok || s == "" {
			continue
		}
		if !e.seenIDs[strings.ToUpper(s)] {
			bad = append(bad, k)
		}
	}
	return bad
}

// enumParams は enum で候補が固定されている引数名を返す。
// 候補外の値は文法で生成できないので、出所の検証から外してよい。
func enumParams(t toolschema.Tool) map[string]bool {
	out := map[string]bool{}
	for k, p := range t.Parameters.Properties {
		if p != nil && len(p.Enum) > 0 {
			out[k] = true
		}
	}
	return out
}

// recordIDs は Tool 結果に現れた ID 的な値を既知として記録する。
func (e *Executor) recordIDs(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && idParamName.MatchString(k) {
				e.seenIDs[strings.ToUpper(s)] = true
			}
			e.recordIDs(val)
		}
	case []any:
		for _, item := range x {
			e.recordIDs(item)
		}
	}
}

// buildURL は Tool 定義から実際の URL を組み立てる。
// パスに {name} があればパスパラメータ、残りはクエリ文字列へ回す。
func buildURL(baseURL string, t toolschema.Tool, args map[string]any) (string, error) {
	path := t.HTTP.Path
	used := map[string]bool{}
	for _, k := range toolschema.SortedKeys(args) {
		ph := "{" + k + "}"
		if strings.Contains(path, ph) {
			path = strings.ReplaceAll(path, ph, url.PathEscape(fmt.Sprint(args[k])))
			used[k] = true
		}
	}
	if i := strings.Index(path, "{"); i >= 0 {
		j := strings.Index(path[i:], "}")
		return "", fmt.Errorf("必須のパスパラメータ %s が指定されていません", path[i+1:i+j])
	}
	q := url.Values{}
	for _, k := range toolschema.SortedKeys(args) {
		if used[k] {
			continue
		}
		q.Set(k, fmt.Sprint(args[k]))
	}
	u := baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u, nil
}

// project はレスポンスを Projection 定義に従って絞り込む。
// LLM の context を守る最重要処理。
func project(body any, p toolschema.Projection) any {
	if p.DataPath != "" {
		if obj, ok := body.(map[string]any); ok {
			if inner, ok := obj[p.DataPath]; ok {
				body = inner
			}
		}
	}
	if p.ListPath == "" {
		return pickFields(body, p.Fields)
	}
	obj, ok := body.(map[string]any)
	if !ok {
		return pickFields(body, p.Fields)
	}
	rawList, _ := obj[p.ListPath].([]any)
	total := len(rawList)
	limit := total
	if limit > p.MaxItems {
		limit = p.MaxItems
	}
	items := make([]any, 0, limit)
	for _, it := range rawList[:limit] {
		items = append(items, pickFields(it, p.Fields))
	}
	out := map[string]any{"items": items, "count": total}
	if total > limit {
		out["truncated"] = true
		out["shown"] = limit
	}
	return out
}

func pickFields(v any, fields []string) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for _, f := range fields {
		if val, ok := m[f]; ok && val != nil {
			out[f] = val
		}
	}
	return out
}

func errMessage(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := m["error"].(string); ok {
			return s
		}
	}
	s := string(raw)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func jsonLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}
