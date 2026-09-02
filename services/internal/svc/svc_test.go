package svc

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPg(t *testing.T) {
	tests := []struct {
		query string
		want  Page
	}{
		{"", Page{Limit: DefaultLimit, Offset: 0}},
		{"?limit=25&offset=50", Page{Limit: 25, Offset: 50}},
		// 上限を超える指定は上限に丸める。1 リクエストで DB を舐めさせない。
		{"?limit=99999", Page{Limit: MaxLimit, Offset: 0}},
		{"?offset=999999999", Page{Limit: DefaultLimit, Offset: MaxOffset}},
		// 不正な値で 0 件や負の OFFSET を作らない。
		{"?limit=0", Page{Limit: DefaultLimit, Offset: 0}},
		{"?limit=-5&offset=-5", Page{Limit: DefaultLimit, Offset: 0}},
		{"?limit=abc", Page{Limit: DefaultLimit, Offset: 0}},
	}
	for _, tt := range tests {
		got := Pg(httptest.NewRequest("GET", "/orders"+tt.query, nil))
		if got != tt.want {
			t.Errorf("Pg(%q) = %+v, 期待 %+v", tt.query, got, tt.want)
		}
	}
}

func TestPageMetaHasNext(t *testing.T) {
	// has_next は「この後にまだ行があるか」。総件数と返却行数を比べると
	// 2 ページ目以降で常に true になり、最終ページで「次がある」と嘘をつく。
	tests := []struct {
		name     string
		total    int
		pg       Page
		returned int
		want     bool
	}{
		{"1 ページ目、続きあり", 50011, Page{Limit: 100, Offset: 0}, 100, true},
		{"最終ページ", 50011, Page{Limit: 100, Offset: 50000}, 11, false},
		{"ちょうど割り切れる最終ページ", 200, Page{Limit: 100, Offset: 100}, 100, false},
		{"1 ページに収まる", 11, Page{Limit: 100, Offset: 0}, 11, false},
	}
	for _, tt := range tests {
		m := pageMeta(tt.total, tt.pg, tt.returned)
		if m["has_next"] != tt.want {
			t.Errorf("%s: has_next = %v, 期待 %v", tt.name, m["has_next"], tt.want)
		}
		if m["total"] != tt.total {
			t.Errorf("%s: total は該当総件数であるべき: %v", tt.name, m["total"])
		}
	}
}

func TestOrderBy(t *testing.T) {
	allowed := Sortable{
		"ordered_at_asc":    {Col: "ordered_at", Type: "date"},
		"total_amount_desc": {Col: "total_amount", Desc: true, Type: "bigint"},
	}
	def := Sort{Col: "ordered_at", Desc: true, Type: "date"}

	tests := []struct {
		query string
		want  string
	}{
		{"", "ORDER BY ordered_at DESC, order_id DESC"},
		{"?sort=ordered_at_asc", "ORDER BY ordered_at ASC, order_id ASC"},
		{"?sort=total_amount_desc", "ORDER BY total_amount DESC, order_id DESC"},
		// 向きを欠いた値は許可リストに無いので既定へ落ちる。
		// sort と order を分けていたときは、ここが呼び出し側の想定と食い違った。
		{"?sort=total_amount", "ORDER BY ordered_at DESC, order_id DESC"},
		// 許可リストに無い列は SQL に到達しない。LLM は enum で縛っているが
		// API は直接叩けるので、サービス側でも落とす。
		{"?sort=1%3BDROP+TABLE+orders", "ORDER BY ordered_at DESC, order_id DESC"},
		{"?sort=password", "ORDER BY ordered_at DESC, order_id DESC"},
	}
	for _, tt := range tests {
		got := OrderBy(httptest.NewRequest("GET", "/orders"+tt.query, nil),
			allowed, def, "order_id", "text").SQL()
		if got != tt.want {
			t.Errorf("OrderBy(%q) = %q, 期待 %q", tt.query, got, tt.want)
		}
	}
}

func TestOrderByAlwaysTiebreaks(t *testing.T) {
	// tiebreak が無いと、同値の行の並びが不定になる。
	// ページ送りと組み合わせたとき、同じ行が 2 回出たり
	// どのページにも出なかったりする。
	allowed := Sortable{"amount_desc": {Col: "amount", Desc: true, Type: "bigint"}}
	def := Sort{Col: "due_at", Type: "date"}
	for _, q := range []string{"", "?sort=amount_desc", "?sort=unknown"} {
		got := OrderBy(httptest.NewRequest("GET", "/invoices"+q, nil),
			allowed, def, "invoice_id", "text").SQL()
		if !strings.Contains(got, "invoice_id") {
			t.Errorf("OrderBy(%q) = %q に tiebreak が無い", q, got)
		}
	}
}

func TestCursorRoundTripAndSortGuard(t *testing.T) {
	c := Cursor{Sort: "ordered_at_asc", Value: "2024-01-01", ID: "O-50940"}
	enc := c.Encode()

	got := DecodeCursor(enc, "ordered_at_asc")
	if got == nil || got.Value != "2024-01-01" || got.ID != "O-50940" {
		t.Fatalf("往復できていない: %+v", got)
	}
	// **並び順が変わった cursor は捨てる。**
	// 受注日順で作った cursor を金額順に渡されると、飛ばす行と返す行が
	// 噛み合わず、静かに歯抜けの結果を返すことになる。
	if DecodeCursor(enc, "total_amount_desc") != nil {
		t.Error("並び順が違う cursor は無効にすべき")
	}
	if DecodeCursor("not-base64!!", "ordered_at_asc") != nil {
		t.Error("壊れた cursor は無効にすべき")
	}
	if DecodeCursor("", "ordered_at_asc") != nil {
		t.Error("空の cursor は nil にすべき")
	}
}

func TestKeysetDirection(t *testing.T) {
	// 昇順は「より後ろ」、降順は「より前」を取る。
	// 行値比較なので、列と tiebreak の向きが揃っている前提。
	asc := Order{Sort: Sort{Col: "ordered_at", Type: "date"}, Tiebreak: "order_id", TieType: "text"}
	w := &W{}
	asc.Keyset(w, &Cursor{Value: "2024-01-01", ID: "O-1"})
	if !strings.Contains(w.SQL(), ">") || strings.Contains(w.SQL(), "<") {
		t.Errorf("昇順は > を使うべき: %s", w.SQL())
	}
	desc := Order{Sort: Sort{Col: "ordered_at", Desc: true, Type: "date"}, Tiebreak: "order_id", TieType: "text"}
	w2 := &W{}
	desc.Keyset(w2, &Cursor{Value: "2024-01-01", ID: "O-1"})
	if !strings.Contains(w2.SQL(), "<") {
		t.Errorf("降順は < を使うべき: %s", w2.SQL())
	}
	// 型のキャストが入っていないと date と text を比較して落ちる。
	if !strings.Contains(w.SQL(), "::date") {
		t.Errorf("cursor 値は列の型へキャストすべき: %s", w.SQL())
	}
}
