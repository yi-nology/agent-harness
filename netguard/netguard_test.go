package netguard

import (
	"fmt"
	"sync"
	"testing"

	"github.com/oekit/pkg/errors"
)

func TestCheckURL(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		allowPrivate bool
		allowLoop    bool
		wantErr      bool
	}{
		{"外网 https 放行", "https://oapi.dingtalk.com/send?x=1", true, false, false},
		{"外网 http 放行", "http://example.com/hook", false, false, false},
		{"回环拒绝（外部输入面）", "http://127.0.0.1:8900/api", true, false, true},
		{"回环名拒绝", "http://localhost/x", true, false, true},
		{"回环放行（凭证探测）", "http://127.0.0.1:18940/metrics", true, true, false},
		{"云元数据拒绝", "http://169.254.169.254/latest/meta-data/", true, false, true},
		{"未指定地址拒绝", "http://0.0.0.0/x", true, false, true},
		{"私网放行（allowPrivate）", "http://192.168.1.10:8080/hook", true, false, false},
		{"私网拒绝（严格档）", "http://10.0.0.5/hook", false, false, true},
		{"非 http scheme 拒绝", "file:///etc/passwd", true, false, true},
		{"缺 host 拒绝", "http:///path", true, false, true},
	}
	for _, tc := range cases {
		err := CheckURL(tc.url, tc.allowPrivate, tc.allowLoop)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: CheckURL(%q) err=%v wantErr=%v", tc.name, tc.url, err, tc.wantErr)
		}
	}
}

// TestCheckURLBlockedNameBeforeDNS 名字层必须在 DNS 之前判。
//
// 断言点是错误类型：命中名字名单时必须给出 BlockedNameError，而不是
// 「解析失败」——这些名字在公网 DNS 上本来就解不出来，若结论来自解析器就说明
// 名字层没生效（或者只在解析成功时才判，等于给攻击者留了一条 DNS 可控的旁路）。
func TestCheckURLBlockedNameBeforeDNS(t *testing.T) {
	cases := []struct{ name, url string }{
		{"精确名（元数据）", "http://metadata.google.internal/x"},
		{"精确名（元数据别名）", "http://metadata.goog/x"},
		{"精确名（容器网桥）", "http://host.docker.internal/x"},
		{"精确名（容器网关）", "http://gateway.docker.internal/x"},
		{"后缀族（.internal）", "http://svc.internal/x"},
		{"后缀族（.local）", "http://printer.local/x"},
		{"后缀族（.lan）", "http://box.lan/x"},
		{"大小写与尾点归一化", "http://Metadata.Google.Internal./x"},
	}
	for _, tc := range cases {
		err := CheckURL(tc.url, false, false)
		var bn *BlockedNameError
		if !errors.As(err, &bn) {
			t.Errorf("%s: 应在解析前被名字层拒（BlockedNameError），实得 %v", tc.name, err)
			continue
		}
		if !errors.Is(err, ErrBlockedName) {
			t.Errorf("%s: 应 Unwrap 出 ErrBlockedName 哨兵，实得 %v", tc.name, err)
		}
		if bn.Host == "" {
			t.Errorf("%s: 拒绝结论应带 host 便于留痕", tc.name)
		}
	}
}

// TestCheckURLBlockedNameIgnoresAllowFlags 非回环族名单项不看放行旗标：
// allowPrivate/allowLoopback 都为 true（放行面参数）时依然拒绝。
// 这条断言钉住「allowPrivate 只放行私网地址、不放行私有命名空间」的口径。
//
// 断言必须落在「拒绝来自名字层」上（ErrBlockedName），不能只断言「有错」——
// 名单里的名字在真实 DNS 上大多也解不出来，只看有没有错的话，
// 「这些名字被改成旗标豁免、最终死在解析失败」也会蒙混过关。
func TestCheckURLBlockedNameIgnoresAllowFlags(t *testing.T) {
	for _, u := range []string{
		"http://metadata.google.internal/x",
		"http://host.docker.internal/x",
		"http://svc.internal/x",
		"http://printer.local/x",
		"http://box.lan/x",
	} {
		if err := CheckURL(u, true, true); !errors.Is(err, ErrBlockedName) {
			t.Errorf("%s: 放行面参数下仍应由名字层拒，实得 %v", u, err)
		}
	}
}

// TestCheckURLLoopbackFamilyFollowsAllowFlag 回环族名字是 127.0.0.1 的名字形式，
// 必须与回环地址同待遇：allowLoopback=true 放行、false 拒绝。
// 少了这条，新增名字层会把「allowLoopback=true 够不到自身服务」变成回归。
func TestCheckURLLoopbackFamilyFollowsAllowFlag(t *testing.T) {
	if err := CheckURL("http://localhost:9/x", true, true); err != nil {
		t.Fatalf("allowLoopback=true 时回环族名字应放行: %v", err)
	}
	if err := CheckURL("http://api.localhost:9/x", true, true); err != nil {
		t.Fatalf("allowLoopback=true 时 .localhost 后缀应放行: %v", err)
	}
	for _, u := range []string{"http://localhost/x", "http://api.localhost/x"} {
		if err := CheckURL(u, true, false); !errors.Is(err, ErrBlockedName) {
			t.Errorf("%s: allowLoopback=false 时应被名字层拒，实得 %v", u, err)
		}
	}
}

// TestCheckURLPublicNameAllowed 名字层不得误伤公网名（含大小写/尾点写法）。
func TestCheckURLPublicNameAllowed(t *testing.T) {
	for _, u := range []string{"https://example.com/hook", "http://EXAMPLE.com/x", "http://example.com./x"} {
		if err := CheckURL(u, false, false); err != nil {
			t.Errorf("%s: 公网名应放行，实得 %v", u, err)
		}
	}
}

// TestExtraBlockedNamesCannotDisableDefaults 扩展表与默认名单并集生效，
// 只能加不能减：宿主把扩展表改写成无关内容，也摘不掉默认名单。
func TestExtraBlockedNamesCannotDisableDefaults(t *testing.T) {
	defer func() {
		SetExtraBlockedHostnames()
		_ = SetExtraBlockedHostSuffixes()
	}()
	SetExtraBlockedHostnames("extra-blocked.example")
	if err := SetExtraBlockedHostSuffixes(".blocked-example"); err != nil {
		t.Fatalf("合法后缀不应被拒: %v", err)
	}

	if err := CheckURL("http://extra-blocked.example/x", false, false); !errors.Is(err, ErrBlockedName) {
		t.Errorf("扩展精确名单应生效，实得 %v", err)
	}
	if err := CheckURL("http://a.blocked-example/x", false, false); !errors.Is(err, ErrBlockedName) {
		t.Errorf("扩展后缀名单应生效，实得 %v", err)
	}
	// 默认名单一个都不能少。断言同样落在 ErrBlockedName 上：
	// 只看「有错」的话，扩展表被改成可覆盖默认时不一定暴露得出来。
	for _, u := range []string{
		"http://metadata.google.internal/x", "http://metadata.goog/x",
		"http://host.docker.internal/x", "http://gateway.docker.internal/x",
		"http://svc.internal/x", "http://printer.local/x", "http://box.lan/x",
	} {
		if err := CheckURL(u, false, false); !errors.Is(err, ErrBlockedName) {
			t.Errorf("%s: 扩展表不应能摘掉默认名单项，实得 %v", u, err)
		}
	}
}

// TestExtraSuffixNormalizedOnInjection 后缀大小写/空白在**注入期**归一，
// 匹配侧只做 HasSuffix、不做任何清洗。
//
// `.CORP` 曾经**永不匹配**——挂着一个看着在拦、实际不拦的后缀，且没有任何信号。
// 对拦截名单来说这是最坏的失败模式：失效方向是「放行」，不是「误拦」。
func TestExtraSuffixNormalizedOnInjection(t *testing.T) {
	defer func() { _ = SetExtraBlockedHostSuffixes() }()
	if err := SetExtraBlockedHostSuffixes("  .CORP  ", "\t.Corp-Internal\n"); err != nil {
		t.Fatalf("合法后缀不应被拒: %v", err)
	}
	// 命中类断言走 CheckURL：这些名字在名字层就被拒，走不到解析，无 DNS 依赖。
	for _, u := range []string{
		"http://a.corp/x", "http://A.CORP/x", "http://svc.corp-internal/x",
	} {
		if err := CheckURL(u, false, false); !errors.Is(err, ErrBlockedName) {
			t.Errorf("%s: 归一化后应命中扩展后缀，实得 %v", u, err)
		}
	}
	// 反向断言走 checkHostName：未命中意味着还要继续解析，CheckURL 会真去查 DNS。
	// 这里要验的只是「后缀没有退化成子串/前缀匹配」，不必付网络的代价。
	for _, h := range []string{"corp.example.com", "notcorp-internal.example.com", "corp"} {
		if err := checkHostName(h, false); err != nil {
			t.Errorf("主机名 %q 不应被后缀命中（后缀不等于子串），实得 %v", h, err)
		}
	}
}

// TestExtraSuffixDotlessRejected 无前导点的后缀在**注入处**拒绝，不静默收下。
//
// 为什么拒绝而不是自动补点：默认表里每个后缀都带点，因为 `internal` 是顶级域，
// 不该因为 `foo.internal` 被拦就整片拒掉。`corp` 照单全收会把语义从「拦 foo.corp」
// 悄悄变成「拦一切以 corp 结尾的名字」——那比不拦更糟，误伤面不可预测。
// 同理拒绝归一化后永不匹配的写法（裸 `.` / 带尾点）：主机名判定前已去尾点，
// 这类后缀进去了也一条都拦不到，属同一类静默失效。
//
// 拒绝必须发生在调用点：等到匹配时才发现，这次注册早已悄悄放行了。
func TestExtraSuffixDotlessRejected(t *testing.T) {
	for _, bad := range []string{"corp", "", "   ", ".", ".corp.", "\tCORP\n", "corp "} {
		err := SetExtraBlockedHostSuffixes(bad)
		if !errors.Is(err, ErrInvalidBlockedSuffix) {
			t.Errorf("后缀 %q 应在注入处被拒（ErrInvalidBlockedSuffix），实得 %v", bad, err)
		}
	}
}

// TestExtraSuffixRejectionKeepsPrevious 校验不过就整批不装，旧名单原样保留。
// 半套名单比旧名单更难推理：调用方以为注册成功了，日后却查不出哪几条在拦。
func TestExtraSuffixRejectionKeepsPrevious(t *testing.T) {
	defer func() { _ = SetExtraBlockedHostSuffixes() }()
	if err := SetExtraBlockedHostSuffixes(".keep-example"); err != nil {
		t.Fatalf("前置注册失败: %v", err)
	}
	if err := SetExtraBlockedHostSuffixes(".good-example", "nodot"); !errors.Is(err, ErrInvalidBlockedSuffix) {
		t.Fatalf("含非法项的批次应整批被拒，实得 %v", err)
	}
	if err := CheckURL("http://a.good-example/x", false, false); err == nil {
		t.Error("被拒批次里的合法项不应被部分安装")
	}
	if err := CheckURL("http://a.keep-example/x", false, false); !errors.Is(err, ErrBlockedName) {
		t.Errorf("被拒批次不应动到上一份名单，实得 %v", err)
	}
}

// TestExtraHostnamesNormalizedOnInjection 精确名单与判定侧同用 normalizeHost
// （小写 + 去尾点），调用方不必自己保证大小写；空白项直接丢弃。
func TestExtraHostnamesNormalizedOnInjection(t *testing.T) {
	defer SetExtraBlockedHostnames()
	SetExtraBlockedHostnames("  Blocked.Example.  ", "exact-example", "", "   ")
	for _, h := range []string{"blocked.example", "BLOCKED.EXAMPLE.", "exact-example"} {
		if err := checkHostName(h, false); !errors.Is(err, ErrBlockedName) {
			t.Errorf("主机名 %q 归一化后应命中，实得 %v", h, err)
		}
	}
	if err := checkHostName("other-example", false); err != nil {
		t.Errorf("未注册名不应被拦，实得 %v", err)
	}
}

// TestExtraBlockedConcurrentSetAndClassify 并发注册 + 并发判定的竞态钉子。
//
// 扩展位曾是导出 `var`：注册（写）与 classifyHostName（读）并发即数据竞争，而
// map 的并发读写是 **fatal runtime error，recover 抓不住**——不是 panic，是进程直接没。
// 非 -race 下它大概率「看起来能跑」，所以这条断言只在 -race 下有意义；
// 但它必须长期留在库里，否则扩展位下次被人改回导出变量时没有网。
//
// 判定结果本身故意不断言：名单一直在换，任一时刻的结论都是合法的。要验的是
// 「并发下不崩、不竞争」。
func TestExtraBlockedConcurrentSetAndClassify(t *testing.T) {
	defer func() {
		SetExtraBlockedHostnames()
		_ = SetExtraBlockedHostSuffixes()
	}()
	const iterations = 200
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				SetExtraBlockedHostnames(fmt.Sprintf("blocked-%d.example", i))
				_ = SetExtraBlockedHostSuffixes(fmt.Sprintf(".blocked-%d.example", i))
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, _, _ = classifyHostName(fmt.Sprintf("host-%d.example", i))
				_ = checkHostName(fmt.Sprintf("host-%d.internal", i), false)
			}
		}()
	}
	wg.Wait()
}

// TestCheckChainBlockedNameEveryHop 名字层对每一跳都生效（CheckChain 逐跳过 CheckURL）。
func TestCheckChainBlockedNameEveryHop(t *testing.T) {
	err := CheckChain("https://example.com/start", []string{"http://svc.internal/x"}, false, false, 0)
	var hd *HopDeniedError
	if !errors.As(err, &hd) || hd.Hop != 2 {
		t.Fatalf("第二跳跳进拦截后缀应报 Hop=2，实得 %v", err)
	}
	if !errors.Is(err, ErrBlockedName) {
		t.Errorf("应能判定出是名字层拒绝（Unwrap 链完整），实得 %v", err)
	}
}
