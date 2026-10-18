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

# fmt-check 的文件集由 git 决定，不由 `.` 递归决定。
#
# 为什么不用 `gofmt -l .`：devlink 会在仓内留下 `.*-work/`（当前 .toolgate-work
# 174MB），里面是 Go 官方与各库的 testdata——**故意写坏语法的样本**。gofmt 扫到
# 它们会刷几十行 expected type found ')' 的 stderr，而 `grep -v '^\.'` 只能过滤
# stdout，接不住 stderr。实测 fmt-check 被拖到 12 秒，真违规淹没在噪音里。
#
# git ls-files 覆盖「已跟踪」，--others --exclude-standard 覆盖「新写但没 add」
#（如本仓测试夹具），两者合起来正好等于「gitignore 之外的全部 Go 源码」——
# 第三方工作副本已被 .gitignore 收走，天然不在文件集里。CI 里两者都在版本库中，
# 覆盖面不变。
fmt-check:
	@files=$$(gofmt -l $$(git ls-files '*.go'; git ls-files --others --exclude-standard '*.go') || true); \
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

# ── 仓库卫生门禁（issue #1 的修法②）────────────────────────────────
# 这两条原先只挂在 ci.yml 上，于是本地 `make check` 跑不到——门禁要等推到远端
# 才说话，等于把「提交前」变成了「推送后」。搬进 check，与其余门禁同一条路径。
# 逻辑原样搬，未重写：重写会重新引入下面那条已被反向验证排除的漏报。

modreplace: ## go.mod 不得含 replace（本仓是公开 module）
	@# replace 只在**主模块**生效：本仓自己是主模块 → 本地全绿；下游把它当依赖
	@# → 该 replace 被静默忽略 → 按 MVS 回落旧版本 → 报 no required module
	@# provides package ...，错指依赖而非真正原因（见 issue #1）。
	@if grep -nE '^[[:space:]]*replace' go.mod; then \
	  echo "go.mod 含 replace 指令。本仓是公开 module，本地路径的 replace 一旦入库，"; \
	  echo "所有外部 go get 都会失败。本地并行开发请用 'make devlink' 走 go.work。"; \
	  exit 1; \
	fi
	@echo "go.mod 无 replace ✓"

modwork: ## go.work / go.work.sum 不得入库（只应存在于本机）
	@# go.work 会让 clone 下本仓的人进入 workspace 模式（go.mod 的版本解析被整体
	@# 旁路），而那些 use 的本机路径在别处不存在，报错与真实原因毫无关联。
	@# 判据是「是否被 git 跟踪」而非「文件是否存在」：本地生成未提交才是正确状态。
	@#
	@# 注意别写成 `git ls-files --error-unmatch go.work go.work.sum`：
	@# --error-unmatch 是「任一 pathspec 没命中就整体非零退出」，于是
	@# 「go.work 已入库、go.work.sum 没入库」这个最常见的组合会被**放行**。
	@# 该写法已实测漏报（反向验证时才发现），故用无 --error-match 的
	@# `git ls-files -- <paths>`，输出为空即未跟踪。
	@tracked=`git ls-files -- go.work go.work.sum`; \
	if [ -n "$$tracked" ]; then \
	  echo "go.work / go.work.sum 被 git 跟踪：$$tracked"; \
	  echo "本地并行开发用 make devlink 生成的文件只应存在于本机。"; \
	  echo "请 git rm --cached go.work go.work.sum，并确认 .gitignore 已覆盖。"; \
	  exit 1; \
	fi
	@echo "go.work 未入库 ✓"

check: fmt-check vet test cover-gate architecture-check docsync modreplace modwork ## 提交前门禁聚合

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

# 已有 go.work 时只**追加**不重建：重建会抹掉别人已加的仓，
# 而「再跑一次 devlink」本该是无操作（幂等），不是一次事故。
# （注：续行块内部不能放 # 注释——shell 会连同反斜杠一起注释掉，
#   报 "syntax error: unexpected end of file"。注释一律写在块外。）
devlink: ## 把 guardx 工作副本接入本地 workspace（不改任何 go.mod，可重复执行）
	@test -d "$(GUARDX_WORK)" || { \
	  echo "找不到 $(GUARDX_WORK) —— 先把 guardx 克隆到该路径："; \
	  echo "  git clone https://github.com/oagentkit/guardx.git $(GUARDX_WORK)"; \
	  exit 1; }
	@if [ -f go.work ]; then \
	  $(EXPORT) $(GOENV) go work use -add . "$(GUARDX_WORK)"; \
	  echo "已把 $(GUARDX_WORK) 追加进现有 go.work（其余条目保留）。"; \
	else \
	  $(EXPORT) $(GOENV) go work init . "$(GUARDX_WORK)"; \
	  echo "已生成 go.work：本仓 + $(GUARDX_WORK) 进入同一 workspace。"; \
	fi
	@echo "注意：workspace 模式下 guardx 读的是本地副本，go.mod 里写的版本被旁路。"
	@echo "验完跑 'make devunlink' 断开，否则构建会一直读本地副本。"
	@echo "另：workspace 期间**别跑 go work sync**——它按 workspace 的构建列表回写各"
	@echo "模块的 go.mod/go.sum。仓不全时会把用不到的依赖直接删掉（实测把 oagentkit"
	@echo "四个依赖整片剥掉）。要整合同步请先 devunlink 再在单模块下 go mod tidy。"

devunlink: ## 断开本地 workspace（删除 go.work / go.work.sum）
	@rm -f go.work go.work.sum
	@echo "已断开本地 workspace，构建回到 go.mod 声明的已发布版本。"

clean:
	rm -rf bin coverage.out
