// Package toolfail 工具失败的错误分类契约（门面）。
//
// 核心已下沉 `github.com/oagentkit/guardx/toolfail`：失败分类与处置判定是
// 跨宿主一致的语义，与编排无关，故归 guardx（tier 0 安全层）。本包只做一件事——
// 保住调用路径稳定，宿主的 `toolfail.Classify(err)` / `toolfail.ClassifyAs(...)`
// 调用点一行不动。
//
// 为什么归上游而不是留在本仓：判定口径必须**全局一致**。各宿主各判一套必然漂移，
// 漂移的后果是重试策略静默错乱（对永久失败死循环重试，或对瞬时失败一次就放弃）。
// 而「同一机制在本仓与上游并存两份实现」正是 docs/OAGENTKIT-OVERLAP.md 警告的事——
// 散文说「保持门面」不够，门禁（archcheck 的 facade 判定）才真的守着这条。
//
// 本包与任何宿主领域无关：它是 LLM 应用的通用失败处置面。
//
// 与上游 mcpkit/mcp.ErrorKind 的分工（互补，不重复）：后者判**协议层事实**
// （连不上/协议错/工具不存在/参数错），本包判**处置层语义**（该不该重试、
// 能不能原样重发、要不要人工、模型下一步该干什么）。
package toolfail

import (
	upstream "github.com/oagentkit/guardx/toolfail"
)

// 类型别名：编译期同一类型，故宿主现有的类型断言（errors.As 取 *Failure）
// 与结构体字面量继续可用——**API 逐字兼容**。
type (
	// Class 失败类别（机读值）。
	Class = upstream.Class
	// Policy 单个失败类别的重试处置策略。
	Policy = upstream.Policy
	// Failure 结构化失败：类别 + 稳定机读码 + 人读文案 + 可选等待时长 + 可选根因。
	Failure = upstream.Failure
	// Idempotency 幂等性声明（未表态 / 明确幂等 / 明确有副作用）。
	Idempotency = upstream.Idempotency
	// RetryableReporter 错误自报的「可重试」结论。
	RetryableReporter = upstream.RetryableReporter
)

// 类别常量别名（可成块）。
const (
	ClassTransient        = upstream.ClassTransient
	ClassPermanent        = upstream.ClassPermanent
	ClassAuthentication   = upstream.ClassAuthentication
	ClassNotFound         = upstream.ClassNotFound
	ClassPermissionDenied = upstream.ClassPermissionDenied
	ClassInvalidArgument  = upstream.ClassInvalidArgument
	ClassRateLimited      = upstream.ClassRateLimited
	ClassSelfReported     = upstream.ClassSelfReported
	ClassUnknown          = upstream.ClassUnknown
)

// 幂等性三态常量别名。
const (
	IdempotencyUnset = upstream.IdempotencyUnset
	IdempotencyYes   = upstream.IdempotencyYes
	IdempotencyNo    = upstream.IdempotencyNo
)

// 哨兵错误别名：工具侧可原样返回它们，由 Classify 归类。
var (
	ErrTransient        = upstream.ErrTransient
	ErrPermanent        = upstream.ErrPermanent
	ErrAuthentication   = upstream.ErrAuthentication
	ErrNotFound         = upstream.ErrNotFound
	ErrPermissionDenied = upstream.ErrPermissionDenied
	ErrInvalidArgument  = upstream.ErrInvalidArgument
	ErrRateLimited      = upstream.ErrRateLimited
)

// AllClasses 全类别集合。
func AllClasses() []Class { return upstream.AllClasses() }

// PolicyOf 取某类别的策略。
func PolicyOf(c Class) Policy { return upstream.PolicyOf(c) }

// Classify 归类：链上已有 *Failure → 错误自报 → 哨兵 → unknown。
func Classify(err error) Class { return upstream.Classify(err) }

// ClassifyAs 按调用方给定的类别归类。
func ClassifyAs(c Class, err error) *Failure { return upstream.ClassifyAs(c, err) }

// As 从错误链上取出 *Failure。
func As(err error) (*Failure, bool) { return upstream.As(err) }

// Retryable 判断任意错误是否值得原样重试。
func Retryable(err error) bool { return upstream.Retryable(err) }

// New 构造失败（类别为空按 unknown 处理；码为空时取类别值）。
func New(c Class, code, msg string) *Failure { return upstream.New(c, code, msg) }
