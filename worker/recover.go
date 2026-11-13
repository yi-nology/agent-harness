// recover.go —— 任务恢复：接管前任留下的过期认领。
//
// # 判资格不判时钟
//
// 「这个租约过期了吗」由**任务板自己的时钟与资格判定**回答（Board.Claim
// 对过期租约授予接管、对活租约报 ErrTaken——issue #87 之后的既有语义）。
// Recover 不自建第二套时钟判据：它对每个认领态的任务直接尝试 Claim，
// 存储侧的结论就是结论。这与「判据归存储，不归进程」同一条纪律——
// 恢复方拿本地时钟比对 Until，跨进程时钟对不上时会把活任务抢过来，
// 两个执行者做同一件事。
package worker

import (
	"context"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// Recover 接管任务板上「认领已过期」的任务（崩溃恢复入口）。
//
// 语义：
//
//   - 认领态且租约已过期 → Claim 成功（接管，新令牌新代次），计入返回；
//   - 认领态且租约仍活 → @@ErrTaken@@（前任还活着），跳过——**不抢活**；
//   - 其余状态（open/failed/done）→ 不属于「恢复」的范畴，跳过
//     （open 任务本来就能被正常领取循环拿走）；
//   - 竞争性失败（ErrTaken/ErrBlocked/ErrTerminal/ErrNotFound）都是
//     「这次没轮上」的正常结论，不构成 error；非竞争类错误（任务板
//     不可用等）原样上抛。
//
// 返回本次成功接管的任务 id（调用方据此留痕：哪些任务换了主人）。
// agent 是接管者标识；ttl 是接管后新租约的有效期（>0）。
func Recover(ctx context.Context, board taskboard.Board, agent taskboard.AgentID, ttl time.Duration) ([]taskboard.TaskID, error) {
	if board == nil {
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 恢复缺任务板")
	}
	if agent == "" {
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 恢复缺接管者标识")
	}
	if ttl <= 0 {
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 恢复的租约 TTL 必须为正")
	}
	tasks, err := board.List(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "worker: 恢复列任务板失败")
	}
	var took []taskboard.TaskID
	for _, t := range tasks {
		if t.State != taskboard.StateClaimed {
			continue
		}
		if _, err := board.Claim(ctx, t.ID, agent, taskboard.ClaimRequest{TTL: ttl}); err != nil {
			if errors.Is(err, taskboard.ErrTaken) || errors.Is(err, taskboard.ErrTerminal) {
				continue // 活租约不抢；终态不碰
			}
			if errors.Is(err, taskboard.ErrBlocked) || errors.Is(err, taskboard.ErrNotFound) {
				continue // 恢复不了也轮不到本函数裁决，留给正常领取环
			}
			return nil, errors.Wrap(err, "worker: 恢复认领失败", "task", string(t.ID))
		}
		took = append(took, t.ID)
	}
	return took, nil
}
