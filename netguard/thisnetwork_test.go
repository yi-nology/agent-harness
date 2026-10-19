package netguard

import (
	"testing"

	"github.com/oekit/pkg/errors"
)

// TestThisNetworkRangeBlocked 钉住 0.0.0.0/8（RFC 1122 "this network"）整段被拒。
//
// ## 这条用例的来历
//
// 本包原先只有一条「未指定地址拒绝」用例，测的是 `http://0.0.0.0/x`——而全零地址
// 本来就能过 `netip.Addr.IsUnspecified()`，所以那条用例**一直是绿的，缺口却真实
// 存在**：`0.0.0.0/8` 不在 egress 的显式段表里，于是除全零之外的整段（`0.1.2.3`
// / `0.255.255.255` …）在 IP 判定层全放行——一条可用的 SSRF 路径。
//
// 2026-10-19 上游修复（oagentkit/guardx#2 → v0.4.1）把 `0.0.0.0/8` 补进
// `ipv4ExplicitBlocks`，本用例随之成立。留着它是为了让这条缺口**再也不会靠一条
// 只测全零地址的用例蒙过去**。
//
// 断言只落在「被拒」上，不绑具体错误类型：本包的 IP 判定单源委派 egress，拒它的
// 理由与形态由上游决定，本包不该把上游的实现细节钉成自己的契约。
func TestThisNetworkRangeBlocked(t *testing.T) {
	// `0.0.0.0` 这个**全零字面量**不在此列：上游刻意把它与回环同组、由
	// allowLoopback 管辖（「未指定地址」与回环一样是"本机"的两种写法），
	// allowLoopback=true 时放行。断言它「无条件拒」会与该契约冲突。
	//
	// 真正要钉的是 `/8` 余下整段：它们既不是回环也不是私网，AllowLoopback /
	// AllowPrivate 都碰不到，**必须无条件拒**——哪怕放行面参数全开。
	for _, u := range []string{
		"http://0.1.2.3/x",
		"http://0.255.255.255/x",
		"http://0.128.0.1/x",
		"http://0.0.0.1/x",
	} {
		if err := CheckURL(u, true, true); err == nil {
			t.Errorf("%s: this network 段应无条件拒（放行面参数全开也不例外），实得放行", u)
		}
	}

	// 全零字面量随旗标走，与回环同待遇——单独钉住，免得日后有人把
	// 「整段无条件拒」的错误理解顺带改到它头上。
	if err := CheckURL("http://0.0.0.0/x", true, false); err == nil {
		t.Error("0.0.0.0 在 allowLoopback=false 时应拒")
	}
}

// TestThisNetworkIPv4MappedBlocked IPv4-mapped 形态同样落在同一段内，不得绕过。
//
// 归一化若发生在判定之后，`::ffff:0.1.2.3` 就会以一个「看起来是 IPv6」的身份
// 逃过 v4 段表——这是 mapped 地址最经典的绕过形态，故单独钉一条。
func TestThisNetworkIPv4MappedBlocked(t *testing.T) {
	// URL 里的 IPv6 字面量必须带方括号；Hostname() 会摘掉，判定看到的是同一个 v4 值。
	for _, u := range []string{
		"http://[::ffff:0.1.2.3]/x",
		"http://[::ffff:0.255.255.255]/x",
	} {
		err := CheckURL(u, true, true)
		if err == nil {
			t.Errorf("%s: IPv4-mapped 形态应被拒，实得放行", u)
			continue
		}
		// 断言不是「随便哪个错」：解析器报错与策略拒绝混为一谈会让这条用例失去意义。
		// 这里只要求它不是 BlockedNameError 之外的静默通过——即必须真的判过 IP。
		var bn *BlockedNameError
		if errors.As(err, &bn) {
			t.Errorf("%s: 走的是名字层（%v），但该地址是 IP 字面量，期望走 IP 判定", u, err)
		}
	}
}
