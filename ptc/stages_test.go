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

// TestStagesFaultAndDenyGetDistinctKinds 守卫崩了与守卫判定拒绝必须是两个类别。
//
// 这条区分决定调用方去修谁：合成一类，调用方就会去改那段本来没问题的程序，
// 而真正该修的是守卫代码。判据取自 invoke 的归口哨兵（ErrStageFailed 只在
// 「环自身出错」时归口），不读环节名——归口环节名是上游包内未导出常量。
func TestStagesFaultAndDenyGetDistinctKinds(t *testing.T) {
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}
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
			r := Run(context.Background(), `host.t("x")`, []Binding{bind},
				Options{Stages: []invoke.Stage{tc.ring}})

			if r.OK {
				t.Fatalf("未被程序接住的门禁失败应判失败，实得 OK=true value=%s", r.Value)
			}
			if r.ErrorKind != tc.wantKind {
				t.Fatalf("ErrorKind 应为 %s，实得 %q（Error=%q）", tc.wantKind, r.ErrorKind, r.Error)
			}
			if !strings.HasPrefix(r.Error, tc.wantPrefix) {
				t.Fatalf("Error 前缀应为 %q（别让模型以为是程序自己抛的），实得 %q", tc.wantPrefix, r.Error)
			}
			if len(r.Suggestions) == 0 {
				t.Fatalf("失败必须带面向模型的建议（kind=%s）", r.ErrorKind)
			}
			advice := strings.Join(r.Suggestions, "；")
			if !strings.Contains(advice, "门禁") {
				t.Fatalf("建议应点明是宿主门禁而非程序 bug，实得 %q", advice)
			}
		})
	}

	// 守卫故障的建议必须说清「改程序没用」——否则模型会去反复改那段没问题的程序。
	fault := Run(context.Background(), `host.t("x")`, []Binding{bind},
		Options{Stages: []invoke.Stage{crasher}})
	if !strings.Contains(strings.Join(fault.Suggestions, "；"), "不是程序的问题") {
		t.Fatalf("守卫故障的建议应明说不是程序的问题，实得 %v", fault.Suggestions)
	}

	// 反向：策略拒绝不该拿到与 JS 运行时错误同一套建议——那套让模型去查参数形状，
	// 对一次策略拒绝是完全错的方向。比对的是「整份建议」，不比对某几个字：
	// 措辞会变，「建议是否指错了方向」这件事不会。
	denied := Run(context.Background(), `host.t("x")`, []Binding{bind},
		Options{Stages: []invoke.Stage{deny}})
	runtimeCase := Run(context.Background(), `const y = null; return y.x.y;`, nil, Options{})
	if runtimeCase.ErrorKind != "runtime" {
		t.Fatalf("对照样本应是 runtime，实得 %q", runtimeCase.ErrorKind)
	}
	if strings.Join(denied.Suggestions, "；") == strings.Join(runtimeCase.Suggestions, "；") {
		t.Fatalf("策略拒绝不该复用 runtime 的建议（那会把模型引向参数形状），实得 %v", denied.Suggestions)
	}
}

// TestStagePanicDoesNotKillProgram 判定环 panic 不得杀掉编排进程，
// 且**不得被归口成「守卫自己判定拒绝」**。
//
// 后半句是本包排过一份环留下的坑：那份把 panic 归口到守卫自己的名字、Cause 留空，
// 于是「守卫自己崩了」与「守卫正常拒绝了」在调用方眼里完全一样——调用方于是去修
// 策略，而真正该修的是守卫代码。判据用否定式（消息里不该出现守卫名）而不是去匹配
// 上游的归口环节名：上游把那个名字改成什么，本包都不该跟着碎。
func TestStagePanicDoesNotKillProgram(t *testing.T) {
	hits := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) { hits++; return "x", nil }}
	crasher := invoke.StageFunc{StageName: "crasher", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
		panic("环里炸了")
	}}
	r := Run(context.Background(), `try { host.t("x") } catch (e) { return "已拦下" } return "放行了"`,
		[]Binding{bind}, Options{Stages: []invoke.Stage{crasher}})

	if !r.OK || r.Value != `"已拦下"` {
		t.Fatalf("环 panic 应被 recover 并按拒绝收口，实得 OK=%v value=%s err=%q", r.OK, r.Value, r.Error)
	}
	if hits != 0 {
		t.Fatalf("环 panic 不得触达执行体，实得 %d 次", hits)
	}

	var denied *CallRecord
	for i := range r.CallLog {
		if r.CallLog[i].Status == StatusDenied {
			denied = &r.CallLog[i]
		}
	}
	if denied == nil {
		t.Fatalf("被拒调用应留一条 denied 账，实得 %+v", r.CallLog)
	}
	if !strings.Contains(denied.Error, "环里炸了") {
		t.Fatalf("拒绝账应带 panic 原文（定位要能到那一句），实得 %q", denied.Error)
	}
	if strings.Contains(denied.Error, "crasher") {
		t.Fatalf("守卫 panic 不得归口到守卫自己的名字——那正是把它谎报成「守卫判定拒绝」，实得 %q", denied.Error)
	}
	if r.Calls < 1 {
		t.Fatalf("被拒同样计数，否则程序可以 try/catch 后无限重试，实得 Calls=%d", r.Calls)
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
