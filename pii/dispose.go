package pii

// 处置执行面：策略感知的打码与载荷下探。
//
// 与 policy.go 的分工：那边是**注册表**（登记哪些敏感面、面开不开、处置强度
// 枚举是什么），这边是**执行面**（拿到策略后怎么处置一段文本/一棵载荷）。
// 拆分的直接原因：policy.go 的文件头写的是「处置策略面（面开关 + 策略枚举）」，
// 而执行引擎原本也在那个文件里，文档只描述了它一半的内容。

// 处置策略面（面开关 + 策略枚举）。
//
// ## 为什么原包不够
//
// 原包只有「检出即保长打码」一种处置，且出口面只有一维开关。本仓所有 PII 出口
// （审计链、事件双出口、外发文案、**工具返回**）走的都是同一个 RedactValue——
// 但工具返回与模型输出根本不是同一个风险面：工具返回常被当作可信数据回灌模型，
// 值面敏感度与「给人看的输出」不是一个量级，用同一处置既是过度脱敏也是
// 处置不足并存。
//
// octop-harness 走的是同一条路（`pii_surfaces` 三面独立开关 +
// `block/redact/mask/hash` 四值策略），本包取其形，不取其内容资产。
//
// ## 三条不变量
//
//  1. **默认三面全开**。新增一个出口面若默认关闭，等于给漏打开后门。
//  2. **默认策略是 mask，不得改成 block**。block 会打断整条审计链；
//     把它作为默认值等于「为了不漏打而把链子砍断」，是更严重的失职。
//     要用 block 必须由宿主显式 SetStrategy 并自行履行阻断。
//  3. **面未启用或全局关闭 → 原样透传且 blocked=false**。透传不是静默放行的
//     漏洞面：它是宿主显式关闭后的既定语义。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// redactedPlaceholder redact 策略的固定占位。
const redactedPlaceholder = "[redacted]"

// applySpan 按**给定**策略处置单个 span 的原值。
//
// 策略由调用方传入而不是就地读全局：一次处置是一个不可分割的动作，让处置形态
// 依赖调用时刻而返回的阻断信号依赖返回时刻，两者可能对不上（见 applySpans）。
func applySpan(v string, kind Kind, s StrategyKind) string {
	switch s {
	case StrategyRedact:
		return redactedPlaceholder
	case StrategyHash:
		sum := sha256.Sum256([]byte(v + "|" + string(kind)))
		return hex.EncodeToString(sum[:])[:16]
	default:
		return maskSpan(v, kind) // mask 与 block 都走保长打码；block 的处置在 Apply 层
	}
}

// applySpansAs 以**指定**策略处置，是「一次调用一个策略」这一不变量的可测形式：
// 测试可固定策略断言「处置形态与 blocked 互相自洽」，不必与全局开关赛跑出竞态窗口。
//
// 策略由 Policy 快照带进来，而不是就地读全局——见 applySpans 下方注释。
func applySpansAs(text string, s StrategyKind) (string, bool) {
	spans := Detect(text)
	if len(spans) == 0 {
		return text, false
	}
	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, sp := range spans {
		b.WriteString(text[prev:sp.Start])
		b.WriteString(applySpan(text[sp.Start:sp.End], sp.Kind, s))
		prev = sp.End
	}
	b.WriteString(text[prev:])
	return b.String(), s == StrategyBlock
}

// Apply 对指定面处置一段文本。
//
// 返回 (处置后文本, 是否阻断)：
//   - 面未启用 / 全局关闭 → (原文, false)
//   - 检出且策略为 block → (处置后文本, true)，**调用方必须履行阻断**
//   - 其余 → (处置后文本, false)
func Apply(s Surface, text string) (string, bool) {
	return applyWith(CurrentPolicy(), s, text)
}

// applyWith 以**指定策略快照**处置一段文本。
//
// # 为什么先取快照再逐项判
//
// enabled / 面开关 / 策略是同一份 Policy 的三个字段，分三次调用访问器去读，
// 中间会被热重载插进来——读到「旧面开关 + 新策略」这种从未被设置过的组合，
// 表现为「宿主已关掉某个面，本次调用却仍回报 blocked=true」。取一次快照，
// 三项判断与处置形态必然同源。
func applyWith(p Policy, s Surface, text string) (string, bool) {
	if !p.enabled {
		return text, false
	}
	// 未知面名**不静默放行**：面未登记与「宿主显式关闭」是两件事，塌缩成同一个
	// 状态就等于拼错一个字符即可让该面完全不脱敏——而 tool_results 正是 PII
	// 最需要兜住的面（工具产出直接回喂模型）。无信号才是这里真正危险的地方：
	// 宿主会以为自己在保护，其实什么都没做。
	//
	// 取舍：整段替换 + 要求阻断，而不是 panic。panic 更响，但会顺着调用栈杀掉
	// 正在跑的编排；静默放行最省事，代价是数据裸奔。这里取两者之间可恢复的那个：
	// 结果仍然是「原文没有出去」，同时让宿主在返回值里看见异常。
	if _, err := SurfaceBit(s); err != nil {
		return redactedPlaceholder, true
	}
	if !p.surfaces.Has(s) {
		return text, false // 确属显式关闭：既定语义，原文返回
	}
	return applySpansAs(text, p.strategy)
}

// ApplyValue 递归下探常见载荷结构处置（与 RedactValue 同结构面，收口其
// 行为并加上面/策略维度）。返回 (处置后载荷, 是否阻断)。
func ApplyValue(s Surface, v any) (any, bool) {
	return applyValueWith(CurrentPolicy(), s, v)
}

// applyValueWith 以**指定策略快照**下探载荷。
//
// # 为什么递归闭包要捕获快照
//
// 本函数此前把闭包交给 applyValue，每个字符串叶子都回调 `Apply(s, t)`——
// 于是**每个叶子重走一遍全部三份全局状态**。一次遍历跨越一次热重载，载荷内
// 就会同时出现两种处置形态（前半占位、后半打码），而 blocked 由 OR 累加得出，
// 与文本形态不同源。这正是 applySpansAs 当初要消除的现象，只不过那条保证止步
// 于单文本路径，复合载荷从旁边绕了过去——而 ApplyValue 才是宿主处理工具返回
// 的主入口。policy_atomic_test.go 的实测：旧实现第 5 次迭代即出现 61/64 混合。
func applyValueWith(p Policy, s Surface, v any) (any, bool) {
	if !p.enabled {
		return v, false
	}
	// 未登记面名**不在这里短路**：短路会返回裸字符串，把本函数「保持载荷结构」
	// 的契约砸掉（宿主按原结构解回，拿到的却是 string）。
	// 交给下面的递归即可——每个字符串叶子都过 applyWith，
	// 那里对未登记面已经 fail-closed，结构与阻断信号都自然保住。
	if !p.surfaces.Has(s) {
		if _, err := SurfaceBit(s); err == nil {
			// 快路径，不是语义守卫：面关闭时逐叶走 applyWith 本来也会透传，
			// 且 applyValue 保留 nil 容器，两者行为完全等价。
			// 这里提前返回只是为了省掉一次载荷遍历。
			// （实测把本分支拆掉，`surface_disabled_test.go` 的用例仍全绿——
			//  这行因此无法被行为断言覆盖，别再往这里加"语义"注释。）
			return v, false
		}
	}
	return applyValue(v, func(t string) (string, bool) { return applyWith(p, s, t) })
}

// applyAny 面无关处置：任一面启用即处置。供历史门面 Redact/RedactValue 使用。
func applyAny(p Policy, text string) (string, bool) {
	if !p.enabled || p.surfaces == 0 {
		return text, false
	}
	return applySpansAs(text, p.strategy)
}

func applyAnyValue(p Policy, v any) (any, bool) {
	if !p.enabled || p.surfaces == 0 {
		return v, false
	}
	return applyValue(v, func(t string) (string, bool) { return applyAny(p, t) })
}

// applyValue 递归下探：字符串交给 fn 处置，标量原样返回。
//
// # 为什么 nil 容器必须原样返回
//
// JSON 载荷里 null 与空容器是两件事（「没有」与「有一个但是空的」），审计链的下游
// 按这个形状做判断。make(...) 出来的非 nil 空容器会把 null 悄悄改写成 [] / {}，
// 且改写不可逆——事后没人能分辨该字段本来是 null 还是空。四个容器分支因此各自先判
// nil 并**返回原值**（连动态类型一起保留，调用方的类型断言仍成立）。
func applyValue(v any, fn func(string) (string, bool)) (any, bool) {
	switch t := v.(type) {
	case string:
		return fn(t)
	case map[string]any:
		if t == nil {
			return t, false
		}
		out := make(map[string]any, len(t))
		blocked := false
		for k, vv := range t {
			ov, ob := applyValue(vv, fn)
			out[k], blocked = ov, blocked || ob
		}
		return out, blocked
	case map[string]string:
		if t == nil {
			return t, false
		}
		out := make(map[string]string, len(t))
		blocked := false
		for k, vv := range t {
			sv, sb := fn(vv)
			out[k], blocked = sv, blocked || sb
		}
		return out, blocked
	case []any:
		if t == nil {
			return t, false
		}
		out := make([]any, len(t))
		blocked := false
		for i, vv := range t {
			ov, ob := applyValue(vv, fn)
			out[i], blocked = ov, blocked || ob
		}
		return out, blocked
	case []string:
		if t == nil {
			return t, false
		}
		out := make([]string, len(t))
		blocked := false
		for i, vv := range t {
			sv, sb := fn(vv)
			out[i], blocked = sv, blocked || sb
		}
		return out, blocked
	default:
		return v, false
	}
}
