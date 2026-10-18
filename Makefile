BIN      := archcheck
ENV      ?= local
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
# EXPORT：2026-10-18 起本仓依赖全部为 public 模块（github.com/oagentkit/*、
# github.com/oekit/*），门面 agentkit 已退役并从 go.mod/go.sum 移除。
# 曾用于直拉私有模块的 GOPRIVATE/GONOSUMCHECK 一并清理——本仓是公开仓，
# 构建脚本里不该残留任何私有域名。万一来日确需私有模块，在此按需追加。
EXPORT   :=
# macOS 新 SDK 与 CLT linker 不兼容 + 许可未接受：一切 go build/test 带 CGO_ENABLED=0。
GOENV    := CGO_ENABLED=0
# cover-gate 覆盖率阈值（内核仓门禁应随成熟度上调；参照宿主仓做法设真阈值而非永不红）。
COV_MIN  := 60

.PHONY: build fmt-check vet lint test cover cover-gate architecture-check check tidy clean

build: ## 编译架构门禁二进制
	$(EXPORT) $(GOENV) go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BIN) ./cmd/$(BIN)

fmt-check:
	@files=$$(gofmt -l . | grep -v '^\.' || true); \
	if [ -n "$$files" ]; then echo "gofmt 未格式化文件:"; echo "$$files"; exit 1; fi

vet:
	$(EXPORT) $(GOENV) go vet ./...

lint: ## 静态检查（未安装时明确报错退出，与宿主仓同口径）
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint 未安装：brew install golangci-lint"; exit 1; }
	$(EXPORT) $(GOENV) golangci-lint run ./...

test:
	$(EXPORT) $(GOENV) go test ./... -count=1

race:
	$(EXPORT) $(GOENV) go test ./... -race -count=1

cover:
	$(EXPORT) $(GOENV) go test ./... -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -1

cover-gate: ## 覆盖率阈值门禁（阈值应随内核成熟度上调，不设则门禁形同虚设）
	$(EXPORT) $(GOENV) go test ./... -coverprofile=coverage.out -count=1 >/dev/null
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print substr($$3, 1, length($$3)-1)}'); \
	echo "harness 覆盖率: $$total% (门槛 $(COV_MIN)%)"; \
	awk -v t="$$total" -v m="$(COV_MIN)" 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }' || \
	{ echo "覆盖率 $$total% 低于阈值 $(COV_MIN)%"; exit 1; }

architecture-check: ## 架构治理门禁（域词污染红线 + 依赖方向）
	$(EXPORT) $(GOENV) go run ./cmd/archcheck $(if $(findstring --report,$(MAKECMDGOALS)),--report,)

check: fmt-check vet test cover-gate architecture-check ## 提交前门禁聚合

tidy:
	$(EXPORT) $(GOENV) go mod tidy

clean:
	rm -rf bin coverage.out
