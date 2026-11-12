//go:build unix

// pg_e2e_txkill_test.go —— 进程死在**事务中间**时的库侧状态。
//
// # 这一条补的是什么
//
// 已有的三条 e2e 覆盖的是：死在施加之前、死在施加之后记账之前、双进程竞争。
// 三条里的 SIGKILL 都发生在**没有未提交事务**的时刻（子进程挂在动作体里，
// 动作体的事务早已提交）⇒ 库侧本来就干净，「回滚」这件事没被走过。
//
// 本条把 SIGKILL 落在**子进程持有未提交事务**的时刻：它卡在 Acquire 的事务里
// （等父进程持有的行锁），父进程在它卡住时 SIGKILL 它 ⇒ 那个事务必须整体回滚，
// 库里不能留下半截状态（悬着的意图、悬着的锁）。
//
// # 为什么这不是「测 PostgreSQL」
//
// 测的是**本仓存储实现的事务边界**：Acquire 把「三判合一 + 令牌 + 意图」放在
// 一次事务里（见 effect_acquire.go 的头注）。若将来有人把它拆成两个事务，或
// 漏了一次提交，被杀的进程就会在库里留下半截状态——而这个形态只有在
// 「进程死在事务中间」时才会显形（正常路径下事务都会提交或回滚）。
//
// # 一个必须说清的技术前提
//
// SIGKILL 之后，PG 侧的后端要等到「下一次从 socket 读」才发现对端已经没了。
// 而它此刻正卡在行锁上（不读 socket）⇒ 事务与锁会滞留一段时间，库里暂时是
// 「已死进程留下的未提交事务」。断言它什么时候消失就成了时序赌博。
// 故本用例在 SIGKILL 之后**显式回收**那个后端（PG 回收后端时回滚其事务）——
// 与 PG 自己发现对端死亡后的处置是同一条路径，只是把时机变成确定的。
package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// TestPGE2EKillInsideTransactionLeavesNoHalfState 进程死在 Acquire 的事务中间：
// 库里不留半截状态，同一个 Key 之后照常可被执行。
func TestPGE2EKillInsideTransactionLeavesNoHalfState(t *testing.T) {
	e := freshE2E(t, 60*time.Second)
	ctx := context.Background()

	// 预置动作行：子进程的 Acquire 要在这一行上等锁（行不存在时它会直接插入，
	// 插完就往下走 ⇒ 就没有「卡在未提交事务里」这个状态了）。
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO pgref_effect_action (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`,
		e.key); err != nil {
		t.Fatalf("预置动作行失败: %v", err)
	}

	// 父进程占住该 Key 的行锁 ⇒ 子进程的 Acquire 会卡在取这行的地方，
	// 而那一步在**事务内**：此刻它的事务已开、未提交。
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开事务失败: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedKey string
	if err := tx.QueryRow(ctx,
		`SELECT key FROM pgref_effect_action WHERE key = $1 FOR UPDATE`,
		e.key).Scan(&lockedKey); err != nil {
		t.Fatalf("锁住动作行失败: %v", err)
	}

	child := e.startChild(t, childModeAcquireOnly)
	// 等它真的卡在锁上（不是还没开始）：看它是否在等锁。
	waitFor(t, 30*time.Second, "子进程卡在行锁上", func() (bool, error) {
		return backendWaitingOnLock(ctx, e.pool, "pgref_effect_action")
	})
	child.kill(t) // 真实进程死亡
	reclaimBackend(ctx, e.pool, "pgref_effect_action")

	// 父进程的行锁不能被死掉的那个进程拖住：回滚必须干脆。
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("父进程回滚失败: %v", err)
	}

	// 被杀进程的事务必须整体回滚：它插入的资源行不该留下。
	// （若 Acquire 把「登记资源行」拆成独立的一次提交，这一行就会残留——
	// 那正是「半截状态」：库里有一条属于一个已经死掉的进程的行。）
	var resRows int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM pgref_effect_resource WHERE resource = $1`,
		e.resource).Scan(&resRows); err != nil {
		t.Fatalf("读资源行失败: %v", err)
	}
	if resRows != 0 {
		t.Fatalf("被杀进程的事务应整体回滚（它插入的资源行不该残留），实得 %d 行", resRows)
	}

	// 同一个 Key 能正常授予执行权：没有悬着的意图、没有悬着的锁。
	outcome, _, _, err := e.eff.Acquire(ctx, e.action(), e.ttl)
	if err != nil {
		t.Fatalf("被杀进程之后的 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		t.Fatalf("被杀进程的事务回滚后，同一 Key 应能正常授予执行权，实得 %v", outcome)
	}

	// 死在抢闸阶段的进程没执行过动作体：库里没有「已生效的副作用」。
	// （读不到行 = 一次都没执行过：子进程连动作体都没进，计数行自然不存在。）
	calls, err := e.effectCalls(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("读动作体调用次数失败: %v", err)
	}
	if calls != 0 {
		t.Fatalf("死在抢闸阶段的进程不该执行过动作体，实得 %d 次", calls)
	}
}

// backendWaitingOnLock 是否有后端正卡在与该表名相关的锁上。
//
// 用 pg_stat_activity 而不是「睡一会儿再看」：被等的那个后端处在锁等待里，
// 这是它确实卡住了的可观测证据。
func backendWaitingOnLock(ctx context.Context, pool *pgxpool.Pool, table string) (bool, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_stat_activity
		  WHERE wait_event_type = 'Lock' AND datname = current_database()
		    AND query LIKE '%' || $1 || '%'`,
		table).Scan(&n)
	return n > 0, err
}

// reclaimBackend 回收被 SIGKILL 的子进程在 PG 侧的后端（回滚它的未提交事务）。
//
// 找不到就当它已经自己退了（PG 读到 EOF 时同样会回滚）——两条路径的库侧结果一致。
func reclaimBackend(ctx context.Context, pool *pgxpool.Pool, table string) {
	var pid int
	err := pool.QueryRow(ctx,
		`SELECT pid FROM pg_stat_activity
		  WHERE wait_event_type = 'Lock' AND datname = current_database()
		    AND query LIKE '%' || $1 || '%' LIMIT 1`,
		table).Scan(&pid)
	if err != nil {
		return
	}
	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		// 终止失败不致命：后端迟早会自己发现对端没了。
		return
	}
}
