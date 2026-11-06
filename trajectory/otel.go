// otel.go —— 轨迹行的 **OTel 语义映射**（只出属性名与取值域，不做投递）。
//
// # 为什么映射在本仓、投递不在
//
// 参照系的分层是一致的：Codex 的 @otel@ 与 DSH 的 @session-telemetry-otel@ 都是
// 「**business 选事件、transport 负责编码与投递**」。本仓落在 business 那一侧——
// 它知道一行轨迹是什么意思（轮次 / 环节 / 工具调用），不知道宿主把 span 发给谁。
//
// 反过来做（本仓直连 OTLP）会带来两件本仓不该背的东西：多一个相当大的依赖面，
// 以及「导出失败算不算执行失败」这个判定。后者尤其危险——遥测后端抖一下就让
// 执行失败，是把可观测性做成了可用性风险。
//
// # 为什么必须有「约定」而不是让宿主各认字段
//
// 没有约定时，宿主只能自己把 @Row@ 的字段翻成属性名。于是同一个语义在不同宿主里
// 叫 @turn_id@ / @turn@ / @harness.turn@——跨宿主的看板与告警全部落空，而这
// **不会报错**：遥测数据照常上行，只是拼不起来。
//
// 属性名带 @harness.@ 前缀：本仓不是 OTel 语义约定的一部分，冒用 @gen_ai.*@
// 这类官方命名空间会让「按官方约定解析」的工具读到半真半假的数据。
//
// # 三个守住的纪律
//
//  1. **span 名取低基数**：只有 @turn@ / @step@ / @tool_call@ / @tool_result@ 四种。
//     工具名进**属性**而不是进名字——把工具名拼进 span 名会让 span 名的基数随
//     工具目录膨胀，而大多数后端的聚合与采样都按名字做。
//  2. **时长只在实测时写**：@duration_ms@ 仅当 @Row.Duration()@ 给出实测值才出现。
//     没有实测就不写这个属性——而不是写 0 或估一个。这条与 @Row@ 结构上不设
//     时长字段是同一件事：估算值不许冒充实测。
//  3. **状态三值，缺省是 unset 不是 error**：把「仍在途」记成 error，会让真正的
//     事故淹在噪声里。
//
// # 映射面不含 payload
//
// 本文件只吃 @Row@ 的**结构字段**（标识 / 时刻 / 状态 / 配对），不碰任何事件载荷。
// 故这条通路天然不需要额外的脱敏层——不是「顺手脱敏了」，是**没有那条数据**。

package trajectory

import (
	"time"
)

// OTel 属性名（本仓命名空间）。宿主据此建 dashboard 与告警；改名即破坏兼容。
const (
	// AttrRowID 行标识（与 Row.ID 同值）。
	AttrRowID = "harness.trajectory.row_id"
	// AttrKind 行类目（turn / step / tool_call / tool_result）。
	AttrKind = "harness.trajectory.kind"
	// AttrRowStatus 行的收口状态（open / closed / orphan）。
	AttrRowStatus = "harness.trajectory.status"
	// AttrTurnSeq 所属轮次序号（1 起；轮次外的事件不写此项）。
	AttrTurnSeq = "harness.turn.seq"
	// AttrStepID 所属环节号（仅带环节号的行）。
	AttrStepID = "harness.step.id"
	// AttrToolName 工具名（仅工具行；**属性**而不是 span 名，见头注纪律 1）。
	AttrToolName = "harness.tool.name"
	// AttrToolCallID 工具调用标识（配对键，宿主跨 span join 用）。
	AttrToolCallID = "harness.tool.call_id"
	// AttrToolPairing 配对状态（paired / missing_result / duplicate_result / …）。
	AttrToolPairing = "harness.tool.pairing"
	// AttrToolDupResults 重复结果计数（>0 才写）。
	AttrToolDupResults = "harness.tool.duplicate_results"
	// AttrDurationMs 实测时长（毫秒，float64）。**仅实测时出现**（见头注纪律 2）。
	AttrDurationMs = "harness.trajectory.duration_ms"
	// AttrSummary 行摘要（宿主可用 Options.Summarize 覆盖；空则不写）。
	AttrSummary = "harness.trajectory.summary"
)

// SpanStatus span 状态（OTel 的三值词表）。
type SpanStatus string

const (
	// SpanStatusUnset 未定：这一行还没收口，或「仍在等结果」。
	//
	// 缺省就是这个——把未定硬记成 error 会让真正的事故淹在噪声里。
	SpanStatusUnset SpanStatus = "unset"
	// SpanStatusOK 已完成且没有观察到问题。
	SpanStatusOK SpanStatus = "ok"
	// SpanStatusError 观察到**发生过**的问题（不是「还没等到」）。
	SpanStatusError SpanStatus = "error"
)

// Span 一行轨迹的 span 表达（不含 exporter 相关字段：那是投递侧的事）。
type Span struct {
	// Name span 名（低基数，取值域见 Kind）。
	Name string
	// Status span 状态。
	Status SpanStatus
	// StartAt 起点（未收口时为零值，宿主导出时可自行取当前时刻）。
	StartAt time.Time
	// EndAt 终点（未收口时为零值）。
	EndAt time.Time
	// Attributes 属性表（键取本文件的 Attr* 常量，值是 string/int64/float64）。
	Attributes map[string]any
}

// SpanOf 把一行轨迹映射成一个 span。
//
// 纯函数、不分配时间戳（不读 @time.Now()@）：导出时刻由宿主决定，
// 内核读时钟会让同一份轨迹在两次导出里得出不同的时间——那是第二个真相。
func SpanOf(r Row) Span {
	s := Span{
		Name:    string(r.Kind),
		Status:  spanStatusOf(r),
		StartAt: r.StartAt,
		EndAt:   r.EndAt,
		Attributes: map[string]any{
			AttrKind:      string(r.Kind),
			AttrRowStatus: string(r.Status),
		},
	}
	if r.ID != "" {
		s.Attributes[AttrRowID] = r.ID
	}
	if r.Turn > 0 {
		s.Attributes[AttrTurnSeq] = int64(r.Turn)
	}
	if r.HasStep {
		s.Attributes[AttrStepID] = int64(r.StepID)
	}
	if r.Kind == KindToolCall || r.Kind == KindToolResult {
		if r.Tool != "" {
			s.Attributes[AttrToolName] = r.Tool
		}
		if r.CallID != "" {
			s.Attributes[AttrToolCallID] = r.CallID
		}
		if r.Pairing != PairingNone {
			s.Attributes[AttrToolPairing] = string(r.Pairing)
		}
		if r.DupResults > 0 {
			s.Attributes[AttrToolDupResults] = int64(r.DupResults)
		}
	}
	// 时长：唯一来源是起止相减（见 Row.Duration）。没有实测就**没有这个属性**。
	if d, ok := r.Duration(); ok {
		s.Attributes[AttrDurationMs] = float64(d) / float64(time.Millisecond)
	}
	if r.Summary != "" {
		s.Attributes[AttrSummary] = r.Summary
	}
	return s
}

// SpansOf 批量映射（保持输入顺序 = 行序）。
func SpansOf(rows []Row) []Span {
	out := make([]Span, 0, len(rows))
	for _, r := range rows {
		out = append(out, SpanOf(r))
	}
	return out
}

// spanStatusOf 判一行的 span 状态。
//
// 判据只看两件事：**发生过什么问题**（error）与**是否收口**（ok）。
// 其余一律 unset——包括 @missing_result@：
// 「还没等到结果」与「结果重复了」是两件性质完全不同的事，前者可能仍在途。
func spanStatusOf(r Row) SpanStatus {
	switch r.Pairing {
	case PairingDuplicateResult, PairingDuplicateCall, PairingUnpairedResult:
		// 这三类都是**已经发生**的不一致：重复结果可能意味着副作用被施加了两次
		// （与 sideeffect 的「至多一次」正面冲突），结果无对应调用则说明有一次
		// 调用从未被声明。
		return SpanStatusError
	}
	if r.Pairing == PairingMissingResult {
		return SpanStatusUnset
	}
	if r.Complete() && (r.Kind != KindToolCall || r.Pairing == PairingPaired) {
		return SpanStatusOK
	}
	return SpanStatusUnset
}
