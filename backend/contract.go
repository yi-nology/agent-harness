// contract.go —— 后端契约与「要求 vs 实际」的对照判据。
//
// 本文件的立论：隔离强度的声明必须有人签字，且签字必须**可被问询**。
// 此前「声明了容器隔离」在执行侧没有任何对照物——宿主写了一档要求，
// 没有任何东西能回答「这个后端做不做得到」。判据在这里，不判就在报告里读。
package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sandbox"
)

// Backend 一个任务式执行后端。
//
// 后端自己解释 Job 的执行语义，但**必须**如实自报隔离档位：档位是宿主做
// 风险判断的依据，说谎的成本落在宿主身上（与 sandbox.Providers 登记
// 门禁同一条纪律——「档位声明要有人签字」）。
type Backend interface {
	// ID 后端标识，在登记簿里唯一。
	ID() string
	// Isolation 该后端**能强制**的隔离档位。
	//
	// 说的不是「我期望这一档」，而是「我能强制这一档」——作业真的跑在
	// 这个边界之内，而不是「配了一组看起来很严的参数」。
	Isolation() Isolation
	// Exec 执行一次作业。
	//
	// 返回 error = 后端基础设施故障（作业没跑起来）；返回 Result 且
	// OK=false = 作业自己失败（见 Result 注释）。两层失败判据不同，
	// 不许混在一个通道里。
	Exec(ctx context.Context, j Job) (Result, error)
}

// EgressEnforcer 可选能力面：后端声明它能**强制**哪一档出站。
//
// 与 sandbox.EgressEnforcer 同构同词表（sandbox.EgressMode）：任何满足
// 那个接口的后端自动满足本接口（Go 是结构化类型）。**不实现即视为没有
// 做出任何声明**——在判据里沉默按最弱处理：一个没实现本接口的后端连
// 「我能禁网」都说不出口，它当然兜不住禁网。
//
// 为什么是可选接口而不是 Backend 的必选方法：进程内兜底后端（Inline）没有
// 任何出站强制面，必选方法会逼它撒谎或报错——可选 + 沉默按最弱，两个都不用。
type EgressEnforcer interface {
	// Egress 返回该后端实际能兑现的出站档位。
	Egress() sandbox.EgressMode
}

// Capable 可选能力面：后端声明它能**兜住**哪组预算。
//
// 与 sandbox.Capable 同构（同一条纪律的同一形状）：不实现的后端只兑现
// 基本闸（墙钟经 context 兜底），不兑现内存与输出截断。内存是唯一
// 「兜不住就出事」的项——任务式后端跑的是完整作业，一个无界分配能撑爆
// 宿主；声明不了就别被选中。
type Capable interface {
	// Capable 报告该后端能否兑现这份预算的全部非零项。
	Capable(b sandbox.Budget) bool
}

// Spec 一份执行要求：隔离下限、出站档位与必须兜住的预算。
//
// 零值的语义由字段各自承担：MinIsolation 零值 = IsolationNone（不要求隔离，
// 显式的一档）；Egress 零值 = sandbox.EgressUnrestricted（不要求出站）；
// Budget 零值 = 不要求兜任何预算。三个零值合起来 = 「随便找个后端」——
// 这也是显式的：没有一项被要求，判据全过。
type Spec struct {
	// MinIsolation 隔离档位下限（满足下限者才有资格）。
	MinIsolation Isolation
	// Egress 要求的出站档位（Unrestricted = 不要求）。
	Egress sandbox.EgressMode
	// Budget 要求兜住的预算（只看非零项；MaxMemory > 0 ⇒ 要求后端兜内存）。
	Budget sandbox.Budget
}

// Validate 判要求自身是否在词表内。
//
// 越界档位不得被当成最弱档处理——那是沉默放行；负预算同 Job.Validate。
func (s Spec) Validate() error {
	if !s.MinIsolation.Valid() {
		return fmt.Errorf("backend: 隔离要求 %d 不在词表内（none/process/container/vm）", uint8(s.MinIsolation))
	}
	if !s.Egress.Valid() {
		return fmt.Errorf("backend: 出站要求 %d 不在词表内（unrestricted/proxy/denied）", int(s.Egress))
	}
	if s.Budget.WallClock < 0 || s.Budget.MaxCalls < 0 || s.Budget.MaxBytes < 0 || s.Budget.MaxMemory < 0 {
		return errors.New("backend: 预算要求出现负值")
	}
	return nil
}

// 错误哨兵（判定方按 errors.Is 认人；实现方复用，不要各造一套）。
var (
	// ErrNotSatisfied 要求未被该后端满足。包装它给出未满足的轴与两侧值。
	ErrNotSatisfied = errors.New("backend: 要求未被该后端满足")
	// ErrNoBackend 登记簿里没有能满足要求的后端。
	//
	// 无满足者即拒绝，不降级：降级会让「宿主声明的强度」与「实际执行的
	// 强度」之间没有信号，档位整套机制就成了装饰。
	ErrNoBackend = errors.New("backend: 没有能满足要求的后端")
	// ErrBadSpec 要求自身不合法（越界档位 / 负预算）。
	ErrBadSpec = errors.New("backend: 要求不合法")
	// ErrSilent 后端对某条能力面保持沉默（未声明 = 兜不住）。
	//
	// 它与 ErrNotSatisfied 的区别是归因：一个是「声明了但不够强」，
	// 一个是「根本没声明」——修法不同（换后端 vs 补声明），报错必须分得开。
	ErrSilent = errors.New("backend: 后端未声明该能力面（沉默按最弱处理）")
)

// SatisfiedBy 判该后端是否满足要求。
//
// 判定顺序固定（isolation → egress → budget），报错与轴一一对应：
//
//   - 隔离：Isolation() 达不到下限 → ErrNotSatisfied；
//     档位越界（词表外）→ ErrNotSatisfied（词表外不是「更强」，是声明缺陷）；
//   - 出站：要求高于 unrestricted 时，后端必须实现 EgressEnforcer 且其档位
//     合法且 ≥ 要求；未实现 → ErrSilent；实现了但不满足 → ErrNotSatisfied；
//   - 预算：MaxMemory > 0 时后端必须实现 Capable 且自报兜得住；
//     未实现 → ErrSilent；实现了但报兜不住 → ErrNotSatisfied。
//     MaxCalls > 0 同理（任务式执行通常兜不住逐调用计数——如实自报，
//     不要假装支持）。
//
// 墙钟不在问询之列：context 截止是所有后端的公共义务，没有「兜不住墙钟」
// 的合法声明。
func (s Spec) SatisfiedBy(b Backend) error {
	if err := s.Validate(); err != nil {
		return errors.Wrap(ErrBadSpec, "backend: "+err.Error())
	}
	if b == nil {
		return errors.Wrap(ErrSilent, "backend: 没有可判定的后端")
	}
	if !b.Isolation().Valid() {
		return errors.Wrap(ErrNotSatisfied, fmt.Sprintf("backend: %s 的隔离档位 %q 不在词表内", b.ID(), b.Isolation()))
	}
	if !b.Isolation().AtLeast(s.MinIsolation) {
		return errors.Wrap(ErrNotSatisfied, fmt.Sprintf("backend: 要求隔离 ≥ %s，%s 只声明了 %s",
			s.MinIsolation, b.ID(), b.Isolation()))
	}
	if s.Egress.Satisfies(sandbox.EgressProxy) { // 严格高于 unrestricted 才问
		ee, ok := b.(EgressEnforcer)
		if !ok {
			return errors.Wrap(ErrSilent, fmt.Sprintf("backend: 要求出站 %s，%s 未声明出站能力面",
				s.Egress, b.ID()))
		}
		got := ee.Egress()
		if !got.Valid() {
			return errors.Wrap(ErrNotSatisfied, fmt.Sprintf("backend: %s 声明的出站档位越界（%d）", b.ID(), int(got)))
		}
		if !got.Satisfies(s.Egress) {
			return errors.Wrap(ErrNotSatisfied, fmt.Sprintf("backend: 要求出站 %s，%s 只能强制 %s",
				s.Egress, b.ID(), got))
		}
	}
	var asks []string
	if s.Budget.MaxMemory > 0 {
		asks = append(asks, fmt.Sprintf("max_memory(%d)", s.Budget.MaxMemory))
	}
	if s.Budget.MaxCalls > 0 {
		asks = append(asks, fmt.Sprintf("max_calls(%d)", s.Budget.MaxCalls))
	}
	if s.Budget.MaxBytes > 0 {
		asks = append(asks, fmt.Sprintf("max_bytes(%d)", s.Budget.MaxBytes))
	}
	if len(asks) > 0 {
		c, ok := b.(Capable)
		if !ok {
			return errors.Wrap(ErrSilent, fmt.Sprintf("backend: 要求预算 %s，%s 未声明预算能力面",
				strings.Join(asks, ", "), b.ID()))
		}
		if !c.Capable(s.Budget) {
			return errors.Wrap(ErrNotSatisfied, fmt.Sprintf("backend: 要求预算 %s，%s 自报兜不住",
				strings.Join(asks, ", "), b.ID()))
		}
	}
	return nil
}
