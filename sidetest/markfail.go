package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「标记失败」一族的契约场景（v0.33.0 新增）。
//
// # 这一族判的是什么
//
// MarkUncertain **写失败**之后，存储侧必须还剩下什么。
//
// 失效形态：标记没写进去 ⇒ 存储侧既没有「完成」也没有「未知」⇒ 另一个
// worker（或重启后的自己）再来一次 Acquire，看到的是「未完成、未占用」
// ⇒ 授予执行权 ⇒ 重放一个可能已生效的动作。第一次调用的返回值只是
// 栈上的错误——跨进程什么都没留下，「失败形态是变红」退化成了
// 「红只在这一次调用的栈上」。
//
// 契约要求（Store.MarkUncertain 第 5 条）：标记写失败时，该 Key 的执行
// 意图必须原样留在存储里——它是唯一还在挡着重试的东西。本场景用一个
// 套件内部的**故障注入装饰器**模拟「存储侧此刻不可用」。

// failingMarkStore 套件内部的故障注入装饰器：让 MarkUncertain **对调用方
// 报告失败**，模拟「存储侧此刻不可用」——语义拒绝（三项联查不过）由
// stale_fence_cannot_mark 一族覆盖，这里注入的是写失败。
//
// # 为什么「先走宿主实现、再把返回值替换成失败」
//
// 真实世界里「方法返回 error」与「存储状态没被动过」是两件事：宿主代码
// 完全可能在返回 error 的路径上已经动了状态（「先撤意图、标记没落地」
// 就是最坏的那种写法）。契约硬要求（Store.MarkUncertain 第 5 条）钉的是
// **剩下的状态必须挡住 Acquire**，不是「宿主的代码顺序好看」。故装饰器
// 先让宿主实现跑完（它想怎么动状态就怎么动），再把调用方看到的返回值
// 换成注入的失败——这样无论宿主内部怎么写，只要失败后存储侧挡不住
// 下一次 Acquire，本场景就红。
type failingMarkStore struct {
	sideeffect.Store

	fired bool
}

// failMark 让下一次 MarkUncertain 以存储故障形态失败（只失败一次）。
func (f *failingMarkStore) failMark() {
	f.fired = true
}

func (f *failingMarkStore) MarkUncertain(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	err := f.Store.MarkUncertain(ctx, act, lease, reason)
	if f.fired {
		return errors.New("注入的存储故障：标记未写入")
	}
	return err
}

// checkMarkFailureBlocksAcquire 钉住：标记失败后，同一 Key 的下一次 Acquire
// 必须被拒（不得授予执行权）。
//
// 判据支点是**执行意图**：标记没写成 ⇒ 意图还在 ⇒ Acquire 看见一次尚未
// 裁决的尝试 ⇒ 不得重新授予执行权。
func checkMarkFailureBlocksAcquire(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-markfail", "res-markfail")

	wrapped := &failingMarkStore{Store: st}

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	wrapped.failMark()
	if err := wrapped.MarkUncertain(ctx, a, lease, "注入的失败"); err == nil {
		return fmt.Errorf("故障注入的标记本应失败（场景自身的注入装置失效）")
	}

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("标记失败后的 Acquire 失败: %w", err)
	}
	if outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("标记写失败后不得授予执行权（意图必须还在挡着），实得 applied——重复施加的入口已打开")
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runMarkFailureBlocksAcquire(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkMarkFailureBlocksAcquire(st); err != nil {
		t.Fatalf("违反「标记失败后不得授予执行权」契约: %v", err)
	}
}
