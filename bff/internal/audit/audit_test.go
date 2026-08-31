package audit

import (
	"encoding/json"
	"testing"

	"github.com/mktkhr/nlops/orchestrator/loop"
	"github.com/mktkhr/nlops/pkg/authctx"
)

func identity() authctx.Identity {
	return authctx.Identity{UserID: "u_admin", Role: authctx.RoleAdmin}
}

func TestOutcomeOf(t *testing.T) {
	tests := []struct {
		name  string
		trace *loop.Trace
		want  string
	}{
		{"エラーが最優先", &loop.Trace{Err: "boom", Proposal: &loop.Proposal{}}, "error"},
		{"提案", &loop.Trace{Proposal: &loop.Proposal{Command: "order.cancel"}}, "propose"},
		{"画面遷移", &loop.Trace{Navigate: &loop.Navigation{Route: "/orders"}}, "navigate"},
		{"通常の回答", &loop.Trace{Answer: "3 件です"}, "answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeOf(tt.trace); got != tt.want {
				t.Errorf("outcomeOf() = %q, 期待 %q", got, tt.want)
			}
		})
	}
}

func TestNullableUUID(t *testing.T) {
	// トレース ID を伴わない実行 (API を直接叩いた場合) も記録できる必要がある。
	if nullableUUID("") != nil {
		t.Error("空文字は NULL にすべき")
	}
	if nullableUUID("not-a-uuid") != nil {
		t.Error("UUID でない値は NULL にすべき (外部キー違反で監査ごと落とさない)")
	}
	id := NewTraceID()
	if nullableUUID(id) != id {
		t.Errorf("正しい UUID はそのまま通すべき: %v", nullableUUID(id))
	}
}

func TestJSONB(t *testing.T) {
	if jsonb(nil) != nil {
		t.Error("nil は NULL にすべき")
	}
	b, ok := jsonb(map[string]any{"order_id": "O-1002"}).([]byte)
	if !ok {
		t.Fatal("[]byte を返すべき")
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["order_id"] != "O-1002" {
		t.Errorf("往復で値が変わった: %v", back)
	}
}

func TestDisabledRecorderIsSafe(t *testing.T) {
	// Pool を持たない Recorder でも呼び出しで落ちてはいけない。
	// 監査が無効でもユーザーの操作は止めない、という方針の担保。
	var r *Recorder
	if r.Enabled() {
		t.Error("nil の Recorder は無効であるべき")
	}
	empty := &Recorder{}
	if empty.Enabled() {
		t.Error("Pool が nil なら無効であるべき")
	}
	empty.RecordTrace(t.Context(), NewTraceID(), identity(), &loop.Trace{})
	empty.RecordExecution(t.Context(), Execution{Command: "order.cancel"})
	empty.Close()
}
