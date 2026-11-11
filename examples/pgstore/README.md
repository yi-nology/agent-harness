# examples/pgstore —— PostgreSQL 参考存储 + 部署案例

`sideeffect.Store` / `sideeffect.FencedSink` / `taskboard.Board`（含
`RenewingBoard`）/ `checkpoint.StoreCAS` 的 **PostgreSQL 参考实现**，
连同把它们的判据在真实数据库上跑通的集成环境。

它是**参考实现，不是内核依赖**（「内核不实现存储」的边界不动）：内核包的
依赖图里没有 pgx；本子包只属于 `examples/`，宿主照抄它、不 import 它。
与 `examples/reference-runtime`（内存版）的分工：内存版证明**判据本身是对的**，
这一份证明**判据在跨进程下依然成立**——用内核的共享契约套件
（`sidetest` / `boardtest` / `sidetest.RunSinkContract`）在真实 PostgreSQL 上
原样跑通，不为任何实现开后门（场景级硬失败）。

## 判据落库形态（宿主抄它的那部分）

| 契约 | 落库形态 |
|---|---|
| `sideeffect.Store.Acquire` | **一个事务**里的三判合一：`SELECT … FOR UPDATE` 行锁（锁序恒为资源行 → 动作行）+ 按契约顺序判定（已完成 > 结果未知 > 执行中 > 资源被占 > 授予）；授予时令牌 = 已见最大值 + 1、持有者身份 `sideeffect.MintHolder()` 同事务铸造、执行意图同事务落库 |
| `Complete` / `Release` / `Abandon` / `Renew` / `MarkUncertain` | 同一事务内**三项联查**（令牌 + 持有者 + 资源一致），校验失败即整体回滚——「标记写失败必须保住执行意图」由此成立 |
| `ResolveUncertain` / `BeginReconcile` | 裁决不校验栅栏（依据是下游事实）；对账占用同刻至多一人，过期可重入 |
| `sideeffect.FencedSink.WriteFenced` | 一条带条件的 `INSERT … ON CONFLICT DO UPDATE … WHERE seen_fence <= presented` 原子预占；写入体经 `ExecWithin` 走**同一事务**，报错即整体回滚（**不得部分写**） |
| `taskboard.Board` | 认领是事务内的读-判-写；令牌随机铸造、接管代次同一次落库；释放/完成/续期条件更新只认令牌 |
| `checkpoint.StoreCAS` | 条件 upsert（`WHERE 已持久化序号 < 本次序号`），`Seq == 0` 先拒 |

## 跑起来（三选一，按场景）

### 1) 部署冒烟：一条命令起环境（默认 profile）

```bash
docker compose -f examples/docker-compose.yml up --abort-on-container-exit --exit-code-from smoke
```

起 `postgres:16` + 参考宿主冒烟（`cmd/pgsmoke`：幂等键重放 / 栅栏拒陈旧 /
双宿主竞争恰好一个 applied / 双 agent 认领恰好一个赢家），退出码 0 即通过。

### 2) 契约验证：同一套共享套件 + SIGKILL e2e（verify profile）

```bash
docker compose -f examples/docker-compose.yml --profile verify up \
  --abort-on-container-exit --exit-code-from contract
```

跑 `go test ./examples/pgstore/` 的全部内容（见下）。

### 3) 本地开发（复用宿主 Go 工具链，最快）

```bash
make integration        # compose 起 postgres → go test → down；compose 起不来会明确报错
# 或指向已有实例：
GAK_PGREF_DSN=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable \
  go test ./examples/pgstore/ -count=1
```

无 PostgreSQL 时用例在**环境入口**显式跳过（`t.Skipf` 写明原因与起法，
`go test -v` 可见）；套件场景本身不跳——跑不过就是跑不过。

## 测试都判了什么

| 层 | 内容 |
|---|---|
| `TestSideEffectStoreContract` | `sidetest` 29 条：三判合一 / 三项联查 / 执行意图 / 结果未知 / 对账占用 / Unsafe 裁决 / 标记失败……与内存版参考实现跑**同一段套件**（issue #94 的核心验收） |
| `TestTaskBoardContract` | `boardtest` 全家族（含并发认领 10 与 100 两档） |
| `TestDownstreamSinkContract` | `sidetest.RunSinkContract` 6 条：栅栏一族（issue #84 的核心验收，判据收编自 sideeffect 包内用例） |
| `TestSnapshot…` | 快照单调闸（拒陈旧 / 拒零 / 无记录 NotFound / 并发同序号恰好一个赢家） |
| `TestPGE2E*` | **真实进程故障恢复**（issue #95）：子进程执行动作 → `SIGKILL` → 等 TTL 过期 → 断言同一 Key 转需对账**不被重放**；对账「已生效」后只重放结果；双进程竞争恰好一个 `OutcomeApplied`。子进程是 re-exec 本测试二进制（unix build tag），跨进程共享状态只有 PostgreSQL |

CI 的 `integration-pg` job（postgres service container）每次推送都真跑同一批用例。

## compose 服务清单

| 服务 | profile | 职责 |
|---|---|---|
| `postgres` | 默认 | PostgreSQL 16（healthcheck 门控依赖方） |
| `smoke` | 默认 | 参考宿主冒烟（`cmd/pgsmoke`）——「起环境即验证部署可用」 |
| `contract` | `verify` | 共享契约套件 + SIGKILL e2e 在容器里真跑 |

### Kubernetes（可选 profile，非默认路径）

compose 里的三个角色对应 K8s 对象：`postgres` → StatefulSet（持久卷 +
readinessProbe 对应 healthcheck）、`smoke`/参考宿主 → Deployment（环境变量
`GAK_PGREF_DSN` 指 Service DNS）、`contract` → Job（跑同一批契约用例，作为
发布前的验收闸）。本仓不附带 manifest：无云环境时 compose 已覆盖全部可验证
面，K8s 形态留给宿主按自己的底座装配。

## 新依赖

`github.com/jackc/pgx/v5`（v5.13.0，纯 Go，`CGO_ENABLED=0` 交叉构建不受影响）——
**只进本子包的依赖图**，内核各包 `requires` 均不含它（policy 的
`dependency_direction.ext_prefixes` 登记了 `github.com/jackc`，内核包若 import
驱动会被依赖方向门禁当场拦下）。
