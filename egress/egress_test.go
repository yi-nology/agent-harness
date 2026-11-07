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

func TestVerify(t *testing.T) {
	spec := egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"}
	if err := spec.Verify(map[string]string{"HTTPS_PROXY": "http://127.0.0.1:3128"}); err != nil {
		t.Fatalf("兑现了却判失败: %v", err)
	}
	if err := spec.Verify(map[string]string{"https_proxy": "http://127.0.0.1:3128"}); err != nil {
		t.Fatalf("小写变量同样算兑现: %v", err)
	}
	err := spec.Verify(map[string]string{"HTTPS_PROXY": "http://other:1"})
	if err == nil || !strings.Contains(err.Error(), "未兑现代理声明") {
		t.Fatalf("没兑现必须报错（含实际值），got %v", err)
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
	if err := spec.Verify(map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:3128",
		"NO_PROXY":    "a.internal,b.internal",
	}); err != nil {
		t.Fatalf("兑现了却判失败: %v", err)
	}
	if err := spec.Verify(map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:3128",
		"NO_PROXY":    " b.internal , a.internal ",
	}); err != nil {
		t.Fatalf("顺序/空白漂移是等价形态，判失败就是假红: %v", err)
	}

	for name, env := range map[string]map[string]string{
		"整条缺失": {"HTTPS_PROXY": "http://127.0.0.1:3128"},
		"少一条":  {"HTTPS_PROXY": "http://127.0.0.1:3128", "NO_PROXY": "a.internal"},
		"多一条":  {"HTTPS_PROXY": "http://127.0.0.1:3128", "NO_PROXY": "a.internal,b.internal,c.internal"},
	} {
		if err := spec.Verify(env); err == nil {
			t.Fatalf("绕过名单%s却判通过: %v", name, env)
		}
	}
}
