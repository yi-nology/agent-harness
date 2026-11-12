// registry.go —— 后端登记簿：登记、对照与选择。
//
// 选择的两条纪律与 sandbox.Registry.Resolve / delegate.Route 同源：
// 无满足者即拒绝（不降级），满足者取最弱（同档按 ID 字典序）。
package backend

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/oekit/pkg/errors"
)

// ErrDuplicateID 同一 ID 被登记两次。
var ErrDuplicateID = errors.New("backend: 后端 ID 重复登记")

// ErrBadBackend 试图登记不合法的后端（nil / 空 ID）。
var ErrBadBackend = errors.New("backend: 后端不合法")

// Registry 任务式执行后端的登记簿。
//
// 并发安全：登记与解析都可能来自不同 goroutine，内部加锁。
type Registry struct {
	mu   sync.Mutex
	list []Backend // 按登记顺序存放；Resolve 时排序，不依赖登记顺序
}

// Register 登记一个后端。
//
// 拒绝的三种情形都报错而不是「尽力而为」：nil / 空 ID 是声明缺陷，
// ID 重复会让「选到了谁」不可判定。
func (r *Registry) Register(b Backend) error {
	if b == nil {
		return errors.Wrap(ErrBadBackend, "backend: 登记的后端是 nil")
	}
	if strings.TrimSpace(b.ID()) == "" {
		return errors.Wrap(ErrBadBackend, "backend: 登记的后端没有 ID（留痕与选择无从定位）")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, have := range r.list {
		if have.ID() == b.ID() {
			return errors.Wrap(ErrDuplicateID, fmt.Sprintf("backend: ID %q 已被登记", b.ID()))
		}
	}
	r.list = append(r.list, b)
	return nil
}

// Candidates 返回满足要求的全部后端，按「最弱优先」排序：
// 隔离档位升序，同档按 ID 字典序。
//
// 顺序即选择的依据，且**不依赖登记顺序**——登记顺序通常来自配置文件的
// 书写次序，一次无关的重排就会换掉执行方（delegate.Route 的同源教训）。
func (r *Registry) Candidates(spec Spec) ([]Backend, error) {
	if err := spec.Validate(); err != nil {
		return nil, errors.Wrap(ErrBadSpec, "backend: "+err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Backend
	for _, b := range r.list {
		if spec.SatisfiedBy(b) == nil {
			out = append(out, b)
		}
	}
	slices.SortStableFunc(out, func(a, b Backend) int {
		if c := cmp.Compare(a.Isolation(), b.Isolation()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out, nil
}

// Resolve 从登记簿里选一个满足要求的后端。
//
// 选择口径：**满足者取最弱**（够用且最省），同档按 ID 字典序。
// 一个都不满足时报 ErrNoBackend，不降级、不挑「差不多的」。
func (r *Registry) Resolve(spec Spec) (Backend, error) {
	cands, err := r.Candidates(spec)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, errors.Wrap(ErrNoBackend, fmt.Sprintf(
			"backend: 要求隔离 ≥ %s、出站 %s、预算（mem=%d calls=%d bytes=%d）——登记簿共 %d 个后端，无一满足",
			spec.MinIsolation, spec.Egress,
			spec.Budget.MaxMemory, spec.Budget.MaxCalls, spec.Budget.MaxBytes, len(r.list)))
	}
	return cands[0], nil
}
