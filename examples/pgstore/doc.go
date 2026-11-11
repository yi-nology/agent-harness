// Package pgstore 副作用闸 / 任务认领面 / 进度快照的 **PostgreSQL 参考实现**。
//
// # 它是什么、不是什么
//
// 这是 examples 里的**参考实现**，不是内核依赖：内核（sideeffect / taskboard /
// checkpoint）依旧只给契约与判据，不给存储。本包演示的是「把同样的判据落到
// 一个真实数据库的原子操作上」长什么样——宿主照抄它，比照着契约散文重写，
// 漂移的概率小一个量级。
//
// 它与 examples/reference-runtime（内存版参考实现）是一对：
//
//   - 内存版证明「判据本身是对的」（进程内互斥锁即可）；
//   - 本包证明「判据在**跨进程**下依然成立」（判据归存储，不归进程）——
//     用内核的共享契约套件（sidetest / boardtest / RunSinkContract）在
//     真实 PostgreSQL 上原样跑通，不为任何实现开后门。
//
// # 三套契约各自的落库形态
//
//   - sideeffect.Store：Acquire 的「三判合一」（已完成 > 结果未知 > 执行中 >
//     资源被占 > 授予）在一个事务里完成——SELECT … FOR UPDATE 行锁把
//     「读状态 → 判 → 写」钉成跨进程原子；执行意图与授予（令牌 + 持有者身份，
//     sideeffect.MintHolder 在同一事务里铸造）写在同一事务里。Complete /
//     Renew / MarkUncertain / Abandon / Release 全部三项联查（令牌 + 持有者 +
//     资源一致），缺一即拒（ErrFenceStale / ErrNotHolder / ErrResourceMismatch）。
//   - sideeffect.FencedSink：一条带条件的 INSERT … ON CONFLICT DO UPDATE
//     （WHERE seen_fence <= presented）先原子预占，写入体在同一事务里执行；
//     写入体报错 ⇒ 整个事务回滚 ⇒ 不得部分写。
//   - taskboard.Board：认领是一个事务里的读-判-写（行锁），令牌与接管代次
//     在同一次操作里铸造；释放 / 完成 / 续期只认令牌。
//   - checkpoint.StoreCAS：一条带条件的 upsert（WHERE 持久化序号 < 本次序号），
//     影响行数为 0 即判陈旧。
//
// # 时钟
//
// 过期判定读可注入时钟（sidetest / boardtest 的硬要求）：缺省 time.Now，
// 经 UseClock 注入。跨进程部署时所有进程共享真实时钟即可；测试注入假时钟时，
// 存进数据库的时刻与比较用的「当前时刻」同源，判据不变。
//
// # 数据库连接
//
// 构造函数直接收 *pgxpool.Pool 与 schema 名：本包不管理连接串与凭据，
// 那是宿主的接线责任。隔离方案是**每个实例一个 schema**（EnsureSchema 建
// CREATE TABLE IF NOT EXISTS 一套表），契约套件每个子测试建一份全新实现
// ——与「factory 每次构造全新实现」的套件约定对齐。
//
// # 部署与验证入口
//
//   - 契约 + 故障恢复用例：GAK_PGREF_DSN=… go test ./examples/pgstore/…
//     （见 examples/docker-compose.yml 与 Makefile 的 integration 目标）；
//   - 冒烟部署：docker compose -f examples/docker-compose.yml up（pgsmoke 服务）。
package pgstore

import (
	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// 编译期断言：本包的实现确实满足三套存储中立契约（+ 下游栅栏与续期扩展）。
var (
	_ sideeffect.Store        = (*EffectStore)(nil)
	_ sideeffect.FencedSink   = (*Downstream)(nil)
	_ taskboard.Board         = (*BoardStore)(nil)
	_ taskboard.RenewingBoard = (*BoardStore)(nil)
	_ checkpoint.StoreCAS     = (*SnapStore)(nil)
)
