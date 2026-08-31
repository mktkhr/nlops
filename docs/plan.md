# nlops PoC 実行計画

自然言語による業務サービス横断操作基盤の技術的成立性を検証する。
進捗はこのファイルのチェックボックスで管理する。

---

## P1. リポジトリ骨組み

- [x] リポジトリ作成 `~/ghq/github.com/mktkhr/nlops`
- [x] 計画・決定事項ドキュメント
- [x] `go.work` と 4 モジュール構成
- [x] `.gitignore` / `Makefile`

## P2. ドメインカタログ確定

Tool Registry の入力になるので、実装より先に確定させる。

- [x] 5 サービス / 24 API のカタログ定義 (`catalog/services.json`)
- [x] 権限モデル定義（role × scope）
- [x] Response Projection 定義（API ごとの返却フィールド whitelist + 件数上限）
- [x] ゴールデンセット初版（NL クエリ → 期待 service / tool / args）

## P3. 実測スパイク（コード投資前の関門）

ここで JSON 追従率が出なければ設計が変わる。最優先。

- [x] llama-swap 経由の OpenAI 互換クライアント（最小）
- [x] モデル階段の JSON Schema 追従率を測定
- [ ] prefill / TTFT / 生成速度を context 長別に測定 — **未実施**。マシンを他作業から空けて再測定が必要
- [x] `--cache-reuse` の効きを 1 段階 / 2 段階で測定（prefix 安定性の影響を確認）
- [x] 1 段階ルーティング vs 2 段階ルーティングの比較
- [x] スパイク結果を `docs/spike-report.md` に記録

## P4. モックサービス実装

- [x] Postgres `nlops` データベースと schema 作成
- [x] seed データ（権限差が出る所有関係を含む）
- [x] 5 サービスの HTTP 実装（24 API）
- [x] 各サービスで authctx による絞り込み / 403

## P5. Tool Registry + Executor

- [x] Tool 定義の共通型（`pkg/toolschema`）
- [x] カタログ読み込みと Tool → HTTP 変換
- [x] Executor（認証情報付与・user identity 伝播）
- [x] Response Projection（whitelist / 件数上限 / トークン見積り）

## P6. Orchestrator

- [x] LLM クライアント抽象（差し替え点を 1 箇所に閉じる）
- [x] Stage 1 Service Router
- [x] Stage 2 Tool Selector
- [x] Tool Execution Loop（prompt prefix 安定性を守る）
- [x] Final Response Generator
- [x] CLI `orchctl`

## P7. 評価ハーネス

- [x] ゴールデンセット採点ランナー
- [x] service routing / tool selection / argument 一致の個別スコア
- [x] 権限差の検証（同一クエリ × 異なる role で結果差を確認）
- [x] モデル階段の一括比較

## P8. 結果レポート

- [x] 計測結果を `docs/result.md` にまとめる
- [x] PoC 判定（成立 / 条件付き成立 / 不成立）と次の一手

---

## 未解決の論点

| # | 論点 | 状態 |
|---|---|---|
| 1 | ceiling を 35B-A3B(Q4_K_XL) とすることの妥当性 | **決着**: Q4_K_XL と IQ4_XS で差ゼロ。天井は IQ4_XS で足りる |
| 2 | 2 段階ルーティングが PoC 規模で割に合うか | **決着**: 割に合わない。prefix cache を壊すので 1 段階を既定に |
| 7 | 1 段階が破綻する Tool 数 | **モデル依存**。Gemma 4 12B は 504 Tool でも 100%、Qwen 35B-A3B は 304 Tool 付近から崩れる |
| 8 | strict スキーマのコスト | **決着**: 124 Tool で +30%、504 Tool で **+111〜123%**。Executor 側検証へ逃がせば loose のレイテンシは Tool 数にほぼ非依存 |
| 3 | 小型 dense が MoE 35B-A3B を精度タスクで上回るか | **決着**: Gemma 4 12B が 40/40 で最高成績 |
| 4 | context 削減と cache-reuse のトレードオフ | **決着**: 履歴圧縮は禁止。Projection のみで削減する |
| 5 | gpt-oss 20B が Loop で成立しない原因 | **未解決**。harmony 形式と制約デコードの相互作用を疑っている |
| 6 | Projection の精度への寄与 | **未検証**。結果集合が小さくコスト差 (+23% tok) しか観測できていない |

## P9. スケール検証

- [x] ダミーサービス定義 (`catalog/decoys.json`) と展開ツール (`eval/cmd/mkcatalog`)
- [x] 24 〜 504 Tool のカタログ生成 (テンプレート生成のバルクダミーを追加)
- [x] 実サービスをプロンプト全体へ等間隔に分散させる
- [x] 2 モデル × 2 モードで Tool 選定精度とレイテンシを測定 (マシンを空けて実施)
- [x] 124 / 504 Tool での strict / loose 比較
- [x] 結果を `docs/scale-report.md` に記録
- [ ] 504 Tool を超える領域 — **未測定**
- [ ] 互いに紛らわしい Tool を 500 個用意した場合 — **未検証** (バルクはテンプレート生成)
- [ ] ダミー Tool を選んでしまったときの Loop の回復挙動 — **未検証**

## P10. BFF と Web UI

- [x] 過剰探索の抑制 (プロンプトのルール + 空振り連続時の finish 強制)
- [x] Go BFF — SSE で Tool Loop の進捗を配信
- [x] BFF の Aggregation (注文一覧に顧客名を合成)
- [x] React + MUI の最小 UI (アシスタント / 注文 / 顧客)
- [x] ユーザー切り替えによる権限差の確認
- [x] §14 UI State 生成 (`{route, filters}`) — Tool Loop の分岐として実装
- [x] 画面カタログ (`catalog/routes.json`) と遷移先・フィルタの文法制約
- [x] 画面のフィルタを URL クエリへ移行 (LLM の出力をそのまま反映でき、URL 共有も可能に)
- [ ] モバイル幅での表示確認 — **未確認**
- [x] navigate の精度測定 — ゴールデンセットを 150 件へ拡充し navigate 25 件を新設
- [x] Intent Gate (navigate / tool の 2 択判定) — navigate 正答 72% → 100%
- [x] 遷移先の画面を実際に開いての権限検証
- [x] Intent Gate のコスト分析 — キャッシュ奪い合い説は**誤り**だった。
      約 500ms は 1 リクエスト分の固定費で、`--parallel 2` は不要
- [x] 過剰探索抑制 — exploration カテゴリ 15 件を新設して測定できるようにした (15/15)

## P11. ゴールデンセット拡充と Intent Gate

- [x] ゴールデンセットを 40 → **150 件**へ (navigate 25 / exploration 15 を新設)
- [x] 採点に画面遷移とステップ数上限を追加
- [x] Intent Gate を実装し A/B 測定
- [x] 遷移先の画面を開いての権限検証
- [x] 全 150 件で **99.3%** (失敗 1 件: D22「出荷日」で order.get を選択)
- [x] 150 件でのモデル階段の再測定 — Gemma 99% に対し他モデルは 77〜83%。
      差は Tool 選定ではなく「いつ止めるか」で開いた
- [x] Intent Gate の出力短縮 (600ms → 約 500ms、精度は不変)

## P12. WRITE Command Proposal (§13)

- [x] `catalog/commands.json` — 提案してよい操作 3 つ
- [x] 各サービスの更新 API と業務ルール (キャンセル可否など)
- [x] 参照とは別の書き込み権限 (`authctx.writeMatrix`)
- [x] Loop の `propose` 分岐。LLM は提案までで実行しない
- [x] Intent Gate を 3 値 (navigate / tool / write) へ拡張 — 既存カテゴリの劣化なし
- [x] BFF の実行エンドポイント (カタログ再検証 + 権限確認)
- [x] 画面の確認 UI (実行 / 取り消し、拒否理由の表示)
- [x] write カテゴリ 20 件を追加し 95%
- [x] 提案の監査ログ → P13 で実装
- [ ] 複数ステップの更新 (1 提案 = 1 操作に限定している) — **未対応**

## P13. 監査と Observability (§21)

- [x] `audit` schema (traces / trace_steps / command_executions)
- [x] BFF がトレースを永続化。判定・Tool・引数・Projection 済み結果・レイテンシ
- [x] 更新の承認記録。**拒否された試行も残す**
- [x] 提案と承認の紐づけ (traceId を画面から返す)
- [x] 監査閲覧 API と画面 (admin のみ)
- [x] 監査が無効なら WARN、不正な DSN なら起動時に落ちる
- [ ] トレースからゴールデンセットを起こす導線 — **未着手**
- [ ] 保持期間とローテーション — **未検討**
- [ ] 変更前の値の記録 — **未対応**。現状は引数と実行後の状態のみ

---

## 判定

**成立**。詳細は [result.md](result.md)、実測の経緯は [spike-report.md](spike-report.md)、
スケール検証は [scale-report.md](scale-report.md)。

次フェーズの優先順位は result.md の「次の一手」を参照。
