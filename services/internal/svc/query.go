package svc

import (
	"fmt"
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
