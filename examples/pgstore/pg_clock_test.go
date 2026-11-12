package pgstore

// pg_clock_test.go —— 「租约时间来自数据库」的两条判据（评审第三轮 P0 #2）。
//
// # 为什么判据长这样
//
// 评审要求「模拟 ±30s 的 worker 时钟偏移，验证不会提前抢占租约」。修复后的
// 架构里这个场景**不可表达**：worker 不再提供任何时间（判定与写入都来自
// 数据库 clock_timestamp，注入只是系统时间的测试替身）——「某个 worker 的
// 本地偏移」在存储接口上没有入口，这正是修复本身。可证的等价判据是下面两条：
//
//  1. 源码判据：生产代码里不存在进程本地时钟的读取点（time.Now）。
//     它直接钉住缺陷的物理形态——本地时钟读取点一旦写回来，跨节点漂移的
//     入口就回来了。不依赖任何环境，make check 阶段即判。
//  2. 行为判据：默认构造（未注入）的存储，租约到期时刻锚在**取得行锁之后**
//     的数据库时间上——等锁期间的时间流逝不得计入租约寿命（评审点名
//     CURRENT_TIMESTAMP 固定事务起点的坑）。这条有时序对照：把取时刻挪到
//     锁前，本用例必红。
//
// 同机测试里 time.Now 与 clock_timestamp 差异 <1ms，判据 2 分不出「读本地
// 还是读 DB」——那正是判据 1 存在的理由（结构性判定管形态，行为判定管锚点）。
import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// TestNoProcessClockInProduction 源码判据：本包生产文件不得读进程本地时钟。
//
// 注释豁免（剥掉 // 之后再查）：修复的讨论本身必须提到 time.Now；
// 判据钉的是**代码**在读哪个时钟，不是词面出现了没有。
// 反向验证：把 NewEffectStore 的 now 改回 time.Now → 本用例必红。
func TestNoProcessClockInProduction(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读包目录失败: %v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		checked++
		for i, line := range strings.Split(string(data), "\n") {
			if idx := strings.Index(line, "//"); idx >= 0 {
				line = line[:idx] // 剥行注释与行内注释
			}
			if strings.Contains(line, "time.Now") {
				t.Errorf("%s:%d 生产代码读进程本地时钟 time.Now——租约/认领是跨节点共享的事实，"+
					"时间源必须是数据库 clock_timestamp（见 doc.go「时钟」段）", name, i+1)
			}
		}
	}
	if checked == 0 {
		t.Fatal("判据空转：没扫到任何生产文件（目录结构变了？）")
	}
}

// TestLeaseDeadlineAnchoredAfterRowLock 行为判据：租约到期时刻锚在
// **取得行锁之后**的数据库时间上。
//
// 结构：父事务锁住资源行 → 另一个连接上的 Acquire 卡在行锁上（事务已开、
// 未提交）→ 保持 2.5s → 释放锁 → Acquire 完成。若实现把「当前时刻」取在
// 锁**之前**（事务开头 / 等锁之前），写入的 until 会比「锁后时刻 + TTL」
// 早约 2.5s——断言到期余量 ≥ TTL - 1.5s 即可把锚点错误判红。
//
// 为什么父事务先插资源行：资源行不存在时 Acquire 会直接 INSERT 新行，
// 拿不到「等已有行的锁」这个形态。
func TestLeaseDeadlineAnchoredAfterRowLock(t *testing.T) {
	dsn := requireDSN(t)
	eff, pool := freshEffectEnv(t, dsn)
	ctx := context.Background()

	const ttl = 10 * time.Minute
	resource := fmt.Sprintf("clk-res-%d", time.Now().UnixNano())
	key := sideeffect.Key(fmt.Sprintf("clk-key-%d", time.Now().UnixNano()))

	// 预建资源行（提交），父事务随后锁它。
	if _, err := pool.Exec(ctx,
		`INSERT INTO pgref_effect_resource (resource) VALUES ($1)`, resource); err != nil {
		t.Fatalf("预置资源行失败: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开父事务失败: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT resource FROM pgref_effect_resource WHERE resource = $1 FOR UPDATE`,
		resource); err != nil {
		t.Fatalf("锁资源行失败: %v", err)
	}

	// Acquire 从另一条连接进来，卡在行锁上（其事务已开、未提交）。
	type res struct {
		err error
	}
	done := make(chan res, 1)
	go func() {
		act := sideeffect.Action{Key: key, Resource: resource, Payload: []byte("p")}
		_, _, _, err := eff.Acquire(ctx, act, ttl)
		done <- res{err: err}
	}()

	time.Sleep(2500 * time.Millisecond) // 足够让 Acquire 卡进锁等待
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("释放父事务失败: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Acquire 失败: %v", r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Acquire 在锁释放后仍未完成")
	}

	// 断言：到期余量锚在「取得锁之后」——SQL 侧同源比较（读的是 DB 时间）。
	var remainSec float64
	if err := pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM (until - clock_timestamp()))
		   FROM pgref_effect_resource WHERE resource = $1`,
		resource).Scan(&remainSec); err != nil {
		t.Fatalf("读到期余量失败: %v", err)
	}
	if min := ttl.Seconds() - 1.5; remainSec < min {
		t.Fatalf("租约余量 %.1fs < TTL-1.5s（%.1fs）：到期时刻锚在了取得行锁之前"+
			"（等锁时长被算进了租约寿命，或时间源不是数据库）", remainSec, min)
	}
}
