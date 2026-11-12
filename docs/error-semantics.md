# 全仓错误哨兵语义总表

> 写给**要在自己产品里判定内核错误的宿主开发者**：哪个包有哪些哨兵、
> 什么条件下触发、拿到之后该怎么处置——一张表判完，不再逐包翻注释。
>
> 本表与代码由 `make apilifecheck` 机器校验（`cmd/apilifecheck`）：
> 代码里新增哨兵/导出错误类型而未登记 ⇒ 红；表里出现代码中已不存在的
> 幽灵条目 ⇒ 红。**先登记再合入**，不是发版后补文档。
> 清单小节名必须与 `api-lifecycle.yaml` 的 `pkg` 完全一致（含 `internal/`
> 前缀这类路径形态），段落挂错包名同样红。

## 判定方式

- 本仓全仓单套 errors 包：`github.com/oekit/pkg/errors`（家规
  `harness-single-errors-package` 门禁强制）。哨兵用它 `New`，包装用它
  `Wrap`（输出 `msg + ": " + err`，与 `%w` 同型），判定用同包
  `errors.Is` / `errors.As`，解到根用 `Cause`。
- **未导出哨兵也参与判定**：包内私有哨兵会经包装链到达调用方，
  `errors.Is` 判包装链不需要符号导出。表中标注「包内」的那些，
  宿主用字面哨兵判不了，但能通过导出包装函数或结构化类型间接判定
  （ptc 的三个预算哨兵经 `Result` 分类暴露，egress/gatecheck 的经
  返回错误文案与其所属档位判定暴露）。
- 处置三档口径（下表「调用方处置」列的动词统一按这个语义用）：
  - **重试**：可恢复，退避后重试同一动作即可；
  - **对账**：结论未知或已有赢家，先裁决再动作，不许盲目重做；
  - **修声明**：装配/配置/编程缺陷，重试无意义，改调用方或配置。

## 哨兵总表（按包）

### budget

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrTooFewItems` | 有效条目数低于最小压实体量 | 放弃本轮压实（正常业务态，非故障）；把 Detail 呈给用户 |
| `ErrCutoffTooLow` | 截断下标非正，保留尾部会吞掉整段会话 | 修声明（调用方参数错），重试无意义 |
| `ErrTooFewToSummarize` | 待摘要条目数低于最小摘要批量 | 放弃本轮压实，等条目积累后再建计划 |
| `ErrPlanMismatch` | Apply 入参条目与计划不符（计划已失效或下标算错） | 重建计划再压实；不得按过期计划硬压（会吞掉该保留的尾部） |
| `ErrEmptyPlan` | 零值计划（未指定任何待摘要条目） | 修声明：排查建计划路径，零值计划不该被拿来 Apply |
| `ErrPlanInconsistent` | 计划字段互不自洽（非 BuildPlan 产物） | 修声明：计划只能由 BuildPlan 造，手搓矛盾字段一律拒绝 |
| `ErrNotWired` | 归档/摘要接口未装配 | 修声明：装配宿主接口后再压实 |

### backend

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNotSatisfied` | 要求未被该后端满足（隔离不够强 / 出站档低于要求 / 自报兜不住预算） | 换后端或降要求；报错文案带两侧值，重试无意义 |
| `ErrNoBackend` | 登记簿里没有能满足要求的后端 | **绝不降级**：补登记能满足的后端，或改要求；重试无意义 |
| `ErrBadSpec` | 要求自身不合法（越界档位 / 负预算） | 修声明：改调用方参数 |
| `ErrSilent` | 后端未声明该能力面（沉默按最弱处理） | 补声明或换后端——「没声明」与「声明了但不够强」修法不同，两类报错分得开 |
| `ErrDuplicateID` | 同一 ID 被登记两次 | 修声明：登记簿去重 |
| `ErrBadBackend` | 登记的后端不合法（nil / 空 ID） | 修声明 |
| `ErrUnknownProgram` | 兜底后端没有登记这个名字的处理函数 | 修声明：先 `Inline.Register` 再执行；不静默跑空 |

### checkpoint

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNotFound` | 该 runID 无进度快照（**首次执行的正常情形**） | 按「没有快照」走全量首跑；宿主实现 `Load` 无记录时**必须**返回它，不得返回 `(零值, nil)`——那会被误判成「从没跑过」而重复副作用 |
| `ErrStaleSeq` | 快照序号未严格递增，被单调闸拒绝 | 放弃本次写入，按 Committer 流程重新抢新序号（对账：以库内最新为准） |
| `ErrStoreMissing` | 未装配存储就要提交快照 | 修声明：装配存储（显式失败，不静默成功） |

### cmdpolicy

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrUnjudgeable` | 命令超长或解析失败，未进 AST 判定 | 修声明口径的 fail-closed：**判不了就不放行**，按审批拒绝处置，不得当成「没有命中规则」放行 |

### commitgate

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrAlreadyCommitted` | 该环节终态已被提交过（两条分支都赢了） | 对账：放弃本次提交；用 `As` 取 `AlreadyCommittedError` 读先前提交者与状态，判定谁是赢家 |
| `ErrEmptyRef` | 引用缺字段（空 RunID 或空 Step） | 修声明：补全引用后重试 |

### delegate

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrUnsupported` | 被选中的执行方不提供所需能力 | 换执行方（最小能力原则选出的这一个接不了） |
| `ErrNoRunner` | 没有任何 offer 能满足请求 | 放弃或扩 offer 集合；重试无意义 |

### drain

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrDeadline` | 排水预算耗尽，轮次被强制中断 | 不是普通停机：宿主按此区分留痕来源，对被中断的轮次按「未收口」对账 |

### egress

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `errEnvCannotEnforce`（包内） | 禁网档位无法靠环境变量兑现（必须由带能力声明的后端强制） | 修声明：区分「档位声明写错」与「执行环境缺能力」，后者换带能力声明的后端 |

### eval

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNoChecks` | 评估清单为空（零判项不给「全过」的假绿） | 修声明：显式列出判项；「没有检查」与「检查通过」必须分得开 |
| `ErrBadSpec` | 评估清单不自洽（判项不在词表内 / 重复列出 / 期望清单缺失） | 修声明 |
| `ErrBadRecord` | 录制文件不合法（编码失败 / 行解码失败） | 修数据：按行号定位坏行 |

### gatecheck

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `errNoInner`（包内） | 受管后端没有内层真后端 | 修声明：装配内层执行后端 |

### guardset

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrEmptySet` | 构造空守卫集合 | 修声明：没人看着的调用不该看起来能用 |
| `ErrUnnamedGuard` | 守卫缺名字（留痕无法回答「哪一环拒的」） | 修声明：给守卫命名 |
| `ErrDuplicateGuard` | 守卫重名（同一答案两个候选） | 修声明：去重 |
| `ErrUnassembled` | 未经构造的 Set 被拿来求值（运行期 fail-closed） | 修声明：先构造再求值 |
| `ErrUnknownGuard` | 撤销一个不存在的守卫 | 修声明：不静默忽略，核对宿主的守卫账本 |
| `ErrNilMutate` | 换份的改写函数为 nil | 修声明：静默无动作比报错更难查 |
| `ErrNilSubscriber` | 换份订阅回调为 nil | 修声明：装配订阅回调 |

### handoff

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrUnknownKey` | 交接单不存在 | 修声明：先 `Offer` 再动作；首交前的查询走它属正常态 |
| `ErrTaken` | 交接单已被领取（并发 Take 的输家 / 已领取后 Cancel） | 输家属正常结果：放弃本次领取；收回方按「已被接手」改走对账 |
| `ErrExpired` | 交接单已超时限（迟到领取 / 迟到回执） | 重新交接 = 新一代；先对账（工作可能已被做了或没人做） |
| `ErrNotAddressee` | 领取者不是指定接收方 | 修声明：只有 To 能领取 |
| `ErrNotSource` | 收回者不是交接发起方 | 修声明：收回权只在 From |
| `ErrLeaseLost` | 领取令牌不匹配（凭令牌认人，不凭内容） | 对账：责任期已易主或已收口，迟到动作一律放弃 |
| `ErrAlreadySettled` | 交接单已是终态 | 放弃本次动作；重新交接开新一代 |

### netguard

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrBlockedName` | 出站主机名命中拦截名单 | 放弃该主机；用 `As` 取 `BlockedNameError` 读 host 与命中规则留痕 |
| `ErrNilSubscriber` | 扩展策略订阅回调为 nil | 修声明：装配订阅回调 |
| `ErrInvalidBlockedSuffix` | 扩展拦截后缀非法（注入期拒绝） | 修声明：修正后缀配置 |
| `ErrTooManyHops` | 重定向跳数超限 | 放弃该 URL（安全策略拦下，重试同一 URL 无意义） |
| `ErrEmptyLocation` | 3xx 无 Location——「链条到此终止」信号，**不是被拒** | 把当前响应当最终响应返回；不要重试，也不要把它当失败留痕 |

### offload

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNoStore` | 未装配落盘面（`Request.Store` 为 nil） | 修声明：装配落盘面 |
| `ErrBadExt` | 扩展名非法（会拼进逻辑键，未经净化使去重失效） | **条目级** fail-soft：该条目保持内联，修配置后下轮生效 |
| `ErrBadHash` | 内容摘要长度不足，无法推导分片目录 | 条目级 fail-soft：同上 |

### pii

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrUnknownSurface` | 宿主配置注入了未登记的敏感面（fail-closed） | 修声明：修正配置里的面名 |
| `ErrUnknownStrategy` | 配置注入了未登记的处置策略 | 修声明：修正策略名 |
| `ErrNilSubscriber` | 处置策略订阅回调为 nil | 修声明：装配订阅回调 |

### protocol

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNotReport` | 输出不是统一报告 JSON | 降级读 markdown 主体，**不阻塞**（容错解析的正常分支） |
| `ErrViolation` | 报告未通过 schema 白名单校验 | **必须拦截**；违规项经 `errors.Join` 附带，逐项处置 |

### ptc

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `errDeadline`（包内，经 `Result` 分类暴露） | 程序执行超过墙钟预算 | 放弃本轮程序执行，走逐轮 ReAct 兜底；对已发生的绑定调用按幂等键对账 |
| `errBudgetCall`（包内，同上） | 绑定调用数超预算 | 同 `errDeadline` |
| `errBudgetByte`（包内，同上） | 绑定输出字节超预算 | 同 `errDeadline`（并缩小采集口径） |
| `ErrInvalidSignature` | 绑定调用形态串非法（装配期缺陷，非程序问题） | 修声明：修正装配期签名 |
| `ErrMemoryLimitUnsupported` | 本环境无法施加内存上限 | 换执行环境或去掉该要求；**与执行域异常终止是两类失败**，不得合并处置 |

### sideeffect

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrInvalidAction` | 动作定义不完整（缺 Key 或 Resource） | 修声明：补全动作定义 |
| `ErrNoStore` | 未装配存储就要执行副作用 | 修声明：装配存储（显式失败，不静默成功） |
| `ErrUnknownOutcome` | 存储侧返回了未定义的结论 | 修声明：存储实现违约（必须显式处理「不知道」，不许下游猜默认值） |
| `ErrLeaseLost` | 续期时发现闸已易主（持有资格失效） | **事前收手**：本次动作不得继续；与 `ErrFenceStale` 的区别是它发生在施加之前，还能悬崖勒马 |
| `ErrFenceStale` | 栅栏令牌过期，或本次尝试已被存储侧转入未知态——这把钥匙不再有效 | 下游拒收迟到写入是对的；持有者停止使用旧租约，重新走接管/重试流程（对账） |
| `ErrOutcomeUnknown` | 该 Key 处于「结果未知」态 | **对账**：先裁决（确认已生效/未生效）才能再动这个 Key；默认挑「没生效」＝默认重复施加 |
| `ErrNotUncertain` | 对不处于未知态的 Key 发起裁决 | 按裁决对象的实际状态处置：可能已正常完成，也可能压根没失败过；排查调用时序 |
| `ErrNotHolder` | 持有者身份不匹配——出示的凭据不是本次授予铸造的那份（令牌经下游流转后被人冒用） | 放弃本次操作并排查凭据流转链；与 `ErrFenceStale` 的分工：那个是「这一代已过去」，这个是「代或许还在，出示者不对」 |
| `ErrResourceMismatch` | 租约与动作的资源不一致（A 资源的租约被拿去做 B 资源的操作） | 修声明：Complete/Renew/MarkUncertain 的 act 与 lease 必须同资源，两个真相等于没有真相 |
| `ErrReconcileTaken` | 该 Key 已有未过期的对账占用（同一时刻至多一个对账者） | 等待或放弃：占用过期后可重新认领（BeginReconcile 可重入）；不要并发抢对账 |
| `ErrUnsafeNotRetryable` | 动作声明为不可重试（Unsafe）——对账判「未生效」也不解除阻塞 | 二选一：承认查询不可信重新对账；或确认结论可靠并接受风险后，带 `ForceRetryable=true` **显式**重新裁决 |
| `ErrActionConflict` | 同一 Key 的动作身份（Resource/Payload/RetrySafety）与首次登记不一致——判定优先于「已完成 → 重放」 | 不可重试：要么用与登记一致的身份，要么承认 Key 撞号（两条逻辑动作共用一个 Key），换 Key |
| `ErrReconcileLost` | 出示的对账占用凭据不是当前有效的一份（已过期 / 已被接管 / 从未认领） | 迟到的对账者：放弃本次裁决；要提交就重新 BeginReconcile 认领后再查（旧查询窗口跨越了状态变化，不可信） |
| `ErrInvalidTTL` | BeginReconcile 的 ttl ≤ 0（出生即过期的认领 = 没有占用） | 修正入参：给正时长的认领窗口（对账查询的预估耗时 + 余量） |

### spill

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrBadName` | 定位符非法（含分隔符/点段/空名——只允许本会话存档目录下的裸文件名） | 修声明：修正定位符 |

### sidetest

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `errSinkBoom`（包内） | 栅栏契约套件 `write_error_no_partial_write` 场景注入的写入体故障——断言「写入体报错 ⇒ 预占与部分写一起回滚」 | 套件内部判据用：断言错误经 `WriteFenced` 原样透出且零落地；宿主无需处置 |

### taskboard

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNotFound` | 任务不存在 | 修声明：核对任务 id（可能拼写错或已被清理） |
| `ErrTaken` | 任务已被他人活着持有 | 不重试：走协调（或等租约过期后接管） |
| `ErrBlocked` | 依赖未完成 | 等：依赖完成后再来认领（可稍后重试） |
| `ErrTerminal` | 任务已是终态 | 放弃：读既有结论，不重开 |
| `ErrLeaseLost` | 令牌不匹配（租约已易主或被释放过） | 对账：本次操作作废，重新 `Claim` |

### examples/containerbackend

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrEngineCall` | Docker Engine 调用失败（创建 / 启动 / 等待 / 读取任一步） | 重试或查引擎侧（不可达 / 镜像不存在 / 参数被拒）；作业没跑起来属基础设施故障 |
| `ErrNotWired` | K8s Job 适配尚未接入（占位） | 修声明：按包注释四步接入后再登记进登记簿 |
| `errWallClock`（包内） | 墙钟预算到点（context.Cause 的标记；对外呈现为 `Result{OK:false, Detail:{budget_exceeded: wall_clock}}`，不是 error 返回） | 无需处置：预算被强制兑现是契约行为；作业没跑完按作业失败对待 |

### tooldisclose

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrNotLoaded` | 工具在目录内，但 schema 尚未取回 | **重试**：门禁已把它作为副作用激活，重试即成（两哨兵重试性相反，混淆会把策略否定变成无穷重试） |
| `ErrNotInCatalog` | 工具不在目录内，从未可用 | **放弃**：不存在的东西不会因重试出现，任何退避策略都应停手 |
| `ErrSchemaNotDerivable` | 该工具的 schema 派不出合法的位置调用形态（含工具名为空） | 修声明：修工具 schema/名字 |

### metrics

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `ErrUnregisteredMetric` | 埋点用了未在 `Specs` 登记表登记的指标名（`Validate` 判，`Mem` 严格档计数） | 修声明：先在 `metrics.go` 登记指标（名称/类型/维度/基数上界）+ ACCESS-POINTS.md 加接入点，再埋点——未登记的数是看板口径外的暗数 |

## 结构化错误类型（errors.As 判定）

> 哨兵判「是什么错」，下面的类型判「错的现场长什么样」——调用方要读
> 结构化字段（谁提交的、哪一跳被拒）时用 `As`。

| 符号 | 含义 | 调用方处置 |
|---|---|---|
| `commitgate.AlreadyCommittedError` | 终态重复提交的现场：先前提交者（`Prev`）、`PrevSeq`、双方状态与分支 | `As` 后读字段对账谁是赢家；`Unwrap` 回 `ErrAlreadyCommitted` 可统一 Is 判定 |
| `netguard.BlockedNameError` | 名字层拒绝现场：`Host` 与命中原因 `Reason` | `As` 后读字段留痕；`Unwrap` 回 `ErrBlockedName` |
| `netguard.HopDeniedError` | 重定向链某一跳被拒：`Hop`（1 = 起始 URL）、`URL`、底层 `Err` | `As` 后按跳号定位是哪一跳出的问题，再对 `Err` 做 Is 判定 |
| `ptc.ExecutionAborted` | 子进程执行域异常终止：退出状态 `Exit` 与**已脱敏**的 `Stderr` 尾部 | `As` 后读 `Stderr` 判死因（内核兜底 vs 自身 fatal）；与 `ErrMemoryLimitUnsupported` 分开处置 |
| `tooldisclose.GateError` | 门禁拒绝的结构化形态：工具名、拒绝类别、是否可重试、是否已激活 | `As` 后按字段分派处置；模型侧指引走链上 `Reason`，不要自己拼文案 |

## 维护纪律

1. 新增哨兵/导出错误类型 ⇒ 同一提交里在本表登记（`make apilifecheck` 拦截漏登记）；
2. 删除哨兵 ⇒ 同步删行，不留幽灵条目（同门禁双向判定）；
3. 「触发条件」「调用方处置」两列写**事实与动作**，不写设计推导——
   推导留在各包注释与本仓 ADR 里；
4. 各包哨兵语义的**权威定义**仍是包内注释；本表是导航与总览，
   两处不一致时以门禁红为信号先对齐再改。
