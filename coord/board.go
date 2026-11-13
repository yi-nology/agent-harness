// board.go —— taskboard.Board 的**通用适配器**：判定逻辑落在 Backend 原语之上。
//
// # 判定逻辑只有这一份
//
// Board 的语义（认领原子、凭令牌释放/完成、续期是资格检查、接管代次严格
// 递增）全部在本适配器里实现；宿主的后端只负责让三个原语真的原子。
// 判定与提交分离的窗口由断言护住：判定读过的每个键都进断言，
// 窗口内被人动过就取新快照重判——这就是「CAS 循环」形态在 Board 上的落法。
//
// # 令牌由适配器在提交路径上铸造
//
// token 每次认领尝试用 crypto/rand 现铸：断言保证只有铸这枚令牌的那次
// 事务能提交，输家的令牌从不落库——「由存储铸造」的本意（凭据出自判定面，
// 不由调用方自带）由此成立。到期时刻由适配器的时钟算（时钟可注入，
// 契约套件用它做过期场景），不是调用方的时钟。
//
// # 依赖键的动态展开
//
// 认领判定要读依赖任务的状态，而依赖清单在任务记录里——首轮快照只有
// 任务键。这里不假装键集是静态的：首轮解码出依赖后重取一次快照
// （把依赖键一并纳入断言面）；认领期间拓扑被改（断言失败）时按新清单
// 再展开。拓扑变更在运行期是病态事件，多花一次快照读比漏护一扇窗便宜。
package coord

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// taskKeyPrefix 任务键前缀（Board 适配器的键空间与其余适配器隔离）。
const taskKeyPrefix = "coord/task/"

// fenceKeyPrefix 接管代次计数器前缀。
//
// # 为什么代次不住在任务记录里
//
// taskboard.Check 的快照不变量（CodeLeaseNoClaim）要求：非认领态的任务
// **不得残留代次**——释放清掉租约时把代次一并清掉。但契约同时要求
// 代次「同一任务上严格递增」（释放后再认领同样算换代）。两个要求合起来，
// 代次的真相只能放在任务记录**之外**：一把随任务生灭的单调计数器，
// 每次认领成功在同一事务里 +1。任务记录里的 Lease.Fence 是它的投影
// （写入当刻的值），不是真相本身——「派生状态只能有一个真相」的同一条纪律。
const fenceKeyPrefix = "coord/taskfence/"

// BoardAdapter 把任意 Backend 适配成 taskboard.Board（含 RenewingBoard）。
type BoardAdapter struct {
	b Backend

	mu  sync.Mutex
	now func() time.Time
}

// NewBoardAdapter 构造 Board 适配器。
func NewBoardAdapter(b Backend) *BoardAdapter {
	return &BoardAdapter{b: b, now: time.Now}
}

// UseClock 注入时钟（boardtest 契约套件要求：过期判定必须读可注入的时钟）。
func (a *BoardAdapter) UseClock(now func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now != nil {
		a.now = now
	}
}

// clock 当前时刻（持锁调用）。
func (a *BoardAdapter) clock() time.Time { return a.now() }

// Seed 摆初始任务（boardtest 契约套件的接缝：契约本身没有「创建」入口，
// 任务拓扑是宿主资产）。
//
// 套件的接缝签名没有 error（与 reference-runtime 的内存板同口径）：任务体是
// 纯数据、编码不会失败；摆入的后端故障只能静默——这个方法不是生产 API，
// 生产路径的任务落库在宿主自己的装配代码里。
func (a *BoardAdapter) Seed(tasks ...taskboard.Task) {
	ctx := context.Background()
	muts := make([]Mutation, 0, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		t.Seq++ // 摆入也是一次变更，Seq 前进
		enc, err := json.Marshal(t)
		if err != nil {
			panic("coord: Seed 编码任务失败（任务体是纯数据，不应发生）: " + string(t.ID))
		}
		muts = append(muts, Mutation{Key: taskKeyPrefix + string(t.ID), Val: enc})
	}
	if _, err := a.b.Txn(ctx, nil, muts); err != nil {
		panic("coord: Seed 提交失败（套件接缝无 error 通道）: " + err.Error())
	}
}

// List 返回一致快照（按键名取全量后逐键解码，按键 ID 序返回）。
func (a *BoardAdapter) List(ctx context.Context) ([]taskboard.Task, error) {
	keys, err := a.b.Keys(ctx, taskKeyPrefix)
	if err != nil {
		return nil, errors.Wrap(err, "coord: List 列键失败")
	}
	if len(keys) == 0 {
		return nil, nil
	}
	snap, err := a.b.Snapshot(ctx, keys...)
	if err != nil {
		return nil, errors.Wrap(err, "coord: List 快照读失败")
	}
	out := make([]taskboard.Task, 0, len(keys))
	for _, k := range keys {
		t, derr := decodeTask(snap[k])
		if derr != nil {
			return nil, derr
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(x, y taskboard.Task) int { return strings.Compare(string(x.ID), string(y.ID)) })
	return out, nil
}

// Claim 认领一个任务（读-判-写窗口由断言护住，冲突即重判）。
//
// 代次与认领在同一事务里推进（fenceKey +1 + 任务落库，同一次原子操作铸造）。
func (a *BoardAdapter) Claim(ctx context.Context, id taskboard.TaskID, agent taskboard.AgentID, req taskboard.ClaimRequest) (taskboard.Task, error) {
	tk, fk := taskKeyPrefix+string(id), fenceKeyPrefix+string(id)
	keys := []string{tk, fk}
	for range casMaxAttempts {
		snap, err := a.b.Snapshot(ctx, keys...)
		if err != nil {
			return taskboard.Task{}, errors.Wrap(err, "coord: Claim 快照读失败")
		}
		cur, derr := decodeTask(snap[tk])
		if derr != nil {
			return taskboard.Task{}, derr
		}
		if !snap[tk].Exists {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
		}
		if cur.State.Terminal() {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrTerminal, "任务已是终态", "task", string(id))
		}
		// 依赖键动态展开：清单与当前快照键集不一致 ⇒ 重取快照（带依赖键）。
		need := claimKeySet(id, cur.Deps)
		if !sameStrings(need, keys) {
			keys = need
			continue
		}
		// 依赖判定读的是同一份快照：依赖键都在断言里——判定与提交之间
		// 有人改了任何一侧，这次认领因断言失败而重判。
		for _, dep := range cur.Deps {
			dk := taskKeyPrefix + string(dep)
			if !snap[dk].Exists {
				return taskboard.Task{}, errors.Wrap(taskboard.ErrBlocked,
					"依赖未完成（依赖不存在）", "task", string(id), "dep", string(dep))
			}
			d, dderr := decodeTask(snap[dk])
			if dderr != nil {
				return taskboard.Task{}, dderr
			}
			if !d.State.Terminal() {
				return taskboard.Task{}, errors.Wrap(taskboard.ErrBlocked,
					"依赖未完成", "task", string(id), "dep", string(dep))
			}
		}
		if leaseHeld(cur, a.clock()) {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, "任务已被他人认领", "task", string(id))
		}
		token, terr := mintToken()
		if terr != nil {
			return taskboard.Task{}, terr
		}
		// 代次：计数器 +1（计数器是真相，见 fenceKeyPrefix 的注释）。
		curFence, ferr := decodeFence(snap[fk])
		if ferr != nil {
			return taskboard.Task{}, ferr
		}
		fence := curFence + 1
		next := cur
		next.State = taskboard.StateClaimed
		next.Owner = agent
		next.Lease = taskboard.Lease{Token: token, Fence: taskboard.Fence(fence)}
		if req.TTL > 0 {
			next.Lease.Until = a.clock().Add(req.TTL)
		}
		next.Seq++
		enc, merr := json.Marshal(next)
		if merr != nil {
			return taskboard.Task{}, errors.Wrap(merr, "coord: Claim 编码任务失败", "task", string(id))
		}
		fenc, ferr := json.Marshal(fence)
		if ferr != nil {
			return taskboard.Task{}, errors.Wrap(ferr, "coord: Claim 编码代次失败", "task", string(id))
		}
		ok, terr := a.b.Txn(ctx, assertsFrom(snap), []Mutation{
			{Key: tk, Val: enc},
			{Key: fk, Val: fenc},
		})
		if terr != nil {
			return taskboard.Task{}, errors.Wrap(terr, "coord: Claim 提交失败")
		}
		if ok {
			return next, nil
		}
		// 断言失败：状态在快照与提交之间被人改了——取新快照重判。
	}
	return taskboard.Task{}, errors.Wrap(ErrTxnConflict,
		"coord: 认领重试上限内未提交（高争用或后端原子性可疑）")
}

// Release 归还认领（凭令牌；非持有者一律 ErrLeaseLost，不静默成功）。
func (a *BoardAdapter) Release(ctx context.Context, id taskboard.TaskID, token string) error {
	tk := taskKeyPrefix + string(id)
	_, err := casLoop(ctx, a.b, []string{tk},
		func(snap map[string]Entry) (taskboard.Task, []Assert, []Mutation, error) {
			cur, err := requireClaimed(snap[tk], id, token)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			cur.State = taskboard.StateOpen
			cur.Owner = ""
			cur.Lease = taskboard.Lease{}
			cur.Seq++
			muts, err := setTaskMutation(cur)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			return cur, assertsFrom(snap), muts, nil
		})
	return err
}

// Complete 标记完成（凭令牌；终态不带 owner，避免「谁做完的」与
// 「谁还占着」混在一起）。
func (a *BoardAdapter) Complete(ctx context.Context, id taskboard.TaskID, token string) error {
	tk := taskKeyPrefix + string(id)
	_, err := casLoop(ctx, a.b, []string{tk},
		func(snap map[string]Entry) (taskboard.Task, []Assert, []Mutation, error) {
			cur, err := requireClaimed(snap[tk], id, token)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			cur.State = taskboard.StateDone
			cur.Owner = ""
			cur.Lease = taskboard.Lease{}
			cur.Seq++
			muts, err := setTaskMutation(cur)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			return cur, assertsFrom(snap), muts, nil
		})
	return err
}

// Renew 续期并确认持有资格（taskboard.RenewingBoard）：令牌不匹配或已过期
// 都报 ErrLeaseLost 且不做任何修改——续期不是心跳，是资格检查。
func (a *BoardAdapter) Renew(ctx context.Context, id taskboard.TaskID, token string, req taskboard.ClaimRequest) (taskboard.Task, error) {
	tk := taskKeyPrefix + string(id)
	return casLoop(ctx, a.b, []string{tk},
		func(snap map[string]Entry) (taskboard.Task, []Assert, []Mutation, error) {
			cur, err := requireClaimed(snap[tk], id, token)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			if !leaseHeld(cur, a.clock()) {
				return taskboard.Task{}, nil, nil, errors.Wrap(taskboard.ErrLeaseLost,
					"租约已过期，资格终止（请重新认领）", "task", string(id))
			}
			if req.TTL > 0 {
				cur.Lease.Until = a.clock().Add(req.TTL)
			} else {
				cur.Lease.Until = time.Time{} // TTL=0 = 不限期（与 Claim 同一语义）
			}
			cur.Seq++
			muts, err := setTaskMutation(cur)
			if err != nil {
				return taskboard.Task{}, nil, nil, err
			}
			return cur, assertsFrom(snap), muts, nil
		})
}
