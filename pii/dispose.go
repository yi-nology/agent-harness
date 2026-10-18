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

// applySpans 处置核心：检出 → 按**入口快照的**策略逐段处置 → 回报是否应阻断。
//
// # 为什么策略只读一次
//
// SetStrategy 是原子的，所以逐段读全局**没有数据竞争**，但它让一次调用变成非原子的：
// 宿主并发换策略时，前几段可能已按 mask 打码、末段按 redact 替换，而函数末尾再读一次
// 得到的 blocked 会跟实际处置形态对不上。丢的那个信号恰好是 fail-closed 的 block——
// 「文本已按非阻断形态处置」与「回报不阻断」叠在一起，调用方会安静地放行。
// 快照一次把这件事消掉：一次调用要么整体按 A 处置并按 A 回报，要么整体按 B。
//
// **唯一处置实现**：Apply / ApplyValue（按面）与 Redact / RedactValue（面无关的
// 历史门面）都走这里。两套并行实现等于给策略留后门——历史上正是如此：Redact 只看
// 全局开关、硬编码 mask，宿主把策略配成 block 之后调用它仍然静默打码。
func applySpans(text string) (string, bool) {
	return applySpansAs(text, Strategy())
}

// applySpansAs 以**指定**策略处置，是「一次调用一个策略」这一不变量的可测形式：
// 测试可固定策略断言「处置形态与 blocked 互相自洽」，不必与全局开关赛跑出竞态窗口。
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
	if !Enabled() {
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
	if !SurfaceEnabled(s) {
		return text, false // 确属显式关闭：既定语义，原文返回
	}
	return applySpans(text)
}

// ApplyValue 递归下探常见载荷结构处置（与 RedactValue 同结构面，收口其
// 行为并加上面/策略维度）。返回 (处置后载荷, 是否阻断)。
func ApplyValue(s Surface, v any) (any, bool) {
	if !Enabled() {
		return v, false
	}
	// 未登记面名**不在这里短路**：短路会返回裸字符串，把本函数「保持载荷结构」
	// 的契约砸掉（宿主按原结构解回，拿到的却是 string）。
	// 交给下面的递归即可——applyValue 的每个字符串叶子都过 Apply，
	// 那里对未登记面已经 fail-closed，结构与阻断信号都自然保住。
	if !SurfaceEnabled(s) {
		if _, err := SurfaceBit(s); err == nil {
			// 快路径，不是语义守卫：面关闭时逐叶走 Apply 本来也会透传，
			// 且 applyValue 保留 nil 容器，两者行为完全等价。
			// 这里提前返回只是为了省掉一次载荷遍历。
			// （实测把本分支拆掉，`surface_disabled_test.go` 的用例仍全绿——
			//  这行因此无法被行为断言覆盖，别再往这里加"语义"注释。）
			return v, false
		}
	}
	return applyValue(v, func(t string) (string, bool) { return Apply(s, t) })
}

// applyAny 面无关处置：任一面启用即处置。供历史门面 Redact/RedactValue 使用。
func applyAny(text string) (string, bool) {
	if !Enabled() || Surfaces() == 0 {
		return text, false
	}
	return applySpans(text)
}

func applyAnyValue(v any) (any, bool) {
	if !Enabled() || Surfaces() == 0 {
		return v, false
	}
	return applyValue(v, applyAny)
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
