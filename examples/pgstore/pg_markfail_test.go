package pgstore

// pg_markfail_test.go —— 「标记写失败」在真实 PostgreSQL 上的形态（评审问题 A 的另一半）。
//
// 契约（sideeffect.Store.MarkUncertain 第 5 条）：标记写失败时，该 Key 的执行
// 意图必须**仍在**——它是唯一挡着重试的东西。若失败时把意图一并撤了，这个 Key 就
// 重新变成「可被执行」⇒ 同一动作被施加两次，而调用方全程只拿到一个普通错误。
//
// # 为什么这条要在真实 PG 上钉住
//
// 共享契约套件（sidetest 的「标记失败」族）注入的是**调用面**故障：底层写成功、
// 调用方拿到错误。那是最坏的一种，但它**没有**真的让写失败。
// 真实 PG 上的写失败走的是另一条路：语句报错 ⇒ 事务整体回滚 ⇒ 意图原封不动。
// 内存实现用一把 mutex 模拟原子性，它的「回滚」只是「写了一半」的镜像；真库的
// 回滚语义（一个事务里既有撤销、又有置未知、又有写意图）得由真库自己证明。
//
// # 怎么注入「真写失败」
//
// 给动作行加一条 CHECK 约束，让 MarkUncertain 写进去的 reason 违反约束 ⇒ UPDATE
// 语句报错 ⇒ 事务回滚。约束只加在本用例自己的 schema 里（schema 用完即删），
// 不碰生产代码，也不动别的实现。
//
// # 断言链
//
//  1. 标记失败时意图仍在、未知标记没被写上（事务整体回滚）；
//  2. 同一 Key 的下一次 Acquire 拿不到执行权（意图还在 ⇒ contended）；
//  3. 租约过期且仍未裁决 ⇒ 转入需对账态（而不是重新授予执行权）。
//
// 第 3 条是这条用例真正的价值：它把「标记写失败」与「进程死在记账之前」这两种
// 形态收敛到**同一条判据**上——存储侧只剩一条未裁决的意图 ⇒ 必须先对账。
import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/sidetest"
)

// markFailReason 注入用的失败原因串：它同时是 CHECK 约束的禁写值与
// MarkUncertain 写入的 reason。
const markFailReason = "注入：标记写失败"

// TestPGMarkUncertainWriteFailureKeepsIntent 标记写失败 ⇒ 意图保住 ⇒ 不得重试。
func TestPGMarkUncertainWriteFailureKeepsIntent(t *testing.T) {
	dsn := requireDSN(t)
	eff, pool := freshEffectEnv(t, dsn)
	ctx := context.Background()

	// 时钟注入：过期判定读注入的时钟 ⇒ 过期场景确定触发（不靠 sleep 赌时序）。
	clock := sidetest.NewFakeClock()
	eff.UseClock(clock.Now)

	act := sideeffect.Action{
		Key:         sideeffect.Key("pg-markfail-key"),
		Resource:    "pg-markfail-res",
		Payload:     []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnknown, // 按最弱处理：失败即需对账
	}

	outcome, lease, _, err := eff.Acquire(ctx, act, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		t.Fatalf("首次 Acquire 应授予执行权，实得 %v", outcome)
	}

	// 注入真写失败：reason 写成被禁的值 ⇒ UPDATE 报错 ⇒ 事务整体回滚。
	// 约束的字面量内联：PostgreSQL 的 DDL 不接受绑定参数（参数只许出现在 DML 里）。
	if _, err := pool.Exec(ctx,
		`ALTER TABLE pgref_effect_action ADD CONSTRAINT pgref_markfail_poison CHECK (reason <> '`+markFailReason+`')`); err != nil {
		t.Fatalf("注入写失败约束失败: %v", err)
	}

	if err := eff.MarkUncertain(ctx, act, lease, markFailReason); err == nil {
		t.Fatalf("被约束拦下的标记写应当返回错误（实际返回了 nil）")
	}

	// 契约的直接形态：意图仍在、未知标记没被写上。
	var intentFence *int64
	var uncertain bool
	if err := pool.QueryRow(ctx,
		`SELECT intent_fence, uncertain FROM pgref_effect_action WHERE key = $1`,
		string(act.Key)).Scan(&intentFence, &uncertain); err != nil {
		t.Fatalf("读动作行失败: %v", err)
	}
	if intentFence == nil {
		t.Fatalf("标记写失败后执行意图必须仍在（它是唯一挡着重试的东西）")
	}
	if uncertain {
		t.Fatalf("标记写失败 ⇒ 未知标记不该被写上（事务已整体回滚）")
	}

	// 意图未裁决 ⇒ 不得重新授予执行权。
	outcome, _, _, err = eff.Acquire(ctx, act, time.Minute)
	if err != nil {
		t.Fatalf("标记写失败后的 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("未裁决的尝试不得被重新授予执行权（否则就是重复施加），实得 %v", outcome)
	}

	// 租约过期且仍未裁决 ⇒ 转入需对账态（与「进程死在记账之前」同一形态）。
	clock.Advance(2 * time.Minute)
	outcome, _, _, err = eff.Acquire(ctx, act, time.Minute)
	if err != nil {
		t.Fatalf("过期后的 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("租约过期且未裁决 ⇒ 该 Key 应转入需对账态，实得 %v", outcome)
	}
}

// freshEffectEnv 一个只含副作用闸的干净环境：独立 schema + 独立连接池。
//
// 为什么不用 freshStores：那条要建三份存储，这里只需要闸与连接池（注入约束、
// 读动作行都要直连）。schema 用完即删 ⇒ 用例之间互不可见。
func freshEffectEnv(t *testing.T, dsn string) (*EffectStore, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	admin := basePool(t, dsn)
	schema, err := NewSchema(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DropSchema(ctx, admin, schema) })
	if err := EnsureSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	pool := schemaPool(t, dsn, schema)
	return NewEffectStore(pool), pool
}
