// allow.go —— 出站守卫的**唯一放行词汇表**。
//
// # 为什么不用双 bool
//
// `CheckURL(url, allowPrivate, allowLoopback)` 这种签名里，两个 bool 的含义只能
// 靠调用点约定区分——`CheckURL(u, true, true)` 读起来是「true true」，看不出
// 是「私网与回环都放行」还是别的什么意图。而放行面是**安全边界**：写错一位就是
// 一条 SSRF 路径（issue #2 的「缺口 1b」：双 bool 让「探测自建服务」与
// 「SSRF 穿透」共用同一组比特，区别只存在于调用点的口头约定里）。
//
// 具名之后：调用点自解释（`Allow{Private: true}`），意图有名字可挂
// （`AllowSelfService()`），且**将来加第三个维度不会破签名**——
// 双 bool 的签名加一个维度就是一次 API 破坏。
//
// # 为什么必须校验层与拨号层共用
//
// issue #6 的一条行动项是「`DialOptions` 的两个开关与 `CheckURL` 的两个开关
// **必须同口径**」。若两层各用各的类型，这句话就只是文档里的一句叮嘱；
// 用了同一个类型之后，两层之间的差异只剩**值**（宿主的选择），
// 而「怎么映射到上游 egress」只有一处实现（allow.egressOptions），
// 不可能各自漂移。
//
// 方向相反的不一致后果更重：校验层按「允许私网」放行、拨号层按缺省（禁私网）
// 拒 → 「配置明明允许内网，调用却失败」；反向则是校验层拒、拨号层放行——
// **后者才是安全问题**。

package netguard

import "github.com/oagentkit/guardx/egress"

// Allow 描述「哪些目标地址族放行」。
//
// **零值是安全默认**：私网与回环都拒（外部输入面：平台代发的目标不该落在
// 内网或本机）。要放行必须显式写出来——放行面是安全边界，默认就该是关的。
type Allow struct {
	// Private 放行私网段（RFC1918 等内网地址族）。
	Private bool
	// Loopback 放行回环族（127.0.0.0/8、::1，以及未指定地址的本地形态）。
	Loopback bool
}

// AllowSelfService 返回「私网与回环都放行」的放行面。
//
// 用途是**点名意图**：这组比特恰好也是 SSRF 穿透的入口，而「为什么放行」在双
// bool 签名里无处安放。名字把意图留在代码里，review 时看得见。
//
// 做成函数而不是可变的包级变量：放行面是安全边界，能被别处改写的全局变量
// 不是边界（调用方拿到的是一个**值**，改不动它）。
func AllowSelfService() Allow { return Allow{Private: true, Loopback: true} }

// egressOptions 是本包到上游 egress 的**唯一转换点**。
//
// 单一转换点的意义：校验层与拨号层不可能在「怎么把放行面翻译成上游判据」这件事上
// 各写一份而漂移。上游 egress 的 IP 策略是本包的判据单源（见包头「与上游 urlguard
// 的分工」），本包不私补网段表。
func (a Allow) egressOptions() egress.Options {
	return egress.Options{AllowPrivate: a.Private, AllowLoopback: a.Loopback}
}
