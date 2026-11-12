// Package metrics 内核指标契约面（issue #100）：指标名 / 类型 / 维度 / 基数上界的
// **单一来源**。
//
// 生命周期: Experimental（v0.34.1 定级）——口径面仍会随宿主看板的需求增补，
// 不承诺任何兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 为什么是「契约面」而不是一个 SDK
//
// 与 trajectory 的 OTel 映射同一条分工：**business 选指标、transport 负责编码与
// 投递**。本仓知道一个量是什么意思（执行耗时 / 重试 / 租约竞争 / 恢复成败 /
// 未知结果积压），不知道宿主把数投给谁（OTel / Prometheus / 自建看板）。本包若直连
// 任何 SDK，就会把「导出失败算不算执行失败」这类可用性问题引进内核——遥测后端
// 抖一下就影响执行，正是 trajectory/otel.go 头注拒绝的那条路。
//
// 没有约定时各宿主自造指标名，同一个「租约竞争次数」在不同系统里可以是不同含义，
// 跨宿主的看板与查询全部拼不起来——而这**不会报错**：数照常上行，只是拼不起来。
// 故名称与标签在这里写成常量，宿主的看板按本包的口径查询，本包按口径埋点。
//
// # 命名与口径纪律（与 trajectory 同源）
//
//  1. **前缀 @harness.@**：与 trajectory 的属性命名同一条理由——本仓不在 OTel
//     官方命名空间里，冒用官方前缀会让「按官方约定解析」的工具读到半真半假的数据。
//  2. **耗时单位毫秒（ms）且只记实测**：与 trajectory.AttrDurationMs 同一口径——
//     起止相减现算，估算值不许冒充实测（trajectory「结构上不设时长字段」的镜像）。
//  3. **无界字段不进标签**：与 trajectory/otelmap.go 的基数纪律同源。session_id /
//     run_id / task_id / 幂等键这类值进标签就是无界基数——指标时序数按业务量
//     膨胀，后端先爆；要关联用 trace_id（真相源在 oekit/tracing），那是**标识**
//     不是维度。禁区清单见 ForbiddenLabels。
//  4. **先登记再埋点**：每个指标的名称 / 类型 / 维度 / 基数上界在 Specs 里登记，
//     自校验（metrics_test）保证登记表自洽。埋一个没登记的指标 = 看板口径外
//     的暗数，Validate 会显式报错。
//
// # 覆盖清单（issue #100 验收的五类量）
//
// 	| 量             | 指标                        | 类型      |
// 	|----------------|-----------------------------|-----------|
// 	| 执行耗时       | harness.exec.duration       | histogram |
// 	| 重试次数       | harness.retry.total         | counter   |
// 	| 租约竞争       | harness.lease.contended     | counter   |
// 	| 恢复成功率     | harness.recovery.total      | counter   |
// 	| 未知结果积压   | harness.sideeffect.uncertain| gauge     |
//
// 各量的**接入点**（在哪个包哪个函数上埋）见 [ACCESS-POINTS.md]。
package metrics

import "slices"

// ── 指标名 ──────────────────────────────────────────────────────────────────
//
// 名字是契约：看板与查询按它对应，改名 = 看板静默断链。新增指标先登记 Specs
// 再埋点；改名走 BREAKING（Experimental 阶段允许，但要同步 ACCESS-POINTS.md）。
const (
	// NameExecDuration 一次编排单元的实测耗时（直方图，ms）。
	// 口径与 trajectory.AttrDurationMs 对齐：起止相减现算，只记实测。
	NameExecDuration = "harness.exec.duration"
	// NameRetryTotal 重试决策计数（计数器）。每次 retryplan.Decide 落地一条。
	NameRetryTotal = "harness.retry.total"
	// NameLeaseContended 租约竞争 / 未知结果拒绝计数（计数器）。
	// 「没轮上我」与「须先对账」都进这里——两者都是「这次没执行成」的竞争面信号。
	NameLeaseContended = "harness.lease.contended"
	// NameRecoveryTotal 恢复尝试计数（计数器，按 outcome 分成功 / 失败）。
	// 恢复成功率 = outcome=success 与 outcome=failure 的比值，不在本包另算——
	// 派生量只算一遍（宿主看板算），内核不产出第二个真相。
	NameRecoveryTotal = "harness.recovery.total"
	// NameUncertainBacklog 未知结果积压量（仪表）：当前处于「结果未知」态的
	// 副作用 Key 数。这是本轮 P0/P1 的核心运营指标——积压不清偿 = 有动作
	// 可能已施加而无人知晓。
	NameUncertainBacklog = "harness.sideeffect.uncertain"
)

// ── 标签键 ──────────────────────────────────────────────────────────────────
//
// 标签键同样有界：每个键的取值域在 Specs 的 LabelDim 里写死。
const (
	// LabelExecKind 执行单元类目。取值对齐 trajectory 的行类目（Kind* 常量）：
	// 轨迹上已经按这个类目分了 span，指标沿用同一枚举，看板两边拼得起来。
	LabelExecKind = "harness.exec.kind"
	// LabelExecStatus 执行语义状态（三值，与 trajectory 状态三值纪律同源：
	// 「仍在途」记 unset 不记 error——把在途记成 error 会把事故淹进噪声里）。
	LabelExecStatus = "harness.exec.status"
	// LabelRetryPath 重试路径，取值 = retryplan.Path 的三个值。
	LabelRetryPath = "harness.retry.path"
	// LabelLeaseSubsystem 租约竞争发生在哪个子系统。
	LabelLeaseSubsystem = "harness.lease.subsystem"
	// LabelLeaseOutcome 租约竞争的拒绝形态。
	LabelLeaseOutcome = "harness.lease.outcome"
	// LabelRecoverySubsystem 恢复发生在哪个子系统。
	LabelRecoverySubsystem = "harness.recovery.subsystem"
	// LabelRecoveryOutcome 恢复结局（success / failure 两值；blocked 也是一种
	// failure——按「没恢复成」计）。
	LabelRecoveryOutcome = "harness.recovery.outcome"
)

// ── 有界枚举（标签取值域）────────────────────────────────────────────────────

// 执行单元类目：逐字对齐 trajectory 的行类目（KindTurn/…/KindCheckpoint 的
// 字符串值）。metrics_test 会拿 trajectory 的常量做逐字比对，口径漂移即红。
const (
	ExecKindTurn       = "turn"
	ExecKindStep       = "step"
	ExecKindToolCall   = "tool_call"
	ExecKindToolResult = "tool_result"
	ExecKindSideEffect = "sideeffect"
	ExecKindCheckpoint = "checkpoint"
)

// 执行语义状态三值（「缺省是 unset 不是 error」与 trajectory 同源）。
const (
	ExecStatusUnset = "unset"
	ExecStatusOK    = "ok"
	ExecStatusError = "error"
)

// 重试路径：逐字对齐 retryplan.Path 的 String()（none / direct / replayed）。
const (
	RetryPathNone     = "none"
	RetryPathDirect   = "direct"
	RetryPathReplayed = "replayed"
)

// 租约子系统。
const (
	LeaseSubsystemSideEffect = "sideeffect"
	LeaseSubsystemTaskBoard  = "taskboard"
)

// 租约拒绝形态：contended=「还没轮上我」（等一等再来），
// uncertain=「可能已施加，须先对账」（硬阻塞）。语义来自 sideeffect.Outcome。
const (
	LeaseOutcomeContended = "contended"
	LeaseOutcomeUncertain = "uncertain"
)

// 恢复子系统。
const (
	RecoverySubsystemCheckpoint = "checkpoint"
	RecoverySubsystemSideEffect = "sideeffect"
)

// 恢复结局。blocked（计划判 blocked）也计 failure——「没恢复成」只有一种计法，
// 看板才不用先学会内核的细分语义。
const (
	RecoveryOutcomeSuccess = "success"
	RecoveryOutcomeFailure = "failure"
)

// ── 禁区标签 ────────────────────────────────────────────────────────────────

// ForbiddenLabel 明确禁止作为 metric label 的键（宿主不得自行补进 label）。
type ForbiddenLabel struct {
	Name string
	Why  string
}

// ForbiddenLabels 禁区清单。
//
// 前三项与 trajectory.ForbiddenLabels 同源（同一批「看起来很像维度」的无界键）；
// 后三项是本包补的内核侧键：run_id / task_id 随业务量无界增长，key / resource
// 是幂等键与资源标识——基数等于业务写入量，进标签后端必爆。要关联它们，
// 走日志 / 轨迹（trace_id / 行 ID），不走指标标签。
var ForbiddenLabels = []ForbiddenLabel{
	{Name: "session_id", Why: "一次会话一个取值：进 label 会让指标时序数按会话数膨胀，且会话标识是可关联标识"},
	{Name: "agent_slug", Why: "取值数随部署的 agent 目录增长，本仓定不出上界"},
	{Name: "payload", Why: "自由文本、基数无界，且可能含未脱敏内容"},
	{Name: "run_id", Why: "编排运行标识，随业务量无界增长；关联走轨迹的 trace_id（轮边界）"},
	{Name: "task_id", Why: "任务标识随业务量无界增长；关联走日志与 taskboard 的 List 面"},
	{Name: "key", Why: "副作用幂等键，基数等于业务写入量；积压量只报 gauge 总数，明细走日志"},
	{Name: "resource", Why: "资源标识随业务量无界增长，同 key"},
}

// ── 登记表 ──────────────────────────────────────────────────────────────────

// Kind 指标类型（OTel 数据模型里本仓用到的三类）。
type Kind string

const (
	// KindCounter 只增计数。
	KindCounter Kind = "counter"
	// KindGauge 可升可降的瞬时量。
	KindGauge Kind = "gauge"
	// KindHistogram 分布观测（本仓只用它记毫秒耗时）。
	KindHistogram Kind = "histogram"
)

// LabelDim 一个标签维度：键、有界取值域、基数上界与**依据**。
//
// 基数上界没写依据 = 声明自相矛盾（与 trajectory/otelmap_test 的判据同源）：
// 「上界是几」与「为什么敢这么上界」是两件事，后者才是让人敢在阈值判定里依赖它的理由。
type LabelDim struct {
	// Key 标签键（Label* 常量之一）。
	Key string
	// Values 有界取值域（封闭枚举；基数上界 = len(Values)）。
	Values []string
	// Why 这套取值域为什么封闭（为什么将来不会悄悄涨）。
	Why string
}

// Card 基数上界（= len(Values)；单列一个方法是为了让「上界有主」可机器判定）。
func (d LabelDim) Card() int { return len(d.Values) }

// Metric 一个指标的登记项。
type Metric struct {
	// Name 指标名（Name* 常量之一）。
	Name string
	// Kind 指标类型。
	Kind Kind
	// Unit 单位（耗时恒 ms；计数与仪表用 "1"——OTel 约定无量纲写作 "1"）。
	Unit string
	// Desc 看板读得懂的语义说明（写清「数的是什么」而不是「在哪里埋的」）。
	Desc string
	// Labels 维度（无维度时为 nil——零维指标不是缺陷，积压总量就该是零维）。
	Labels []LabelDim
}

// specs 登记表本体。**先登记再埋点**：新增指标在这里加一行 + ACCESS-POINTS.md
// 加一个接入点，自校验才认。
var specs = []Metric{
	{
		Name: NameExecDuration,
		Kind: KindHistogram,
		Unit: "ms",
		Desc: "一次编排单元（轮 / 环节 / 工具调用 / 副作用 / 快照落库）的实测耗时。" +
			"口径与轨迹属性 " + "harness.trajectory.duration_ms" + " 同源：起止相减现算，" +
			"只记实测，估算值不许冒充实测。",
		Labels: []LabelDim{
			{
				Key: LabelExecKind,
				Values: []string{
					ExecKindTurn, ExecKindStep, ExecKindToolCall,
					ExecKindToolResult, ExecKindSideEffect, ExecKindCheckpoint,
				},
				Why: "逐字对齐 trajectory 的行类目（Kind* 常量），轨迹上加类目才加这里——两份枚举不许各自长",
			},
			{
				Key:    LabelExecStatus,
				Values: []string{ExecStatusUnset, ExecStatusOK, ExecStatusError},
				Why:    "状态三值纪律与 trajectory 同源：在途记 unset 不记 error，事故不被在途噪声淹没",
			},
		},
	},
	{
		Name: NameRetryTotal,
		Kind: KindCounter,
		Unit: "1",
		Desc: "重试决策计数。每次 retryplan.Decide 落地一条（按 path 分）——数的是" +
			"「决定走哪条路」，不是「实际重发了几次」：后者是宿主执行面的事，内核不猜。",
		Labels: []LabelDim{
			{
				Key:    LabelRetryPath,
				Values: []string{RetryPathNone, RetryPathDirect, RetryPathReplayed},
				Why:    "取值 = retryplan.Path 的 String()，路径枚举加值须两边同步（metrics_test 比对）",
			},
		},
	},
	{
		Name: NameLeaseContended,
		Kind: KindCounter,
		Unit: "1",
		Desc: "租约竞争拒绝计数：「这次没执行成」的竞争面信号。contended=等一等再来，" +
			"uncertain=须先对账（硬阻塞）。两类合看就是「竞争压力」与「对账压力」。",
		Labels: []LabelDim{
			{
				Key:    LabelLeaseSubsystem,
				Values: []string{LeaseSubsystemSideEffect, LeaseSubsystemTaskBoard},
				Why:    "租约面只有 sideeffect（副作用闸）与 taskboard（任务认领）两个子系统；加新租约面才算新值",
			},
			{
				Key:    LabelLeaseOutcome,
				Values: []string{LeaseOutcomeContended, LeaseOutcomeUncertain},
				Why:    "拒绝形态对齐 sideeffect.Outcome 的 Contended / Uncertain 二值",
			},
		},
	},
	{
		Name: NameRecoveryTotal,
		Kind: KindCounter,
		Unit: "1",
		Desc: "恢复尝试计数（按 outcome 分）。恢复成功率由看板用 success / failure 两时序" +
			"现算——内核不产出第二个真相（派生量只算一遍）。",
		Labels: []LabelDim{
			{
				Key:    LabelRecoverySubsystem,
				Values: []string{RecoverySubsystemCheckpoint, RecoverySubsystemSideEffect},
				Why:    "恢复面目前只有快照恢复与副作用对账两处；新增恢复路径才加值",
			},
			{
				Key:    LabelRecoveryOutcome,
				Values: []string{RecoveryOutcomeSuccess, RecoveryOutcomeFailure},
				Why:    "两值封闭：blocked 也计 failure——「没恢复成」只有一种计法，看板不用先学会内核细分语义",
			},
		},
	},
	{
		Name: NameUncertainBacklog,
		Kind: KindGauge,
		Unit: "1",
		Desc: "未知结果积压量：当前处于「结果未知」态的副作用 Key 数。" +
			"零维（无标签）——积压看总量与趋势，明细（哪个 Key）走日志与对账面，不进指标。",
	},
}

// Specs 返回登记表的副本（调用方改不动登记表——口径只有一处能改）。
func Specs() []Metric { return slices.Clone(specs) }

// SpecOf 按名取登记项。未登记的指标名 = 埋点面之外的暗数，Validate 会拦。
func SpecOf(name string) (Metric, bool) {
	for _, m := range specs {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

// forbiddenSet 禁区标签的集合视图（Validate 用；包级惰性一次构建）。
var forbiddenSet = func() map[string]ForbiddenLabel {
	set := make(map[string]ForbiddenLabel, len(ForbiddenLabels))
	for _, f := range ForbiddenLabels {
		set[f.Name] = f
	}
	return set
}()
