package containerbackend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	kerrors "github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/backend"
	"github.com/yi-nology/goagent-kernel/sandbox"
)

// fakeEngine 假引擎：记录各步调用与请求体，按脚本应答。
type fakeEngine struct {
	mu sync.Mutex

	createReqs []createBody
	started    int
	killed     int
	removed    int

	// 行为脚本
	createStatus int           // 0 = 正常
	waitDelay    time.Duration // wait 阻塞时长（模拟长作业）
	exitCode     int           // 作业退出码
	oomKilled    bool          // 引擎判定 OOM
	logsRaw      []byte        // 多路复用日志流
}

func (f *fakeEngine) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1.41/containers/create", func(w http.ResponseWriter, r *http.Request) {
		var body createBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.createReqs = append(f.createReqs, body)
		st := f.createStatus
		f.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"message":"rejected"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"cid-1"}`))
	})
	mux.HandleFunc("POST /v1.41/containers/cid-1/start", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.started++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1.41/containers/cid-1/wait", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		delay := f.waitDelay
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return // 客户端先放弃（墙钟到点）——引擎侧不回包
			}
		}
		_, _ = w.Write([]byte(`{"StatusCode":1,"Error":{"Message":""}}`))
	})
	mux.HandleFunc("POST /v1.41/containers/cid-1/kill", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.killed++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /v1.41/containers/cid-1", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.removed++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1.41/containers/cid-1/json", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		code, oom := f.exitCode, f.oomKilled
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"State":{"ExitCode":` + intStr(code) + `,"OOMKilled":` + boolStr(oom) + `}}`))
	})
	mux.HandleFunc("GET /v1.41/containers/cid-1/logs", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.logsRaw)
	})
	return mux
}

func intStr(n int) string   { b, _ := json.Marshal(n); return string(b) }
func boolStr(b bool) string { s, _ := json.Marshal(b); return string(s) }

func newFake(t *testing.T, f *fakeEngine) *Docker {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewDocker(WithEndpoint(srv.URL))
}

func TestDockerDeclarations(t *testing.T) {
	d := NewDocker()
	if d.ID() != backendDockerID || d.Isolation() != backend.IsolationContainer {
		t.Fatalf("自报错误：id=%s iso=%s", d.ID(), d.Isolation())
	}
	if d.Egress() != sandbox.EgressDenied {
		t.Fatal("network=none 形态必须自报禁网档")
	}
	cases := []struct {
		name string
		b    sandbox.Budget
		want bool
	}{
		{"只要求内存：兜得住", sandbox.Budget{MaxMemory: 1 << 20}, true},
		{"要求输出截断：如实报兜不住", sandbox.Budget{MaxMemory: 1 << 20, MaxBytes: 100}, false},
		{"要求逐调用计数：作业形态兜不住", sandbox.Budget{MaxMemory: 1 << 20, MaxCalls: 10}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Capable(tc.b); got != tc.want {
				t.Fatalf("Capable(%+v)=%v，期望 %v", tc.b, got, tc.want)
			}
		})
	}
	t.Run("判据联动：禁网 + 内存要求由它满足", func(t *testing.T) {
		spec := backend.Spec{MinIsolation: backend.IsolationContainer,
			Egress: sandbox.EgressDenied, Budget: sandbox.Budget{MaxMemory: 1 << 20}}
		if err := spec.SatisfiedBy(d); err != nil {
			t.Fatalf("期望满足，得到 %v", err)
		}
	})
}

func TestDockerExecHappyPath(t *testing.T) {
	f := &fakeEngine{exitCode: 0}
	d := newFake(t, f)

	// 多路复用日志：帧 1 stdout "out1\n"，帧 2 stderr "err1\n"，帧 3 stdout "out2"
	f.logsRaw = frame(1, "out1\n")
	f.logsRaw = append(f.logsRaw, frame(2, "err1\n")...)
	f.logsRaw = append(f.logsRaw, frame(1, "out2")...)

	res, err := d.Exec(context.Background(), backend.Job{
		Image:   "registry.example/app@sha256:deadbeef",
		Args:    []string{"--flag", "v"},
		Env:     []string{"K=V"},
		Mounts:  []backend.Mount{{Source: "/src", Target: "/dst", ReadOnly: true}},
		WorkDir: "/w",
		Budget:  sandbox.Budget{MaxMemory: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ExitCode != 0 {
		t.Fatalf("res=%+v", res)
	}
	if res.Stdout != "out1\nout2" || res.Stderr != "err1\n" {
		t.Fatalf("日志解复用错误：stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.createReqs) != 1 {
		t.Fatalf("创建调用 %d 次", len(f.createReqs))
	}
	body := f.createReqs[0]
	if body.Image != "registry.example/app@sha256:deadbeef" {
		t.Fatalf("digest 被改动：%s", body.Image)
	}
	if body.HostConfig.NetworkMode != "none" {
		t.Fatalf("Egress=denied 的兑现点缺失：NetworkMode=%s", body.HostConfig.NetworkMode)
	}
	if body.HostConfig.Memory != 1<<20 {
		t.Fatalf("内存上限未兑现：%d", body.HostConfig.Memory)
	}
	if len(body.HostConfig.Binds) != 1 || !strings.HasSuffix(body.HostConfig.Binds[0], ":ro") {
		t.Fatalf("只读挂载声明丢失：%v", body.HostConfig.Binds)
	}
	if f.started != 1 || f.removed != 1 {
		t.Fatalf("启动/清理次数：start=%d remove=%d", f.started, f.removed)
	}
}

func TestDockerExecFailures(t *testing.T) {
	t.Run("作业非零退出：作业自己的失败（Result 层）", func(t *testing.T) {
		f := &fakeEngine{exitCode: 3}
		d := newFake(t, f)
		res, err := d.Exec(context.Background(), backend.Job{Image: "img"})
		if err != nil {
			t.Fatalf("非零退出不是基础设施故障：%v", err)
		}
		if res.OK || res.ExitCode != 3 {
			t.Fatalf("res=%+v", res)
		}
	})

	t.Run("引擎拒绝创建：基础设施故障（error 层）", func(t *testing.T) {
		f := &fakeEngine{createStatus: http.StatusBadRequest}
		d := newFake(t, f)
		_, err := d.Exec(context.Background(), backend.Job{Image: "img"})
		if !kerrors.Is(err, ErrEngineCall) {
			t.Fatalf("期望 ErrEngineCall，得到 %v", err)
		}
	})

	t.Run("OOM 被引擎标记：不算成功且留明细", func(t *testing.T) {
		f := &fakeEngine{exitCode: 137, oomKilled: true}
		d := newFake(t, f)
		res, err := d.Exec(context.Background(), backend.Job{Image: "img"})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK {
			t.Fatal("OOM 不得算成功")
		}
		if !strings.Contains(res.Stderr, "oom_killed") {
			t.Fatalf("OOM 明细缺失：%q", res.Stderr)
		}
	})

	t.Run("墙钟到点：预算强制收场（Result 层，不是引擎故障）", func(t *testing.T) {
		f := &fakeEngine{waitDelay: 5 * time.Second}
		d := newFake(t, f)
		res, err := d.Exec(context.Background(), backend.Job{
			Image:  "img",
			Budget: sandbox.Budget{WallClock: 50 * time.Millisecond},
		})
		if err != nil {
			t.Fatalf("墙钟超时应记作业失败而非引擎故障：%v", err)
		}
		if res.OK {
			t.Fatal("超时不得算成功")
		}
		if res.Detail == nil {
			t.Fatal("超时明细缺失")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.killed != 1 {
			t.Fatalf("墙钟到点必须 SIGKILL（kill=%d）", f.killed)
		}
	})

	t.Run("不合法作业：先于引擎调用拒绝", func(t *testing.T) {
		f := &fakeEngine{}
		d := newFake(t, f)
		_, err := d.Exec(context.Background(), backend.Job{Image: " "})
		if err == nil || kerrors.Is(err, ErrEngineCall) {
			t.Fatalf("期望作业校验错，得到 %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.createReqs) != 0 {
			t.Fatal("校验失败不应触达引擎")
		}
	})
}

func TestDemuxLogs(t *testing.T) {
	t.Run("多路复用帧拆分", func(t *testing.T) {
		raw := frame(1, "a\n")
		raw = append(raw, frame(2, "b\n")...)
		stdout, stderr := demuxLogs(raw)
		if stdout != "a\n" || stderr != "b\n" {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("无帧头按 TTY 整段归 stdout", func(t *testing.T) {
		stdout, stderr := demuxLogs([]byte("plain"))
		if stdout != "plain" || stderr != "" {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("半截帧不装作完整", func(t *testing.T) {
		raw := frame(1, "ok")
		raw = append(raw, frame(1, "trunc")[:6]...) // 帧头 + 2 字节，负载声明未兑现
		stdout, _ := demuxLogs(raw)
		if stdout != "ok" {
			t.Fatalf("半截帧被当成完整：%q", stdout)
		}
	})
}

// frame 构造一段引擎多路复用日志帧。
func frame(stream byte, payload string) []byte {
	n := len(payload)
	out := []byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	return append(out, payload...)
}

func TestK8sJobPlaceholder(t *testing.T) {
	k := NewK8sJob()
	if k.ID() != "k8s-job" {
		t.Fatalf("id=%s", k.ID())
	}
	t.Run("未接入前按最弱档自报（fail-closed）", func(t *testing.T) {
		if k.Isolation() != backend.IsolationNone {
			t.Fatalf("占位实现不得自报容器档：%s", k.Isolation())
		}
		spec := backend.Spec{MinIsolation: backend.IsolationContainer}
		if err := spec.SatisfiedBy(k); kerrors.Is(err, backend.ErrNoBackend) || err == nil {
			t.Fatalf("占位不应满足容器要求：err=%v", err)
		}
	})
	t.Run("Exec 响亮拒绝", func(t *testing.T) {
		res, err := k.Exec(context.Background(), backend.Job{Image: "img"})
		if err == nil || !kerrors.Is(err, ErrNotWired) {
			t.Fatalf("期望 ErrNotWired，得到 %v", err)
		}
		if res.OK {
			t.Fatal("占位不得返回成功")
		}
	})
}
