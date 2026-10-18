# 独立复核发现并修复的缺陷（2026-10-18 收尾）

> 来源：对九个包做的一次独立代码复核（执行 + 阅读并用），
> 外加我自己对着本仓纪律的复查。**已全部修复或明确记录**。

## 一、`pii`：历史门面成了策略的后门

`pii` 新增三面开关与处置策略（`Apply`/`ApplyValue`）后，旧的 `Redact`/`RedactValue`
仍在，且**只看全局开关、硬编码 `maskSpan`**——无视 `SurfaceEnabled()` 与 `Strategy()`。
而包注释把 `RedactValue` 称作「出口面单源调用」。于是宿主把策略配成 `block`
并关掉某个面之后调它，得到的仍是**静默打码**：既不阻断、也不看面。
两套并行实现 = 给处置策略留了一条绕过路径，正是本包自己声明要避免的 fail-open。

**修法**：收敛为唯一实现 `applySpans`，两条路径只是入口不同；
`Redact`/`RedactValue` 标记 `Deprecated`，写明拿不到 blocked 信号。
回归护栏 `pii/facade_test.go`。

## 二、`checkpoint`：`SeqGate` 没有回滚路径，且重启即失效

**（a）无回滚**：`Accept` 先记门、调用方再 `Store.Save`。落库失败时门已跑到存储前面，
同序号重试被判陈旧拒绝、跳过又留空洞——该 run 静默卡死。
**修法**：新增 `Release(runID, seq) bool`，仅当当前最大序号**恰好等于** `seq` 才回滚
（已有更新写入时拒绝回滚，否则等于让陈旧写重新获得通行权）。

**（b）重启即空**：闸的记录只在进程内存，而本包的立论就是崩溃恢复。
进程重启后闸是空的，上一进程的陈旧快照畅通写入——正是它要防的「恢复点静默回退」。
**修法**：新增 `Seed(runID, seq)`，宿主重启后用 `Store.Load` 的结果灌基线；
文档写明「不灌就等于放弃这道闸」。测试刻意断言了**失效行为**本身，
这样将来闸改成持久化时会有人知道这里曾经靠 Seed 补过。

## 三、`tooldisclose` × `toolfail`：同一个错误，两个包给出相反答案

`tooldisclose` 门禁的 `ErrNotLoaded` 自报 `retryable=true`，且**顺手激活了该工具**，
重试本会成功；`toolfail.Classify` 不认这个自报，回落 `ClassUnknown` → 不可重试。
而 `toolfail` 自称「上层最常用的入口，不需要知道错误体系长什么样」——
通用退避循环用它，会把可恢复失败判成永久失败，门禁那次激活白做。

**修法**：`toolfail` 新增 `RetryableReporter` 接口与 `ClassSelfReported` 类，
`Classify` 优先级：**显式 `*Failure` → 自报 → 哨兵 → unknown**。
自报 false 归 unknown（一个 bit 不够发明新类别，且 unknown 本就不可重试，两边自然一致）。

## 四、其余已修

| 位置 | 问题 | 修法 |
|---|---|---|
| `tooldisclose/gate.go` | typed-nil `*GateError` 让 `Retryable` 与 `errors.Is` 双双空指针 panic | 全族 nil 接收者加护栏；`Retryable() bool` 的接口签名也随之必须加 |
| `netguard` | `ExtraBlocked*` 是导出可变全局，`-race` 报 DATA RACE（生产上是**不可 recover 的致命错误**）；且与它自己批评的形态矛盾 | 改 `SetExtraBlocked*()` + `atomic.Pointer`，写侧永远替换新容器 |
| `netguard` | 扩展后缀未归一化：`.CORP` **静默永不命中**（SSRF 黑名单的 fail-open）；`corp` 无点反而命中 | 注入时归一化；**无点后缀在注入点大声拒绝**（全有或全无，半截列表更难推理） |
| `netguard/chain.go` | 跳数预算检查早于 start 校验，报出的跳号对应一个从未校验过的 URL | 先校验 start |
| `pii/policy.go` | `applySpans` 每次 span 都读一次 `Strategy()`，逻辑非原子——丢的正是 fail-closed 的 block 信号 | 入口快照一次并下传 |
| `pii` | nil 容器被改成非 nil 空值（JSON `null` → `[]`/`{}`，静默改写审计链载荷形状） | 四个 case 各自保留 nil |
| `budget/compact.go` | 零值 `Plan{}` 通过校验并产出幻影段——正是包注释自己写的「报成功却什么都没做更糟」 | 入口拒绝空计划 |
| `budget/window.go` | `used*100` 的 int64 溢出，把最严重压成最不严重 | 改除法比较 |
| `offload.State` | 无锁 map，而包注释让人以为可并发复用（`SeqGate`/`Meter`/`Catalog` 都加了锁，只有它没有） | 加 `sync.Mutex`，与全仓同一口径 |
| 若干注释 | 声称了实际不成立的不变量（`substringBonus` 的压制关系、`Segments()` 空值语义等） | 改成描述真实行为 |

## 五、仍未修、明确记录的不足

1. **模块改址后门禁规则出现新缺口**（**新发现，需拍板**）：
   本仓 module 已迁到 `github.com/yi-nology/agent-harness`，而规则三
   （`architecture-policy.yaml:260`）的 denylist 仍只写
   `git.enjoye.top/enjoydream/(bianque|ekit|agentkit)`。
   将来若 import `github.com/yi-nology/agentkit/...`，**门禁放行**——
   违反的是同一条红线。当前无此类 import（宿主 `bianque` 仍在旧路径，规则暂未失效），
   但缺口是真的。修法是一行：denylist 追加 `github\.com/yi-nology/(bianque|ekit|agentkit)`。
2. **`tooldisclose` 打分是子串包含，参考实现是词集合交集**：
   `art` 会命中 `partial`。加了词干还原后 `file` 会命中 `profile`——
   召回更高、精度更低。已由上限 5 兜底，未改匹配形态。
3. **`offload` 的 provider 消息块形态识别仍未做**（交给宿主）：内核不该认识各家消息结构。
4. **`SeqGate` 依赖宿主调用 `Seed`**：机制正确，但漏调就等于闸失效，属人的纪律而非机械保证。
