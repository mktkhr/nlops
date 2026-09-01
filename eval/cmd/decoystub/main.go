// Command decoystub はダミーサービスの応答を返すスタブ。
//
// スケール検証では「ダミー Tool を選ばないこと」だけを測っていた。
// ダミーの base_url は誰も listen していないので、選んでしまった場合は
// 必ず接続エラーになる。**実運用のダミーは応答を返す。** 購買サービスは
// 実在し、発注書を返す。それは受注ではないというだけで、形は整っている。
//
// このスタブは 3 つの壊れ方を作り分ける:
//
//	down      : listen しない (何もしない。スタブを起動しなければよい)
//	error     : 404 / 500 を返す。Tool は在るが対象が無い
//	plausible : Tool 定義の fields どおりの、形の整った**別ドメインのデータ**を返す
//
// plausible が本命。Projection を通過するので、LLM から見ると
// 正しい Tool の結果と区別がつかない。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/mktkhr/nlops/pkg/toolschema"
)

func main() {
	var (
		catalogPath = flag.String("catalog", "catalog/scale/tools-504.json", "ダミーを含むカタログ")
		addr        = flag.String("addr", "127.0.0.1:9199", "listen アドレス")
		mode        = flag.String("mode", "plausible", "plausible / error")
		real        = flag.String("real", "catalog/services.json", "実サービスのカタログ (これらは中継せず 404)")
	)
	flag.Parse()

	cat, err := toolschema.Load(*catalogPath)
	if err != nil {
		log.Fatalf("カタログ: %v", err)
	}
	realCat, err := toolschema.Load(*real)
	if err != nil {
		log.Fatalf("実カタログ: %v", err)
	}
	realNames := map[string]bool{}
	for _, s := range realCat.Services {
		realNames[s.Name] = true
	}

	s := &stub{mode: *mode, served: map[string]bool{}}
	// パスの形 → Tool の対応を作る。パスパラメータは * に潰して照合する。
	for _, svc := range cat.Services {
		if realNames[svc.Name] {
			continue
		}
		for _, t := range svc.Tools {
			s.routes = append(s.routes, route{pattern: segments(t.HTTP.Path), tool: t})
		}
	}
	log.Printf("decoystub: %d ダミー Tool / mode=%s / %s", len(s.routes), s.mode, *addr)
	log.Fatal(http.ListenAndServe(*addr, s))
}

type route struct {
	pattern []string
	tool    toolschema.Tool
}

type stub struct {
	mode   string
	routes []route

	// served は返した値を覚える。最終回答にこれらが混ざっていれば
	// 「ダミーの結果を答えに使った」と判定できる。
	mu     sync.Mutex
	served map[string]bool
}

func segments(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 返した値の一覧。汚染判定のために外から読む。
	if r.URL.Path == "/__served" {
		s.mu.Lock()
		out := make([]string, 0, len(s.served))
		for k := range s.served {
			out = append(out, k)
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"values": out})
		return
	}
	if r.URL.Path == "/__reset" {
		s.mu.Lock()
		s.served = map[string]bool{}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}

	t, ok := s.match(segments(r.URL.Path))
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "そのようなエンドポイントはありません"})
		return
	}
	if s.mode == "error" {
		// Tool は在るが対象が無い、という最もありふれた壊れ方。
		writeJSON(w, 404, map[string]any{"error": "対象が見つかりません"})
		return
	}

	body, values := synth(t)
	s.mu.Lock()
	for _, v := range values {
		s.served[v] = true
	}
	s.mu.Unlock()
	writeJSON(w, 200, body)
}

func (s *stub) match(path []string) (toolschema.Tool, bool) {
	for _, rt := range s.routes {
		if len(rt.pattern) != len(path) {
			continue
		}
		hit := true
		for i, seg := range rt.pattern {
			if strings.HasPrefix(seg, "{") {
				continue // パスパラメータは何でも通す
			}
			if seg != path[i] {
				hit = false
				break
			}
		}
		if hit {
			return rt.tool, true
		}
	}
	return toolschema.Tool{}, false
}

// synth は Tool 定義の fields どおりの行を作る。
// 返した ID 的な値も併せて返す (汚染判定に使う)。
func synth(t toolschema.Tool) (map[string]any, []string) {
	n := t.Projection.MaxItems
	if n <= 0 || n > 5 {
		n = 5
	}
	single := t.Projection.ListPath == ""
	if single {
		n = 1
	}
	rows := make([]any, 0, n)
	var values []string
	for i := 1; i <= n; i++ {
		row := map[string]any{}
		for _, f := range t.Projection.Fields {
			v := fieldValue(f, i)
			row[f] = v
			// ID だけでなく**数値も**記録する。
			// 最初の実測で「商品の単価は 90100 です」という回答を
			// 汚染なしと判定してしまった。捏造された金額こそ最悪の混入。
			if sv := token(v); sv != "" {
				values = append(values, sv)
			}
		}
		rows = append(rows, row)
	}
	// 実サービスと同じ「本物の API らしい冗長さ」を付ける。
	// ここが素直すぎると Projection の効き方が実測とずれる。
	meta := map[string]any{
		"api_version": "2026-05-01", "page": 1, "per_page": 100,
		"has_next": false, "trace_id": "00000000-0000-4000-8000-000000000000",
	}
	if single {
		out := rows[0].(map[string]any)
		out["meta"] = meta
		return out, values
	}
	return map[string]any{
		"items": rows, "count": len(rows), "meta": meta,
	}, values
}

// token は汚染判定に使える値を返す。
// 短い値は実データと偶然一致するので 4 文字未満は捨てる。
func token(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) >= 4 {
			return x
		}
	case int:
		s := strconv.Itoa(x)
		if len(s) >= 4 {
			return s
		}
	}
	return ""
}

// fieldValue はフィールド名から「それらしい値」を作る。
// ID は 9 万番台にして実データ (C001 / O-1001 / INV-2001) と衝突させない。
// 最終回答にこの番号が出てきたら、ダミーの結果を使ったことが分かる。
func fieldValue(field string, i int) any {
	switch {
	case strings.HasSuffix(field, "_id") || field == "id":
		return fmt.Sprintf("%s-9%04d", abbrev(field), i)
	case strings.HasSuffix(field, "_at") || strings.HasSuffix(field, "_date"):
		return fmt.Sprintf("2026-0%d-1%d", (i%9)+1, i%10)
	case strings.Contains(field, "amount") || strings.Contains(field, "cost") ||
		strings.Contains(field, "price") || strings.Contains(field, "balance"):
		return 90000 + i*100
	case strings.Contains(field, "quantity") || strings.Contains(field, "days") ||
		strings.Contains(field, "count") || strings.Contains(field, "score"):
		// 3 桁以下だと実データと偶然一致して汚染を誤検出する。
		return 9000 + i
	case field == "status":
		return "ACTIVE"
	case strings.Contains(field, "name") || strings.Contains(field, "title"):
		return fmt.Sprintf("ダミー%s%d", field, i)
	case strings.Contains(field, "email"):
		return fmt.Sprintf("decoy%d@example.invalid", i)
	default:
		return fmt.Sprintf("DECOY-%s-%d", field, i)
	}
}

// abbrev は purchase_order_id → PO のような接頭辞を作る。
func abbrev(field string) string {
	parts := strings.Split(strings.TrimSuffix(field, "_id"), "_")
	var b strings.Builder
	for _, p := range parts {
		if p != "" {
			b.WriteByte(p[0] - 32)
		}
	}
	if b.Len() == 0 {
		return "X"
	}
	return b.String()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
