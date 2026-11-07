// e2e_test.go —— 出口声明链的端到端验收：声明 → env → 真客户端 → 真强制代理 → 真请求。
//
// # 为什么判定面之外还要这一层
//
// 单元判据只能证明「env 长成什么样」，证明不了「照这份 env 走，流量真的经过代理
// 并被判定」。GAP 台账 §四 第 2 步的验收要的正是后者：与 egress.Verify 的 env
// 对接跑通，**端到端一条真请求**。少了这一层，「生成了 env」与「兑现了声明」之间
// 仍然只隔着一句话。
//
// # 区分器：代理的拒绝名单
//
// 对同一个健康后端，「经代理」与「直连」都是一次 200，从响应上看不出差别。故用
// **代理拒绝该目标**当区分器：经代理 ⇒ 403（且带 X-Egress-Blocked）；绕过代理
// 直连 ⇒ 200。这条区分器同时是 TestAllowDoesNotBypassProxy 的判据来源——
// 一个「允许名单」若被写进 NO_PROXY，它命中的目标就绕过代理，deny 名单、端口
// 白名单、只读档对它全部失效。
package egress_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/guardx/egressproxy"

	"github.com/yi-nology/agent-harness/egress"
)

// backend 一个健康的后端：直连与经代理都能通。于是「经没经代理」必须由代理的
// 判定来区分，不能靠后端通不通来猜。
func backend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "backend-ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startProxy 起一个真强制代理（回环随机端口），返回可直接写进 Spec.Proxy 的地址。
func startProxy(t *testing.T, p egressproxy.Policy) string {
	t.Helper()
	px, err := egressproxy.New(p)
	if err != nil {
		t.Fatalf("构造代理失败: %v", err)
	}
	addr, err := px.Start()
	if err != nil {
		t.Fatalf("启动代理失败: %v", err)
	}
	t.Cleanup(func() { _ = px.Close() })
	return addr
}

// hostPort 从 httptest 的 URL 取主机与端口（代理的放行面按这两项判）。
func hostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("解析后端地址失败: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("解析后端端口失败: %v", err)
	}
	return u.Hostname(), port
}

// clientFromEnv 用 **env 里下发的代理地址**建客户端——这是「子进程照这份 env
// 走」的等价形态：子进程不读我们的 Go 变量，只读环境。
//
// 刻意不 os.Setenv：net/http 的 ProxyFromEnvironment 对进程环境有包级缓存（首个
// 请求之后不再读），在测试里改环境变量既不可靠又会污染并行用例。
func clientFromEnv(t *testing.T, env []string) *http.Client {
	t.Helper()
	raw := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HTTP_PROXY="); ok {
			raw = v
			break
		}
	}
	if raw == "" {
		t.Fatal("env 里没有 HTTP_PROXY：声明没落到子进程能看见的地方")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("env 里的代理地址无法解析: %v", err)
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
	}
}

// envMap 把 []key=value 折成 Spec.Verify 要的形态。
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// TestE2EListedTargetGoesThroughProxy 端到端：名单内目标 ⇒ 经代理 ⇒ 200，
// 且这份 env 能被 Spec.Verify 自证。
func TestE2EListedTargetGoesThroughProxy(t *testing.T) {
	srv := backend(t)
	host, port := hostPort(t, srv)
	addr := startProxy(t, egressproxy.Policy{AllowHosts: []string{host}, AllowPorts: []int{port}})

	spec := egress.Spec{Mode: egress.ModeProxy, Proxy: addr}
	if err := spec.Validate(); err != nil {
		t.Fatalf("声明自检失败: %v", err)
	}
	env, err := spec.Env([]string{"PATH=/bin"})
	if err != nil {
		t.Fatalf("生成 env 失败: %v", err)
	}
	if err := spec.Verify(envMap(env)); err != nil {
		t.Fatalf("这份 env 没兑现声明: %v", err)
	}

	resp, err := clientFromEnv(t, env).Get(srv.URL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("名单内目标应放行，实得 %d（X-Egress-Blocked=%q）",
			resp.StatusCode, resp.Header.Get("X-Egress-Blocked"))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend-ok" {
		t.Fatalf("响应体不是后端原文（可能没到达后端）: %q", body)
	}
}

// TestE2EUnlistedTargetIsDenied 端到端：不在名单内的目标 ⇒ 代理拒绝（默认拒绝）。
//
// 403 只可能来自代理——后端自己对任何请求都回 200。所以这条同时证明「流量真的
// 经过了代理」，而不只是「env 写得对」。
func TestE2EUnlistedTargetIsDenied(t *testing.T) {
	srv := backend(t)
	addr := startProxy(t, egressproxy.Policy{AllowHosts: []string{"only-this.internal"}})

	env, err := (egress.Spec{Mode: egress.ModeProxy, Proxy: addr}).Env(nil)
	if err != nil {
		t.Fatalf("生成 env 失败: %v", err)
	}
	resp, err := clientFromEnv(t, env).Get(srv.URL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("未列名目标必须被代理拒绝，实得 %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Egress-Blocked") == "" {
		t.Fatal("拒绝必须带 X-Egress-Blocked 原因（区分「被墙拒」与「目标自己 403」）")
	}
}

// TestE2EDenyWinsOverAllow 端到端：拒绝名单优先于放行名单（"*" 也压不住它）。
func TestE2EDenyWinsOverAllow(t *testing.T) {
	srv := backend(t)
	host, port := hostPort(t, srv)
	addr := startProxy(t, egressproxy.Policy{
		AllowHosts: []string{"*"},
		DenyHosts:  []string{host},
		AllowPorts: []int{port},
	})

	env, err := (egress.Spec{Mode: egress.ModeProxy, Proxy: addr}).Env(nil)
	if err != nil {
		t.Fatalf("生成 env 失败: %v", err)
	}
	resp, err := clientFromEnv(t, env).Get(srv.URL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("拒绝名单优先，实得 %d", resp.StatusCode)
	}
}

// TestAllowDoesNotBypassProxy 钉住：声明里的**允许目标**不得让目标绕过代理。
//
// 曾经 Spec.Allow 被写进 NO_PROXY —— 语义正好相反：允许名单里的目标直连，于是
// 代理的拒绝名单、端口白名单、只读档对它**全部失效**，而报告里那条声明看起来
// 仍然「已兑现」。「允许经代理访问」与「绕过代理直连」是相反的两件事，不能用
// 同一个字段表达。
//
// 为什么要判 env 而不是跑一次请求：NO_PROXY 的生效在**客户端库**里（net/http 的
// ProxyFromEnvironment、curl、各语言的 transport 各写一套），本仓不复刻那些实现。
// 而「代理地址是否被这份 env 指定」与「目标是否被这份 env 排除在代理之外」是
// 两个都能直接读出来的事实——判据就判事实。
func TestAllowDoesNotBypassProxy(t *testing.T) {
	env, err := (egress.Spec{
		Mode:  egress.ModeProxy,
		Proxy: "http://127.0.0.1:3128",
		Allow: []string{"api.internal"},
	}).Env(nil)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || (k != "NO_PROXY" && k != "no_proxy") {
			continue
		}
		if strings.Contains(v, "api.internal") {
			t.Fatalf("允许名单被写进 %s=%s ⇒ 该目标绕过代理直连（拒绝名单/端口白名单/只读档对它全部失效）；"+
				"要表达「不经代理」请用显式的绕过字段，不要把 Allow 借给它", k, v)
		}
	}
}
