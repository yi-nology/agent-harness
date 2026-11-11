# examples/reference-runtime —— 参考运行时示例

一个**可运行**的编排运行时参考实现：把本仓四个机制件（`checkpoint` /
`commitgate` / `sideeffect` / `taskboard`）接到一条真实的链路上，
并演示它们合起来解决的那件事——**进程在中途没了以后，接着跑，而不是重做**。

示例本身不含任何领域概念：步骤叫什么由宿主决定，内核只认「步骤 id + 依赖声明 +
幂等键 + 资源」。

## 怎么跑

```bash
# 跑演示（6 步，每步打印一行带编号的记录）
CGO_ENABLED=0 go run ./examples/reference-runtime

# 跑测试（含本仓两个共享契约套件）
CGO_ENABLED=0 go test ./examples/... -count=1
```

## 演示了什么

| 步 | 内容 | 用到的机制件 |
|---|---|---|
| 1 | 建一条 3 步骤的任务，步骤间声明 `DependsOn` | `checkpoint.StepDef` |
| 2 | 执行前两步，每步落一次进度快照、提交一次终态 | `checkpoint.Committer` / `commitgate.Guard` |
| 3 | 中断：流程停在第 2 步，第 3 步未开始 | —（模拟进程没了） |
| 4 | 恢复续跑：重建计划 ⇒ 前两步被跳过、只跑第三步，**副作用未重放**（执行体调用次数为证） | `checkpoint.BuildPlan` |
| 5 | 并发认领：两个执行者同时认领同一条待领任务 ⇒ 恰好一个赢家 | `taskboard.Board` |
| 6 | 副作用对账：执行体报错 ⇒ 判「结果未知」⇒ **重试被拒**（动作体不再被调用）⇒ 裁决 ⇒ 之后同一键只重放结果 | `sideeffect.Runner` |

第 4 步的「未重放」是整份示例的要点：**续跑不是重放**。已完成的步骤由
`checkpoint` 的可信完成集判定为跳过，副作用由 `sideeffect` 的幂等键保证
「同一逻辑动作至多施加一次」。

## 目录里各文件是什么

| 文件 | 职责 |
|---|---|
| `main.go` | 演示入口：跑完 6 步并打印过程记录（`runDemo` 可被测试直接调用） |
| `store.go` | 内存存储：一份实现同时满足 `checkpoint.StoreCAS` / `sideeffect.Store` / `taskboard.Board` |
| `runtime.go` | 编排骨架：建任务 → 按计划执行步骤 → 落快照 → 提交终态 |
| `runtime_test.go` | 端到端用例（含负对照：证明判据有分辨力） |
| `store_test.go` | 存储实现的契约用例（跑通 `sidetest.Run` 与 `boardtest.Run`） |

## 真实 SQLite / PostgreSQL 怎么接：宿主要实现哪些接口

内核**只给契约与判据，不给存储**。宿主要落库，实现下面这些接口即可；
本仓的 `archcheck` 门禁会检查你声明的依赖边是否与实际 import 一致。

**可抄的成品已就位（v0.33.0 起）**：[`examples/pgstore`](../pgstore/README.md)
是三套契约的 PostgreSQL 参考实现——`sidetest` / `boardtest` / 栅栏套件在真实
数据库上跑同一段判据，外加 SIGKILL 故障恢复 e2e 与 docker compose 部署案例。
本节的落库形态表是它的**摘要**，实现细节以那份代码为准。

### 1. `checkpoint.StoreCAS`（进度快照，单调存取）

| 方法 | 语义 | SQLite / PostgreSQL 形态 |
|---|---|---|
| `SaveIfNewer(ctx, snap)` | 仅当已持久化序号**严格小于**本次序号才写入，否则返回 `ErrStaleSeq` 且**不得写入** | `UPDATE checkpoint_snapshot SET seq=?, status=?, done=? WHERE run_id=? AND seq < ?`；影响行数为 0 ⇒ 判陈旧 |
| `Load(ctx, runID)` | 读快照；无记录返回 `ErrNotFound`（**不得**返回零值 + nil） | `SELECT ... WHERE run_id=?` |

可选再实现 `checkpoint.Store`（`Save` + `Load`）：`Save` 对同一
`(RunID, Seq)` 幂等。

表形：`checkpoint_snapshot(run_id TEXT PRIMARY KEY, seq INTEGER, status TEXT,
done TEXT, fields TEXT)`。

### 2. `sideeffect.Store`（副作用闸，跨进程去重）

六个方法，其中 **`Acquire` 是全部难点**：

| 方法 | 语义 | 落库形态 |
|---|---|---|
| `Acquire(ctx, act, ttl)` | **一次原子操作**里同时判定：已完成 ⇒ `replayed`；结果未知 ⇒ `uncertain`；资源被占且租约未过期 ⇒ `contended`；否则授予（令牌 = 该资源已见过的最大值 + 1） | 一个事务内的条件更新：`UPDATE sideeffect_resource SET fence=fence+1, holder=?, until=? WHERE resource=? AND (holder IS NULL OR until < ?)` + 读动作表的完成/未知标记 |
| `Complete(ctx, act, lease, res)` | 令牌仍是最新代才接受；记为已完成并留存结果；**先到者为准** | 条件更新 + `INSERT ... ON CONFLICT DO NOTHING` |
| `Release(ctx, act, lease)` | 只释放自己的持有；令牌陈旧 ⇒ `ErrFenceStale` | 条件更新 `WHERE resource=? AND fence=? AND holder=?` |
| `Renew(ctx, act, lease, ttl)` | 续期 + **校验资格**（闸已易主 ⇒ `ErrFenceStale`） | 同上，条件更新里带上 `fence=?` |
| `MarkUncertain(ctx, act, lease, reason)` | 把该键标记为「结果未知」；令牌陈旧 ⇒ 拒绝 | 条件更新动作表 |
| `ResolveUncertain(ctx, act, res)` | 裁决：已生效 ⇒ 记为已完成并留存结果（之后 `replayed`）；未生效 ⇒ 清除未知标记；**先到者为准** | 同上 |

表形：`sideeffect_action(key TEXT PRIMARY KEY, done INTEGER, result BLOB,
uncertain INTEGER, reason TEXT)` 与 `sideeffect_resource(resource TEXT PRIMARY KEY,
fence INTEGER, holder TEXT, until TIMESTAMP)`。

⚠️ **`Acquire` 不得拆成「读闸 → 判 → 写闸」三步**：那三步之间正是重复施加
的窗口——两个执行者会同时读到「没做过、资源空闲」，各自认定自己该做。

### 3. `taskboard.Board`（任务认领面）

| 方法 | 语义 | 落库形态 |
|---|---|---|
| `List(ctx)` | 返回一致快照 | `SELECT ... ORDER BY id` |
| `Claim(ctx, id, agent, lease)` | **原子**认领：不存在 / 被占 / 依赖未完成 / 已终态各有各的哨兵错误 | `UPDATE task SET state='claimed', owner=?, token=?, until=? WHERE id=? AND state IN ('open','failed') AND (until IS NULL OR until < ?) AND <依赖均已完成>`；影响行数为 1 才算赢 |
| `Release(ctx, id, token)` | 凭令牌归还；令牌不匹配 ⇒ `ErrLeaseLost` | `UPDATE ... WHERE id=? AND token=?` |
| `Complete(ctx, id, token)` | 凭令牌完成 | `UPDATE ... WHERE id=? AND token=?` |

表形：`task(id TEXT PRIMARY KEY, title TEXT, deps TEXT, state TEXT, owner TEXT,
token TEXT, until TIMESTAMP, seq INTEGER)`。

### 4. 时钟

过期判定读**哪个**时钟必须与实现一致：示例里 `sideeffect` 的过期判定读可注入
时钟（本仓 `sidetest` 套件会注入假时钟，否则过期场景恒不触发、套件退化成一组
空断言），`taskboard` 的租约判定读应用时间。生产实现里两者都应统一读数据库
时间（`CURRENT_TIMESTAMP`），避免多机时钟漂移。

## 边界声明

- **本示例的原子性只来自一把进程内的互斥锁**，只在单进程内成立。跨进程部署时，
  上面的每一条判据都必须落到数据库的原子操作上——内核的判据是**确定性的**，
  但「谁赢」由存储判，不由进程判。落到数据库上的成品见
  [`examples/pgstore`](../pgstore/README.md)：同一段 `sidetest` / `boardtest`
  契约套件 + SIGKILL 故障恢复 e2e 在真实 PostgreSQL 上判过它。
- **副作用的上界是「同一逻辑动作至多一次施加」，不是 exactly-once**。
  远端是否生效由远端说了算：内核能做的只是把「不知道」显式化（结果未知）并要求
  对账，**不能**替调用方判定「没生效」。要拿到 exactly-once，需要把幂等键一路
  传到远端 API，那是宿主的接线责任。
- **续跑依赖快照可信**：`checkpoint.BuildPlan` 按依赖声明判定可信完成集，
  失序的完成记录一律不采信（宁可多跑一步，也不采信说不清来历的完成记录）。
- 本示例不实现存储、不实现重试策略、不实现补偿回滚，也不做任何领域判断。
