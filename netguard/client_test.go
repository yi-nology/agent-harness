// client_test.go —— 接线件（NewTransport / NewClient）的用例。
//
// # 为什么接线件要有测试
//
// 接线件的失败形态是**静默放行**：守卫没挂上，客户端照常工作，宿主的开发环境
// 与生产环境都不报错——直到出站请求真的打到了不该打的地址。
// 尤其「自定义 RoundTripper 挂不上守卫」这一支，如果实现选择「悄悄放行」，
// 宿主的测试与生产会同时失去防线，且没有任何信号。
//
// 所以本文件钉三件事：守卫真的挂上了、base 的字段没丢、挂不上时**响亮失败**。

package netguard

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// echoListener 起一个本机回环 HTTP 服务器，返回地址。
//
// # 为什么用 http.Server 而不是裸 TCP
//
// 早先的夹具是裸 TCP：accept 到连接后手写一行
// `HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n` 然后立刻 close。
// 但那行响应**没有 `Connection: close` 头**——按 HTTP/1.1 语义它是 keep-alive，
// 客户端读完会把连接放回空闲池，而服务端已经把它关了。
//
// 于是池里可能躺着一条「客户端以为可复用、实际已关或还带着残留字节」的连接：
// 下一次复用时 Go transport 先 peek 它，peek 到**响应**（而非 EOF）就报
// `Unsolicited response received on idle HTTP channel`——CI 上偶发红
// （2026-10-21 实测：netguard 的 TestNewClientAttachesGuard）。
//
// 用 http.Server 则由标准库负责 keep-alive 与关闭语义，客户端与服务端对
// 「这条连接还能不能用」的判断一致，从根上消除这类协议不一致。
//
// # 仍然要回一个合法响应
//
// 只关连接的话客户端读到 EOF，失败形态会与「被守卫拦住」混在一起
// （两者都表现为 Get 出错）——那这条用例就分不清「守卫拦了」与「服务端没回话」。
func echoListener(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 最小合法响应：200 + 零长度 body。
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
	})
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String()
}

// TestNewClientAttachesGuard 守卫必须真的挂在客户端上。
//
// 判据：同一个客户端对同一个回环目标，零值放行面拒、放行回环时通。
// 若守卫没挂上，两次都会通（用例红）；若守卫挂错层（挂在别处），两次行为一致
// 但第一次也通（同样红）。
func TestNewClientAttachesGuard(t *testing.T) {
	addr := echoListener(t)

	strict, err := NewClient(nil, Allow{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Get("http://" + addr + "/x"); err == nil {
		t.Fatal("零值放行面下必须拒回环目标：守卫没挂上或挂错层")
	}

	permissive, err := NewClient(nil, Allow{Loopback: true})
	if err != nil {
		t.Fatal(err)
	}
	// 目标在监听器上，连接会成功建立（响应内容无关，只看 err）。
	if _, err := permissive.Get("http://" + addr + "/x"); err != nil {
		t.Fatalf("放行回环后必须能连上（回环监听器在 %s）: %v", addr, err)
	}
}

// TestNewClientPreservesBaseFields base 的字段必须原样保留，base 自身不被改。
//
// 两条各自会红：字段丢 → 宿主原有的超时设置消失（表现为「请求卡死」）；
// base 被改 → 宿主同时持有「带守卫」与「不带守卫」两个客户端，后者是暗门。
func TestNewClientPreservesBaseFields(t *testing.T) {
	base := &http.Client{Timeout: 7 * time.Second}
	got, err := NewClient(base, Allow{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Timeout != base.Timeout {
		t.Fatalf("base 的 Timeout 丢了：got %v want %v", got.Timeout, base.Timeout)
	}
	if base.Transport != nil {
		t.Fatalf("base 的 Transport 被就地改了：%v", base.Transport)
	}
	if got.Transport == nil {
		t.Fatal("返回的客户端必须带守卫传输层")
	}
	// 每次构造都要新的传输层：共用一个传输层会让两个客户端的放行面互相污染。
	got2, err := NewClient(base, Allow{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Transport == got2.Transport {
		t.Fatal("两次构造必须得到不同的传输层（否则放行面会互相污染）")
	}
}

// TestNewClientRejectsCustomRoundTripper 挂不上守卫时必须响亮失败。
//
// 不能悄悄放行：宿主会以为「守卫接上了」，实际每次出站都在裸奔。
func TestNewClientRejectsCustomRoundTripper(t *testing.T) {
	base := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})}
	if _, err := NewClient(base, Allow{}); err == nil {
		t.Fatal("自定义 RoundTripper 挂不上拨号层守卫时必须报错，不得悄悄放行")
	}
}

// TestNewTransportWrapsExistingDialer base 自己的拨号器必须被包住而不是被丢掉。
//
// 守卫是**叠加**在 base 之上的：宿主已有的代理设置、连接池参数都在 base 的
// DialContext 里，丢掉它等于让守卫之外的行为变了（且多半表现为连不上）。
func TestNewTransportWrapsExistingDialer(t *testing.T) {
	addr := echoListener(t)
	var called int
	base := &http.Transport{
		DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			called++
			return (&net.Dialer{}).DialContext(ctx, network, a)
		},
	}
	tr := NewTransport(base, Allow{Loopback: true})
	if tr.DialContext == nil {
		t.Fatal("挂上守卫后 DialContext 不得为 nil")
	}
	conn, err := tr.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	_ = conn.Close()
	if called != 1 {
		t.Fatalf("base 自己的拨号器必须被包住并被调用，实得调用 %d 次", called)
	}
}

// roundTripperFunc 让测试能造一个自定义 RoundTripper。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
