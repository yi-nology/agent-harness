---
home: true
title: agent-harness
heroText: agent-harness
tagline: 编排是代码，LLM 只做单环节
actions:
  - text: 怎么用 →
    link: /guide/getting-started.html
    type: primary
  - text: 设计初衷
    link: /guide/philosophy.html
    type: secondary
features:
  - title: 机制面，不认识领域
    details: 只给确定性编排的机制——内容、领域、拓扑归宿主。
  - title: 工具调用只有一条路
    details: 披露、守卫、失败、审批、拓扑接成同一条判定环，程序内调用也不例外。
  - title: 崩了能接着跑
    details: 恢复判定是纯函数；终态独占凭令牌，「两条分支都赢了」不可能。
  - title: 上下文是有限预算
    details: budget 计量、offload 外置、spill 溢出，三件套各管一段。
  - title: 失败是数据表
    details: 可重试性 / 退避 / 重发安全做成数据表；不可归类一律不可重试。
  - title: 边界不是文档，是门禁
    details: 域词、依赖方向、结构判定全由机器判，挂在 make check 上。
footer: MIT License · github.com/yi-nology/agent-harness
---

<div class="home-punch"><strong>「这个包需要知道自己在给谁干活吗？」</strong>需要 → 属于宿主。</div>

<div class="home-install"><span class="prompt">▸</span>go get github.com/yi-nology/agent-harness@latest</div>

<div class="home-links">
  <a class="chip" href="./guide/introduction.html">介绍</a>
  <a class="chip" href="./guide/capabilities.html">能力详解</a>
  <a class="chip" href="./guide/getting-started.html">接入路线</a>
  <a class="chip" href="./reference/pitfalls.html">坑速查</a>
  <a class="chip" href="./reference/packages.html">机器事实</a>
  <a class="chip chip-ai" href="/agent-harness/llms.txt">AI 直读 llms.txt</a>
  <a class="chip" href="https://github.com/yi-nology/agent-harness">GitHub &#8599;</a>
</div>
