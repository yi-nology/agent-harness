// Package contract 编排运行契约：编排引擎与宿主状态之间的**唯一边界**。
//
// ## 为什么需要这个包
//
// L2 抽取在 `flowEngine`（42 方法）处遇到硬墙，实测结论（详见 docs/SPLIT_PLAN.md §一.4）：
// 被称作「通用环节执行机制」的 14 个方法里，11 个直接依赖 `model.Session` /
// `model.CaseStep` / `repo`——而 `Session` 有 37 个字段，其中
// `Host` / `Cluster` / `TargetPending` / `PendingQuestion` / `PendingOptions` /
// `Entry`(incident|patrol|query) / `PolicyMode` 全是纯运维域语义。
//
// **结论：只要编排的状态模型是带领域语义的宿主资产，编排机制就不可能下沉。**
// 本包就是那道出口——编排只认接口，宿主负责把领域结构适配进来。
//
// ## 设计取舍：核心字段定死 + 领域字段开放
//
// 纯粹的开放键（`Field(string)` 走天下）契约太松、两边必然漂移；纯粹定死则
// 每加一个运维概念都要改内核。故采用混合：
//
//   - **编排语义字段定死在接口上**：会话标识、环节标识、状态、当前环节——
//     编排引擎的算法只依赖这些，任何宿主都必须提供；
//   - **领域字段走开放键**：宿主专属状态（目标、策略档、租户…）由宿主自取自存，
//     内核不解释、不校验、也不需要知道它们存在。
//
// 这样约 80% 的编排逻辑可下沉，剩下 20% 留在宿主适配层。
//
// ## 稳定承诺
//
// 本包是**契约面**：方法集只增不改（加新能力时新增方法并给零值默认实现），
// 以免宿主适配层跟着内核升级频繁返工。
package contract

import "context"

// 会话状态（编排语义；与宿主存储列名无关）。
// 取值由宿主定义，但编排引擎只按「可流转」使用，不解释具体语义。
const (
	StatusCreated         = "created"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
	StatusTimedOut        = "timed_out"
)

// RunContext 编排运行上下文。
//
// 生命周期：一轮编排对应一个实例，由宿主在派发时构造、终态后释放。
// 实现须**并发安全**：编排引擎会在事件回调与主流程间共享同一实例。
type RunContext interface {
	// ID / CaseID 会话与案例标识（编排日志与事件载荷用，必须稳定）。
	ID() string
	CaseID() string

	// Status 当前状态（取值见上常量集）。
	Status() string
	// SetStatus 状态迁移；宿主应在此实现其状态机约束（如 created→running 唯一）。
	SetStatus(status string) error

	// CurrentStep 当前环节标识（agent slug 或链节点路径；空=尚未起步）。
	CurrentStep() string
	// SetCurrentStep 推进当前环节。
	SetCurrentStep(step string) error

	// Field 取宿主领域状态（按**语义键**，不是存储列名）。
	// 内核不解释键的含义，也不假设它存在——取不到返回 (false) 由调用方降级。
	Field(key string) (string, bool)
	// SetField 写宿主领域状态；宿主拒绝未知键时返回错误（fail-closed，不静默吞）。
	SetField(key, value string) error
}

// StepRef 环节引用：宿主分配的稳定标识，回传给 StepSink.Finish。
type StepRef struct {
	ID   uint64
	Name string
}

// StepSink 环节推进出口：建行 / 收口。
//
// 编排引擎不直接碰存储——所有环节状态的读写都经此出口，
// 这样换一个宿主（文件系统、内存、别的库）不必改编排逻辑。
type StepSink interface {
	// Open 新建环节：宿主应完成「建行 + 状态迁移到 running」两件事。
	// name 为环节标识（agent slug 或链节点路径），label 为用户可见语义标签。
	Open(name, label string) (StepRef, error)
	// Finish 环节收口：status 为终态（succeeded / failed / degraded / skipped）。
	Finish(ref StepRef, status string) error
}

// ToolBinder 工具面绑定出口。
//
// 编排侧只说「我要 server X 的这几个工具」，**不关心工具从哪来**
// （MCP 池、静态注册表、测试替身都实现同一接口）。这是宿主可替换的关键接缝。
type ToolBinder interface {
	// Tools 按 server + allow 白名单取工具面。
	// allow 为空 = 取该 server 全部工具。
	// 该 server 级失败（未装配/未返回）必须显式报错，不得静默返回空面——
	// 空面会让编排把「没工具」误判成「工具失败」，两者处置完全不同。
	Tools(ctx context.Context, server string, allow []string) (Tools, error)
}

// Tools 一次绑定得到的工具面。
type Tools interface {
	// Names 返回工具名列表（诊断与留痕用）。
	Names() []string
	// Invoke 按名调用；未知名返回错误。
	Invoke(ctx context.Context, name string, args map[string]any) (result map[string]any, err error)
}

// Clock 时钟注入点（避免编排逻辑直接依赖 time.Now，测试可注入确定性时钟）。
type Clock interface {
	Now() (unixNano int64)
}

// FieldKey 约定语义键。内核只**建议**这些键名（便于不同宿主对齐语义），
// 不强制——宿主可用自己的键。
//
// 注意：这些都是**编排通用语义**，不是运维域语义；运维域字段（主机/集群/目标）
// 由宿主自定键，本包不列。
const (
	FieldQueuedInput   = "queued_input"   // 待处理输入队列（多轮续跑的载体）
	FieldPendingAsk    = "pending_ask"    // 待用户答复的挂起态（见 harness/pending）
	FieldTargetSummary = "target_summary" // 目标的人类可读摘要（仅用于日志/事件载荷）
)
