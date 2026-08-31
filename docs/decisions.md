# 決定事項

すり合わせで確定した内容。元の技術スタック案からの変更点を含む。

## 確定

| # | 項目 | 決定 |
|---|---|---|
| D1 | PoC 範囲 | Orchestrator 優先。モック Microservice 群 + CLI で検証し、BFF / React / Nginx は成立確認後 |
| D2 | 実行環境 | RTX 4080 SUPER 16GB / llama.cpp + llama-swap (:11435) |
| D3 | ceiling 測定 | 外部 API を使わず、ローカルで動く最大構成を上限の代理指標とする |
| D4 | Tool 定義 | 自前 Tool Registry（MCP 非採用） |
| D5 | リポジトリ | Monorepo / Go workspaces |
| D6 | 権限 | Authorization Context の伝播に加え、**権限差の検証まで PoC に含める** |
| D7 | モック規模 | 5 サービス / 24 API（order↔billing の境界を意図的に曖昧にする） |
| D8 | LLM 構成 | context を削って量子化ビット数を優先する |
| D9 | データストア | 既存 Postgres 18.3 (:5432) に別データベース `nlops` を作成 |
| D10 | 実装言語 | Orchestrator・モックサービス・評価ハーネスすべて Go 一本 |
| D11 | ルーティング段数 | **1 段階に確定**。504 Tool まで比較計測した結果、2 段階は常に遅く精度も同等以下 |
| D12 | 引数の妥当性検査 | 〜124 Tool は strict スキーマ。それ以上は **loose + Executor 側の enum 検証**へ切り替える（504 Tool で strict の文法コストが 2 倍以上） |
| D13 | モデル | **Gemma 4 12B (QAT q4_0) に確定**。504 Tool でも 100%。MoE の 35B-A3B は 90% まで落ちる |

## 元案からの主な変更

### C1. Response Projection を設計の中心に置く

元案には項目自体がなかったが、実 API の JSON をそのまま context に戻すと小型モデルは破綻する。
Tool 定義ごとに「LLM へ返すフィールドの whitelist」「最大件数」「ネスト展開の深さ」を必須項目とする。

### C2. context 削減は「履歴の後処理」ではなく「Tool Result 追加時点の Projection」で行う

`--cache-reuse 256` が有効なため、prompt prefix をバイト単位で安定させる必要がある。
途中で履歴を要約圧縮すると prefix が壊れてフル再 prefill になり、レイテンシが跳ねる。

Orchestrator が守る制約:

- system prompt と Tool 定義の順序を固定する（map の iteration 順で並べない）
- prefix に現在時刻・ランダム ID・リクエスト ID を入れない
- 履歴は append のみ

### C3. モデル階段は単調ではない

`Qwen3.6-35B-A3B` は active 3B の MoE。総パラメータは 35B だが 1 トークンあたりに通る重みは 3B 相当。
Tool 選定と引数抽出は精度タスクなので、dense `Qwen3-14B Q4_K_M` (active 14B / 4.8bpw) が
`35B-A3B IQ3_S` (active 3B / 3.4bpw) を上回る可能性がある。上限を決め打ちせず測定する。

### C4. ルーティング段数を測定対象に格上げ

PoC 規模（5 サービス / 24 API）では Stage 1 の追加往復がレイテンシを支配しうる。
2 段階を前提とせず、1 段階と同一条件で比較する。

## モデル階段（llama-swap に既存の構成を利用）

| 段 | モデル ID | 構成 |
|---|---|---|
| 上限候補 A | `qwen36-35b-q4kxl-greedy` | UD-Q4_K_XL / ncmoe=17 / KV q8_0 / temp 0 |
| 上限候補 B | `qwen36-35b-iq4xs-greedy` | UD-IQ4_XS / ncmoe=10 / KV q8_0 / temp 0 |
| 現行 | `qwen36-35b-iq3s-greedy` | UD-IQ3_S / オフロードなし / temp 0 |
| tool use 対抗 | `gpt-oss-20b` | MXFP4 / harmony ネイティブパーサ |
| dense 対抗 | `qwen3.5-9b` / `qwen3.5-9b-q8` | Q4_K_M / Q8_0 |
| 下限 | `qwen3.5-4b` | Q4_K_M |
| 参考 | `gemma4-12b` | tool-call ネイティブハンドラなし（Generic フォールバック） |

ローカル GGUF に `Qwen3-14B-Q4_K_M` / `Qwen3-8B-Q4_K_M` / `Qwen3-4B-Instruct-2507-Q4_K_M` もあり、
dense 14B を階段に加える場合は llama-swap に構成を追加する。

## 保留

- **ceiling の妥当性**: ローカル最大構成で失敗した場合、「アーキが悪い」のか「まだモデルが小さい」のか
  切り分けられない。LLM クライアントを OpenAI 互換インターフェース 1 枚に閉じ込め、切り分けが
  必要になった時点でホスト型モデルへ向けられる状態だけ確保しておく（使うかは後で判断）。

## スケール検証で決着した論点 (2026-08-31, 24〜504 Tool)

元案 §7〜§9 の 2 段階ルーティング (Stage 1 Service Router → Stage 2 Tool Selector) は
**削除してよい**。24〜504 Tool の全域で、`loose` + Executor 検証の 1 段階が
精度・レイテンシとも 2 段階以上だった。

前提だった「全 API 定義を context に入れられない」は prefix cache のある環境では成立しない。
Tool 定義は約 48 tok/Tool で線形に増えるが (504 Tool = 24,363 tok)、
system prompt が固定である限り prefill はキャッシュが吸収する。
**`loose` なら 124 → 504 Tool でレイテンシが +7% しか増えない。**

代わりに効いてくるのは 2 つ。

1. **`anyOf` 分岐の GBNF 文法コスト** — 124 Tool で +30%、504 Tool で **+111〜123%**。
   Executor 側の検証 (`invalidEnums` / `sanitizeArgs`) へ逃がせる。
2. **モデルの限界** — Gemma 4 12B は 504 Tool でも 100%、
   Qwen3.6-35B-A3B は 304 Tool 付近から崩れて 504 Tool で 90%。
   **「何 Tool まで大丈夫か」はアーキテクチャではなくモデルの性質だった。**

詳細は [scale-report.md](scale-report.md)。
