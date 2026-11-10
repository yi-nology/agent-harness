---
title: 介绍
---

# 介绍：这是什么

**agent-harness 是多智能体编排的通用内核。** 一句话定位：**编排是代码，LLM 只做单环节**——
它只提供确定性编排的**机制面**，**不认识任何领域概念**。

所谓机制面，是这些与「给谁干活」无关的东西：

- **报告协议**（`protocol`）：模型输出怎么变成可信的结构化报告；
- **工具判定环**（`tooldisclose` / `guardset` / `toolfail` / `policy` / `dispatch`）：
  一次工具调用从披露、守卫、审计到派发拓扑的完整判定链；
- **编排契约与续跑**（`contract` / `statemachine` / `checkpoint` / `commitgate`）：
  环节状态怎么流转、崩了怎么接着跑、终态怎么独占；
- **上下文预算**（`budget` / `spill` / `offload`）：有限的上下文窗口怎么不被撑爆；
- **执行沙箱**（`ptc` / `sandbox`）：模型写程序、沙箱执行、原始数据不过模型面。

## 什么不属于本仓

| ✓ 有（机制，与领域无关） | ✗ 没有（资产，属于宿主） |
|---|---|
| 报告契约 + 容错解析 + 白名单校验 + 渲染 | 专家包 / 专家清单 / 技能 |
| 编排运行契约（宿主状态的唯一边界） | 诊断链 / 编排图（拓扑内容） |
| 环节状态机、续跑契约、终态独占提交闸 | 运维词表、目标类型、领域字段语义 |
| 工具披露、守卫合成、失败分类、派发守卫 | 审批风险分级与处置动作 |
| 上下文计量、溢出、外置 | 摘要生成的具体实现（走 `Summarizer` 接口） |
| 出站 SSRF 守卫、个人信息脱敏 | IM 通道 / 通知模板 / 品牌文案 |
| 程序化工具编排沙箱、命令级判据 | 工具实现（MCP server）、工具描述文案 |
| 副作用至多一次、重试决策 | 幂等键的存储实现（Redis / PG / etcd） |

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

```
oekit（基础库：errors / logx / concurrency / shellx …）
  └─→ oagentkit（agent 原语：invoke / guardx / toolgate / procx …）
        └─→ agent-harness（编排内核：本仓）
              └─→ 宿主产品（业务、领域、拓扑、文案）
```

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
| 仓内 [docs/USAGE.md](https://github.com/yi-nology/agent-harness/blob/main/docs/USAGE.md) | 完整接入指南（逐包：职责 → 集成片段 → 关键符号 → 坑） |
| 仓内 [README](https://github.com/yi-nology/agent-harness#readme) | 定位、边界纪律、下游升级须知（破坏性变更表） |
