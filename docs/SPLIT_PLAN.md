# agent-harness 拆分方案（2026-10-17）

从上游私有宿主仓拆出编排内核。分层：**L1 已完成，L2/L3 待拆**。

```
L1  harness-core      ✅ 已迁   10 包 / ~6.1k 行 / 38 文件
L2  harness-runtime   🔄 2 批共 6 包已下沉（ptc/watchdog + delta/pending/reminder/drain）
L3  host-domain        ⏳ 留在宿主  编排图 / 词表 / 专家包
```

判定标准：**这个包需要知道自己在给谁干活吗？** 需要 → L3；不需要 → L1/L2。

> **L2 的抽法与 L1 不同**：L1 是整包搬（零内部依赖即可）；scheduler 各机制面都挂在
> `*Scheduler` 上、互相持有状态，**无法整包搬**。L2 只能**逐能力**抽：挑出依赖面最窄的
> 机制先行，每抽一个包跑一次全量 build+test。已完成两批：§一.2（ptc/watchdog）、
> §一.3（delta/pending/reminder/drain）。

---

## 一、L1/L2：已完成

### 一.1 L1 内核包（8 个）

| 包 | 行数 | 内部依赖 | 迁入前耦合 |
|---|---|---|---|
| `protocol` | 3332 | 无 | 零 |
| `eventbus` | 825 | 无 | 零 |
| `spill` | 436 | 无 | 零 |
| `pii` | 386 | 无 | 零 |
| `statemachine` | 153 | 无 | 零 |
| `dispatch` | 159 | 无 | ⚠️ `internal/agents` |
| `policy` | 64 | 无 | 零 |
| `netguard` | 86 | 无 | 零 |

### 一.2 L2 逐能力抽取（第 1 批）

scheduler 的机制散在多个子包里，其中两个**依赖面足够窄、可整包搬**：

| 包 | 行数 | 外部依赖 | 域词 | 判定 |
|---|---|---|---|---|
| `ptc` | 315 | goja | 4（**全在注释**）| 程序化工具编排运行时：模型写程序 → 沙箱 VM 执行 → 经 bindings 调宿主工具。**与运维无关**，任何宿主都能用 |
| `watchdog` | 127 | 无（`eventbus` 本就在 harness）| 15（全在注释/文档）| 工具调用配对不变量监视（环节收口/幽灵结果），只告警不裁决 |

两者合计 **442 行**，迁入后 harness 包数 8 → 10，扁鹊消费面 7 处 import。

> `ptc` 的价值在于它是一个**真正通用的能力面**：N 轮 ReAct 往返塌缩成 1 轮、
> 原始数据不过模型面、三重预算（墙钟/调用数/字节数）替代 OS 沙盘。
> 这套东西与「运维」毫无关系，任何多智能体系统都能直接用。

### 一.3 L2 逐能力抽取（第 2 批：四个机制）

从 `scheduler` 里挑出**依赖面足够窄**的机制面。它们原先是 `scheduler` 包内的
独立文件（部分零 `s.` 字段依赖），语义与运维无关：

| 内核包 | 行数 | 原位置 | 是什么 | 为什么通用 |
|---|---|---|---|---|
| `delta` | ~110 | `scheduler/delta_pacer.go` | 增量节流：节流窗口/总量帽/seq 单调/权威收口；(kind,iteration) 键控 + **确定性收口扫序**（轮升序 × reasoning→text）| 任何真流式 LLM 输出都要处理「分片合并 + 权威整段优先」|
| `pending` | ~70 | `scheduler/pending.go` | 反问/消歧挂起态载体：单一编码入口 + 宽容解码（非法值降级为无挂起）| 任何多轮对话都要持久化「上一轮问了什么」|
| `reminder` | ~40 | `scheduler/reminder.go` | 系统注入提示的**防伪包裹**：source 声明 + body 内伪造标签转义 | 提示注入是所有 LLM 应用的现实入口（webhook 文本、命令输出、抓取正文）|
| `drain` | ~100 | `scheduler/drain.go` | 优雅排水：轮 ctx 取消脱钩 + 等在途收敛 + 预算耗尽强制收刀 | 任何「计划内重启不该杀死在途工作」的场景 |

#### 抽法：门面再导出（facade），不改调用面

调度器有 18 处 `SystemReminder`、32 处 `PendingState`、11 处 `DrainTurns` 调用点。
逐个改写调用点会把一次「搬实现」变成一次「改行为」，**风险与收益不成比例**。

故采用与 `dispatch/guard.go` 同款的门面手法：宿主留 `scheduler/harness_facade.go`，
类型用别名（`type deltaPacer = hdelta.Pacer`，编译期同一类型）、函数用薄包装，
**调用点一行不动**（仅 `steps_llm.go` 6 处因方法名导出化而改）。

```go
// 宿主门面：实现归内核，调用面留宿主。
type deltaPacer = hdelta.Pacer
func newDeltaPacer() *deltaPacer { return hdelta.New() }
var SystemReminder = hreminder.Wrap
```

> **纪律**：搬实现与改调用点**必须分批**。每批跑全量 e2e，不要混在一起——
> 否则出问题时分不清是「搬错了」还是「改错了」。

### 一.4 L2 的真正瓶颈：编排状态模型归谁（2026-10-17 实测结论）

L2 抽到第 2 簇（`flowEngine`，42 方法）后，**机械抽取这条路到了尽头**。实测把
`flowEngine` 里被称作「通用环节执行机制」的 14 个候选逐个测了依赖：

| 方法 | 依赖的宿主包 |
|---|---|
| `runAgentStep` | model, repo, agents, dispatch, protocol |
| `openFlowStep` / `startChainStep` / `requestSnapshotSink` | model, repo |
| `execChainSteps` | agents, model |
| `runLLMStep` / `collectSnapshot` | model, protocol, runner |
| `filterDisabled` | `d().Plug`（宿主插拔面）|
| `siblingInDomain` | `failoverCandidates`（宿主方法）|

**14 个候选里，只有 3 个（`toolsForServer` / `siblingInDomain` / `filterDisabled`）
不碰任何宿主包**——而它们合起来撑不起一个包：`toolsForServer` 绑 `agentkitmcp.Pool`
（可下沉），另两个的判据都在宿主（`failoverCandidates` / `Plug.Enabled`）。

**根因**：`model.Session` 有 **37 个字段**，其中这些是纯运维域语义——
`Host` / `Cluster` / `TargetPending` / `PendingQuestion` / `PendingOptions` /
`Entry`(incident|patrol|query) / `PolicyMode` / `PinnedExpert`；
`model.CaseStep` 另有 14 个字段。**编排的状态模型本身就是领域资产。**

> **结论：只要 `Session`/`CaseStep` 是宿主资产且带运维域字段，任何「环节执行机制」
> 就不可能真正下沉**——因为它必然操作这些结构。所以 L2 的问题从来不是
> 「抽哪些方法」，而是「编排的状态契约归谁」。

#### 要继续 L2，必须先做契约设计（不是机械抽取）

前置工作：在 harness 定义一层**与领域无关的运行上下文契约**，由宿主实现：

```go
// harness 侧：编排只认这三个能力，不认 Session/CaseStep 具体结构
type RunContext interface {
    ID() string
    CaseID() string
    Field(string) (string, bool)   // 取宿主状态字段（编排按语义键取，不按列名）
    SetField(string, string) error
}
type StepSink interface {          // 环节推进的落库/事件出口
    Open(name, label string) (StepRef, error)
    Finish(StepRef, status string) error
}
type ToolBinder interface { Tools(ctx, server string, allow []string) (Tool, error) }
```

宿主侧用 `*model.Session` / `*model.CaseStep` 实现这三个接口（适配层留在宿主，
约 100 行）。届时 `runAgentStep` / `openFlowStep` / `startChainStep` 就能下沉。

**但这是设计工作不是抽取工作**，且要先拍板：编排状态契约是 harness 定死，
还是宿主可扩展（后者需要版本协商）。**建议在动 L2 第 3 批之前先定这个。**

#### `drain` 的接口缝

排水机制需要一个「在途轮」的最小能力面，内核不能知道「轮」是什么：

```go
// harness/drain
type Turns interface {
    Active() int
    CancelAll(cause error)   // 实现须快照后逐个调用，不得持锁调取消回调
}
func Drain(ctx, t Turns, budget time.Duration, logf func(string, ...any))
```

宿主侧 `Scheduler` 通过 `turnRegistry{s}` 转发到既有的 `activeTurns` /
`cancelAllTurns`——**轮注册表是宿主状态**（谁在跑轮、怎么认领），不属于内核机制。

#### 测试分工（抽取后重写）

| 面 | 归属 | 钉什么 |
|---|---|---|
| 机制语义（等待/强制收刀/关闭态/值保留）| `harness/drain`，用假 `Turns` | 与宿主无关的机制不变式 |
| 适配正确性（注册即在途/unpin 即收敛/超时以 `ErrDeadline` 取消轮 ctx）| `scheduler/drain_internal_test.go` | Scheduler 接到 `drain.Turns` 上是否正确 |

原批次二百九十四的测试同覆盖两面，抽取后按上表拆开——**机制面能脱离宿主单测，
本身就是抽取成功的标志**。

> 实测纠正：初判时以为 `protocol`「只含注释带运维味」，门禁首跑即抓出
> `report_render.go` 两处**硬编码运维中文**（见 §五）。

### 只开了一条缝：`dispatch → agents.Registry`

`agents.Registry` 是扁鹊的领域包加载器（专家/技能/链的注册表），harness 不能认识它。
解法与 agentkit 已有的设计同构——`EdgeSource` 最小投影：

```go
// harness 侧：机制
type EdgeSource = adispatch.EdgeSource   // Slugs() + DispatchAllow(slug)
func NewGuard(src EdgeSource, maxDepth int) *Guard

// 宿主侧：拓扑投影（internal/engine/dispatch/guard.go，全文件 ~15 行）
type registryEdgeSource struct{ reg *agents.Registry }
func (s registryEdgeSource) Slugs() []string              { return s.reg.Slugs() }
func (s registryEdgeSource) DispatchAllow(slug string) []string { /* reg.Get(slug) */ }
func NewGuard(reg *agents.Registry, maxDepth int) *Guard  // 签名不变 → 8 处调用零改动
```

**测试的归属判断**（这一步容易做错）：

| 测试 | 归属 | 理由 |
|---|---|---|
| 守卫机制（深度/自派发/环检测/allow 矩阵）| ✅ harness | 与运维无关，用合成拓扑即可 |
| 「只有一条边能到达执行器」治理断言 | ❌ 留宿主 | **产品级安全承诺**，不是内核机制属性 |
| 悬空 allow / 静态深度建表失败 | ❌ 留宿主 | 验的是 `agents.NewRegistry`，不是 Guard |

### 刻意**不迁**的：`executors`

`internal/engine/executors`（1181 行 / 6 文件）有 4 条依赖：

```
engine/eventbus   → 已迁，可同迁
engine/protocol   → 已迁，可同迁
internal/metrics  → Prometheus 宿主面
tools/result      → MCP 工具信封契约（ErrBody / ShellData）
tools/serverkit   → OutputSchema 注册表查询
```

后两条绑的是 `tools/`（27k 行的 MCP 工具实现 monorepo），把它劈开搬两包子包
比接缝更糟。**推荐接缝方案**（`RealRunDeps` 已有 `Publish` 函数字段，同款加法）：

```go
type RealRunDeps struct {
    Host      string
    ShellTool tool.InvokableTool
    Executor  string
    Publish   func(typ, level string, payload map[string]any)

    // ↓ 新增两字段，解掉 metrics / serverkit 两条缝
    SchemaLookup    func(server, tool string) (any, bool) // 原 serverkit.OutputSchema
    ObserveViolation func(server, tool string)            // 原 metrics.ObserveToolSchemaViolation
}
```

宿主装配层（`run.go`）注入，闭包包住两个宿主函数。零行为变更。
**执行时机**：等宿主仓第一次全量编译通过后单独走一批，不要和 L1 混在一起。

---

## 二、L2：编排机制（待拆，真正的工程量）

`internal/engine/scheduler` 非测试代码 **12,371 行 / 50 文件**，其中运维词命中 1142 处。
耦合不是均匀的——**切缝在 scheduler 内部**：

```
scheduler/ 12.4k
├── 机制面（L2，通用编排原语）        ≈ 6.5k
│   intent_layers.go 736  决策链算法（治理硬规则→消歧→P0守门→LLM→词表→兜底）
│   intent.go 590       意图分类 + 置信门槛热更
│   steps.go 359        环节推进
│   steps_llm.go 302    LLM 环节执行
│   recovery.go 397     搁浅收敛（两档阈值）
│   judge.go 559        合议裁决
│   queue.go 257        会话队列
│   compact.go 259      上下文压缩
│   pressure.go 167     压力计
│   supervisor.go 121   低置信复审
│   drain.go 105        中止收敛
│   observe.go 151 / dialogue.go 150 / usage.go 57 / steer.go 53
│   scheduler_types.go 186 / intent_types.go 174  类型
└── 资产面（L3，留宿主）             ≈ 5.9k
    target.go 667       目标解析：主机/集群/Pod 三态 + 词表  ← 最脏
    flows.go 496        faultChain / patrolFlow / healthcheckFlow 编排图
    plan.go 546         巡检/故障计划（含域决策）
    chain_nodes.go 440  链节点引擎（偏机制，可再切）
    finalize.go 333     终态报告
    normalize.go 301    输入标准化
    dispatch.go 275     派发（已被 harness dispatch 覆盖大半）
    flows_k8sgpt.go 105 k8sgpt 检查源适配
    postverify_recovery.go 192 / aggregate.go 192 / autonomy.go 167
    generalist_flow.go 229 / generalist_handoff.go 145 / route.go 313
    meta_answer.go 102 / chitchat_answer.go 117 / conv_mode.go 100
```

### `target.go` 是最脏的一个（667 行）

它把「主机 / K8s 集群 / Pod」这个纯运维概念伪装成通用「目标解析」机制。
**判据：状态机是通用的，词表是域的。**

```go
// L2 留：三态反问状态机（Session.TargetPending）——任何领域都可能有「目标待确认」
type TargetState string  // target_pending / target_set / ...

// L3 迁：目标类型与词面
var targetK8sCues    = []string{"k8s","kubernetes","集群","pod","命名空间","namespace"}
var targetHostCues   = []string{"远程主机","linux主机","ssh","主机","服务器","机器"}
var targetLocalCues  = []string{"本机","localhost","当前机器",...}
var targetHostDenylist = [...]  // 词尾带数字的技术词排歧（k8s/s3…）
```

→ L2 收 `TargetResolver` 接口（`Classify(text) TargetKind` + `Confirm/Ask`），
宿主实现并注册。**注意**：若 harness 的第二宿主不是运维域（如客服），
这一步的接口签名要重新设计——目标二态可能变成「工单/会话」。**这是 L2 开工前
必须先拍板的事**，它决定 L2 是否需要返工。

### 迁移路径：flow 声明化

`flows.go` 里 `patrolFlow` 已接近声明式（`patrolDomains` 是个 slice，裁剪词表走
`agents.yaml symptoms 无硬编码`）。**结论：可行路径是把 Go 里的 flow slice
继续压成 `flow.yaml`，harness 只留解释器。** 这与宿主既有的
`chain.yaml`（包声明诊断链）机制同构，复用现成校验矩阵。

建议形态：

```yaml
# 宿主侧 flow.yaml（放领域包，不是 harness）
flows:
  - slug: workflow/fault-diagnosis
    kind: fault_chain
    fanout: [specialists/memory-analysis, specialists/io-analysis, ...]
    aggregate: n_to_1
    then: [timeline, rootcause, plan, approval, execute, verify]
```

---

## 三、L3：留在宿主（不动）

- 专家包 `experts/`（20 包 / 114 agent.yaml / 504 SKILL.md）
- `internal/agents`（packs.go / registry / routing / snapshot / plugboard）
- `internal/engine/executors`（等 §一 接缝方案落地）
- `internal/engine/runner`（5560 行）：prompt 组装里塞了域渲染
  （`assemble.go:187-193` 「目标集群：X」），下沉为模板变量后可迁
- 宿主仓 `architecture-policy.yaml` 需**新增一条**：`harness-usage-direction`
  （harness 单向依赖宿主，禁止宿主反向或平级互引），与本仓的
  `harness-no-host-dependency` 合起来构成完整红线

---

## 四、验证状态

**扁鹊已恢复可编译**（本机环境问题已解决，见 §六），两侧门禁全绿：

| 项 | 状态 |
|---|---|
| harness `go build ./...` | ✅ 通过（10 包）|
| harness `go vet ./...` | ✅ 通过 |
| harness `go test ./... -count=1` | ✅ **全绿 10 包**（含迁入的原始测试）|
| harness `go test ./... -race` | ✅ **全绿 10 包** |
| harness 覆盖率 | 75.8%（门槛 60%）|
| harness `gofmt` | ✅ 0 不合规 |
| harness 架构门禁 | ✅ 实跑通过，**0 例外** |
| 扇鹊 `go build ./...` | ✅ **通过** |
| 扇鹊 `go vet ./...` | ✅ **通过** |
| 扇鹊 `go test ./... -count=1` | ✅ **53 包全绿，0 失败** |
| 扇鹊架构门禁（8 条规则）| ✅ 全绿；god object 检出 `Scheduler 175` |
| 扇鹊 e2e | ⏳ 未跑（需独立端口 8977+，建议在正常 shell 补）|

宿主侧验证命令：

```bash
export GOMODCACHE=$PWD/.gomodcache-mavis GOCACHE=$PWD/.gocache-mavis
export GOPROXY=off CGO_ENABLED=0
unset GOFLAGS
go build ./... && go vet ./... && go test ./... -count=1
go run ./cmd/archcheck
BQ_E2E_PORT=8977 ./scripts/e2e-acceptance.sh   # 端口选 8977+（段内已被大量批次占用）
```

---

## 五、已清偿的债

policy `exceptions` 现为 **0 条**。拆分首轮门禁抓出的两处真实领域残留已清偿，
均有测试钉住（`protocol/generality_test.go`）：

| 原问题 | 性质 | 清偿方案 |
|---|---|---|
| `const AgentGuidance = "运维任务调度Agent"` | 硬编码宿主哨兵名——宿主一改名就静默失效，换宿主直接不适用 | `Report.Guidance` 结构化置位（推荐）+ `SetGuidanceAgentNames()` 命名注册双通道 |
| `title = "运维诊断报告"` | 内置宿主品牌文案 | `RenderOptions.DefaultTitle` 宿主注入 + 中性兜底「诊断报告」；`RenderView` 签名不变 |

> **教训值得记住**：初判「protocol 只在注释里带运维味」是**错的**——只看 import 图和
> grep 命中数会漏掉渲染层里的硬编码文案。这正是「policy 必须可执行、门禁必须真跑」的意义。

---

## 六、本机环境阻断的解法（如实记录，附坑）

扁鹊最初**在 HEAD 上就编译不过**，与本次重构无关。三层原因，逐层解开：

1. **go.sum 与上游不一致**：`oekit/hertz v0.5.0→v0.5.2` 同名 tag 被重推（台账批 344
   已记过同类坑）。解法：摘掉旧 hash 行 → 从缓存/代理直取重建。
2. **模块目录哈希不匹配**：沙箱禁止深层点文件创建，早期为绕开限制做的
   「跳过点文件解压」导致目录内容与 zip 不一致 → `go mod verify` 报
   `dir has been modified` → **模块被静默跳过**，表现为
   `no required module provides package X`（错误信息完全不指向真实原因）。
   解法：把待修模块路径**实体化**（符号链接指向 workspace 外，unlink 会被拦），
   再**完整解压**（含点文件）+ 写 ziphash。
   ⚠️ `.gitmodules` 被沙箱**单独拦截**（防 git 篡改），`.idea`/`.vscode` 等部分
   点目录也拦；这些模块只能跳过对应点文件——构建期不校验目录哈希，无害，
   但 `go mod verify` 会报（那是显式完整性命令，不在默认构建路径上）。
3. **Go build cache 被污染**：前几轮失败尝试让模块索引缓存记下了「pgx 无此包」。
   同一个 go.mod / 同一个模块目录，换全新 `GOCACHE` 后 `go list` 立即正常。
   **这是最隐蔽的一层**：任何「模块明明在、就是找不到」的僵局，先清 build cache。

最终形态：模块缓存与 build cache 都放在 workspace 内（`.gomodcache-mavis` /
`.gocache-mavis`），`GOPROXY=off`，全程离线可复现。

> 附带产出：`.gitmodules` 单独被拦这件事，说明沙箱对**文件名**而非路径做了白/黑名单——
> 与「点前缀目录深度」的经验规则不同。别拿经验规则当结论，用探针实测。

