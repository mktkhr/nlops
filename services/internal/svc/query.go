package svc

import (
	"fmt"
	"net/http"
	"strings"
)

// W は WHERE 句を組み立てる。値が空の条件は自動的に落とす。
type W struct {
	conds []string
	args  []any
}

// Eq は等値条件を足す。v が空文字なら何もしない。
func (w *W) Eq(col string, v any) *W {
	if s, ok := v.(string); ok && s == "" {
		return w
	}
	w.args = append(w.args, v)
	w.conds = append(w.conds, fmt.Sprintf("%s = $%d", col, len(w.args)))
	return w
}

// Like は部分一致条件を足す。v が空文字なら何もしない。
func (w *W) Like(col, v string) *W {
	if v == "" {
		return w
	}
	w.args = append(w.args, v)
	w.conds = append(w.conds, fmt.Sprintf("%s ILIKE '%%' || $%d || '%%'", col, len(w.args)))
	return w
}

// Gte は以上条件を足す。
func (w *W) Gte(col string, v any) *W {
	if s, ok := v.(string); ok && s == "" {
		return w
	}
	w.args = append(w.args, v)
	w.conds = append(w.conds, fmt.Sprintf("%s >= $%d", col, len(w.args)))
	return w
}

// Lte は以下条件を足す。
func (w *W) Lte(col string, v any) *W {
	if s, ok := v.(string); ok && s == "" {
		return w
	}
	w.args = append(w.args, v)
	w.conds = append(w.conds, fmt.Sprintf("%s <= $%d", col, len(w.args)))
	return w
}

// Raw は引数を伴わない条件を足す。呼び出し側リテラルのみに使う。
func (w *W) Raw(cond string) *W {
	w.conds = append(w.conds, cond)
	return w
}

// SQL は " WHERE ..." を返す。条件が無ければ空文字。
func (w *W) SQL() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// Args はプレースホルダに対応する引数を返す。
func (w *W) Args() []any { return w.args }

// In は複数値のいずれかに一致する条件を足す。空なら何もしない。
//
// BFF が顧客名を顧客 ID の集合へ解決してから注文サービスへ渡すために要る。
// これが無いと BFF は両方の一覧を 1 ページずつ取って
// メモリ上で突き合わせるしかなく、件数が増えた途端に嘘の結果を返す。
func (w *W) In(col string, vs []string) *W {
	if len(vs) == 0 {
		return w
	}
	ph := make([]string, 0, len(vs))
	for _, v := range vs {
		w.args = append(w.args, v)
		ph = append(ph, fmt.Sprintf("$%d", len(w.args)))
	}
	w.conds = append(w.conds, fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ", ")))
	return w
}

// Sortable は並べ替えを許す指定。
// キーが API から渡される値、値が ORDER BY に置く SQL 式 ("ordered_at DESC")。
//
// **向きを列と分けない。** sort と order を別々の引数にすると
// 「sort だけ指定された」状態が生まれ、向きが呼び出し側の想定と食い違う
// (実測: モデルが sort=total_amount だけを指定してきた)。
// 1 つの enum に畳んで、曖昧な状態そのものを作れなくする。
//
// SQL 式を呼び出し側リテラルだけに限るための型でもある。
// 受け取った文字列をそのまま ORDER BY へ入れてはいけない。
type Sortable map[string]string

// OrderBy はクエリパラメータから ORDER BY 句を組み立てる。
//
//   - 許可リストに無い sort は既定へ落とす。LLM 側は enum (GBNF) で
//     生成できないようにしてあるが、API は直接叩ける。ここが最後の砦。
//   - **必ず主キーで tiebreak する。** 同値の並びが不定だと、ページ送りで
//     同じ行が 2 回出たり、どのページにも出なかったりする。
//     並べ替えを入れる以上、これが無いとページ送りが壊れる。
func OrderBy(r *http.Request, allowed Sortable, def, tiebreak string) string {
	expr, ok := allowed[Q(r, "sort")]
	if !ok {
		expr = def
	}
	dir := "ASC"
	if strings.HasSuffix(strings.ToUpper(expr), "DESC") {
		dir = "DESC"
	}
	return fmt.Sprintf("ORDER BY %s, %s %s", expr, tiebreak, dir)
}
