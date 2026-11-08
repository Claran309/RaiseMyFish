# RaiseMyFish · 开发命令
# 用法：make help

SHELL := /bin/bash
BINARY := fishd
CTL := fishctl
GOFLAGS := -trimpath
COMPOSE := docker compose -f deployment/docker-compose.yml

.PHONY: help build run test test-race lint fmt vet migrate-up migrate-down dev-status clean docker-up docker-down docker-logs docker-migrate

help: ## 显示所有命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## 构建 fishd 与 fishctl
	go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/fishd
	go build $(GOFLAGS) -o bin/$(CTL) ./cmd/fishctl

run: ## 本地运行主服务（需已配置 .env）
	go run ./cmd/fishd

test: ## 跑全部测试
	go test ./...

test-race: ## 带竞态检测的测试（CI 用）
	go test -race ./...

lint: ## golangci-lint
	golangci-lint run ./...

fmt: ## 格式化
	gofmt -w .
	goimports -w .

vet: ## go vet
	go vet ./...

migrate-up: ## 应用数据库迁移（需 FISH_DB_DSN）
	go run ./cmd/fishd migrate up

migrate-down: ## 回滚一步迁移
	go run ./cmd/fishd migrate down 1

dev-status: ## 查看各组件状态
	./bin/$(CTL) status

# ── Docker（交付 / 全栈）────────────────────────────────────
docker-up: ## 构建并启动全部服务（首次会自动跑迁移）
	$(COMPOSE) up -d --build

docker-down: ## 停止全部服务（保留数据卷）
	$(COMPOSE) down

docker-logs: ## 跟随 fishd 日志
	$(COMPOSE) logs -f fishd

docker-migrate: ## 手动应用数据库迁移
	$(COMPOSE) run --rm migrate

clean: ## 清理构建产物
	rm -rf bin/
