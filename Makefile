BIN      := archcheck
ENV      ?= local
# 本地并行开发用的 guardx 工作副本路径（未跟踪，见 .gitignore）。
# 走 make devlink 接入 workspace，不要在 go.mod 里写 replace 指向它。
GUARDX_WORK := .guardx-work/guardx
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

.PHONY: build fmt-check vet lint test race cover cover-gate architecture-check docsync check tidy devlink devunlink clean

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

docsync: ## README 包表 ↔ 仓库实际（生产行数 / 外部依赖）一致性门禁
	$(EXPORT) $(GOENV) go run ./cmd/docsync

check: fmt-check vet test cover-gate architecture-check docsync ## 提交前门禁聚合

tidy:
	$(EXPORT) $(GOENV) go mod tidy

# ── 本地多仓并行开发（issue #1 的修法①）────────────────────────────
# 为什么不能图省事在 go.mod 里写 `replace ... => $(GUARDX_WORK)`：
# Go 的 replace 只在**主模块**生效。本仓自己是主模块 → 本地构建绿；
# 但下游（bianque 等以 `replace => ../agent-harness` 引入本仓的宿主）里
# agent-harness 是**依赖模块**，它的 replace 被静默忽略 → guardx 按 MVS
# 回落到已发布的旧版本 → 报 `no required module provides package
# github.com/oagentkit/guardx/injectband`，指向 guardx 而非真正原因。
# 一行本地 replace，代价是断掉所有下游，且只在别人构建时才暴露。
#
# workspace 的 `use` 跨仓生效且不碰任何 go.mod，是本地并行开发的正确姿势：
# 下游零影响，go.mod 保持对外契约（零 replace），也不存在「忘记删」的风险。

devlink: ## 把 guardx 工作副本接入本地 workspace（不改任何 go.mod）
	@test -d "$(GUARDX_WORK)" || { \
	  echo "找不到 $(GUARDX_WORK) —— 先把 guardx 克隆到该路径："; \
	  echo "  git clone https://github.com/oagentkit/guardx.git $(GUARDX_WORK)"; \
	  exit 1; }
	@$(EXPORT) $(GOENV) go work init . "$(GUARDX_WORK)"
	@echo "已生成 go.work：本仓 + $(GUARDX_WORK) 进入同一 workspace。"
	@echo "注意：workspace 模式下 guardx 读的是本地副本，go.mod 里写的版本被旁路。"
	@echo "验完跑 'make devunlink' 断开，否则构建会一直读本地副本。"

devunlink: ## 断开本地 workspace（删除 go.work / go.work.sum）
	@rm -f go.work go.work.sum
	@echo "已断开本地 workspace，构建回到 go.mod 声明的已发布版本。"

clean:
	rm -rf bin coverage.out
