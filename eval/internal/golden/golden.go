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
