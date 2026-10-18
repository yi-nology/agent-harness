# 自查发现的两处真实缺陷（2026-10-18 收尾）

> 这两条不是对照 octop 发现的，是对着**本仓自己的纪律**发现的：
> 「判定语义归内核，处置归宿主」与「fail-closed 不得被绕过」。

## 一、`pii`：历史门面成了策略的后门

**症状**：`pii` 包在新增三面开关与处置策略枚举（`Apply` / `ApplyValue`）之后，
旧的 `Redact` / `RedactValue` **仍然存在**，且：

- 只检查全局开关 `Enabled()`，**从不检查 `SurfaceEnabled()`**；
- **硬编码 `maskSpan`**，完全无视 `Strategy()`。

而包注释把 `RedactValue` 称作「出口面单源调用」。于是：

> 宿主把策略配成 `block`（期望检出即中断）、或关掉某个敏感面之后，
> 调 `RedactValue` 得到的仍是**静默打码**——既不阻断、也不看面。

两套并行实现 = 给处置策略留了一条绕过路径。这正是本包自己在注释里
声明要避免的 fail-open。

**修法**：收敛为**唯一处置实现** `applySpans`，两条路径都走它——
`Apply`（按面）与 `Redact`（面无关的历史门面）只是入口不同。
`Redact` / `RedactValue` 同时标记 `Deprecated`，并写明**拿不到 blocked 信号**，
需要 fail-closed 的宿主必须改用 `Apply` / `ApplyValue`。

**回归护栏**：`pii/facade_test.go` 三条用例分别钉住
「门面必须跟随策略」「全部面关闭时门面必须透传」「结构下探能力不得因收敛而丢失」。

## 二、`checkpoint`：`SeqGate` 没有回滚路径，会把 run 卡死

**症状**：`SeqGate.Accept` 先把序号记进门，再由调用方去 `Store.Save`。
若落库失败，**门已经跑到实际存储前面**：重试同序号被判 `ErrStaleSeq` 拒绝，
跳过该序号又留下一条补不回来的空洞——这个 run 从此写不进这条快照。

后果不是「多花一个序号」，是**静默卡死**，且不报错。

**修法**：新增 `Release(runID, seq) bool`——仅当该 RunID 当前记录的最大序号
**恰好等于** `seq` 时才回滚。若期间已有更新的写入落地（`last > seq`），
返回 false 且不动状态：那时把门退回去反而会让陈旧写重新获得通行权。
同时把正确调用顺序写进 `Accept` 的文档注释（`Accept → Save → 失败则 Release`）。

**回归护栏**：`checkpoint/release_test.go` 四条用例，含
「回滚后同序号可重试」「已有更新写入时拒绝回滚」「seq=0 不影响状态」
「回滚不削弱单调性」。

## 三、本轮未能验证的部分（如实记录）

`delta` / `dispatch` / `eventbus` / `watchdog` / `ptc` 五个包在本沙箱内**跑不了测试**：
`github.com/oagentkit/runtime@v0.3.0` 的解压被标记为 `.partial`，
Go 的修复路径需要 `unlinkat` 重建目录，而沙箱全局拒绝 `unlink`/`rename`。
（该模块目录本身内容是完整的，13 个 `.go` 文件齐全，纯粹是标记状态挡住了。）

已尝试并全部失败的解法：默认缓存、仓外新缓存、`/tmp` 新缓存、
`curl` 直填 zip、`GOPROXY=off`、`GONOSUMDB`/`GONOSUMCHECK`。
`netguard` / `drain` 已在仓内跑通（92.9% / 100%），说明缓存大部分是好的。
