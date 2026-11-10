---
title: 版本记录
---

# 版本记录

::: warning 本页由 `site/gen.mjs` 生成，勿手改
版本 / 日期 / 主题投影自仓内 `CHANGELOG.md`（发版时同步 CHANGELOG，本页随之更新）；
逐版完整变更与**下游升级须知**看仓内 `CHANGELOG.md` 与 README 的「下游升级须知」表。
:::

| 版本 | 日期 | 主题 |
|---|---|---|
| `v0.29.0` | 2026-11-11 | 判据面补强：cmdpolicy 事实面收编上游 bashguard，闭合前缀判据三类绕过 |
| `v0.28.0` | 2026-11-11 | 修复：egress 判据收严 + 门面补缺 + 子进程后端契约三处（#62 #63 #64 #65 #66 #67） |
| `v0.27.1` | 2026-11-09 | 修复：agent-harness 第三轮 issue 清零（#53 #54 #55 #56 #57 #58 #59） |
| `v0.27.0` | 2026-11-08 | 修复(lint)：删除已死的 stageErr 字段——分类载体化后无读写（unused 门禁） |
| `v0.26.0` | 2026-11-08 | 性能：检索与披露的两处热路径把不变量提出内层循环 |
| `v0.25.2` | 2026-11-07 | 重构：把剩下的横向替换一并做掉（slices.Clone / slicex / encoding），净减 25 行 |
| `v0.25.1` | 2026-11-07 | 重构：该替换的替换掉（slicex/stringx + stdlib slices），代码净减 40 行 |
| `v0.25.0` | 2026-11-07 | 修复(archcheck)：include 模式不可命中让规则静默失效（+三处哈希走上游） |
| `v0.24.0` | 2026-11-07 | 修复(archcheck)：forbid_import_regex 结构上永不命中时不再绿（issue #41） |
| `v0.23.0` | 2026-11-07 | 修复(archcheck)：遍历失败与陈旧 scope/scan_roots 不再被吞成绿灯 |
| `v0.22.0` | 2026-11-07 | 重构：收掉同体异名包装与手写等价物（三处，无行为变化） |
| `v0.21.0` | 2026-11-07 | 新增(trajectory)：OTel 映射从 seam 长成契约表——每个属性有主，父子关系落 Span.ParentID |
| `v0.20.1` | 2026-11-07 | 修复(egress)：Allow 不得让目标绕过代理——绕过名单改成显式 Bypass |
| `v0.20.0` | 2026-11-07 | 新增(sandbox,egress,ptc,archcheck)：出口声明必须有人兑现——问不到后端就不起子进程 |
| `v0.19.0` | 2026-11-07 | 新增(platform)：windows/amd64 回归支持矩阵——上游 procx Windows 进程树路径落地（issue #34 收尾） |
| `v0.18.0` | 2026-11-07 | 新增(archcheck)：全量环境判据纳入 procx.InheritAllExcept + 三处文档补齐第五条 |
| `v0.17.0` | 2026-11-06 | 修复(ptc)：边际往返成本判据改用最小值统计量——它会偶发假红 |
| `v0.16.0` | 2026-11-06 | 修复(test)：nil-ctx 契约测试豁免 SA1012（钉的就是 nil 形态） |
| `v0.15.0` | 2026-10-29 | 新增(ticket)：新增 ticket 包——一次性短时授权票据原语 |
| `v0.14.0` | 2026-10-28 | （见 CHANGELOG） |
| `v0.13.0` | 2026-10-26 | 新增(checkpoint)：依赖声明 —— 并行 fan-out 的合法乱序完成不再被作废重跑 |
| `v0.12.0` | 2026-10-23 | 新增(protocol)：判定面契约 —— 来源词表 + 置信语义种类 + 升级回落事件面 |
| `v0.11.0` | 2026-10-22 | 新增(archcheck)：执行路径门禁 —— 执行必须经 sandbox 登记簿（修 issue：P3-候选本仓侧） |
| `v0.10.0` | 2026-10-22 | 新增(guardset)：装配面对齐 netguard 的整份原子交换 —— 修两处静默失效 |
| `v0.9.0` | 2026-10-22 | 新增(guardset)：装配面 Assembly —— 撤销的归属明确化（换整份，不改其中一条） |
| `v0.8.4` | 2026-10-21 | （见 CHANGELOG） |
| `v0.8.3` | 2026-10-21 | （见 CHANGELOG） |
| `v0.8.2` | 2026-10-21 | （见 CHANGELOG） |
| `v0.8.1` | 2026-10-21 | 新增(ptc)：新增子进程执行后端——用内核地址空间上限兑现 MaxMemory |
| `v0.8.0` | 2026-10-21 | 新增(harness)：新增 retryplan 重试处置决策 + sideeffect/sidetest 续期与拒收契约 |
| `v0.7.0` | 2026-10-21 | 重构(harness)：toolfail 与 sandbox 下沉上游 guardx，本仓转门面 |
| `v0.6.5` | 2026-10-20 | （见 CHANGELOG） |
| `v0.6.4` | 2026-10-20 | （见 CHANGELOG） |
| `v0.6.3` | 2026-10-20 | （见 CHANGELOG） |
| `v0.6.2` | 2026-10-20 | （见 CHANGELOG） |
| `v0.6.1` | 2026-10-20 | 修复(archcheck)：import 规则必须认 negative_follow 豁免；补 CHANGELOG.md 与发版纪律 |
| `v0.6.0` | 2026-10-20 | 新增(netguard)：放行面具名且校验层与拨号层共用一个类型，并补上宿主接线件 |
| `v0.5.0` | 2026-10-20 | 新增(deps)：依赖地板追平上游最新一轮，并把「本仓用了哪个上游矩阵模块」纳入依赖方向门禁 |
| `v0.4.0` | 2026-10-19 | 新增(archcheck)：门禁执行体下沉为可导入包——宿主复用同一套判据，不必各写一遍 |
| `v0.3.2` | 2026-10-19 | （见 CHANGELOG） |
| `v0.3.1` | 2026-10-19 | 修复(netguard)：guardx → v0.4.1，闭合 0.0.0.0/8（"this network"）SSRF 缺口 |
| `v0.3.0` | 2026-10-19 | 新增(gates)：把 lint 接进 make check，CI 钉 runner 与 action 版本 |
| `v0.2.0` | 2026-10-18 | 新增(protocol)：把五个引擎侧字段从 LLM 解码路径剥离出来 |

> 完整变更：仓内 [CHANGELOG.md](https://github.com/yi-nology/agent-harness/blob/main/CHANGELOG.md)；
> 破坏性变更与迁移指引：仓内 [README「下游升级须知」](https://github.com/yi-nology/agent-harness/blob/main/README.md#下游升级须知破坏性变更)。
