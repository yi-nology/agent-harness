// Package backend 任务式执行后端的**契约面**：隔离声明、能力问询与选择判据。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是哪一段（issue #109）
//
// 本仓的执行面此前只有「程序式」一条：@@ptc@@ 的两个后端（进程内解释器 /
// 子进程）跑的是 JS 子集程序，档位词表在上游 `guardx/sandbox.Level`
// （none / process / os / machine）。Docker、Kubernetes Job 这类**任务式**
// 执行——按镜像 + 参数跑一个完整作业——在这张表上没有位置：
//
//   - 上游 Level 词表是**冻结的**：往中间插一档（container）要么重排枚举值
//     （跨仓破坏性变更），要么追加在 machine 之后（强弱序立刻说谎）；
//   - 任务式执行与程序式执行的请求形状不同：前者是「镜像 + 参数 + 挂载 +
//     资源上限」，后者是「程序源码 + 预算」。
//
// 故本包给任务式执行定**自己的契约面**，三条与既有执行面同源的纪律：
//
//  1. **能力是后端自报的，沉默按最弱处理**——后端没声明能强制哪一档隔离 /
//     出站 / 内存，判据就当它兜不住（与 @@sandbox@@ 的 Capable /
//     EgressEnforcer 同一条方向：漏声明得到的是「兜不住」，不是「兜得住」）。
//  2. **要求无满足者即拒绝，绝不降级**——登记簿选不到满足要求的后端就报错，
//     不挑一个「差不多的」（与 sandbox.Registry.Resolve 同一条纪律）。
//  3. **满足者取最弱**——多个后端都满足时取隔离最弱者，同档按 ID 字典序
//     （与 delegate.Route 的最小能力原则同源：按登记顺序取第一个是错的，
//     一次无关的配置重排就会换掉执行方）。
//
// # 隔离轴为什么在本包重立而出口词表复用
//
// 出站档位（unrestricted / proxy / denied）的词表已经长在上游 tier 0
// （`sandbox.EgressMode`），本包直接引用——跨模块共享的词表必须低于所有
// 实现方，另立一份就是第二真相。隔离轴不同：它的词表被上游枚举值**钉死**
// （插入即破坏强弱序），没有可复用的扩展点，故本包给任务式执行立自己的轴，
// 并在 Isolation 的注释里写清与 sandbox.Level 的对应关系与为什么不共用。
//
// # 为什么容器适配的参考实现不在内核里
//
// Docker Engine / Kubernetes Job 的接入牵涉宿主的部署形态（socket 位置、
// 集群凭据、镜像仓库策略），都是宿主的事——内核只给契约与判据。参考实现
// 在 `examples/containerbackend`（Docker Engine API 逐字实现本契约 +
// Kubernetes Job 接入占位），宿主抄它、不 import 它（与 examples/pgstore
// 对 taskboard.Board 的分工同构：内核证契约，示例证接线）。
package backend

import (
	"fmt"
	"strings"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sandbox"
)

// Isolation 任务式执行的隔离轴（序即强弱：档位越大，被执行的作业能触达的
// 宿主面越少）。
//
// # 为什么不复用 sandbox.Level
//
// 上游 `guardx/sandbox.Level` 的枚举值即强弱序（none=0 … machine=3），往
// 中间插「容器」一档必然重排既有值——那是对全部既有实现的跨仓破坏性变更；
// 追加在 machine 之后则让「容器弱于虚机」的强弱序说谎。任务式执行因此
// 立自己的轴，对应关系**只写在注释里、不写进代码**（代码级映射会变成
// 第二真相，两份迟早漂移）：
//
//	IsolationNone      ↔ sandbox.LevelNone      与宿主同权限
//	IsolationProcess   ↔ sandbox.LevelProcess   进程内解释器边界
//	IsolationContainer ↔ sandbox.LevelOS        容器边界（namespace/cgroup，
//	                                              共享内核——内核级裁剪，
//	                                              但逃逸面大于独立内核）
//	IsolationVM        ↔ sandbox.LevelMachine   独立内核（硬件虚拟化）
//
// # 零值是最弱的一档，这是刻意的
//
// 未声明档位的后端零值即 IsolationNone，只会被「要求=none」选中；任何更高
// 要求的解析都会因「无满足者」而拒绝。方向是安全的那一侧：漏声明的后端拿到
// 的是最弱的选择，不是最强的豁免。
type Isolation uint8

const (
	// IsolationNone 无隔离：作业与宿主同权限，能触达宿主能触达的一切。
	//
	// 它是**显式的一档**，不是「缺省即无」：把「没有隔离」写成档位，宿主
	// 必须显式登记一个这样的后端，而不是让「没登记」悄悄等于「没隔离」。
	IsolationNone Isolation = iota
	// IsolationProcess 进程内边界：作业跑在宿主进程内的解释器/闭包里，
	// 能力面靠语言子集或注册白名单裁剪。不是内核级边界——解释器自身的缺陷
	// 可以越过它。
	IsolationProcess
	// IsolationContainer 容器边界：namespace / cgroup / seccomp 由内核裁剪
	// 文件、网络与系统调用视图。共享宿主内核，逃逸面大于独立内核。
	IsolationContainer
	// IsolationVM 独立内核边界：作业有自己的内核与虚拟设备。
	IsolationVM
)

// isolationNames 档位名，下标即档位序。
var isolationNames = [...]string{"none", "process", "container", "vm"}

// String 档位名（声明与错误文案用）。
func (i Isolation) String() string {
	if int(i) < len(isolationNames) {
		return isolationNames[i]
	}
	return fmt.Sprintf("isolation(%d)", uint8(i))
}

// AtLeast 报告 i 是否达到（或超过）min。
func (i Isolation) AtLeast(min Isolation) bool { return i >= min }

// Valid 报告档位是否在词表内（越界值不得被当成最弱档——那是沉默放行）。
func (i Isolation) Valid() bool { return i >= IsolationNone && i <= IsolationVM }

// Mount 一次挂载声明：宿主路径 → 容器内路径。
type Mount struct {
	// Source 宿主侧来源路径（后端语义内的引用，原样携带）。
	Source string
	// Target 作业侧目标路径。
	Target string
	// ReadOnly 是否只读。可写挂载是暴露面，声明面要能区分它。
	ReadOnly bool
}

// Job 一次任务式执行的请求。
//
// 与 sandbox.Request（程序源码）不同：任务式执行的请求形状是「镜像 + 参数 +
// 挂载 + 预算」。Image 是必填锚点——没有镜像的作业无从执行，也无从隔离。
type Job struct {
	// Image 镜像引用。带 digest 的引用（name@sha256:…）必须**原样携带**：
	// 后端不得把 digest 换成浮动 tag——「钉住什么就跑什么」是作业级可复现
	// 的前提。
	Image string
	// Args 位置参数（原样传给镜像入口点）。
	Args []string
	// Env 环境变量，K=V 形态，**白名单**：由宿主显式构造，后端不得替它补全
	// （与 egress_paths 的同一条纪律——整份环境交给被执行的作业，等于把
	// 凭据也交出去）。
	Env []string
	// Mounts 挂载声明（后端可拒绝不认识的形态，见各实现说明）。
	Mounts []Mount
	// WorkDir 作业内工作目录（空 = 镜像缺省）。
	WorkDir string
	// Budget 资源预算（sandbox.Budget 逐字复用——资源词表只有一份）。
	//
	// MaxCalls 在任务式执行里没有对应物（作业不是逐调用解释的），要求了它
	// 的 Spec 不会被声明 Capable 的任务式后端满足（见 Spec.SatisfiedBy）。
	Budget sandbox.Budget
}

// Validate 判作业请求是否自洽（不满足即报错，不静默补全）。
//
//   - Image 必填：空镜像的作业既不能执行也无从隔离；
//   - Env 每条必须是 K=V 且键非空：格式错的环境变量在后端侧要么被静默丢弃
//     要么被静默拼进一条——两种都让「作业看见了什么」不可判定；
//   - 挂载两端都必填：缺端点的挂载在后端侧无从解释；
//   - 预算不得为负：负值是「忘了设」与「设错了」的混合体，拒绝比按 0 处理
//     更响亮。
func (j Job) Validate() error {
	if strings.TrimSpace(j.Image) == "" {
		return errors.New("backend: 作业没有镜像——既不能执行，也无从隔离")
	}
	for _, kv := range j.Env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("backend: 环境变量 %q 不是合法的 K=V 形态（键为空或缺少 =）", kv)
		}
	}
	for _, m := range j.Mounts {
		if strings.TrimSpace(m.Source) == "" || strings.TrimSpace(m.Target) == "" {
			return fmt.Errorf("backend: 挂载 %q→%q 缺少端点（两端都必须显式声明）", m.Source, m.Target)
		}
	}
	if j.Budget.WallClock < 0 || j.Budget.MaxCalls < 0 || j.Budget.MaxBytes < 0 || j.Budget.MaxMemory < 0 {
		return errors.New("backend: 预算出现负值——「忘了设」与「设错了」不能共用一个静默解释")
	}
	return nil
}

// Result 一次任务式执行的结果。
//
// 失败分两层，判据不同：
//
//   - Exec 返回 error：**后端基础设施故障**（引擎不可达、镜像不存在、
//     创建失败）——作业根本没跑起来，重试与修复的对象是后端；
//   - Result.OK == false：**作业自己失败**（非零退出）——作业跑过了，
//     结论在 ExitCode / Stderr 里，重试与修复的对象是作业。
//
// 把两层混进一个 error，调用方就分不清「该换后端」还是「该改作业」。
type Result struct {
	// OK 作业是否成功（退出码为 0 且未被引擎判定失败）。
	OK bool
	// ExitCode 退出码（作业没有跑起来时无值）。
	ExitCode int
	// Stdout 标准输出（后端可按预算截断——截断是能力声明，见 Capable）。
	Stdout string
	// Stderr 标准错误。
	Stderr string
	// Detail 后端私有明细（引擎 id、节点标识、OOM 标记…）。
	//
	// 与 sandbox.Outcome.Detail 同一条纪律：不得成为任何可判定事实的第二份
	// 真相——成败以 OK / ExitCode 为准，Detail 只是明细。
	Detail any
}
