// client.go —— 出站守卫的**接线件**：把拨号层守卫挂到宿主的 HTTP 客户端上。
//
// # 为什么需要它（issue #6）
//
// 拨号层守卫要生效，宿主必须把 `Transport.DialContext` 换成 `netguard.DialFunc`。
// 这一步在宿主侧散落着，且有两个反复出现的坑：
//
//  1. **client 写死在业务代码里，测试无法注入**——宿主有 `&http.Client{Timeout: ...}`
//     的写法，测试连不上自己的假服务器（httptest 跑在 127.0.0.1，而守卫按缺省
//     放行面拒回环）。于是要么测试被守卫挡住，要么有人把生产配置一起放宽——
//     **后者等于把刚补上的窗口重新打开**。
//  2. **两层口径容易不一致**——校验层与拨号层各写一份开关，值传岔了就成了
//     「分层放行」。
//
// 本文件把这两件事收口：
//
//   - 守卫挂在**传输层**（拨号发生的地方），宿主已有 client/transport 可整体注入；
//   - 放行面用**同一个类型**（Allow，见 allow.go），两层不可能各持一套开关。
//
// # 用法
//
// 生产：
//
//	hc, err := netguard.NewClient(nil, netguard.Allow{})   // 零值 = 私网与回环全拒
//
// 测试（要连回环上的假服务器时，**只在测试里放宽**回环）：
//
//	hc, err := netguard.NewClient(&http.Client{Timeout: 2 * time.Second},
//	                             netguard.Allow{Loopback: true})
//
// 生产路径不放宽——放宽不是「配置」，是把出站防线重新打开。

package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// NewTransport 返回一个挂了出站守卫拨号器的传输层。
//
// base 为 nil 时克隆 http.DefaultTransport。base 自己的 DialContext 会被守卫
// **包住**（守卫叠加在它之上，不是替换它）——宿主已有的自定义拨号器
// （代理、连接池参数）不会丢。
//
// base 只设了废弃字段 Dial（未设 DialContext）时同样成立：旧拨号器被适配成
// DialFunc 的 next 交给守卫包住（issue #152）。此前这里只包装 DialContext，
// 只设 Dial 的宿主自定义拨号器被静默丢弃——克隆后 DialContext 为 nil，
// DialFunc(nil,…) 落缺省拨号器，而 net/http 的 DialContext 优先语义使旧 Dial
// 被完全忽略，连接行为静默改变。适配丢掉的只有 ctx：废弃 API 本身不接收
// context（请求取消/期限不再传导到拨号器，这是 http.Transport.Dial 的既有
// 局限，不是本包装引入的）。
//
// 守卫只能挂在传输层：拨号发生在传输层内部，绕开传输层就没有拨号层防线。
func NewTransport(base *http.Transport, allow Allow) *http.Transport {
	var t *http.Transport
	if base == nil {
		t = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		t = base.Clone()
	}
	if t.DialContext == nil && t.Dial != nil {
		// 只设 Dial 的 base：把旧拨号器适配成 next（issue #152）——
		// 守卫包住它，而不是用缺省拨号器把它替换掉。
		next := t.Dial
		t.DialContext = DialFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return next(network, addr)
		}, DialOptions{Allow: allow})
		t.Dial = nil // DialContext 已设，net/http 会忽略 Dial——留着是死字段
		return t
	}
	t.DialContext = DialFunc(t.DialContext, DialOptions{Allow: allow})
	return t
}

// NewClient 返回一个挂了出站守卫的 HTTP 客户端。
//
// base 为 nil 时用 http.DefaultClient 的形状（零值 client）。base 的其余字段
// （Timeout / Jar / CheckRedirect）原样保留。
//
// base.Transport 若是 *http.Transport 则克隆并挂上守卫；若是自定义 RoundTripper
// 则**无法**挂拨号层守卫（拨号发生在传输层内部），此时返回错误——而不是悄悄
// 放行。「悄悄放行」的失败形态是「守卫看起来接上了，实际没接」。
//
// base.Transport 为 nil 时用 http.DefaultTransport 的克隆。
func NewClient(base *http.Client, allow Allow) (*http.Client, error) {
	c := &http.Client{}
	if base != nil {
		// http.Client 无锁，浅拷贝安全；Transport 随后被替换成克隆，base 不受影响。
		cc := *base
		c = &cc
	}
	switch tr := c.Transport.(type) {
	case nil:
		c.Transport = NewTransport(nil, allow)
	case *http.Transport:
		c.Transport = NewTransport(tr, allow)
	default:
		return nil, fmt.Errorf(
			"客户端的 Transport 是自定义 RoundTripper（%T），无法挂拨号层守卫：拨号发生在传输层内部；"+
				"请改用 *http.Transport，或在传输层之外另设出站闸", tr)
	}
	return c, nil
}
