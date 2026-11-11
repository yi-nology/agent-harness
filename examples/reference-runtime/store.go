// 本文件是参考运行时的**内存存储**：三份存储合起来满足三套存储中立契约——
// checkpoint.StoreCAS（进度快照的单调存取）、sideeffect.Store（副作用闸）
// 与 taskboard.Board（任务认领面）。
// 之所以是三个类型：三套契约里有两个方法同名而不同义（sideeffect.Store.Complete
// 与 taskboard.Board.Complete 签名不同），塞进一个类型必然二选一；分开还顺带把
// 「哪段判据归哪把锁」写清楚了——每套契约各自原子，彼此不共享状态。
//
// 三份存储各自住在自己的文件里（snap_store.go / effect_store.go /
// board_store.go）：单文件塞三套契约会顶破结构阈值，而拆开也顺带把
// 「哪段判据归哪把锁」写得更清楚。
//
// ⚠️ 原子性来自**进程内的互斥锁**，只在单进程内成立；真实宿主必须把同样的判据
// 落到数据库的原子操作上（条件更新 / CAS 循环 / 事务），见 README。
package main

import (
	"context"
	"sync"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// memStore 三份存储的集合。
type memStore struct {
	snaps   *snapStore
	effects *effectStore
	tasks   *boardStore
}

func newMemStore() *memStore {
	return &memStore{
		snaps:   newSnapStore(),
		effects: newEffectStore(),
		tasks:   newBoardStore(),
	}
}

// ── checkpoint.Store / checkpoint.StoreCAS ──────────────────────────────────

// snapStore 进度快照的存取（序号单调）。
type snapStore struct {
	mu    sync.Mutex
	snaps map[string]checkpoint.Snapshot
}

func newSnapStore() *snapStore {
	return &snapStore{snaps: map[string]checkpoint.Snapshot{}}
}

// SaveIfNewer 仅当已持久化的序号**严格小于**本次序号时才写入。
//
// ⚠️ 序号为 0 的快照**一律拒收**（ErrStaleSeq，且不写入）。
//
// 理由：序号 0 不是一次真实提交（见 checkpoint.StoreCAS 的契约），
// 把它当普通写落库 ⇒ 一份「未初始化序号」的快照能盖掉已有的 Seq=10
// ⇒ 恢复点静默回退，正是本契约要防的那件事。此前这里只跳过单调判定
// （`snap.Seq != 0 &&`），于是 Seq=0 反而成了唯一能覆盖既有快照的写。
func (s *snapStore) SaveIfNewer(_ context.Context, snap checkpoint.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap.Seq == 0 {
		return errors.Wrap(checkpoint.ErrStaleSeq,
			"序号为 0 的快照不是一次真实提交，不得落库（更不得覆盖既有快照）",
			"run_id", snap.RunID)
	}

	cur, ok := s.snaps[snap.RunID]
	if ok && cur.Seq >= snap.Seq {
		return errors.Wrap(checkpoint.ErrStaleSeq,
			"快照序号未严格递增", "run_id", snap.RunID, "seq", snap.Seq, "persisted", cur.Seq)
	}
	s.snaps[snap.RunID] = snap.Clone()
	return nil
}

// Save 满足 checkpoint.Store：同一 (RunID, Seq) 幂等。
func (s *snapStore) Save(_ context.Context, snap checkpoint.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.snaps[snap.RunID]
	if ok && cur.Seq == snap.Seq {
		return nil
	}
	if ok && cur.Seq > snap.Seq {
		return errors.Wrap(checkpoint.ErrStaleSeq, "快照序号回退", "run_id", snap.RunID)
	}
	s.snaps[snap.RunID] = snap.Clone()
	return nil
}

// Load 读取快照。无记录时返回 checkpoint.ErrNotFound（**不得**返回零值 + nil）。
func (s *snapStore) Load(_ context.Context, runID string) (checkpoint.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, ok := s.snaps[runID]
	if !ok {
		return checkpoint.Snapshot{}, errors.Wrap(checkpoint.ErrNotFound, "该运行无进度快照", "run_id", runID)
	}
	return snap.Clone(), nil
}

// 编译期断言：三份存储各自满足对应的存储中立契约。
var (
	_ sideeffect.Store    = (*effectStore)(nil)
	_ taskboard.Board     = (*boardStore)(nil)
	_ checkpoint.StoreCAS = (*snapStore)(nil)
)
