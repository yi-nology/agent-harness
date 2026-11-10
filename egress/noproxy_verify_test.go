package egress

import (
	"strings"
	"testing"
)

// fullProxyEnv 六个代理变量全部指向 proxy 的环境（#62 收严后的绿判基线；
// 词表显式展开，不借生产清单——要钉的正是这六个名字）。
func fullProxyEnv(proxy string) map[string]string {
	env := make(map[string]string, 6)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		env[k] = proxy
	}
	return env
}

// withVars 在六变量齐备的 env 上叠加若干键值（绕过名单判据的用例构造器）。
func withVars(proxy string, kv ...string) map[string]string {
	env := fullProxyEnv(proxy)
	for i := 0; i+1 < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}

// issue #42：声明无绕过时，环境携带 NO_PROXY/no_proxy 不得验证为绿——
// NO_PROXY=* 会把代理架空成全直连。声明带绕过时既有等价判定不受影响。
func TestVerifyRejectsNoProxyWhenBypassEmpty(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128"}
	for _, env := range []map[string]string{
		withVars(spec.Proxy, "NO_PROXY", "*"),
		withVars(spec.Proxy, "no_proxy", "localhost,.internal"),
		withVars(spec.Proxy, "NO_PROXY", "10.0.0.0/8"),
	} {
		if err := spec.Verify(env); err == nil {
			t.Fatalf("无绕过声明 + 环境 NO_PROXY 必须拒绝, env=%v", env)
		}
	}
	// 干净环境（无 NO_PROXY）照常绿
	if err := spec.Verify(fullProxyEnv(spec.Proxy)); err != nil {
		t.Fatalf("干净环境必须绿: %v", err)
	}
	// 空串 NO_PROXY 视同缺席（与 Env 生成的「整条不出现」形态兼容）
	if err := spec.Verify(withVars(spec.Proxy, "NO_PROXY", "")); err != nil {
		t.Fatalf("空串 NO_PROXY 视同缺席: %v", err)
	}
}

// TestVerifyBypassBothVariants 绕过名单的大小写两变体必须同判（issue #63）。
//
// 旧判据只读 env["NO_PROXY"]：大写对、小写 no_proxy=* 验成绿（小写读者——
// requests/pip/多数 Go 客户端——对所有目标直连，绕过名单成装饰）；只有小写
// 且正确反而误红。收口为对两变体取**并集**的单一判定：并集与声明语义等价即绿
// （空串变体折成空集，视同缺席），多出或少于声明即红并点名变体与实际值。
func TestVerifyBypassBothVariants(t *testing.T) {
	single := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128", Bypass: []string{"internal.example"}}
	double := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128", Bypass: []string{"a.internal", "b.internal"}}
	cases := []struct {
		name    string
		spec    Spec
		env     map[string]string
		wantErr bool
		wantMsg string // 判红时应点到的内容
	}{
		{
			name:    "大写对、小写携带通配（旧假绿）",
			spec:    single,
			env:     withVars(single.Proxy, "NO_PROXY", "internal.example", "no_proxy", "*"),
			wantErr: true,
			wantMsg: "no_proxy",
		},
		{
			name:    "小写携带更宽名单（旧假绿，非通配形态）",
			spec:    single,
			env:     withVars(single.Proxy, "NO_PROXY", "internal.example", "no_proxy", "internal.example,evil.example"),
			wantErr: true,
			wantMsg: "evil.example",
		},
		{
			name: "只有小写且正确（旧假红）",
			spec: single,
			env:  withVars(single.Proxy, "no_proxy", "internal.example"),
		},
		{
			name: "两个变体都在且一致（对照）",
			spec: single,
			env:  withVars(single.Proxy, "NO_PROXY", "internal.example", "no_proxy", "internal.example"),
		},
		{
			name: "小写比声明窄（并集=声明，绿）",
			spec: double,
			env:  withVars(double.Proxy, "NO_PROXY", "a.internal,b.internal", "no_proxy", "a.internal"),
		},
		{
			name:    "两个变体都缺席",
			spec:    single,
			env:     fullProxyEnv(single.Proxy),
			wantErr: true,
			wantMsg: "缺席",
		},
		{
			name:    "只有大写但值不兑现",
			spec:    single,
			env:     withVars(single.Proxy, "NO_PROXY", "other.host"),
			wantErr: true,
			wantMsg: "NO_PROXY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Verify(tc.env)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("必须判红, env=%v", tc.env)
				}
				if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("判红须点名 %q, got %q", tc.wantMsg, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("兑现形态必须绿, env=%v, err=%v", tc.env, err)
			}
		})
	}
}

func TestVerifyBypassNonEmptyStillCompares(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://proxy.internal:3128", Bypass: []string{"localhost"}}
	// 兑现的绕过照常绿
	if err := spec.Verify(withVars(spec.Proxy, "NO_PROXY", "localhost")); err != nil {
		t.Fatalf("兑现绕过必须绿: %v", err)
	}
	// 不兑现照常红
	if err := spec.Verify(withVars(spec.Proxy, "NO_PROXY", "other.host")); err == nil {
		t.Fatal("绕过不兑现必须红")
	}
}
