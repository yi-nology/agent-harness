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
	env, err := (egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128", Allow: []string{"b.internal", "a.internal"}}).Env(base)
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
