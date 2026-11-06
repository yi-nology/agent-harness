package ptc

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/oagentkit/runtime/procx"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/agent-harness/egress"
)

// 本文件是「怎么把父子之间的管道接起来」——与 subprocess.go 里的
// 「怎么用这些管道交换数据」是**两个关注点**，故分开。
//
// 分开的另一个好处：管道建立的错误路径很啰嗦（建到第几条失败，
// 前面已建的那几条都要关掉），独立成组后这段逻辑不必挤在 run() 里。

// childPipes 一次执行用到的全部管道端。
//
// 端在父子之间的归属（每个 os.Pipe 有一读一写两端）：
//
//	startReadR  父写→子读   Start 帧
//	startWriteR 子写→父读   Result 帧
//	callReqR   子写→父读   Request 帧（子进程发起）
//	callRespR  父写→子读   Response 帧
//
// 子进程拿四个端中的「读端或写端」各一：stdin/startReadR、stdout/startWriteW、
// fd3=callReqW、fd4=callRespR。
type childPipes struct {
	startReadR, startReadW   *os.File
	startWriteR, startWriteW *os.File
	callReqR, callReqW       *os.File
	callRespR, callRespW     *os.File

	// child 已配好管道与环境的子进程（**尚未启动**）。
	child *exec.Cmd
	// stderrTail 接住子进程 stderr 的缓冲（死因就在里面）。
	stderrTail *tailBuffer
}

// newChildPipes 建立四条管道并把子进程那一侧配好（**不启动**）。
//
// 启动与「放进 cgroup」在调用方做——因为它们需要与失败路径的收尾放在一起。
//
// # 出口声明在这里兑现，而不在别处
//
// 子进程一旦起来，它内部的出站流量就**完全在本进程的 netguard 之外**——
// 「声明了受限出口」在那一侧没有任何东西兑现。本仓能给的最强保证是：
// **在 spawn 之前**把出口档位的环境一次生成出来，生成不出就**不起子进程**。
//
// spec 的零值（不限制）逐字返回 base，故旧调用点行为不变。
func newChildPipes(exe string, spec egress.Spec) (*childPipes, error) {
	p := &childPipes{}

	// 四条单向管道：启动读/启动写/调用请求写/调用响应读（子进程侧视角）。
	var err error
	if p.startReadR, p.startReadW, err = os.Pipe(); err != nil {
		return nil, fmt.Errorf("ptc: 建启动读管道失败: %w", err)
	}
	if p.startWriteR, p.startWriteW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("ptc: 建启动写管道失败: %w", err)
	}
	if p.callReqR, p.callReqW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("ptc: 建调用请求管道失败: %w", err)
	}
	if p.callRespR, p.callRespW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, fmt.Errorf("ptc: 建调用响应管道失败: %w", err)
	}

	child := exec.Command(exe) //nolint:gosec // 子进程执行的是宿主自己的可执行文件（re-exec），路径不来自外部输入
	child.Stdin = p.startReadR
	child.Stdout = p.startWriteW
	// fd3 = 调用请求写端（子进程发 Request），fd4 = 调用响应读端（子进程读 Response）。
	child.ExtraFiles = []*os.File{p.callReqW, p.callRespR}
	// 接住子进程的 stderr：**死因就在这里**（例：fatal error: runtime: out of memory）。
	// 不接住的话，父侧从管道 EOF 只看得到「没了」。
	// 只留末尾（tailBuffer 有上限）——子进程可能狂写日志，
	// 无界缓冲会把父进程内存也吃进去，那正是这个后端要防的形态。
	p.stderrTail = &tailBuffer{limit: childStderrLimit}
	child.Stderr = p.stderrTail
	// 出口档位的 env **必须**经过 egress 生成：直接 append 会跳过
	// 「代理变量覆盖旧值」「NO_PROXY 排序确定性」两条口径，而 egress 的
	// 判据（Spec.Verify）正是拿这份 env 去对照声明的。
	// 生成失败 ⇒ **不起子进程**（fail closed）：假装受限比明确拒绝更危险。
	childEnv, err := spec.Env(childBaseEnv())
	if err != nil {
		p.closeAll()
		return nil, errors.Wrap(err, "ptc: 子进程出口声明兑现不了")
	}
	child.Env = childEnv
	p.child = child
	return p, nil
}

// childBaseEnv 子进程该看见的环境（交给 egress 叠加出口声明之前的那一层）。
//
// # 为什么不是 os.Environ()
//
// 子进程是本进程的 **re-exec**，它只需要三样：起得来、能读临时目录、能按地方
// 编码解释字符串。而全量继承会把宿主进程手里的一切一起交出去——模型 provider
// 的 API key、对象存储令牌、数据库口令，全都在父进程的环境里。子进程跑的是
// **模型写的程序**（进程内 goja 的隔离面是 JS 子集，不是 OS 沙盘），把整份环境
// 递过去等于给这条隔离面开了一个谁也没声明过的侧门：程序没有网络也没有 exec，
// 但它能读到宿主的秘密再写进返回值——而返回值会回灌模型上下文。
//
// # 为什么白名单在上游而不是在这里
//
// 「子进程该看见什么」是跨宿主必须一致的判定，不是本仓的编排语义：各写一份
// 白名单，一个宿主放了云厂商密钥、另一个没放，同一个后端在不同宿主下的实际
// 强度就不同，而报告里两处都显示「已隔离」。故判据单源
// oagentkit/runtime/procx.ChildEnv：
//
//   - 基础集（PATH/HOME/TMPDIR/USER/SHELL/LANG/TERM + 代理变量与 CA 目录）照传；
//   - 形如 KEY=VALUE 的入参**字面注入**且优先于父进程同名值；
//   - 其余一律不透传。
//
// 协议三件必须走**字面注入**：它们是父子之间的握手参数（子进程读不到就退化成
// 「这是一次普通启动」或直接报 fd 非法），不能指望父进程环境里恰好有。
//
// 出口声明仍然成立：代理变量在基础集内，spec.Env 之后仍按声明覆盖它们，
// 而 Spec.Verify 判的就是最终这一份。
func childBaseEnv() []string {
	return procx.ChildEnv([]string{
		childModeEnv + "=1",
		childCallReqFDEnv + "=3",
		childCallRespFDEnv + "=4",
	})
}

// closeAll 关掉全部管道端（失败路径上用）。
func (p *childPipes) closeAll() {
	closeAll(
		p.startReadR, p.startReadW,
		p.startWriteR, p.startWriteW,
		p.callReqR, p.callReqW,
		p.callRespR, p.callRespW,
	)
}
