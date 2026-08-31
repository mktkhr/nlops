// Command mkcatalog はスケール検証用のカタログを生成する。
//
// 実サービス (catalog/services.json) にダミーサービス (catalog/decoys.json) を
// 混ぜ、Tool 総数の異なるカタログを作る。ゴールデンセットの正解は常に実 Tool 側なので、
// ダミーを選び始めたら「Tool 数が増えて選定が壊れた」ことになる。
//
// 実サービスとダミーサービスは交互に並べる。全ダミーを末尾へ寄せると
// プロンプト内の位置の違いが結果に混ざるため。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// compactSpec は decoys.json の圧縮表現。
type compactSpec struct {
	Note        string           `json:"note"`
	StubBaseURL string           `json:"stub_base_url"`
	Services    []compactService `json:"services"`
}

type compactService struct {
	Name           string        `json:"name"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Responsibility string        `json:"responsibility"`
	Tools          []compactTool `json:"tools"`
}

// bulkSpec は 500 Tool 規模の検証用。1 ドメインから 10 Tool をテンプレート生成する。
// 手書きの decoys.json と違い業務的な競合は作らないので、担うのは
// 難易度ではなく context への圧力。
type bulkSpec struct {
	StubBaseURL string       `json:"stub_base_url"`
	Domains     []bulkDomain `json:"domains"`
}

type bulkDomain struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Entity string `json:"entity"`
	EID    string `json:"eid"`
	Sub    string `json:"sub"`
	SubID  string `json:"subid"`
	Desc   string `json:"desc"`
	Resp   string `json:"resp"`
}

// compactTool は n=名前 / d=説明 / p=引数 / f=返却フィールド / m=最大件数。
type compactTool struct {
	N string            `json:"n"`
	D string            `json:"d"`
	P map[string]string `json:"p"`
	F []string          `json:"f"`
	M int               `json:"m"`
}

func main() {
	var (
		realPath  = flag.String("real", "catalog/services.json", "実サービスのカタログ")
		decoyPath = flag.String("decoys", "catalog/decoys.json", "手書きダミーサービスの定義")
		bulkPath  = flag.String("bulk", "catalog/decoys-bulk.json", "テンプレート生成するダミードメインの定義")
		outDir    = flag.String("out", "catalog/scale", "出力先ディレクトリ")
		sizes     = flag.String("sizes", "24,44,64,84,104,124", "生成する Tool 総数 (カンマ区切り)")
	)
	flag.Parse()

	realRaw, err := os.ReadFile(*realPath)
	if err != nil {
		die(err)
	}
	var real map[string]any
	if err := json.Unmarshal(realRaw, &real); err != nil {
		die(err)
	}
	realServices, _ := real["services"].([]any)

	decoyRaw, err := os.ReadFile(*decoyPath)
	if err != nil {
		die(err)
	}
	var spec compactSpec
	if err := json.Unmarshal(decoyRaw, &spec); err != nil {
		die(err)
	}
	decoyServices := make([]any, 0, len(spec.Services))
	for _, s := range spec.Services {
		decoyServices = append(decoyServices, expand(s, spec.StubBaseURL))
	}

	// 手書きダミーを使い切ったらテンプレート生成分を足す。
	if bulkRaw, err := os.ReadFile(*bulkPath); err == nil {
		var bulk bulkSpec
		if err := json.Unmarshal(bulkRaw, &bulk); err != nil {
			die(err)
		}
		for _, d := range bulk.Domains {
			decoyServices = append(decoyServices, expand(synthesize(d), bulk.StubBaseURL))
		}
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die(err)
	}
	for _, s := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			die(fmt.Errorf("サイズ %q が数値ではありません", s))
		}
		out, total := compose(realServices, decoyServices, n)
		path := filepath.Join(*outDir, fmt.Sprintf("services-%d.json", total))
		b, _ := json.MarshalIndent(map[string]any{"version": "1", "services": out}, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			die(err)
		}
		fmt.Printf("%s: %d サービス / %d Tool\n", path, len(out), total)
	}
}

// synthesize は 1 ドメインから 10 Tool を組み立てる。
func synthesize(d bulkDomain) compactService {
	e, id, sub, subID := d.Entity, d.EID, d.Sub, d.SubID
	t := func(n, desc string, p map[string]string, f []string, m int) compactTool {
		return compactTool{N: n, D: desc, P: p, F: f, M: m}
	}
	return compactService{
		Name: d.Name, Title: d.Title, Description: d.Desc, Responsibility: d.Resp,
		Tools: []compactTool{
			t("search", e+"を状態や登録日で検索する。", map[string]string{
				"status":  "e:DRAFT|ACTIVE|CLOSED|CANCELLED:" + e + "の状態",
				"keyword": "s:名称の部分一致", "created_from": "s:登録日の下限 (YYYY-MM-DD)",
			}, []string{id, "name", "status", "created_at"}, 20),
			t("get", "IDを指定して"+e+"の詳細を取得する。", map[string]string{
				"!" + id: "s:" + e + "のID",
			}, []string{id, "name", "status", "created_at", "owner_id"}, 1),
			t("list"+title(subID), e+"に紐づく"+sub+"の一覧を取得する。", map[string]string{
				"!" + id: "s:" + e + "のID",
			}, []string{subID, "name", "status"}, 20),
			t("countByStatus", e+"を状態ごとに件数集計する。", map[string]string{
				"owner_id": "s:担当者ID",
			}, []string{"status", "count"}, 10),
			t("getSummary", e+"を期間で集計する。", map[string]string{
				"!period": "e:THIS_MONTH|LAST_MONTH|THIS_YEAR:集計期間", "owner_id": "s:担当者ID",
			}, []string{"period", "count", "total_amount"}, 1),
			t("listHistory", e+"の変更履歴を取得する。", map[string]string{
				"!" + id: "s:" + e + "のID",
			}, []string{"changed_at", "changed_by", "summary"}, 20),
			t("getStatus", e+"の現在の処理状況を取得する。", map[string]string{
				"!" + id: "s:" + e + "のID",
			}, []string{id, "status", "stage", "updated_at"}, 1),
			t("listRecent", "最近登録された"+e+"の一覧を取得する。", map[string]string{
				"within_days": "i:何日以内か。省略時は 7。",
			}, []string{id, "name", "created_at"}, 20),
			t("listByOwner", "担当者ごとの"+e+"の一覧を取得する。", map[string]string{
				"!owner_id": "s:担当者ID", "status": "e:DRAFT|ACTIVE|CLOSED|CANCELLED:" + e + "の状態",
			}, []string{id, "name", "status"}, 20),
			t("getStats", e+"の件数と平均処理日数の統計を取得する。", map[string]string{
				"!period": "e:THIS_MONTH|LAST_MONTH|THIS_YEAR:集計期間",
			}, []string{"period", "total_count", "avg_days"}, 1),
		},
	}
}

// title は先頭 1 文字を大文字にする (Tool 名の組み立て用)。
func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// compose は実サービスをダミーの中へ等間隔に散らす。
//
// 実サービスは常に全て含め、ダミーは Tool 総数が target に届くまで足す。
// 単純な交互配置だと、ダミーが増えたときに実サービスが先頭へ固まってしまい、
// プロンプト内の位置が結果に混ざる。消化率で按分して全体へ散らす。
func compose(real, decoy []any, target int) ([]any, int) {
	total := 0
	for _, r := range real {
		total += toolCount(r)
	}
	var picked []any
	for _, d := range decoy {
		if total >= target {
			break
		}
		picked = append(picked, d)
		total += toolCount(d)
	}

	out := make([]any, 0, len(real)+len(picked))
	ri, di := 0, 0
	for ri < len(real) || di < len(picked) {
		takeReal := ri < len(real) && (di >= len(picked) ||
			float64(ri)/float64(len(real)) <= float64(di)/float64(len(picked)))
		if takeReal {
			out = append(out, real[ri])
			ri++
		} else {
			out = append(out, picked[di])
			di++
		}
	}
	return out, total
}

func toolCount(svc any) int {
	m, ok := svc.(map[string]any)
	if !ok {
		return 0
	}
	tools, _ := m["tools"].([]any)
	return len(tools)
}

// expand は圧縮表現を実カタログと同じ形へ展開する。
func expand(s compactService, baseURL string) map[string]any {
	outTools := make([]any, 0, len(s.Tools))
	for _, t := range s.Tools {
		props := map[string]any{}
		var required []string
		for rawKey, rawVal := range t.P {
			key := rawKey
			if strings.HasPrefix(key, "!") {
				key = key[1:]
				required = append(required, key)
			}
			props[key] = parseParam(rawVal)
		}
		if required == nil {
			required = []string{}
		}
		listPath, dataPath := "items", ""
		if t.M == 1 {
			listPath, dataPath = "", "data"
		}
		outTools = append(outTools, map[string]any{
			"name":        s.Name + "." + t.N,
			"description": t.D,
			"http":        map[string]any{"method": "GET", "path": "/" + t.N},
			"parameters":  map[string]any{"type": "object", "properties": props, "required": required},
			"projection": map[string]any{
				"data_path": dataPath, "list_path": listPath,
				"fields": t.F, "max_items": t.M,
			},
		})
	}
	return map[string]any{
		"name": s.Name, "title": s.Title, "description": s.Description,
		"responsibility": s.Responsibility, "base_url": baseURL, "tools": outTools,
	}
}

// parseParam は "s:説明" / "i:説明" / "e:A|B:説明" を JSON Schema へ展開する。
func parseParam(v string) map[string]any {
	switch {
	case strings.HasPrefix(v, "s:"):
		return map[string]any{"type": "string", "description": v[2:]}
	case strings.HasPrefix(v, "i:"):
		return map[string]any{"type": "integer", "description": v[2:]}
	case strings.HasPrefix(v, "e:"):
		rest := v[2:]
		i := strings.Index(rest, ":")
		if i < 0 {
			return map[string]any{"type": "string", "description": rest}
		}
		return map[string]any{
			"type":        "string",
			"enum":        strings.Split(rest[:i], "|"),
			"description": rest[i+1:],
		}
	default:
		return map[string]any{"type": "string", "description": v}
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "エラー:", err)
	os.Exit(1)
}
