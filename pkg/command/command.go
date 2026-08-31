// Package command は「LLM が提案してよい更新操作」の定義を読み込む。
//
// 元案 §13 の Command Proposal 方式を支える。境界は次のとおり。
//   - LLM はここに定義されたコマンドと引数しか書けない (JSON Schema で固定する)
//   - LLM は実行しない。生成するのは提案だけ
//   - 実行は人間が UI で確認した後、BFF が各サービスの更新 API を呼ぶ
//   - 実行可否の業務判断 (キャンセルできる状態かなど) はサービス側の責務
//
// http と service は Executor / BFF だけが参照し、LLM には見せない。
package command

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Catalog は更新操作の全体定義。
type Catalog struct {
	Version  string    `json:"version"`
	Note     string    `json:"note"`
	Commands []Command `json:"commands"`
}

// Command は 1 つの更新操作。
type Command struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Service     string           `json:"service"`
	HTTP        HTTPBinder       `json:"http"` // LLM には見せない
	Parameters  map[string]Param `json:"parameters"`
	Confirm     string           `json:"confirm"` // UI で人間に見せる確認文
}

// HTTPBinder は更新 API の呼び出し先。
type HTTPBinder struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Param は引数の定義。
type Param struct {
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description"`
}

// Load はコマンド定義を読み込む。
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("コマンド定義読み込み: %w", err)
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("コマンド定義解析: %w", err)
	}
	for _, cmd := range c.Commands {
		if cmd.Service == "" {
			return nil, fmt.Errorf("command %q: service が空", cmd.Name)
		}
		if !strings.HasPrefix(cmd.Name, cmd.Service+".") {
			return nil, fmt.Errorf("command %q は service %q の接頭辞を持たない", cmd.Name, cmd.Service)
		}
		if cmd.HTTP.Method == "GET" {
			return nil, fmt.Errorf("command %q: 更新操作に GET は使えない", cmd.Name)
		}
	}
	return &c, nil
}

// Names はコマンド名を定義順で返す。
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.Commands))
	for _, cmd := range c.Commands {
		out = append(out, cmd.Name)
	}
	return out
}

// ByName はコマンドを名前で引く。
func (c *Catalog) ByName(name string) (Command, bool) {
	for _, cmd := range c.Commands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return Command{}, false
}

// ParamNames は引数名を昇順で返す。
// prompt prefix を安定させるため map の iteration 順には依存しない。
func (c Command) ParamNames() []string {
	out := make([]string, 0, len(c.Parameters))
	for k := range c.Parameters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Validate は提案された引数を検証する。
// 定義外の引数は落とし、必須の欠落と enum 外の値はエラーにする。
func (c Command) Validate(in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range in {
		p, ok := c.Parameters[k]
		if !ok {
			continue // 定義外は黙って落とす
		}
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				continue
			}
			if len(p.Enum) > 0 && !contains(p.Enum, s) {
				return nil, fmt.Errorf("引数 %s の値 %q は候補外です (%s)", k, s, strings.Join(p.Enum, " / "))
			}
		}
		if v == nil {
			continue
		}
		out[k] = v
	}
	for _, k := range c.ParamNames() {
		if c.Parameters[k].Required {
			if _, ok := out[k]; !ok {
				return nil, fmt.Errorf("必須の引数 %s がありません", k)
			}
		}
	}
	// 「email か phone のどちらかは必要」のような業務ルールはここでは見ない。
	// 判断はサービス側の責務であり、BFF や LLM が代わりに決めてはいけない。
	return out, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
