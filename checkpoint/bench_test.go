// bench_test.go —— Checkpoint 的性能基线（issue #101）。
//
// Commit 是「每完成一个步骤落一次」的热路径（Snapshot 粒度约定）：闸判定 +
// 存储往返。基线量两段：SeqGate.Accept（进程内闸，纯内存）与 Committer.Commit
// （闸 + 最小内存存储的完整提交链）。存储用进程内最小实现——量的是**本包
// 判据链**的成本，不是某个真实存储的 IO（那是宿主实现的事）。
//
// # 怎么读这些数
//
// 基线不是门槛（口径见 docs/BENCH-BASELINE-20261113.md）。数据规模：
// Snapshot.Done 装入 100 个步骤 ID（Clone 深拷贝随规模线性，这是基线要
// 暴露的真实成本面），RunID 固定、Seq 逐次递增。
package checkpoint_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/yi-nology/goagent-kernel/checkpoint"
)

// benchStore checkpoint.StoreCAS 的最小内存实现（SaveIfNewer 严格递增语义）。
type benchStore struct {
	mu    sync.Mutex
	snaps map[string]checkpoint.Snapshot
}

func (s *benchStore) SaveIfNewer(_ context.Context, snap checkpoint.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.snaps[snap.RunID]; ok && cur.Seq >= snap.Seq {
		return checkpoint.ErrStaleSeq
	}
	s.snaps[snap.RunID] = snap.Clone()
	return nil
}

func (s *benchStore) Load(_ context.Context, runID string) (checkpoint.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snaps[runID]
	if !ok {
		return checkpoint.Snapshot{}, checkpoint.ErrNotFound
	}
	return snap, nil
}

// benchSnapshot 100 步的快照（Clone 成本随 Done 规模线性，规模写在头注）。
func benchSnapshot(runID string, seq uint64) checkpoint.Snapshot {
	done := make([]string, 100)
	for i := range done {
		done[i] = "step-" + strconv.Itoa(i)
	}
	return checkpoint.Snapshot{RunID: runID, Seq: seq, Status: "running", Done: done}
}

func BenchmarkCheckpointSeqGateAccept(b *testing.B) {
	gate := checkpoint.NewSeqGate()
	snap := benchSnapshot("run-bench", 0)
	for b.Loop() {
		snap.Seq++
		if err := gate.Accept(snap); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckpointCommit(b *testing.B) {
	ctx := context.Background()
	c := checkpoint.NewCommitter(checkpoint.NewSeqGate(), &benchStore{snaps: map[string]checkpoint.Snapshot{}})
	snap := benchSnapshot("run-bench", 0)
	for b.Loop() {
		snap.Seq++
		if err := c.Commit(ctx, snap); err != nil {
			b.Fatal(err)
		}
	}
}
