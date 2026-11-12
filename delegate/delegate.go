// Package delegate 异构委托的**契约面**：请求能不能交出去、交出去的结果算不算完成。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 为什么只做契约面
//
// 「把任务委托给另一个 agent 运行时（ACP / 别的 CLI）」方向明确，但它同时是
// **协议面 + 传输**：握手版本、stdio 帧格式、会话续接都属于传输，落点大概率在
// 上游。此时由内核定一套线格式，等于把一种传输焊进契约。
//
// 故本包只回答三个跨传输恒定的问题：
//
//  1. **能不能交给它**：请求需要的能力必须是对方**声明提供**的子集。
//     做不到的事一律拒绝，不许「尽力而为」——尽力而为的结果无法判定
//     「没做到」与「本来就没要求」，而委托方只能照单全收。
//  2. **交给谁**：多个能满足的 offer 里取**能力最少**的那个（最小能力原则）。
//     委托的本质是把控制权交出去；把任务交给权限更大的那一方，是在没有
//     任何收益的情况下扩大暴露面。
//  3. **回来算不算完成**：回执的状态是**三值**（completed / failed / refused），
//     且失败必须带原因。把「被拒绝执行」混进「执行失败」会让委托方
//     去修一个不存在的 bug。
package delegate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/slicex"
)

// Capability 能力标识（宿主自定词表：本包只做集合包含判定）。
type Capability string

// ErrUnsupported 对方不提供所需能力。
var ErrUnsupported = errors.New("delegate: 对方不提供所需能力")

// ErrNoRunner 没有任何 offer 能满足请求。
var ErrNoRunner = errors.New("delegate: 没有能满足请求的执行方")

// Request 一次委托请求。
type Request struct {
	// Goal 目标描述（自然语言，传输与解释归双方）。
	Goal string
	// Cwd 期望的工作目录（空 = 由执行方决定）。
	Cwd string
	// Needs 本次委托**必需**的能力（子集判定，不做「差不多」）。
	Needs []Capability
	// Constraints 约束（原样携带；语义由执行方解释）。
	Constraints []string
}

// Offer 一个执行方的能力声明。
type Offer struct {
	// Name 执行方标识（留痕与选择用）。
	Name string
	// Provides 它**声明提供**的能力。
	Provides []Capability
	// MaxGoalBytes 目标描述的字节上限（<=0 = 不限制）。
	MaxGoalBytes int
}

// Accept 判这个执行方能否接受该请求。
//
// 拒绝的三种情形都报错而不是返回 false：拒绝的理由要能进报告，
// 而一个 bool 会把「能力不足」与「目标太长」合成同一个沉默。
func (o Offer) Accept(r Request) error {
	if strings.TrimSpace(o.Name) == "" {
		return errors.New("delegate: 执行方没有名字（留痕无从定位）")
	}
	if strings.TrimSpace(r.Goal) == "" {
		return errors.New("delegate: 目标为空（没有目标的委托无法判定完成）")
	}
	if o.MaxGoalBytes > 0 && len(r.Goal) > o.MaxGoalBytes {
		return fmt.Errorf("delegate: 目标 %d 字节超过 %s 的上限 %d", len(r.Goal), o.Name, o.MaxGoalBytes)
	}
	have := make(map[Capability]bool, len(o.Provides))
	for _, c := range o.Provides {
		have[c] = true
	}
	var missing []Capability
	for _, need := range r.Needs {
		if !have[need] {
			missing = append(missing, need)
		}
	}
	if len(missing) > 0 {
		return errors.Wrap(ErrUnsupported, "delegate: 执行方 "+o.Name+" 不提供能力 "+joinCaps(missing))
	}
	return nil
}

// Route 从一组 offer 里挑一个能接的执行方。
//
// 选择口径：**能力最少者优先**（最小能力原则），同数量按名字字典序。
// 多个都满足时按顺序取第一个是错的：那取决于 offer 的排列顺序，
// 而排列顺序通常来自配置文件的书写次序——一次无关的重排就会换掉执行方。
func Route(offers []Offer, r Request) (Offer, error) {
	cands := make([]Offer, 0, len(offers))
	for _, o := range offers {
		if err := o.Accept(r); err == nil {
			cands = append(cands, o)
		}
	}
	if len(cands) == 0 {
		return Offer{}, errors.Wrap(ErrNoRunner, "delegate: 请求需要能力 "+joinCaps(r.Needs))
	}
	slices.SortStableFunc(cands, func(a, b Offer) int {
		if c := cmp.Compare(len(a.Provides), len(b.Provides)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return cands[0], nil
}

// Status 回执状态（三值：把「被拒绝执行」混进「执行失败」会让委托方
// 去修一个不存在的 bug）。
type Status string

const (
	// StatusCompleted 完成。
	StatusCompleted Status = "completed"
	// StatusFailed 执行失败（执行方做过尝试）。
	StatusFailed Status = "failed"
	// StatusRefused 拒绝执行（执行方没有尝试，通常因为它判定不该做）。
	StatusRefused Status = "refused"
)

// valid 报告状态是否在词表内。
func (s Status) valid() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusRefused
}

// Receipt 一次委托的回执。
type Receipt struct {
	// Status 状态。
	Status Status
	// Summary 一句话结论（completed 时必填：没有结论的「完成」无法使用）。
	Summary string
	// Failure 失败/拒绝原因（failed 与 refused 时必填）。
	Failure string
	// Artifacts 产出物引用（路径或标识，原样携带）。
	Artifacts []string
}

// Validate 判回执是否自洽。
//
// 判据（不满足即报错，不静默补全）：
//
//   - 状态在词表内；
//   - completed 必须有 Summary，且**不得**带 Failure（带了说明双方对
//     「完成」的理解不一致，而委托方会按「完成」处理）；
//   - failed / refused 必须有 Failure（没有原因就无法决定重试还是改需求）。
func (r Receipt) Validate() error {
	if !r.Status.valid() {
		return fmt.Errorf("delegate: 回执状态 %q 不在词表内（completed / failed / refused）", r.Status)
	}
	switch r.Status {
	case StatusCompleted:
		if strings.TrimSpace(r.Summary) == "" {
			return errors.New("delegate: completed 的回执没有结论——「完成了」但说不出做了什么，无法使用")
		}
		if strings.TrimSpace(r.Failure) != "" {
			return errors.New("delegate: completed 的回执带了 Failure（双方对「完成」的理解不一致）")
		}
	default:
		if strings.TrimSpace(r.Failure) == "" {
			return fmt.Errorf("delegate: %s 的回执没有原因——没有原因就无从决定重试还是改需求", r.Status)
		}
	}
	return nil
}

// joinCaps 把能力列表拼成可读文本。
func joinCaps(cs []Capability) string {
	return strings.Join(slicex.Map(cs, func(c Capability) string { return string(c) }), ", ")
}
