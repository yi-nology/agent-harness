# 迁移清单：agentkit 门面 → oagentkit + stdlib errors → oekit/pkg/errors

> **状态：已于 2026-10-18 执行完毕**（本节以下内容保留为执行记录与踩坑说明）。
>
> 实测结果：依赖升级 ✅ / 5 处 import 替换 ✅ / 24 个文件 errors 链路改写 ✅ /
> 门禁两条规则收紧并反向验证 ✅ / `make check` 全绿（覆盖率 83.1%，门槛 60%）。
>
> ⚠️ **本文是历史快照。** 里面的版本号（`oekit/pkg v0.13.0`、四个 oagentkit 模块的
> 当时版本）是**迁移当时**的值；本仓当前钉的版本已远高于此（`oekit/pkg v0.18.0`、
> `agent v0.10.0`、`guardx v0.8.2`、`toolgate v0.12.0` 等，见 `go.mod`），
> 且此后上游又搬过家（`toolgate/invoke` → `guardx/invoke`、`obs/obsx` → `llm/obsx`）。
> **当前事实以 `go.mod` + [README 包表](../README.md) 为准。**
> 本文保留的价值是**踩坑记录**：§1.5 的 sonic build tag 坑（报错完全指不到真实原因）、
> §2 的 `progress` 撞名坑，这两条至今仍会绊到人。
>
> 清单外额外发现并修复的**既有缺陷**（均与本次迁移无关，是被 build cache 长期掩盖的）：
> ① `bytedance/sonic v1.15.0` 的 build tag 排除 Go 1.27（见 §1.5）；
> ② `pii` 包 `type Strategy` 与 `func Strategy()` 同名重名，无法编译；
> ③ `drain` 的 `TestGlobalDrainStillWorks` 传 `context.Background()` 导致死循环。
>
> 依据：<https://oekit.github.io/llms.txt>、<https://oagentkit.github.io/llms.txt>、
> <https://oekit.github.io/guide/errors.html>（2026-10-18 抓取核对）

## 0. 为什么现在做

两条家规的前提都已失效，属于**到期债**而非新增工作：

| 家规要求 | 原阻塞理由 | 现状 |
|---|---|---|
| 一律用 `oekit/pkg/errors` | `pkg/errors` 缺 `New` 与通用 `As`，会逼出双 import | **v0.12.0 起 `New/As/Is/Unwrap` 与 stdlib 同型透传**（guide/errors.html 原文），双 import 问题消失 |
| 安装 `github.com/oagentkit/<mod>` | 门面仓 `agentkit` 仍在服务 | 门面仓已发到 **v1.0.0**，llms.txt 明确「v1.x 后退役。新代码不要引它」 |

## 1. 依赖升级

> ⚠️ **先看 1.5 —— 本仓当前在 Go 1.27 下根本编译不过，与本次迁移无关，
> 但它会先把你卡住。**

```bash
cd /Users/zhangyi/my_project/agent-harness

# ① 先解 Go 1.27 阻塞（见 1.5）：sonic v1.15.0 的 build tag 主动排除 go1.27
go get github.com/bytedance/sonic@v1.15.4

# ② oekit 追平到 errors 能力线（pkg 必须 ≥ v0.12.0）
go get github.com/oekit/pkg@v0.13.0 \
       github.com/oekit/logx@v0.7.0 \
       github.com/oekit/concurrency@v0.13.1

# ③ 引入 oagentkit 四个模块（远端已核验存在）
go get github.com/oagentkit/runtime@v0.3.0 \
       github.com/oagentkit/agent@v0.2.0 \
       github.com/oagentkit/guardx@v0.1.0 \
       github.com/oagentkit/toolgate@v0.2.0
```

改完第 2 节后执行：

```bash
go mod tidy   # 门面 agentkit 与间接依赖 github.com/pkg/errors 应从 go.mod 消失
```

期望：旧门面仓 `agentkit` 与 `github.com/pkg/errors v0.9.1 // indirect`
从 `go.mod` 移除（后者是门面仓带入的，门面退役后无引用）。

### 1.5 阻塞项：`github.com/bytedance/sonic v1.15.0` 不支持 Go 1.27

**这是本次排查挖出的真实仓库缺陷，与迁移无关，但不修就编译不过。**

sonic 用 build tag 显式圈定支持的 Go 版本区间，v1.15.0 写的是：

```go
//go:build (amd64 && go1.17 && !go1.27) || (arm64 && go1.20 && !go1.27)
```

`!go1.27` 意味着**在 Go 1.27 下该目录下没有任何文件属于包 `sonic`**，
于是依赖它的 `eino/schema` 报出极具误导性的错误：

```
eino/schema/agentic_message.go:29:2: no required module provides package github.com/bytedance/sonic
```

这行错误完全看不出真实原因——它跟 `errors.New` 之类毫无关系，
但极易把人引向"模块没装好/网络问题"的错误方向（本次排查前期就被它带偏过很久）。

**修法**：`go get github.com/bytedance/sonic@v1.15.4`。v1.15.4 的约束是
`!go1.28`，Go 1.27 落在区间内，正常生效。**不要**改 go.mod 的 `go 1.27`
指令去迁就依赖——那会牵动全仓工具链版本。

验证方式（应无输出即通过）：

```bash
go build ./...
```

> 附带效应：sonic 装载失败时，`goph/emperror`、`jsonschema/v6` 会**同时**
> 报出同样的 `no required module provides package`——它们是同一棵依赖树上
> 被连带阻断的，看到三个一起报错时，先怀疑根上那个。

## 2. 五处 import 替换

逐文件精确替换，路径映射已按 llms.txt 的模块/包清单核对。

| # | 文件:行 | 旧 import | 新 import |
|---|---|---|---|
| 1 | `eventbus/eventbus.go:10` | `"agentkit/progress"` | `"github.com/oagentkit/runtime/progress"` |
| 2 | `delta/delta.go:20` | `"agentkit/pacer"` | `"github.com/oagentkit/runtime/pacer"` |
| 3 | `dispatch/guard.go:13` | `adispatch "agentkit/dispatch"` | `adispatch "github.com/oagentkit/agent/dispatch"` |
| 4 | `netguard/netguard.go:22` | `"agentkit/egress"` | `"github.com/oagentkit/guardx/egress"` |
| 5 | `policy/policy.go:11` | `policykit "agentkit/policy"` | `policykit "github.com/oagentkit/toolgate/policy"` |

### ⚠️ progress 撞名（llms.txt 明列的头号坑）

`oekit/concurrency/progress` 是**长任务阶段进度追踪器**，
`oagentkit/runtime/progress` 是**泛型事件总线**，语义相反。

`eventbus/` 用的是 `progress.Bus[Event]` / `progress.NewBus[Event]()`（已读码确认），
属事件总线语义 → 取 `oagentkit/runtime/progress`。
**不要**因为包名叫 progress 就换成 concurrency 的。

### 注释同步（3 处文案提到 agentkit，建议一并改）

- `netguard/netguard.go:3` —「IP 策略单源 agentkit/egress」→ `oagentkit/guardx/egress`
- `delta/delta.go:3` —「基础机制在 agentkit/pacer」→ `oagentkit/runtime/pacer`
- `policy/policy.go:1-2` —「核心已沉淀 agentkit/policy（v0.9.6…）」→ `oagentkit/toolgate/policy`
- `policy/policy.go:60-61` —「agentkit/policy v0.9.6；v0.10.12 起 kit 侧更名 AuditGate」→ 按新模块实际版本改写

### 替换后必须验证

门面仓是 re-export，理论上 API 同源，但**不要假设**：

```bash
go build ./... 2>&1 | head -30
```

重点看 `policy.WithAuditGate` → `policykit.AuditGate` 这类**门面曾改过名**的符号
（注释记载「v0.10.12 起 kit 侧更名 AuditGate——With* 前缀保留给 option applier，
本仓出口名不变」）。若 `AuditGate` 在新模块里名字不同，改 `policy/policy.go:63`
的转发实现，**不要改本仓对外的 `WithAuditGate` 出口名**（宿主在用）。

## 3. 错误处理改写

### 3.1 权威 API（guide/errors.html 核对，勿凭记忆写）

```go
import "github.com/oekit/pkg/errors"

errors.Wrap(err, "query user", "user_id", uid)  // 变参 kv；nil-safe
errors.New("msg")                               // sentinel（v0.12.0 起）
errors.Is(err, target)                           // v0.12.0 起，与 stdlib 同型
errors.As(err, &target)                          // v0.12.0 起，与 stdlib 同型
errors.Cause(err)                                // 解包到最底层
```

**没有 `Wrapf`，也没有泛型 `Wrap[Errf]`** —— 这是本次最容易写错的地方。

家规允许 `fmt.Errorf("...: %w", err)` 作为包装，但**不含 `%w` 的 `fmt.Errorf`
不应拿来当 sentinel 或跨层传播的错误**。

### 3.2 sentinel 定义（必改，保证类型同一性）

import 从 `"errors"` 改为 `"github.com/oekit/pkg/errors"`，调用点不变：

| 文件:行 | 符号 |
|---|---|
| `spill/spill.go:66` | `ErrBadName` |
| `drain/drain.go:38` | `ErrDeadline` |
| `protocol/report.go:72` | `ErrNotReport` |
| `protocol/report.go:74` | `ErrViolation` |
| `ptc/ptc.go:43-45` | `errDeadline` / `errBudgetCall` / `errBudgetByte` |

`spill/spill.go:83,87,91` 三处内联 `errors.New(...)`（无 sentinel 语义，纯一次性错误）
一并换包，保持单 import。

### 3.3 判定点（改 import 即可，调用签名同型）

| 文件:行 | 用法 |
|---|---|
| `ptc/ptc.go:224,226,228` | `errors.Is(err, errDeadline/errBudgetCall/errBudgetByte)` |
| `ptc/ptc.go:232` | `errors.As(err, &intErr)`（`*goja.InterruptedError`） |
| `drain/drain_test.go:69` | `errors.Is(err, ErrDeadline)` |
| `protocol/report_test.go:55,64,74,83,88,97,114,123,134,220,227` | `errors.Is(err, ErrViolation/ErrNotReport)` |
| `dispatch/guard_test.go:54,65,76` | `errors.As(err, &deny)` |

`contract/contract_test.go:34`、`ptc/ptc_test.go:24` 里的 `errors.New` 是**测试自造
错误**，同样换包以保持全仓单 import。

### 3.4 包装点（建议改，且其中 3 处是**实质缺陷修复**）

**必须改 —— 当前用 `%v` 吞掉了错误链，链路断了：**

```go
// spill/spill.go:161
- return Page{}, fmt.Errorf("存档不存在或不可读: %v", err)
+ return Page{}, errors.Wrap(err, "存档不存在或不可读")

// spill/spill.go:166
- return Page{}, fmt.Errorf("存档状态读取失败: %v", err)
+ return Page{}, errors.Wrap(err, "存档状态读取失败")

// spill/spill.go:188
- return Page{}, fmt.Errorf("存档读取失败: %v", rerr)
+ return Page{}, errors.Wrap(rerr, "存档读取失败")
```

这三条 `%v` 让上层拿不到 `os.ErrNotExist` 之类的判定能力，
`List()` 里的 `os.IsNotExist(err)` 同理可改 `errors.Is`。

**建议改 —— 已有 `%w`，换 Wrap 拿到 kv 与 nil-safe：**

| 文件:行 | 现状 | 改后 |
|---|---|---|
| `netguard/netguard.go:31` | `fmt.Errorf("出站 URL 非法: %w", err)` | `errors.Wrap(err, "出站 URL 非法")` |
| `netguard/netguard.go:44` | `fmt.Errorf("出站目标 %s 解析失败: %w", host, err)` | `errors.Wrap(err, "出站目标解析失败", "host", host)` |
| `netguard/netguard.go:51` | `fmt.Errorf("出站目标 %s 已拒绝: %w", host, err)` | `errors.Wrap(err, "出站目标已拒绝", "host", host)` |
| `ptc/ptc.go:141` | `fmt.Errorf("ptc aborted: %w", ctx.Err())` | `errors.Wrap(ctx.Err(), "ptc aborted")` |
| `protocol/schema.go:32,36,39` | `fmt.Errorf("...: %w", err)` | `errors.Wrap(err, "...")` |
| `cmd/archcheck/main.go:153,158` | `fmt.Errorf("规则 %s ... 非法: %w", r.ID, err)` | `errors.Wrap(err, "规则定义非法", "rule", r.ID)` |

**消息格式零变化（已按源码核实，勿被旧版本说明误导）**：

`pkg/errors.Wrap` 内部返回 `&wrappedError{}`，其 `Error()` 为
`w.msg + ": " + w.err.Error()`（源码 `errors/wrap.go:41-46`），与
`fmt.Errorf("msg: %w", err)` 的输出**逐字相同**。也就是说
`fmt.Errorf("出站 URL 非法: %w", err)` → `errors.Wrap(err, "出站 URL 非法")`
对上层是**行为等价**的，`errors.Is/As` 判定链也照旧（`Unwrap` 已实现）。

唯一的实际差异有两个，都只会让输出更好：
- `Wrap` 是 nil-safe，不会产出 `%!w(<nil>)`；
- kv 以 `k=v` 追加在末尾（`operation=xx`），供结构化日志关联。

⚠️ 唯一**会**改变文本方向的是 `fmt.Errorf("%w: %s", ErrViolation, msg)` 这种
把 sentinel 当 `%w` 前缀的写法——但第 3.4 节已说明那种写法**保持原样**。

**不改 —— 纯格式化、不涉及 err 传播（共 6 处）：**

`spill/spill.go:108`、`statemachine/step.go:66`、`cmd/archcheck/main.go:88`、
`netguard/netguard.go:34`（scheme 不在白名单）、`netguard/netguard.go:38`（缺 host），
以及 `report_validate.go` 里所有 `fmt.Sprintf` 拼 `critical`/`warns` 的地方
（那些是**消息内容**不是 error，保持 `Sprintf`）。

> 全仓口径：`errors.New` 13 处、`fmt.Errorf` 18 处，已逐条对账，
> 无未归类调用点。

`protocol/report_validate.go:94` 保持原样更稳：

```go
return warns, fmt.Errorf("%w: %s", ErrViolation, strings.Join(critical, "；"))
```

它把 sentinel 作为 `%w` 而非包装外来 err，改成 `Wrap` 只会颠倒消息顺序，
无收益。家规显式允许 `%w` 形式。

## 4. 门禁收紧（policy）

> ⚠️ **顺序警告**：本节必须**在第 2 节（import 替换）之后**执行。
> 规则一改就生效，而第 2 节没做完时代码还在 import `agentkit/`，
> 门禁会立刻自造红灯 —— 那是假失败，不是真违规。

`architecture-policy.yaml` 规则三目前只拦宿主仓与 `ekit`，需把**门面 agentkit**
也纳入，防止有人把 import 改回去：

```yaml
# 规则三：harness 包不得依赖宿主的运行时原语库。
- id: harness-...            # id 保持不变
  forbid_import_regex: 'git\.enjoye\.top/enjoydream/(bianque|ekit|agentkit)/'
```

并把规则六注释从「当前状态（2026-10-18）：代码仍是纯 stdlib 链路」更新为已清偿，
与本清单第 3 节完成后的实际状态对齐。

**可选加固**：新增一条规则禁止 import 门面仓
（`forbid_import_regex: 'git\.enjoye\.top/enjoydream/agentkit/'`），
因为「退役」这件事只有文档约束、没有机器约束，半年后极易回退。

## 5. 文档同步（**第 2、3 节完成后**再执行）

> 为什么单独一节：文档描述的是**当前事实**。在 import 与错误改写落地之前就把
> README/AGENTS.md 改成「依赖 oagentkit」，文档就开始说谎 —— 比文档滞后更糟。

`README.md` 共 8 处：

| 行 | 现状 | 改为 |
|---|---|---|
| 7 | 「定位对标同族的 `agentkit`（agent 内核原语）」 | `oagentkit`（Agent/LLM 编排工具链矩阵），并注明 `agentkit` 门面已发至 v1.0.0 退役 |
| 36 | 依赖列 `agentkit 无` | `oagentkit 无` |
| 37 | `agentkit/dispatch` | `oagentkit/agent/dispatch` |
| 38 | `agentkit/progress` | `oagentkit/runtime/progress` |
| 39 | `agentkit/policy` | `oagentkit/toolgate/policy` |
| 45 | `agentkit/pacer` | `oagentkit/runtime/pacer` |
| 71 | 门禁说明「对宿主仓 / `ekit` 的 import（**允许 `agentkit`**）」 | 删掉「允许 agentkit」，与第 4 节 policy 改动保持一致 |
| 75 | 依赖链「`oekit` → `agentkit` → `agent-harness` → 上游宿主仓」 | `agentkit` 换成 `oagentkit` |

`AGENTS.md` 两处：

- **「与 oekit 家族的一致性」**开头的依赖链段落：`agentkit` → `oagentkit`
- **「本机环境」**：原写的私有模块 GOPRIVATE 直拉条目——迁移后已无 `GOPRIVATE` 依赖，该条已清理
  —— **已完成**：Makefile 的 `EXPORT` 已清空，私有域名不再出现在构建脚本里

### 明确**不要**改的

`docs/SPLIT_PLAN.md:96` 的 `agentkitmcp.Pool` 与 `:165` 的「解法与 agentkit 已有的
设计同构」，是**当时拆分决策的历史记录**。改它们等于篡改历史文档，
不改变任何行为 —— 保持原样。

## 6. 验证

```bash
make check
```

依次过 `fmt-check` / `vet` / `test` / `cover-gate`(≥60%) / `architecture-check`。

改完 `AGENTS.md` 后按该文件纪律，把「### 错误处理：从 stdlib 链路迁移到
`oekit/pkg/errors`」小节末尾的**当前状态**段改为「已清偿」，并删掉
`architecture-policy.yaml` 规则六注释里的过渡说明。

提交纪律提醒（AGENTS.md）：`git add <具体路径>`，**禁止 `git add -A`**。

## 7. 风险清单

1. **`policy.AuditGate` 可能改名** —— 门面曾做过 `With*` → `AuditGate` 改名。
   保持本仓 `WithAuditGate` 出口名不动，只改内部转发。
2. **错误消息顺序变化** —— 见 3.4 注意事项，宿主若消费全文需通知。
3. **间接依赖变动** —— `go mod tidy` 后 `github.com/pkg/errors` 应消失；
   若仍在，说明还有依赖它的地方，需查清。
4. **覆盖率** —— `COV_MIN=60` 是真阈值。本次改动集中在错误路径，
   若覆盖率跌破需补测试而非下调阈值。
5. **`Makefile` 的 GOPRIVATE** —— 迁移后本仓不再有私有域依赖，
   `EXPORT` 里的 `GOPRIVATE` / `GONOSUMCHECK` 已清理（公开仓不该残留私有域名），
   不影响功能。
