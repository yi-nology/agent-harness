// sideeffect_row.go —— 副作用行与快照行的**词表与标识**（v0.33.0，issue #99）。
//
// 单独成文件的两件事：
//
//   - SideEffectPhase 的取值域与 sideeffect 包的生命周期词表同源（授予 / 施加 /
//     放弃 / 转未知 / 裁决两态 / 两个点观察）——本包不 import sideeffect，
//     取值域在契约表（otelmap.go）里逐字声明，一致性由契约用例钉住；
//   - 行标识格式（sideeffect#<key> / checkpoint#<run>#<seq>）的唯一生成点
//     在这里——otel 的父子关系复用同一组函数，另写一份格式父子就会连不上。

package trajectory

import (
	"fmt"
)

// SideEffectPhase 副作用行所处的阶段（与 sideeffect 包的 Outcome / 生命周期词表对齐；
// 本包不 import sideeffect——投影层只吃事件载荷字符串，取值域在契约表里逐字声明，
// 两边的一致性由契约用例钉住）。
type SideEffectPhase string

const (
	// SideEffectGranted 闸已授予执行权，动作进行中（行打开，未收口）。
	SideEffectGranted SideEffectPhase = "granted"
	// SideEffectCompleted 施加并记账成功（Complete 被接受）。
	SideEffectCompleted SideEffectPhase = "completed"
	// SideEffectAbandoned 显式放弃：确定未生效，或下游按幂等键去重（可安全重试）。
	SideEffectAbandoned SideEffectPhase = "abandoned"
	// SideEffectUncertain 结果未知：可能已生效。该行的授予段就此收口，
	// 同 Key 的对账段开启（等 resolved 收口；收不了口会在 Finalize 显形）。
	SideEffectUncertain SideEffectPhase = "uncertain"
	// SideEffectResolvedApplied 对账裁决「已生效」：Key 记为已完成，后续取回结果。
	SideEffectResolvedApplied SideEffectPhase = "resolved_applied"
	// SideEffectResolvedCleared 对账裁决「未生效」：未知标记清除，Key 重新可执行。
	SideEffectResolvedCleared SideEffectPhase = "resolved_cleared"
	// SideEffectReplayed 点观察：动作早已完成，本次取回既有结果（无括号段，
	// 落成孤儿行——原动作发生在别的进程里，这里只有「观测到重放」这一刻）。
	SideEffectReplayed SideEffectPhase = "replayed"
	// SideEffectContended 点观察：资源正被他人持有 / 该 Key 有未裁决尝试。
	// 双 Worker 争用的证据面。
	SideEffectContended SideEffectPhase = "contended"
)

// sideeffectRowID 副作用行的标识：幂等键是它的自然键。同一个 Key 的授予段、
// 对账段、点观察共用一个标识——它们是**同一个逻辑动作**的先后两段/侧面，
// 跨 span 按标识 join 时应聚到一起。
func sideeffectRowID(key string) string { return "sideeffect#" + key }

// checkpointRowID 快照行的标识：run + 序号唯一定位一次提交。
func checkpointRowID(runID string, seq uint64) string {
	return fmt.Sprintf("checkpoint#%s#%d", runID, seq)
}

// seqID 生成无自然键的行的标识（"-" 前缀 = 投影生成，不是业务自然键）。
func seqID(kind Kind, seq int) string {
	return fmt.Sprintf("%s#-%d", kind, seq)
}

// 行标识的**唯一生成点**（与 seqID 同族）。
//
// 三个格式各有一个第二处消费者：otel 的 ParentID 必须与投影写出的 Row.ID 逐字相同，
// 否则父子连不上——而连不上**不会报错**，导出器只会把那一行当根，trace 少一段。
// 故格式只在这里写一次，投影与映射都调这几个函数。
//
// 唯一性的范围是**轮内**：环节号由调用方给定，同一环节号在两轮里各出现一次是合法的
// （故 step#7 会出现在两个轮次里）。trace 边界因此定在轮，见 otel.go 头注。
func turnRowID(turn int) string      { return fmt.Sprintf("turn#%d", turn) }
func stepRowID(id uint64) string     { return fmt.Sprintf("step#%d", id) }
func toolRowID(callID string) string { return "tool#" + callID }
