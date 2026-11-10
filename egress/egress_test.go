package egress_test

import (
	"strings"
	"testing"

	"github.com/yi-nology/agent-harness/egress"
)

func TestModeOrdering(t *testing.T) {
	if !egress.ModeDenied.Satisfies(egress.ModeProxy) {
		t.Fatal("禁网应满足「经代理」的要求（没有流量能出）")
	}
	if egress.ModeProxy.Satisfies(egress.ModeDenied) {
		t.Fatal("经代理不满足禁网")
	}
	if !egress.ModeUnrestricted.Satisfies(egress.ModeUnrestricted) {
		t.Fatal("不要求应满足不要求")
	}
	if egress.ModeUnrestricted.Satisfies(egress.ModeProxy) {
		t.Fatal("不要求不满足经代理")
	}
	if egress.ModeDenied.String() != "denied" || egress.Mode(9).String() != "unknown" {
		t.Fatal("词表漂移")
	}
}

func TestValidate(t *testing.T) {
	if err := (egress.Spec{Mode: egress.ModeProxy}).Validate(); err == nil {
		t.Fatal("经代理但没给地址必须报错")
	}
	if err := (egress.Spec{Mode: egress.ModeProxy, Proxy: "127.0.0.1:3128"}).Validate(); err == nil {
		t.Fatal("缺 scheme 必须报错")
	}
	if err := (egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"}).Validate(); err != nil {
		t.Fatalf("合法声明不该报错: %v", err)
	}
	if err := (egress.Spec{Mode: egress.Mode(9)}).Validate(); err == nil {
		t.Fatal("未知档位必须报错")
	}
	if err := (egress.Spec{Mode: egress.ModeDenied}).Validate(); err != nil {
		t.Fatalf("禁网声明本身合法: %v", err)
	}
}

func TestEnvProxy(t *testing.T) {
	base := []string{"PATH=/bin", "HTTPS_PROXY=http://old:1", "HOME=/root"}
	// 绕过名单才产生 NO_PROXY；Allow（允许经代理访问的目标）不产生它——
	// 反向判据见 e2e_test.go 的 TestAllowDoesNotBypassProxy。
	env, err := (egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128", Bypass: []string{"b.internal", "a.internal"}}).Env(base)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "http://old:1") {
		t.Fatal("旧代理变量必须被覆盖（声明换了 env 还是旧的 ⇒ 兑现不了）")
	}
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:3128", "ALL_PROXY=http://127.0.0.1:3128", "NO_PROXY=a.internal,b.internal"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env 缺 %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/bin") {
		t.Fatal("必须保留 base 的其他变量")
	}
}

func TestEnvUnrestrictedIsPassthrough(t *testing.T) {
	base := []string{"A=1"}
	env, err := (egress.Spec{}).Env(base)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	if len(env) != 1 || env[0] != "A=1" {
		t.Fatalf("不要求时应原样透传，got %v", env)
	}
}

// ⭐ 禁网不能用环境变量伪造：伪造成功比拒绝更危险（报告会显示「已兑现」）。
func TestDeniedCannotBeFakedByEnv(t *testing.T) {
	if _, err := (egress.Spec{Mode: egress.ModeDenied}).Env(nil); err == nil {
		t.Fatal("禁网的 Env 必须报错，不得伪造")
	}
	if err := (egress.Spec{Mode: egress.ModeDenied}).Verify(map[string]string{"HTTPS_PROXY": "http://x"}); err == nil {
		t.Fatal("禁网的 Verify 必须报错")
	}
}

// fullProxyEnv 六个代理变量全部指向 proxy 的环境（Env 生成形态的对照基线）。
//
// 词表在测试里显式展开而不是借 egress 内部清单：判据要钉的正是「这六个名字」，
// 借生产清单会把「清单少了一个变量」这种缺陷一起藏掉。
func fullProxyEnv(proxy string) map[string]string {
	env := make(map[string]string, 6)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		env[k] = proxy
	}
	return env
}

// TestVerify 代理变量判据（issue #62 收严为「六个一个不缺、一个不歪」）。
//
// 旧判据「六个里命中一个即绿」的最坏覆盖是应有面的 1/6：只配 HTTP_PROXY 的
// 环境验成 enforced，而子进程的 HTTPS 流量没有任何代理、裸奔出网；HTTP_PROXY
// 对而 HTTPS_PROXY 指向别处的环境也验成绿——流量走了一条声明从未授权的链路。
func TestVerify(t *testing.T) {
	spec := egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"}
	if err := spec.Verify(fullProxyEnv("http://127.0.0.1:3128")); err != nil {
		t.Fatalf("六个变量全对却判失败: %v", err)
	}
	lower := fullProxyEnv("http://127.0.0.1:3128")
	delete(lower, "HTTP_PROXY")
	delete(lower, "HTTPS_PROXY")
	delete(lower, "ALL_PROXY")
	if err := spec.Verify(lower); err == nil {
		t.Fatal("只配小写三件（大写缺席）必须红——大写读者（curl 等）没代理")
	}

	// issue #62 的两个假绿形态必须红，且点名缺/歪的变量。
	onlyHTTP := fullProxyEnv(spec.Proxy)
	for _, k := range []string{"HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		delete(onlyHTTP, k)
	}
	err := spec.Verify(onlyHTTP)
	if err == nil || !strings.Contains(err.Error(), "未兑现代理声明") {
		t.Fatalf("只配 HTTP_PROXY 必须红（HTTPS 裸奔），got %v", err)
	}
	if !strings.Contains(err.Error(), "缺失") || !strings.Contains(err.Error(), "HTTPS_PROXY") {
		t.Fatalf("缺失的变量必须点名，got %v", err)
	}

	diverted := fullProxyEnv(spec.Proxy)
	diverted["HTTPS_PROXY"] = "http://evil.example:9999"
	err = spec.Verify(diverted)
	if err == nil || !strings.Contains(err.Error(), "异值") || !strings.Contains(err.Error(), "HTTPS_PROXY=http://evil.example:9999") {
		t.Fatalf("HTTPS_PROXY 指向别处必须红并点名异值，got %v", err)
	}

	if err := spec.Verify(nil); err == nil {
		t.Fatal("空 env 必须报错")
	}
}

type fakeEnforcer struct{ mode egress.Mode }

func (f fakeEnforcer) Egress() egress.Mode { return f.mode }

type silentProvider struct{}

func TestSatisfiedBy(t *testing.T) {
	req := egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"}
	if err := req.SatisfiedBy(fakeEnforcer{mode: egress.ModeProxy}); err != nil {
		t.Fatalf("同档应满足: %v", err)
	}
	if err := req.SatisfiedBy(fakeEnforcer{mode: egress.ModeDenied}); err != nil {
		t.Fatalf("更强档应满足: %v", err)
	}
	if err := req.SatisfiedBy(silentProvider{}); err == nil {
		t.Fatal("沉默的后端必须判不满足（沉默按最弱处理）")
	}
	if err := req.SatisfiedBy(fakeEnforcer{mode: egress.ModeUnrestricted}); err == nil {
		t.Fatal("更弱档必须判不满足")
	}
	if err := (egress.Spec{}).SatisfiedBy(silentProvider{}); err != nil {
		t.Fatalf("不要求时不看后端能力: %v", err)
	}
	denied := egress.Spec{Mode: egress.ModeDenied}
	if err := denied.SatisfiedBy(fakeEnforcer{mode: egress.ModeProxy}); err == nil {
		t.Fatal("要求禁网但后端只能经代理，必须判不满足")
	}
	if err := (egress.Spec{Mode: egress.ModeProxy}).SatisfiedBy(silentProvider{}); err == nil {
		t.Fatal("配置错也要先报出来")
	}
}

// TestVerifyBypass 成对：绕过名单必须被判到。
//
// 只判「代理地址对不对」是不够的：NO_PROXY 一条写错，子进程就被排到代理之外，
// 而 Verify 会说「兑现了」。三种坏形态各钉一次。
//
// 反向也要钉：顺序与空白漂移是**等价形态**，必须判通过。NO_PROXY 无语义顺序，
// 判字面相同就是假红——那会把「自己拼 env 的调用方」判死，而它们其实兑现了。
func TestVerifyBypass(t *testing.T) {
	spec := egress.Spec{
		Mode:   egress.ModeProxy,
		Proxy:  "http://127.0.0.1:3128",
		Bypass: []string{"b.internal", "a.internal"},
	}
	// 代理变量给全六个（#62 收严后的基线），绕过名单的判据才不被代理判据抢先红。
	withBypass := func(noproxy string) map[string]string {
		env := fullProxyEnv(spec.Proxy)
		if noproxy != "" {
			env["NO_PROXY"] = noproxy
		}
		return env
	}
	if err := spec.Verify(withBypass("a.internal,b.internal")); err != nil {
		t.Fatalf("兑现了却判失败: %v", err)
	}
	if err := spec.Verify(withBypass(" b.internal , a.internal ")); err != nil {
		t.Fatalf("顺序/空白漂移是等价形态，判失败就是假红: %v", err)
	}

	for name, env := range map[string]map[string]string{
		"整条缺失": fullProxyEnv(spec.Proxy),
		"少一条":  withBypass("a.internal"),
		"多一条":  withBypass("a.internal,b.internal,c.internal"),
	} {
		if err := spec.Verify(env); err == nil {
			t.Fatalf("绕过名单%s却判通过: %v", name, env)
		}
	}
}
