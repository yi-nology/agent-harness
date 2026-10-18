# agent-harness · 多智能体编排通用内核

> 一句话：**编排是代码，LLM 只做单环节**——本仓只提供确定性编排的**机制面**，
> **不认识任何领域概念**。

从上游私有宿主仓（代号见内部文档）拆出的内核层，宿主业务不公开。定位对标同族的
`oagentkit`（Agent/LLM 编排工具链矩阵）与 `oekit`（基础库）：**独立发版、版本化引入、
零 replace**（宿主仓当前处于本地并行开发期，暂用 replace，见下）。

## 边界：本仓有什么、没有什么

```
✓ 有（机制，与领域无关）        ✗ 没有（资产，属于宿主/领域包）
─────────────────────────      ──────────────────────────────
protocol   统一报告协议          专家包 / 专家清单 / 技能
           宽容解析 + 结构化校验  诊断链 / 编排图（faultChain 等）
           白名单校验            运维词表（巡检域、K8s 词面…）
           Markdown 渲染         目标类型（主机 / 集群 / Pod）
statemachine 环节状态机          审批风险分级（restart/delete 四档）
eventbus   进程内有损广播        拓扑内容（谁允许派发谁）
policy     操作审计门            品牌文案（报告标题、引导 agent 名）
spill      输出预算溢出          事件持久化与表结构
budget     输入侧上下文预算      摘要生成的具体实现（走 Summarizer 接口）
offload    内联大对象外置        IM 通道 / 通知模板
toolfail   工具失败分类契约      HTTP 路由 / 鉴权
tooldisclose 渐进式工具披露 + 调用门禁    工具实现（MCP server）
pii        个人信息脱敏          工具目录内容（本包只管披露与门禁）
netguard   出站 SSRF 守卫        诊断链/巡检图（编排图属宿主）
dispatch   派发守卫（权限即拓扑） 摘要策略（何时压实属宿主）
ptc        程序化工具编排沙箱    环节语义（收口/幽灵结果的具体判据属宿主）
watchdog   工具调用配对监视      工具描述文案
checkpoint 编排续跑契约          快照的存储介质（SQLite / PG / 文件）
delta      增量节流 + 标签分流   模型供应商路由
```

判定口诀：**「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主。**

## 包清单

| 包 | 生产行数 | 外部依赖 | 职责 |
|---|---|---|---|
| `protocol` | 1735 | oekit/pkg、jsonschema | Report 契约 + 五级容错解析 + 白名单校验 + Markdown 渲染 |
| `dispatch` | 36 | oagentkit/agent | allow 矩阵物化 + 深度上限 + 自派发拒绝 + 环检测（拓扑经 `EdgeSource` 由宿主提供）|
| `eventbus` | 341 | oagentkit/runtime | 进程内有损广播（缓冲 chan）；持久化在发布点由宿主负责 |
| `policy` | 64 | cloudwego/eino、oagentkit/toolgate | 操作审计门：阈值只降不升、解析失败即拒 |
| `spill` | 251 | oekit/pkg | 输出预算溢出：提示契约 / 取回 / 按龄清理 |
| `pii` | 541 | oekit/pkg | 个人信息价值面脱敏（手机号 / 身份证四重校验）|
| `netguard` | 412 | oagentkit/guardx、oekit/pkg | 出站 URL 守卫（SSRF）：名字层（解析前按名拒）+ 解析后逐 IP 过 egress。**非纯 stdlib**；DNS 解析与拨号间的 rebinding 窗口见包头「已知局限」|
| `statemachine` | 79 | 无 | 环节状态机：条件迁移的纯函数件 |
| `contract` | 124 | 无 | 编排运行契约：编排引擎 ↔ 宿主状态的**唯一边界**（纯接口面，零实现）|
| `checkpoint` | 374 | oekit/pkg | 编排续跑契约：进度快照存取边界 + **「续跑而非重放」**的恢复判定（空洞不采信 / 终态不自动重开 / 序号单调闸）|
| `budget` | 756 | oekit/pkg | 上下文预算：分段计量 + 窗口钳位 + 压实计划骨架（三重空转守卫；摘要生成与原文落盘各留宿主接口）|
| `offload` | 589 | oekit/pkg | 内联大对象外置：sha 去重 + **两阶段**（首次标记、后续轮才替换占位）+ fail-soft（与 `spill` 是姊妹机制）|
| `tooldisclose` | 615 | oekit/pkg | 渐进式工具披露（确定性打分检索换完整 schema）+ 执行门禁（**未加载可重试 / 不在目录禁止绕过** 两类严格二分）|
| `toolfail` | 408 | 无 | 工具失败的错误分类契约：类别 → 可重试性/退避/需人工为**数据表**而非散落 if；不可归类一律不可重试 |
| `delta` | 241 | oagentkit/runtime | 增量节流：分片合并 + 权威整段优先 + 确定性收口扫序 |
| `pending` | 53 | 无 | 多轮挂起态载体：单一编码入口 + 宽容解码（非法值降级为无挂起）|
| `reminder` | 30 | 无 | 系统注入提示的防伪包裹：source 声明 + body 内伪造标签转义 |
| `drain` | 188 | oekit/logx、oekit/pkg | 优雅排水：轮 ctx 取消脱钩 + 等在途收敛 + 预算耗尽强制收刀（经 `Turns` 接口）|
| `ptc` | 321 | goja、oekit/concurrency、oekit/pkg | 程序化工具编排：模型写程序 → 沙箱 VM 执行 → 经 `bindings` 调宿主工具；三重预算（墙钟/调用数/字节数）替代 OS 沙盘。**N 轮 ReAct 往返塌缩成 1 轮，原始数据不过模型面** |
| `watchdog` | 127 | 无| 被动配对不变量监视：环节收口仍有未执行声明 / 结果无对应声明；**只告警不裁决**（热路径裁决在守卫侧）|

> 「生产行数」口径：`wc -l` 统计 `*.go` 且**不含** `_test.go`——包体量看的是要交付的实现，
> 不是它的测试。依赖列只列**仓外** import；「无」表示该包生产代码只用 stdlib。
> 本表每次改动实现后需同步，`make check` 不覆盖它。

> 「行数」列口径 = **生产代码 + 测试代码**总和，不是纯生产行数。
> 纯生产口径见 `go test -cover` 的 per-package 输出。

## 头号红线：不得长出领域知识

拆分做完后最现实的风险不是拆不动，而是**业务代码一年年渗回内核**，等发现时
harness 已经退化成「换了个名字的 monolith」。所以：

```bash
make architecture-check   # 域词污染 + 依赖方向，违反即非零退出
```

规则源 [`architecture-policy.yaml`](architecture-policy.yaml)，执行器 `cmd/archcheck`：

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-no-domain-vocabulary` | **error** | 生产代码里的运维领域词（故障链/巡检/告警/本机/内存分析…）|
| `harness-test-fixture-domain-vocabulary` | warn | 测试夹具用了真实运维词面（应改合成数据，不阻断）|
| `harness-no-host-dependency` | **error** | 任何对宿主仓的 import |
| `harness-no-domain-runtime-deps` | **error** | 对宿主仓 / 旧门面仓 / **已退役的 `agentkit` 门面** 的 import |

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

```bash
make build          # 编译 archcheck
make test           # 全量单测
make check          # fmt + vet + test + cover-gate + architecture-check
make architecture-check --report   # 看全量违规清单（默认前 10 条）
```

> macOS：本机 go build/test 一律带 `CGO_ENABLED=0`（新 SDK 27.0 与 CLT linker
> 不兼容 + 许可未接受），Makefile 已内置。

## 宿主接入

```go
// go.mod：发布后删除 replace，回到与 oagentkit 一致的版本化引入
require github.com/yi-nology/agent-harness v0.1.0
```

```go
import "github.com/yi-nology/agent-harness/protocol"

// 派发守卫：宿主提供拓扑，harness 提供机制
type myEdgeSource struct{ reg *myRegistry }
func (s myEdgeSource) Slugs() []string              { return s.reg.Slugs() }
func (s myEdgeSource) DispatchAllow(slug string) []string { return s.reg.Allow(slug) }

g := hdispatch.NewGuard(myEdgeSource{reg: reg}, maxDepth)
if err := g.Assert(caller, callee, depth); err != nil { /* 结构化 *DenyError */ }
```

宿主侧的安全不变量（如「全拓扑只有一条边能到达执行器」）的**断言测试应留在宿主仓**——
那是产品级承诺，不是内核机制属性。`dispatch/guard_test.go` 用合成拓扑测机制，
可复用于任何宿主。

## 演进纪律

- 新增能力前先问：**这是机制还是资产？** 资产进宿主/领域包，机制才进本仓。
- 新依赖方向先改 policy 再写代码；例外带注释写明拆除条件。
- 内核包规模远小于宿主，`max_file_lines: 400` 可直接阻断（宿主仓是报告口径）。
- 拆自宿主仓的代码历史见 `docs/SPLIT_PLAN.md`（L2/L3 待拆部分）。
