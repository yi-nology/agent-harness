# metrics 接入点清单（issue #100）

> 「落地 = 指标规范文档 + 参考埋点实现/骨架 + 各子系统接入点清单」的第三件。
> 规范本体（名称 / 类型 / 维度 / 基数上界）在 [`metrics.go`](metrics.go) 的
> `Specs` 登记表——本文只回答「**在哪埋、埋什么值**」。两边必须同步改：
> 登记表加指标而本文不加接入点，等于埋了个没人知道在哪响的铃。

埋点形态：生产代码经包级便利入口（`metrics.Count` / `metrics.Observe` /
`metrics.Set`）一行埋；宿主装配期 `metrics.SetDefault(...)` 接自己的后端
（参考形态见 `recorder.go` 的 `Mem`）。**内核各包当前都不自动埋**——本仓是
机制库，指标经宿主装配才投递；本清单给出的是宿主装配埋点时的准确接入位置。

## harness.exec.duration（histogram / ms）

口径：与轨迹属性 `harness.trajectory.duration_ms` 同源——**起止相减现算，
只记实测**。轨迹上没有实测时长的行（点事件、缺配对）不埋，不写 0 不估算。

| 接入点 | 埋什么 |
|---|---|
| 宿主导出/渲染轨迹行处（`trajectory` 投影产出的 `Row`） | `Observe(NameExecDuration, ms, {LabelExecKind: 行类目, LabelExecStatus: 状态映射})` |

- `LabelExecKind` 取 `Row` 的 kind：`trajectory.KindTurn`…`KindCheckpoint` 对应的
  字符串（与 `ExecKind*` 常量逐字一致，测试已比对）。
- `LabelExecStatus`：行已收口且成功 → `ok`；已收口且失败/中止 → `error`；
  仍在途（无结束时刻）→ `unset`（三值纪律：在途不记 error）。

## harness.retry.total（counter / 1）

| 接入点 | 埋什么 |
|---|---|
| `retryplan.Decide` 的**每个调用方**（拿到 `Decision` 之后） | `Count(NameRetryTotal, 1, {LabelRetryPath: d.Path.String()})` |

`retryplan` 只给路径不埋点：它是纯函数件，不知道调用方想把计数投给谁。
宿主在自己调用 `Decide` 的地方（工具失败处置、任务重试入口）埋，一次决策一条。

## harness.lease.contended（counter / 1）

「这次没执行成」的竞争面信号，两处子系统：

| 接入点 | 埋什么 |
|---|---|
| `sideeffect.Runner.Execute` / `ExecuteWith` / `Run` 返回后 | `OutcomeContended` → `Count(NameLeaseContended, 1, {LabelLeaseSubsystem: "sideeffect", LabelLeaseOutcome: "contended"})`；`OutcomeUncertain` → `outcome: "uncertain"`（成功 / replayed 不计——那是正常路径不是竞争） |
| `taskboard.ConcurrentClaim` / `DuplicateTokenSubmit` 返回后 | 输家数（`len(agents) - 赢家数`）→ `Count(NameLeaseContended, 输家数, {LabelLeaseSubsystem: "taskboard", LabelLeaseOutcome: "contended"})`（taskboard 的认领竞争没有 uncertain 语义：输家明确知道没赢） |

## harness.recovery.total（counter / 1）

恢复成功率 = `outcome=success` 与 `outcome=failure` 两时序的比值，看板现算。

| 接入点 | 埋什么 |
|---|---|
| 宿主的快照恢复循环（`checkpoint.BuildPlan` 产 Plan → 执行 Plan 的收口处） | 恢复推进完成 → `Count(NameRecoveryTotal, 1, {LabelRecoverySubsystem: "checkpoint", LabelRecoveryOutcome: "success"})`；`Plan.Blocked()` 为真或执行中断 → `outcome: "failure"`（blocked 也计 failure——「没恢复成」只有一种计法） |
| `sideeffect.Runner.Reconcile` 返回后 | 裁决成功落地 → `subsystem: "sideeffect", outcome: "success"`；裁决失败或 Key 再次转未知 → `outcome: "failure"` |

## harness.sideeffect.uncertain（gauge / 1）

未知结果积压量 = 当前处于「结果未知」态的副作用 Key 数。**仪表语义：设值，
不是累加**——每次上报用存储侧的真实计数 Set，自愈口径（漏埋一次不会永久偏）。

| 接入点 | 埋什么 |
|---|---|
| 宿主 `sideeffect.Store` 实现的未知态计数（`MarkUncertain` 转入 +1；`Reconcile` 裁决清偿 -1；以存储查询值为准） | `Set(NameUncertainBacklog, 未知Key数, nil)`（零维，无标签） |

明细（哪个 Key、哪个资源）走日志与对账面，不进指标——`key` / `resource`
在禁区清单（基数 = 业务写入量）。PostgreSQL 参考实现见
`examples/pgstore`（未知态一张表，`COUNT(*)` 即积压量）。

## 新增指标的流程

1. `metrics.go`：加常量 + `specs` 登记（类型 / 单位 / 维度 / 基数上界与依据）；
2. 本文档加接入点（在哪个包哪个函数上埋、埋什么值）；
3. `metrics_test.go` 的自校验会查登记表自洽；枚举与上游对齐的（exec.kind 对
   trajectory、retry.path 对 retryplan）补一条逐字比对测试。
