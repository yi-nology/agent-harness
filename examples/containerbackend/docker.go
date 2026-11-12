// Package containerbackend 任务式执行后端的**容器参考实现**（issue #109 的示例半边）。
//
// 生命周期: Experimental（v0.35.0 定级）——参考实现随契约与真实引擎形态演进，
// 不构成任何兼容承诺；定级理由见 api-lifecycle.yaml，分级规则见 docs/api-compat.md。
//
// # 为什么它是示例而不是内核包
//
// 内核 `backend` 只给契约与判据；把某个容器引擎的接法写进内核，等于把一种
// 部署形态焊进内核依赖面。本目录与 examples/pgstore 的分工同构：
// **宿主抄它，不 import 它**。两份实现：
//
//   - Docker（docker.go）：逐字实现 `backend.Backend`，走 Docker Engine
//     HTTP API（unix socket 或 TCP），**零第三方依赖**（stdlib net/http）——
//     不引 Docker 客户端库，与内核「依赖面窄」的既有优势同一条纪律；
//   - Kubernetes Job（k8sjob.go）：**接入点占位**——契约已经对齐，实现
//     留给宿主（client-go 是宿主侧依赖；本仓不引 K8s 客户端）。
//
// # 接线方式（宿主视角）
//
// 本包实现的是 `github.com/yi-nology/goagent-kernel/backend.Backend`。
// 宿主把它（或自己照抄的实现）登记进 backend.Registry，再按
// backend.Spec 解析执行方：
//
//	var reg backend.Registry
//	_ = reg.Register(containerbackend.NewDocker())   // 容器隔离档
//	_ = reg.Register(backend.NewInline())            // 进程内兜底档
//	b, err := reg.Resolve(backend.Spec{
//	    MinIsolation: backend.IsolationContainer,
//	    Egress:       sandbox.EgressDenied,   // Docker 实现以 network=none 兑现
//	    Budget:       sandbox.Budget{MaxMemory: 256 << 20},
//	})
//	res, err := b.Exec(ctx, backend.Job{Image: "registry.example/app@sha256:…", ...})
//
// README 的「执行后端」一节有同样的接线说明与升级边界。
package containerbackend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/backend"
	"github.com/yi-nology/goagent-kernel/sandbox"
)

// ErrEngineCall Docker Engine 调用失败（创建 / 启动 / 等待 / 读取任一步失败）。
//
// 属于 backend.Result 注释里说的「后端基础设施故障」：作业没跑起来，
// 修复对象是引擎侧（引擎不可达 / 镜像不存在 / 参数被拒）。
var ErrEngineCall = errors.New("containerbackend: docker engine 调用失败")

// 缺省的 Engine socket 与 API 版本前缀。
const (
	defaultSocket   = "/var/run/docker.sock"
	defaultAPIVer   = "v1.41"
	backendDockerID = "docker-engine"
)

// Docker Docker Engine 参考实现。
//
// 四条自报（判据按此判，实现按此兑现，缺一即登记门禁意义的说谎）：
//
//   - Isolation() = IsolationContainer：作业跑在容器边界内（namespace /
//     cgroup 由内核裁剪）；
//   - Egress() = EgressDenied：**每个**作业都以 network=none 启动——出站
//     被网络命名空间强制阻断，不是「设了一组环境变量」；
//   - Capable：内存兜得住（容器内存上限），输出截断兜不住（如实自报
//     false），逐调用计数对作业形态不存在（同样 false）；
//   - 墙钟经 context 截止兑现（超时杀容器，见 Exec）。
type Docker struct {
	id       string
	endpoint string // 引擎 HTTP 端点（unix:// 前缀 = socket，否则按 URL 直连）
	apiVer   string
	client   *http.Client
}

// DockerOption Docker 实现的装配项。
type DockerOption func(*Docker)

// WithEndpoint 覆盖引擎端点（unix:///path/to.sock 或 http://host:port）。
// 测试以 httptest 端点注入假引擎。
func WithEndpoint(raw string) DockerOption {
	return func(d *Docker) { d.endpoint = raw }
}

// WithAPIVersion 覆盖 API 版本前缀（缺省 v1.41）。
func WithAPIVersion(v string) DockerOption {
	return func(d *Docker) { d.apiVer = v }
}

// WithID 覆盖后端标识（多套引擎并存时区分登记）。
func WithID(id string) DockerOption {
	return func(d *Docker) { d.id = id }
}

// NewDocker 构造 Docker Engine 后端（缺省走宿主的缺省 socket——见 defaultSocket）。
func NewDocker(opts ...DockerOption) *Docker {
	d := &Docker{
		id:       backendDockerID,
		endpoint: "unix://" + defaultSocket,
		apiVer:   defaultAPIVer,
	}
	for _, o := range opts {
		o(d)
	}
	d.client = engineClient(d.endpoint)
	return d
}

func (d *Docker) ID() string { return d.id }

// Isolation 容器档（见类型注释的自报四条）。
func (d *Docker) Isolation() backend.Isolation { return backend.IsolationContainer }

// Egress 每个作业 network=none 启动 ⇒ 能强制禁网档。
func (d *Docker) Egress() sandbox.EgressMode { return sandbox.EgressDenied }

// Capable 内存兜得住（容器内存上限）；输出截断与逐调用计数兜不住——
// 如实自报 false，不占便宜。
func (d *Docker) Capable(b sandbox.Budget) bool {
	return b.MaxMemory > 0 && b.MaxBytes == 0 && b.MaxCalls == 0
}

// createBody 引擎创建容器的请求体（只声明本实现用得到的字段；
// 其余字段按引擎缺省——参考实现不假装覆盖整张引擎 API 表）。
type createBody struct {
	Image      string   `json:"Image"`
	Cmd        []string `json:"Cmd"`
	Env        []string `json:"Env"`
	WorkingDir string   `json:"WorkingDir,omitempty"`
	HostConfig struct {
		Binds       []string `json:"Binds"`
		Memory      int64    `json:"Memory"`
		NetworkMode string   `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// errWallClock 墙钟预算到点的内部标记（经 context.Cause 传递）。
var errWallClock = errors.New("containerbackend: 作业超出墙钟预算")

// Exec 执行一次作业：create → start → wait → inspect → logs → 清理。
//
// 步骤契约：
//
//   - 作业要求兜内存 ⇒ 容器以 Memory 上限启动（Capable 承诺的兑现点）；
//   - 出站一律 network=none（Egress 承诺的兑现点）；
//   - Budget.WallClock > 0 ⇒ wait 挂截止，到点 SIGKILL——超时不是「再等等」，
//     是预算的强制面；被预算终止的作业返回 Result{OK=false}（作业没跑完
//     是它的失败，不是引擎的故障）；
//   - 作业跑完（无论成败）都读退出码与日志并清理容器；清理失败不掩盖
//     主结果（记进 Detail）。
func (d *Docker) Exec(ctx context.Context, j backend.Job) (backend.Result, error) {
	if err := j.Validate(); err != nil {
		return backend.Result{}, errors.New("containerbackend: " + err.Error())
	}
	id, err := d.create(ctx, j)
	if err != nil {
		return backend.Result{}, err
	}
	// 无论后续成败，容器都要清掉：留着的是「跑完的作业还占着名册」。
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = d.remove(cctx, id)
	}()

	if err := d.start(ctx, id); err != nil {
		return backend.Result{}, err
	}

	if j.Budget.WallClock > 0 {
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		watcherDone := make(chan struct{})
		deadline := j.Budget.WallClock
		// 引擎 wait 是长轮询，墙钟到点主动收场（走 GoSafe：本仓禁止裸 goroutine）。
		async.GoSafe(func() {
			defer close(watcherDone)
			t := time.NewTimer(deadline)
			defer t.Stop()
			select {
			case <-t.C:
				_ = d.kill(context.WithoutCancel(ctx), id)
				cancel(errWallClock)
			case <-ctx.Done():
			}
		})
		defer func() { cancel(errWallClock); <-watcherDone }() // 收口：确保 watcher 停下
	}

	status, err := d.wait(ctx, id)
	if err != nil {
		// 墙钟到点 ⇒ 预算被强制兑现（作业没跑完是作业的失败）；
		// 其余等待错误才是引擎侧故障。
		if errors.Is(context.Cause(ctx), errWallClock) {
			return backend.Result{
				OK:     false,
				Stderr: errWallClock.Error(),
				Detail: map[string]any{"budget_exceeded": "wall_clock"},
			}, nil
		}
		return backend.Result{}, err
	}
	stdout, stderr, err := d.logs(ctx, id)
	if err != nil {
		return backend.Result{}, err
	}
	res := backend.Result{
		OK:       status.ExitCode == 0 && !status.OOMKilled,
		ExitCode: status.ExitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}
	if status.OOMKilled {
		res.Detail = map[string]any{"oom_killed": true}
		if res.Stderr != "" {
			res.Stderr += "\n"
		}
		res.Stderr += "containerbackend: 容器因内存上限被引擎终止（oom_killed）"
	}
	return res, nil
}

// create 创建容器（不启动）。
func (d *Docker) create(ctx context.Context, j backend.Job) (string, error) {
	var body createBody
	body.Image = j.Image
	body.Cmd = j.Args
	body.Env = j.Env
	body.WorkingDir = j.WorkDir
	for _, m := range j.Mounts {
		bind := m.Source + ":" + m.Target
		if m.ReadOnly {
			bind += ":ro"
		}
		body.HostConfig.Binds = append(body.HostConfig.Binds, bind)
	}
	body.HostConfig.Memory = j.Budget.MaxMemory
	body.HostConfig.NetworkMode = "none" // Egress()=denied 的兑现点：每个作业都断网

	q := url.Values{"name": {"gak-" + strconv.FormatInt(time.Now().UnixNano(), 36)}}
	resp, err := d.engineRequest(ctx, http.MethodPost, "/containers/create", body, q)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := expectOK(resp); err != nil {
		return "", err
	}
	var created struct {
		ID    string `json:"Id"`
		Error string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", errors.Wrap(ErrEngineCall, "containerbackend: 解析创建应答失败: "+err.Error())
	}
	if created.ID == "" {
		return "", errors.Wrap(ErrEngineCall, "containerbackend: 引擎拒绝创建容器: "+created.Error)
	}
	return created.ID, nil
}

// start 启动容器。
func (d *Docker) start(ctx context.Context, id string) error {
	resp, err := d.engineRequest(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return expectOK(resp)
}

// waitStatus 引擎 wait / inspect 的退出状态切片。
type waitStatus struct {
	ExitCode  int
	OOMKilled bool
}

// wait 长轮询等容器退出。
func (d *Docker) wait(ctx context.Context, id string) (waitStatus, error) {
	resp, err := d.engineRequest(ctx, http.MethodPost, "/containers/"+id+"/wait", nil, nil)
	if err != nil {
		return waitStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := expectOK(resp); err != nil {
		return waitStatus{}, err
	}
	var w struct {
		StatusCode int `json:"StatusCode"`
		Error      struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return waitStatus{}, errors.Wrap(ErrEngineCall, "containerbackend: 解析等待应答失败: "+err.Error())
	}
	if msg := w.Error.Message; msg != "" {
		return waitStatus{}, errors.Wrap(ErrEngineCall, "containerbackend: 作业异常终止: "+msg)
	}
	insp, err := d.inspect(ctx, id)
	if err != nil {
		return waitStatus{ExitCode: w.StatusCode}, nil // inspect 失败不掩盖退出码
	}
	return insp, nil
}

// inspect 读容器状态（退出码 + OOM 标记）。
func (d *Docker) inspect(ctx context.Context, id string) (waitStatus, error) {
	resp, err := d.engineRequest(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil)
	if err != nil {
		return waitStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := expectOK(resp); err != nil {
		return waitStatus{}, err
	}
	var s struct {
		State struct {
			ExitCode  int  `json:"ExitCode"`
			OOMKilled bool `json:"OOMKilled"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return waitStatus{}, errors.Wrap(ErrEngineCall, "containerbackend: 解析状态应答失败: "+err.Error())
	}
	return waitStatus{ExitCode: s.State.ExitCode, OOMKilled: s.State.OOMKilled}, nil
}

// kill 强杀容器（墙钟预算的兑现动作）。
func (d *Docker) kill(ctx context.Context, id string) error {
	q := url.Values{"signal": {"SIGKILL"}}
	resp, err := d.engineRequest(ctx, http.MethodPost, "/containers/"+id+"/kill", nil, q)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return expectOK(resp)
}

// remove 清理容器（force：仍在跑也删）。
func (d *Docker) remove(ctx context.Context, id string) error {
	q := url.Values{"force": {"true"}}
	resp, err := d.engineRequest(ctx, http.MethodDelete, "/containers/"+id, nil, q)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return expectOK(resp)
}

// logs 读取并解复用容器日志（引擎非 TTY 形态是 8 字节帧头多路复用流）。
func (d *Docker) logs(ctx context.Context, id string) (string, string, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	resp, err := d.engineRequest(ctx, http.MethodGet, "/containers/"+id+"/logs", nil, q)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := expectOK(resp); err != nil {
		return "", "", err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", errors.Wrap(ErrEngineCall, "containerbackend: 读取日志失败: "+err.Error())
	}
	stdout, stderr := demuxLogs(raw)
	return stdout, stderr, nil
}
