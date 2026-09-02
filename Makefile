# 手元固有の設定は local.mk (追跡しない) に書く。
-include local.mk

# ローカル開発で使う PostgreSQL。自分の環境に合わせて local.mk で上書きする。
PG_CONTAINER ?= nlops-db
PG_USER      ?= nlops
PG_PASSWORD  ?= nlops
PG_DB        ?= nlops
PG_HOST      ?= 127.0.0.1
PG_PORT      ?= 5432

DSN ?= postgres://$(PG_USER):$(PG_PASSWORD)@$(PG_HOST):$(PG_PORT)/$(PG_DB)?sslmode=disable
export NLOPS_DSN = $(DSN)

PSQL = docker exec -i -e PGPASSWORD=$(PG_PASSWORD) $(PG_CONTAINER) psql -U $(PG_USER) -d $(PG_DB) -v ON_ERROR_STOP=1 -q

.PHONY: build db db-bulk services stop bff web recovery followup test fmt secrets cert up down reset logs ps
build:
	cd pkg && go build ./...
	cd services && go build -o ../bin/ ./cmd/...
	cd orchestrator && go build -o ../bin/ ./cmd/... 2>/dev/null || true
	cd eval && go build -o ../bin/ ./cmd/...
	cd bff && go build -o ../bin/ ./cmd/...

db:
	$(PSQL) < services/schema/001_schema.sql
	$(PSQL) < services/schema/002_seed.sql
	$(PSQL) < services/schema/003_audit.sql

# 実運用に近い規模のデータを足す。既存の C001-C006 等はそのまま残る。
db-bulk: db
	@echo "大量データを投入中 (数十秒かかります)..."
	$(PSQL) < services/schema/bulk/010_bulk_seed.sql

services: build
	@mkdir -p .run
	@for s in customer order inventory shipping billing; do \
		./bin/$$s > .run/$$s.log 2>&1 & echo $$! > .run/$$s.pid; done
	@sleep 1; echo "起動: 9101-9105"

bff: build
	@mkdir -p .run
	@NLOPS_DSN="$(DSN)" ./bin/bff > .run/bff.log 2>&1 & echo $$! > .run/bff.pid
	@sleep 1; echo "BFF 起動: :8080"

web:
	@cd frontend && pnpm dev

stop:
	@for s in customer order inventory shipping billing bff; do \
		[ -f .run/$$s.pid ] && kill $$(cat .run/$$s.pid) 2>/dev/null; rm -f .run/$$s.pid; done; echo stopped

# 誤った Tool を踏んだ後の回復を測る (docs/recovery-report.md)
# モックサービスの起動が必要。MODE=down のときはスタブを起動しない。
MODE ?= plausible
recovery: build
	@if [ "$(MODE)" != "down" ]; then \
		./bin/decoystub -catalog catalog/scale/services-124.json -mode $(MODE) & \
		sleep 1; \
	fi
	@./bin/recovery -mode $(MODE) || true
	@P=$$(ss -lptn 'sport = :9199' 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2); \
		[ -n "$$P" ] && kill $$P || true

# 連続した問い合わせ (追い質問) を測る。HIST=0 で履歴なしと比較できる。
followup: build
	@./bin/followup $(if $(filter 0,$(HIST)),-no-history,)

test:
	@for m in pkg services orchestrator eval bff; do (cd $$m && go test ./...); done

fmt:
	@for m in pkg services orchestrator eval bff; do (cd $$m && gofmt -l -w .); done

# ---- コンテナ構成 (元案 §3) ----
# ホストへ公開するのは Nginx (:8081) だけ。DB も BFF も各サービスも内部に閉じる。
# LLM はホストの llama-swap (:11435) をそのまま使う。

COMPOSE = docker compose -f deploy/compose.yaml

# 秘密情報は Docker secrets としてファイルで渡す。環境変数には置かない。
# deploy/secrets/ は追跡しない。
secrets:
	@mkdir -p deploy/secrets
	@if [ -s deploy/secrets/db_password ]; then echo "秘密情報は既にあります"; else \
		P=$$(head -c 24 /dev/urandom | base64 | tr -d '/+='); \
		printf '%s' "$$P" > deploy/secrets/db_password; \
		printf 'postgres://nlops:%s@db:5432/nlops?sslmode=disable' "$$P" > deploy/secrets/db_dsn; \
		chmod 700 deploy/secrets; chmod 644 deploy/secrets/*; \
		echo "deploy/secrets/ を作成しました (パスワードは乱数)"; fi
	@# ファイルは 644。コンテナは非 root で動くので読める必要がある。
	@# 保護はディレクトリの 700 が担う (他ユーザーは辿れない)。
	@chmod 700 deploy/secrets; chmod 644 deploy/secrets/* 2>/dev/null || true

# 自己署名証明書。tailnet で使うなら tailscale cert の方が正しい (下記参照)。
cert:
	@mkdir -p deploy/certs
	@if [ -s deploy/certs/fullchain.pem ]; then echo "証明書は既にあります"; else \
		openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
			-keyout deploy/certs/privkey.pem -out deploy/certs/fullchain.pem \
			-subj "/CN=$${NLOPS_HOST:-localhost}" \
			-addext "subjectAltName=DNS:$${NLOPS_HOST:-localhost},DNS:localhost,IP:127.0.0.1" \
			2>/dev/null && chmod 600 deploy/certs/privkey.pem && \
		echo "自己署名証明書を作成しました (CN=$${NLOPS_HOST:-localhost})"; fi

up: secrets
	$(COMPOSE) up -d --build
	@echo "起動: http://localhost:$${NLOPS_PORT:-8081}/"

down:
	$(COMPOSE) down

# データも消す。seed からやり直したいとき。
reset:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f --tail=50

ps:
	$(COMPOSE) ps
