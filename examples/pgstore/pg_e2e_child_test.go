//go:build unix

// pg_e2e_child_test.go —— 故障恢复 e2e 的**子进程侧**（issue #95）。
//
// 父进程（pg_e2e_test.go）用 os/exec re-exec 本测试二进制的这个入口，
// 对它做真实的进程操作（SIGKILL / 等待退出）。子进程与父进程唯一的共享
// 状态是 PostgreSQL（GAK_PGREF_DSN + 测试专属 schema）——这正是
// 「判据归存储，不归进程」要成立的那个前提：SIGKILL 之后，内存里的一切
// （连接、锁、变量）都不存在，留下的只有库里的行。
package pgstore

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 子进程模式的环境变量（父进程写、子进程读；DSN 复用 dsnEnv）。
const (
	envChildMode = "GAK_PGREF_CHILD"
	envSchema    = "GAK_PGREF_SCHEMA"
	envKey       = "GAK_PGREF_KEY"
	envResource  = "GAK_PGREF_RESOURCE"
	envTTLMS     = "GAK_PGREF_TTL_MS"
	envRunID     = "GAK_PGREF_RUN_ID"
	envSeq       = "GAK_PGREF_SEQ"
	envStartAt   = "GAK_PGREF_START_AT"
)

// 四种子进程模式（名字即子进程里效果体的剧本）。
const (
	// childModeRecordThenHang 效果体登记调用后挂住（等 SIGKILL）——死在施加之前。
	childModeRecordThenHang = "record-then-hang"
	// childModeApplyThenHang 效果体经栅栏写完下游后挂住——死在施加之后、记账之前。
	childModeApplyThenHang = "apply-then-hang"
	// childModeOneShot 效果体写完下游、短暂停顿后正常收场——竞争用例的两个进程。
	childModeOneShot = "one-shot"
	// childModeAcquireOnly 只抢闸、不施加不记账：它会卡在父进程持有的行锁上
	// （事务已开、未提交）——父进程在这个状态下 SIGKILL 它，验证「进程死在
	// 事务中间 ⇒ 库侧不留半截状态、不留悬挂锁」。
	childModeAcquireOnly = "acquire-only"
	// childModeSnapshotSave 只提交一次进度快照：并发快照用例的两个进程。
	childModeSnapshotSave = "snapshot-save"
)

// 子进程提交快照的两种落点（父进程按行解析）。
const (
	// snapSaved 快照已落库（本次提交被存储接受）。
	snapSaved = "SAVED"
	// snapStale 快照被判陈旧（单调闸拒绝，未写入）。
	snapStale = "STALE"
)

// TestPGRefChildProcess 子进程入口：只在父进程 re-exec 本测试二进制
// （设置了 GAK_PGREF_CHILD）时才真正执行；常规测试运行显式跳过。
func TestPGRefChildProcess(t *testing.T) {
	mode := os.Getenv(envChildMode)
	if mode == "" {
		t.Skip("子进程入口（re-exec 模式）：常规测试运行不执行，见 pg_e2e_test.go 的父进程用例")
	}
	runChildE2E(t, mode)
}

// runChildE2E 子进程剧本：抢闸 → 按模式施加 → 打印判定结论。
//
// 判定结论走 stdout（OUTCOME=/UNCERTAIN= 前缀行），父进程按行解析；
// 「结果未知」是合法判据不是故障——照常上报，以 0 退出。
func runChildE2E(t *testing.T, mode string) {
	t.Helper()
	if mode == childModeSnapshotSave {
		runChildSnapshotSave(t)
		return
	}
	dsn := os.Getenv(dsnEnv)
	schema := os.Getenv(envSchema)
	key := os.Getenv(envKey)
	resource := os.Getenv(envResource)
	if dsn == "" || schema == "" || key == "" || resource == "" {
		t.Fatalf("子进程环境不完整：dsn/schema/key/resource 均必须设置")
	}
	ttlMS, err := strconv.Atoi(os.Getenv(envTTLMS))
	if err != nil || ttlMS <= 0 {
		t.Fatalf("子进程环境不完整：TTL 非法 %q", os.Getenv(envTTLMS))
	}
	ttl := time.Duration(ttlMS) * time.Millisecond

	pool := schemaPool(t, dsn, schema)
	eff := NewEffectStore(pool)
	ds := NewDownstream(pool)
	runner := sideeffect.NewRunner(eff, ttl)
	act := sideeffect.Action{
		Key:         sideeffect.Key(key),
		Resource:    resource,
		Payload:     []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnknown, // 按最弱处理：失败即需对账
	}

	if mode == childModeAcquireOnly {
		// 直接调 Acquire：它卡在父进程持有的行锁上（事务已开、未提交），
		// 父进程要在这个状态下 SIGKILL 它。
		outcome, _, _, err := eff.Acquire(context.Background(), act, ttl)
		fmt.Printf("OUTCOME=%s\n", outcome)
		if err != nil {
			t.Fatalf("子进程 Acquire 失败: %v", err)
		}
		return
	}

	outcome, _, execErr := runner.Execute(context.Background(), act, func(ctx context.Context, lease sideeffect.Lease) (sideeffect.Result, error) {
		if err := recordEffectCall(ctx, pool, key); err != nil {
			return sideeffect.Result{}, err
		}
		switch mode {
		case childModeRecordThenHang:
			// 施加之前挂住：下游不会有任何写入。
			time.Sleep(60 * time.Second)
			return sideeffect.Result{Payload: []byte("never")}, nil
		case childModeApplyThenHang, childModeOneShot:
			// 经**栅栏下游**施加（Fence 随租约传入，比较与写入同一次原子操作）。
			if err := applyDownstream(ctx, ds, resource, lease.Fence, "applied"); err != nil {
				return sideeffect.Result{}, err
			}
			if mode == childModeOneShot {
				time.Sleep(300 * time.Millisecond) // 撑开竞争窗口，让双进程真的撞上
				return sideeffect.Result{Payload: []byte("applied")}, nil
			}
			time.Sleep(60 * time.Second) // 挂住等 SIGKILL（记账不会发生）
			return sideeffect.Result{Payload: []byte("applied")}, nil
		default:
			return sideeffect.Result{}, fmt.Errorf("未知子进程模式 %q", mode)
		}
	})

	fmt.Printf("OUTCOME=%s\n", outcome)
	if execErr != nil {
		if errors.Is(execErr, sideeffect.ErrOutcomeUnknown) {
			fmt.Println("UNCERTAIN=1")
			return
		}
		t.Fatalf("子进程执行失败: %v", execErr)
	}
}

// runChildSnapshotSave 子进程剧本：等到屏障时刻后提交**一次**进度快照，
// 报告落点（SAVED = 存储接受 / STALE = 单调闸拒绝）。
//
// 为什么单独一个剧本：快照单调闸是**存储侧**的判定（一条带条件的 upsert，
// 见 snapshot.go），它不依赖任何进程内状态——正因如此，「两个进程同时提交」
// 与「一个进程里两个 goroutine 同时提交」的差别必须由**真实进程**来证：
// 后者共享同一个连接池与同一个运行时，前者连连接都不共享。
//
// 屏障：两个子进程等到**同一个绝对时刻**（envStartAt，Unix 纳秒）再提交，
// 使两次提交真的撞在一起，而不是谁先起跑谁先写完。
func runChildSnapshotSave(t *testing.T) {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	schema := os.Getenv(envSchema)
	runID := os.Getenv(envRunID)
	seqRaw := os.Getenv(envSeq)
	startRaw := os.Getenv(envStartAt)
	if dsn == "" || schema == "" || runID == "" || seqRaw == "" || startRaw == "" {
		t.Fatalf("子进程环境不完整：dsn/schema/run_id/seq/start_at 均必须设置")
	}
	seq, err := strconv.ParseUint(seqRaw, 10, 64)
	if err != nil || seq == 0 {
		t.Fatalf("子进程序号非法 %q（0 不是一次真实提交）", seqRaw)
	}
	startAt, err := strconv.ParseInt(startRaw, 10, 64)
	if err != nil {
		t.Fatalf("子进程屏障时刻非法 %q", startRaw)
	}
	if d := time.Until(time.Unix(0, startAt)); d > 0 {
		time.Sleep(d)
	}

	pool := schemaPool(t, dsn, schema)
	snaps := NewSnapStore(pool)
	err = snaps.SaveIfNewer(context.Background(), checkpoint.Snapshot{
		RunID:  runID,
		Seq:    seq,
		Status: "running",
	})
	switch {
	case err == nil:
		fmt.Printf("OUTCOME=%s\n", snapSaved)
	case errors.Is(err, checkpoint.ErrStaleSeq):
		fmt.Printf("OUTCOME=%s\n", snapStale)
	default:
		t.Fatalf("子进程提交快照失败: %v", err)
	}
}

// applyDownstream 经栅栏下游写一次业务数据（参考宿主的「施加」动作）。
func applyDownstream(ctx context.Context, ds *Downstream, resource string, fence sideeffect.Fence, value string) error {
	return ds.WriteFenced(ctx, resource, fence, func(wctx context.Context) error {
		_, err := ExecWithin(wctx, `UPDATE pgref_downstream SET value = $2 WHERE resource = $1`, resource, value)
		return err
	})
}

// recordEffectCall 效果体调用计数（UPSERT 自增）：跨进程断言「动作体执行了几次」
// 的唯一真相——内存计数器随进程死亡消失，库里这行不会。
func recordEffectCall(ctx context.Context, pool *pgxpool.Pool, key string) error {
	if _, err := pool.Exec(ctx,
		`INSERT INTO pgref_e2e_calls (key, calls) VALUES ($1, 1)
		 ON CONFLICT (key) DO UPDATE SET calls = pgref_e2e_calls.calls + 1`, key); err != nil {
		return errors.Wrap(err, "pgstore e2e: 登记效果体调用失败", "key", key)
	}
	return nil
}
