package svc

import (
	"net/http/httptest"
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
