// Package toolschema は Tool Registry のカタログ定義を読み込む。
//
// LLM に見せてよい情報 (name / description / parameters) と、
// Executor だけが知る情報 (base_url / http) を同じ構造体で保持し、
// LLM 向けのシリアライズ時に後者を落とす。
package toolschema

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Catalog はサービスと Tool の全体定義。
type Catalog struct {
	Version  string    `json:"version"`
	Services []Service `json:"services"`
}

// Service は 1 つのマイクロサービス。
type Service struct {
	Name           string `json:"name"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Responsibility string `json:"responsibility"`
	BaseURL        string `json:"base_url"` // LLM には見せない
	Tools          []Tool `json:"tools"`
}

// Tool は 1 つの API。
type Tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	HTTP        HTTPBinder `json:"http"` // LLM には見せない
	Parameters  Schema     `json:"parameters"`
	Projection  Projection `json:"projection"`

	service string // 逆引き用。JSON には出さない。
}

// Service は Tool が属するサービス名を返す。
func (t Tool) Service() string { return t.service }

// HTTPBinder は Tool 名と実際の HTTP リクエストの対応。Executor のみが参照する。
type HTTPBinder struct {
	Method string `json:"method"`
	Path   string `json:"path"` // {param} をパスパラメータとして展開する
}

// Projection は Tool 実行結果のうち LLM へ返す部分を定める。
// これを通さない生の API レスポンスを context へ戻してはならない。
type Projection struct {
	DataPath string   `json:"data_path"` // ペイロードを包むキー。"" ならレスポンス直下。
	ListPath string   `json:"list_path"` // 配列を保持するキー。"" なら単一オブジェクト。
	Fields   []string `json:"fields"`    // LLM へ返すフィールドの whitelist
	MaxItems int      `json:"max_items"` // LLM へ返す最大件数
}

// Schema は JSON Schema の必要最小限の表現。
type Schema struct {
	Type       string             `json:"type,omitempty"`
	Properties map[string]*Schema `json:"properties,omitempty"`
	Required   []string           `json:"required,omitempty"`
	Items      *Schema            `json:"items,omitempty"`
	Enum       []string           `json:"enum,omitempty"`
	Const      string             `json:"const,omitempty"`
	AnyOf      []*Schema          `json:"anyOf,omitempty"`
	// AdditionalProperties は false を明示したいので *bool にする。
	AdditionalProperties *bool  `json:"additionalProperties,omitempty"`
	Description          string `json:"description,omitempty"`
}

// Load はカタログ JSON を読み込む。
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("カタログ読み込み: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("カタログ解析: %w", err)
	}
	for i := range c.Services {
		for j := range c.Services[i].Tools {
			c.Services[i].Tools[j].service = c.Services[i].Name
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Catalog) validate() error {
	seen := map[string]bool{}
	for _, s := range c.Services {
		if s.BaseURL == "" {
			return fmt.Errorf("service %q: base_url が空", s.Name)
		}
		for _, t := range s.Tools {
			if seen[t.Name] {
				return fmt.Errorf("tool %q が重複している", t.Name)
			}
			seen[t.Name] = true
			if !strings.HasPrefix(t.Name, s.Name+".") {
				return fmt.Errorf("tool %q は service %q の接頭辞を持たない", t.Name, s.Name)
			}
			if len(t.Projection.Fields) == 0 {
				return fmt.Errorf("tool %q: projection.fields が空 (生レスポンスの context 投入を防ぐため必須)", t.Name)
			}
			if t.Projection.MaxItems <= 0 {
				return fmt.Errorf("tool %q: projection.max_items が未設定", t.Name)
			}
		}
	}
	return nil
}

// ServiceNames はカタログ順のサービス名一覧を返す。
// prompt prefix を安定させるため、順序は常にカタログ定義順とする。
func (c *Catalog) ServiceNames() []string {
	out := make([]string, 0, len(c.Services))
	for _, s := range c.Services {
		out = append(out, s.Name)
	}
	return out
}

// Tools は指定サービスの Tool をカタログ順で返す。services が空なら全 Tool。
func (c *Catalog) Tools(services ...string) []Tool {
	want := map[string]bool{}
	for _, s := range services {
		want[s] = true
	}
	var out []Tool
	for _, s := range c.Services {
		if len(want) > 0 && !want[s.Name] {
			continue
		}
		out = append(out, s.Tools...)
	}
	return out
}

// ToolByName は Tool を名前で引く。
func (c *Catalog) ToolByName(name string) (Tool, bool) {
	for _, s := range c.Services {
		for _, t := range s.Tools {
			if t.Name == name {
				return t, true
			}
		}
	}
	return Tool{}, false
}

// ToolNames は Tool 名一覧をカタログ順で返す。
func ToolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// SortedKeys は map のキーを昇順で返す。prompt prefix を安定させるために使う。
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Bool はスキーマの additionalProperties: false を書くためのヘルパ。
func Bool(b bool) *bool { return &b }
