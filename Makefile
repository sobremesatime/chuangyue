# 窗月 · 统一构建入口
# Windows 下可通过 WSL / Git Bash 使用，或安装 make for Windows。

MODULE := github.com/sobremesatime/chuangyue
COMPOSE := docker compose -f deploy/compose/docker-compose.yml

.PHONY: help tidy fmt vet build test lint proto openapi run up down logs clean

help: ## 显示所有可用命令
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

tidy: ## 整理依赖
	go mod tidy

fmt: ## 格式化代码
	gofmt -w .

vet: ## 静态检查
	go vet ./...

build: ## 编译所有服务
	go build ./...

test: ## 运行测试
	go test ./... -race -cover

lint: ## 运行 golangci-lint
	golangci-lint run

proto: ## 由 proto 契约生成 Go 代码
	bash scripts/gen_proto.sh

openapi: ## 由 OpenAPI 契约生成前端类型
	bash scripts/gen_openapi.sh

run: ## 启动网关（当前版本，读取 configs/dev/config.yaml）
	go run ./cmd/gateway -c configs/dev/config.yaml

up: ## 启动本地依赖（MySQL / Redis / Kafka / Etcd / MinIO）
	$(COMPOSE) up -d

down: ## 停止本地依赖
	$(COMPOSE) down

logs: ## 查看本地依赖日志
	$(COMPOSE) logs -f

clean: ## 清理构建产物
	go clean -cache -testcache
