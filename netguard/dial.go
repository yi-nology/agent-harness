// 拨号层出站守卫：把 netguard 的出站策略落到**建连那一刻**，闭合 rebinding 窗口。
//
// # 它补的是哪一段
//
// CheckURL 在**校验时**解析 DNS 并逐 IP 判定；实际请求在**之后**由 HTTP 客户端
// 发出，它会**再解析一次**。两次解析之间，权威 DNS 可以改口：
//
//	写面校验：evil.example → 1.2.3.4        （公网，放行）
//	实际请求：evil.example → 169.254.169.254 （元数据段，命中「恒拒」但已不在判定面内）
//
// 名字层（解析前按名拒）挡不住这一段——它判的是**名字**，而攻击改的是**解析结果**。
//
// # 三道判据，缺一不可
//
//  1. **名字层**：与 CheckURL 同一套名单（checkHostName），不花一次 DNS 往返。
//  2. **预检**：拨号前把主机名解析出的**每一个** IP 过 egress——任一被拒即拒，
//     连尝试都不做。
//  3. **端点核对**：建连成功后核对**真实** RemoteAddr。被拒则立刻关闭连接。
//     这一步才是闭合 TOCTOU 的关键：预检到建连之间仍有窗口，而实际连上的
//     对端地址是唯一的事实。
//
// # 为什么不用上游 urlguard.DialFunc
//
// 上游 `oekit/pkg/urlguard` 确有 `SafeDialFunc`，语义与本文件几乎相同。但它带
// **自己的一份 IP 清单**，而本包的 IP 策略单源在 egress（见包头「与上游 urlguard
// 的分工」一节：IP 策略**继续单源 egress**，不因 urlguard 出现而改判）。
//
// 用上游那份等于引入**第二套 IP 策略**：两份清单今天一致，明天上游加一段而
// egress 没加，拨号层就会放行一个校验层认定危险的地址——而漂移的形态是
// 「静默放行」，不是「误拦」。故本包自建这一层，**判据仍是 egress**。
//
// # 用法
//
// 最省事的是用本包的构造件（它把「守卫挂在传输层」这件事收成一行，
// 且保留宿主自己的 client/transport 注入口——见 client.go）：
//
//	hc, err := netguard.NewClient(nil, netguard.Allow{})
//
// 手搓传输层时（已有自己的 *http.Transport）：
//
//	hc := &http.Client{Transport: &http.Transport{
//	    DialContext: netguard.DialFunc(nil, netguard.DialOptions{
//	        Allow: netguard.Allow{Private: true},
//	    }),
//	}}
//
// # 与 CheckURL 的策略口径必须一致
//
// DialOptions 的放行面与校验层是**同一个类型**（Allow，见 allow.go）：
// 两者共用一份语义与一个到上游 egress 的转换点。
// **口径不一致的后果是分层放行**：校验层按「允许私网」放行、拨号层按缺省
// （禁私网）拒，表现为「配置明明允许内网，调用却失败」；反向则是校验层拒、
// 拨号层放行——后者才是安全问题。
//
// 逐 URL 的策略（同一个宿主里不同 URL 允许范围不同）**无法**在拨号层表达：
// 拨号是按连接的，校验是按 URL 的。此时取**拨号层最宽、校验层逐条**的组合：
// 拨号层放行校验层可能放行的全部范围（私网/回环按开关），但**不放行校验层
// 恒拒的段**（链路本地/元数据、保留段）——那些段的拒绝与开关无关，两层一致。

package netguard

import (
	"context"
	"net"
	"time"

	"github.com/oagentkit/guardx/egress"
	"github.com/oekit/pkg/errors"
)

// DialOptions 拨号层守卫的策略选项。
type DialOptions struct {
	// Allow 放行面——与校验层（CheckURLWith / CheckChainWith）**同一个类型**，
	// 同一份语义。零值即私网与回环全拒。
	Allow Allow
	// Dialer 底层拨号器。nil 时用带 10s 超时的 net.Dialer 缺省。
	Dialer *net.Dialer
	// Lookup 主机解析器。nil 时用 net.DefaultResolver.LookupIP；测试可注入
	// 假解析器来模拟「解析结果在校验与建连之间改变」。
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

// dialTimeout 缺省拨号超时。拨号层多了一次解析往返，故给一个明确上限——
// 无上限时一次卡住的解析会把调用方一起挂住。
const dialTimeout = 10 * time.Second

// DialFunc 返回包在传输层 DialContext 上的出站守卫。
//
// next 为 nil 时用缺省拨号器。返回值可直接赋给 http.Transport.DialContext。
func DialFunc(next func(ctx context.Context, network, addr string) (net.Conn, error), opts DialOptions) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dial := next
	if dial == nil {
		d := opts.Dialer
		if d == nil {
			d = &net.Dialer{Timeout: dialTimeout}
		}
		dial = d.DialContext
	}
	lookup := opts.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, errors.Wrap(err, "拨号地址非法", "addr", addr)
		}
		// 名字层：与 CheckURL 同一套名单。
		if err := checkHostName(host, opts.Allow.Loopback); err != nil {
			return nil, err
		}
		// 预检：解析出的每个 IP 都必须过 egress。
		if err := precheckIPs(ctx, host, lookup, opts); err != nil {
			return nil, err
		}
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// 端点核对：真实对端地址是唯一事实，与预检结果不符即拒并断开。
		if err := checkRemoteAddr(conn, opts); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// precheckIPs 预检：把主机名解析出的每一个 IP 过 egress。
//
// IP 字面量直接判（不解析）；域名解析后逐个判——**任一**被拒即整体拒。
// 逐个判而不是只看第一个：DNS 可以给多个 A 记录，其中一个是内网地址，
// 客户端挑到哪个取决于实现（Happy Eyeballs 等），只看第一个等于赌它挑对了。
func precheckIPs(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error), opts DialOptions) error {
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(host, ip, opts)
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return errors.Wrap(err, "出站目标解析失败", "host", host)
	}
	if len(ips) == 0 {
		return errors.Wrap(ErrBlockedName, "出站目标解析为空", "host", host)
	}
	for _, ip := range ips {
		if err := checkIP(host, ip, opts); err != nil {
			return err
		}
	}
	return nil
}

// checkRemoteAddr 端点核对：建连后核对真实对端地址。
//
// 这一步闭合的是「预检 → 建连」之间的窗口：DNS 在那之后改口，实际连上的
// 就不是预检过的那个地址。核对不通过则拒（并由调用方关闭连接）。
func checkRemoteAddr(conn net.Conn, opts DialOptions) error {
	if conn == nil || conn.RemoteAddr() == nil {
		return errors.Wrap(ErrBlockedName, "拨号结果无对端地址，无法核对")
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		// 对端地址形态异常：不猜，按拒处理。
		return errors.Wrap(err, "对端地址解析失败", "remote", conn.RemoteAddr().String())
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.Wrap(ErrBlockedName, "对端地址不是 IP 字面量，无法核对", "remote", host)
	}
	return checkIP(host, ip, opts)
}

// checkIP 单个 IP 过 egress（与 CheckURL 同一判据、同一开关）。
func checkIP(host string, ip net.IP, opts DialOptions) error {
	if err := egress.CheckHostWith(ip.String(), opts.Allow.egressOptions()); err != nil {
		return errors.Wrap(err, "出站目标已拒绝", "host", host, "ip", ip.String())
	}
	return nil
}
