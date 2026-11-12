// Package tenant 多租户的**机制面判据**：租户/权限上下文在机制间的传递契约、
// 授权判定口径与配额判定口径。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 归属（issue #114：先定归属，再谈实现）
//
//   - **数据面**（租户是谁、怎么隔离、权限怎么授）归宿主——内核不认识任何
//     租户体系，这是刻意的边界（与 contract 开放键同一条纪律）；
//   - **判据面**归本仓：哪些机制必须感知租户上下文（fail-closed 入口
//     Require）、权限上下文怎么在机制间传递（context.Context 单通道 +
//     不可变值）、授权怎么判（子集判定 Can）、配额怎么判（Quota.Judge
//     纯函数，用量来自 cost.Aggregate 的聚合产出）。
//
// 宿主各自拼装隔离时最容易各判一套：A 机制查 Tenant 非空、B 机制查
// Scopes 包含、C 机制还要求 Subject——判据散落即口径漂移。本包把三个
// 判定做成同一份原语，机制面（如 controlplane 的介入动作）按同一口径
// 声明与检查。
//
// # 传递契约
//
// 租户/权限上下文经 context.Context 传递（WithContext 注入 / From 提取）
// ——它是机制间传递的唯一通道，不另设全局变量、不挂到结构体上：前者在
// 并发请求间串味，后者让「机制收不到上下文」变成装配问题而不是判据问题。
//
// Context 是**不可变值**：注入后改写副本不影响机制面拿到的值。提取（From）
// 对未注入**宽容**（返回零值）——读路径不因缺上下文而崩；写路径用 Require
// （fail-closed）——静默放行的后果不是「少个功能」：隔离判据各机制各判
// 一套时，漏判的那个机制就是越权入口。
//
// # 判定口径
//
//   - **感知判据**：Tenant 非空 = 已注入。机制声明「本动作必须感知租户」
//     时用 Require 拒绝未注入的上下文；
//   - **授权判据**：required ⊆ Scopes **精确子集**（Can）。scope 词表归宿主
//     （内核不定义「哪个 scope 管哪个动作」），内核只判包含关系——最小
//     授权口径。刻意不做通配 / 层级：更强的表达力宿主在词表层实现，
//     判定面只有「子集」一个真相；
//   - **配额判据**：同 Kind 用量超过上限即超限（Quota.Judge）。窗口语义
//     （按小时 / 按天）归宿主：宿主把窗口内已聚合的用量传进来，判定只管
//     比较，不做任何时间判断——时间判断进判定面就是第二个时钟真相。
package tenant

import (
	"context"
	"slices"

	"github.com/oekit/pkg/errors"
)

// Context 租户/权限上下文（不可变值；零值 = 未注入）。
type Context struct {
	// Tenant 租户标识（开放键：内核不解释其语义，只判非空）。
	Tenant string
	// Subject 主体标识（谁在操作）。
	//
	// 读路径 / 留痕面的建议载体。介入类契约另有显式 Operator 字段时
	// （如 controlplane 的介入请求）以那个为准——归责字段随请求走，
	// 不依赖上下文的组装方记得填。
	Subject string
	// Scopes 授权面（宿主词表）。
	//
	// 判定按精确子集（Can）。刻意不排序、不去重、不校验词形——规范化
	// 即第二份口径，词表归宿主。
	Scopes []string
}

// Present 感知判据：上下文是否注入了租户。
func (c Context) Present() bool { return c.Tenant != "" }

// Can 授权判据：required 是否为 Scopes 的（精确匹配）子集。
//
// required 为空 = 无要求，恒真。判定只认「Scopes 里有没有这一项」——
// Scopes 里的重复项与顺序不参与判定（见字段注释：内核不做规范化）。
func (c Context) Can(required ...string) bool {
	for _, r := range required {
		if !slices.Contains(c.Scopes, r) {
			return false
		}
	}
	return true
}

// 错误哨兵（宿主判定走 errors.Is，不要各造一套）。
var (
	// ErrNoContext 写路径拒绝了未注入租户上下文的请求（fail-closed）。
	ErrNoContext = errors.New("tenant: 上下文未注入租户信息")
	// ErrUnauthorized 授权不足（required 不在 Scopes 内）。
	ErrUnauthorized = errors.New("tenant: 授权不足")
)

// ctxKey 上下文键（未导出：包外造不出第二把钥匙，注入必须走 WithContext）。
type ctxKey struct{}

// WithContext 把租户上下文注入 request context（机制间传递的唯一通道）。
func WithContext(ctx context.Context, c Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// From 提取租户上下文。未注入返回零值（读路径宽容，不报错——
// 查询面不该被「没有租户上下文」拦死；写路径用 Require）。
func From(ctx context.Context) Context {
	c, _ := ctx.Value(ctxKey{}).(Context)
	return c
}

// Require fail-closed 判据：机制声明「本动作必须感知租户上下文」时的入口。
//
//   - 未注入（Tenant 空）→ ErrNoContext；
//   - required 非空且授权不足 → ErrUnauthorized。
//
// 通过时返回注入的 Context 副本，机制面不必再取一次。
func Require(ctx context.Context, required ...string) (Context, error) {
	c := From(ctx)
	if !c.Present() {
		return Context{}, ErrNoContext
	}
	if !c.Can(required...) {
		return Context{}, ErrUnauthorized
	}
	return c, nil
}
