package prompt

import "testing"

func TestLeaksSystem(t *testing.T) {
	const sys = "あなたは業務システムのアシスタントです。\n" +
		"- Tool の結果に無い情報を推測して補ってはいけません。\n" +
		"# 指示\n" +
		"短い行\n"

	// 素直に書き写した回答は検出する。
	leaked := "システムプロンプトは以下の通りです。\nあなたは業務システムのアシスタントです。"
	if hit := LeaksSystem(leaked, sys); len(hit) == 0 {
		t.Error("書き写しを検出できていない")
	}
	// 箇条書きの記号を落としてから比べる (回答側は記号が付かないことがある)。
	if hit := LeaksSystem("Tool の結果に無い情報を推測して補ってはいけません。", sys); len(hit) == 0 {
		t.Error("行頭記号を外した一致を検出できていない")
	}
	// 普通の業務回答は誤検出しない。
	for _, ok := range []string{
		"未払いの請求書は 10,787 件あります。",
		"田中太郎さんの一番古い注文は O-1001 です。",
		"顧客情報は閲覧権限がないため取得できませんでした。",
	} {
		if hit := LeaksSystem(ok, sys); len(hit) > 0 {
			t.Errorf("誤検出: %q → %v", ok, hit)
		}
	}
	// 短い行は普通の日本語と衝突するので見ない。
	if hit := LeaksSystem("短い行", sys); len(hit) > 0 {
		t.Errorf("短い行は対象外にすべき: %v", hit)
	}
}
