package ptc

// 子进程侧：收 Start → 跑程序 → 回 Result。
//
// # 宿主的接入成本：一行
//
// 宿主在自己的 main 开头调一次 `ptc.MaybeRunChild()`：
//
//	func main() {
//	    ptc.MaybeRunChild() // 若本进程是被 re-exec 出来的子进程，这里不返回
//	    …正常启动…
//	}
//
// 命中子进程模式时它跑完子进程主循环并 `os.Exit`——**宿主的其余启动逻辑不执行**。
// 故这一行必须放在最前面。
//
// # 为什么要 re-exec 宿主自己的二进制
//
// 备选是「随包发一个小二进制」，代价是宿主**要额外安装它**——接入成本高一个量级，
// 且要处理跨平台分发与版本对齐。re-exec 自己的二进制只需一行调用。
//
// 代价是宿主必须知道「我可能被 re-exec 成子进程」——这正是 `MaybeRunChild` 存在的
// 理由：把这件事收进一个函数，宿主不需要理解子进程协议。

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/oagentkit/guardx/sandbox/wire"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oekit/pkg/errors"
)

// 子进程模式的环境变量（内部约定，宿主不需要知道）。
const (
	// childModeEnv 命中即表示本进程是子进程。
	childModeEnv = "AGENT_HARNESS_PTC_CHILD"
	// childCallReqFDEnv 调用请求管道写端的 fd 号。
	childCallReqFDEnv = "AGENT_HARNESS_PTC_CALL_REQ_FD"
	// childCallRespFDEnv 调用响应管道读端的 fd 号。
	childCallRespFDEnv = "AGENT_HARNESS_PTC_CALL_RESP_FD"
	// childMemLimitEnv 父侧要求的内存上限（字节）。0 = 不要求。
	childMemLimitEnv = "AGENT_HARNESS_PTC_MEM_LIMIT"
)

// MaybeRunChild 若本进程是执行沙箱的子进程，则跑完子进程主循环并退出；
// 否则直接返回，宿主继续正常启动。
//
// **必须放在宿主 main 的最前面**：命中时它会 os.Exit，宿主其余启动逻辑不执行。
func MaybeRunChild() {
	if os.Getenv(childModeEnv) != "1" {
		return
	}
	_ = runChild()
	os.Exit(0)
}

// runChild 子进程主循环：收 Start、跑程序、回 Result。
func runChild() error {
	fmt.Fprintf(os.Stderr, "CHILD-DIAG mode=%q reqfd=%q respfd=%q stdin=%v\n",
		os.Getenv(childModeEnv), os.Getenv(childCallReqFDEnv), os.Getenv(childCallRespFDEnv), os.Stdin != nil)
	reqFD, err := strconv.Atoi(os.Getenv(childCallReqFDEnv))
	if err != nil {
		return fmt.Errorf("ptc: 子进程调用请求 fd 无效: %w", err)
	}
	respFD, err := strconv.Atoi(os.Getenv(childCallRespFDEnv))
	if err != nil {
		return fmt.Errorf("ptc: 子进程调用响应 fd 无效: %w", err)
	}
	callW := os.NewFile(uintptr(reqFD), "ptc-call-req")
	callR := os.NewFile(uintptr(respFD), "ptc-call-resp")
	if callW == nil || callR == nil {
		return errors.New("ptc: 子进程调用管道 fd 无效")
	}
	// 内存上限：父侧要求多少，这里就设多少。**设不上就拒绝执行**——
	// 「以为兜住了、实际没有」正是这套机制要消灭的东西。
	memLimit, err := strconv.ParseInt(os.Getenv(childMemLimitEnv), 10, 64)
	if err != nil {
		return fmt.Errorf("ptc: 子进程内存上限参数无效: %w", err)
	}
	if err := applyMemoryLimit(memLimit); err != nil {
		return err
	}

	// 启动通道：stdin 读 Start、stdout 回 Result。
	runner := wire.NewRunner(&stdioRW{in: os.Stdin, out: os.Stdout})
	start, err := runner.AwaitStart(context.Background())
	if err != nil {
		return fmt.Errorf("ptc: 子进程等启动帧失败: %w", err)
	}
	// 调用通道：子进程是**发起方**（程序里的 host.<tool>() 由程序发起）。
	client := wire.NewClient(&pipeRW{r: callR, w: callW})
	remote := remoteCaller(client)

	res := runProgramInChild(start, remote)
	if err := runner.SendResult(res); err != nil {
		return err
	}
	return nil
}

// remoteCaller 子进程侧的绑定调用面：把调用**请求**发给父侧执行。
//
// 父侧那边才是宿主工具与判定环的所在地——子进程里只有这一个桩。
func remoteCaller(client *wire.Client) bindingCaller {
	return func(ctx context.Context, name, argsJSON string) (string, error) {
		resp, err := client.Call(ctx, name, argsJSON)
		if err != nil {
			return "", err
		}
		if !resp.OK {
			// 父侧已把失败归类（toolfail 的分类）——这里原样带回去，
			// 让子进程侧的收口与父侧对失败的理解一致。
			kind := resp.Kind
			if kind == "" {
				kind = string(toolfail.ClassUnknown)
			}
			return "", &remoteFailure{kind: kind, msg: resp.Error}
		}
		return resp.Value, nil
	}
}

// remoteFailure 父侧回传的失败（带分类）。
type remoteFailure struct {
	kind string
	msg  string
}

func (e *remoteFailure) Error() string { return e.msg }

// Kind 失败分类（toolfail 的分类值）。
func (e *remoteFailure) Kind() string { return e.kind }

// stdioRW 用 stdin/stdout 组成一个 io.ReadWriter（启动通道：读 Start / 写 Result）。
type stdioRW struct {
	in  interface{ Read([]byte) (int, error) }
	out interface{ Write([]byte) (int, error) }
}

func (s *stdioRW) Read(b []byte) (int, error)  { return s.in.Read(b) }
func (s *stdioRW) Write(b []byte) (int, error) { return s.out.Write(b) }
