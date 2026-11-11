// Command pgsmoke 参考部署的**冒烟入口**：一个宿主进程连上 PostgreSQL，
// 把副作用闸 + 栅栏下游 + 任务认领的关键判据各走一遍，全部通过即退出 0。
//
// 生命周期: Experimental（v0.33.0 定级）——部署冒烟入口，随参考部署演进，
// 不承诺任何兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// docker compose 的 smoke 服务跑它（examples/docker-compose.yml），一条命令
// 起「PostgreSQL + 参考宿主」并跑通冒烟。它不是测试替代品——完整契约判定
// 在 go test ./examples/pgstore/（compose 的 contract 服务 / make integration）；
// 冒烟只回答「这套部署能不能跑」。
//
// 用法：
//
//	GAK_PGREF_DSN=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
//	    go run ./examples/pgstore/cmd/pgsmoke
package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/concurrency/async"

	"github.com/yi-nology/goagent-kernel/examples/pgstore"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// smokeSchema 冒烟用固定 schema：每次运行先整块重建（可重复执行）。
const smokeSchema = "pgref_smoke"

func main() {
	dsn := os.Getenv("GAK_PGREF_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "pgsmoke: 未设置 GAK_PGREF_DSN（如 postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable）")
		os.Exit(1)
	}
	if err := run(context.Background(), dsn); err != nil {
		fmt.Fprintf(os.Stderr, "pgsmoke: 冒烟未通过: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("pgsmoke: 冒烟通过（副作用闸 / 栅栏下游 / 任务认领 全部落到 PostgreSQL）")
}

func run(ctx context.Context, dsn string) error {
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+smokeSchema+" CASCADE"); err != nil {
		return fmt.Errorf("清理冒烟 schema 失败: %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+smokeSchema); err != nil {
		return fmt.Errorf("建冒烟 schema 失败: %w", err)
	}
	if err := pgstore.EnsureSchema(ctx, admin, smokeSchema); err != nil {
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, func() *pgxpool.Config {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			panic(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = smokeSchema
		return cfg
	}())
	if err != nil {
		return fmt.Errorf("建连接池失败: %w", err)
	}
	defer pool.Close()

	effects := pgstore.NewEffectStore(pool)
	downstream := pgstore.NewDownstream(pool)
	board := pgstore.NewBoardStore(pool)

	// 1) 同一 Key 第二次 Acquire → 重放既有结果（已完成优先）。
	const key = sideeffect.Key("smoke/idempotency")
	const resource = "smoke/res-idempotency"
	runner := sideeffect.NewRunner(effects, time.Minute)
	act := sideeffect.Action{Key: key, Resource: resource, RetrySafety: sideeffect.RetrySafetySafe}
	first, _, err := runner.Execute(ctx, act, func(wctx context.Context, lease sideeffect.Lease) (sideeffect.Result, error) {
		return sideeffect.Result{Payload: []byte("v1")}, applyDownstream(wctx, downstream, resource, lease.Fence)
	})
	if err != nil || first != sideeffect.OutcomeApplied {
		return fmt.Errorf("首次执行应为 applied，实得 %v / %v", first, err)
	}
	second, res, err := runner.Execute(ctx, act, func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		return sideeffect.Result{}, fmt.Errorf("已完成的动作不该再执行")
	})
	if err != nil || second != sideeffect.OutcomeReplayed || string(res.Payload) != "v1" {
		return fmt.Errorf("第二次执行应为 replayed + 既有结果，实得 %v / %q / %v", second, res.Payload, err)
	}
	fmt.Println("[1] 幂等键：第二次 Acquire 重放既有结果（动作体未再执行）✓")

	// 2) 栅栏下游：陈旧令牌被拒且零写入（拒收 = ErrFenceStale）。
	if err := downstream.WriteFenced(ctx, resource, 0, func(context.Context) error { return nil }); err == nil {
		return fmt.Errorf("陈旧令牌的写入应被拒（实得 nil）")
	}
	if got, err := downstream.Marker(ctx, resource); err != nil || got != "m" {
		return fmt.Errorf("陈旧写入不得部分写（marker=%q, err=%v）", got, err)
	}
	fmt.Println("[2] 栅栏下游：陈旧令牌被拒且没有发生任何写入 ✓")

	// 3) 双宿主竞争同一 Key：恰好一个 applied（这里是同进程两个 goroutine，
	//    判据在存储——两个独立进程得到同一结论，e2e 用例在测试层验证）。
	const raceKey = sideeffect.Key("smoke/race")
	const raceRes = "smoke/res-race"
	raceRunner := sideeffect.NewRunner(effects, time.Minute)
	raceAct := sideeffect.Action{Key: raceKey, Resource: raceRes, RetrySafety: sideeffect.RetrySafetySafe}
	var wg sync.WaitGroup
	var applied int
	var mu sync.Mutex
	for range 2 {
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func：本仓门禁禁止裸 goroutine。
		async.GoSafe(func() {
			defer wg.Done()
			outcome, _, err := raceRunner.Execute(ctx, raceAct, func(wctx context.Context, lease sideeffect.Lease) (sideeffect.Result, error) {
				time.Sleep(200 * time.Millisecond) // 撑开窗口
				return sideeffect.Result{Payload: []byte("win")}, applyDownstream(wctx, downstream, raceRes, lease.Fence)
			})
			if err == nil && outcome == sideeffect.OutcomeApplied {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if applied != 1 {
		return fmt.Errorf("双宿主竞争应恰好一个 applied，实得 %d", applied)
	}
	fmt.Println("[3] 双宿主竞争：恰好一个获得执行权 ✓")

	// 4) 任务认领：两个 agent 抢同一任务，恰好一个赢家，令牌由存储铸造。
	board.Seed(taskboard.Task{ID: "smoke-task", State: taskboard.StateOpen})
	winners, err := taskboard.ConcurrentClaim(ctx, board, "smoke-task",
		[]taskboard.AgentID{"worker-a", "worker-b"}, time.Minute)
	if err != nil {
		return fmt.Errorf("并发认领判定失败: %w", err)
	}
	if winners != 1 {
		return fmt.Errorf("并发认领应恰好一个赢家，实得 %d", winners)
	}
	fmt.Println("[4] 任务认领：两个 agent 抢同一任务，恰好一个赢家 ✓")
	return nil
}

// applyDownstream 经栅栏下游写参考业务标记（冒烟的「施加」动作）。
func applyDownstream(ctx context.Context, ds *pgstore.Downstream, resource string, fence sideeffect.Fence) error {
	return ds.WriteFenced(ctx, resource, fence, func(wctx context.Context) error {
		_, err := pgstore.ExecWithin(wctx,
			`UPDATE pgref_downstream SET value = value || 'm' WHERE resource = $1`, resource)
		return err
	})
}
