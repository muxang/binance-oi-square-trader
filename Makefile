# =============================================================================
# binance-oi-square-trader Makefile
# =============================================================================

.PHONY: help bootstrap dev build test test-race typecheck lint migrate migrate-down \
        sqlc sqlc-check e2e-phase1 docker-up docker-down docker-logs clean

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
	@echo "  test          运行测试 (无 race detector, Windows 直连可用)"
	@echo "  test-race     运行测试 + race detector (Windows 自动经 WSL)"
	@echo "  typecheck     go vet"
	@echo "  lint          golangci-lint"
	@echo "  migrate       跑 DB 迁移到最新"
	@echo "  migrate-down  回滚一次迁移"
	@echo "  sqlc          重新生成 sqlc 代码"
	@echo "  sqlc-check    CI=true 时验证 generated 跟 source 同步; 否则 skip"
	@echo "  e2e-phase1    Phase 1 端到端测试 (1.10 实现)"
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
	$(GO) test -timeout 120s ./cmd/... ./internal/...

test-race:
ifeq ($(OS),Windows_NT)
	wsl -e bash -lc "cd $$(wslpath -a '$(CURDIR)') && go test -race -timeout 180s ./cmd/... ./internal/..."
else
	$(GO) test -race -timeout 180s ./cmd/... ./internal/...
endif

typecheck:
	$(GO) vet ./cmd/... ./internal/...

lint:
	$(GOLANGCI) run ./cmd/... ./internal/...

migrate:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL not set, source .env first"; exit 1; fi
	$(MIGRATE) -path internal/storage/postgres/migrations -database "$(DATABASE_URL)" up

migrate-down:
	@if [ -z "$(DATABASE_URL)" ]; then echo "DATABASE_URL not set, source .env first"; exit 1; fi
	$(MIGRATE) -path internal/storage/postgres/migrations -database "$(DATABASE_URL)" down 1

sqlc:
	cd internal/storage/postgres && $(SQLC) generate

# sqlc-check: CI-aware. Locally: prints a hint and skips. In CI (CI=true):
# regenerates and asserts no diff. Skips entirely if no .sql queries exist
# yet (Phase 1.0 — queries land in 1.1+).
# Single shell so guard `exit` short-circuits the rest of the recipe (Make
# runs each line in its own shell otherwise).
sqlc-check:
	@if [ "$$CI" != "true" ]; then \
		echo "sqlc-check: skipped (set CI=true to run)"; \
	elif [ -z "$$(find internal/storage/postgres/queries -name '*.sql' 2>/dev/null)" ]; then \
		echo "sqlc-check: no .sql queries yet, skip"; \
	else \
		cd internal/storage/postgres && $(SQLC) generate && cd - >/dev/null && \
		if [ -n "$$(git status --porcelain internal/storage/postgres/gen/)" ]; then \
			echo "ERROR: generated sqlc code out of sync. Run 'make sqlc' and commit."; \
			git status --short internal/storage/postgres/gen/; \
			exit 1; \
		fi; \
	fi

# e2e-phase1 placeholder. 1.10 fills in real test/e2e/phase1_test.go content.
e2e-phase1:
	@if [ ! -d test/e2e ] || [ -z "$$(find test/e2e -name '*.go' 2>/dev/null)" ]; then \
		echo "e2e-phase1: no tests yet (1.10 will populate test/e2e/)"; \
		exit 0; \
	fi
	$(GO) test -tags=e2e -timeout 600s ./test/e2e/...

docker-up:
	docker compose -f deploy/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker-compose.yml down

docker-logs:
	docker compose -f deploy/docker-compose.yml logs -f --tail=100

clean:
	rm -rf $(BIN_DIR)
	$(GO) clean -cache -testcache
