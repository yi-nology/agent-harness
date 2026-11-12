// bench_test.go —— SideEffect 的性能基线（issue #101）。
//
// Runner.Execute 是「至多一次」施加的热路径：Acquire（一次原子判定，发放
// 令牌与持有者身份）→ 施加函数 → Complete（三项联查记账）。基线用进程内
// 最小存储量**本包判定链**的成本——存储的判据（意图登记 / 三项联查）按契约
// 写进替身，但真实存储的 IO 是宿主实现的事，不进本基线。
//
// # 怎么读这些数
//
// 基线不是门槛（口径见 docs/BENCH-BASELINE-20261113.md）。数据规模：
// 每次迭代一个全新 Key / Resource（授予路径满血形态：无重放、无竞争），
// 施加函数返回固定小 Result。
package sideeffect_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// benchStore sideeffect.Store 的最小内存实现：Acquire 的判定顺序按契约写
// （已完成 > 结果未知 > 执行中 > 资源被占 > 授予），令牌与持有者身份在授予时
// 铸造。基准只走「授予 → 施加 → 完成」路径，其余方法给契约形态的最小实现。
type benchStore struct {
	mu      sync.Mutex
	fence   map[string]sideeffect.Fence
	done    map[sideeffect.Key]sideeffect.Result
	held    map[string]string // resource → holder
	holders map[string]string // key → holder（三项联查用）
	maxSeen map[string]uint64 // 供 MintHolder 形态的不可预测身份派生（基准里用计数近似）
	seq     uint64
}

func newBenchStore() *benchStore {
	return &benchStore{
		fence:   map[string]sideeffect.Fence{},
		done:    map[sideeffect.Key]sideeffect.Result{},
		held:    map[string]string{},
		holders: map[string]string{},
		maxSeen: map[string]uint64{},
	}
}

func (s *benchStore) Acquire(_ context.Context, act sideeffect.Action, _ time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res, ok := s.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
	}
	if holder, ok := s.held[act.Resource]; ok && holder != "" {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	s.seq++
	s.fence[act.Resource]++
	s.held[act.Resource] = "holder-" + strconv.FormatUint(s.seq, 10)
	lease := sideeffect.Lease{
		Resource: act.Resource,
		Fence:    s.fence[act.Resource],
		Holder:   "holder-" + strconv.FormatUint(s.seq, 10),
	}
	s.holders[string(act.Key)] = lease.Holder
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

func (s *benchStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.done[act.Key]; ok {
		return cur, nil
	}
	if s.holders[string(act.Key)] != lease.Holder {
		return sideeffect.Result{}, sideeffect.ErrNotHolder
	}
	s.done[act.Key] = res
	delete(s.held, act.Resource)
	return res, nil
}

func (s *benchStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holders[string(act.Key)] != lease.Holder {
		return sideeffect.ErrNotHolder
	}
	delete(s.held, act.Resource)
	return nil
}

func (s *benchStore) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holders[string(act.Key)] != lease.Holder {
		return sideeffect.ErrNotHolder
	}
	delete(s.holders, string(act.Key))
	delete(s.held, act.Resource)
	return nil
}

func (s *benchStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ time.Duration) (sideeffect.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// ExecuteWith 在施加前必调 Renew 做持有资格检查（runner.go「检查做了什么」）：
	// 资格没了 = 闸已易主，返回 ErrLeaseLost 让调用方干净放弃。
	if s.holders[string(act.Key)] != lease.Holder {
		return sideeffect.Lease{}, sideeffect.ErrLeaseLost
	}
	return lease, nil
}

func (s *benchStore) MarkUncertain(_ context.Context, _ sideeffect.Action, _ sideeffect.Lease, _ string) error {
	return errors.New("bench：MarkUncertain 不在基准路径上")
}

func (s *benchStore) ResolveUncertain(_ context.Context, _ sideeffect.Action, _ sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	return sideeffect.OutcomeUncertain, sideeffect.Result{}, errors.New("bench：ResolveUncertain 不在基准路径上")
}

func (s *benchStore) BeginReconcile(_ context.Context, _ sideeffect.Action, _ time.Duration) (sideeffect.Reconcile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return sideeffect.Reconcile{
		Holder:    "reconciler-" + strconv.FormatUint(s.seq, 10),
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func BenchmarkSideEffectExecute(b *testing.B) {
	ctx := context.Background()
	store := newBenchStore()
	runner := sideeffect.NewRunner(store, 30*time.Second)
	apply := func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		return sideeffect.Result{Payload: []byte("ok")}, nil
	}
	var i int // 迭代号自计（b.Loop 下不读 b.N）：每个 Key 全新，授予路径满血形态
	for b.Loop() {
		i++
		act := sideeffect.Action{
			Key:         sideeffect.Key(strconv.Itoa(i)),
			Resource:    "res-" + strconv.Itoa(i),
			Payload:     []byte("bench"),
			RetrySafety: sideeffect.RetrySafetySafe,
		}
		outcome, _, err := runner.Execute(ctx, act, apply)
		if err != nil || outcome != sideeffect.OutcomeApplied {
			b.Fatalf("授予路径应成功：outcome=%v err=%v", outcome, err)
		}
	}
}
