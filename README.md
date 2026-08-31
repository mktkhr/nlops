# nlops

自然言語による業務サービス横断操作基盤の PoC。

LLM に業務ロジックを持たせず、**自然言語を既存システムの操作・検索へ変換する
Input Adapter** として使えるかを検証する。

- 計画と進捗: [docs/plan.md](docs/plan.md)
- 決定事項: [docs/decisions.md](docs/decisions.md)
- スパイク結果: [docs/spike-report.md](docs/spike-report.md)
- PoC 判定: [docs/result.md](docs/result.md)
- スケール検証: [docs/scale-report.md](docs/scale-report.md)

## 構成

```text
Browser (React + MUI)
  ↓  /api/*  (Nginx が静的配信とリバースプロキシ。開発時は Vite の proxy)
BFF ──────────  認証 / DTO 変換 / Aggregation / SSE で進捗を配信
  ↓
Orchestrator ── Tool Registry (catalog/services.json)
  │               「Tool 名 → HTTP」の対応は Executor だけが持つ
  ↓
Executor ─────  認証情報を付与 / Response Projection
  ↓
Microservices (customer / order / inventory / shipping / billing)
  ↓
PostgreSQL (schema をサービスごとに分離)
```

LLM が触れないもの: URL / 認証情報 / SQL / ドメインルール / 更新系 API。

## リポジトリ

```text
catalog/          サービスと Tool の定義 (LLM に見せる情報と Executor だけが知る情報)
  services.json     実サービス 5 / 24 Tool
  routes.json       LLM が遷移先として選べる画面とフィルタ (§14)
  commands.json     LLM が提案してよい更新操作 (§13)。実行はしない
  decoys.json       スケール検証用の手書きダミー 10 サービス / 100 Tool (責務が隣接)
  decoys-bulk.json  テンプレート生成用の 45 ドメイン (context への圧力担当)
  scale/            mkcatalog が生成する 44〜504 Tool のカタログ
pkg/              共有パッケージ
  toolschema/       カタログの型と読み込み
  llm/              OpenAI 互換クライアント (モデル差し替えの唯一の接点)
  prompt/           プロンプトと JSON Schema の組み立て
  authctx/          ユーザー識別情報の伝播
orchestrator/     Orchestrator
  executor/         Tool → HTTP 変換 / 認証付与 / Projection / 未解決 ID の差し戻し
  loop/             Tool Execution Loop
  cmd/orchctl/      CLI
deploy/           コンテナ構成 (Nginx + BFF + 5 サービス + PostgreSQL)
services/         モックマイクロサービス (5 サービス / 24 API)
bff/              Backend For Frontend (Presentation / Orchestration のみ)
  internal/audit/   トレースと更新承認の永続化 (audit schema を所有)
frontend/         React + MUI (pnpm + Vite+)
  src/app/          シェルとルーティング
  src/features/     assistant / order / customer / audit
  src/shared/       api / ui / user
eval/             評価ハーネス
  golden/cases.json ゴールデンセット (170 ケース / 8 カテゴリ)
  cmd/spike/        初手 Tool 選定だけを測る (サービス起動不要)
  cmd/evalrun/      Tool Loop 全体を測る (サービス起動が必要)
  cmd/mkcatalog/    スケール検証用カタログの生成
```

## 動かす

### コンテナで動かす (元案 §3 の構成)

```sh
make up      # ビルドして起動。http://localhost:8081/
make logs
make down    # 停止 (データは残る)
make reset   # データも消して seed からやり直す
```

ホストへ公開するのは Nginx の 8081 だけで、DB も BFF も各サービスも内部ネットワークに
閉じる。DB は初回起動時に `services/schema/` の SQL が順に流れる。
LLM はホストの llama-swap (`:11435`) をそのまま使う (VRAM を二重に消費しないため)。

ポートを変えるなら `NLOPS_PORT=9000 make up`。

`make up` は `deploy/.env` が無ければ乱数パスワードで作る。このファイルは追跡しない。
**`POSTGRES_PASSWORD` を後から変えた場合は `make reset` が必要。**
PostgreSQL はパスワードを初回起動時にしか適用しないので、ボリュームが残っていると
古いパスワードのままになり、BFF が起動時に落ちる。

### HTTPS

証明書を `deploy/certs/` に置くと自動で HTTPS が有効になる (無ければ HTTP のみ)。

```sh
# 自己署名。ブラウザは警告を出す。
NLOPS_HOST=your-host make cert && make up

# Tailscale 上で使うなら、こちらが正しい (本物の証明書が得られる)
sudo tailscale cert --cert-file deploy/certs/fullchain.pem \
                    --key-file  deploy/certs/privkey.pem \
                    "$(tailscale status --json | jq -r .Self.DNSName | sed 's/\.$//')"
sudo chown "$USER" deploy/certs/*
make up
```

既定のポートは HTTP 8081 / HTTPS 8443。

### ローカルで直接動かす (開発時)

## 前提

- Go 1.26 以上
- Docker で PostgreSQL 18 (`nlops-db` コンテナ、ユーザー `nlops`)
- llama.cpp / llama-swap が OpenAI 互換で `:11435` を提供していること
- コンテナで動かす場合は Docker と compose v2 以上
- 既定モデルは `gemma4-12b` (Gemma 4 12B QAT q4_0)。選定根拠は [docs/scale-report.md](docs/scale-report.md)

## 使い方

```sh
# 初回のみ: DB の作成と seed
make db

# モックサービスの起動 (9101-9105)
make services

# BFF (:8080) と Web UI (:5173)
make bff
make web

# 単発の問い合わせ
./bin/orchctl -user u_admin "田中太郎さんの未発送の注文を確認したい"

# 画面遷移で答える例 (Tool を実行せず 1.3〜2 秒で返る)
./bin/orchctl -user u_admin "西日本の顧客の一覧を開いて"

# 権限差を見る (同じ問い合わせ、違うユーザー)
./bin/orchctl -user u_sales_e "田中という名前の顧客を探して"
./bin/orchctl -user u_sales_w "田中という名前の顧客を探して"
./bin/orchctl -user u_wh      "田中という名前の顧客を探して"   # 403

# 評価
./bin/spike                                      # 初手のみ (サービス不要)
./bin/evalrun                                    # Tool Loop 全体
make test                                        # 単体テスト

# スケール検証
./bin/mkcatalog                                                  # カタログ生成
./bin/mkcatalog -sizes 124,204,304,404,504
./bin/spike -catalog catalog/scale/services-504.json -models gemma4-12b -schemas loose

make stop            # サービスと BFF を停止
```

Web UI は `http://localhost:5173/`。右上のユーザー切り替えで権限差をそのまま確認できる。

> **認証は実装していない。** ユーザー切り替えは `X-Nlops-User-Id` ヘッダを変えているだけで、
> 誰でも管理者として全データを参照できる。権限差を見せるための PoC 用の作り。

### 主なフラグ

| フラグ | 既定 | 用途 |
|---|---|---|
| `-model` / `-models` | `gemma4-12b` | llama-swap のモデル ID |
| `-mode` / `-modes` | `one_stage` | `one_stage` / `two_stage` |
| `-reasoning` | `none` | `reasoning_effort`。gpt-oss 系は `low` が必要 |
| `-strict` | `true` | Tool ごとに引数スキーマを固定する |
| `-no-projection` | `false` | Response Projection を切って比較する |
| `-no-guard` | `false` | 未解決 ID の差し戻しを切って比較する |
| `-intent-gate` | `true` | Loop の前に navigate / tool を 2 択で判定する |
| `-catalog` | `catalog/services.json` | 使用するカタログ (スケール検証で差し替える) |
| `-json` | `false` | 実行トレースを JSON で出す |
| `-audit-dsn` | 空 | BFF の監査 DB。空なら `NLOPS_DSN`、それも無ければ記録しない |

### 環境変数

| 変数 | 用途 |
|---|---|
| `NLOPS_DSN` | 各サービスと BFF の DB 接続先 |
| `NLOPS_BASE_<SERVICE>` | カタログの `base_url` を上書きする（例: `NLOPS_BASE_ORDER=http://order:9102`）。接続先はデプロイ構成であって Tool 定義ではないため |
| `NLOPS_PORT` | コンテナ構成で Nginx を公開するホストポート（既定 8081） |

## ユーザーと権限

`catalog/roles.json` で定義。

| ユーザー | ロール | 参照できる範囲 |
|---|---|---|
| `u_admin` | admin | 全サービス全件 |
| `u_sales_e` | sales (EAST) | 東日本の顧客と、その顧客の注文・配送・請求。在庫は全件 |
| `u_sales_w` | sales (WEST) | 西日本のみ。他は同上 |
| `u_wh` | warehouse | 在庫のみ。顧客・注文・配送・請求は 403 |
| `u_support` | support | 顧客・注文・配送・在庫は全件。請求は 403 |

## 設計上の制約

Orchestrator を触るときに壊してはいけない前提。

1. **生の API レスポンスを context へ入れない。** 必ず
   `catalog/services.json` の `projection` を通す。
2. **prompt prefix をバイト単位で安定させる。** `--cache-reuse` が効かなくなると
   全再 prefill でレイテンシが跳ねる。
   - system prompt と Tool 定義の順序を固定する (map の iteration 順で並べない)
   - prefix に現在時刻・ランダム ID・リクエスト ID を入れない
   - 履歴は append のみ。途中の要約圧縮をしない
3. **LLM に URL と認証情報を渡さない。** Tool 名と引数だけを出させ、
   HTTP の組み立ては `orchestrator/executor` が行う。
4. **READ-only。** 更新系 Tool はカタログに載せない。画面遷移も
   `catalog/routes.json` に定義した画面とフィルタしか生成できない。
5. **BFF に Domain Logic を置かない。** 業務ルール・Validation・データ整合性は
   Microservice の責務。BFF は Presentation と Orchestration に限る。
