package pii

// 处置策略面（面开关 + 策略枚举 + 全局开关）。
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

// validStrategy 策略是否已登记。SetStrategy 的唯一职责就是这道校验：
// 未知策略报错并**保持原值**，不静默降级——静默降级会让宿主以为自己
// 配成了 block，实际按 mask 放行。
func validStrategy(s StrategyKind) error {
	switch s {
	case StrategyMask, StrategyRedact, StrategyHash, StrategyBlock:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnknownStrategy, s)
}

// ── 运行时策略：整份一次原子交换 ──

// Policy 一份完整的运行时策略快照。
//
// # 为什么不是三个独立的原子变量
//
// enabled / surfaces / strategy 三样东西**合起来才是一份策略**，不是三个独立开关。
// 本包此前正是三个各自原子的包级变量（atomic.Bool + 两个 atomic.Uint32），
// 逐项看每一次读写都线程安全，看起来没问题——但读侧一次处置要分三次取：
//
//	Apply:      Enabled() → SurfaceEnabled(s) → applySpans → Strategy()
//	ApplyValue: 每个字符串叶子重走上面整条序列
//
// 热重载若发生在两次取之间，读到的是**从未被任何写侧设置过的组合**。
// policy_atomic_test.go 的对照组量到了这个窗口：2483 万次读取里 252 万次
// （约 10%）读到非法组合。真实后果不是「多打一次码」，而是语义错误：宿主已经
// `SetSurfaces(0)` 关掉了某个面，本次调用却仍读到旧的面开着 + 新的 block 策略，
// 于是回报 blocked=true——**面已关闭却要求阻断**，宿主可能据此中断整条编排链。
//
// 与 netguard 的改造同形（那边是把主机名与后缀两个指针并成一份 Policy）。
type Policy struct {
	gen      uint64       // 代次，每次替换 +1，供可观测性与调试
	enabled  bool         // 全局开关（缺省开 = secure by default）
	surfaces SurfaceSet   // 面集合（缺省三面全开，见不变量 1）
	strategy StrategyKind // 处置策略（缺省 mask，见不变量 2）
}

// curPolicy 当前生效的策略。**整份策略只有一个原子指针**——这是「不可撕裂读」
// 的全部机制，也是与旧版三个独立变量的根本差别。
var curPolicy atomic.Pointer[Policy]

func init() {
	curPolicy.Store(&Policy{gen: 1, enabled: true, surfaces: SurfaceAll, strategy: StrategyMask})
}

// CurrentPolicy 取当前生效策略的快照。构造后不再被任何写侧触碰（写侧只整体
// 替换新实例），故取出后可安全长期持有与并发遍历，无需再加锁。
//
// 处置路径必须经由本函数拿快照，不得分字段调访问器再拼——那正是撕裂读的来源。
func CurrentPolicy() Policy { return *curPolicy.Load() }

// Gen 当前策略代次（与快照同步递增，供可观测性使用）。
func Gen() uint64 { return curPolicy.Load().gen }

// updatePolicy 整体替换策略：读当前 → 改一个字段 → CAS 写回，失败则重试。
//
// # 为什么必须用 CAS 而不是读改写
//
// 三个写侧（SetEnabled / SetSurfaces / SetStrategy）可能并发。把「读—改—写」
// 拆成独立的 Load 与 Store，中间被别的写侧插进来就会丢更新：宿主刚设的
// enabled=false 被并发进来的 SetSurfaces 用旧副本覆盖回去。CAS 让「基于最新值
// 修改」这条读改写链变成一次原子操作。
func updatePolicy(mutate func(*Policy)) {
	for {
		old := curPolicy.Load()
		next := *old
		mutate(&next)
		next.gen = old.gen + 1
		if curPolicy.CompareAndSwap(old, &next) {
			return
		}
	}
}

// SetEnabled 注入全局开关（conf security.pii_value_mask / env BQ_PII_VALUE_MASK）。
func SetEnabled(on bool) { updatePolicy(func(p *Policy) { p.enabled = on }) }

// Enabled 当前是否启用（关闭时 Redact/RedactValue 原样透传，零改写）。
func Enabled() bool { return curPolicy.Load().enabled }

// SetSurfaces 注入面集合。
func SetSurfaces(ss SurfaceSet) { updatePolicy(func(p *Policy) { p.surfaces = ss }) }

// Surfaces 当前面集合。
func Surfaces() SurfaceSet { return curPolicy.Load().surfaces }

// SurfaceEnabled 指定面当前是否启用。
func SurfaceEnabled(s Surface) bool { return curPolicy.Load().surfaces.Has(s) }

// SetStrategy 注入处置策略（未知策略报错并保持原值，不静默降级）。
func SetStrategy(s StrategyKind) error {
	if err := validStrategy(s); err != nil {
		return err
	}
	updatePolicy(func(p *Policy) { p.strategy = s })
	return nil
}

// Strategy 当前处置策略。
func Strategy() StrategyKind { return curPolicy.Load().strategy }
