// k8sjob.go —— Kubernetes Job 适配的**接入点占位**（issue #109 明确本轮不做实现）。
//
// 为什么占位也要实现 backend.Backend：契约的接入点就是接口本身。占位结构体
// 让宿主在接线前就能编译通过（方法集齐全），同时**fail-closed**——见下面
// Isolation 的自报说明：未接入前它按最弱档自报，判据不会在任何高于「无隔离」
// 的要求下选中它，Exec 一律报 ErrNotWired（响亮失败，不静默跑空）。
//
// # 宿主接入 Kubernetes Job 的四步（写明边界，不留猜测空间）
//
//		client-go 属**宿主侧依赖**：本仓与示例都不引 K8s 客户端（与
//		「Docker 客户端不进依赖面」同一条纪律；K8s 的清单/凭据更是宿主资产）。
//
//	 1. **构造**：宿主用 client-go 的 kubernetes.NewForConfig 建客户端集，
//	    存进自己的结构体（本占位不持有任何客户端字段）；
//	 2. **Isolation() 改回 Container**：Job 能落容器边界（namespace/cgroup
//	    由内核裁剪）——接入后按事实自报，占位期的 IsolationNone 随即作废；
//	 3. **实现 Exec**：Job → batchv1.Job 的映射参考 docker.go 的同一张表：
//	    Job.Image → Containers[0].Image（**带 digest 原样携带**）、Job.Env → Env、
//	    Job.Mounts → Volumes/VolumeMounts、Budget.MaxMemory → Resources.Limits、
//	    Budget.WallClock → activeDeadlineSeconds（到点由 kubelet 终止，
//	    对应 docker.go 的 kill 路径）、Job.BackoffLimit 设 1（作业失败不重试——
//	    「跑几次」不是本契约的字段，交给 spec 层决定）；
//	 4. **出站能力如实声明**：集群的网络策略（NetworkPolicy / CNI）决定了
//	    这个后端能强制哪一档 Egress——不能强制就别实现 EgressEnforcer
//	    （沉默 = 判据按最弱处理，永远不会被要求禁网的 Spec 选中）。
//
// # 为什么不提供一个「半成品 Exec」
//
// 任何半成品（空跑、只打日志、返回假成功）都会在宿主装配后把「没接上」
// 伪装成「跑过了」——那是本仓反复强调的最贵失败形态。ErrNotWired 让
// 未接入保持**未接入的形状**。
package containerbackend

import (
	"context"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/backend"
)

// ErrNotWired Kubernetes Job 适配尚未接入（接入步骤见包注释）。
var ErrNotWired = errors.New("containerbackend: k8s job 适配未接入（client-go 归宿主侧，接入步骤见包注释）")

// K8sJob Kubernetes Job 的接入点占位。
type K8sJob struct {
	id string
}

// NewK8sJob 构造占位后端。
func NewK8sJob() *K8sJob {
	return &K8sJob{id: "k8s-job"}
}

// ID 后端标识。
func (k *K8sJob) ID() string { return k.id }

// Isolation 未接入前按**最弱档**自报。
//
// 这不是谦虚，是 fail-closed：占位实现无法兑现容器边界，若自报 Container，
// 判据会在「要求=container」时选中它，然后 Exec 报错——「选到了、跑不了」
// 比一开始就选不到更浪费一次派发。自报 IsolationNone 时，任何更高的要求
// 都不会走到它头上（无满足者即拒绝，降级的空间为零）。
func (k *K8sJob) Isolation() backend.Isolation { return backend.IsolationNone }

// Exec 占位：一律拒绝（ErrNotWired）。
func (k *K8sJob) Exec(_ context.Context, _ backend.Job) (backend.Result, error) {
	return backend.Result{}, ErrNotWired
}
