// otelmap.go —— 轨迹行 → OTel 属性的**契约表**（属性名 / 来源字段 / 取值域 / 基数上界）。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 为什么单独一张表
//
// otel.go 的映射函数回答「这一行导出去长什么样」，这张表回答「**宿主可以依赖什么**」。
// 分开是因为失效形态不同：映射写错会出可见的错值；契约缺失**不会报错**——宿主建看板时
// 只能自己猜字段名，于是同一个语义在 A 宿主叫 @turn_id@、在 B 宿主叫 @turn@，跨宿主的
// 看板与告警全部落空，而遥测数据照常上行。
//
// # 判据：每一项都必须「有主」
//
// 判据在 @otelmap_test.go@，且是**双向**的：映射函数写出了表里没有的属性 ⇒ 红；表里列了
// 映射函数产不出的属性 ⇒ 也红（后者防的是「表变成了许愿池」）。另逐项判四件事：来源字段
// 非空、取值域写出、基数上界写出依据、属性名唯一。缺一即红。
//
// # Go 类型为什么进契约
//
// 取值域的形态写的是 **Go 类型名**（string / int64 / float64 / bool）：宿主导出时按类型
// 选 OTel 属性类型，类型不符的属性在多数后端里**静默丢弃**（不报错、不告警），表现为
// 「数据看起来上行成功、看板上就是没有」。写成 Go 类型名让用例拿反射直接比对映射函数的
// 真实产物——这是「表」与「代码」之间唯一不会漂的对拍方式。
//
// # 哪些字段不许进 metric label
//
// 只有**本仓枚举得出、也给得出上界**的属性才允许作 metric label（@LabelSafe@ 蕴含
// 取值域是枚举、且 @Card.Max@ 不小于取值数）。理由不是洁癖：label 的代价是时序数按取值数
// **乘**上去，一个无界 label 就能让指标后端按会话数膨胀。@session_id@ / @agent_slug@ 是
// 典型，列在 @ForbiddenLabels@ 里——而它们**根本不在 Row 上**：结构上装不进去，不靠纪律拦。
package trajectory

// TraceGranularity trace 的切分单位。
type TraceGranularity string

const (
	// TracePerTurn 一轮 turn 一个 trace（**本仓选它**）。
	TracePerTurn TraceGranularity = "turn"
	// TracePerStep 一个环节一个 trace（本仓不选：行标识只在轮内唯一，
	// 同号环节会在两个 trace 里撞同一个标识）。
	TracePerStep TraceGranularity = "step"
)

// TraceBoundary 本仓选定的 trace 切分单位。
//
// 依据见 otel.go 头注「trace 边界」：选「轮」不是习惯，是因为 Row.ID 的唯一性
// 范围恰好是轮内——把边界挪到环节，标识唯一性与 trace 内唯一性就不再是同一个范围。
const TraceBoundary = TracePerTurn

// Domain 属性取值域。
type Domain struct {
	// Kind 取值形态，取 **Go 类型名**（string / int64 / float64 / bool）。
	// 见头注「Go 类型为什么进契约」。
	Kind string
	// Values 枚举取值（按本仓词表顺序）；空 = 非枚举。
	Values []string
	// Note 取值域说明。非枚举必写（枚举的取值域已由 Values 表达完整）。
	Note string
}

// Cardinality 属性基数上界。
type Cardinality struct {
	// Max 本仓能保证的取值上界；0 = 本仓定不出上界。
	Max int
	// Why 上界依据（Max 为 0 时写清为什么定不出）。
	Why string
}

// AttrSpec 一个属性的映射契约。
type AttrSpec struct {
	// Name 属性名（必是 otel.go 里的某个 Attr* 常量，且全表不重复）。
	Name string
	// Source 来源字段：Row 上的哪一个字段（派生值写清怎么算出来的）。
	Source string
	// Domain 取值域。
	Domain Domain
	// Card 基数上界。
	Card Cardinality
	// LabelSafe 能否作为 metric label；为真必须「枚举取值 + 本仓给得出上界」。
	LabelSafe bool
}

// AttrSpecs 映射契约表（顺序 = otel.go 的常量声明顺序，便于逐行对读）。
var AttrSpecs = []AttrSpec{
	{
		Name:   AttrRowID,
		Source: "Row.ID",
		Domain: Domain{Kind: "string", Note: "行标识；形态由投影器决定（turn#N / step#N / tool#<call_id> / <kind>#-<seq>），唯一范围是轮内"},
		Card:   Cardinality{Why: "取值随行数增长（行数由事件流长度决定），本仓定不出上界"},
	},
	{
		Name:   AttrKind,
		Source: "Row.Kind",
		Domain: Domain{Kind: "string", Values: []string{
			string(KindTurn), string(KindStep), string(KindToolCall), string(KindToolResult),
			string(KindSideEffect), string(KindCheckpoint),
		}},
		Card:      Cardinality{Max: 6, Why: "Kind 词表固定六值（与 Span.Name 同一词表：新增类目必须同步这里与用例）"},
		LabelSafe: true,
	},
	{
		Name:   AttrRowStatus,
		Source: "Row.Status",
		Domain: Domain{Kind: "string", Values: []string{
			string(StatusOpen), string(StatusClosed), string(StatusOrphan),
		}},
		Card:      Cardinality{Max: 3, Why: "Status 词表固定三值"},
		LabelSafe: true,
	},
	{
		Name:   AttrTurnSeq,
		Source: "Row.Turn",
		Domain: Domain{Kind: "int64", Note: "1 起的轮次序号；轮次外的事件（Turn 为 0）不写此项"},
		Card:   Cardinality{Why: "随会话轮数增长，本仓定不出上界"},
	},
	{
		Name:   AttrStepID,
		Source: "Row.StepID（Row.HasStep 为真时才写：环节号 0 是合法值，不能靠零值判有无）",
		Domain: Domain{Kind: "int64", Note: "环节号，由调用方给定（uint64 全域）"},
		Card:   Cardinality{Why: "取值由调用方决定，本仓定不出上界"},
	},
	{
		Name:   AttrToolName,
		Source: "Row.Tool（仅工具行）",
		Domain: Domain{Kind: "string", Note: "工具名，来自工具调用事件载荷"},
		Card:   Cardinality{Why: "取值数由宿主的工具目录规模决定，本仓看不到那个目录；宿主若要按工具聚合，应在 label 侧用自己的白名单收敛"},
	},
	{
		Name:   AttrToolCallID,
		Source: "Row.CallID（仅工具行）",
		Domain: Domain{Kind: "string", Note: "配对键：一次工具调用一个值，宿主跨 span join 用"},
		Card:   Cardinality{Why: "调用次数无上界"},
	},
	{
		Name:   AttrToolPairing,
		Source: "Row.Pairing（仅工具行，且不为 PairingNone）",
		Domain: Domain{Kind: "string", Values: []string{
			string(PairingPaired), string(PairingMissingResult), string(PairingDuplicateResult),
			string(PairingUnpairedResult), string(PairingDuplicateCall),
		}},
		Card:      Cardinality{Max: 5, Why: "Pairing 词表五值（PairingNone 是「不适用」，不写进属性）"},
		LabelSafe: true,
	},
	{
		Name:   AttrToolDupResults,
		Source: "Row.DupResults（仅工具行，且 >0）",
		Domain: Domain{Kind: "int64", Note: "重复结果计数，1 起；没有重复就没有这个属性"},
		Card:   Cardinality{Why: "重复次数无上界（正常情况下恒为 1）"},
	},
	{
		Name:   AttrDurationMs,
		Source: "Row.Duration()（StartAt 与 EndAt 相减，不是单独存的字段）",
		Domain: Domain{Kind: "float64", Note: "实测毫秒；**仅实测时出现**——没有实测就没有这个属性，不写 0、不估一个"},
		Card:   Cardinality{Why: "连续值，本仓定不出上界"},
	},
	{
		Name:   AttrSummary,
		Source: "Row.Summary（可由 Options.Summarize 覆盖）",
		Domain: Domain{Kind: "string", Note: "宿主注入的展示文案；内核默认标签不含任何领域词"},
		Card:   Cardinality{Why: "自由文本，本仓定不出上界"},
	},
	// ── 副作用行（v0.33.0，issue #99）：「这个副作用到底施加了吗」的属性面 ──
	{
		Name:   AttrSideEffectKey,
		Source: "Row.SideEffectKey（仅副作用行）",
		Domain: Domain{Kind: "string", Note: "幂等键：一个逻辑动作一个值（宿主构造，须稳定）；同 Key 的授予段/对账段/点观察共用一个行标识，宿主跨 span join 用"},
		Card:   Cardinality{Why: "动作数无上界（随运行规模增长）"},
	},
	{
		Name:   AttrSideEffectPhase,
		Source: "Row.SideEffectPhase（仅副作用行）",
		Domain: Domain{Kind: "string", Values: []string{
			string(SideEffectGranted), string(SideEffectCompleted), string(SideEffectAbandoned),
			string(SideEffectUncertain), string(SideEffectResolvedApplied), string(SideEffectResolvedCleared),
			string(SideEffectReplayed), string(SideEffectContended),
		}},
		Card:      Cardinality{Max: 8, Why: "Phase 词表固定八值（授予/施加/放弃/转未知/裁决两态/两个点观察；与 sideeffect 的 Outcome 词表同源，新增须同步这里与用例）"},
		LabelSafe: true,
	},
	{
		Name:   AttrSideEffectResource,
		Source: "Row.SideEffectResource（仅副作用行）",
		Domain: Domain{Kind: "string", Note: "目标资源标识：租约的独占单位（宿主定义）"},
		Card:   Cardinality{Why: "资源数由宿主的领域决定，本仓定不出上界"},
	},
	{
		Name:   AttrSideEffectFence,
		Source: "Row.SideEffectFence（仅副作用行，且 >0；授予段与点行为才携带）",
		Domain: Domain{Kind: "int64", Note: "栅栏令牌：同一资源上单调递增的代数，由存储侧在原子操作里分配"},
		Card:   Cardinality{Why: "随接管次数增长，本仓定不出上界"},
	},
	{
		Name:      AttrSideEffectSafety,
		Source:    "Row.SideEffectSafety（仅副作用行，且非空——空声明不写，不替宿主补默认值）",
		Domain:    Domain{Kind: "string", Values: []string{"unknown", "safe", "unsafe"}},
		Card:      Cardinality{Max: 3, Why: "与 sideeffect.RetrySafety 的 String() 逐字一致（unknown=未声明，沉默按最弱处理）"},
		LabelSafe: true,
	},
	// ── 快照行（v0.33.0，issue #99）：「恢复点停在哪个序号」的属性面 ──
	{
		Name:   AttrCheckpointRunID,
		Source: "Row.CheckpointRunID（仅快照行）",
		Domain: Domain{Kind: "string", Note: "编排运行标识（宿主分配，须稳定且可跨进程重建）"},
		Card:   Cardinality{Why: "运行数随宿主的编排规模增长，本仓定不出上界"},
	},
	{
		Name:   AttrCheckpointSeq,
		Source: "Row.CheckpointSeq（仅快照行，且 >0——seq=0 的快照不是一次真实提交）",
		Domain: Domain{Kind: "int64", Note: "快照序号：同一 RunID 下严格递增（存储侧 CAS 判据）"},
		Card:   Cardinality{Why: "随步骤推进次数增长，本仓定不出上界"},
	},
	{
		Name:   AttrCheckpointStatus,
		Source: "Row.CheckpointStatus（仅快照行，且非空）",
		Domain: Domain{Kind: "string", Note: "快照的编排状态：取值域**应当**是 contract 包的状态常量集（created/running/waiting_approval/succeeded/failed/cancelled/timed_out），但值由宿主写入快照——本仓保证不了宿主不写别的，故不列 Values、不标 label-safe（对宿主写面的保证不该由本包代签）"},
		Card:   Cardinality{Why: "词表本身七值，但写入面在宿主，本仓给不出可保证的上界"},
	},
}

// AttrSpecOf 按属性名取契约（不存在时 ok=false）。
func AttrSpecOf(name string) (AttrSpec, bool) {
	for _, s := range AttrSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return AttrSpec{}, false
}

// ForbiddenLabel 明确禁止作为 metric label 的语义（宿主不得自行补进 label）。
type ForbiddenLabel struct {
	// Name 语义名。
	Name string
	// Why 为什么禁止。
	Why string
}

// ForbiddenLabels 禁区清单。
//
// 这三项都不在 Row 上——写在这里是给宿主的：它们看起来「很像维度」（也确实是有用的
// 关联键），但落进 metric label 就变成无界基数。要关联会话，用 trace_id（真相源在
// @oekit/tracing@），那是**标识**不是维度。
var ForbiddenLabels = []ForbiddenLabel{
	{
		Name: "session_id",
		Why:  "一次会话一个取值：进 label 会让指标时序数按会话数膨胀，且会话标识是可关联标识——进了指标后端就等于把它变成长期可查询的面",
	},
	{
		Name: "agent_slug",
		Why:  "取值数随部署的 agent 目录增长，本仓定不出上界；同一部署内它也更像维度而不是枚举",
	},
	{
		Name: "payload",
		Why:  "事件载荷：自由文本、基数无界，且可能含未脱敏内容——映射面刻意不读它（见 otel.go 头注「映射面不含 payload」）",
	},
}
