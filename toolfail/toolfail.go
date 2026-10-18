// Package toolfail 工具失败的错误分类契约：**这个失败值不值得重试**的机械判据。
//
// ## 为什么需要这个包
//
// 编排引擎要自动退避重试，第一件事是判断「这次失败再试一次有没有可能不同」。
// 本仓此前只有 policy 的「解析失败即拒」——那是**准入**语义（这份输入能不能进），
// 不是**恢复**语义（失败后该重试、退避还是叫停）。两层缺一不可，且不能互相顶替：
// 用准入语义去驱动重试，会把「本来就该拒」的输入反复重试到超时。
//
// 之所以单独立一个包而不是散落在各调用点：退避判定是**编排语义**，属于内核；
// 而「某次调用到底失败在哪一类」取决于具体工具，由宿主/工具侧产出。
// 两边各判一套必然漂移，漂移的后果是重试策略静默错乱（对永久失败死循环重试，
// 或对瞬时失败一次就放弃）。
//
// ## 关键取舍一：不可分类 = 不可重试（保守缺省）
//
// 无法归类的错误归 ClassUnknown，且**不可重试**。
//
// 理由：退避重试的代价是不对称的——重试一个本来会成功的调用，只是多花一点时间；
// 重试一个本来不会成功的调用，是把一次失败放大成 N 次，还可能对有副作用的工具
// 重复施加副作用。判据缺失时，两种误判的成本不在一个量级，故取保守的一侧。
// 宿主若认为某类未知错误可重试，应显式归类，而不是让内核替它乐观。
//
// ## 关键取舍二：策略是数据不是 if 链
//
// 每个类的可重试性/退避/延迟提示/人工介入/重发安全/处置指引写进 PolicyOf 的表里，
// 而不是散成一串 if。上层只需问「PolicyOf(c)」，不必知道「限流可重试但要退避、
// 鉴权不可重试但要人工」这些并列事实——策略表与 contract 契约测试同源，
// 任何人改表都会被契约测试挡住。
//
// ## 关键取舍三：重发安全与重试价值是两件事（2026-10-18 补）
//
// 本包原先只回答「重试有没有收益」（Retryable）。但自动重试真正要问的是两问：
//
//	① 重试有没有收益？            → Retryable / Backoff
//	② 原样重发安不安全（会不会重复施加副作用）？ → SafeToResubmit
//
// 两问的答案可以相反，且相反的组合最危险：**「值得重试」不等于「可以重发」**。
// 超时正是典型——它多半值得重试，可服务端那边请求**可能已经执行完毕**，
// 原样重发就可能把一次调用变成两次副作用。原有的单一维度表达不出这件事，
// 调用方只能要么盲目重试（制造重复副作用），要么因噎废食放弃退避（失去可恢复性）。
//
// 之所以归内核而不是宿主：判定「重发是否安全」的**默认口径**必须是全局一致的，
// 各宿主各判一套就会漂移；而**具体工具是否幂等**是宿主的知识，由 Failure 逐次
// 声明覆盖（Idempotency），内核只提供缺省与查询。
//
// ## 关键取舍四：失败必须能对模型说话（2026-10-18 补）
//
// Remediation 是**面向模型**的处置指引，与 Message 刻意分开：Message 是给人看的
// （进日志、进工单），会被改写、会含具体数值，不适合进提示词；而模型需要知道的是
// 「你下一步该干什么」，不是「发生了什么」。
//
// 它与 Guidance 必须成对存在：一张建好却没有消费方的表等于散文——判据建了没人读，
// 等于没建。故本包同时提供 Guidance 作为**确定的消费方**，把 Remediation 与
// 重试/重发/人工三维结论渲染成一行给模型。
//
// ## 边界
//
// 本包只做分类与策略**判定**，不做重试调度（何时重试、重试几次、退避曲线是编排层的事），
// 也不认识任何具体工具——工具名、参数、返回结构全在宿主侧。
package toolfail

import (
	"strconv"
	"strings"
	"time"
)

// Class 失败类别（机读值，取值见下常量集）。
//
// 用字符串而非整数枚举：类别要随事件载荷落日志、随调用链跨进程传递，
// 数字序一旦被无意依赖（排序、相减）就会变成隐藏契约。
type Class string

const (
	// ClassTransient 瞬时故障：网络抖动、连接重置、超时、服务端临时不可用。
	// 重试有意义——同样的调用稍后可能成功。
	ClassTransient Class = "transient"

	// ClassPermanent 永久性失败：同样的输入再调一次仍然失败（能力不支持、
	// 请求与服务端能力不匹配等）。重试是纯浪费。
	ClassPermanent Class = "permanent"

	// ClassAuthentication 身份凭证无效/过期。重试不会让过期凭证变新，
	// 除非期间有别的环节刷新了它——那属于编排层决策，不属于「原样重试」。
	ClassAuthentication Class = "authentication"

	// ClassNotFound 目标不存在。重试不创造对象。
	ClassNotFound Class = "not_found"

	// ClassPermissionDenied 权限不足。重试不提升权限，需要有人放开权限。
	ClassPermissionDenied Class = "permission_denied"

	// ClassInvalidArgument 参数非法。修参数才有用，原样重试必然复现。
	ClassInvalidArgument Class = "invalid_argument"

	// ClassRateLimited 被限流。可重试，且服务端通常会给出建议等待时长。
	ClassRateLimited Class = "rate_limited"

	// ClassSelfReported 错误**自己**声明了「原样重试有收益」，但本包不认得它属于哪一类。
	//
	// 为什么单立一类而不并入 ClassTransient：可重试 ≠ 瞬时。典型如执行门禁——
	// 工具在目录内、schema 尚未取回时，它把这个拒绝标成可重试（并已顺手激活该工具，
	// 重试必成），可这类失败与网络抖动的成因、处置手段都不一样。挤进 transient
	// 就把调用方可能想要的判断（退避多久、失败源在哪一段）一并抹掉了。
	//
	// 本类只承载「可重试」这一位信息，不承载成因：要连成因一起表达，走
	// ClassifyAs 显式归类成别的类。
	ClassSelfReported Class = "self_reported"

	// ClassUnknown 无法归类。**不可重试**（见包注释「关键取舍一」）。
	ClassUnknown Class = "unknown"
)

// AllClasses 全类别集合（新判据：每类都必须落在表里）。
func AllClasses() []Class {
	return []Class{
		ClassTransient, ClassPermanent, ClassAuthentication, ClassNotFound,
		ClassPermissionDenied, ClassInvalidArgument, ClassRateLimited,
		ClassSelfReported, ClassUnknown,
	}
}

// Policy 单个失败类别的重试处置策略。
type Policy struct {
	// Class 该策略对应的类别。
	Class Class

	// Retryable 原样重试是否有意义。
	Retryable bool

	// Backoff 是否建议退避后重试（重试有收益但不该立刻打）。
	//
	// 与 Retryable 分开是因为「立刻重试」与「退避重试」是两种不同代价：
	// 限流立刻重试只会立刻再被限流，把一次退避能吸收的拥塞放大成重试风暴。
	Backoff bool

	// SurfaceDelay 是否应把服务端给的等待时长向上层暴露（限流类才有此信息）。
	SurfaceDelay bool

	// NeedsOperator 是否需要人工介入（改凭证/放权限，机器自己无能为力）。
	NeedsOperator bool

	// SafeToResubmit 该类失败**默认**能否原样重发（不产生重复副作用）。
	//
	// 与 Retryable 正交，组合起来才完整表达自动重试的决策：
	//   - 两者皆真  → 直接重发；
	//   - 可重试但不可重发 → 重发前必须确认上次是否已生效（典型的超时类）；
	//   - 不可重试 → 换处置路径（改参数 / 找人工 / 收口）。
	//
	// 取值一律保守：只有**能证明服务端未受理本次调用**的类别才置真
	// （限流即如此——请求根本没进业务逻辑）。超时类不给真：超时的定义就是
	// 「不知道对方执行到哪」，把它当成安全重发是把一次调用放大成两次。
	SafeToResubmit bool

	// Remediation 面向**模型**的处置说明（回答「这是什么情况、我该改什么」）。
	//
	// 与 Failure.Message 刻意分开：Message 是给人看的（进日志与上报），
	// 会被改写、含具体数值；提示词里要的是可执行的指引，不是现场描述。
	// 取值不得包含具体标识、数值与业务词面——它会原样进入提示词。
	//
	// **不得自带重试结论**（不要写「重试」「等待 N 秒」「需要人工」）：
	// 那是 Guidance 的机械判定，写在这里会与之重复，个别类别还会直接矛盾
	//（自报类文案写「可以直接重试」，而其 SafeToResubmit 为假，Guidance 会说
	//「先确认上一次是否已生效」——同一段提示词里两个相反的指令，模型无从取舍）。
	// 该约束由 TestRemediationCarriesNoRetryVerdict 钉住。
	Remediation string
}

// policyTable 类别 → 策略的单源表。改这里必然改到契约测试（TestPolicyTableContract）。
var policyTable = map[Class]Policy{
	ClassTransient: {
		Class: ClassTransient, Retryable: true, Backoff: true,
		SafeToResubmit: false,
		Remediation:    "目标暂时不可用，稍后多半会恢复。",
	},
	ClassPermanent: {
		Class: ClassPermanent, Retryable: false,
		Remediation: "该能力不支持这类请求。",
	},
	ClassAuthentication: {
		Class: ClassAuthentication, Retryable: false, NeedsOperator: true,
		Remediation: "凭证无效或已过期，刷新后才会恢复。",
	},
	ClassNotFound: {
		Class: ClassNotFound, Retryable: false,
		Remediation: "目标不存在，且不会自行出现。请先确认标识是否正确。",
	},
	ClassPermissionDenied: {
		Class: ClassPermissionDenied, Retryable: false, NeedsOperator: true,
		Remediation: "当前权限不足以完成该调用。",
	},
	ClassInvalidArgument: {
		Class: ClassInvalidArgument, Retryable: false,
		Remediation: "参数不符合要求，请修正参数后重新构造调用。",
	},
	ClassRateLimited: {
		Class: ClassRateLimited, Retryable: true, Backoff: true, SurfaceDelay: true,
		// 唯一可以断言「重发安全」的类别：限流发生在受理之前，请求没有进到业务逻辑，
		// 不存在「上一次已经生效」的可能。
		SafeToResubmit: true,
		Remediation:    "请求因过于频繁被拒绝，尚未进入处理流程。",
	},
	// 自报类只给出一位信息，据此推不出「可以立刻再打」：立刻重试是否安全取决于
	// 成因，而成因正是本包不知道的那部分。故退避取保守侧——多等一会儿只是慢一点，
	// 立刻重试撞上尚未恢复的条件则是把一次失败放大。确知可立刻重试的宿主应当
	// 显式归类成有成因的类，而不是靠自报蒙混。重发安全同理取保守侧：自报只回答了
	// 「有没有收益」，没回答「上次有没有生效」。
	ClassSelfReported: {
		Class: ClassSelfReported, Retryable: true, Backoff: true,
		Remediation: "调用方声明该失败可恢复，但未说明成因。若想得到更精确的处置，请显式归类。",
	},
	ClassUnknown: {
		Class: ClassUnknown, Retryable: false,
		Remediation: "失败原因无法归类，只能按保守口径处理。请显式归类以获得明确的处置指引。",
	},
}

// PolicyOf 取某类别的策略。未知类别（含零值）一律按 ClassUnknown 处理，
// 保持「查不到就不可重试」的单向失败——策略查询不该是第二个不确定来源。
func PolicyOf(c Class) Policy {
	if p, ok := policyTable[c]; ok {
		return p
	}
	return policyTable[ClassUnknown]
}

// Failure 结构化失败：类别 + 稳定机读码 + 人读文案 + 可选等待时长 + 可选根因。
//
// 为什么三者（Class/Code/Message）都要留：
//
//   - Class 给编排**策略**（问 PolicyOf）；
//   - Code 给上报与统计**聚合**（文案会变，码不能跟着变）；
//   - Message 给人**定位**。
//
// 只留文案是最常见的做法，但文案是给模型看的、会被改写，按文案聚合统计必然失真。
type Failure struct {
	// Class 失败类别，决定重试策略。
	Class Class

	// Code 稳定机读码（如 "rate_limited"）。跨版本稳定，用于聚合与判定，
	// 不得写入具体数值、标识或耗时等每次都变的内容。
	Code string

	// Message 人读文案（进日志与上报，不参与判定）。
	Message string

	// RemediationOverride 该次失败专有的模型指引；为空时取 PolicyOf(Class).Remediation。
	//
	// 为什么允许逐次覆盖：处置指引的下一步动作常常取决于**具体是哪个**参数非法、
	// 哪个标识没找到，逐次覆盖比让模型去猜更快也更准。
	RemediationOverride string

	// Idempotency 宿主对本次调用的幂等性表态；未表态时按类别默认取值。
	//
	// 为什么要宿主逐次声明：同一个类别下不同工具的幂等性可以完全相反
	//（同样是 transient，一个只读查询重发毫无风险，另一个创建接口重发就重复了）。
	// 类别只能给缺省，逐次表态才是准确的。
	Idempotency Idempotency

	// RetryAfter 服务端建议的等待时长；0 表示无此提示。
	// 仅在 PolicyOf(Class).SurfaceDelay 为真时有意义。
	RetryAfter time.Duration

	// Err 可选根因（底层错误）。保留供 errors.Is/As 判定与排查，
	// 不参与策略判定——策略只看 Class。
	Err error
}

// New 构造失败（类别为空按 ClassUnknown 处理；码为空时取类别值，保证机读码恒非空）。
func New(c Class, code, msg string) *Failure {
	if c == "" {
		c = ClassUnknown
	}
	if strings.TrimSpace(code) == "" {
		code = string(c)
	}
	return &Failure{Class: c, Code: code, Message: msg}
}

// WithRetryAfter 附上建议等待时长，返回自身以便链式装配。
func (f *Failure) WithRetryAfter(d time.Duration) *Failure {
	if f != nil {
		f.RetryAfter = d
	}
	return f
}

// WithCause 附上根因，返回自身以便链式装配。
func (f *Failure) WithCause(err error) *Failure {
	if f != nil {
		f.Err = err
	}
	return f
}

// Error 实现 error。nil 接收者返回缺省串，避免把 nil *Failure 传进日志链路时 panic。
func (f *Failure) Error() string {
	if f == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString(string(f.Class))
	b.WriteString("/")
	b.WriteString(f.Code)
	if f.Message != "" {
		b.WriteString(": ")
		b.WriteString(f.Message)
	}
	if f.RetryAfter > 0 {
		b.WriteString(" (retry_after=")
		b.WriteString(strconv.FormatInt(int64(f.RetryAfter/time.Second), 10))
		b.WriteString("s)")
	}
	return b.String()
}

// Unwrap 交出根因，保住 errors.Is/As 判定链。
func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

// Retryable 是否值得原样重试（策略查询的薄封装，上层不必先拿 Class）。
func (f *Failure) Retryable() bool {
	if f == nil {
		return false
	}
	return PolicyOf(f.Class).Retryable
}
