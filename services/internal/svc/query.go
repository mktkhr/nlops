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
