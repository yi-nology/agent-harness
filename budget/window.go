package budget

// MinInputTokens 输入侧上限的**地板值**，只在「窗口 - 输出预留」真的算成非正时兜底。
//
// 为什么地板是 1 而不是报错：算出 <=0 时真正的病灶在宿主配置（窗口给小了，还是
// 输出预留要大了），内核猜不出是哪一条，就不该替宿主定性一个配置错误。让它返回 1
// 的后果是这次请求几乎必然溢出——而溢出是**看得见**的信号：展示口径 Overflow 为真、
// Classify 判 critical。反之抛错只会把「窗口小」和「预留大」两种病因混成一个错误。
//
// 注意地板**不作用于**「没预留」的早退分支：宿主没要求预留时说「给 1」是在凭空
// 发明一个它没要求的约束，那比让它拿到自己写的上限更糟（见 MaxInput）。
const MinInputTokens = 1

// MaxInput 输入侧可用上限。对齐参考实现的 input_token_budget：
// 无预留或窗口未知时**原样返回自设上限**，否则返回
// min(baseInput, max(window-outputReserve, MinInputTokens))。
//
// # 为什么要扣掉输出预留
//
// 窗口是输入输出**共享**的。若拿整个窗口当输入上限，请求是「装得下」的，
// 下一轮模型却在生成到一半时被截断——而那时用户看到的现象是「回答说到一半没了」，
// 与上下文超限完全无关，排查方向会被带偏。预留先扣，截断就不会发生在生成为零的时候。
//
// # 为什么没有预留就原样返回（而非仍然相减再取地板）
//
// 参考实现在这里直接早退，理由是「没要求预留」与「预留算出来不够」是两种不同的现场：
// 前者是宿主**没有**这项约束，扣减纯属内核自作主张，后者才是宿主约束与窗口打架。
// 早退还顺带保证了自设上限宽于窗口时不会被内核单方面收窄——那属于宿主的显式决定。
//
// # 参数取值的含义
//
//   - window：模型上下文窗口总长；<=0 视为未知。
//   - outputReserve：输出预留（宿主按任务形态定）；<=0 视为不预留。
//   - baseInput：调用方自设的输入上限；<=0 表示「没有自设上限」，此时只由窗口推导。
//
// 地板只在**确实扣成非正**时兜底（预留吃光窗口 / 预留超过窗口 / 全零输入）。
func MaxInput(window, baseInput, outputReserve int) int {
	if outputReserve <= 0 || window <= 0 {
		return baseInput // 无预留或窗口未知：自设上限原样返回，不扣减也不取地板
	}
	safe := window - outputReserve
	if safe < MinInputTokens {
		safe = MinInputTokens // 只有扣成非正才兜底；此时上限已经失去物理意义
	}
	if baseInput <= 0 {
		return safe // 无自设上限：完全由窗口与预留推导
	}
	if baseInput < safe {
		return baseInput // 自设上限更紧：以它为准
	}
	return safe
}

// MaxInputWithHostCap 在 MaxInput 之上让**宿主显式配置**的提示上限直接生效。
//
// # 为什么需要这条独立入口
//
// 参考实现里，显式上限与推导上限不是同一个数被反复比较，而是**分岔**的：宿主一旦
// 在配置里写死了提示上限就直接用，不再与「窗口 - 预留」取小。若把它塞进 MinInput
// 里，这条显式决定会被窗口值悄悄推翻，而推翻它的那一方（内核）没有任何依据。
//
// hostCap > 0 时直接返回它，**不做任何窗口比较也不取地板**：显式配置不是推导值，
// 地板保护只服务于推导路径。
func MaxInputWithHostCap(window, baseInput, outputReserve, hostCap int) int {
	if hostCap > 0 {
		return hostCap
	}
	return MaxInput(window, baseInput, outputReserve)
}

// Pressure 上下文压力档位。
type Pressure int

const (
	// PressureNormal 余量充足，不需要处置。
	PressureNormal Pressure = iota
	// PressureTight 接近上限，该准备压实了。
	PressureTight
	// PressureCritical 已越界或余量不足，本轮大概率截断。
	PressureCritical
)

// String 档位名（留痕与事件载荷用）。
func (p Pressure) String() string {
	switch p {
	case PressureNormal:
		return "normal"
	case PressureTight:
		return "tight"
	case PressureCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// 压力阈值（百分比整数，不用浮点：浮点在 0.9 这种边界上会因舍入把 89% 判成 tight
// 之外的档，判定必须可复现）。
const (
	// TightAtPct 进入 tight 档的占用率。
	TightAtPct = 75
	// CriticalAtPct 进入 critical 档的占用率。
	CriticalAtPct = 90
)

// thresholdAt 占用率 pct% 对应的最小 used，即 used*100 >= c*pct 的精确整数解。
//
// # 为什么改成除法
//
// 原始判据 `used*100 >= c*pct` 两边都是可溢出量：c 取到 int64 量级时 pct 的乘法必然
// 回绕成负数，于是「满载」被比成「占用率远低于阈值」——报出来的档位与真实压力**相反**。
// 除法版没有这个问题：c 可达 int64 上限，全程不产生任何超过 int64 的中间值。
//
// # 为什么还要拆成 a/r 两段
//
// 直接写 c*pct 一样溢出。pct 是小常量，把 c 拆成 c = 100*a + r 之后，
// floor(c*pct/100) = a*pct + r*pct/100，余数非零时再进一取上整。
// a*pct 的上界是 (int64上限/100)*90 < int64上限，r*pct < 100*90，两段都不溢出。
func thresholdAt(c, pct int64) int64 {
	a, r := c/100, c%100
	q := a*pct + r*pct/100
	if r*pct%100 != 0 {
		q++ // 向上取整：阈值取「到达即进档」，故解为最小满足者
	}
	return q
}

// Classify 按占用率判定压力档。纯函数，无副作用。
//
// # 边界取「到达即进档」
//
// 阈值处判**上一档之外更高的一档**：占用率正好等于 90% 时，下一轮几乎必然溢出，
// 若把它算作 tight，最后一个来得及处置的时点就被抹掉了。宁可早一档提示。
//
// # 上限未知即 critical
//
// capacity <= 0 说明调用方没给出窗口约束，此时**不能声称余量充足**——
// 「不知道会不会溢出」和「不会溢出」在行为上完全相反。
func Classify(input, capacity int) Pressure {
	if capacity <= 0 {
		return PressureCritical
	}
	used := int64(input)
	if used < 0 {
		used = 0
	}
	c := int64(capacity)
	switch {
	case used >= thresholdAt(c, CriticalAtPct):
		return PressureCritical
	case used >= thresholdAt(c, TightAtPct):
		return PressureTight
	default:
		return PressureNormal
	}
}
