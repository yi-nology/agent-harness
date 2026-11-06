# 横向对比与差距台账（2026-11-06 刷新）

> 前一版：[docs/GAP-ANALYSIS-20261021.md](GAP-ANALYSIS-20261021.md)。
> 本文是对它的**刷新**：五个参照系全部重拉到 2026-11-06 的最新提交后重做逐项对齐。
>
> **口径**：只比**机制**，不比产品面。判据仍是本仓那条——
> 「这个包需要知道自己在给谁干活吗？」需要 → 归宿主，不进本仓。
>
> **有效期**：文中的提交号、包数、版本号都是 **2026-11-06 实测快照**，
> 不受任何机器判据管辖；引用前请重跑 §五 的命令，别把它们当活事实。

---

## 零、参照系本身变了（这轮最该先看的一段）

| 参照系 | 2026-10-21 时的认知 | 2026-11-06 实测 | 变化 |
|---|---|---|---|
| **Codex** | 「完整 Agent Loop + OS 级沙箱 + 两层审批」 | `openai/codex` @ `ddd1812`（2026-11-06），**134 个 crate** | 10-21 以来 **100+ 提交**。新增/成型：`execpolicy`（Starlark 命令前缀规则）、`network-proxy`（本地 HTTP+SOCKS5 出口代理）、`memories`（读写两段管线）、`skills`、`hooks`、`plugin-host`（V8 插件进程，有界 IPC）、`message-board`+`agent-roles`+`agent-graph-store`（agent 协作面）、`worktree`、`guardian-context`（监护者审阅）、`otel` |
| **DSH** | 「一切皆插件，Cordis 插件树」 | `deepseek-ai/deepseek-harness` @ `fcc0ad4`，**0.2.2-rc.1** | 叶子包 **219 → 400**。新增 `team`（子 agent 树的任务归属与依赖）、`ptc-runtime`（Node/Python 程序执行，异步函数体各起新进程）、`telemetry`、`deliverables`、`document`、`ssh`、`computer-use`、`browser-use`。`subagent` 家族已含 `subagent-acp` / `subagent-claude-code` / `subagent-codex` |
| **Octop** | 「单进程自托管、专家团、IM 通道」 | `TencentCloud/Octop` @ `4022e24e`，**1.0.3b4** | `AgentTeams`（Beta）、**云端协作**（本机实例连另一实例、跨实例对话）、OpenSandbox 桥接、`octop-memory` 可迁移记忆、LDAP/WebAuthn |
| **Coze Studio** | 「活跃对标：工作流/知识库/多租户」 | `coze-dev/coze-studio` 最后提交 **2026-07-29**，最后发布 **v0.5.1（2026-02-05）** | ❗**已停更**。从「活跃对标」降级为「冻结参考」 |
| **本仓（yi-nology）** | v0.15.x，27 包 | **v0.17.0** @ `e37384a`，**31 个包**（README 包表口径，`make docsync` 判） | 新增 `gatecheck` / `guardset.Assembly` / `archcheck.execution_paths`（10-21 台账的 P0/P1 已结清），`ticket` / `trajectory` / `drain` / `internal/subhub` |

> **Coze 停更这条要单独记**：上一版把「Coze 的工作流 UI / 知识库 / 多租户」
> 当成「我们不补的产品面」。现在那个参照系不动了——**没有活跃参照的维度，
> 更不该去争**。剩下三家全部走「机制 + 互操作」，没有一家在比谁的 UI 更全。

---

## 一、结论先行

1. **上一版的核心结论已经过期，但只过期一半。**
   上一版判定「差距只在**声明的可撤销性**」；本轮 `gatecheck`（审批 × 档位联合判据）、
   `guardset.Assembly`（换整份 = 撤销）、`execution_paths`（声明面 × 执行路径一致性门禁）
   落地后，**那条缝基本合上**。

2. **新露出来的缺口是「判定面的宽度」，不是「判定的深度」。**
   我们的判定链已经**很深**（判定环、代次、CAS、幂等键、租约、栅栏、预算、失败分类……），
   但判定的**边界停在「工具调用」这一层**。工具**内部**发生了什么没人判：
   - 工具起的**子进程**（`ptc` 的子进程 Provider 走 `exec.Command` re-exec）发起的网络连接，
     `netguard` 管不到——它的判定挂在 Go 的 `http.Client`/`Transport` 上；
   - 工具内部执行的**命令本身**（`bash` 类工具的一条命令）没有任何判据；
   - 于是「要求了强隔离 / 要求了出站受限」这类**声明**，在子进程那一侧没有任何东西兑现。

   三家活跃参照系都在补这一层，而且补法一致：**Codex** 的 `network-proxy`（本地代理，
   进程级强制）+ `execpolicy`（命令前缀规则，`allow`/`prompt`/`forbidden`）；
   **DSH** 的 `sandbox-policy` + `sandbox-local`；**Octop** 的 Docker/OpenSandbox 后端。

3. **参照系集体转向「异构 agent 互操作 + 任务协调」，我们的 `dispatch` 停在准入判定。**
   `dispatch` 判的是「允不允许派」（allow 矩阵 / 深度上限 / 环检测），
   **不提供派出去之后怎么协调**：Codex 有 `message-board`（local/内存/远端可换后端）、
   `agent-graph-store`（父子拓扑，存储中立）、`agent-roles`（角色声明文件）；
   DSH 有 `team`（任务归属 + 依赖 + 任务板）；Octop 有 AgentTeams + 跨实例。
   更关键的是**跨 harness 委托**：DSH 的 `subagent-claude-code` / `subagent-codex` / `subagent-acp`
   能把活交给**别人家的 harness** 跑，Octop 的 ACP 双向同理。

---

## 二、逐维度对齐表

| 维度 | Codex | DSH | Octop | 本仓 | 判 |
|---|---|---|---|---|---|
| Agent Loop | 完整 | 完整（Loop 本身是插件） | 完整（LangGraph） | 无（`contract` 只给边界） | ❌ 宿主的事 |
| 环节状态机 | — | plan-mode / goal | 工作流 | `statemachine` | ✅ 够 |
| **命令级判据** | `execpolicy`：前缀规则 + `allow/prompt/forbidden` | `sandbox-policy` | 沙箱后端 | **无**（`policy` 只到工具粒度） | 🔴 **P0** |
| **子进程出口强制** | `network-proxy`：本地 HTTP+SOCKS5，allow/deny + 只读模式 | `sandbox-policy` | 容器网络 | `netguard` 仅覆盖 Go HTTP 客户端 | 🔴 **P0** |
| 沙箱档位 | bwrap/landlock/seatbelt/windows + integrity + violation | local/policy/windows-acl | Docker/OpenSandbox | `sandbox`（档位 + 登记簿） | ✅ 声明齐，兑现归宿主 |
| 审批 × 档位联合判据 | 两层都同意才放行（产品内建） | `interaction/user-approval` + `permission-presets` | 工具审批 | **`gatecheck`**（本轮已补） | ✅ 本仓更强（单点可判） |
| 命名权限预设 | `:read-only` / `:workspace` / `:danger-no-sandbox` | `permission-presets` | 有 | **无**（Level 与 Mode 要宿主各拼一遍） | 🟡 **P1** |
| 渐进披露 | 工具 schema + 技能按需 | `tool-skill` / `tool-subagent` | 专家库 | `tooldisclose`（工具层，确定性打分） | ✅ 够（技能层见 P2） |
| PTC / 代码模式 | `code-mode` + gRPC 会话 + V8 | `ptc-runtime`（Node/Python，异步体各起新进程） | — | `ptc`（goja + 子进程 Provider） | ✅ 机制齐 |
| 上下文计量/压实 | 扩展预算前置检查 + 压实 | `compaction` + `token-meter` + `spill` | 记忆 + 精简 | `budget` / `spill` / `offload` | ✅ 不弱 |
| **长期记忆** | `memories` 读写两段管线 + 引用解析 + 用量遥测 | `session-reference` / `agent-instructions` | `octop-memory`（可迁移） | **无** | 🟡 P2（机制可拆，见 §三） |
| **可观测** | `otel` + `rollout-trace` | `session-telemetry-otel` + `telemetry` | — | `trajectory`（事件投影）**无 OTel 映射** | 🟡 **P1** |
| **agent 协调面** | message-board / agent-roles / agent-graph-store | `team`（归属 + 依赖 + 任务板） | AgentTeams | `dispatch`（只判准入） | 🟡 **P1** |
| **跨 harness 委托** | `external-agent-migration`（搬配置） | `subagent-claude-code/codex/acp` | ACP 双向 | **无** | 🟡 P2 |
| 插件/扩展 | `plugin-host`（V8 进程，有界 IPC） | 动态 Cordis 包（define/stop/undefine） | 插件包 | 无（归上游 `oagentkit`） | ❌ 上游的事 |
| Hooks 引擎 | `hooks`（规则/事件/环境/MCP/输出溢出） | `hooks`（`hook-protocol` + claude-code/codex 桥） | — | **无** | 🟡 P2（两家都做了，形态分了叉） |
| 工作区隔离 | `worktree` | `workspace` | 可插拔后端 | 无 | 🟡 P2 |
| 恢复/续跑 | thread-store | `session-checkpoint-policy` + sqlite/jsonl | LangGraph checkpoint（无 CAS） | `checkpoint`（跨进程 CAS + 续跑而非重放） | ✅ 本仓更强 |
| 跨进程副作用 | — | — | — | `sideeffect` / `commitgate` / `ticket` | ✅ 本仓独有 |
| 门禁/文档纪律 | gen + 校验 | doc-sync + 工具目录生成 + 双语配对校验 | — | `archcheck` + `docsync` + 十余条 policy | ✅ 同档（DSH 的双语配对校验值得抄，见 P1-5） |

---

## 三、本仓该补的（按「值 ÷ 成本」排序）

### P0-1 命令级判据（`cmdpolicy`）：把判定面从「工具调用」下探到「工具内部的命令」

**为什么是 P0**：这是当前**最大的一片未判面**。工具调用被 `policy` / `tooldisclose` /
`watchdog` 围得很严，但一条 `bash` 工具内部的命令串——`rm -rf`、`curl | sh`、
带凭据的 `git push`——没有任何机制面看得见。宿主只能靠正则黑名单，而黑名单
在「写法和语义不一致」时必然失效。

**抄什么**：`codex-rs/execpolicy`（前缀规则 DSL，Starlark 语法）：

```python
prefix_rule(pattern=["git", "push"], decision="prompt",
            justification="Use a PR instead.")
prefix_rule(pattern=["rm"], decision="forbidden",
            match=["rm -rf /"], not_match=["rmdir tmp"])
```

三条设计值得照搬，且**都不是「写个规则引擎」这件事本身**：

1. **三态裁决 `allow/prompt/forbidden`**——与 `policy` 的 `Mode/Verdict` 同词汇，
   不引入第二套口径；`prompt` 直接接 `gatecheck`（需人审优先于档位不足）。
2. **规则自带样例（`match`/`not_match`），加载期即校验**。这是「从没见过它触发」
   那条纪律的**机器化**：一条判据如果连自己声明的正例都判不出、反例都误判，
   加载就失败，而不是等到事故那天。
3. **`justification` 随裁决一起回给模型**——拒绝不是一句话，是「为什么 + 该用什么替代」。

**落点**：新包 `cmdpolicy`（纯函数判据 + 加载期自检）；接线走已有的判定环
（`invoke.Stage` 同款），工具侧用 `tooldisclose`/「拒绝即激活」的既有语义。
**验收**：反向验证——把 `not_match` 判成 match 必红；`forbidden` 优先级反转必红。

### P0-2 子进程出口的**声明面一致性**判据

**现状实测**：`netguard` 的判定面是 `NewClient` / `NewTransport` / `CheckURLWith`；
`ptc/childpipes.go:62` 用 `exec.Command` re-exec 起子进程。**两者之间没有任何桥**——
子进程里的出站流量完全在守卫之外，「声明了受限出口」在那一侧是空话。

**这不是「再写一个 netguard」**（那是第二套 IP 策略，本仓明令禁止）。
本仓该给的是**判据 + 接线件**，三层：

| 层 | 归谁 | 形态 |
|---|---|---|
| 出口代理（HTTP/SOCKS5，allow/deny） | 上游 `guardx`（与 `egress` 同族） | 参考 Codex `network-proxy`：本地代理，子进程通过 env 指过来 |
| 「子进程是否在强制面内」的判定 | **本仓** | 与 `execution_paths` 同款：判据进 `archcheck`，声明进 policy |
| 装配 | 宿主 | 一行把代理挂进 `ptc` 的子进程 env，且**不可关**（关掉即判据红） |

**验收**：与 `architecture-policy.yaml` 的 `execution_paths` 同一套——
注入「子进程未挂代理」⇒ 门禁红；本仓自己的 `ptc` 子进程路径先过。

### P1-1 命名权限预设 → `(Level, Mode, Allow)` 展开件

Codex 有 `:read-only` / `:workspace` / `:danger-no-sandbox` 三档，
DSH 有 `interaction/permission-presets`，本仓**有零件没组装件**：
`sandbox.Level`、`policy.Mode`、`netguard.Allow` 各是一个参数，宿主每次自己拼，
必然拼歪（拼歪的形态是「档位配松了但没人发现」）。

**做法**：一个 `preset` 包——**具名**预设展开成三者的一**整份**（沿用
`netguard`/`guardset` 的「换整份」纪律，不做可变字段），并让 `gatecheck`
接受预设名而不是三个散参数。**不做**的事：不做「自定义预设存库」（那是宿主资产）。

### P1-2 遥测 seam（`trajectory` → OTel）

本仓已有 `trajectory`（事件流投影成轨迹行），但**没有语义约定映射**：
宿主想把轨迹接进 OTel 只能自己认字段。Codex 的 `otel` / DSH 的
`session-telemetry-otel` 都以「**business 选事件、transport 负责编码投递**」分层。
抄这个分层：本仓出**语义映射**（属性名与取值域），投递归宿主/上游。

### P1-3 跨会话引用快照

DSH `session-reference` 的定位是「跨会话快照准备」：把另一会话的结论作为
**有界**片段注入当前上下文。本仓 `budget` 管的是「窗口内怎么压」，
**不管「引用外部会话时边界在哪、什么时候失效」**。这是 `offload`/`spill` 的第三个兄弟，
且与「派生状态只有一个真相」那条纪律同源：快照必须带**来源与失效点**，
否则引用的是「当时的结论」却当成「现在的结论」用。

### P1-4 agent 协调面（消息板 / 任务板）

`dispatch` 只答「能不能派」。派出去之后：谁负责哪个子任务、依赖谁、
怎么回消息、怎么中断——全部没有机制面。Codex 把三件事**分开**做
（`agent-graph-store` 存拓扑、`message-board` 收发、`agent-roles` 声明角色），
DSH 的 `team` 把「任务归属 + 依赖 + 任务板」收成一个服务。
**本仓的落点**应是**存储中立的契约 + 一致性判据**（类似 `checkpoint.StoreCAS`）：
- 拓扑/任务板的**存储**归宿主（本仓只给接口，不给参考实现——上一版已定过这条）；
- **并发正确性判据**归本仓（判据套件，像 `sidetest` 那样给出可跑的断言）。

### P1-5 抄一条纪律：规则/文档的**样例自检**

我们在 `archcheck` 里已经做了「反向验证」，但那是**测试里**做的；
Codex 的 `execpolicy` 把样例**写进规则本身**并在加载期校验。
DSH 更进一步：`docs/*.md` 是**生成**的（`verify-tool-catalog` / `gen-doc-graphs`），
**双语文档还要过配对校验**（`verify-translation-pairing`）。
⇒ 我们的 `architecture-policy.yaml` 可以给每条规则加 `examples` 段（正例/反例），
`archcheck` 加载时先自检再判仓库——**把「这条规则还活着吗」变成加载期的断言**。

### P2（趋势观察位，本轮不做，但要留着看）

| 项 | 为什么是 P2 而不是 P0 |
|---|---|
| **记忆读写管线** | 摘要生成已定归宿主（`budget` 留了 `Summarizer` 接口）。机制可拆的只剩「注入-引用-打标」三段，但缺了宿主侧的写入时机就无从判 |
| **Hooks 引擎** | 两家都做了，但形态**已经分叉**（Codex = 外部进程协议 + 规则文件；DSH = 动态插件包）。此时定我们自己的形态，等于替宿主选边 |
| **技能披露（skills）** | 与 `tooldisclose` 同族但语义不同（按提及 / 按相关度 / 按资源文件）。先看宿主有没有真需求 |
| **异构委托（ACP / claude-code / codex）** | 方向明确（三家都在做），但它是**协议面 + 传输**，落点大概率上游；先等 `oagentkit` 是否出 ACP 原语 |
| **工作区隔离（worktree）** | 与文件系统分区有关，而本仓至今不碰 FS 语义 |

---

## 四、明确**不该**在本仓补的

| 主流做法 | 为何不补（重申） |
|---|---|
| Coze 的工作流编辑器 / 知识库 / 多租户 | 资产；且该参照系**已停更**（2026-07-29），没有活跃参照的维度不该去争 |
| 插件树 / 动态包（Cordis、Codex V8 插件宿主） | 上游 `oagentkit` 的事；本仓造第二套 = 并存实现 |
| 完整 Agent Loop、IM 通道、浏览器/远程桌面、云任务 | 宿主的事（`contract` 就是那条边界） |
| OS 级沙箱本体（bwrap/landlock/seatbelt） | 宿主/上游；本仓只**如实自报档位**，不谎报 |
| 记忆的存储与摘要生成 | 宿主的事（`budget` 的 `Summarizer` 接口即边界） |

---

## 五、核实方法（可复现，2026-11-06 实跑）

```bash
# 0. 网络：github.com 直连不通时走本机代理 7897
P=http://127.0.0.1:7897

# 1. 各参照系拉到最新（本机 :443 形态绕过 url.insteadOf 的 ssh 改写）
cd ~/deepseek-harness && git -c http.proxy=$P pull --ff-only      # 0.2.2-rc.1 @ fcc0ad4
cd ~/my_project/Octop   && git -c http.proxy=$P pull --ff-only      # 1.0.3b4  @ 4022e24e
git clone --depth 1 https://github.com:443/openai/codex.git Codex   # ddd1812
git clone --depth 1 https://github.com:443/coze-dev/coze-studio.git Coze

# 2. 「参照系还活着吗」——别再用记忆里的活跃度
gh api repos/coze-dev/coze-studio --jq '.pushed_at'                # 2026-07-29
gh api 'repos/coze-dev/coze-studio/releases?per_page=1' --jq '.[0].published_at'

# 3. Codex 的能力面（crate 名即能力清单）
ls ~/my_project/Codex/codex-rs | wc -l                             # 134
head -20 ~/my_project/Codex/codex-rs/execpolicy/README.md          # 前缀规则 + match/not_match
head -20 ~/my_project/Codex/codex-rs/network-proxy/README.md       # 本地 HTTP+SOCKS5 强制面

# 4. DSH 的规模（叶子包数比包组数更能说明拆分粒度）
ls -d ~/deepseek-harness/packages/*/*/ | wc -l                     # 400
ls ~/deepseek-harness/packages/team ~/deepseek-harness/packages/subagent

# 5. 本仓的缺口（不是「没 grep 到」而是「结构上没有」）
ls -d */ | tr -d '/' | tr '
' ' '                                  # 无 hooks / skills / memory / otel 包
grep -n 'func NewClient|func NewTransport' netguard/client.go      # 判定面 = Go HTTP 客户端
grep -n 'exec.Command' ptc/childpipes.go                            # 子进程不经过 netguard
```

**没有验证到的**（写清楚，别当成已验证）：
- 三家参照系的**运行时行为**均未实跑，只读源码/文档/目录结构；
- DSH 的 `CHANGELOG` 不存在于仓库根，版本变化靠 `git log` 与 `package.json`；
- Coze 因已停更，只确认活跃度与顶层结构，未做能力面深挖。

---

## 六、落地状态（2026-11-06 本轮，全部已进仓）

> 本节是 §三「该补的」的**结账单**，不是计划书。每条都写了「在哪儿」与「怎么复验」，
> 复验一律走命令，不靠读文档。**P2 五项超出了本节的原始建议**——原表写的是
> 「本轮不做，只留着看」，实际按指令一并落地了，故单列一行注明。

| 缺口 | 落地物 | 证据路径 | 复验命令 |
|---|---|---|---|
| P0-1 命令级判据 | `cmdpolicy`（409 行）：前缀 token 规则 + 三态裁决 + 规则自带样例（`New` 加载期跑真实判定路径） | `cmdpolicy/` | `go test ./cmdpolicy/ -cover` |
| P0-2 出口**声明面** | `egress`（207 行）：三档单调 + `Validate` 自检 + `Env`/`Verify` 兑现 + `SatisfiedBy` 后端问询；`ModeDenied` 拒绝伪造 | `egress/` | `go test ./egress/` |
| P0-2 出口**执行面** | 新门禁 `egress_paths`（走 `go/ast`）：每个子进程启动点必须登记档位，`proxy` 必须在启动函数里看得见 env 兑现证据，`denied` 直接红 | `archcheck/egresspaths.go`、policy 的 `egress_paths:` 段 | `make architecture-check` |
| P0-2 ptc 真接线 | `SubprocessOptions.Egress`；`newChildPipes` 用 `spec.Env(base)` 生成子进程 env，生成失败即 `closeAll` + 不起子进程（fail closed），零值行为不变 | `ptc/subprocess.go`、`ptc/childpipes.go` | `go test ./ptc/` |
| P1-1 命名权限预设 | `preset`（232 行）：`(Level, Mode, Egress)` 三面收成具名只读件；三面全松须显式 `Unsafe`+理由；同义两名字即报错 | `preset/` | `go test ./preset/` |
| P1-2 遥测 seam | `trajectory/otel.go`：跨 OTel 的接缝，**不引入 otel 依赖**（坐实了「seam 不等于接线」） | `trajectory/otel.go` | `go test ./trajectory/` |
| P1-3 跨会话引用 | `sessionref`（214 行）：来源/版本/时间随快照走，版本**回退**与 TTL 到期都判 stale，渲染只标注不拒绝 | `sessionref/` | `go test ./sessionref/ -cover`（100%） |
| P1-4 协调面 | `taskboard`（403 行）+ `boardtest`：存储中立契约（不给实现）、`Check` 十类不变量（含依赖无环）、`ConcurrentClaim` 并发判据（成功者必须恰好 1 个） | `taskboard/`、`taskboard/boardtest/` | `go test ./taskboard/... -cover` |
| P1-5 样例自检纪律 | `require_rule_examples: true` + `archcheck` 的样例自检：policy 里每条规则必须带能过的样例，**不带即红** | `archcheck/examples.go`、policy 顶部开关 | `make architecture-check` |
| P2 五项（**原表建议不做**） | `skilldisclose`(290) / `hookengine`(291) / `memorypipe`(223) / `delegate`(179) / `worktree`(232) | 各自目录 | `go test ./skilldisclose/ ./hookengine/ ./memorypipe/ ./delegate/ ./worktree/` |

**全局复验一条命令**：`make check`。它现在同时跑

```
竞态(-race, CGO_ENABLED=1) / 漏洞(govulncheck) / 交叉构建(linux×2 + darwin) /
覆盖率(全局 60% + 逐包下限 70%) / 8 条规则 + 5 条 go/ast 结构规则 + 43 包依赖方向 + README 包表
```

**这轮暴露出来、值得单独记的三件事**：

1. **「有声明、没人判」是本轮最反复出现的形态**。`execution_paths`（沙箱档位）、
   `modules.requires`（依赖方向）都曾有过「表躺在 policy 里、反序列化成 `[]struct{}`」
   的历史；本轮新加的 `egress_paths` 与 `require_rule_examples` 一开始就带执行体，
   而不是先声明再补——**这一条是本轮唯一真正降低了未来成本的改动**。
2. **判据的量纲要跟前提对齐**。`max_methods_per_type` 对 façade 型类型不成立（它量的是
   API 表面积，不是复杂度集中度），故阈值改判**实质方法数**；`egress` 的
   「后端不声明就按最弱处理」是同一件事的另一面——**沉默不能算作满足**。
3. **期货式的判定面不许「假装兑现」**。`ModeDenied` 的 `Env` 直接报错而不是尽力而为；
   `ptc` 在 env 生成失败时不起子进程。两条都是「失败形态必须是变红，不能是变绿」的落地。

**没有做的**（写清楚）：三家参照系的**运行时行为**仍未实跑（只读源码/目录结构）；
`cmdpolicy` 不内置任何领域命令词表（那是宿主资产，本仓给的是判据与样例机制）。

