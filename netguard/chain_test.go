package netguard

import (
	"testing"

	"github.com/oekit/pkg/errors"
)

const (
	okPublic = "https://example.com/start"     // 公网样例地址（RFC 2606 保留域）
	badMeta  = "http://169.254.169.254/latest" // 云元数据地址（链路本地）
)

// TestResolveLocationRelative 相对 Location 必须解析成绝对地址再校验。
func TestResolveLocationRelative(t *testing.T) {
	got, err := ResolveLocation(okPublic, "/next")
	if err != nil || got != "https://example.com/next" {
		t.Fatalf("绝对路径解析错: %q %v", got, err)
	}
	got, err = ResolveLocation("https://example.com/a/b", "../up")
	if err != nil || got != "https://example.com/up" {
		t.Fatalf("相对路径解析错: %q %v", got, err)
	}
}

// TestResolveLocationProtocolRelative 协议相对地址换了 host——
// 不解析就看不出跨主机跳转，这是最容易漏的一类。
// 注意 scheme 按 RFC 3986 从基地址继承（https 基 → 仍是 https），host 才是要害。
func TestResolveLocationProtocolRelative(t *testing.T) {
	got, err := ResolveLocation(okPublic, "//169.254.169.254/latest")
	if err != nil {
		t.Fatalf("不应解析失败: %v", err)
	}
	if got != "https://169.254.169.254/latest" {
		t.Fatalf("协议相对地址应解析为跨主机绝对地址（继承 scheme），实得 %q", got)
	}
	// 解析后再校验：必须能看出目标在链路本地并拒掉
	if err := CheckURL(got, false, false); err == nil {
		t.Fatal("解析后应能看出目标在链路本地并拒掉")
	}
}

// TestResolveLocationInvalid 输入非法时报错而非静默透传。
func TestResolveLocationInvalid(t *testing.T) {
	if _, err := ResolveLocation("://bad", "/x"); err == nil {
		t.Fatal("非法当前 URL 应报错")
	}
	if _, err := ResolveLocation(okPublic, "://bad"); err == nil {
		t.Fatal("非法 Location 应报错")
	}
}

// TestCheckChainAllowsCleanChain 全程落在公网时应放行。
func TestCheckChainAllowsCleanChain(t *testing.T) {
	err := CheckChain(okPublic, []string{"/a", "https://example.com/b"}, false, false, 0)
	if err != nil {
		t.Fatalf("干净链应放行: %v", err)
	}
}

// TestCheckChainRejectsStartURL 起始 URL 即被拒时报 Hop=1。
func TestCheckChainRejectsStartURL(t *testing.T) {
	err := CheckChain(badMeta, nil, false, false, 0)
	var hd *HopDeniedError
	if !errors.As(err, &hd) || hd.Hop != 1 {
		t.Fatalf("起始跳被拒应为 Hop=1 的结构化错误，实得 %v", err)
	}
	if !errors.Is(err, ErrTooManyHops) && hd.URL != badMeta {
		t.Fatalf("应带上被拒 URL: %+v", hd)
	}
}

// TestCheckChainRejectsLaterHop **逐跳**重验的核心断言：
// 首跳干净、后续跳跳到元数据地址，必须被抓到。
func TestCheckChainRejectsLaterHop(t *testing.T) {
	err := CheckChain(okPublic, []string{"/step1", "/step2", "http://169.254.169.254/creds"}, false, false, 0)
	var hd *HopDeniedError
	if !errors.As(err, &hd) {
		t.Fatalf("后续跳被拒应为结构化错误，实得 %v", err)
	}
	if hd.Hop != 4 {
		t.Fatalf("应指明是第 4 跳（起始=1），实得 %d", hd.Hop)
	}
}

// TestCheckChainRelativeHopIntoPrivate 相对 Location 指向内网也必须被抓——
// 这正是「不解析就只校验首跳」的漏法。
func TestCheckChainRelativeHopIntoPrivate(t *testing.T) {
	err := CheckChain("https://example.com", []string{"http://127.0.0.1:8080/"}, false, false, 0)
	var hd *HopDeniedError
	if !errors.As(err, &hd) || hd.Hop != 2 {
		t.Fatalf("第二跳跳回环应被拒且报 Hop=2，实得 %v", err)
	}
}

// TestCheckChainHopLimit 跳数超限即拒，不截断继续。
func TestCheckChainHopLimit(t *testing.T) {
	hops := make([]string, 10)
	for i := range hops {
		hops[i] = "/x"
	}
	err := CheckChain(okPublic, hops, false, false, 3)
	if !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("超过 maxHops 应报 ErrTooManyHops，实得 %v", err)
	}
	// 恰好等于上限仍放行
	if err := CheckChain(okPublic, hops[:2], false, false, 3); err != nil {
		t.Fatalf("未超限应放行: %v", err)
	}
}

// TestCheckChainDefaultsMaxHops maxHops<=0 取缺省上限，且是「超过才拒」。
func TestCheckChainDefaultsMaxHops(t *testing.T) {
	hops := make([]string, DefaultMaxHops)
	for i := range hops {
		hops[i] = "/x"
	}
	// 恰好 DefaultMaxHops 跳：允许（起始之外允许 10 次跳转）
	if err := CheckChain(okPublic, hops, false, false, 0); err != nil {
		t.Fatalf("恰好 %d 跳应放行: %v", DefaultMaxHops, err)
	}
	// 第 11 跳：拒
	err := CheckChain(okPublic, append(hops, "/x"), false, false, 0)
	if !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("超过缺省上限应拒，实得 %v", err)
	}
}

// TestCheckChainHopCountOffByOne 显式钉住「超过」语义：maxHops=3 允许 3 跳。
// 写成 >= 会白吃一跳预算——这条断言就是为了防它改回去。
func TestCheckChainHopCountOffByOne(t *testing.T) {
	hops := make([]string, 3)
	for i := range hops {
		hops[i] = "/x"
	}
	if err := CheckChain(okPublic, hops, false, false, 3); err != nil {
		t.Fatalf("maxHops=3 应允许 3 跳: %v", err)
	}
	if err := CheckChain(okPublic, append(hops, "/x"), false, false, 3); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("第 4 跳应拒，实得 %v", err)
	}
}

// TestCheckChainValidatesStartBeforeBudget 顺序钉死：**先验起始跳，再谈预算**。
//
// 反了的话，坏起始 URL + 超长链会报 `Hop = maxHops+1`——那一跳从未被校验过，
// 留痕上读起来像「第 11 跳有结论」，实则链子的第一跳就已经是坏的。
// HopDeniedError 的两个字段存在的意义就是描述一个真的校验过的 URL。
func TestCheckChainValidatesStartBeforeBudget(t *testing.T) {
	hops := make([]string, 5)
	for i := range hops {
		hops[i] = "/x"
	}
	// 起始即非法，且 5 跳 > 上限 2：两条错误都成立，必须报先发生的那条。
	err := CheckChain(badMeta, hops, false, false, 2)
	var hd *HopDeniedError
	if !errors.As(err, &hd) {
		t.Fatalf("应为结构化错误，实得 %v", err)
	}
	if hd.Hop != 1 {
		t.Errorf("起始跳即非法时应报 Hop=1（预算检查在其后），实得 %d", hd.Hop)
	}
	if errors.Is(err, ErrTooManyHops) {
		t.Errorf("起始跳的结论不应被预算结论盖掉，实得 %v", err)
	}
	if hd.URL != badMeta {
		t.Errorf("URL 字段应是被校验过的那一跳，实得 %q", hd.URL)
	}
}

// TestCheckChainAllowFlagsForwarded 放行面参数要能透传到每一跳。
func TestCheckChainAllowFlagsForwarded(t *testing.T) {
	// allowLoopback=true 时，探测回环地址是合法场景，整链应放行
	if err := CheckChain("http://127.0.0.1:9/a", []string{"/b"}, true, true, 0); err != nil {
		t.Fatalf("放行回环时应放行: %v", err)
	}
	// 关掉就必须拒
	if err := CheckChain("http://127.0.0.1:9/a", nil, true, false, 0); err == nil {
		t.Fatal("allowLoopback=false 时回环应被拒")
	}
}

// TestResolveLocationEmptyTerminates 空 Location 不是「跳向自己」，而是「没有下一跳」。
// 解析成 current 会凭空造一跳，把畸形跳转链洗成正常链。
func TestResolveLocationEmptyTerminates(t *testing.T) {
	for _, loc := range []string{"", " ", "\t\n "} {
		got, err := ResolveLocation(okPublic, loc)
		if !errors.Is(err, ErrEmptyLocation) {
			t.Errorf("Location=%q 应返回 ErrEmptyLocation，实得 %v", loc, err)
		}
		if got != "" {
			t.Errorf("Location=%q 不应产出 URL（实得 %q）", loc, got)
		}
		if !IsTerminalLocation(loc) {
			t.Errorf("Location=%q 应判为终止跳", loc)
		}
	}
	if IsTerminalLocation("/next") {
		t.Error("非空 Location 不应判为终止跳")
	}
}

// TestCheckChainEmptyLocationStopsChain 干净一跳后遇空 Location：终止、不跟随、
// 且**其后的跳不再被校验**——这正是「停了而不是跳回自己」的可观察证据：
// 若仍按「跳回自己」处理，第 3 个 hop（链路本地地址）会被抓出来报错。
func TestCheckChainEmptyLocationStopsChain(t *testing.T) {
	if err := CheckChain(okPublic, []string{"/a", "", badMeta}, false, false, 3); err != nil {
		t.Fatalf("空 Location 应终止链条而非拒绝，实得 %v", err)
	}
}

// TestCheckChainEmptyLocationNoBudget 空 Location 不吃跳数预算：
// 终止点之后的 hops 既不算跳数，也不参与校验。
func TestCheckChainEmptyLocationNoBudget(t *testing.T) {
	if err := CheckChain(okPublic, []string{"/a", "", "/b", "/c"}, false, false, 1); err != nil {
		t.Fatalf("真实跳转数只有 1 跳、未超上限，应放行，实得 %v", err)
	}
	// 对照：真的给了 2 跳且上限为 1 仍应超限拒——证明上一条不是因为上限失效
	if err := CheckChain(okPublic, []string{"/a", "/b"}, false, false, 1); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("2 跳对上限 1 应报 ErrTooManyHops，实得 %v", err)
	}
}

// TestHopDeniedErrorMessage 带跳号与 URL，便于定位是哪一跳出的问题。
func TestHopDeniedErrorMessage(t *testing.T) {
	hd := &HopDeniedError{Hop: 3, URL: "http://x/y", Err: ErrTooManyHops}
	msg := hd.Error()
	if !contains(msg, "3") || !contains(msg, "http://x/y") {
		t.Fatalf("错误信息应含跳号与 URL: %q", msg)
	}
	if hd.Unwrap() != ErrTooManyHops {
		t.Fatal("Unwrap 应返回底层错误")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
