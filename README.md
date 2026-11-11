# goagent-kernel · 多智能体编排通用内核

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
> - **[文档站](https://yi-nology.github.io/goagent-kernel/)** = 介绍 / 设计初衷 / 怎么用 / 能力 / 参考（VuePress，源码在 `site/`）

> **全部文档一览**（`docs/` 下标着**历史快照**的那些记录当时的判断与踩坑，
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
> | [docs/HARNESS-BENCHMARK-20261021.md](docs/HARNESS-BENCHMARK-20261021.md) | 对标 Codex / DSH / Octop / Coze 五家：借鉴了什么、什么归宿主、什么本轮没做 | 活文档 |
> | [docs/GAP-ANALYSIS-20261021.md](docs/GAP-ANALYSIS-20261021.md) | 与 Codex / DSH / Octop / Coze 的**逐项实测对齐**：差距在哪个维度、哪些该补、哪些不该补 | 活文档 |
> | [docs/GAP-ANALYSIS-20261106.md](docs/GAP-ANALYSIS-20261106.md) | 同一对标的**刷新**（2026-11-06 重拉五家）：Coze 已停更、DSH 219→400 叶子包、缺口从「可撤销性」移到「判定面的宽度」| 活文档 |
> | [docs/GAP-ANALYSIS-20261107.md](docs/GAP-ANALYSIS-20261107.md) | 第三次刷新（2026-11-07 重拉五家）：**参照系这轮没长出新机制面**；唯一开着的 P0 是出口强制件的**两端都空**（无代理 + 无 `Enforcer` 实现）| 活文档 |
> | [docs/CROSS-ENV-VERIFY-20261021.md](docs/CROSS-ENV-VERIFY-20261021.md) | 跨环境验证：平台相关的机制怎么在「CI 绿但其实跳过」的情况下真验（实测环境矩阵 + 交叉编译流程）| 活文档 |
> | [docs/CHECKPOINT.md](docs/CHECKPOINT.md) | 续跑契约的设计推导、三条不变量与跨进程闸 | 活文档 |
> | [docs/ENGINEERING-HISTORY.md](docs/ENGINEERING-HISTORY.md) | 「工程底座」表**九轮校准**的过程与账：为什么它漂过、后来怎么接进门禁 | **历史快照**（从 README 下沉，2026-11-11）|
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
| `protocol` | 2341 | oekit/pkg、jsonschema | Report 契约 + 五级容错解析 + 白名单校验 + Markdown 渲染 |
| `dispatch` | 36 | oagentkit/agent | allow 矩阵物化 + 深度上限 + 自派发拒绝 + 环检测（拓扑经 `EdgeSource` 由宿主提供）|
| `eventbus` | 377 | oagentkit/runtime | 进程内有损广播（缓冲 chan）；持久化在发布点由宿主负责 |
| `policy` | 89 | cloudwego/eino、oagentkit/guardx、oagentkit/toolgate | 操作审计门：阈值只降不升、解析失败即拒。**两个接入口**：`WithAuditGate`（包单个工具）与 `Stage`（接成**判定环的一环**——工具调用、程序内调用、子进程侧调用全走同一条 invoke 链，审批规则**只装配一次**；门面原先缺后者，宿主想接审批进判定链只能 import 上游 toolgate，「调用面逐字兼容」的承诺就断在那里）|
| `spill` | 263 | oekit/pkg | 输出预算溢出：提示契约 / 取回 / 按龄清理 |
| `pii` | 712 | oekit/pkg | 个人信息价值面脱敏（手机号 / 身份证四重校验）；运行时策略（开关/面/强度）整份单指针原子交换 + 变更订阅 `Subscribe`，代次从此不只可查、还可响应 |
| `netguard` | 1212 | oagentkit/guardx、oekit/pkg | 出站 URL 守卫（SSRF）：名字层（解析前按名拒）+ 解析后逐 IP 过 egress + 重定向逐跳重验。扩展名单**整份原子交换 + 代次可观测**（两次独立取会读到撕裂策略）。代次**只有一个真相**（策略指针内的 gen，全局值是它的投影；交换走 CAS 循环，故并发下代次唯一、单调、不回卷）。**`DialFunc` 把同一套判定落到建连那一刻**（名字层 + egress 预检 + 建连后核对真实 RemoteAddr）——闭合「校验 → 拨号」的 rebinding 窗口；判据仍是 egress，**不引入第二套 IP 策略**。放行面**具名且两层共用同一类型**（`Allow`，零值即严）；`NewClient`/`NewTransport` 是接线件——宿主一行把守卫挂上，且 client 可注入（测试连本机假服务器时**只在测试里**放宽回环）**非纯 stdlib**；DNS 解析与拨号间的 rebinding 窗口见包头「已知局限」与 dial.go；与上游 `oekit/pkg/urlguard` 的分工（IP 策略单源 egress 不换、域名后缀层本包为超集）同样见包头|
| `statemachine` | 79 | 无 | 环节状态机：条件迁移的纯函数件 |
| `contract` | 124 | 无 | 编排运行契约：编排引擎 ↔ 宿主状态的**唯一边界**（纯接口面，零实现）|
| `checkpoint` | 882 | oekit/pkg | 编排续跑契约：进度快照存取边界 + **「续跑而非重放」**的恢复判定（空洞不采信 / 终态不自动重开 / 序号单调闸）+ **跨进程 CAS 存取契约**（`StoreCAS.SaveIfNewer`）与提交序列固化（`Committer`：抢序号→落库→**仅失败时**回滚，宿主无法漏掉）+ **依赖声明**（`StepDef.DependsOn` / `Isolated`）：并行 fan-out 的合法乱序完成按各自前驱采信，不再被「连续前缀」整批作废；零值 = 旧口径逐字不变|
| `commitgate` | 328 | oekit/pkg | 环节终态独占提交闸：同一 (run, 环节) 的终态只允许被提交一次（`statemachine` 是纯谓词，`SeqGate` 只排快照序号，两者都排不出「两条分支都赢了」）。终态词表由宿主注入。**放闸靠凭证**：`Acquire` 返回唯一令牌、`ReleaseLease` 凭令牌精确释放——靠 (状态,分支,序号) 认人的旧 `Release` 分辨不出两次同名同序的占用，迟到的释放会放掉别人的闸 |
| `budget` | 750 | oekit/pkg | 上下文预算：分段计量 + 窗口钳位 + 压实计划骨架（三重空转守卫；摘要生成与原文落盘各留宿主接口）|
| `offload` | 587 | oekit/pkg | 内联大对象外置：sha 去重 + **两阶段**（首次标记、后续轮才替换占位）+ fail-soft（与 `spill` 是姊妹机制）|
| `tooldisclose` | 959 | oagentkit/guardx、oekit/pkg | 渐进式工具披露（确定性打分检索换完整 schema）+ 执行门禁（**未加载可重试 / 不在目录禁止绕过** 两类严格二分，**拒绝即激活**，重试不绕路）+ 接成 `invoke` 链的一环（`Stage`，链上 Reason 直接回面向模型的指引）|
| `toolfail` | 89 | oagentkit/guardx | 工具失败的错误分类契约（**门面**，核心在 `oagentkit/guardx/toolfail`）：类别 → 可重试性/退避/需人工/重发安全/模型指引为**数据表**而非散落 if；不可归类一律不可重试。**重发安全与重试价值是两个正交维度**——超时值得重试但服务端可能已执行，原样重发即重复施加副作用；`Guidance()` 是模型侧的唯一消费方。判定口径必须跨宿主一致，故核心归上游安全层，本仓只保调用面稳定 |
| `toolname` | 96 | 无 | 工具名 → 程序内可用的 JS 标识符：**一条单一真相**。`IsJSIdent`（能不能直接用）+ `Normalize`（不能的话用哪个）。生产侧（`tooldisclose` 派生调用形态）与消费侧（`ptc` 挂载绑定并校验形态里的函数名）共用它——两侧必须逐字一致，否则就是「这里派得出、那里认不出」的夹缝。规整**不保证唯一**（`get-logs` 与 `get_logs` 同名），撞不撞由调用方判 |
| `sideeffect` | 540 | oekit/pkg | 副作用「至多一次」的跨进程契约：**幂等键**（同一逻辑动作重试不重复施加）+ **租约**（同一资源独占，带 TTL）+ **栅栏令牌**（过期持有者的迟到写入被下游拒绝）+ **下游拒收契约**（`FencedSink`：比较与写入须同一次原子操作，否则两个持有者会同时读到同一个旧值、双双通过检查，更旧的那次写入反而后落覆盖新的）。上游三个判据共用**一次原子操作**（`Store.Acquire`）——拆成三份存储就会出现「租约过了但幂等没过」的夹缝，允许同一动作被施加两次。执行失败**不记账**（没做成的不能记成做成了），但**释放闸**（否则失败残留的闸会把后续重试全挡成「别人占着」）。**施加前有资格检查**（`ExecuteWith` 两段式：准备 → 检查 → 施加）——闸已易主时**根本不施加**，因为 Complete 被拒只说明没记上账，**副作用早已生效，事后补救不了**。承诺的是**至多一次**而非 exactly-once——后者要求下游也接受幂等键 |
| `sidetest` | 436 | oekit/pkg | sideeffect.Store 的**共享契约测试套件**：宿主写完存储实现后跑一遍，违反哪条契约就红在哪一条——免得「各写一遍必然各判一套，漂移到没人发现为止」。10 条子测试覆盖「只做一次 / 已完成优先于占用 / 未过期时争用 / 过期后接管 / 令牌单调 / 过期持有者不得记账 / 过期持有者不得放闸 / 释放不等于完成 / 续期延长且不换令牌 / 闸已易主后续期被拒」。范式沿用上游 `cachetest` / `singleflighttest`：**硬失败不静默跳过**（显式接入套件＝声明要满足契约，跳过等于把「没测过」伪装成「通过了」）。时钟可注入（`ClockedStore`），否则过期场景恒不触发，套件会变成一组空断言 |
| `cmdpolicy` | 527 | oagentkit/tools、oekit/pkg | 命令级判据：把判定面从「工具调用」**向下延伸到工具内部实际执行的那条命令**。此前边界停在工具名上——一次 `exec` 审批通过后，它内部跑的是 `git status` 还是 `rm -rf /` 没有任何机制面看得见，宿主只能靠正则黑名单兜，而黑名单在「写法与语义不一致」时必然失效。本包用**前缀 token 规则**（先分词再比 token，不随引号/转义/空白漂移）+ **三态裁决**（allow/prompt/forbidden，与 toolgate 词表同义不另立口径）+ **规则自带样例**（每条规则可带 Match/NotMatch，`New` 在**加载期**用真实判定路径跑一遍，不一致即报错——这是「从没见过它触发的规则等于没有规则」的机器化：规则会随命令名漂移，而漂移不会报错，只会静静地不再命中）。冲突口径确定性：前缀更长者胜 → 同长度取更严 → 同档取名字字典序最小。`SplitCommands` 单独存在是因为前缀判据的典型绕过：`git status; rm -rf /` 的前缀是 `git`，对整串只判一次就会让一条 git 的 allow 规则放行后半段。**分词面已收编上游 `oekit/pkg/shellx`**（2026-11-11）：口径归上游一处，本仓不再维护第二份分词器；顺带修掉手写版三处 POSIX 错误（双引号内反斜杠被丢弃、分隔符按 token 相邻误合并、CR 未归空白），且 `SplitCommands` 的段文本改用上游 `QuoteArgv` 拼装，**段可往返**（段再分词得回原 token——否则含空格的备选前缀永不命中，是安全方向的漏判）。**事实面已收编上游 `oagentkit/tools/bashguard`**（真 shell AST）：前缀判据的三类典型绕过（多段 `git status; rm -rf /`、命令替换 `echo $(rm -rf /)`、写重定向 `git status > /etc/passwd`）由「全调用面取最严 + 事实面下限（写重定向/动态展开 ⇒ 不低于 prompt）」闭合，本仓不手写 shell 解析器 |
| `egress` | 250 | oekit/pkg | 出口策略：**三档**（`ModeUnrestricted` < `ModeProxy` < `ModeDenied`，单调可比、零值即最松）+ 声明自检（`Validate`）+ 兑现判据（`Env` / `Verify`）+ 后端能力问询（`SatisfiedBy`）。本包**不含代理本体**（那是上游 `guardx` 与宿主的活），只回答此前没人回答的三件事：这次执行要求哪一档出口 / 宿主给的环境是否**真的**兑现了那一档 / 某个沙箱后端能不能兑现它。两条底线都是「沉默与伪造都落在安全的那一侧」：**后端不声明就按最弱处理**（不是「没说不支持」＝满足，否则从没实现过出口控制的后端会被选中，声明与实际之间的落差没有信号）；**禁网档拒绝伪造**（`ModeDenied` 的 `Env`/`Verify` 直接报错，不做「尽力而为」——假装禁网比明确拒绝更危险，报告会显示「已兑现」而网络其实通着） |
| `gatecheck` | 504 | oagentkit/guardx、oagentkit/toolgate、oekit/pkg | 审批层与沙箱层的**联合判据**：Codex 的两层安全模型是「沙箱强制 + 审批策略，**两层都同意才放行**」。上游 `guardx/sandbox`（档位×预算）与 `toolgate/policy`（审批裁决）各自完整但**互不知情**——「两层是否都同意」这个判定没有单点，落在各宿主手里必然漂移。本包只做**合成**（不重新实现任何一层，那会造成与上游并存的第二份真相）。判据顺序有意为之：**审批已否 ⇒ 直接否**（档位再高也不该执行）；**需人审 ⇒ 优先于档位不足**（人该在场，而不是让宿主去找后端——否则这次调用既没执行也没人管，**静默丢失**）；**计划模式单列**（只审不执行 ≠ 拒绝）。`Managed` 是**受管后端**（把真后端包起来）：判据不通过就**不委派**（副作用不会发生）——判据与执行**不可分**，否则「判过的后端 ≠ 实际跑的后端」又有了信号落差 |
| `preset` | 230 | oekit/pkg | 命名权限预设：把「隔离档位 × 审批模式 × 出口策略」三件散参数收成一份**具名、只读、加载期自检**的组合（`Preset{Name,Level,Mode,Egress,Unsafe,Justification}`）。散参数的病不是难写，是**改一处漏两处**——档位调到 OS 级而出口忘开代理、审批放到 auto 而沙箱仍是进程内，组合出来的权限谁都没声明过。自检两条最关键：**三面全松必须显式 `Unsafe` 且写理由**（「全都放开」最容易在调试时临时出现、然后留下来）；**语义相同的两个名字即报错**——两个名字一份组合在配置里看起来是「两个选择」，真正的伤害在留痕：报告里出现两个名字，读者会以为执行过两种权限。`Resolve` 遇未登记名字**报错而不降级**（降级方向不可预测，可能更松，而「打错一个字就用上另一档权限」没有任何信号）。方向是 preset → gatecheck（高层组装件认识低层判据），gatecheck 不认识 preset |
| `retryplan` | 192 | oagentkit/guardx | 重试处置决策：把「该不该重试」（`toolfail` 判）与「重试会不会重复施加」（`sideeffect` 判）合成**一条可执行结论**——三档路径（不重试 / 原样重发 / 走幂等键重放）。与 `guardset` 同族：**组合逻辑归内核，判定归各自机制**（各宿主各写一遍组合逻辑，必然各判一套）。关键一档是「可重试但原样重发不安全」——`toolfail` 只说「先确认上一次是否已生效」，**没说什么怎么做到**，而这正是幂等键的活；故该档判为 `PathReplayed`（走幂等键）。关键取舍：**没有幂等键就不许「猜着重试」**（降级为不重试，而非原样重发赌一次） |
| `delta` | 253 | oagentkit/runtime | 增量节流：分片合并 + 权威整段优先 + 确定性收口扫序 |
| `pending` | 73 | 无 | 多轮挂起态载体：单一编码入口 + 宽容解码（非法值降级为无挂起）|
| `reminder` | 30 | oagentkit/guardx | 系统注入提示的防伪包裹（门面）：核心已下沉 `guardx/injectband`，本包只保调用路径稳定，宿主调用点一行不动 |
| `sessionref` | 212 | oekit/pkg | 跨会话引用快照：把**另一个会话的结论**以有界、带来源、带失效点的片段注入当前上下文。`budget`/`spill`/`offload` 三者回答的都是「**同一份上下文**里内容放不下怎么办」，而「引用别的会话的结论」是另一件事——那份内容不在当前上下文里，它的边界与保鲜度没有任何机制面管。不管的后果不是「引用得不好」，是把**当时的**结论当成**现在的**结论用，而这类错误在报告里读起来完全正常（那段话当时确实是真的）。三条判据都走结构：① 裁剪上限采集时给定且裁剪必须显式（`Truncated` + `OriginalBytes` + 注入面的 `truncated=true`，静默截断会让模型以为看到全文）；② `Source`/`SourceVersion`/`CapturedAt`/`ExpiresAt` 随快照走；③ 渲染写 `state=fresh\|expired\|stale`。**版本这一维不可省**：TTL 是采集者猜的，源是否前进是事实（长 TTL + 高频源更新是最常见的失效形态，只靠时间会一路判有效）；版本**回退**同样判 stale（同一个 id 已不是同一段历史）；两者同时成立报 expired。渲染**不拒绝、只标注**——拒不渲染会让「引用过期」变成「引用消失」，宿主无从区分「本来就没引用」与「引用了但过期」。防伪不另写一套，走 `reminder.Wrap` |
| `drain` | 320 | oekit/logx、oekit/pkg | 优雅排水：轮 ctx 取消脱钩 + 等在途收敛 + 预算耗尽强制收刀（经 `Turns` 接口）|
| `ptc` | 2745 | dop251/goja、oagentkit/guardx、oagentkit/runtime、oekit/concurrency、oekit/logx、oekit/pkg | 程序化工具编排：模型写程序 → 沙箱 VM 执行 → 经 `bindings` 调宿主工具；三重预算（墙钟/调用数/字节数）替代 OS 沙盘。**N 轮 ReAct 往返塌缩成 1 轮，原始数据不过模型面**；绑定可带 Signature 让模型按位置传参（可选形参以 `?` 标记、未传则不出现在 JSON 里；形态里的函数名必须是**净化后**的名字，规则见 `toolname`）；**程序内的 host.<tool>() 与普通工具调用走同一套 Stage 判定环**（不做这一步，本包就成了整条链上唯一绕过所有门禁的路径）；绑定名净化后撞名即装配期拒（否则后绑者顶替前绑者，副作用错位无信号）。**新增子进程后端**（`ptc-subprocess`）：把执行搬出宿主进程，用 **cgroup v2 的 `memory.max`** 兑现 `MaxMemory`（限**物理内存**；`RLIMIT_AS` 限的是虚拟地址空间，会把 Go runtime 一起掐死——已实测否掉）——进程内形态兜不住内存（Go 运行时没有「只给一个 VM 限内存」的开关，goja 公开 API 也没有内存限制），故子进程形态**如实声明**自己兑现这一项（`sandbox.Capable`），**建不出即拒绝执行**（fail closed），兜不住的后端在登记簿里不会被选中 |
| `sandbox` | 116 | oagentkit/guardx | 执行沙箱的**档位语义**与**后端协议**（**门面**，核心在 `oagentkit/guardx/sandbox`）：`Level`（进程内解释器 → 内核级 → 硬件虚拟化，单调可比，零值即最弱档——漏声明的后端只落到最弱一档，方向是安全的那一侧）+ `Provider`（后端自报提供哪一档并执行程序）+ `Registry`（登记簿；按要求档位选**最弱**满足者，**无满足者即拒绝、不降级**——降级会让「宿主声明的强度」与「实际执行的强度」之间没有信号，档位机制就成了装饰）。零内部依赖，ptc 反过来依赖它（**沙箱机制不认识任何后端，后端认识沙箱**）。**已知局限**：档位是后端自报的，内核不校验其真实性——宿主不信任某后端，唯一办法是不登记它。**档位声明要签字**：后端的档位登记在 policy 的 `sandbox_providers` 段（人 review 时看得见「它凭什么说自己有这一档」），门禁 `harness-sandbox-provider-declared` 核对登记与代码事实不漂移（走 `go/ast` 按方法指纹认出后端实现）|
| `watchdog` | 127 | 无| 被动配对不变量监视：环节收口仍有未执行声明 / 结果无对应声明；**只告警不裁决**（热路径裁决在守卫侧）|
| `guardset` | 1027 | oagentkit/guardx、oekit/pkg | 守卫装配契约：一组**相互独立**的判定一并求值，多条拒绝合成一条（按 Rank 定序、**不可重试优先**的保守裁决、指引指向真正卡住的那一条）。接成 `invoke` 链上单独一环——链在第一个拒绝处就收口，后面的守卫永远没机会说话，模型一次只听得到一个理由，这正是本包补的那一面；链本身的顺序/留痕/panic 兜底沿用上游，不重复实现。**装配面 `Assembly` 提供「撤销」的机械路径**：撤销 = **换整份**，不是改其中一条——改其中一条会让「当前有哪些守卫」出现两个真相；换份走 **CAS + mutate(旧值)**（并发撤销**不丢更新**，否则「撤销被撤销」）、代次住在被换的那份里（全局代次是投影）、链不用重接、旧份不被修改、在途判定跑在旧份上；「全部撤销」不是降级是失守（换上去的那份永远非空） |
| `archcheck` | 5073 | gopkg.in/yaml.v3 | 架构门禁执行体：`architecture-policy.yaml` 的**可执行面**（规则判定 / 门包判定 / 依赖方向判定 / policy 自校验 / 结构阈值）。**可导入包**——宿主仓复用同一套判据，不必各写一遍（各写一遍必然各判一套，漂移到没人发现为止）。判据与执行体分离：结构性判定一律走 `go/ast`（import 声明有六种以上书写形态，逐行正则必然漏）。依赖方向判三条：越界内部依赖 / 越界跨仓依赖（`ext:` 登记）/ 未登记包目录。**执行沙箱后端的档位登记**（`sandbox_providers` 段 ↔ 代码事实，走 `go/ast` 按方法指纹认出后端实现，核对档位漂移与未登记）。**陈旧包根当天即红**——root 指向空目录即「声明与事实已偏离」，而没有执行体时这句话没人读 |
| `trajectory` | 1043 | 无 | 轨迹投影器：**事件流 → 一轮 turn 的步骤序列**（增量投影：喂新事件继续更新既有行）。行含 kind / 起止时刻 / **实测耗时**（起止相减现算）/ 工具调用**配对状态**（paired / missing_result / duplicate_result / unpaired_result / duplicate_call）/ 摘要。⭐ **结构上不设时长字段**——时长唯一来源是起止相减，**估算值无处安放**（没有实测就是没有实测，不拿 payload 体积之类糊过去）；配对缺口语义分两层：`missing_result`/`unpaired_result` 是**当前状态**（等到了会被清掉），`duplicate_*` 是**发生过**（永久留在清单里）。事件倒挂（结果先于调用）按「就地补起点」处理且此时判为无实测值。与 `watchdog` 分工：watchdog 判**不变量**（只告警），本包判**这一轮的流水**（给宿主渲染）。另有 **OTel 映射契约**（`otelmap.go`）：每个属性写明来源字段 / 取值域（含 Go 类型）/ 基数上界 / 能否进 metric label，`Span.ParentID` 声明父子，**trace 边界定在轮**（`Row.ID` 的唯一性范围恰好就是轮内）；无界看似维度的字段（`session_id`/`agent_slug`）列进禁区清单 |
| `taskboard` | 398 | oekit/concurrency、oekit/pkg | 多 agent 协调面的**存储中立契约 + 一致性判据**：`dispatch` 只回答「能不能派」，派出去之后「谁负责哪个子任务、依赖谁、怎么还回去」此前没有任何机制面——各宿主自己拿一张表兜着，于是「同一子任务被两个 agent 同时认领」在报告里读起来完全正常（两条分支都干完了活，各自都以为自己是负责人）。本包**不给存储实现**（表结构/事务边界/持久化是宿主资产，给一份参考实现宿主就会照着改它，最后两份真相并存——与 `checkpoint.StoreCAS` 同一分工）。三条口径：① 认领必须**原子**由存储保证，本包只判结果（`ConcurrentClaim` 并发跑一遍，成功者必须恰好 1 个）；② 释放/完成**凭令牌不凭内容**（同名 agent 与「失败→释放→重试」都会让内容比对认错人）；③ **failed 不是终态**（一次失败不能把子任务永久钉死，否则补救路径静默失效）。`Check` 判快照不变量：id 唯一、依赖存在且**无环**、认领态与租约必须同时成立、依赖未完成不得认领、终态不带 owner |
| `skilldisclose` | 360 | oekit/pkg | 技能披露：**确定性**挑出该给模型看的那几个技能。与 `tooldisclose` 不是重复——那边的检索键是「工具调用需求」，而技能的触发信号有三种且**强度不同**：点名 > 资源命中 > 描述相关。混成一个分数会让「相关」压过「点名」，而那正是用户已经明确表达过的意图（故点名权重 1000，任何相关度信号之和都够不到）。判据三条：点名压过一切；同分按名字（不依赖目录遍历顺序与 map 顺序）；预算裁剪**显式**（`Truncated`，静默截断会让宿主以为「就这几个相关」）。空结果必须写明原因——「没有检查」与「检查通过」在调用方看起来一样，而前者是「模型不知道有这个能力」 |
| `hookengine` | 296 | oekit/pkg | Hooks 的**判定面**（不是传输面）：Codex 是「外部进程协议 + 规则文件」，DSH 是「动态插件包」，形态**已经分叉**——由内核选边等于把一种传输焊进契约，故只定三件跨形态恒定的事：**匹配**（事件 + 工具前缀）/ **裁决**（allow / deny）/ **顺序与收口**（按名字确定序，第一个拒绝即停）。传输与加载归宿主（`Runner` 是唯一接缝）。两条 fail-closed：阻塞 hook **执行失败 ⇒ deny**（跑不起来不等于没问题，否则一个写错的检查会静默变成空检查）；**超时裁决必须显式声明**（`OnTimeout`，缺省 deny）——没有缺省值时超时会被 ctx 取消掩盖成「这次没拒绝」。非阻塞 hook 不参与裁决但失败必须留痕：它一旦能影响裁决就只是「慢的阻塞 hook」，而宿主会以为它是旁路的 |
| `memorypipe` | 219 | oekit/pkg | 记忆注入的三段：**注入 / 引用 / 打标**（读写时机与摘要生成归宿主，与 `budget` 留 `Summarizer` 是同一条边界——内核不替宿主决定「这段对话值不值得记住」）。注入侧三件与策略无关、各宿主各写一遍又必然各判一套的事：① **来源是前提**——没有来源的记忆与模型的猜测在文本上无法分辨，故不注入、显式丢弃并给出原因；② **逐条打标**（`id` / `source` / `at` 三样缺一就少一条追溯路径：没 id 无法引用，没 source 无法判可信度，没 at 无法判新鲜度）；③ **裁剪显式**。`scope` **精确匹配、不做父级兜底**：按 `project` 查却回退到 `global` 看起来友好，代价是作用域失去意义——只该在某个项目里生效的记忆会在任何会话里生效，而报告完全正常。同 ID 只留最新（两条正文不同的同 ID 记忆让读者只能猜） |
| `delegate` | 176 | oekit/pkg | 异构委托的**契约面**：ACP 一类东西同时是协议 + 传输，线格式归上游；本包只定跨传输恒定的三条。① 能力必须**子集**（做不到就拒，不许「尽力而为」——尽力而为的结果无法判定「没做到」与「本来就没要求」）；② **最小能力原则**选执行方：多个都能接时取能力**最少**者，同数量按名字——按顺序取第一个是错的，顺序通常来自配置文件书写次序，一次无关重排就会换掉执行方；③ 回执状态**三值**（completed / failed / refused），且 completed 不得带 Failure、failed 与 refused 必须带原因——把「被拒绝执行」混进「执行失败」会让委托方去修一个不存在的 bug |
| `worktree` | 232 | oekit/pkg | 工作区隔离的**声明面与冲突判据**（**不碰真实文件系统**——建目录/挂载/chroot 是宿主与执行后端的活）。并行工作区会不会互相踩此前没人判：两个 agent 各自「在自己的目录里工作」，可写区完全重叠时任何一边的写入都会让另一边的结论失效，而报告里只有两份「各自成功」。判三件：声明自检（同一工作区不得既只读又可写）、**可写区重叠即冲突**（只读重叠不算——多个人读同一份文件没有危害，**写**没有第二份真相）、无命中即不可读写（fail-closed：默认允许会让漏写的一行声明变成全域可写）。**不支持通配**（写明的局限）：`*` 与 `**` 的语义各实现不同，进契约就会出现「按 A 的理解写、按 B 的理解判」，需要更强表达力时宿主自己展开成多条精确声明 |
| `ticket` | 203 | 无 | 一次性短时授权票据的机制面：签发 / **单次消费**（用过即焚）/ TTL / **用途绑定**（载荷原样携带，语义归宿主）。⭐ 过期判定与消费判定**同一处**（两处各判一次必有一处漂）；时钟**可注入**——过期语义要能被确定性测，不靠 sleep（靠 sleep 的话「等到了」会被当成「判对了」，判定写反了测试照样绿）；票据空间**有界且满了显式拒发**（默默淘汰会让宿主以为票据还在、实际已失效 ⇒ 表现为「偶发地连不上」，极难查）；随机源失败**显式报错**（不降级成可预测 id——id 一旦可预测，单次消费就没意义了）。进程内存储（重启即失效）对「只活几十秒的票据」是**正确默认**：落盘即多一份要擦的痕迹 |

> 「生产行数」口径：`wc -l` 统计 `*.go` 且**不含** `_test.go`——包体量看的是要交付的实现，
> 不是它的测试。依赖列只列**仓外** import；「无」表示该包生产代码只用 stdlib。
>
> 本表由 `make check` 末步的 `docsync` 机器校验（`cmd/docsync`），判定分两类：
> **依赖列逐项相等**——加依赖是显式动作，必须同步；**行数按 25% 相对容差判定**
> ——容差内的逐行涨落不拦，改一行代码不必顺手改文档，而跨量级的误述
> （某包声称 ~86 行、实际 412 行那种）仍然拦下。行数取较大者为分母，
> 「少写」与「夸大」同等对待。职责列与依赖列非空也在判定内。
>
> **`internal/` 下的包刻意不在本表**：它们是内部件，不是对外承诺（`docsync` 的
> 反向收录判定只看一级目录，故 `internal/subhub` 不会要求一行）。但它们**不受
> 「未登记包」豁免**——`architecture-policy.yaml` 的 `modules` 段照样要给
> `{id, roots, requires, owner}`，否则依赖方向门禁判它「对本门禁隐形」。

## 特性

### 边界不是文档，是门禁

「内核不许长出领域知识」这句话写在散文里就会烂掉。本仓把它做成了 8 条正则规则
+ 5 条 `go/ast` 结构性规则，全部挂在 `make check` 上，CI 只执行这一条。
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
那个（要么盲目全量重跑、要么盲目跳过）。三条硬规则：**可信边界按依赖判定**
（默认只信连续完成的前缀，失序时宁可多跑几步；并行组显式声明依赖后按各自前驱采信，
合法的乱序完成不再整批作废）、**终态不自动重开**（拒绝替宿主决定）、**序号单调闸**。

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
| 生产代码 | 26,281 行 / 143 文件（不含 `_test.go`）|
| 测试 | 183 个测试文件 |
| 覆盖率 | 门槛：全局 60% + **逐包下限 70%**（两条判据判两件事，见下）。**实测值不抄在这里**，以 `make cover-gate` 的输出为准 |
| 结构阈值 | `max_file_lines: 400`，`max_methods_per_type: 25`（按**实质方法数**判，见下），`enforce: true` |
| 门禁 | 8 条规则（`rules` 段）+ 5 条 `go/ast` 结构规则（门包不变 / 依赖方向 / 沙箱档位登记 / 执行路径 / **子进程出口**）+ **5 个门包**（`dispatch`/`policy`/`reminder`/`toolfail`/`sandbox`——核心归上游、只保调用面稳定）+ 43 包依赖方向 + README 包表一致性 + **竞态（`-race`）+ 漏洞扫描（`govulncheck`）+ 交叉构建矩阵** |
| 例外登记 | **0** |

> **上表分两类：前两行有机器判定，其余仍是散文**（issue #31）。
>
> ✅ **已判**：生产代码行数 / 文件数 / 测试文件数——三项由 `docsync` **判相等，不留容差**。
> 包表那 25% 容差是给「逐行涨落」留的噪声滤波，而这张表的历史漂移量恰好落在容差里
> （895 / 578 / 573 行相对 1.9 万行都在 3% 上下），给它容差等于让静默过期继续成立。
> 口径就是本段末尾那三条命令，门禁报错时也会原样给出来。
>
> ⚠️ **未判**：覆盖率与其余四行。
>
>   - **覆盖率不判是刻意的**：它随「跑的是哪个用例集合」变——2026-11-06 同一批用例、
>     生产代码一行未改，量出 85.5 / 85.0 / 87.0 三个值。让散文去复述一个由门禁**实时**
>     给出的数字，只会制造两处不一致；故这一格只写门槛（门槛由 Makefile 判，不会漂），
>     实测值看 `make cover-gate` 的输出。
>   - 结构阈值 / 门禁条数 / 例外登记数是**描述**，各自有执行体（policy + 各条门禁），
>     写错不会让门禁失效、只是描述不准——判定成本高于收益，故不判。
>
> 于是「改这张表」的规矩变成两半：**前两行照门禁的报错改**（它会告诉你实测值），
> **其余行改前请手算**，别信上一版。
> v0.6.4 那次修「工程底座」三处漂移（生产代码 11,491 行/66 文件 → 13,336 行/77 文件、
> 测试 84 → 99 个文件、覆盖率 80.4% → 86.6%）时，这三处已经过期了**三个月**——
> 它们是 `archcheck` 从 `cmd/` 下沉成独立包（2026-10-19）之前的旧值，此后没人更新。
> 这正是本仓「散文不是门禁」那条纪律的反面案例：**机器只管得着被写成规则的那部分**。
> 改这张表时请手动重算，**别信上一版**。
>
> **重算口径（照抄这三条，别手算）**——「生产代码」= 全仓非 `_test.go` 的 `.go`，
> 排除隐藏目录与 `vendor`：
>
> ```bash
> find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*' | wc -l
> find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*' -print0 | xargs -0 cat | wc -l
> find . -name '*_test.go' -not -path './.*' -not -path './vendor/*' | wc -l
> ```
>
> 覆盖率取 `make cover` 的最后一行（`go tool cover -func=coverage.out | tail -1`）。
>
> 2026-11-06 第八轮校准（**生产代码一行没动**）：19,624 行 / 113 文件未变，测试文件
> 139 → **141**（`sandbox/forwarders_test.go`、`cmd/docsync/run_test.go`），覆盖率
> 85.5% → **87.0%**。这 +1.5pp 全部来自补测，不是新写的生产代码：`sandbox` 60.0% →
> **100%**、`cmd/docsync` 23.6% → **88.8%**（`run()` 端到端，用 `t.Chdir` 造夹具）。
>
> 中间有一次「只补了 `sandbox`」的测量是 **85.0%**——比上一版**低** 0.5pp，而生产代码
> 没变、只多了一个测试文件。这件事本身值得记下来：**这张表的数字会随测量口径与批次
> 漂**（同一批用例在不同轮次里给出 85.5 / 85.0 / 87.0 三个值）。
>
> 2026-11-06 第九轮（**这一轮把表接进了门禁**，issue #31）：19,624 → **20,397** 行 /
> 113 → **118** 文件 / 141 → **146** 个测试文件（+773 行）。前两行从此由 `docsync`
> **判相等**，报错时会把实测值与三条重算命令一并给出，不需要再靠「记得回来手算」；
> 覆盖率那一格**删掉了手抄的百分数**，只留门槛（门槛由 Makefile 判）。
> 这一轮记下的教训是：**「会漂的数」与「不会漂的数」要分开对待**——覆盖率属于前者，
> 让它出现在散文里，就等于承认「这里迟早会说谎」；行数/文件数属于后者，那就判相等，
> 连容差都不该给。
>
> 同日补的第四条门禁口径：`cover-gate` 从「只判全局」变成「全局 + 逐包下限 70%」。
> 起因是全局 85.5% 同时掩盖着 `cmd/docsync` 23.6% 与 `sandbox` 60.0%——单个包的
> 塌方在大盘里完全看不见。`sandbox` 的 60% 已补到 **100%**（门面包的每条语句就是
> 一句转发，转发目标或参数顺序写错编译得过、语义却错，故对门面包而言语句覆盖率
> 恰好是恰当的度量）；`cmd/docsync` 当时先登记豁免（判定逻辑仍在 `package main`、
> 没有可注入的接缝），**同日即拆除**——改用 `t.Chdir` 造夹具后 `run()` 端到端可测，
> 23.6% → **88.8%**，`PKG_COV_EXEMPT` 现已为空。
>
> 2026-11-06 第九轮校准（差距台账落地轮）：上一版记的 19,624 行/113 文件、141 个测试
> 文件、87.0%，这一轮按 `docs/GAP-ANALYSIS-20261106.md` 落了 P0-1/P0-2、P1-1..P1-5 与
> 五项 P2（`cmdpolicy` / `egress` / `preset` / `sessionref` / `taskboard` /
> `skilldisclose` / `hookengine` / `memorypipe` / `delegate` / `worktree`，外加
> `archcheck` 的样例自检与子进程出口判据），实测 **23,281 行 / 130 文件、156 个测试
> 文件、87.6%**；门禁表里「依赖方向」那一格也从 34 包改为 **42 包**（`archcheck` 实测）。
> 这一轮值得记一笔的**不是**行数涨了 3,657——而是十条门禁里有两条是**这一轮才有的
> 判定面**（规则样例自检、子进程出口），它们此前都是「有声明、没人判」。


### 平台支持矩阵（2026-11-06 补齐）

此前本仓**从未声明支持哪些平台**，也没有任何东西验过——本机 darwin 全绿，而本仓是
「被宿主编进二进制」的库，真正的交付面是 linux。2026-11-06 实测才发现 Windows
根本编不过，而这个事实在仓里**一个字都没有**。

| 平台 | `go build ./...` | 依据 |
|---|---|---|
| linux/amd64 | ✅ 支持 | `make crossbuild` 每次 `make check` 都验 |
| linux/arm64 | ✅ 支持 | 同上 |
| darwin/arm64 | ✅ 支持 | 同上（本机开发面） |
| **windows/amd64** | ✅ **支持（语义降级，见下）** | 2026-11-07 回归：上游 `oagentkit/runtime` procx 落地 Windows 进程树路径（Job Object 建组 + TerminateJobObject 整树硬杀，runtime v0.11.0 起，oagentkit/runtime#24）——`make crossbuild` 恢复 windows/amd64，CI 增加 windows job 真跑测试。**语义降级**：Windows 无进程组信号语义，TERM 优雅退出降级为整树硬终止；这是接口层写明的差异，不是对等实现 |

处置方式：**显式声明不支持**，而不是留着「看起来支持、其实编不过」。`make crossbuild`
只判 `CROSS_TARGETS` 里列出的平台——为一条永远编不过的目标留门禁不是严谨，是把红线
泡软：它会常年红，然后被人忽略，连带把别的红一起忽略掉。

要在 Windows 上使用，前提是上游 `procx` 先给出可移植的进程组实现；在那之前本仓
`ptc` 的子进程后端不适用于 Windows。这条限制**不在本仓能力范围内**，故不做兼容层。

### 三条「目标存在但没人跑」的门禁（2026-11-06 补齐）

2026-11-06 的仓库审计里有三条门禁的共同形态：**目标/文档里有，但没有任何东西执行它**。
这和 `lint` 当年一模一样（见 Makefile 的 lint 注释），处置方式也一样——接进
`make check`，不挂到 `ci.yml` 上变回「推送后门禁」。

| 门禁 | 此前状态 | 它漏掉的东西（实测） |
|---|---|---|
| `race` | `race` 目标存在，但 `check` 依赖链与 `ci.yml` **都没有它** | 本仓的并发面（策略代次、提交闸、共享 map）从没被竞态检测过。补上时踩到一个硬事实：linux 上 `-race requires cgo`，而 `GOENV := CGO_ENABLED=0` ⇒ race 目标**不能**跟着 GOENV，否则在 CI 上必红 |
| `vuln` | Makefile 里没有这个目标，`govulncheck` **从未跑过** | 首次跑出 **6 条漏洞，全部在标准库**（`crypto/x509` / `net/http` 系列 / `crypto/tls`），修法是抬 `go` 指令到 1.27.3。载体正是「下游用本仓编出来的那个二进制」 |
| `crossbuild` | 没有任何交叉构建门禁 | 见上一节：Windows 编不过这件事从来没被记录过 |

顺带补上的第四条：**覆盖率此前只判全局**。全局 85.5% 同时掩盖着 `cmd/docsync` 23.6%
与 `sandbox` 60.0%——大盘涨 1% 就能抵消一个包从 70 掉到 20 的塌方，而报告里只有一行
「85.5%，绿」。故 `cover-gate` 现在跑**两条**判据（逐包判据的执行体是 `cmd/pkgcov`——原先是
`scripts/pkgcov.awk`，issue #36 收编为 Go 并带上自己的用例），
且逐包下限的豁免**失效即红**（豁免包一旦自己达标，门禁会要求当天删掉那条豁免）——
本轮就是这样把它拆掉的：`cmd/docsync` 补测到 88.8% 后第一次跑门禁，报的正是
「豁免失效，请从 `PKG_COV_EXEMPT` 移除」，而不是默默放行。

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
| `harness-no-bare-goroutine` | **error** | 生产代码裸 `go func`（goroutine 内 panic 无法被外层 recover，直接杀进程）|
| `harness-no-printf-log-callback` | **error** | 公开 API 里的 `logf func(string, ...any)`（printf 回调一旦焊进签名就是**不可逆 API 债**）|
| `harness-single-errors-package` | **error** | stdlib `"errors"` 与 `github.com/pkg/errors`（全仓只允许 `oekit/pkg/errors` 一套，混用会打断 `As`/`Is` 判定链）|
| `harness-sandbox-execution-single-entry` | **error** | 执行沙箱的后端不得多入口（执行路径与声明路径必须一致）|

另有三条不走正则表、由 `go/ast` 判定的结构性规则（判据是语法结构，逐行看文本判不出）：

| 规则 | 级别 | 拦什么 | 落点 |
|---|---|---|---|
| `harness-facade-stays-facade` | **error** | `dispatch`/`policy`/`reminder`/`toolfail`/`sandbox` 五个门包长出实现（声明新类型、给类型挂方法、函数体超过一条转发语句、调用非上游包、import 非允许依赖）| `policy.yaml` 的 `facades` 段 + `archcheck/facade.go` |
| `harness-module-dependency-direction` | **error** | 包间依赖超出 `modules[].requires` 声明的方向；越界的**跨仓**依赖（`ext:` 面已登记却没在 requires 声明）；以及**未登记的包目录**（它对本门禁隐形，任何模块都能 import 而不判违规）| `policy.yaml` 的 `modules` / `dependency_direction` 段 + `archcheck/deps.go` |
| 沙箱档位登记 | **error** | 执行沙箱后端未在 `sandbox_providers` 段登记，或登记的档位与代码事实漂移（**档位声明要有人签字**）| `policy.yaml` 的 `sandbox_providers` 段 + `archcheck/sandbox.go` |
| 执行路径 | **error** | 执行**绕过 `sandbox` 登记簿**（直接调裸执行入口，如 `ptc.Run`）⇒ 档位下限、预算可兑现性、审批门三层同时失效，而门禁全绿 | `policy.yaml` 的 `execution_paths` 段 + `archcheck/execution.go` |

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

	"github.com/yi-nology/goagent-kernel/archcheck"
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

7. **泛型实例化各自成条**（v0.9.0 起）。按 Go 规范，按实例特化的方法
   （`func (m *Manager[int]) C()`）**只属于 `Manager[int]`**，不是 `Manager` 的方法。
   剥掉实参会把它并进基类型——两个实例化各挂 20 个特化方法就被判成
   「Manager 有 40 个方法」，而它其实不胖（**方向是虚高 ⇒ 假红**）。
   故聚合键保留实参：报告里出现 `pkg.Manager[int]` / `pkg.Manager[string]`，
   **一眼看出是哪个实例化胖**。
   **非泛型类型的键逐字不变**（`pkg.Report` 仍是 `pkg.Report`）——这条必须成立，
   否则改动会波及所有宿主，而不只是泛型用户（宿主仓实测无泛型接收者，零影响）。

   判据走 `go/ast`（不是逐行正则）——「一个函数体是不是只有一条转发语句」是结构性
   问题，只有语法树能判。实现见 `archcheck/godobject.go`，判据的由来与实测数据见
   issue #13。

### 与 oekit 家族的一致性（2026-10-17 补齐）

`oekit`（基础库）→ `oagentkit`（agent 原语）→ `goagent-kernel`（编排内核）→
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

## 文档站（GitHub Pages · VuePress）

**[yi-nology.github.io/goagent-kernel](https://yi-nology.github.io/goagent-kernel/)**

**VuePress 文档站**：介绍、设计初衷、怎么用（接入路线图 + 最小示例）、能力详解（九组）、
门禁体系、坑速查、包清单与机器事实。

**站点源码在本仓 [`site/`](site/)**（`docs/` 内容 + `gen.mjs` + `.vuepress/config.js`），
发布走 [`.github/workflows/pages.yml`](.github/workflows/pages.yml)（`build_type: workflow`）：
push 到 main 且站点内容或其**事实输入**（`README.md` / `go.mod` / `Makefile` /
`architecture-policy.yaml` / `docs/USAGE.md`）有变更时，CI 自动构建并部署。

本地构建：

```bash
make site     # = cd site && npm ci && npm run docs:build（开发预览：npm run docs:dev）
```

**站点上的每个数字都不手抄。** 生成器 `site/gen.mjs` 在每次构建前把 README（包表 /
工程底座）、`go.mod`（依赖版本）、`architecture-policy.yaml`（规则清单）、`Makefile`
（覆盖率门槛）、`docs/USAGE.md`（坑速查表）抽成站点页——**解析不到即构建失败**，
不静默填默认值。生成页不入库（入库就会没人重新生成，等于把「自动」改回「手抄」）。
理由与本仓其它纪律同源：**手抄一遍就等于多一份会静默失真的真相**。

> **迁移记录（2026-11-11）**：站点原先住在 `gh-pages` 独立分支（`build_type: legacy`），
> 生成器与源码不在 main 里——恰好违反本仓「单一真相」的纪律。当日迁回 `site/`
> 并升级为 VuePress 文档站；`gh-pages` 分支**冻结为历史归档**（迁走前的最后一版，
> 只作对照，不再接收产物）。

## 快速开始

**要接入本仓**：直接看 **[docs/USAGE.md](docs/USAGE.md)**，那是按「宿主接入顺序」
组织的完整指南——九组能力逐个讲清「职责 → 最小集成片段 → 关键符号 → 坑」，
末尾有一张按症状索引的坑速查表。

**要改本仓**：

```bash
make build          # 编译 archcheck
make test           # 全量单测
make check          # fmt + vet + lint + test + cover-gate + vuln + race + crossbuild + architecture-check + docsync + 仓库卫生
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
| **v0.31.0**（已发版，tag `v0.31.0`） | **BREAKING：项目整体更名 `agent-harness` → `goagent-kernel`**——旧名在语境里与「模型外壳 agent harness」「评测 harness」「harness.io」三头撞车（实测搜索前排全是本仓明确不做的东西），名字把读者引向错误预期。GitHub 仓、Go module path、文档站 URL 全部换名；仓内 94 个文件的 import 与活文档已全部替换。**历史快照刻意保留旧名**（CHANGELOG 与 docs/ 标注「历史快照」的文档不改写历史）；门禁规则 ID（`harness-*`）是内部标识符，不在本次更名范围。**API 逐字不变**——本次只是身份换名 | **① 机械替换 import**：`github.com/yi-nology/agent-harness/…` → `github.com/yi-nology/goagent-kernel/…`（全局替换即可，调用面零改动）。<br>**② `go mod tidy`**——module path 是编译期身份，不跑 tidy 会直接报找不到模块。<br>**③ 旧 GitHub URL 自动 301**（仓页面与 git 协议均然，旧 remote 不用急着改）；**文档站 URL 不重定向**：`yi-nology.github.io/agent-harness/` → [`yi-nology.github.io/goagent-kernel/`](https://yi-nology.github.io/goagent-kernel/)。<br>**④ `go` 指令地板 1.27.3 → 1.27.4**（随 Go 2026-11-10 安全发布抬升，标准库 CVE-2026-97033…97054；govulncheck 当天报本仓命中 1 条）：`go mod tidy` 后 GOTOOLCHAIN 自动对齐——地板抬升对下游是隐式 breaking，不跑 tidy 会报工具链不符 |
| **v0.29.0**（已发版，tag `v0.29.0`） | **判据面补强：cmdpolicy 事实面收编上游 bashguard**：① `DecideCommand` 从「只比 argv 前缀」升级为**全调用 AST 判定**（事实面收编 `oagentkit/tools/bashguard`，mvdan.cc/sh 真 AST）——前缀判据的三类典型绕过在此闭合：**多段**（`git status; rm -rf /` 的 rm 段）、**命令替换**（`echo $(rm -rf /)` 里的嵌套 rm）、**写重定向**（`git status > /etc/passwd` 的 `>` 是语句级事实、不在 argv 里）；② 新增**事实面下限**：含写重定向或动态展开（`$()`/反引号/`$VAR`）⇒ 裁决**不低于 prompt**（这两类语义前缀规则结构上看不见，静默 allow 即「判据比声明宽」）；③ 新增哨兵 `ErrUnjudgeable`（AST 超长/解析失败 ⇒ 判不了就不放行）；`Verdict` 新增 `Wrote`/`Dynamic` 事实字段，`Explain` 追加信号说明。**依赖**：新增 `github.com/oagentkit/tools` v0.14.3（带入 `mvdan.cc/sh` v3.14.1）；其余第一方地板与 v0.28.0 相同（该轮已抬到 guardx v0.24.1 / runtime v0.12.6 / toolgate v0.17.0 / pkg v0.42.0 等，以 `go.mod` 为准） | **① `go mod tidy`**——引入新依赖 `oagentkit/tools`（含 `mvdan.cc/sh` 传递依赖），不跑就报 `updates to go.mod needed`。<br>**② `DecideCommand` 的判定结果会变严**（这是修复不是回归）：此前被绕过的三类命令现在会被拦/升 prompt——`git status > x` 由 allow 变 **prompt**、`git status; rm -rf /` 由 allow 变 **forbidden**（全调用面取最严）。若你的宿主依赖旧行为放行这类命令：要么按新口径补规则，要么改用 **`Decide(argv)`**（纯前缀、不做事实面判定，调用面一字未动）。<br>**③ `Verdict` 新增 `Wrote`/`Dynamic` 两个字段**：键名构造不受影响；**无键名字面量**构造会编译不过，改键名即可。<br>**④ 命令解析失败/超长从「放行」变报错**（`ErrUnjudgeable`）——判不了就不放行，与 `ErrUnclosedQuote` 同一处置口径 |
| **v0.28.0**（已发版，tag `v0.28.0`） | **两处上游能力收编 + 一处下游可见的行为修正 + 第四轮 issue 清零**：① `cmdpolicy` 的**分词面收编上游 `oekit/pkg/shellx`**（本仓删掉手写分词器与 `mergeSeparators`，约 100 行）——收编**顺带修掉三处 POSIX 错误**：双引号内的反斜杠不再被一律丢弃（`"a\b"` 由 `ab` 变回 `a\b`，bash 实测即此）、分隔符只在**字符相邻**时合并（`a & & b` 不再被误并成 `a && b`）、CR 归空白（CRLF 命令行的行尾 CR 不再粘进最后一个 token）；② `cmdpolicy.SplitCommands` 的段文本改用上游 `shellx.QuoteArgv` 拼装，**段变得可往返**（段再分词逐项还原原 token）；③ `ptc` 的子进程 stderr 落进 `ExecutionAborted.Stderr` 前**过上游 `guardx/logredact` 脱敏**（凭据形态打码：URL 内嵌凭据、Bearer 头、JWT、厂商 token 前缀、长 hex、键值对）——子进程常把连接串与 API key 打进 stderr（MCP server 启动回显配置是最高频形态），而这段文本原先原样进错误消息与诊断报告；④ **第四轮 issue 清零（#62-#67）**：`egress` 代理变量判据收严为「一个不缺、一个不歪」（#62）、绕过名单收口为 NO_PROXY/no_proxy 并集单判定（#63）、`policy` 门面补 `Mutating(b)` 转发（#64，`toolgate` v0.17.0：`Op.Mutating` 三态化，**nil=未知 ⇒ 审计门 fail-safe 升人审**）、`ptc` 子进程后端三处契约修复（#65-#67，判定环归因复用 `stageOutcome` 等） | **① `cmdpolicy` 的分词结果在三处形态上会变**（只影响含反斜杠 / 空格分隔的 `& ` / CRLF 行尾的命令行）：本仓旧实现给 `"a\b"` → `ab`，现在给 `a\b`——**这是修复不是回归**（bash 实测逐字一致），但若你的宿主按旧行为做过前缀匹配，需按新口径核对。② `SplitCommands` 返回的段文本形态变了（含空格的段现在带引号）——**段仍是合法的 shell 命令行**，可再分词、可回放，但**不要再按字面比对段文本**（改判段再分词后的 token）。③ `ptc` 的 `ExecutionAborted.Stderr` 现在是**脱敏后**的文本：崩溃原因（fatal error / panic 栈）原样保留，凭据被打码——若你的宿主把 stderr 全文写进自己的日志系统，脱敏后是安全方向（凭据不再二次扩散）；若你的宿主**依赖**从 stderr 里读到某个明文 key（不当用法），请改从别的通道取。<br>**④ `go mod tidy`**：本轮无新增依赖（`shellx` 与 `logredact` 都在已声明的 `oekit/pkg` 与 `oagentkit/guardx` 模块内），故依赖地板不变。<br>**⑤ 若你的宿主依赖「不标变更=非变更=放行」**：`toolgate` v0.17.0 起 `Op.Mutating` 三态化（`*bool`），nil=未知 ⇒ 审计门升人审——`gatecheck`/`tooldisclose`/`ptc` 不再替宿主猜变更标记，**需显式声明**。<br>**⑥ `egress.Verify` 收严**：六个代理变量「一个不缺、一个不歪」才绿、`Bypass` 判定对 NO_PROXY/no_proxy 取并集——此前靠缺省通过的配置会开始报违约（修复假绿，#62/#63） |
| **v0.27.1**（已发版，tag `v0.27.1`） | **修复轮，无破坏性变更**：`ptc` 的拒绝分类载体化（issue #53-#59）——分类随抛出对象一起走，收口处据此把「守卫判定拒绝」与「守卫自己崩了」分成两类，不再一律映射成 transient；另修 `protocol` 的报告校验、`drain` 的预算归一、`pending` 的列值解析。**依赖地板不变** | **无需 `go mod tidy`**，调用面逐字不变。**若你的宿主按「拒绝一律可重试」处理 ptc 的拒绝**，请改按 `toolfail` 的分类分流（`Kind` 随拒绝一起给出）——本版起「权限拒绝被当成瞬时错」这条误判已修正 |
| **v0.27.0**（已发版，tag `v0.27.0`） | **第一方依赖全量抬到最新 + 一处下游可见的解析口径变化**：① 依赖地板抬升——`oekit/concurrency` v0.28.4→**v0.31.0**、`oekit/pkg` v0.37.4→**v0.40.0**、`oekit/logx` v0.8.10→**v0.8.12**、`oagentkit/agent` v0.14.3→**v0.14.5**、`guardx` v0.23.2→**v0.23.4**、`runtime` v0.12.2→**v0.12.4**、`toolgate` v0.15.2→**v0.15.3**、`mcpkit` v0.12.1→**v0.12.4**；② **`protocol` 的解析通道边界随之扩宽**——`oekit/pkg` v0.38 起 `jsonrepair.Repair` 的输出契约明写「`:` 后缺值补 null」（pkg#268），故形如 `{"agent_name":"X","confidence":}` 的**非法 JSON 从「拒收」变成「被修复通道救回」**；救回点落在本仓 `markRepaired` 的既有设计内（修复产物**必降置信 low**，下游 verify/needs_review 承接），本仓代码**零改动**、只把测试的不变量从「失败即拒收」改写为「凡经修复通道放行必为 low」，并补了成对的反向判据（缺冒号、无 JSON 结构仍拒收） | **① `go mod tidy`**——地板抬升对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。<br>**② 若你的宿主直接调 `protocol.ParseReport` / `ParseLLMReport`**：缺值形态的模型输出现在**不再返回 `ErrNotReport`**，而是返回一份 `Confidence="low"` 的报告。这不是回归——上游修复通道的扩宽是刻意行为，且本仓既有设计（`markRepaired` 降置信）本就是为这类形态准备的。**若你把「解析失败」当拒收信号用**，请改判 `Confidence == protocol.ConfidenceLow`。（结构不可修复的形态——缺冒号、无 JSON 结构——仍照常返回 `ErrNotReport`，本仓补了成对的反向用例钉住这条边界。）<br>**③ 上游 `oekit/concurrency` v0.28.2 起 `async.PanicHandler` 签名改为 `func(ctx context.Context, r any, stack []byte)`**——本仓**未定义**该 handler（故编译未破），但**你的宿主若自行实现了它，需要改签名**。这是本轮唯一一处**真正传导到下游的 BREAKING**，请务必核对。 |
| **v0.22.0**（已发版，tag `v0.22.0`） | **依赖地板抬升 + 三处零行为重构**：`guardx` v0.21.0→v0.22.0、`toolgate` v0.13.0→v0.14.0、`mcpkit` v0.11.0→v0.12.0、`logx` v0.8.4→v0.8.6；本仓代码只做等价收口（`archcheck` 删掉两个**同体异名**的证据判定包装、`protocol` 的手写 nil 判空换 `ptrx.Deref`、`taskboard` 的循环二次拼接换 `strings.Join`），**公开 API 零变更、零行为差异** | **只需 `go mod tidy`**——地板抬升对下游是隐式 breaking：不跑就报 `updates to go.mod needed`，且报错不提真正原因。三处重构的调用面逐字不变。**一处刻意不采用**：`mcpkit` v0.12.0 新增的 `DisclosureGate`（模型面三态披露契约）本仓不接——它绑 `mcpkit/mcp` 的 `Catalog`，而那个包依赖 eino + mcp-go，引入等于拿一个三态判定换一整条 mcp SDK 依赖；且本仓 `tooldisclose.Gate` 的哨兵二分 + `Retryable` 自报 + `Activated` 副作用仍要留（`toolfail` 靠 `Retryable` 分流，不解析文案） |
| **v0.21.0**（已发版，tag `v0.21.0`；**本行由 v0.22.0 轮补录**，该版当时未补行） | `trajectory` 的 OTel 映射长成**契约表**：新增 `AttrSpecs` / `AttrSpecOf` / `ForbiddenLabels` 与 `TraceGranularity` / `Domain` / `Cardinality`——每个属性有主、只有有界枚举才许当 label、父子关系落 `Span.ParentID`。**无 API 删除、无依赖地板变化** | **无需 `go mod tidy`**、无需改调用面。补录依据是 `git log v0.20.1..v0.21.0`（一个 feat + 两个 docs 提交）；**若你依赖 `trajectory` 的 label 面**，请自行对照 `AttrSpecs` 自查——本次补录未逐条核对这版对 label 的收窄 |
| **v0.20.1**（已发版，tag `v0.20.1`） | **`egress.Spec.Allow` 的语义修正（安全方向）**：`Allow` 是「**允许经代理访问**的目标」，此前被写进 `NO_PROXY`——那等于让允许名单里的目标**绕过代理直连**：代理的拒绝名单 / 端口白名单 / 只读档对它全部失效，而声明在报告里仍显示「已兑现」。现 `Allow` 不再产生 `NO_PROXY`；新增 **`Spec.Bypass []string`** 表达「不经代理直连」，`Verify` 增判绕过名单（**语义等价**比较：顺序与空白漂移不算违约，判字面就是假红）。另：`egress` 补齐端到端验收用例——真强制代理 + 真请求（GAP 台账 P0-1 遗留的那一条）。 | **① 若你把 `Spec.Allow` 当「直连豁免」用**（即依赖它产生的 `NO_PROXY`）：改用 `Spec.Bypass`。名字与语义都变了，但这一版是**把危险的默认关掉**——旧行为让「允许」变成「豁免」。<br>**② 若你把 `Allow` 当「允许目标」用**（正确用法）：变得更严格，不破坏调用面——`Allow` 现在只是交给兑现者的声明（代理按自己的名单判），env 里不再出现 `NO_PROXY`。<br>**③ `Verify` 收紧了**：`Bypass` 非空而 `NO_PROXY` 缺失或不一致即判违约。<br>**④ 不需要 `go mod tidy`**——本版无依赖地板变化。 |
| **v0.20.0**（已发版，tag `v0.20.0`） | **出口声明的兑现面收口（行为收紧）**：① `egress.Mode` / `egress.Enforcer` 的**真相上移**到上游 `guardx/sandbox`（`EgressMode` / `EgressEnforcer`），本仓这两个名字改为 **type alias**（编译期同一类型，调用面逐字兼容）——词表留在本仓时，上游任何执行后端要声明「我能兑现」都得 import 本仓 ⇒ 成环 ⇒ 那条接口在结构上只可能由宿主实现，且编译期一声不响；② `ptc.SubprocessOptions` 新增 **`EgressBackend egress.Enforcer`**，**spawn 之前**先问后端「你能强制这一档吗」，问不到就**不起子进程**；③ `archcheck` 的 `egress_paths` 判据补上**第二半证据**（`proxy` 档除 env 生成外还须看得见能力问询 `SatisfiedBy`），policy 新增可选字段 `capability`。另：`guardx` v0.19.0→v0.21.0、`oekit/pkg` v0.36.0→v0.37.1 | **① 若你用了 `ptc.SubprocessOptions.Egress` 且档位不是 `unrestricted`：必须同时填 `EgressBackend`**（一个实现 `sandbox.EgressEnforcer` 的对象，例如宿主起的强制代理；上游 `guardx/egressproxy` 就是现成实现，它的 `Egress()` 只声称 `proxy` 档）。不填 ⇒ 现在**拒绝执行**（v0.18.0 时会照常起子进程、只生成 env）——这是**修复不是回归**：环境变量只是建议，「生成了 env」当成「已兑现」正是这条链此前空转的原因。<br>**② `go mod tidy`**——地板抬升对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。<br>**③ 若你在自己仓里复用 `archcheck`**：`egress_paths` 的 `proxy` 档现在要求**两半证据**（env 生成 + 能力问询），只做前一半的启动点会红。这条是刻意的（旧判据对「只生成 env」静默放行）；确需过渡就在该条登记的 `capability` 字段指明你们的能力问询方法名。<br>**④ 本仓 `sandbox` 门面新增 `EgressMode` / `EgressEnforcer` / 三个档位常量别名**——宿主实现 Provider 时可在门面里拿到这两个名字，不必直接 import 上游。<br>**⑤ 上游 `guardx` v0.20.0 收紧了 `egress.Check` / `CheckWith` / `CheckResolving` 的判定面**：新增 **scheme 白名单（只认 `http` / `https`）**，`file:` / `gopher:` / `ftp:` / `ws(s):` / 空 scheme（相对 URL）**一律拒绝**；`Policy.NewClient` 同时补上 `DialTLS` 这条此前被绕过的拨号路径。若你（或你的宿主）把这两个判定用于**非 http(s) 出站**，升级后从「通过」变成 `BlockedError`——这是**修复不是回归**（判定面本来就只该覆盖 LLM 可控的 fetch 类 URL）；按 host 判定的 `CheckHost` / `CheckHostWith` 没有 scheme 概念，行为不变。 |
| **v0.18.0**（已发版，tag `v0.18.0`） | **纯新增：十个机制包 + 两条新判定面**——`cmdpolicy`（命令级判据）、`egress`（出口策略）、`preset`（权限预设）、`sessionref`（跨会话引用）、`taskboard` + `boardtest`（协调面契约与并发判据）、`skilldisclose`、`hookengine`、`memorypipe`、`delegate`、`worktree`；`ptc.SubprocessOptions` 新增 `Egress egress.Spec`；`archcheck` 新增两条 `go/ast` 判定面（`require_rule_examples` 规则样例自检、`egress_paths` 子进程出口登记）。详见 [docs/GAP-ANALYSIS-20261106.md](docs/GAP-ANALYSIS-20261106.md) 的第六节 | **无需改动**——全部是新增 API 与新增 policy 字段，不写新字段即按旧行为生效。三点可选留意：<br>**① `ptc.SubprocessOptions.Egress` 零值 = 不限制**，与旧版逐字一致；一旦你开始用 `ModeProxy`，子进程会拿到注入的 `HTTP(S)_PROXY` 等变量（不会自动发生，必须显式声明）<br>**② 若你在自己仓里复用 `archcheck`**：可把 `egress_paths` 段抄过去（判定执行体是同一份可导入包），把「spawn 点必须登记 + `proxy` 必须看得见兑现证据」这条判据一起带走<br>**③ `preset` 的方向是 preset → gatecheck**，别反向接（低层判据认识高层组装件会立刻成环）|
| **v0.17.0** | **三处破坏性收窄 + 依赖地板抬升**：① `drain.Budget` 返回**命名类型** `DrainBudget`（`Drain` / `DrainSession` / `DetachCtx` 只收它），于是 `context.WithTimeout(ctx, drain.Budget(x))` **编译不过**——那正是「优雅停机静默变硬切」的唯一入口；收尾预算一律 `drain.ShutdownBudget`（返回普通 `time.Duration`）。`drain` 包同时把 `Drain` / `DrainSession` 两份收敛循环合并成一份。② 删 `netguard.CheckURL` / `netguard.CheckChain`（双 bool 包装），放行面唯一形态是 `Allow{...}` + `CheckURLWith` / `CheckChainWith`。③ 删 `pii.Redact` / `pii.RedactValue`（面无关门面），唯一入口是 `pii.Apply` / `ApplyValue`（**必须显式指定面**）。另：`ptc` 三处预算归一收进 `ptc/limits.go`、三份快照式订阅表收编到 `internal/subhub`（均为内部实现，无 API 变化）。**依赖**：oekit / oagentkit 家族抬到本轮最新（逐项见 [CHANGELOG.md](CHANGELOG.md) 的 v0.17.0 段）；根目录 `tools.go` 版本钉子删除 ⇒ `oagentkit/{llm,obs,textx}` **不再是本仓依赖** | **① `go mod tidy`**——地板抬升对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。<br>**② `drain` 接线改两个入口**：`Drain` / `DrainSession` / `DetachCtx` 的实参包一层 `drain.Budget(cfg.X)`；喂 `context.WithTimeout` 的那一个数改走 `drain.ShutdownBudget(cfg.X)`。机械改法、**语义不变**（`Budget` 对正值与负值都原样，只有 0 补缺省），但**别把 `Budget` 的负值归一**——`<0` 是「关闭排水」的配置语义，归一会把「少做」静默翻成「多做 60 秒」<br>**③ `netguard` 改调用形态**：`CheckURL(u)` → `CheckURLWith(u, Allow{...})`、`CheckChain(c)` → `CheckChainWith(c, Allow{...})`——两个 bool 的位置不再需要记（写反不报错，只是静默改变放行面）<br>**④ `pii` 改调用形态**：`Redact` / `RedactValue` → `Apply` / `ApplyValue` 并显式传面；面无关门面拿不到 `blocked` 信号，策略配成 `block` 时它只会**静默打码**<br>**⑤ 若你的 `go.mod` 靠本仓间接拉起过 `oagentkit/{llm,obs,textx}` 的版本地板**，请自己在 `go.mod` 里显式钉——本仓不再声明它们 |
| **v0.14.0** | **依赖地板抬高**（`oekit/pkg` v0.28.0→v0.30.0、`oekit/logx` v0.7.1→v0.8.0、`oekit/concurrency` v0.22.0→v0.23.0、`oekit/core` v0.12.0→v0.12.1、`oagentkit/guardx` v0.15.0→v0.17.0、`oagentkit/llm` v0.17.1→v0.18.1、`oagentkit/textx` v0.6.2→v0.7.1、`oagentkit/mcpkit` v0.8.6→v0.9.0）；**本仓自身代码零改动** | **① `go mod tidy`**——地板抬高对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。<br>**② 若你直接 import 过 `github.com/oagentkit/guardx/audit` 或 `github.com/oagentkit/guardx/bashguard`：这两个包自 guardx v0.17.0 起**不再属于 guardx 模块**，要改 import 路径**——audit → `github.com/oagentkit/obs/audit`；bashguard → `github.com/oagentkit/tools/bashguard`。本仓自身零命中这两个路径（用的是新路径 `obs/audit`），但你的宿主代码若直接依赖过，需跟着换这一行 import。<br>**其余全是等价改写与纯新增，无公开 API 变更**：`oekit/pkg` v0.30.0 补 `convert.AnyToStrOr` / `OptStr` / `OptBool` / `OptInt32` / `OptInt64` / `AnyToInt32` 六个 nil 安全入口（`AnyToStr(nil)` 此前返回 字符串 `"<nil>"` 这种**长得像值**的哨兵），并修 `cache.HashKey` 的参数边界歧义（`("a","b")` 与 `("a:b")` 曾算出同一个 key ⇒ 缓存串数据）；`oekit/logx` v0.8.0 补 `Nop()` 静默 logger 与 `LevelOff` 档；`oekit/concurrency` v0.23.0 修 `budget.Ledger` 的 int64 溢出绕过（一次 `n ≈ MaxInt64` 的消耗曾让该维度**永久**变成「无限制」）与 `singleflight.DoChan` 的清理漂移（`Forget` 后同 key 曾并发跑两次 fn），并补 `Ledger.Used` 观测口；`oagentkit/mcpkit` v0.9.0 补 `Pool.Tool(server, name)` 与 `ToolsResult.ByServer`（纯新增，`Tools` 的扁平列表保留） |
| **v0.13.0** | **`spill` 路径逃逸修复（fail-closed 收口）**：会话标识与定位符的判定改委托 `oekit/pkg/pathx.IsSafeFileName`——旧实现用 `filepath.Base` 判等，而 `filepath.Base("..") == ".."`，于是 `sessionID=".."` 能让落点指到 spill 根目录的**父目录**（三重守卫两道一起失效，实测可读出根外文件）；**检索条数钳制**改用 `oekit/pkg/pagination.Policy` 口径（等价）；**schema 编译缓存**收编 `oekit/pkg/cache`（内部实现，契约不变）；**依赖地板抬高**（`oekit/pkg` v0.22.0→v0.28.0、`oekit/concurrency` v0.20.1→v0.22.0、`oagentkit/agent` v0.10.6→v0.11.0、`guardx` v0.14.0→v0.15.0、`llm` v0.16.0→v0.17.1、`obs` v0.9.6→v0.9.7、`toolgate` v0.12.4→v0.12.5、`mcpkit` v0.8.5→v0.8.6） | **① `spill` 的判定口径收紧了**：会话标识/定位符含路径形态（`.` / `..` / 含 `/` `\` / 含 Windows 盘符相对路径）此前多数**静默通过**，现在一律**显式拒绝**（返回 `ErrBadName` / 「会话标识非法」）。若你传的是用户可控的会话标识，非法形态从「悄悄读到别的目录」变成「拿到错误」——**这是修复，不是回归**。<br>**② `go mod tidy`**——地板抬高对下游是隐式破坏性变更，不跑就报 `updates to go.mod needed`，且报错不提真正原因。<br>**③ 两处上游 BREAKING 会传导到你**：`oekit/concurrency` v0.21.0 起 `ratelimit.Limiter` 接口**启用了 `Wait(ctx context.Context) error`**——**自行实现该接口的宿主需补上这个方法**（旧实现不再满足接口）；`oekit/pkg` v0.26.0 起 `convert.ResolveClientIP` **去掉了 `xri` 入参**（确需采信改用 `ResolveClientIPWithXRI`）。本仓自身两处都没直接用到，但你的宿主代码可能用了。<br>**其余全是等价改写，无公开 API 变更**：`pagination.Policy` 口径与旧 `clampLimit` 逐条等价（n≤0→默认、超限→钳到上限、区间内原样）；schema 缓存与单边界钳位均为内部实现替换 |
| **v0.10.0** | **`guardset.Assembly` 对齐 `netguard` 的「整份原子交换」**：换份走 **CAS + `mutate(旧值)`**（新增 `SwapFunc`）；新增**代次**（`Assembly.Generation()` / `Snapshot()` / `Outcome.Generation`）与**换份订阅**（`Subscribe`/`Unsubscribe`）；**修两处静默失效**：① 并发换分**丢更新**⇒「撤销被撤销」（被撤掉的守卫悄悄复活）；② 通知期回调 panic 把**已落地**的换份说成失败 | **若你按 v0.9.0 的示例用 `Swap(next)` 做读改写，请改用 `SwapFunc(ctx, mutate)`**——`Swap` 的语义是「整份替换为这一份」，**不基于当前那一份**；两个并发撤销者会互相覆盖，被撤掉的守卫悄悄复活且**没有任何提示**。`SwapFunc` 基于当前份做读改写（CAS 循环），并发下不丢更新。**其余全是新增 API，无接口变更**（`SwapRecord`/`Outcome` 只**新增**字段）；`Set` 仍只读，`guardset.Stage(set)` 仍可用 |
| **v0.9.0** | **新增 `gatecheck` 包**（审批层 × 沙箱层的**联合判据**：两层都同意才放行，含 `ExecuteAndRun` 与受管后端 `Managed`——判据与执行不可分）；**新增 `guardset.Assembly`**（守卫集合的装配面：撤销 = **换整份**，链不用重接）；**`policy` 门面新增 `Stage`**（审批门接进判定环的唯一入口）；**`archcheck` 新增 `import_exempt`**（import 路径级**精确**例外，可放行被禁依赖树里的某个具体子包）；**`ptc` 修复「子进程不回结果时父侧永久挂死」**（上游 `guardx/wire` 的 `Starter.Start` 收了 ctx 却不用它，本仓改为取 ctx deadline 与墙钟预算较早者）+ 内存兜底改用 cgroup v2（原 RLIMIT_AS 会把 Go runtime 一起掐死）+ 执行异常终止的**死因分类**；**依赖地板追平**（`oagentkit/agent` v0.10.2→v0.10.4、`llm` v0.15.0→v0.15.2、`obs` v0.9.4→v0.9.5、`toolgate` v0.12.1→v0.12.4、`mcpkit` v0.8.4→v0.8.5） | **`go mod tidy`**——地板抬高对下游是隐式破坏性变更，不跑就报 `updates to go.mod needed`，且报错不提真正原因。**其余全是新增 API，无接口变更**：不接 `gatecheck` / `guardset.Assembly` / `policy.Stage` / `archcheck` 的 `import_exempt` 的宿主代码**逐字不变**（policy 不写新字段即按旧行为生效）。**一处行为变化需知**：`ptc` 现在尊重 ctx deadline——此前父侧可能在子进程不回结果时**永久挂死**，现在到点收刀并报错；若你依赖「父侧一直等」请显式给足 deadline |
| **v0.8.4** | **依赖地板追平矩阵本轮新版本**（`oagentkit/agent` v0.10.1→v0.10.2、`obs` v0.9.3→v0.9.4、`runtime` v0.7.1→v0.7.2、`textx` v0.6.1→v0.6.2、`toolgate` v0.12.0→v0.12.1、`mcpkit` v0.8.3→v0.8.4）；**无代码变更** | **`go mod tidy`**——地板抬高对下游是隐式破坏性变更：不跑就报 `updates to go.mod needed`，且报错不提真正原因。**无行为差异**：这 6 个上游版本**全部是纯依赖地板追平**（各仓自身代码零改动，只抬 `oekit/pkg` / `oekit/concurrency` / `guardx` / `llm` 四类地板之一），逐仓核对见 [CHANGELOG.md](CHANGELOG.md) 的 v0.8.4 段 |
| **v0.8.3** | **依赖地板追平上游本轮新版本**（`oagentkit/llm` v0.14.2→v0.15.0、`oekit/concurrency` v0.18.0→v0.19.0）；**无代码变更** | **`go mod tidy`**——地板抬高对下游是隐式 breaking：不跑就报 `updates to go.mod needed`，且报错不提真正原因。**无行为差异**：llm v0.15.0 只新增了一个**可选**字段 `OpenAIProviderConfig.HTTPClient`（缺省 nil 时行为逐字不变），本仓只用 llm 的空导入（`tools.go` 的工具依赖登记），不碰其 API。**顺带值得知道**：llm v0.15.0 起宿主可注入自己的 `*http.Client` → 配合本仓 `netguard.NewClient` 把出站守卫挂到 `Transport.DialContext`（写面校验拦不住 DNS rebinding，守卫必须在拨号层）——接线形态见 [llm v0.15.0](https://github.com/oagentkit/llm/releases/tag/v0.15.0) |
| **v0.8.2** | **依赖地板追平上游本轮新版本**（`oekit/pkg` v0.18.0→v0.20.0、`oagentkit/guardx` v0.13.0→v0.13.1）；**无代码变更** | **`go mod tidy`**——地板抬高对下游是隐式 breaking：不跑就报 `updates to go.mod needed`，且报错不提真正原因。两处抬升**均无行为差异**（`guardx` v0.13.1 只改 `go.mod`/`go.sum`；`oekit/pkg` v0.20.0 相对 v0.18.0 是纯增量 + 一处退避溢出修复），逐项核对见 [CHANGELOG.md](CHANGELOG.md) 的 v0.8.2 段 |
| **v0.8.0** | **`sideeffect.Store` 新增 `Renew` 方法**（续期 + 校验持有资格），并新增 `sideeffect.FencedSink` 契约（下游拒收陈旧写入）；新增 `retryplan` 包（重试处置决策） | **接口层面的破坏性变更**：宿主若已实现 `sideeffect.Store`，需补上 `Renew(ctx, act, lease, ttl) (Lease, error)` 方法——语义见包头注释（**续期必须同时校验令牌仍是最新代**，否则是「假续期」）。宿主若已实现下游栅栏拒收，可改用 `FencedSink` 契约对齐比较口径与原子性要求。`FencedSink` 与 `retryplan` 是**新增**的，不影响既有代码 |
| **v0.7.0** | **`toolfail` 与 `sandbox` 下沉上游 `oagentkit/guardx`（v0.9.0）**，本仓转**门面**（类型别名 + 常量别名 + 单条上游转发）；依赖地板抬到上游本轮新版本（`guardx` v0.8.2→v0.9.0、`llm` v0.14.1→v0.14.2、`agent` v0.10.0→v0.10.1）| **调用面逐字兼容，宿主代码不用改**——类型别名是编译期同一类型，宿主现有的 `errors.As` / `errors.Is` 判定链继续有效。**但必须 `go mod tidy`**：地板抬高对下游是隐式 breaking，不跑就报 `updates to go.mod needed`，且报错不提真正原因。下游若直接 import 过 `github.com/yi-nology/goagent-kernel/toolfail` 或 `.../sandbox` 的**具体实现**（如自造 `*Failure`、注册自定义 Provider），请改 import 上游路径 `github.com/oagentkit/guardx/toolfail` / `.../sandbox`——门面只保转发，不提供实现。逐版差异见 [CHANGELOG.md](CHANGELOG.md) 的 v0.7.0 段 |
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
go get github.com/yi-nology/goagent-kernel@latest
```

```go
require github.com/yi-nology/goagent-kernel v0.29.0   // 零 replace，直接版本化引入
```

> 升级前用 `git ls-remote --tags origin` 核候选版本真的存在——
> `go list -m -versions` 报的是 proxy 的缓存目录，不是远端现实。
> 当前已发布 tag 以远端为准（写稿时为 v0.29.0）。
>
> ⚠️ **require 里写的是「最低版本」，不是「锁定版本」**：下游 `go mod tidy`
> 只保证不低于这个版本，不会自动升到最新版。要用新版本必须显式
> `go get ...@<新版本>`——这也是为什么这里写当前版本号而不是 `v0.0.0`。

```go
import "github.com/yi-nology/goagent-kernel/dispatch"

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
