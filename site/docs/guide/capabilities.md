---
title: 能力详解
---

# 能力详解：九组能力

> 🤖 本页要点已收录进 [llms.txt](https://yi-nology.github.io/goagent-kernel/llms.txt)——AI 代理可直接读取。

本章按「解决什么问题」分组，**先看图、再看字**。逐包的集成片段与坑在仓内
[docs/USAGE.md](https://github.com/yi-nology/goagent-kernel/blob/main/docs/USAGE.md)，
包级事实（行数、依赖）在[包清单与机器事实](../reference/packages.md)。

<div class="diagram">
<svg viewBox="0 0 760 396" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar1" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
  </defs>
  <rect x="140" y="8" width="200" height="36" rx="8" fill="#1c2330" stroke="#262e3d"/>
  <text x="240" y="31" text-anchor="middle" fill="#e6edf3" font-size="13">LLM 模型 / 程序</text>
  <rect x="420" y="8" width="200" height="36" rx="8" fill="#1c2330" stroke="#262e3d"/>
  <text x="520" y="31" text-anchor="middle" fill="#e6edf3" font-size="13">宿主工具（MCP 等）</text>
  <line x1="240" y1="44" x2="240" y2="60" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar1)"/>
  <line x1="520" y1="44" x2="520" y2="60" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar1)"/>
  <g font-size="12.5" fill="#e6edf3">
    <rect x="20" y="68" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="130" y="94" text-anchor="middle">① 报告与协议</text>
    <text x="130" y="118" text-anchor="middle" fill="#9aa7b8" font-size="10.5">protocol · 容错解析 · 白名单校验 · 渲染</text>
    <rect x="270" y="68" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="380" y="94" text-anchor="middle">② 工具判定环</text>
    <text x="380" y="118" text-anchor="middle" fill="#9aa7b8" font-size="10.5">tooldisclose · guardset · toolfail</text>
    <rect x="520" y="68" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="630" y="94" text-anchor="middle">③ 编排契约与续跑</text>
    <text x="630" y="118" text-anchor="middle" fill="#9aa7b8" font-size="10.5">contract · statemachine · checkpoint · commitgate</text>
    <rect x="20" y="156" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="130" y="182" text-anchor="middle">④ 上下文三件套</text>
    <text x="130" y="206" text-anchor="middle" fill="#9aa7b8" font-size="10.5">budget · spill · offload</text>
    <rect x="270" y="156" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="380" y="182" text-anchor="middle">⑤ 程序化编排</text>
    <text x="380" y="206" text-anchor="middle" fill="#9aa7b8" font-size="10.5">ptc · sandbox · cmdpolicy · egress</text>
    <rect x="520" y="156" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="630" y="182" text-anchor="middle">⑥ 实时与流式</text>
    <text x="630" y="206" text-anchor="middle" fill="#9aa7b8" font-size="10.5">eventbus · watchdog · delta · drain · pending</text>
    <rect x="20" y="244" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="130" y="270" text-anchor="middle">⑦ 安全面</text>
    <text x="130" y="294" text-anchor="middle" fill="#9aa7b8" font-size="10.5">netguard（SSRF）· pii（脱敏）</text>
    <rect x="270" y="244" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="380" y="270" text-anchor="middle">⑧ 副作用只做一次</text>
    <text x="380" y="294" text-anchor="middle" fill="#9aa7b8" font-size="10.5">retryplan · sideeffect · sidetest</text>
    <rect x="520" y="244" width="220" height="72" rx="10" fill="#161b26" stroke="#262e3d"/>
    <text x="630" y="270" text-anchor="middle">⑨ 协作与治理</text>
    <text x="630" y="294" text-anchor="middle" fill="#9aa7b8" font-size="10.5">taskboard · skilldisclose · hookengine · memorypipe…</text>
  </g>
  <line x1="380" y1="316" x2="380" y2="338" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar1)"/>
  <rect x="200" y="344" width="360" height="38" rx="10" fill="#1c2330" stroke="#262e3d"/>
  <text x="380" y="368" text-anchor="middle" fill="#9aa7b8" font-size="12">宿主设施：存储 · 通道 · 拓扑 · 文案（经接口注入，内核只判定）</text>
</svg>
</div>
<p class="diagram-cap">能力全景：模型与工具的每一次交互都被中间三层机制管住，落点是宿主自己的设施。</p>

## 1 · 报告与协议：`protocol`

**解决**：模型输出的 JSON 不可信——字段漂移、置信虚高、越权字段。
**形态**：五级容错解析 → 白名单校验 → 注入式渲染。

- 解析分信任级：`ParseLLMReport`（剥引擎侧字段，解析 LLM 产出用这个）与
  `ParseReport`（落库往返入口）；
- 校验分两级：warn 留痕不拦截，critical 才 `ErrViolation` 整份拦；
- 渲染三档，标题与文案走 `RenderOptions` 注入；
- 证据面：`AssignEvidenceIDs` / `CheckEvidenceRefs`（编号唯一、引用完整）。

**什么时候用**：凡是「LLM 产出 → 结构化下游消费」的出口。

## 2 · 工具判定环：`tooldisclose` + `guardset` + `toolfail` + `policy` + `dispatch`

**解决**：工具调用的「披露、守卫、失败、审批、拓扑」五件事若各走各的路，必有绕过点。

<div class="diagram">
<svg viewBox="0 0 430 420" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
  </defs>
  <g font-size="12.5" fill="#e6edf3">
    <rect x="15" y="12" width="180" height="36" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="105" y="35" text-anchor="middle">普通工具调用</text>
    <rect x="235" y="12" width="180" height="36" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="325" y="35" text-anchor="middle">ptc 程序内 host.&lt;tool&gt;()</text>
    <rect x="120" y="92" width="190" height="36" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="215" y="115" text-anchor="middle">invoke.Stage 判定环</text>
    <rect x="120" y="168" width="190" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="215" y="191" text-anchor="middle">guardset 并求值 · 合成拒绝</text>
    <rect x="15" y="248" width="180" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="105" y="271" text-anchor="middle">tooldisclose 披露门</text>
    <rect x="235" y="248" width="180" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="325" y="271" text-anchor="middle">policy 审批门</text>
    <rect x="235" y="322" width="180" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="325" y="345" text-anchor="middle">dispatch 派发拓扑</text>
  </g>
  <line x1="105" y1="48" x2="180" y2="88" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="325" y1="48" x2="250" y2="88" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="215" y1="128" x2="215" y2="164" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="165" y1="204" x2="115" y2="244" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="265" y1="204" x2="315" y2="244" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="325" y1="284" x2="325" y2="318" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="105" y1="284" x2="105" y2="378" stroke="#3fb950" stroke-width="1.5" marker-end="url(#ar2)"/>
  <line x1="325" y1="358" x2="325" y2="378" stroke="#3fb950" stroke-width="1.5" marker-end="url(#ar2)"/>
  <text x="20" y="400" font-size="12" fill="#3fb950">通过 → 执行</text>
  <text x="245" y="400" font-size="12" fill="#f85149">拒绝 → 结构化 *DenyError</text>
</svg>
</div>
<p class="diagram-cap">任何一次工具调用（包括模型写的程序里的调用）都走同一条判定环——少接一段，那里就是唯一绕过所有门禁的路径。</p>

- `tooldisclose` **渐进式披露**：工具面按需展开，「拒绝即激活」是正常行为；
- `guardset` **多守卫合成**：相互独立的判定**一并求值**，多条拒绝合成一条，
  可重试取「全部可重试才算」的保守侧——不要用逐个过、第一个拒绝就收口的链；
- `toolfail` **失败是数据表**：类别 → 可重试性 / 退避 / 需人工 / **重发安全** /
  模型指引；只看 `Retryable` 不看 `SafeToResubmit`，重试会把一次副作用变成两次；
- `policy.Stage` 把**审批门接进判定环**：工具调用、程序内调用、子进程侧调用
  走同一条 invoke 链，审批规则只装配一次。

**什么时候用**：只要你放一个工具给模型。**全部接进同一条 `invoke.Stage` 链**。

## 3 · 编排契约与续跑：`contract` + `statemachine` + `checkpoint` + `commitgate`

**解决**：状态存哪、怎么流转、崩了怎么接着跑、终态怎么独占。

<div class="diagram">
<svg viewBox="0 0 720 226" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar3" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
  </defs>
  <text x="20" y="22" font-size="13" fill="#e6edf3">checkpoint：续跑而非重放</text>
  <text x="392" y="22" font-size="13" fill="#e6edf3">commitgate：终态独占</text>
  <line x1="360" y1="34" x2="360" y2="210" stroke="#262e3d"/>
  <g font-size="12" fill="#e6edf3">
    <rect x="20" y="42" width="130" height="34" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="85" y="63" text-anchor="middle">崩溃前快照</text>
    <rect x="196" y="42" width="130" height="34" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="261" y="63" text-anchor="middle">BuildPlan（纯函数）</text>
    <rect x="20" y="106" width="306" height="34" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="173" y="127" text-anchor="middle">跳过已终态 · 重跑缺口 · 并行按前驱采信</text>
  </g>
  <line x1="150" y1="59" x2="192" y2="59" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar3)"/>
  <line x1="261" y1="76" x2="261" y2="102" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar3)"/>
  <text x="20" y="172" font-size="11" fill="#9aa7b8">可信边界按依赖判定（失序宁可多跑）· 终态不自动重开 · 序号单调闸</text>
  <g font-size="12" fill="#e6edf3">
    <rect x="392" y="42" width="112" height="34" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="448" y="63" text-anchor="middle">Acquire 令牌</text>
    <rect x="528" y="42" width="92" height="34" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="574" y="63" text-anchor="middle">提交终态</text>
    <rect x="644" y="42" width="66" height="34" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="677" y="63" text-anchor="middle">Release</text>
    <rect x="392" y="106" width="318" height="34" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="551" y="127" text-anchor="middle">放闸凭令牌，不比对内容 —— 同名/同序也放不错</text>
  </g>
  <line x1="504" y1="59" x2="524" y2="59" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar3)"/>
  <line x1="620" y1="59" x2="640" y2="59" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar3)"/>
  <text x="392" y="172" font-size="11" fill="#9aa7b8">同一 (run, 环节) 的终态只允许提交一次 —— 「两条分支都赢了」不可能</text>
</svg>
</div>
<p class="diagram-cap">左：恢复判定是纯函数，不是宿主的临场决定；右：终态独占权凭令牌，不靠内容比对认人。</p>

- `contract`：`RunContext` / `StepSink` / `ToolBinder` / `Clock`——
  编排引擎与宿主状态的**唯一边界**（纯接口面，零实现）；
- `statemachine`：环节状态机纯谓词（含 `interrupted` 终态：中止 ≠ 失败）；
- `checkpoint`：`StoreCAS` 跨进程 CAS 存取、并行 fan-out 按各自前驱采信；
- `commitgate`：`Acquire` / `ReleaseLease` 凭令牌放闸。

**什么时候用**：编排超过一个环节、或「崩了不能全量重跑」的时候。

## 4 · 上下文三件套：`budget` + `spill` + `offload`

**解决**：上下文窗口是有限预算，不是无限历史。

<div class="diagram">
<svg viewBox="0 0 720 242" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar4" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
  </defs>
  <rect x="270" y="92" width="200" height="52" rx="10" fill="#161b26" stroke="#58a6ff"/>
  <text x="370" y="115" text-anchor="middle" fill="#e6edf3" font-size="13">上下文窗口</text>
  <text x="370" y="133" text-anchor="middle" fill="#9aa7b8" font-size="10.5">有限预算，不是无限历史</text>
  <g font-size="12" fill="#e6edf3">
    <rect x="20" y="38" width="200" height="50" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="120" y="59" text-anchor="middle">budget 输入侧</text>
    <text x="120" y="77" text-anchor="middle" fill="#9aa7b8" font-size="10.5">分段计量 · 压实计划 · 毛/净分列</text>
    <rect x="500" y="38" width="200" height="50" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="600" y="59" text-anchor="middle">spill 输出侧</text>
    <text x="600" y="77" text-anchor="middle" fill="#9aa7b8" font-size="10.5">溢出存档 · 分页取回 · 按龄清理</text>
    <rect x="270" y="182" width="200" height="50" rx="10" fill="#1c2330" stroke="#262e3d"/>
    <text x="370" y="203" text-anchor="middle">offload 大对象</text>
    <text x="370" y="221" text-anchor="middle" fill="#9aa7b8" font-size="10.5">两阶段外置：首次只标记不替换</text>
  </g>
  <line x1="220" y1="72" x2="280" y2="100" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar4)"/>
  <line x1="500" y1="72" x2="452" y2="100" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar4)"/>
  <line x1="370" y1="182" x2="370" y2="148" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar4)"/>
</svg>
</div>
<p class="diagram-cap">三件套各管一段：进来的计量与压实、出去的溢出与取回、躺在线里的大对象。</p>

- `budget`：区分「工具面膨胀」与「历史膨胀」；要减账用 `NetRemovedTokens`（净）；
- `spill`：截断 + 分页取回，提示契约定死；
- `offload`：**首次只标记不替换**——直接替换会让模型引用一个自己没看过的东西。

**什么时候用**：工具面大、历史长、或产出含大对象（图片、日志、报告全文）。

## 5 · 程序化编排：`ptc` + `sandbox` + `cmdpolicy` + `egress` + `preset`

**解决**：N 轮 ReAct 往返的上下文成本与原始数据过模型面的问题。

<div class="diagram">
<svg viewBox="0 0 720 270" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ar5" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/>
    </marker>
    <marker id="ar5b" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 z" fill="#f85149"/>
    </marker>
  </defs>
  <text x="20" y="22" font-size="13" fill="#9aa7b8">N 轮 ReAct 往返 —— 每轮原始数据都过模型面</text>
  <g font-size="12" fill="#e6edf3">
    <rect x="30" y="36" width="110" height="34" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="85" y="57" text-anchor="middle">模型</text>
    <rect x="260" y="36" width="110" height="34" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="315" y="57" text-anchor="middle">工具</text>
  </g>
  <line x1="140" y1="46" x2="256" y2="46" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar5)"/>
  <line x1="260" y1="62" x2="144" y2="62" stroke="#f85149" stroke-width="1.5" marker-end="url(#ar5b)"/>
  <text x="410" y="58" font-size="12" fill="#9aa7b8">× N 轮，上下文被撑爆</text>
  <line x1="20" y1="96" x2="700" y2="96" stroke="#262e3d"/>
  <text x="20" y="126" font-size="13" fill="#e6edf3">ptc —— 塌缩成 1 轮，原始数据不过模型面</text>
  <g font-size="12" fill="#e6edf3">
    <rect x="20" y="142" width="120" height="36" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="80" y="164" text-anchor="middle">模型写程序</text>
    <rect x="182" y="142" width="110" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="237" y="164" text-anchor="middle">沙箱执行</text>
    <rect x="334" y="142" width="130" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="399" y="164" text-anchor="middle">bindings 调工具 ×N</text>
    <rect x="506" y="142" width="120" height="36" rx="8" fill="#161b26" stroke="#262e3d"/>
    <text x="566" y="164" text-anchor="middle">归并小结果</text>
  </g>
  <line x1="140" y1="160" x2="178" y2="160" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar5)"/>
  <line x1="292" y1="160" x2="330" y2="160" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar5)"/>
  <line x1="464" y1="160" x2="502" y2="160" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar5)"/>
  <path d="M566,142 C566,118 120,118 88,138" fill="none" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ar5)"/>
  <text x="20" y="218" font-size="11" fill="#9aa7b8">三重预算（墙钟 / 调用数 / 字节数）替代 OS 沙盘 · 纯 Go 零 CGO</text>
  <text x="20" y="242" font-size="11" fill="#9aa7b8">程序内 host.&lt;tool&gt;() 走同一套判定环 · cmdpolicy 把判据下探到命令行 · egress 管子进程出口</text>
</svg>
</div>
<p class="diagram-cap">同一个任务：上面每一轮的原始数据都要挤进上下文；下面只有一次往返，原始数据留在沙箱里。</p>

- `ptc`：绑定可带 Signature（净化名，撞名装配期拒）；
- `sandbox`：档位声明与兑现（`sandbox.Capable`）——子进程后端用 cgroup v2 `memory.max`
  兑现 `MaxMemory`，**建不出即拒绝执行**；
- `cmdpolicy`：命令级判据（前缀 token 规则 + 三态裁决，规则自带样例、加载期自检）；
- `preset`：把「档位 × 审批 × 出口」收成一份具名组合。

**什么时候用**：一次任务要聚合多次工具调用（多表查询、批量巡检、逐机采样）。

## 6 · 实时事件与流式：`eventbus` + `watchdog` + `delta` + `drain` + `pending`

- `eventbus`：进程内有损广播——**先落库再广播**，SSE 断了靠 DB 回放游标补达；
- `watchdog`：工具调用配对监视（收口/幽灵调用的判据）；
- `delta`：增量节流（`Pacer` **每环节新建**，长生命周期会内存涨、收口卡住）；
- `drain`：优雅排水（用 `DrainSession` 而非全局 `Drain`）；
- `pending`：多轮挂起态载体（落列必须走 `pending.ColumnValue`）。

## 7 · 安全面：`netguard` + `pii`

- `netguard`：出站 SSRF 守卫——名字层 + 逐 IP 过 egress + 重定向逐跳重验 +
  `DialFunc` 建连后核对真实 RemoteAddr（闭合 rebinding 窗口）；
- `pii`：个人信息脱敏（手机号/身份证四重校验），内核只回报信号，阻断由宿主执行。

**什么时候用**：模型/工具能碰外网，或报告/上下文里可能进个人信息。

## 8 · 副作用只做一次：`retryplan` + `sideeffect` + `sidetest`

**接线范式**：`retryplan` 判策（`Retryable` × `SafeToResubmit` 两个正交维度）→
`sideeffect` 执行（幂等键 + 租约 + 栅栏）。`sidetest` 给你的存储实现一份**验收标准**。

## 9 · 协作与治理面

`taskboard`（多 agent 认领与依赖）· `skilldisclose`（技能渐进披露）·
`hookengine`（钩子装配）· `memorypipe`（记忆管道）· `ticket` / `trajectory`（工单与轨迹）·
`toolname`（工具名净化）· `gatecheck`（审批 × 档位联合判据）· `sessionref` / `worktree` /
`delegate`（会话引用 / 工作树 / 委派）；门面三件 `dispatch` / `policy` / `reminder`
调用面稳定、核心归上游。

## 怎么选：三个判断

1. **这判定换个宿主要不要改？** 要 → 写在宿主适配层；不要 → 可以内核机制化。
2. **这语义跨宿主必须一致吗？**（失败分类/隔离档位/终态独占）→ 用内核的机制包，别自研。
3. **这条路径绕过判定环了吗？** 任何工具到达模型的执行面都要过同一条 `invoke.Stage` 链；
   发现第二条路 = 缺陷。
