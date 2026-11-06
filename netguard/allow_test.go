// allow_test.go —— 放行词汇表（Allow）的用例。
//
// # 为什么这些用例必须存在
//
// **校验层与拨号层必须同口径**（issue #6 的行动项）。两层各判各的时，
// 不一致的后果是分层放行：校验层拒、拨号层放行——**后者才是安全问题**。
// 这条判据无法靠文档保证，只能靠用例钉住。
//
// 历史：本文件曾用一整条用例钉「双 bool 包装与具名形态逐字等价」——
// 那条包装已在 2026-11-06 的破坏性重构里删除（双 bool 的位序写反不会编译报错、
// 也不会告警，只会静默改变放行面）。包装既已不存在，等价的唯一形态就是
// `Allow` 本身。

package netguard

import (
	"net"
	"testing"
)

// TestAllowZeroValueIsStrict 零值必须是严的。
//
// 放行面是安全边界，默认就该是关的：调用方忘了传放行面时，判「拒」而不是
// 「放行」。这条钉住的是默认值方向——写反了就是一条默认放行的 SSRF 路径。
func TestAllowZeroValueIsStrict(t *testing.T) {
	var zero Allow
	if zero.Private || zero.Loopback {
		t.Fatalf("Allow 零值必须全拒，实得 %+v", zero)
	}
	if err := CheckURLWith("http://127.0.0.1/x", zero); err == nil {
		t.Fatal("零值放行面下回环必须被拒")
	}
	if err := CheckURLWith("http://10.0.0.5/x", zero); err == nil {
		t.Fatal("零值放行面下私网必须被拒")
	}
}

// TestAllowSelfServiceNamesTheIntent AllowSelfService 的两个开关都要开。
//
// 它存在的意义是**点名意图**：这组比特也是 SSRF 穿透的入口，而名字让 review
// 时看得见「为什么放行」。若将来有人改了这个函数（少开一个开关），放行面会
// 静默变严——探测自建服务的调用点会开始失败，而这里会红。
func TestAllowSelfServiceNamesTheIntent(t *testing.T) {
	a := AllowSelfService()
	if !a.Private || !a.Loopback {
		t.Fatalf("AllowSelfService 必须同时放行私网与回环，实得 %+v", a)
	}
	if err := CheckURLWith("http://127.0.0.1/x", a); err != nil {
		t.Fatalf("AllowSelfService 下回环必须放行: %v", err)
	}
	if err := CheckURLWith("http://10.0.0.5/x", a); err != nil {
		t.Fatalf("AllowSelfService 下私网必须放行: %v", err)
	}
	// 但元数据段与保留段**与开关无关**，一律拒——放行面不是「什么都放行」。
	if err := CheckURLWith("http://169.254.169.254/x", a); err == nil {
		t.Fatal("元数据段必须与放行面无关地拒绝")
	}
}

// TestValidationAndDialLayersAgree 校验层与拨号层对同一地址必须同判。
//
// 这是 issue #6 那条「两个开关必须同口径」的**可执行形态**：同一个 Allow 值下，
// 校验层（CheckURLWith）与拨号层（checkIP）对同一组地址的结论必须一致。
//
// 两层各判各的时，这个用例会红——而这正是「分层放行」在落地前的最后一个
// 可发现点：一旦接进宿主，差异只表现为「配置允许内网却调用失败」或更糟的
// 「校验层拒、拨号层放行」。
func TestValidationAndDialLayersAgree(t *testing.T) {
	addrs := []string{
		"127.0.0.1",       // 回环
		"10.0.0.5",        // 私网
		"172.16.0.9",      // 私网
		"169.254.1.1",     // 链路本地
		"169.254.169.254", // 云元数据
		"100.64.0.1",      // CGNAT
		"8.8.8.8",         // 公网
	}
	allows := []Allow{
		{},
		{Private: true},
		{Loopback: true},
		AllowSelfService(),
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			t.Fatalf("夹具地址非法: %q", addr)
		}
		for _, allow := range allows {
			vErr := CheckURLWith("http://"+addr+"/x", allow)
			dErr := checkIP(addr, ip, DialOptions{Allow: allow})
			if (vErr == nil) != (dErr == nil) {
				t.Fatalf("地址 %s 在放行面 %+v 下两层判定不一致：校验层=%v 拨号层=%v",
					addr, allow, vErr, dErr)
			}
		}
	}
}

// TestAllowMapsToEgressOnce 放行面到上游 egress 的映射只有一处。
//
// 本用例钉的是「单一转换点」这条纪律：若将来有人在别处又手拼了一份
// egress.Options{AllowPrivate: ..., AllowLoopback: ...}，映射就多了一份，
// 两处可能漂移。本用例不检查实现细节，只检查**行为**：Allow 的两个字段与
// egress 的两个字段必须一一对应（写反了就是放行面反了）。
func TestAllowMapsToEgressOnce(t *testing.T) {
	cases := []struct {
		allow Allow
		priv  bool
		loop  bool
	}{
		{Allow{}, false, false},
		{Allow{Private: true}, true, false},
		{Allow{Loopback: true}, false, true},
		{AllowSelfService(), true, true},
	}
	for _, c := range cases {
		opts := c.allow.egressOptions()
		if opts.AllowPrivate != c.priv || opts.AllowLoopback != c.loop {
			t.Fatalf("放行面 %+v 映射到 egress 错了：got priv=%v loop=%v want priv=%v loop=%v",
				c.allow, opts.AllowPrivate, opts.AllowLoopback, c.priv, c.loop)
		}
	}
}
