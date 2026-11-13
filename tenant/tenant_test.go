package tenant

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/cost"
)

// 示例 scope 词表（合成数据：词面是机制侧通用表述，词表本体归宿主）。
const (
	scopeRead    = "task.read"
	scopeOperate = "task.operate"
)

func TestPresent(t *testing.T) {
	if (Context{}).Present() {
		t.Fatal("零值上下文应判定为未注入")
	}
	if !(Context{Tenant: "acme"}).Present() {
		t.Fatal("Tenant 非空应判定为已注入")
	}
	// Subject/Scopes 不参与感知判据：判据只有一个真相（Tenant 非空）
	if !(Context{Tenant: "acme", Scopes: nil}).Present() {
		t.Fatal("无 Scopes 的上下文只要 Tenant 非空就是已注入")
	}
}

func TestCanSubsetSemantics(t *testing.T) {
	c := Context{Tenant: "acme", Scopes: []string{scopeRead}}
	if !c.Can() {
		t.Fatal("required 为空 = 无要求，应恒真")
	}
	if !c.Can(scopeRead) {
		t.Fatal("授权面内的 scope 应判真")
	}
	if c.Can(scopeOperate) {
		t.Fatal("授权面外的 scope 应判假")
	}
	if c.Can(scopeRead, scopeOperate) {
		t.Fatal("多项 required 缺一即假（子集判定，不是任一命中）")
	}
	// 重复/乱序的授权面不参与规范化：判定只认「有没有」
	dup := Context{Tenant: "acme", Scopes: []string{scopeRead, scopeRead}}
	if !dup.Can(scopeRead) {
		t.Fatal("重复项不应影响包含判定")
	}
}

func TestContextPassing(t *testing.T) {
	c := Context{Tenant: "acme", Subject: "op-1", Scopes: []string{scopeRead}}
	ctx := WithContext(context.Background(), c)
	if got := From(ctx); got.Tenant != "acme" || got.Subject != "op-1" {
		t.Fatalf("注入/提取往返漂移： %+v", got)
	}
	// 不可变值：改写提取出的副本不牵动已注入的上下文
	got := From(ctx)
	got.Scopes = append(got.Scopes, scopeOperate)
	got.Tenant = "改写"
	if again := From(ctx); again.Tenant != "acme" || len(again.Scopes) != 1 {
		t.Fatalf("上下文被改写： %+v", again)
	}
	// 未注入：读路径宽容（零值，不报错）
	if (From(context.Background())).Present() {
		t.Fatal("未注入应返回零值")
	}
}

func TestRequireFailClosed(t *testing.T) {
	// 未注入 → ErrNoContext（写路径不静默放行）
	if _, err := Require(context.Background(), scopeRead); !errors.Is(err, ErrNoContext) {
		t.Fatalf("未注入应拒绝 ErrNoContext，got %v", err)
	}
	// 注入但授权不足 → ErrUnauthorized
	ctx := WithContext(context.Background(), Context{Tenant: "acme", Scopes: []string{scopeRead}})
	if _, err := Require(ctx, scopeOperate); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("授权不足应拒绝 ErrUnauthorized，got %v", err)
	}
	// required 为空：只判注入，不判授权
	c, err := Require(ctx)
	if err != nil || c.Tenant != "acme" {
		t.Fatalf("空 required 应只判注入： %v %+v", err, c)
	}
	// 正常通过：返回注入的副本
	c, err = Require(ctx, scopeRead)
	if err != nil || c.Subject != "" {
		t.Fatalf("正常通过应返回副本： %v %+v", err, c)
	}
}

func TestJudgeBoundaries(t *testing.T) {
	q := Quota{Max: map[cost.Kind]int64{
		cost.KindInputTokens: 100,
		cost.KindToolCalls:   0, // 归零声明
	}}
	used := func(amounts map[cost.Kind]int64) cost.Total {
		return cost.Total{Amounts: amounts}
	}
	// used == limit 不超限（上限是允许值）
	if got := q.Judge(used(map[cost.Kind]int64{cost.KindInputTokens: 100})); len(got) != 0 {
		t.Fatalf("用量等于上限不应超限： %+v", got)
	}
	// used == limit+1 超限
	got := q.Judge(used(map[cost.Kind]int64{cost.KindInputTokens: 101, cost.KindToolCalls: 1}))
	if len(got) != 2 {
		t.Fatalf("应报 2 条超限，got %d： %+v", len(got), got)
	}
	if got[0].Kind != cost.KindInputTokens || got[0].Used != 101 || got[0].Max != 100 {
		t.Fatalf("排序或明细漂移： %+v", got[0])
	}
	if got[1].Kind != cost.KindToolCalls || got[1].Max != 0 {
		t.Fatalf("归零声明应判超限： %+v", got[1])
	}
	// 未声明上限的 Kind 不设限
	if got := q.Judge(used(map[cost.Kind]int64{cost.KindOutputTokens: 1 << 40})); len(got) != 0 {
		t.Fatalf("未声明的 Kind 不设限： %+v", got)
	}
	// 归零声明 + 零用量：不超限
	if got := q.Judge(used(map[cost.Kind]int64{cost.KindToolCalls: 0})); len(got) != 0 {
		t.Fatalf("归零声明在零用量下不超限： %+v", got)
	}
	// 确定性：两次判定同一份顺序
	again := q.Judge(used(map[cost.Kind]int64{cost.KindInputTokens: 101, cost.KindToolCalls: 1}))
	if len(again) != 2 || again[0].Kind != got[0].Kind || again[1].Kind != got[1].Kind {
		t.Fatalf("判定顺序非确定： %+v vs %+v", again, got)
	}
}

func TestRemaining(t *testing.T) {
	q := Quota{Max: map[cost.Kind]int64{
		cost.KindInputTokens: 100,
		cost.KindToolCalls:   0, // 归零声明：剩余额度恒 0
	}}
	rem := q.Remaining(cost.Total{Amounts: map[cost.Kind]int64{
		cost.KindInputTokens:  30,
		cost.KindToolCalls:    1,
		cost.KindOutputTokens: 999, // 未声明的 Kind 不进结果
	}})
	if len(rem) != 2 {
		t.Fatalf("Remaining 应只含声明过的 Kind，got %+v", rem)
	}
	if rem[cost.KindInputTokens] != 70 {
		t.Fatalf("剩余额度漂移： %d", rem[cost.KindInputTokens])
	}
	if rem[cost.KindToolCalls] != 0 {
		t.Fatalf("归零声明的剩余额度应为 0，got %d", rem[cost.KindToolCalls])
	}
	// 超限 → 剩余额度钳位为 0，不为负
	rem = q.Remaining(cost.Total{Amounts: map[cost.Kind]int64{cost.KindInputTokens: 150}})
	if rem[cost.KindInputTokens] != 0 {
		t.Fatalf("超限的剩余额度应钳位为 0，got %d", rem[cost.KindInputTokens])
	}
}

// fakeSource 测试夹具：内存声明源。
type fakeSource struct {
	limits map[string]Quota
	err    error
}

func (f fakeSource) QuotaFor(tenant string) (Quota, error) {
	if f.err != nil {
		return Quota{}, f.err
	}
	return f.limits[tenant], nil // 未配置的租户返回零值 Quota（不设限）
}

func TestEnforce(t *testing.T) {
	src := fakeSource{limits: map[string]Quota{
		"acme": {Max: map[cost.Kind]int64{cost.KindInputTokens: 100}},
	}}
	used := cost.Total{Amounts: map[cost.Kind]int64{cost.KindInputTokens: 120}}

	// 未注入上下文 → ErrNoContext
	if err := Enforce(src, Context{}, used); !errors.Is(err, ErrNoContext) {
		t.Fatalf("未注入应拒绝 ErrNoContext，got %v", err)
	}
	// 超限 → ErrQuotaExceeded，明细在包装链消息里
	err := Enforce(src, Context{Tenant: "acme"}, used)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("超限应报 ErrQuotaExceeded，got %v", err)
	}
	// 未配置的租户 = 零值 Quota = 不设限
	if err := Enforce(src, Context{Tenant: "glob"}, used); err != nil {
		t.Fatalf("未配置的租户不设限，got %v", err)
	}
	// 未超限 → nil
	ok := cost.Total{Amounts: map[cost.Kind]int64{cost.KindInputTokens: 100}}
	if err := Enforce(src, Context{Tenant: "acme"}, ok); err != nil {
		t.Fatalf("用量在上限内不应报错，got %v", err)
	}
	// 声明源自身故障原样上抛（不被吞成「超限」或「放行」）
	boom := errors.New("source down")
	if err := Enforce(fakeSource{err: boom}, Context{Tenant: "acme"}, used); !errors.Is(err, boom) {
		t.Fatalf("声明源故障应原样上抛，got %v", err)
	}
}
