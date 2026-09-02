package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mktkhr/nlops/pkg/toolschema"
)

func testTool() toolschema.Tool {
	return toolschema.Tool{
		Name: "order.search",
		HTTP: toolschema.HTTPBinder{Method: "GET", Path: "/orders"},
		Parameters: toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"customer_id": {Type: "string"},
				"status":      {Type: "string", Enum: []string{"PLACED", "SHIPPED"}},
			},
		},
	}
}

func TestInvalidEnums(t *testing.T) {
	tool := testTool()
	tests := []struct {
		name    string
		args    map[string]any
		wantBad bool
	}{
		{"候補内なら通す", map[string]any{"status": "PLACED"}, false},
		{"候補外は差し戻す", map[string]any{"status": "UNSHIPPED"}, true},
		{"enum のない引数は見ない", map[string]any{"customer_id": "なんでも"}, false},
		{"大文字小文字は区別する", map[string]any{"status": "placed"}, true},
		{"引数なしなら通す", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := invalidEnums(tool, tt.args)
			if (len(got) > 0) != tt.wantBad {
				t.Fatalf("invalidEnums(%v) = %v, 差し戻し期待=%v", tt.args, got, tt.wantBad)
			}
		})
	}
}

func TestUnresolvedIDs(t *testing.T) {
	e := New(nil)
	e.Reset("注文 O-1001 の状況を教えて")

	if bad := e.unresolvedIDs(map[string]any{"order_id": "O-1001"}, nil); len(bad) != 0 {
		t.Errorf("ユーザー入力に現れた ID は通すべき: %v", bad)
	}
	if bad := e.unresolvedIDs(map[string]any{"customer_id": "CUST-999"}, nil); len(bad) != 1 {
		t.Errorf("どこにも現れていない ID は差し戻すべき: %v", bad)
	}
	// enum で候補が固定されている引数は捏造できないので検査しない。
	skip := map[string]bool{"warehouse_id": true}
	if bad := e.unresolvedIDs(map[string]any{"warehouse_id": "WH_TOKYO"}, skip); len(bad) != 0 {
		t.Errorf("enum 引数は検査対象外にすべき: %v", bad)
	}
	if bad := e.unresolvedIDs(map[string]any{"warehouse_id": "WH_TOKYO"}, nil); len(bad) != 1 {
		t.Errorf("skip 指定が無ければ従来どおり差し戻すべき: %v", bad)
	}
	// Tool 結果に現れた ID は以後既知として扱う。
	e.recordIDs(map[string]any{"items": []any{map[string]any{"customer_id": "C001"}}})
	if bad := e.unresolvedIDs(map[string]any{"customer_id": "C001"}, nil); len(bad) != 0 {
		t.Errorf("Tool 結果に現れた ID は通すべき: %v", bad)
	}
	// ID でない引数は対象外。
	if bad := e.unresolvedIDs(map[string]any{"name": "田中"}, nil); len(bad) != 0 {
		t.Errorf("ID 以外の引数は見るべきでない: %v", bad)
	}
}

func TestProject(t *testing.T) {
	tests := []struct {
		name string
		body string
		proj toolschema.Projection
		want string
	}{
		{
			name: "一覧: whitelist で絞り max_items で切る",
			body: `{"items":[{"a":1,"junk":"x"},{"a":2,"junk":"y"},{"a":3}],"count":3,"meta":{"trace_id":"t"}}`,
			proj: toolschema.Projection{ListPath: "items", Fields: []string{"a"}, MaxItems: 2},
			want: `{"count":3,"items":[{"a":1},{"a":2}],"shown":2,"truncated":true}`,
		},
		{
			name: "単一: data_path を辿って whitelist で絞る",
			body: `{"data":{"a":1,"junk":"x","_links":{"self":"/x"}},"meta":{"trace_id":"t"}}`,
			proj: toolschema.Projection{DataPath: "data", Fields: []string{"a"}, MaxItems: 1},
			want: `{"a":1}`,
		},
		{
			name: "件数が max_items 以下なら truncated を付けない",
			body: `{"items":[{"a":1}],"count":1}`,
			proj: toolschema.Projection{ListPath: "items", Fields: []string{"a"}, MaxItems: 5},
			want: `{"count":1,"items":[{"a":1}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body any
			if err := json.Unmarshal([]byte(tt.body), &body); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(project(body, tt.proj))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("project() = %s\n期待 = %s", got, tt.want)
			}
		})
	}
}

func TestBuildURL(t *testing.T) {
	pathTool := toolschema.Tool{
		HTTP: toolschema.HTTPBinder{Method: "GET", Path: "/customers/{customer_id}/credit"},
	}
	got, err := buildURL("http://x", pathTool, map[string]any{"customer_id": "C001"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://x/customers/C001/credit" {
		t.Errorf("パスパラメータの展開が違う: %s", got)
	}

	// パスに使われなかった引数はクエリ文字列へ回す。
	got, err = buildURL("http://x", testTool(), map[string]any{"status": "PLACED", "customer_id": "C001"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://x/orders?customer_id=C001&status=PLACED" {
		t.Errorf("クエリ文字列の組み立てが違う: %s", got)
	}

	// 必須のパスパラメータが無ければエラーにする (URL を組み立てない)。
	if _, err = buildURL("http://x", pathTool, map[string]any{}); err == nil {
		t.Error("パスパラメータ未指定はエラーにすべき")
	}
}

func TestSanitizeArgs(t *testing.T) {
	e := New(nil)
	got := e.sanitizeArgs(testTool(), map[string]any{
		"status":      "PLACED",
		"unknown_arg": "捏造された引数",
		"customer_id": "",
		"nil_arg":     nil,
	})
	if len(got) != 1 || got["status"] != "PLACED" {
		t.Errorf("定義外・空・nil の引数は落とすべき: %v", got)
	}
}

func TestErrorMessagesAreActionable(t *testing.T) {
	// 差し戻しメッセージは LLM が次の行動を決められる内容でなければならない。
	bad := invalidEnums(testTool(), map[string]any{"status": "UNSHIPPED"})
	if len(bad) != 1 {
		t.Fatalf("差し戻しが 1 件のはず: %v", bad)
	}
	for _, want := range []string{"UNSHIPPED", "PLACED", "SHIPPED"} {
		if !strings.Contains(bad[0], want) {
			t.Errorf("メッセージに %q が含まれるべき: %s", want, bad[0])
		}
	}
}

func TestAmbiguousIDs(t *testing.T) {
	e := New(nil)
	e.Reset("山田さんの電話番号を変更して")

	// 候補が複数あった一覧から拾った ID は、更新の対象にしてはいけない。
	e.markAmbiguity(map[string]any{
		"count": float64(250),
		"items": []any{
			map[string]any{"customer_id": "C10011", "name": "山田太郎"},
			map[string]any{"customer_id": "C10031", "name": "山田花子"},
		},
	})
	if bad := e.AmbiguousIDs(map[string]any{"customer_id": "C10011"}, nil); len(bad) != 1 {
		t.Errorf("候補が 250 件ある中の 1 件は差し戻すべき: %v", bad)
	}

	// 1 件に絞り込めた検索の結果は、以前曖昧だった ID でも特定できたとみなす。
	e.markAmbiguity(map[string]any{
		"count": float64(1),
		"items": []any{map[string]any{"customer_id": "C10011", "name": "山田太郎"}},
	})
	if bad := e.AmbiguousIDs(map[string]any{"customer_id": "C10011"}, nil); len(bad) != 0 {
		t.Errorf("1 件に絞り込めた ID は通すべき: %v", bad)
	}

	// 利用者が自分で書いた ID は、広い一覧に出てきても曖昧ではない。
	e2 := New(nil)
	e2.Reset("C005 のメールを更新して")
	e2.markAmbiguity(map[string]any{
		"count": float64(5006),
		"items": []any{map[string]any{"customer_id": "C005"}},
	})
	if bad := e2.AmbiguousIDs(map[string]any{"customer_id": "C005"}, nil); len(bad) != 0 {
		t.Errorf("利用者が指定した ID は通すべき: %v", bad)
	}
}

func TestProjectKeepsServiceCount(t *testing.T) {
	// サービスがページングして 100 行だけ返し、count に総件数 50011 を入れた場合。
	// ここで count を返却行数に置き換えると、
	// 「何件ありますか」に対して LLM がページサイズを答えてしまう。
	items := make([]any, 100)
	for i := range items {
		items[i] = map[string]any{"order_id": "O-1", "junk": "x"}
	}
	body := map[string]any{"items": items, "count": float64(50011), "returned": float64(100)}
	got := project(body, toolschema.Projection{
		ListPath: "items", Fields: []string{"order_id"}, MaxItems: 20,
	}).(map[string]any)

	if got["count"] != 50011 {
		t.Errorf("count はサービスが返した総件数のままにすべき: %v", got["count"])
	}
	if n := len(got["items"].([]any)); n != 20 {
		t.Errorf("items は max_items まで切るべき: %d", n)
	}
	if got["truncated"] != true || got["shown"] != 20 {
		t.Errorf("切り詰めたことを伝えるべき: %v %v", got["truncated"], got["shown"])
	}
}

func TestMarkAmbiguityUsesServiceCount(t *testing.T) {
	// Projection を通った後の count は Go の int。float64 だけを見ていると
	// 「251 件の候補」を「10 件」(見えている行数) と誤って覚え、
	// 差し戻しメッセージが嘘の件数を伝える。
	for _, count := range []any{251, float64(251)} {
		e := New(nil)
		e.Reset("高橋さんの一番古い注文は？")
		e.markAmbiguity(map[string]any{
			"count": count,
			"items": []any{
				map[string]any{"customer_id": "C005", "name": "高橋みどり"},
				map[string]any{"customer_id": "C10003", "name": "高橋彩"},
			},
		})
		bad := e.AmbiguousIDs(map[string]any{"customer_id": "C005"}, nil)
		if len(bad) != 1 {
			t.Fatalf("count=%v: 差し戻すべき: %v", count, bad)
		}
		if !strings.Contains(bad[0], "251") {
			t.Errorf("count=%v: 候補件数を伝えるべき: %q", count, bad[0])
		}
	}
}
