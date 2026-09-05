// Package golden はゴールデンセットの読み込みと採点判定を担う。
package golden

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Set はゴールデンセット全体。
type Set struct {
	Version string `json:"version"`
	Note    string `json:"note"`
	Cases   []Case `json:"cases"`
}

// Case は 1 件の検証ケース。
type Case struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	UserID   string `json:"user_id"`
	Query    string `json:"query"`
	Note     string `json:"note"`

	ExpectedServices []string  `json:"expected_services"`
	FirstCall        FirstCall `json:"first_call"`
	RequiredTools    []string  `json:"required_tools"`
	ForbiddenTools   []string  `json:"forbidden_tools"`

	Permission *Permission `json:"permission,omitempty"`

	// Navigate は画面遷移に対する期待。nil のときは遷移の有無を採点しない。
	Navigate *NavigateExpect `json:"navigate,omitempty"`

	// Proposal は更新提案に対する期待。nil のときは提案の有無を採点しない。
	Proposal *ProposalExpect `json:"proposal,omitempty"`

	// MaxSteps はこのケースで許容する最大ステップ数。0 なら採点しない。
	// 過剰探索を測るために使う。
	MaxSteps int `json:"max_steps,omitempty"`
}

// NavigateExpect は画面遷移の期待。
type NavigateExpect struct {
	// Expected が false のとき「遷移してはいけない」を意味する。
	Expected bool               `json:"expected"`
	Route    string             `json:"route,omitempty"`
	Filters  map[string]Matcher `json:"filters,omitempty"`

	// FiltersAnyOf は「どれか 1 つに合えばよい」フィルタ集合。
	//
	// 同じ条件を複数の書き方で表せる場合がある。「在庫が0個」は
	// below=1 でも at_most=0 でも正しい。片方だけを期待値に書くと、
	// **特定のモデルの言い回しを正解として固定してしまう。**
	// モデルを入れ替えて比較するときに、正しい出力を落としてしまうので分ける。
	FiltersAnyOf []map[string]Matcher `json:"filters_any_of,omitempty"`
}

// Check は実際の遷移結果を採点する。
func (n *NavigateExpect) Check(route string, filters map[string]string) (bool, string) {
	navigated := route != ""
	if !n.Expected {
		if navigated {
			return false, fmt.Sprintf("遷移すべきでないのに %s へ遷移した", route)
		}
		return true, ""
	}
	if !navigated {
		return false, "画面遷移を期待したが遷移しなかった"
	}
	if n.Route != "" && n.Route != route {
		return false, fmt.Sprintf("遷移先が %s、期待は %s", route, n.Route)
	}
	for k, m := range n.Filters {
		v, ok := filters[k]
		if !ok {
			return false, fmt.Sprintf("フィルタ %s が無い", k)
		}
		if !m.Matches(v) {
			return false, fmt.Sprintf("フィルタ %s の値が %q", k, v)
		}
	}
	if len(n.FiltersAnyOf) > 0 {
		for _, want := range n.FiltersAnyOf {
			if matchesAll(want, filters) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("どの書き方にも合わない (実際: %v)", filters)
	}
	return true, ""
}

func matchesAll(want map[string]Matcher, got map[string]string) bool {
	for k, m := range want {
		v, ok := got[k]
		if !ok || !m.Matches(v) {
			return false
		}
	}
	return true
}

// ProposalExpect は更新提案の期待。
type ProposalExpect struct {
	// Expected が false のとき「提案してはいけない」を意味する。
	Expected  bool               `json:"expected"`
	Command   string             `json:"command,omitempty"`
	Arguments map[string]Matcher `json:"arguments,omitempty"`
}

// Check は実際の提案結果を採点する。
func (n *ProposalExpect) Check(cmd string, args map[string]any) (bool, string) {
	proposed := cmd != ""
	if !n.Expected {
		if proposed {
			return false, fmt.Sprintf("提案すべきでないのに %s を提案した", cmd)
		}
		return true, ""
	}
	if !proposed {
		return false, "更新の提案を期待したが提案しなかった"
	}
	if n.Command != "" && n.Command != cmd {
		return false, fmt.Sprintf("提案が %s、期待は %s", cmd, n.Command)
	}
	for k, m := range n.Arguments {
		v, ok := args[k]
		if !ok {
			return false, fmt.Sprintf("引数 %s が無い", k)
		}
		if !m.Matches(v) {
			return false, fmt.Sprintf("引数 %s の値が %v", k, v)
		}
	}
	return true, ""
}

// FirstCall は初手の Tool 呼び出しに対する期待。
type FirstCall struct {
	AcceptableTools []string           `json:"acceptable_tools"`
	Args            map[string]Matcher `json:"args"`
}

// Matcher は引数 1 つに対する一致条件。
type Matcher struct {
	Match string `json:"match"` // equals | contains | one_of
	Value any    `json:"value"`
}

// Permission は権限差検証の期待。
type Permission struct {
	MustIncludeIDs []string `json:"must_include_ids"`
	MustExcludeIDs []string `json:"must_exclude_ids"`
	ExpectDenied   bool     `json:"expect_denied"`
}

// Load はゴールデンセットを読み込む。
func Load(path string) (*Set, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ゴールデンセット読み込み: %w", err)
	}
	var s Set
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("ゴールデンセット解析: %w", err)
	}
	return &s, nil
}

// ServiceScore は Stage 1 の選定結果を採点する。
// 期待サービスをすべて含んでいれば recall=1、余計なサービスがなければ precision=1。
func ServiceScore(expected, got []string) (recall, precision float64) {
	if len(expected) == 0 {
		return 1, 1
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	hit := 0
	for _, e := range expected {
		if gotSet[e] {
			hit++
		}
	}
	recall = float64(hit) / float64(len(expected))
	if len(got) == 0 {
		return recall, 0
	}
	expSet := map[string]bool{}
	for _, e := range expected {
		expSet[e] = true
	}
	inExp := 0
	for _, g := range got {
		if expSet[g] {
			inExp++
		}
	}
	precision = float64(inExp) / float64(len(got))
	return recall, precision
}

// ToolOK は初手 Tool が許容集合に含まれるかを返す。
func (f FirstCall) ToolOK(got string) bool {
	if len(f.AcceptableTools) == 0 {
		return true
	}
	for _, t := range f.AcceptableTools {
		if t == got {
			return true
		}
	}
	return false
}

// ArgsScore は初手引数の一致率を返す。期待引数が 0 件なら 1 を返す。
// 不一致だった引数名も返す。
func (f FirstCall) ArgsScore(got map[string]any) (float64, []string) {
	if len(f.Args) == 0 {
		return 1, nil
	}
	hit := 0
	var miss []string
	for k, m := range f.Args {
		v, ok := got[k]
		if ok && m.Matches(v) {
			hit++
		} else {
			miss = append(miss, k)
		}
	}
	return float64(hit) / float64(len(f.Args)), miss
}

// Matches は 1 つの値が条件を満たすかを判定する。
func (m Matcher) Matches(got any) bool {
	switch m.Match {
	case "contains":
		return strings.Contains(toStr(got), toStr(m.Value))
	case "one_of":
		list, ok := m.Value.([]any)
		if !ok {
			return false
		}
		for _, v := range list {
			if equalLoose(got, v) {
				return true
			}
		}
		return false
	default: // equals
		return equalLoose(got, m.Value)
	}
}

// equalLoose は JSON 由来の数値/文字列のゆらぎを吸収して比較する。
func equalLoose(a, b any) bool {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return strings.EqualFold(strings.TrimSpace(toStr(a)), strings.TrimSpace(toStr(b)))
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}
