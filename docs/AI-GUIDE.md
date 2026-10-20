# AI 改造指南（agent-harness）

> 这份文档写给**要动手改这个仓的 AI 编码代理**。
>
> 它是**操作型**的：回答「我现在要做 X，该改哪些文件、什么顺序、会被哪条门禁拦、
> 怎么验收」。**为什么**这么设计由 [AGENTS.md](../AGENTS.md) 回答；
> **是什么**由 [README](../README.md) 回答；**怎么接进自己产品**由
> [USAGE.md](USAGE.md) 回答（那篇是给宿主开发者的，不是给改本仓的人的）。
>
> 读本文前先读一遍 [AGENTS.md](../AGENTS.md) 的「边界纪律」与「工程门禁」两节——
> 那里是不可协商的约束，本文只讲怎么在约束下干活。

---

## 0. 怎么用这份文档

**别从头读。** 按你现在要做的事跳：

| 你的任务 | 直接跳到 |
|---|---|
| 第一次进这个仓，不了解结构 | [§1 心智模型](#1-一分钟建立心智模型) → [§2 上手路径](#2-十分钟上手路径) |
| 要加一个新能力（新包） | [§3.1 加一个新能力包](#31-任务-a加一个新能力包) |
| 要改某个已有包的判定逻辑 | [§3.2 改已有包的判据](#32-任务-b改一个已有包的判据) |
| 要加或改一条门禁规则 | [§3.3 改门禁规则](#33-任务-c加或改一条门禁规则) |
| 要引入/升级上游矩阵依赖 | [§3.4 动上游依赖](#34-任务-d动上游依赖地板) |
| 门禁报超长文件，要拆 | [§3.5 拆文件](#35-任务-e拆文件) |
| 要发版 | [§3.6 发版](#36-任务-f发版) |
| 门禁红了，不知道怎么修 | [§4 门禁速查](#4-门禁速查红了就查这里) |
| 上手期反复踩同一个坑 | [§5 高发失败模式](#5-ai-代理高发失败模式) |
| 改完了不确定对不对 | [§6 反向验证](#6-反向验证改门禁必做) |
| 只想要命令 | [§7 命令速查](#7-命令速查) |

---

## 1. 一分钟建立心智模型

### 1.1 这个仓是什么

**多智能体编排的通用运行时内核。** 一句话定位：

> **编排是代码，LLM 只做单环节。**

本仓只提供**确定性编排的机制面**（报告协议、环节状态机、事件总线、派发守卫、
审计策略、溢出存档、PII 脱敏、出站守卫等 24 个包），**不认识任何领域概念**。

### 1.2 判定口诀（改任何代码前先问这一句）

> **「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主，不进本仓。**

配套三条：

1. **判定语义归内核，判定依据归宿主。** 内核说「这是引导式直答还是诊断报告」，
   宿主决定「哪条算引导」——所以内核给结构化字段 + 注册表，不硬编码名字。
2. **展示文案归宿主。** 标题、兜底、品牌词一律走 `RenderOptions` 注入。
3. **测试夹具用合成数据。** 夹具是契约的示例，示例带领域味等于把契约钉死在某个领域上。

### 1.3 24 个包分三组

| 组 | 包 | 特征 |
|---|---|---|
| **机制面**（20 个） | `protocol` `statemachine` `contract` `eventbus` `spill` `budget` `offload` `toolfail` `tooldisclose` `toolname` `guardset` `ptc` `watchdog` `checkpoint` `commitgate` `delta` `pending` `drain` `pii` `netguard` | 真正的逻辑所在。改这些包 = 改本仓的对外能力。 |
| **门面**（3 个） | `dispatch` `policy` `reminder` | **只能有四种声明**：类型别名、常量别名、单条上游转发函数、允许集合内的 import。加真逻辑三者必居其一 → 门禁红。核心在 `oagentkit/*`。 |
| **门禁执行体**（1 个 + 2 个 CLI） | `archcheck` / `cmd/archcheck` `cmd/docsync` | policy 的可执行面。可导入包，宿主可复用同一套判据。 |

> 门面包不可变是**硬约束**，不是建议。`docs/OAGENTKIT-OVERLAP.md` 里那句
> 「保持门面，别再在本仓加逻辑」原本只是散文，散文不是门禁——所以才有了
> `harness-facade-stays-facade` 这条 AST 规则。

### 1.4 三条贯穿全仓的纪律（改代码时会撞上）

1. **门禁的失败形态必须是变红，不能是变绿。** 任何「改判定逻辑」的动作，
   必须同时给出「坏实现会红」的用例——见 [§6](#6-反向验证改门禁必做)。
2. **结构性判定走 `go/ast`，不走正则逐行看文本。** import 声明有六种以上书写形态
   （单行/块/别名/空白/点导入/括号同行），逐行正则必然漏且漏得毫无声响。
3. **派生状态只能有一个真相。** 「代次/版本/序号」这类计数不许开第二个计数器；
   需要全局视图时让它是真相的投影。

---

## 2. 十分钟上手路径

### 2.1 按这个顺序读

| 顺序 | 读什么 | 读多久 | 读完你要能回答 |
|---|---|---|---|
| 1 | [README](../README.md) 的「边界」与「包清单」两节 | 3 min | 哪个包管什么事 |
| 2 | [AGENTS.md](../AGENTS.md) 的「边界纪律」+「工程门禁」 | 5 min | 什么不能做、什么会红 |
| 3 | `architecture-policy.yaml` 的 `modules` 段（24 个 id + requires） | 2 min | 哪个包能依赖哪个 |
| 4 | 你要动的那一个包的包头注释 | 3 min | 这个包为什么长这样 |
| 5 | 那个包的 `_test.go` | — | 契约的实际形状（比注释准） |

> **包头注释是这个仓的特产**：每个包开头都有一段「为什么是这样而不是那样」，
> 包含真实事故的记录。改一个包之前先把它的包头读完——那里写着的往往正是
> 你正想重复的那个错。

### 2.2 先跑一遍，确认环境是绿的

```bash
cd /Users/zhangyi/my_project/agent-harness

make test                 # 全量单测（当前 99 个测试文件，~35s）
make architecture-check   # 架构门禁（当前 7 规则 + 3 门包 + 24 包依赖方向全绿）
```

**上手第一步就跑一次**，这样后面每次红都知道是自己造成的，不是本来就红的。

> macOS：一切 `go build` / `go test` 必须带 `CGO_ENABLED=0`（新 SDK 27.0 与
> CLT linker 不兼容 + 许可未接受）。Makefile 已内置，**直接用 make 目标**，
> 别手敲 `go test`。

### 2.3 认地图：包 → 典型改动

| 你要改的东西 | 去哪个包 | 注意 |
|---|---|---|
| 报告结构、解析容错、Markdown 渲染 | `protocol` | `report_types.go` 已近 400 行阈值；枚举位归一在 `report_normalize.go` |
| 环节状态流转 | `statemachine` | 纯函数件，79 行，零依赖 |
| 工具失败分类（可重试/退避/需人工/重发安全） | `toolfail` | 分类是**数据表**，不要散落 if |
| 渐进披露、披露门禁 | `tooldisclose` | 拒绝即激活，重试不绕路 |
| 多守卫合成拒绝 | `guardset` | 取「全部可重试才算可重试」的保守侧 |
| 工具名 → JS 标识符 | `toolname` | **单一真相**，`tooldisclose` 与 `ptc` 共用 |
| 沙箱内程序化编排 | `ptc` | 程序内 `host.<tool>()` 也要过判定环 |
| 续跑快照、序号闸 | `checkpoint` | 「续跑而非重放」三条硬规则 |
| 终态独占提交闸 | `commitgate` | 放闸靠**令牌**，不靠内容比对认人 |
| 输入侧上下文预算 | `budget` | 分段计量（区分工具面膨胀 vs 历史膨胀） |
| 输出溢出取回 | `spill` | 与 `offload` 是姊妹机制，别搞混职责 |
| 大对象外置 | `offload` | **两阶段**：首次只标记不替换 |
| 出站 SSRF | `netguard` | 拨号层闭窗 + 放行面具名 |
| 个人信息脱敏 | `pii` | 只回报信号，**阻断动作归宿主** |
| 依赖方向 / 门禁规则 | `archcheck` + `architecture-policy.yaml` | 先改 policy 再写代码 |

---

## 3. 改造任务手册

> 通用顺序：**先改声明（policy / 表），再改实现，最后跑门禁。**
> 反过来做会得到「代码写完门禁红，再回头补声明」的双份工作。

### 3.1 任务 A：加一个新能力包

**判据**：这个能力是机制吗？换个宿主还要改吗？要改 → 属于宿主，别加。

**改动清单**（按顺序，漏任何一步门禁立刻红）：

1. **`architecture-policy.yaml` → `modules` 段**，先加条目：

   ```yaml
   - id: 新包名
     roots: [新包名]
     requires: []        # 真实依赖，不是理想形态
     owner: harness
     managed: true
     notes: ...
   ```

   - `requires` 填**真实**依赖（不是「打算用」的）。内部包写包名，
     第一方矩阵写 `ext:github.com/oagentkit/xxx` 这种**矩阵模块级**前缀。
   - `ext:` 前缀必须落在 `dependency_direction.ext_prefixes` 内
     （当前是 `github.com/oagentkit` + `github.com/oekit` 整体）。

2. **`thresholds.scan_roots`**：把新包加进去。
   `scan_roots` 必须**覆盖 `modules` 段的全部 roots**——漏一个，该包对
   「超长文件」「god object」两条阈值**完全隐形**（policy 自校验会强制两者一致）。

3. **写代码**（生产代码 + `_test.go`）。

4. **`README.md` 包表**加一行，格式固定：

   ```markdown
   | `新包名` | 123 | oekit/pkg | 一句话职责 |
   ```

   - 行数口径：`*.go` **不含** `_test.go`。
   - 依赖列：仓外 import，取 module 前两段，**逐项相等**才过。
   - 职责列不能为空。
   - 行数按 **25% 相对容差**判定——容差内的逐行涨落不拦，跨量级误述拦。
   - 仓里有包但 README 没写 → 同样红（反向检查）。

5. **验收**：

   ```bash
   make architecture-check && make docsync && make test
   ```

**新包必带测试**：覆盖率门槛 `COV_MIN=60` 是真阈值，内核包新增必须带测试。

### 3.2 任务 B：改一个已有包的判据

改动清单：

1. **先读包的 `_test.go`**，确认现有契约钉住了什么。
2. 改实现。
3. **改判据 → 必须补反向验证用例**（见 [§6](#6-反向验证改门禁必做)）。
   只测「好实现能过」的用例，一个把数字比较写成字符串比较的错实现照样全过。
4. 跨包契约的改动 → **端到端用例**钉「派生 → 装配 → 执行」整条链，
   不要只测生产侧或只测消费侧（两侧漂移时两边都绿）。
5. 若该包的**外部依赖或生产行数**跨了容差 → 同步 `README.md` 包表。
6. 验收：`make check`（全套）。

**改判据时的三条自检**：

- 新判据是**结构性**的（函数体是不是只有一条转发、import 有几种写法）吗？
  是 → 走 `go/ast`（`archcheck/` 里已有现成模式可抄），不要塞进正则表。
- 新判据的**失败形态是变红还是变绿**？变绿 = 没生效（最贵的形态）。
- 判据涉及**计数/代次/序号**吗？是 → 检查是不是只有**一个真相**。

### 3.3 任务 C：加或改一条门禁规则

**规则源**：`architecture-policy.yaml` 的 `rules` 段（正则类）
或 `facades` 段（AST 类，门面包专用）。

**加规则**：

1. 先想清楚**这条规则拦的失效形态是什么**。写不出具体形态 → 这条规则
   是仪式，不是门禁。
2. `rules` 段条目：

   ```yaml
   - id: harness-xxx
     desc: 一句话说清拦什么
     severity: error
     scope: [.]
     include: ["*.go"]
     ignore_test: true          # 三态：true=只扫生产 / false=生产+测试都扫
     only_test: false
     scope_exclude: [cmd]
     forbid_regex: '...'        # 或 forbid_import_regex（判 import 路径，走 AST）
     negative_follow:           # 负向前置：RE2 不支持后顾断言，用结构化表达
       - anchor: 'strings.Contains(out, "'
         forbid_follow: 'xxx")'
   ```

3. **判 import 路径的规则用 `forbid_import_regex`**，不要用 `forbid_regex` 逐行看源码——
   单行 import、别名 import、括号同行全漏。
4. **例外必须显式登记**（`exceptions` 段：rule + path + line 三元组 + `until` + 拆除条件）。
   当前例外数 = 0。**不做静默豁免。**
5. **反向验证**（[§6](#6-反向验证改门禁必做)）：注入违规 → 门禁报错 + 退出码 1。

**改规则**：先确认现有规则**真的在判**——本仓有 5 处「规则照常出现在报告里、
severity 照常是 error，唯独一件事都不判」的历史静默失效（`AGENTS.md` 有逐条记录）。
所以改判据时，必须验证「坏实现会红」。

> **policy 自身的红线**：
> - 规则须有执行体（两个正则都为空 = 静默跳过，自校验会报「规则无执行体」）。
> - `id` 唯一。
> - `scan_roots` 须覆盖全部登记包。
> - 例外须写 `until` 与拆除条件。
> - 写不存在的字段会被 YAML **静默丢弃**（那条规则于是变成「声明还在、一条都判不了」）。

### 3.4 任务 D：动上游依赖地板

本仓处在 `oagentkit`（agent 原语）/ `oekit`（基础库）矩阵里，
上游**把能力搬家的频率不低**（2026-10-20 一轮就搬了两处：
`toolgate/invoke` → `guardx/invoke`、`obs/obsx` → `llm/obsx`）。

**升级依赖时**：

1. `go.mod` 里 require 的版本必须是**已发布的 tag**。未发布的新包用
   `make devlink`（见下），不要动 go.mod。
2. 用了矩阵模块 → 在该包的 `requires` 里声明 `ext:<矩阵模块>`。
   上游搬家时，**声明面必须跟着改**——搬家在本仓门禁眼里不能「什么都没发生」。
3. 地板抬高对下游是**隐式 breaking**：`go mod tidy` 不跑会报
   `updates to go.mod needed`，且报错不提真正原因。
   → 破坏性变更要写进 [README 的「下游升级须知」表](../README.md#下游升级须知破坏性变更)。

**本地并行开发（改上游仓时）**：

```bash
make devlink     # 生成 go.work，把 guardx 工作副本接入（不改任何 go.mod）
# ...改上游，本仓构建即时生效...
make devunlink   # 断开，构建回到 go.mod 声明的已发布版本
```

- **go.mod 零 replace**（`replace` 只在主模块生效 → 下游被静默忽略 → 断掉所有下游）。
- **workspace 期间别跑 `go work sync`**（会按构建列表回写各模块 go.mod，
  实测把 oagentkit 四个依赖整片剥掉）。要整合同步：先 `devunlink` 再 `go mod tidy`。
- `devlink` 可重复执行（已有 go.work 时走 `use -add` 追加，不重建）。

### 3.5 任务 E：拆文件

两条真阈值（`thresholds.enforce: true`，超限即非零退出）：

| 阈值 | 值 | 含义 |
|---|---|---|
| `max_file_lines` | 400 | 单文件行数上限 |
| `max_methods_per_type` | 25 | 单类型方法数上限（god object 判据） |

**拆分只为满足机械阈值。** 若某个模块拆完仍读不动，该**重新分组**，
不要靠继续拆小来假装问题解决了。

拆分时注意 `max_methods_per_type` 是**按类型**判的——把一个胖类型拆成两个胖类型
仍然红。**按职责分类型**，不是按行数分文件。

### 3.6 任务 F：发版

**顺序不可反**（本仓是按版本引入的下游依赖，发版本身就是给下游的契约）：

```
① 改代码并提交
② 生成 CHANGELOG 并提交（这个提交只改 CHANGELOG.md）
③ 打 tag
④ 推 tag
⑤ 发 GitHub Release
```

**② 的生成命令**（生成器必须是 matrix-tools 的当前版本）：

```bash
python3 <matrix-tools>/scripts/gen_changelogs.py \
    --repo agent-harness --root /Users/zhangyi/my_project \
    --next-version vX.Y.Z
```

> 生成器旧版有两个会**静默丢东西**的缺陷（分类渲染分支对 chore/docs/test 类提交
> 只取标题、body 整段丢弃；round 前缀使 conventional commit 解析失效）。
> 判据：**产物行数突然变少、或某版的说明只剩标题没有正文 = 踩到了这个坑**。

**② 的提交纪律**：生成 CHANGELOG 的那个提交**必须只改 `CHANGELOG.md`**。
生成器会过滤掉「只改 CHANGELOG.md」的提交（否则自我指涉）；一旦那个提交同时改了
别的文件，过滤失效——它就会漏在自己那一版的说明之外。

**tag 快照里的 CHANGELOG 必须已含该 tag 自己那一段**——顺序反了会重演上游那条
「已发布 tag 的 CHANGELOG 缺自己那段」的缺口（下游拿到模块缓存才发现，
而 `go mod download` 拿到的就是 tag 快照）。

---

## 4. 门禁速查：红了就查这里

一条命令跑全套（CI 只执行它）：

```bash
make check
```

| 目标 | 拦什么 | 红了怎么修 |
|---|---|---|
| `fmt-check` | gofmt 未格式化 | `gofmt -w <文件>`；文件集由 git 决定，不递归扫 `.*-work/` |
| `vet` | go vet | 按提示改 |
| `lint` | golangci-lint（未安装时**明确报错退出**，不静默跳过） | `brew install golangci-lint`；CI 钉 v2.14.0 |
| `test` | 全量单测 | 看失败断言 |
| `cover-gate` | 覆盖率 < 60%（当前实测 86.6%） | 补测试，别降阈值 |
| `architecture-check` | 7 条规则 + 3 门包 + 24 包依赖方向 | 见下表 |
| `docsync` | README 包表 ↔ 仓库实际 | 重算行数与依赖，改 README 表 |
| `modreplace` | go.mod 含 `replace` | 改用 `make devlink` |
| `modwork` | go.work / go.work.sum 被跟踪 | `git rm --cached` |

### 4.1 架构门禁的 9 条规则

| 规则 | 拦什么 | 典型修法 |
|---|---|---|
| `harness-no-domain-vocabulary` | 生产代码里的运维领域词 | 换成机制侧通用表述；词表属宿主 |
| `harness-test-fixture-domain-vocabulary` | 测试夹具用了真实运维词面 | **改合成数据**（不是改词表） |
| `harness-no-host-dependency` | 任何对宿主仓的 import | 定义接口由宿主实现 |
| `harness-no-domain-runtime-deps` | 对宿主仓 / 旧门面仓 / 已退役 `agentkit` 门面的 import | 改走 `github.com/oagentkit/*` |
| `harness-no-bare-goroutine` | 生产代码裸 `go func` / `go someFunc()` | 走 `concurrency/async.GoSafe` |
| `harness-no-printf-log-callback` | 公开 API 里的 `logf func(string, ...any)` | 留痕走 `logx.Delegating` 懒解析 |
| `harness-single-errors-package` | stdlib `"errors"` 与 `github.com/pkg/errors` | 只用 `oekit/pkg/errors` 一套 |
| `harness-facade-stays-facade` | 三个门面包长出实现 | 逻辑放上游 `oagentkit/*`，本仓只留转发 |
| `harness-module-dependency-direction` | 越界内部依赖 / 越界跨仓依赖 / **未登记包目录** | 改 `modules` 段登记 `requires` |

> 最后一条的第三种形态最隐蔽：**未登记的包对本门禁完全隐形**——
> 任何模块都能 import 它而不判违规。判定粒度是**包目录**，不是顶层目录。

### 4.2 门禁红了先自查的三件事

1. **是代码错了还是规则该改？** 后者必须**先改 policy 再写代码**，
   且在 commit 说明里写清理由。**不要为了变绿而绕过。**
2. **这条规则真的在判吗？** 本仓有 5 处「规则出现在报告里、severity 是 error、
   但一件事都不判」的历史静默失效。改判据时必须做反向验证。
3. **门禁是不是根本没跑？** 「从没见过它触发」的规则等于没有规则；
   「没人跑的门禁」也不等于有门禁——本仓有 `lint` 曾同时不在 `check` 也不在 CI。

---

## 5. AI 代理高发失败模式

> 这一节是本仓的**经验沉淀**。每一条都对应一次真实事故，
> 触发条件都是「AI 按最直觉的写法写代码」。

### 5.1 先写代码，后补声明

**症状**：门禁红，报「越界依赖」或「未登记包」。

**为什么直觉会错**：AI 倾向于「先把功能写完，配置最后补」。但本仓的门禁是
**声明先行**——policy 是声明面，代码是实现面，声明不写，实现对门禁隐形。

**做法**：见 [§3.1](#31-任务-a加一个新能力包) 的四步顺序。

### 5.2 测试夹具里写真实领域词

**症状**：`harness-test-fixture-domain-vocabulary` 报红。

**为什么直觉会错**：AI 写测试时**会为了让场景真实而引入领域词**
（真实主机名、真实告警文本）。但夹具是**契约的示例**——示例带领域味，
等于把契约钉死在某个领域上，宿主就再也套不进自己的场景。

**做法**：一律用**合成数据**（`host-a` / `node-1` / 中性描述）。

### 5.3 在门面包里「顺手加两行」

**症状**：`harness-facade-stays-facade` 报红。

**为什么直觉会错**：「这个 helper 放这儿更顺手」——但门面包只能有四种声明。
加真逻辑三者必居其一：要新类型、要多条语句、要有新依赖。**没有「就两行」的例外。**

**做法**：逻辑写进 `oagentkit/*` 上游，本仓只留单条转发。

### 5.4 双 import errors 包

**症状**：`harness-single-errors-package` 报红；或混用后 `errors.Is/As` 判定失效
（Not Found 变 500）。

**做法**：哨兵用 `oekit/pkg/errors.New`，包装用 `errors.Wrap(err, msg, kv...)`
（**变参 kv，不是 `Wrapf`**），判定用同包 `Is`/`As`，解包到根用 `Cause`。
链路保持**单 import**，不再双 import stdlib。

### 5.5 裸 `go func()`

**症状**：`harness-no-bare-goroutine` 报红。

**为什么直觉会错**：AI 觉得 `go func` 是最直接的并发写法。但**裸 goroutine 里的
panic 无法被外层 recover**（panic 发生在 goroutine 内部），会直接杀进程。

**做法**：走 `concurrency/async.GoSafe`。

### 5.6 公开 API 里塞日志回调

**症状**：`harness-no-printf-log-callback` 报红。

**为什么直觉会错**：AI 会觉得「给调用方一个 logf 回调最灵活」。但 printf 回调
一旦焊进公开签名就是**不可逆 API 债**。

**做法**：留痕走 `logx.Delegating` 懒解析，宿主用 `logx.SetDefault` 换后端。

### 5.7 给「派生计数」开第二个计数器

**症状**：并发下代次不唯一 / 回退，轮询方看到「6 → 6」以为没换过。

**为什么直觉会错**：AI 为了「方便查询」会加一个全局计数器。但两次交换拿到
同一代次的形态是**静默**的——不崩溃，只是偶尔给错答案，比崩溃难查得多。

**做法**：只允许一个真相；需要全局视图时让它是真相的**投影**。

### 5.8 判定环断链

**症状**：某个位置成了整条链上**唯一绕过所有门禁**的路径。

**为什么直觉会错**：AI 会把「程序化调用」当成特例（普通工具调用过判定环，
程序内 `host.<tool>()` 走另一条路）。但那正是最危险的缝。

**做法**：`ptc` 程序内调用与普通工具调用**走同一套 Stage 判定环**。

### 5.9 契约规则各写一份

**症状**：「生产侧派得出、消费侧认不出」。

**做法**：跨包契约的规则收成**单一真相**（如 `toolname`），
两侧共用；并用**端到端用例**钉「派生 → 装配 → 执行」整条链。

### 5.10 枚举位逐个字段补

**症状**：模型写 `"OUT"` 而非 `"out"`，语义被**反转**（scope 被翻成 in），
且不归一时条目**被静默丢弃**——连 warn 都没有。

**做法**：枚举位归一**收成一处共享口径**（`protocol/report_normalize.go`），
并用**枚举式**用例把「漏一个就红」变成机械判定。逐个字段补不算数。

### 5.11 变更纪律类操作

| 纪律 | 为什么 |
|---|---|
| 提交用 `git add <具体路径>`，**禁止 `git add -A`** | 历史教训：卷入 scratch 目录 |
| 提交身份用本仓 local config 的 `MurphyYi <zy84338719@hotmail.com>`，**不动 global** | global 是别的项目在用的工作身份 |
| 中文文档批量编辑后 `grep U+FFFD` 自检 | 编码损坏 |
| macOS bash 3.2 多字节炸弹 | `$VAR` 后紧跟中文/全角字符时 bash 会吞掉多字节首字节，**一律写 `${VAR}`** |

---

## 6. 反向验证（改门禁必做）

**纪律原文**：

> 门禁的失败形态是**变绿**，不是变红。改任何门禁判定逻辑，必须同时给
> 「坏实现会红」的用例——只测「好实现能过」的用例，一个把数字比较写成
> 字符串比较的错实现照样全过。

### 6.1 怎么做

```bash
# 1. 注入违规（临时改坏实现）
# 2. 跑门禁，必须红
make architecture-check; echo "退出码: $?"     # 期望：报错 + 退出码 1
# 3. 撤销注入
```

对**每条**新增/修改的判据都要做一遍。判定标准：**注入 → 红 → 撤销 → 绿**，
且红的**位置**正确（红在该红的地方，不是红在相邻的偶然原因上）。

### 6.2 三个必须分开看的失败形态

| 形态 | 症状 | 含义 |
|---|---|---|
| **假红** | 用例红了，但红在错的点 | 断言选错了区分特征 |
| **假绿** | 用例没红，但根本没执行到 | 共享失败点 / 提前 return，**更隐蔽**——它连「红」的机会都没有 |
| **用例没红 ≠ 用例钉住了** | 绿了 | 变异验证时每个子用例都要**单独**看过「红/绿」；绿的要问「它在旧实现下也绿吗」 |

**表驱动用例的写法**（避免假绿的关键）：

```go
for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) {   // ← 逐条独立报告
        ...
    })
}
```

**不要**用一张表 + 一个共享 `t.Fatalf`——那样只有第一条会跑，
后面的形态**从未被执行过**，却让用例看起来钉住了全部形态。

### 6.3 parity 测试要比「归因」，不只是「结论」

「静态预检的通过/拒绝结论」与「启动时的拒载结论」一致，**不代表两者说的是同一件事**——
两边都拒、但归因不同（通用层报通用文案 vs 专门层报专门文案）时，
运维照预检的归因去改会改错地方。

**判据**：断言要选**区分性最强**的那个特征；用例必须包含
「静态层说 OK、启动说拒」或反之的形态，否则分叉只藏在错误文案里，测试看不见。

---

## 7. 命令速查

```bash
# ── 门禁 ──────────────────────────────────
make check                          # 全套（CI 只执行它）
make architecture-check             # 架构门禁
make architecture-check --report    # 看全量违规清单（默认每条截断到 10 条）
make test                           # 全量单测
make cover                          # 覆盖率报告
make fmt-check                      # 只查格式
make vet                            # 只查 vet
make lint                           # 只跑 golangci-lint

# ── 单包迭代 ──────────────────────────────
go test ./protocol/ -run TestXxx -count=1
go test ./protocol/ -race -count=1
go build ./...

# ── 门禁工具直用 ───────────────────────────
go run ./cmd/archcheck                      # 默认读仓库根的 architecture-policy.yaml
go run ./cmd/archcheck -policy <path>       # 判另一份 policy（宿主复用）
go run ./cmd/archcheck --report
go run ./cmd/docsync                        # README 包表一致性

# ── 依赖与本地并行 ─────────────────────────
go mod tidy                           # 先确认没有 go.work（workspace 期间别跑）
make devlink                          # 接入本地 workspace（不改 go.mod）
make devunlink                        # 断开

# ── 宿主侧复用本仓门禁（示例）─────────────
# import "github.com/yi-nology/agent-harness/archcheck"
# pol, err := archcheck.Load("policy.yaml")
# res, err := archcheck.Run(root, pol)
# if res.HasBlocking() { os.Exit(1) }
```

**门禁执行体是包不是脚本**（`archcheck`）——宿主仓要复用同一套判据时
**import 它**，不要复制一份实现（各写一遍必然各判一套，漂移到没人发现为止）。

---

## 8. 不确定时怎么办

```
「这东西该放本仓还是宿主？」
  └─ 问：这个包需要知道自己在给谁干活吗？需要 → 宿主

「门禁红了，是代码错了还是规则该改？」
  └─ 先看规则想拦的失效形态写得出来吗
       写得出来 + 代码确实错了 → 改代码
       写得出来 + 代码没错    → 规则过时 → 先改 policy，再改代码，commit 写清理由
       写不出来               → 这条规则是仪式，考虑删掉

「加个功能，从哪开始？」
  └─ 先改 policy（modules / scan_roots），再写代码，最后同步 README 包表

「改完了怎么知道对？」
  └─ make check 全绿 + 反向验证（注入违规会红）
```

---

## 附：相关文档

| 文档 | 回答什么 |
|---|---|
| [README](../README.md) | 是什么、为什么这么设计、边界纪律、下游升级须知 |
| [AGENTS.md](../AGENTS.md) | 在本仓干活的工作纪律（含每条纪律的事故来源） |
| [docs/USAGE.md](USAGE.md) | **宿主开发者**：怎么把 harness 接进自己产品 |
| [docs/OAGENTKIT-OVERLAP.md](OAGENTKIT-OVERLAP.md) | 本仓与上游 `oagentkit` 的能力边界 |
| [docs/CHECKPOINT.md](CHECKPOINT.md) | 续跑契约的细节 |
| [architecture-policy.yaml](../architecture-policy.yaml) | 门禁声明面（模块拓扑 / 规则 / 阈值） |
| [CHANGELOG.md](../CHANGELOG.md) | 版本历史与破坏性变更 |
