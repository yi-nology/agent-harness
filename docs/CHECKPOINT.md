# checkpoint：编排续跑契约（2026-10-18 初稿，2026-10-20 补跨进程闸）

> 动机来自对标 `octop-harness` v1.0.4 的盘点。结论：本仓缺的不是功能面，
> 是**「崩溃后能接着跑」这一条编排基本保证**——`eventbus` 注明「持久化在发布点
> 由宿主负责」，此前内核自己回答不了「这一步执行到哪了、重启后该从哪继续」。
>
> **2026-10-20 补充**：§四 增加「跨进程闸」一节（`StoreCAS` / `Committer`），
> §七 的接入片段改为现行 API（`BuildPlan` 收**指针**、存取走 `StoreCAS`）。
> 接入全景见 [USAGE.md §3.6](USAGE.md#36-断点续跑checkpoint--commitgate)。

## 一、为什么这是内核的活，不是宿主的

「编排是代码，LLM 只做单环节」的必然推论是：编排过程可被**重演**。
既然可重演，恢复方式就该是**可机械判定**的，而不是留给宿主在崩溃那一刻临场决定
——临场决定必然选最坏的那个（要么盲目全量重跑、要么盲目跳过）。

宿主负责的是**存哪**（DB / 文件 / KV），内核负责**该不该继续、继续到哪**。

## 二、关键取舍：续跑而非重放

| 形态 | 好处 | 代价 |
|---|---|---|
| 重放 | 编排是代码，结果确定 | **已产生的副作用会再来一次** |
| 续跑 | 副作用不重复 | 依赖快照足够可信 |

本包选**续跑**，并把它变成纯函数 `BuildPlan(graph, snap) Plan`。

## 三、三条不变量

### 1. 已完成的可信边界 = 图内**连续已完成的前缀**

一步的完成，只有在它图内的前驱也都已完成时才可信。
若完成集有空洞（后面几步完成了、前面没完成），说明推进本身已失序——
此时采信其中一部分，等于**承认一个已失序的快照仍然可信**，而那正是续跑唯一危险的前提。

故：**空洞之后的完成一律不采信**，整体重跑，并报 `AnomalyDoneOutOfOrder`。

> 代价是「失序时会多跑几步」。这个方向是刻意选的：**多跑可恢复，重复副作用往往不可。**

### 2. 已终结的运行不被自动重开

快照标记运行已终结（`succeeded`/`failed`/`cancelled`/`timed_out`）、图里却有未完成步骤时，
计划置为 `Blocked()` 且 `Run`/`Skip` **均留空**——不擅自重开已终结的运行，
也不给一个「看起来能跑」的半截计划。是否重开是宿主的决定。

### 3. 图收缩必须可见

快照里的某步在当前图中不存在时，报 `AnomalyDoneNotInGraph`（点名该步）。
内核不重跑它（不在图里），但**必须报告**——宿主可能刚换过编排包，
无声跳过会让「换包后少做了事」彻底不可见。

## 四、并发落库乱序：进程内闸与跨进程闸

`Store` 的幂等实现挡不住它——两次写都是合法的。真实故障模式是
**第 N+1 步的快照先落库、第 N 步的慢写随后到达**，把新状态覆盖成旧状态。
结果是恢复点**静默回退**：不报错，只在下一次崩溃时让已完成的步骤重跑。

`SeqGate` 要求同一 `RunID` 下 `Seq` **严格递增**（相等也拒）。
注意它只保证「不倒退」，**不保证「已落库」**——两者失败必须分别处置，不要互相掩盖。

### 进程内闸的边界：重启即空

`SeqGate` 的记录只在**进程内存**里，而本包的立论恰恰是崩溃恢复——
进程重启后闸是空的，上一进程写下的陈旧快照会畅通写入。
补救是宿主在重启后用 `Store.Load` 把最新快照灌进来（`Seed` / `SeedFromStore`）：
**不灌不是「闸没生效」，是「闸被重置后没人告知」。**

### 跨进程闸：`StoreCAS` 与 `Committer`

进程内闸挡不住**多副本**——两个进程各自有一份 `SeqGate`，各自都认为自己是第一道。
故本包另给一条跨进程边界 `StoreCAS`：唯一增量是 `SaveIfNewer`
（**仅当已持久化序号严格小于本次 `Seq` 才写入**，否则必须返回 `ErrStaleSeq` 且不得写入）。

宿主实现 `StoreCAS` 即自动获得跨进程的单调保证，`SeqGate` 退化为**进程内的快速过滤**
（省掉绝大多数注定失败的数据库往返），**不再是唯一的防线**。

`Committer` 则把「进程内抢序号 → 落库 → **仅失败时**回滚」固化成一个调用
（`Commit`）：三步的顺序是**可回滚性的要害**——
`Commit` 成功时绝不能回滚（否则闸退到存储前面，一次陈旧慢写就畅通无阻），
落库失败时**必须**回滚（否则该 run 卡死：重试被拒为陈旧、跳过则留永久空洞）。
固化进 `Committer` 是为了让宿主**无法漏掉**这两步中的任何一步。

## 五、边界

与 `contract` 同口径：**编排语义字段定死，领域字段走开放键**。
内核解释 `Done`/`Seq`/`Status`，不解释 `Fields` 里的任何键。

终态常量直接复用 `contract` 的状态集（policy `requires: [contract]`）——
终态语义不允许被两个包各定义一份，否则必然漂移。

## 六、未做（明确划界）

- **不实现任何 `Store`**：本包是纯接口面 + 纯函数，零 I/O。持久化方案属宿主。
- **不定义快照粒度**：每步一次还是更细，由宿主按自己副作用的代价选。
- **不做跨 run 的编排图版本协商**：`AnomalyDoneNotInGraph` 只报告，不试图自动迁移。
- **不做锁**：`SeqGate` 只护单进程内的序号单调；跨进程并发靠宿主实现的
  `StoreCAS.SaveIfNewer`（条件更新）保证——**内核不代宿主做原子性**。

## 七、宿主接入

```go
// 宿主用 *model.Session 实现 StoreCAS（适配层留在宿主，约 40 行）
type sessionStore struct{ repo *repo.Repo }

// SaveIfNewer：条件更新——序号不大于已存序号则不写，且不得部分写
func (s sessionStore) SaveIfNewer(ctx context.Context, snap checkpoint.Snapshot) error {
    return s.repo.CASUpsert(snap.RunID, snap.Seq, snap)
}

func (s sessionStore) Load(ctx context.Context, runID string) (checkpoint.Snapshot, error) {
    // 无记录必须返回 ErrNotFound，不得返回 (零值, nil)——
    // 那会被上游误判成「从没跑过」从而全量重跑。
    snap, ok, err := s.repo.Get(runID)
    if !ok {
        return checkpoint.Snapshot{}, checkpoint.ErrNotFound
    }
    return snap, err
}

gate := checkpoint.NewSeqGate()
store := sessionStore{repo: r}
cm := checkpoint.NewCommitter(gate, store)

// ★ 进程启动/重启后必须回灌进程内闸，否则闸是空的、陈旧写畅通无阻
if err := cm.SeedFromStore(ctx, runID); err != nil {
    return err
}

snap, err := store.Load(ctx, runID)
if errors.Is(err, checkpoint.ErrNotFound) {
    snap = checkpoint.Snapshot{} // 首次执行，全量 Run
} else if err != nil {
    return err
}

// BuildPlan 的第二个参数是**指针**（内核不复制快照，省一次分配）
plan := checkpoint.BuildPlan(graph, &snap)
if plan.Blocked() { /* 交人工/宿主决策，内核不替它决定 */ }

// 提交用 Committer：三步顺序已固化，宿主无法漏掉「失败必须回滚」
if err := cm.Commit(ctx, nextSnap); err != nil {
    return err
}
```

> 接缝有两个，按需要选：**只在本进程内续跑**用 `Store`；
> **多副本 / 跨进程**用 `StoreCAS`（多一个 `SaveIfNewer`，换来跨进程单调保证）。
> 完整接入路线见 [USAGE.md §3.6](USAGE.md#36-断点续跑checkpoint--commitgate)。

**测试分工**（同 `contract` 的做法）：

| 面 | 归属 | 钉什么 |
|---|---|---|
| 恢复判定机制（前缀/空洞/终态/图收缩）| ✅ `agent-harness/checkpoint`，合成编排图 | 与宿主无关的机制不变式 |
| 「换包后少做了事」的告警是否真的送达用户 | ❌ 留宿主 | 产品级承诺，不是内核机制属性 |
