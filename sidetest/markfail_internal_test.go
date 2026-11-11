package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「标记失败」一族的**负对照**：证明这个场景真的有分辨力。
//
// brokenMarkStore 模拟「先撤意图、标记没落地」的坏实现：它**会**登记执行
// 意图（Acquire 是正经的），但 MarkUncertain 进来先把意图删掉、不写未知态，
// 然后返回成功——经故障注入装饰器替换后，调用方看到的是 error，存储侧
// 却什么都不剩。这正是契约第 5 条要封的形态：失败的红只在这一次调用的
// 栈上，跨进程等于「没有标记」，下一个 worker 直接重放。
type brokenMarkStore struct {
	sideeffect.Store

	fence   sideeffect.Fence
	intents map[sideeffect.Key]sideeffect.Lease
}

func newBrokenMarkStore() *brokenMarkStore {
	return &brokenMarkStore{intents: map[sideeffect.Key]sideeffect.Lease{}}
}

func (b *brokenMarkStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	// 意图登记是正经的：未裁决的尝试挡住下一次 Acquire（不坏在这一步）。
	if _, ok := b.intents[act.Key]; ok {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	b.fence++
	lease := sideeffect.Lease{Resource: act.Resource, Fence: b.fence, Holder: "broken", TTL: ttl}
	b.intents[act.Key] = lease
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

func (b *brokenMarkStore) MarkUncertain(_ context.Context, act sideeffect.Action, _ sideeffect.Lease, _ string) error {
	// 坏就坏在这里：意图先撤，未知标记没落地，返回「成功」。
	delete(b.intents, act.Key)
	return nil
}

func (b *brokenMarkStore) Release(context.Context, sideeffect.Action, sideeffect.Lease) error {
	return nil
}

// TestMarkFailureScenarioRejectsBrokenStore 负对照：失败路径上把意图撤掉的
// 实现，必须被场景判红。
//
// 场景里的故障注入装饰器会**先走宿主实现**（让它的状态改动真的发生），
// 再把调用方看到的返回值换成注入的失败——所以这个坏实现「先撤意图」的
// 行为逃不掉：调用方看到失败，存储侧已经什么都不剩。
func TestMarkFailureScenarioRejectsBrokenStore(t *testing.T) {
	broken := newBrokenMarkStore()

	if err := checkMarkFailureBlocksAcquire(broken); err == nil {
		t.Fatal("坏实现（失败路径撤掉意图且不写未知态）应被场景判红，套件却判它合规")
	}
}
