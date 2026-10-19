// Package netguard 出站 URL 守卫（SSRF 防护）：告警回推、凭证连通性探测、IM 推送等
// 「平台代发 HTTP」的出口统一过闸——scheme 白名单 + 目标地址解析后逐 IP 过策略
// （IP 策略单源 oagentkit/guardx/egress：回环/未指定按 allowLoopback、RFC1918 私网按
// allowPrivate 放行，链路本地（含云元数据）/组播/CGNAT/保留段一律拒绝——放行面
// 只覆盖「目标在内网/本机是合法场景」的那部分；CGNAT 等保留段自此比旧版收紧）。
// allowLoopback=false / allowPrivate=false 时对应族一并拒绝（告警回推/IM 推送等
// 外部输入面）；凭证连通性探测传 (true, true)——探测本机/内网自建服务是能力本身，
// 且写面已收 admin。
//
// 已知局限（纵深取舍）：DNS 解析与实际拨号之间存在 TOCTOU（rebinding 可绕过解析期
// 检查）；要根除需 Transport DialContext Control 钩子在连接建立时复核对端地址，本批
// 先拦住「字面量私网/元数据地址」这一渗透测试主路径。域名只在此处解析并逐 IP 判定
// （不把域名交给 egress——其「localhost 域恒拒」是 LLM 可控 URL 的字面量层语义，
// 本包 allowLoopback=true 的自服务探测是合法场景，经解析后按 IP 过闸即可）。
//
// 名字层（见文件下半段）：IP 策略只能看见「解析出来的地址」，而解析本身就是攻击面。
// 故在解析之前先按名字拒一遍——省一次解析，且判定不依赖 DNS 结果。
//
// # 与上游 urlguard 的分工（oekit/pkg v0.15.0 起有此包）
//
// IP 策略**继续单源 egress**，不因 urlguard 出现而改判：逐条比过两处清单，urlguard 的
// blockedCIDRs 是向 egress 的 ipv4ExplicitBlocks **看齐的收敛版**（少了 240.0.0.0/4
// 保留段），换过去是放松防线而不是收紧。
//
// 但**域名后缀层如今有两份**：本包是 .local/.localhost/.internal/.lan，urlguard 是
// localhost/internal/local/localdomain——`.lan` 本包有它没有、`.localdomain` 它有
// 本包没有。两份各自演化就是静默漂移，所以增删后缀前先比对上面两份：确属缺口就改
// 本包这处名单，**不要把 urlguard 引进来当第二份权威**。
//
// urlguard 的 `DialFunc(next)` 恰好能闭合上面「已知局限」记的 rebinding 窗口（预检
// 每个解析 IP + 建连后核对真实 RemoteAddr）。但它是 transport 包装，而本包是刻意的
// 纯判定件、不发起任何 I/O——宿主在 DialContext 上接不接是**接线决策**，不是本包加一个
// 函数的能事，故此处只记关系、不代做。
//
// # 本包不在工具调用的判定环上，这是有意的
//
// 本包判的是「这个 URL 能不能发出去」；工具调用的判定环（oagentkit/toolgate 的
// invoke.Stage）判的是「这次调用要不要放行」。两者是**不同的执行点**，不要把本包
// 做成逐调用的一环——那需要去猜「哪个参数字段是 URL」，是领域知识，不该进内核。
//
// 这条分工成立的前提是：程序化执行（ptc 沙箱）里**程序本身够不着网络**——无 fetch /
// XMLHttpRequest / WebSocket，出站只可能发生在宿主绑定的实现里，而那是宿主自己的代码。
// 该前提由 ptc/sandbox_test.go 的用例钉住；哪天沙箱里出现了网络原语，就得重新回答
// 「本包该接在哪」，而不是让程序自己就能出站。
package netguard

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/oagentkit/guardx/egress"
	"github.com/oekit/pkg/errors"
)

// CheckURL 校验出站 URL 是否允许平台代发。allowPrivate=false 时 RFC1918 私网一并拒绝；
// allowLoopback=false 时回环/未指定地址一并拒绝（告警回推/IM 推送等外部输入面）；
// 凭证连通性探测传 (true, true)——探测本机/内网自建服务是能力本身，且写面已收 admin。
func CheckURL(rawURL string, allowPrivate, allowLoopback bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.Wrap(err, "出站 URL 非法")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("出站 scheme %q 不在白名单（仅 http/https）", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("出站 URL 缺 host")
	}
	// 名字层必须在解析之前跑：判定不依赖 DNS 结果，且省掉一次解析。
	// CheckChain 逐跳调用本函数，故每一跳都过名字层。
	if err := checkHostName(host, allowLoopback); err != nil {
		return err
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else if resolved, err := net.LookupIP(host); err != nil {
		return errors.Wrap(err, "出站目标解析失败", "host", host)
	} else {
		ips = resolved
	}
	opts := egress.Options{AllowPrivate: allowPrivate, AllowLoopback: allowLoopback}
	for _, ip := range ips {
		if err := egress.CheckHostWith(ip.String(), opts); err != nil {
			return errors.Wrap(err, "出站目标已拒绝", "host", host)
		}
	}
	return nil
}

// ── 名字层：DNS 解析之前按名字拒 ──
//
// ## 为什么解析之后判不够
//
// IP 策略（egress）单看「解析出来的地址」，而解析本身就是攻击面：内网约定名
// （.internal/.local/.lan）可以在公网 DNS 上被指向任意**公网**地址——
// 「解析结果是公网地址」并不等于「这是一个公网服务」。元数据类主机名更是
// 连查都不该查。故名字层跑在解析之前：判定不依赖 DNS 结果，且省一次解析。
//
// ## 与两个放行旗标的关系（唯一口径）
//
//   - 回环族名字（localhost / *.localhost）是 127.0.0.1 的名字形式，与回环地址
//     **同待遇**：allowLoopback=true 放行（探测自身自建服务是合法入口，写面已收
//     admin），allowLoopback=false 拒绝。这样本包 allowLoopback=true 的场景不会
//     因为新增名字层而够不到目标。
//   - 其余名单项（元数据名、容器网桥名、.internal/.local/.lan 后缀）**无条件拒绝**，
//     不看旗标。理由：allowPrivate 的语义是「放行 RFC1918 私网**地址**」，不是
//     「放行任意私有命名空间的**主机名**」；把它做成可豁免，等于把一个地址级承诺
//     偷偷升成命名空间级承诺，名字层当场失去存在意义。
var (
	// defaultBlockedHostnames 精确主机名名单（回环族不在此表，见 loopbackHostnames）。
	defaultBlockedHostnames = map[string]struct{}{
		"metadata.google.internal": {},
		"metadata.goog":            {},
		"host.docker.internal":     {},
		"gateway.docker.internal":  {},
	}
	// defaultBlockedHostSuffixes 非公网命名空间后缀。一律带前导点号：`internal`
	// 是顶级域，不该因为 `foo.internal` 被拦就整片拒掉。
	defaultBlockedHostSuffixes = []string{".local", ".localhost", ".internal", ".lan"}
	// loopbackHostnames / loopbackHostSuffixes 回环族名字——唯一受 allowLoopback 影响的分组。
	loopbackHostnames    = map[string]struct{}{"localhost": {}}
	loopbackHostSuffixes = []string{".localhost"}
)

// ── 宿主扩展位 ──
//
// 扩展位（精确主机名 + 非公网命名空间后缀）与默认名单**并集**生效，只能加不能减。
// 判据与交换语义全部在 policy.go：本文件只负责读，读的时候**一次取快照**。
//
// 为什么读侧必须只取一次：策略是「主机名 + 后缀」两份数据合起来的，两次 Load
// 落在不同时刻，热重载的中间态就会被读成撕裂策略——每个字段都原子，组合起来
// 的那一瞬间不是。详见 policy.go 的文件头。

var ErrBlockedName = errors.New("出站主机名命中拦截名单")

// BlockedNameError 名字层拒绝结论。带 host 与命中原因，便于留痕判断是哪个名、
// 按哪条规则拒的；Unwrap 出哨兵让调用方能 errors.Is 统一判定。
type BlockedNameError struct {
	Host   string
	Reason string
}

func (e *BlockedNameError) Error() string {
	return fmt.Sprintf("出站主机名 %q 命中拦截名单（%s）", e.Host, e.Reason)
}

func (e *BlockedNameError) Unwrap() error { return ErrBlockedName }

// normalizeHost 归一化待判定主机名：小写 + 去尾点。url.Parse 两件事都不做，
// 而 `Svc.INTERNAL.` 与 `svc.internal` 是同一个名字。
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// checkHostName 名字层判定。allowLoopback 只对回环族名字有效，见本文件上半段口径。
func checkHostName(host string, allowLoopback bool) error {
	h := normalizeHost(host)
	blocked, loopbackFamily, reason := classifyHostName(h)
	if !blocked || (loopbackFamily && allowLoopback) {
		return nil
	}
	return &BlockedNameError{Host: h, Reason: reason}
}

// classifyHostName 返回 (是否拦截, 是否回环族, 命中原因)。
//
// 扩展位**一次取快照**，本次判定全程用这一份：主机名与后缀来自同一次 Load，
// 因此读不到「新主机名 + 旧后缀」的撕裂策略。取到的 Policy 在其生命周期内
// 不可被写侧触碰（写侧只整体替换新实例），故拿到指针后可安全遍历，无需再加锁。
func classifyHostName(h string) (blocked, loopbackFamily bool, reason string) {
	if h == "" {
		return false, false, ""
	}
	if _, ok := defaultBlockedHostnames[h]; ok {
		return true, false, "精确名单"
	}
	p := CurrentPolicy()
	if p.BlocksHostname(h) {
		return true, false, "扩展精确名单"
	}
	if _, ok := loopbackHostnames[h]; ok {
		return true, true, "回环族名字"
	}
	for _, sfx := range defaultBlockedHostSuffixes {
		if strings.HasSuffix(h, sfx) {
			return true, isLoopbackSuffix(sfx), "后缀 " + sfx
		}
	}
	if sfx, hit := p.BlocksSuffix(h); hit {
		return true, false, "扩展后缀 " + sfx
	}
	return false, false, ""
}

func isLoopbackSuffix(suffix string) bool {
	for _, s := range loopbackHostSuffixes {
		if s == suffix {
			return true
		}
	}
	return false
}
