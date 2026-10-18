# AGENTS.md — AI 编码代理工作纪律（agent-harness）

本仓是**内核**：只放编排机制，不放领域知识。这条边界靠纪律 + 门禁双保险，
AI 代理在本仓干活时最容易犯的错是「顺手把宿主的需求写进来」。

## 边界纪律（最高优先级）

- **新增任何代码前先判归属**：这是机制还是资产？
  - 机制（与「谁在用它」无关）→ 本仓；
  - 资产（运维词表/专家包/诊断链/拓扑内容/品牌文案）→ 宿主仓，走宿主接口提需求。
- **不得 import 宿主仓**：`git.enjoye.top/enjoydream/bianque/` 一律禁止
  （`harness-no-host-dependency` 门禁阻断）。需要宿主能力时，定义接口由宿主实现。
- **不得引入领域词**进生产代码。`make architecture-check` 会拦；
  确有不可立即清偿的债，走 policy `exceptions` 显式登记 + 写 `until` 与拆除条件，
  **不做静默豁免**。
- **测试夹具用合成数据**：不要把「内存分析Agent」「本机」「故障诊断」写进测试。
  夹具是契约的示例，示例带领域味就等于把契约钉死在运维域上。
  （原 15 处 warn 级债——`protocol` 11 + `watchdog` 4——已于 2026-10-18 全部改为
  合成数据，规则 `harness-test-fixture-*` 随之从 warn **升为 error**：长期挂在 warn
  的规则等于没有规则，同一批词面会无声回涨。）

## 与 oekit 家族的一致性（2026-10-17 确立）

本仓不是孤岛：**oekit**（基础库）→ **agentkit**（agent 原语）→ **agent-harness**
（编排内核）→ 上游宿主仓是同一条依赖链。harness 曾是矩阵里唯一不守
家规的成员——家规只写在别处的文档里，本仓既不执行也不自查。现已把可机械判定的
三条做成门禁（`architecture-policy.yaml` rules 四~六）：

| 规则 | 拦什么 | 为什么必须拦（不是风格问题） |
|---|---|---|
| `harness-no-bare-goroutine` | 生产代码裸 `go func` | goroutine 内 panic **无法被外层 recover**，直接杀进程。走 `concurrency/async.GoSafe` |
| `harness-no-printf-log-callback` | 公开 API 里的 `logf func(string, ...any)` | printf 回调一旦焊进签名就是**不可逆 API 债**；留痕走 `logx.Delegating` 懒解析，宿主用 `SetDefault` 换后端 |
| `harness-single-errors-package` | `github.com/pkg/errors` 等第二套 errors 包 | 类型同一性红线：混用会打断 BizError 的 `As/Is` 判定（Not Found 变 500）|

三条规则都已做**反向验证**（注入违规 → 门禁报错 + 退出码 1），不是走过场。
「从没见过它触发」的规则等于没有规则——改规则时同样要这么验。

### 错误处理：从 stdlib 链路迁移到 `oekit/pkg/errors`（2026-10-18 起）

此前本仓**有意不执行** oekit 家规的「一律使用 pkg/errors」，三条理由见下——
**但这三条已全部失效，债到期**：

1. ~~**`pkg/errors` 缺两个必需能力**：`New` 与通用 `As`~~ → **已补齐**。依据
   <https://oekit.github.io/llms.txt>：`oekit/pkg` 自 **v0.12.0** 起
   `errors（…v0.12.0 起 New/As/Is/Unwrap 与 stdlib 同型透传——哨兵/As 判定单
   import 即可）`。原判断「双 import 两套 errors 比现状更乱」不再成立。
2. ~~**收益为零**~~ → `Wrap` 的 nil-safe 优势现在可用，kv 上下文照家规用。
3. ~~**本仓无 BizError**~~ → 仍然无 BizError，但维持理由的成本已归零。

**目标口径**：哨兵用 `pkg/errors.New`，包装用 `pkg/errors.Wrap(err, msg, kv...)`
（变参 kv，非 `Wrapf`；nil-safe + kv 结构化上下文），判定用同包 `Is`/`As`，
解包到根用 `Cause`，链路保持单 import，不再双 import stdlib。

**已清偿（2026-10-18）**：`go.mod` 已升到 `oekit/pkg v0.13.0`，全仓 24 个文件
（含测试）统一切到 `pkg/errors`，`pkg/errors.New` 就是 `stderrors.New` 的 shim
（`pkg/errors/std.go`），`Wrap` 的输出格式 `msg + ": " + err.Error()` 与
`fmt.Errorf("msg: %w", err)` 逐字相同，故改写是行为等价的。

门禁 `harness-single-errors-package` 已**收紧为「只允许这一套」**：
`forbid_import_regex: '^\s*"errors"$|"github\.com/pkg/errors"'`，
两条均已做反向验证（注入违规 → 报错 + 退出码 1）。
所以「不小心又写回 stdlib errors」从此不是靠自觉，是机器拦。

配套记录：`docs/MIGRATION_OAGENTKIT_20261018.md`。

## 工程门禁

```bash
make check                # 全量门禁，见下方 check 目标的依赖链
make architecture-check   # 单跑架构门禁；--report 看全量
```

门禁本体**全部在 `make check` 里**，CI 只执行它——门禁若只挂在 ci.yml 上，
本地要到推送之后才知道自己违规，那不是「提交前门禁」而是「推送后门禁」。
新增门禁一律接进 `check`，不要只加到工作流文件里。

- 提交前必须绿。门禁红了不要绕过——先判断是**代码错了**还是**规则该改**，
  后者必须走「先改 policy 再写代码」，且在 commit 说明里写清理由。
- 覆盖率阈值 `COV_MIN=60` 是真阈值不是摆设；内核包新增必须带测试。
- 结构阈值同样是**真阈值**：`thresholds.enforce: true`，`max_file_lines: 400`
  超限即非零退出（2026-10-18 起，此前 3 处超长文件已拆分归零）。
  注意拆分只是让机械阈值过线——若某个模块拆完仍读不动，该重新分组，
  不要靠继续拆小来假装问题解决了。
- 正则一律 RE2（Go 原生不支持 `(?!...)`）；负向前置用 policy 的
  `negative_follow` 结构化表达，不写进正则。

## LICENSE 署名

`Copyright (c) 2024-2026 enjoydream` —— **照此为准，不要因为仓名是 `yi-nology`
就改**。版权归属看的是作者实体，不是代码挂在哪个 GitHub org 下；`oagentkit/*`
与 `oekit/*` 全部是这一行，本仓属同一家族，改了反而制造不一致。
（`yi-nology` org 下各仓目前都没有 LICENSE 文件，无从参照。）

## 本机环境

- **一切 `go build` / `go test` 带 `CGO_ENABLED=0`**（macOS 新 SDK 27.0 与 CLT
  linker 不兼容 + Xcode 许可未接受）。Makefile 已内置，不要绕过。
- 私有模块直拉已下线（2026-10-18 起依赖全部为 public 的 `github.com/oagentkit/*`
  与 `github.com/oekit/*`），`GOPRIVATE` 配置已随公开化一并从 Makefile 移除。
- 若遇 `checksum mismatch`：先确认是否**同名 tag 被重推**
  （`agentkit` / `oekit` 历史上发生过）。修法：
  摘掉 `go.sum` 旧 hash 行 → 直取重建 → **不要** `git add -A` 式覆盖别人的 go.sum。
- **要同时改上游矩阵仓（guardx 等）时，用 `make devlink` / `make devunlink`**，
  **不要**在 go.mod 写 `replace => ./.guardx-work/...`。
  根因：replace 只在主模块生效，本仓自己构建全绿，下游把它当依赖时该 replace 被静默忽略，
  guardx 按 MVS 回落到已发布旧版本 → 报 `no required module provides package ...injectband`，
  错指 guardx 而非真正原因（见 issue #1）。CI 的 `go.mod 不得含 replace` /
  `go.work 不得入库` 两条门禁已做反向验证，别绕过。

## 会话协作

- **提交身份固定为 `MurphyYi <zy84338719@hotmail.com>`**，本仓已 `--local` 配好。
  该 identity 在本仓的 local config 里，**不要动 global**（global 是别的项目在用的工作身份）；
  也不要用 `-c user.name=...` 临时覆盖绕过——那样绕过的是约定不是修正。
- 提交用 `git add <具体路径>`，**禁止 `git add -A`**（历史教训：卷入 scratch 目录）。
- 中文文档批量编辑后 `grep U+FFFD` 自检。
- macOS bash 3.2 多字节炸弹：`$VAR` 后紧跟中文/全角字符时 bash 会把多字节首字节
  吞进变量名，`set -u` 下直接崩——一律写 `${VAR}`。
