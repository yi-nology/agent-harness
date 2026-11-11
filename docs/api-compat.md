# API 兼容策略与生命周期分级

> 写给两拨人：**宿主开发者**——升级内核前想知道「哪些包敢随便升、哪些要读
> CHANGELOG」；**改本仓的 AI 代理与维护者**——想改一个包之前先知道「这个
> 级别允许我改什么」。
>
> 本文件是 `api-lifecycle.yaml` 清单与 `make apilifecheck` 门禁的**口径书**：
> 分级的语义在这里定义，清单登记事实，门禁保证两边不漂。
> 三者缺一不可——只有文档没有清单，分级半年后漂移；只有清单没有文档，
> 「什么算 breaking」没有裁决依据。

## 为什么要机器可判

本仓发版历史里已有多次 BREAKING：项目更名 `agent-harness` → `goagent-kernel`、
`ptc` 安全默认装配、`sideeffect` Store 接口两轮变更、`taskboard` 令牌铸造、
`checkpoint` 拒收零序号。每次都靠单篇 CHANGELOG 承担全部迁移说明——
**没有机器可判的机制**，意味着「文档里写了但代码里没改」没有任何信号。

机制面因此由三件东西组成，各管一段漂移：

| 件 | 管什么漂移 |
|---|---|
| 包注释标记（`// 生命周期: <级别>`） | 分级跟着包走（godoc、代码评审现场都看得见） |
| `api-lifecycle.yaml` 清单 | 全仓一张总表：定级 + 理由 + 定级版本，机器可读 |
| `cmd/apilifecheck`（`make apilifecheck`） | 清单 ↔ 包注释 ↔ 生产包集合 ↔ 错误登记表，四处一致才绿 |

## 分级判定规则

**breaking 的定义（四类，命中任何一类即 breaking）：**

1. **改签名**——已有导出函数/方法的参数、返回值、error 类型变化；
   接口新增方法也算（实现方被迫改代码；Stable 接口扩方法的唯一合法形态
   是「新方法 + 零值默认实现」，见 `contract` 包注释的既有承诺）；
2. **改语义**——签名不变但可观测行为变了：判定结论变化、错误类型变化、
   并发/时序保证变化、输入的合法域变化；
3. **改默认值**——零值语义、缺省配置、缺省超时/阈值变化（调用方一行不改
   却拿到不同行为的，全部算）；
4. **删导出符号**——删除或改名任何导出类型/函数/变量/常量，包括把导出
   符号降级为未导出。

**不是 breaking 的：** 新增导出符号、新增包、修 bug（行为向文档承诺回归）、
性能优化、内部实现重构（导出面与可观测语义不变）、未导出符号的任何变动。

### 三个级别的承诺

| 级别 | 承诺 | 判定 |
|---|---|---|
| `Experimental` | **不承诺任何兼容**。可随时改、删、拆。升级风险自负 | 上述四类都可能发生；发生时不视为违约，仍须进 CHANGELOG |
| `Stable` | **只允许 additive**。上述四类 breaking 一律不发生 | 违反即视为缺陷：要么回滚，要么走下面的破坏性变更流程先改级别声明 |
| `Deprecated` | 自身仍按 Stable 兼容，但已给出替代 | 删除只发生在 minor 版本边界；删除前 CHANGELOG 必须有 BREAKING 段 |

### 门禁判据（`make apilifecheck`）

1. **清单 ↔ 包注释**：每个登记包的包注释里恰好一行
   `// 生命周期: <级别>`（全角冒号亦可），级别与清单一致；
2. **覆盖**：清单覆盖**全部生产包**（每个含生产 .go 文件的目录；
   `cmd/` 下的命令除外——命令是交付产物不是 import 面）。漏登记与
   幽灵登记都红；
3. **Deprecated 必须给替代**：`replacement` 必须是完整符号路径
   （`github.com/yi-nology/goagent-kernel/<pkg>.<Symbol>`），且该导出符号
   真实存在——替代指向指到不存在的符号等于把调用方引向悬崖；
4. **门面跟随**：`follows_upstream` 声明的上游 module 必须真的出现在
   该包 import 面里；
5. **错误登记**：`docs/error-semantics.md` 的哨兵/结构化错误类型表与
   各包代码声明的集合双向相等（见该文档头注）。

新增导出符号的稳定性标注由**所在包的级别**承载：包是 Stable，新导出符号
自动落入同一承诺——这是刻意设计，逐符号打标会让标注面膨胀到没人维护。
符号级的弃用用标准 godoc 约定 `// Deprecated:`（staticcheck 会对使用点告警），
包级生命周期仍以本清单为准。

## 破坏性变更流程

Stable 包确需 breaking 时，按序走完全部五步，缺一步 `make apilifecheck`
或评审就该拦下：

1. **先改声明，后改代码**：`api-lifecycle.yaml` 里把级别改为
   `Deprecated`（若整包退役）或保持 `Stable` 并在 CHANGELOG 起草 BREAKING 段
   ——policy 先行是本仓对依赖方向的同一条纪律，对 API 面同样成立；
2. **Deprecated 过渡**：旧符号挂 `// Deprecated:` 注释指向替代符号，
   清单加 `replacement`（门禁会验符号存在）；至少保留一个 minor 版本；
3. **同批落库**：接口一改、实现与 sidetest/boardtest 套件同批改
   （v0.33.0 三处契约同批落库就是这一条的实例：「改接口」与「改实现」
   互为前提，只能一个提交，否则中间提交是红的）；
4. **CHANGELOG BREAKING 段**：动机、旧新对照、迁移路径三件齐；
5. **版本边界**：删除动作只发生在 minor 版本（v0.y 的 y +1），不发补丁版删 API。

v0.y 阶段（当前）的版本承诺：**patch 不 breaking**；minor 允许 breaking，
但必须走上述流程并显式在 CHANGELOG 标注。1.0 之后是否收紧到 SemVer
full promise，到时候按宿主面实际再定，这里不预先承诺。

## 清单格式

```yaml
version: 1
packages:
  - pkg: sideeffect          # 包目录（相对仓库根）
    level: Stable            # Experimental / Stable / Deprecated
    since: v0.33.0           # 定级版本
    reason: ...              # 定级理由，每条必填（没有理由的承诺无从复核）
    # follows_upstream: github.com/oagentkit/toolgate   # 门面包专用
    # replacement: github.com/yi-nology/goagent-kernel/newpkg.NewThing  # Deprecated 专用
```

包注释标记（与清单 level 一致，门禁判等）：

```go
// Package sideeffect 副作用「至多一次」的跨进程契约。
//
// 生命周期: Stable（v0.33.0 定级）——只允许 additive 变更（新增导出符号 /
// 新增方法且给零值默认实现 / 新增错误值）；改签名、改语义、改默认值、
// 删导出符号都算 breaking，判定规则与变更流程见 docs/api-compat.md，
// 逐包理由见 api-lifecycle.yaml。
```

## 当前定级一览

api-lifecycle.yaml 登记 **44** 个包（Stable 26 / Experimental 18 / Deprecated 0）。
逐包理由看清单 `reason` 字段；本节只说分组的**依据**：

- **核心契约面 Stable**：`contract` / `protocol` / `sideeffect` / `taskboard` /
  `checkpoint` / `commitgate` / `statemachine` / `sidetest`——宿主实现这些
  接口就是绑定承诺，它们的变更成本最高，所以最先进 Stable；
- **门面 Stable + 跟随上游**：`dispatch` / `policy` / `reminder` / `toolfail` /
  `sandbox`——调用面稳定，判据语义随上游 oagentkit/guardx、toolgate 演进
  （`follows_upstream` 登记，门禁验 import 面）；
- **零依赖小判据 Stable**：`toolname` / `pending` / `ticket` / `watchdog` /
  `statemachine`——小、冻结、无依赖，判据已写死；
- **Experimental**：两类——「明显还在演进」的（`ptc` 两轮 BREAKING 未远、
  `budget` 压实骨架刚落、`trajectory` 投影面随上游事件流扩、`egress`/
  `netguard` 出口强制件两端仍空、2026-11-06 批次的七个新包
  `sessionref`/`delegate`/`worktree`/`memorypipe`/`hookengine`/
  `skilldisclose`/`cmdpolicy`），以及**无对外承诺也不该有**的门禁与内部件
  （`archcheck` 判据面随 policy 演进、`internal/subhub` 被 Go 可见性挡住、
  `taskboard/boardtest` 与 `examples/reference-runtime` 是测试/示例）。

Experimental → Stable 的晋级条件：**一个 minor 版本内无 breaking**且
判定语义有配套契约套件或负对照的，下一版可晋级——晋级是显式动作
（改清单 + 改包注释 + CHANGELOG 一行），不是自动发生的。

## 相关文件

| 文件 | 角色 |
|---|---|
| `api-lifecycle.yaml` | 清单（声明 + 机器可读） |
| `cmd/apilifecheck` | 门禁执行体（`make apilifecheck`） |
| `docs/error-semantics.md` | 全仓错误哨兵语义总表（同门禁校验） |
| `docs/USAGE.md` | 宿主视角的接入指南（口径表含本清单登记数） |
| `CHANGELOG.md` | 每次破坏性变更的迁移说明（BREAKING 段） |
