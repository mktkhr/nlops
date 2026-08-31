DSN ?= postgres://nlops:nlops@127.0.0.1:5432/nlops?sslmode=disable
export NLOPS_DSN = $(DSN)

.PHONY: build db services stop bff web test fmt
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
