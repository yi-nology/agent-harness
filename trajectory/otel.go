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
//  1. **span 名取低基数**：只有 @turn@ / @step@ / @tool_call@ / @tool_result@ /
//     @sideeffect@ / @checkpoint@ 六种。工具名与幂等键进**属性**而不是进名字
//     ——把可变取值拼进 span 名会让名字的基数随工具目录 / 动作数膨胀，
//     而大多数后端的聚合与采样都按名字做。
//  2. **时长只在实测时写**：@duration_ms@ 仅当 @Row.Duration()@ 给出实测值才出现。
//     没有实测就不写这个属性——而不是写 0 或估一个。这条与 @Row@ 结构上不设
//     时长字段是同一件事：估算值不许冒充实测。
//  3. **状态三值，缺省是 unset 不是 error**：把「仍在途」记成 error，会让真正的
//     事故淹在噪声里。
//
// # trace 边界：一轮一个 trace
//
// @Span.ParentID@ 表达父子；@ParentID@ 为空的那一行是**根**——宿主应为它开一个
// 新 trace（@trace_id@ 的真相源在 @oekit/tracing@，本仓不生成 ID，只声明关系）。
//
// 边界定在**轮**（@TraceBoundary@），不是环节。依据不是习惯，是**行标识的唯一边界**：
// @Row.ID@ 只在轮内唯一（@step#7@ 在第一轮与第二轮各出现一次是合法的，投影器不把
// 轮次拼进环节号）。以环节为 trace 边界时，同号环节会在两个 trace 里获得同一个 span
// 标识，而工具行同时引用环节与轮次——按 OTel 语义，父 span 不在本 trace 内即断链。
// 以轮为边界时，「行 ID 唯一」与「trace 内标识唯一」是同一个范围，两条约束不打架。
//
// # 父子是**声明的**，悬空父要清成根
//
// 单行映射只能按行上的字段推父（工具行有环节号就挂环节，否则挂轮次），推出来的父
// 可能**在本批行里根本不存在**（环节行是孤儿，或调用方只导出了部分行）。故 @SpansOf@
// 做一次解析：父不在本批内就清空 @ParentID@，让这一行成为根，而不是导出一个指向
// 不存在 span 的断链。
//
// 「这一行声明属于哪个环节」这层事实不会因此丢失——@AttrStepID@ 照样写着。两个东西
// 说的是两件事：一个说「它属于谁」，一个说「导出时连得到谁」。
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

	// ── 副作用行（v0.33.0，issue #99）─────────────────────────────
	// 排障第一问「这个副作用到底施加了吗」的属性面。属性命名与既有三类同构：
	// 领域段（harness.sideeffect.*）+ 语义后缀。
	// AttrSideEffectKey 幂等键（配对键：同 Key 的授予段 / 对账段 / 点观察聚合到
	// 同一行标识下；宿主跨 span join 用）。无界标识——禁止作 metric label。
	AttrSideEffectKey = "harness.sideeffect.key"
	// AttrSideEffectPhase 副作用阶段（granted / completed / abandoned / uncertain /
	// resolved_applied / resolved_cleared / replayed / contended）。
	AttrSideEffectPhase = "harness.sideeffect.phase"
	// AttrSideEffectResource 目标资源标识（租约的独占单位）。无界——禁止作 metric label。
	AttrSideEffectResource = "harness.sideeffect.resource"
	// AttrSideEffectFence 栅栏令牌（授予段 / 点行为观察到的代数；非授予段不写）。
	AttrSideEffectFence = "harness.sideeffect.fence"
	// AttrSideEffectSafety 重试安全性声明（unknown / safe / unsafe，与
	// sideeffect.RetrySafety.String() 逐字一致；空声明不写——沉默按最弱处理，
	// 属性面不替宿主补默认值）。
	AttrSideEffectSafety = "harness.sideeffect.retry_safety"

	// ── 快照行（v0.33.0，issue #99）──────────────────────────────
	// 排障第二问「恢复点停在哪个序号」的属性面。
	// AttrCheckpointRunID 编排运行标识（宿主分配的稳定标识）。无界——禁止作 metric label。
	AttrCheckpointRunID = "harness.checkpoint.run_id"
	// AttrCheckpointSeq 快照序号（严格递增；0 不会出现——seq=0 的快照不是一次真实提交）。
	AttrCheckpointSeq = "harness.checkpoint.seq"
	// AttrCheckpointStatus 快照的编排状态（取值域见 contract 的状态常量集；
	// 本包**引用**那份词表而不复制——契约表里用 Note 指过去，抄一份就是第二真相）。
	AttrCheckpointStatus = "harness.checkpoint.status"
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
	// ParentID 父 span 的行标识（与 Row.ID 同源，见头注「trace 边界」）；
	// 空 = 根，宿主应为它开一个新 trace。
	ParentID string
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
		Name:     string(r.Kind),
		ParentID: parentIDOf(r),
		Status:   spanStatusOf(r),
		StartAt:  r.StartAt,
		EndAt:    r.EndAt,
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
	if r.Kind == KindSideEffect {
		if r.SideEffectKey != "" {
			s.Attributes[AttrSideEffectKey] = r.SideEffectKey
		}
		if r.SideEffectPhase != "" {
			s.Attributes[AttrSideEffectPhase] = string(r.SideEffectPhase)
		}
		if r.SideEffectResource != "" {
			s.Attributes[AttrSideEffectResource] = r.SideEffectResource
		}
		if r.SideEffectFence > 0 {
			s.Attributes[AttrSideEffectFence] = int64(r.SideEffectFence)
		}
		if r.SideEffectSafety != "" {
			s.Attributes[AttrSideEffectSafety] = r.SideEffectSafety
		}
	}
	if r.Kind == KindCheckpoint {
		if r.CheckpointRunID != "" {
			s.Attributes[AttrCheckpointRunID] = r.CheckpointRunID
		}
		if r.CheckpointSeq > 0 {
			s.Attributes[AttrCheckpointSeq] = int64(r.CheckpointSeq)
		}
		if r.CheckpointStatus != "" {
			s.Attributes[AttrCheckpointStatus] = r.CheckpointStatus
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
//
// 与逐条调用 @SpanOf@ 的唯一差别是**解析悬空父**：父行不在本批内时把 @ParentID@
// 清成空，这一行随之成为根（见头注）。宿主导出一批行时应当用这个入口——
// 逐条导出的断链 span 在多数后端里表现为「trace 缺了一段」，而不会报错。
func SpansOf(rows []Row) []Span {
	out := make([]Span, 0, len(rows))
	known := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.ID != "" {
			known[r.ID] = struct{}{}
		}
	}
	for _, r := range rows {
		s := SpanOf(r)
		if s.ParentID != "" {
			if _, ok := known[s.ParentID]; !ok {
				s.ParentID = ""
			}
		}
		out = append(out, s)
	}
	return out
}

// parentIDOf 推一行的父 span 标识（**声明**的父，不保证在该批行里存在）。
//
// 层级只有一条：轮 → 环节 → 工具 / 副作用 / 快照。带环节号的行优先挂环节
// （更近的因果），没有环节号时挂轮次；不在任何轮次里的行（轮次外的事件、
// 孤儿行）没有父，即它是根。
func parentIDOf(r Row) string {
	switch r.Kind {
	case KindTurn:
		// 轮次行恒为根：一轮就是一个 trace（见头注「trace 边界」）。
		return ""
	case KindToolCall, KindToolResult, KindSideEffect, KindCheckpoint:
		if r.HasStep {
			return stepRowID(r.StepID)
		}
	}
	if r.Turn > 0 {
		return turnRowID(r.Turn)
	}
	return ""
}

// spanStatusOf 判一行的 span 状态。
//
// 判据只看两件事：**发生过什么问题**（error）与**是否收口**（ok）。
// 其余一律 unset——包括 @missing_result@：
// 「还没等到结果」与「结果重复了」是两件性质完全不同的事，前者可能仍在途。
func spanStatusOf(r Row) SpanStatus {
	// 副作用行：问题与结论都在阶段里，先于通用的「收口即 ok」判定。
	// uncertain 是**已经发生**的事（结果未知、Key 被硬阻塞、必须对账）——
	// 不是「还没等到」；但它不是 ok：两种语义都丢不得，error 承载前者，
	// 未收口（对账挂起中）仍是 unset。
	if r.Kind == KindSideEffect {
		switch r.Status {
		case StatusOpen:
			return SpanStatusUnset // 授予在途 / 对账挂起中
		case StatusOrphan:
			// 孤儿 = 点观察或只有终点的结论，都是**确定的观测**。
			if r.SideEffectPhase == SideEffectUncertain {
				return SpanStatusError
			}
			return SpanStatusOK
		default: // closed
			if r.SideEffectPhase == SideEffectUncertain {
				return SpanStatusError
			}
			return SpanStatusOK
		}
	}
	// 快照行是点观察（孤儿形态）：提交本身是确定发生的成功观测。
	if r.Kind == KindCheckpoint {
		return SpanStatusOK
	}
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
