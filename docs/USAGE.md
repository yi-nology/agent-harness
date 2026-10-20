# agent-harness 使用指南

> 这份文档写给**要把 harness 接进自己产品的宿主开发者**。
>
> [README](../README.md) 回答「这是什么、为什么这么设计、边界纪律是什么」；
> 本文回答另一个问题——**「我怎么接、接错了会怎样」**。
>
> 本文所有 API 签名与行为均照抄源码，行号对应写稿时的 HEAD。
> 代码块是**集成片段**（用于说明装配顺序与调用面），不是可直接 `go run` 的完整程序；
> 完整可编译示例需要你在宿主工程里补齐存储、HTTP、模型客户端等上下文。

---

## 怎么读这份文档

**按需跳读，不必全接。** 22 个能力包分 10 组彼此独立的能力，典型接入顺序如下：

```
① 契约面        contract + statemachine    ← 你的状态长什么样（必接）
② 工具判定环    tooldisclose + guardset + toolfail  ← 工具怎么被管住（必接）
③ 报告协议      protocol                   ← 环节产出怎么变成结构化报告（必接）
④ 续跑契约      checkpoint + commitgate    ← 崩了怎么接着跑（强烈建议）
⑤ 上下文三件套  budget + spill + offload   ← 上下文怎么不被撑爆（建议）
⑥ 实时事件      eventbus + watchdog        ← 进度怎么给前端（可选）
⑦ 流式与退出    delta + drain + pending    ← 流式输出与优雅停机（可选）
⑧ 安全面        netguard + pii             ← 出站与个人信息（按需）
⑨ 程序化编排    ptc                        ← 多工具聚合场景（进阶）
⑩ 门面三件      dispatch + policy + reminder（薄转发，核心在 oagentkit）
```

每节的结构一致：**职责 → 最小集成片段 → 关键符号 → 坑**。
最后一节 [§6 坑速查表](#6-坑速查表) 是全文浓缩，接线完成后拿它自查一遍。

> **22 与 24 的口径差**：本文覆盖 **22 个能力包**（下面 10 组）。
> `architecture-policy.yaml` 的 `modules` 段登记 **24 个**——
> 多出的两个是 `toolname`（工具名归一的**单一真相**，被 `tooldisclose` 与 `ptc`
> 共用，不单独成组接入）与 `archcheck`（门禁工具本身，宿主不用引）。
> 两个数都对，说的不是一件事。

---

## 0. 五分钟速览

### 0.1 定位

**编排是代码，LLM 只做单环节。**

harness 提供的是确定性编排的**机制面**：环节状态怎么流转、工具调用怎么被判定、
产出怎么被结构化、崩了怎么续跑、上下文怎么计量。它**不认识任何领域概念**——
专家包、诊断链、运维词表、拓扑内容、品牌文案全部属于宿主。

判定口诀：**「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主，不属于本仓。**

### 0.2 你会拿到什么

| 你要解决的事 | 用哪个包 | 你要提供什么 |
|---|---|---|
| 会话/环节状态存哪、怎么流转 | `contract` + `statemachine` | `RunContext` / `StepSink` 两个接口的实现 |
| 模型输出怎么变成可信结构 | `protocol` | agent 名注册（可选）、报告标题（可选） |
| 工具怎么被限制 | `tooldisclose` + `guardset` + `policy` | 工具目录、宿主守卫、拓扑 `EdgeSource` |
| 崩了怎么接着跑 | `checkpoint` + `commitgate` | `StoreCAS` 实现、终态词表注入 |
| 上下文怎么不被撑爆 | `budget` + `spill` + `offload` | `Summarizer` / `Archive` / `Store` 三个接口 |
| 进度怎么推给前端 | `eventbus` + `watchdog` | 落库 + 广播、告警发布出口 |
| 出站请求怎么防 SSRF | `netguard` | 挂在自己的 `DialContext` 上 |
| 个人信息怎么脱敏 | `pii` | 阻断决策（内核只回报信号） |
| 多工具聚合怎么省往返 | `ptc` | 工具绑定、判定环切片 |

### 0.3 三个必须先知道的约束

**① 内核只判定，不处置。**
判定语义归内核，判定依据与处置归宿主。典型表现：`pii.Apply` 检出敏感信息时返回
`(文本, true)`——`true` 表示「宿主应当阻断」，但**阻断动作由你执行**，内核不抛错、
不中断你的控制流。`toolfail` 同理：分类完了，怎么恢复是你的事。

**② 门面的门永远朝外。**
`dispatch` / `policy` / `reminder` 是**门面**，只允许类型别名、常量别名、
单条上游转发函数。核心分别落在 `oagentkit/agent/dispatch`、
`oagentkit/toolgate/policy`、`oagentkit/guardx/injectband`。
在这三个包里加一行真逻辑会被 `harness-facade-stays-facade` 门禁拦下（error 级）。

**③ 本仓不 import 你，你 import 本仓。**
方向是单向的硬约束（`harness-no-host-dependency` 门禁）。harness 需要宿主能力时
定义接口由宿主实现，从不反过来依赖。

---

## 1. 引入

```bash
# 模块路径
module github.com/yi-nology/agent-harness
```

```bash
# 按最新发布版引入
go get github.com/yi-nology/agent-harness@latest

# 或钉到具体版本（查已发布版本：git ls-remote --tags origin）
go get github.com/yi-nology/agent-harness@v0.6.4
```

> **版本号为什么写 `@latest` 而不是钉死一个数字**：本文写稿时是 v0.6.4，
> 而它会随发版变旧。钉死的数字在几轮发版后就是**过期指引**——
> 按它引入会拿到一个缺了好几轮能力的旧版本，且没有报错。
> `go get @latest` 不会过期；要复现特定版本再显式钉。

**零 `replace`。** 本仓 go.mod 不得含 `replace`（`make check` 的 `modreplace` 门禁）。
本地需要同时改上游仓时用 `make devlink` 走 `go.work`，见 [§5](#5-本地多仓并行开发)。

依赖全部为 public 模块：`github.com/oagentkit/*` 与 `github.com/oekit/*`。
**按包引入**即可——`contract` / `statemachine` 是零外部依赖的叶子包，
只想拿状态机不需要拖进 goja、jsonschema 这些。

```go
import "github.com/yi-nology/agent-harness/contract"
import "github.com/yi-nology/agent-harness/statemachine"
```

---

## 2. 心智模型：为什么接进来是这些形状

理解三条设计取舍，后面的 API 形状就都不奇怪了。

### 2.1 编排语义定死，领域字段开放

`contract.RunContext` 是**混合**设计：编排语义字段（`ID` / `Status` / `CurrentStep`）
定死在接口上，因为编排引擎的算法只依赖这些；领域状态走开放键
（`Field(key)` / `SetField(key, value)`），内核不解释、不校验、不假设它存在。

这样约 80% 的编排逻辑可下沉，剩下 20% 留在宿主适配层。
纯开放键契约太松、两边必然漂移；纯定死则每加一个领域概念都要改内核。

> 取不到的键返回 `(false)` 由调用方降级；`SetField` 遇未知键**返回错误**而不是静默吞——
> 宿主可以 fail-closed 拒绝，但不能悄悄把数据丢了。

### 2.2 续跑而非重放

「编排是代码」的必然推论是编排过程可被重演。既然可重演，恢复方式就该是**可机械判定**
的，而不是留给宿主在崩溃那一刻临场决定——临场决定必然选最坏的那个
（要么盲目全量重跑、要么盲目跳过）。

所以 `checkpoint.BuildPlan` 是纯函数，把恢复判定做成内核机制。
详见 [§3.6](#36-断点续跑checkpoint--commitgate)。

### 2.3 内部一致优于外部通用

同一件事的多个维度分列而非合并，因为合并会丢信息：

- `toolfail`：**重发安全**与**重试价值**是两个正交维度——超时值得重试，但服务端可能
  已执行，原样重发就是重复施加副作用。
- `budget`：**毛移除量**（扣摘要之前的量）与**净移除量**（扣掉摘要回填后的量）分开。
- `guardset`：**多条拒绝合成一条**时，可重试取「全部可重试才算」，不是「任一」。

---

## 3. 接入路线图

### 3.1 契约面：`contract` + `statemachine`

**这一层是你与内核之间的唯一边界。** 编排引擎不碰存储、不碰数据库——
所有环节状态的读写都经 `StepSink` 出口。

```go
package myapp

import (
	"context"

	"github.com/yi-nology/agent-harness/contract"
)

// ① 会话运行上下文：编排语义字段定死，领域状态走开放键。
type myRun struct {
	id     string
	caseID string

	status string
	step   string

	fields map[string]string
}

func (r *myRun) ID() string                 { return r.id }
func (r *myRun) CaseID() string             { return r.caseID }
func (r *myRun) Status() string             { return r.status }
func (r *myRun) SetStatus(s string) error   { r.status = s; return nil }
func (r *myRun) CurrentStep() string        { return r.step }
func (r *myRun) SetCurrentStep(s string) error { r.step = s; return nil }
func (r *myRun) Field(k string) (string, bool) {
	v, ok := r.fields[k]
	return v, ok
}
func (r *myRun) SetField(k, v string) error {
	r.fields[k] = v
	return nil
}

// ② 环节推进出口：编排只说「开一个环节 / 收口」，落库由你做。
type myStepSink struct{ repo *Repo }

func (s myStepSink) Open(name, label string) (contract.StepRef, error) {
	// 建行 + 状态迁移到 running，两件事你都要做
	return s.repo.insertStep(name, label)
}

func (s myStepSink) Finish(ref contract.StepRef, status string) error {
	return s.repo.finishStep(ref.ID, status)
}

// ③ 环节状态机：纯谓词件，先快速失败，DB 侧仍有条件 UPDATE 兜底。
func advance(from, to statemachine.StepStatus) error {
	if err := statemachine.MustTransition(from, to); err != nil {
		return err // 非法转移，repo 条件 UPDATE 前快速失败
	}
	// ...
	return nil
}
```

**关键符号**

| 符号 | 说明 |
|---|---|
| `RunContext` | 会话运行上下文；实现须**并发安全**（事件回调与主流程共享同一实例） |
| `StepSink` | 环节推进出口：`Open` / `Finish` |
| `StepRef` | `{ID uint64; Name string}`，宿主分配的稳定标识 |
| `ToolBinder` / `Tools` | 工具面绑定出口——**不关心工具从哪来**（MCP 池 / 静态注册表 / 测试替身同接口） |
| `Clock` | 时钟注入点，测试可注入确定性时钟 |
| `FieldQueuedInput` / `FieldPendingAsk` / `FieldTargetSummary` | 内核**建议**的语义键，不强制 |

**坑**

- **`ToolBinder.Tools` 的 server 级失败必须显式报错**，不得静默返回空面。空面会让
  编排把「没工具」误判成「工具失败」——两者处置完全不同。
- **`statemachine` 的终态包含 `interrupted`**：环节因用户取消/会话超时/进程中断而中止，
  **区别于失败**（执行了但失败）。下一轮对话历史如实携带，LLM 自行决定重做或跳过。
- **`RequiresApproval(TypePlan)` 与 `TypeExecute` 为真**；`readonly` 裁剪审批。
- `statemachine` 零外部依赖，纯 stdlib。

---

### 3.2 报告协议：`protocol`

模型输出的 JSON 不可信。`protocol` 给的是一条**五级容错解析 + 白名单校验 + 渲染**
的流水线，核心思路是「能修就修，但该拦的拦死」。

```go
package myapp

import (
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/protocol"
)

func toReport(raw string) (*protocol.Report, error) {
	// ① 解析：宿主解析 LLM 产出**必须用 ParseLLMReport**
	r, err := protocol.ParseLLMReport(raw)
	if errors.Is(err, protocol.ErrNotReport) {
		return nil, err // 降级读 markdown 主体，不阻塞聚合
	}
	if err != nil {
		return nil, err
	}

	// ② 白名单校验：返回 warn 列表 + critical 拦截
	warns, err := r.Validate()
	if err != nil {
		// errors.Is(err, protocol.ErrViolation) → 整份报告被拦
		return nil, err
	}
	for _, w := range warns {
		metrics.Counter("report.warn", w) // warn 只留痕，不拦截
	}

	// ③ 渲染：标题走注入，内核只留中性兜底
	view := protocol.RenderViewWith(r, protocol.ViewSummary,
		protocol.RenderOptions{}.WithTitle("你的品牌标题"))
	send(view)

	return r, nil
}
```

**关键符号**

| 符号 | 签名 |
|---|---|
| `ParseLLMReport` | `func ParseLLMReport(raw string) (*Report, error)` |
| `ParseReport` | `func ParseReport(raw string) (*Report, error)` |
| `(*Report).Validate` | `func (r *Report) Validate() ([]string, error)` |
| `Render` / `RenderView` / `RenderViewWith` | 三档渲染，`ViewFull` / `ViewSummary` |
| `RenderOptions` | `{DefaultTitle string}` + `.WithTitle(...)` |
| `SetGuidanceAgentNames` | `func SetGuidanceAgentNames(names ...string)` |
| `PayloadCandidates` / `UnwrapMCPText` | MCP 信封候选提取 |
| `ValidateStructured` | `func ValidateStructured(schemaSrc, envelope string) error` |
| `AssignEvidenceIDs` / `CheckEvidenceRefs` | 证据编号与引用校验 |
| `ErrNotReport` / `ErrViolation` | 两个哨兵，用 `errors.Is` 判 |

**坑**

- **`ParseReport` 与 `ParseLLMReport` 信任级别不同，不是别名。**
  前者**保留**引擎侧字段（`Followups` / `Meta` / `Profile` / `AwaitingUser` / `Guidance`），
  是**落库往返入口**；后者剥离这五个字段。**宿主解析 LLM 产出必须用 `ParseLLMReport`**——
  走 `ParseReport` 等于把呈现权交给模型。
- **解析输入有 2 MiB 上限，超限直接判非报告**（`ErrNotReport`），不做截断。
  这条曾经写成「截断到 2 MiB 再解析」，而截断会让提取通道把「报告对象 + 巨量尾巴」
  里的对象原样捞出并丢弃其余——**3.4 MB 输入实测以 `confidence=high` 解析成功**，
  上限等于没有，且残缺内容未被降置信。已修为 fail-closed，
  由 `protocol.TestOversizeInputRejectedNotTruncated` 反向验证钉住。
- **`Validate` 的 `[]string` 是 warn 级，不拦截**；只有 critical 才 `ErrViolation`。
  snippet 超长**不截断**——证据保真。
- **`steps` 的执行契约是 critical**：以 `{` 或 `【` 开头的步骤条目被整份拦截，
  因为执行器会把每条当 shell 命令跑，中文/JSON 形态会「假成功」（exit=0 但什么都没执行）。
- **内核不认识任何具体 agent 名**。引导声位判定二选一：`Report.Guidance = true`（推荐）
  或装配期 `SetGuidanceAgentNames(...)`。早期版本硬编码过中文哨兵名，宿主改名即静默失效。
- **「证据要点」这个标题字面不可单边改**——前端折叠按它匹配，改字面即破坏折叠。
- **severity 序表刻意不导出**。导出的 map 可被就地改写，
  `SeverityRank["critical"] = 0` 会静默把最高危降级为「未评估」。
  读取一律走 `SeverityRankOf` / `MaxSeverity` / `IsSeverityValid`。
- **经修复通道救回的报告 `Confidence` 被钳为 low**——下游 `needs_review` 语义据此承接。
- `ValidateStructured` 对「未声明 schema」与「旧 server 无 structuredContent」双写兼容，
  **直接放行**；校验失败是漂移信号，不阻断主流程。

---

### 3.3 工具判定环：`tooldisclose` + `guardset` + `toolfail`

这是 harness 里最核心的一组：**任何一次工具调用，都必须过同一套判定环。**

#### 3.3.1 为什么不是「一条链逐个过」

`invoke.Chain` 的语义是排成一队逐个过、第一个拒就收口——顺序即语义，
但**后面的守卫永远没机会说话**。模型一次只听得到第一个理由，于是它改掉那个理由、
重试、再撞上下一个，如此往复若干轮。

`guardset` 补的正是这一面：**一组相互独立的判定一并求值，多条拒绝合成一条**。
二者叠加而非取代——`guardset.Set` 把自己接成 `invoke.Chain` 上的**一环**，
链照旧短路，只是这一环内部已经把该说的都说了。

#### 3.3.2 渐进式披露：`tooldisclose`

首轮只给「名字 + 描述」的**退化占位**，模型用关键词检索换完整 schema。
披露门与检索同包——因为渐进披露制造了「模型凭占位猜出一个调用」这一新失败形态，
必须在同一处判。

```go
package myapp

import (
	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/guardset"
	"github.com/yi-nology/agent-harness/tooldisclose"
)

func buildGate() (invoke.Stage, *tooldisclose.Catalog) {
	// ① 目录：宿主登记全部工具。跨轮持有 Catalog 是宿主的责任
	//    （本包没有隐式请求作用域），按会话键持有、会话结束释放。
	cat := tooldisclose.NewCatalog([]tooldisclose.Tool{
		{
			Name:        "get_logs",
			Description: "读取目标主机近期日志",
			Schema: tooldisclose.Schema{
				Type: "object",
				Properties: map[string]tooldisclose.Param{
					"host":  {Type: "string", Description: "目标主机"},
					"count": {Type: "integer", Description: "返回条数"},
				},
				Required: []string{"host"},
			},
		},
	})

	// ② 守卫集：披露门 + 宿主自己的守卫，一并求值
	set, err := guardset.New(
		guardset.Guard{
			Name: "tool_disclosure",
			Rank: 10,
			Check: func(ctx context.Context, c invoke.Call) invoke.Verdict {
				if e := cat.Check(c.Name); e != nil {
					return invoke.Verdict{
						Allow:     false,
						Reason:    tooldisclose.Guidance(e), // 给模型看，要能照着做
						Retryable: tooldisclose.Retryable(e),
					}
				}
				return invoke.Verdict{Allow: true}
			},
		},
		guardset.Guard{
			Name:  "quota",
			Rank:  20,
			Check: quotaCheck, // 你自己的配额守卫
		},
	)
	if err != nil {
		panic(err) // 空集合 / 空名 / 重名都在这里失败
	}

	// ③ 单一 Stage 出口。两个变体的差别只在 Reason 的用途：
	//    Stage            → Reason = out.Reason()  （给人看，要全且准）
	//    StageWithGuidance→ Reason = out.Guidance()（给模型看，要能照着做）
	return guardset.StageWithGuidance(set), cat
}
```

**判定环的严格二分**——`Gate.Check` 三种结局互不代偿：

| 情形 | 结局 | 可重试 | 附带动作 |
|---|---|---|---|
| 已加载 | `nil` | — | — |
| 在目录内但未加载 | `*GateError`（`ErrNotLoaded`） | ✅ | **顺手 `Load` 激活** |
| 不在目录内 | `*GateError`（`ErrNotInCatalog`） | ❌ | 就地终止 |

「拒绝即激活、重试不绕路」：第一次拒绝已经把工具激活了，同参重试必成。
模型不需要换参数、换措辞再来一遍。

**关键符号**

| 符号 | 签名 |
|---|---|
| `NewCatalog` / `NewCatalogWithLimit` | `func NewCatalog(tools []Tool) *Catalog` |
| `Catalog.Load` / `ExposeAll` / `Search` | `Load` 是披露的唯一写入口 |
| `NewGate` / `Gate.Check` | 三种结局见上表 |
| `Retryable` / `Guidance` | `func Retryable(err error) bool` / `func Guidance(err error) string` |
| `Stage` / `StageName` | `func Stage(cat *Catalog) invoke.Stage` / `const StageName = "tool_disclosure"` |
| `SignatureOf` / `ErrSchemaNotDerivable` | 从 schema 派生调用形态，如 `get_logs(host, count?)`（函数名是**净化后**的，见下） |
| `guardset.New` / `Set.Evaluate` / `Outcome.Guidance` | 一并求值 + 合成拒绝 |

**坑**

- **目录成员构造后不变**，没有删除/新增入口。要增删工具必须重建 `Catalog`。
- **重名静默去重，first wins**；要发现冲突得自己在调用本包前去重。空名工具构造期被丢弃。
- **描述按 rune 截到 320 字符，在存进目录前截断**——不是渲染时截，
  否则检索打分读全文、模型看摘要。
- **`Search` 无命中返回空，不兜底给「随便几个」；且只排序不激活**——
  检索顺手激活会把刚省下的上下文吐回去。
- **`SignatureOf` 派生不出来就报错，不硬凑**。退化占位 schema（Properties 为 nil）、
  参数名非合法 JS 标识符、`Required` 点名了不存在的参数，三种都返回 `ErrSchemaNotDerivable`。
  宁可装配失败，也不让「看起来能用、实际不对」的东西进模型面前。
- **`SignatureOf` 里的函数名是净化后的名字，不是目录里的原名**。
  程序里真正挂上去的是 `host.<净化名>`（`get-logs` 挂上去叫 `host.get_logs`），
  所以形态串里的函数名也必须是净化后的那个，否则 ptc 装配期直接拒
  （`binding_signature`）。两侧共用 `toolname` 包（`Normalize`/`IsJSIdent`），
  规则只有一份，不会漂移。**别自己手写形态串**——手写就要照净化后的名字写。
- **`Stage(nil)` 按「不在目录内」处理**（fail-closed）。
- **`guardset` 合成取保守侧：全部可重试才算可重试**。只要有一条拒绝不可恢复，
  重试整个调用就绝无可能成功；取「任一」会把永久性拒绝洗白成可重试。
- **`guardset` 的 `Check` 不得依赖其他 Guard 的结论**，否则一并求值就改变单条判定的语义。
  需要有先后的守卫请直接排进 `invoke.Chain`。
- **`Check` 为 nil、panic、或拒绝却不说理由，一律按拒绝收口（fail-closed）**，
  且 `Retryable=false`——与「正常判定得出的拒绝」区分开，两者处置不同。
- **拒绝走 `(Verdict, nil)`，不返回 error**——拒绝是正常判定结论，
  error 在链上会被归口成「这一环自己坏了」。

#### 3.3.3 失败分类：`toolfail`

「这个失败值不值得重试」的**机械判据**。类别 → 可重试性/退避/需人工/重发安全/模型指引
是**数据表**而非散落的 if。

```go
package myapp

import (
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/toolfail"
)

// 工具侧在知道类别的那一刻调 ClassifyAs，不要指望 Classify 猜出来
func callTool(...) error {
	switch {
	case errPortInvalid(err):
		return toolfail.ClassifyAs(toolfail.ClassInvalidArgument, err)
	case errUpstreamReset(err):
		return toolfail.New(toolfail.ClassTransient, "upstream_reset", "上游连接重置")
	default:
		return err
	}
}

// 模型侧：Guidance() 是唯一消费方
func advise(err error) string {
	f, ok := toolfail.As(err)
	if !ok {
		return toolfail.Guidance(err) // 不可归类 → 不可重试，fail-closed
	}
	// 正交决策：
	//   f.Retryable()      —— 重试有没有收益
	//   f.SafeToResubmit() —— 原样重发安不安全
	//   f.NeedsOperator    —— 要不要叫人
	return f.Guidance()
}
```

**两个正交维度**：`Retryable`（重试有没有收益）与 `SafeToResubmit`（原样重发安不安全）。
超时值得重试，但服务端可能已执行，原样重发就把一次调用变成两次副作用。
`Idempotency` 用三态而非 bool——「没想过」与「确实不幂等」不被压成同一个 false。

**坑**

- **不可归类一律不可重试**（fail-closed）。宿主认为某类可重试必须显式归类，
  不能让内核替它乐观。
- **只有 `ClassRateLimited` 的 `SafeToResubmit` 为真**——唯一能证明服务端未受理本次调用的类别。
  超时类不给真。
- **`Remediation` 不得自带重试结论**（不要写「重试」「等待 N 秒」「需要人工」）——
  那是 `Guidance` 的机械判定，写在这里会与 Guidance 直接矛盾。
  取值也不得含具体标识、数值与业务词面——它会原样进入提示词。
- **`Code` 不得写入每次都变的内容**（数值、标识、耗时），它要跨版本稳定用于聚合。
- **`RetryableReporter` 的实现方必须 nil-safe**：链上可能挂 typed-nil 指针。

---

### 3.4 程序化工具编排：`ptc`

模型写一段程序，进程内 goja VM 执行，程序内经 bindings 调宿主工具，
**只把归并后的小结果带回上下文**。

> **N 轮 ReAct 往返塌缩成 1 轮，原始数据不过模型面。**
> 典型场景：要查 20 台机器的指标，ReAct 要 20 轮往返、每次都过模型面；
> ptc 一轮内循环完，只把「20 台里 3 台异常」带回去。

```go
package myapp

import (
	"context"
	"time"

	"github.com/yi-nology/agent-harness/ptc"
)

func runProgram(ctx context.Context, stages []invoke.Stage) ptc.Result {
	bindings := []ptc.Binding{
		{
			// 带 Signature：模型按位置传参，ptc 负责打包成 JSON 再交给 Call
			// 形态里的函数名必须是**净化后**的名字（= 绑定名经 toolname.Normalize），
			// 否则装配期被拒（binding_signature）。最好用 tooldisclose.SignatureOf 派生。
			Name:      "get_logs",
			Signature: "get_logs(host, count?)", // 可选形参以尾部 ? 标记
			Call: func(ctx context.Context, argsJSON string) (string, error) {
				return callHostTool(ctx, "get_logs", argsJSON) // 回原文，不截断
			},
		},
		{
			// 无 Signature：退化为 host.<name>(argsJSON)
			Name: "get_metrics",
			Call: func(ctx context.Context, argsJSON string) (string, error) {
				return callHostTool(ctx, "get_metrics", argsJSON)
			},
		},
	}

	program := `
const out = [];
for (const h of ["h1", "h2", "h3"]) {
  const m = JSON.parse(await host.get_metrics('{"host":"' + h + '"}'));
  if (m.cpu > 0.9) out.push({h, m});
}
return out;
`

	return ptc.Run(ctx, program, bindings, ptc.Options{
		Timeout:  10 * time.Second,
		MaxCalls: 20,
		MaxBytes: 64 << 10,
		Stages:   stages, // ★ 用与普通工具调用**同一条链**的 Stage 切片
	})
}
```

> ★ 这一行是 `ptc` 最重要的接入纪律：程序内的 `host.<tool>()` 与普通工具调用
> 走同一套 Stage 判定环。不接的话，本包就成了整条链上唯一绕过所有门禁的路径。

**三重预算替代 OS 沙盘**：goja 进程内 VM（纯 Go 零 CGO，无 Node 分发面）+
保守 JS 子集（无 fetch/fs/require，能力只来自 bindings）+ 墙钟 deadline（`vm.Interrupt`）、
绑定调用数上限（`MaxCalls`）、绑定输出累计字节上限（`MaxBytes`）。

**`ErrorKind` 取值全集**：`""`（成功）/ `deadline` / `budget_calls` / `budget_bytes` /
`runtime` / `pending` / `bad_result` / `syntax` / `binding_collision` /
`binding_signature` / `stage_denied` / `stage_failed`。

`stage_denied` 与 `stage_failed` 分开不是洁癖：前者是**正常裁决**（程序改不掉），
后者是**宿主门禁自己坏了**（该修守卫代码，不是程序）。

**坑**

- **撞名即装配期拒，不进 VM**：`toolname.Normalize` 把 `-` `.` 等映射成 `_`，`get-logs` 与
  `get_logs` 净化后同名。放任覆盖会让 `host.get_logs()` 执行的是**另一个工具**——无报错、
  无日志，CallLog 还照常记着 B 的账。规整规则只有一份（`toolname` 包），
  **别在宿主侧另写一套净化逻辑**——两套规则漂移开的形态是「目录里派得出、程序里认不出」。
- **可选形参必须排在必填之后**。否则中间出现可选、后面又有必填时，
  模型为了传后面的必填项就得先把前面的空位填上——比直接判非法更坏。
- **可选槽位只在真的给了值时才写入 JSON**。写进去一个 `undefined` 到 JSON 里就是 `null`，
  宿主收到 `{"count":null}` 与「没给 count」是两件事。
- **被判定环拒绝的调用同样计数**（`res.Calls++`）：程序可以 try/catch 掉这次错误再循环，
  若不计预算，一个被拒的工具就能被无限重试，`MaxCalls` 形同虚设。
- **`Stages` 为 nil 时不接任何判定**，行为与引入 Stages 之前逐字一致，
  但那条路径**不带门禁**——接入方须自行确认这是有意的。
- **运行期异常的 `Location` 经常是空的**，这是工具能力边界：程序体是 async IIFE，
  函数体异常变成 Promise rejection，goja 未提供「rejection 处回溯调用栈」的公开面。
  拿不到就零值，**不猜**。
- **`await` 永不落定的 Promise → `ErrorKind=pending`**（VM 无定时器、绑定均为同步调用）。
- **审批挂起 = 终止 + 已执行步骤留痕**：`vm.Interrupt` 打不断阻塞中的 Go 绑定调用，
  中断驱动先 cancel 运行期 ctx 再 Interrupt。
- **出站守卫（netguard）不在判定环上，这是有意的**：判定环判「这次调用要不要放行」，
  netguard 判「这个 URL 能不能发出去」，是两个执行点。
  分工前提是沙箱性质——**谁给 VM 加了 fetch，谁就得重新回答 netguard 该接在哪**。

---

### 3.5 上下文三件套：`budget` + `spill` + `offload`

三者的分工泾渭分明，接线时别混：

| 包 | 管什么 | 时机 | 方向 |
|---|---|---|---|
| `budget` | 输入面计量与压实 | 每轮前 | 输入侧 |
| `offload` | 二进制本体躺在消息里 | 每轮装配 | 输入侧 |
| `spill` | 输出超预算被截断 | 产出时 | 输出侧 |

#### 3.5.1 `budget`：分段计量 + 压实计划

分段计量不是为了好看——**只给总数无法区分「工具面膨胀」与「历史膨胀」**，
而两者的处置完全不同。

```go
package myapp

import (
	"context"
	"strings"

	"github.com/yi-nology/agent-harness/budget"
)

// ① 宿主实现：摘要是一次 LLM 调用，属模型面不属编排面
type llmSummarizer struct{}

func (llmSummarizer) Summarize(ctx context.Context, leading []budget.Item) (budget.Summary, error) {
	var sb strings.Builder
	for _, it := range leading {
		sb.WriteString(it.ID + "；")
	}
	text := sb.String()
	return budget.Summary{Text: text, Tokens: len([]rune(text)) / 2}, nil
}

// ② 宿主实现：原文落盘，返回取回句柄
type blobArchive struct{}

func (blobArchive) Archive(ctx context.Context, items []budget.Item) (budget.Locator, error) {
	return budget.Locator("s3://audit/conv-42/compaction-3.json"), nil
}

func maybeCompact(ctx context.Context, m *budget.Meter, limit int,
	items []budget.Item) error {
	// ③ 压力档：tight 75% / critical 90%（到达即进档，宁可早一档提示）
	if budget.Classify(m.Total(), limit) != budget.PressureCritical {
		return nil
	}

	// ④ 计划是纯计算，无 I/O；三条空操作守卫返回「原因」而非 bool
	plan, err := budget.BuildPlan(budget.PlanInput{Items: len(items), KeepTail: 4})
	if err != nil {
		return nil // 条数不够等，本轮不压
	}

	res, err := budget.Apply(ctx, plan,
		items[:plan.SummarizeItems], items[plan.KeepTailItems:],
		blobArchive{}, llmSummarizer{})
	if err != nil {
		return err
	}

	// ⑤ 压实不产生模型调用，所以必须显式把账改回来
	m.Shrink(budget.SegmentConversation,
		budget.NetRemovedTokens(res.GrossRemovedTokens, res.Summary.Tokens))
	return nil
}
```

**坑**

- **毛/净移除量必须分开取**。`GrossRemovedTokens` 是扣摘要**之前**的毛量；
  按名字对齐参考实现的同名字段会**静默错账**，方向恰好是「看起来更省了」。
  要「减掉多少」请直接用 `NetRemovedTokens`（钳位在 0）。
- **分段估算永不回填 provider 报数**。provider 报的是总数，回填到分段等于让
  tokenizer 误差披上分段精确度的外衣，而压实计划恰按段分配裁剪——喂残差等于喂假账。
- **`MaxInput` 无预留或窗口未知时原样返回 `baseInput`**——扣减纯属内核自作主张。
- **`Classify` 在 `capacity<=0` 时返回 `PressureCritical`**：
  「不知道会不会溢出」与「不会溢出」行为完全相反，不得声称余量充足。
- **归档必须先于摘要**：摘要失败时原文已落盘可直接重试（重跑归档幂等）；
  反序可能出现「摘要已丢弃原文、摘要本身又没拿到」的净损失。
- **`Apply` 的三条计划校验全在任何副作用之前**——被拒的调用不得留下任何副作用。

#### 3.5.2 `spill`：输出溢出存档

**本包不做写入**，只提供清单、分页取回与写侧清理。

```go
package myapp

import (
	"github.com/yi-nology/agent-harness/spill"
)

func onTruncated(root, sessionID string, omitted, total int) string {
	name := "out-0007.bin"
	if err := writeSpillFile(root, sessionID, name, fullOutput); err != nil {
		return "（输出过长且存档失败）" // fail-soft：不让存档失败阻断主流程
	}
	// 提示文案是双侧钉档契约：前端按同一契约解析，改文案须同步前端与其单测
	return spill.TruncHint(spill.TruncNote{
		Omit: omitted, Total: total, Retrievable: true, Name: name,
	})
}

// 模型侧分页取回，防「分页取回」退化成「全文重塞」
func retrieve(root, sessionID, hint string) {
	locator, truncated := spill.ParseHint(hint)
	if !truncated {
		return
	}
	pg, err := spill.ReadPage(root, sessionID, locator, 0, spill.DefaultLimit)
	_ = pg, err
}
```

**坑**

- **会话隔离三重守卫，fail-closed**：`sanitizeName`（只允许单段裸文件名）→
  `sessionDir`（`root` 空报「未装配」、`sessionID` 空报「无法归属会话」）→ 前缀校验。
  任一不过即 `ErrBadName` 或显式 error，**不回退到宽松读**。
- **`List` 无存档返回 `[]Item{}, nil`**，不是错误——宿主不必判空。
- **`Prune` 是 best-effort**：无返回值、清理失败不影响本次存档。
- `ReadPage` 边界钳位：`off<0` 视作 0、`off>total` 收到 `total`、`limit<=0` 用 `DefaultLimit`。
- **`List` 返回 mtime 降序**，`spill` 侧有 `Prune` 而 `offload` 侧没有（回收由宿主决定）。

#### 3.5.3 `offload`：内联大对象外置

把躺在消息历史里的二进制本体（base64 图片/音频/归档）换成占位文本。

**两阶段**：首次见到时落盘 + 标记，但**内容原地保留**（`Replaced` 恒为 false）；
后续轮才替换为占位。理由是若首次就替换，模型这一轮从未真正看到过该对象，
后续轮的占位文本又把它指向「模型没看过的东西」——等于凭空丢失输入。

```go
package myapp

import (
	"github.com/yi-nology/agent-harness/offload"
)

// 唯一接缝：Store（Put 必须对同一 relPath 幂等）
type fsStore struct{ root string }

func (s fsStore) Put(relPath string, data []byte) error {
	// relPath 是**逻辑键**（blobs/<前两位>/<摘要><扩展名>），不是本机路径
	return writeFile(s.root, relPath, data)
}

func applyOffload(st *offload.State, msg []byte) offload.Report {
	return offload.Apply(offload.Request{
		Blobs:        scanBlobs(msg),
		State:        st, // 归调用方持有，须跨轮存活
		Store:        fsStore{root: "/var/lib/harness/blobs"},
		MinBytes:     0,  // <=0 取缺省 offload.MinBytes（4 KiB）
		RetrieveHint: "需要时用 read_blob 工具按 path 取回（id 相同即字节相同）。",
	})
}
```

**坑**

- **`Apply` 刻意不返回 error**：条目级失败在机制上无法中断本轮，调用方不存在
  「忘处理 err 整轮报废」的可能。失败一律落到 `Item.Reason` 与 `Report.Inline`，
  内容原样保留。
- **落盘失败不得标记**：否则下一轮会放出指向从未写入的路径的占位文本（悬空引用），
  比不外置更糟。
- **标记按轮提交，不按条目提交**。否则同一消息里两份相同图片，第二份会被自己刚写的
  标记判成「后续轮」而当场替换——那一轮模型从未完整看到过其中任何一份。
  **宿主若自行调用 `State.Mark` 破坏这条，会打掉正确性保证。**
- **`State` 归调用方，`nil` 是保守方向**：`nil` 视作「什么都没见过」，每轮都按首次处理
  ——内容永远完整可见、只是省不下上下文。**永不丢输入。**
- **`Ext` 由调用方给，内核不猜**（不给时用 `ExtForMimeType(m.MimeType)`）。
  宿主各写一套映射会让同一份内容在不同宿主落进不同的键，去重与占位复用**同时失效**。
- **占位文本必须逐字节稳定**（不含时间戳/序号/随机数），`Ref.Path` 必须与真实落盘键逐字
  一致，否则模型按图索骥必然落空。摘要**只暴露 12 位前缀**——全量摘要进消息等于给内容
  做跨库比对指纹。
- **`Item.Reason` 仅供留痕，不可据以重试。**

---

### 3.6 断点续跑：`checkpoint` + `commitgate`

#### 3.6.1 `checkpoint`：续跑而非重放

内核把恢复判定做成纯函数 `BuildPlan(graph, snap) Plan`，三条硬规则：

**① 空洞不采信**——只信「图内**连续已完成的前缀**」。若快照完成集有空洞
（后面几步完成、前面没完成），空洞之后的完成**一律不采信**，`Skip` 停在前缀末尾、
其余进 `Run`（重跑），并报 `AnomalyDoneOutOfOrder`。
代价是「失序时会多跑几步」——刻意选的：多跑可恢复，重复副作用往往不可。

**② 终态不自动重开**——快照 `Status` 是终态且图内仍有未完成步骤时，
计划 `Blocked()=true` 且 `Run`/`Skip` **均置 nil**。内核拒绝替宿主决定是否重开，
也不给「看起来能跑」的半截计划。**这是唯一一种阻断异常**，其余异常都按最保守解释
给出可执行计划。

**③ 序号单调闸**——`SeqGate` 保证快照序号不倒退。

```go
package myapp

import (
	"context"

	"github.com/yi-nology/agent-harness/checkpoint"
)

// 宿主提供：跨进程单调写 + 读取
type storeCAS struct{ db *DB }

func (s *storeCAS) SaveIfNewer(ctx context.Context, snap checkpoint.Snapshot) error {
	// 条件更新：seq 不大于已存序号则不写，且不得部分写
	return s.db.CASUpsert(snap.RunID, snap.Seq, snap)
}

func (s *storeCAS) Load(ctx context.Context, runID string) (checkpoint.Snapshot, error) {
	snap, ok, err := s.db.Get(runID)
	if !ok {
		return checkpoint.Snapshot{}, checkpoint.ErrNotFound // 不得返回 (零值, nil)
	}
	return snap, err
}

func resume(ctx context.Context, runID string, graph []checkpoint.StepDef) error {
	st := &storeCAS{}
	gate := checkpoint.NewSeqGate()
	cm := checkpoint.NewCommitter(gate, st)

	// ★ 进程启动/重启后必须回灌进程内闸，否则闸是空的、陈旧写畅通无阻
	if err := cm.SeedFromStore(ctx, runID); err != nil {
		return err
	}

	snap, err := st.Load(ctx, runID)
	if errors.Is(err, checkpoint.ErrNotFound) {
		snap = checkpoint.Snapshot{} // 首次执行，全量 Run
	} else if err != nil {
		return err
	}

	plan := checkpoint.BuildPlan(graph, &snap)
	if plan.Blocked() {
		return errNeedsExplicitReopen // 终态不自动重开：交宿主显式决定
	}
	for _, a := range plan.Anomalies {
		metrics.Inc("checkpoint.anomaly", a.Kind) // 只报告不裁决
	}

	for _, step := range plan.Run {
		if err := cm.Commit(ctx, snapshotAfter(snap, step)); err != nil {
			return err // Commit 内部已按「成功不回滚 / 失败回滚」固化顺序
		}
	}
	return nil
}
```

**坑**

- **`SeqGate` 只在进程内**，记录只在内存，重启后为空。补救是 `Seed` / `SeedFromStore`。
  **不灌不是「闸没生效」，是「闸被重置后没人告知」。**
- **裸用 `SeqGate` 时 `Accept → Save` 且 Save 失败必须 `Release`**，
  否则该 run 卡死（重试被拒为陈旧、跳过则留永久空洞）。用 `Committer.Commit` 可免抄这一步。
- **`Commit` 成功时绝不能回滚**，否则闸退到存储前面，一次陈旧慢写就畅通无阻。
- **`Release` 回滚到前一个值，不是清零**。内部靠 `prev` 一格保留上一个已成功落库的序号；
  走 delete 会把「地板」一起抹掉。
- **`Snapshot` 落库前必须 `Clone`**：持引用的话，调用方落库后继续改动快照，
  恢复判定的依据不再可信。
- **`Seq == 0` 恒视为不参与单调判定**——不是一次真实提交。
- **`store == nil` 时 `Commit` 恒失败**（`ErrStoreMissing`），**不静默放行**。

#### 3.6.2 `commitgate`：终态独占提交闸

**为什么 `statemachine` 排不出来**：它是**纯谓词**，只回答「这次迁移合不合法」，
**不回答「有没有人已经提交过了」**。编排把一个环节拆成两条并发分支时，
两条分支各自通过同一个纯谓词检查、各自把活干完、各自提交终态——
没有任何一层会发现这件事发生过。

`checkpoint.SeqGate` 也挡不住：它按 RunID 排**快照序号**，两个分支先后提交
seq=5、seq=6 是完全合法的递增，两个都被接受，后写的覆盖先写的——
**而先写分支的副作用早已发生**。

```go
package myapp

import (
	"github.com/yi-nology/agent-harness/commitgate"
)

// 终态词表由宿主注入——本包只管「独占」，不认任何状态取值
func newCommitGate() *commitgate.Guard {
	return commitgate.NewGuard(func(status string) bool {
		switch status {
		case contract.StatusSucceeded, contract.StatusFailed,
			contract.StatusCancelled, contract.StatusTimedOut:
			return true
		}
		return false
	})
}

func commitStep(g *commitgate.Guard, ref commitgate.Ref,
	status, branch string, seq uint64, save func() error) error {
	c := commitgate.Commit{Ref: ref, Status: status, Branch: branch, Seq: seq}
	lease, err := g.Acquire(c) // ★ 抢终态独占权（拿凭证）
	if err != nil {
		return err // 抢不到就别干后面的活
	}
	if err := save(); err != nil {
		g.ReleaseLease(lease) // ★ 落库失败必须回滚，否则该环节闸被永久占住
		return err
	}
	return nil
}
```

**坑**

- **Commit / Release 必须成对**。落库失败不 Release，该环节闸被永久占住，
  从此**再也无法提交终态**——不是「多花一个序号」，是彻底卡死。
- **`Ref` 必须是「运行 + 环节」两级**：不同 run 的同名环节是不同对象，
  只按环节名去重会把第二次运行整个判成重复提交。
- **`isTerminal == nil` 时按「全部都是终态」处理**——保守侧，宁可多拦。
  反过来说：宿主漏注入会让闸被架空。
- **回滚要用凭证（`Acquire` + `ReleaseLease`），不要用旧的 `Release(c)`**。
  旧接口靠 (状态, 分支, 序号) 三元组认人，而那三元组**不唯一**：
  并行分支常常同名（`Branch` 写的是 agent 名），「落库失败 → 回滚 → 重试同一份快照」
  时三元组又与上一轮逐字相同。于是**迟到的 `Release` 会把别人的闸放掉**——
  实测 8 路同名并发提交出现 4 个赢家，同一环节的终态被提交两次，
  且失效是静默的（`Commit` 返回 nil，宿主无从察觉）。
  凭证路径把「身份」从内容换成令牌，那条承诺才无条件成立。
  旧接口仍在（同一分支、同一序号不会同时存在的场景可直接用），
  但它已加严：状态也必须相同才算数。
- **零值不可用**，请用 `NewGuard`。
- **`Seed` 重启后回灌**：`Seed` 幂等，且已有记录不被更旧的覆盖。
- 与 `checkpoint` 互补不替代：一个环节推进**先过本闸拿终态独占权，
  再由 SeqGate 保证快照不倒退**。

---

### 3.7 实时事件：`eventbus` + `watchdog`

```go
package myapp

import (
	"github.com/yi-nology/agent-harness/eventbus"
	"github.com/yi-nology/agent-harness/watchdog"
)

// ① 发布路径：先落库拿自增 ID，再广播——保证 SSE 游标是唯一的单调空间
func publish(ctx context.Context, bus *eventbus.Bus, e eventbus.Event) error {
	id, err := repo.InsertEvent(e) // 拿 bq_event 自增 ID
	if err != nil {
		return err
	}
	e.ID = id
	bus.Broadcast(e)
	return nil
}

// ② SSE 桥接：按会话订阅，满缓冲丢帧由 DB 回放游标兜底补达
func serveSSE(hub *eventbus.Hub, sessionID string) {
	ch, cancel := hub.Subscribe(sessionID)
	defer cancel()
	for ev := range ch {
		pushToClient(ev)
	}
}

// ③ 配对不变量监视：只告警，不裁决
func runWatchdog(ctx context.Context, bus *eventbus.Bus) {
	pub := func(sessionID, caseID string, stepID *uint64, typ, level string, payload map[string]any) {
		publishEvent(ctx, sessionID, caseID, stepID, typ, level, payload)
	}
	go watchdog.New(pub).Run(ctx, bus)
}
```

**「有损」的确切语义**

- **`Bus`**：每订阅者 **64 缓冲**，满了就丢该订阅者的这条事件，**发布永不阻塞**。
- **`Hub`**：按会话键控，每订阅者缓冲 256；`Publish` 用 `select`+`default` 非阻塞投递。
- **丢的时候没有逐条信号**——只有累计计数 `Dropped() int64`。
  丢帧的补偿责任在宿主：SSE 的 **DB 回放游标 + 全局 tail 兜底补达**。
- 因此**持久化在发布点由宿主负责**：先落库拿自增 ID，再广播。
  SSE after 游标只认这一个单调空间，跨重启/跨副本都成立。

**坑**

- **`Bus.Publish` 赋的是未落库的临时序号**，生产路径改用 `InsertEvent` + `Broadcast`。
- **`Hub.Publish` 对 `SessionID` 为空的系统级事件直接忽略**（无会话受众）。
- **`Bus` 与 `Hub` 的 `Dropped` 是两套独立计数**，别混用。
- **`Hub` 的 cancel 幂等安全**；宿主仍应在 SSE 断开时调用。
- **`watchdog` 只告警不裁决**：热路径裁决在守卫侧（`IdentityGuard`），不在本包。
  **别让宿主误以为它能兜底。**
- **`Watcher` 不是并发安全的**，只能由 `Run` 单 goroutine 驱动。
- `watchdog` 的 INV3（事件面 checksum 抽验）**不实现**：`tool_result` 载荷截断到 2000 字符，
  无法重算 checksum；内容级校验由守卫在源头全量覆盖。

---

### 3.8 流式与退出：`delta` + `drain` + `pending`

#### 3.8.1 `delta`：增量节流

⚠️ **生命周期必须是「单个环节」，不是「整个进程」。**

```go
package myapp

func streamRound(p *delta.Pacer, sp *delta.Splitter, publish func(delta.Pub), chunk string) {
	// ① Splitter：安全发射窗口，跨分片的标签不会被污染
	reasoning, content := sp.Feed(chunk)
	if r, ok := p.Offer("reasoning", 1, reasoning); ok {
		publish(r)
	}
	if c, ok := p.Offer("text", 1, content); ok {
		publish(c)
	}
}

func finish(p *delta.Pacer, publish func(delta.Pub)) {
	// ② 环节收口：按 轮升序 × reasoning→text 冲刷所有残尾，保证落序可回放
	for _, pub := range p.FlushAll() {
		publish(pub)
	}
}
```

**这个生命周期约束为什么重要**：`Pacer` 的 `entries` 按 `(kind, iteration)` 惰性建流
且**从不回收**，`FlushAll` 扫序上界又是 `max(iteration)`——两处单独看都像缺陷，
安全**只因为一个外部不变量**：宿主每个 LLM 环节新建一个 Pacer，环节 defer 里
`FlushAll` 后整体丢弃；`iteration` 是环节内局部计数器、不跨环节累加。
改成长生命周期对象（如「整个会话一个 Pacer」）会同时引爆这两条，
症状「跑久了内存涨 / 收口卡住」极难归因。

**其他坑**

- **`Splitter` 不 `Flush` 就丢流 = 永远扣住最后几个字符**。`Feed` 返回空不是丢内容，
  是扣在 `pending` 等下一片。
- **`NewSplitterTags` 任一标签为空则回落缺省**：空标签会让「后缀是标签前缀」恒真，
  内容永远扣住不发，是最难查的挂起。
- **`Text` 是分片非累积快照**：消费方按序追加，避免长文本 O(n²) 重发。
- **超帽静默停发**：`CapBytes`（64KB）单轮单类增量总量帽。权威快照会补全被帽截断的内容。

#### 3.8.2 `drain`：优雅排水

把「被杀」变成「自然收敛」。双件配合：轮 ctx 与根 ctx **取消脱钩**（值保留、
取消传播切断），`Drain` 是唯一收刀点。

```go
package myapp

import (
	"github.com/yi-nology/agent-harness/drain"
)

// ① 每轮派发：轮 ctx 从根 ctx 脱钩，轮内取消/超时/接管语义原样保留
func dispatch(rootCtx context.Context) context.Context {
	return drain.DetachCtx(rootCtx, drain.Budget(0)) // 0 = 缺省 60s
}

// ② 进程收到终止信号：等在途轮收敛，预算耗尽才强制取消
func shutdown(ctx context.Context, turns drain.Turns) {
	drain.Drain(ctx, turns, drain.Budget(0)) // 恒无返回值，排水失败不阻断关停链
}
```

**坑**

- **这是本包唯一危险的使用前提**：脱钩后必须仍有一条取消通路兜底（轮内超时 /
  `Drain` 强制收刀），否则轮会真的泄漏。
- **部署宽限期须 ≥ `DefaultTimeout`(60s) + 启动对账余量**，
  否则外层 SIGKILL 先于本包收刀，排水形同虚设。
- **实现 `Turns` / `SessionTurns` 须「快照后逐个调用」，不得持锁调取消回调**——
  取消会触发收尾路径，持锁重入即死锁。
- **`DrainSession` 空 sessionID 拒绝执行**，不退化成 `CancelAll`
  （退化成全局取消正是要消除的误伤）。
- **`CancelOf` 该会话无在途轮时必须无操作**，不得把 cause 广播给其他会话。
- 日志走 `logx.Delegating`，**不收回调**——留痕一律经包级 Delegating logger，
  宿主用 `logx.SetDefault` 换后端即可。

#### 3.8.3 `pending`：多轮挂起态载体

```go
package myapp

import "github.com/yi-nology/agent-harness/pending"

// ① 写入：唯一入口，保证列内容恒 JSON
func storePending(clarify string) string {
	return pending.ColumnValue(clarify, "disambig")
}

// ② 读路径只认 Encode 产物；非法/损坏一律降级为「无挂起」
func readPending(col string) (pending.State, bool) {
	s := pending.Parse(col)
	return s, s.Kind != ""
}
```

**坑**

- **必须走 `ColumnValue` 落列**。守卫原先只有 `strings.HasPrefix(clarify, "{")`，
  于是 `"{节点A}和{节点B}"`、`"{"` 这类串原样落列，`Parse` 解不出返回零值，
  **挂起态被静默丢掉**（不是降级，是消失）。透传条件已改成「真能 `Parse` 出带 `Kind`
  的 `State`」，坏串被当作 `Word` 保留，至少内容不丢。
- **读路径只认 JSON**，不留历史协议兼容——挂起态是瞬态，跨版本存量行早已结算完毕。

---

### 3.9 安全面：`netguard` + `pii`

#### 3.9.1 `netguard`：出站 SSRF 守卫

**三层判定**：名字层（解析前按名拒）+ 解析后逐 IP 过 egress + 重定向逐跳重验。

```go
package myapp

import (
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/netguard"
)

func guardOutbound(rawURL string) error {
	// 外部输入面（告警回推 / IM 推送）：零值放行面 = 私网与回环全拒
	// 凭证连通性探测：探测本机/内网自建服务是能力本身，用 AllowSelfService()
	// ——那个名字把意图留在代码里（这组比特也是 SSRF 穿透的入口）
	return netguard.CheckURLWith(rawURL, netguard.Allow{})
}

func guardRedirects(start string, hops []string) error {
	return netguard.CheckChainWith(start, hops, netguard.Allow{}, netguard.DefaultMaxHops)
}

// 扩展名单：整份原子交换，先全量校验再一次性交换
func updatePolicy() error {
	// 同时更新两项时必须用 SetPolicy —— 策略是两份数据合起来的
	return netguard.SetPolicy([]string{"internal-metrics.corp"}, []string{".corp"})
}
```

**坑**

- **`CheckURL` / `CheckChain` 的双 bool 形态已废弃**，请用具名的 `CheckURLWith` /
  `CheckChainWith` + `netguard.Allow`。旧签名仍在（不破宿主 API），但两个 bool 的
  含义只能靠调用点约定区分，而放行面是安全边界；具名形态让调用点自解释，
  且将来加第三个放行维度不必破签名。**零值 `Allow{}` 是严的**（私网与回环全拒），
  要放行必须显式写出来。
- **拨号层要接上，否则 rebinding 窗口仍在**。`CheckURLWith` 是**写面校验**
  （配置时判一次）；实际请求在之后由 HTTP 客户端再解析一次 DNS，两次之间权威
  DNS 可以改口——那是活的 SSRF 窗口。`netguard.DialFunc` 把同一套判定落到
  建连那一刻（名字层 + egress 预检 + 建连后核对真实 `RemoteAddr`），
  **接法见下节**。
- **名单分两组，口径不同**：回环族名字与回环地址同待遇，随 `Allow.Loopback`；
  其余名单项（精确名元数据/容器网桥名、`.internal/.local/.lan` 后缀）**无条件拒绝，
  不看旗标**。`Allow.Private` 的语义是「放行 RFC1918 私网**地址**」，
  不是「放行任意私有命名空间的**主机名**」。
- **内置默认名单不可被削减**：`SetPolicy` 只能增补扩展位。
- **后缀校验发生在注入期**：无前导点的后缀被拒（`corp` 会把语义从「拦 foo.corp」
  悄悄变成「拦一切以 corp 结尾的名字」，比不拦更糟）。任一项非法则**旧策略原样保留**，
  不做部分应用。
- **代次只是信号，不是响应**：两次比对之间换了两次，代次说不出「变成了什么」，
  用 `Subscribe` 接上这半句。
- **`Subscribe` 注册后立刻回调一次当前快照**，否则订阅者带着从没填过的视图上线
  （比没有订阅更危险，因为它以为自己已经跟上了）。
- **`CheckChain` 返回 nil 不代表整条 hops 都被校验过**——空 Location 终止链条，
  其后的 hops 不可达，既不校验也不占预算。
- **`ResolveLocation` 必须解析而非直接送 `CheckURL`**：Location 头允许相对地址；
  协议相对地址 `//host/x` 更危险，不解析看不出跨主机跳转。
- `netguard` **不在工具调用的判定环上，这是有意的**——塞进去就得猜「哪个参数字段是 URL」，
  那是领域知识。

#### 3.9.1.1 拨号层接线：把 rebinding 窗口闭上

写面校验（`CheckURLWith`）与实际请求分属两次 DNS 解析，两次之间权威 DNS 可以改口
（`evil.example` 写面解析成公网地址、实际请求解析成元数据地址）。**接线是宿主的
决策，但机制本仓已经备好**——只剩把 `Transport.DialContext` 换成 `netguard.DialFunc`。

最省事的接法（`NewClient` 把「守卫挂在传输层」收成一行，并保留宿主自己的
client 注入口）：

```go
import "github.com/yi-nology/agent-harness/netguard"

// 生产：零值放行面 = 私网与回环全拒
hc, err := netguard.NewClient(nil, netguard.Allow{})
if err != nil {
	return err
}
resp, err := hc.Get(targetURL)
```

已有自己的 `*http.Transport` 时：

```go
tr := netguard.NewTransport(myTransport, netguard.Allow{})
hc := &http.Client{Timeout: 5 * time.Second, Transport: tr}
```

**三条接线纪律**：

1. **两层放行面必须同一个值**。`Allow` 是校验层与拨号层**共用的类型**
   （`CheckURLWith` / `CheckChainWith` / `DialOptions.Allow`），映射到上游 egress
   也只有一处实现。口径不一致的后果是**分层放行**：校验层拒、拨号层放行——后者
   才是安全问题。
2. **逐 URL 的策略无法在拨号层表达**（拨号按连接、校验按 URL）。此时取
   **拨号层最宽、校验层逐条**：拨号层放行校验层可能放行的全部范围，但**不放行
   校验层恒拒的段**（链路本地/元数据、保留段）——那些段的拒绝与放行面无关。
3. **测试要连本机假服务器时，只在测试里放宽回环**：

   ```go
   hc, err := netguard.NewClient(&http.Client{Timeout: 2 * time.Second},
       netguard.Allow{Loopback: true})   // 只在测试里
   ```

   **不要**为了省事把生产配置一起放宽——那等于把刚补上的窗口重新打开。
   若客户端的 `Transport` 是自定义 `RoundTripper`（不是 `*http.Transport`），
   `NewClient` 会**报错**而不是悄悄放行：拨号发生在传输层内部，挂不上就没有
   拨号层防线。

#### 3.9.2 `pii`：个人信息脱敏

在 `logredact`（凭据类值模式）之外补「个人信息价值面」——大陆手机号与 18 位居民身份证。

```go
package myapp

import "github.com/yi-nology/agent-harness/pii"

func main() {
	// 装配期：面名自查（fail-closed 是保险，不是日常路径）
	ss, err := pii.ParseSurfaces("input", "output", "tool_results")
	if err != nil {
		panic(err)
	}
	pii.SetEnabled(true)  // 缺省开 = secure by default
	pii.SetSurfaces(ss)   // 缺省三面全开

	// 策略热重载订阅（注册即收到一次当前快照）
	h, err := pii.Subscribe(func(p pii.Policy) {
		metrics.Gauge("pii.strategy", string(pii.Strategy()))
	})
	if err != nil {
		return
	}
	defer pii.Unsubscribe(h)
}

func guardOutflow(userMsg, modelAns string, toolResult any) error {
	// 面未启用/全局关闭 → (原文, false)；检出且策略=block → (文本, true)
	auditText, b1 := pii.Apply(pii.SurfaceInput, userMsg)
	shownText, b2 := pii.Apply(pii.SurfaceOutput, modelAns)
	toolPayload, b3 := pii.ApplyValue(pii.SurfaceToolResults, toolResult)
	if b1 || b2 || b3 {
		return errBlocked // ★ 阻断决策归宿主，本包只回报信号
	}
	_, _, _ = auditText, shownText, toolPayload
	return nil
}
```

**坑**

- **默认策略是 `mask`，不得改成 `block`**——block 会打断整条审计链，
  要用必须宿主显式 `SetStrategy` 并自行履行阻断。
- **未登记面名 fail-closed**：`Apply` 遇未知面返回 `("[redacted]", true)`——
  整段替换 + 要求阻断，**不 panic、不静默放行**。面未登记与「宿主显式关闭」是两件事，
  塌缩成同一状态等于拼错一个字符即可让该面完全不脱敏。
- **策略是单原子指针，不可撕裂读**。旧的三个独立原子变量在热重载下会产出从未被任何
  写侧设置过的组合。**处置路径必须经 `CurrentPolicy()` 拿快照**，
  不得分字段调访问器再拼。
- **`Policy` 结构体是不透明值**：字段全部非导出且**没有任何方法**，
  回调里只能调包级访问器读取。
- **订阅者 panic 被逐个 recover**。回调里**可以**再调 `SetStrategy`（无锁不自死锁），
  但会再触发一轮通知，**递归深度由订阅者自己收敛**。
- **15 位旧身份证不检**（已停用 20 年）；**裸 `86` 前缀不认**，只认 `+86` / `0086`。
- **不下探标量**：数字/布尔原样返回——JSON number 形态的手机号不在覆盖面。
- `Redact` / `RedactValue` 已 **Deprecated**（面无关且不回报是否阻断）。

---

### 3.10 门面三件：`dispatch` + `policy` + `reminder`

这三个包是**门面**，核心都在 `oagentkit`。**不要在本仓加实现**——
`harness-facade-stays-facade` 门禁（error 级，走 `go/ast`）会拦下新类型、
多语句方法或非上游调用。

#### `dispatch`：权限即拓扑

agent 间可达性由 allow 矩阵物理决定，矩阵在构建期物化、运行期只读。

```go
package myapp

import (
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/dispatch"
)

// 宿主把自己的注册表投影成拓扑
type myRegistry struct{ edges map[string][]string }

func (m myRegistry) Slugs() []string {
	out := make([]string, 0, len(m.edges))
	for s := range m.edges {
		out = append(out, s)
	}
	return out
}

func (m myRegistry) DispatchAllow(slug string) []string { return m.edges[slug] }

func main() {
	g := dispatch.NewGuard(myRegistry{edges: myEdges}, 3) // ★ 矩阵含环会 panic

	if err := g.Assert("triage", "restart", 0); err != nil {
		var de *dispatch.DenyError
		if errors.As(err, &de) {
			// 不得转述、不得降级，按失败兜底如实上报
			switch de.Reason {
			case dispatch.ReasonNotAllowed:
			case dispatch.ReasonDepthExceeded:
			case dispatch.ReasonSelfDispatch:
			}
		}
	}
}
```

**坑**

- **`NewGuard` 在 allow 矩阵含环时 panic**（自环同样拒绝）——
  宿主必须在装配前保证拓扑无环，或自行在调用点外围 recover。
- **拒绝原因请按常量比对而非裸字符串**，常量在此原样透出，跨仓比对不会因字面量漂移而失效。
- 本包不认识任何具体 agent：拓扑由宿主的 `EdgeSource` 实现提供。

#### `policy`：操作审计门

```go
package myapp

import "github.com/yi-nology/agent-harness/policy"

// 缺文件回内置基座，阈值只降不升
p, err := policy.LoadOverrides(policyFS) // 路径固定为 _shared/operation_policy.yaml
g := policy.NewGate(p, nil)              // p 为 nil 用内置基座；arbiter 为 nil 则灰区直接升人审

d := g.Decide(ctx, policy.Op{
	Type:     policy.OpToolCall,
	Mode:     policy.Normalize(sessionMode), // 空/非法一律回落 confirm
	Tool:     "restart",
	Mutating: true,
})
```

**坑**

- **门面上没有 `Stage` 转发**（上游 `policykit.Stage` 未被透出）。
  要接成 invoke 链一环，宿主需自包一层 `guardset.Guard`，`Check` 里调 `Gate.Decide`。
- **`DefaultPolicy` 未被门面转发**：要内置基座就传 `NewGate(nil, nil)`。
- **`Normalize` 对空/非法一律回落 `confirm`**——审计门不允许因模式脏值放大权限。
- **`Decide` 只读 policy，并发安全，可放工具调用热路径。**

#### `reminder`：系统注入提示的防伪包裹

```go
package myapp

import "github.com/yi-nology/agent-harness/reminder"

// 纪律：body 来自用户输入 / 工具产出 / 历史结论等不可信内容时，
// 必须先 SanitizeBody 再进 prompt
msg := reminder.Wrap("alert", reminder.SanitizeBody(webhookText))
```

**坑**

- **调用顺序不可颠倒**：`SanitizeBody` 是「包裹前调用」。
- **与 guardx 的 `fence` 互补而非替代**：`fence` 封数据侧（不可信内容不得越出数据区
  取指令待遇），`injectband` 封系统侧（系统带外注入的身份标记不得被伪造）。
  只做前者时，攻击者不必逃出数据区，只需在内容里写一个像系统注入的标签即可抬升身份。

---

## 4. 门禁与提交前检查

### 4.1 一条命令

```bash
make check
```

CI 只执行它。**门禁本体全部在 Makefile 里，不在 `ci.yml` 里**——
门禁若只挂在工作流文件上，本地要到推送之后才知道自己违规，
那不是「提交前门禁」而是「推送后门禁」。

`make check` 的依赖链：

| 目标 | 拦什么 |
|---|---|
| `fmt-check` | gofmt 未格式化（文件集由 git 决定，不递归扫 `.*-work/`） |
| `vet` | go vet |
| `lint` | golangci-lint（**未安装时明确报错退出**，不静默跳过） |
| `test` | 全量单测 |
| `cover-gate` | 覆盖率 < 60% 即红（当前实测 **86.6%**） |
| `architecture-check` | 7 条规则 + 3 个门面包 + 24 个模块的依赖方向 |
| `docsync` | README 包表 ↔ 仓库实际（生产行数 / 外部依赖） |
| `modreplace` | go.mod 不得含 `replace` |
| `modwork` | go.work / go.work.sum 不得入库 |

```bash
make architecture-check --report   # 看全量违规清单（默认前 10 条）
```

### 4.2 七条架构规则

| 规则 | 级别 | 拦什么 |
|---|---|---|
| `harness-no-domain-vocabulary` | **error** | 生产代码里的运维领域词 |
| `harness-test-fixture-domain-vocabulary` | **error** | 测试夹具用了真实运维词面（须改合成数据） |
| `harness-no-host-dependency` | **error** | 任何对宿主仓的 import |
| `harness-no-domain-runtime-deps` | **error** | 对宿主仓 / 旧门面仓 / 已退役 `agentkit` 门面的 import |
| `harness-no-bare-goroutine` | **error** | 生产代码裸 `go func`（panic 无法被外层 recover） |
| `harness-no-printf-log-callback` | **error** | 公开 API 里的 `logf func(string, ...any)`（printf 回调焊进签名即不可逆 API 债） |
| `harness-single-errors-package` | **error** | stdlib `"errors"` 与 `github.com/pkg/errors`（全仓只允许 `oekit/pkg/errors` 一套） |

另有两条**不走正则表、由 `go/ast` 判定**的结构性规则（判据是语法结构，逐行看文本判不出）：

| 规则 | 拦什么 | 落点 |
|---|---|---|
| `harness-facade-stays-facade` | `dispatch`/`policy`/`reminder` 长出实现 | `policy.yaml` 的 `facades` 段 + `archcheck/facade.go` |
| `harness-module-dependency-direction` | 包间依赖超出 `modules[].requires` 声明；**以及未登记的包目录** | `policy.yaml` 的 `modules` 段 + `archcheck/deps.go` |

> 「未登记的包目录」这条更隐蔽：未登记的包对本门禁完全隐形，
> 任何模块都能 import 它而不判违规。

**例外必须显式登记**（policy `exceptions` 段：rule + path + line 三元组精确豁免 +
必须写 `until` 与拆除条件）。**当前例外数 = 0。**

### 4.3 在本仓加新包的正确顺序

**先改 policy 再写代码。**

```yaml
- id: 新包名
  roots: [新包名]
  requires: []        # 真实依赖，不是理想形态
  owner: harness
  managed: true
  notes: ...
```

不登记则 `architecture-check` 立刻红。判定面**只含生产代码**（跳过 `_test.go`）：
依赖方向决定交付物里出现什么，测试依赖是临时的。

### 4.4 结构阈值

`max_file_lines: 400`，`thresholds.enforce: true`，超限即非零退出。

**拆分只为满足机械阈值**——若某个模块拆完仍读不动，该重新分组，
不要靠继续拆小来假装问题解决了。

另有 `max_methods_per_type`（god object 判据），它比的是**实质方法数**而不是方法
总数——类型退化成 façade（实现下沉到子包、自己只做转发）之后，方法数 ≈ **公共 API
表面规模**，不是复杂度集中度，拿它判 god object 等于「把正确的工作判成错的」。

「转发壳」= 方法体只有一条转发语句、且剥掉注释与空白后长度 ≤
`forwarding_shell_max_chars`（**不写 = 缺省 140**；显式写 0 = 关闭该区分，
判定逐字退回旧行为）。老 policy 无需改动即可享受修正。

---

## 5. 本地多仓并行开发

本仓处在 `oagentkit` / `oekit` 矩阵里，改本仓时经常要同时改上游仓。
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

**配套纪律**

1. **go.mod 零 replace**；本地 `go.work` 由 `.gitignore` 兜底。
2. **go.mod 里 require 的版本必须是已发布的 tag**。若确需依赖未发布的新包，用
   `make devlink`，不要动 go.mod。CI 在干净环境构建，未发布版本解析不到直接红。
3. **workspace 期间别跑 `go work sync`**——它按 workspace 的构建列表**回写**各模块的
   `go.mod` / `go.sum`，仓不全时用不到的依赖会被直接删掉。要整合同步，
   先 `make devunlink`，再在单模块下 `go mod tidy`。
4. **`make devlink` 可重复执行**：已有 `go.work` 时走 `go work use -add` 追加。
5. **验下游而不是只验本仓**——本仓作为主模块绿，不代表作为依赖也绿。

**升级第一方依赖前**，先用 `git ls-remote --tags` 核一遍候选版本真的存在。
`go list -m -versions` 报的是 **proxy 的缓存目录**，不是远端现实——
tag 被删后条目仍留着直到缓存过期，而拿它当采购单会得到「本地全绿、下游全红」。

**若上游发生过 tag 重写**，先清本仓的 setup-go 缓存再推：
`actions/setup-go` 的缓存 key 含 `hashFiles('**/go.sum')`，go.sum 一改 key 就变，
于是命中 restore-keys 前缀回退，恢复**上一份**缓存——那份建于重写之前，
里面存的就是旧模块副本，表现为 checksum mismatch 而本地全绿。

---

## 6. 坑速查表

接线完成后拿这张表自查。左列是症状，右列是真因。

| 症状 | 真因 |
|---|---|
| 模型第一次调工具被拒，第二次同参重试就过了 | 正常行为——「拒绝即激活」。看 `Retryable(err)` 是不是 true |
| 同一个工具被反复拒，每次理由都不同 | 你没用 `guardset`，链在第一个拒绝处收口了 |
| 重启后陈旧快照写畅通无阻 | `SeedFromStore` 没回灌，`SeqGate` 重启即空 |
| 落库失败后某个环节再也提交不了终态 | `commitgate` 的 `Release` 没调 |
| 压实后界面还显示「上下文紧张」 | 没显式 `Meter.Shrink` 把账改回来 |
| 净移除量对不上，方向是「看起来更省了」 | 用了 `GrossRemovedTokens`，要减应该用 `NetRemovedTokens` |
| 摘要写进消息的指纹能被跨库比对 | 占位暴露了全量摘要，应该只给 12 位前缀 |
| 模型从未见过某张图片，之后却在引用它 | `offload` 首轮就替换了，或落盘失败仍被标记 |
| 会话一长跑内存就涨、收口卡住 | `delta.Pacer` 被做成了长生命周期对象，应每环节新建 |
| 一个卡死会话把所有会话陪葬 | 用了 `Drain` 而非 `DrainSession` |
| 挂起态莫名其妙消失 | 落列绕过了 `pending.ColumnValue` |
| 超大模型输出居然「解析成功」且置信度不低 | 命中过截断式上限失效的坑，现已 fail-closed；若你 fork 了本仓请确认走的是 `ErrNotReport` 分支 |
| SSE 进度条停在中间态 | 正常——总线有损，靠 DB 回放游标补达；先确认发布路径是「先落库再广播」 |
| 判定环拒绝后 host.<tool>() 仍然执行 | `ptc.Options.Stages` 没传 |
| 工具重试把一次副作用变成两次 | 只看了 `Retryable`，没看 `SafeToResubmit` |
| `make check` 报域词污染 | 领域资产漏进内核了——它该在宿主仓 |
| 加了个新包门禁立刻红 | 没在 `architecture-policy.yaml` 的 `modules` 段登记 |
| 本地全绿但 CI 报 checksum mismatch | 见 §5 的 tag 重写 / 缓存回退两条 |

---

## 7. 延伸阅读

| 文档 | 回答什么 |
|---|---|
| [README](../README.md) | 定位、边界纪律、包清单、门禁总览、下游升级须知 |
| [AI-GUIDE.md](AI-GUIDE.md) | **改本仓的 AI 代理**：任务 → 改哪些文件 → 哪条门禁拦 → 怎么验收 |
| [OAGENTKIT-OVERLAP.md](OAGENTKIT-OVERLAP.md) | 与 oagentkit 的重叠面盘点：哪些该做、哪些上游已有 |
| [CHECKPOINT.md](CHECKPOINT.md) | 续跑契约的设计推导与三条不变量 |
| [AGENTS.md](../AGENTS.md) | 代理在本仓干活的工作纪律 |
| [SPLIT_PLAN.md](SPLIT_PLAN.md) | 从宿主仓拆分的分层依据（**2026-10-17 历史快照**，非当前包清单）|
| [MIGRATION_OAGENTKIT_20261018.md](MIGRATION_OAGENTKIT_20261018.md) | 门面迁移与 errors 链路改写的执行记录（**历史快照**，2026-10-18 已执行完毕）|
| [SELF-AUDIT-20261018.md](SELF-AUDIT-20261018.md) | 独立复核发现并修复的缺陷（**历史快照**，2026-10-18 收尾）|

规则源：[`architecture-policy.yaml`](../architecture-policy.yaml)。
执行体：`archcheck` 包（`deps.go` 判依赖方向、`facade.go` 判门包、`scan.go` 跑正则规则、`selfcheck.go` 校验 policy 自身）；`cmd/archcheck` 只是它的 CLI 装配。
