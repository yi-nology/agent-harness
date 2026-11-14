package ptc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oagentkit/guardx/wire"
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/toolname"
)

// 本文件验证子进程后端的**纯契约**部分（不真的起进程——端到端在
// subprocess_exec_test.go，那是外部测试包）。

// TestSubprocessProviderBasics 子进程后端的基本契约。
func TestSubprocessProviderBasics(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{Stages: allowStages()})
	if p.ID() != SubprocessProviderID {
		t.Errorf("ID=%q，应为 %q", p.ID(), SubprocessProviderID)
	}
	// 档位仍是 LevelProcess（能力面没变，强的只是资源面）。
	if p.Level() != sandbox.LevelProcess {
		t.Errorf("档位=%v，应为 process", p.Level())
	}
	// 支持面：**没配上限时不声称能兑现**——那是撒谎。
	// （配了上限才声明支持，见下一个用例。）
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限时不应声称兑现")
	}
}

// TestSubprocessProviderWithMemoryConfigured 配了上限**且当前环境兜得住**才声明支持。
//
// 判据是「当前环境兜不兜得住」而不是「配没配上限」：
// 要求了内存上限而当前环境建不出带 memory.max 的 cgroup（macOS 没有 cgroup；
// Linux 容器里没委派 memory 控制器也没有）⇒ **不支持**。
//
// 若这里报支持，登记簿就会选中本后端，然后 Exec 因建不出 cgroup 而失败——
// 宿主等到的却是「选了它却用不了」这种最难排查的形态。
func TestSubprocessProviderWithMemoryConfigured(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{Stages: allowStages(), MaxMemory: 256 << 20})
	// 零要求 → 支持（不要求内存上限就不受兜底能力约束）。
	if !p.Supports(sandbox.Budget{}) {
		t.Error("零要求应视为支持")
	}
	// 配了上限 → 是否支持取决于当前环境兜不兜得住。
	want := memLimitSupported()
	if got := p.Supports(sandbox.Budget{MaxMemory: 1 << 20}); got != want {
		t.Errorf("配了上限时的支持判定=%v，应等于当前环境兜不兜得住=%v", got, want)
	}
}

// TestSubprocessProviderWithoutMemoryConfigured 没配上限却要求兜底 ⇒ 不支持。
//
// 这是「不撒谎」的方向：宿主要内存兜底，而本后端没配 ⇒ 明确说兜不住，
// 让登记簿去选能兜的后端，而不是选一个装样子的。
//
// # 一条环境性事实：本条在「兜不住」的环境上无法区分两个分支
//
// `Supports` 的两个返回 false 的分支是：
//   - **没配上限**（配置问题）——本条要抓的；
//   - **配了上限但当前环境兜不住**（环境问题）。
//
// 在 macOS（无 cgroup）这类兜不住的环境上，两者**返回值相同**（都 false），
// 故「没配上限」这个缺陷在此环境**不可观测**——不是用例没写对，
// 是这两条分支在结果上无法区分。
//
// ⇒ 本条的区分力**依赖 Linux + 可写 cgroup 的环境**（CI 的 ubuntu-24.04）：
// 那里 `memLimitSupported()` 为 true，「没配」与「配了但兜不住」才分得开。
// 反向对照见 TestSubprocessProviderWithMemoryConfigured。
func TestSubprocessProviderWithoutMemoryConfigured(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{Stages: allowStages()})
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限却声称能兑现，是撒谎")
	}
	// 反向对照：配了上限时的判定**只**取决于当前环境兜不兜得住。
	// 在兜得住的环境（Linux + 可写 cgroup）上，这两条分支才分得开。
	p2 := NewSubprocessProvider(SubprocessOptions{Stages: allowStages(), MaxMemory: 256 << 20})
	got := p2.Supports(sandbox.Budget{MaxMemory: 1 << 20})
	if got != memLimitSupported() {
		t.Errorf("配了上限时的判定=%v，应等于当前环境能力=%v", got, memLimitSupported())
	}
}

// TestSubprocessProviderUnsetMemoryNotSupported 没配 MaxMemory 时不声称兑现内存。
//
// 「漏声明得到的是兜不住，不是兜得住」——保守的那一侧。
func TestSubprocessProviderUnsetMemoryNotSupported(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{Stages: allowStages()})
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限时不应声称兑现内存")
	}
	if !p.Supports(sandbox.Budget{}) {
		t.Error("零要求应视为支持")
	}
}

// TestKindForErrorKind 失败分类映射。
func TestKindForErrorKind(t *testing.T) {
	cases := map[string]string{
		"budget_calls":      "rate_limited",
		"budget_bytes":      "rate_limited",
		"syntax":            "permanent",
		"binding_signature": "permanent",
		"bad_result":        "permanent",
		"deadline":          "transient",
		"runtime":           "transient",
		"permission_denied": "permission_denied",
		"":                  "unknown",
		"没见过的":              "unknown",
	}
	for in, want := range cases {
		if got := kindForErrorKind(in); got != want {
			t.Errorf("kindForErrorKind(%q)=%q，应为 %q", in, got, want)
		}
	}
}

// TestRemoteFailureCarriesKind 父侧回传的失败带分类。
func TestRemoteFailureCarriesKind(t *testing.T) {
	f := &remoteFailure{kind: "permission_denied", msg: "没权限"}
	if f.Kind() != "permission_denied" {
		t.Errorf("Kind=%q，应为 permission_denied", f.Kind())
	}
	if f.Error() != "没权限" {
		t.Errorf("错误信息=%q，应为 没权限", f.Error())
	}
}

// TestToolNameNormalizeStable 工具名净化规则（形态与挂载名必须逐字相同）。
func TestToolNameNormalizeStable(t *testing.T) {
	if toolname.Normalize("get-logs") != "get_logs" {
		t.Error("工具名净化规则变了")
	}
}

// TestSubprocessHandlerClassifiesStageOutcomes 父侧应答的判定环归因与进程内
// 路径同一份（issue #65）。
//
// 旧实现把一切 EvaluateStages 错误（含守卫 panic/故障）都映射成
// permission_denied——「宿主门禁坏了」被上报为「策略否定」，排障方向被引向
// 守卫配置；#59 在进程内路径修掉的误归因在子进程路径复发。判据与
// TestStagesFaultAndDenyGetDistinctKinds 同一套：拒绝/故障两个词，加一个
// 「非 DenyError 不得冠判定环词汇」的防御分支。
func TestSubprocessHandlerClassifiesStageOutcomes(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		return invoke.Verdict{Allow: false, Reason: "变更须走方案审批"}, nil
	}}
	crasher := invoke.StageFunc{StageName: "crasher", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		panic("环里炸了")
	}}
	cases := []struct {
		name       string
		ring       invoke.Stage
		wantKind   string
		wantPrefix string
	}{
		{"判定拒绝", deny, "stage_denied", "判定环拒绝"},
		{"守卫崩溃", crasher, "stage_failed", "判定环故障"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSubprocessProvider(SubprocessOptions{Stages: []invoke.Stage{tc.ring}})
			resp, err := p.handler()(context.Background(), wire.Request{Tool: "t", Args: "{}"})
			if err != nil {
				t.Fatalf("handler 不应返回 error（失败是 Response 字段）: %v", err)
			}
			if resp.OK {
				t.Fatal("被拦下的调用不得 OK")
			}
			if resp.Kind != tc.wantKind {
				t.Fatalf("Kind=%q，应为 %q（子进程侧收口对它逐字采信，两个后端必须同词）", resp.Kind, tc.wantKind)
			}
			if !strings.HasPrefix(resp.Error, tc.wantPrefix) {
				t.Fatalf("Error 前缀应为 %q，实得 %q", tc.wantPrefix, resp.Error)
			}
			if tc.wantKind == "stage_failed" && strings.Contains(resp.Error, "permission") {
				t.Fatalf("守卫故障不得冠权限类词汇: %q", resp.Error)
			}
		})
	}
}

// TestSubprocessHandlerNonDenyErrorNotPermissionDenied 非 DenyError 的判定环
// 错误不得冠 permission_denied（issue #65 的防御分支）：EvaluateStages 的公开
// 契约是归口为 *invoke.DenyError，真出现别的错误只能按 unknown 走——
// 「权限否定」是可被宿主拿去做免重试决策的词，不能当兜底桶。
func TestSubprocessHandlerNonDenyErrorNotPermissionDenied(t *testing.T) {
	// 构造一个返回非 DenyError 的环：正常配置下 EvaluateStages 会归口，
	// 这里直接用一个既不 deny 也不 panic 但返回普通 error 的 StageFunc——
	// 若归口层把它包成 DenyError(stage_failed) 也正确，本用例退化为验证
	// stage_failed 路径；关键是任何情况下 Kind 都不得是 permission_denied
	// 除非它真的是判定拒绝。
	flaky := invoke.StageFunc{StageName: "flaky", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		return invoke.Verdict{Allow: true}, errors.New("判定源不可用")
	}}
	p := NewSubprocessProvider(SubprocessOptions{Stages: []invoke.Stage{flaky}})
	resp, err := p.handler()(context.Background(), wire.Request{Tool: "t", Args: "{}"})
	if err != nil {
		t.Fatalf("handler 不应返回 error: %v", err)
	}
	if resp.OK {
		t.Fatal("环报错必须 fail-closed 拒绝，不得放行")
	}
	if resp.Kind == string(toolfail.ClassPermissionDenied) {
		t.Fatalf("守卫自身故障不得上报为 permission_denied: Kind=%q Error=%q", resp.Kind, resp.Error)
	}
}

// TestSubprocessHandlerBindingFailureCarriesClass 绑定执行失败带真实分类回
// 子进程（issue #150）：此前父侧 handler 对 b.Call 的错误硬编码 unknown——
// 分类跨进程丢失，同一失败进程内报 permission_denied/rate_limited/…，
// 子进程形态一律 unknown，与进程内路径（binding.go 的 thrownRejection 走
// rejectionKindOf）分叉。判据：handler 的 wire.Response.Kind 与进程内
// rejectionKindOf(err) 同值。
func TestSubprocessHandlerBindingFailureCarriesClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"规范失败类型", toolfail.New(toolfail.ClassPermissionDenied, "", "宿主拒绝")},
		{"规范失败类型经 %w 包装", fmt.Errorf("绑定执行: %w", toolfail.New(toolfail.ClassRateLimited, "", "限流"))},
		{"宿主自定义 Kind() 接口", &customKindError{kind: "transient"}},
		{"无分类的普通错误", errors.New("绑定炸了")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewSubprocessProvider(SubprocessOptions{
				Bindings: []Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
					return "", tc.err
				}}},
			})
			resp, herr := p.handler()(context.Background(), wire.Request{Tool: "t", Args: "{}"})
			if herr != nil {
				t.Fatalf("handler 不应返回 error: %v", herr)
			}
			if resp.OK {
				t.Fatal("绑定失败必须 fail-closed 拒绝")
			}
			// 与进程内路径同一份判据：分类相等地比较，两边用的是同一个函数。
			if want := rejectionKindOf(tc.err); want != "" {
				if resp.Kind != want {
					t.Fatalf("Kind=%q，应与错误自带分类一致 %q", resp.Kind, want)
				}
				return
			}
			if resp.Kind != string(toolfail.ClassUnknown) {
				t.Fatalf("无分类错误应落 unknown，实得 %q", resp.Kind)
			}
		})
	}
}

// TestSubprocessHandlerBindingParityWithInProcess 子进程/进程内两形态对同一
// 绑定失败报同一分类（issue #150 的 parity 判据）：进程内走
// thrownRejection.kind = rejectionKindOf(err)，子进程走父侧 handler 的
// wire.Response.Kind——两处判据若是两份，同一失败两形态就分叉。
func TestSubprocessHandlerBindingParityWithInProcess(t *testing.T) {
	errs := []error{
		toolfail.New(toolfail.ClassPermissionDenied, "", "宿主拒绝"),
		toolfail.New(toolfail.ClassRateLimited, "", "限流"),
		fmt.Errorf("包装: %w", toolfail.New(toolfail.ClassNotFound, "", "没有")),
	}
	for _, berr := range errs {
		// 子进程形态：父侧 handler 的应答分类。
		p := NewSubprocessProvider(SubprocessOptions{
			Bindings: []Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
				return "", berr
			}}},
		})
		resp, herr := p.handler()(context.Background(), wire.Request{Tool: "t", Args: "{}"})
		if herr != nil {
			t.Fatalf("handler 不应返回 error: %v", herr)
		}
		// 进程内形态：同一错误经同一判据探测出的分类。
		if resp.Kind != rejectionKindOf(berr) {
			t.Fatalf("子进程 Kind=%q 与进程内分类 %q 不一致: %v", resp.Kind, rejectionKindOf(berr), berr)
		}
	}
}

// TestSubprocessEffectiveBudget 装配期配置与每请求预算逐项取严（issue #66）。
//
// 同一协议的进程内后端把 req.Budget 全量透传；本后端此前整个不读它——宿主
// 请求更紧预算时静默按装配期宽限跑。判据：每项取 min（请求零值 = 沿用装配），
// 内存同口径（含「请求了而后端没配 ⇒ 按请求」的 fail-closed 方向）。
func TestSubprocessEffectiveBudget(t *testing.T) {
	cases := []struct {
		name       string
		opts       SubprocessOptions
		req        sandbox.Budget
		wantClock  time.Duration
		wantCalls  int
		wantBytes  int
		wantMemory int64
	}{
		{
			name:      "请求零值沿用装配期（缺省归一）",
			opts:      SubprocessOptions{Stages: allowStages()},
			req:       sandbox.Budget{},
			wantClock: DefaultTimeout, wantCalls: DefaultMaxCalls, wantBytes: DefaultMaxBytes,
		},
		{
			name:      "请求更紧的墙钟被采纳",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: 30 * time.Second, MaxCalls: 50, MaxBytes: 4096},
			req:       sandbox.Budget{WallClock: time.Second},
			wantClock: time.Second, wantCalls: 50, wantBytes: 4096,
		},
		{
			name:      "请求更紧的调用数与字节被采纳",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: 30 * time.Second, MaxCalls: 50, MaxBytes: 4096},
			req:       sandbox.Budget{MaxCalls: 2, MaxBytes: 128},
			wantClock: 30 * time.Second, wantCalls: 2, wantBytes: 128,
		},
		{
			name:      "请求更宽不放大（安全方向）",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: time.Second, MaxCalls: 2, MaxBytes: 128},
			req:       sandbox.Budget{WallClock: time.Hour, MaxCalls: 9999, MaxBytes: 1 << 20},
			wantClock: time.Second, wantCalls: 2, wantBytes: 128,
		},
		{
			name:      "内存取严：请求更紧被采纳",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: time.Second, MaxCalls: 5, MaxBytes: 512, MaxMemory: 256 << 20},
			req:       sandbox.Budget{MaxMemory: 64 << 20},
			wantClock: time.Second, wantCalls: 5, wantBytes: 512,
			wantMemory: 64 << 20,
		},
		{
			name:      "内存取严：请求更宽不放大",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: time.Second, MaxCalls: 5, MaxBytes: 512, MaxMemory: 64 << 20},
			req:       sandbox.Budget{MaxMemory: 1 << 30},
			wantClock: time.Second, wantCalls: 5, wantBytes: 512,
			wantMemory: 64 << 20,
		},
		{
			name:      "内存：后端没配而请求了 ⇒ 按请求（不退回不设上限）",
			opts:      SubprocessOptions{Stages: allowStages(), Timeout: time.Second, MaxCalls: 5, MaxBytes: 512},
			req:       sandbox.Budget{MaxMemory: 32 << 20},
			wantClock: time.Second, wantCalls: 5, wantBytes: 512,
			wantMemory: 32 << 20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim, maxMemory := NewSubprocessProvider(tc.opts).effectiveBudget(tc.req)
			if lim.timeout != tc.wantClock || lim.maxCalls != tc.wantCalls || lim.maxBytes != tc.wantBytes {
				t.Fatalf("三闸 = (%v,%d,%d)，应为 (%v,%d,%d)",
					lim.timeout, lim.maxCalls, lim.maxBytes, tc.wantClock, tc.wantCalls, tc.wantBytes)
			}
			if maxMemory != tc.wantMemory {
				t.Fatalf("内存上限 = %d，应为 %d", maxMemory, tc.wantMemory)
			}
		})
	}
}
