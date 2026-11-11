// resume_no_replay_e2e_test.go —— 跨包履约链：崩溃 → 重启 → 续跑**不重放**副作用。
//
// # 为什么单包单测盖不住这件事
//
// 三个包各自的判据都是对的，但它们**各答一半的问题**：
//
//	checkpoint.BuildPlan   —— 「哪几步不用再跑」
//	sideeffect.Runner      —— 「同一个逻辑动作只施加一次」
//	commitgate.Guard       —— 「同一环节的终态只被提交一次」
//
// 合起来才是本仓对宿主的承诺：**进程死过一次之后，已经发生的副作用不再发生，
// 已经落定的终态不再被改**。而这条承诺的成败取决于「重启时有没有用持久化事实
// 回灌进程内状态」——三处回灌（SeqGate / sideeffect store / commitgate）漏一处，
// 每一个单包判据仍然全绿，只有整条链路跑一遍才看得见。
//
// 本文件就是那条链路：一个共享的「持久化存储」+ 一次真崩溃（不交还闸、不落最终
// 快照）+ 一次重启，逐条断言承诺成立。存储是进程内的最小实现，但**语义按契约写**
// （Acquire 一次原子判定、Complete 校验令牌、SaveIfNewer 严格递增）——把契约
// 抄成宽松版就等于把被测对象换掉，用例会绿得毫无意义。
package checkpoint_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/commitgate"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// durable 一份「持久化」状态：宿主重启后仍然为真的全部事实。
//
// 快照、副作用记账、终态提交三样**必须落在同一个存储里**，因为它们要回答的是
// 同一个问题（「这次运行到哪了」）；拆成三份就会出现「快照说做过、账本说没做过」
// 这种各说各话的组合。
type durable struct {
	mu sync.Mutex

	snaps map[string]checkpoint.Snapshot

	done      map[sideeffect.Key]sideeffect.Result
	held      map[string]sideeffect.Lease
	expiry    map[string]time.Time
	maxFence  map[string]sideeffect.Fence
	terminals map[commitgate.Ref]commitgate.Commit

	// applied 真实世界的副作用计数：**跨重启累计**，是「有没有重放」的唯一证据。
	applied map[string]int
}

func newDurable() *durable {
	return &durable{
		snaps:     map[string]checkpoint.Snapshot{},
		done:      map[sideeffect.Key]sideeffect.Result{},
		held:      map[string]sideeffect.Lease{},
		expiry:    map[string]time.Time{},
		maxFence:  map[string]sideeffect.Fence{},
		terminals: map[commitgate.Ref]commitgate.Commit{},
		applied:   map[string]int{},
	}
}

// ── checkpoint.StoreCAS ──────────────────────────────────────────────

func (d *durable) SaveIfNewer(_ context.Context, s checkpoint.Snapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.snaps[s.RunID]; ok && cur.Seq >= s.Seq {
		return checkpoint.ErrStaleSeq
	}
	d.snaps[s.RunID] = s.Clone()
	return nil
}

func (d *durable) Load(_ context.Context, runID string) (checkpoint.Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.snaps[runID]
	if !ok {
		return checkpoint.Snapshot{}, checkpoint.ErrNotFound
	}
	return s.Clone(), nil
}

// ── sideeffect.Store ────────────────────────────────────────────────

func (d *durable) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 已完成优先于占用：后来者应当拿回结果，而不是被「资源正忙」挡住。
	if res, ok := d.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
	}
	if lease, ok := d.held[act.Resource]; ok && time.Now().Before(d.expiry[act.Resource]) {
		_ = lease
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	d.maxFence[act.Resource]++
	lease := sideeffect.Lease{Resource: act.Resource, Fence: d.maxFence[act.Resource], TTL: ttl}
	d.held[act.Resource] = lease
	d.expiry[act.Resource] = time.Now().Add(ttl)
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

func (d *durable) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur, ok := d.held[act.Resource]
	if !ok || cur.Fence != lease.Fence {
		return sideeffect.Result{}, sideeffect.ErrFenceStale
	}
	d.done[act.Key] = res
	delete(d.held, act.Resource)
	delete(d.expiry, act.Resource)
	return res, nil
}

func (d *durable) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.held[act.Resource]; ok && cur.Fence == lease.Fence {
		delete(d.held, act.Resource)
		delete(d.expiry, act.Resource)
	}
	return nil
}

func (d *durable) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur, ok := d.held[act.Resource]
	if !ok || cur.Fence != lease.Fence {
		return sideeffect.Lease{}, sideeffect.ErrFenceStale
	}
	d.expiry[act.Resource] = time.Now().Add(ttl)
	lease.TTL = ttl
	return lease, nil
}

// ── 宿主侧接线：终态持久化与回灌 ──────────────────────────────────────

// persist 把一次终态提交记进持久化事实（宿主在 Acquire 成功后立即做）。
func (d *durable) persist(c commitgate.Commit) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.terminals[c.Ref] = c
}

// seedGate 用持久化事实回灌一道**新**闸（宿主每次启动做一次）。
func (d *durable) seedGate(g *commitgate.Guard) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.terminals {
		g.Seed(c)
	}
	return len(d.terminals)
}

func (d *durable) appliedCount(step string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.applied[step]
}

// apply 施加一步的副作用（真实世界计数写在 durable.applied 上）。
//
// 计数放在**执行体内部**而不是调用方：只有执行体真的被调用了才算重放，
// 用返回值或 Outcome 反推都可能把「没执行」误读成「执行了」。
func apply(ctx context.Context, r *sideeffect.Runner, d *durable, runID, step, resource string) (sideeffect.Outcome, error) {
	act := sideeffect.Action{Key: sideeffect.Key(runID + ":" + step), Resource: resource}
	out, _, err := r.Execute(ctx, act, func(context.Context) (sideeffect.Result, error) {
		d.mu.Lock()
		d.applied[step]++
		d.mu.Unlock()
		return sideeffect.Result{Payload: []byte(step)}, nil
	})
	return out, err
}

// isTerminal 终态词表由宿主注入（本包不认状态取值）。
func isTerminal(status string) bool { return status == "succeeded" }

// TestE2EResumeAfterCrashDoesNotReplay 整条履约链一次跑完：
//
//	① checkpoint 判定「第一步不用再跑」
//	② sideeffect 判定「同一个动作不重复施加」——即便宿主重试了它
//	③ commitgate 判定「终态不会被重复提交」
//	④ checkpoint 判定「快照序号不回退」
//
// 四条都在**重启后**验，因为要验的正是「持久化事实能不能独立支撑恢复」。
func TestE2EResumeAfterCrashDoesNotReplay(t *testing.T) {
	ctx := context.Background()
	st := newDurable()
	graph := []checkpoint.StepDef{{ID: "collect"}, {ID: "enrich"}, {ID: "publish"}}

	// ── 第一次进程：施加第一步的副作用、落一次快照、提交一个终态，然后崩
	runner := sideeffect.NewRunner(st, time.Minute)
	committer := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st)

	if plan := checkpoint.BuildPlan(graph, nil); len(plan.Run) != 3 || len(plan.Skip) != 0 {
		t.Fatalf("首次执行应全量 Run，实得 run=%v skip=%v", plan.Run, plan.Skip)
	}
	if out, err := apply(ctx, runner, st, "run-1", "collect", "store-main"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("第一步应当施加，实得 outcome=%v err=%v", out, err)
	}
	if err := committer.Commit(ctx, checkpoint.Snapshot{
		RunID: "run-1", Seq: 1, Status: "running", Done: []string{"collect"},
	}); err != nil {
		t.Fatalf("第一步后的快照应当落库: %v", err)
	}

	gate := commitgate.NewGuard(isTerminal)
	terminal := commitgate.Commit{
		Ref:    commitgate.Ref{RunID: "run-1", Step: "collect"},
		Status: "succeeded", Branch: "worker-a", Seq: 1,
	}
	if _, err := gate.Acquire(terminal); err != nil {
		t.Fatalf("首次终态应当被接受: %v", err)
	}
	st.persist(terminal)

	// 崩溃：**不** Release（真实崩溃不会交还闸），也**不**写最终快照。
	// 进程内的 SeqGate 与 Guard 随之消失——它们只活在这段作用域里。

	// ── 重启：只剩持久化事实
	snap, err := st.Load(ctx, "run-1")
	if err != nil {
		t.Fatalf("重启后应能读回快照: %v", err)
	}
	plan := checkpoint.BuildPlan(graph, &snap)
	if len(plan.Skip) != 1 || plan.Skip[0] != "collect" {
		t.Fatalf("① 续跑应跳过已完成且可信的 collect，实得 skip=%v", plan.Skip)
	}
	if len(plan.Run) != 2 || plan.Run[0] != "enrich" || plan.Run[1] != "publish" {
		t.Fatalf("① 其余步骤应继续执行且保持图序，实得 run=%v", plan.Run)
	}

	// 宿主重试第一步：它是「已完成」的那一步，重试是合法动作（宿主可能不知道
	// 自己崩在哪）。这一步必须**不产生第二次副作用**。
	restartedRunner := sideeffect.NewRunner(st, time.Minute)
	out, err := apply(ctx, restartedRunner, st, "run-1", "collect", "store-main")
	if err != nil {
		t.Fatalf("② 重试已完成的第一步不应报错: %v", err)
	}
	if out != sideeffect.OutcomeReplayed {
		t.Fatalf("② 已完成动作的重试必须判 replayed，实得 %v", out)
	}
	if got := st.appliedCount("collect"); got != 1 {
		t.Fatalf("② 续跑不得重放副作用：实际施加次数 = %d, want 1", got)
	}

	// 终态独占：新进程的闸是空的，回灌之后才拦得住。
	restartedGate := commitgate.NewGuard(isTerminal)
	if n := st.seedGate(restartedGate); n != 1 {
		t.Fatalf("③ 回灌的终态条数 = %d, want 1", n)
	}
	if _, err := restartedGate.Acquire(terminal); !errors.Is(err, commitgate.ErrAlreadyCommitted) {
		t.Fatalf("③ 重启后重复提交同一终态必须被拒（ErrAlreadyCommitted），实得 %v", err)
	}

	// ④ 快照序号不回退。这条判据有**两道**闸，必须分别验：只测「整体被拒」
	// 会把存储侧 CAS 的功劳记在进程内闸头上——把 Seed 改成空操作照样绿。
	// 这正是「判据因为别的原因绿」的典型形态，故这里把存储换成永不作判据的实现。
	stale := checkpoint.Snapshot{RunID: "run-1", Seq: 1, Status: "running", Done: []string{"collect"}}
	gateCommitter := checkpoint.NewCommitter(checkpoint.NewSeqGate(), alwaysAccept{st})
	if err := gateCommitter.SeedFromStore(ctx, "run-1"); err != nil {
		t.Fatalf("④ 重启后回灌序号基线失败: %v", err)
	}
	if err := gateCommitter.Commit(ctx, stale); !errors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("④a 进程内闸回灌后必须拒绝同序号快照（ErrStaleSeq），实得 %v", err)
	}
	// 进程内闸只是快速过滤；**跨进程的最终判据在存储侧**。不带闸的提交器
	// 也必须被拒——否则「两个进程各有一份内存闸」时恢复点会静默回退。
	if err := checkpoint.NewCommitter(nil, st).Commit(ctx, stale); !errors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("④b 存储侧 CAS 必须拒绝陈旧快照（ErrStaleSeq），实得 %v", err)
	}
}

// alwaysAccept 一个**不做任何判据**的存储：SaveIfNewer 恒成功。
//
// 它存在的唯一用途是把「谁拦住了这次写」这件事孤立出来——用它当底层时，
// 唯一还能拒绝的只有被回灌的进程内 SeqGate。
type alwaysAccept struct{ checkpoint.StoreCAS }

func (alwaysAccept) SaveIfNewer(_ context.Context, _ checkpoint.Snapshot) error { return nil }

// TestE2ERestartWithoutSeedingRepeatsTheEffect 是上面那条的**反面对照**。
//
// 手工跳过「回灌」这一步，其余一字不改：副作用会被施加第二次，终态会被接受
// 第二次。这不是实现 bug，是**恢复纪律被跳过**——而它不会报任何错，只会让
// 已经发生的对外动作再发生一次。
//
// 有这条对照，TestE2EResumeAfterCrashDoesNotReplay 的绿才有含义：它绿是因为
// 回灌真的起作用了，不是因为整条链路本来就拦不住（判据整体失灵时也会绿）。
func TestE2ERestartWithoutSeedingRepeatsTheEffect(t *testing.T) {
	ctx := context.Background()
	st := newDurable()

	runner := sideeffect.NewRunner(st, time.Minute)
	if out, err := apply(ctx, runner, st, "run-2", "collect", "store-main"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("首次应当施加，实得 outcome=%v err=%v", out, err)
	}

	// 崩溃 + 重启，但这一次**不**回灌终态。
	gate := commitgate.NewGuard(isTerminal)
	terminal := commitgate.Commit{
		Ref:    commitgate.Ref{RunID: "run-2", Step: "collect"},
		Status: "succeeded", Branch: "worker-a", Seq: 1,
	}
	if _, err := gate.Acquire(terminal); err != nil {
		t.Fatalf("空闸应当接受首次终态: %v", err)
	}

	// 副作用一侧不受影响：账本在持久化存储里，重试仍是 replayed。
	out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-2", "collect", "store-main")
	if err != nil || out != sideeffect.OutcomeReplayed {
		t.Fatalf("副作用账本在存储里，重试应判 replayed，实得 outcome=%v err=%v", out, err)
	}
	if got := st.appliedCount("collect"); got != 1 {
		t.Fatalf("副作用实际施加次数 = %d, want 1", got)
	}

	// 而终态闸没有回灌 ⇒ 同一个终态被接受第二次——本仓要防的正是这件事。
	if _, err := commitgate.NewGuard(isTerminal).Acquire(terminal); err != nil {
		t.Fatalf("未回灌的闸会放行重复终态（这正是回灌必须存在的理由）: %v", err)
	}
}
