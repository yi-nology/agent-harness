// effect.go —— sideeffect.Store 的 PostgreSQL 参考实现：类型、事务骨架与行读写。
//
// # 三条判据在库里的落法（与内存版 effectStore 逐条对应）
//
//  1. **三项联查**：Complete / Release / Abandon / Renew / MarkUncertain 都在
//     同一个事务里校验「令牌仍是最新代 + 持有者身份一致 + 租约与动作的资源
//     一致」。校验与写入在同一事务 ⇒ 任何一步失败都发生在**任何状态被改之前**
//     ——这正是 MarkUncertain「标记写失败必须保住执行意图」的落库形态。
//  2. **执行意图**：Acquire 的授予（令牌 + 持有者身份）与意图登记写在
//     同一事务里；意图过期未裁决 ⇒ 同一 Key 转入未知态而非重新授予。
//  3. **对账占用**：BeginReconcile 与裁决分表字段，占用过期可重入。
//
// # 原子性来自哪里
//
// 每个方法 = 一个事务 = 若干条 `SELECT … FOR UPDATE` + 条件写。行锁把
// 「读状态 → 判 → 写」钉成跨进程原子：第二个进程在第一个提交前读到的
// 永远是旧状态，且会在行锁上等待。这与「一条带条件的 UPDATE」等价，
// 是契约点名认可的两种形态之一。
//
// 锁序恒为「资源行 → 动作行」：所有方法按同一顺序加锁，两个并发事务
// 不可能交叉持锁（交叉死锁在锁序一致时不存在）。
package pgstore

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// EffectStore sideeffect.Store 的 PostgreSQL 参考实现。
//
// 一个实例绑定一个 schema（全部表在其中）；并发安全。
type EffectStore struct {
	pool *pgxpool.Pool

	// mu 保护 now：UseClock 与各方法的时钟读取互斥（套件在测试里注入时钟）。
	mu  sync.Mutex
	now func() time.Time
}

// NewEffectStore 构造副作用闸存储。pool 归调用方管理（Close 由它负责）。
//
// schema 必须已建好表（EnsureSchema）；search_path 已被连接池指到该 schema
// （见 NewEffectStores），故本包的 SQL 不写 schema 前缀。
func NewEffectStore(pool *pgxpool.Pool) *EffectStore {
	return &EffectStore{pool: pool, now: time.Now}
}

// UseClock 注入时钟（sidetest 契约套件用）。
//
// 过期判定必须读它，否则过期场景恒不触发（套件的硬要求）。
func (s *EffectStore) UseClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// clock 当前时刻（各方法的入口读一次，整个判定用同一时刻）。
func (s *EffectStore) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

// runTx 事务骨架：fn 报错即整体回滚（不落任何一行），成功才提交。
func runTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, "pgstore: 开事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, "pgstore: 提交事务失败")
	}
	return nil
}

// withinTx / withinTx2 / withinTx3 事务骨架的按返回值个数分的变体
// （Go 泛型变参表达不了「N 个返回值 + error」，拆具名变体最直白）。
//
// 「事务」是本实现全部原子性的来源——契约允许「一条语句**或**一个事务」，
// 本实现选事务：Acquire 的三判合一读多写多，一条语句表达不了。
func withinTx[T any](ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) (T, error)) (T, error) {
	var zero T
	err := runTx(ctx, pool, func(tx pgx.Tx) error {
		out, err := fn(tx)
		if err != nil {
			return err
		}
		zero = out
		return nil
	})
	return zero, err
}

func withinTx2[T1, T2 any](ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) (T1, T2, error)) (T1, T2, error) {
	var zero1 T1
	var zero2 T2
	err := runTx(ctx, pool, func(tx pgx.Tx) error {
		out1, out2, err := fn(tx)
		if err != nil {
			return err
		}
		zero1, zero2 = out1, out2
		return nil
	})
	return zero1, zero2, err
}

func withinTx3[T1, T2, T3 any](ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) (T1, T2, T3, error)) (T1, T2, T3, error) {
	var zero1 T1
	var zero2 T2
	var zero3 T3
	err := runTx(ctx, pool, func(tx pgx.Tx) error {
		out1, out2, out3, err := fn(tx)
		if err != nil {
			return err
		}
		zero1, zero2, zero3 = out1, out2, out3
		return nil
	})
	return zero1, zero2, zero3, err
}

// ensureActionRow / ensureResourceRow 保证行存在（Acquire 的自增主键语义：
// 「没做过」「没人占」就是空行——首个动作顺手把行建出来）。
func ensureActionRow(ctx context.Context, tx pgx.Tx, key sideeffect.Key) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO pgref_effect_action (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`,
		string(key))
	if err != nil {
		return errors.Wrap(err, "pgstore: 登记动作行失败", "key", string(key))
	}
	return nil
}

func ensureResourceRow(ctx context.Context, tx pgx.Tx, resource string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO pgref_effect_resource (resource) VALUES ($1) ON CONFLICT (resource) DO NOTHING`,
		resource)
	if err != nil {
		return errors.Wrap(err, "pgstore: 登记资源行失败", "resource", resource)
	}
	return nil
}

// actionRow Key 级状态的一行（列语义见 schema.go 的表注释）。
type actionRow struct {
	done      bool
	result    *[]byte
	uncertain bool
	reason    string

	intentResource *string
	intentFence    *int64
	intentHolder   *string
	intentExpires  *time.Time

	reconHolder  *string
	reconExpires *time.Time
}

// hasIntent 该 Key 上是否挂着一次未裁决的尝试。
func (r *actionRow) hasIntent() bool { return r.intentFence != nil }

// hasRecon 该 Key 上是否挂着对账占用。
func (r *actionRow) hasRecon() bool { return r.reconExpires != nil }

// lockAction 锁住并读取 Key 级状态行（调用前必须 ensureActionRow）。
func lockAction(ctx context.Context, tx pgx.Tx, key sideeffect.Key) (*actionRow, error) {
	row := tx.QueryRow(ctx,
		`SELECT done, result, uncertain, reason,
		        intent_resource, intent_fence, intent_holder, intent_expires,
		        recon_holder, recon_expires
		   FROM pgref_effect_action WHERE key = $1 FOR UPDATE`, string(key))
	var r actionRow
	if err := row.Scan(&r.done, &r.result, &r.uncertain, &r.reason,
		&r.intentResource, &r.intentFence, &r.intentHolder, &r.intentExpires,
		&r.reconHolder, &r.reconExpires); err != nil {
		return nil, errors.Wrap(err, "pgstore: 读动作行失败", "key", string(key))
	}
	return &r, nil
}

// resourceRow 资源级状态的一行。
type resourceRow struct {
	fence  int64
	holder *string
	until  *time.Time
}

// held 该资源当前是否被占着且租约未过期（零值 holder = 空闲）。
func (r *resourceRow) held(now time.Time) bool {
	return r.holder != nil && r.until != nil && r.until.After(now)
}

// lockResource 锁住并读取资源级状态行（调用前必须 ensureResourceRow）。
func lockResource(ctx context.Context, tx pgx.Tx, resource string) (*resourceRow, error) {
	row := tx.QueryRow(ctx,
		`SELECT fence, holder, until FROM pgref_effect_resource WHERE resource = $1 FOR UPDATE`,
		resource)
	var r resourceRow
	if err := row.Scan(&r.fence, &r.holder, &r.until); err != nil {
		return nil, errors.Wrap(err, "pgstore: 读资源行失败", "resource", resource)
	}
	return &r, nil
}

// leaseResourceErr 三项联查的第一项：租约的资源必须与动作的资源一致。
// （与内存版同一段判据：A 资源的租约被拿去做 B 资源的操作时，两份真相
// 拼出一个错误的「合规」。）
func leaseResourceErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if lease.Resource != act.Resource {
		return errors.Wrap(sideeffect.ErrResourceMismatch,
			"凭据被拒：租约与动作的资源不一致",
			"act_resource", act.Resource, "lease_resource", lease.Resource)
	}
	return nil
}

// holdOwnerErr 三项联查：资源闸是否仍由**这份凭据**持有（行已锁，直接比）。
//
// 判定顺序与内存版一致：令牌不匹配或已释放 → ErrFenceStale；
// 令牌还在这一代但持有者不是出示者 → ErrNotHolder。
func holdOwnerErr(act sideeffect.Action, lease sideeffect.Lease, rr *resourceRow) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if lease.Fence == 0 || rr.holder == nil || rr.fence != int64(lease.Fence) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次持有已失效（令牌不是最新代或资源已释放）",
			"key", string(act.Key), "resource", act.Resource)
	}
	if *rr.holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（令牌不是本次授予铸造的那份）",
			"key", string(act.Key), "resource", act.Resource)
	}
	return nil
}

// intentOwnerErr 三项联查：本次尝试是否仍归**这份凭据**所有。
func intentOwnerErr(act sideeffect.Action, lease sideeffect.Lease, a *actionRow) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if !a.hasIntent() {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已无未裁决的执行意图",
			"key", string(act.Key))
	}
	if *a.intentFence != int64(lease.Fence) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	if a.intentHolder == nil || *a.intentHolder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（意图不是本次授予铸造的那份）",
			"key", string(act.Key))
	}
	return nil
}
