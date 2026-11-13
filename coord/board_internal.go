// board_internal.go —— Board 适配器的内部件（键集构造 / 断言面 / 解码 / 凭据铸造）。
// 从 board.go 拆出：结构阈值（400 行）按判据的一侧切——适配器的判定入口
// 与它们消费的内部件分开，两边各自可读。
package coord

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// ── 内部件 ────────────────────────────────────────────────────────────────

// claimKeySet 认领判定的完整键集：任务自身 + 代次计数器 + 全部依赖键（去重）。
func claimKeySet(id taskboard.TaskID, deps []taskboard.TaskID) []string {
	tk := taskKeyPrefix + string(id)
	keys := []string{tk, fenceKeyPrefix + string(id)}
	seen := map[string]bool{tk: true}
	for _, d := range deps {
		k := taskKeyPrefix + string(d)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// decodeFence 解码代次计数器（键不存在从 0 起；内容损坏报 ErrMalformedRecord
// ——按 0 起会破坏「严格递增」，静默吞掉比报错贵）。
func decodeFence(e Entry) (uint64, error) {
	if !e.Exists {
		return 0, nil
	}
	var v uint64
	if err := json.Unmarshal(e.Val, &v); err != nil {
		return 0, errors.Wrap(ErrMalformedRecord, "代次计数器解码失败", "key", e.Key)
	}
	return v, nil
}

// sameStrings 报告两个键集（不计顺序）是否相同。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]int, len(a))
	for _, s := range a {
		ma[s]++
	}
	for _, s := range b {
		ma[s]--
		if ma[s] < 0 {
			return false
		}
	}
	return true
}

// assertsFrom 把快照里读过的全部键原样列进断言（少列一个就是少护一扇窗）。
func assertsFrom(snap map[string]Entry) []Assert {
	out := make([]Assert, 0, len(snap))
	for k, e := range snap {
		out = append(out, Assert{Key: k, Ver: e.Ver})
	}
	return out
}

// requireClaimed 取「已认领且令牌匹配」的任务；不匹配报 ErrLeaseLost。
func requireClaimed(e Entry, id taskboard.TaskID, token string) (taskboard.Task, error) {
	if !e.Exists {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
	}
	cur, err := decodeTask(e)
	if err != nil {
		return taskboard.Task{}, err
	}
	if cur.State != taskboard.StateClaimed || cur.Lease.Token != token {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrLeaseLost,
			"租约令牌不匹配（租约已易主或被释放过）", "task", string(id))
	}
	return cur, nil
}

// leaseHeld 该任务当前是否仍被持有（零值 Until = 不限期，按在持判——
// 当成已过期会让「两个执行者同时认领」变成「两个赢家」）。
func leaseHeld(t taskboard.Task, now time.Time) bool {
	if t.State != taskboard.StateClaimed {
		return false
	}
	if t.Lease.Until.IsZero() {
		return true
	}
	return t.Lease.Until.After(now)
}

// mintToken 铸一枚认领令牌（crypto/rand，128 位，授予之间不重合）。
func mintToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.Wrap(err, "coord: 铸造认领令牌失败")
	}
	return hex.EncodeToString(b[:]), nil
}

// decodeTask 解码任务记录。
func decodeTask(e Entry) (taskboard.Task, error) {
	var t taskboard.Task
	if !e.Exists {
		return t, nil
	}
	if err := json.Unmarshal(e.Val, &t); err != nil {
		return taskboard.Task{}, errors.Wrap(ErrMalformedRecord,
			"任务记录解码失败（键下内容不是本适配器的编码）", "key", e.Key)
	}
	return t, nil
}

// setTaskMutation 编码整份任务为一次写入。
func setTaskMutation(t taskboard.Task) ([]Mutation, error) {
	enc, err := json.Marshal(t)
	if err != nil {
		return nil, errors.Wrap(err, "coord: 任务编码失败", "task", string(t.ID))
	}
	return []Mutation{{Key: taskKeyPrefix + string(t.ID), Val: enc}}, nil
}
