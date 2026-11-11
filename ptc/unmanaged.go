package ptc

import (
	"github.com/oagentkit/guardx/invoke"
)

// ErrorKindUnmanaged 未受管执行被拒：装配了工具绑定，却没装判定环。
//
// 它与 stage_denied / stage_failed 分开不是分类洁癖：
//   - stage_denied：判定环**判了**，结论是不许（正常裁决）；
//   - stage_failed：判定环**自己坏了**（宿主门禁的故障）；
//   - unmanaged：判定环**压根不在**，于是无人判过——这是「没接线」，不是「判了不许」。
//
// 三者的处置完全不同：前两者改程序或修门禁代码，第三者**必须先装上门禁**。
// 合成一类的话，调用方会去改一段本来没问题的程序，而真正该做的是把门禁接上。
const ErrorKindUnmanaged = "unmanaged"

// unmanagedRefusal 判「装配了工具绑定却没装判定环」这一档。
//
// 返回空串 = 放行；返回非空 = 拒绝理由（会进 Result.Error）。
//
// # 为什么默认要拒
//
// 本包是整条工具链上**唯一能批量调工具**的路径：普通工具调用一轮一个，
// PTC 一段程序里能连着调几十个。若这条路径不带判定环，那么披露门不判、
// 审计门不判、权限门不判——而它恰恰是对外**推荐**的那条路。
// 于是「看起来更安全的路径反而最不安全」（信任边界移动了，沙箱没跟着移动）。
//
// # 为什么「没有绑定」不算逃逸
//
// 风险不在程序，在**能力**：绑定是程序能触达宿主工具的唯一通道。
// 没有绑定就没有可绕过的判定——那是纯计算，执行它不产生任何副作用。
// 故判据看的是「有没有绑定」，不是「有没有判定环」。
//
// # 逃逸口必须显式
//
// allowUnmanaged 为真时才放行「有绑定、无判定环」这一档。它是宿主对
// 「我这一层的工具另有一套管控」的显式声明，不是默认。
// 声明了也不「无声」：结果里带 ErrorKindUnmanaged 的那条路径由调用方留痕，
// 且本仓的门禁会拦生产代码里的显式逃逸（见 archcheck 的未受管逃逸规则）。
func unmanagedRefusal(bindings []Binding, stages []invoke.Stage, allowUnmanaged bool) string {
	if len(bindings) == 0 {
		return "" // 纯计算：没有绑定就没有可绕过的判定
	}
	if len(stages) > 0 {
		return "" // 有判定环：走的是与其他工具调用同一套判定
	}
	if allowUnmanaged {
		return "" // 宿主显式声明：本层的工具另有一套管控
	}
	return "装配了 " + itoa(len(bindings)) + " 个工具绑定却没有判定环：" +
		"程序里的 host.<tool>(...) 将直达宿主工具，绕过披露、审计与权限判定。" +
		"请装配 invoke.Stage 判定环（与普通工具调用同一套）；" +
		"若本层的工具确由别处管控，请显式声明 AllowUnmanaged。"
}

// itoa 小的十进制转换（避免为一个计数引入 strconv 依赖面）。
//
// 为什么不用 strconv：这不是「怕依赖」，是本包只需要 0~99 的计数，
// 手写十行比引入一个包更直白——而依赖面越窄，本仓越容易被审。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
