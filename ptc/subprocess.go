// 子进程执行后端：把程序放到**宿主进程之外**跑，用内核兜住资源。
//
// # 它解决什么
//
// 进程内 goja VM（见 ptc.go）跑在宿主进程里，而三闸（墙钟 / 调用数 / 输出字节）
// **没有内存闸**——Go 运行时没有「只给一个 VM 限内存」的开关，goja 公开 API
// 也没有内存限制（只有 SetMaxCallStackSize，那是栈深）。故一个快速分配的程序
// 能在 deadline 到点**之前**把宿主进程撑爆，而宿主里还跑着编排引擎、事件总线、
// 别的环节。
//
// 本后端把执行搬出宿主进程：子进程里跑 goja，用**内核的地址空间上限**
// （RLIMIT_AS）兑现内存上限。程序被内核杀掉时，父侧从管道 EOF 认出
// 「执行域异常终止」——不是「程序返回空」。
//
// # 两条通道 × 四个单向管道
//
//	启动通道：父发 Start → 子；子回 Result → 父
//	调用通道：子发 Request → 父；父回 Response → 子
//
// **两条通道必须是两条连接**（见 guardx/wire 的包注释）。又因为
// `os.Pipe` 是**单向**的，故每条通道用两条单向管道——共 4 条。
// 用 socketpair 能省成 2 条，但那是 Unix 专有；`os.Pipe` 跨平台。
//
// 文件描述符分配：
//
//	子进程：fd0=启动读  fd1=启动写  fd3=调用请求写  fd4=调用响应读
//	父进程：启动写 / 启动读 / 调用请求读 / 调用响应写
//
// # 判定环留在父侧
//
// 程序里的 host.<tool>() 与普通工具调用走**同一套** invoke.Stage 判定环——
// 而判定环与宿主工具都在父侧。子进程里只有一个「发请求」的桩，判定在父侧做。
// 故本后端**不弱化**门禁：子进程形态下判定环照走。
package ptc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oagentkit/guardx/wire"
	"github.com/oekit/concurrency/async"
)

// SubprocessProviderID 子进程后端的标识（登记在 policy 的 sandbox_providers 段）。
const SubprocessProviderID = "ptc-subprocess"

// SubprocessOptions 子进程后端的配置。
type SubprocessOptions struct {
	// Program 被执行的程序体（由子进程里的 goja 解释）。
	Program string
	// Bindings 宿主工具绑定。**实现留在父侧**——子进程只拿到名字与调用形态。
	Bindings []Binding
	// Stages 每次绑定调用前必过的判定环（与普通工具调用同一套）。
	Stages []invoke.Stage
	// Timeout 墙钟期限。<=0 用 DefaultTimeout。
	Timeout time.Duration
	// MaxCalls 调用数上限。<=0 用 DefaultMaxCalls。
	MaxCalls int
	// MaxBytes 输出字节上限。<=0 用 DefaultMaxBytes。
	MaxBytes int
	// MaxMemory 内存上限（字节）。<=0 = 不要求。
	MaxMemory int64
	// Discover 可选：程序内工具发现入口。
	Discover Discoverer
	// ExePath 子进程可执行文件路径。空 = 用宿主自己的可执行文件（re-exec）。
	//
	// **re-exec 宿主自己的二进制**是默认：随包发小二进制要宿主额外安装，
	// 接入成本高一个量级。代价是宿主要在 main 里加一行 `ptc.MaybeRunChild()`。
	ExePath string
}

// SubprocessProvider 子进程执行后端，实现 sandbox.Provider。
//
// 档位仍是 **LevelProcess**：能力面没变（还是 JS 子集 + 绑定白名单），
// 变的是**资源面**——子进程 + 内核上限比进程内强。这正是 Capable 存在的理由：
// 档位（能力面）与支持面（资源兜底兑现哪几项）是**两个正交维度**。
type SubprocessProvider struct {
	opts SubprocessOptions
}

// NewSubprocessProvider 构造子进程后端。
func NewSubprocessProvider(opts SubprocessOptions) *SubprocessProvider {
	return &SubprocessProvider{opts: opts}
}

// ID 实现 sandbox.Provider。
func (p *SubprocessProvider) ID() string { return SubprocessProviderID }

// Level 实现 sandbox.Provider。
//
// **LevelProcess 而不是 LevelOS**：本后端没有内核级的文件/网络裁剪——
// 能力面与进程内解释器同级，强的只是资源兜底那一维。标成 LevelOS 就是让
// 档位表说谎，而档位表是宿主做风险判断的依据。
func (p *SubprocessProvider) Level() sandbox.Level { return sandbox.LevelProcess }

// Supports 实现 sandbox.Capable。
//
// 本后端**能兑现内存上限**（子进程 + RLIMIT_AS），故 MaxMemory > 0 时也支持。
// 进程内后端**不实现** Capable（兜不住），两者在登记簿里由此区分。
func (p *SubprocessProvider) Supports(b sandbox.Budget) bool {
	return b.MaxMemory <= 0 || p.opts.MaxMemory > 0
}

// Exec 实现 sandbox.Provider：起子进程、跑一次程序、收结果。
func (p *SubprocessProvider) Exec(ctx context.Context, req sandbox.Request) (sandbox.Outcome, error) {
	res, err := p.run(ctx)
	if err != nil {
		return sandbox.Outcome{}, err
	}
	out := sandbox.Outcome{OK: res.OK, Value: res.Value}
	if !res.OK {
		out.Err = &sandbox.Failure{Kind: res.Kind, Message: res.Error}
	}
	return out, nil
}

// run 起子进程跑一次程序。
func (p *SubprocessProvider) run(ctx context.Context) (wire.Result, error) {
	exe := p.opts.ExePath
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return wire.Result{}, fmt.Errorf("ptc: 取宿主可执行文件路径失败: %w", err)
		}
		exe = self
	}
	timeout := p.opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxCalls := p.opts.MaxCalls
	if maxCalls <= 0 {
		maxCalls = DefaultMaxCalls
	}
	maxBytes := p.opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	// 四条单向管道：启动读/启动写/调用请求写/调用响应读（子进程侧视角）。
	startReadR, startReadW, err := os.Pipe()
	if err != nil {
		return wire.Result{}, fmt.Errorf("ptc: 建启动读管道失败: %w", err)
	}
	startWriteR, startWriteW, err := os.Pipe()
	if err != nil {
		_ = startReadR.Close()
		_ = startReadW.Close()
		return wire.Result{}, fmt.Errorf("ptc: 建启动写管道失败: %w", err)
	}
	callReqR, callReqW, err := os.Pipe()
	if err != nil {
		_ = startReadR.Close()
		_ = startReadW.Close()
		_ = startWriteR.Close()
		_ = startWriteW.Close()
		return wire.Result{}, fmt.Errorf("ptc: 建调用请求管道失败: %w", err)
	}
	callRespR, callRespW, err := os.Pipe()
	if err != nil {
		_ = startReadR.Close()
		_ = startReadW.Close()
		_ = startWriteR.Close()
		_ = startWriteW.Close()
		_ = callReqR.Close()
		_ = callReqW.Close()
		return wire.Result{}, fmt.Errorf("ptc: 建调用响应管道失败: %w", err)
	}

	// 管道端在父子之间的归属（每个 os.Pipe 有一读一写两端）：
	//
	//	startReadR  父写→子读   Start 帧
	//	startWriteR 子写→父读   Result 帧
	//	callReqR   子写→父读   Request 帧（子进程发起）
	//	callRespR  父写→子读   Response 帧
	//
	// 子进程拿四个端中的「读端或写端」各一：stdin/startReadR、stdout/startWriteW、
	// fd3=callReqW、fd4=callRespR。
	child := exec.Command(exe) //nolint:gosec // 子进程执行的是宿主自己的可执行文件（re-exec），路径不来自外部输入
	child.Stdin = startReadR
	child.Stdout = startWriteW
	// fd3 = 调用请求写端（子进程发 Request），fd4 = 调用响应读端（子进程读 Response）。
	child.ExtraFiles = []*os.File{callReqW, callRespR}
	child.Env = append(os.Environ(),
		childModeEnv+"=1",
		childCallReqFDEnv+"=3",
		childCallRespFDEnv+"=4",
		childMemLimitEnv+"="+strconv.FormatInt(p.opts.MaxMemory, 10),
	)

	if err := child.Start(); err != nil {
		closeAll(startReadR, startReadW, startWriteR, startWriteW, callReqR, callReqW, callRespR, callRespW)
		return wire.Result{}, fmt.Errorf("ptc: 起子进程失败: %w", err)
	}
	// 父侧关掉**自己那份**副本——子进程要用的是另一个端（startReadR / startWriteW /
	// callReqW / callRespR 已交给子进程，exec 在 Start 之后会关闭父侧副本）。
	//
	// 父侧继续持有的是：startWriteR（写 Start）、startReadW（读 Result）、
	// callReqR（读 Request）、callRespW（写 Response）。
	closeAll(startReadR, startWriteW, callReqW, callRespR)

	// 调用通道：父侧应答子进程发来的工具调用（判定环 + 绑定执行都在这里）。
	server := wire.NewServer(&pipeRW{r: callReqR, w: callRespW}, p.handler())
	serveDone := make(chan struct{})
	// 走 async.GoSafe 而非裸 go func：本 goroutine 跑的是判定环与宿主绑定
	// （宿主代码可能 panic），裸 goroutine 的 panic **无法被外层 recover 捕获**，
	// 会直接杀掉整个宿主进程——那正是「兜住子进程、却让宿主陪葬」的形状。
	async.GoSafe(func() {
		defer close(serveDone)
		_ = server.Serve(ctx)
	})

	// 启动通道：父侧发 Start（写 startReadW）、等 Result（读 startWriteR）。
	starter := wire.NewStarter(&pipeRW{r: startWriteR, w: startReadW})
	start := wire.Start{
		Program:  p.opts.Program,
		Bindings: startBindings(p.opts.Bindings),
		Budget: wire.StartBudget{
			WallClock: timeout,
			MaxCalls:  maxCalls,
			MaxBytes:  maxBytes,
			MaxMemory: p.opts.MaxMemory,
		},
		ResultID: "r-1",
	}
	res, startErr := starter.Start(ctx, start)

	// 收尾：终止子进程（无论成功失败），并等调用通道的服务循环退出。
	_ = child.Process.Kill()
	_ = child.Wait()
	<-serveDone
	closeAll(startWriteR, startReadW, callReqR, callRespW)

	if startErr != nil {
		// 管道 EOF（子进程被内核杀掉）在这里显现——它是「执行域异常终止」，
		// 不是「程序返回空」。这正是内存兜底生效时的形态。
		return wire.Result{}, startErr
	}
	return res, nil
}

// handler 父侧应答一次工具调用：判定环 → 执行绑定 → 回帧。
//
// 判定环在**父侧**：宿主工具在父侧，判定环也必须在父侧——子进程里只有一个
// 发请求的桩。若判定环放到子进程，「程序里的 host.<tool>() 与普通工具调用
// 走同一套判定环」这条纪律就断了。
func (p *SubprocessProvider) handler() wire.Handler {
	byName := make(map[string]Binding, len(p.opts.Bindings))
	for _, b := range p.opts.Bindings {
		byName[b.Name] = b
	}
	return func(ctx context.Context, req wire.Request) (wire.Response, error) {
		// 判定环：与普通工具调用同一套。
		if serr := invoke.EvaluateStages(ctx, p.opts.Stages, invoke.Call{Name: req.Tool, Args: req.Args}); serr != nil {
			return wire.Response{
				OK:    false,
				Kind:  string(toolfail.ClassPermissionDenied),
				Error: serr.Error(),
			}, nil
		}
		b, ok := byName[req.Tool]
		if !ok {
			return wire.Response{
				OK:    false,
				Kind:  string(toolfail.ClassNotFound),
				Error: fmt.Sprintf("工具 %q 未装配", req.Tool),
			}, nil
		}
		out, err := b.Call(ctx, req.Args)
		if err != nil {
			return wire.Response{
				OK:    false,
				Kind:  string(toolfail.ClassUnknown),
				Error: err.Error(),
			}, nil
		}
		return wire.Response{OK: true, Value: out}, nil
	}
}

// startBindings 把绑定清单转成协议形态：**只给名字与调用形态，不给实现**。
//
// 实现留在父侧——这正是跨进程的意义：宿主工具只在宿主进程里存在。
func startBindings(bindings []Binding) []wire.StartBinding {
	out := make([]wire.StartBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, wire.StartBinding{Name: b.Name, Signature: b.Signature})
	}
	return out
}

// pipeRW 把两个方向的文件合成一个 io.ReadWriter。
type pipeRW struct {
	r interface{ Read([]byte) (int, error) }
	w interface{ Write([]byte) (int, error) }
}

func (p *pipeRW) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeRW) Write(b []byte) (int, error) { return p.w.Write(b) }

// closeAll 关掉一批文件（忽略错误——收尾路径上关不掉也不影响正确性）。
func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
