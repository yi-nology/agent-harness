# 与 oagentkit 的重叠面盘点（2026-10-18 盘点，2026-10-20 更新处置状态）

> 背景：仓依赖已从旧门面仓 `agentkit` 迁移到 `github.com/oagentkit/*`
> 矩阵。本文回答一个问题：**迁移之后，agent-harness 还该做什么、不该做什么。**
>
> 核对方式：从 goproxy 取 `agent v0.2.0` / `runtime v0.3.0` / `guardx v0.1.0` /
> `toolgate v0.2.0` / `mcpkit v0.1.0` 的模块 zip 直接读源码（本地缓存解压被沙箱拒绝）。
>
> ⚠️ **版本口径**：上面那组版本是**盘点当时**的上游版本。本仓当前钉的版本已远高于此
> （`agent v0.10.0` / `runtime v0.7.1` / `guardx v0.8.2` / `toolgate v0.12.0` /
> `mcpkit v0.8.3`，以 `go.mod` 为准）——**上游能力仍在搬家**（2026-10-20 一轮就把
> `toolgate/invoke` 挪到 `guardx/invoke`、`obs/obsx` 挪到 `llm/obsx`）。
> 故 §一 的「上游现在提供了什么」是**当时的快照**，重叠面结论需按当前版本复核；
> 本文已随实际改动更新的是 §二 的处置状态与 §五 的遗留限制。

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
README 清单，全部同步完成；改名后覆盖率仍 100%。

> 2026-10-20 更正：本仓**现已 import 上游 toolgate**（`policy` 门面包转发
> `toolgate/policy`）与 `guardx/invoke`（判定环原语，2026-10-20 由
> `toolgate/invoke` 迁来）。同名冲突仍不存在——冲突的前提是「本仓有包也叫
> toolgate」，而本仓那个包已更名为 `tooldisclose`。

## 二、oracle 验出的分歧（对照 octop 自带测试）

参考侧用 octop-harness **v1.0.2**（唯一带 `tests/` 的副本；已安装的 1.0.4 wheel 不含测试，
RECORD 375 条 0 条含 test）。版本差为补丁级，以下均为**行为**差异而非 API 拼写差异。

**处置状态（截至 2026-10-20）：九项全部处置完毕，无遗留待定项。**

| # | 包 | 分歧 | 状态 |
|---|---|---|---|
| 1 | `netguard` | 跳数差一：`>= maxHops` 只允许 9 跳，参考 `range(_MAX_REDIRECTS+1)` 允许 10 跳 | ✅ **已修**（`> maxHops`，并加钉住语义的回归用例）|
| 2 | `tooldisclose` | 整串加成拿**原始查询**比对，多词查询（最需要加成的场景）永远拿不到；参考先归一为 `alpha_lookup` | ✅ **已修**（`normalizeQuery` 词下划线相连 + 6 条新用例，覆盖率仍 100%）|
| 3 | `tooldisclose` | 检索上限 5/20 vs 参考 3/5——**20 恰是本包要防的「退化成全量披露」** | ✅ **已对齐**：`DefaultSearchLimit = 3`、`MaxSearchLimit = 5`，两个数都与参考一致；防退化的意图保留在 `clampLimit`（钳到 `[1,5]`）而不是靠一个宽上限 |
| 4 | `tooldisclose` | 无词干还原：参考对 len>3 的词去尾 s，`files` 能命中 `read_file` | ✅ **已修**：`stemTerm`（长度 > 3 去尾 `s`，**按 rune 计**——按字节计会让含多字节字符的短词越过门槛，与参考不一致）|
| 5 | `budget` | `MaxInput` 语义三处不一致：参考 `reserved<=0` 原样返回不设地板；`baseInput>window` 时参考返 baseInput | ✅ **已对齐为有意分歧**（推导见 §三 第 1 条）：`MaxInput` **任何情况下都取 min**，只有 `outputReserve<=0` / `window<=0` 才提前返回 `baseInput` |
| 6 | `budget` | `RemovedTokens` 参考是**净值**（原长 − 摘要长），本仓是**毛移除量**，**同名字段不同义** | ✅ **已改**：拆成两个字段——`GrossRemovedTokens`（毛，未扣摘要）/ `NetRemovedTokens`（净，钳位 0）。要「减掉多少」走后者，**别按名字对齐参考实现的同名字段**（那会静默错账，方向恰好是「看起来更省了」）|
| 7 | `budget` | 守卫阈值 `MinItems=2` 与参考的 `8`（且与 KeepTail 耦合）不符 | ✅ **已对齐**：`ForceKeepItems(6) + MinSummarizeItems(2) = MinEffectiveItems = 8`，与参考一致；且**刻意不随调用方 keep-tail 变化**（理由见 §三 第 2 条——否则两条守卫会数学上永不可达，等于悄悄废掉它们）|
| 8 | `offload` | **同轮重复内容**：参考逐 block 处理、同轮两条相同图片都保持内联；本仓 `State.Mark` 循环内即时生效，第二条同内容本轮即被替换——与本仓自己注释「首次见到恒为 false」矛盾，**两侧测试都没覆盖** | ✅ **已修**：`applyOne` 引入**本轮内** `fresh` 集合，本轮刚标记的一律按「首次」处理并**原样保留**（`Replaced` 恒为 false）；同轮重复内容各自落盘一次（内容寻址、幂等），去重收益体现在**跨轮** |
| 9 | `netguard` | 参考有**按名拦截层**（`metadata.google.internal`、`.internal/.lan/.local` 后缀），不看 DNS 结果就拒；本仓只把解析出的 IP 交给 egress，故解析到公网的 `foo.internal` 参考拒、本仓放行 | ✅ **已修**：名字层落地为 `checkHostName` / `classifyHostName` + `BlockedNameError`——**解析前按名拒**，不花一次 DNS 往返；拨号层（`DialFunc`）走的是**同一套**名单 |

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

### 本仓 → 上游的完整映射（2026-10-21 实测复核后的定论）

**先看这张表**——「某个包归谁」的第一入口在这，别去翻散文。

| 本仓包 | 归属 | 核心位置 | 形态 |
|---|---|---|---|
| `dispatch` | 上游 | `oagentkit/agent/dispatch` | 门面（类型别名） |
| `policy` | 上游 | `oagentkit/toolgate/policy` | 门面（转发） |
| `reminder` | 上游 | `oagentkit/guardx/injectband` | 门面（转发） |
| `toolfail` | 上游 | `oagentkit/guardx/toolfail` | 门面（类型别名 + 常量别名 + 转发） |
| `sandbox` | 上游 | `oagentkit/guardx/sandbox` | 门面（类型别名 + 常量别名 + 转发） |
| `delta` | 上游基础机制 | `oagentkit/runtime/pacer` | 本仓在其上叠编排层（(kind,iteration) 键控 + 收口扫序） |
| `eventbus` | 上游基础机制 | `oagentkit/runtime/progress` | 本仓在其上叠事件面 |
| `pii` | 本仓 | — | 个人信息**价值面**脱敏；上游 `guardx/logredact` 是**凭据**脱敏（互补） |
| `netguard` | 本仓 | IP 策略单源 `guardx/egress` | 本仓的解析层 + 名字层 + 拨号层闭窗是真差异化（上游 `egress` **不解析 DNS**） |
| `budget` | 本仓 | — | 分段计量 + 上限计算 + 压实计划骨架（职责面与上游 `textx/compact` 不重叠，见下） |
| `tooldisclose` | 本仓 | — | 渐进披露 + 两类失败二分（模型面；上游 `mcpkit.Catalog` 是 server 面） |
| `offload` | 本仓 | — | 两阶段外置（sha 去重 + 首次标记后续替换） |
| `checkpoint` | 本仓 | — | 续跑而非重放 + 跨进程 CAS 存取契约 |
| `commitgate` | 本仓 | — | 环节终态独占提交闸（凭令牌释放） |
| `contract` | 本仓 | — | 编排引擎 ↔ 宿主状态的唯一边界 |
| `protocol` | 本仓 | — | Report 契约 + 容错解析 + 白名单校验 + 渲染 |
| `statemachine` | 本仓 | — | 环节状态机（纯谓词） |
| `ptc` | 本仓 | 跨进程协议用上游 `guardx/wire` | 程序化工具编排沙箱（goja VM，三重预算）。**两种执行形态**：进程内（`Run`，兜不住内存）与**子进程**（`NewSubprocessProvider`，内核地址空间上限兑现 `MaxMemory`；程序被内核杀掉时父侧从管道 EOF 认出「执行域异常终止」）|
| `guardset` | 本仓 | 接上游 `guardx/invoke` | 守卫装配契约（多守卫合成一条拒绝） |
| `watchdog` | 本仓 | — | 被动配对不变量监视（只告警不裁决） |
| `pending` | 本仓 | — | 反问/消歧挂起态载体 |
| `drain` | 本仓 | — | 优雅排水（经 `Turns` 接口） |
| `spill` | 本仓 | — | 输出溢出存档平面 |
| `toolname` | 本仓 | — | 工具名 → JS 标识符的单一真相（被 `tooldisclose` + `ptc` 共用） |
| `archcheck` | 本仓 | — | 架构门禁执行体（可被宿主复用） |

**已下沉的 5 个门面**由 `harness-facade-stays-facade` 门禁守着（policy 的 `facades` 段登记），
「核心归上游、门面留本仓」有机器约束，不只靠本文。

**该让（上游已覆盖或本就属宿主/运行时）**：bashguard 式规则 DSL、permgate、fence、
澄清/合议/复审/路由、工具调度与限流、tokenizer 估算、provider 消息块形态识别。

### 2026-10-21：`toolfail` 与 `sandbox` 已下沉，本仓转门面

本文 §四 的初稿把 `toolfail` 列在「该守」一侧（**失败分类契约**）。该判断**已过时**，
本轮实测复核后推翻：

- 失败分类与处置判定是**跨宿主一致的语义**，与编排无关——各宿主各判一套必然漂移，
  漂移的后果是重试策略静默错乱（对永久失败死循环重试，或对瞬时失败一次就放弃）。
- 隔离档位同理：档位若各宿主各定一套，「宿主声明的强度」与「实际执行的强度」之间
  就没有信号——**降级了没人知道**。
- 两者与 `invoke`（调用链）、`permgate`（许可判定）同族，都是「要求 vs 实际」的判定语义，
  故归 `github.com/oagentkit/guardx`（tier 0 安全层）。

**已处置**：

| 项 | 状态 |
|---|---|
| 上游 `guardx/toolfail` | ✅ 已落地（PR oagentkit/guardx#14），随 **v0.9.0** 发布 |
| 本仓 `toolfail` | ✅ 转门面（类型别名 + 常量别名 + 单条上游转发），调用面**逐字兼容**——宿主 bianque 的 `internal/engine/scheduler/experts/dispatch.go` 零改动 |
| 本仓 `sandbox` | ✅ 同样下沉（PR oagentkit/guardx#15），转门面；档位声明门禁（`harness-sandbox-provider-declared`）留在本仓，因为它核对的是**本仓 policy 的登记面** |
| 矩阵登记 | ✅ `matrix-tools/modules.json` + 站点 `llms.txt` / `versions.json` 已重生成（PR matrix-tools#53、站点#24） |
| 门禁判据修复 | ✅ matrix-tools#54：门面符号门禁的枚举基准从「矩阵的全部包」改为「门面实际转发的包」——否则矩阵每新增一个包就要求门面跟着长（而门面早已废弃） |

**门禁同步**：本仓 `facades` 段从 3 条扩到 **5 条**（新增 `toolfail`、`sandbox`），
门包不可变门禁（`harness-facade-stays-facade`）开始管这两个包——
「核心归上游、门面留本仓」从此有机器约束，不只是散文。

> 教训：**§一 的「上游现在提供了什么」是快照，不是事实**。本文自己在开头就标注过
> 这一点（「上游能力仍在搬家」），但 §四 的结论没有跟着复核，于是同一份文档里
> 「该让」清单与「该守」清单都成了可能过期的陈述。**结论表比事实表更容易腐烂**——
> 事实表改了会有人注意到（它与代码对不上），结论表没人核对。

**路线图影响**：`docs/SPLIT_PLAN.md` §二 的 L2 清单里，`intent.go`(→`agent/clarify`)、
`judge.go`(→`agent/blackboard`)、`supervisor.go`(→`agent/reflection`) 已有上游对应物；
`filterDisabled` 的卡点可能被 `runtime/hotplug.Plugboard` 解开。
**L2 不该再按原清单推进**，应改为「先清点上游已覆盖，再定剩余缺口」。

**仍未下沉的两处（2026-10-21 实测复核）**：

| 包 | 状态 | 理由 |
|---|---|---|
| `budget` | **不下沉**（2026-10-21 实测复核） | 上游 `textx/compact` 判的是「**该压缩了吗**」（阈值/熔断/token 三轨），本仓 `budget` 答的是「**输入侧可用上限是多少**」+「**压实计划怎么执行**」——**职责面不重叠**（本仓 `budget` 包内没有「该压了」的阈值判定，那正是上游的职责）。「窗口 − 预留」这一层两边是**同一条算式**，实测唯一差异是地板值（本仓 1 / 上游 0），且本仓有明确理由（地板 1 让后果由 `Overflow` / `Classify(critical)` 这类**看得见的信号**承接，而不是抛错把两种病因混成一个）。本仓多出的是**分段计量**（system/tools/rules/skills/mcp/conversation）与**宿主接口**（摘要生成 / 原文落盘各留一个接口）——强行合并会把编排侧概念塞进上游纯函数集 |
| `tooldisclose` | **不下沉**（2026-10-21 实测复核） | 上游 `mcpkit/mcp.Catalog` 是「**某个 server 声明了哪些工具名**」——`NewCatalog(server, tools []string)` + `Contains` + `Diff`（期望 vs 实际的差异）。本仓 `Catalog` 是「**模型可见的渐进披露目录**」——带 `Description` 与 `Schema`、有并发加载集（`loaded`）、有检索上限（`DefaultSearchLimit`/`MaxSearchLimit` 钳到 [1,5]）、有重名 first-wins 规则。**两者不是同一层**：上游那个是「server 面」，本仓这个是「模型面」。且上游**没有**渐进披露（确定性打分检索）与**两类失败二分**（未加载可重试 / 不在目录禁绕过）——那是本仓的真差异化 |

### 2026-10-21 更正：本节此前写着一句不实断言

初稿在这一节写过「**同一个上下文窗口在两处算出两个阈值**」。实测评数值后**该断言不成立**，
已删除。错在哪：

1. **对照方式本身错了**。初稿拿上游 `EffectiveWindow()`（纯窗口口径）与本仓
   `MaxInput()`（含「自设上限」的最终可用上限）直接比大小——**不是同一个东西**。
   正确的对照是拿本仓的「窗口−预留」推导分支（`baseInput <= 0`）去比，两边这才是
   同一条算式。
2. **职责面本就不同**。上游 `ShouldCompact` 答「该压了吗」，本仓 `budget` 答「上限是多少
   + 计划怎么执行」——本仓包内**没有**「该压了」的阈值判定。

> 教训与本节开头那条同源：**结论表比事实表更容易腐烂**，而这次腐烂的是**我自己写的
> 结论**。写「两个实现算出两个阈值」这种断言时，**必须先跑数值对照**——
> 判据对不对，要看输入相同、输出不同；只读代码注释是看不出口径差的。


## 五、遗留限制（如实记录）

- ✅ **已解除（2026-10-20）**：`delta` / `netguard` / `dispatch` / `eventbus` / `watchdog`
  曾因沙箱拒绝解压 `github.com/oagentkit/*` 模块 zip 而**在本沙箱内无法跑测试**——
  本轮 `netguard` 的新逻辑（按名拦截 + 空 Location）当时是在 `/tmp/vnet` **隔离仓**里
  用桩替换第三方依赖后实跑的，并做了反向验证，但**未在真实依赖树上复测**。
  现已解除：这些包的测试都在**真实依赖树**上跑通，`make test` 全绿
  （99 个测试文件、覆盖率 86.6%），按名拦截层与空 Location 行为均有回归用例钉住。
- 参照侧用的是 octop-harness **v1.0.2**（唯一带 `tests/` 的副本），代码是 **1.0.4**。
  补丁级差异下结论可用，但严格说未验过 1.0.4 的测试（其 wheel 不含测试）。
- `netguard.go` 包注释里仍有「告警」「本机」等词。架构门禁**不扫注释行**
  （执行体已下沉为 `archcheck` 包，落点在 `archcheck/scan.go:167`
  ——无条件跳过 `//` 开头的行），故从未被抓到。
  该豁免**只作用于 `forbid_regex` 类规则**：判 import 的 `forbid_import_regex`
  走 `go/ast`，注释本来就不是 import，豁免与否无差别。
  豁免是有意的（否则约 26 处正当注释会误红，其中大半正是解释「为什么排除领域概念」），
  但代价是注释里的领域词会静默累积。是否收窄尚未决策。
