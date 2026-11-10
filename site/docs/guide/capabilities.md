---
title: 能力详解
---

# 能力详解：九组能力

本章按「解决什么问题」分组。每组给出关键符号与**什么时候用**；
逐包的集成片段与坑在仓内
[docs/USAGE.md](https://github.com/yi-nology/agent-harness/blob/main/docs/USAGE.md)，
包级事实（行数、依赖）在[包清单与机器事实](../reference/packages.md)。

## 1 · 报告与协议：`protocol`

**解决**：模型输出的 JSON 不可信——字段漂移、置信虚高、越权字段。
**形态**：五级容错解析 → 白名单校验 → 注入式渲染。

- 解析分信任级：`ParseLLMReport`（剥引擎侧字段，解析 LLM 产出用这个）与
  `ParseReport`（落库往返入口）；
- 校验分两级：warn 留痕不拦截，critical 才 `ErrViolation` 整份拦；
  `steps` 执行契约是 critical（命令形态会被执行器当 shell 跑，假成功风险）；
- 渲染三档（`Render` / `RenderView` / `RenderViewWith`），标题与文案走
  `RenderOptions` 注入；
- 证据面：`AssignEvidenceIDs` / `CheckEvidenceRefs`（编号唯一性、引用完整性）。

**什么时候用**：凡是「LLM 产出 → 结构化下游消费」的出口。

## 2 · 工具判定环：`tooldisclose` + `guardset` + `toolfail` + `policy` + `dispatch`

**解决**：工具调用的「披露、守卫、失败、审批、拓扑」五件事若各走各的路，
必有绕过点。

- `tooldisclose`：渐进式披露 + 调用门禁（拒绝即激活）；
- `guardset`：多守卫**并求值**、拒绝合成一条、可重试取保守侧；`Assembly` +
  `Swap` 支持热撤销（`Without` 对不存在的名字**报错**——撤销要留痕）；
- `toolfail`：失败分类数据表（可重试 / 退避 / 需人工 / 重发安全 / 模型指引）；
- `policy`：操作审计门，两个接入口——`WithAuditGate`（包单个工具）与
  `Stage`（接进判定环，审批规则只装配一次）；
- `dispatch`：权限即拓扑——allow 矩阵物化 + 深度上限 + 自派发拒绝 + 环检测，
  拓扑经 `EdgeSource` 由宿主投影。

**什么时候用**：只要你放一个工具给模型。**全部接进同一条 `invoke.Stage` 链**，
包括程序内调用与子进程侧调用。

## 3 · 编排契约与续跑：`contract` + `statemachine` + `checkpoint` + `commitgate`

**解决**：状态存哪、怎么流转、崩了怎么接着跑、终态怎么独占。

- `contract`：`RunContext` / `StepSink` / `ToolBinder` / `Clock`——
  编排引擎与宿主状态的**唯一边界**（纯接口面，零实现）；
- `statemachine`：环节状态机纯谓词（含 `interrupted` 终态：中止 ≠ 失败）；
- `checkpoint`：续跑而非重放——`BuildPlan` 纯函数、可信边界按依赖判定、
  终态不自动重开、序号单调闸、跨进程 CAS 存取（`StoreCAS.SaveIfNewer`）、
  并行 fan-out 按各自前驱采信；
- `commitgate`：终态独占提交闸，`Acquire` / `ReleaseLease` 凭令牌放闸。

**什么时候用**：编排超过一个环节、或「崩了不能全量重跑」的时候。

## 4 · 上下文三件套：`budget` + `spill` + `offload`

**解决**：上下文窗口是有限预算，不是无限历史。

| 包 | 管什么 | 关键点 |
|---|---|---|
| `budget` | 输入侧分段计量 + 压实计划 | 区分工具面/历史膨胀；毛/净移除量分列；三重空转守卫 |
| `spill` | 输出侧截断 + 分页取回 | 溢出提示契约、按龄清理 |
| `offload` | 内联大对象外置 | 两阶段：首次只标记；sha 去重 |

**什么时候用**：工具面大（长工具描述/多工具）、历史长（多轮）、或产出含大对象
（图片、日志、报告全文）。

## 5 · 程序化编排：`ptc` + `sandbox` + `cmdpolicy` + `egress` + `preset`

**解决**：N 轮 ReAct 往返的上下文成本与原始数据过模型面的问题。

- `ptc`：模型写程序、沙箱执行、bindings 调宿主工具、归并结果才回上下文；
  三重预算（墙钟/调用数/字节数）；绑定可带 Signature（净化名，撞名装配期拒）；
- `sandbox`：隔离档位声明与兑现（`sandbox.Capable`）——子进程后端用 cgroup v2
  `memory.max` 兑现 `MaxMemory`，建不出即拒绝执行；
- `cmdpolicy`：**命令级判据**——审批通过的工具内部执行的命令（前缀 token 规则 +
  三态裁决 allow/prompt/forbidden，规则自带样例、加载期自检）；
- `egress`：子进程出口登记（执行路径不得绕过沙箱登记簿）；
- `preset`：把「档位 × 审批 × 出口」收成一份具名组合，避免每次手拼。

**什么时候用**：一次任务要聚合多次工具调用（多表查询、批量巡检、逐机采样）。

## 6 · 实时事件与流式：`eventbus` + `watchdog` + `delta` + `drain` + `pending`

- `eventbus`：进程内有损广播——**先落库再广播**，SSE 断了靠 DB 回放游标补达；
- `watchdog`：工具调用配对监视（收口/幽灵调用的判据）；
- `delta`：增量节流 + 标签分流（`Pacer` **每环节新建**，长生命周期会内存涨、
  收口卡住）；
- `drain`：优雅排水（用 `DrainSession` 而非全局 `Drain`，否则一个卡死会话
  陪葬所有会话）；
- `pending`：多轮挂起态载体（落列必须走 `pending.ColumnValue`）。

## 7 · 安全面：`netguard` + `pii`

- `netguard`：出站 SSRF 守卫——名字层（解析前按名拒）+ 逐 IP 过 egress +
  重定向逐跳重验 + `DialFunc` 建连后核对真实 RemoteAddr（闭合「校验 → 拨号」的
  rebinding 窗口）。扩展名单整份原子交换，**代次只有一个真相**；
- `pii`：个人信息脱敏（手机号/身份证四重校验），运行时策略单指针原子交换 +
  变更订阅；内核只回报信号，阻断由宿主执行。

**什么时候用**：模型/工具能碰外网，或报告/上下文里可能进个人信息。

## 8 · 副作用只做一次：`retryplan` + `sideeffect` + `sidetest`

- `retryplan`：重试处置决策（该重试？该人工？该原样重发？）；
- `sideeffect`：幂等键 + 租约 + 栅栏——**副作用至多一次**契约；
- `sidetest`：给你的存储实现一份**验收标准**（注入时钟跑一遍套件）。

**接线范式**：`retryplan` 判策 → `sideeffect` 执行。判策只看
`Retryable` 与 `SafeToResubmit` 两个正交维度。

## 9 · 协作与治理面：`taskboard` + `skilldisclose` + `hookengine` + `memorypipe` + 其余

- `taskboard` + `boardtest`：多 agent 的认领与依赖（带验收套件）；
- `skilldisclose`：技能面的渐进披露；
- `hookengine`：钩子装配点；
- `memorypipe`：记忆管道；
- `ticket` / `trajectory`：工单与轨迹载体；
- `toolname`：工具名净化（ptc 绑定名复用同一套判据）；
- `gatecheck`：审批 × 档位的联合判据；
- `sessionref` / `worktree` / `delegate`：会话引用、工作树、委派原语；
- 门面三件 `dispatch` / `policy` / `reminder`：调用面稳定，核心归上游
  （`reminder` 是系统注入提示的防伪包裹）。

## 怎么选：三个判断

1. **这判定换个宿主要不要改？** 要 → 写在宿主适配层；不要 → 可以考虑内核机制。
2. **这语义跨宿主必须一致吗？**（失败分类/隔离档位/终态独占）→ 用内核的机制包，
   别自研——各判一套必然漂移。
3. **这条路径绕过判定环了吗？** 任何工具到达模型的执行面都要过同一条
   `invoke.Stage` 链；发现第二条路 = 缺陷。
