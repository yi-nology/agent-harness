// board.go —— taskboard.Board 的 PostgreSQL 参考实现。
//
// # 判据落库形态
//
//   - **认领是读-判-写不可分割**：一个事务里 SELECT … FOR UPDATE 锁任务行，
//     依次判（不存在 / 终态 / 依赖 / 被活持有），通过才写——两个进程同时认领
//     同一任务时，输家会在行锁上等到赢家提交，然后读到 claimed 状态 ⇒
//     ErrTaken。并发正确性由 taskboard.ConcurrentClaim 的共享套件判。
//   - **令牌与接管代次由存储铸造**：令牌用随机 128 位（跨进程不复用不重合，
//     ABA 封死）；代次 = 该任务已见最大值 + 1，与令牌同一次写落库（同源）。
//   - **释放 / 完成 / 续期只认令牌**：条件更新 WHERE token = $2，影响行数为 0
//     即 ErrLeaseLost（迟到持有者放不掉别人的闸）。
//
// 到期时刻由**存储自己的时钟**算（可注入）：调用方的时钟与存储的时钟可能
// 对不上，跨进程时更不能假设一致。
package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// BoardStore taskboard.Board（含 RenewingBoard）的 PostgreSQL 参考实现。
type BoardStore struct {
	pool *pgxpool.Pool

	mu  sync.Mutex
	now func() time.Time
}

// NewBoardStore 构造任务认领面存储。
func NewBoardStore(pool *pgxpool.Pool) *BoardStore {
	// 默认（未注入）不读进程本地时钟：nowIn 走数据库 clock_timestamp——
	// 认领与续期的租约判定是跨节点共享的事实，时间源必须是数据库
	//（与 EffectStore 同口径，评审第三轮 P0）。
	return &BoardStore{pool: pool, now: nil}
}

// UseClock 注入时钟（boardtest 契约套件用）。
func (b *BoardStore) UseClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now != nil {
		b.now = now
	}
}

// nowIn 事务内的当前时刻：注入（boardtest 套件）→ 注入值；否则读数据库
// clock_timestamp。必须在**取得行锁之后**调用（与 EffectStore.nowIn 同款
// 理由：等锁时长不得计入租约寿命）。
func (b *BoardStore) nowIn(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	b.mu.Lock()
	injected := b.now
	b.mu.Unlock()
	if injected != nil {
		return injected(), nil
	}
	var t time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t); err != nil {
		return time.Time{}, errors.Wrap(err, "pgstore: 读数据库时间失败")
	}
	return t, nil
}

// Seed 摆初始任务（契约里没有「创建」入口：任务拓扑是宿主的资产）。
//
// 签名与套件要求的形态一致（boardtest 经 interface{ Seed(tasks ...Task) }
// 取用，没有 error 返回位）。落库失败时**panic**：这是装载入口，静默失败
// 会让套件在一张空表上跑出误导性的结论——摆不进去就当场响。
//
// 幂等：同 id 重摆覆盖内容并清租约（套件每个子测试从全新 schema 起步，
// 生产宿主通常只在装载拓扑时调用它）。
func (b *BoardStore) Seed(tasks ...taskboard.Task) {
	ctx := context.Background()
	err := runTx(ctx, b.pool, func(tx pgx.Tx) error {
		for _, t := range tasks {
			if t.ID == "" {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO pgref_board_task (id, title, deps, state, seq)
				 VALUES ($1, $2, $3, $4, 1)
				 ON CONFLICT (id) DO UPDATE SET
				     title = EXCLUDED.title, deps = EXCLUDED.deps, state = EXCLUDED.state,
				     owner = NULL, token = NULL, until = NULL, seq = pgref_board_task.seq + 1`,
				string(t.ID), t.Title, depIDs(t.Deps), string(t.State)); err != nil {
				return errors.Wrap(err, "pgstore: 摆任务失败", "task", string(t.ID))
			}
		}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("pgstore: Seed 摆任务失败（装载入口不容静默失败）: %v", err))
	}
}

// depIDs 把依赖 id 转成 TEXT[]（pgx 直接收 []string）。
func depIDs(deps []taskboard.TaskID) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, string(d))
	}
	return out
}

// boardRow 任务行（列语义见 schema.go）。
type boardRow struct {
	id    string
	title string
	deps  []string
	state string
	owner *string
	token *string
	fence int64
	until *time.Time
	seq   int64
}

// held 该任务当前是否仍被活持有（零值到期 = 不限期，当成已过期会让
// 「两个执行者同时认领」变成「两个赢家」）。
func (r *boardRow) held(now time.Time) bool {
	return r.state == string(taskboard.StateClaimed) &&
		(r.until == nil || r.until.After(now))
}

const boardCols = `id, title, deps, state, owner, token, fence, until, seq`

func scanBoardRow(row pgx.Row) (*boardRow, error) {
	var r boardRow
	if err := row.Scan(&r.id, &r.title, &r.deps, &r.state, &r.owner, &r.token, &r.fence, &r.until, &r.seq); err != nil {
		return nil, err
	}
	return &r, nil
}

// List 返回一致快照（单个事务内读完全部行）。
func (b *BoardStore) List(ctx context.Context) ([]taskboard.Task, error) {
	rows, err := b.pool.Query(ctx, `SELECT `+boardCols+` FROM pgref_board_task`)
	if err != nil {
		return nil, errors.Wrap(err, "pgstore: 读任务快照失败")
	}
	defer rows.Close()
	var out []taskboard.Task
	for rows.Next() {
		r, err := scanBoardRow(rows)
		if err != nil {
			return nil, errors.Wrap(err, "pgstore: 解析任务行失败")
		}
		out = append(out, r.task())
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "pgstore: 读任务快照失败")
	}
	// 一致快照按 id 排序输出（判据会排序，这里先排好便于人读）。
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// task 行 → 契约值类型（拷贝语义：调用方改它不影响库）。
func (r *boardRow) task() taskboard.Task {
	t := taskboard.Task{
		ID:    taskboard.TaskID(r.id),
		Title: r.title,
		State: taskboard.State(r.state),
		Seq:   uint64(r.seq),
	}
	for _, d := range r.deps {
		t.Deps = append(t.Deps, taskboard.TaskID(d))
	}
	if r.owner != nil {
		t.Owner = taskboard.AgentID(*r.owner)
	}
	if r.token != nil {
		t.Lease.Token = *r.token
		t.Lease.Fence = taskboard.Fence(r.fence)
	}
	if r.until != nil {
		t.Lease.Until = *r.until
	}
	return t
}

// Claim 认领一个任务（读-判-写不可分割：一个事务完成）。
func (b *BoardStore) Claim(ctx context.Context, id taskboard.TaskID, agent taskboard.AgentID, req taskboard.ClaimRequest) (taskboard.Task, error) {
	return withinTx(ctx, b.pool, func(tx pgx.Tx) (taskboard.Task, error) {
		r, err := lockTask(ctx, tx, id)
		if err != nil {
			return taskboard.Task{}, err
		}
		// 判定时刻：行锁之后取（见 nowIn 的调用位置要求）。
		now, err := b.nowIn(ctx, tx)
		if err != nil {
			return taskboard.Task{}, err
		}
		if r == nil {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
		}
		if r.state == string(taskboard.StateDone) {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrTerminal, "任务已是终态", "task", string(id))
		}
		if err := depsDone(ctx, tx, r.deps); err != nil {
			return taskboard.Task{}, err
		}
		if r.held(now) {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, "任务已被他人认领", "task", string(id))
		}

		// 铸一枚新令牌 + 接管代次，与状态写**同一次落库**（同源）：
		// 令牌跨进程不复用（随机），代次在同一任务上严格递增。
		token, err := mintToken()
		if err != nil {
			return taskboard.Task{}, err
		}
		fence := r.fence + 1
		var until *time.Time
		if req.TTL > 0 {
			u := now.Add(req.TTL)
			until = &u
		}
		r.state = string(taskboard.StateClaimed)
		r.owner = ptr(string(agent))
		r.token = ptr(token)
		r.fence = fence
		r.until = until
		r.seq++
		if _, err := tx.Exec(ctx,
			`UPDATE pgref_board_task SET state = $2, owner = $3, token = $4, fence = $5, until = $6, seq = $7 WHERE id = $1`,
			string(id), r.state, r.owner, r.token, r.fence, r.until, r.seq); err != nil {
			return taskboard.Task{}, errors.Wrap(err, "pgstore: 认领写回失败", "task", string(id))
		}
		return r.task(), nil
	})
}

// lockTask 锁住并读取任务行；不存在返回 (nil, nil)。
func lockTask(ctx context.Context, tx pgx.Tx, id taskboard.TaskID) (*boardRow, error) {
	r, err := scanBoardRow(tx.QueryRow(ctx,
		`SELECT `+boardCols+` FROM pgref_board_task WHERE id = $1 FOR UPDATE`, string(id)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "pgstore: 读任务行失败", "task", string(id))
	}
	return r, nil
}

// depsDone 判依赖是否全部完成（不存在的依赖视为未完成）。
func depsDone(ctx context.Context, tx pgx.Tx, deps []string) error {
	for _, d := range deps {
		var state string
		err := tx.QueryRow(ctx, `SELECT state FROM pgref_board_task WHERE id = $1`, d).Scan(&state)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.Wrap(taskboard.ErrBlocked, "依赖未完成", "dep", d)
			}
			return errors.Wrap(err, "pgstore: 读依赖失败", "dep", d)
		}
		if taskboard.State(state) != taskboard.StateDone {
			return errors.Wrap(taskboard.ErrBlocked, "依赖未完成", "dep", d)
		}
	}
	return nil
}

// mintToken 铸一枚随机令牌（跨进程不复用不重合 ⇒ ABA 封死）。
func mintToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", errors.Wrap(err, "pgstore: 铸造令牌失败")
	}
	return "tok-" + hex.EncodeToString(buf[:]), nil
}

func ptr[T any](v T) *T { return &v }

// Release 归还认领（凭令牌）：非持有者释放一律被拒，不许静默成功。
func (b *BoardStore) Release(ctx context.Context, id taskboard.TaskID, token string) error {
	_, err := withinTx(ctx, b.pool, func(tx pgx.Tx) (bool, error) {
		if err := releaseOrComplete(ctx, tx, id, token, string(taskboard.StateOpen)); err != nil {
			return false, err
		}
		return false, nil
	})
	return err
}

// Complete 标记完成（凭令牌）：终态不带 owner，避免「谁做完的」与「谁还占着」混在一起。
func (b *BoardStore) Complete(ctx context.Context, id taskboard.TaskID, token string) error {
	_, err := withinTx(ctx, b.pool, func(tx pgx.Tx) (bool, error) {
		if err := releaseOrComplete(ctx, tx, id, token, string(taskboard.StateDone)); err != nil {
			return false, err
		}
		return false, nil
	})
	return err
}

// releaseOrComplete 释放与完成共用的条件更新：只认令牌，不认内容。
func releaseOrComplete(ctx context.Context, tx pgx.Tx, id taskboard.TaskID, token, state string) error {
	tag, err := tx.Exec(ctx,
		`UPDATE pgref_board_task
		    SET state = $3, owner = NULL, token = NULL, until = NULL, seq = seq + 1
		  WHERE id = $1 AND token = $2 AND state = $4`,
		string(id), token, state, string(taskboard.StateClaimed))
	if err != nil {
		return errors.Wrap(err, "pgstore: 归还/完成写回失败", "task", string(id))
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// 没命中：区分「任务不存在」与「令牌不匹配」——两者的处置不同。
	r, err := scanBoardRow(tx.QueryRow(ctx,
		`SELECT `+boardCols+` FROM pgref_board_task WHERE id = $1`, string(id)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
		}
		return errors.Wrap(err, "pgstore: 读任务行失败", "task", string(id))
	}
	return errors.Wrap(taskboard.ErrLeaseLost, "租约令牌不匹配", "task", string(id), "state", r.state)
}

// Renew 续期并确认持有资格（taskboard.RenewingBoard）。
//
// 校验（令牌是当前租约的 + 租约未过期）与延长在**同一个事务**里——原子；
// TTL 语义与 Claim 一致；续期不换令牌、不递增代次（续期不是接管）。
func (b *BoardStore) Renew(ctx context.Context, id taskboard.TaskID, token string, req taskboard.ClaimRequest) (taskboard.Task, error) {
	return withinTx(ctx, b.pool, func(tx pgx.Tx) (taskboard.Task, error) {
		r, err := lockTask(ctx, tx, id)
		if err != nil {
			return taskboard.Task{}, err
		}
		// 判定时刻：行锁之后取（见 nowIn 的调用位置要求）。
		now, err := b.nowIn(ctx, tx)
		if err != nil {
			return taskboard.Task{}, err
		}
		if r == nil {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
		}
		if r.state != string(taskboard.StateClaimed) || r.token == nil || *r.token != token {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrLeaseLost, "租约令牌不匹配", "task", string(id))
		}
		if !r.held(now) {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrLeaseLost, "租约已过期，资格终止（请重新认领）", "task", string(id))
		}
		if req.TTL > 0 {
			u := now.Add(req.TTL)
			r.until = &u
		} else {
			r.until = nil
		}
		r.seq++
		if _, err := tx.Exec(ctx,
			`UPDATE pgref_board_task SET until = $2, seq = $3 WHERE id = $1`,
			string(id), r.until, r.seq); err != nil {
			return taskboard.Task{}, errors.Wrap(err, "pgstore: 续期写回失败", "task", string(id))
		}
		return r.task(), nil
	})
}
