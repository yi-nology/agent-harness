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
	"fmt"
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

// ValidateSurface 校验面名是否已登记，供宿主在**接线期**自查配置。
//
// # 为什么需要它
//
// Apply / ApplyValue 对未登记面采取 fail-closed（整段替换 + 要求阻断），那是
// 运行期的兜底，代价是一次误伤。宿主若把面名做成配置项，应该在启动时调一次
// 本函数把拼写错误挡在外面——fail-closed 是保险，不是日常路径。
//
//	// 宿主启动时：
//	for _, name := range cfg.PiiSurfaces {
//	    if err := pii.ValidateSurface(pii.Surface(name)); err != nil {
//	        return fmt.Errorf("PII 面名 %q 未登记: %w", name, err)
//	    }
//	}
func ValidateSurface(s Surface) error {
	_, err := SurfaceBit(s)
	return err
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
