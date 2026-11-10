package ptc

import (
	"fmt"

	"github.com/dop251/goja"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oekit/pkg/errors"
)

// thrownRejection 分类随值走的拒绝载体（issue #44/#45/#57，#67 定形为身份锚定）。
//
// 此前两条类型化分类通道都是**结果级单槽**或**包装丢失**形态：
//   - 判定环拒绝走 res.stageErr 单槽——程序把拒绝 try/catch 掉后再抛
//     自己的错误，收口处读到的是**陈旧**的 stageErr，程序自己的 bug 被
//     误归为 stage_denied（调用方去质问守卫而不是改程序，#44）；
//   - 子进程侧 remoteFailure 携带的 toolfail 分类（父侧判定环显式附加）
//     在 vm.NewGoError 的裸 error 包装里丢失，落进 runtime→transient——
//     「重试也永远不会满足」的策略否定被报成「值得重试」（#45）。
//
// 修法：每次抛出都把分类装进载体，并在 Result.lastThrown 记下「最近一次
// 本包抛出的载体」；收口处确认「这次拒绝就是我们抛的那次」再采信分类。
//
// # 锚定的演进：单向 Contains → nonce 全等 → 对象身份（issue #67）
//
// 锚定判据历经三代，各有教训：
//
//   - #44/#45 用的单向 Contains 会被「catch(e){ throw new Error("failed: " +
//     e.message) }」这类改写命中——改写后的消息包含载体消息，程序自己的
//     bug 误继承 stage 分类；
//   - #57 改为 nonce + 渲染全等：锚文本每次抛出随机且须逐字一致，改写不再
//     命中。但它要求把被拒值与载体**各渲染一遍**再比——而 *goja.Object 的
//     String() 会执行 JS 的 toString/valueOf（goja value.go 的
//     toPrimitiveString），那是**用户代码**：敌意 toString 死循环 ⇒ 收口
//     无界挂起（唯一上界是墙钟中断）；deadline/ctx 取消恰在渲染窗口触发
//     vm.Interrupt ⇒ InterruptedError panic 穿透 fmt，被 Run 的 recover
//     误收成 host_panic——「宿主侧要修的」，而真实归因（stage_denied/
//     toolfail）丢失（#67）。
//   - #67 起锚定比较**对象身份**：抛出时记下包装载体的 GoError 对象（anchor
//     字段），收口处拿它与被拒值做指针相等——本次拒绝就是那次抛出 ⇔ 同一个
//     对象。两个方向都严格强于文本锚定：复制/改写文本不可能伪造指针相等；
//     指针比较不进 VM，敌意 toString 无从介入。程序**原样**抛出捕获到的载体
//     （throw e）仍正确归因——同一个对象在 JS 里流转，身份不变，那是同一次
//     拒绝的再抛。对抗面（程序偷藏载体对象改日重抛以污染归因）不在威胁模型
//     内：lastThrown 只记最近一次抛出，陈旧载体天然失配。
//
// 载体仍经 vm.NewGoError 包装进 JS 抛出（goja 对非 goja 值的 panic 会原样
// 穿透成 host panic，不能当 JS 异常被 try/catch）——分类的存取都在 Go 侧，
// 不依赖 goja 值语义。
type thrownRejection struct {
	err    error        // 原始错误（分类的语义来源）
	kind   string       // ErrorKind 词汇：stage_denied / stage_failed / toolfail 分类值；"" = 程序自身运行时错误
	anchor *goja.Object // 抛出时的 GoError 包装对象；收口处以指针相等锚定（nil = 不参与归因）
}

// Error 让载体能作为 error 穿过 vm.NewGoError（其文本渲染进 JS 侧的
// message 属性——程序 catch 后读到的 e.message 就是原始错误文本）。
func (e *thrownRejection) Error() string { return e.err.Error() }

// rejectionIsCarrier 判被拒值是否就是载体抛出时的那个对象。
//
// 刻意不执行任何 JS（issue #67）：类型断言 + 指针比较，不触碰对象属性——
// 属性读取对 Proxy/带 getter 的对象会执行用户代码。
func rejectionIsCarrier(rej goja.Value, tr *thrownRejection) bool {
	if tr == nil || tr.anchor == nil {
		return false
	}
	obj, ok := rej.(*goja.Object)
	return ok && obj == tr.anchor
}

// renderRejection 渲染锚定失配的被拒值文本（issue #67：收口阶段唯一的
// **受保护执行路径**）。
//
// 渲染必须**直调 rej.String() 而不走 fmt**：*goja.Object 的 String() 会执行
// JS 的 toString/valueOf（用户代码）——保留这条路径是因为 Error 对象的可读
// 文案（"Error: boom"）只能从 toString 来，改用 Export 会把 message 折进 map
// 丢掉。而 fmt 对 Stringer 的调用自带 catchPanic：panic 在 fmt 内部被吞成
// "%!v(PANIC=String method: ...)" 文本——墙钟到点的中断事件就此被报成
// 「程序拒绝 + panic 残渣」（issue #67 旧缺陷的实测形态）。直调让 panic
// 穿透到本函数的 recover，两层各自归位：
//
//   - 墙钟/ctx 取消的 Interrupt 打穿渲染（*goja.InterruptedError）：就地
//     归类中断（第二返回值非空，deadline 或「执行被中断」）——子进程路径
//     没有 Run 的 recover 兜底，不接住就整个子进程崩成「执行域异常终止」；
//   - 渲染自身抛 JS 异常（被拒值是带敌意 toString/getter 的对象）：退到
//     非执行形态的占位文本——宁可少一段文案，不挂起、不误归因。
func renderRejection(rej goja.Value) (text, kind string) {
	defer func() {
		if r := recover(); r != nil {
			if ie, ok := r.(*goja.InterruptedError); ok {
				text, kind = interruptMessage(ie)
				return
			}
			text, kind = "<被拒值不可渲染（渲染被保护拦下）>", ""
		}
	}()
	return rej.String(), ""
}

// interruptMessage 中断错误的归类文本与 kind（与 failFrom 对 RunProgram
// 错误的口径一致：中断哨兵 → deadline；其余中断 → runtime 带 ctx 语义）。
//
// *goja.InterruptedError 的 Unwrap 暴露中断载荷（vm.Interrupt 的入参），
// 故 errors.Is 穿得进去——这里与 failFrom 走同一份判据，不另立口径。
func interruptMessage(ie *goja.InterruptedError) (string, string) {
	if errors.Is(ie, errDeadline) {
		return errDeadline.Error(), "deadline"
	}
	return fmt.Sprintf("执行被中断: %v", ie.Value()), "runtime"
}

// rejectionKindOf 探测调用错误自带的分类（toolfail 口径）。
//
// 判定面优先走**链感知**的 toolfail.As（issue #58）：*toolfail.Failure 是本仓
// 及其矩阵唯一的规范失败类型，宿主用它表态是标准动作——而它**没有** Kind()
// 方法，裸断言（甚至 errors.As 接口探针）都取不到它的分类，进程内路径上
// permission_denied 一律降级成 runtime→transient，「重试也不会满足」的策略
// 否定被报成「值得重试」。链感知的 As 还顺带治了「套一层 %w 丢分类」——
// 单向类型断言不穿包装，规范类型包一层上下文就探不到。
// Kind() 探针退为兜底（errors.As 穿包装），保住自定义宿主类型的扩展点；
// 两头都探不到返回 ""（无分类，收口处按程序自身错误处理）。
func rejectionKindOf(err error) string {
	if f, ok := toolfail.As(err); ok && f != nil && f.Class != "" {
		return string(f.Class)
	}
	var k interface{ Kind() string }
	if errors.As(err, &k) {
		return k.Kind()
	}
	return ""
}
