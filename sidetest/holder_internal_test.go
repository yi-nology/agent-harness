package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「持有者身份」一族的**负对照**：证明这些场景真的有分辨力。
//
// brokenHolderStore 模拟「只比令牌、不铸造不校验持有者」的实现形态：
// Acquire 照发令牌但 Holder 恒为空；Renew / Complete / Release 一律放行。
// 它正是「顺手就写出来」的那种实现——栅栏令牌它校验了（这里连校验都省了），
// 持有者维度在它的世界观里不存在。
type brokenHolderStore struct {
	sideeffect.Store

	fence sideeffect.Fence
}

func (b *brokenHolderStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	b.fence++
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: b.fence, TTL: ttl},
		sideeffect.Result{}, nil
}

func (b *brokenHolderStore) Renew(context.Context, sideeffect.Action, sideeffect.Lease, time.Duration) (sideeffect.Lease, error) {
	return sideeffect.Lease{}, nil
}

func (b *brokenHolderStore) Complete(context.Context, sideeffect.Action, sideeffect.Lease, sideeffect.Result) (sideeffect.Result, error) {
	return sideeffect.Result{}, nil
}

func (b *brokenHolderStore) Release(context.Context, sideeffect.Action, sideeffect.Lease) error {
	return nil
}

// TestHolderScenariosRejectBrokenStore 负对照：不铸造、不校验持有者的实现，
// 必须被这一族场景判红。
func TestHolderScenariosRejectBrokenStore(t *testing.T) {
	broken := &brokenHolderStore{}

	if err := checkHolderMintedByStore(broken); err == nil {
		t.Fatal("坏实现（不铸造持有者身份）应被场景判红，套件却判它合规")
	}
	if err := checkNonHolderRejected(broken); err == nil {
		t.Fatal("坏实现（不校验持有者）应被「冒充被拒」场景判红，套件却判它合规")
	}
	if err := checkResourceMismatchRejected(broken); err == nil {
		t.Fatal("坏实现（不校验资源一致）应被「跨资源凭据」场景判红，套件却判它合规")
	}
}
