---
home: true
title: agent-harness
heroText: agent-harness
tagline: 编排是代码，LLM 只做单环节——多智能体编排的通用内核
actions:
  - text: 怎么用 →
    link: /guide/getting-started.html
    type: primary
  - text: 设计初衷
    link: /guide/philosophy.html
    type: secondary
features:
  - title: 机制面，不认识领域
    details: 报告协议、工具判定环、续跑契约、上下文预算、执行沙箱——只提供确定性编排的机制面。专家包、诊断链、运维词表、拓扑内容、品牌文案全部属于宿主。
  - title: 工具调用只有一条路
    details: 渐进披露、多守卫合成、失败分类、审批门、派发拓扑全部接成同一条判定环——包括模型自己写的程序里的工具调用。少接一段，那里就成了唯一绕过所有门禁的路径。
  - title: 崩了能接着跑
    details: 恢复判定是纯函数而不是宿主的临场决定；终态独占权单独一道闸，放闸凭令牌、不靠内容比对——「两条分支都赢了」不可能发生。
  - title: 上下文是有限预算
    details: budget 管输入侧分段计量与压实、offload 管大对象两阶段外置、spill 管输出溢出与分页取回，三件套分工泾渭分明。
  - title: 失败是数据表
    details: 「类别 → 可重试性 / 退避 / 需人工 / 重发安全 / 模型指引」做成数据表而非散落的 if；不可归类一律不可重试（fail-closed）。
  - title: 边界不是文档，是门禁
    details: 「内核不许长出领域知识」写成正则规则 + go/ast 结构判定，挂在 make check 上，CI 只执行这一条。每条规则都做过反向验证。
footer: MIT License · github.com/yi-nology/agent-harness
---

## 判定口诀只有一句

**「这个包需要知道自己在给谁干活吗？」需要 → 属于宿主，不属于本仓。**

agent-harness 是从真实宿主产品里拆出来的编排内核：环节状态怎么流转、工具调用怎么被
判定、产出怎么被结构化、崩了怎么续跑、上下文怎么计量——这些**与领域无关的编排机制**
沉淀在本仓；给谁干活、判断什么、处置什么，全部留在宿主。

## 从哪里开始读

| 你是谁 | 从这里开始 |
|---|---|
| 想把 harness 接进自己的产品 | [怎么用](./guide/getting-started.md) → [能力详解](./guide/capabilities.md) |
| 想知道为什么长这样 | [设计初衷](./guide/philosophy.md) |
| 接完了想自查 | [坑速查](./reference/pitfalls.md) |
| 想看数字与门禁的事实面 | [包清单与机器事实](./reference/packages.md) · [门禁体系](./reference/gates.md) |
| 想改这个仓 | 仓内 [docs/AI-GUIDE.md](https://github.com/yi-nology/agent-harness/blob/main/docs/AI-GUIDE.md) 与 [AGENTS.md](https://github.com/yi-nology/agent-harness/blob/main/AGENTS.md) |

::: tip 站点数字不手抄
本页与各章的**数字**（包数、行数、依赖版本、规则清单、坑表）由
`site/gen.mjs` 从 README、go.mod、architecture-policy.yaml、Makefile 与
docs/USAGE.md 生成——手抄一遍就等于多一份会静默失真的真相。详见
[包清单与机器事实](./reference/packages.md)。
:::
