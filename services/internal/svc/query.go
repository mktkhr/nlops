package svc

import (
	"encoding/base64"
	"encoding/json"
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

// Sort は 1 つの並べ替え指定。
//
// Type を持たせているのは keyset ページングのため。cursor に入れた値は
// JSON を経由するので文字列や float になる。列の型が分からないと
// `date > text` のような比較を組み立ててしまう。
type Sort struct {
	Col  string // 列 (または式)
	Desc bool
	Type string // cursor 値を比較するときにキャストする型 ("date" / "int" など)
}

// Sortable は並べ替えを許す指定。キーが API から渡される値。
//
// **向きを列と分けない。** sort と order を別々の引数にすると
// 「sort だけ指定された」状態が生まれ、向きが呼び出し側の想定と食い違う
// (実測: モデルが sort=total_amount だけを指定してきた)。
// 1 つの enum に畳んで、曖昧な状態そのものを作れなくする。
//
// SQL 式を呼び出し側リテラルだけに限るための型でもある。
// 受け取った文字列をそのまま ORDER BY へ入れてはいけない。
type Sortable map[string]Sort

// Order は組み立て済みの並び順。ページングと共有するために構造を保つ
// (文字列にしてしまうと keyset の比較式を作れない)。
type Order struct {
	Sort     Sort
	Tiebreak string // 主キー。同値の並びを決める
	TieType  string
}

// SQL は ORDER BY 句を返す。
func (o Order) SQL() string {
	dir := "ASC"
	if o.Sort.Desc {
		dir = "DESC"
	}
	return fmt.Sprintf("ORDER BY %s %s, %s %s", o.Sort.Col, dir, o.Tiebreak, dir)
}

// OrderBy はクエリパラメータから並び順を決める。
//
//   - 許可リストに無い sort は既定へ落とす。LLM 側は enum (GBNF) で
//     生成できないようにしてあるが、API は直接叩ける。ここが最後の砦。
//   - **必ず主キーで tiebreak する。** 同値の並びが不定だと、ページ送りで
//     同じ行が 2 回出たり、どのページにも出なかったりする。
//     並べ替えを入れる以上、これが無いとページ送りが壊れる。
func OrderBy(r *http.Request, allowed Sortable, def Sort, tiebreak, tieType string) Order {
	sort, ok := allowed[Q(r, "sort")]
	if !ok {
		sort = def
	}
	return Order{Sort: sort, Tiebreak: tiebreak, TieType: tieType}
}

// Keyset は cursor より後ろ (降順なら前) の行に絞る条件を返す。
//
// 行値比較 `(a, b) > (x, y)` は `ORDER BY a, b` とちょうど対応する。
// **向きが列と tiebreak で揃っていることが前提**で、これは Order が
// 両方に同じ向きを使っているから成り立つ。
func (o Order) Keyset(w *W, c *Cursor) {
	if c == nil {
		return
	}
	op := ">"
	if o.Sort.Desc {
		op = "<"
	}
	w.args = append(w.args, c.Value, c.ID)
	w.conds = append(w.conds, fmt.Sprintf("(%s, %s) %s ($%d::%s, $%d::%s)",
		o.Sort.Col, o.Tiebreak, op, len(w.args)-1, o.Sort.Type, len(w.args), o.TieType))
}

// Cursor は「どこまで返したか」を指す。
//
// offset と違い、読み飛ばしが要らないので深いページでも一定時間で返る。
// 代わりに**任意のページへ飛べない**ので、画面のページ番号には使えない。
// 用途が違うので offset と併存させる。
type Cursor struct {
	Sort  string `json:"s"` // どの並び順で作った cursor か
	Value any    `json:"v"` // 並べ替え列の値
	ID    any    `json:"i"` // 主キーの値
}

// Encode は URL に載る形にする。
func (c Cursor) Encode() string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor は cursor を復元する。
//
// **並び順が変わっていたら無効として捨てる。** 受注日順で作った cursor を
// 金額順のクエリに渡されると、飛ばす行と返す行が噛み合わず、
// 静かに歯抜けの結果を返すことになる。
func DecodeCursor(raw, sort string) *Cursor {
	if raw == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	var c Cursor
	if json.Unmarshal(b, &c) != nil || c.Sort != sort {
		return nil
	}
	return &c
}

// joinWhere は既存の "FROM ... WHERE ..." に条件を足す。
// W が持つ条件を、元の句が WHERE を含むかどうかで繋ぎ分ける。
func joinWhere(fromWhere string, w *W) string {
	if len(w.conds) == 0 {
		return fromWhere
	}
	glue := " WHERE "
	if strings.Contains(strings.ToUpper(fromWhere), " WHERE ") {
		glue = " AND "
	}
	return fromWhere + glue + strings.Join(w.conds, " AND ")
}
