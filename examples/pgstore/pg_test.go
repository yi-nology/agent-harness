package pgstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	oerrors "github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sidetest"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/taskboard/boardtest"
)

// 本文件是 PostgreSQL 参考实现的**契约接线**：
//
//   - TestSideEffectStoreContract  sidetest 29 条（三判合一 / 三项联查 / 意图 /
//     对账占用 / Unsafe 裁决 / 标记失败……全家族，硬失败不静默跳过）；
//   - TestTaskBoardContract        boardtest 全家族（含并发认领规模档）；
//   - TestDownstreamSinkContract   sidetest.RunSinkContract 6 条（栅栏一族，
//     与 sideeffect 包内内存替身跑同一段判据）；
//   - TestSnapshot…                checkpoint.StoreCAS 的单调 / 拒零用例。
//
// # 跳过与硬失败的边界
//
// 套件场景本身**硬失败**（本文件没有任何场景级 skip）；唯一的 skip 发生在
// **环境入口**：没有可连的 PostgreSQL 时用例无从构造被测实现。此时
// t.Skipf 写明原因与起法（go test -v 可见），与「静默跳过伪装成通过」
// 是两回事。CI 的 integration job（postgres service container）必跑本包，
// 本地跑 make integration；无 docker 时用 go test -short 显式跳过。
const dsnEnv = "GAK_PGREF_DSN"

// requireDSN 返回测试用 DSN。缺失即显式跳过（原因 + 起法写在跳过信息里）。
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s 未设置：PG 契约验证需要 PostgreSQL（不会在无库时伪造结果）。"+
			"起法：make integration，或 docker compose -f examples/docker-compose.yml up -d postgres 后"+
			" GAK_PGREF_DSN=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable go test ./examples/pgstore/",
			dsnEnv)
	}
	return dsn
}

// basePool 全测试共用的管理连接（建/删 schema 用）。
func basePool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("连接 PostgreSQL 失败（DSN 是否正确？服务是否健康？）: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("PostgreSQL 不可达（服务是否健康？）: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshStores 一个子测试一份全新实现：随机 schema → 建表 → 独立连接池
// （search_path 指到该 schema）→ 三份存储。t.Cleanup 整块删除。
//
// sidetest / boardtest 的 factory 每个子测试调一次、要求全新实现——
// 在共享数据库里「全新」的落法就是独立 schema。
func freshStores(t *testing.T, dsn string) (eff *EffectStore, sink *Downstream, board *BoardStore, snaps *SnapStore) {
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
	return NewEffectStore(pool), NewDownstream(pool), NewBoardStore(pool), NewSnapStore(pool)
}

// schemaPool 为指定 schema 开一个独立连接池（search_path 已指好）。
func schemaPool(t *testing.T, dsn, schema string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("解析 DSN 失败: %v", err)
	}
	searchPath(cfg, schema)
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("建连接池失败（schema %s）: %v", schema, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSideEffectStoreContract 跑副作用闸的**共享契约套件**（PG 版）。
//
// 这条就是 issue #94 的核心验收：跨进程判据（三判合一 / 三项联查 / 执行
// 意图 / 对账占用）第一次在真实数据库上被同一段套件判过——套件本身
// 不为任何实现开后门，内存版与 PG 版跑的是同一批场景。
func TestSideEffectStoreContract(t *testing.T) {
	dsn := requireDSN(t)
	sidetest.Run(t, func(_ *testing.T, _ *sidetest.FakeClock) sidetest.ClockedStore {
		eff, _, _, _ := freshStores(t, dsn)
		return eff
	})
}

// TestTaskBoardContract 跑任务认领面的**共享契约套件**（PG 版）。
func TestTaskBoardContract(t *testing.T) {
	dsn := requireDSN(t)
	boardtest.Run(t, func(_ *testing.T, _ *boardtest.FakeClock) boardtest.ClockedBoard {
		_, _, board, _ := freshStores(t, dsn)
		return board
	})
}

// TestDownstreamSinkContract 跑下游栅栏的**共享契约套件**（PG 版，issue #84）。
func TestDownstreamSinkContract(t *testing.T) {
	dsn := requireDSN(t)
	sidetest.RunSinkContract(t, func(*testing.T) sidetest.SinkProbe {
		_, sink, _, _ := freshStores(t, dsn)
		return sink
	})
}

// TestSnapshotSaveIfNewerRejectsStaleSeq 快照序号必须严格递增，陈旧写被拒且不落库。
func TestSnapshotSaveIfNewerRejectsStaleSeq(t *testing.T) {
	dsn := requireDSN(t)
	_, _, _, snaps := freshStores(t, dsn)
	ctx := context.Background()
	if err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 2, Status: "running"}); err != nil {
		t.Fatalf("首次落库失败: %v", err)
	}
	err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 1, Status: "running"})
	if !oerrors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("陈旧快照必须被拒为 ErrStaleSeq，实得 %v", err)
	}
	snap, err := snaps.Load(ctx, "r")
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap.Seq != 2 {
		t.Fatalf("落库的序号应仍为 2（陈旧写不得覆盖），实得 %d", snap.Seq)
	}
}

// TestSnapshotSaveIfNewerRejectsZeroSeq 序号为 0 的快照不得落库（不得覆盖既有快照）。
func TestSnapshotSaveIfNewerRejectsZeroSeq(t *testing.T) {
	dsn := requireDSN(t)
	_, _, _, snaps := freshStores(t, dsn)
	ctx := context.Background()
	if err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 10, Status: "running"}); err != nil {
		t.Fatalf("首次落库失败: %v", err)
	}
	err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 0, Status: "running"})
	if !oerrors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("序号为 0 的快照必须被拒为 ErrStaleSeq，实得 %v", err)
	}
	snap, err := snaps.Load(ctx, "r")
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap.Seq != 10 {
		t.Fatalf("落库的序号应仍为 10（序号为 0 的快照不得覆盖），实得 %d", snap.Seq)
	}
}

// TestSnapshotLoadMissingIsNotFound 无记录时必须返回 ErrNotFound（不是零值 + nil）。
func TestSnapshotLoadMissingIsNotFound(t *testing.T) {
	dsn := requireDSN(t)
	_, _, _, snaps := freshStores(t, dsn)
	if _, err := snaps.Load(context.Background(), "nope"); !oerrors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("无记录的读取必须返回 ErrNotFound，实得 %v", err)
	}
}

// TestSnapshotConcurrentSaveIfNewer 并发提交同一 (RunID, Seq)：恰好一个赢家；
// 此后更低序号被拒、更高序号可落。单调闸在存储侧成立。
func TestSnapshotConcurrentSaveIfNewer(t *testing.T) {
	dsn := requireDSN(t)
	_, _, _, snaps := freshStores(t, dsn)
	ctx := context.Background()
	const workers = 8
	errs := make(chan error, workers)
	for range workers {
		go func() {
			errs <- snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 3, Status: "running"})
		}()
	}
	wins := 0
	for range workers {
		if err := <-errs; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("并发提交同一序号应恰好一个成功，实得 %d", wins)
	}
	if err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 2, Status: "running"}); !oerrors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("更低的序号必须被拒为 ErrStaleSeq，实得 %v", err)
	}
	if err := snaps.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 5, Status: "running"}); err != nil {
		t.Fatalf("更高的序号应可落库: %v", err)
	}
	snap, err := snaps.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Seq != 5 {
		t.Fatalf("落库的应是最大序号 5，实得 %d", snap.Seq)
	}
}

// TestBoardSnapshotPassesInvariants 完成后的快照必须自洽（终态不带 owner 与令牌）。
func TestBoardSnapshotPassesInvariants(t *testing.T) {
	dsn := requireDSN(t)
	_, _, board, _ := freshStores(t, dsn)
	ctx := context.Background()
	board.Seed(taskboard.Task{ID: "t1", Title: "待领", State: taskboard.StateOpen})
	task, err := board.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	if err := board.Complete(ctx, "t1", task.Lease.Token); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	tasks, err := board.List(ctx)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if problems := taskboard.Check(tasks); len(problems) > 0 {
		t.Fatalf("快照不变量被违反: %v", problems)
	}
}
