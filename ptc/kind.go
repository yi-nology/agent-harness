package ptc

import (
	"github.com/oagentkit/guardx/toolfail"
)

// kindForErrorKind 把本地 ErrorKind 映射成 toolfail 的失败分类。
//
// # 为什么要有映射
//
// 跨进程传的失败分类必须是 **toolfail 的分类**（wire 的不变量 2：分类跨进程
// 同源）——父侧拿到后要据此决定「可重试 / 需人工 / 永久拒绝」。而本地的
// ErrorKind 是**诊断口径**（给模型看的形态名），两套词表不是一回事。
//
// 映射按「处置含义」而非字面：
//
//	预算类（budget_calls / budget_bytes）→ 限流类（可重试但要退避）
//	确定性失败（syntax / binding_* / bad_result）→ 永久类（重试无意义）
//	超时与中断 → 瞬时类（值得重试）
//	其余 → unknown（不可分类 = 不可重试，保守侧）
func kindForErrorKind(kind string) string {
	switch kind {
	case "budget_calls", "budget_bytes":
		return string(toolfail.ClassRateLimited)
	case "syntax", "binding_collision", "binding_signature", "bad_result", "pending":
		return string(toolfail.ClassPermanent)
	case "deadline", "runtime":
		return string(toolfail.ClassTransient)
	case "permission_denied":
		return string(toolfail.ClassPermissionDenied)
	default:
		return string(toolfail.ClassUnknown)
	}
}
