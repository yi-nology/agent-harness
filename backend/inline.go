// inline.go —— 进程内兜底后端：契约的**可满足性证明**与单进程编排的最小执行件。
//
// 它不是容器隔离的实现——它声明 IsolationNone（显式最弱一档），存在的意义
// 有三：① 让契约在零外部依赖下可测（判据跑在它身上）；② 给单进程编排一个
// 不引任何容器运行时的保底执行件；③ 作为「如何实现 Backend」的最小样板。
// 真实的容器隔离见 examples/containerbackend（Docker Engine 参考实现）。
package backend

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/oekit/pkg/errors"
)

// ErrUnknownProgram 兜底后端没有登记这个名字的处理函数。
//
// 兜底后端把 Job.Image 读作**处理函数名**（进程内没有镜像可拉）：
// 名字对不上即拒绝，不静默跑一个空操作——「跑了但什么都没发生」是最贵的
// 失败形态。
var ErrUnknownProgram = errors.New("backend: 兜底后端没有登记这个名字的处理函数")

// Handler 兜底后端的执行体：拿到作业请求，返回结果。
//
// 返回 error 的语义与 Backend.Exec 一致：基础设施故障走 error，
// 作业自身失败写在 Result.OK / ExitCode 里。
type Handler func(ctx context.Context, j Job) (Result, error)

// Inline 进程内兜底后端。
//
// 三条自报（都与判据对齐，不占便宜）：
//
//   - Isolation() = IsolationNone：处理函数跑在宿主进程里，能触达宿主
//     能触达的一切。它是显式声明，不是漏报——漏报零值也是 none，但那
//     是「没签字」，这是「签了字的最弱档」；
//   - 不实现 EgressEnforcer：进程内执行没有任何出站强制面，沉默 = 兜不住；
//   - 不实现 Capable：内存与输出截断兜不住（Go 运行时没有「只给一个
//     处理函数限内存」的开关），沉默 = 兜不住。
//
// 于是任何高于「要求=none、出站=unrestricted、不要求预算」的 Spec 都不会
// 选中它——判据替它守住诚实。
type Inline struct {
	mu       sync.Mutex
	handlers map[string]Handler
}

// NewInline 构造兜底后端。
func NewInline() *Inline {
	return &Inline{handlers: map[string]Handler{}}
}

// ID 后端标识。
func (i *Inline) ID() string { return "inline" }

// Isolation 显式声明最弱一档（见类型注释的自报三条）。
func (i *Inline) Isolation() Isolation { return IsolationNone }

// Register 登记一个处理函数（name 即 Job.Image 的取值）。
//
// 名字重复即拒绝：静默覆盖会让「上一次注册的行为」变成不可判定。
func (i *Inline) Register(name string, h Handler) error {
	if name == "" {
		return errors.New("backend: 兜底处理函数的名字为空（Job.Image 将无处匹配）")
	}
	if h == nil {
		return errors.New("backend: 兜底处理函数为 nil（登记等于没登记）")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.handlers == nil {
		i.handlers = map[string]Handler{}
	}
	if _, dup := i.handlers[name]; dup {
		return errors.New("backend: 兜底处理函数 " + name + " 已登记（静默覆盖会让行为不可判定）")
	}
	i.handlers[name] = h
	return nil
}

// Names 已登记的处理函数名（升序，确定性）。
func (i *Inline) Names() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Sorted(maps.Keys(i.handlers))
}

// Exec 执行一次作业：按 Job.Image 查处理函数，查到即执行。
func (i *Inline) Exec(ctx context.Context, j Job) (Result, error) {
	if err := j.Validate(); err != nil {
		return Result{}, errors.New("backend: " + err.Error())
	}
	i.mu.Lock()
	h, ok := i.handlers[j.Image]
	i.mu.Unlock()
	if !ok {
		return Result{}, errors.Wrap(ErrUnknownProgram, "backend: 兜底后端没有登记 "+j.Image)
	}
	return h(ctx, j)
}
