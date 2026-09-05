package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// claude コマンド経由でホスト型モデルを呼ぶ経路。
//
// なぜ CLI かというと、手元のサブスクリプションの認証をそのまま使えるからで、
// API キーを別に用意せずに「境界にあるケースが、モデルの限界なのか設計の穴なのか」
// を切り分けられる。**測定のための経路であって、常用を想定していない。**
//
// 常時使うなら Messages API を直接叩くべき理由が 3 つある。
//
//   - 1 回あたり 12,384 トークンが床。--system-prompt を差し替え、ツールを全禁止し、
//     動的セクションを除いても、claude コマンド自身のハーネスが必ず載る。
//     nlops の実プロンプトは平均 6,458 トークンなので、業務内容の倍が固定費になる。
//   - 呼び出しごとにプロセスを起動するので 1.5 秒が床。
//   - サブスクリプションは対話的な開発利用向けで、レート制限もその想定。

// IsCLIModel はモデル ID が claude コマンド経由かを判定する。
//
// **モデル ID そのものを切り替えスイッチにしている。** 呼び出し側 (evalrun, BFF,
// Loop) は Model を渡すだけで、どちらの経路かを知らなくてよい。
// -models gemma4-12b,claude-opus-5 のように 1 回の実行で並べて比較できる。
func IsCLIModel(model string) bool {
	return model == "claude" || strings.HasPrefix(model, "claude-")
}

// cliResult は claude -p --output-format json の出力。
type cliResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	StopReason string `json:"stop_reason"`
	SessionID  string `json:"session_id"`
	DurationMS int    `json:"duration_ms"`
	CostUSD    float64
	Usage      struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// chatCLI は claude コマンドを 1 回起動して応答を得る。
//
// **セッションは繋がない。** --resume でモデル側に履歴を持たせると、
// 履歴の所有権が nlops から出ていき、ゴールデンセットの再現性 (同じ入力から
// 同じ出力) も失われる。履歴は今までどおり Orchestrator が組み立てて毎回渡す。
func (c *Client) chatCLI(ctx context.Context, req Request) (*Response, error) {
	system, prompt := splitMessages(req.Messages)

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", req.Model,
		// 構造化出力はツール呼び出しとして実現されており、それ自体が 1 ターンを消費する。
		// 1 にすると「答えは出したがターン上限」で max_turns エラーになる (実測)。
		// ツールは全禁止してあるので、余分なターンがあってもできることは無い。
		"--max-turns", "3",
		// 手元の環境や設定を持ち込ませない。測定の再現性のため。
		"--disallowed-tools", "Bash Read Write Edit Glob Grep WebFetch WebSearch Task TodoWrite NotebookEdit",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--exclude-dynamic-system-prompt-sections",
	}
	if system != "" {
		args = append(args, "--system-prompt", system)
	}
	// 制約デコードに相当するもの。llama.cpp の GBNF と違って内部は
	// ツール呼び出しの強制だが、**書けない値は返ってこない**点は同じ。
	wrapped := false
	if req.ResponseFormat != nil && req.ResponseFormat.JSONSchema != nil {
		var body any
		body, wrapped = adaptSchema(req.ResponseFormat.JSONSchema.Schema)
		schema, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("スキーマ整形: %w", err)
		}
		args = append(args, "--json-schema", string(schema))
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "claude", args...)
	// Stdin を明示的に閉じる。開いたままだと claude が入力を待って
	// 「no stdin data received in 3s」の警告を出し、3 秒無駄になる。
	cmd.Stdin = nil
	// エラー内容は stdout 側に出ることがあるので両方を保持する。
	// stderr だけ見ていて「exit status 1 ()」としか分からず原因を追えなかった。
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.Bytes()
	if err != nil {
		return nil, fmt.Errorf("claude 呼び出し: %w (stderr=%s stdout=%s)", err,
			truncate(stderr.String(), 300), truncate(stdout.String(), 1200))
	}

	var r cliResult
	if err := json.Unmarshal(lastJSONLine(out), &r); err != nil {
		return nil, fmt.Errorf("claude の出力を解析できない: %w (out=%s)", err, truncate(string(out), 400))
	}
	if r.IsError {
		return nil, fmt.Errorf("claude がエラーを返した: %s", truncate(r.Result, 400))
	}

	text := r.Result
	if wrapped {
		if text, err = unwrap(text); err != nil {
			return nil, err
		}
	}

	resp := &Response{Wall: time.Since(start)}
	resp.setText(text)
	resp.setFinish(finishOf(r.StopReason))
	// キャッシュ作成分も入力として課金されるので prompt に含める。
	resp.Usage.PromptTokens = r.Usage.InputTokens + r.Usage.CacheReadTokens + r.Usage.CacheCreationTokens
	resp.Usage.CompletionTokens = r.Usage.OutputTokens
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	resp.Usage.PromptTokensDetails.CachedTokens = r.Usage.CacheReadTokens
	return resp, nil
}

// splitMessages は messages を --system-prompt と 1 本のプロンプト文字列に分ける。
//
// claude -p はプロンプトを 1 つしか受け取らないので、system 以外は役割を明記して
// 連結する。**ローカル経路とは入力の形が違う。** 同じ prompt を送っているとは
// 言えないので、両者の数値を比べるときはこの差を織り込む必要がある。
func splitMessages(msgs []Message) (system, prompt string) {
	var sys, rest []string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			sys = append(sys, m.Content)
		case "assistant":
			rest = append(rest, "[これまでの応答]\n"+m.Content)
		default:
			rest = append(rest, m.Content)
		}
	}
	return strings.Join(sys, "\n\n"), strings.Join(rest, "\n\n")
}

// lastJSONLine は出力の最後の JSON らしい行を返す。
// 警告が先に出ることがあるので、行頭が { の最後の行を採る。
func lastJSONLine(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); strings.HasPrefix(s, "{") {
			return []byte(s)
		}
	}
	return out
}

// finishOf は claude の stop_reason を OpenAI 互換の finish_reason に読み替える。
// 構造化出力はツール呼び出しの強制で実現されているため tool_use が返るが、
// 呼び出し側から見れば正常終了である。
func finishOf(stop string) string {
	switch stop {
	case "end_turn", "tool_use", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "":
		return "stop"
	default:
		return stop
	}
}

// wrapKey は最上位 anyOf を 1 段下げるときの入れ物の名前。
// スキーマ内の他のキーと衝突しない名前にしてある。
const wrapKey = "nlops_choice"

// adaptSchema は nlops のスキーマを Anthropic の input_schema が通る形へ直す。
//
// nlops は Tool ごとの厳密なスキーマを**最上位 anyOf** で書いている。
// llama.cpp はこれをそのまま GBNF に落とせるが、Anthropic 側は構造化出力を
// ツールの input_schema として扱うため、
// 「input_schema does not support oneOf, allOf, or anyOf at the top level」
// で 400 になる。そこで 1 段下げて包む。制約の意味は変わらない。
//
// **ローカル経路のスキーマは変えない。** プロンプトが 1 バイトでも変われば
// 比較の基準がずれる。差はこちら側で吸収する。
//
// 第 2 返り値が true のとき、応答は {"nlops_choice": ...} で返るので unwrap で外す。
func adaptSchema(schema any) (any, bool) {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema, false
	}
	union := false
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if _, has := m[k]; has {
			union = true
			break
		}
	}
	if !union {
		if _, has := m["type"]; !has {
			out := copyMap(m)
			out["type"] = "object"
			return out, false
		}
		return schema, false
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{wrapKey: schema},
		"required":             []string{wrapKey},
		"additionalProperties": false,
	}, true
}

// unwrap は包んだ 1 段を外して元の形の JSON 文字列に戻す。
func unwrap(text string) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return "", fmt.Errorf("包んだ応答を解析できない: %w (text=%s)", err, truncate(text, 300))
	}
	inner, ok := m[wrapKey]
	if !ok {
		// 包んだのに素の形で返ってきた。そのまま通す方が呼び出し側は困らない。
		return text, nil
	}
	return string(inner), nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
