package ptc

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/toolgate/invoke"
	"github.com/oekit/pkg/errors"
)

// denyStage 恒拒绝的判定环。
func denyStage(name, reason string, retryable bool) invoke.Stage {
	return invoke.StageFunc{StageName: name, Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		return invoke.Verdict{Allow: false, Reason: reason, Retryable: retryable}, nil
	}}
}

// allowStage 恒放行的判定环。
func allowStage(name string) invoke.Stage {
	return invoke.StageFunc{StageName: name, Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		return invoke.Verdict{Allow: true}, nil
	}}
}

// TestStagesGateCodeModeCalls 判定环必须真正拦下程序里的工具调用。
//
// 这是本条改动的全部理由：此前 host.<tool>() 直达宿主工具，披露门、审计门、
// 出站门一个都不经过——而本包恰恰是对外推荐的那条路。安全路径反而最不安全。
func TestStagesGateCodeModeCalls(t *testing.T) {
	hits := 0
	bind := Binding{Name: "restart_service", Call: func(context.Context, string) (string, error) {
		hits++
		return "已重启", nil
	}}
	const prog = `try { return host.restart_service("h1") } catch (e) { return "已拦下" }`
	r := Run(context.Background(), prog, []Binding{bind}, Options{
		Stages: []invoke.Stage{allowStage("前置"), denyStage("审计门", "变更须走方案审批", false)},
	})

	if !r.OK {
		t.Fatalf("try/catch 后应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"已拦下"` {
		t.Fatalf("程序内的工具调用必须被判定环拦下，实得 %s", r.Value)
	}
	if hits != 0 {
		t.Fatalf("被拒的调用不得触达执行体，实得 %d 次", hits)
	}
}

// TestStagesDenyIsCatchable 与工具报错同语义：程序可 try/catch 后带着已取得的部分继续。
func TestStagesDenyIsCatchable(t *testing.T) {
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}
	r := Run(context.Background(),
		`let a = 1; try { host.t("x") } catch (e) { return a }`,
		[]Binding{bind}, Options{Stages: []invoke.Stage{denyStage("g", "不让", false)}})
	if !r.OK || r.Value != `1` {
		t.Fatalf("被拒后程序应能带着已算出的结果继续，实得 OK=%v value=%s err=%q", r.OK, r.Value, r.Error)
	}
}

// TestStagesDeniedStillCountsAgainstCallBudget 被拒的调用同样计预算。
//
// 程序可以 try/catch 掉这次错误再循环。不计数的话，一个被拒的工具能被无限重试，
// MaxCalls 形同虚设——而 try/catch 恰恰是本包鼓励模型用的手法。
func TestStagesDeniedStillCountsAgainstCallBudget(t *testing.T) {
	hits := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { hits++; return "x", nil }}
	const prog = `for (let i=0;i<99;i++) { try { host.t("x") } catch (e) {} } return 1`
	r := Run(context.Background(), prog, []Binding{bind}, Options{
		MaxCalls: 4,
		Stages:   []invoke.Stage{denyStage("g", "不让", false)},
	})
	if r.OK || r.ErrorKind != "budget_calls" {
		t.Fatalf("被拒的循环调用也应撞上调用次数闸，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
	if hits != 0 {
		t.Fatalf("被拒的调用不得触达执行体，实得 %d 次", hits)
	}
}

// TestStagesRecordDeniedInCallLog 被拒的调用要在 CallLog 里留下 denied 痕。
//
// 审计要能回答「这次程序跑了哪些调用、哪些被拦」，不留痕等于这次调用没发生过。
func TestStagesRecordDeniedInCallLog(t *testing.T) {
	bind := Binding{Name: "danger", Call: func(context.Context, string) (string, error) { return "x", nil }}
	r := Run(context.Background(), `try { host.danger("x") } catch (e) {} return 1`,
		[]Binding{bind}, Options{Stages: []invoke.Stage{denyStage("g", "高危", false)}})

	if len(r.CallLog) != 1 {
		t.Fatalf("被拒的调用应留一条痕，实得 %d 条", len(r.CallLog))
	}
	if r.CallLog[0].Status != StatusDenied {
		t.Fatalf("留痕状态应为 denied，实得 %q", r.CallLog[0].Status)
	}
	if !strings.Contains(r.CallLog[0].Error, "高危") {
		t.Fatalf("留痕应带拒绝原因，实得 %q", r.CallLog[0].Error)
	}
}

// TestStagesFailClosedOnStageError 判定环**自己报错**按拒绝处理，不按「跳过这一环」。
//
// 跳过等于把一次故障静默降级成不设防——环坏了于是这一环等于没挂，而这没有任何信号。
func TestStagesFailClosedOnStageError(t *testing.T) {
	hits := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { hits++; return "x", nil }}
	boom := invoke.StageFunc{StageName: "flaky", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		return invoke.Verdict{Allow: true}, errors.New("判定源不可用")
	}}
	r := Run(context.Background(), `try { host.t("x") } catch (e) { return "已拦下" } return "放行了"`,
		[]Binding{bind}, Options{Stages: []invoke.Stage{boom}})

	if !r.OK || r.Value != `"已拦下"` {
		t.Fatalf("环报错应 fail-closed 拒绝，实得 OK=%v value=%s", r.OK, r.Value)
	}
	if hits != 0 {
		t.Fatalf("环报错不得触达执行体，实得 %d 次", hits)
	}
}

// TestStagePanicDoesNotKillProgram 判定环 panic 不得杀掉编排进程。
func TestStagePanicDoesNotKillProgram(t *testing.T) {
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}
	crasher := invoke.StageFunc{StageName: "crasher", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		panic("环里炸了")
	}}
	r := Run(context.Background(), `try { host.t("x") } catch (e) { return "已拦下" } return "放行了"`,
		[]Binding{bind}, Options{Stages: []invoke.Stage{crasher}})

	if !r.OK || r.Value != `"已拦下"` {
		t.Fatalf("环 panic 应被 recover 并按拒绝收口，实得 OK=%v value=%s err=%q", r.OK, r.Value, r.Error)
	}
}

// TestStagesShortCircuitOnFirstDeny 第一环拒绝后，后面的环不得再被评估。
// 后面的环若有副作用（计费、配额、留痕），在已拒绝的调用上白跑一遍。
func TestStagesShortCircuitOnFirstDeny(t *testing.T) {
	later := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}
	opts := Options{Stages: []invoke.Stage{
		denyStage("first", "不让", false),
		invoke.StageFunc{StageName: "later", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			later++
			return invoke.Verdict{Allow: true}, nil
		}},
	}}
	_ = Run(context.Background(), `try { host.t("x") } catch (e) {} return 1`, []Binding{bind}, opts)
	if later != 0 {
		t.Fatalf("第一环拒绝后不得再评估后续环，实得评估 %d 次", later)
	}
}

// TestStagesReceiveCallArgs 判定环拿到的必须是本次调用的真实参数。
//
// 拿不到参数就判不了「这个工具被用在了不该用的地方」，环接了等于没接。
func TestStagesReceiveCallArgs(t *testing.T) {
	bind := Binding{Name: "get_logs", Signature: "get_logs(host, count)", Call: func(context.Context, string) (string, error) {
		return "x", nil
	}}
	var seen invoke.Call
	spy := invoke.StageFunc{StageName: "spy", Fn: func(_ context.Context, c invoke.Call) (invoke.Verdict, error) {
		seen = c
		return invoke.Verdict{Allow: true}, nil
	}}
	_ = Run(context.Background(), `return host.get_logs("h1", 60)`, []Binding{bind}, Options{Stages: []invoke.Stage{spy}})

	if seen.Name != "get_logs" {
		t.Fatalf("环应拿到工具名，实得 %q", seen.Name)
	}
	if !strings.Contains(seen.Args, `"host":"h1"`) {
		t.Fatalf("环应拿到打包后的参数，实得 %q", seen.Args)
	}
}

// TestNoStagesKeepsLegacyBehaviour 未装配判定环时行为与引入前逐字一致。
func TestNoStagesKeepsLegacyBehaviour(t *testing.T) {
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}
	r := Run(context.Background(), `return host.t("x")`, []Binding{bind}, Options{})
	if !r.OK {
		t.Fatalf("未装配判定环应照常执行，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if len(r.CallLog) != 1 || r.CallLog[0].Status != StatusOK {
		t.Fatalf("留痕状态应为 ok，实得 %+v", r.CallLog)
	}
}
