// 分类辅助：把「一次调用返回的 error」归到某个类别上。
//
// 三条判定路径，刻意分开：
//
//  1. Classify(err) 的**哨兵**分支——工具侧直接返回本包哨兵（最省事，
//     但要求工具 import 本包）；
//  2. ClassifyAs(c, err) 的 **\***Failure** 分支——工具侧有自己的错误体系时，
//     在它自己判定出类别的那一刻调一次，之后链路上的 *Failure 会被 As 捡走；
//  3. Classify(err) 的**自报**分支——错误自带 Retryable() 方法即可，
//     **不要求 import 本包**。这条路径治的是「判据本来就在，只是没人去问」：
//     有的失败（典型是执行门禁的「工具在目录内但 schema 未取回」）可重试与否只有
//     产出它的那一层知道，不问就会落到 unknown，把可恢复的失败判成永久失败。
//
// 三条路径都要求宿主**表态**（归类或自报），本包不猜。猜不出来的归 ClassUnknown，
// 而 ClassUnknown 不可重试（见包注释「关键取舍一」）——这正是「内核不替宿主乐观」
// 的落地形态。区别在于第 3 条不必改宿主的错误体系，只要多一个方法，
// 是三者里成本最低、也最容易在换包时被漏掉的一条。

package toolfail

import "github.com/oekit/pkg/errors"

// 哨兵错误集：工具侧可原样返回本包的哨兵，由 Classify 归类。
//
// 为什么给哨兵而不是让工具自造错误：归类判据必须单源。若允许每个工具各自
// 拼字符串给上游猜，判定就会退化成「看文案里有没有某个词」，而文案会被改写。
var (
	ErrTransient        = errors.New("瞬时故障")
	ErrPermanent        = errors.New("永久性失败")
	ErrAuthentication   = errors.New("身份凭证无效")
	ErrNotFound         = errors.New("目标不存在")
	ErrPermissionDenied = errors.New("权限不足")
	ErrInvalidArgument  = errors.New("参数非法")
	ErrRateLimited      = errors.New("被限流")
)

// sentinelTable 哨兵 → 类别（哨兵集合的判据单源）。
var sentinelTable = []struct {
	err   error
	class Class
}{
	{ErrTransient, ClassTransient},
	{ErrPermanent, ClassPermanent},
	{ErrAuthentication, ClassAuthentication},
	{ErrNotFound, ClassNotFound},
	{ErrPermissionDenied, ClassPermissionDenied},
	{ErrInvalidArgument, ClassInvalidArgument},
	{ErrRateLimited, ClassRateLimited},
}

// RetryableReporter 错误**自报**的「可重试」结论。
//
// 为什么需要它：有些失败的可重试性只有产出它的那一层知道。典型是执行门禁——
// 它知道这次拒绝已经顺手把工具激活了，故重试必成，而「重试会成」这件事在
// 错误链上除了这一个方法之外没有任何痕迹。若本包不接这根线，Classify 只能把它
// 落到 ClassUnknown（不可重试），通用退避循环于是把一次**可恢复**的失败当成
// 永久失败，既该重试的不重试，还把门禁那次激活的收益一并作废。
// 两边各答一套必然漂移——本接口就是那根接线，判据本来就在，只是没人去问。
//
// 命名说明：本来想直接叫 Retryable，但同包已有同名**函数** Retryable(err)，
// 同包内类型与函数不能同名；保留 Reporter 是为了点明它是「错误侧的报告者」，
// 不是本包的判定入口（判定入口仍是 Retryable）。
type RetryableReporter interface {
	// Retryable 这次失败原样重试是否有意义。
	//
	// 实现约定：接收者可能为 nil（错误链上挂着 typed-nil 指针），此时必须返回
	// false 而非解引用——本包对链上每一环都直接调方法，不替你防 panic。
	Retryable() bool
}

// Classify 归类。四级判据，**顺序不可调换**：
//
//  1. 链上已有非 nil 的 *Failure → 取它的类别。显式分类是最具体的一份判定：
//     它带着类别 + 码 + 等待时长，是产出它的那一层已经做完判断的结果。
//  2. 错误自报（RetryableReporter）→ 报「可重试」给 ClassSelfReported，
//     报「不可重试」给 ClassUnknown。两包各答一套的漂移正出在第 2 级缺席时，
//     故它必须排在哨兵之前：自报是这一次的实情，哨兵只是某个通用含义。
//  3. 哨兵集合逐个 errors.Is（工具侧直接返回本包哨兵的路径）。
//  4. 都判不出来 → ClassUnknown，不可重试（见包注释「关键取舍一」）。
//
// 第 2 级报「不可重试」时直接给 ClassUnknown 而**不再**走哨兵：若让哨兵把结论翻成
// 可重试，就等于本包亲口否定了错误自己的报告，两个入口（错误自报 / Retryable(err)）
// 会对同一个错误给出相反答案——那正是这条判据要消灭的漂移，换个方向再来一遍而已。
func Classify(err error) Class {
	if err == nil {
		return ClassUnknown
	}
	var f *Failure
	if errors.As(err, &f) && f != nil && f.Class != "" {
		return f.Class
	}
	if retryable, ok := selfReported(err); ok {
		if retryable {
			return ClassSelfReported
		}
		return ClassUnknown
	}
	for _, row := range sentinelTable {
		if errors.Is(err, row.err) {
			return row.class
		}
	}
	return ClassUnknown
}

// selfReported 沿错误链问「有没有哪个节点自报可重试」，返回 (结论, 是否问到了)。
//
// 为什么用 errors.As 而不是一次类型断言：调用点几乎都会用 %w 往上加一层上下文，
// 断言最外层只会看到包装器，判定随即退化成 unknown——等于这道判据对真实链路失效。
//
// 为什么把 true/false 都原样交回去：自报「不可重试」该落到哪个类别是**策略决定**
// （见 Classify），判据留在那一处。拆成两半各判一次，迟早各改一次、慢慢漂移。
func selfReported(err error) (retryable bool, ok bool) {
	var r RetryableReporter
	if !errors.As(err, &r) {
		return false, false
	}
	// r 是接口，链上挂 typed-nil 时它是「非 nil 的接口包着 nil 指针」，判不出 nil；
	// 真正的防护在实现方（见 RetryableReporter 的实现约定）。
	return r.Retryable(), true
}

// ClassifyAs 按调用方给定的类别归类：把 err 包成 *Failure 后照常返回。
//
// 用于工具侧已有自有错误体系的场景——它在**知道类别的那一刻**调一次，
// 之后整条链路都可由 Classify/As 直接捡走，无需内核去猜。
// err 为 nil 时仍返回 ClassUnknown（没有失败就没有类别可言）。
func ClassifyAs(c Class, err error) *Failure {
	if err == nil {
		return nil
	}
	f := New(c, "", err.Error())
	return f.WithCause(err)
}

// As 从错误链上取出 *Failure（取出即可读类别、码与等待时长）。
func As(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, f != nil
	}
	return nil, false
}

// Retryable 判断任意错误是否值得原样重试：先归类再查策略表。
//
// 这是上层最常用的入口——它不需要知道错误体系长什么样。
//
// 自报错误在这里与在错误自身那里必然**同答**：ClassSelfReported 的策略表项是
// 可重试、自报不可重试落到 ClassUnknown（亦不可重试），两头都不翻。上层只认这一个
// 入口就够，不必回头去 errors.As 找 RetryableReporter——那正是两处各判一套的开始。
func Retryable(err error) bool {
	return PolicyOf(Classify(err)).Retryable
}
