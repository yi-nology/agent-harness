# agent-harness · 多智能体编排通用内核

> 一句话：**编排是代码，LLM 只做单环节**——本仓只提供确定性编排的**机制面**，
> **不认识任何领域概念**。

从上游私有宿主仓（代号见内部文档）拆出的内核层，宿主业务不公开。定位对标同族的
`oagentkit`（Agent/LLM 编排工具链矩阵）与 `oekit`（基础库）：**独立发版、版本化引入、
零 replace**（go.mod 含 replace 即门禁红；本地并行开发走 `make devlink` 的 workspace，见下）。

> **本文档导航**
> - [README](README.md)（本文）= 是什么、为什么这么设计、边界纪律
> - **[docs/USAGE.md](docs/USAGE.md) = 怎么接、接错了会怎样** ← 宿主开发者从这儿开始
> - **[docs/AI-GUIDE.md](docs/AI-GUIDE.md) = 怎么改、会被哪条门禁拦** ← 要动这个仓的 AI 代理从这儿开始
> - [AGENTS.md](AGENTS.md) = 在本仓干活的工作纪律
>
> **全部文档一览**（`docs/` 下的三份是**历史快照**，记录当时的判断与踩坑，
> 不是当前事实；当前事实一律以本文 + 门禁 + 源码为准）：
>
> | 文档 | 回答什么 | 性质 |
> |---|---|---|
> | [README](README.md) | 是什么、为什么这么设计、边界纪律、下游升级须知 | 活文档 |
> | [docs/USAGE.md](docs/USAGE.md) | 宿主开发者：怎么把 harness 接进自己产品 | 活文档 |
> | [docs/AI-GUIDE.md](docs/AI-GUIDE.md) | 改本仓的 AI 代理：任务 → 改哪些文件 → 哪条门禁拦 → 怎么验收 | 活文档 |
> | [AGENTS.md](AGENTS.md) | 在本仓干活的工作纪律（每条纪律的事故来源） | 活文档 |
> | [architecture-policy.yaml](architecture-policy.yaml) | 门禁**声明面**：模块拓扑 / 规则 / 阈值 / 例外 | 活文档（机器判定）|
> | [CHANGELOG.md](CHANGELOG.md) | 版本历史与破坏性变更 | 活文档（按 tag 切段生成）|
> | [docs/OAGENTKIT-OVERLAP.md](docs/OAGENTKIT-OVERLAP.md) | 本仓与上游 `oagentkit` 的能力边界：哪些该做、哪些上游已有 | 活文档 |
> | [docs/SANDBOX-DESIGN-20261021.md](docs/SANDBOX-DESIGN-20261021.md) | 执行沙箱的下一层：跨域执行与资源兜底（发现「内存无兜底」真缺陷 + 与上游 guardx 的分工）| 活文档 |
| [docs/HARNESS-BENCHMARK-20261021.md](docs/HARNESS-BENCHMARK-20261021.md) | 对标 Codex / DSH / Octop / Coze 五家：借鉴了什么、什么归宿主、什么本轮没做 | 活文档 |
> | [docs/CHECKPOINT.md](docs/CHECKPOINT.md) | 续跑契约的设计推导、三条不变量与跨进程闸 | 活文档 |
> | [docs/SPLIT_PLAN.md](docs/SPLIT_PLAN.md) | 从宿主仓拆分的分层依据（L1/L2/L3）| **历史快照**（2026-10-17 写于 10 个包时）|
> | [docs/MIGRATION_OAGENTKIT_20261018.md](docs/MIGRATION_OAGENTKIT_20261018.md) | 门面迁移与 errors 链路改写的执行记录 | **历史快照**（已于 2026-10-18 执行完毕）|
> | [docs/SELF-AUDIT-20261018.md](docs/SELF-AUDIT-20261018.md) | 独立复核发现并修复的缺陷 | **历史快照**（2026-10-18 收尾）|

## 边界：本仓有什么、没有什么

```
✓ 有（机制，与领域无关）        ✗ 没有（资产，属于宿主/领域包）
─────────────────────────      ──────────────────────────────
protocol   统一报告协议          专家包 / 专家清单 / 技能
           宽容解析 + 结构化校验  诊断链 / 编排图（faultChain 等）
           白名单校验            运维词表（巡检域、K8s 词面…）
           Markdown 渲染         目标类型（主机 / 集群 / Pod）
contract   编排运行契约（唯一边界） 宿主状态的落库方式与并发约束
statemachine 环节状态机          审批风险分级（restart/delete 四档）
eventbus   进程内有损广播        拓扑内容（谁允许派发谁）
policy     操作审计门            品牌文案（报告标题、引导 agent 名）
spill      输出预算溢出          事件持久化与表结构
budget     输入侧上下文预算      摘要生成的具体实现（走 Summarizer 接口）
offload    内联大对象外置        IM 通道 / 通知模板
toolfail   工具失败分类契约      HTTP 路由 / 鉴权
tooldisclose 渐进式工具披露 + 调用门禁    工具实现（MCP server）
guardset   守卫装配与拒绝合成      具体守卫实现（披露门/出站门/审计门）
pii        个人信息脱敏          工具目录内容（本包只管披露与门禁）
netguard   出站 SSRF 守卫        诊断链/巡检图（编排图属宿主）
dispatch   派发守卫（权限即拓扑） 摘要策略（何时压实属宿主）
ptc        程序化工具编排沙箱    环节语义（收口/幽灵结果的具体判据属宿主）
watchdog   工具调用配对监视      工具描述文案
checkpoint 编排续跑契约          快照的存储介质（SQLite / PG / 文件）
commitgate 环节终态独占提交闸    并行分支的冲突裁决（该归宿主还是机制）
delta      增量节流 + 标签分流   模型供应商路由
sideeffect 副作用至多一次契约   幂等键的存储实现（Redis / PG / etcd）
sidetest  副作用闸契约测试套件   存储实现的并发正确性（原子性 / 跨进程）
retryplan 重试处置决策          幂等键的具体存储（内核只给路径，不给实现）
```

判定口诀：**「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主。**

## 包清单

| 包 | 生产行数 | 外部依赖 | 职责 |
|---|---|---|---|
| `protocol` | 1850 | oekit/pkg、jsonschema | Report 契约 + 五级容错解析 + 白名单校验 + Markdown 渲染 |
| `dispatch` | 36 | oagentkit/agent | allow 矩阵物化 + 深度上限 + 自派发拒绝 + 环检测（拓扑经 `EdgeSource` 由宿主提供）|
| `eventbus` | 341 | oagentkit/runtime | 进程内有损广播（缓冲 chan）；持久化在发布点由宿主负责 |
| `policy` | 64 | cloudwego/eino、oagentkit/toolgate | 操作审计门：阈值只降不升、解析失败即拒 |
| `spill` | 251 | oekit/pkg | 输出预算溢出：提示契约 / 取回 / 按龄清理 |
| `pii` | 827 | oekit/pkg | 个人信息价值面脱敏（手机号 / 身份证四重校验）；运行时策略（开关/面/强度）整份单指针原子交换 + 变更订阅 `Subscribe`，代次从此不只可查、还可响应 |
| `netguard` | 1251 | oagentkit/guardx、oekit/pkg | 出站 URL 守卫（SSRF）：名字层（解析前按名拒）+ 解析后逐 IP 过 egress + 重定向逐跳重验。扩展名单**整份原子交换 + 代次可观测**（两次独立取会读到撕裂策略）。代次**只有一个真相**（策略指针内的 gen，全局值是它的投影；交换走 CAS 循环，故并发下代次唯一、单调、不回卷）。**`DialFunc` 把同一套判定落到建连那一刻**（名字层 + egress 预检 + 建连后核对真实 RemoteAddr）——闭合「校验 → 拨号」的 rebinding 窗口；判据仍是 egress，**不引入第二套 IP 策略**。放行面**具名且两层共用同一类型**（`Allow`，零值即严）；`NewClient`/`NewTransport` 是接线件——宿主一行把守卫挂上，且 client 可注入（测试连本机假服务器时**只在测试里**放宽回环）**非纯 stdlib**；DNS 解析与拨号间的 rebinding 窗口见包头「已知局限」与 dial.go；与上游 `oekit/pkg/urlguard` 的分工（IP 策略单源 egress 不换、域名后缀层本包为超集）同样见包头|
| `statemachine` | 79 | 无 | 环节状态机：条件迁移的纯函数件 |
| `contract` | 124 | 无 | 编排运行契约：编排引擎 ↔ 宿主状态的**唯一边界**（纯接口面，零实现）|
| `checkpoint` | 545 | oekit/pkg | 编排续跑契约：进度快照存取边界 + **「续跑而非重放」**的恢复判定（空洞不采信 / 终态不自动重开 / 序号单调闸）+ **跨进程 CAS 存取契约**（`StoreCAS.SaveIfNewer`）与提交序列固化（`Committer`：抢序号→落库→**仅失败时**回滚，宿主无法漏掉）|
| `commitgate` | 328 | oekit/pkg | 环节终态独占提交闸：同一 (run, 环节) 的终态只允许被提交一次（`statemachine` 是纯谓词，`SeqGate` 只排快照序号，两者都排不出「两条分支都赢了」）。终态词表由宿主注入。**放闸靠凭证**：`Acquire` 返回唯一令牌、`ReleaseLease` 凭令牌精确释放——靠 (状态,分支,序号) 认人的旧 `Release` 分辨不出两次同名同序的占用，迟到的释放会放掉别人的闸 |
| `budget` | 756 | oekit/pkg | 上下文预算：分段计量 + 窗口钳位 + 压实计划骨架（三重空转守卫；摘要生成与原文落盘各留宿主接口）|
| `offload` | 589 | oekit/pkg | 内联大对象外置：sha 去重 + **两阶段**（首次标记、后续轮才替换占位）+ fail-soft（与 `spill` 是姊妹机制）|
| `tooldisclose` | 862 | oagentkit/guardx、oekit/pkg | 渐进式工具披露（确定性打分检索换完整 schema）+ 执行门禁（**未加载可重试 / 不在目录禁止绕过** 两类严格二分，**拒绝即激活**，重试不绕路）+ 接成 `invoke` 链的一环（`Stage`，链上 Reason 直接回面向模型的指引）|
| `toolfail` | 89 | oagentkit/guardx | 工具失败的错误分类契约（**门面**，核心在 `oagentkit/guardx/toolfail`）：类别 → 可重试性/退避/需人工/重发安全/模型指引为**数据表**而非散落 if；不可归类一律不可重试。**重发安全与重试价值是两个正交维度**——超时值得重试但服务端可能已执行，原样重发即重复施加副作用；`Guidance()` 是模型侧的唯一消费方。判定口径必须跨宿主一致，故核心归上游安全层，本仓只保调用面稳定 |
| `toolname` | 96 | 无 | 工具名 → 程序内可用的 JS 标识符：**一条单一真相**。`IsJSIdent`（能不能直接用）+ `Normalize`（不能的话用哪个）。生产侧（`tooldisclose` 派生调用形态）与消费侧（`ptc` 挂载绑定并校验形态里的函数名）共用它——两侧必须逐字一致，否则就是「这里派得出、那里认不出」的夹缝。规整**不保证唯一**（`get-logs` 与 `get_logs` 同名），撞不撞由调用方判 |
| `sideeffect` | 540 | oekit/pkg | 副作用「至多一次」的跨进程契约：**幂等键**（同一逻辑动作重试不重复施加）+ **租约**（同一资源独占，带 TTL）+ **栅栏令牌**（过期持有者的迟到写入被下游拒绝）+ **下游拒收契约**（`FencedSink`：比较与写入须同一次原子操作，否则两个持有者会同时读到同一个旧值、双双通过检查，更旧的那次写入反而后落覆盖新的）。上游三个判据共用**一次原子操作**（`Store.Acquire`）——拆成三份存储就会出现「租约过了但幂等没过」的夹缝，允许同一动作被施加两次。执行失败**不记账**（没做成的不能记成做成了），但**释放闸**（否则失败残留的闸会把后续重试全挡成「别人占着」）。**施加前有资格检查**（`ExecuteWith` 两段式：准备 → 检查 → 施加）——闸已易主时**根本不施加**，因为 Complete 被拒只说明没记上账，**副作用早已生效，事后补救不了**。承诺的是**至多一次**而非 exactly-once——后者要求下游也接受幂等键 |
| `sidetest` | 436 | oekit/pkg | sideeffect.Store 的**共享契约测试套件**：宿主写完存储实现后跑一遍，违反哪条契约就红在哪一条——免得「各写一遍必然各判一套，漂移到没人发现为止」。10 条子测试覆盖「只做一次 / 已完成优先于占用 / 未过期时争用 / 过期后接管 / 令牌单调 / 过期持有者不得记账 / 过期持有者不得放闸 / 释放不等于完成 / 续期延长且不换令牌 / 闸已易主后续期被拒」。范式沿用上游 `cachetest` / `singleflighttest`：**硬失败不静默跳过**（显式接入套件＝声明要满足契约，跳过等于把「没测过」伪装成「通过了」）。时钟可注入（`ClockedStore`），否则过期场景恒不触发，套件会变成一组空断言 |
| `retryplan` | 192 | oagentkit/guardx | 重试处置决策：把「该不该重试」（`toolfail` 判）与「重试会不会重复施加」（`sideeffect` 判）合成**一条可执行结论**——三档路径（不重试 / 原样重发 / 走幂等键重放）。与 `guardset` 同族：**组合逻辑归内核，判定归各自机制**（各宿主各写一遍组合逻辑，必然各判一套）。关键一档是「可重试但原样重发不安全」——`toolfail` 只说「先确认上一次是否已生效」，**没说什么怎么做到**，而这正是幂等键的活；故该档判为 `PathReplayed`（走幂等键）。关键取舍：**没有幂等键就不许「猜着重试」**（降级为不重试，而非原样重发赌一次） |
| `delta` | 253 | oagentkit/runtime | 增量节流：分片合并 + 权威整段优先 + 确定性收口扫序 |
| `pending` | 62 | 无 | 多轮挂起态载体：单一编码入口 + 宽容解码（非法值降级为无挂起）|
| `reminder` | 30 | oagentkit/guardx | 系统注入提示的防伪包裹（门面）：核心已下沉 `guardx/injectband`，本包只保调用路径稳定，宿主调用点一行不动 |
| `drain` | 188 | oekit/logx、oekit/pkg | 优雅排水：轮 ctx 取消脱钩 + 等在途收敛 + 预算耗尽强制收刀（经 `Turns` 接口）|
| `ptc` | 1789 | dop251/goja、oagentkit/guardx、oekit/concurrency、oekit/pkg | 程序化工具编排：模型写程序 → 沙箱 VM 执行 → 经 `bindings` 调宿主工具；三重预算（墙钟/调用数/字节数）替代 OS 沙盘。**N 轮 ReAct 往返塌缩成 1 轮，原始数据不过模型面**；绑定可带 Signature 让模型按位置传参（可选形参以 `?` 标记、未传则不出现在 JSON 里；形态里的函数名必须是**净化后**的名字，规则见 `toolname`）；**程序内的 host.<tool>() 与普通工具调用走同一套 Stage 判定环**（不做这一步，本包就成了整条链上唯一绕过所有门禁的路径）；绑定名净化后撞名即装配期拒（否则后绑者顶替前绑者，副作用错位无信号）。**新增子进程后端**（`ptc-subprocess`）：把执行搬出宿主进程，用内核地址空间上限（RLIMIT_AS）兑现 `MaxMemory`——进程内形态兜不住内存（Go 运行时没有「只给一个 VM 限内存」的开关，goja 公开 API 也没有内存限制），故子进程形态**如实声明**自己兑现这一项（`sandbox.Capable`），兜不住的后端在登记簿里不会被选中 |
| `sandbox` | 76 | oagentkit/guardx | 执行沙箱的**档位语义**与**后端协议**（**门面**，核心在 `oagentkit/guardx/sandbox`）：`Level`（进程内解释器 → 内核级 → 硬件虚拟化，单调可比，零值即最弱档——漏声明的后端只落到最弱一档，方向是安全的那一侧）+ `Provider`（后端自报提供哪一档并执行程序）+ `Registry`（登记簿；按要求档位选**最弱**满足者，**无满足者即拒绝、不降级**——降级会让「宿主声明的强度」与「实际执行的强度」之间没有信号，档位机制就成了装饰）。零内部依赖，ptc 反过来依赖它（**沙箱机制不认识任何后端，后端认识沙箱**）。**已知局限**：档位是后端自报的，内核不校验其真实性——宿主不信任某后端，唯一办法是不登记它。**档位声明要签字**：后端的档位登记在 policy 的 `sandbox_providers` 段（人 review 时看得见「它凭什么说自己有这一档」），门禁 `harness-sandbox-provider-declared` 核对登记与代码事实不漂移（走 `go/ast` 按方法指纹认出后端实现）|
| `watchdog` | 127 | 无| 被动配对不变量监视：环节收口仍有未执行声明 / 结果无对应声明；**只告警不裁决**（热路径裁决在守卫侧）|
| `guardset` | 406 | oagentkit/guardx、oekit/pkg | 守卫装配契约：一组**相互独立**的判定一并求值，多条拒绝合成一条（按 Rank 定序、**不可重试优先**的保守裁决、指引指向真正卡住的那一条）。接成 `invoke` 链上单独一环——链在第一个拒绝处就收口，后面的守卫永远没机会说话，模型一次只听得到一个理由，这正是本包补的那一面；链本身的顺序/留痕/panic 兜底沿用上游，不重复实现 |
| `archcheck` | 2665 | gopkg.in/yaml.v3 | 架构门禁执行体：`architecture-policy.yaml` 的**可执行面**（规则判定 / 门包判定 / 依赖方向判定 / policy 自校验 / 结构阈值）。**可导入包**——宿主仓复用同一套判据，不必各写一遍（各写一遍必然各判一套，漂移到没人发现为止）。判据与执行体分离：结构性判定一律走 `go/ast`（import 声明有六种以上书写形态，逐行正则必然漏）。依赖方向判三条：越界内部依赖 / 越界跨仓依赖（`ext:` 登记）/ 未登记包目录。**执行沙箱后端的档位登记**（`sandbox_providers` 段 ↔ 代码事实，走 `go/ast` 按方法指纹认出后端实现，核对档位漂移与未登记）。**陈旧包根当天即红**——root 指向空目录即「声明与事实已偏离」，而没有执行体时这句话没人读 |

> 「生产行数」口径：`wc -l` 统计 `*.go` 且**不含** `_test.go`——包体量看的是要交付的实现，
> 不是它的测试。依赖列只列**仓外** import；「无」表示该包生产代码只用 stdlib。
>
> 本表由 `make check` 末步的 `docsync` 机器校验（`cmd/docsync`），判定分两类：
> **依赖列逐项相等**——加依赖是显式动作，必须同步；**行数按 25% 相对容差判定**
> ——容差内的逐行涨落不拦，改一行代码不必顺手改文档，而跨量级的误述
> （某包声称 ~86 行、实际 412 行那种）仍然拦下。行数取较大者为分母，
> 「少写」与「夸大」同等对待。职责列与依赖列非空也在判定内。

## 特性

### 边界不是文档，是门禁

「内核不许长出领域知识」这句话写在散文里就会烂掉。本仓把它做成了 7 条正则规则
+ 2 条 `go/ast` 结构性规则，全部挂在 `make check` 上，CI 只执行这一条。
散文与门禁的分界线很清楚：**散文不是门禁**——真有人往门面包里塞一份实现，
规则不响，结果是与上游并存的第二份实现。

```
policy/     只有类型别名、常量别名、单条上游转发函数 —— 多一条语句就红
modules[]   包间依赖方向逐条登记，未登记的包目录对本门禁隐形（所以必须同步登记）
rules[]     域词污染、裸 goroutine、printf 回调、双 errors 包
```

### 工具调用只有一条路

渐进披露、披露门、多守卫合成、失败分类、程序内调用——**全部接成同一条判定环**。
包括 `ptc` 里模型自己写的 `host.<tool>()`，也一样过。少接一段，那个位置就成了
整条链上唯一绕过所有门禁的路径。

```
普通工具调用 ─┐
             ├─→ invoke.Stage 链 ─→ guardset(并求值·合成拒绝) ─→ tooldisclose(披露门)
ptc 程序内   ─┘                            └─→ policy(审计门) ─→ dispatch(派发拓扑)
```

`guardset` 补的是「链在第一个拒绝处就收口、模型一次只听得到一个理由」那一面：
一组**相互独立**的判定一并求值，多条拒绝合成一条，取「全部可重试才算可重试」的
保守侧，并点出该先处理哪一条。

### 崩了能接着跑，而且不会「两条分支都赢了」

`checkpoint` 把恢复判定做成纯函数而不是留给宿主临场决定——临场决定必然选最坏的
那个（要么盲目全量重跑、要么盲目跳过）。三条硬规则：**空洞不采信**（只信连续完成的
前缀，失序时宁可多跑几步）、**终态不自动重开**（拒绝替宿主决定）、**序号单调闸**。

`commitgate` 补的是另一个洞：`statemachine` 是纯谓词，只答「这次迁移合不合法」，
不答「有没有人已经提交过了」；`SeqGate` 能排序记录，排不出「两条并发分支都以为自己
赢了」。终态独占权单独一道闸，且 **Commit / Release 必须成对**。

### 上下文是有限预算，不是无限历史

三件套分工泾渭分明：**`budget`** 管输入侧分段计量与压实（只给总数无法区分
「工具面膨胀」与「历史膨胀」，而两者处置完全不同）、**`offload`** 管二进制本体躺在
消息里（两阶段：首次只标记不替换，否则模型会引用一个自己从没看过的东西）、
**`spill`** 管输出侧截断与分页取回。

### 失败是数据表，不是散落的 if

`toolfail` 把「类别 → 可重试性 / 退避 / 需人工 / 重发安全 / 模型指引」做成数据表。
其中**重发安全与重试价值是两个正交维度**——超时值得重试，但服务端可能已执行，
原样重发就是重复施加副作用。不可归类一律不可重试（fail-closed）。

### N 轮 ReAct 往返塌缩成 1 轮

`ptc` 让模型写程序、进程内 goja VM 执行、程序内经 bindings 调宿主工具，
只把归并后的小结果带回上下文：**原始数据不过模型面**。三重预算（墙钟 / 调用数 /
字节数）替代 OS 沙盘，纯 Go 零 CGO，无 Node 分发面。

### 工程底座

| 指标 | 实测 |
|---|---|
| 生产代码 | 14,806 行 / 84 文件（不含 `_test.go`） |
| 测试 | 111 个测试文件 |
| 覆盖率 | **86.1%**（门槛 60%，真阈值不是摆设） |
| 结构阈值 | `max_file_lines: 400`，`max_methods_per_type: 25`（按**实质方法数**判，见下），`enforce: true` |
| 门禁 | 8 条规则（`rules` 段）+ 3 条 `go/ast` 结构规则（门包不变 / 依赖方向 / 沙箱档位登记）+ **5 个门包**（`dispatch`/`policy`/`reminder`/`toolfail`/`sandbox`——核心归上游、只保调用面稳定）+ 28 包依赖方向 + README 包表一致性 |
| 例外登记 | **0** |

> ⚠️ **上表不在 `docsync` 的判定面内**——`docsync` 只校验下面那张「包清单」表的
> 包名、依赖列与行数。**本表是纯散文**：数字漂了不会有人拦。
> v0.6.4 那次修「工程底座」三处漂移（生产代码 11,491 行/66 文件 → 13,336 行/77 文件、
> 测试 84 → 99 个文件、覆盖率 80.4% → 86.6%）时，这三处已经过期了**三个月**——
> 它们是 `archcheck` 从 `cmd/` 下沉成独立包（2026-10-19）之前的旧值，此后没人更新。
> 这正是本仓「散文不是门禁」那条纪律的反面案例：**机器只管得着被写成规则的那部分**。
> 改这张表时请手动重算，**别信上一版**。

## 头号红线：不得长出领域知识

拆分做完后最现实的风险不是拆不动，而是**业务代码一年年渗回内核**，等发现时
harness 已经退化成「换了个名字的 monolith」。所以：

```bash
make architecture-check   # 域词污染 + 依赖方向，违反即非零退出
```

规则源 [`architecture-policy.yaml`](architecture-policy.yaml)，执行体是 `archcheck` 包
（`cmd/archcheck` 只是它的 CLI 装配，`make architecture-check` 调的就是它）：

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-no-domain-vocabulary` | **error** | 生产代码里的运维领域词（故障链/巡检/告警/本机/内存分析…）|
| `harness-test-fixture-domain-vocabulary` | **error** | 测试夹具用了真实运维词面（须改合成数据）。原 15 处 warn 级债已于 2026-10-18 清偿并升级为阻断 |
| `harness-no-host-dependency` | **error** | 任何对宿主仓的 import |
| `harness-no-domain-runtime-deps` | **error** | 对宿主仓 / 旧门面仓 / **已退役的 `agentkit` 门面** 的 import |

另有两条不走正则表、由 `go/ast` 判定的结构性规则（判据是语法结构，逐行看文本判不出）：

| 规则 | 级别 | 拦什么 | 落点 |
|---|---|---|---|
| `harness-facade-stays-facade` | **error** | `dispatch`/`policy`/`reminder` 三个门面包长出实现（声明新类型、给类型挂方法、函数体超过一条转发语句、调用非上游包、import 非允许依赖）| `policy.yaml` 的 `facades` 段 + `archcheck/facade.go` |
| `harness-module-dependency-direction` | **error** | 包间依赖超出 `modules[].requires` 声明的方向；越界的**跨仓**依赖（`ext:` 面已登记却没在 requires 声明）；以及**未登记的包目录**（它对本门禁隐形，任何模块都能 import 而不判违规）| `policy.yaml` 的 `modules` / `dependency_direction` 段 + `archcheck/deps.go` |

> 依赖方向这条是 2026-10-18 补的执行体。`modules` 段此前把 22 个包的依赖拓扑
> 写得清清楚楚，README 也宣称门禁查「域词污染 + 依赖方向」——但执行器把
> `modules` 解析成 `[]struct{}`，`requires` 连内存都没进，于是那一半是空的，
> 且声明已经开始漂移（`watchdog` 写 `requires: []`，实际 import 了 `eventbus`）。
> 新规则上线第一次跑就抓出 `commitgate` **从未登记**过——它早已在 README 包表里，
> 却在依赖拓扑上完全隐形。这类问题零报错、零告警、构建全绿，只有把全图摊开才看得见，
> 正是它必须由机器而不是 review 来看的原因。

> **2026-10-20 补：本仓的上游依赖面也纳入这张表。** 此前它只管内部包，管不到
> 「本仓用了哪个上游矩阵模块」——而上游把能力搬家的频率不低（这一轮就把
> `toolgate/invoke` 挪到了 `guardx/invoke`、`obs/obsx` 挪到了 `llm/obsx`），
> 搬家在本仓门禁眼里什么都没发生。现在第一方矩阵（`github.com/oagentkit` +
> `github.com/oekit`）整体登记为受管包面，各包在 `requires` 里声明自己实际用了
> 哪个矩阵模块（`ext:github.com/oagentkit/guardx` 这种粒度）。

### 门禁执行体可被宿主复用（2026-10-19）

执行体此前锁在 `package main` 里，宿主仓够不着——于是每个宿主要么各写一遍判据
（各判一套，漂移到没人发现为止），要么干脆不写（policy 里的依赖拓扑表变成**死条文**：
躺在文件里、看起来有人维护过，实际一条都不判）。现在它是可导入的包：

```go
import (
	"path/filepath"

	"github.com/yi-nology/agent-harness/archcheck"
)

// ① 定位仓根：从当前目录向上找 architecture-policy.yaml
//    （宿主仓把自己的 policy 放仓根，本仓同样如此——**不在 docs/ 下**）
root, err := archcheck.FindRoot(".")
if err != nil {
    return err
}

// ② 加载声明面。policy 解析失败 = 门禁给不出结论，必须响亮失败
pol, err := archcheck.Load(filepath.Join(root, "architecture-policy.yaml"))
if err != nil {
    return err
}

// ③ 跑判定
res, err := archcheck.Run(root, pol)
if err != nil {
    return err
}

// ④ 出报告 + ⑤ 判阻断
archcheck.WriteReport(os.Stderr, res, archcheck.ReportOptions{Full: false})
if res.HasBlocking() {
    os.Exit(1)
}
```

> `archcheck.PolicyPath(root, pol)` **不是加载路径**——它返回的是**报告里显示用的
> 相对路径**（缺省即 `"architecture-policy.yaml"`）。拿它去 `Load` 会读到错的地方。

宿主复用时要知道的六条：

1. **policy 的 `modules` 段要求每个 root 指向真实存在的包目录**——陈旧的 root 会被
   policy 自校验判红（这正是「机制包迁出当天即红」的落点）。
2. **跨仓依赖用 `ext:<import 路径前缀>` 登记**，且该前缀必须落在
   `dependency_direction.ext_prefixes` 声明的受管包面之内。两层结构的理由见
   `archcheck/deps.go` 包头：若「哪些外部包受管」也只由各模块自己声明，那么最后
   一个声明它的模块撤掉声明时，整片包面随之消失，那条边就静默失管了。
3. **未登记包判定按「包目录」粒度**，不是顶层目录——宿主的包根通常是嵌套的
   （`internal/xxx`），按顶层目录判会把整棵 `internal/` 误报成一个未登记的包。
4. **判据与实现分离**：policy 是声明，本包是执行体。宿主可以只用自己的声明面
   （只写 `modules` 段 + `dependency_direction` 段），复用本包的全部判定。
5. **判据面只有两种**（import 路径 / 源码文本），没有 `forbid_call_regex` /
   `allow_paths` 这类字段——常见诉求用现有字段表达：

   | 诉求 | 表达方式 |
   |---|---|
   | 某个第三方包只允许经某一个适配层引入（路径白名单）| `forbid_import_regex` + `scope_exclude` |
   | 某目录禁止裸 `fetch(`，但 api 客户端目录与两个 SSE 钩子除外 | `forbid_regex`（匹配调用形态）+ `scope_exclude` |

   写了不存在的字段会被 YAML **静默丢弃**（那条规则于是变成「声明还在、一条都判
   不了」），policy 自校验会把它报成「规则无执行体」并给出可执行的下一步。
6. **`max_methods_per_type` 比的是「实质方法数」，不是方法总数**（v0.6.5 起）。
   类型退化成 façade（实现下沉到子包、自己只做转发）之后，方法数 ≈ **公共 API
   表面规模**，不是复杂度集中度——拿它判 god object 是「把正确的工作判成错的」，
   逼维护者在「撤销一次正确的重构」与「调高阈值（等于放弃判据）」之间二选一。
   故「转发壳」（方法体只有一条转发语句、且剥掉注释与空白后长度 ≤
   `forwarding_shell_max_chars`，缺省 140）**不计入**阈值；报告里同时给出方法总数
   与壳数，原始信号不丢。

   | policy 写法 | 含义 |
   |---|---|
   | 不写 `forwarding_shell_max_chars` | 用缺省 140（**老 policy 无需改动即可享受修正**）|
   | `forwarding_shell_max_chars: 0` | 关闭该区分，所有方法都算实质方法，判定逐字退回旧行为 |
   | `forwarding_shell_max_chars: <N>` | 自定义刻度（方法体有效长度 ≤ N 才算壳）|

   判据走 `go/ast`（不是逐行正则）——「一个函数体是不是只有一条转发语句」是结构性
   问题，只有语法树能判。实现见 `archcheck/godobject.go`，判据的由来与实测数据见
   issue #13。

### 与 oekit 家族的一致性（2026-10-17 补齐）

`oekit`（基础库）→ `oagentkit`（agent 原语）→ `agent-harness`（编排内核）→
上游宿主仓是同一条依赖链。harness 曾是矩阵里唯一不守家规的成员，现已把
可机械判定的三条也做成门禁（理由详见 [AGENTS.md](AGENTS.md)）：

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-no-bare-goroutine` | **error** | 生产代码裸 `go func`（panic 无法被外层 recover，会杀进程）|
| `harness-no-printf-log-callback` | **error** | 公开 API 里的 `logf func(string, ...any)`（printf 回调焊进签名即不可逆 API 债）|
| `harness-single-errors-package` | **error** | stdlib `"errors"` 与 `github.com/pkg/errors`（全仓只允许 `oekit/pkg/errors` 一套，打断 BizError 的 `As/Is`）|

三条均已做**反向验证**（注入违规 → 报错 + 退出码 1）。

### 执行沙箱：入口唯一 + 档位登记（2026-10-21 补齐）

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-sandbox-execution-single-entry` | **error** | 生产代码在 `sandbox` 包之外直接 `import "os/exec"`——外部程序执行只允许经 `sandbox` 包登记的后端 |
| `harness-sandbox-provider-declared` | **error** | 执行沙箱后端的隔离档位没登记在 `sandbox_providers` 段，或登记的档位与代码事实不一致 |

**为什么需要它**：隔离档位这套机制要成立，前提是「被执行的程序都在某个**已登记
的后端**里跑」。一旦某个包绕过登记簿直接起一个外部进程，那次执行就没有档位——
宿主按「它在受控后端里」做的风险判断，对那次执行全部落空，而**没有任何信号**：
构建全绿、测试全绿、审计日志里只有一条普通的进程记录。

这类绕过最常见的形态是 `os/exec`（Go 里起外部进程的标准入口），加进某个包
只要一行。于是本规则把「起外部进程」收口到 `sandbox` 包，其余生产代码一律不得
直接 import。判定面是 **import 路径**（走 `go/ast`），单行/块/别名/空白导入
一视同仁。

**已知边界（写明，而不是假装覆盖）**：本规则只拦 `os/exec` 这一个标准入口。
直接用 `syscall` 起进程的形态拦不住——那需要拦整个 `syscall` 面，代价远大于
收益，且当前本仓生产代码零 `os/exec`。规则拦的是**常见形态**，不是全部形态。

已做**反向验证**（注入违规 → 报错 + 退出码 1；同一形态放进 `sandbox` 包不报）。

**第二条（档位登记）为什么需要**：档位是**后端自报**的。若那份声明只活在代码里，
它有三重失效：review 时看不见（档位是实现里的一个常量，混在方法之间）、改了没人
会响（漂移）、标高了没人管（后端自称「内核级」其实只是进程内解释器，宿主据此做的
风险判断全部落空）。

于是 policy 的 `sandbox_providers` 段成了档位声明的**签字处**：每个后端一条登记
（标识 / 档位 / 所在包），「它凭什么说自己有这一档」写在该条的 `notes` 里——
那是人 review 时会看到的地方。门禁核对两件事（走 `go/ast`）：**登记 ↔ 代码事实
一致**（档位漂移 = 代码改了登记没改）、**实现 ↔ 登记齐全**（未登记的后端，
它的档位声明不在被复核的清单里）。

**这条门禁判不了什么**：「后端实际提供的隔离强度是否兑现其声明」——那要审实现，
机械判不了。它保证的是三件事：声明**可见**（在 policy 里）、**不漂移**（门禁核对）、
**有签字**（人 review 时看到那一行）。

已做**反向验证**：改代码里的档位 → 红在 `drift` 上；删掉登记 → 红在 `unregistered`
上；撤销后归零。

### 门面包不可变（2026-10-18 补齐）

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-facade-stays-facade` | **error** | `dispatch`/`policy`/`reminder` 三个门面包长出实现（声明新类型、给类型挂方法、函数体超过一条语句、调用非上游包、import 非允许依赖）|

**为什么单独一条**：现有七条规则查的全是**写法**（用什么词、import 什么、
goroutine 怎么起、回调怎么声明、errors 怎么包），没有一条查「本仓自己的门面有没有
被自己改胖」。[`docs/OAGENTKIT-OVERLAP.md`](docs/OAGENTKIT-OVERLAP.md) 里那句
「保持门面，别再在本仓加逻辑」是**散文**——散文不是门禁。真有人往 `policy/` 里加
一份实现，规则不会响，结果是与上游并存的第二份实现。

**为什么用 AST 而不是正则**：正则逐行看文本，判不出「一个函数体是不是只有一条
上游转发」，而这条纪律恰恰是结构性的。执行体在 `archcheck/facade.go`（`go/ast`），
不走 `rules` 段的正则表。门面包只允许四种声明：类型别名、常量别名、单条上游转发函数、
以及允许集合内的 import。「加真逻辑」三者必居其一（要新类型、要多条语句、或要新依赖），
所以规则不能被「顺手写两行」绕过去。五条判定路径均已反向验证（注入违规 → 退出码 1）。

**例外必须显式登记**（policy `exceptions` 段：rule + path + line 三元组精确豁免 +
必须写 `until` 与拆除条件）。**当前例外数 = 0。**

> 首轮门禁曾抓出 `protocol/report_render.go` 两处硬编码运维中文（`AgentGuidance`
> 哨兵名 + 默认报告标题），均已清偿并有测试钉住：

| 原硬编码 | 现形态 | 宿主怎么用 |
|---|---|---|
| `AgentGuidance = "运维任务调度Agent"` | `Report.Guidance` 结构化置位 + `SetGuidanceAgentNames()` 命名注册 | 装配期 `protocol.SetGuidanceAgentNames("你的引导声位名")`，或逐条报告置 `Guidance: true`（推荐） |
| `title = "运维诊断报告"` | `RenderOptions.DefaultTitle` + 中性兜底「诊断报告」 | `protocol.RenderViewWith(r, view, protocol.RenderOptions{}.WithTitle("你的品牌标题"))` |

`RenderView` / `Render` 签名不变，老调用零漂移。

## 通用化判据

内核每加一个能力，先过这一问：

> **换个宿主，这段代码要不要改？** 要 → 属于宿主，不该在内核。

落到三条可执行约定：

1. **判定语义归内核，判定依据归宿主。** 例：「这是引导式直答还是诊断报告」是
   机制（内核），「哪条算引导」是命名（宿主）——所以内核给结构化字段 + 注册表，
   不硬编码名字。
2. **展示文案归宿主。** 标题、兜底、品牌词一律走 `RenderOptions` 注入，
   内核只留中性默认值。
3. **测试夹具用合成数据。** 夹具是契约示例，示例带领域味等于把契约钉死在
   某个领域上。

## 快速开始

**要接入本仓**：直接看 **[docs/USAGE.md](docs/USAGE.md)**，那是按「宿主接入顺序」
组织的完整指南——九组能力逐个讲清「职责 → 最小集成片段 → 关键符号 → 坑」，
末尾有一张按症状索引的坑速查表。

**要改本仓**：

```bash
make build          # 编译 archcheck
make test           # 全量单测
make check          # fmt + vet + lint + test + cover-gate + architecture-check + docsync + 仓库卫生
make architecture-check --report   # 看全量违规清单（默认前 10 条）
```

> macOS：本机 go build/test 一律带 `CGO_ENABLED=0`（新 SDK 27.0 与 CLT linker
> 不兼容 + 许可未接受），Makefile 已内置。
>
> `make check` 需要 `golangci-lint`（CI 钉 v2.14.0）；未安装时该目标**明确报错退出**，
> 不静默跳过——没人跑的门禁等于没有门禁。

## 本地多仓并行开发

本仓处在 `oagentkit` / `oekit` 矩阵里，改本仓时经常要同时改上游仓（如 guardx）。
这里有个必须知道的陷阱：

> **Go 的 `replace` 只在主模块生效，依赖模块的 replace 不级联。**

在本仓 `go.mod` 里写 `replace github.com/oagentkit/guardx => ./.guardx-work/guardx`，
本仓构建全绿（自己就是主模块）；但下游宿主把本仓当依赖时，这条 replace 被**静默忽略**，
guardx 按 MVS 回落到已发布的旧版本，于是报
`no required module provides package github.com/oagentkit/guardx/injectband`——
错指 guardx，真正原因（本仓依赖了未发布的新包）不出现，一行本地验证代码断掉所有下游。

正确姿势是 workspace（`use` 跨仓生效，且不碰任何 go.mod）：

```bash
git clone https://github.com/oagentkit/guardx.git .guardx-work/guardx   # 未跟踪
make devlink     # 生成本地 go.work，把 guardx 工作副本接入
# ...改 guardx，本仓构建即时生效...
make devunlink   # 断开，构建回到 go.mod 声明的已发布版本
```

配套纪律：

1. **go.mod 零 replace。** `go.mod 不得含 replace` / `go.work 不得入库` 两条门禁在
   `make check` 里（本地与 CI 同一条路径），本地 `go.work` 由 `.gitignore` 兜底。
2. **go.mod 里 require 的版本必须是已发布的 tag。** 若确需依赖未发布的新包，
   用 `make devlink`，不要动 go.mod。CI 在干净环境构建，未发布版本解析不到直接红——
   这是发版顺序纪律的第二道防线。
3. **workspace 期间别跑 `go work sync`。** 它按 workspace 的构建列表**回写**各模块的
   `go.mod` / `go.sum`：仓不全时用不到的依赖会被直接删掉（实测把 oagentkit 的四个依赖
   整片剥掉）。要整合同步，先 `make devunlink`，再在单模块下 `go mod tidy`。
4. **`make devlink` 可重复执行。** 已有 `go.work` 时走 `go work use -add` 追加而不是
   重建，所以往 workspace 里加第二个仓不会抹掉别人已加的条目。
5. **验下游而不是只验本仓。** 本仓作为主模块绿，不代表作为依赖也绿（replace 的失效就发生在这个差别里）。

## 下游升级须知（破坏性变更）

本仓的公开 API 里有一部分**类型来自上游矩阵**（`invoke` 判定环原语）。上游把它换了包，
本仓跟随，**下游的 import 路径就要跟着换**——这类变更记在这里，不记在 tag 注解里
（tag 注解会沉到 git 历史里，README 不会）。

| 本仓版本 | 变更 | 下游要做什么 |
|---|---|---|
| **v0.8.0** | **`sideeffect.Store` 新增 `Renew` 方法**（续期 + 校验持有资格），并新增 `sideeffect.FencedSink` 契约（下游拒收陈旧写入）；新增 `retryplan` 包（重试处置决策） | **接口层面的破坏性变更**：宿主若已实现 `sideeffect.Store`，需补上 `Renew(ctx, act, lease, ttl) (Lease, error)` 方法——语义见包头注释（**续期必须同时校验令牌仍是最新代**，否则是「假续期」）。宿主若已实现下游栅栏拒收，可改用 `FencedSink` 契约对齐比较口径与原子性要求。`FencedSink` 与 `retryplan` 是**新增**的，不影响既有代码 |
| **v0.7.0** | **`toolfail` 与 `sandbox` 下沉上游 `oagentkit/guardx`（v0.9.0）**，本仓转**门面**（类型别名 + 常量别名 + 单条上游转发）；依赖地板抬到上游本轮新版本（`guardx` v0.8.2→v0.9.0、`llm` v0.14.1→v0.14.2、`agent` v0.10.0→v0.10.1）| **调用面逐字兼容，宿主代码不用改**——类型别名是编译期同一类型，宿主现有的 `errors.As` / `errors.Is` 判定链继续有效。**但必须 `go mod tidy`**：地板抬高对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。下游若直接 import 过 `github.com/yi-nology/agent-harness/toolfail` 或 `.../sandbox` 的**具体实现**（如自造 `*Failure`、注册自定义 Provider），请改 import 上游路径 `github.com/oagentkit/guardx/toolfail` / `.../sandbox`——门面只保转发，不提供实现。逐版差异见 [CHANGELOG.md](CHANGELOG.md) 的 v0.7.0 段 |
| **v0.6.5** | **依赖地板追平上游本轮新版本**（`oekit/pkg` v0.17.0→v0.18.0、`oagentkit/obs` v0.9.2→v0.9.3、`oagentkit/mcpkit` v0.8.2→v0.8.3）；**`archcheck` 的 god object 判据改比「实质方法数」**（方法总数 − 转发壳数），policy 新增可选字段 `forwarding_shell_max_chars` | **`go mod tidy`**——地板抬高对下游是隐式 breaking：不跑就报 `updates to go.mod needed`，且报错不提真正原因。**判据变更对下游无感**：policy 不写新字段即按缺省 140 生效；若宿主要保持旧语义（方法总数），在自己 policy 里显式写 `forwarding_shell_max_chars: 0` 即可逐字退回旧行为。逐模块版本差异与判据推导见 [CHANGELOG.md](CHANGELOG.md) 的 v0.6.5 段 |
| **v0.6.4** | 纯文档：新增 `docs/AI-GUIDE.md`（面向 AI 代理的操作手册），并校准 README「工程底座」表的三处漂移 | 无（无 API 变更、无地板抬高）|
| **v0.6.3** | **依赖地板追平上游本轮新版本**（llm / obs / mcpkit / concurrency / core 共 5 个模块）| **`go mod tidy`**——地板抬高对下游是隐式 breaking：不跑就报 `updates to go.mod needed`，且报错不提真正原因。逐模块版本差异与「是否影响本仓」已逐个核对，写在 [CHANGELOG.md](CHANGELOG.md) 的 v0.6.3 段 |
| **v0.6.2** | 纯文档：发版纪律补上「发 GitHub Release」这一步，并补齐本表此前缺的 v0.6.1 与 v0.3.0–v0.3.2 两行 | 无（纯文档）。本仓发布页此前 v0.4.0–v0.6.1 四版是空的，已按各 tag 注解补发 |
| **v0.6.1** | 修 archcheck 的静默假红（import 规则不认 `negative_follow` 豁免）；补 `CHANGELOG.md` 与发版纪律 | 若你的 policy 在 import 规则上写过 `negative_follow`：**此前它一条都没生效**，现在生效了，构建可能从红变绿；若写过 `forbid_call_regex` / `allow_paths`：那两字段一直被 YAML 静默丢弃，自校验现在会点名 |
| **v0.6.0** | `netguard` 的放行面改为具名类型（`Allow`），校验层与拨号层共用同一个类型；新增接线件 `NewClient` / `NewTransport`；依赖地板抬到上游最新一轮（7 个模块）| `netguard.DialOptions.AllowPrivate` / `AllowLoopback` 两字段合并为 `Allow`（**宿主若已接线，改字段名**）。其余为新增 API，旧签名保留但已废弃。**地板抬高对下游是隐式 breaking**：`go mod tidy` 不跑会报 `updates to go.mod needed`，且报错不提真正原因——迁移说明见 [CHANGELOG.md](CHANGELOG.md) |
| **v0.5.0** | 上游把判定环原语 `invoke` 从 `oagentkit/toolgate` 迁到 `oagentkit/guardx`（框架无关的原语不该被 eino 重仓的地板拖住），本仓跟随；依赖地板抬到上游最新一轮 | import 路径换成 `github.com/oagentkit/guardx/invoke`；require 升到 `toolgate v0.12.0` + `guardx v0.8.0`。**编译期报错**（类型身份变了），不是静默行为变化 |
| **v0.3.0 – v0.3.2** | 依赖地板连续抬到上游当时最新（oagentkit 八模块 + oekit 四模块）| **`go mod tidy`**——地板抬高对下游是隐式 breaking：不改代码就编不过，而报错只说 `updates to go.mod needed`，不提真正原因。逐版抬了哪些地板见 [CHANGELOG.md](CHANGELOG.md) 与对应 tag 的 release 说明 |
| v0.4.0 | 门禁执行体从 `package main` 下沉为可导入包 `archcheck/` | 无（新增包，不改既有 API）|

受影响的本仓 API（签名里带 `invoke` 类型）：

```go
guardset.Guard          // Check func(ctx, c invoke.Call) invoke.Verdict
guardset.Set.Evaluate   // (ctx, c invoke.Call) Outcome
guardset.Stage          // invoke.Stage
tooldisclose.Stage      // invoke.Stage
ptc.Options.Stages      // []invoke.Stage
```

> 依赖面变轻了：判定环从 eino 重仓挪到了零依赖底座。下游若自己 import 过
> `toolgate/invoke`，那条依赖曾把 eino 拖进下游的模块图，现在没有了。

## 宿主接入

```bash
go get github.com/yi-nology/agent-harness@latest
```

```go
require github.com/yi-nology/agent-harness v0.6.4   // 零 replace，直接版本化引入
```

> 升级前用 `git ls-remote --tags origin` 核候选版本真的存在——
> `go list -m -versions` 报的是 proxy 的缓存目录，不是远端现实。
> 当前已发布 tag 以远端为准（写稿时为 v0.6.4）。
>
> ⚠️ **require 里写的是「最低版本」，不是「锁定版本」**：下游 `go mod tidy`
> 只保证不低于这个版本，不会自动升到最新版。要用新版本必须显式
> `go get ...@<新版本>`——这也是为什么这里写当前版本号而不是 `v0.0.0`。

```go
import "github.com/yi-nology/agent-harness/dispatch"

// 派发守卫：宿主提供拓扑，harness 提供机制
type myEdgeSource struct{ reg *myRegistry }

func (s myEdgeSource) Slugs() []string                   { return s.reg.Slugs() }
func (s myEdgeSource) DispatchAllow(slug string) []string { return s.reg.Allow(slug) }

g := dispatch.NewGuard(myEdgeSource{reg: reg}, maxDepth) // 矩阵含环会在构造期 panic
if err := g.Assert(caller, callee, depth); err != nil { /* 结构化 *dispatch.DenyError */ }
```

> ⚠️ 上面只是九组能力之一的片段。**完整接入路线图见 [docs/USAGE.md](docs/USAGE.md)**：
> 契约面（`contract` + `statemachine`）、工具判定环（`tooldisclose` + `guardset` +
> `toolfail`）、报告协议（`protocol`）、续跑契约（`checkpoint` + `commitgate`）、
> 上下文三件套（`budget` + `spill` + `offload`）、实时事件、流式与退出、
> 安全面（`netguard` + `pii`）、程序化编排（`ptc`）、门面三件。

宿主侧的安全不变量（如「全拓扑只有一条边能到达执行器」）的**断言测试应留在宿主仓**——
那是产品级承诺，不是内核机制属性。`dispatch/guard_test.go` 用合成拓扑测机制，
可复用于任何宿主。

## 演进纪律

- 新增能力前先问：**这是机制还是资产？** 资产进宿主/领域包，机制才进本仓。
- 新依赖方向先改 policy 再写代码；例外带注释写明拆除条件。
- **改了包或门禁，同步本 README 的包表**：新增/删除包、改外部依赖、改生产行数跨过 25% 容差——三者任一发生就要改表，`make docsync` 会拦。
- 内核包规模远小于宿主，`max_file_lines: 400` 直接阻断（宿主仓是报告口径）。
  2026-10-18 起 `thresholds.enforce` 由 false 切 true：此前 3 处超长文件
  （`protocol/report_types.go` 660、`protocol/report_test.go` 782、
  `cmd/archcheck/main.go` 490）已拆分归零，2026-10-19 起执行体整体下沉为 `archcheck` 包，切换条件才成立。**拆分只为满足
  机械阈值，单文件读不动时该模块应重新分组，而不是继续往上加。**
- 拆自宿主仓的代码历史见 [`docs/SPLIT_PLAN.md`](docs/SPLIT_PLAN.md)——那是 2026-10-17
  写于 10 个包时的**分层依据快照**，其中「L2/L3 待拆」一节描述的是当时的待办，
  **不是当前包清单**（当前清单见本文「包清单」表）。
