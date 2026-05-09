# =============================================================================
# binance-oi-square-trader Makefile
# =============================================================================

.PHONY: help bootstrap dev build test typecheck lint migrate migrate-down \
        sqlc docker-up docker-down docker-logs clean

GO          ?= go
GOLANGCI    ?= golangci-lint
SQLC        ?= sqlc
MIGRATE     ?= migrate
BIN_DIR     := bin
TRADER_BIN  := $(BIN_DIR)/trader

help:
	@echo "Targets:"
	@echo "  bootstrap     首次安装依赖 (Go modules + 工具链)"
	@echo "  dev           启动 trader (testnet 默认)"
	@echo "  build         编译 binary 到 bin/"
	@echo "  test          运行测试 (含 -race)"
	@echo "  typecheck     go vet"
	@echo "  lint          golangci-lint"
	@echo "  migrate       跑 DB 迁移到最新"
	@echo "  migrate-down  回滚一次迁移"
	@echo "  sqlc          重新生成 sqlc 代码"
	@echo "  docker-up     启动基础设施 (PG/Redis/Prometheus/Grafana/Loki)"
	@echo "  docker-down   停止基础设施"
	@echo "  docker-logs   查看容器日志"
	@echo "  clean         清理 build 产物"

bootstrap:
	$(GO) mod download
	$(GO) install github.com/golang-migrate/migrate/v4/cmd/migrate@latest
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "Bootstrap done. Next: cp .env.example .env && make docker-up && make migrate"

dev:
	@if [ ! -f .env ]; then echo ".env missing. cp .env.example .env"; exit 1; fi
	$(GO) run ./cmd/trader

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(TRADER_BIN) ./cmd/trader
	@echo "Built $(TRADER_BIN)"

test:
	$(GO) test -race -timeout 120s ./...

typecheck:
	$(GO) vet ./...

lint:
	$(GOLANGCI) run ./...

migrate:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL not set, source .env first"; exit 1; fi
	$(MIGRATE) -path internal/storage/postgres/migrations -database "$(DATABASE_URL)" up

migrate-down:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL not set, source .env first"; exit 1; fi
	$(MIGRATE) -path internal/storage/postgres/migrations -database "$(DATABASE_URL)" down 1

sqlc:
	cd internal/storage/postgres && $(SQLC) generate

docker-up:
	docker compose -f deploy/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker-compose.yml down

docker-logs:
	docker compose -f deploy/docker-compose.yml logs -f --tail=100

clean:
	rm -rf $(BIN_DIR)
	$(GO) clean -cache -testcache
