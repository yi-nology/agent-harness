package pii

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
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/oekit/pkg/errors"
)

// Surface 敏感面。
type Surface string

const (
	// SurfaceInput 进入模型前的外部输入（用户消息、抓取正文、命令输出）。
	SurfaceInput Surface = "input"
	// SurfaceOutput 模型产出（给人看的）。
	SurfaceOutput Surface = "output"
	// SurfaceToolResults 工具返回——独立成面的理由：它会被回灌进下一轮上下文，
	// 泄漏路径与「给人看」完全不同，用同一处置无法同时满足两边。
	SurfaceToolResults Surface = "tool_results"
)

// ErrUnknownSurface 未登记的敏感面名（宿主配置注入时的 fail-closed）。
var ErrUnknownSurface = errors.New("未登记的敏感面")

// SurfaceSet 面集合（位集合）。
type SurfaceSet uint8

const (
	surfaceInputBit       SurfaceSet = 1 << iota // bit0
	surfaceOutputBit                             // bit1
	surfaceToolResultsBit                        // bit2

	// SurfaceAll 三面全开（缺省值）。
	SurfaceAll = surfaceInputBit | surfaceOutputBit | surfaceToolResultsBit
)

// SurfaceBit 单个面在集合中的位。
func SurfaceBit(s Surface) (SurfaceSet, error) {
	switch s {
	case SurfaceInput:
		return surfaceInputBit, nil
	case SurfaceOutput:
		return surfaceOutputBit, nil
	case SurfaceToolResults:
		return surfaceToolResultsBit, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownSurface, s)
}

// Has 该集合是否含指定面。
func (ss SurfaceSet) Has(s Surface) bool {
	bit, err := SurfaceBit(s)
	if err != nil {
		return false // 未登记的面按「不含」处理，不 panic 也不放行
	}
	return ss&bit != 0
}

// ParseSurfaces 由宿主配置解析面集合（未知面名报错，不静默丢弃）。
func ParseSurfaces(names ...string) (SurfaceSet, error) {
	if len(names) == 0 {
		return SurfaceAll, nil
	}
	var ss SurfaceSet
	for _, n := range names {
		bit, err := SurfaceBit(Surface(n))
		if err != nil {
			return 0, err
		}
		ss |= bit
	}
	return ss, nil
}

// surfaceBits 当前面集合（缺省三面全开，见不变量 1）。
var surfaceBits atomic.Uint32

func init() { surfaceBits.Store(uint32(SurfaceAll)) }

// SetSurfaces 注入面集合。
func SetSurfaces(ss SurfaceSet) { surfaceBits.Store(uint32(ss)) }

// Surfaces 当前面集合。
func Surfaces() SurfaceSet { return SurfaceSet(surfaceBits.Load()) }

// SurfaceEnabled 指定面当前是否启用。
func SurfaceEnabled(s Surface) bool { return Surfaces().Has(s) }

// StrategyKind 处置强度（类型名不与同包的 Strategy() 访问器同名）。
type StrategyKind string

const (
	// StrategyMask 保长打码（缺省）：前段+星号+尾段，日志对齐与残留定位友好。
	StrategyMask StrategyKind = "mask"
	// StrategyRedact 整段删除换固定占位：不可见但长度可变。
	StrategyRedact StrategyKind = "redact"
	// StrategyHash 换成不可逆摘要：保留「同一值相同摘要」的可关联性。
	StrategyHash StrategyKind = "hash"
	// StrategyBlock 检出即阻断——**必须由宿主显式开启并自行履行**，
	// 本包只回报 blocked=true，不代为抛错（抛错会把出口面的处置决策
	// 焊进内核，与「判定语义归内核、处置归宿主」相悖）。
	StrategyBlock StrategyKind = "block"
)

// ErrUnknownStrategy 未登记的处置策略。
var ErrUnknownStrategy = errors.New("未登记的处置策略")

func strategyCode(s StrategyKind) (uint32, error) {
	switch s {
	case StrategyMask:
		return 1, nil
	case StrategyRedact:
		return 2, nil
	case StrategyHash:
		return 3, nil
	case StrategyBlock:
		return 4, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownStrategy, s)
}

func codeStrategy(c uint32) StrategyKind {
	switch c {
	case 2:
		return StrategyRedact
	case 3:
		return StrategyHash
	case 4:
		return StrategyBlock
	default:
		return StrategyMask // 兜底回 mask，不回 block——见不变量 2
	}
}

// strategyCodeNow 当前策略码（缺省 mask，见不变量 2）。
var strategyNow atomic.Uint32

func init() { strategyNow.Store(1) }

// SetStrategy 注入处置策略（未知策略报错并保持原值，不静默降级）。
func SetStrategy(s StrategyKind) error {
	c, err := strategyCode(s)
	if err != nil {
		return err
	}
	strategyNow.Store(c)
	return nil
}

// Strategy 当前处置策略。
func Strategy() StrategyKind { return codeStrategy(strategyNow.Load()) }

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
	if !Enabled() || !SurfaceEnabled(s) {
		return text, false
	}
	return applySpans(text)
}

// ApplyValue 递归下探常见载荷结构处置（与 RedactValue 同结构面，收口其
// 行为并加上面/策略维度）。返回 (处置后载荷, 是否阻断)。
func ApplyValue(s Surface, v any) (any, bool) {
	if !Enabled() || !SurfaceEnabled(s) {
		return v, false
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
