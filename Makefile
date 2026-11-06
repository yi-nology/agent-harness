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
# 逐包下限。全局 85% 会**掩盖单个包的塌方**——2026-11-06 实测：全局 85.5%，
# 而 cmd/docsync 只有 23.6%、sandbox 60.0%，两个数字都被大盘盖住，没有任何信号。
# 全局阈值管「整体没退步」，逐包下限管「没有一个包躲在大盘后面」，两者不能互相代替。
PKG_COV_MIN := 70
# 逐包下限的显式豁免（逗号分隔，按包路径**后缀**匹配）。**当前为空。**
#
# 纪律同 policy 的 exceptions：豁免必须写原因 + 拆除条件，且**到期即拆**——
# 豁免包一旦自己达到下限，cover-gate 会报「豁免失效」并拒绝变绿，
# 逼人当天删掉这一行，而不是让它无限期留着变成装饰。
#
# # 这里曾经有过唯一一条豁免，已当场拆除（2026-11-06，同一天）
#
# 豁免对象是 cmd/docsync（23.6%）。当时登记的原因写的是：判定逻辑全在
# `package main` 里，run() 把 `README.md` 写成相对路径、失败走 os.Exit，没有可注入
# 的接缝 ⇒ 想测就得先改造它。拆除条件写的是「把判定逻辑搬进可导入包」。
#
# 实际清偿走的是一条更短的路径，**没有搬家**：Go 1.24+ 的 `t.Chdir` 让用例可以
# 切进临时目录，在那里摆一份 README 与若干包目录，就是 run() 眼里的整个世界
# （见 cmd/docsync/run_test.go）。覆盖率 23.6% → 88.8%，豁免随之下线。
#
# 记这一笔的原因是它验证了「失效即红」这条机制：补完用例后第一次跑 cover-gate，
# 门禁自己报的就是「豁免失效，请从 PKG_COV_EXEMPT 移除」，而不是默默放行。
PKG_COV_EXEMPT :=
# -race 必须开 cgo：Linux 上 `-race requires cgo`（实测 GOOS=linux 下
# `CGO_ENABLED=0 go test -race -c` 直接报这句）。故 race 目标**不能**跟着
# 全局的 GOENV(CGO_ENABLED=0)——那样它在 CI 上必红。本机 cgo=1 已验证可
# 完整链接全仓（`CGO_ENABLED=1 go test -race -run '^$' ./...` 零输出）。
RACE_ENV := CGO_ENABLED=1
# 交叉构建矩阵：只声明**真的验过**的平台。
# windows 不在列：上游 github.com/oagentkit/runtime/procx 用了 unix 专属
# syscall（SysProcAttr.Setpgid / syscall.Kill），本仓改不了它。
# ⇒ 处理方式是**显式声明不支持**（README 平台矩阵），不是假装支持、
#   也不是为一个编不过的平台留一条永远不跑的门禁。
CROSS_TARGETS := linux/amd64 linux/arm64 darwin/arm64
# govulncheck 版本口径：CI 装这个版本，与本机验证过的一致（同 lint 的理由）。
GOVULNCHECK_VERSION := v1.7.0

.PHONY: build fmt-check vet lint test race cover cover-gate vuln crossbuild architecture-check docsync check tidy devlink devunlink clean

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

# lint 版本口径：CI 装 v2.14.0（见 ci.yml），与本机验证过的版本一致。
# **lint 原先既不在 check 也不在 CI**——它在哪儿都不跑，于是
# `archcheck` 包的死变量、`protocol` 的孤儿谓词这类问题会一路存活到没人查。
# 「从没见过它触发」的规则等于没有规则，同理「没人跑的门禁」也不等于有门禁，
# 故按 AGENTS.md「门禁本体全部在 check 里」把它接进来。CI 侧只负责把二进制装好，
# 规则本体仍在本 Makefile——不放到 ci.yml 里，避免变成「推送后门禁」。
lint: ## 静态检查（未安装时明确报错退出，与宿主仓同口径）
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint 未安装：brew install golangci-lint"; exit 1; }
	$(EXPORT) $(GOENV) golangci-lint run ./...

test:
	$(EXPORT) $(GOENV) go test ./... -count=1

race: ## 竞态检测（**开 cgo**：Linux 上 -race 强制要求 cgo，见 RACE_ENV 注释）
	@# 这条门禁此前从未被跑过：race 目标存在，但既不在 check 的依赖链里，
	@# ci.yml 也全文没有它 ⇒ 「有没有数据竞争」在本仓永远没人判。
	@# 竞态是本类内核最贵的一类缺陷（代次/闸/共享 map 都在并发路径上，
	@# 而它们的失效形态是「偶尔给错答案」，单线程用例永远绿）。
	$(EXPORT) $(RACE_ENV) go test ./... -race -count=1

cover:
	$(EXPORT) $(GOENV) go test ./... -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -1

cover-gate: ## 覆盖率阈值门禁（全局门槛 + 逐包下限；阈值应随内核成熟度上调）
	@# 测试输出先落临时文件：成功时静默（与原口径一致），失败时把日志打出来。
	@#
	@# 原口径是 `go test ... >/dev/null`——**用例失败也只剩一句 "Error 1"**。
	@# 红是红了，但看不出哪个用例、为什么红。而本仓的纪律是「门禁的失败形态
	@# 是变红」：看不见原因的变红与「常红」同样会训练人忽略门禁。
	@#
	@# 为什么这个失败路径值得单独修：`make check` 里 `test` 目标先跑一遍同一套
	@# 用例且**输出可见**，所以 cover-gate 自己失败只可能是「第一次绿、第二次红」
	@# ——即**抖动**。而抖动是唯一一次能看见抖动用例的机会，输出被丢掉就再也
	@# 没线索了（本轮实测撞到过一次）。
	@#
	@# 两条判据：全局门槛（COV_MIN）管「整体没退步」，逐包下限（PKG_COV_MIN）
	@# 管「没有一个包躲在大盘后面」。两者判的是两件事，故都要跑；判定细则见
	@# scripts/pkgcov.awk 的头注。
	@#
	@# 日志必须留到逐包判定之后再删：原实现「成功即 rm 日志」，于是逐包数字在
	@# 判定前就已经没了——这正是它此前只能判全局的实现层原因。
	@#
	@# ⚠️ 注释只能写在块**外**：下面是一个用 `\` 续行的 shell 块，块内出现的 `#`
	@# 会把该行连同续行符一起变成注释，报 "syntax error: unexpected end of file"。
	@log=$$(mktemp); \
	$(EXPORT) $(GOENV) go test ./... -cover -coverprofile=coverage.out -count=1 >"$$log" 2>&1 || { \
	  echo "覆盖率测试未通过——失败详情："; \
	  cat "$$log"; \
	  rm -f "$$log"; \
	  exit 1; \
	}; \
	total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print substr($$3, 1, length($$3)-1)}'); \
	echo "harness 覆盖率: $$total% (门槛 $(COV_MIN)%)"; \
	if ! awk -v t="$$total" -v m="$(COV_MIN)" 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }'; then \
	  echo "覆盖率 $$total% 低于阈值 $(COV_MIN)%"; \
	  rm -f "$$log"; \
	  exit 1; \
	fi; \
	if ! awk -v m="$(PKG_COV_MIN)" -v ex="$(PKG_COV_EXEMPT)" -f scripts/pkgcov.awk "$$log"; then \
	  rm -f "$$log"; \
	  exit 1; \
	fi; \
	rm -f "$$log"

vuln: ## 依赖与标准库漏洞扫描（govulncheck）
	@# 这条同样此前从未进过门禁。价值不在「扫出第三方 CVE」——本仓依赖面窄，
	@# 而在**标准库**：2026-11-06 首次跑出 6 条，全部在 stdlib（crypto/x509、
	@# net/http 系列、crypto/tls），修法是抬 go 指令到 1.27.3。而这类漏洞的
	@# 载体正是「下游用本仓编出来的那个二进制」，本仓不判就没人判。
	@#
	@# 未安装时明确报错退出（与 lint 同口径），不静默跳过：门禁缺失的形态必须是
	@# 变红，不能是变绿。
	@command -v govulncheck >/dev/null 2>&1 || { \
	  echo "govulncheck 未安装：go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)"; \
	  exit 1; }
	$(EXPORT) $(GOENV) govulncheck ./...

crossbuild: ## 交叉构建矩阵（声明支持的平台必须真的编得过）
	@# 为什么这条要有：本仓是**给宿主当依赖编进二进制**的，本机 darwin 绿不等于
	@# linux 绿。此前没有任何东西判这件事——2026-11-06 实测 GOOS=linux 能过、
	@# GOOS=windows 编不过（上游 procx 的 unix syscall），而这个事实从未被记录。
	@# 判据取 `go build ./...`：它覆盖全部生产代码，且不需要 cgo。
	@for t in $(CROSS_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  echo "交叉构建 $$os/$$arch"; \
	  $(EXPORT) CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build ./... || exit 1; \
	done
	@echo "交叉构建矩阵 ✓（$(CROSS_TARGETS)；windows 显式不支持，见 README 平台矩阵）"

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

# check 的依赖链就是「提交前必须全绿」的定义。2026-11-06 复核时补上三条
# **此前根本没被跑过**的门禁：race（竞态）、vuln（依赖与标准库漏洞）、
# crossbuild（声明支持的平台必须真编得过）。它们的共同点是「目标/文档里有，
# 但没人执行」——与 lint 当年同样的形态，故按同一条纪律接进 check，
# 不挂到 ci.yml 上变回「推送后门禁」。
check: fmt-check vet lint test cover-gate vuln race crossbuild architecture-check docsync modreplace modwork ## 提交前门禁聚合

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
