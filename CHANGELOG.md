# agent-harness CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v0.10.0] — 2026-10-22

### 新增(guardset)：装配面对齐 netguard 的整份原子交换 —— 修两处静默失效

v0.9.0 的装配面只借了 netguard 的**名字**，没借它的**机制**。本轮把它对齐，
过程中发现第一版有两处**静默失效**（门禁绿、构建绿、测试绿，而机制已不成立）。
## 对齐了 netguard 的四处机制
| netguard | 本包 |
|---|---|
| `curPolicy atomic.Pointer[Policy]` | `current atomic.Pointer[assemblyState]` |
| 代次住在指针目标里（`Policy.gen`），`PolicyGeneration()` 是**投影** | 代次住在 `assemblyState.gen`，`Assembly.Generation()` 是**投影** |
| `swapPolicy(mutate)` 走 **CAS 循环** | `SwapFunc(mutate)` 走 **CAS 循环** |
| 订阅表 copy-on-write + 锁外通知 + 逐个 recover | 同款 |
各写一套必然各判一套，漂移到没人发现为止——故刻意沿用既有先例。
## 两处静默失效（已修）
1. **并发换分丢更新 ⇒ 撤销被撤销**。
   第一版 `Swap(next)` 收的是**预先构造好**的一份：两个并发撤销者（A 撤 g1、
   B 撤 g2）各自从同一份旧算出新的一份，后写者把先写者**静默盖掉**
   ⇒ **g1 悄悄复活**，没有任何东西会提示。
   修法：`SwapFunc(ctx, mutate)` 走 CAS 循环——mutate 从**观察到的**旧值算出下一份，
   CAS 失败则重来（mutate 重新看到最新那份）。
   并把 `Swap(ctx, next)` 的语义**显式**为「整份替换，不基于当前那一份」，
   两个口语义不同，用错就丢更新。
2. **通知期回调 panic 把已落地的换份说成失败**。
   换份**已经落地**（指针已换），此时让某个订阅者/留痕回调抛异常，
   就把整次变更说成失败——把已经发生的事实改写成没发生。
   修法：逐个 recover（与 netguard 的 `callSubscriber` 同款）。
## 新增能力
- **代次**：`Assembly.Generation()`（当前份的**投影**，单调不回卷）、
  `Snapshot()`（集合与代次**一次读出**，分开读会得到自相矛盾的一对）、
  `Outcome.Generation`（审计能回答「这次判定是按第几代守卫做的」）。
- **订阅**：`Subscribe`/`Unsubscribe`——换份真正落地的那一刻把新记录推给订阅者
  （当场生效的「换成了什么」），运行期可增删。
## 拆分（按职责，不是为凑行数）
`assembly.go` 598 行超了 `max_file_lines: 400` ⇒ 拆成三个关注点：
`assembly.go`（装配面本体）/ `assembly_subscribe.go`（订阅与通知）/
`without.go`（撤销一条的纯函数）；求值与接链并入 `stage.go`
（它与 `Stage(*Set)` 是同一件事：把守卫接成判定环上的一环）。
## 反向验证
12 条变异全部打红（代次两个真相 / CAS 退化成 Store / 通知不 recover /
不通知订阅者 / 退订不删 / 判定不填代次 / 原地覆盖旧份 / 允许 nil / 不跑闸门 /
`Without` 三条）。并发用例一律**确定性构造**（通道握手 + mutate 内嵌嵌套换分
制造 CAS 失败），不靠时序去撞；`-race` 亦通过。
## 对下游
新增 API，**无破坏性变更**：`Swap`/`WithSwapGate`/`WithSwapRecorder` 签名不变，
`SwapRecord`/`Outcome` 只**新增**字段；`Set` 仍只读，`guardset.Stage(set)` 仍可用。
唯一要留意：按 v0.9.0 示例用 `Swap(next)` 做读改写的，请改用 `SwapFunc`。

## [v0.9.0] — 2026-10-22

### 新增(guardset)：装配面 Assembly —— 撤销的归属明确化（换整份，不改其中一条）

`guardset.Set` 构造后只读是它的**不变量**（并发共享下的可变集合就是竞态源，
且会让「当前有哪些守卫」出现两个真相）。但只读的代价是真实的：运行期想临时
停掉某条守卫（应急降级、灰度、误伤止损），宿主只能**整个进程重启**。
⇒ 解法不是「让 Set 可变」，而是新增**装配面**持有「当前这一份」，撤销 = **换整份**。
## 做了什么
- `Assembly`：持有当前份 + `Swap` 整份原子换（`atomic.Pointer`）+ `Current` 读当前份。
  判定路径不加锁；在途判定跑在**旧份**上，下一次跑在新份上，没有中间态。
- `Assembly.Stage` / `StageWithGuidance`：链上那一环指向**装配面**而非某一份
  ⇒ 换份后**不用重接链**，撤销从「改代码 + 重启」变成一条机械路径。
- `Set.Without(names...)`：**纯函数**，产出少了指定守卫的**新**集合，原集合不动。
- `WithSwapGate`：换份闸——「谁在什么条件下可以换整份」的**接入点**。
  授权是宿主的业务决定，本包不替宿主判，只提供可判定的接入点。
- `WithSwapRecorder` + `SwapRecord`：换份留痕（换了几次 / 换走了什么 / 换上了什么）。
- `ErrUnknownGuard`：撤销**不存在**的守卫要报错，不静默忽略。
## 两条边界一起守住
- **「全部撤销」不是降级，是失守**：换上去的那份永远非空。空集合在 `New` 就被拒绝，
  `Without` 去掉最后一条返回 `ErrEmptySet`。降级可以撤一条，不能撤到一条不剩。
- **旧份不被修改**：它可能正被在途判定读着，也可能是留痕/审计的「上一份」。
## 反向验证
7 条单变异 + 1 条**组合**变异全部打红。其中组合变异值得单记：
「换份原地覆盖旧份」与「求值时重读集合内容」**各自单独都不撕裂**
（Go 的 `for _, g := range s.guards` 在循环开始就固定了切片头，
所以原地改旧份也影响不到在途判定），**两者同时出现才真的撕裂**
⇒ 有些不变式无法用单条变异表达，必须成对组合才能证伪。
## 对下游
新增 API，**无破坏性变更**：不接 `Assembly` 的宿主行为逐字不变。
`Set` 仍是只读的；`guardset.Stage(set)` 仍然可用（它绑死某一份）。

### 新增(archcheck)：import 路径级精确例外 import_exempt —— 修 issue #15

宿主有一条不变式「记忆内核的依赖只经 internal/memstore 进入」，业务层要用到
内核的某个具体子包时，宿主只有三条路：放宽整条规则（静默放宽不变式）、
改成逐包白名单（上游新增包即静默放行）、或在宿主侧自写一层转译。
第三条可行，但代价是同一个上游能力要在宿主侧再写一遍转译（类型不许外泄、
等价性自己核）——而这层转译本可以由上游的一个 import 例外免掉。
## 做了什么
- `Rule` 新增 `ImportExempt []string`（yaml: import_exempt）：import 路径级的
  **精确**例外。
- 判据：整条路径相等，**不是前缀、不是通配**。豁免 a/b 不连带放行 a/b/... ⇒
  上游将来在 a/b 下新增的包**不会静默放行**（一条被静默放宽的声明等于没有声明）。
- 刻意**不做前缀归一**：带尾斜杠的项不匹配任何具体 import 路径 ⇒ 写错的人看到
  「豁免没生效」，而不是误以为整棵树放行了。
- policy 自校验新增「豁免项形状」判定（空串 / 带尾斜杠 / 含通配或正则字符）：
  这三类**在任何时刻都不可能匹配**。不判的后果是**误诊**——宿主看到 import 被拒，
  却不知道是自己登记的那条豁免没生效。
  判「形状」而不是判「当前有没有命中」：后者分不清「写错」与「将来才用」，
  硬判就是误报，而误报会让人学会忽略告警。
- `docs/USAGE.md` 新增 §4.3 写给宿主的用法。
## 反向验证（坏实现必须会红）
三条语义变异各打中 2~4 条用例：
- 豁免不被判据认（声明与执行分离）→ 4 条红
- 前缀匹配（子树被连带放行）→ 2 条红
- 子串匹配（相近路径被放行）→ 2 条红
自校验两个方向成对验证：判据失效（该报的不报）→ 3 条红；判据过度（不该报的
也报）→ 「合法豁免不报」那条红。**只验「开着绿」等于没钉住。**
## 顺带清偿（散文漂移）
README「工程底座」与两份文档的计数已按实测重算：生产代码 14,806→16,974 行、
84→102 文件、测试 111→126 个、覆盖率 86.1%→84.9%、依赖方向 28→29 包；
README / USAGE 的规则表补齐到 8 条规则 + 3 条结构规则 + 5 个门包。
这些数字**不在 docsync 的判定面内**（docsync 对包表行数也是相对容差判定），
所以只能手算——本次一次校准里 6 个包的行数都偏了。
## 对下游
新增**可选**字段：policy 不写 `import_exempt` 即按旧行为逐字不变。

### 新增(policy)：门面补上 Stage 转发 —— 审批门接进判定环的唯一入口

## 缺口
审计门（`policy`）有两个接入口：
- `WithAuditGate`——把**单个工具**包起来（eino 的 InvokableTool 形态）；
- `Stage`——把门接成**判定环的一环**（invoke 链的形态）。
本仓门面原先**只转发了前者**。而后者恰恰是「一处装配、处处生效」的形态：
工具调用、程序内调用（`ptc` 的 `host.<tool>()`）、子进程侧调用全部走同一条
invoke 链 ⇒ 审批规则**只装配一次**。
缺了它的后果：宿主想接审批进判定链只能去 import 上游 toolgate ——
那就绕过了本仓门面，**「调用面逐字兼容」的承诺断在这里**。
顺带查实：`ptc` 的判定环（`invoke.EvaluateStages`）里**完全不含审批**
（生产代码 grep `policy/approval` 零命中）⇒ 程序内调用这条路径上，
审批要么宿主自己装进 Stages，要么就没人管。
## 改动
- `policy.Stage(g, op, onDecision) invoke.Stage`：单条上游转发（门包纪律允许的
  四种声明之一）。
- policy 的 `facades` 段补 `allowed_imports: guardx/invoke`（签名所需，非实现）；
  `modules` 段补 `requires: ext:github.com/oagentkit/guardx`（跨仓依赖声明面）。
- 用例钉的是**接线契约**（与本包既有风格一致）：转发能用、装上后**真的参与判定**、
  没装门时**不拦下**但留痕可查。
## USAGE.md 补两节
- §3.3.4 审批门接进判定环（`policy.Stage`）——两个接入口的对照与接法。
- §3.11 审批 × 档位的联合判据（`gatecheck`）——原 §3.11「门面三件」顺延为 §3.12。
  补这一节是因为 §3.3.4 里引用了它，**指向不存在章节的死引用**是文档缺陷。
## 反向验证
两条变异都被抓住：传给上游的 Op 被抹掉 / 丢弃 onDecision（审计留痕断了）。
> 第二条一开始**没抓住**：我只钉了「放行/拒绝」，而 Op 被抹掉后结论**仍是拒绝**
> （上游对未知操作类型 fail-safe 升人审）——差异只在**按哪条规则判**。
> 补了「裁决的 RuleID」断言才抓住。
> ⇒ 这已是本轮第三次「断言点不在差异上」——**同一个坑栽三次**，
> 说明它值得单独立条纪律（见下）。
## 顺带记一条纪律
**用例的断言点必须打在差异上**：若所有输入都得到同一结论，那条用例就
**观察不到任何东西**——绿着不是因为对，是因为没测到。
判据：**「如果被测行为坏掉，这条会红吗？」** 若所有输入结果相同，它就测不到任何东西。
构造法：**造一个只有被测行为不同才有差异的场景**（如：让规则只对特定 op 类型生效）。

### 新增(gatecheck)：接线件 ExecuteAndRun + 受管后端 Managed —— 判据与执行不可分

## 起因：自省发现上一版的 gatecheck 是个「没人用的包」
上一版只提供了 `Check`（判定），把后端放进 `Decision.Provider` 却不执行。
这有两处问题：
1. **没人接线** —— 宿主拿那个 provider 自己去 `Exec`，而本仓 `sandbox.Execute`
   在仓内**无人调用**（它只是门面的一个转发），所以「档位判定」在真实链路上
   根本没发生。
2. **判据与执行分离** —— 中间任何一步走错（拿别的 provider、重新 Resolve 一次、
   抄错字段），就产生「**判过的后端 ≠ 实际跑的后端**」的落差。
   而「声明与实际之间没有信号」正是本包要消灭的东西。
## 补了两件
1. **`ExecuteAndRun`**：判定通过就**就地执行**（用判出来的那一个 provider），
   调用方没有「换一个后端跑」的机会。审批判 deny / need_human / plan_only 时
   **根本不执行**——尤其 deny：执行了再判「已拒绝」没有意义（副作用已发生）。
2. **`Managed`**（新文件）：**受管后端**——把真后端包起来，判据在 `Exec` 内部、
   紧挨着执行，**不可绕过**。宿主把 `Managed` 注册进登记簿即可。
   另配 `Guard`（审批门 + 登记簿的组合件），把「装配形态」固定在一处，
   免得各宿主各接一遍导致**合成规则的次序漂移**。
`Managed` 的档位**转发不了**内层后端（那是运行期才知道的值，签字无从签起），
故对外声明它保证的**隔离强度下限**（进程级）；实际跑哪个档位的内层后端
由 `Check` 在运行期用登记簿判。
## 门禁的意外收获：档位登记门禁当场拦住了我
新增 `Managed`（它实现了 `sandbox.Provider`）立刻触发
`harness-sandbox-provider-declared`——**「档位声明要有人签字」那条纪律在起作用**：
新后端必须去 policy 登记。
登记时又逼出一个真问题：`Level()` 若写 `return ManagedLevel`（标识符），
门禁静态读到的是 "ManagedLevel"，与 policy 登记的 "LevelProcess" 对不上
⇒ 报「档位漂移」。⇒ **门禁要静态可比，代码就必须写成可静态可比的样子**
（`return sandbox.LevelProcess`）。
## 反向验证与两条新纪律
新增用例的反向验证里，有两次变异打不中，根因**不是实现问题，是断言点不对**：
用例（如「Op 映射有没有被用上」）若所有输入都得到同一结果，就**观察不到任何东西**——
绿着不是因为对，是因为没测到。修法是构造一个只有被测行为不同才有差异的场景。
AGENTS.md 相应补了三条：
- **用例的断言点必须打在差异上**；
- **判据与执行必须不可分**；
- **门禁要静态可比 ⇒ 代码必须写成可静态可比的样子**。
## 顺带
测试文件按关注点拆成三份（判据 / 执行入口 / 受管后端）+ 一份共用夹具，
共享夹具单放一处（抄三份会漂移——漂移的夹具比没有夹具更坏）。

### 新增(gatecheck)：审批层与沙箱层的联合判据 + 与主流 harness 的逐项实测对齐

## 一、差距分析（docs/GAP-ANALYSIS-20261021.md）
**实测对齐**了 Codex / DeepSeek Harness / Octop / Coze Studio，结论：
**差距不在单个能力，在两个维度。**
| 维度 | 主流 | 本仓 | 该不该补 |
|---|---|---|---|
| A. 装配的运行时性 | DSH：能力可从**配置层**替换（插件树） | 守卫集合**构造后只读** | ❌ 上游/宿主的事 |
| B. 声明的可撤销性 | DSH：插件可**声明权限、限定边界、记录轨迹、随时撤销** | ①③有、**②④缺** | ✅ 本仓该补 |
| C. 平台完整度 | Coze：工作流 UI / 知识库 / 多租户 | 无（内核定位） | ❌ 宿主的事 |
| D. 端到端执行体 | 各家都有完整 Agent Loop | 只有机制面 | ❌ 宿主的事 |
文档里每条「现状」都带出处且实测过（Codex 的两层安全模型出自其公开文档；
DSH 的 Cordis 插件树出自 deepseekharness.online 的架构文档），
**不转述二手材料**——那份对比材料自己标着「需具体验证」。
## 二、落地：`gatecheck` 包（补 B 的核心缺口）
核实上游后确认：`guardx/sandbox`（档位×预算的 `Registry.Resolve`）与
`toolgate/policy`（审批裁决）**各自完整但互不知情**——
`toolgate/policy` 不 import `guardx/sandbox`，反之亦然。
于是「两层是否都同意」这个判定**没有单点**，落在各宿主手里必然漂移。
本包只做**合成**，不重新实现任何一层（重新实现 = 与上游并存第二份真相）：
    审批已否        ⇒ 拒绝（优先于一切）
    需人审          ⇒ 需人审（**优先于档位不足**）
    计划模式        ⇒ 只审不执行（单列，≠ 拒绝）
    放行 + 选不到后端 ⇒ 拒绝（档位不足）
    放行 + 选得到    ⇒ 放行
**「需人审」优先于「档位不足」是关键**：两条拒绝对宿主处置完全不同——
档位不足该**换环境**，需人审该**去问人**。先报哪条决定宿主去找谁；
先报档位不足 ⇒ 宿主不找人 ⇒ 这次调用既没执行也没人管，**静默丢失**。
**两处 fail-closed**：没装审批门 ⇒ 按需人审；要求了档位却没有登记簿 ⇒ 拒绝。
**反向验证**：四条变异全部打中并变红（deny 优先级反转 / 需人审优先级反转 /
无登记簿也放行 / 没装审批门也放行）。
> 变异过程踩到一个坑：第一次的「deny 优先级反转」**根本没反转成功**
> （分支仍插在沙箱层之前），用例于是绿着——看着像「用例没牙齿」，
> 实际是**变异自己没改到点子上**。已写进 AGENTS.md：
> **变异不生效时先怀疑变异，再怀疑用例。**
## 三、AGENTS.md 新增一条纪律
「合成判定时，**谁先谁后本身就是机制**」——次序写反的后果表，
以及「计划模式要单列」与上面那个变异坑。

### 新增(ptc)：内存兜底改用 cgroup v2 + 执行域异常终止的死因分类

## 为什么换掉 rlimit（实测结论，不是偏好）
原实现用 RLIMIT_AS 兑现 MaxMemory。**它限的是虚拟地址空间**，而 Go runtime 的
虚拟 arena 预留远超任何有意义的内存帽（Linux 7.0 / Ubuntu 26.04 实测）：
	RLIMIT_AS=256MB → fatal error: runtime: out of memory（runtime 自己起不来）
	RLIMIT_AS=1GB   → 同上
	RLIMIT_AS=4GB   → runtime 起来了，但 4GB 里已有 2.2GB 是虚拟预留
	RLIMIT_DATA=1GB → fatal error: out of memory allocating heap arena metadata
即：上限必须设到 GB 量级才起得来，而那时虚拟预留已吃掉大半——作为兜底它
**既兜不住**（大数才生效）又**会把 runtime 一起掐死**（小数直接 fatal）。
两条路都错。
cgroup v2 的 memory.max 限的是**物理内存**，不管虚拟预留：runtime 的虚拟
arena 不落地 ⇒ 不占配额 ⇒ 起得来；程序真去拿物理内存 ⇒ 内核 OOM-killer
杀它（SIGKILL）⇒ 父侧从管道 EOF 认出「执行域异常终止」。
非 Linux 一律不支持（macOS 无 cgroup；seatbelt 裁能力面不裁内存，
且实测 `Setrlimit(RLIMIT_AS, 256MB)` 在 Darwin 上直接返回 invalid argument）。
故删除 rlimit_unix.go / rlimit_other.go——新方案（linux / !linux）完整覆盖
旧方案（unix / !unix），能力不丢，且 Linux 上从「错的做法」换成实测正确的。
## 顺带补上的三处
1. **执行域异常终止的结构化分类**（ExecutionAborted）：「被内核杀掉」是
   内存兜底**生效了**（预期行为），「自己非零退出」是**启动失败**（故障）。
   合成一句话的话，宿主只能一律当故障处理——兜底生效这种正常结局被误报，
   真正该修的启动失败被淹没。死因在 stderr 尾部（`fatal error: runtime:
   out of memory`），故接住它。
2. **收尾顺序：先取状态，再收刀**。我们自己 Kill 的子进程与被内核 OOM-killer
   杀的**都是**信号终止；只有子进程**自己先结束**时取到的才是真死因。
   而 startErr 非 nil（管道 EOF）恰恰说明子进程已自己结束 ⇒ 此时取到的状态
   就是真死因。
3. **Supports 的判据从「配没配上限」改成「当前环境兜不兜得住」**（探测，
   不是按平台猜——同一台 Linux 机器换个容器跑结论就不同）。原先 macOS 上
   配了上限就报支持，然后 Exec 里因建不出 cgroup 而失败，宿主等到的却是
   「选了它却用不了」这种最难排查的形态。
## fail-closed：以为兜住了、实际没有 = 不可能发生
建不出带 memory.max 的 cgroup ⇒ **拒绝执行**；attach 失败 ⇒ **杀掉子进程
并回收**，不放它裸跑（它会带着「以为自己受约束」的前提跑不受约束的程序）。
## 死因分类与 fail-closed 的反向验证（各 4 条，全部打中并变红）
- 恒判非信号终止 / 恒判信号终止（分不清兜底生效 vs 启动失败）
- Unwrap 不通到底层错误 / 错误消息不带 stderr 尾部（死因丢失）
- 建不出 cgroup 也继续跑（fail open）/ attach 失败也放子进程裸跑
- 执行完不回收 cgroup / 不探测当前环境能力
「没配上限也报支持」这条在**兜不住的环境上不可观测**（两个返回 false 的
分支在结果上无法区分）——已在用例注释里写明，其区分力依赖 Linux + 可写
cgroup 的环境（CI 的 ubuntu-24.04）。
## 平台约束
内存兜底靠 cgroup v2，故「真兜底」那条端到端用例只在能建出 cgroup 的环境
真跑；macOS / 容器里跳过并**写明跳过理由**（不让恒跳过的用例看起来像
「测过了」）。CI 跑 ubuntu-24.04（cgroup v2 默认开启），故它在 CI 上真跑。

### 修复(archcheck)：god object 聚合键保留泛型实参 —— 修 issue #14 的假红

## 问题
god object 判据按「包路径 + 接收者**基类型**名」聚合，泛型实参被剥掉：
`Manager[int]` 与 `Manager[string]` 归到同一个 `Manager`。
但按 Go 规范，**按实例特化的方法只属于该实例化**
（`func (m *Manager[int]) C()` 是 `Manager[int]` 的方法，不是 `Manager` 的）。
于是三个不同的类型被并成一个计数：两个实例化各挂 20 个特化方法 ⇒
判成「Manager 有 40 个方法」，而它其实不胖。**方向是虚高 ⇒ 假红**
（与 issue #13 同类：同一个门禁上还有一处没清）。
## 改法
聚合键从「基类型名」改为「**完整接收者类型**」：
| 接收者 | 键 |
|---|---|
| `*Plain` | `pa.Plain`（**逐字不变**）|
| `*Manager[int]` | `pa.Manager[int]` |
| `*Pair[string, int]` | `pa.Pair[string, int]` |
**非泛型类型的键逐字不变**——这条必须成立，否则改动会波及所有宿主，
而不只是泛型用户。宿主仓实测**无泛型接收者**（grep 零命中，阈值 60）⇒ 零影响。
报告里因此出现方括号（`pa.Manager[int]`）——这是**刻意的**：
一眼看出是哪个实例化胖，而不是被并成一个假象。
## 顺带收敛一处重复
`baseTypeName`（剥实参）与 `fullTypeName`（保留实参）原本是两套独立 switch，
形态一多就漂移。改为「先取完整名，再剥方括号及其内容」——**一条真相**。
## 反向验证（三条变异全部打中并变红）
| 变异 | 结果 |
|---|---|
| 退回旧语义（并成一条） | `TestRecvTypeNameForms` 红 ✓ |
| 键里丢掉实参 | `TestRecvTypeNameForms` 红 ✓ |
| 多实参只留第一个 | `TestRecvTypeNameForms` 红 ✓ |
> 第三条一开始**没抓住**：fixture 里只有单实参的实例化。补了
> `Pair[string,int]` / `Pair[int,string]` 两个实例化才打中——
> 又是「断言点不在差异上」那个坑（本次第四次）。
另加 `TestGenericInstanceStillDetectedAsGodObject`：一个实例化挂 30 个实质方法
⇒ 必须被点名且方法数为 30（确保新键形态**没让门禁对真 god object 失明**——
那是「门禁变绿」最贵的形态）。
## 顺带
测试文件按关注点拆开（godobject_test.go 423 行超 400 阈值）：
`godobject_type_test.go`（接收者类型识别）/ `godobject_generic_test.go`（真 god object 反验）。
README 补第 7 条口径说明（含 policy 写法表之后）。

### 修复(ptc)：子进程不回结果时父侧永久挂死 —— 加期限 + 收刀 + 指向真因的错误

## 挖出来的真缺陷（生产级挂死）
上游 `guardx/wire` 的 `Starter.Start(ctx, ...)` **收了 ctx 却从不用它**：
内部是 `ReadFrame(r, &res)`——**无超时的阻塞读**。
于是「子进程不回结果也不退出」这一档 ⇒ 父侧**永久阻塞**，`ctx` 取消**无效**。
而那一档不是假想：**宿主忘了在 `main` 里调 `ptc.MaybeRunChild()`**，
子进程就会跑起**宿主的完整 main**——它不退出（正常启动服务）、
也不回 Result 帧。症状是「执行卡住」，真实原因（少加了一行）完全看不出来。
实测复现：父侧挂死 20s 仍在等（用例超时）。
## 修法：在调用侧自己加期限（不改上游语义）
- `startWithDeadline`（新文件）：取 `ctx` 的 deadline 与墙钟预算中**较早**者，
  到期即放弃等待并报错。
- `waitChildNonBlocking`（新文件）：**非阻塞**试取子进程状态——
  拿到就是真死因；拿不到说明它**还活着** ⇒ 先收刀再取。
  （只给「等结果」加期限而收尾无条件 `Wait()` ⇒ **挂死从一处挪到另一处**。）
- 错误信息**直接指向真因**：
  「子进程未回报结果且未结束（宿主是否忘了在 main 里调 ptc.MaybeRunChild()？）」
## 反向验证：三条放弃路径必须互不遮蔽
起初 ctx 取消那条与墙钟那条**期限接近**（5s vs 2s），去掉任意一条另一条仍兜住
⇒ 两条都测不出问题。故补用例把期限**拉开**：
| 放弃路径 | 钉住它的用例 |
|---|---|
| 墙钟期限（ctx 无 deadline，Timeout=2s） | `TestSubprocessTimesOutWithoutCtxDeadline` |
| ctx 显式取消（cancel() 后 100ms，Timeout=30s） | `TestSubprocessHonorsCtxCancellation` |
| 收尾直接 `Wait()`（挂死挪到另一处） | `TestSubprocessDoesNotHangWhenChildNeverReports` |
三条分别去掉对应分支 → 各自变红。
## 顺带的结构整理（门禁要求）
- 两处裸 `go func` 改走 `async.GoSafe`（门禁 `harness-no-bare-goroutine`：
  裸 goroutine 的 panic 杀宿主进程）。
- `subprocess.go` 441 行超 400 阈值 → 按**关注点**拆出：
  `childpipes.go`（怎么把管道接起来）、`childwait.go`（怎么取退出状态）、
  `startdeadline.go`（怎么等结果）。拆分后各文件都在阈值内。
## 文档
- README 的 ptc 行：原来写着「用 RLIMIT_AS 兑现 MaxMemory」——**这句已过时**
  （实测否掉、已改 cgroup v2），一并更正；行数与依赖列同步。
- 设计档 §九 第 2 条：re-exec 的失败形态已查清并兜住——
  接入陷阱从「静默挂死」变成「一条能照着改的错误信息」；
  但「re-exec 还是随包发小二进制」这个**选择本身仍待定**（本包不替宿主决定）。

### 修复(probe)：探测 workflow 补 checkout（setup-go 要读 go.mod）

- a68f620

- 杂务(deps)：依赖地板追平矩阵本轮新版本（5 块）

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是**隐式破坏性变更**：不跑就报
`updates to go.mod needed`，且报错不提真正原因（**不是本仓的 API 变了**）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪 5 块、为什么无行为差异
```
oagentkit/agent    v0.10.2 → v0.10.4    oagentkit/toolgate  v0.12.1 → v0.12.4
oagentkit/llm      v0.15.0 → v0.15.2    oagentkit/mcpkit    v0.8.4  → v0.8.5（间接）
oagentkit/obs      v0.9.4  → v0.9.5
```
**这 5 个上游版本全部是「纯依赖地板追平」——各仓自身代码零改动**：
- agent v0.10.3/v0.10.4、llm v0.15.1/v0.15.2、obs v0.9.5、toolgate v0.12.4、
  mcpkit v0.8.5：都只抬了 `oekit/pkg` / `oekit/concurrency` / `guardx` / `llm` /
  `textx` / `runtime` / `mcpkit` 这些矩阵内或基座的地板，没有一行业务代码变更。
- 其中 **llm v0.15.1 与 v0.15.2 是「只修发布说明」的一版**（补回浅克隆期间漏掉的
  CHANGELOG 条目 + 抬 textx 地板），代码零变更。
## 门禁
```
make check        ✅ 全绿（lint 0 issues / 28 包测试过 / 覆盖率 84.4% / 架构门禁通过）
```
> 注：门禁在**独立 worktree** 上跑的——主工作区里有并行工作线**在途未提交**的
> `ptc/` 改动与设计档改动，那些不是本次改动的一部分。
> 本次提交只含 `go.mod` / `go.sum` / `tools.go` / `README.md`。

- 杂务(ci)：删掉临时探测 workflow

探测结论已拿到：ubuntu-24.04 runner 上 memory 控制器虽可见，但**建不出子 cgroup**
（统一层级根与进程自己的 cgroup 之下都是 Permission denied）⇒ 内存兜底那条
端到端用例在 CI 上是 **SKIP** 而非真跑。
这意味着「真兜底」的验收在 CI 上不成立，需要另找可验证的环境。


- CI(probe)：临时探测 CI 环境的 cgroup v2 能力

用于确认 ubuntu-24.04 runner 上内存兜底那条端到端用例是**真跑**还是**跳过**——
CI 绿灯的原因必须区分，否则「真兜底」的核心验收等于没验。
验证完即删。


- 文档(readme)：下游升级须知补 v0.9.0 —— 预写的 v0.8.5 行随批次变大而失效

README 的「下游升级须知」里原有一行 **v0.8.5**，是为「依赖地板追平」那次提交
预写的（它当时确实是本批的第一件事）。但那一版**从未发布**——后续又落了两个新包
（`gatecheck`）与三处新 API/修复，批次内容已远超一次 patch。
按本仓惯例（新包 → minor，如 v0.8.0 因 `retryplan`），本批应为 **v0.9.0**。
故把那行改写为 v0.9.0，并按发版纪律写清「下游要做什么」：
- `go mod tidy`（地板抬高是隐式破坏性变更，不跑就报 `updates to go.mod needed`，
  且报错不提真正原因）；
- 其余**全是新增 API，无接口变更**——不接 `gatecheck` / `guardset.Assembly` /
  `policy.Stage` / `archcheck` 的 `import_exempt` 的宿主代码逐字不变
  （policy 不写新字段即按旧行为生效）；
- **一处行为变化需知**：`ptc` 现在尊重 ctx deadline——此前父侧可能在子进程
  不回结果时永久挂死，现在到点收刀并报错。
另把「泛型实例化各自成条」的版本注记从 v0.8.5 改为 v0.9.0（该修正随本批发布）。

- 文档(agents)：补「结论相同不等于行为相同」——断言点要钉在差异上

本轮连续三次反向验证打不中，根因是同一个：断言点不在差异上。
最值得记的是第三例——Op 被抹掉后调用**照样被拒**（看起来没坏），
但走的是 fail-safe 而非规则。这类漂移只钉「放行/拒绝」抓不到，
得钉「按哪条规则判的」（Decision.RuleID）。
修法优先级：造可观察差异 → 换更细的断言点 → 都做不到则如实写明
「该行为在本环境不可观测」，不要硬凑用例。

- 文档(usage)：补 sideeffect / sidetest / retryplan 三包的宿主接入指引

这三个包随 v0.8.0 已发布，但 USAGE.md（宿主接入指南）里**完全没有提到它们**——
宿主的「你会拿到什么」表里也没有对应行。也就是说：包发出去了，宿主无从下手。
新增 3.10 节（插在安全面之后、门面三件之前），按「一条链上的三段」组织：
- **3.10.1 `retryplan`**：三档路径（不重试 / 原样重发 / 走幂等键重放）的由来，
  以及关键取舍「值得重试但不确定能不能重发 + 没有幂等键兜底 = 不能重试」
  （宁可如实失败交上层降级/人工，不赌一次重复副作用）。
- **3.10.2 `sideeffect`**：`ExecuteWith` 三段式（准备 → 资格检查 → 施加）、
  幂等键的正确用法（同一逻辑动作的所有重试必须用同一个 Key）、
  下游 `FencedSink` 要钉的两件事（比较口径用「小于」、比较与写入须原子），
  以及承诺边界「至多一次而非 exactly-once」。
- **3.10.3 `sidetest`**：宿主怎么接套件、为什么实现必须支持注入时钟
  （否则过期场景恒不触发，套件退化成空断言）、为什么本仓不给参考实现。
- **3.10.4 接线范式**：`retryplan` 决策 → `sideeffect` 执行的实际代码，
  并点明核心不变量——走 PathReplayed 时**必须用同一个 act.Key**，
  换了 Key 就是两个不同的动作，去重失效。
「你会拿到什么」表补两行（副作用怎么保证只做一次 / 存储实现怎么验收）。
**示例代码已实际编译验证**：把两段示例放进临时包跑 go build + go vet，
确认 API 签名与类型都对（文档里的示例编译不过是最贵的文档缺陷）。
门禁：在独立 worktree 上跑 make check 全绿（主工作区有并行工作线在途未
接线的 ptc/* 新文件，lint 会因 unused 报红，与本次改动无关）。


- 测试(ptc)：挂死用例支持预编译子进程 —— 让它能在真 Linux 上复验

上一版的挂死修复只在**本机**验过（macOS）。而本机与 CI 都不该被当成
「已验证」——本轮已在真实 Linux 上跑通（能建出子 cgroup 的那台），
这条要能复现。
## 改动
- `cmd/ptcchild` 增一个形态：`AGENT_HARNESS_PTC_CHILD_MODE=noop`
  ⇒ 读掉 stdin 后干等，**不回 Result 帧**——这正是「宿主忘了调
  `MaybeRunChild()`」的真实形态（子进程跑起宿主完整 main：不退出、不回帧）。
- 测试侧新增 `AGENT_HARNESS_PTC_NOOP_BIN` 环境变量指向预编译的 noop 二进制；
  用它时**同时**设 `AGENT_HARNESS_PTC_CHILD_MODE=noop`
  （形态由运行时环境变量决定，子进程继承父进程环境）。
## 为什么必须是两个环境变量
`AGENT_HARNESS_PTC_CHILD_BIN` 指向的是**正常子进程**（会回 Result），
`AGENT_HARNESS_PTC_NOOP_BIN` 指向的是**不回结果**的那个。
混用一个 ⇒ 装错时表现为「测试莫名其妙地通过了」（实测踩过：
Linux 上 0.01s 就 PASS，因为 noop 二进制其实走了正常形态）。
## 真实 Linux 上的复验结果
    TestSubprocessDoesNotHangWhenChildNeverReports  PASS (5.05s)
    TestSubprocessHonorsCtxCancellation              PASS (151ms)
    TestSubprocessTimesOutWithoutCtxDeadline         PASS (2.05s)
    TestSubprocessMemoryLimitKillsChild              PASS (4.61s)
三条收场路径的耗时分别对应 ctx 取消（151ms）、墙钟预算（2.05s）、
ctx deadline（5.05s）——**与设计一致**。

- 测试(ptc)：实测管道往返延迟 + 查清跨域执行与 sideeffect 的关系

设计档 §九 的两条「待办/未定」本轮清掉。
## 一、管道往返延迟：从「待实测」到「已实测」
初稿猜「百纳秒到几十微秒」，**实测 174µs**——比猜测慢约一个数量级。
但结论比初稿更有意思：
| 口径 | 数值 |
|---|---|
| 子进程启动成本（一次性） | ≈ 13.4 ms |
| 子进程内每次调用的**边际**成本（差分法） | ≈ 174 µs |
| 进程内每次调用（含 goja 解释 + 判定环） | ≈ 4.1 ms |
⇒ **别按「IPC 一定更慢」做决策**：一次性调用选进程内（省 13ms 启动），
程序内循环上百次选子进程（省每次 4ms 的解释开销——goja 只初始化一次）。
**测法是差分法**：`(T(50) − T(1)) / 49`。直接测「N 次耗时」会把**启动成本**
摊进每次调用，严重高估往返成本（实测：50 次调用的总耗时里启动占大头）。
## 二、两条延迟用例的门槛：贴着实测值，否则形同虚设
初版门槛取 50ms/次（比实测宽三个数量级）。实测发现**测量噪声本身就能掩盖
一个数量级的劣化**——给每次调用加一次额外管道往返后，边际成本反而从
174µs 变成 112µs（噪声 > 信号）。那种门槛**永远碰不到**，等于没有判据。
⇒ 边际成本那条改用 **1ms/次**（比实测宽约 5.7 倍，吸收机器差异），
并**做了反向验证**：在父侧每次调用前 busy-wait 1ms → 边际成本 1.196ms
→ 越线 → 用例红。
**顺带一条**：判据不能拿「本次测到的值」当基线（4× 自身恒真）——
判据必须**独立于被测量**。
## 三、跨域执行与 sideeffect/sidetest 的关系：已查清，不冲突
设计档 §九 第 4 条记「关系未定」。查清了：**副作用发生在父侧，不在子进程里**。
    子进程程序 host.ping()
      → remoteCaller（子进程侧只是发请求的桩）
        → 管道 → 父侧 handler：判定环 → b.Call(ctx, args)  ← 副作用在这里
⇒ 宿主若要幂等保护，在**自己的 `Binding.Call` 里**接 `sideeffect.Runner` 即可
（`Binding.Call` 是宿主实现的，ptc 不管它的语义）。子进程里的重复调用
最终都收敛到父侧那一次 `b.Call`，幂等键在那一层判，**天然覆盖子进程重跑**。
这与「怎么说话」（跨进程通道）/「怎么去重」（幂等键）的分工一致：
两者**零代码耦合**（ptc 不 import sideeffect），是**接线关系**。
## 顺带
`buildChildProgram` 抽成收 `testing.TB` 的共用实现（benchmark 也要用），
并沿用上一轮加的**预编译子进程**支持（环境变量指定），使这份测试能在
**没有 Go 工具链的 Linux 机器**上跑。

- 测试(ptc)：跨环境验证内存兜底 + 记下「CI 绿但其实跳过」这件事

## 起因：上一版的 CI 绿灯是假的
内存兜底（cgroup v2）改完后，本机 `make check` 全绿，CI（ubuntu-24.04）
**也全绿**——但那条端到端验收在 CI 上是 **SKIP**，机制从未被执行过。
发现方式不是读 CI 日志（`make check` 不加 `-v`，日志里没有测试名），
而是**主动探测 CI 环境有没有那个能力**：
	cgroup.controllers: cpuset cpu io memory ...   ← memory 控制器「可见」
	mkdir /sys/fs/cgroup/probe-$$  → Permission denied
**「控制器可见」≠「能建子组」**：前者是内核认得它，后者是权限问题。
GitHub 的 runner 是容器化的，两条候选路径都建不出来。
## 真验：找一台能跑的 Linux，交叉编译过去
实测五种环境，只有「裸机 root 的 Linux」能验（详见新文档的矩阵）。
在 bridge-work 上：
- 直接验机制本身：memory.max=128MB + 狂分配内存的子进程
  → 被 Killed，退出码 137（SIGKILL），memory.events: oom 1 / oom_kill 1
- 跑本仓的端到端验收：**PASS（4.31s，非 SKIP）**
  —— 子进程申请超限内存 ⇒ 被内核杀 ⇒ 父侧收到「执行域异常终止」⇒ 宿主存活
- 顺带确认 cgroup **已回收干净**（本仓建的 `ptc-<pid>-<nano>` 全部回收，
  残留的只有手工探测时建的 `ptc-probe-*`）
## 两条仓内改动（让下次的验证可复现）
1. **测试支持预编译子进程**（环境变量 `AGENT_HARNESS_PTC_CHILD_BIN`）：
   目标机没有 Go 工具链时也能跑同一份测试二进制——而那正是唯一能建出
   子 cgroup 的环境形态。顺带加了 `cmd/ptcchild`（交叉编译入口）。
2. **跳过必须写明理由**：文案写「本环境建不出带 memory.max 的 cgroup
   （内存兜底不可用），该场景在此环境无法验证」——
   不让恒跳过的用例看起来像「测过了」。
## 文档与纪律
- 新增 docs/CROSS-ENV-VERIFY-20261021.md：翻车经过、正确的验法、
  五种环境的实测矩阵、给下一轮的三条检查项。
- AGENTS.md 新增「门禁绿 ≠ 机制成立」一条：
  **带 `t.Skip` 的用例在 `make check` 输出里与真跑通过完全一样（都是 `ok`）**；
  平台的可用性判据要写成「探测」而非「按平台猜」；
  通用判据是「**这条路径在当前环境会被执行吗？**」——
  那是环境问题（换环境），不是机制问题（改代码）。


## [v0.8.4] — 2026-10-21

- 杂务(deps)：依赖地板追平矩阵本轮新版本（6 块）

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是隐式 breaking：不跑就报
`updates to go.mod needed`，且报错不提真正原因（**不是本仓的 API 变了**）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪 6 块、为什么无行为差异
```
oagentkit/agent    v0.10.1 → v0.10.2    oagentkit/runtime  v0.7.1 → v0.7.2
oagentkit/obs      v0.9.3  → v0.9.4     oagentkit/textx    v0.6.1 → v0.6.2
oagentkit/toolgate v0.12.0 → v0.12.1    oagentkit/mcpkit   v0.8.3 → v0.8.4（间接）
```
**这 6 个上游版本全部是「纯依赖地板追平」——各仓自身代码零改动**，只抬了
`oekit/pkg`（v0.18.0→v0.20.0）、`oekit/concurrency`（v0.18.0→v0.19.0）、
`oagentkit/guardx`（v0.13.0→v0.13.1）、`oagentkit/llm`（v0.14.2→v0.15.0）
四类地板之一，没有一行业务代码变更。
四类上游地板本身也已逐项核过：
- `oekit/pkg`：`errors` 纯增量（新增 `Errorf`）、`httpjson` 退避溢出修复
  （正常 attempt 下逐字相同）、`urlguard` 新增可选 `WithExactHosts`、
  `stringx` 仅注释、`pathx` 新包。
- `oekit/concurrency`：`retry` 抖动 panic 修复 + `syncx` 新增 `Gate`；
  **`breaker` 与 `async` 一字未动**（本仓只用 `async`）。
- `oagentkit/guardx` v0.13.1：上游只改 `go.mod`/`go.sum`，**无 Go 源码变更**。
- `oagentkit/llm` v0.15.0：新增**可选**字段 `OpenAIProviderConfig.HTTPClient`
  （缺省 nil 时行为逐字不变）。本仓只用 llm 的空导入，不碰其 API。
## 门禁
```
make check        ✅ 全绿（lint 0 issues / 28 包测试过 / 覆盖率 84.1% / 架构门禁通过）
```
> 注：门禁在**独立 worktree** 上跑的——主工作区里有并行工作线**在途未提交**的
> \`ptc/*\` 新文件（cgroup 内存上限 + 子进程 stderr 截断），它们尚未接线完，
> 在主工作区跑 lint 会因「未使用」报红。那些文件不是本次改动的一部分，
> 本次提交只含 \`go.mod\` / \`go.sum\` / \`tools.go\` / \`README.md\`。


## [v0.8.3] — 2026-10-21

- 杂务(deps)：依赖地板追平——llm v0.15.0、oekit/concurrency v0.19.0

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是隐式 breaking：不跑就报
`updates to go.mod needed`，且报错不提真正原因（**不是本仓的 API 变了**）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪两块、为什么无行为差异
**`github.com/oagentkit/llm` v0.14.2 → v0.15.0**
上游新增 `OpenAIProviderConfig.HTTPClient`（**可选**字段）：宿主可注入自己的
`*http.Client`，把出站守卫挂到 `Transport.DialContext` 上——写面校验（按 URL 判）
拦不住 DNS rebinding（两次解析之间权威 DNS 可以改口），守卫必须在拨号层。
缺省 nil 时行为**逐字不变**。
本仓只用 llm 的**空导入**（`tools.go` 的工具依赖登记），不碰其 API。
**`github.com/oekit/concurrency` v0.18.0 → v0.19.0**（随 llm 传递而来）
本仓只用 `concurrency/async`（GoSafe）——该包在 v0.18.0→v0.19.0 之间**一字未动**
（改动在 `retry` 的抖动 panic 修复与 `syncx` 的新原语，本仓不 import）。
## 顺带：docs/ 与 tools.go 里的版本钉子（散文，需手改）
`tools.go` 的「钉的是哪个版本」段落已回改（llm v0.14.2 → v0.15.0），并在段首
保留了「散文不受机器管、改地板后要手工回来改」的警示与核对命令。
## 门禁
```
make check        ✅ 全绿（lint 0 issues / 28 包测试过 / 覆盖率 84.1% / 架构门禁通过）
```


## [v0.8.2] — 2026-10-21

- 杂务(deps)：依赖地板追平上游本轮新版本——oekit/pkg v0.20.0、guardx v0.13.1

## 下游要做什么
**`go mod tidy`**。地板抬高对下游是隐式 breaking：不跑就报
`updates to go.mod needed`，且报错不提真正原因（不是本仓的 API 变了）。
本仓**自身代码零改动**——只看本仓的 diff 看不出这是一次地板抬高。
## 抬了哪两块、为什么无行为差异
**`github.com/oagentkit/guardx` v0.13.0 → v0.13.1**
上游 diff 只有 `go.mod` / `go.sum` / `CHANGELOG.md`——**没有 Go 源码变更**
（内容是 `chore(deps): oekit/pkg 依赖地板抬到 v0.20.0`）。本仓的 `toolfail` 与
`sandbox` 是它的门面（类型别名 + 常量别名 + 单条上游转发），转发面一字未动。
**`github.com/oekit/pkg` v0.18.0 → v0.20.0**
逐包核过本仓实际用到的 9 个子包（cache / encoding / errors / httpjson /
jsonrepair / mathx / singleflighttest / stringx / urlguard）：
- `errors`：**纯增量**——新增 `Errorf`（格式化 + `%w` 包装，nil-safe）。本仓用的
  `New` / `As` / `Is` / `Wrap` 四个函数是同型透传，**一字未动**。
- `httpjson`：**退避溢出修复**——旧写法 `base << (attempt-1)` 在 attempt 较大时
  int64 溢出成负数，负数绕过 `MaxDelay` 封顶，`time.After(负数)` 立刻返回 ⇒
  **该轮退避彻底消失**。改为乘法 + 饱和封顶。正常 attempt 数（1..20）下
  结果**逐字相同**，差别只在溢出的病态路径。
- `urlguard`：**纯增量**——新增 `WithExactHosts`（精确 host 白名单），未设置时
  行为与既有完全一致。本仓 `netguard` 走的是自己的 egress 判据（IP 策略单源
  egress），不换判据。
- `stringx`：**仅注释**——补 `TruncEllipsis` 的 n 语义说明。本仓未使用该函数
  （已 grep 确认生产代码无调用）。
- `pathx`：新增包，本仓未使用。
## 门禁
```
make check        ✅ 全绿（lint 0 issues / 28 包测试过 / 覆盖率 84.1% / 架构门禁通过）
```
另：文档里的版本号是散文、不受机器管，已手工回改
`docs/MIGRATION_OAGENTKIT_20261018.md` 的「当前钉的版本」一句与 README 的
「下游升级须知」表。


- 文档：更正两处文档漂移——映射表补子进程后端，设计档标记 P0/P1 已完成

## 1. OVERLAP §四 的映射表：ptc 行漏了子进程后端
那行还写着「程序化工具编排沙箱（goja VM，三重预算）」——而 v0.8.1 起本仓有两种
执行形态：进程内（兜不住内存）与子进程（内核地址空间上限兑现 MaxMemory）。
这是「该让/该下沉」判定表，漏一维会误导后来人以为只有一种形态。
## 2. 设计档 P0/P1 段还写着「已提 issue」
实际两期都已实现并发布（P0 → guardx v0.10.0，P1 → v0.11.0/v0.12.0/v0.13.0 +
本仓 v0.8.1）。文档停在「已提 issue」会让后来人以为还没做——而对应的 issue
（guardx#17 / #18）其实已随实现关闭。改为「✅ 已完成」并列出实际 PR 与版本号。
## 3. 依赖地板欠账：5 个仓的 guardx 地板 PR 已开
矩阵的依赖地板时效门禁（matrix-tools 的 verify_dep_floors.py）会报「地板落后
远端最新」。guardx 发了 v0.10.0 → v0.13.0 后，以下 5 个仓的 guardx 地板落后：
| 仓 | 原 | 新 | PR |
|---|---|---|---|
| tools | v0.9.0 | v0.13.0 | oagentkit/tools#16 |
| obs | v0.11.0 | v0.13.0 | oagentkit/obs#20 |
| skillkit | v0.11.0 | v0.13.0 | oagentkit/skillkit#17 |
| toolgate | v0.11.0 | v0.13.0 | oagentkit/toolgate#21 |
| agentkit（门面） | v0.11.0 | v0.13.0 | oagentkit/agentkit#45 |
**全是零行为差异**：这 5 个仓用的 guardx 包（logredact / fence / invoke / egress /
injectband / permgate）在 v0.11.0 → v0.13.0 之间**未变**——那几版只加了 sandbox 的
Capable 与 wire，而门面不转发这两个新包。门面那条属「只做别名转发与安全修复」
范畴（不改变转发面，48 包集合不变）。
门禁自检：fmt-check ✅ vet ✅ test ✅ cover-gate ✅ architecture-check ✅ docsync ✅


## [v0.8.1] — 2026-10-21

### 新增(ptc)：新增子进程执行后端——用内核地址空间上限兑现 MaxMemory

承接 docs/SANDBOX-DESIGN-20261021.md 的 P1（跨进程执行与资源兜底）。
# 缺口
进程内 goja VM 跑在宿主进程里，而三闸（墙钟 / 调用数 / 输出字节）**没有内存闸**
——Go 运行时没有「只给一个 VM 限内存」的开关，goja 公开 API 也没有内存限制
（只有 SetMaxCallStackSize，那是栈深）。故一个快速分配的程序能在 deadline 到点
**之前**把宿主进程撑爆，而宿主里还跑着编排引擎、事件总线、别的环节。
本后端把执行搬出宿主进程：子进程里跑 goja，用**内核的地址空间上限**
（RLIMIT_AS）兑现内存上限。
# 三条设计决定
## 1. 协议归上游（guardx/sandbox/wire）
跨进程调用协议（帧格式 + 两条通道 × 四个角色）归上游 guardx——协议是最不该
私有的东西（各后端各写一遍 ⇒ 换个后端就得换一套）。本仓只实现后端：
起进程、连管道、跑程序。
## 2. 判定环留在父侧
程序里的 host.<tool>() 与普通工具调用走**同一套** invoke.Stage 判定环——
而判定环与宿主工具都在父侧。子进程里只有一个「发请求」的桩，判定在父侧做。
**故本后端不弱化门禁**：子进程形态下判定环照走。
若把判定环放到子进程，「程序里的 host.<tool>() 绕过所有门禁」这条纪律就断了。
## 3. 执行面只有一份（不复制）
进程内与子进程的差别**只有一处**：绑定调用怎么落到宿主工具上。其余（语法预检、
三预算闸、中断驱动、调用形态包装、Promise 收口、错误分类）逐字相同。
故把 Run 的执行主体抽成 `execute`（ptc/execute.go），两种形态共用——否则
子进程形态就得把这段复制一遍，而漂移出来的往往是**安全语义**（比如子进程版
漏了墙钟闸，就成了「看起来更安全的路径反而更不安全」）。
绑定调用则抽成 `bindingCaller` 间接层：进程内传 localCaller（本地闭包），
子进程传 remoteCaller（经管道请求父侧）。
# 档位与支持面（两维正交）
档位仍是 **LevelProcess**：能力面没变（还是 JS 子集 + 绑定白名单），强的只是
**资源面**。故：
- 进程内后端**不实现** `sandbox.Capable`（兜不住内存）⇒ 缺省即「只兑现三闸」；
- 子进程后端**实现** Capable（能兑现 MaxMemory）；
- 登记簿据此区分：要求 MaxMemory 时，进程内后端**不被选中**（要求与实际之间
  不允许有静默落差），子进程后端被选中。
# 平台差异
`RLIMIT_AS` 是 Unix 专有。非 Unix 平台**返回错误**而不是静默放行——「以为兜住了、
实际没有」正是这套机制要消灭的东西。
`RLIMIT_AS` 限的是**虚拟地址空间**（可能远大于物理内存），故它是**粗但硬**的
上限：兜得住「快速分配撑爆宿主」这个形态，但可能比真实内存占用更早触发。
本后端**如实声明**自己兑现的是「地址空间有硬上限」，不是「物理内存精确封顶」。
# 验收用例（真兜底）
```
TestSubprocessExecutesProgram      程序在子进程跑，工具调用经管道往返父侧执行
TestSubprocessMemoryLimitKillsChild 子进程申请超限内存 ⇒ 被内核杀掉 ⇒
                                   父侧收到「执行域异常终止」，宿主进程存活
```
第二个用例是设计档 §八 P1 的原文要求：「一个真兜底的用例——子进程内程序申请
超过上限的内存 ⇒ 子进程被内核杀掉 ⇒ 父侧收到「执行域异常终止」，**宿主进程
存活**」。它用**现场编译的小程序**当子进程（走与生产完全一致的 MaybeRunChild
入口与 fd 传递），故测出来的就是生产行为。
# 门禁连带改动
- **`harness-sandbox-execution-single-entry` 的收口点从 `sandbox` 迁到 `ptc`**：
  sandbox 已转为**门面**（核心在上游），而「起外部进程」的实现落在 ptc 的子进程
  后端。收口**一处**的意图不变（仍只有一个包能 import os/exec），只是那个包变了。
- `ptc/ptc.go` 拆出 `ptc/execute.go`（抽 execute 后涨到 410 行，顶破 400 阈值）。
# 门禁
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅
architecture-check ✅（8 条规则 + 5 个门包 + 28 个包的依赖方向）
docsync ✅  modreplace ✅  modwork ✅
golangci-lint ✅ 0 issues
```
（上游 guardx v0.12.0 提供 sandbox/wire：帧格式 + 启动通道 St archer/Runner +
调用通道 Server/Client。）

### 新增(sandbox)：跟随上游 guardx v0.10.0——门面加 Capable，档位登记门禁扩「预算支持面」一维

上游 guardx v0.10.0（PR #19，承接 guardx#17）落地了执行沙箱的「预算支持面」：
Budget 补 MaxMemory、新增可选能力面 Capable、Resolve 按支持面过滤。
本仓同步三处。
## 1. 门面加 Capable 别名
`Budget` 是类型别名 ⇒ 上游加的 MaxMemory 字段**自动跟随**，门面不用改。
需显式转发的是新增的接口与函数：Capable / CapableFunc / SupportsBasic /
SupportsAll。`Resolve` 的 variadic 化（`Resolve(min, budget ...Budget)`）在
门面上**不可见**——本仓门面不转发 Resolve，宿主直接用上游的即可。
## 2. 档位登记门禁扩一维：登记「支持哪些预算项」
policy 的 `sandbox_providers` 段新增 `supports` 登记项（取值
wall_clock / max_calls / max_bytes / max_memory）。
为什么登记这一维：档位（Level）答的是「能力面多强」，预算支持面答的是
「资源兜底兑现哪几项」。两者**正交**——进程内解释器的能力面与 LevelProcess
同级，却兜不住内存。少了这一维，「要求了内存兜底而后端兜不住」这件事在
**声明面**是隐形的，宿主只能在运行期才发现。
门禁核对「登记与代码事实一致」，判据走 `go/ast` 的**方法指纹**（门禁不能
import 被判的包）：
- 后端**没有** Supports 方法 ⇒ 只能登记基本三闸（上游 Capable 的缺省语义：
  漏声明得到的是「兜不住」，不是「兜得住」）；
- 登记了 max_memory 而代码无 Supports ⇒ **报红**（声明面说了兜得住、代码兜不住，
  要求与实际之间出现静默落差）；
- 代码有 Supports 而登记只写三闸 ⇒ **不报红**（后端自报得比登记更保守，
  不是撒谎）。
## 3. ptc-goja 如实登记「只兑现三闸」
`supports: [wall_clock, max_calls, max_bytes]`——**不兑现内存**，理由写在
登记的注释里：进程内 goja VM 跑在宿主进程里，Go 运行时没有「只给一个 VM 限内存」
的开关，goja 公开 API 也没有内存限制（只有 SetMaxCallStackSize，那是栈深）。
故它**真兜不住**内存，登记必须如实落在保守的一侧。
## 反向验证
注入两条违规，门禁都准确报红（事后已恢复）：
- 登记 max_memory 但代码无 Supports ⇒ `sandbox_provider_supports_drift`
- 登记未知预算项 gpu_memory ⇒ `sandbox_provider_unknown_budget`
## 门禁
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅
architecture-check ✅（8 条规则 + 5 个门包 + 28 个包的依赖方向）
docsync ✅  modreplace ✅  modwork ✅
golangci-lint ✅ 0 issues
```
（archcheck/sandbox.go 因加支持面核对顶破 400 行阈值，按关注点拆出
archcheck/sandbox_supports.go——「档位核对」与「支持面核对」是两个关注点。）
（本提交不含并发工作线的 ptc/ptc.go 改动与 docs/SANDBOX-DESIGN-20261021.md。）

### 修复(netguard)：测试夹具改用规范 HTTP 服务器——修掉 CI 偶发红

## 现象
2026-10-21 CI 首次出现 netguard 红（此前 12 次全绿）：
```
Unsolicited response received on idle HTTP channel starting with
"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"; err=<nil>
--- FAIL: TestNewClientAttachesGuard (0.00s)
    client_test.go:69: 放行回环后必须能连上: Get "http://127.0.0.1:33819/x":
    readLoopPeekFailLocked: %!w(<nil>)
```
本地跑 10 次全过（macOS），故是**时序相关的偶发**，非确定性失败。
## 根因
**那串「unsolicited 响应」的字节逐字等于旧夹具写的那一行** —— 根因就在夹具。
旧夹具是裸 TCP：accept 到连接后手写
`HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n` 然后立刻 `c.Close()`。
但那行响应**没有 `Connection: close` 头**——按 HTTP/1.1 语义它是 keep-alive，
客户端读完会把连接放回空闲池，而服务端已经把它关了。于是池里可能躺着一条
「客户端以为可复用、实际已关或还带着残留字节」的连接：下次复用时 Go transport
先 peek 它，peek 到**响应**（而非 EOF）就报 unsolicited response。
**协议不一致是客观存在的**，与平台无关；CI 的 Linux + 负载环境只是更容易撞上
那个时序窗口。
## 修法
夹具改用 `net/http.Server`（`http.NewServeMux` + `Server.Serve`）——由标准库
负责 keep-alive 与关闭语义，客户端与服务端对「这条连接还能不能用」的判断一致，
这类协议不一致不可能再出现。
**断言语义逐字不变**：仍回一个最小合法响应（200 + 零长度 body）——只关连接的
话客户端读到 EOF，失败形态会与「被守卫拦住」混在一起（两者都表现为 Get 出错），
那条用例就分不清「守卫拦了」与「服务端没回话」。
## 如实说明验证强度
- 本地（macOS）**未能复现**该失败：跑 10 次普通 + 20 次 -race + 5 次全包
  -race 全过；专门写的探针（旧夹具 200 次连发 / 16 并发 × 10 次）在本地也不复现。
- 故本修复的依据是**根因分析 + CI 日志里那串字节与夹具字面量的逐字匹配**，
  不是「复现后再修」。改动只碰测试夹具，**不动生产代码**，且让协议一致性变好。
## 门禁
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅
architecture-check ✅  docsync ✅
golangci-lint ✅ 0 issues
```

- 杂务(deps)：跟随 guardx v0.13.0——wire 的 import 路径迁到仓根

上游 guardx#22 把 wire 从 `sandbox/wire/` 移到 `wire/`（仓根下）——
矩阵的包路径规则是「github.com/{org}/{repo}/{包名末段}，中间层目录丢弃」，
不支持「仓内包的子包」，故 wire 必须与 sandbox 平级。
故本仓的 import 路径随之从
  github.com/oagentkit/guardx/sandbox/wire
变为
  github.com/oagentkit/guardx/wire
依赖地板同时抬到 v0.13.0。代码内容无变化（仅 import 路径），子进程后端的
真兜底用例复跑全绿。

- 杂务(deps)：core 地板抬到 v0.11.0 + 修 tools.go 版本钉子注释的漂移

## 依赖地板
`oekit/core` v0.10.0 → v0.11.0（上游最新）。
变更性质核对：v0.11.0 把配置加载入口**按来源拆开**——新增 `LoadFile`（任意路径）
与 `Unmarshal`（字节源），三个入口共享同一条解码线（展开 ${VAR} → 可选严格判定 →
解码 → tag 接线 → FillDefaults → ValidateTags），故严格模式的诊断面
（未知键拒载 + 合法键清单 + 拼写建议）**与来源无关**。
`LoadBootstrap` 行为**逐字不变**，既有调用方零改动——**无破坏性变更**。
本仓只 import `oekit/core/version`（版本号常量），影响面为零。
## 顺带修一处「散文数字」漂移
`tools.go`（版本钉子，`//go:build tools`）的「钉的是哪个版本」一节里写着
`llm v0.14.1` / `obs v0.9.2`——而 go.mod 实际已是 v0.14.2 / v0.9.3
（上一轮抬地板时改了 go.mod，没回改这段注释）。
**这类漂移没有任何东西会报**：go.mod 是机器管的地板，那段注释是纯散文，
而它恰恰是读者判断「钉子领先还是落后于上游地板」的**唯一依据**——
照着过期版本号做判断，结论会反。
本仓纪律「文档里的每个数字，要么被机器判，要么在它旁边写明『不受机器管，
改前请手算』」对**代码注释里的版本号**同样适用。故本次在该段加了两件事：
1. 显式标注 **「下面是散文，不受机器管——go.mod 才是权威，改依赖地板后要手工
   回来改这一段」**；
2. 给出**一条机械核对命令**（`go list -m` 四个模块），让核对不必靠记忆。
顺带把 core 那条说明更新到 v0.11.0 的实际内容（入口按来源拆开），
不再停留在 v0.10.0 时的「config 严格模式」。
## 门禁
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅
architecture-check ✅（8 条规则 + 5 个门包 + 28 个包的依赖方向）
docsync ✅  modreplace ✅  modwork ✅
golangci-lint ✅ 0 issues
```
（本提交只含 go.mod / go.sum / tools.go 三个文件，不含并发工作线的改动。）


- 文档(sandbox-design)：补 P2 判定与 P3 记录

## P2：判定为「本仓不做，宿主自己做」
P1 落地后实测确认：宿主实现容器/microVM 后端**完全不需要本仓参与**——
只依赖 guardx 即可（sandbox.Provider 就是那个扩展点：ID/Level/Supports/Exec，
Registry 按「档位下限 + 预算可兑现性」挑后端）。
故 P2 的形态是**宿主侧工作，不是内核侧工作**。本仓若再写一个容器后端，就变成
「同一个机制两份实现」——正是 OVERLAP.md 明令禁止的形态。
三个不做理由（写进档里）：① 价值前提未被证明（本档 §三 已论证：今天更强的
能力裁剪在 ptc 的 JS 子集 + 绑定白名单下已经拿到了）；② 要给内核引入 docker
运行时依赖（为一个尚未证明需要的后端抬地板不划算）；③ 本地/CI 不可复现
（门禁要求覆盖率与测试在 CI 上可跑）。
若宿主确实需要：实现 sandbox.Provider 并注册进 sandbox.Registry，本仓不动。
## P3：本档初稿没有这一期
核对全文确认：初稿的分期只有 P0 / P1 / P2——不是漏写，是没规划。
按 P1/P2 落地后的现状，唯一还没被覆盖且值得做的一期是「P3-候选」：
**把「跨域执行」这条纪律从文档变成门禁**。
缺口是真的：本仓的档位登记门禁只核对「登记 ↔ 代码事实」，不核对
「**实际执行用的后端**是不是登记过的那个」。形态是宿主绕过 sandbox.Registry
直接调 ptc.Run（进程内形态），于是**要求了 OS 级隔离却跑在进程内解释器上**——
门禁全绿，而实际强度与声明不符。
但它**跨仓**（判宿主行为要宿主仓的 policy），且落点需与上游/宿主对齐：
本仓的 archcheck 已是「可导入的判据执行体」形态，宿主复用同一份是可行方向
（与本仓「门禁执行体必须可被宿主复用」的既有纪律一致）。
**不擅自开工**——跨仓设计决定需要拍板，不是「照着做」的实现任务。

- 文档(agent-harness)：补三条纪律 + 校准包数与工程底座数字

代码侧（retryplan / sideeffect.FencedSink / Renew）已在 54959e2 入库，
本次只补纪律与文档同步。
AGENTS.md 新增三条纪律（都是本轮真实踩出来的）：
1. **反向验证打不中时，先问「这真的是契约要求吗」，再问「用例够不够强」**。
   给 FencedSink 做反向验证时，两条变异打不中——查下去发现它们在契约下
   不可观测（陈旧拒收那条路径根本不碰已见令牌，拒收即 return）。
   第一反应是「用例不够强」，补完才发现自己把**实现的内部状态**当成了
   契约的一部分，于是又删掉了那两条。
   契约里只写**调用方能观测到的**要求，不写**实现内部的**状态演变——
   后者是把实现方式钉死，且会掩盖真正的契约缺口。
2. **并发用例要「确定性构造」，不能靠时序去撞**。
   要证明「比较与写入必须原子」非空，就得有三步实现的反面教材替身。
   第一版靠 goroutine 调度时序去撞，结果偶发复现：写成「必须复现」是
   flake，写成「不要求复现」等于没断言。修法是加**确定性竞态窗口钩子**
   （读已见最大之后、判断之前卡住），并用**原子标志**保证只有首次调用
   阻塞——用 sync.Once 会阻塞后续调用者，主流程与钩子互等即死锁。
3. **组合逻辑归内核，判定归各自机制**（retryplan 的立论）。
   每个宿主各写一遍组合逻辑，必然各判一套，漂移到没人发现为止。
   组合逻辑里恰好藏着必须全局一致的判定——本例是「这次重试到底原样
   重发，还是走幂等键重放」。
README / AI-GUIDE 同步：
- 包数 27 → 28（新增 retryplan），AI-GUIDE 分组表补「组合机制」一组
  （retryplan 与 sidetest 同属非机制面，但性质不同：sidetest 是判据，
  retryplan 是决策机制）。
- 「工程底座」表校准：生产代码 14,806 行 / 84 文件，测试 111 个文件，
  覆盖率 86.1%，依赖方向 28 包。
- README 的「下游升级须知」表已由 v0.8.0 行覆盖（911c874 定的 minor：
  Store 接口新增方法是源码不兼容变更）。
注：README 里 sandbox 设计文档的导航行由并发工作线加入，随 README
一并提交（共享文件无法拆分）。本次未触碰并发工作线的 ptc/ptc.go 与
docs/SANDBOX-DESIGN-20261021.md。


## [v0.8.0] — 2026-10-21

### 新增(harness)：新增 retryplan 重试处置决策 + sideeffect/sidetest 续期与拒收契约

# 一、本次下沉带来的下游消费（并发工作线产出）
`retryplan` 把「该不该重试」与「重试会不会重复施加」两问合成一条可执行结论
（三档路径：不重试 / 原样重发 / 走幂等键重放）。判定来自上游 `guardx/toolfail`
（处置层语义），幂等键由 `sideeffect` 保证「至多一次」——本包对 `sideeffect`
**零代码依赖**，两者是契约上的衔接而非耦合。
它消费的是本轮下沉的 `guardx/toolfail`，且**直接 import 上游路径**（不走本仓门面）——
这恰好验证了「门面只保转发」的用法：直接引上游与走本仓门面在行为上不可区分
（本轮实测两侧判定链六种场景全同，含错误链穿透）。
`sideeffect` / `sidetest` 同期补齐续期（renew）与下游拒收契约。
# 二、本轮我做的三件事
1. **复核并更正 `docs/OAGENTKIT-OVERLAP.md` §四**（提交 105dee5）：
   删掉一句**不实断言**——我上一轮写的「同一上下文窗口在两处算出两个阈值」。
   实测才发现：① 对照方式本身错了（拿上游纯窗口口径与本仓含自设上限的最终上限
   直接比大小，不是同一个东西）；② 职责面本就不同（上游判「该压了吗」，本仓答
   「上限是多少 + 计划怎么执行」，本仓包内没有「该压了」的阈值判定）。
   正确对照下「窗口−预留」是同一条算式，唯一差异是地板值（本仓 1 / 上游 0），
   且本仓有明确理由。同时把 §四 补成一张**完整的「本仓 → 上游」映射表**
   （26 个包逐个列归属、核心位置、形态），作为后来人查「某个包归谁」的第一入口。
2. **policy 登记 `retryplan`**（modules 段 + `scan_roots`）：
   未登记的包对本门禁**隐形**——任何模块都能 import 它而不判违规；不在
   `scan_roots` 里则「超长文件」「god object」两条阈值对它从来不生效。
3. **两处 stdlib `errors` → `oekit/pkg/errors`**：
   本仓只允许一套 errors 包（类型同一性红线：混用会打断 BizError 的 As/Is 判定）。
   涉及 `retryplan/retryplan_test.go` 与 `sideeffect/sink_contract_test.go`。
# 三、门禁
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅
architecture-check ✅（8 条规则 + 5 个门包 + 28 个包的依赖方向）
docsync ✅（README 包表与仓库一致）
golangci-lint ✅ 0 issues
```
> 过程中抓到一次真实的测试 hang（`sideeffect` 的续期用例，CPU 0% 空等 10 分钟
> 被 Go 超时打断），已由并发工作线修复，复跑 0.3s 通过。

- 文档(release)：下一版定为 v0.8.0（minor）——Store 接口新增方法是源码不兼容变更

原写 v0.7.1（patch）。按家族明文规则（上游 guardx AGENTS.md：「SemVer。
0.x 阶段 minor 允许 breaking」），patch 号留给兼容修复；**给宿主实现的接口
新增必填方法 = 源码不兼容**，宿主实现缺 Renew 就编不过，属 minor 范畴。
本版的破坏性变更：sideeffect.Store 新增 Renew(ctx, act, lease, ttl) (Lease, error)。
其余（sideeffect.FencedSink 契约、retryplan 包）是新增，不影响既有代码。
（升级须知的正文沿用并发工作线已写好的内容，只改版本号与判定依据。）

- 文档(overlap)：复核并更正 §四 的结论——补完整映射表，删掉一句不实断言

上一轮下沉 toolfail/sandbox 时，我在 §四 写了「同一个上下文窗口在两处算出两个
阈值」。**实测评数值后该断言不成立**，本轮更正。
## 断言错在哪
1. **对照方式本身错了**：拿上游 `EffectiveWindow()`（纯窗口口径：窗口−预留）与
   本仓 `MaxInput()`（含「自设上限」的最终可用上限）直接比大小——不是同一个东西。
   正确对照是本仓的「窗口−预留」推导分支（baseInput<=0）去比，两边这才是同一条算式。
   正确对照下：**唯一差异是地板值**（本仓 1 / 上游 0），且本仓有明确理由
   （地板 1 让后果由 Overflow / Classify(critical) 这类看得见的信号承接，
   而不是抛错把「窗口小」和「预留大」两种病因混成一个错误）。
2. **职责面本就不同**：上游 `textx/compact.ShouldCompact` 答「该压了吗」（阈值/熔断/
   token 三轨），本仓 `budget` 答「输入侧可用上限是多少」+「压实计划怎么执行」——
   **本仓 budget 包内没有「该压了」的阈值判定**，那正是上游的职责。
## 本轮另做的一件事
§四 原本是散文式的「该守/该让」两段清单。补成一张**完整的「本仓 → 上游」映射表**
（本仓 26 个包逐个列归属、核心位置、形态），作为后来人查「某个包归谁」的第一入口。
顺带把 `tooldisclose` 的判定从「部分重叠」改为「不下沉」并给出实测依据：
上游 `mcpkit/mcp.Catalog` 是「某个 server 声明了哪些工具名」（NewCatalog(server, tools)
+ Contains + Diff），本仓 `Catalog` 是「模型可见的渐进披露目录」（带 Description/Schema、
并发加载集、检索上限、重名 first-wins）——**server 面 vs 模型面，不是同一层**。
## 教训
与本节开头那条同源：**结论表比事实表更容易腐烂**，而这次腐烂的是我自己写的结论。
写「两个实现算出两个阈值」这类断言时，**必须先跑数值对照**——判据对不对，
要看输入相同、输出不同；只读代码注释是看不出口径差的。
（本提交只改 docs/OAGENTKIT-OVERLAP.md，不含并发工作线的新增包。）


## [v0.7.0] — 2026-10-21

### 重构(harness)：toolfail 与 sandbox 下沉上游 guardx，本仓转门面

# 一、本次下沉（agent-harness → oagentkit/guardx）
## 背景
复核实测上游 8 个模块的全部子包后，发现本仓有两处包属于「跨宿主必须一致的判定
语义」而非「编排语义」——它们留在本仓，处置口径就会各宿主各判一套：
- `toolfail`：失败分类与处置判定。判定口径必须全局一致，否则重试策略静默错乱
  （对永久失败死循环重试，或对瞬时失败一次就放弃）。
- `sandbox`：隔离档位与执行后端协议。档位若各宿主各定一套，「宿主声明的强度」
  与「实际执行的强度」之间就没有信号——降级了没人知道。
两者与 `invoke`（调用链）、`permgate`（许可判定）同族，都是「要求 vs 实际」的
判定语义，故归 guardx（tier 0 安全层，零外部依赖）。
## 上游
| 仓 | PR | 内容 |
|---|---|---|
| oagentkit/guardx | #14 | 新增 `toolfail`（99.0% 覆盖） |
| oagentkit/guardx | #15 | 新增 `sandbox`（100.0% 覆盖） |
| oagentkit/matrix-tools | #53 | 矩阵登记（modules.json 单一数据源） |
| oagentkit/oagentkit.github.io | #24 | 重生成 llms.txt / versions.json |
guardx 已发 **v0.9.0**。issue：oagentkit/mcpkit#17（errclass 与 toolfail 的层级
关系：协议层 vs 处置层，互补不重复）。
## 本仓
- `toolfail` / `sandbox` 转**门面**：类型别名 + 常量别名 + 单条上游转发。
  **调用面逐字兼容**——宿主 bianque 的 `internal/engine/scheduler/experts/dispatch.go`
  在真实使用 `toolfail`，类型别名是编译期同一类型，其 `errors.As` 判定链继续有效。
- 实现层测试归上游（99%/100% 覆盖），本仓只留**门面契约测试**（类型同一性 /
  导出符号齐全 / 调用形态不变 / 核心语义不弱化）。同一机制的两份测试会各自漂移。
- policy `facades` 段从 3 条扩到 **5 条**——「核心归上游、门面留本仓」从此有机器
  约束（`harness-facade-stays-facade`），不只是散文。
- 依赖地板：guardx v0.8.2 → v0.9.0，llm v0.14.1 → v0.14.2，agent v0.10.0 → v0.10.1
  （后两者均为修复类，无破坏性变更落在本仓用到的原语上）。
- 文档：`docs/OAGENTKIT-OVERLAP.md` §四 的结论已更正（初稿把 `toolfail` 列在
  「该守」一侧，该判断已过时）；README 包表与「工程底座」三处数字同步。
## 门禁自检
```
fmt-check ✅  vet ✅  test ✅  cover-gate ✅（85.9%）
architecture-check ✅（8 条规则 + 5 个门包 + 27 包依赖方向，无阻断级违反）
docsync ✅（README 包表与仓库一致，27 个包）
modreplace ✅  modwork ✅
```
转门面时门禁当场报出 3 处违规（两处 stdlib errors、一处 context 未登记
allowed_imports）——门禁是活的，修法与既有 `policy` 包登记 `io/fs` 同款。
# 二、并发会话的在制品（本提交一并纳入，原因见下）
`sandbox` 包 / `sideeffect` / `sidetest` / `archcheck/sandbox.go` /
`ptc/provider.go` 等改动由另一条工作线产出，不出自本次下沉。
**为何一并提交**：本仓 policy 里 `sandbox` 包的登记（并发会话）与 `sandbox`
转门面（本次下沉）在**同一个文件的同一处**，拆开提交会产生「中间状态门禁红」
——`sandbox` 门包已登记但包目录未被 modules 段覆盖。当前状态自洽且门禁全绿。
`sideeffect` / `sidetest` 的 lint 告警（unused 辅助方法）未修：`sidetest` 的
`ClockedStore` 契约与 `sideeffect` 的 `useClock` 尚未接上，属那条工作线的在途项。

- 文档(release)：下游升级须知补 v0.7.0 行

本次是「依赖地板抬高 + 转门面」两件事叠在一起，对下游的处置要写清：
- 调用面逐字兼容（类型别名是编译期同一类型），宿主既有 errors.As 判定链继续有效；
- 但地板抬高必须 go mod tidy，否则报 updates to go.mod needed 且不提真正原因；
- 且若下游直接 import 过本仓 toolfail / sandbox 的具体实现（自造 *Failure、
  注册自定义 Provider），要改 import 上游路径——门面只保转发，不提供实现。
这行是下游读发布说明的入口（tag 注解会沉进 git 历史，README 不会）。


## [v0.6.5] — 2026-10-20

- 文档：校准 USAGE.md 与 README 的三处漂移（版本号 / 包数 / 覆盖率）

docs/USAGE.md 与 README 都已存在且内容完整（24 包全覆盖），
本次不是补写，是**修掉会误导接入方的过期数字**。
一、go get 版本号停在 v0.2.0（落后四个 minor）
两处都硬编码了 @v0.2.0。照它接入会拿到一个缺了好几轮能力的旧版本，
而且**不报错**——这正是「过期指引」最坏的地方。
改为 @latest + 注明当前版本 v0.6.4，并说明为什么不钉死数字：
钉死的数字在几轮发版后就是过期指引，要复现特定版本再显式钉。
顺带在 README 补一条**容易踩的语义**：require 里写的是「最低版本」，
下游 go mod tidy 只保证不低于它，不会自动升到最新版——要新版本必须显式 go get。
二、USAGE.md 说「22 个包是 9 组」，两处都不对
- 组数：下面列的是 10 组，不是 9 组。
- 包数：policy 的 modules 段登记 24 个，文档写 22 个。
两个数说的不是一件事：本文覆盖 22 个**能力包**（10 组），
多出的两个是 toolname（工具名归一的单一真相，被 tooldisclose 与 ptc 共用，
不单独成组接入）与 archcheck（门禁工具本身，宿主不用引）。
已在正文写明口径，避免读者对着 policy 数出 24 个而困惑。
三、覆盖率写 80.4%，实际 86.6%
README 早已是 86.6%，USAGE.md 停在 80.4%——典型的文档双源漂移。
实测 `go tool cover -func=coverage.out` 为 86.6%，门禁 cover-gate 输出同值。
验证：make check 全绿。其中门禁输出直接印证了本次修改——
「架构门禁通过（7 条规则 + 3 个门面包 + 24 个包的依赖方向）」
与「README 包表与仓库一致（24 个包）✓」；覆盖率门禁报 86.6%。
另核对了 USAGE.md 引用的 12 个关键符号（BuildPlan / StepSink / RunContext /
SeedFromStore / SeqGate / NetRemovedTokens / GrossRemovedTokens / DrainSession /
ColumnValue / ErrNotReport / SafeToResubmit / Retryable），
逐个在源码中确认存在，无失效引用。


- 文档(release)：下游升级须知表补 v0.6.5 行

按发版纪律，这张表是**手写**的精简版（生成器每次都会重写 CHANGELOG，手写内容
活不过下一次生成）——表里放「哪一版会让下游红、红在哪儿」。
v0.6.5 有两件对下游有影响的事：
1. **依赖地板抬高**（oekit/pkg v0.18.0 / oagentkit/obs v0.9.3 /
   oagentkit/mcpkit v0.8.3）——对下游是**隐式 breaking**：本仓源码零改动，
   但下游的 go.sum 需要跟着更新，不跑 `go mod tidy` 会报
   `updates to go.mod needed`，而报错不提真正原因。
2. **archcheck 的 god object 判据改比「实质方法数」**——对下游**无感**：
   policy 不写新字段即按缺省 140 生效（老 policy 无需改动）；若某宿主要保持
   旧语义（方法总数），在自己的 policy 里显式写 `forwarding_shell_max_chars: 0`
   即可逐字退回旧行为。
本条提交在 CHANGELOG 生成**之前**落地——否则它不会出现在 v0.6.5 段里。

- fix(archcheck)：god object 判据区分「转发壳」与「实质方法」（issue #13）

方法数超阈值的**前提**是判据自己写明的那句：「该类型同时承担了过多关注点，
任何一次改动都要在它的方法全集上评估影响面」。
**这个前提对退化成 façade 的类型不成立**：把实现全部下沉到子包、自己只做转发的
façade 承担了零关注点——它挂在类型上的方法数 ≈ **公共 API 表面规模**，是另一个量。
于是同一个数在两种类型上含义完全不同：真 god object 上它是复杂度集中度，
façade 上它是 API 表面积。
量纲与前提不对齐，后果不是「报得不准」，是**逼维护者做坏事**：一个已经拆得很
干净的 façade 撞上阈值时，只有两个选择——把子簇合回去（**撤销一次正确的重构**），
或调高阈值（**等于放弃判据**）。这是「假红」最坏的形态：**报错把正确的工作判成
错的**，判据对最该被表扬的宿主最不友好。
## 改了什么
- 阈值改比**实质方法数** = 方法总数 − 转发壳数。
  「转发壳」= 方法体只有一条转发语句、且剥掉注释与空白后长度 ≤
  `forwarding_shell_max_chars`（缺省 140）。
- **原始信号不丢**：报告里同时给出方法总数与壳数
  （`pkg.T (45 个方法 = 7 转发壳 + 38 实质，实质 38 > 25)`），
  维护者仍看得到「这个类型的 API 表面有多大」，只是不再被告知那是复杂度集中。
- policy 新增**可选**字段 `forwarding_shell_max_chars`：
  不写 = 缺省 140（**老 policy 无需改动即可享受修正**）；
  显式写 0 = 关闭该区分，判定**逐字退回旧行为**。
- 判据从**逐行正则**升级到 **`go/ast`**——「一个函数体是不是只有一条转发语句」
  是结构性问题，只有语法树能判。旧实现逐行正则拿到的是「某行长得像方法定义」，
  **拿不到函数体**，所以它分不出壳与实质。这不是实现偷懒，是量纲选错了。
  与本仓其余结构判定（门包面 facade.go / 依赖方向 deps.go / import 路径
  imports.go）同一条路。
- 解析失败**响亮失败**：某个 .go 文件解析不了，它的方法数就没被计入，
  那个类型的方法数被低估 → 门禁对它隐形。这正是「门禁变绿」最贵的形态，
  与 policy 解析失败同一条纪律：门禁给不出结论时不能装作通过。
## 刻度依据
宿主实测样本（issue #13）：一个 45 方法的纯 façade 有 39 个方法体 ≤140 字符、
只有 4 个超过 400 字符；而三个真 god object 对照组（47 / 46 / 42 方法）的
转发壳占比是 **0%**——即 140 这个刻度能把「拆干净的 façade」与「真复杂」分开，
且**不改变对照组的判定**。
## 反向验证（两边都验过，不是「从没见过它触发」）
- 注入 28 个**实质**方法构成的真 god object → 门禁**红**，退出码 1，
  报告 `28 个方法 = 0 转发壳 + 28 实质`。
- 注入 30 个**纯转发**方法构成的 façade → 门禁**绿**。
- 用例成对钉住：同一批代码，逃逸口开着时绿、关上时红。**只有「开着绿」的用例
  等于没钉住**——它可能只是因为判据整个失灵而绿（假绿）。本次新增
  `archcheck/godobject_test.go` 12 组用例，含对照组不受影响那条。
## 刻意没做的事
- **没改聚合键**：「泛型实参被剥掉」（`Manager[int]` 与 `Manager[string]` 归到
  同一个 `Manager`）是既有语义，本次**原样保留**——改聚合键是另一件事，
  混进来会让 diff 难审、也让「本次只改了什么」说不清。单独记 issue 跟进。
  影响面：只有**按实例特化**的方法会被算到基类型头上。
- **没加新规则**：判据面仍是 7 条正则 + 2 条 go/ast，本次改的是阈值判据。
下游要做什么：**policy 无需改动**即可生效（缺省即修正）。若宿主的
`max_methods_per_type` 语义要求「方法总数」，在自己的 policy 里显式写
`forwarding_shell_max_chars: 0` 即可逐字退回旧行为。本仓**无 API 变更**：
`archcheck` 的公开 API 只多了一个可选的 policy 字段。

- 文档(accuracy)：补齐文档导航 + 给「散文数字」加显式警示 + 校准版本引用

三件事：
1. README 补「全部文档一览」表，并给每份文档标注**性质**（活文档 / 历史快照）——
   docs/SPLIT_PLAN.md、MIGRATION_OAGENTKIT_20261018.md、SELF-AUDIT-20261018.md
   是**历史快照**（记录当时的判断与踩坑），不是当前事实。此前 README 导航只列
   三份入口文档，读者按 SPLIT_PLAN 的「L2/L3 待拆」去找当前包清单，会拿到
   写于 10 个包时的过期答案——**指路牌本身会过期**。
   当前事实一律以 README + 门禁 + 源码为准。
2. README「工程底座」表与 AI-GUIDE 的 docsync 条目都加一句显式警示：
   **那张表不在 docsync 的判定面内**——docsync 只校验「包清单」表的包名 /
   依赖列 / 行数；「工程底座」里的生产行数 / 测试文件数 / 覆盖率是纯散文，
   **漂了不会有人拦**。v0.6.4 修那三处漂移时它们已经过期了三个月
   （archcheck 从 cmd/ 下沉成独立包是 2026-10-19，此后没人更新）。
   纪律：**文档里的每个数字，要么被机器判，要么在它旁边写明「不受机器管，
   改前请手算」**。没有第三条路——「我记得更新过了」不是保证。
3. 校准三处版本引用（AGENTS.md / MIGRATION / OVERLAP 里的 oekit/pkg、
   oagentkit/mcpkit 版本号），与本次 deps 提交对齐。
顺带把 architecture-policy.yaml 注释里的「规则四：门面包只能是门面」改为
「规则七（AST 类，不在 rules 段）」：门禁现在是 7 条正则规则 + 2 条 go/ast
规则，注释里仍写着「规则四」，按字面读会以为门面规则是正则表里的一条。
**纯注释改动，判据面零变化**（rules 段与 facades 段一字未动）。
下游要做什么：无（纯文档 + 注释，零 API 变更、零依赖地板抬高）。

- 杂务(deps)：依赖地板追平上游本轮新版本（pkg / obs / mcpkit）

三个模块到各自最新 tag：
- oekit/pkg v0.17.0 → v0.18.0
- oagentkit/obs v0.9.2 → v0.9.3
- oagentkit/mcpkit v0.8.2 → v0.8.3
均**无本仓可见的破坏性变更**（逐个读上游 CHANGELOG 核实）：
- pkg v0.18.0：jsonrepair 补抽数组原语 ExtractArray，纯增量。本仓只 import
  pkg/errors（New / Wrap / Is / As / Cause），不涉及 jsonrepair。
  注：pkg v0.17.0 那条 BREAKING（stringx.Masker 短值改整段全量打码）落在本仓
  **未使用**的原语上——已 grep 确认全仓零引用，故不受影响。
- obs v0.9.3 / mcpkit v0.8.3：两者的内容都是「依赖地板抬到最新」的杂务提交，
  本仓可见面零变更（无 API 增删改、无 import 路径迁移）。
go.mod 零连带变动：三个版本各自抬升不牵动其他 require 行，tidy 后无新增/移除。
下游要做什么：依赖地板抬高对下游是**隐式 breaking**——本仓源码零改动，
但下游的 go.sum 需要跟着更新（不跑 `go mod tidy` 会报 `updates to go.mod needed`，
而报错不提真正原因）。本仓**无 API 变更**：公开包（protocol / eventbus / netguard /
ptc / archcheck …）的签名与语义均未变，宿主按 v0.6.4 写的代码不需要改。


## [v0.6.4] — 2026-10-20

- 文档(guide)：补一份面向 AI 代理的改造指南 + 校准 README 工程底座三处漂移

本仓已有三份文档，但都不回答「要改这个仓的 AI 代理下一步做什么」：
README 讲「是什么」、docs/USAGE.md 讲「怎么接进宿主产品」、AGENTS.md 讲
「为什么这么规定」。缺的是**操作型**的那一份——「任务 → 改哪些文件 → 什么顺序
→ 会被哪条门禁拦 → 怎么验收」。新增 docs/AI-GUIDE.md 补这一面。
新增（docs/AI-GUIDE.md，29KB）：
- 任务 → 改动清单映射：加新包 / 改判据 / 改门禁规则 / 动依赖 / 拆文件 / 发版，
  每类给出「先改哪张声明表 → 再写什么代码 → 最后同步哪张文档表」的顺序。
- 9 条门禁速查 + 红了怎么修（含「是代码错了还是规则该改」的判据）。
- 11 条高发失败模式，每条标注「为什么直觉会错」——每条都对应一次真实事故。
- 反向验证章：假红 / 假绿 / 用例没红≠钉住了 三种形态，及表驱动用例为何不能
  共享 t.Fatalf。
- 决策树：不确定放本仓还是宿主、门禁红了改代码还是改规则。
顺带修 README「工程底座」表三处事实漂移：那些数字是 archcheck 从 cmd/ 下沉成
独立包（2026-10-19）之前的旧值，此后未更新——生产代码 11,491 行/66 文件 →
13,336 行/77 文件，测试 84 → 99 个文件，覆盖率 80.4% → 86.6%，依赖方向包数
22 → 24。**该表不在 docsync 判定面内**（docsync 只校验包表），所以门禁不会拦
它——正是本仓「散文不是门禁」那条纪律的反面案例：机器只管得着被写成规则的那部分。
另在 README 导航与 AGENTS.md 开头挂上指南入口。
下游要做什么：无。纯文档，**无 API 变更、无依赖地板抬高**。本仓源码零改动，
宿主按 v0.6.3 写的代码不需要改。

- 文档(release)：记下两条生成器相关的发版纪律（当前版本、生成提交只碰 CHANGELOG.md）

## [v0.6.3] — 2026-10-20

- 杂务(deps)：依赖地板追平上游本轮新版本（llm / obs / mcpkit / concurrency / core）

五个模块到各自最新 tag：
- oagentkit/llm v0.13.0 → v0.14.1
- oagentkit/obs v0.9.1 → v0.9.2
- oagentkit/mcpkit v0.8.1 → v0.8.2
- oekit/concurrency v0.17.1 → v0.18.0
- oekit/core v0.9.2 → v0.10.0
均**无本仓可见的破坏性变更**（逐个读 CHANGELOG + compare diff 核实）：
- llm v0.14.0 新增 `port` 窄接口层（不依赖 eino 的降级链/预算/成本），**根包用
  类型别名保持 API 不变**；本仓只钉了 `llm` 与 `llm/obsx`，两者路径未变。
- core v0.10.0 新增 `config.WithStrict()`（未知键拒载 + 合法键清单 + 近似拼写
  建议），**默认行为不变**（未知键仍静默忽略），严格模式显式开启；本仓只钉了
  `core/version`，不涉及配置装载。
- concurrency v0.18.0 新增 `retry.DoValue`（带返回值的重试），纯增量。
- obs v0.9.2 / mcpkit v0.8.2：Go 地板降到 1.24 + 新增 mcp 自检/目录差集/健康
  聚合/错误分类原语，纯增量。
tools.go 的钉子说明按事实重写：core 钉子从「等于 logx 声明的地板」变为**领先**
（logx v0.7.1 声明 core v0.9.2，本仓钉 v0.10.0——上游给 core 加了 config 严格
模式）；四个钉子的版本与地板关系同步更新。
下游要做什么：依赖地板抬高对下游是**隐式 breaking**——本仓源码零改动，但下游的
go.sum 需要跟着更新（不跑 `go mod tidy` 会报 `updates to go.mod needed`，而报错
不提真正原因）。本仓**无 API 变更**：公开包（protocol / eventbus / netguard /
ptc / archcheck …）的签名与语义均未变，宿主按 v0.6.2 写的代码不需要改。


- 文档(changelog)：用修好的生成器重生成——恢复被旧版丢弃的 449 行正文

## [v0.6.2] — 2026-10-20

- 文档(release)：发版纪律补上「发 GitHub Release」这一步，并补齐下游升级须知表

发现一个流程缺口：v0.3.0 起每个 tag 都配了 GitHub Release（发布页是下游读发布说明
的入口），而 2026-10-19/20 这几轮我**只打了 tag、没发 release**——发布页上
v0.4.0 之后四版全是空的。而 `go get` 的人未必会去看 tag 的注解。
两处补齐：
1. AGENTS.md「发版纪律」第 1 条补上「→ 发 GitHub Release」，并写明 release 正文的
   **第一段必须是「下游要做什么」**（release 页是下游读者的第一落点）。
2. README「下游升级须知」表补 v0.6.1 行与 **v0.3.0–v0.3.2** 行——那三版连续抬了
   依赖地板，对下游是隐式 breaking（不改代码就编不过，报错只说
   `updates to go.mod needed`），此前表里没有它们，下游无从查起。
**下游要做什么**：无（纯文档）。本仓的发布页此前缺 v0.4.0–v0.6.1 四条，已补发
（正文取自各 tag 注解 + 「下游要做什么」段）；本版无 API 变更、无地板抬高。


## [v0.6.1] — 2026-10-20

### 修复(archcheck)：import 规则必须认 negative_follow 豁免；补 CHANGELOG.md 与发版纪律

清 issue #11（真缺陷）、#12（文档化等价表达）、#10（缺 CHANGELOG.md）。
一、#11：import 分支不应用 negative_follow（静默假红）
`forbid_import_regex` 分支此前**不应用** negative_follow 豁免——同一个字段在文本
分支生效、在 import 分支被忽略。后果是**假红**：policy 作者按字段名的自然用法
（import 规则配 import 豁免）写负向前置，实际一条都豁免不掉，该放行的 import 照样
报违规，而报错文案里没有任何「你的豁免没生效」的线索。
失败形态是「门禁变红但红得没道理」：维护者的第一反应是「规则太严」，于是把豁免
删掉，真正的缺陷留在原地。
修法：豁免的判据面统一为「**被匹配的那段文本**」——文本分支是源码行，import 分支
是 import 路径。锚点随之写在路径片段上（如 anchor: "…/internal/engine/" +
forbid_follow: statemachine）。
反向验证：把 import 分支的豁免调用去掉 → TestImportRuleHonorsNegativeFollow 红
（变异已确认打中）。另加一条用例钉住「豁免不得扩大成整条规则都放行」。
二、#12：调用面禁令与路径白名单的等价表达（方案 2，issue 作者倾向）
policy **没有** forbid_call_regex / allow_paths 字段——判据面只有两种（import 路径 /
源码文本）。两种常见诉求用现有字段表达：
- 路径白名单（某第三方包只允许经某适配层引入）→ forbid_import_regex + scope_exclude
- 调用面禁令（某目录禁止裸 fetch(，但几个目录除外）→ forbid_regex + scope_exclude
已写进 archcheck 包文档（含对照表）、README 宿主复用清单、以及 policy 自校验的
「规则无执行体」报错（给出可执行的下一步，并点明「policy 里没有那两个字段，写了
会被 YAML 静默丢弃」）。
为什么不再加第三种判据面：每多一种判据面就多一套豁免语义要记；本包已经因为
「同一字段在两个分支语义不同」吃过一次亏，那次的修法是让两个分支同构，而非加一种。
三、#10：补 CHANGELOG.md + 发版纪律
本仓此前没有 CHANGELOG.md，发版信息只散在 commit message 与 GitHub Release 里。
对「按版本引入」的下游这是实打实的障碍：v0.5.0/v0.6.0 抬了多个依赖地板，**对下游
是隐式 breaking**（不改代码就编不过，报错却只说 `updates to go.mod needed`，不提
真正原因），而下游根本无处可查迁移说明。
- 用矩阵同款生成器（matrix-tools/scripts/gen_changelogs.py）从 git 历史生成，
  7 个版本段（v0.2.0 … v0.6.0）。**发版 commit 的正文就是发布说明的正文**——
  所以发版 commit 必须写清「下游要做什么」。
- AGENTS.md 新增「发版纪律」：① 顺序必须是「改代码提交 → 生成 CHANGELOG 提交 →
  打 tag → 推 tag」（tag 快照里的 CHANGELOG 必须已含该 tag 自己那一段，顺序反了就
  重演上游那条「已发布 tag 的 CHANGELOG 缺自己那段」的缺口）；② 发版 commit 必须写
  下游动作（import 路径变更 / 地板抬高 / 字段改名）；③ 地板抬高对下游是隐式 breaking，
  必须在 CHANGELOG 里点出抬了哪些地板、迁移说明在哪个仓的哪个版本。
- README「下游升级须知」表补 v0.5.0 / v0.6.0 行（表是手写的精简版——生成器每次都会
  重写 CHANGELOG，手写内容活不过下一次生成）。
四、顺带修：docsync 的依赖列改走 go/ast
docsync 此前用正则扫**全文**的引号内容取依赖，于是**文档注释里的示例路径**会被
当成真依赖（本次写完 #11 的文档示例后，README 依赖列多出一项「demo/internal」——
那不是依赖，是举例）。与 archcheck 包当年的同一个病（import 声明有六种以上书写形态，
逐行正则必然漏）同源，故同样走语法树。补一条用例钉住（文档注释/字符串里的
example.com 路径不得进依赖列）。改完后 README 依赖列逐项不变——说明此前的文本扫描
没有别的假阳性（它们要么是真依赖，要么被同一个归并键吸收了）。
make check 全绿（覆盖率 86.6%，24 个包）、race 全过。

## [v0.6.0] — 2026-10-20

### 新增(netguard)：放行面具名且校验层与拨号层共用一个类型，并补上宿主接线件

清 issue #2 遗留项 3（netguard 双 bool → 具名能力）与 #6 的宿主侧卡点。
一、放行面（netguard/allow.go）
`CheckURL(url, allowPrivate, allowLoopback)` 这种双 bool 签名里，两个开关的含义
只能靠调用点约定区分——`CheckURL(u, true, true)` 读起来是「true true」，看不出
意图。而放行面是**安全边界**：写错一位就是一条 SSRF 路径。
- 新增 `Allow{Private, Loopback}`：具名、零值为严（要放行必须显式写出来）。
- `AllowSelfService()` 给「私网+回环都放行」点名意图——那组比特恰好也是 SSRF
  穿透的入口。做成函数而非可变的包级变量：放行面是安全边界，能被别处改写的
  全局变量不是边界。
- **校验层与拨号层共用同一类型**（CheckURLWith / CheckChainWith / DialOptions.Allow），
  映射到上游 egress 也只有一处（Allow.egressOptions）。此前 DialOptions 的两个
  开关与 CheckURL 的两个开关是两套类型，「两层必须同口径」（#6 的行动项）就只是
  文档里的一句叮嘱；共用类型后差异只剩**值**（宿主的选择），不可能各自漂移。
- 旧的双 bool 签名保留但废弃（转发到具名形态），不破宿主 API。
二、接线件（netguard/client.go）
`NewClient` / `NewTransport` 把「守卫挂在传输层」收成一行，并保留宿主自己的
client/transport 注入口——#6 的三个宿主侧卡点（散落接线、测试无法注入 client、
两层口径容易不一致）由此一次性消掉。
「自定义 RoundTripper 挂不上拨号层守卫」这一支**必须报错**而不是悄悄放行：拨号
发生在传输层内部，挂不上就没有拨号层防线，而「悄悄放行」的失败形态是「守卫看起来
接上了，实际没接」。
三、反向验证（四处变异逐个确认用例会红）
- deprecated 包装里两个 bool 写反 → TestDeprecatedBoolWrappersMatchNamedForm 红
- 单一转换点丢掉一个字段 → 5 个用例红
- NewClient 悄悄不挂守卫 → 2 个用例红
- 挂不上守卫时悄悄放行 → TestNewClientRejectsCustomRoundTripper 红
其中前两处变异**第一次没打中**（perl 多行模式未匹配、且 grep 命中的 `return c, nil`
本来就在原代码里），重做并先确认打中才作数；第四处第一次**构建失败**（fmt 导入
闲置），补占位后才是可运行的变异——「变异没打中」与「变异跑不起来」同样证明不了
任何事。
四、文档
- docs/USAGE.md：新增「拨号层接线」小节（#6 的宿主指南：三层纪律——两层放行面同一个
  值、逐 URL 策略取「拨号层最宽校验层逐条」、测试只在测试里放宽回环）；示例换成
  具名形态；「已知局限 rebinding 窗口」那条陈旧描述改掉（拨号层已补上，剩下的
  是接线）。
- README/AGENTS.md：记下这条纪律——**放行面这类安全参数不该用位置敏感的裸 bool
  承载；两处以上用同一组开关时，让它们共用一个具名类型**，口径一致就从「靠记得」
  变成「结构上没法不一致」。
五、依赖地板
追平上游本轮全部新版本：guardx v0.8.2 / llm v0.13.0 / obs v0.9.1 / runtime v0.7.1
/ textx v0.6.1 / mcpkit v0.8.1 / pkg v0.17.0。均无本仓可见的破坏性变更
（pkg v0.17.0 的 BREAKING 在本仓未使用的 stringx.Masker 上；guardx v0.8.1 的
egress 文档修正说明 egress 不校验 scheme——本包的 scheme 白名单是自带的，不受影响）。
make check 全绿（覆盖率 86.7%，24 个包）、race 全过。

## [v0.5.0] — 2026-10-20

### 新增(deps)：依赖地板追平上游最新一轮，并把「本仓用了哪个上游矩阵模块」纳入依赖方向门禁

上游 6 个模块出新版（均为各自最新 tag）：guardx v0.5.0→v0.8.0、llm v0.11.0→v0.12.0、
obs v0.7.0→v0.9.0、textx v0.5.0→v0.6.0、toolgate v0.11.0→v0.12.0、pkg v0.15.1→v0.16.0。
guardx/egress（本仓 SSRF 判据的单源）逐字未变，判据面不受影响。
上游两处搬家，本仓跟随（import 路径变更）：
- toolgate/invoke → guardx/invoke（框架无关的判定环原语不该被 eino 重仓的地板拖住）
- obs/obsx → llm/obsx（eino 耦合的观测适配层该待在重仓）
⚠️ 对下游是**编译期破坏**：本仓公开 API 里的 invoke 类型换了包路径（guardset.Guard /
Set.Evaluate / Stage、tooldisclose.Stage、ptc.Options.Stages），宿主需改 import。
影响清单见 README「下游升级须知」与 issue #9。附带好处：判定环从 eino 重仓挪到
零依赖底座，下游的依赖面变轻。
新纪律：本仓的上游依赖面也要被声明。
此前依赖方向表只管内部包，管不到「本仓用了哪个上游矩阵模块」——而上游搬家的频率
不低（这一轮就搬了两处），那次搬家在本仓门禁眼里**什么都没发生**：import 路径变了、
依赖面变了（判定环从 eino 重仓挪到零依赖底座），却没有任何一处声明跟着改。
修法：用 issue #7 落地的 ext: 机制，把第一方矩阵整体（github.com/oagentkit +
github.com/oekit）登记为受管包面，各包在 requires 里声明自己实际用了哪个矩阵模块
（粒度取矩阵模块级，不是包级——包级会把 requires 撑成 import 清单，每换个包名就是
一次声明变更）。第三方依赖（goja / eino / jsonschema）不在受管面内，由
forbid_import_regex 那几条规则管。
反向验证三处：偷引未登记的矩阵模块 → 红；撤掉用着的 ext 声明 → 红（4 处连带报出）；
撤掉受管面清单 → policy 解析失败、退出码 2（声明失去依据，不会静默变惰性）。
tools.go 钉子按事实重写：llm / textx / obs 三个钉子重新领先于上游地板（llm v0.12.0
是功能前提——旧版 llm 里根本没有 obsx 这个包，钉在 v0.11.0 会直接编译不过；obs 钉子
改指 obs/audit，因为 obsx 已迁往 llm），core 仍等于 logx 声明的地板。
上游问题已提 issue：oagentkit/matrix-tools#17——7 个 oagentkit 模块的**已发布 tag**
里 CHANGELOG.md 缺该 tag 自己的版本段（含 toolgate v0.12.0、obs v0.9.0 两处破坏性
变更）。根因是生成器跑在特性提交之前，生成器口径本身是对的。本轮是靠 git diff
才发现的，不是靠 CHANGELOG。
另修三处陈旧叙述：guardset / netguard 的包注释仍写「toolgate 的 invoke.Chain」，
上游搬家后已非事实；docs/OAGENTKIT-OVERLAP.md 那句「本仓不 import 上游 toolgate」
在 policy 门面转发 toolgate/policy 之后也已不成立。
v0.4.0..HEAD 共 2 笔，make check 全绿（覆盖率 86.7%，24 个包）、race 全过。

## [v0.4.0] — 2026-10-19

### 新增(archcheck)：门禁执行体下沉为可导入包——宿主复用同一套判据，不必各写一遍

解决 issue #7：依赖方向门禁的执行体此前锁在 package main 里，宿主仓够不着。
判据是「AST + 一张声明表」，不认识任何领域概念——机制面，与 protocol/eventbus/
netguard 同类，但锁在 main 里就意味着：各写一遍判据（各判一套，漂移到没人
发现为止），或干脆不写（policy 的依赖拓扑表变成死条文：躺在文件里、看起来有人
维护过，实际一条都不判）。宿主仓 bianque 正是后一种——它的 modules 段反序列化
成 []struct{}，requires 连内存都没进，那张表里 6 个 root 指向已迁走的包目录，
而没有一条会红，因为没有任何东西会去读它。
下沉内容：
- 门禁执行体整体移入 archcheck/ 包（git mv 保留历史），cmd/archcheck 退化为
  纯装配：找 policy → 跑门禁 → 写报告 → 给退出码（286 行 → 76 行）。
- 对外 API：Load / FindRoot / Run / WriteReport + Result.HasBlocking()；
  另单开 CheckDependencyDirection，供只想接依赖方向门禁的宿主直接用。
- schema 类型全部导出（Policy/Module/Rule/FacadeRule/Threshold/Exception/
  NegFollow），宿主读自己的 policy 不用再抄一份结构定义。
- policy 文件路径不再写死 architecture-policy.yaml（宿主那份可能叫别的名字，
  写死会让自校验违规指向一个不存在的文件）。
顺带补上三条判据（都是这次下沉时才暴露的真问题）：
1. requires 支持 ext:<import 路径前缀> 表达跨仓依赖。原先宿主 policy 里写的
   netguard/pii/spill 这些**外仓包名**不是本仓模块 id，buildModuleIndex 直接
   判 policy 解析失败——「只认本仓 id」的判据表达不了跨仓依赖。
2. 跨仓依赖分两层声明：受管面在 policy 的 dependency_direction.ext_prefixes
   （policy 级），逐模块的边在 requires（模块级）。分层是必须的：若两层合一，
   最后一个声明它的模块撤掉声明时整片包面随之消失，那些 import 重新变成不判定
   的，而外部包没有「未登记目录」那道兜底（本仓没有它的目录），这条边就静默
   失管了——门禁的失败形态必须是变红，不能是变绿。
3. 陈旧包根当天即红：modules 段登记的 root 指向空目录 = 声明与事实已偏离，
   判在 policy 自校验这一层（自校验恒开，而依赖方向门禁可被 policy 关掉）。
   这正是 issue 验收口径「迁出的包根当天即红」的落点。
4. 未登记包判定粒度从顶层目录改为**包目录**：宿主的包根通常是嵌套的
   （internal/xxx），按顶层目录判会把整棵 internal/ 误报成一个未登记的包，
   宿主只能把 internal 整个加进豁免名单——那棵树就彻底不受管了。
反向验证（注入违规 → 门禁报错，三处逐个打断确认用例会红）：
- ext: 边不再计入允许面 → TestHostReuseEndToEnd 红
- 陈旧包根检查被摘掉 → TestHostReuseStaleRootIsRed 红
- 未登记包覆盖检查被摘掉 → 3 个用例红
另有一处**变异没打中**（替换正则没匹配、且 grep -c 匹配数为 0 时退出码 1 把
后续测试短路了）——反向验证本身也要被验证，否则「没打中」会被误读成「用例够
有力」。重打一次才确认用例真的能红。
端到端用例走**宿主会走的完整路径**（archcheck.Load → archcheck.Run → 结果），
夹具是结构不同的假宿主仓（嵌套包根 + 跨仓依赖 + 陈旧包根 + 未登记包），
而不是本仓目录——夹具是契约的示例，示例带本仓味道就把契约钉死在「本仓包根长
这样」上了。另在 /tmp 建真实外部模块 import 本包跑通一遍，确认宿主不需要
复制实现。
搬迁副作用两处，都按纪律处理而非绕开：
- 新包不在 cmd/ 下，而 cmd/ 在域词规则与 printf 回调规则的豁免名单里——门禁
  搬进内核后，它自己开始被自己判：告警→警告、运维→合成词「禁词」、printf
  回调夹具拆开写（拆开只改源码文本，拼出的夹具内容逐字不变）。门禁拿自己开刀
  的样子，正好说明这条红线在起作用。
- .gitignore 的 /archcheck（为旧版根目录二进制写的）把整个新包目录静默排除
  出版本库——git status 里什么都不显示。构建目标早已改成 bin/$(BIN)，该条目
  是陈旧物，已删并把这条坑记进 .gitignore 注释。

### 修复(harness)：深度评审七条纪律的逐条兑现——门禁自校验、代次单一真相、闸凭凭证、编号唯一、契约单源、枚举归一、拨号层闭窗

上一轮深度评审找出七处「失败形态是门禁变绿 / 静默给错答案」的真缺陷，本批逐条偿还。
每条都配了反向验证用例（注入违规 → 门禁报错 / 用例转红），并把结论写进 AGENTS.md。
1) 门禁自己也要能被检查（cmd/archcheck/selfcheck.go、imports.go）
   - 判定面改三态：旧实现 `if r.OnlyTest != isTest` 把「生产与测试都扫」这一态
     变成「所有测试文件被整体跳过」，三条 ignore_test:false 的规则集体对测试文件
     失明——失败形态是门禁变绿。
   - import 规则改走 go/ast：旧实现逐行正则匹配**源码文本**，`^\s*"errors"$` 只认
     「整行就是一个裸路径」，单行 import / 别名 import / 空白导入全漏，而本仓大量
     使用单行 import。
   - 阈值判定面交叉校验：scan_roots 与 modules.roots 无交叉校验，guardset 登记在
     modules 却漏在 scan_roots 之外 → 「超长文件」「god object」两条阈值对它从不生效。
   - 例外到期即失效：until 字段解析后再没人看过，该清偿的债可无限期豁免。
   - 规则无执行体即报错：两正则皆空时旧实现 return nil,nil 静默跳过，却仍计入
     「N 条规则」。
   顺带清偿一处被上述漏洞藏了很久的真实违规：ptc/bytebudget_regression_test.go
   用的还是 stdlib "errors"，而 AGENTS.md 写着全仓已统一切到 pkg/errors。
2) 派生状态只能有一个真相（netguard/policy.go）
   策略代次此前用策略指针内的 gen 与全局 policyGen 两个变量记同一件事，而全局代次
   来自非原子的读-改-写：并发写者读到同一个旧代次各自算出同一个 next，两次交换拿到
   同一个代次（实测 64 次并发交换只推进 54 代）。代次一重复，轮询方看到 6→6 就以为
   没换过，而策略确实换了一次——「换过没有」给不出答案。改走 CAS 循环，代次从观察到的
   旧策略推导，天然唯一、单调、不回卷。
3) 独占闸的释放要凭凭证（commitgate/commitgate.go）
   Release(c) 靠 (状态,分支,序号) 三元组认人，而三元组不唯一：分支名/agent 名常同名，
   且包文档推荐的「落库失败→回滚→重试同一份快照」重试时三元组逐字相同。实测 8 路同名
   并发提交出现 4 个赢家，而失效是静默的（Commit 返回 nil，宿主无从察觉）。
   新增 Acquire(c) (Lease, error) + ReleaseLease(Lease)：占用令牌由闸铸造，释放只认
   令牌不认内容。
4) 确定性编号必须唯一（protocol/report_citation.go）
   AssignEvidenceIDs 用「数组下标+1」发号且跳过已有编号的条目，与模型自报编号一撞就
   同号；而 CheckEvidenceRefs 只查「引用了但不存在」，同号时该编号确实存在，故一声不
   吭。改为先收齐占用集再发号，模型自报的重复编号走 warn 通道不改写。
5) 跨包契约的规则只能有一份（toolname/、ptc/、tooldisclose/）
   tooldisclose.SignatureOf 与 ptc 的绑定名校验各写一份规范名与净化规则，漂移后
   「工具明明在目录里，程序就是调不通」；更糟的是旧版用例把「派生形态会被拒」断言成
   预期行为，测试绿着、文档绿着、功能是坏的。新增 toolname 包（IsJSIdent + Normalize）
   作单一真相，并把用例改成钉「派生→装配→执行」整条链。
6) 枚举位必须归一（protocol/report_normalize.go）
   五处枚举位此前各自没做大小写与空白归一，逐个字段补必然漏网。收成一处口径
   （ToLower+TrimSpace），并用枚举式用例 TestAllEnumFieldsAreCaseNormalized 把
   「枚举位必须归一」钉成机械判定——新增枚举位若忘了归一，用例直接红。
   其中 scope 的 "OUT" 形态此前被翻成 "in"（语义反转），且 scope=out 时 redirect 必填
   的联动校验不再触发。
7) 出站守卫的窗口要在拨号层闭合（netguard/dial.go）
   CheckURL 是写面校验，实际请求在之后由 HTTP 客户端再解析一次 DNS，两次之间权威
   DNS 可以改口——活的 SSRF 窗口。新增 DialFunc：名字层判据 + 拨号前逐 IP 过 egress
   + 建连后核对真实 RemoteAddr（闭合 TOCTOU 的关键）。
   刻意不用上游 urlguard.SafeDialFunc：它带自己的一份 IP 清单，等于引入第二套 IP
   策略，而漂移的形态是静默放行。本包的判据仍是 egress，权威只有一份。

## [v0.3.2] — 2026-10-19

- 杂务(deps)：升级 oagentkit 八模块到最新，tools.go 注释同步事实

agent      v0.8.0  -> v0.10.0
guardx     v0.4.1  -> v0.5.0
llm        v0.9.0  -> v0.11.0
mcpkit     v0.6.0  -> v0.8.0
obs        v0.5.0  -> v0.7.0
runtime    v0.6.0  -> v0.7.0
textx      v0.4.0  -> v0.5.0
toolgate   v0.9.0  -> v0.11.0
oekit 侧 core / logx / pkg / concurrency 已是最新，未动。
## 变更面
八个模块的新版本全部是纯依赖地板对齐，零 .go 源码改动（逐仓 diff 验证）。
本仓源码无需跟着改，也未出现破坏性 API 变更。
## go.sum
214 行不变，三方条目逐字未变，无新增三方依赖。
第一方 16 行仅版本号与哈希变化。
## tools.go 注释同步
钉子的性质变了。agent v0.10.0 声明 llm v0.11.0 / obs v0.7.0 / textx v0.5.0，
logx v0.7.1 声明 core v0.9.2，与此处钉的版本完全相等。
也就是说钉子不再领先于上游地板，作用退化为两件更朴素的事：让这四条 require
在 go.mod 里显式可见（而不是仅以传递依赖身份藏在 agent / logx 的 go.mod 里），
以及记录本仓实际验证过这些版本。注释已按事实改写，不再沿用抬高地板的说法。
## 验证
- make check 全绿：lint 0 issues，覆盖率 83.3%，架构门禁 7 规则 + 3 门面包 + 22 包依赖方向
- go test ./... -race -count=1：24 包全过，无数据竞争
- go build -tags tools ./...：钉子有效
- go mod tidy -diff：无差异，幂等


- 测试(archcheck)：门面包门禁的反向验证固化——facade.go 覆盖率 0% → ~90%

## 发现
`cmd/archcheck` 有 8 个测试函数，但**全部覆盖 `deps.go`**。而
`harness-facade-stays-facade`（AGENTS.md 里写着「五条判定路径均已反向验证」的那道
go/ast 门禁）的执行体 `facade.go` 覆盖率为 **0.0%**，`scan.go` 仅 13.3%。
对照极清楚：**较老的依赖方向门禁有 8 个端到端测试，最新、最复杂的那道反而零覆盖。**
AGENTS.md 那句「五条判定路径均已反向验证」指的是**写代码时手工注入过一次**，
仓库里没有任何东西保证它下次还成立。而这道门禁的失效方向恰好是**变绿**不是变红：
    1. 类型定义漏判   type X = ... 与 type X struct{} 混淆 → 真定义放行
    2. 方法漏判       func (T) M() 不被拦 → 绕开上游类型演进
    3. 多语句漏判     转发函数里顺手加一行 if —— 这正是纪律要防的
    4. 非上游调用漏判 调本包自己的函数，字符串不含任何上游特征
    5. 非允许 import  漏判 → 转发目标不再指名道姓
任一条失效的表现都是「门禁变绿」——与上游并存的第二份实现从此无人拦截，正是
`docs/OAGENTKIT-OVERLAP.md` 警告的事，且没有任何信号。
## 修法
新增 `cmd/archcheck/facade_test.go`（10 例），复用 `deps_test.go` 的 `writeFixture`
惯例：造最小仓 → 跑 `facadeViolations` → 断言违规条数与文本。
覆盖上述五条判定路径，另加分支语句（单条但非调用）、`.`/`_` 形态导入、空壳函数体、
测试文件跳过；**同时钉住四种合法形态必须零违规**——门禁最容易的失败不是漏判而是
误伤真转发，误伤一次维护者就学会忽略它，规则随即名存实亡。
`facade.go` 各函数覆盖率：0.0% → 78.9%~100%；全仓覆盖率 **80.4% → 83.3%**。
## 写用例时自己撞了两次门禁的口径（都判对了，是用例错）
1. 起初把 `allowed_imports` 的类型拿来**构造**返回值（`return sig.T{N: n}`），
   门禁报「唯一语句不是对上游包的调用」。查 `architecture-policy.yaml` 的 facades 段
   才确认口径：`allowed_imports` 是**签名本身**要求的依赖——policy 段注释写得很清楚
   「io/fs 是 LoadOverrides 的参数类型，eino/components/tool 是 WithAuditGate 的
   出入参。两者都不提供任何实现」。构造就不是转发。
2. 「测试文件应被跳过」那条用例里，我把**生产文件本身**写成了违规却断言零违规。
两处都是门禁判对、用例写错，已按真实契约改正。口径本身也补进了用例注释——
`allowed_imports` 只服务签名、不参与构造，这条不看配置根本猜不到。
## 验证
`make check` 全绿：fmt / vet / lint / test / 覆盖率 83.3%（门槛 60%）/ 架构门禁
7 条规则 + 3 个门面包 + 22 包依赖方向 / docsync 一致 / go.mod 无 replace /
go.work 未入库。`-race` 全过。


## [v0.3.1] — 2026-10-19

### 修复(netguard)：guardx → v0.4.1，闭合 0.0.0.0/8（"this network"）SSRF 缺口

上游采纳了本轮提的 oagentkit/guardx#2，v0.4.1 把 `0.0.0.0/8` 补进 egress 的
`ipv4ExplicitBlocks`，本包随之闭合——**没有在本仓私补一份网段表**，修复走的是
上游单源，本包只负责消费与钉住行为。
## 缺口的准确形态
此前 `0.0.0.0/8` 除全零地址外整段放行，是可用的 SSRF 路径：
    http://0.1.2.3/x            放行
    http://0.255.255.255/x      放行
    http://[::ffff:0.1.2.3]/x   放行（IPv4-mapped 同样漏判）
根因：`netip.Addr.IsUnspecified()` 只认全零这一个地址，而该段不在显式段表里。
**本仓此前没发现，是因为那条用例只测全零地址**——`http://0.0.0.0/x` 本来就能过
`IsUnspecified()`，用例一直是绿的，缺口却是真的。这正是「测试覆盖了看起来相近的
那一例，却漏了真正出问题的那一段」的典型。
## 新增 thisnetwork_test.go 钉住修复后的行为
- `TestThisNetworkRangeBlocked`：`/8` 余下整段（0.1.2.3 / 0.128.0.1 / 0.255.255.255 /
  0.0.0.1）在**放行面参数全开**时仍须拒——它们既非回环也非私网，旗标碰不到它们。
- `TestThisNetworkIPv4MappedBlocked`：IPv4-mapped 形态同样被拒，且断言它走的是
  **IP 判定**而非名字层（否则「解析器报错」也会让用例蒙混过关）。
- `TestCheckURL` 表补两条字面量用例。
## 顺带记下一条上游的刻意契约
`0.0.0.0` 这个**全零字面量**跟回环同组、由 `allowLoopback` 管辖；而 `/8` 余下整段
无条件拒、**不受任何放行旗标影响**。两者同属一段但待遇不同，egress 的
`IsUnspecified` 分支注释里写明了理由。我第一版用例把 `0.0.0.0` 也断言成「无条件
拒」，跑出来是红的——是断言写错、不是修复不对，已按真实契约改正并把这条区分
显式写进用例注释与包头。
## 验证
- `go build -tags tools ./...` 退出 0
- `go mod tidy -diff` 干净
- `make check` 全绿（覆盖率 80.4% / 门槛 60%，lint 0 issues，架构门禁 7 规则 +
  3 门面包 + 22 包依赖方向，docsync 一致）
- `-race` 全过
- go.mod 只动一行（guardx），go.sum 只换 guardx 两条，**三方条目逐字未变**
至此本仓第一方依赖全部为远端最新：agent v0.8.0、guardx v0.4.1、runtime v0.6.0、
toolgate v0.9.0、llm v0.9.0、mcpkit v0.6.0、obs v0.5.0、textx v0.4.0、
oekit/pkg v0.15.1、oekit/concurrency v0.17.1、oekit/logx v0.7.1、oekit/core v0.9.2。

## [v0.3.0] — 2026-10-19

### 新增(gates)：把 lint 接进 make check，CI 钉 runner 与 action 版本

## lint 门禁原先在哪儿都不跑
`lint` target 既不在 `make check` 的依赖链里，也不在 ci.yml 里——它只在你手动敲
`make lint` 时才存在。后果很具体：本轮升级顺手清掉的 6 条既存问题（archcheck 的死
变量、protocol 的孤儿谓词、两处 errcheck 等）在修完之后**没有任何机制保证它们不会
回涨**。
按 AGENTS.md「门禁本体全部在 make check 里，CI 只执行它」把它接进 check，并在
Makefile 的 lint target 上写明理由：CI 只负责装二进制，不在 ci.yml 里再跑一遍
lint——那正是「门禁本体在 ci.yml」，与本仓一贯的分工相反。
反向验证（AGENTS.md：没见它触发的门禁等于没有门禁）：往 statemachine/step.go 注入
一个未使用函数，`make check` 在 lint 这一步即失败并定位到 `statemachine/step.go:81`，
连 test 都没跑到；样本已还原。
## CI 版本钉死
- `runs-on: ubuntu-latest` → `ubuntu-24.04`：GitHub 公告该标签自 2026-10-19（今天）
  起迁移到 Ubuntu 26，迁移后镜像的 Go/工具链环境可能与本机不同——门禁在与开发者不同
  的镜像上跑，绿灯可信度要打折。钉死 major.minor，让迁移变成一次显式的、可 review
  的升版动作。
- `actions/checkout@v4` → `@v7`、`actions/setup-go@v5` → `@v7`：两者原先仍以 Node.js
  20 为目标、已被 GitHub 弃用警告，v4/v5 版本经 GitHub API 核实为最新可用。
- 新增安装 golangci-lint 的 step，版本钉 **v2.14.0**（与本机验证过的版本一致；本机是
  v2.15.0 时 CI 与本地可能给出不同判定，那种差异应该在升版时显式发生，而不是随某台
  机器的 brew upgrade 悄悄发生）。
## netguard 补 `.localdomain`，与上游 urlguard 收敛
上轮记录了 netguard 与 `oekit/pkg/urlguard` 域名后缀层的漂移，这次按「收紧方向合
并」处理：把 `.localdomain` 补进默认后缀名单，**纯收紧、不放松任何放行面**。
依据是本包既有的设计口径——「私有命名空间无条件拒绝、不看 allowPrivate」，因为
allowPrivate 的语义是放行 RFC1918 私网**地址**，不是放行私有命名空间的**主机名**。
`.localdomain` 正是这类后缀，且此前是真缺口：`.local` 的点分边界匹配不到
`svc.localdomain`。
合并后本包成为 urlguard 的严格超集（多 `.lan`，因它是局域网约定域而非 RFC 保留段、
urlguard 那边是收敛方向）。包头注释同步更新为「已收敛」的事实陈述。
测试钉三处：解析前拦截、放行旗标无关、扩展表摘不掉默认项；另加
TestLocaldomainSuffixBoundary 专门钉住**点分边界不是子串**——命中 `svc.localdomain`
但不命中 `notlocaldomain.example.com` 与顶级域 `localdomain` 本身。反向断言走
checkHostName 而非 CheckURL，避免为验名单匹配去付 DNS 的代价（沿用既有写法）。
## README 包表
netguard 行数 719 → 741，职责列补上与 urlguard 的分工指针。
注：docsync 对行数有 25% 容差（locTolerance），本次 3.1% 的偏差本就不会报——该容差
是刻意设计（严格相等曾因 protocol 差 1 行连续三次假红 CI）。这里主动更新是让表格与
事实对齐，不是为了过门禁。
验证：make check 全绿（覆盖率 80.3% / 门槛 60%，架构门禁 7 规则 + 3 门面包 + 22 包
依赖方向，docsync 一致）、lint 0 issues、-race 全过。

### 新增(ptc)：判定环拒绝与门禁故障分成两个 ErrorKind，别再把策略拒绝说成程序 bug

# 问题
程序内 host.<tool>() 被判定环拒绝后，错误经 goja 的 rejection 出来只剩字符串，
收口处一律归成 runtime。于是两件事被抹平：
  - **给模型的建议是错的**。runtime 类别的建议是「确认调用参数形状与该工具的契约
    一致」「用 try/catch 包裹可疑调用」——对一次策略拒绝，这两条都指向错误方向：
    模型会去改参数形状、改程序结构，而真正的原因是策略不允许。模型被指错方向，
    就会反复改一段本来没问题的程序。
  - **调用方分不出该修谁**。守卫判定拒绝（正常裁决，程序改不掉）与守卫自己崩溃
    （宿主门禁故障，程序更不该改）在 Error 上都只是 runtime。
# 做法
判定环的类型化结论用 Result 上一个**不导出的字段**带出来（stageErr），在
PromiseStateRejected 收口处分类成两个新 ErrorKind：
  - stage_denied：守卫判定拒绝（invoke 侧 Cause 为 nil 的正常裁决）
  - stage_failed：守卫自己报错/崩溃，或压根没有可判定的对象（ErrStageFailed /
    ErrEmptyName）
判据用 errors.Is 命中 invoke 的公开哨兵，**不读环节名**——归口环节名是上游包内
未导出常量，本包引用不到；哨兵是它留的契约（这也正是 #6 让上游导出统一入口的
另一个理由：环语义只有一份，分类口径也不该各写一套）。
两个 kind 都进了 TestSuggestionsPresentForEveryFailureKind 那张「每个失败类别都必须
有面向模型的建议」的表，并各配了说清「改程序没用」的文案。
# 影响面
- ErrorKind **新增两个取值，既有取值一个没改**：runtime / deadline / budget_* /
  pending / bad_result / syntax / binding_collision 语义均不变，调用方按旧值
  分派的逻辑对「门禁拒绝」这一类会收到新值——而那正是它此前收到假值 runtime 的地方。
- Result 只多了一个不导出字段：encoding/json 跳过它，对外 JSON 形状不变；
  包外用字段名构造的结构体字面量不受影响。
- stageErr 只在**未被程序接住**的路径上被读到：程序自己 try/catch 掉的拒绝
  仍是成功返回（语义不变，TestStagesDenyIsCatchable 钉住）。
# 验证
make check 全绿：覆盖率 80.1%（门槛 60%）、架构门禁 7 条规则 + 22 包依赖方向、
docsync 22 包一致、无 replace、go.work 未入库。
反向验证四个变异均确认用例会转红：守卫崩溃误判为判定拒绝、收口处断掉类型化通道、
拒绝类别复用 runtime 那套建议、前缀互换。

### 新增(toolfail,guardset)：失败契约补两个维度；守卫拒绝要能合成一条

## toolfail：重发安全与重试价值是两件事
本包原先只回答「重试有没有收益」（Retryable），而自动重试真正要问两问：
  ① 重试有没有收益？                     → Retryable / Backoff
  ② 原样重发安不安全（会不会重复施加副作用）？ → SafeToResubmit
两问的答案可以相反，且相反的组合最危险：「值得重试」不等于「可以重发」。
超时正是典型——多半值得重试，可服务端那边请求可能已经执行完毕，原样重发
就把一次调用变成两次副作用。原有单一维度表达不出这件事，调用方只能要么
盲目重试（制造重复副作用），要么因噎废食放弃退避（失去可恢复性）。
缺省一律保守：只有能证明服务端未受理本次调用的类别才置真（限流即如此）。
例外是超时类——超时的定义就是「不知道对方执行到哪」，当成安全重发等于把
一次调用放大成两次。宿主对「我这个工具幂等吗」确有知识，故 Failure 上挂
三态 Idempotency 逐次覆盖；用三态而非 bool 是因为「没想过」与「确实不幂等」
必须区分开，否则缺省口径会被静默改成「一律不安全」。
一并补面向模型的 Remediation，与给人看的 Message 刻意分开，并新增
Guidance() 作为**确定消费方**——判据建了没人读等于没建。
写完把九个类别的 Guidance 真正渲染出来读，暴露了三类文案缺陷，已一并修掉：
transient 与 permanent 等四条重复（「不要重试」说两遍），guardset 留痕拼出
「。；」，以及最严重的一条——自报类文案写「可以直接重试」而 Guidance 说
「先确认上一次调用是否已经生效」，同一段提示词里两个相反指令，模型无从取舍。
根因是 Remediation 与 Guidance 都负责「能不能重试」。已定死职责边界：
Remediation 只讲「这是什么情况、我该改什么」，机械结论一律由 Guidance 追加，
并用两条契约测试钉住（禁止类别文案出现「重试/等待/人工介入」；逐类渲染全文
断言无相反指令）。
## guardset：一组独立守卫一并求值，多条拒绝合成一条
本仓守卫此前是并列的独立机制，接进执行链的活儿全推给宿主，于是「顺序 /
冲突 / 合成」这三个必须全局一致的判定变成每个宿主各写一遍。同一个调用在 A
宿主被报成不可重试、B 宿主被报成可重试，两边都没写错，只是合成规则不同。
与 invoke.Chain 的关系是**组合而非替代**：Chain 已提供顺序执行、首个拒绝即
短路、留痕与 panic 兜底，本包不重复其中任何一条，只补它按设计不覆盖的那面——
链在第一个拒绝处就收口，后面的守卫永远没机会说话，模型一次只听得到第一个
理由。guardset 把自己接成链上的单独一环，链照旧短路，而这一环内部已把该说
的都说了。需要严格先后时仍应直接排多个 Stage。
裁决取保守侧：只有**全部**拒绝都可重试才算可重试。理由是只要有一条拒绝
不可恢复，重试整个调用就绝无可能成功；取「任一」的话，一条永久性拒绝会被
无关的可重试拒绝洗白，退避层于是把一次策略否定拖成无穷重试。
装配期三条硬约束都直接失败而不留到运行期：空集合拒绝构造（没人看着的调用
不该看起来能用）、守卫不得无名、不得重名。运行期另有两条 fail-closed：守卫
自身报错或 panic 一律计入不可重试的拒绝，与「正常判定得出的拒绝」区分开；
拒绝却不给理由时补出中性文本——空指引会让模型收到一条没有内容的否定。
README 包表与边界图同步（新增 guardset、toolfail 与 tooldisclose 行数口径
变化），由 docsync 机器校验。

### 新增(tooldisclose)：拒绝要能对模型说话，否则两类结局要求相反动作时模型只能猜

调研 octop-harness 时记下一条差距：它的守卫把拒绝统一翻成模型可见的
ToolMessage(status="error")，而本包的链上 Reason 过去只回
「工具 schema 尚未取回（工具 "x"）」——说清了发生了什么，没说下一步做什么。
而本包两类结局要求模型做的动作恰好相反：
  目录内未加载 → 必须**重试**（且 Check 已把工具激活，重试必成）
  不在目录内   → 必须**换工具**（不存在的东西不会因为重试出现）
指引缺位时，模型在第二类上继续重试，退避层也无能为力（它只看 Retryable），
一段策略否定就被拖成空转。
补 GateError.Guidance()，并把 Stage 的 Reason 换成指引。指引文本本身带工具名，
审计要的「哪次调用被拒」不会因此丢失，故同一字段同时满足留痕与模型两头，
不必再开一个字段——留成可选项正是「建了没人读等于没建」的由来。
不做成可选能力，也不让各宿主各拼一份文案：同一类失败在不同环节给模型不同
措辞，而模型对措辞的敏感度远高于人，措辞漂移会直接变成行为漂移。
注：本包早已实现「拒绝即激活」（gate.go 的 Check 顺手 Load 该工具并记
Activated=true），自愈不是缺口，缺的一直是模型侧那一半。

### 新增(archcheck)：包间依赖方向门禁，modules[].requires 从声明变成可执行

modules 段早已逐包登记 {id, roots, requires}，requires 是本仓 22 个包之间
**允许**的依赖方向；但 policy.go 把 modules 反序列化成 []struct{}，requires
连内存都没进，于是 README 宣称的「域词污染 + 依赖方向」里「依赖方向」那一半
没有执行体。且声明已经开始漂移：watchdog 写 requires: []，实际 import 了
eventbus，并在 policy 里附了一句「同仓叶子引用不算模块间方向约束」自我合理化。
执行体放 deps.go（go/ast 判 import，不走正则表），两条判定路径：
1. 越界依赖——包 A 的生产代码 import 了包 B，而 A 的 requires 未登记 B；
2. 未登记包——顶层目录含生产 Go 文件却不在 modules 段。这条更隐蔽：未登记的
   包对本门禁完全隐形，任何模块都能 import 它而不判违规。
依赖缠绕是本仓最难靠 review 发现的债——零报错、零告警、构建全绿，每个单点
import 看起来都合理，只有把全图摊开才看得出 A→B→C→A。其余七条规则查的全是
「写法」（用什么词、import 什么、goroutine 怎么起），没有一条查包与包之间
允许连成什么形状。
新规则上线第一次跑就抓出 commitgate 从未登记过——它早已在 README 包表里、
早已被 docsync 计入行数，却在依赖拓扑上完全没人管。同时把 watchdog 的
requires 改成真实依赖 [eventbus]，删除那句自我合理化。
声明自身出错一律响亮失败（退出码 2）而不是静默降级：requires 引用错模块 id
不会让任何人看到错误，却会让「这条边是否被允许」变成永远为真的判定——门禁
看似在跑，实则对该边完全放行。
判定面只含生产代码：依赖方向决定交付物里出现什么，测试依赖是临时的、可随
用例删掉的，纳入只会逼出「只为共享夹具而做的注册」噪声。
三条路径均已反向验证：注入越界依赖 → 报错退出码 1；造未登记目录 → 报错退出
码 1；requires 拼错模块 id → 退出码 2 并点名。嵌套 roots 下的归属按最长路径
前缀判定（遍历是递归的，不过滤会把内层包的文件记到外层名下——这条是写测试
时反向验证才发现的真 bug）。

### 新增(tooldisclose)：SignatureOf 从 schema 派生调用形态，消除第二份描述

对标 OpenCode codemode/src/tool-schema.ts 的 inputProperties：他们从工具声明的 schema
自动派生签名与必填/可选标注，schema 是唯一事实源。
我们的 Signature 此前要宿主手写，等于同一组参数有两份描述。加了参数忘了改签名，
模型就按旧签名调用——传不进去的、或多传的，两种都表现为「工具明明支持，模型却
调不对」，而排查时极容易先怀疑模型。派生把「靠人记得同步」变成「不可能忘记」。
三条派生不出来时**返回错误**而不是硬凑：
- 参数名不是合法 JS 标识符 → 改名会改掉 JSON 键名，等于把参数悄悄换掉；
- Required 点名了 Properties 里没有的参数 → schema 不自洽，静默忽略的话该参数
  对模型不存在而宿主以为必填，两边认知分叉且都无信号；
- 退化占位 schema → 派生空签名会让程序照跑、宿主收不到任何参数，表现为「工具调了
  但什么都没传」，比当场报错难查。
参数顺序**必须确定**：Schema.Properties 是 map，Go 迭代顺序随机，而签名是给模型
读的文本，顺序漂移等于让模型每次看到不同的签名。取必填按 Required 的声明序
（宿主已表达过意图）、可选按名升序——这个顺序同时满足 ptc 侧「可选排在必填之后」
的约束。
新增端到端用例（外部测试包 ptc_test，可同时 import 两边而生产代码互不依赖，
deps 门禁只判生产代码）：派生 → 装配 → 执行一条龙，并断言第二个位置实参确实落到
签名里的第二个形参。两侧各验一遍是不够的——派生端与解析端各有一套「什么算合法
标识符」的判据，不一致就会出现「这里派生得出、那边解析不了」的夹缝。
反向验证：可选参数降序 → 端到端用例变红（第二个实参落到 since 而不是 count）；
去掉排序 → 确定性用例变红；不校验 Required ⊆ Properties → 一致性用例变红；
放过退化占位 → 退化用例变红。
只新增 tooldisclose/signature.go 与测试，未触碰并行开发中的 gate.go / stage.go。

### 新增(ptc)：Signature 支持可选形参，工具不再被迫全参数传值

对标 OpenCode codemode/src/tool-schema.ts 时发现的能力缺口：他们的 inputProperties 会
给每个参数标 required/optional，可选形参在生成的签名里是 name?。而我们的位置绑定
此前**根本无法表达可选**——少传一个参数直接被元数校验拦下。
后果很具体：工具一带可选参数，模型只剩两条路——为不填的槽位瞎编一个值，或者干脆
不用 Signature 退回手写 JSON。两条都是「让模型适配工具的缺陷」。
改法：形参名尾部 ? 标记可选，元数下限取必填个数；未提供的槽位**不出现在 JSON 里**，
而不是写成 null。这是两件不同的事——宿主收到 {"count":null} 可能走进显式的空值分支，
"没给 count" 才是走默认值。显式传 undefined 与不传等价（模型写 host.f("h1", undefined)
是很自然的写法，它不知道省略实参要靠 arguments.length）。
另加一条约束：可选形参必须排在必填之后。中间可选、后面又有必填时，模型为了传后面的
必填项就得先把前面的空位填上——填进去的值是编的。与其让它看起来能用，不如装配期拒。
过程中三次补丁写坏、两次是**假绿**：
1. 生成的对象字面量少一个 } → SyntaxError；
2. 重写时又把 __ptcName 当首参传给下游（此前修过的同一个 bug，这次是两个分支只改了一个）；
3. required 默认留在零值，导致「没有可选形参」这条最常见的路径下限塌成 0，少传照样放行；
4. 反向验证的注入把 !==undefined 换成 count，if(count) 对 undefined 同样为假，
   行为没变却看起来「注入无效」——重构成真正无条件写入后才复现出 {"count":null}。
反向验证三条：可选形参无条件写入 → 2 个用例变红；全必填 required 退零 → 1 个变红；
放开「可选排在必填之前」 → 1 个变红。
又一条自查：清理路径上的门禁会挂死而不是报红那类问题，本轮在重写时特意让每处改动
都先跑既有测试，避免同类退化。README 行数已更新（工作区状态，未随本提交入库）。

### 新增(netguard,pii)：策略变更订阅，把「换过没有」从可查询变成可响应

两个包的运行时策略都已并成单 atomic.Pointer、每次交换递增代次，但代次只是**信号**：
调用方得自己起轮询循环，前后各取一次比对。轮询有三件它解决不了的事——空转期仍在读
全局状态；两次轮询之间换了两次只能看出「变了」看不出「变成了什么」；代次单调只保证
「不漏掉发生过变更」，不保证「每一代都被谁记下过」。
订阅把这件事一次做完：变更发生的那一刻由写侧直接把新快照推给订阅者手上。
这也正是 OpenCode 治自己 #52333（配置热重载窗口绕过 deny 策略）的手段——他们用
ctx.<domain>.reload()，我们此前只有代次没有 reload 的等价物。
公开面两包同形：
  type Subscription uint64
  var ErrNilSubscriber error
  func Subscribe(fn func(*Policy)) (Subscription, error)  // netguard 传指针
  func Subscribe(fn func(Policy)) (Subscription, error)   // pii 传值
  func Unsubscribe(h Subscription)
四条硬语义，每条都有对应用例钉住：
- **通知在锁外**。订阅表用 atomic.Pointer 整体替换而非读写锁——读写锁只能让
  「取名单」不阻塞，「遍历名单并逐个调用」仍落在读锁里。通知遍历一次快照，读侧无锁、
  永远看不到改了一半的名单。
- **订阅者 panic 不影响策略面**。一次成功的交换已经落地，让某个订阅者抛异常就把它
  说成「交换失败」等于把已发生的事实改写成没发生。逐个 recover，其余继续收到。
- **回调内可重入**。回调里再调 SetPolicy 是允许的（无锁，不会自锁），但会再触发一轮
  通知，递归由订阅者自己收敛——本包有意不做环检测，两个头注都写明了。
- **被校验拒绝的变更不通知**。netguard 的后缀全量校验失败时不得触发，否则订阅者会
  收到一次根本没发生的变更。
另修一处测试基础设施缺陷：t.Cleanup 跑在**测试 goroutine 本身**上，而清理要写策略
于是要走通知路径——若通知真在锁内（正是重入用例要抓的缺陷），清理自己也会撞锁卡死，
死锁就从「那个用例红」退化成「整个二进制挂到 Go 的总超时」。现在整段交互放在 goroutine
里、测试体只做 channel select、清理限时等待。实测：注入「通知持锁」后从 90 秒挂死变成
7 秒报红。
worker 侧自查出两处自己测试的缺陷（注入点打在校验之后故被拒路径走不到；重入用例在
Subscribe 的立即回调里触发、通知路径根本没被调用过），均已修正并重新验证。
反向验证 12 条注入全部变红：校验前就通知 / 通知放进 mutex / 去掉 recover /
Unsubscribe 空实现 / 去掉注册时立即回调 / 只通知第一个订阅者（两包各一套）。
ptc 顺带拆分：新增功能把 ptc.go 顶到 438 行越过 max_file_lines。接缝取在「一个工具调用
在沙箱里长什么样」——预算闸、调用记账、标识符净化、调用形态包装——与「Run 的编排流程」
本就是两件事，拆出 binding.go（ptc.go 334 / binding.go 142）。拆是为了过阈值，
但接缝本身是自然的。

### 新增(ptc)：类型化绑定与程序内工具发现，Code Mode 的两半接上

对标 OpenCode packages/codemode 后补的两条。
一、类型化绑定。此前模型必须在程序里手写 host.get_logs('{"host":"h1","n":3600}')。
现在 Binding 可带 Signature: "get_logs(host, count)"，模型写 host.get_logs("h1", 3600)，
ptc 把形参打包成 JSON 交给宿主：少一层引号转义、少一份重复参数名，宿主那侧拿到的是
结构化入参不必再解析一次。
**Signature 由宿主给，ptc 不从 schema 生成**：工具的参数模型是宿主资产，ptc 若要
生成调用形态就得依赖宿主的 schema 类型，那等于把工具目录的形状泄漏进内核。故只约定
一个极简形态串 name(a, b)，由宿主用自己的目录翻译。ptc 侧不认识任何 schema 类型。
形参校验全在装配期：形态串非法、形参重名、形态里的函数名与绑定名净化后的标识符不一致，
一律以 ErrorKind=binding_signature 拒掉，不进 VM。第三条最隐蔽——两侧不一致时静默取
其一，模型调用的名字和实际挂载的名字会对不上，而两边都不报错，「怎么调都不对」是最难
查的一类故障。元数校验放在 JS 侧，报错直接指向形参名与实参数；交给宿主的话宿主看到的是
缺字段的 JSON，报错多半指向内部反序列化，读起来像宿主坏了。
二、程序内工具发现（Options.Discover）。渐进披露与程序化执行此前是两半没接上：检索在
模型面、程序在程序面，模型想在程序里换个工具得退回一轮 ReAct，Code Mode 收益被抵掉
一半。现在宿主把检索器接进来，程序内即可 discover("重启")。未装配 Discoverer 时
**不挂**这个全局：挂一个永远返回空数组的空壳，会让模型以为「没有别的工具了」而放弃
检索——那比「函数不存在」更坏，后者给出的是可纠正的 ReferenceError。发现不计入工具
调用预算：它是本地目录检索，计入等于让模型查一次工具表也要交税，预算紧张时它就不敢
检索，而检索正是它省调用的手段。
过程中用例抓出一个真 bug：包装函数把 __ptcName 当第一个实参传给了下游，而下游把
Argument(0) 当 argsJSON 读，于是无参绑定收到的是工具名本身。已修。
反向验证四条均变红：去掉形参打包 / 还原首参 bug / 去掉元数校验 / 无条件挂 discover。

### 新增(ptc)：诊断面补位置与修复建议，给模型可操作的信息而非一个类别串

对标 OpenCode packages/codemode 的 Diagnostic {kind, message, location{line,column},
suggestions[]}。本包此前只有 ErrorKind 一个字符串，而本包的调用方是模型——它不知道改
哪一行，也不知道该改什么，只能原样重写一遍碰运气。「第 12 行」这一个数字就能把重试
从盲猜变成定点修。
Result 新增 Location 与 Suggestions：位置取不到就留零值，**不猜**；建议由
ErrorKind 唯一决定（在 Run 的唯一收口处统一补，不在各 return 分支里各写一遍——
分支一多必然漏一处，漏掉的后果是「模型收到一条没有修复建议的失败」这种安静退化）。
位置来源与能力边界，两条来源的可得性不一样，这是事实不是缺陷：
- 语法错误：CompilerSyntaxError 有 File/Offset 时走结构化路径（用 goja 自己的
  File.Position，制表/Unicode/Base 偏移的折算规则只有它知道）。解析器一次报多个
  错误时 File 为 nil、位置只在消息文本里，故加了一条明确标注为「尽力而为」的兜底
  兜底正则：格式一旦变化只会让我们**失去**位置，不会产生假位置。
- 运行期：只有错误经 RunProgram 直接返回时才拿得到 *goja.Exception。程序体是
  async IIFE，函数体里的 throw 变成 Promise rejection，rejection 原因只是一个值，
  goja 没有提供「rejection 处回溯调用栈」的公开面。故这条路径上位置经常为空，
  已有用例钉住「不编造位置」。
一处顺带修掉的设计缺陷：折算里的 -1 原本写死，等于把「源码一定被 IIFE 包裹过」
这个前提藏进函数里——调用方一改包裹方式行号就无声错开一行。现改为显式传
wrapLines，写死的那行假设同时被单测暴露（裸程序编译时确实差了一行）。
反向验证：摘掉建议补齐 → 8 个子用例变红；折算多减一行 → 位置用例变红。
另一条反向验证的老教训在这轮又出现一次：诊断用例最初假设「运行期异常一定有位置」，
与工具能力不符；正确做法是钉住「拿不到就不给」，而不是让实现编一个。

### 新增(checkpoint)：跨进程 CAS 存取契约 + 提交序列固化，恢复点静默回退有了判据

Store 原有的契约只要求「Save 对同一 (RunID, Seq) 幂等」。幂等说的是同键同结果，它
挡不住**陈旧覆盖**：进程 A 落库 seq=5，进程 B 拿更旧视图落库 seq=3——两次写的键不同，
都「幂等」，B 的慢写照样盖掉 A 的新状态。结果是恢复点静默回退，不报错，只在下一次
崩溃时让已完成的步骤重跑。
SeqGate 在进程内挡得住，但它**只在进程内**：执行一旦搬出主进程（后台服务、worker、
子进程），两个进程各有各的闸，谁也不知道对方写过什么。正确的判据是「持久化的序号是否
严格小于本次」，那是存储层的事（一条 WHERE seq < ?），内核在进程内加多少锁都判不出来。
故新增 StoreCAS.SaveIfNewer 把跨进程判据定成契约，SQL 留给宿主。
Committer 把「抢序号→落库→失败回滚」三步收成一个调用。动机与本仓既有的
「散文不是门禁」同源：这三步写在 SeqGate 的注释里，但注释拦不住任何一个手抄漏
Release 的宿主——漏了之后该 run 从此卡死（重试被判陈旧、跳过又留下补不回来的空洞），
且不报错。现在正确顺序是唯一可走的路径。
两条相反的规则都钉住：
- 失败**必须**回滚，否则闸卡死；
- 成功**绝不能**回滚——闸的当前值就是「本进程已见过的最大序号」，落库成功后把它退回去，
  一次陈旧的 seq=3 慢写就会畅通无阻，正是本机制要防的恢复点静默回退。
SeedFromStore 把重启回灌收成一个调用，ErrNotFound（首次执行）不算错误。未装配存储
显式失败：静默返回 nil 会让调用方以为进度已落库，恢复时从错误的位置续跑。
反向验证（注入 → 必须变红）：
  失败时不回滚        → 2 个用例变红（含双进程慢写场景）
  成功时也回滚        → 2 个用例变红
  未装配存储静默成功  → TestMissingStoreFailsLoudly 变红
覆盖率 76.9%（门槛 60），门禁与 docsync 全绿，README 包表 21 个包一致。

### 新增(commitgate)：环节终态独占提交闸，并行分支的双赢不再无人拦截

statemachine 是纯谓词：CanTransition/MustTransition 只回答「这次迁移合不合法」，不回答
「有没有人已经提交过了」。宿主把一个环节拆成两条并发分支推进时，两条分支各自通过
同一个谓词检查、各自把活干完、各自提交终态——没有任何一层会发现这件事发生过。
checkpoint.SeqGate 挡不住：它按 RunID 排快照序号，只管「不倒退」。两个分支先后提交
seq=5 与 seq=6 是完全合法的递增，两个都会被接受，后写的覆盖先写的，而先写那条分支
的副作用早已发生。它能排序记录，排不出「有两条分支都以为自己赢了」。
不并入 statemachine / checkpoint 的理由：前者自我声明是「纯函数件」，加带锁有状态闸
等于把两个相反性质塞进一包；后者管崩溃恢复，本包管并发执行，故障形态与恢复手段都不同。
本闸只管「独占」，不认任何状态取值——终态词表由宿主注入（可直接传
statemachine.IsTerminal）。
三条设计约束：
- 引用必须「运行 + 环节」两级。只按环节名去重会把第二次运行整个判成重复提交。
- 词表未知（isTerminal 为 nil）时按终态从严处理。判据写反会让宿主漏注入时闸整个架空。
- Commit 与 Release 必须成对。落库失败不回滚，该环节的闸被永久占住，从此再也无法
  提交终态——不是多花一个序号，是彻底卡死。Release 只交还自己占的那一次（分支+序号
  都对上），否则一次迟到的 Release 会把后来者的独占权一起放掉，等于给第三次开了门。
拒绝错误带出**先前是谁提交的**：两个分支都以为赢了之所以难查，正是因为日志里只有
两次提交、没有「第二次本该被谁挡下」。
反向验证（注入 → 必须变红）：
  取消独占判定          → 8 个用例变红
  Release 无条件放掉    → TestReleaseDoesNotStealOtherHolder 变红
  引用丢掉 RunID 维度   → 4 个用例变红
另：过程中用例抓出真 bug——「词表未知从严」的判据写反了，nil 时直接放行，闸形同不存在。
覆盖率 76.8%（门槛 60），门禁 7 规则 + 3 门面包通过，README 包表 21 个包一致。

### 新增(tooldisclose)：披露门接成 invoke.Stage，装配点漏接的失效形态消失

本包的 Gate 是独立判定件：宿主自己决定在什么位置判、判完怎么办。后果是一个真实的
失效形态——装配点忘了接，这次工具调用就没有披露门，而模型拿到的是完整 schema 直接
执行，事后从留痕里也看不出「这次没走门」。
接进 invoke.Chain 后这条路径消失：链由构造保证、裸执行体不外泄，宿主拿到的不是
「一个可以选择调用的 Gate」，而是「一条必须走完的链」。审计门同理走
toolgate/policy v0.6.1 新增的 policy.Stage（装饰器形态保留兼容既有调用点）。
适配器刻意**不软化**本包既有的严格二分：
  目录内未取回 → 拒绝但可重试（Check 已激活，重试即可成）
  不在目录     → 拒绝且不可重试（不存在的东西不会因重试出现）
链路一「顺畅」就有人把可重试一并抹平，于是模型为取 schema 得多绕一圈，渐进披露
的收益当场归零——故把这条单独立了用例。
空目录按「不在目录」处理（fail-closed）：不装配目录就是没有工具，门禁不因缺配置
而乐观放行。空目录会让全部门禁消失且无任何信号，是这个面最危险的默认。
跨包集成测试钉的是「协同起来仍绕不过去」：两环放行/披露门拒/审计门拒三态各留
恰好一条痕、披露门放行不构成审计门豁免、任一环拒都触达不到执行体、环节名可被
点名（否则留痕答不出「哪一环拒的」）。
反向验证：抹平可重试判定 → TestChainNotLoadedIsRetryable 变红；让未取回也放行 →
4 个用例变红。
依赖：toolgate v0.2.1 → v0.6.1（连带 runtime v0.4.0 / concurrency v0.16.0 /
mcpkit v0.4.0），全量 build + test 通过，既有包未受影响。

### 新增(archcheck)：门面包不可变门禁，AST 判定，五条路径已反向验证

现有七条规则查的全是写法（用什么词、import 什么、goroutine 怎么起、回调怎么
声明、errors 怎么包），没有一条查「本仓自己的门面有没有被自己改胖」。
docs/OAGENTKIT-OVERLAP.md 里那句「保持门面，别再在本仓加逻辑」是散文——散文不是
门禁：真有人往 policy/ 里塞一份实现，没有规则会响，结果是与上游并存的第二份实现，
正是该文档警告的事。
新增 harness-facade-stays-facade（error）。dispatch/policy/reminder 只允许四种
声明：类型别名、常量别名、单条上游转发函数、允许集合内的 import。「加真逻辑」
三者必居其一（要新类型、要两条以上语句、或要新依赖），所以规则不能被「顺手写
两行」绕过去。
判据走 go/ast 不走正则：正则逐行看文本，判不出「函数体是不是只有一条转发语句」，
而这条纪律恰恰是结构性的。执行体在 cmd/archcheck/facade.go，配置在 policy 新增
的 facades 段（逐包声明转发目标与允许依赖）——故意不塞进 rules 段，那是正则表，
塞不下语法树判定。
反向验证（注入违规 → 退出码 1）：类型定义冒充别名、两语句函数、给类型挂方法、
非转发的单条 return、引入非允许依赖，五条判定路径均已触发。
make check 全绿：覆盖率 76.3%（门槛 60），门禁 7 规则 + 3 门面包通过。

### 修复(protocol)：解析输入上限改为 fail-closed，截断实现等于没有上限

maxReportInput 的注释写「超限直接判非报告」，代码却是截断到 2MiB 再解析。
实测两者行为差异是实质性的，不是风格问题：
截断后的 candidate = 完整报告对象 + 巨量垃圾尾巴，ExtractObject 通道会把
对象原样捞出并丢弃其余部分。实测 3,400,079 字节输入（1.6 倍上限）以
confidence="high" 解析成功——上限形同虚设，且残缺内容未被降置信：
主解析通道就成功了，根本不经过 markRepaired 的修复通道兜底。
方向比不设上限更坏（看起来成功、内容残缺、置信度不低）。
该实现在公开仓第一个提交就在（承自私有宿主仓），此前无任何测试覆盖，
也没有一条测试钉住截断行为，所以它一直「绿着」。
修法：len(raw) > maxReportInput 时直接返回 ErrNotReport，与注释所述意图一致。
反向验证：TestOversizeInputRejectedNotTruncated 在截断实现下 3 个子用例全红
（垃圾尾巴 / 纯空白填充 / 第二个合法对象），改回 fail-closed 后全绿；
另加 TestOversizeBoundaryNotOffByOne 守住「> 上限」而非「>= 上限」的边界，
防止合法报告被误拒或上限写错一字节而失效。

### 修复(ptc)：程序内的工具调用必须过判定环，本包此前是整条链上唯一绕过所有门禁的路径

普通工具调用走 invoke.Chain → tooldisclose.Stage（披露门）+ policy.Stage（审计门）；
而程序里的 host.<tool>(...) 此前**直达宿主工具**（mountBindings 直接调 b.Call），
披露门不判、审计门不判、出站门不判。
要紧的是这个方向：本包恰恰是对外推荐的那条路——N 轮 ReAct 塌缩成 1 轮、原始数据
不过模型面。于是**看起来更安全的路径反而最不安全**。这与 OpenCode 2.0 #52333
（配置热重载窗口绕过 deny 策略）是同一形状：信任边界移动了，沙箱没跟着移动。
改法：Options.Stages 接同一套 invoke.Stage，逐环判定后才触达执行体。语义与链
同源而非各写一套——两处若不一致，会出现「普通调用按环报错放行、程序调用按环报错
拒绝」的分裂，那正是本条要消除的同一类问题。
三条容易被做错的，都钉住了：
- **环报错按拒绝**（fail-closed），不是「跳过这一环」。跳过等于把一次故障静默降级成
  不设防：环坏了于是这一环等于没挂，且没有任何信号。
- **环 panic 被 recover**，宿主代码不该有机会杀掉编排进程。
- **被拒的调用同样计入预算**。程序可以 try/catch 掉错误再循环——而 try/catch 恰恰
  是本包鼓励模型用的手法；不计数的话一个被拒的工具能被无限重试，MaxCalls 形同虚设。
  CallLog 相应多一个 denied 状态（未触达执行体），审计才能回答「这次程序跑了哪些
  调用、哪些被拦」。
拒绝对程序是可捕获的（与工具报错同语义），让程序能带着已取得的部分结果继续。
未装配 Stages 时行为与引入前逐字一致，不破坏既有调用方——但那条路径确实不带门禁，
接入方须自行确认这是有意的。
反向验证三条：摘掉判定环 → 7 个用例变红；环报错改跳过 → fail-closed 用例变红；
被拒不计数 → 预算用例变红。

### 修复(ptc)：修掉注释里的一个替换字符

上一条提交里 ptc/signature.go 第 155 行混入了一个 U+FFFD（"没查[U+FFFD]到"）。这是提交前
的 U+FFFD 自检发现的——自检脚本已经报了，但输出被后续命令吞掉，仍然提交了。
补一条独立提交而不是 amend：那条已经推送，改历史会让已拉取的人对不上。

### 修复(ptc)：绑定净化撞名即拒，否则后绑工具顶替前绑工具的副作用

对标 OpenCode packages/codemode 源码时发现的一处真缺陷。
jsIdent 把 - . 等非标识符字符一律映射成 _，于是 get-logs 与 get_logs 净化后同为
get_logs。而挂载处是 host.Set(name, fn) 连续覆盖，且错误被显式丢弃（_ = host.Set）。
后果不是「那个工具调不到」，是**顶替**：程序里 host.get_logs() 实际执行的是
get-logs。实测复现——把真正叫 get_logs 的先绑定、再绑 get-logs，程序调用
host.get_logs() 拿到 "OTHER-get-logs"，ErrorKind 为空，无报错无日志，
CallLog 里还照常记着被顶者的账。
只读工具这只是结果不对；对有副作用的工具（重启、删除、写入），这是执行了调用方
没打算执行的东西。
改为装配期预检：findBindingCollision 先扫一遍绑定表，撞名即以
ErrorKind=binding_collision 拒掉，不进 VM。对应 OpenCode 的
Extension "<name>" global "<g>" is already defined。
三条附带修正：
- 预检排在语法预检**之前**。装配缺陷是根因，语法只是表象，先报语法会把人往错方向带。
- host.Set 的错误不再丢弃——goja 在只读属性等情形下会返回 error，_ = 扔掉它就等于
  让「绑定没挂上」这件事没有任何信号。
- 空标识符（全非法字符名）跳过而非判冲突，那类绑定本就无处可寻，不能因此拒掉整张表。
反向验证：摘掉预检 → TestBindingCollisionRejected /
TestBindingCollisionNeverRunsSideEffects / TestBindingCollisionBeatsSyntaxError
三个用例变红；还原后全绿。另有一条用例把 hits 计数钉 0，确保被拒的程序一个绑定都
触达不到——先建 VM 再判的话，被顶者的构造期副作用可能已经发生。
对照中另记两条尚未吸收的（属功能差异非缺陷，未做）：OpenCode 的 CodeMode 工具带
input/output schema 且 execute 收解码后的值，我们仍是 JSON 文本进、字符串出；其
Diagnostic 带 location{line,column} 与 suggestions，我们的 ErrorKind 只有类别串。
两条都需要宿主配合建模，故先记录不擅动。

### 修复(protocol)：SeverityRank 收成不可变访问器，静默降级改为编译错误

全仓扫描「导出可变容器」的结果：除哨兵 error 外，本仓导出变量中只有
protocol.SeverityRank 一个是可变 map。
protocol.SeverityRank["critical"] = 0 一句就能让 MaxSeverity（聚合取 max、dispute
钳制都靠它）把最高危静默降级成「未评估」——无编译错误、无运行错误、无日志，只在若干
天后表现为「报告的严重度排序不对」，极难回溯到这一行。
这不是假想风险。netguard/policy.go 对拦截名单早就写下了同一条纪律（「默认名单刻意
不做成可写变量：一个 = 赋值就能把元数据名拦截悄悄摘掉」），而本包是全仓唯一的例外——
守规矩的和不守规矩的在同一个仓里，这条纪律就不是纪律了。
改为 severityRank 不导出 + SeverityRankOf(s) 访问器。Go 没有只读 map，所以唯一能真正
封住「被外部就地改写」的办法就是不给导出符号。
反向验证：注入 protocol.SeverityRank[SeverityCritical] = 0 → 编译错误
undefined: SeverityRank，即绕过形态从「运行时静默降级」变成「编译期报错」。
另补三条用例把序钉死：严格递增、未知值恒 0、MaxSeverity 对未评估值不敏感
（这三条在导出可变 map 的形态下是测不出来的——改一张表就能重排全序而测试照样绿）。
宿主侧同步：bianque/internal/engine/scheduler/judge.go:369 一行改为 SeverityRankOf，
语义逐字等价。全仓再无 SeverityRank[...] 引用。
覆盖率 77.0%（门槛 60），门禁与 docsync 全绿。

### 修复(make)：fmt-check 的文件集由 git 决定，不再递归扫 .

devlink 会在仓内留下 .*/-work/（当前 .toolgate-work 174MB），里面是 Go 官方与各库的 testdata——故意写坏语法的样本。gofmt 扫到它们会刷几十行 expected type found ')' 的 stderr，而原有的 grep -v '^\.' 只过滤 stdout，接不住 stderr。实测 fmt-check 单项被拖到 12 秒，真违规淹没在噪音里。
改为 gofmt -l 走 git ls-files + --others --exclude-standard：等于「gitignore 之外的全部 Go 源码」，第三方工作副本已被 .gitignore 收走天然不在文件集里。CI 里两者都在版本库中，覆盖面不变。
双向验证：故意写坏已跟踪文件 → 报错退出 2；故意写坏未跟踪的新文件（?? 状态）→ 同样报错，确认没引入漏检。fmt-check 耗时 12s → 0.27s，make check 全程 7.9s。

### 修复(pii)：策略面并成单指针，ApplyValue 多叶载荷不再混合处置

issue #2 第五节第 2 条判为「脱敏开关不存在撕裂读，形状相同但判据不同」——该判据不准确，且漏掉了更严重的一处。
一、撕裂读真实存在，且方向危险。enabled/surfaces/strategy 三样合起来才是一份策略，读侧一次处置却分三次取：Apply 走 Enabled()→SurfaceEnabled()→Strategy()。热重载落在两次取之间，读到的是从未被任何写侧设置过的组合。对照组实测：2483 万次读取中 252 万次（约 10%）读到非法组合。后果不是多打一次码，而是宿主已经 SetSurfaces(0) 关掉某面，本次调用仍读到旧面开关 + 新 block 策略，于是回报 blocked=true——面已关闭却要求阻断，宿主可能据此中断整条编排链。
二、更严重的一处漏检：ApplyValue 把闭包交给递归，每个字符串叶子都回调 Apply，于是每个叶子重走一遍全部三份全局状态。一次遍历跨越一次热重载，载荷内就同时出现两种处置形态，而 blocked 由 OR 累加得出，与文本形态不同源。这正是 applySpansAs 注释里声明要消除的现象——那条保证止步于单文本路径，复合载荷从旁边绕了过去，而 ApplyValue 才是宿主处理工具返回的主入口。旧实现实测第 5 次迭代即出现 61/64 混合。
改为整份策略一个 atomic.Pointer[Policy]，读侧一律先取快照再逐项判断；写侧用 CAS 循环做读改写，避免三个写侧并发时丢更新。与 netguard 的改造同形。
反向验证四条：A 闭包改回调 Apply → 第 1 次迭代转红；B applyWith 改分三次读访问器 → 第 9 次转红；D 抽掉 CompareAndSwap → 稳定 3/3 转红。C 是假绿：先写的朴素并发测试（64 轮，提至 20000 轮仍全绿）抓不住 CAS 缺失，因为丢更新窗口在 mutate 到 Store 之间只有几纳秒，比 Go 调度粒度还窄。已用 barrier 同步起跑 + 放慢 mutate 把窗口撑到毫秒级重写，D 即建立在重写版之上。

### 修复(netguard)：策略面改为单指针原子交换，杜绝热重载窗口的撕裂读

此前扩展名单用两个独立的 atomic.Pointer 分别装主机名与后缀。逐字段看每次读写都
原子，看起来没毛病——但策略是两份数据合起来的：classifyHostName 里两处 Load()
落在不同时刻（先主机名、若干次比较后再后缀），热重载若「先换主机名、再换后缀」，
两步之间的并发读者就会读到**新主机名 + 旧后缀**的撕裂策略。
这正是 OpenCode 2.0 issue #52333 的形状（至 2026-10 未修）：每个字段各自都正确，
组合起来的那一瞬间不是。名单「不命中」是这个拦截面最坏的失效模式——挂着一个看着
在拦、实际没拦的策略，不报错、不告警，事后也查不出用的是哪份。
改为单张 Policy 整体替换：读者一次 Load 拿到的就是同一份策略，不存在读出半套的
时点。顺带修掉半应用——两个设置器各自只改一项，后一次校验失败不会回滚前一次，
策略会停在半应用状态且无任何信号；现在两者都改为「取出当前 → 改一项 → 整份换回」，
单独调用依然安全，需要同改两项时用 SetPolicy 一次校验一次交换。
补可观测性：每次交换递增 Generation，经 CurrentPolicy / PolicyGeneration 暴露。
「闸被重置后没人告知」是 checkpoint.SeqGate 记录过的失效形态，这里必须能回答
「那一刻用的是哪份策略」。代次单调且清空不回卷——否则「换上→清空→再换上」与
「什么都没换」在代次上不可区分。
反向验证：把双指针装回去（读写各两次独立取）后，TestPolicyReadNeverTears 立刻
报「主机名 p1=true p2=false，后缀 s1=false s2=true」；单指针下 0 次撕裂。
另附对照组用例实测旧设计：20 万次读中撕裂 4.7 万次，证明该失败形态真实且高频。
过程中被自己的用例抓出一处设计错误：曾断言「清空后代次应为 0」。代次必须单调，
清空回卷会让可观测性失效——已改为显式契约 TestPolicyGenerationNeverRewinds。
（第一次反向验证的注入补丁写错，主机名侧被整个置空导致判据恒假、假绿；重做后
才得到真实结果。写验证本身也会骗人。）

### 重构(ptc)：判定环求值交给 invoke.EvaluateStages，删掉本包那份副本

本包此前自己排了一遍环（runStages/runStage），注释还写着「刻意不各写一套」——
现实是已经与 invoke.Chain.Do 漂移：
  - 环 panic 与环报错归口到守卫自己的名字、Cause 留空。于是经程序内
    host.<tool>() 触发的守卫崩溃，与「守卫正常判定拒绝」在调用方眼里完全
    一样：调用方会去修策略，而真正该修的是守卫代码。
  - nil 环被 continue 跳过（fail-open），链那边判为 internal 拒绝。
  - 没有空名前置校验。
归口用的环节名是 invoke 包内未导出常量，本包补不了；上游为此导出了
EvaluateStages（oagentkit/toolgate#6，52dfe6f）。故删掉副本改调它：
环求值只剩一份，程序内调用与普通调用不可能再分叉。
toolgate 提到 v0.8.0（连带 mcpkit / runtime 到 v0.5.0）：该 tag 是随
oagentkit/toolgate#6 的修复发的 v0.8.0。
发 tag 不是单仓动作：oagentkit/.github 的 verify_facade_release.py 比对
「各模块仓最新 tag」与「门面仓 agentkit 最新 tag 的 go.mod 地板」，只给
toolgate 打 tag 会让门面仓 CI 转红，须全矩阵联动。
测试：TestStagePanicDoesNotKillProgram 补三条断言——被拒留 denied 账、账里
带 panic 原文、且**不得**把守卫自己的名字当归口环节。判据用否定式而不是去
匹配上游的归口环节名：上游把那个名字改成什么，本仓不该跟着碎。
已用「换回 HEAD 的旧副本实现」反向验证过会红，旧实现下的消息正是
「拒绝调用 t（crasher：判定环 panic: 环里炸了）」。
make check 全绿：覆盖率 80.0%（门槛 60%）、架构门禁 7 条规则、
docsync 22 包一致、无 replace、go.work 未入库。
已知遗留（本 PR 不处理）：本包把错误压成字符串出程序（Result.Error /
CallLog[].Error），宿主拿不到 typed error，故 errors.Is(err,
invoke.ErrStageFailed) 在这一层仍用不上，守卫崩溃与策略拒绝的区分目前只体现
在消息文本上。要真正可判定，得给 Result/CallRecord 补一个 ErrorKind
（如 stage_failed）——那是本包对外契约的设计变更，留给该仓自己定。

- 杂务(deps)：logx → v0.7.1（core 地板已由上游抬到 v0.9.2）

上游采纳了本轮提的 oekit/logx#22，logx v0.7.1 的 CHANGELOG 明写：
    build: core 地板 v0.6.0 → v0.9.2（logx#22）——无直接 require core 的下游
    经 MVS 落到本仓声明的下界，抬至最新带动修复传递；logx 唯一使用的
    core/lifecycle 逐字节未变，零行为风险
本仓正是那条「无直接 require core 的下游」——`drain` 走 `logx.Delegating`，
对 core 零直接依赖，此前只能靠 `tools.go` 把 core 钉在 v0.9.2。现在地板由上游
直接抬好了，该 issue 的诉求已在上游侧闭合。
v0.7.0 → v0.7.1 变更面：**零 `.go` 源码改动**，仅 go.mod / go.sum / README
（另含 hlog、kitexlog 两个子模块的 go.mod 同步）。
go.mod 只动一行，go.sum 只换 logx 的两条记录，**三方条目逐字未变**。
## 验证
- `go build -tags tools ./...` 退出 0
- `go mod tidy -diff` 干净
- `make check` 全绿（覆盖率 80.4% / 门槛 60%，lint 0 issues，架构门禁 7 规则 +
  3 门面包 + 22 包依赖方向，docsync 一致）
- `-race` 全过

- 杂务(deps)：agent → v0.8.0（llm 包上移至 module 根，本仓未受影响）

agent v0.7.0 → v0.8.0。变更面：`refactor!(agent): import 跟随 llm 包上移`，
全仓 5 文件 / +6 −6：
- `agentrun/agentrun.go`、`reflection/reflection.go`、`router/router.go`：
  `github.com/oagentkit/llm/llm` → `github.com/oagentkit/llm`
- `go.mod` / `go.sum`：llm 地板 v0.7.2 → v0.9.0
**`dispatch` 不在变更文件里**（`git diff --name-only v0.7.0 HEAD | grep -c dispatch`
= 0），而本仓对 agent 的唯一 import 就是 `agent/dispatch`，故本仓零影响。
go.mod 只动一行（agent），go.sum 只换 agent 的两条记录，**三方条目逐字未变**。
## 顺带记录一处 llm 包路径变更对 tools.go 的影响
llm v0.9.0 把 `llm` 包从子目录上移到 module 根（`llm/*.go` → 根目录 `*.go`，
纯 rename、0 内容变化），旧路径 `github.com/oagentkit/llm/llm` 已不存在。
本仓 `tools.go` 钉的正是那个旧路径，随 llm 升到 v0.9.0 时已同步改为
`github.com/oagentkit/llm`（见 2c8eca5）。这恰好是 tools.go 注释里预告的那类
失效——**钉在子包上，就会随上游重构一起断**。根路径比子路径更抗变动，
今后给新模块做钉子时优先挑根包/最稳定的叶子包。
## 验证
- `go build -tags tools ./...` 退出 0（四个钉子 import 全部有效）
- `go mod tidy -diff` 干净
- `go list -deps ./...` 第一方包仍为 **14 个**，闭包未变
- `make check` 全绿（覆盖率 80.3% / 门槛 60%，lint 0 issues，架构门禁 7 规则 +
  3 门面包 + 22 包依赖方向，docsync 一致）
- `-race` 全过

- 杂务(deps)：llm → v0.9.0（包上移至 module 根，import 路径变更）

oagentkit/llm v0.9.0 起把包从 llm/llm/ 移到 module 根，
import 路径由 github.com/oagentkit/llm/llm 变为 github.com/oagentkit/llm。
- tools.go 的 blank import 同步改名（包名仍是 llm，注册行为不变）
- go.mod 地板 v0.8.0 → v0.9.0，go.sum 重算
本仓 CI 不设 GOPRIVATE，走公共 proxy + go.sum 校验；
go.sum 已含 v0.9.0 条目，无需 sumdb 收录即可校验通过。

- 杂务(deps)：用 tools.go 把四个非闭包内模块钉到最新（llm/obs/textx/core）

## 背景：纯 require 写法钉不住，实测过
llm / obs / textx / core 四个都不在 `go list -deps ./...` 的包级闭包里（harness
第一方闭包只有 14 个包）。Go 的规则是 go.mod 只记录构建本模块所需的模块，于是
「手写 require 钉到最新版」这件事根本不成立——实测：
    go get llm@v0.8.0 obs@v0.5.0 textx@v0.4.0 core@v0.9.2   # 四个都加进去了
    go mod tidy                                            # 四个 // indirect 全被剥掉
只有 tools.go（`//go:build tools` 强制 blank import）能扛住 tidy。选子包而不是
模块根路径，是因为这四个模块**都没有根包**——写 `_ "github.com/oagentkit/textx"`
会直接报 `does not contain package`。实际钉的是 llm/llm、obs/obsx、textx/textutil、
core/version，均已 `go build -tags tools` 实测编译通过。
## 明写代价：引入了 5 个三方依赖
这不是零成本的版本声明，它把用不到的东西拉进了依赖图：
    + github.com/cloudwego/eino-ext/components/model/openai v0.1.13 // indirect
    + github.com/cloudwego/eino-ext/libs/acl/openai        v0.1.17 // indirect
    + github.com/evanphx/json-patch                        v0.5.2  // indirect
    + github.com/meguminnnnnnnnn/go-openai                 v0.1.6  // indirect
    + golang.org/x/time                                    v0.14.0 // indirect
go.sum 由 189 行增至 214 行。一个**从不调用 LLM 的编排内核**因此挂上了 OpenAI SDK，
纯粹是为了让 go.mod 里那四个版本号好看且不被 tidy 抹掉。成本已量化，值不值得由
维护者判断；要撤销只需删掉 tools.go 再 `make tidy`。
其中 textx/textutil 与 core/version 各自真实新增 0 个三方模块，是零成本的那两个；
llm 与 obs 各引入约 1 个（OpenAI SDK + x/time）。
## 验证
- `go build -tags tools ./...` 退出 0：四个钉子的 import 路径全部有效
- `go mod tidy -diff` 干净：钉子扛住了 tidy（这正是它存在的理由）
- `go list -deps ./...` 第一方包仍为 **14 个**：tools.go 不参与普通构建，闭包未受污染
- `make check` 全绿（覆盖率 80.4% / 门槛 60%，lint 0 issues，架构门禁 7 规则 +
  3 门面包 + 22 包依赖方向，docsync 一致）
- `-race` 全过
## 删除判据
tools.go 的注释里写了，这里再强调一次：判据是「这条 require 还有没有真实依赖
支撑」，**不是**「版本号变没变」。若将来 harness 真的开始 import 其中某个包，
该 require 由真实依赖接管，对应 blank import 即可删；若上游把地板抬过这里钉的
版本（MVS 取更高者），钉子自动失效，同样可删。

- 杂务(deps)：concurrency → v0.17.1、mcpkit → v0.6.0（其余新版本不在本仓依赖路径上）

## 先说结论：看到的「很多新版本」里，大部分与本仓无关
逐个核过远端真实 tag（`git ls-remote --tags`，不用 `go list -m -versions`——
后者会把已被删除的 tag 也报出来）后，图内 12 个第一方模块的差距是：
| 模块 | 图内 | 远端最新 | 在包闭包内？ | 本次动作 |
|---|---|---|---|---|
| oekit/concurrency | v0.17.0 | v0.17.1 | 是（async/singleflight） | **升** |
| oagentkit/mcpkit | v0.5.1 | v0.6.0 | 是（mcpkit/mcp） | **升** |
| oagentkit/llm | v0.7.2 | v0.8.0 | 否 | 不动 |
| oagentkit/obs | v0.4.1 | v0.5.0 | 否 | 不动 |
| oagentkit/textx | v0.3.0 | v0.4.0 | 否 | 不动 |
| oekit/core | v0.6.0 | v0.9.2 | 否 | 不动 |
判据是 `go list -deps ./...` 的**包级闭包**，只有 14 个第一方包：
    agent/dispatch · guardx/{egress,injectband} · mcpkit/mcp
    runtime/{pacer,procx,progress} · toolgate/{invoke,policy}
    concurrency/{async,singleflight} · logx · pkg/{errors,jsonrepair}
llm / obs / textx / core 只存在于**模块图**（上游 go.mod 的声明），没有任何本仓包
import 它们。它们的版本由上游各自的下次发版经 MVS 自动抬升，本仓既无控制也无需要——
往 go.mod 里显式 require 会被 `go mod tidy` 立刻剥掉。故只升真正在闭包里的两个。
## 两处升都是零源码改动
- **concurrency v0.17.1**：纯依赖地板对齐（go.mod 里 pkg v0.11.0 → v0.15.1），
  AGENTS.md/CHANGELOG/README/go.mod 变，**零 .go 改动**
- **mcpkit v0.6.0**：只有 go.mod 变（concurrency 地板 v0.16.0 → v0.17.0），
  **零 .go 改动**
两者都不引入新的三方依赖（go.mod 差异全是第一方地板对齐）。
## 为什么 oekit/core 跨 3 个 minor 也不动
core v0.6.0 → v0.9.2 确有实质变更：新增 `chain`/`transport` 两个包，config 侧加了
FieldSet / `require` tag / env 卫生、修了 inline yaml 的 FieldSet 缺口（v0.9.1）、
移除了依赖进程环境的 env 拼写冒烟检查（v0.9.2，issue #8）。
但 logx v0.7.0 只 import core 的 **`lifecycle` 一个包**，而该包在 v0.6.0 与 v0.9.2
之间**逐字节相同**（diff -r 无输出）。也就是说 core 的这批变更全部不在本仓的依赖
路径上，为它强行升 floor 只会得到一个上游从未声明过的组合（logx v0.7.0 的 go.mod
写的是 core v0.6.0）。等 logx 自己抬地板时，MVS 会把新 core 带进来，那时才是正确
的验证时机。
## go.sum
干净 GOMODCACHE + GOPRIVATE 直连 VCS 重算。验证判据：**三方条目逐字未变**
（校验面未被削弱），仅两处第一方版本替换，go.sum 仍 189 行。
本次不涉及 tag 重写（v0.17.1 / v0.6.0 都是新追加的 tag），故未清 CI 的 setup-go 缓存
——上一轮清理是针对「缓存里存着重写前的副本」，本轮前提不成立，清了只是白白拖慢构建。
验证：make check 全绿（覆盖率 80.3% / 门槛 60%，lint 0 issues，架构门禁 7 规则 +
3 门面包 + 22 包依赖方向，docsync 一致）、-race 全过、go mod tidy -diff 幂等、
升级前后第一方包闭包逐项一致。

- 杂务(deps)：依赖地板升到最新 tag（两个旧 tag 远端已删，此前只能靠 proxy 缓存解析）

agent v0.2.1 → v0.7.0、guardx v0.1.1 → v0.4.0、toolgate v0.8.0 → v0.9.0、
runtime v0.5.0 → v0.6.0、mcpkit v0.5.0 → v0.5.1（indirect）、
oekit/concurrency v0.16.0 → v0.17.0、oekit/pkg v0.14.0 → v0.15.1。
这不是可选升级而是必修：agent v0.2.1 与 guardx v0.1.1 已被从远端删除，
`go list -m -versions` 还能看到它们是 goproxy.cn 的缓存条目。直连 VCS 构建报
`reading go.mod at revision v0.2.1: unknown revision v0.2.1`。
源码零改动即可编译：harness 实际用到的 5 个 oagentkit 包（agent/dispatch、
guardx/egress、guardx/injectband、runtime/*、toolgate/*）在旧/新版本间逐字节相同；
oekit 侧只动了 ratelimit/keyed.go（本仓未使用）与新增 urlguard 包（本仓未使用）。
go.sum 用干净 GOMODCACHE + GOPRIVATE 直连 VCS 重算——旧文件记的是 tag 重写**之前**
的哈希，mcpkit@v0.5.0 校验直接报 SECURITY ERROR。验证判据：三方条目逐字未变
（校验面未被削弱），仅第一方 12 条按预期更新。
顺带清掉 6 条既存 golangci-lint 问题（CI 不跑 lint，此前全是潜伏债）：
- cmd/archcheck：死变量 stillBlocked，append 结果从未被读，门禁打印实际走
  all → byRule，删掉不改变行为
- protocol：死函数 decisionProse，2026-09-25 normalizeDecision 改为「未知值一律
  归空」后遗留的孤儿谓词，零调用点
- cmd/docsync、spill：只读句柄 defer f.Close() 的 errcheck
- netguard/chain.go：ST1005 错误串首字母大写
- tooldisclose：QF1008 冗余嵌入字段
动过门禁执行体，故按纪律做反向验证：往 statemachine/step.go 注入 stdlib errors，
门禁准确报出 harness-single-errors-package [error]、statemachine/step.go:6、
forbid_import 并退出非零；样本已还原，工作区无残留。
netguard 包头补记与上游 oekit/pkg/urlguard 的分工：逐条比对后，urlguard 的
blockedCIDRs 是向 egress 的 ipv4ExplicitBlocks 看齐的**收敛版**（少了 240.0.0.0/4
保留段），换过去是放松防线而非收紧，IP 策略继续单源 egress；但域名后缀层如今有两份
且已漂移（.lan 本包有 urlguard 无、.localdomain urlguard 有本包无），记下「增删前先
比对、缺就改本包这处名单、不要引第二份权威」。
验证：make check 全绿（覆盖率 80.3% / 门槛 60%，架构门禁 7 规则 + 3 门面包 +
22 个包依赖方向，docsync 一致、go.mod 无 replace、go.work 未入库）、
golangci-lint 0 issues、-race 全过、go mod tidy -diff 幂等。

- 杂务(go.sum)：清掉 toolgate 升版后残留的旧版本 hash

go get 升到 toolgate v0.6.1 时 go.sum 里仍留着 mcpkit v0.1.0 / runtime v0.3.1 /
toolgate v0.2.1 的 hash。构建不受影响（未被选中），但留着会让 go.sum 与 go.mod 的
实际选择对不上，掩盖真正的依赖漂移。go mod tidy 已确认这是唯一差异。


- 文档(netguard)：更正与 urlguard 的关系结论——两处清单不是包含关系

上一版包头写「urlguard 的 blockedCIDRs 是向 egress 的 ipv4ExplicitBlocks 看齐的
收敛版（少了 240.0.0.0/4）」，**该结论有误**，两处都要更正：
1. urlguard 并**不少** 240.0.0.0/4——它的 blockedCIDRs 里有这条。
2. urlguard 不是 egress 的子集：egress 靠 netip 谓词覆盖回环/私网/链路本地/
   组播/未指定，只把 CGNAT、192.0.0/24、三个 TEST-NET、198.18/15、240/4 写进
   显式表；urlguard 整表列举，**多出 0.0.0.0/8**。
原判断的错因：只比对了 egress 的显式 CIDR 段，漏了谓词覆盖的部分，把「两表
写法不同」误读成「谁更全」。
实测（22 探针对扫）后确认 egress 有一处**真实缺口**——0.0.0.0/8 除全零地址外
全部放行，且 IPv4-mapped 形态同样漏判：
    0.1.2.3            egress=放行  urlguard=拒
    0.255.255.255      egress=放行  urlguard=拒
    ::ffff:0.1.2.3     egress=放行  urlguard=拒
根因：`netip.Addr.IsUnspecified()` 只认全零这一个地址，而 0.0.0.0/8 不在
`ipv4ExplicitBlocks` 里，于是 RFC 1122 的 "this network" 段漏判——这是可用的
SSRF 路径。修复归 guardx，已向其提 issue；本仓不在此私补一份网段表（那会造出
与上游并存的第二份清单，正是本包一直在防的事）。
IP 策略仍单源 egress 的结论不变，但理由改为「缺口在上游修，不在本仓绕过」。

- 文档：补使用指南与特性说明，修 README 三处事实错误

README 讲清了「为什么这么设计」，但仓库缺一份「怎么接」——宿主开发者
无法从架构说明里学会接进来。新增 docs/USAGE.md（按宿主接入顺序组织，
九组独立能力逐个讲清 职责 → 最小集成片段 → 关键符号 → 坑，
末尾附按症状索引的坑速查表），README 补特性章节与文档导航。
顺带修三处会让读者踩空的事实错误：
- 宿主接入写 require v0.1.0，但远端只有 v0.2.0（git ls-remote --tags 核实），
  照抄直接失败；改为 v0.2.0 并补上「用 ls-remote 而非 go list -m -versions
  核 tag」的提醒——后者报的是 proxy 缓存目录，不是远端现实。
- 示例代码调用 hdispatch.NewGuard，但 import 的是 dispatch，包名不存在，
  编译不过。
- 开篇「零 replace（暂用 replace，见下）」自相矛盾：modreplace 门禁早已禁止
  go.mod 含 replace，本地并行开发走 make devlink 的 workspace。
包表 protocol 行数 1821 → 1850，随 cbf85e4 的注释增补同步。

- 文档：补 netguard / ptc 包表行数（ef6ca82 引入的改动）
- 文档：补 ptc 包表更新，使 31bef24 之后的 HEAD 重新通过 docsync

31bef24 给 ptc 接上了 Stage 判定环与 oagentkit/toolgate 依赖，包表随之要改
（919→993 行；依赖列新增 dop251/goja 与 oagentkit/toolgate）。那一行改动当时
留在工作区未提交，于是 HEAD 上 README 的依赖列与实际不符——docsync 对依赖列是
逐项相等判定（行数才有 25% 容差），故 checkout HEAD 后 make check 是红的。
补上这一行，让 HEAD 与工作区一致。


- 测试(ptc)：把「沙箱够不着网络」钉成用例，出站守卫与判定环的分工才有依据

上一轮说「netguard 该不该接进判定环」之前先验了一件事，结果是**没有对应的洞**：
沙箱里 fetch / XMLHttpRequest / WebSocket / require / process / Deno / setTimeout
全部 undefined，程序本身根本够不着网络。出站只可能发生在宿主绑定的实现里，而那是宿主
自己的代码、由宿主自己接 netguard。
所以正确的做法不是加一个没人会用的 OutboundGuard API，而是把这条分工的前提**钉住**：
判定环判「这次调用要不要放行」、netguard 判「这个 URL 能不能发出去」，两个执行点；把
URL 守卫塞进逐调用的判定环就得去猜「哪个参数字段是 URL」，那是领域知识，不该进内核。
而这条前提此前只是一句包头注释。谁哪天给 VM 加个 fetch、或 goja 升级后自带了某个网络
全局，就会静默多出一条「程序能自己出站、绕过 netguard」的路，而没有任何用例会红。
故新增 sandbox_test.go：
- TestSandboxHasNoNetworkPrimitives：逐个断言网络/模块逃逸/定时器原语均为 undefined，
  并带**正控**（typeof host === object）——否则 VM 整体坏掉时本用例会假绿。
- TestDiscoverIsTheOnlyExtraGlobal：装配后多出来的全局只有 discover；未装配不挂。
- TestSandboxCannotReachHostBindingInternals：绑定面只传可序列化的 JSON 文本，
  程序拿不到宿主侧的 Go 值（逃逸的常见形态不是「多一个全局」，是「把宿主对象递回去」）。
反向验证：给沙箱挂一个 fetch → 用例变红，并直接说清后果
（「请重新决定 netguard 接在哪，别让程序自己就能出站」）。
ptc 与 netguard 两处包头都补了这套分工的理由，互相指认——读任一侧的人都能查到对方。

- 测试(ptc)：用真门禁验证程序内的判定环确实生效

上一条修复只证明了**机制**存在（Options.Stages 能跑环）。这一组用**真的**门禁证明
「此前被绕过的那些门，现在确实在程序里生效」。
用桩环证明不了这件事：桩环只能说明环被调用了，说明不了它拦的是哪一种拒绝、
拒绝理由能不能传回模型眼前。后者恰恰是模型能不能自己改正的关键。
- TestRealGatesApplyInsideProgram：tooldisclose.Stage + toolgate/policy.Stage 同时
  在链上。已加载 + 只读 → 放行并拿到工具输出；未取回 schema 的工具 → 被披露门拦下，
  拒绝理由里带环节名（模型能据此知道该去取 schema 而不是换工具）。
- TestRealAuditGateBlocksInsideProgram：审计门拦变异工具。披露门管不了「这个工具
  这次调用该不该放行」，后者只有审计门判——而它此前在程序内根本不跑。用例同时钉住
  hits==0（审计门不能被 try/catch 绕成「换个分支继续执行同一个工具」）与留痕恰为一条。
用外部测试包（package ptc_test）：生产代码里 ptc 与 tooldisclose 互不依赖，
policy 来自 toolgate，deps 门禁只判生产代码。
反向验证：把判定环摘掉（恢复修复前状态）→ 9 个用例变红，含本组两条真实门禁用例。


## [v0.2.0] — 2026-10-18

### 新增(protocol)：把五个引擎侧字段从 LLM 解码路径剥离出来

followups / meta / profile / awaiting_user / guidance 在定义处就标注「引擎侧
确定性填写 / 引擎回填 / 非 LLM 输出字段」，且直接决定**呈现**：profile 切答案
视图会丢掉影响范围、风险回退、合议与 severity 渲染；guidance 置位走轻装视图
（不出 agent 标题与置信度）。让模型能写这几个键，等于让它自己决定「哪些治理
信息不必呈现」——与本包 fail-closed 的口径相反。
ParseReport（外部输入）解析后剥零值；新增 ParsePersistedReport（落库往返）
保留二者。二者共用同一 parseReport 本体，刻意不提供「宽松开关」：外部与往返
是两个不同信任级别的输入，用函数名区分比布尔参数更难用错。
这个拆分是必须的，不能就地剥离：宿主有两处用 ParseReport 回读已落库的
FinalReportJSON（handler 渲染、session 元信息），无条件剥离会让历史报告静默
丢掉 followups/meta——把治理信息缺失做成了「更安全的解析」。
三组测试钉住：外部路径剥、往返路径保、两条路径仅在这五个字段上不同。

### 新增(docsync)：README 包表纳入机器门禁，并修正其陈旧数据

README 包表长期手工维护，已漂到「netguard 声称 ~86 行 / 纯 stdlib 叶子层」
而实际 412 行且依赖 guardx+errors 的程度；6 个包声称依赖「无」实际都 import
oekit/pkg（那次「迁移到 oekit/pkg/errors」的遗留）。行数列口径还混用
「含测试」与「不含测试」两种，行数错得互不一致。
make check 不覆盖文档，所以这次一并补上机器校验：新增 cmd/docsync 比对
README 包表与各包实际生产行数、外部依赖，并反向检查「仓里有包但表里没收录」。
按本仓既有口径（门禁不认文档里说过要迁），这张表不该靠记性维护。
依赖比对允许人类可读简写（jsonschema ↔ santhosh-tekuri/jsonschema），
按位置对齐而非集合排序——README 按实测字典序书写即可。门禁只报错不改文档：
改表是要人判断的动作。

### 修复(docsync)：行数判定加 25% 相对容差，滤掉逐行噪声但保留跨量级误述拦截

根因是 2026-10-18 连续三次 CI 失败（a18b89d / 54b157c / 77ea1ce）全部源于 protocol 差 1 行（README 1822 / 实际 1821），随后又得单独提交「同步 protocol 行数」去消它。
真正该修的不是「行数校验」而是阈值：逐行相等意味着改一行代码就红一次，这种仪式性噪声的真实代价是训练人忽略 CI 红——规则常红等于没有规则。但完全退到不校验也不行：这道门禁存在的初衷正是抓「README 声称 netguard ~86 行、实际 412 行」这类文档误导。
故只滤噪声不砍断言：偏差 >25% 才报。25% 的取法——小于四分之一的偏差不构成对规模的误述（本列本就是约数），而历史上真实漂移都在 2~5 倍量级，离阈值很远，放宽不会放过任何一次真问题。
分母取较大者，使「少写」与「夸大」同等对待；取 README 写的值会让夸大方向更难触发，等于给虚高宣称开后门。
反向验证（改真实 README 而非仅单测）：1821→1822 docsync PASS 退出码 0；1822→800 docsync FAIL 退出码 1。另有 4 条单测双向钉住噪声/误述/对称性/除零。
另修 README 两处过时说明：「make check 不覆盖它」是错的（docsync 就在 make check 末步）；紧接的「「行数」列口径 = 生产 + 测试总和」描述的是本仓不存在的列，是从 guardx README 复制过来的孤儿段。

### 修复(docsync)：行数改为数真实行，堵住指标自指的漏判

原先 loc = strings.Count(s, "\n")，只在**每个文件都以换行结尾**时等于行数。
某个文件丢了结尾换行会被少算一行，于是「改了文件 + 把 README 同步成少算的值」
就能蒙混过关——指标自指，门禁在自己改动的量纲上失准。
末尾无换行时补计残行。当前全部生产文件都以换行结尾，故今天的数字一个不变，
README 无需整表重算；只是堵住将来的洞。配两组用例：缺结尾换行按真实行数计，
且以换行结尾时与旧口径逐字节一致。

### 修复(protocol)：引擎字段剥离改走独立入口，不动 ParseReport 的往返语义

上一版让 ParseReport 直接剥离，看着更安全，实则与本仓既有契约冲突：
TestProfileRoundTrip 明确钉住「形态档随 FinalReportJSON 落库再经 ParseReport
读回必须保真」，注释写明 handler 受众投影靠它自动按档重渲染。就地剥离会把
历史报告的 followups/meta 静默抹掉——把治理信息缺失伪装成「更安全的解析」。
既有测试当场拦下，是它该做的事。
改为：ParseReport 语义完全不动（往返入口），新增 ParseLLMReport 供宿主
解析模型输出，剥那五个引擎侧字段。二者共用同一 parseReport 本体。信任级别
不同故函数名不同，比布尔开关更难用错。
宿主bianque 的三处调用按信任级别分：runner.go 解析模型输出处改用
ParseLLMReport，另两处回读落库 JSON 的保持 ParseReport。

### 修复(protocol,pending)：三处解析容错与校验缺口

缺陷一 protocol：Rec 是本包唯一没有容错 UnmarshalJSON 的结构（对照 StringMap /
// StringList），而 curated_risk 也不在任何归一化分支里。LLM 在字符串位产出数字
（"3"）时严格 unmarshal 到 int 直接失败，三条 tryParse 通道全灭，整份报告判成
ErrNotReport、调用方降级读 markdown 主体。StringMap 的注释把这个失败模式列为
「比单工具失败更疼」的一种，同包却漏了兄弟字段。新增 normalizeRiskTier 做类型收敛，
不改变「引擎基线只升不降」的语义。
缺陷二 protocol：normalizeBoolField 精确匹配 "true"/"false"，而同包的
rawRequiresApproval 用 EqualFold+TrimSpace——同一份代码两套口径。TRUE / True /
// 带空白的 true 既没被归一化、又过不了严格 unmarshal 到 bool，单个字符串的大小写
就能炸掉整份报告解析。统一为 EqualFold+TrimSpace。
缺陷三 protocol：curated_risk 文档写明 1-4 风险档却从不校验，99 或 -1 原样
放行，宿主拿它当引擎基线下限时得不到任何信号。补 Validate 区间判定（0 因
omitempty 与未声明不可区分，不参与）。
缺陷四 pending：ColumnValue 声称「列内容恒 JSON」，守卫却只有 HasPrefix("{")，
于是 {节点A}和{节点B} 这类串原样落列，Parse 解不出返回零值——挂起态被静默丢掉，
不是降级是消失。透传条件改成「真能 Parse 出带 Kind 的 State」，坏串作为 Word
保留，至少内容不丢。
四处各配回归测试，覆盖完整解析路径与越界边界。

### 修复(docsync)：自身依赖排除补路径边界，并重做 module 路径判据

漏洞一：排除仓内自身依赖时用 strings.HasPrefix(path, selfModule) 而无路径边界，
于是形如 agent-harness-fork 的**真实外部依赖**会被当成自身静默排除，README 表
漏写该依赖仍能过——漏判方向与门禁职责正好相反。
漏洞二：importRe 限定全小写，未来引入含大写的 module 路径（Go 允许，代理里
编码为 !x）会整个漏掉，同样是漏判。
两处一起改：正则只负责取带引号 token，是否像 module 路径交给
looksLikeModulePath 判断——用 RE2 硬表达「排除带 scheme 的 URL 与相对路径」
既写不准也难维护。曾试过直接放宽正则，结果把测试里的各类 http/https URL
字面量、协议相对地址，以及注释里的上级相对路径全当成依赖，门禁当场失效；
判据里逐条排除这几类才是对的。
已在真实代码上验证判据：精确识别出 10 个外部依赖，URL 与相对路径零污染。

### 修复(ci)：关闭 issue #1 —— 本地并行开发改走 go.work，并补 go.work 入库门禁

issue #1 记的是矩阵化仓库的通用陷阱：Go 的 replace 只在主模块生效，依赖
模块的 replace 不级联。本仓 go.mod 里那行 TEMP-LOCAL-VERIFY 的本地 replace
让本仓（主模块）构建全绿，却让所有以 replace => ../agent-harness 引入本仓的
下游宿主断供——它们的构建里本仓是依赖模块，该 replace 被静默忽略，guardx 按
MVS 回落到已发布旧版本，报 no required module provides package
.../injectband，错指 guardx 而非真正原因（本仓依赖了未发布的新包）。
修法按 issue 给的两条：
① 本地多仓并行开发改用 workspace。go.work 的 use 跨仓生效且不碰任何 go.mod，
   下游零影响。新增 make devlink / make devunlink，并把「为什么不能图省事写
   replace」连同根因写进 Makefile 注释，避免下一个人重踩。
② 发版顺序纪律已是现状：go.mod require guardx v0.1.1，且 v0.1.1 已发布
   （tag b4ecb659，经代理可解析，含 injectband；v0.1.0 不含，即断供根因）。
门禁：
- 新增「go.work 不得入库」。go.work 一旦提交，clone 下本仓的人会进入
  workspace 模式（go.mod 版本解析被整体旁路），且其中本机路径在别处不存在。
- 反向验证时发现并修正了这条门禁自身的漏报：初版写成
  git ls-files --error-unmatch go.work go.work.sum，而 --error-unmatch 是
  「任一 pathspec 未命中即整体非零退出」，于是「go.work 已入库、go.work.sum
  未入库」这个最常见组合会被放行。改为无 --error-match 的 git ls-files --。
  这正是「从没见过它触发的规则等于没有规则」。
两条门禁均已反向验证（脚本直接从 ci.yml 抽取，未改写）：
- go.mod 不得含 replace：6/6（issue 原案、制表符缩进、两空格缩进、块状
  replace 均判失败；真实 go.mod、仅注释提及 replace 均放行）。
- go.work 不得入库：4/4（两个都未跟踪 / 仅 go.work / 仅 go.work.sum / 两者
  都跟踪，前者放行后三者判失败）。

### 修复(pii)：ApplyValue 未知面不再短路，改为经递归 fail-closed

上一版在 ApplyValue 入口直接返回裸字符串，把「保持载荷结构」的契约砸掉了
（宿主按原结构解回却拿到 string）。去掉短路即可：applyValue 的每个字符串
叶子都过 Apply，那里对未登记面已经 fail-closed，结构与阻断信号都自然保住。
显式关闭的判定仍保留，但只对已登记面生效。
同时修正回归测试：ParseSurfaces 只是解析器，原测试忘了调 SetSurfaces，
于是面根本没关掉——那条断言此前测的并不是它声称在测的东西。

### 修复(pii)：未登记面名改为 fail-closed，不再静默放行原文

SurfaceSet.Has 对未登记面返回 false，而 Apply/ApplyValue 原先把这个 false 当成
「面未启用」直接原文返回。面未登记与「宿主显式关闭」是两件事，塌缩成同一个
状态就等于：宿主把面名做成配置后拼错一个字符（tool_result vs tool_results、
output vs outputs），该面就完全不脱敏，且无报错、无日志、无阻断信号——
而 tool_results 正是工具产出回喂模型的面，PII 最需要兜底的地方。
解析期 ParseSurfaces 本来就是 fail-closed，缺口只在运行期这条路径。
取舍：整段替换 + 返回 blocked=true，而不是 panic。panic 更响但会顺着调用栈
杀掉正在跑的编排；静默放行最省事，代价是数据裸奔。取可恢复的那个：原文确实
没有出去，宿主也能从返回值看见异常。显式关闭的面语义不变（仍原文返回、
不阻断）——否则宿主将无法关掉某个面。
另加 ValidateSurface 供宿主在接线期自查配置：fail-closed 是运行期保险，
不该是日常路径。配 4 组回归测试覆盖未知面/显式关闭/多策略三种情形。

### 修复(checkpoint,ptc)：回滚语义与预算闸的两处真实缺陷

缺陷一 checkpoint：Release 走 delete(g.last,runID) 回滚，而 last 只存最大值，
删记录等于把上一个**已成功落库**的序号一起抹掉。Accept(5) 成功后 Accept(7)
落库失败 → 地板从 5 跌回「无记录」→ 陈旧的 seq=3 畅通无阻，恢复点静默回退——
正是 SeqGate 存在的理由被自己打开。改为保留一格 prev，精确回滚到前一个值；
仅在本就无历史（prev 为 0）时才退化成删除。Seed 抬高基线时作废旧前值，
避免退回一个陈旧地板。
缺陷二 ptc：res.Bytes += len(out) 对报错调用同样记账，但字节闸判定排在
错误分支之后，而错误分支紧接着 panic 出去——判定永远执行不到。程序
try/catch 掉每次报错，报错工具的载荷就能无限累加，MaxBytes 形同虚设。
把判定前移到 panic 之前，并在两条路径上都写 CallLog（留痕不牺牲）。
两处各配回归测试，覆盖完整路径而非直接调内部字段。
同时修正 docsync 的 normalizeDep：strings.Index("github.com", ".") 切出的是
"com" 而非去掉整段 host，所有依赖名都算成了 com/xxx——门禁上线首跑即报错，
正是它该做的。另按实测修正 README 两行：toolfail 实有 oekit/pkg 依赖、
reminder 提交态 41 行。

### 修复(policy)：补上门禁被 module path 迁移打开的缺口

module path 从 git.enjoye.top/enjoydream/agent-harness 迁到
github.com/yi-nology/agent-harness 之后，harness-no-host-dependency 与
harness-no-domain-runtime-deps 两条规则仍只拦旧私有域名——
import github.com/yi-nology/bianque（该仓真实存在）完全绕得过去，
等于给「内核不得依赖宿主」这道定义性约束开了后门。
两条 denylist 各追加 github\.com/yi-nology/ 前缀分支。

### 修复(pii,budget)：四个缺陷修复（来自并发会话的 worker，验证已完成）

本批改动由平级会话 mvs_c906fb38（对比 octop-harness）的 worker
「Fix pii and budget defects」产出，该会话已 idle 且未提交，此处代为入库。
其作者自报验证：gofmt 空 / vet clean / test+cover pii 96.4% budget 100.0% /
race 退出 0 / archcheck 退出 0，四个缺陷均做过反向验证。
缺陷一 pi：applySpans 逐段读全局策略，宿主并发换策略时前几段按 mask、
末段按 redact，而末尾再读一次得到的 blocked 与实际处置形态对不上——丢掉的
恰好是 fail-closed 的 block 信号。改为入口快照一次，策略由 applySpan 形参传入。
缺陷二 pii：applyValue 四个分支对 nil 容器返回无类型 nil，宿主类型断言失效。
改为返回带类型的原 nil（动态类型保持）。
缺陷三 budget：Apply 接受 Cutoff 与实际待摘要数对不上的计划，产出空操作却
报成功。新增 ErrEmptyPlan / ErrPlanInconsistent 两个哨兵，校验前置到任何归档之前。
缺陷四 budget：Classify 的 used*100 >= c*pct 两边都可溢出，c 取到 int64 量级时
满载被判成占用率远低于阈值。新增 thresholdAt 按 a=c/100 r=c%100 两段算，全程不溢出。
另修正若干与实现不符的注释，并把 TestSegmentsEmptyNotNil 更名为
TestSegmentsUnmeasuredReturnsCanonicalZeroes（原名断言的是错误行为）。

### 修复(deps)：go mod tidy 补齐缺失的间接依赖

go.mod 漏声明 4 个间接依赖（sonic/loader、cloudwego/base64x、klauspost/cpuid/v2、twitchyliquid64/golang-asm），其中 golang-asm 等是 goja 的传递依赖，导致 go build 直接报「updates to go.mod needed; to update it: go mod tidy」。另移除一个多余的 golang.org/x/sync。
go.sum 同步从 1383 行收敛到 189 行：约 1200 行是手工迁移 agentkit 时残留、已不在依赖图中的陈旧 hash。
由新增的 CI 门禁首次检出（本地沙箱禁止 rename(2)，go 模块缓存无法落盘，跑不了 make check）。

### 重构(pii)：拆出处置执行面，policy.go 回归其文档所声明的职责

pii/policy.go 原 377 行，距 max_file_lines: 400 只剩 23 行——enforce 切 true
之后那里埋了根绊线。但这次拆分**不是**为了躲开那道闸，而是因为那个文件本身
已经不自洽：
它的文件头写着「处置策略面（面开关 + 策略枚举）」，可文件里实际装着两件事——
  · 注册表：Surface / SurfaceSet 的登记与全局开关、StrategyKind 枚举与全局策略
  · 执行面：applySpan / applySpans / Apply / ApplyValue 的实际打码与载荷下探
文档只描述了它一半的内容。执行引擎跟「枚举」没有任何关系，放在这个文件里是
历史堆积，不是设计。按此切开：
- pii/policy.go  193 行 —— 面集合 + 策略枚举（与其自身文档终于一致）
- pii/dispose.go  217 行 —— 策略感知的处置执行
与 pii.go 的分工也顺势清晰了：pii.go 是**检测面**（正则检出 + 打码原语），
policy.go 是**策略注册**，dispose.go 是**策略执行**。三面各司其职。
纯位置搬迁，零行为变更：pii 测试全绿，harness 覆盖率 80.6% 与拆分前持平
（拆分不改包内生产行数总量，docsync 一致性不受影响）。
## 过程中脚本吞过一次文档
第一版拆分把 package 与 import 之间的 30 行注释块（「为什么原包不够」+
「三条不变量」）整段丢了——编译不报错、vet 也报不出来，只有 diff 才看得见。
已修脚本让它原样带回，并在函数注释里写明：**丢失注释不产生任何错误信号，
它是最容易被拆分脚本静默吃掉的东西**。

### 重构：拆分 3 处超长文件，thresholds.enforce 切 true

清偿结构阈值这笔债，并让阈值**真拦**而不是只报。
policy 里 thresholds.enforce 一直挂着 false，注释写着切换的两条条件：
① 现存超限项清零或显式登记为债 ② 接入 CI。两条都写好了，却一直没人做。
本轮把 ① 清掉——2) 本来就成立（本门禁在 make check 里，而 make check 就是
CI 唯一执行物），于是切 true。
拆分（切点选在天然语义边界，不是随便对半砍）：
| 原文件 | 行 | 拆成 |
|---|---|---|
| protocol/report_types.go | 660 | report_types.go 330 + report_charts.go 342 |
| protocol/report_test.go | 782 | report_test.go 373 + report_charts_test.go 228 + report_tolerance_test.go 197 |
| cmd/archcheck/main.go | 490 | main.go 176 + policy.go 84 + scan.go 254 |
protocol 的切点是 charts 契约自成一块（类型+容错解码 341 行），与报告主干
无交叉依赖；测试的切点是 charts 用例与容错用例两段天然分区。
archcheck 按职责三切：main() 只做装配，policy.go 放反序列化 schema，
scan.go 放遍历与判定——拆之前 490 行里 main() 和引擎逻辑混在一起，
本就不该是一个文件。
**纯位置搬迁，零行为变更**：全量测试绿、覆盖率 80.6%（门槛 60%）不变，
archcheck 自举跑通（它自己就是被拆的那个工具）。
反向验证：
- 干净状态 → 架构门禁通过，exit 0
- 注入 414 行探针文件 → 「结构阈值阻断 / 超长文件（阈值 400 行）1 个」，
  exit 1
- 移除探针 → 恢复通过
两处需要注意的坑，都已写进拆分脚本注释：
1. `re.search` 的 `m.end()` 是**字符偏移**，拿它去切行列表会把文件从
   第 100+ 行切开且**不报任何错**——第一版就因此把 report_types.go 写坏，
   已 git checkout 回滚。
2. import 路径要自己补引号，且 `gopkg.in/yaml.v3` 的包名是 `yaml` 而非
   末段 `yaml.v3`，按末段取名会漏判使用点、把 import 丢掉。
README / AGENTS.md 已从「可直接阻断」改成「直接阻断」，并注明拆分只为让
机械阈值过线——某模块拆完仍读不动时该重新分组，不要靠继续拆小假装解决。

### 重构(protocol,watchdog)：清偿 15 处测试夹具领域词债，规则升为 error

清偿 AGENTS.md 登记的 15 处 warn 级债（protocol 11 + watchdog 4），
并把 harness-test-fixture-domain-vocabulary 从 warn 升为 error。
按 AGENTS.md「先判断是代码错了还是规则该改」，两组都判为**代码该改**，
未改 policy 词表：
1. protocol（11 处）——真领域味夹具。validReport() 这个被几十个用例共用的
   基准报告写死了「内存分析Agent / 本机 / OOM」，等于把报告契约的示例钉在
   运维域上。整份夹具换成中性词面（容量评估Agent / node-a / 限流），
   断言与夹具成对同步改（render 断言、redirect 正则、拍板点文案）。
   跨文件复用的英文 slug（specialists/memory-analysis）不在 policy 词表
   管辖范围内，动它风险大于收益，未改。
2. watchdog（4 处）——断言文案里的「告警」。这组反而是**改了更准确**：
   watchdog 的契约只发 PairingLeak 事件、只告警不裁决，由宿主决定要不要
   告警；用例断言的是「发出了一条事件」，写成中性词更贴合契约语义。
   连带把该测试文件里其余 6 处「告警」一并统一，避免文件内自相矛盾。
规则升 error 的理由：长期挂在 warn 的规则等于没有规则——债清零后若还留在
warn，同一批词面可以无声回涨且没人会看告警清单。这正是本仓对覆盖率阈值
「不设则门禁形同虚设」的同一条判断。
反向验证（升 error 后）：
- 干净状态 → 架构门禁通过，exit 0
- 注入探针文件（3 个词）→ 「架构门禁失败：3 处阻断级违反」，exit 1
- negative_follow 豁免仍然生效：protocol/generality_test.go:87 断言
  「渲染结果不得含『运维』」，没有豁免会被自己这条规则拦下，现状通过
make check 现状：fmt/vet/test/cover-gate(80.5%)/architecture-check 全绿。
docsync 一项为红，报的是 pii 包行数 583→588，与本次改动无关——docsync 只计
非测试文件（cmd/docsync/main.go:157），本次改的全是 _test.go；该差异来自
并发的另一会话正在进行的 pii 改动，未代其修改 README。

### 重构(docsync)：清掉两个从未参与判定的死字段，并拦空职责列

row.desc 与 row.table 都被捕获却从未被引用——table 的注释还写着「供错误信息
定位」，实际一次都没用到。两处一并清掉，死注释比没注释更误导。
职责列散文不比对内容（把代码事实写成散文再与散文比，测的是措辞不是实现），
但「不能为空」是机械可判的，补上：整列被误删或截断时会拦下。

### 重构(ci)：仓库卫生门禁搬进 make check，devlink 改为幂等

问题一：go.mod 不得含 replace / go.work 不得入库 两道门禁只挂在 ci.yml 上，
于是本地 make check 跑不到——门禁要等推到远端才说话，等于把「提交前门禁」
悄悄变成了「推送后门禁」。现搬进 make check，与其余门禁同一条路径，
ci.yml 相应瘦身为只执行 make check。逻辑原样搬、未重写：重写会重新引入
原提交反向验证时排除掉的那条漏报。
问题二：make devlink 不幂等——第二次跑直接撞 go work init 的
「go.work already exists」退出。改为已有 go.work 时走 go work use -add 追加，
所以往 workspace 里加第二个仓不会抹掉别人已加的条目。已验证连跑两次，
条目数不丢。
问题三：workspace 期间跑 go work sync 会按 workspace 的构建列表**回写**
各模块 go.mod/go.sum，仓不全时把用不到的依赖直接删掉——实测把 oagentkit 四个
依赖整片剥掉。已回滚并把该警告写进 devlink 输出与 README 纪律第 3 条。
问题四：README「宿主接入」仍在教「发布后删除 replace」，而本仓早已发布
v0.1.0 且承诺零 replace，该建议已失效，改为直接版本化引入的表述。
两道新门禁均做反向验证：go.mod 注入制表符缩进/块状 replace 判失败、
仅注释提及 replace 放行；go.work 被跟踪（含「已入库但 go.work.sum 未入库」
这个原发现的漏报组合）判失败、未跟踪放行。

### 重构(reminder)：核心下沉 guardx/injectband，本包转为门面

带外注入的身份防伪是通用安全原语，与编排无关，故下沉到 guardx（tier 0
安全层）；与 fence 互补——fence 封数据侧，injectband 封系统侧，只做前者时
攻击者不必逃出数据区，只需在内容里写一个像系统注入的标签即可抬升身份。
本包只保住调用路径稳定，宿主 reminder.Wrap(...) 调用点一行不动。
解阻塞：此前卡在 guardx 未发 injectband，只能用本地 replace 顶着，而那条
replace 一旦入库会让所有外部 go get 失效。现已发布 guardx v0.1.1（CI 通过、
proxy 可解析），故移除临时 replace 并把依赖升到 v0.1.1。
本批改动由并发会话的 worker 产出，此处代为入库。

- 杂务(deps)：guardx 升至 v0.1.1（reminder 门面的实现载体）

ef2143d 把 reminder 转为门面，实现落到 oagentkit/guardx/injectband。v0.1.1 是首个含该包的版本，go.sum 已按正式 release 重建，无 replace（CI 门禁项）。
验证：make check 全绿（覆盖率 80.5%、门禁 7 规则无阻断、docsync 包表一致）；reminder 转发经真实 v0.1.1 依赖编译通过，覆盖率 100%。

- 杂务(deps)：五个直接依赖升至上游最新

oagentkit/agent      v0.2.0 → v0.2.1
  oagentkit/runtime     v0.3.0 → v0.3.1
  oagentkit/toolgate    v0.2.0 → v0.2.1
  oekit/pkg             v0.13.0 → v0.14.0
  oekit/concurrency     v0.13.1 → v0.15.0（跨两个 minor）
间接依赖零变化，go.sum 191 → 189 行。
解析在 CI 的 Go 环境里完成（本地沙箱禁 rename(2)，go 的模块缓存无法落盘，
go get / go mod tidy 都跑不了），产物回传后人工审阅再入库，编译与全量门禁
由 CI 裁决。
版本显式钉住而非 @latest：proxy 的 @latest 端点带缓存，oekit/concurrency
v0.15.0 在解析时刚发布不到十分钟，@latest 仍返回 v0.14.0，静默少升一档。

- 杂务：初始化公开仓，module path 改写 + 内部代号脱敏

首次入库。相对本地开发态做了三类处理：
1. module path 公开化
   git.enjoye.top/enjoydream/agent-harness -> github.com/yi-nology/agent-harness
   覆盖 go.mod、6 个 Go 文件 import、README 安装示例。
   依赖已全部为 public 模块，故 go.sum 中陈旧的 agentkit hash 行、
   Makefile 的 GOPRIVATE/GONOSUMCHECK 一并清理。
2. 内部代号脱敏
   README / AGENTS.md / docs / policy 注释中的内部产品代号与组织名
   替换为中性表述。门禁 forbid_import_regex 与 AGENTS.md 规则路径
   按约定保留真实路径——去掉会让「禁止 import 宿主仓」这道门禁失效。
3. 补 MIT LICENSE（同 oagentkit / oekit 家族惯例）
无凭据、证书、私钥类文件入库。


- CI：给公开 module 加 replace 防线

工作区当前存在一条本地 replace（guardx 本地验证 injectband 转发用，
注释自己标了「禁止提交」），但没有任何机制拦着它：.gitignore 里那段
「本仓 go.mod 当前不含 replace，此处仅作防线」只是注释，git add . 照样带走。
一旦入库，所有外部 go get github.com/yi-nology/agent-harness 都在解析
依赖图时失败——别人机器上没有 ./ 这个路径。且发布那一刻没有任何本地信号，
只有下游报「拉不下来」才暴露。
按本仓「不写进机器约束的约定半年后必然回退」的口径改成机器约束：
CI 新增一步，go.mod 出现 replace 即判失败。已反向验证（干净文件放行、
注入 replace 必拦）。同时把本地工作区 /.guardx-work/ 等收进 .gitignore
——此前只在本地 .git/info/exclude 里，不随仓分发。

- CI：移除临时工作流（tidy-fix / gate-probe）

两者都是为绕开本地沙箱「禁止 rename(2)、go 模块缓存无法落盘」而临时借用
CI runner 的，用途已尽：依赖问题已修、门禁全貌已探明。常驻门禁见 ci.yml。

- CI：临时门禁探测工作流（用完即删）
- CI：临时 tidy 修复工作流（用完即删）
- CI：加 GitHub Actions 门禁工作流

.gitignore 里已写明「门禁的检出能力应由 CI 持续保证，不该靠把违规样本留在仓里」，
本次入库把该原则落实：push/PR 触发 make check（fmt-check + vet + test +
cover-gate + architecture-check）。
Go 版本用 go-version-file 跟随 go.mod，避免两处各写一份后悄悄漂移。


- 文档：把提交身份约定写进 AGENTS.md

本仓 local config 此前是 zhangyi01 <zhangyi01@kylinos.cn>，而 global 才是 MurphyYi <zy84338719@hotmail.com>。local 优先级更高，所以本仓任何一次提交都会落成工作身份——38 个提交里有 31 个是这么来的。
约定：提交身份固定为 MurphyYi <zy84338719@hotmail.com>，配在本仓 local，不动 global（global 是别的项目在用的工作身份），也不用 -c 临时覆盖绕过。
沿用历史写法 MurphyYi 而非 Murphy-Yi：本地 git log 里只有前者，换写法等于在同一仓库制造两种 Author 名。

- 文档：同步 protocol 行数（1822 → 1821）
- 文档：定性 delta 两条 SUSPECTED，并把 LICENSE 署名与门禁位置写成约定

delta 的 entries 无回收、FlushAll 按 max(iteration) 扫序——两处单独看都像缺陷，
审计因「本仓看不出 iteration 是否受模型影响」而标为 SUSPECTED。去宿主侧查实后
结论是都不成立：Pacer 由 bianque runLLMStep 每个环节新建、defer 里 FlushAll 后
整体丢弃，iteration 是该环节内的局部计数器（round, counted），不跨环节累加、
不可由模型取值。故前者不是泄漏，后者也不构成空转。
但这两处的安全性**依赖一个外部不变量**，代码里没写、编译器也不管：一旦有人
把 Pacer 改成长生命周期（如整个会话一个），两条会同时引爆，且症状是「跑久了
内存涨 / 收口卡住」，极难归因。已把该不变量写进 delta 包头，含 bianque 侧的
对应位置，改接线前先看那一段。
另：LICENSE 署名定案为 enjoydream 不改（版权归属看作者实体、不看仓挂在哪个
org；oagentkit/* 与 oekit/* 全部同款）；AGENTS.md 里 make check 那行补上了新增
的门禁与「门禁本体一律进 make check、不要只挂 ci.yml」的约定。

- 文档：同步 pii 行数（541 → 583）
- 文档：同步包表行数（checkpoint 402 / ptc 326）

两处行数变化来自本轮缺陷修复本身——docsync 门禁按设计把它们拦了下来。

- 文档：修正 AGENTS.md 的 warn 级债计数

门禁实测为 15 处（protocol 11 + watchdog 4），原文写的是「protocol 下 12 处」，
数量与范围都已过时。计数来自 architecture-check --report。


- 格式：gofmt ptc 回归测试

- 测试(pii)：补「已登记但显式关闭」路径的护栏，并订正早返回的可覆盖性说法

接管并发会话遗留未提交的一摊（会话中断后工作区只剩这两处），复核后提交。
生产改动是纯注释：`return v, false` 代码未动，只是把原来那句
「确属已登记但显式关闭：既定语义，原样返回」换成如实的描述——这条分支是
**纯快路径**，不是语义守卫。
新增 surface_disabled_test.go（111 行）三条用例构成完整三角，防止两个方向
同时塌缩：
- 已登记 + 显式关闭 → 原样透传（含结构），且不得报阻断（策略设 block 以
  确保「面关闭先于策略生效」这条次序真被验证）
- 已登记 + 面开启 → 照样处置（否则「显式关闭」可能被实现成「永远透传」）
- 未登记面名（拼错）→ 仍须 fail-closed，不被上一条路径吃掉
## 订正了一处自相矛盾
这摊活内部有两处注释对同一事实给了相反的说法：
- policy.go 新注释：「实测把本分支拆掉，surface_disabled_test.go 的用例仍全绿
  ——这行因此无法被行为断言覆盖」
- 测试文件原注释：「nil 容器是这条早返回**唯一**可观测的差异」「这两条才是给
  早返回守卫的牙」
实测裁决：摘掉 ApplyValue 里的早返回分支后，单跑该用例**仍然 PASS**，全量
pii 也全绿。原因是 applyValue 对每种容器类型都有 `if t == nil` 早退，nil
照样原样返回——所以早返回确实是可摘的纯快路径，policy.go 的说法正确，而
测试文件那两条 nil 断言**不构成守卫**，其因果描述是错的。
已把测试文件的注释改成如实口径：nil 断言钉的是「透传保结构」这条契约
本身（JSON `null` 不得静默变 `[]`/`{}`），但它不区分早返回与递归回退，
并交叉引用 policy.go 的说明，避免下一个人再被这条错误因果误导。
三条用例的语义覆盖未动，补的是注释准确性——测试价值在于钉住行为，不在于
声称钉住了哪段实现。
make check 全绿：覆盖率 80.6%（门槛 60%）、架构门禁无阻断、docsync 一致。

- 测试(pending)：用真实 Encode 产物修正透传用例

Encode 用的是短键 k/w（state 的 json tag），不是 kind/word——原先那个
「已是 Encode 产物」的输入其实根本解不出 State，生产代码的包装行为是对的，
是断言写错了。

- 测试(docsync)：给门禁自身补测试，并抽出 isSelfPath 供直接验证

原先「排除仓内自身」写死在 measure 内部，无法被测试打到，只能靠整体运行观察。
抽成 isSelfPath 后，路径边界这条判据有了正反两组用例：自身与其子包排除，
agent-harness-fork / agent-harnessx / agent-harness2-tools 一律**不得**排除。
其余用例钉住本次两处修复：URL 字面量 / 协议相对地址 / 上级相对路径 /
纯 stdlib 标识必须被排除（否则门禁永远红），含大写与前缀近似自身的 module
路径必须被识别（否则 README 漏写依赖能蒙混过关）；normalizeDep 的归并口径与
suffixMatch 的方向性（简写可接受、方向不可反）一并钉住。

- 测试(ptc)：修正回归测试里的 ErrorKind 常量名

上一轮 CI 已证实修复生效（ErrorKind=budget_bytes），失败原因是测试把
常量写成了 byte_budget。
