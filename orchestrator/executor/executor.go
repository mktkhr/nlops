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

	// GuardAmbiguousReads が true のとき、候補が複数ある一覧から拾っただけの
	// ID を引数に使う読み取りも差し戻す。
	//
	// 「高橋さんの一番古い注文」で 251 人の高橋から 1 人を黙って選び、
	// 2 年 7 か月ずれた答えを断定形で返す失敗を実測したため。
	// 更新と違って画面で気づけると考えていたが、**文章で返る場合は気づけない**。
	GuardAmbiguousReads bool

	// ambiguousIDs は「複数候補の 1 つとして現れただけ」の ID と、その候補件数。
	//
	// 顧客が 6 件しか無かった頃は「山田さん」が 0〜1 件しか当たらず、
	// この区別は不要だった。5,000 件では 250 件当たる。その先頭を
	// 勝手に選んで更新提案を作られると、承認画面には 1 件しか出ないため
	// **他に 249 件候補があったという事実が承認者から消える。**
	// 読み取りなら誤りは画面で気づけるが、更新は取り返しがつかない。
	ambiguousIDs map[string]int

	// query は利用者の入力そのもの。絞り込みの根拠が本当に入力にあったかを
	// 文字列として照合するために保持する。
	query string

	// queryIDs は利用者自身が入力に書いた語。
	// 「C005 のメールを更新して」の C005 は、後で広い一覧に出てきても曖昧ではない。
	queryIDs map[string]bool
}

// New は Executor を作る。
func New(cat *toolschema.Catalog) *Executor {
	return &Executor{
		Catalog:             cat,
		HTTP:                &http.Client{Timeout: 15 * time.Second},
		GuardUnresolvedIDs:  true,
		seenIDs:             map[string]bool{},
		GuardAmbiguousReads: true,
		ambiguousIDs:        map[string]int{},
		queryIDs:            map[string]bool{},
	}
}

// Reset は 1 会話分の状態を初期化する。query に含まれる語は既知として扱う。
func (e *Executor) Reset(query string) {
	e.seenIDs = map[string]bool{}
	e.ambiguousIDs = map[string]int{}
	e.queryIDs = map[string]bool{}
	e.query = query
	for _, tok := range tokenPattern.FindAllString(query, -1) {
		e.seenIDs[strings.ToUpper(tok)] = true
		e.queryIDs[strings.ToUpper(tok)] = true
	}
}

var (
	tokenPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)
	idParamName  = regexp.MustCompile(`(^|_)id$`)
	namePattern  = regexp.MustCompile(`(^|_)name$`)
)

// ErrUnresolvedID は未解決 ID を差し戻すときのメッセージ。
const ErrUnresolvedID = "unresolved_id"

// ErrAmbiguousID は候補を絞り込めていない ID を差し戻すときのメッセージ。
const ErrAmbiguousID = "ambiguous_id"

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

	// 候補を絞り込めていない ID での読み取りを止める。
	//
	// 利用者は「高橋さん」としか言っていないのに、251 人の中から 1 人を
	// 選んで答えると、選んだこと自体が回答から消える。
	// 選ばせないのではなく、**選んだまま答えに進ませない**。
	if e.GuardAmbiguousReads {
		if bad := e.AmbiguousIDs(args, enumParams(tool)); len(bad) > 0 {
			r.Error = fmt.Sprintf("%s: 引数 %s。利用者はどれを指すか特定していません。"+
				"検索条件を狭めて 1 件に絞り込むか、候補が複数あることを利用者に伝えて finish してください。",
				ErrAmbiguousID, strings.Join(bad, ", "))
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
	// 並べ替えを明示した検索の先頭行は「勝手に選んだ 1 件」ではない。
	// 利用者が「一番古い」と言い、その順で並べて先頭を取ったのなら、
	// それは一意に定まる答え。2 行目以降は依然として恣意的な選択になる。
	_, sorted := args["sort"].(string)
	e.markAmbiguity(r.Projected, sorted)
	return r
}

// markAmbiguity は一覧結果の絞り込み具合を ID ごとに覚える。
//
// 候補が 2 件以上あった一覧に出てきた ID は「曖昧」として印を付ける。
// 逆に 1 件に絞り込めた検索の結果は、以前に曖昧だった ID であっても
// 特定できたとみなして印を外す (「山田」→ 250 件 → 「山田太郎」→ 1 件)。
func (e *Executor) markAmbiguity(v any, sortedTop bool) {
	obj, ok := v.(map[string]any)
	if !ok {
		return
	}
	items, ok := obj["items"].([]any)
	if !ok {
		return
	}
	// count は Projection を通った後なので Go の int で入っている。
	// JSON 由来の float64 だけを見ていると、251 件を 10 件 (=見えている行数)
	// と誤って覚え、差し戻しメッセージが嘘の件数を伝える。
	total := len(items)
	if n, ok := asInt(obj["count"]); ok && n > total {
		total = n
	}
	for i, it := range items {
		row, ok := it.(map[string]any)
		if !ok {
			continue
		}
		// 並べ替えを指定した検索の先頭行は一意に定まる。
		// 利用者が行の名称をそのまま書いていた場合も、モデルが候補から
		// 選び取ったのではなく**名指しされた**ので一意に定まる。
		determinate := (sortedTop && i == 0) || e.namedInQuery(row)
		for k, val := range row {
			s, ok := val.(string)
			if !ok || s == "" || !idParamName.MatchString(k) {
				continue
			}
			if total > 1 && !determinate && !e.queryIDs[strings.ToUpper(s)] {
				// 件数も覚える。差し戻すときに「251 件のうちの 1 件」と
				// 言えないと、モデルは何を直せばよいか分からない。
				e.ambiguousIDs[strings.ToUpper(s)] = total
			} else {
				delete(e.ambiguousIDs, strings.ToUpper(s))
			}
		}
	}
}

// namedInQuery は行の名称が利用者の入力にそのまま含まれるかを見る。
//
// queryIDs は `[A-Za-z][A-Za-z0-9_-]{2,}` で作るので **ASCII の ID しか拾えない。**
// 「東京倉庫の P001 の在庫数は」と書かれても、listWarehouses が返す
// WH_TOKYO とは結び付かず、倉庫が 2 件あるだけで「2 件のうちの 1 件」と
// 差し戻していた。モデルは正しく解決したのに進めなくなり、
// 同じ呼び出しを繰り返して step 超過で終わる (実測: Qwen3.8 の W23/X15/B17)。
//
// **包含の向きが重要。** 「入力が行の名称を含む」ときだけ一意とする。
// 逆向き (行の名称が入力の語を含む) にすると、
// 「ワイヤレスマウス」で 51 件ヒットする商品すべてが一意になってしまう。
// 名称がちょうど「ワイヤレスマウス」の 1 件だけが名指しであり、
// 「ワイヤレスマウス Lite 1010」は名指しではない。
func (e *Executor) namedInQuery(row map[string]any) bool {
	if e.query == "" {
		return false
	}
	for k, v := range row {
		if !namePattern.MatchString(k) {
			continue
		}
		s, ok := v.(string)
		// 1 文字の名称は偶然一致しやすいので採らない。
		if !ok || len([]rune(s)) < 2 {
			continue
		}
		if strings.Contains(e.query, s) {
			return true
		}
	}
	return false
}

// asInt は count のような数値を int で取り出す。
// Projection 前は JSON 由来の float64、通った後は Go の int になる。
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return int(x), true
	}
	return 0, false
}

// AmbiguousIDs は「候補が複数あった一覧から拾っただけ」の引数を
// "customer_id (251 件の候補)" の形で返す。
func (e *Executor) AmbiguousIDs(args map[string]any, skip map[string]bool) []string {
	if !e.GuardUnresolvedIDs {
		return nil
	}
	var bad []string
	for _, k := range toolschema.SortedKeys(args) {
		if !idParamName.MatchString(k) || skip[k] {
			continue
		}
		s, ok := args[k].(string)
		if !ok || s == "" {
			continue
		}
		if n := e.ambiguousIDs[strings.ToUpper(s)]; n > 1 {
			bad = append(bad, fmt.Sprintf("%s (%s は %d 件の候補のうちの 1 件)", k, s, n))
		}
	}
	return bad
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
	// サービスがページングしている場合、count は総件数であって
	// 返ってきた行数ではない。ここで len に置き換えると LLM が
	// 「何件ありますか」にページサイズを答えてしまう。
	total := len(rawList)
	if n, ok := obj["count"].(float64); ok && int(n) >= total {
		total = int(n)
	}
	limit := len(rawList)
	if limit > p.MaxItems {
		limit = p.MaxItems
	}
	items := make([]any, 0, limit)
	for _, it := range rawList[:limit] {
		items = append(items, pickFields(it, p.Fields))
	}
	out := map[string]any{"items": items, "count": total}
	if total > len(items) {
		out["truncated"] = true
		out["shown"] = len(items)
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
