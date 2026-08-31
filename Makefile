DSN ?= postgres://nlops:nlops@127.0.0.1:5432/nlops?sslmode=disable
export NLOPS_DSN = $(DSN)

.PHONY: build db services stop bff web test fmt env cert up down reset logs ps
build:
	cd pkg && go build ./...
	cd services && go build -o ../bin/ ./cmd/...
	cd orchestrator && go build -o ../bin/ ./cmd/... 2>/dev/null || true
	cd eval && go build -o ../bin/ ./cmd/...
	cd bff && go build -o ../bin/ ./cmd/...

db:
	docker exec -i -e PGPASSWORD=nlops nlops-db psql -U nlops -d nlops -v ON_ERROR_STOP=1 -q < services/schema/001_schema.sql
	docker exec -i -e PGPASSWORD=nlops nlops-db psql -U nlops -d nlops -v ON_ERROR_STOP=1 -q < services/schema/002_seed.sql
	docker exec -i -e PGPASSWORD=nlops nlops-db psql -U nlops -d nlops -v ON_ERROR_STOP=1 -q < services/schema/003_audit.sql

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

test:
	@for m in pkg services orchestrator eval bff; do (cd $$m && go test ./...); done

fmt:
	@for m in pkg services orchestrator eval bff; do (cd $$m && gofmt -l -w .); done

# ---- コンテナ構成 (元案 §3) ----
# ホストへ公開するのは Nginx (:8081) だけ。DB も BFF も各サービスも内部に閉じる。
# LLM はホストの llama-swap (:11435) をそのまま使う。

COMPOSE = docker compose -f deploy/compose.yaml

# 秘密情報は .env に置き、リポジトリには入れない。
env:
	@if [ -f deploy/.env ]; then echo "deploy/.env は既にあります"; else \
		sed "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$$(head -c 24 /dev/urandom | base64 | tr -d '/+=' )|" \
			deploy/.env.example > deploy/.env; \
		chmod 600 deploy/.env; \
		echo "deploy/.env を作成しました (パスワードは乱数)"; fi

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

up: env
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
