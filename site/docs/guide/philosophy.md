---
title: 设计初衷
---

# 设计初衷：为什么这么设计

> 🤖 本页要点已收录进 [llms.txt](https://yi-nology.github.io/agent-harness/llms.txt)——AI 代理可直接读取。

这个仓不是「先设计 API 再找场景」的产物，而是从一个真实宿主产品里**拆**出来的：
多智能体编排里那些与领域无关、又必须跨宿主一致的判定语义，被一层层沉淀成机制。
下面每一条设计决定，背后都有具体的失败形态。

## 编排是代码，LLM 只做单环节

<div class="diagram">
<svg viewBox="0 0 720 252" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ph1" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
    <marker id="ph1r" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#f85149"/></marker>
  </defs>
  <text x="20" y="22" font-size="13" fill="#9aa7b8">编排交给模型 —— 每次跑得都不一样</text>
  <text x="382" y="22" font-size="13" fill="#e6edf3">编排是代码 —— 每次都可重演</text>
  <line x1="360" y1="34" x2="360" y2="238" stroke="#262e3d"/>
  <g font-size="11.5" fill="#e6edf3">
    <rect x="130" y="38" width="90" height="30" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="175" y="57" text-anchor="middle">模型</text>
    <rect x="30" y="100" width="130" height="28" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="95" y="118" text-anchor="middle">流程往哪走 ？</text>
    <rect x="30" y="142" width="130" height="28" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="95" y="160" text-anchor="middle">失败怎么办 ？</text>
    <rect x="30" y="184" width="130" height="28" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="95" y="202" text-anchor="middle">崩了怎么恢复 ？</text>
  </g>
  <path d="M175,68 C120,80 110,88 95,98" fill="none" stroke="#f85149" stroke-width="1.5" marker-end="url(#ph1r)"/>
  <path d="M175,68 C160,110 145,128 95,140" fill="none" stroke="#f85149" stroke-width="1.5" marker-end="url(#ph1r)"/>
  <path d="M175,68 C175,130 150,170 95,182" fill="none" stroke="#f85149" stroke-width="1.5" marker-end="url(#ph1r)"/>
  <text x="20" y="238" font-size="10.5" fill="#f85149">不可重演 · 不可审计 · 不可门禁 · 崩了只能全量重跑</text>
  <g font-size="11.5" fill="#e6edf3">
    <rect x="382" y="44" width="92" height="34" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="428" y="65" text-anchor="middle">环节 1</text>
    <rect x="502" y="44" width="92" height="34" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="548" y="61" text-anchor="middle">环节 2</text>
    <text x="548" y="73" text-anchor="middle" font-size="8.5" fill="#58a6ff">LLM 只在这</text>
    <rect x="622" y="44" width="86" height="34" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="665" y="65" text-anchor="middle">环节 3</text>
  </g>
  <line x1="474" y1="61" x2="498" y2="61" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph1)"/>
  <line x1="594" y1="61" x2="618" y2="61" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph1)"/>
  <text x="382" y="118" font-size="11" fill="#9aa7b8">跳转 / 重试 / 收口 = 确定性 Go 代码</text>
  <text x="382" y="144" font-size="11" fill="#9aa7b8">恢复判定 = 纯函数（快照 → 续跑计划）</text>
  <text x="382" y="170" font-size="11" fill="#9aa7b8">可重演 → 可审计 → 可门禁</text>
  <text x="382" y="238" font-size="10.5" fill="#3fb950">崩了按快照接着跑，不是全量重来</text>
</svg>
</div>
<p class="diagram-cap">左：流程交给概率模型，三个问题每次答案都不同；右：流程是代码，LLM 只在环节内发挥。</p>

所以本仓的立场是：

- **编排是确定性代码**——环节状态怎么流转、工具怎么被判定、产出怎么被结构化，
  全部是可测试、可门禁、可重演的 Go 代码；
- **LLM 只做单环节**——在环节内部发挥，出了环节由契约接管；
- **「编排是代码」的必然推论是编排过程可被重演**——既然可重演，恢复方式就该是
  可机械判定的纯函数，而不是宿主在崩溃那一刻的临场决定（临场决定必然选最坏的那个：
  要么盲目全量重跑、要么盲目跳过）。

## 内核不该知道自己在给谁干活

拆分做完后最现实的风险不是拆不动，而是**业务代码一年年渗回内核**，
等发现时 harness 已经退化成「换了个名字的 monolith」。

判定口诀一张图就够：

<div class="diagram">
<svg viewBox="0 0 600 212" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ph2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
  </defs>
  <g font-size="12" fill="#e6edf3">
    <rect x="210" y="12" width="180" height="32" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="300" y="33" text-anchor="middle">这段代码该放哪？</text>
    <rect x="150" y="78" width="300" height="42" rx="10" fill="#161b26" stroke="#58a6ff"/>
    <text x="300" y="104" text-anchor="middle" font-size="13">换个宿主，这段代码要不要改？</text>
    <rect x="30" y="162" width="230" height="38" rx="10" fill="#1c2330" stroke="#f85149"/>
    <text x="145" y="186" text-anchor="middle">要改 → 属于宿主（资产）</text>
    <rect x="340" y="162" width="230" height="38" rx="10" fill="#1c2330" stroke="#58a6ff"/>
    <text x="455" y="186" text-anchor="middle">不用改 → 属于内核（机制）</text>
  </g>
  <line x1="300" y1="44" x2="300" y2="74" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph2)"/>
  <line x1="240" y1="120" x2="155" y2="158" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph2)"/>
  <line x1="360" y1="120" x2="445" y2="158" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph2)"/>
  <text x="158" y="150" font-size="10.5" fill="#f85149">要改</text>
  <text x="415" y="150" font-size="10.5" fill="#58a6ff">不用改</text>
</svg>
</div>
<p class="diagram-cap">一句口诀当判据：要改的代码属于宿主，不需要改的才有资格进内核。</p>

落到三条可执行约定：

1. **判定语义归内核，判定依据归宿主。** 例：「这是引导式直答还是诊断报告」是机制（内核），
   「哪条算引导」是命名（宿主）——所以内核给结构化字段 + 注册表，不硬编码名字。
2. **展示文案归宿主。** 标题、兜底、品牌词一律走 `RenderOptions` 注入，内核只留中性默认值。
3. **测试夹具用合成数据。** 夹具是契约示例，示例带领域味等于把契约钉死在某个领域上。

而这条边界**不写在散文里**——写成 8 条正则规则 + 5 条 `go/ast` 结构判定，
挂在 `make check` 上。散文不是门禁：真有人往门面包里塞一份实现，散文不响，
结果是与上游并存的第二份实现。

## 判定语义必须跨宿主一致

失败分类、隔离档位、提交闸的判定若各宿主各写一套，必然漂移到没人发现为止。
「什么算可重试」「隔离的哪一档算够」「这次提交算不算终态」——这些是**语义**，
不是实现细节：各判一套的那天起，同一个错误在两个宿主里就有了两种处置。

因此：

- 组合逻辑归内核，判定归各自机制包；
- **门禁执行体是可导入包**（`archcheck/`）：宿主复用同一份判据，不必各写一遍——
  各写一遍的结局就是各判一套。

## 内部一致优于外部通用

同一件事的多个维度**分列而非合并**，因为合并会丢信息：

- `toolfail`：**重发安全**与**重试价值**是两个正交维度——超时值得重试，
  但服务端可能已执行，原样重发就是重复施加副作用；
- `budget`：**毛移除量**（扣摘要之前的量）与**净移除量**（扣掉摘要回填后的量）分开；
- `guardset`：多条拒绝合成一条时，可重试取「**全部可重试才算**」，不是「任一」——
  取保守侧。

## 失败形态必须是变红，不是变绿

这是本仓工程文化的核心。规则的失败形态若是变绿，规则就等于不存在：

<div class="diagram">
<svg viewBox="0 0 720 196" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ph3" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
  </defs>
  <g font-size="11.5" fill="#e6edf3">
    <rect x="20" y="16" width="120" height="32" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="80" y="36" text-anchor="middle">坏实现</text>
    <rect x="200" y="16" width="150" height="32" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="275" y="36" text-anchor="middle">同一个用例跑它</text>
    <rect x="410" y="16" width="160" height="32" rx="8" fill="#1c2330" stroke="#3fb950"/>
    <text x="490" y="36" text-anchor="middle" fill="#3fb950">必须变红 ✓</text>
    <rect x="20" y="110" width="120" height="32" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="80" y="130" text-anchor="middle">坏实现</text>
    <rect x="200" y="110" width="150" height="32" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="275" y="130" text-anchor="middle">只测「好实现能过」</text>
    <rect x="410" y="110" width="160" height="32" rx="8" fill="#1c2330" stroke="#f85149"/>
    <text x="490" y="130" text-anchor="middle" fill="#f85149">假绿 ✗ 等于没规则</text>
  </g>
  <line x1="140" y1="32" x2="196" y2="32" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph3)"/>
  <line x1="350" y1="32" x2="406" y2="32" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph3)"/>
  <line x1="140" y1="126" x2="196" y2="126" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph3)"/>
  <line x1="350" y1="126" x2="406" y2="126" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph3)"/>
  <text x="20" y="78" font-size="10.5" fill="#3fb950">改判定逻辑必须配这条：注入违规 → 门禁报错（反向验证）</text>
  <text x="20" y="172" font-size="10.5" fill="#f85149">一个把数字比较写成字符串的错实现照样全过——「从没见过它触发的规则」等于没有规则</text>
</svg>
</div>
<p class="diagram-cap">上：每条规则都配的反向验证；下：只测好实现的假绿形态。</p>

- **改任何判定逻辑，必须同时给「坏实现会红」的用例**——只测「好实现能过」的用例，
  一个把数字比较写成字符串比较的错实现照样全过；
- **「从没见过它触发的规则」等于没有规则**——每条规则都做过反向验证（注入违规 →
  门禁报错 + 退出码 1）；
- **门禁自己也要能被检查**：测试文件被整体跳过、import 规则漏形态、例外到期不失效……
  这些「静默失效」都实测发生过，修法都配了反向用例；
- **结构性的判定走 `go/ast`，不走逐行正则**——import 声明有六种以上书写形态，
  逐行正则必然漏，且漏得毫无声响。

## fail-closed 是默认姿势

- `toolfail`：**不可归类一律不可重试**；
- `sandbox`：兑不住声明档位的后端**建不出即拒绝执行**，登记簿里不会被选中；
- `protocol`：超限输入**判非报告**而不是截断后继续解析（截断曾让 3.4 MB 输入
  以 `confidence=high` 解析成功——上限等于没有）；
- `netguard`：放行面**具名且零值即严**——不配置 = 全拒绝。

## 派生状态只能有一个真相

<div class="diagram">
<svg viewBox="0 0 720 168" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system,'PingFang SC','Microsoft YaHei',sans-serif">
  <defs>
    <marker id="ph4" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 z" fill="#58a6ff"/></marker>
  </defs>
  <text x="20" y="22" font-size="12" fill="#9aa7b8">两个计数器 ✗</text>
  <text x="392" y="22" font-size="12" fill="#e6edf3">一个真相 + 投影 ✓</text>
  <line x1="360" y1="32" x2="360" y2="152" stroke="#262e3d"/>
  <g font-size="11.5" fill="#e6edf3">
    <rect x="30" y="42" width="120" height="36" rx="8" fill="#1c2330" stroke="#f85149"/>
    <text x="90" y="64" text-anchor="middle">计数器 A</text>
    <rect x="210" y="42" width="120" height="36" rx="8" fill="#1c2330" stroke="#f85149"/>
    <text x="270" y="64" text-anchor="middle">计数器 B</text>
    <rect x="392" y="42" width="130" height="36" rx="8" fill="#161b26" stroke="#58a6ff"/>
    <text x="457" y="64" text-anchor="middle">真相（如策略指针）</text>
    <rect x="568" y="42" width="130" height="36" rx="8" fill="#1c2330" stroke="#262e3d"/>
    <text x="633" y="64" text-anchor="middle">全局视图 = 投影</text>
  </g>
  <text x="172" y="66" font-size="16" fill="#f85149">≠</text>
  <line x1="522" y1="60" x2="564" y2="60" stroke="#58a6ff" stroke-width="1.5" marker-end="url(#ph4)"/>
  <text x="30" y="112" font-size="10.5" fill="#f85149">并发读改写下必然漂移——实测 64 次并发交换只推进 54 代，「换过没有」给不出答案</text>
  <text x="392" y="112" font-size="10.5" fill="#3fb950">投影随真相走，天然一致，不会各说各话</text>
  <text x="30" y="142" font-size="10.5" fill="#9aa7b8">文档数字同理：手抄一遍 = 多一份会静默失真的真相（本站数字全部生成）</text>
</svg>
</div>
<p class="diagram-cap">派生状态只留一个真相；要全局视图就用投影，绝不开第二个计数器。</p>
**手抄一遍就等于多一份会静默失真的真相**，所以本站数字全部生成（见
[包清单与机器事实](../reference/packages.md)）。

## 每个机制都有事故出处

这个仓的文档风格是「纪律 + 事故来源」——不是仪式感，是因为**只有记住形态，
才认得出下一次**。举三个代表：

| 机制 | 它防的事故 |
|---|---|
| `commitgate` 放闸凭令牌 | 靠 (状态,分支,序号) 内容比对认人：同名分支的输家能删掉赢家的闸，ABA 场景迟到的释放会放掉新持有者——实测 8 路同名并发出现 4 个赢家 |
| `protocol` 证据编号唯一 | 自动编号与模型自报编号撞号时「歧义静默」——同号引用查不出错，比缺失更难查 |
| `netguard` 代次单一真相 | 两个独立计数器记录同一代次，非原子读改写下 64 次并发交换只推进 54 代——「换过没有」给不出答案 |

想看完整的形态库，见仓内
[AGENTS.md](https://github.com/yi-nology/agent-harness/blob/main/AGENTS.md) 与
[docs/USAGE.md 的坑速查表](../reference/pitfalls.md)。
