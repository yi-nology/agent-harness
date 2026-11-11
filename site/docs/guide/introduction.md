---
title: 介绍
---

# 介绍：这是什么

> 🤖 本页要点已收录进 [llms.txt](https://yi-nology.github.io/goagent-kernel/llms.txt)——AI 代理可直接读取。

**goagent-kernel 是多智能体编排的通用内核。** 一句话定位：**编排是代码，LLM 只做单环节**——
它只提供确定性编排的**机制面**，**不认识任何领域概念**。

所谓机制面，是这些与「给谁干活」无关的东西：

- **报告协议**（`protocol`）：模型输出怎么变成可信的结构化报告；
- **工具判定环**（`tooldisclose` / `guardset` / `toolfail` / `policy` / `dispatch`）：
  一次工具调用从披露、守卫、审计到派发拓扑的完整判定链；
- **编排契约与续跑**（`contract` / `statemachine` / `checkpoint` / `commitgate`）：
  环节状态怎么流转、崩了怎么接着跑、终态怎么独占；
- **上下文预算**（`budget` / `spill` / `offload`）：有限的上下文窗口怎么不被撑爆；
- **执行沙箱**（`ptc` / `sandbox`）：模型写程序、沙箱执行、原始数据不过模型面。

## 机制 vs 资产：什么不属于本仓

<div class="diagram">
<svg viewBox="0 0 720 356" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <text x="30" y="24" font-size="12.5" fill="#3fb950">✓ 有（机制，与领域无关）</text>
  <text x="392" y="24" font-size="12.5" fill="#f85149">✗ 没有（资产，属于宿主）</text>
  <g font-size="10.5" fill="#e6edf3">
    <rect x="30" y="38" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="57" text-anchor="middle">报告协议：容错解析 · 白名单校验 · 渲染</text>
    <rect x="30" y="74" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="93" text-anchor="middle">编排运行契约（宿主状态的唯一边界）</text>
    <rect x="30" y="110" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="129" text-anchor="middle">环节状态机 · 续跑契约 · 终态独占闸</text>
    <rect x="30" y="146" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="165" text-anchor="middle">工具披露 · 守卫合成 · 失败分类 · 派发守卫</text>
    <rect x="30" y="182" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="201" text-anchor="middle">上下文计量 · 溢出 · 大对象外置</text>
    <rect x="30" y="218" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="237" text-anchor="middle">出站 SSRF 守卫 · 个人信息脱敏</text>
    <rect x="30" y="254" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="273" text-anchor="middle">程序化编排沙箱 · 命令级判据</text>
    <rect x="30" y="290" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="180" y="309" text-anchor="middle">副作用至多一次 · 重试处置决策</text>
    <rect x="392" y="38" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="57" text-anchor="middle">专家包 · 专家清单 · 技能</text>
    <rect x="392" y="74" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="93" text-anchor="middle">诊断链 · 编排图（拓扑内容）</text>
    <rect x="392" y="110" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="129" text-anchor="middle">运维词表 · 目标类型 · 领域字段语义</text>
    <rect x="392" y="146" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="165" text-anchor="middle">审批风险分级与处置动作</text>
    <rect x="392" y="182" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="201" text-anchor="middle">摘要生成的具体实现（走 Summarizer 接口）</text>
    <rect x="392" y="218" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="237" text-anchor="middle">IM 通道 · 通知模板 · 品牌文案</text>
    <rect x="392" y="254" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="273" text-anchor="middle">工具实现（MCP server）· 工具描述文案</text>
    <rect x="392" y="290" width="300" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="542" y="309" text-anchor="middle">幂等键的存储实现（Redis / PG / etcd）</text>
  </g>
  <text x="30" y="344" font-size="10.5" fill="#9aa7b8">判定口诀：「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主。</text>
</svg>
</div>
<p class="diagram-cap">左列全部「与领域无关」，右列全部「换宿主就变」——这条线由门禁守，不由 review 守。</p>

判定口诀：**「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主。**

再直白一点：**换个宿主，这段代码要不要改？要改的就不该在内核里。**

## 你会拿到什么

| 你要解决的事 | 用哪个包 | 你要提供什么 |
|---|---|---|
| 会话/环节状态存哪、怎么流转 | `contract` + `statemachine` | `RunContext` / `StepSink` 两个接口的实现 |
| 模型输出怎么变成可信结构 | `protocol` | agent 名注册（可选）、报告标题（可选） |
| 工具怎么被限制 | `tooldisclose` + `guardset` + `policy` | 工具目录、宿主守卫、拓扑 `EdgeSource` |
| 崩了怎么接着跑 | `checkpoint` + `commitgate` | `StoreCAS` 实现、终态词表注入 |
| 上下文怎么不被撑爆 | `budget` + `spill` + `offload` | `Summarizer` / `Archive` / `Store` 三个接口 |
| 进度怎么推给前端 | `eventbus` + `watchdog` | 落库 + 广播、告警发布出口 |
| 出站请求怎么防 SSRF | `netguard` | 挂在自己的 `DialContext` 上 |
| 个人信息怎么脱敏 | `pii` | 阻断决策（内核只回报信号） |
| 多工具聚合怎么省往返 | `ptc` | 工具绑定、判定环切片 |
| 副作用怎么保证只做一次 | `sideeffect` + `retryplan` | `Store` 实现（跨进程原子）、幂等键 |

内核的接口设计原则是：**内核只判定，不处置**——它告诉你「该阻断」「该重试」「不可归类」，
动作由宿主执行。所以每个能力包要你提供的都是**你的基础设施**（存储、通道、拓扑），
而不是业务逻辑。

## 在依赖链上的位置

<div class="diagram">
<svg viewBox="0 0 720 116" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar6" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
  </defs>
  <g font-size="12.5" fill="#e6edf3">
    <rect x="10" y="30" width="150" height="48" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="85" y="51" text-anchor="middle">oekit</text>
    <text x="85" y="69" text-anchor="middle" fill="#9aa7b8" font-size="10.5">基础库</text>
    <rect x="196" y="30" width="160" height="48" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="276" y="51" text-anchor="middle">oagentkit</text>
    <text x="276" y="69" text-anchor="middle" fill="#9aa7b8" font-size="10.5">agent 原语</text>
    <rect x="392" y="30" width="176" height="48" rx="10" fill="#161b26" stroke="#58a6ff"/>
    <text x="480" y="51" text-anchor="middle">goagent-kernel</text>
    <text x="480" y="69" text-anchor="middle" fill="#9aa7b8" font-size="10.5">编排内核 · 本仓（机制面）</text>
    <rect x="604" y="30" width="106" height="48" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="657" y="51" text-anchor="middle">宿主产品</text>
    <text x="657" y="69" text-anchor="middle" fill="#9aa7b8" font-size="10.5">领域 · 内容</text>
  </g>
  <line x1="160" y1="54" x2="192" y2="54" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar6)"/>
  <line x1="356" y1="54" x2="388" y2="54" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar6)"/>
  <line x1="568" y1="54" x2="600" y2="54" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar6)"/>
</svg>
</div>
<p class="diagram-cap">依赖链单向：本仓 import 上游，宿主 import 本仓——反向依赖是门禁红线。</p>

两个上游都是公开的 Go 模块矩阵，各有文档站，值得一并了解：

**[oekit](https://oekit.github.io/) — Open Easy Kit**（开源 · 易用 · 工具集合）
一套按「框架耦合分层」组织的 Go 模块矩阵：37 个独立发版单元（微工具库、日志/追踪/
指标门面、注册发现、HTTP/Kitex 框架生态、应用引导），依赖严格自底向上，每仓独立
SemVer + 独立 CI。本仓站在它上面的是三块基础件：`oekit/pkg`（errors / stringx /
encoding 等微工具）、`oekit/concurrency`（GoSafe 等并发原语）、`oekit/logx`（日志门面）。🤖 AI 直读：[oekit.github.io/llms.txt](https://oekit.github.io/llms.txt)。

**[oagentkit](https://oagentkit.github.io/) — Open Agent Kit**（Agent / LLM 编排工具链
模块矩阵）13 个独立发版的 Go 模块：模型客户端、MCP 工具池、技能库、知识检索、
Agent 编排、运行时基础设施；与 oekit 是姊妹项目（依赖其 pkg / concurrency / logx），
同为「框架耦合分层」组织。本仓的判定语义核心多落在它上面——派发守卫核心在
`oagentkit/agent/dispatch`、判定环与沙箱在 `oagentkit/guardx`（invoke / sandbox /
injectband）、审计门在 `oagentkit/toolgate`、进程原语在 `oagentkit/runtime/procx`；
本仓的门面包（`dispatch` / `policy` / `reminder` / `toolfail` / `sandbox`）只做
调用面转发，核心实现在上游——**跨宿主必须一致的判定语义不放两份实现**。
🤖 AI 直读：[oagentkit.github.io/llms.txt](https://oagentkit.github.io/llms.txt)。

- **独立发版、版本化引入、零 replace**：`go.mod` 含 replace 即门禁红（公开 module，
  replace 会让下游 `go get` 整体失败）。本地多仓并行开发走 `make devlink` 的 workspace。
- **依赖全部为 public 模块**（`github.com/oekit/*`、`github.com/oagentkit/*`）。
- **按包引入**：`contract` / `statemachine` 是零外部依赖的叶子包——只想要状态机，
  不必拖进 goja、jsonschema。
- **平台支持**：linux/amd64、linux/arm64、darwin/arm64 每次 `make check` 交叉构建验证；
  windows/amd64 可编译，进程树语义降级（无进程组信号，TERM 优雅退出降级为整树硬终止）。

## 文档地图

| 文档 | 回答什么 |
|---|---|
| [设计初衷](./philosophy.md) | 为什么长这样：编排是代码、机制与资产的分界、门禁文化 |
| [怎么用](./getting-started.md) | 安装、三个约束、接入路线图、最小示例 |
| [能力详解](./capabilities.md) | 九组能力：解决什么问题、关键符号、什么时候用 |
| [坑速查](../reference/pitfalls.md) | 接完线自查：症状 → 真因（从仓内 USAGE §6 生成） |
| [包清单与机器事实](../reference/packages.md) | 包表、依赖版本、规则清单、阈值（机器生成） |
| [门禁体系](../reference/gates.md) | make check 全链、规则与结构判定、数字纪律 |
| 仓内 [docs/USAGE.md](https://github.com/yi-nology/goagent-kernel/blob/main/docs/USAGE.md) | 完整接入指南（逐包：职责 → 集成片段 → 关键符号 → 坑） |
| 仓内 [README](https://github.com/yi-nology/goagent-kernel#readme) | 定位、边界纪律、下游升级须知（破坏性变更表） |
