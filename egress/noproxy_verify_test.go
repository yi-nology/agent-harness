package egress

import "testing"

// issue #42：声明无绕过时，环境携带 NO_PROXY/no_proxy 不得验证为绿——
// NO_PROXY=* 会把代理架空成全直连。声明带绕过时既有等价判定不受影响。
func TestVerifyRejectsNoProxyWhenBypassEmpty(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128"}
	for _, env := range []map[string]string{
		{"HTTP_PROXY": "http://proxy.internal:3128", "NO_PROXY": "*"},
		{"HTTP_PROXY": "http://proxy.internal:3128", "no_proxy": "localhost,.internal"},
		{"HTTP_PROXY": "http://proxy.internal:3128", "NO_PROXY": "10.0.0.0/8"},
	} {
		if err := spec.Verify(env); err == nil {
			t.Fatalf("无绕过声明 + 环境 NO_PROXY 必须拒绝, env=%v", env)
		}
	}
	// 干净环境（无 NO_PROXY）照常绿
	if err := spec.Verify(map[string]string{"HTTP_PROXY": "http://proxy.internal:3128"}); err != nil {
		t.Fatalf("干净环境必须绿: %v", err)
	}
	// 空串 NO_PROXY 视同缺席（与 Env 生成的「整条不出现」形态兼容）
	if err := spec.Verify(map[string]string{"HTTP_PROXY": "http://proxy.internal:3128", "NO_PROXY": ""}); err != nil {
		t.Fatalf("空串 NO_PROXY 视同缺席: %v", err)
	}
}

func TestVerifyBypassNonEmptyStillCompares(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128", Bypass: []string{"localhost"}}
	// 兑现的绕过照常绿
	if err := spec.Verify(map[string]string{
		"HTTP_PROXY": "http://proxy.internal:3128",
		"NO_PROXY":   "localhost",
	}); err != nil {
		t.Fatalf("兑现绕过必须绿: %v", err)
	}
	// 不兑现照常红
	if err := spec.Verify(map[string]string{
		"HTTP_PROXY": "http://proxy.internal:3128",
		"NO_PROXY":   "other.host",
	}); err == nil {
		t.Fatal("绕过不兑现必须红")
	}
}
