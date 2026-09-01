// Package uiroute は LLM が遷移先として選べる画面の定義を読み込む。
//
// 元案 §14 の「WRITE API を直接実行する代わりに Frontend の状態を生成する」を
// 実装するための土台。Tool Registry と同じ考え方で、
//   - LLM は画面のパスとフィルタしか出さない
//   - 定義に無い画面・フィルタは JSON Schema の段階で生成できない
//
// とすることで、自由な遷移先の生成を防ぐ。
package uiroute

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Catalog は画面定義の全体。
type Catalog struct {
	Version string  `json:"version"`
	Note    string  `json:"note"`
	Routes  []Route `json:"routes"`
}

// Route は 1 つの画面。
type Route struct {
	Path        string            `json:"path"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Filters     map[string]Filter `json:"filters"`
}

// Filter は画面が受け付ける絞り込み条件。
type Filter struct {
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description"`
}

// Load は画面定義を読み込む。
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("画面定義読み込み: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("画面定義解析: %w", err)
	}
	for _, r := range c.Routes {
		if !strings.HasPrefix(r.Path, "/") {
			return nil, fmt.Errorf("route %q: path は / で始まる必要があります", r.Path)
		}
	}
	return &c, nil
}

// Paths は画面パスの一覧を定義順で返す。
func (c *Catalog) Paths() []string {
	out := make([]string, 0, len(c.Routes))
	for _, r := range c.Routes {
		out = append(out, r.Path)
	}
	return out
}

// ByPath は画面をパスで引く。
func (c *Catalog) ByPath(path string) (Route, bool) {
	for _, r := range c.Routes {
		if r.Path == path {
			return r, true
		}
	}
	return Route{}, false
}

// FilterNames は画面のフィルタ名を昇順で返す。
// prompt prefix を安定させるため map の iteration 順には依存しない。
func (r Route) FilterNames() []string {
	out := make([]string, 0, len(r.Filters))
	for k := range r.Filters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Sanitize は定義外のフィルタと空の値を落とす。
// LLM が余計なキーを出しても URL には載せない。
func (r Route) Sanitize(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		f, ok := r.Filters[k]
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if len(f.Enum) > 0 && !contains(f.Enum, v) {
			continue
		}
		out[k] = v
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
