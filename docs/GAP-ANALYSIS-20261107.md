# 横向对比与差距台账（2026-11-07 刷新）

> 前一版：[docs/GAP-ANALYSIS-20261106.md](GAP-ANALYSIS-20261106.md)（再前一版：[20261021](GAP-ANALYSIS-20261021.md)）。
> 本文是第三次刷新：五个参照系重拉到 2026-11-07 后重做逐项对齐。
>
> **口径**：只比**机制**，不比产品面。判据仍是本仓那条——
> 「这个包需要知道自己在给谁干活吗？」需要 → 归宿主，不进本仓。
>
> **有效期**：文中的提交号、包数、版本号都是 **2026-11-07 实测快照**，
> 不受任何机器判据管辖（上一版的「134 个 crate」这一轮就复现不出来，见 §零 末）。
> 引用前请重跑 §五 的命令，别把它们当活事实。

---

## 零、这轮拉到了什么

| 参照系 | 上一版记录 | 本轮实测 | 位移 | 这轮新增了什么 |
|---|---|---|---|---|
| **Codex** | `ddd1812`（2026-11-06） | `0c58612c`（2026-11-07T02:09Z） | **59 提交 / 300 文件** | 全在既有面里精修：`tui` 100 / `core` 53 / `ext` 33 / `app-server` 18 / `message-board` 14 / `thread-store` 8 / `guardian-context` 7。**没有新 crate 面。** |
| **DSH** | `fcc0ad4621`（0.2.2-rc.1） | **同一提交**（pushed_at 2026-11-04） | 无 | 58 个包组 / 400 个叶子包，与上一版逐字相同 |
| **Octop** | `4022e24e`（1.0.3b4） | **同一提交**（pushed_at 2026-11-06） | 无 | — |
| **Coze Studio** | 已停更（2026-07-29） | pushed_at **仍是 2026-07-29** | 无 | 冷冻进入第 4 个月 |
| **本仓（yi-nology）** | v0.17.0 / 41 包（docsync 口径） | **v0.18.0** @ `3ea2d4e` / 41 包 | +4 包 | 十个机制包、`race`/`vuln`/`crossbuild` 三条门禁、逐包覆盖率下限、`egress_paths` 第五条判据、上游家族追平 7 项、eventbus 契约跟进 |

**这一轮的结论有一半来自「什么都没变」**：三家活跃参照系没有长出新机制面，
Coze 继续制冷。所以本轮要回答的不是「上游又多了什么」，
而是「我们清单上还开着的那几件，为什么一天过去还是开着」。

> 顺带一条方法论账：上一版写 Codex「**134 个 crate**」。本轮在**同一个提交**上量到
> 124（`ls -d codex-rs/*/`）/ 126（`git ls-tree -d ddd1812 codex-rs/`）——
> **134 复现不出来**。派生计数抄进散文就没人维护，这正是 AGENTS.md
> 「文档里的数字要有主」那条纪律的又一次实例。本文凡引用计数都附命令。

---

## 一、结论先行

> ⚠️ **本节记录的是 P0-1 落地**之前**的现场**（2026-11-07 上午的实测）。当日已按 §三 P0-1
> 的三条判据补齐兑现件（`guardx` v0.21.0 + 本仓 `ptc.EgressBackend` + 门禁第二半），
> 结账记录见 §三 P0-1「落地记录」与 §六。下面的「两端都空」现在读作**它为什么当时是 P0**
> ——不是当前状态。

1. **唯一还开着的 P0 是一天前记下的那条；而且它的实际状态比台账写的更靠前——两端都空。**

   - **上游端**：家族里**没有任何进程级出口代理**。`grep -rli 'socks5|x/net/proxy|httpproxy'`
     在整个 `oagentkit` 矩阵上零命中；`guardx/egress` 是 **SSRF 字面量围栏**
     （挂在 Go 的 `http.Client`/`Policy.NewClient` 上），对子进程无效。
   - **兑现端**：**没有任何 `egress.Enforcer` 实现者**。`guardx/sandbox` 没有
     `Egress()` 方法；本仓 `ptc` 只把 `egress.Spec` 翻译成子进程 env
     （`ptc/childpipes.go` 的 `spec.Env(base)`），**从不问后端能不能兑现**；
     宿主 bianque 检索零命中。

   ⇒ 今天任何比 `ModeUnrestricted` 强的档位都是**声明得出来、兑现不了**：
   `ModeProxy` 需要 `Spec.Proxy` 指向一个**不存在的代理**；`ModeDenied` 在
   `Spec.Env()` 里直接报错、要求后端强制。fail-closed 的写法是对的——
   但「安全」不等于「可用」：宿主一旦按 `preset` 声明受限出口，
   真实结果是子进程**连不上网**，而链路上每一层都显示「已声明已校验」。

   这条不是「再确认一次」，是**比上一版的判断更硬的证据**：上一版说
   「代理本体归 guardx」，暗示声明侧已闭环；实测是**连能声明自己兑现的后端都没有**。

2. **我们这一天的进展全在「判定面收严」，没有一件在「兑现面」。**
   本轮的 5 笔改动（第六节的清单）逐条看：全部是「让某个声明被更严地判到」——
   把 `procx.InheritAllExcept` 纳入全量环境判据、跟上上游的 channel 关闭契约、
   修结构阈值、追平依赖地板。**没有一笔让某个声明更可能被真的兑现。**

3. **判定面的宽度这轮又扩了一格，但它证明不了兑现。**
   `egress_paths` 的第五条判据把判定从「工具调用」下探到「子进程 env」
   （`os.Environ` / `procx.InheritAllExcept` **按文件**判）。这是对的，
   但判定面永远只能证明「声明被写下来了」——**证明「声明被兑现了」需要另一件东西存在**，
   而它不存在。这就是第 1 条为什么是 P0 而不是 P2 的原因。

---

## 二、逐维度对齐（只列本轮核验过、或与上一版有变的）

| 维度 | Codex | DSH | Octop | 本仓 | 判 |
|---|---|---|---|---|---|
| 命令级判据 | `execpolicy`（前缀规则 + allow/prompt/forbidden + 规则自带样例） | `sandbox-policy` | 沙箱后端 | `cmdpolicy`（三态裁决 + 加载期样例自检） | ✅ 追平 |
| **出口强制** | `network-proxy`：本地 HTTP(3128) + SOCKS5(8081)，allow/deny + 只读「limited」模式 | `sandbox-policy` | 容器网络 | **`egress` 有声明与判据，强制件与后端能力声明都缺** | 🔴 **P0** |
| 后端能力问询 | — | — | — | `egress.Enforcer` 接口有、**零实现**；`ptc` 不调用 | 🔴 同 P0 |
| 沙箱档位 | bwrap/landlock/seatbelt/windows + integrity + violation | local/policy/windows-acl | Docker/OpenSandbox | `sandbox`（档位 + 后端协议） | ✅ 声明齐，兑现归宿主 |
| 审批 × 档位 | 内建两层 | `interaction` + `permission-presets` | 工具审批 | `gatecheck` + `preset`（三面收成整份） | ✅ 本仓更强 |
| 上下文计量/压实 | 预算前置 + 压实 | `compaction`/`token-meter`/`spill` | 记忆 + 精简 | `budget`/`spill`/`offload` | ✅ 不弱 |
| **片段类型化** | `context-fragments`（answered_question / additional_context / annotated_content / recap_prompt，**各带 token 上限**） | — | — | `sessionref` 有来源+失效，**无类型化准入与按类限额** | 🟡 P2 |
| **监护者审阅** | `guardian-context`：同步审阅与异步打分**共用装配**；contributor 注册与 scope；full/delta 提议游标；bounded history；成本估算 | — | — | **无**（判定全是规则型） | 🟡 P1，**等触发** |
| **可观测** | `otel` + `rollout-trace` | `telemetry` + `session-telemetry-otel` | — | `trajectory/otel.go` **只是 seam**；底座已在 `oekit/tracing`（`Init`/`Provider`/`otlphttp`/`httpotel`/`gormotel`） | 🟡 P1，**缺映射不缺底座** |
| **作业/定时** | `cloud-tasks`（产品面） | `jobs` + `schedule`（机制面） | — | 无（`pending` 是**反问挂起**，不是作业） | 🟡 P2 |
| 凭据 | `secrets` + `keyring-store` + `workload-identity` | `credentials` | — | 无（有 `pii` / `logredact` 止痛） | ❌ 宿主/上游 |
| **进程加固** | `process-hardening`：`pre_main` 禁 core dump、禁 ptrace attach、剔 `LD_PRELOAD`/`DYLD_*` | — | — | 部分：`procx.ChildEnv` 白名单 + `egress_paths` 禁全量继承 | 🟡 观察位 |
| agent 协调面 | `message-board` + `agent-roles` + `agent-graph-store` | `team` | AgentTeams | `taskboard` + `boardtest`（存储中立 + 十类不变量） | ✅ 追平（契约级） |
| 恢复/续跑 | `thread-store` | `session-checkpoint-policy` | LangGraph checkpoint | `checkpoint`（CAS + 续跑非重放）+ `sideeffect`/`commitgate`/`ticket` | ✅ 本仓更强 |
| 技能/插件 | `plugin-host` + `skills` | Cordis 动态包 + `skillkit` | 插件包 | `skilldisclose` + `hookengine` | ⚠️ 与 `skillkit` **预算语义各判一次**，需对齐 |
| 门禁/文档纪律 | 规则样例 + 生成为主 | `verify-tool-catalog` / `verify-translation-pairing` | — | `archcheck`（规则须带样例）+ `docsync` + 逐包覆盖率 | ✅ 同档 |

---

## 三、还缺什么（分级 + 归属）

### P0-1 出口强制件：**归属与最小可用** —— ✅ **2026-11-07 已结账**（见本节末「落地记录」）

**归属**：上游 `guardx`（与 `egress` 同族）。本仓**不造第二套网络栈**——
这条已写死在 `egress` 的包文档里（「不给代理本体与网络强制实现」），本轮不改这条边界。

**形态**（抄 Codex `network-proxy` 的最小集，不要抄它的 Windows 端口协商）：
本地 HTTP `CONNECT` 代理 + allow/deny 名单 + 只读模式；子进程靠
`Spec.Env` 指过来——**本仓这一侧已经做好了，不需要改**。

**三条最小可用判据**（都必须是机器判，不是文档判）：

1. `guardx/egressproxy` 实现 `harness/egress.Enforcer`（`Egress()` 返回它真能兑现的档位）；
2. 代理自带用例：allow 命中放行 / deny 命中拒绝 / **未在名单内的主机默认拒绝** /
   `CONNECT` 到非 443 端口必须显式放行（默认拒绝而非默认放行）；
3. 本仓 `ptc` 在 spawn 前调 `SatisfiedBy(backend)`——把「后端不声明就不执行」
   接进**真实路径**（现在只有接口，没人调用）。

**验收**：注入「后端不实现 `Enforcer`」⇒ 声明 `proxy` 档位的用例红；
接上实现 ⇒ 绿。

#### 落地记录（2026-11-07，三处与本节的原始设想不同）

1. **词表不在 `guardx/egress`**——那个名字**已被占用**：`guardx/egress` 早就是「LLM 可控
   URL 的 SSRF 字面量围栏」（`Policy{AllowPrivate, AllowLoopback}` + `CheckResolving`），
   与「出站档位」是两件事。把档位塞进同名包会让一个包背两个语义。故词表与
   `EgressEnforcer` 落在 **`guardx/sandbox`**：那里本来就是「执行后端协议」（`Level` /
   `Provider` / `Capable` / `Registry`），而出站档位正是后端的第三个可声明维度。
   路径变了，**理由没变**：跨模块共享的词表必须低于所有实现方。
2. **代理落 `guardx/egressproxy`**，只声称 `EgressProxy`——它挡得住经过它的流量，
   挡不住绕开代理的原始 socket。声称 `denied` 就是让档位表说谎。
3. **`SatisfiedBy` 的调用点落在 `newChildPipes`**（与 `Spec.Env` 同一函数），不是 `run()`：
   判据与生成是同一件事（「这份声明兑现得了吗」），分开写会让「只做了一半」看起来
   仍然完成了兑现；而且门禁按「文件 + 函数」定位启动点，两半同处一地才判得出来。

**发布**：`guardx` **v0.21.0**（本仓依赖地板 v0.19.0→v0.21.0，连带 `oekit/pkg` v0.37.1）。

#### 验收补记（2026-11-07 下午）：端到端一条真请求，顺带抓到一个真缺陷

§四 第 2 步的验收还要求「与 `egress.Verify` 的 env 对接跑通（**端到端一条真请求**）」，
上一轮只做到「spawn 前问能力」，这一半没做。补做时用**代理的拒绝名单当区分器**——
对同一个健康后端，「经代理」与「直连」都是一次 200，看不出差别；经代理 ⇒ 403、直连 ⇒ 200。
三条用例落在 `egress/e2e_test.go`：名单内放行 / 未列名拒绝（403 + `X-Egress-Blocked`）/
拒绝名单优先于 `*`。真代理（`guardx/egressproxy`）+ 真请求 + `Spec.Verify` 自证，一条链走完。

**顺带抓到 `Spec.Allow` 是个安全缺陷**：它被写进了 `NO_PROXY`，而 `NO_PROXY` 的语义是
「**绕过代理直连**」——与「允许**经代理**访问」正好相反。后果是允许名单变成豁免名单：
名单里的目标直连，代理的拒绝名单 / 端口白名单 / 只读档对它**全部失效**，而报告里那条
声明仍然显示「已兑现」。这是「声明强度 ≠ 实际强度」的又一次实例，这次它藏在同一个
结构体两个相邻字段的耦合里——单看哪一个字段的注释都觉得没问题。

修法：`Allow` 不再产生 `NO_PROXY`（只作为交给兑现者的声明，由代理按自己的名单判）；
新增 **`Spec.Bypass`** 显式表达「不经代理」；`Verify` 增判绕过名单，且判**语义等价**
（顺序/空白漂移不算违约——判字面就是假红，`NO_PROXY` 本身无语义顺序）。

钉子：`TestAllowDoesNotBypassProxy`（env 判据，修前红）+ `TestVerifyBypass`（三种坏形态红、
两种等价形态绿）。变异验证：短路两处 `Bypass` 判据 ⇒ `TestEnvProxy` 与 `TestVerifyBypass`
双红，还原即绿。

### P1-1 遥测语义映射（**不是底座**）

- **底座已有**：`oekit/tracing`（`Init`/`Provider`/`otlphttp`/`httpotel`/`gormotel`，
  trace_id 真相源）。所以这不是「要不要引 OTel」的问题。
- **缺的是**：本仓事件 → OTel **属性名与取值域的一张表**：
  trace 边界定在「环节」还是「turn」、span 父子怎么连、哪些字段是
  high-cardinality 禁区（`session_id`/`agent_slug` 能不能进 label）。
- **落点**：`trajectory/otel.go` 从 seam 扩成映射表 + 用例钉住属性名不漂。
- **判据**：映射表里每个属性必须写出「来源字段 + 取值域 + 基数上界」，缺一即测试红。

**落地（2026-11-07 晚）**：契约表落在 `trajectory/otelmap.go`（`AttrSpecs` 11 条 +
`ForbiddenLabels` 3 条 + `TraceBoundary`），映射面在 `otel.go` 增 `Span.ParentID`。
四处判断及依据：

1. **trace 边界定在轮**，不是环节。依据不是习惯，是**行标识的唯一边界**：`Row.ID` 只在轮内唯一
   （`step#7` 在两轮各出现一次是合法的，投影器不把轮次拼进环节号）。以环节为边界时，同号环节
   会在两个 trace 里拿同一个标识，而工具行同时引用环节与轮次 ⇒ 按 OTel 语义，父 span 不在本
   trace 内即断链。以轮为边界时，「行 ID 唯一」与「trace 内标识唯一」是同一个范围。
2. **父子是声明的，解析交给批量入口**。单行映射只能按行上的字段推父，推出来的父可能**在本批
   行里根本不存在**（环节行是孤儿，或调用方只导出了一部分行）。故 `SpansOf` 做一次解析：父不在
   批内就清成根——导出一个指向不存在 span 的断链，在后端里表现为「trace 少了一段」，不报错。
   而「这一行声明属于哪个环节」这层事实不丢：`AttrStepID` 照样写着。两个东西说的是两件事：
   一个说「它属于谁」，一个说「导出时连得到谁」。
3. **`session_id` / `agent_slug` 连 span attribute 都不进**——不是因为在 label 里危险，是它们
   **不在 `Row` 上**：结构上装不进去。禁区清单（`ForbiddenLabels`）是写给宿主的：这两样看起来
   很像维度，落进 metric label 就是无界基数；要关联会话用 trace_id（那是**标识**不是维度）。
4. **只有「枚举 + 本仓给得出上界」才许进 label**（`LabelSafe` 蕴含这两条）。label 的代价是时序数
   按取值数**乘**上去，一个无界 label 就能让指标后端按会话数膨胀。

**判据是双向的，缺一向就是半个判据**：映射写出了表里没有的属性 ⇒ 红；表里列了映射产不出的属性
⇒ 也红（防「表变成许愿池」）。另逐项判来源字段 / 取值域 / 基数上界依据 / 名字唯一，并按**反射**
比对契约写的 Go 类型与映射函数实际放进 map 的类型——类型不符的属性在多数后端里**静默丢弃**
（上行成功、看板上就是没有），这一条是表与代码之间唯一不会漂的对拍。

**反向验证（7 处变异，逐处报错，还原即绿）**：表里两条同名 / 表里多一条产不出的 / 取值形态
`int64`→`int` / 给无界属性标 label-safe / 工具行不挂环节 / 悬空父不清 / 行标识格式改一处。
另有一条**前提钉子**（`TestRowIDsAreUniqueWithinTurnOnly`）：它钉住「同号环节跨轮共用一个标识」
这个事实——哪天真把它改成全局唯一，那条会红，提醒回来重读边界依据，而不是顺手改断言。

> 第一次做「表里缺一条」的变异时只得到**编译失败**（我把 Name 改成了一个不存在的常量）。
> 那不是判据的功劳，故重做为「重名」与「多余条目」两个能编译的变体。**编译失败不算反向验证。**

### P1-2 监护者（第二双眼睛）——**列着，但先不做**

`guardian-context` 是**机制**不是产品（上下文装配 + contributor scope + 有界历史 +
成本估算），按本仓判据可以归这里。但我们所有判定都是规则型
（`policy`/`cmdpolicy`/`gatecheck`），缺「让另一个模型在决策前看一眼」的装配面。

**先不做的理由**：现在没有任何宿主提出这个需求。做出来就是**无验收的期货**——
与本仓「判定面不许假装兑现」相冲突。触发条件写死在这里：
**宿主第一次想拿 LLM 复审判定（而不是规则判）时，才做这一段**。

### P2 观察位（本轮不做）

| 项 | 为什么不是现在 |
|---|---|
| `jobs` / `schedule` | DSH 有机制；我们的 `pending` 只解决反问挂起。定时/长任务的真实需求还没出现，先看宿主 |
| 上下文片段类型化 | `sessionref` 已有来源+失效，缺的是「按类型准入 + 按类型限额」。**等第二个消费方**出现再抽，否则是替一个假想的调用方设计 |
| 凭据引用 | 落点在上游（keyring）与宿主（注入时机）。本仓有 `pii`/`logredact` 止痛，先不动 |
| `skilldisclose` × `skillkit` | 渠道不同（我们=**选择判定**，上游=**渲染投递**），不是并存实现；但**预算语义各判一次**（我们的 `Result.Truncated` vs 上游 `RenderList` 的降级链）⇒ 两家口径要对齐，别让宿主以为是一回事 |
| 进程加固 | Codex 做的是 pre-main 级（禁 core dump / 禁 ptrace / 剔 `LD_PRELOAD`）。我们已用 `procx.ChildEnv` 白名单 + 判据挡住「带进坏变量」这一半；「子进程可被 ptrace」这一半没有判据能静态判，暂列观察位 |

### 明确**不该**在本仓补（重申，一条没变）

Coze 的工作流编辑器 / 知识库 / 多租户（参照系冷冻第 4 个月，没有活跃参照的维度更不该争）；
插件树与动态包（上游 `oagentkit`）；完整 Agent Loop / IM 通道 / 云任务（宿主）；
OS 级沙箱本体（bwrap/landlock/seatbelt）；记忆的存储与摘要生成。

---

## 四、还需要怎么做（顺序 = 依赖顺序，不是优先级排序）

> ✅ **1（先接眼）、2（补兑现件）、3（遥测语义映射）均已走完**：1 与 2 于 2026-11-07 完成，
> 含第 2 步要求的「端到端一条真请求」（记录见 §三 P0-1「落地记录」与「验收补记」；补做验收时
> 还顺手抓到 `Spec.Allow` 被当成直连豁免的缺陷）；3 于同日晚上完成（记录见 §三 P1-1「落地」）。
> **剩下的第 4 步是「有需求才做」**：监护者（触发条件写在 P1-2）、`jobs`/`schedule`、片段类型化
> ——它们不是「下一件该做的事」，而是「等它来」。

1. **先接眼（本仓，半天）**：`ptc` 在 spawn 前调 `egress.SatisfiedBy`。
   现在「有接口没人调」= 「有声明、没人判」这条老毛病的第 N 次复现——
   而它恰恰是最便宜的一次修复。
   **验收**：注入一个不实现 `Enforcer` 的后端 ⇒ 用例红。
2. **再补兑现件（`guardx`，1–2 天）**：最小 HTTP `CONNECT` + allow/deny + 默认拒绝，
   实现 `Enforcer`。
   **验收**：§三 P0-1 的三条用例 + 与 `egress.Verify` 的 env 对接跑通（端到端一条真请求）。
3. **然后才谈遥测（本仓，1 天）**：`trajectory/otel.go` 的 seam → 映射表，
   底座直接用 `oekit/tracing`，**不引第二个 OTel 依赖**。
   **验收**：属性名逐条有主的用例 + 一条 e2e span 断言。
4. **有需求才做**：监护者（触发条件见 P1-2）、`jobs`/`schedule`、片段类型化。
5. **每一步都要反向验证**：注入坏形态 ⇒ 红；撤掉实现 ⇒ 红。
   这是本仓唯一真正降低未来成本的做法，比多写一个包重要。

> 第 1 步与第 2 步的次序不能换：先让判定**会问**，再让后端**能答**。
> 反过来的话，代理写完了也没人问它——那就是又一个「表躺在 policy 里」的故事。

---

## 五、核实方法（可复现，2026-11-07 实跑）

```bash
# 0. 网络：本机到 github.com:443 的 HTTPS 不通（curl 28 超时），走 SSH 可读公开仓；
#    查询类一律 gh api（api.github.com 直连可用）。
#    拉取用：git fetch git@github.com:<org>/<repo>.git <branch>（不要改 remote 配置）

# 1. 「参照系还活着吗」——别再用记忆里的活跃度
for r in openai/codex deepseek-ai/deepseek-harness TencentCloud/Octop coze-dev/coze-studio; do
  echo -n "$r  "; gh api repos/$r --jq '.pushed_at'
done
# Codex 2026-11-07T02:09Z / DSH 11-04 / Octop 11-06 / Coze 2026-07-29（冷冻）

# 2. Codex 这轮动了什么（59 提交 / 300 文件，按目录聚合）
gh api repos/openai/codex/commits/main --jq '.sha[0:10]'          # 0c58612c3c
gh api repos/openai/codex/compare/ddd1812...main --jq '{ahead_by, total_commits, files:(.files|length)}'

# 3. crate 数（两种口径都给，别抄单一数字）
ls -d codex-rs/*/ | wc -l                                          # 124
git -C ~/my_project/Codex ls-tree -d --name-only ddd1812 codex-rs/ | wc -l   # 126

# 4. ★ 本轮最关键的两条核实：代理与兑现端到底有没有
grep -rli 'socks5\|golang.org/x/net/proxy\|httpproxy' ~/oagentkit/*/ --include='*.go' | grep -v _test
#   ↑ 空 ⇒ 家族里没有进程级代理
grep -rn 'egress.Enforcer\|SatisfiedBy(' ~/my_project/agent-harness ~/oagentkit ~/my_project/bianque \
  --include='*.go' | grep -v _test | grep -v gomodcache
#   ↑ 只有接口声明与文档，无实现、无调用点
grep -rn 'func .*Egress()' ~/oagentkit/guardx/sandbox/*.go        # 空 ⇒ 沙箱后端不声明出口能力

# 5. OTel 底座在哪（结论：底座有，映射无）
ls ~/oekit/tracing/go.mod && grep -rln 'go.opentelemetry.io' ~/oekit/tracing/*.go | head -3
grep -rn 'go.opentelemetry.io' ~/my_project/agent-harness/trajectory/*.go   # 空 ⇒ 本仓不引依赖（seam）

# 6. 本仓还开着什么（不是「没 grep 到」，是结构上没有）
grep -rli 'guardian\|critic\|reviewer' ~/my_project/agent-harness --include='*.go' | grep -v _test   # 空
grep -rli 'cron\|scheduler' ~/my_project/agent-harness --include='*.go' | grep -v _test              # 空
```

**没有验证到的**（写清楚，别当成已验证）：

- 四家参照系的**运行时行为**仍未实跑，只读源码 / 目录结构 / README / crate 文档；
- **Codex 工作树仍停在 `ddd1812`**：SSH 全量 fetch 过慢已中止，增量信息全部走
  `gh api compare`，未拉取新提交的文件内容（按目录聚合的增量已足以判断「无新机制面」）；
- 上一版「134 个 crate」与实测 124/126 的差异**没查清**，不作为结论；
- 宿主 `bianque` 的检索走 `tools.grep`（受仓库 ignore 规则约束），
  零命中只说明「没有明文实现」，不排除生成代码或其他形态；
- Coze 因持续停更，只确认活跃度，未做能力面深挖（与上一版同口径）。

---

## 六、本轮本仓做了什么（供下轮对照，不是计划书）

| 改动 | 性质 | 与本文的关系 |
|---|---|---|
| `archcheck` 全量环境判据纳入 `procx.InheritAllExcept` | 判定面收严 | 判定宽度 +1 格（§一.3） |
| 三处散文补第五条判据 | 文档同步 | 同一条纪律的落地 |
| `eventbus` 跟上上游「退订即关闭」契约 | 修复回归 | 依赖升级的连带成本 |
| `eventbus_test` 拆分（434 → 353+95 行） | 结构阈值 | 机械阈值的真阈值性 |
| 上游家族追平 7 项（runtime v0.10.0 / pkg v0.36.0 等） | 依赖地板 | §零 末那条「数字要有主」的来源 |
| 发版 v0.18.0（CHANGELOG + tag + Release） | 交付 | — |
| **P0-1 兑现面收口**（`guardx/sandbox.EgressMode` + `guardx/egressproxy` + `ptc.EgressBackend`） | **兑现**（不是判定） | §三 P0-1 结账 |
| `guardx` v0.21.0 发版；本仓依赖地板 v0.19.0→v0.21.0（连带 `oekit/pkg` v0.37.1） | 依赖地板 | 兑现件必须可 import |
| `archcheck` 的 `egress_paths` `proxy` 判据补第二半（能力问询 `SatisfiedBy`） | 判定面收严（配套） | 旧判据对「只生成 env」静默放行 |
| 新增 6 条用例（4 条 ptc 兑现门成对 + 2 条门禁成对：只生成 env 红 / 非 egress 的 SatisfiedBy 不认） | 反向验证 | 「从没见过它触发」= 没有规则 |
| 发版 v0.20.0（原定 v0.19.0，与并行会话的 windows 支持撞号后重编） | 交付 | — |
| **P0-1 遗留验收**：`egress/e2e_test.go` 真代理 + 真请求三条（名单内 / 未列名 / 拒绝优先） | 验收闭环 | 兑现不只看 env 长得对，要看流量真经过代理 |
| **修 `Spec.Allow` → `NO_PROXY` 的安全缺陷**：新增 `Spec.Bypass`，`Verify` 增判绕过名单（语义等价比较） | **修复**（安全方向） | 允许名单被当成豁免名单——「声明强度 ≠ 实际强度」的又一次实例 |
| 发版 v0.20.1 | 交付 | — |
| **P1-1 遥测语义映射**：`trajectory/otelmap.go` 契约表（11 属性 + 3 禁区 + `TraceBoundary`）+ `Span.ParentID` + 悬空父解析 | **契约面**（不是底座） | §三 P1-1：底座在上游，本仓缺的是「属性有主」 |
| 7 处变异反向验证（表多 / 表少 / 类型错 / 无界标 label-safe / 不挂环节 / 悬空父不清 / 标识格式改一处） | 反向验证 | 双向判据缺一向就是半个判据 |

**一句总结**：这一轮补的是**兑现**不是判定——P0-1 的三条最小可用判据（`Enforcer` 有实现 /
代理自带用例 / `ptc` 在真实路径上问能力）全部落地，且第一件事是先把**词表上移**：
此前那条接口在结构上只可能由宿主实现，所以「声明面全齐、兑现面为空」不是疏忽，
是类型系统堵死了它。下一轮如果还要动这一片，先问「这个声明有兑现者吗」。
**P1-1 补的是「契约」不是「依赖」**：底座（`oekit/tracing`）本来就在上游，本仓缺的是一张
「每个属性有主」的表——没有它，宿主各认一套字段名，遥测**照常上行**，而跨宿主的看板与告警
全部落空，且这**不会报错**。所以判据也做成双向的：表里多一条要红（许愿池），少一条也要红
（映射写出了表里没有的属性）。同一把尺子在「父子怎么连」上表现为两条：单行映射只声明、
批量映射才解析悬空父——声明与兑现分开，才看得见「声明了但连不上」这件事。


补做端到端验收时抓到的那条缺陷（`Allow` → `NO_PROXY`）值得单独记一句：**它是被「真跑一遍」
抓到的，不是被「再读一遍」抓到的**。单元判据当时全绿，因为每条判据都只问「env 里有没有
这个东西」，没人问「这个东西的语义与字段名说的是不是同一件事」。同一天里先做完判定面再补
真请求，顺序也是对的——但真正的收获来自后者。
