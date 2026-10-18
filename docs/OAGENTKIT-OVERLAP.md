# 与 oagentkit 的重叠面盘点（2026-10-18）

> 背景：仓依赖已从旧门面仓 `agentkit` 迁移到 `github.com/oagentkit/*`
> 矩阵。本文回答一个问题：**迁移之后，agent-harness 还该做什么、不该做什么。**
>
> 核对方式：从 goproxy 取 `agent v0.2.0` / `runtime v0.3.0` / `guardx v0.1.0` /
> `toolgate v0.2.0` / `mcpkit v0.1.0` 的模块 zip 直接读源码（本地缓存解压被沙箱拒绝）。

## 一、上游现在提供了什么

| 上游子包 | 规模 | 对本仓的意义 |
|---|---|---|
| `agent/dispatch` | 174 行 | **与本仓 `dispatch` 同一套 API**（`Guard`/`EdgeSource`/`DenyError`/`NewGuard`）。本仓 `dispatch/guard.go` 仅 36 行、全是类型别名，是**有意的门面**（SPLIT_PLAN §一.3「调用面一行不动」），不是分叉。**保持门面，但别再在本仓加逻辑。** |
| `guardx/egress` | — | **不解析 DNS**：`checkHost` 对域名直接 `return nil // 域名：不解析`。**本仓 `netguard` 的解析层是真差异化，必须保留。** 反过来提醒：任何人直接拿 `egress.Check(url)` 挡 LLM 可控 URL，会得到零 DNS-rebinding 防护。 |
| `guardx/bashguard` | 493 行 | `Analyze(cmd, *Table) Decision`——**这就是 octop `security/tool_guard/` 那套规则 DSL + 处置裁决的 Go 版**。上轮对标里我列为「最值得抄」的一条，**上游已有，本仓不必做**。 |
| `guardx/permgate` | 325 行 | Capability/Rule/Ruleset/Decision——权限/能力门禁（含 deny 优先于 approval 的位置）。**不必做。** |
| `guardx/fence` · `logredact` · `audit` | — | 转义包裹 / 凭据脱敏 / 审计日志。本仓 `pii`（个人信息类）与 `policy`（门面）与之互补，不重叠。 |
| `toolgate/policy` | 48 行 | `AuditGate`——**与本仓 `policy.WithAuditGate` 签名同款**，属上游内部整合，与本仓无冲突。 |
| `toolgate/toolprior` · `toolsched` | 181 / 462 行 | 优先级表 + 调用限流 + **并发调度器**。本仓无对应物，也不该有（属 agent 运行时，不属编排内核）。 |
| `agent/clarify` | 153 行 | 消歧/澄清（词表加载、答案解析）。对应 SPLIT_PLAN L2 的 `intent.go`。 |
| `agent/blackboard` | 191 行 | `Board` + `Convene`——**合议裁决**。对应 L2 的 `judge.go`。 |
| `agent/reflection` · `router` | — | 低置信复审 / 路由。对应 L2 的 `supervisor` / 分发面。 |
| `runtime/hotplug` | — | `Plugboard`/`Holder[T]`——**可能解开 SPLIT_PLAN §一.4 的 `filterDisabled` 卡点**（该方法当初卡在宿主 `Plug.Enabled` 上）。 |
| `runtime/hookx` | — | 带超时的 Hook Runner。 |
| `runtime/pacer` · `progress` | — | 本仓 `delta` / `eventbus` 的底层原语（已在用）。 |
| `runtime/procx` | — | 进程执行 + 退出码捕获。宿主工具执行面的通用件。 |

### 一条命名冲突（已处置）

本仓原 `toolgate` 与 `github.com/oagentkit/toolgate` **同名但机制不同**：
上游是「调用时审计门 + 优先级 + 并发调度」，本仓是「渐进式披露 + 两类失败二分」。
同一依赖图里两个 toolgate 是维护陷阱。

**已处置（2026-10-18）**：本仓包更名为 **`tooldisclose`**，点名机制而非泛称「门禁」，
与上游彻底区分。涉及包目录、`package` 声明、policy 模块 id 与 `scan_roots`、
README 清单，全部同步完成；改名后覆盖率仍 100%，上游 import 无需改动
（Go 的包名冲突只发生在同时 import 两侧时，本仓不 import 上游 toolgate）。

## 二、oracle 验出的分歧（对照 octop 自带测试）

参考侧用 octop-harness **v1.0.2**（唯一带 `tests/` 的副本；已安装的 1.0.4 wheel 不含测试，
RECORD 375 条 0 条含 test）。版本差为补丁级，以下均为**行为**差异而非 API 拼写差异。

| # | 包 | 分歧 | 状态 |
|---|---|---|---|
| 1 | `netguard` | 跳数差一：`>= maxHops` 只允许 9 跳，参考 `range(_MAX_REDIRECTS+1)` 允许 10 跳 | ✅ **已修**（`> maxHops`，并加钉住语义的回归用例）|
| 2 | `tooldisclose` | 整串加成拿**原始查询**比对，多词查询（最需要加成的场景）永远拿不到；参考先归一为 `alpha_lookup` | ✅ **已修**（`normalizeQuery` 词下划线相连 + 6 条新用例，覆盖率仍 100%）|
| 3 | `tooldisclose` | 检索上限 5/20 vs 参考 3/5——**20 恰是本包要防的「退化成全量披露」** | ⏸ 待定（可调常量，属产品决策）|
| 4 | `tooldisclose` | 无词干还原：参考对 len>3 的词去尾 s，`files` 能命中 `read_file` | ⏸ 待定 |
| 5 | `budget` | `MaxInput` 语义三处不一致：参考 `reserved<=0` 原样返回不设地板；`baseInput>window` 时参考返 baseInput | ⏸ 待定 |
| 6 | `budget` | `RemovedTokens` 参考是**净值**（原长 − 摘要长），本仓是**毛移除量**，**同名字段不同义** | ⏸ 待改名 |
| 7 | `budget` | 守卫阈值 `MinItems=2` 与参考的 `8`（且与 KeepTail 耦合）不符 | ⏸ 待定 |
| 8 | `offload` | **同轮重复内容**：参考逐 block 处理、同轮两条相同图片都保持内联；本仓 `State.Mark` 循环内即时生效，第二条同内容本轮即被替换——与本仓自己注释「首次见到恒为 false」矛盾，**两侧测试都没覆盖** | ⏸ **建议优先修**（是本仓自身的内部矛盾）|
| 9 | `netguard` | 参考有**按名拦截层**（`metadata.google.internal`、`.internal/.lan/.local` 后缀），不看 DNS 结果就拒；本仓只把解析出的 IP 交给 egress，故解析到公网的 `foo.internal` 参考拒、本仓放行 | ⏸ 待定 |

### 三个高价值遗漏的处置

1. **`shrink_context_usage` 就地修正快照**（5 个 oracle 测试）→ ✅ **已做**：
   `budget.Meter.Shrink(seg, tokens) int` 逐段就地扣减、按段钳位（不串到别的段）、
   返回**实际**扣掉多少（返回值 < 请求值即说明触顶，钳位可观测）。
   `Plan.RemovedTokens` 与此前的空头支票注释一并清理。
2. **offload 的 mime→ext 推导 + 块形态识别**（~12 个 oracle 测试）→ ✅ **部分做**：
   `offload.ExtForMimeType` 落库参数剥离 + 显式 override 表 + 未知兜底 `.bin`。
   构造目录里加 override 表是必需的：本机实测 `mime.ExtensionsByType("image/jpeg")`
   返回 `[.jfif .jpe .jpeg .jpg .pjp .pjpeg]`，「取第一个」会产出 `.jfif`，
   **静默改变内容寻址的键**。
   块形态识别（各家 provider 的消息结构）**仍未做**——那属于宿主消息模型，内核不该认识。
3. **tooldisclose 每线程目录 LRU + 描述截断** → ✅ **描述截断已做**
   （`MaxDescriptionLen = 320`，构造期截断并入库，打分与披露共用同一份）；
   **每线程 LRU 明确不做**——Go 没有 Python contextvar 那样的隐式请求作用域，
   凭空造一个属于越界设计。改为在包文档写明：目录自身已被截断有界，
   **跨轮缓存是调用方的责任**。

### 过度设计（做了但参考没有）

- `budget` 的 `Classify`/`Pressure` 分档（75%/90%）——参考零对应物，保留：本包
  已有的 `Pressure` 判定有独立价值，只是**不该**被当成「对齐了参考」。
- `budget` 三个独立哨兵——参考刻意合并成一个 `nothing_to_compact`。
  保留：内核给可区分的拒绝原因，比给一句笼统的话更好排查。
- `netguard` 跳号——参考只有一条消息串。保留。

---

## 三、两处对参照实现意图的**有意分歧**（实现时发现我的指令是错的）

1. **`budget.MaxInput` 不采用「baseInput > window 就返回 baseInput」**。
   参照 `config/__init__.py:389-396` 实际是
   `if reserved <= 0 or total <= 0: return prompt_cap`，
   否则 `min(prompt_cap, max(total - reserve, 1))`——**任何情况下都取 min**。
   「baseInput > window 返回 baseInput」只在 `reserve <= 0` 的提前返回里成立，
   是 case 1 的子集而非第三个分支。按字面实现反而会**新增**一处分歧。
2. **`budget` 守卫阈值不随 keep-tail 变化**。
   参照 `MIN_EFFECTIVE_MESSAGES = FORCE_KEEP_MESSAGES + 2 = 8` 是**模块常量**，
   而 `cutoff` 来自每次调用的 `_determine_cutoff_index`（`compaction.py:37` vs `:190-194`）。
   若把阈值写成调用方 keep-tail 的函数，则「过了守卫一」必然推出 `cutoff >= 2`，
   于是 `ErrCutoffTooLow` 与 `ErrTooFewToSummarize` **在数学上永不可达**——
   等于悄悄废掉另外两个守卫及其覆盖。

## 四、结论：本仓该守什么

**该守（上游不可能有，因为它们是编排语义而非通用原语）**：
`checkpoint`（续跑而非重放）、`budget`（压实计划骨架 + 就地改账）、`offload`（两阶段外置）、
`tooldisclose`（渐进披露 + 两类失败二分）、`toolfail`（失败分类契约）、`netguard`（逐跳重验 +
按名拦截 + DNS 解析层）。

**该让（上游已覆盖或本就属宿主/运行时）**：bashguard 式规则 DSL、permgate、fence、
澄清/合议/复审/路由、工具调度与限流、tokenizer 估算、provider 消息块形态识别。

**路线图影响**：`docs/SPLIT_PLAN.md` §二 的 L2 清单里，`intent.go`(→`agent/clarify`)、
`judge.go`(→`agent/blackboard`)、`supervisor.go`(→`agent/reflection`) 已有上游对应物；
`filterDisabled` 的卡点可能被 `runtime/hotplug.Plugboard` 解开。
**L2 不该再按原清单推进**，应改为「先清点上游已覆盖，再定剩余缺口」。

## 五、遗留限制（如实记录）

- **`delta` / `netguard` / `dispatch` / `eventbus` / `watchdog` 在本沙箱内无法跑测试**：
  迁移后的 `github.com/oagentkit/*` 模块 zip 解压被沙箱的 `rename(2)` 拒绝。
  本轮 `netguard` 的新逻辑（按名拦截 + 空 Location）是在 `/tmp/vnet` **隔离仓**里
  用桩替换第三方依赖后实跑的，并做了反向验证；**尚未在真实依赖树上复测**。
- 参照侧用的是 octop-harness **v1.0.2**（唯一带 `tests/` 的副本），代码是 **1.0.4**。
  补丁级差异下结论可用，但严格说未验过 1.0.4 的测试（其 wheel 不含测试）。
- `netguard.go` 包注释里仍有「告警」「本机」等词。架构门禁**不扫注释行**
  （`cmd/archcheck/main.go:196` 无条件跳过 `//` 开头的行），故从未被抓到。
  该豁免是有意的（否则约 26 处正当注释会误红，其中大半正是解释「为什么排除领域概念」），
  但代价是注释里的领域词会静默累积。是否收窄尚未决策。
