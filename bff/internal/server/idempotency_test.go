package server

import (
	"net/http/httptest"
	"testing"
)

func key(t *testing.T, hdr, traceID, command string, args map[string]any) string {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/commands/execute", nil)
	if hdr != "" {
		r.Header.Set("Idempotency-Key", hdr)
	}
	return idempotencyKey(r, executeRequest{Command: command, TraceID: traceID}, args)
}

func TestIdempotencyKey(t *testing.T) {
	const trace = "11111111-1111-4111-8111-111111111111"
	args := map[string]any{"customer_id": "C005", "email": "a@example.com"}

	// 同じ承認をもう一度送ったら同じ鍵になる。二重送信を止められる。
	if key(t, "", trace, "customer.updateContact", args) !=
		key(t, "", trace, "customer.updateContact", args) {
		t.Error("同じ会話・同じ操作・同じ引数なら鍵は一致すべき")
	}

	// 引数を変えたら別の操作。同じ会話でも通す。
	other := map[string]any{"customer_id": "C005", "email": "b@example.com"}
	if key(t, "", trace, "customer.updateContact", args) ==
		key(t, "", trace, "customer.updateContact", other) {
		t.Error("引数が違えば別の鍵になるべき")
	}

	// 会話が違えば別の操作。
	const other2 = "22222222-2222-4222-8222-222222222222"
	if key(t, "", trace, "customer.updateContact", args) ==
		key(t, "", other2, "customer.updateContact", args) {
		t.Error("会話が違えば別の鍵になるべき")
	}

	// 操作名が違えば別。
	if key(t, "", trace, "customer.updateContact", args) ==
		key(t, "", trace, "order.cancel", args) {
		t.Error("操作が違えば別の鍵になるべき")
	}

	// **会話が無ければ鍵を作らない。** 二重送信と「もう一度やりたい」を
	// 区別できないので、区別できないものを勝手に止めない。
	if k := key(t, "", "", "customer.updateContact", args); k != "" {
		t.Errorf("traceId が無ければ鍵は空にすべき: %q", k)
	}

	// ヘッダが最優先。呼び出し側が明示したものを尊重する。
	if k := key(t, "manual-1", "", "customer.updateContact", args); k != "manual-1" {
		t.Errorf("Idempotency-Key ヘッダを優先すべき: %q", k)
	}
	if k := key(t, "manual-1", trace, "customer.updateContact", args); k != "manual-1" {
		t.Errorf("ヘッダがあれば traceId より優先すべき: %q", k)
	}
}

func TestIdempotencyKeyIgnoresArgumentOrder(t *testing.T) {
	// map の反復順は不定なので、鍵が実行ごとに変わってはいけない。
	// (encoding/json がキーをソートすることに依存している)
	const trace = "11111111-1111-4111-8111-111111111111"
	a := map[string]any{"z": 1, "a": 2, "m": 3}
	b := map[string]any{"m": 3, "z": 1, "a": 2}
	if key(t, "", trace, "x", a) != key(t, "", trace, "x", b) {
		t.Error("引数の並び順で鍵が変わってはいけない")
	}
	for i := 0; i < 20; i++ {
		if key(t, "", trace, "x", a) != key(t, "", trace, "x", a) {
			t.Fatal("同じ入力で鍵が揺れてはいけない")
		}
	}
}
