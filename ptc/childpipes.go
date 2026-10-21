package ptc

import (
	"fmt"
	"os"
	"os/exec"
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
func newChildPipes(exe string) (*childPipes, error) {
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
	child.Env = append(os.Environ(),
		childModeEnv+"=1",
		childCallReqFDEnv+"=3",
		childCallRespFDEnv+"=4",
	)
	p.child = child
	return p, nil
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
