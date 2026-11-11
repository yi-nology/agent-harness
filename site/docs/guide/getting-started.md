---
title: 怎么用
---

# 怎么用：从零接入

> 🤖 本页要点已收录进 [llms.txt](https://yi-nology.github.io/goagent-kernel/llms.txt)——AI 代理可直接读取。

## 安装

```bash
# 按最新发布版引入（不钉数字——钉死的数字几轮发版后就是过期指引）
go get github.com/yi-nology/goagent-kernel@latest

# 要复现特定版本再显式钉
go get github.com/yi-nology/goagent-kernel@vX.Y.Z
```

- **零 `replace`**：本仓 go.mod 不含 replace（含 replace 的公开 module 会让下游
  `go get` 整体失败）。需要同时改上游仓时用 `make devlink` 走 workspace，不碰 go.mod。
- **按包引入**：`contract` / `statemachine` 是零外部依赖的叶子包，只想要状态机
  不必拖进 goja、jsonschema：

```go
import "github.com/yi-nology/goagent-kernel/contract"
import "github.com/yi-nology/goagent-kernel/statemachine"
```

## 三个必须先知道的约束

**① 内核只判定，不处置。**
判定语义归内核，判定依据与处置归宿主。典型表现：`pii.Apply` 检出敏感信息时返回
`(文本, true)`——`true` 表示「宿主应当阻断」，但**阻断动作由你执行**，
内核不抛错、不中断你的控制流。`toolfail` 同理：分类完了，怎么恢复是你的事。

**② 门面的门永远朝外。**
`dispatch` / `policy` / `reminder` 是**门面**，只允许类型别名、常量别名、
单条上游转发函数。核心分别落在 `oagentkit/agent/dispatch`、
`oagentkit/toolgate/policy`、`oagentkit/guardx/injectband`。
往这三个包里加一行真逻辑会被 `harness-facade-stays-facade` 门禁拦下（error 级）。

**③ 本仓不 import 你，你 import 本仓。**
方向是单向的硬约束（`harness-no-host-dependency` 门禁）。harness 需要宿主能力时
定义接口由宿主实现，从不反过来依赖。

## 接入路线图

按需接，不必全接——每一步都是独立可落地的：

<div class="diagram">
<svg viewBox="0 0 720 236" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="gs1" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
  </defs>
  <g font-size="11" fill="#e6edf3">
    <rect x="14" y="176" width="112" height="44" rx="9" fill="#161b26" stroke="#58a6ff"/>
    <text x="70" y="196" text-anchor="middle" font-size="11.5">1 契约面</text>
    <text x="70" y="212" text-anchor="middle" font-size="8.5" fill="#9aa7b8">contract · statemachine</text>
    <rect x="128" y="148" width="112" height="44" rx="9" fill="#161b26" stroke="#262e3d"/>
    <text x="184" y="168" text-anchor="middle" font-size="11.5">2 报告协议</text>
    <text x="184" y="184" text-anchor="middle" font-size="8.5" fill="#9aa7b8">protocol</text>
    <rect x="242" y="120" width="112" height="44" rx="9" fill="#161b26" stroke="#262e3d"/>
    <text x="298" y="140" text-anchor="middle" font-size="11.5">3 工具判定环</text>
    <text x="298" y="156" text-anchor="middle" font-size="8.5" fill="#9aa7b8">tooldisclose + …</text>
    <rect x="356" y="92" width="112" height="44" rx="9" fill="#161b26" stroke="#262e3d"/>
    <text x="412" y="112" text-anchor="middle" font-size="11.5">4 续跑契约</text>
    <text x="412" y="128" text-anchor="middle" font-size="8.5" fill="#9aa7b8">checkpoint · commitgate</text>
    <rect x="470" y="64" width="112" height="44" rx="9" fill="#161b26" stroke="#262e3d"/>
    <text x="526" y="84" text-anchor="middle" font-size="11.5">5 上下文三件套</text>
    <text x="526" y="100" text-anchor="middle" font-size="8.5" fill="#9aa7b8">budget · spill · offload</text>
    <rect x="584" y="36" width="122" height="44" rx="9" fill="#161b26" stroke="#262e3d"/>
    <text x="645" y="56" text-anchor="middle" font-size="11.5">6 程序化编排</text>
    <text x="645" y="72" text-anchor="middle" font-size="8.5" fill="#9aa7b8">ptc · sandbox</text>
  </g>
  <line x1="126" y1="176" x2="146" y2="160" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs1)"/>
  <line x1="240" y1="148" x2="260" y2="132" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs1)"/>
  <line x1="354" y1="120" x2="374" y2="104" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs1)"/>
  <line x1="468" y1="92" x2="488" y2="76" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs1)"/>
  <line x1="582" y1="64" x2="602" y2="50" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs1)"/>
  <text x="14" y="232" font-size="10.5" fill="#9aa7b8">顺序是典型路径不是硬依赖——每步「解决什么 / 你要提供什么」见下方各节</text>
</svg>
</div>
<p class="diagram-cap">六步阶梯：越往上越靠近「省往返」，前两步打地基，按需往上叠。</p>

### 1 · 契约面：`contract` + `statemachine`

**这一层是你与内核之间的唯一边界。** 编排引擎不碰存储、不碰数据库——
所有环节状态的读写都经 `StepSink` 出口：

<div class="diagram">
<svg viewBox="0 0 720 148" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="gs2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
  </defs>
  <g font-size="11.5" fill="#e6edf3">
    <rect x="30" y="36" width="150" height="52" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="105" y="58" text-anchor="middle">编排引擎</text>
    <text x="105" y="76" text-anchor="middle" font-size="9" fill="#9aa7b8">（内核，不碰存储）</text>
    <rect x="270" y="30" width="180" height="64" rx="10" fill="#161b26" stroke="#58a6ff"/>
    <text x="360" y="56" text-anchor="middle">RunContext</text>
    <text x="360" y="74" text-anchor="middle">StepSink</text>
    <rect x="540" y="36" width="150" height="52" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="615" y="58" text-anchor="middle">宿主状态</text>
    <text x="615" y="76" text-anchor="middle" font-size="9" fill="#9aa7b8">（存储 / DB 由你实现）</text>
  </g>
  <line x1="180" y1="56" x2="266" y2="56" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs2)"/>
  <line x1="450" y1="62" x2="536" y2="62" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#gs2)"/>
  <text x="186" y="46" font-size="9" fill="#9aa7b8">开环节 / 收口</text>
  <text x="456" y="52" font-size="9" fill="#9aa7b8">Open / Finish</text>
  <text x="30" y="128" font-size="10.5" fill="#9aa7b8">编排语义字段定死（ID/Status/CurrentStep），领域状态走开放键 Field/SetField——内核不解释、不校验</text>
</svg>
</div>

```go
// ① 会话运行上下文：编排语义字段定死（ID/Status/CurrentStep），
//    领域状态走开放键 Field/SetField——内核不解释、不校验。
type myRun struct {
	id, caseID string
	status     string
	step       string
	fields     map[string]string
}

func (r *myRun) ID() string                 { return r.id }
func (r *myRun) CaseID() string             { return r.caseID }
func (r *myRun) Status() string             { return r.status }
func (r *myRun) SetStatus(s string) error   { r.status = s; return nil }
func (r *myRun) CurrentStep() string        { return r.step }
func (r *myRun) SetCurrentStep(s string) error { r.step = s; return nil }
func (r *myRun) Field(k string) (string, bool)  { v, ok := r.fields[k]; return v, ok }
func (r *myRun) SetField(k, v string) error { r.fields[k] = v; return nil }

// ② 环节推进出口：编排只说「开一个环节 / 收口」，落库由你做。
type myStepSink struct{ repo *Repo }

func (s myStepSink) Open(name, label string) (contract.StepRef, error) {
	return s.repo.insertStep(name, label) // 建行 + 状态迁移到 running
}
func (s myStepSink) Finish(ref contract.StepRef, status string) error {
	return s.repo.finishStep(ref.ID, status)
}

// ③ 环节状态机：纯谓词件，先快速失败，DB 侧仍有条件 UPDATE 兜底。
if err := statemachine.MustTransition(from, to); err != nil {
	return err // 非法转移
}
```

设计取舍值得先理解：**编排语义定死、领域字段开放**——纯开放键契约太松、两边必然漂移；
纯定死则每加一个领域概念都要改内核。定死 + 开放键的混合设计让约 80% 的编排逻辑可下沉，
剩下 20% 留在宿主适配层。注意 `SetField` 遇未知键**返回错误**而不是静默吞——
宿主可以 fail-closed 拒绝，但不能悄悄把数据丢了。

**坑**：`ToolBinder.Tools` 的 server 级失败必须显式报错，不得静默返回空面——
空面会让编排把「没工具」误判成「工具失败」，两者处置完全不同。
`statemachine` 的终态包含 `interrupted`（中止 ≠ 失败，区别于「执行了但失败」）。

### 2 · 报告协议：`protocol`

模型输出的 JSON 不可信。`protocol` 给的是一条**五级容错解析 + 白名单校验 + 渲染**
的流水线，核心思路是「能修就修，但该拦的拦死」：

```go
// ① 解析：宿主解析 LLM 产出**必须用 ParseLLMReport**
r, err := protocol.ParseLLMReport(raw)
if errors.Is(err, protocol.ErrNotReport) {
	return nil, err // 降级读 markdown 主体，不阻塞聚合
}

// ② 白名单校验：warn 只留痕，critical 才拦截
warns, err := r.Validate()
if errors.Is(err, protocol.ErrViolation) {
	return nil, err // 整份报告被拦
}

// ③ 渲染：标题走注入，内核只留中性兜底
view := protocol.RenderViewWith(r, protocol.ViewSummary,
	protocol.RenderOptions{}.WithTitle("你的品牌标题"))
```

**坑**：`ParseReport` 与 `ParseLLMReport` **不是别名**——前者保留引擎侧字段，是落库
往返入口；后者剥离它们。**解析 LLM 产出必须用 `ParseLLMReport`**，走错等于把呈现权
交给模型。解析输入有 2 MiB 上限，超限**判非报告**（fail-closed，不截断）。
经修复通道救回的报告 `Confidence` 被钳为 `low`，下游 `needs_review` 语义据此承接。

### 3 · 工具判定环：`tooldisclose` + `guardset` + `toolfail`

**任何一次工具调用，都必须过同一套判定环**——包括 `ptc` 程序里模型自己写的
`host.<tool>()`：

```
普通工具调用 ─┐
             ├─→ invoke.Stage 链 ─→ guardset(并求值·合成拒绝) ─→ tooldisclose(披露门)
ptc 程序内   ─┘                            └─→ policy(审计门) ─→ dispatch(派发拓扑)
```

少接一段，那个位置就成了整条链上唯一绕过所有门禁的路径。要点：

- `tooldisclose` **渐进式披露**：工具面按需展开，「拒绝即激活」是正常行为——
  模型第一次调被拒、第二次同参重试就过，看 `Retryable(err)`；
- `guardset` **多守卫合成**：一组相互独立的判定**一并求值**，多条拒绝合成一条，
  可重试取「全部可重试才算」的保守侧，并点出该先处理哪一条——不要用逐个过、
  第一个拒绝就收口的链（症状：同一个工具反复被拒、每次理由都不同）；
- `toolfail` **失败是数据表**：类别 → 可重试性 / 退避 / 需人工 / **重发安全** /
  模型指引。重发安全与重试价值是两个正交维度——只看 `Retryable` 不看
  `SafeToResubmit`，工具重试会把一次副作用变成两次；
- `policy.Stage` 把**审批门接进判定环**：工具调用、程序内调用、子进程侧调用
  走同一条 invoke 链，审批规则只装配一次。

### 4 · 续跑契约：`checkpoint` + `commitgate`

恢复判定做成**纯函数**（`checkpoint.BuildPlan`），三条硬规则：

1. **可信边界按依赖判定**——默认只信连续完成的前缀，失序时宁可多跑几步；
   并行组显式声明依赖后按各自前驱采信；
2. **终态不自动重开**——拒绝替宿主决定；
3. **序号单调闸**——重启后要 `SeedFromStore` 回灌，否则 `SeqGate` 重启即空
   （症状：陈旧快照写畅通无阻）。

`commitgate` 管另一半：同一 (run, 环节) 的终态**只允许被提交一次**——
`statemachine` 是纯谓词只答「这次迁移合不合法」，排不出「两条并发分支都赢了」。
**放闸靠凭证**：`Acquire` 返回唯一令牌、`ReleaseLease` 凭令牌释放；
落库失败后必须调 `Release`（症状：某个环节再也提交不了终态）。

### 5 · 上下文三件套：`budget` + `spill` + `offload`

- `budget` **输入侧分段计量 + 压实计划**：只给总数无法区分「工具面膨胀」与
  「历史膨胀」，而两者处置完全不同。压实后要显式 `Meter.Shrink` 把账改回来。
  要减账用 `NetRemovedTokens`（净），别用 `GrossRemovedTokens`（毛）；
- `spill` **输出溢出存档**：截断 + 分页取回，提示契约定死；
- `offload` **大对象两阶段外置**：首次**只标记不替换**——直接替换会让模型引用一个
  自己从没看过的东西；落盘失败不得标记（症状：模型引用了没见过的图片）。

### 6 · 程序化编排：`ptc`

**N 轮 ReAct 往返塌缩成 1 轮**：模型写程序 → 沙箱 VM 执行 → 程序内经 bindings
调宿主工具，只把归并后的小结果带回上下文——**原始数据不过模型面**。
三重预算（墙钟 / 调用数 / 字节数）替代 OS 沙盘，纯 Go 零 CGO。

- 程序内的 `host.<tool>()` 走同一套 Stage 判定环（`ptc.Options.Stages` 不传 =
  绕过判定环，这是坑）；
- `sandbox` 管隔离档位：声明兑现 `MaxMemory` 的后端（子进程 + cgroup v2）建不出
  即拒绝执行，档位要求逐次声明、登记簿统一登记。

## 一个最小闭环

如果只想先验证「接得进来」，从这三个零门槛的点开始：

```go
// ① 派发守卫：宿主提供拓扑，harness 提供机制
type myEdgeSource struct{ reg *myRegistry }

func (s myEdgeSource) Slugs() []string                    { return s.reg.Slugs() }
func (s myEdgeSource) DispatchAllow(slug string) []string { return s.reg.Allow(slug) }

g := dispatch.NewGuard(myEdgeSource{reg: reg}, 5) // 5 = 最深派发链上限
if err := g.Assert("caller", "callee", 0); err != nil {
	// 结构化 *dispatch.DenyError，按 DenyError.Reason 常量判定，不转述
}

// ② 报告解析 + 渲染（见上文 §2 三步）
// ③ 状态机纯谓词（见上文 §1 ③）
```

## 升级须知

破坏性变更逐版记在仓内
[README「下游升级须知」](https://github.com/yi-nology/goagent-kernel/blob/main/README.md#下游升级须知破坏性变更)。
升级前扫一眼那张表；判读口径变化（如解析行为）也会写明改判方式。

## 下一步

- [能力详解](./capabilities.md)——九组能力的完整清单与选型；
- [坑速查](../reference/pitfalls.md)——接完线自查一遍；
- 仓内 [docs/USAGE.md](https://github.com/yi-nology/goagent-kernel/blob/main/docs/USAGE.md)
  ——逐包的「职责 → 集成片段 → 关键符号 → 坑」，包括 eventbus / delta / drain /
  netguard / pii / sideeffect 等本文没展开的组。
