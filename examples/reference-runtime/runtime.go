package main

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/commitgate"
	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// stepDef 一个步骤的完整定义：图身份 + 依赖声明 + 它要施加的动作。
type stepDef struct {
	id        string
	label     string
	dependsOn []string
	action    sideeffect.Action
	apply     func(ctx context.Context, lease sideeffect.Lease) (sideeffect.Result, error)
}

// runtime 编排骨架：建任务 → 按计划执行步骤 → 落快照 → 提交终态。
//
// 它只做编排，不认识任何领域概念：步骤是什么由宿主决定，内核只认
// 「步骤 id + 依赖声明 + 幂等键 + 资源」。
type runtime struct {
	runID string
	steps []*stepDef
	byID  map[string]*stepDef
	graph []checkpoint.StepDef

	snaps     *snapStore
	gate      *checkpoint.SeqGate
	committer *checkpoint.Committer
	guard     *commitgate.Guard
	runner    *sideeffect.Runner

	branch string
	seq    uint64
}

// runReport 一次执行的结果记录。
type runReport struct {
	Skipped     []string
	Executed    []string
	Replayed    []string
	Interrupted bool
	Status      string
	Seq         uint64
}

// newRuntime 组装运行时。ttl 是每次抢副作用闸时申请的租约有效期。
func newRuntime(runID string, store *memStore, steps []*stepDef, ttl time.Duration) *runtime {
	rt := &runtime{
		runID:  runID,
		steps:  steps,
		byID:   map[string]*stepDef{},
		snaps:  store.snaps,
		gate:   checkpoint.NewSeqGate(),
		guard:  commitgate.NewGuard(isTerminalStatus),
		runner: sideeffect.NewRunner(store.effects, ttl),
		branch: "worker-1",
	}
	rt.committer = checkpoint.NewCommitter(rt.gate, store.snaps)
	for _, s := range steps {
		rt.graph = append(rt.graph, checkpoint.StepDef{
			ID:        s.id,
			Label:     s.label,
			DependsOn: s.dependsOn,
		})
		rt.byID[s.id] = s
	}
	return rt
}

// run 执行（或续跑）这条链。
//
// stopAfter > 0 表示「执行到第 N 步就停下」——示例用它模拟进程在中途中断：
// 已完成的步骤已落库、未开始的步骤没有痕迹，恢复点因此是**可信**的。
func (r *runtime) run(ctx context.Context, stopAfter int) (*runReport, error) {
	snap, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	plan := checkpoint.BuildPlan(r.graph, snap)
	if plan.Blocked() {
		return nil, errors.New("恢复计划被阻断：快照已标记终结，但编排图里仍有未完成步骤")
	}

	rep := &runReport{Skipped: plan.Skip}
	done := []string{}
	if snap != nil {
		done = append(done, snap.Done...)
		r.seq = snap.Seq
	}

	for _, id := range plan.Run {
		if stopAfter > 0 && len(rep.Executed) >= stopAfter {
			rep.Interrupted = true
			break
		}
		st := r.byID[id]
		outcome, _, err := r.runner.Execute(ctx, st.action, st.apply)
		if err != nil {
			// 判据与处置之间不能留缝：结果未知时调用方必须自己判一次，
			// 否则「闸拒了但调用方照样执行」就是重复施加的入口。
			if derr := decideRetry(outcome); derr != nil {
				return rep, errors.Wrap(derr, "步骤被拒：结果未知，必须先对账裁决", "step", id)
			}
			return rep, errors.Wrap(err, "步骤执行失败", "step", id)
		}
		if outcome == sideeffect.OutcomeReplayed {
			rep.Replayed = append(rep.Replayed, id)
		} else {
			rep.Executed = append(rep.Executed, id)
		}

		done = append(done, id)
		if err := r.commitSnapshot(ctx, done, contract.StatusRunning); err != nil {
			return rep, err
		}
		// 步骤终态只受理一次：重复提交会被终态闸挡下（ErrAlreadyCommitted）。
		if _, err := r.guard.Acquire(commitgate.Commit{
			Ref:    commitgate.Ref{RunID: r.runID, Step: id},
			Status: contract.StatusSucceeded,
			Branch: r.branch,
			Seq:    r.seq,
		}); err != nil {
			return rep, errors.Wrap(err, "步骤终态提交被拒", "step", id)
		}
	}

	if !rep.Interrupted {
		if _, err := r.guard.Acquire(commitgate.Commit{
			Ref:    commitgate.Ref{RunID: r.runID, Step: runScope},
			Status: contract.StatusSucceeded,
			Branch: r.branch,
			Seq:    r.seq,
		}); err != nil {
			return rep, errors.Wrap(err, "运行终态提交被拒")
		}
		rep.Status = contract.StatusSucceeded
		if err := r.commitSnapshot(ctx, done, contract.StatusSucceeded); err != nil {
			return rep, err
		}
	}
	rep.Seq = r.seq
	return rep, nil
}

// runScope 运行级终态在终态闸里的引用（与步骤引用区分开）。
const runScope = "run"

// load 读取恢复点，并把**已持久化的序号**回灌进进程内的单调闸。
//
// 回灌不能省：闸是进程内存态，重启后是空的——不回灌，上一进程写下的陈旧快照
// 会畅通无余地写进去，恢复点静默回退。
func (r *runtime) load(ctx context.Context) (*checkpoint.Snapshot, error) {
	if err := r.committer.SeedFromStore(ctx, r.runID); err != nil {
		return nil, errors.Wrap(err, "回灌序号基线失败", "run_id", r.runID)
	}
	snap, err := r.snaps.Load(ctx, r.runID)
	if err != nil {
		if errors.Is(err, checkpoint.ErrNotFound) {
			return nil, nil // 首次执行：没有记录
		}
		return nil, errors.Wrap(err, "读取进度快照失败", "run_id", r.runID)
	}
	return &snap, nil
}

// commitSnapshot 落一次进度快照（序号严格递增）。
func (r *runtime) commitSnapshot(ctx context.Context, done []string, status string) error {
	r.seq++
	snap := checkpoint.Snapshot{
		RunID:  r.runID,
		Seq:    r.seq,
		Status: status,
		Done:   append([]string(nil), done...),
	}
	if err := r.committer.Commit(ctx, snap); err != nil {
		return errors.Wrap(err, "进度快照落库失败", "run_id", r.runID, "seq", r.seq)
	}
	return nil
}

// decideRetry 判定「结果未知时该不该放行执行」。
//
// 返回 nil = 放行；返回 error = 拒绝（必须先对账裁决）。
func decideRetry(outcome sideeffect.Outcome) error {
	if outcome == sideeffect.OutcomeUncertain {
		return errors.Wrap(sideeffect.ErrOutcomeUnknown, "该键结果未知，必须先对账裁决")
	}
	return nil
}

// isTerminalStatus 某个状态是否已是终态（注入终态闸的词表）。
func isTerminalStatus(status string) bool {
	switch status {
	case contract.StatusSucceeded, contract.StatusFailed,
		contract.StatusCancelled, contract.StatusTimedOut:
		return true
	default:
		return false
	}
}

// callCounter 统计各步骤的执行体被调用了多少次——续跑不重放的证据。
type callCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func newCallCounter() *callCounter {
	return &callCounter{n: map[string]int{}}
}

func (c *callCounter) inc(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[id]++
}

func (c *callCounter) get(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[id]
}

func (c *callCounter) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.n))
	for k, v := range c.n {
		out[k] = v
	}
	return out
}
