# 性能基线（2026-11-13，issue #101）

> 「数字要有主」纪律在性能面的落法：基线数字随本文入库，**数据规模与机器口径
> 写在同一张表里**——没有口径的数字不可比，不可比的数字不如没有。
>
> 本文回答三个问题：基线是在什么条件下测的；怎么看复跑结果（哪个数是判据、
> 哪个数是参照）；判据层退化时从哪一眼先看起。

## 机器口径

| 项 | 值 |
|---|---|
| 日期 | 2026-11-13 |
| 机器 | Apple M6（darwin/arm64），GOMAXPROCS=12 |
| 工具链 | go 1.27.4 |
| 构建口径 | `CGO_ENABLED=0`（Makefile GOENV 同款），`GOWORK=off` |
| 跑法 | `go test -run '^$' -bench 'BenchmarkPTCRunInProcess\|BenchmarkGuardSet\|BenchmarkCheckpoint\|BenchmarkTaskBoard\|BenchmarkSideEffect' -benchmem -count=1 ./ptc/ ./guardset/ ./checkpoint/ ./taskboard/ ./sideeffect/`（= `make bench`） |

复跑入口：**`make bench`**。它是独立目标，**不在 `make check` 里**——基线是
记录与量级参照，不是每次 PR 都要真跑的提交前门禁（见 issue #101 验收）。

## 怎么读：allocs/op 是判据，ns/op 是参照

- **allocs/op 是主判据**：分配次数由代码结构决定，跨机器可比、不随负载抖动。
  同规模下 allocs/op 上涨 = 判据链多了拷贝 / 装箱 / 分配，**先看这一列**。
- **ns/op 是量级参照**：同一台机器上 ±30% 内的波动视为噪声（CPU 频移 /
  后台负载都会进来）；跨机器比较 ns/op 没有意义。真要判「是不是慢了」，
  用 `-count=10` + `benchstat` 对比两份分布，不要拿单次数字下结论。
- B/op 随数据规模线性，只在同规模下可比。

## 基线表

### PTC 主体（进程内 goja：VM 起停 + 程序执行 + 判定环逐调用过闸）

规模：程序内循环调用绑定 calls 次，绑定返回固定 4 字节；`Stages` 挂恒放行判定环
（判定环成本含在基线里——它是主体路径的一部分）。

| 基准 | 规模 | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `BenchmarkPTCRunInProcess/calls=1` | 1 次绑定调用 | 38,542 | 57,082 | 757 |
| `BenchmarkPTCRunInProcess/calls=10` | 10 次 | 82,414 | 94,243 | 1,329 |
| `BenchmarkPTCRunInProcess/calls=50` | 50 次（缺省 MaxCalls 上限） | 212,675 | 257,495 | 3,851 |

量级结论：VM 构造占大头（calls=1 与 calls=10 差值远小于线性），每次绑定调用的
边际成本 ~3µs。子进程路径的往返延迟另有基线（`ptc/subprocess_bench_test.go`）。

### GuardSet（判定环守卫求值）

规模：8 守卫，Rank 递增，判定函数立即返回。

| 基准 | 规模 | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `BenchmarkGuardSetEvaluateAllow` | 8 守卫全放行 | 68.8 | 0 | 0 |
| `BenchmarkGuardSetEvaluateDeny` | 8 守卫一票拒绝 | 64.7 | 48 | 1 |
| `BenchmarkGuardSetNew` | 8 守卫构造 | 140.7 | 280 | 2 |

量级结论：热路径（每次工具调用过闸）零分配；拒绝形态多一次 Denials 切片分配。
Evaluate 每守卫 ~8ns，装配几十条守卫仍是亚微秒级。

### Checkpoint（进度快照提交链）

规模：Snapshot.Done 装 100 个步骤 ID（Clone 深拷贝随规模线性）；RunID 固定、
Seq 逐次递增。

| 基准 | 规模 | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `BenchmarkCheckpointSeqGateAccept` | 闸判定（纯内存） | 29.2 | 0 | 0 |
| `BenchmarkCheckpointCommit` | Committer.Commit（闸 + 最小内存存储） | 238.6 | 1,792 | 1 |

量级结论：Commit 的 1,792 B/op 主体是 Snapshot.Clone 的深拷贝（100 步）——
每完成一个步骤落一次快照的粒度约定下，这是宿主应知的写放大下限；
换真实存储后 IO 成本另算（不进本基线）。

### TaskBoard（任务图校验 Check，纯函数）

规模：200 任务。一般形态每任务依赖 0..3 个更早任务（约 1/3 终态、1/3 失败）；
链形态串成一条链（环检测每次走到底，最坏形态之一）。

| 基准 | 规模 | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `BenchmarkTaskBoardCheck` | 200 任务 DAG（0..3 依赖） | 52,555 | 66,584 | 671 |
| `BenchmarkTaskBoardCheckChain` | 200 任务单链 | 29,291 | 56,056 | 409 |

量级结论：派发前的图校验是 O(V+E) 级，万级任务仍毫秒内；分配主要来自
Problem 聚合与 map 构建。

### SideEffect（至多一次施加链）

规模：每次迭代全新 Key / Resource（授予路径满血：无重放、无竞争），施加函数
返回固定小 Result，TTL 30s。

| 基准 | 规模 | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `BenchmarkSideEffectExecute` | 全新 Key 的授予→施加→完成 | 700.8 | 400 | 9 |

量级结论：判据链本身亚微秒——「至多一次」的判定不构成调用延迟的瓶颈；
真实存储的原子判定（条件写 / 行锁）才是宿主要量化的部分。

## 演进纪律

1. 新增基准 ⇒ 同一提交里在本文补行（规模与机器口径与数字同行登记）；
2. 改了判据链导致 allocs/op 变化 ⇒ 在 PR 里写明为什么（多了什么、省了什么），
   并更新本文数字——基线不许过期，过期基线比没有基线更误导；
3. 机器口径换了（新机器 / 新工具链）⇒ 整表重测重录，不逐格补丁——
   跨口径的数字拼在同一张表里就是自欺。

## 存量基线

2026-10-21 已有三处（issue #101 前的唯一基线）：`ptc/subprocess_bench_test.go`
（子进程管道往返）、`tooldisclose/search_oracle_test.go`（检索打分）、
`skilldisclose/skilldisclose_test.go`（技能检索）。它们的数字记录在各自
头注与当时的设计文档里，本文不重复收录。
