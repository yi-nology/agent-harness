// dial_test.go —— 拨号层出站守卫的反向验证。
//
// # 为什么这条必须钉
//
// 拨号层存在的**全部理由**是闭合「预检 → 建连」之间的窗口：DNS 在那之后改口，
// 实际连上的就不是预检过的那个地址。只测「解析出来就被拒」是不够的——
// 那是校验层已经做到的事，拨号层的新增能力（**端点核对**）恰恰只在
// 「解析结果与真实对端不一致」时才被触发。
//
// 所以本文件的核心用例是：解析器给一个**能过的**地址，而拨号器连上另一个
// **该拒的**地址——此时必须拒，且连接必须被关闭（不断开等于把已建立的
// 通道交给调用方继续用）。

package netguard

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// fakeConn 可控对端地址的假连接。
type fakeConn struct {
	mu     sync.Mutex
	remote net.Addr
	closed bool
}

func (c *fakeConn) Read(b []byte) (int, error)  { return 0, errors.New("not implemented") }
func (c *fakeConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (c *fakeConn) LocalAddr() net.Addr                { return &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1} }
func (c *fakeConn) RemoteAddr() net.Addr               { c.mu.Lock(); defer c.mu.Unlock(); return c.remote }
func (c *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// addrOf 造一个形如 ip:port 的对端地址。
func addrOf(ip string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(ip), Port: 443}
}

// TestDialFuncRejectsRebinding rebinding 必须被拒，且连接被关闭。
//
// 场景：解析器给公网地址（预检放行），拨号器实际连上元数据段
// （169.254.169.254，链路本地恒拒）。这正是 DNS rebinding 的形状。
func TestDialFuncRejectsRebinding(t *testing.T) {
	conn := &fakeConn{remote: addrOf("169.254.169.254")}
	dialed := false
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = true
			return conn, nil
		},
		DialOptions{
			Allow: Allow{Private: true},
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil // 公网，预检放行
			},
		},
	)
	_, err := dial(context.Background(), "tcp", "evil.example:443")
	if err == nil {
		t.Fatal("rebinding（解析给公网、实际连上元数据段）必须被拒")
	}
	if !dialed {
		t.Fatal("预检放行了公网地址，拨号本应发生——用例没走到端点核对")
	}
	if !conn.isClosed() {
		t.Fatal("端点核对失败后必须关闭连接：不断开等于把已建立的通道交给调用方")
	}
}

// TestDialFuncAllowsConsistentPublic 解析与真实对端一致且合法时必须放行。
//
// 误报是门禁最致命的失败形态：把合法出站全拦掉，宿主的功能就没了。
func TestDialFuncAllowsConsistentPublic(t *testing.T) {
	conn := &fakeConn{remote: addrOf("93.184.216.34")}
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn, nil
		},
		DialOptions{
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
		},
	)
	got, err := dial(context.Background(), "tcp", "good.example:443")
	if err != nil {
		t.Fatalf("合法公网出站必须放行: %v", err)
	}
	if got == nil {
		t.Fatal("放行时必须返回连接")
	}
	if conn.isClosed() {
		t.Fatal("放行时不得关闭连接")
	}
}

// TestDialFuncRejectsBlockedResolvedIP 解析结果被拒时不得拨号。
//
// 预检的价值之一就是「连尝试都不做」——对着元数据地址建立 TCP 连接本身
// 就是一个可被观测的探测动作。
func TestDialFuncRejectsBlockedResolvedIP(t *testing.T) {
	dialed := false
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = true
			return &fakeConn{remote: addrOf("169.254.169.254")}, nil
		},
		DialOptions{
			Allow: Allow{Private: true},
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("169.254.169.254")}, nil
			},
		},
	)
	if _, err := dial(context.Background(), "tcp", "meta.example:443"); err == nil {
		t.Fatal("解析结果落在链路本地段必须被拒")
	}
	if dialed {
		t.Fatal("预检已拒时不得拨号（对着该地址建连本身就是一次可观测的探测）")
	}
}

// TestDialFuncRejectsAnyBlockedAmongMany 多个 A 记录里任一被拒即整体拒。
//
// 只看第一个解析结果是赌客户端挑中了好的那个——而挑哪个取决于实现
// （Happy Eyeballs 等），不由我们决定。
func TestDialFuncRejectsAnyBlockedAmongMany(t *testing.T) {
	dialed := false
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = true
			return &fakeConn{remote: addrOf("93.184.216.34")}, nil
		},
		DialOptions{
			Allow: Allow{Private: true},
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("169.254.169.254")}, nil
			},
		},
	)
	if _, err := dial(context.Background(), "tcp", "mixed.example:443"); err == nil {
		t.Fatal("多个解析结果中含被拒地址时必须整体拒")
	}
	if dialed {
		t.Fatal("预检已拒时不得拨号")
	}
}

// TestDialFuncRejectsBlockedName 名字层必须在解析之前就拒。
func TestDialFuncRejectsBlockedName(t *testing.T) {
	lookupCalled := false
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return &fakeConn{remote: addrOf("93.184.216.34")}, nil
		},
		DialOptions{
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				lookupCalled = true
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
		},
	)
	if _, err := dial(context.Background(), "tcp", "metadata.google.internal:80"); err == nil {
		t.Fatal("拦截名单里的主机名必须在解析前被拒")
	}
	if lookupCalled {
		t.Fatal("名字层已拒时不应再解析（省一次 DNS 往返）")
	}
}

// TestDialFuncPrivatePolicy 私网与回环按开关放行，口径与 CheckURL 一致。
func TestDialFuncPrivatePolicy(t *testing.T) {
	lookupPriv := func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.1.2.3")}, nil
	}
	mk := func(opts DialOptions) func(ctx context.Context, network, addr string) (net.Conn, error) {
		return DialFunc(
			func(ctx context.Context, network, addr string) (net.Conn, error) {
				return &fakeConn{remote: addrOf("10.1.2.3")}, nil
			},
			opts,
		)
	}
	// 允许私网 → 放行。
	d := mk(DialOptions{Allow: Allow{Private: true}, Lookup: lookupPriv})
	if _, err := d(context.Background(), "tcp", "intranet.example:80"); err != nil {
		t.Fatalf("AllowPrivate=true 时私网必须放行: %v", err)
	}
	// 不允许私网 → 拒。
	d2 := mk(DialOptions{Lookup: lookupPriv})
	if _, err := d2(context.Background(), "tcp", "intranet.example:80"); err == nil {
		t.Fatal("AllowPrivate=false 时私网必须被拒")
	}
	// 回环按开关。
	lookupLoop := func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	d3 := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return &fakeConn{remote: addrOf("127.0.0.1")}, nil
		},
		DialOptions{Lookup: lookupLoop},
	)
	if _, err := d3(context.Background(), "tcp", "local.example:80"); err == nil {
		t.Fatal("AllowLoopback=false 时回环必须被拒")
	}
	d4 := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return &fakeConn{remote: addrOf("127.0.0.1")}, nil
		},
		DialOptions{Allow: Allow{Loopback: true}, Lookup: lookupLoop},
	)
	if _, err := d4(context.Background(), "tcp", "local.example:80"); err != nil {
		t.Fatalf("AllowLoopback=true 时回环必须放行: %v", err)
	}
}

// TestDialFuncRejectsLiteralIP IP 字面量直接判，不走解析。
func TestDialFuncRejectsLiteralIP(t *testing.T) {
	lookupCalled := false
	dial := DialFunc(
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return &fakeConn{remote: addrOf("169.254.169.254")}, nil
		},
		DialOptions{
			Allow: Allow{Private: true},
			Lookup: func(ctx context.Context, host string) ([]net.IP, error) {
				lookupCalled = true
				return nil, nil
			},
		},
	)
	if _, err := dial(context.Background(), "tcp", "169.254.169.254:80"); err == nil {
		t.Fatal("IP 字面量落在链路本地段必须被拒")
	}
	if lookupCalled {
		t.Fatal("IP 字面量不应触发解析")
	}
}
