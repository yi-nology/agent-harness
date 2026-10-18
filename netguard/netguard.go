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
package netguard

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync/atomic"

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
// 与默认名单**并集**生效，只能加不能减；默认名单刻意不做成可写变量：一个 `=` 赋值
// 就能把元数据名拦截悄悄摘掉，而这种改动没有任何编译期或运行期信号。确有例外需求，
// 说明这条出站链路不该走共享闸门，应由宿主在自己的连接配置上收紧。
//
// ## 为什么扩展位也是函数、不是导出变量
//
// 这不是风格问题，是两个各自足以致命的问题：
//
//  1. **并发读写是 fatal，不是 panic**。classifyHostName 在每条出站链路上被调用，
//     而注册方在装配期写。导出 `map[string]struct{}` 让这两个动作直接构成数据竞争——
//     map 的并发读写是 runtime fatal error，`recover` **抓不住**，整个进程当场没了。
//     即便换成 `[]string` 也仍是数据竞争（切片头三字 + 底层数组一起被读）。
//  2. **「=` 赋值即静默摘除」正是默认名单拒绝的形状**。上一条注释里写的理由
//     （悄悄摘掉拦截、无任何信号）对扩展位同样成立，却原样发给了调用方。
//
// 故走 atomic.Pointer：读侧一次原子取到快照，写侧整体替换一份新容器，
// 读侧永远看不到「改了一半」的名单。写侧仍是**整体替换**（不是逐项 mutate），
// 这样快照不可变，替换就是一次指针交换。
var (
	extraBlockedHostnames    atomic.Pointer[map[string]struct{}]
	extraBlockedHostSuffixes atomic.Pointer[[]string]
)

// SetExtraBlockedHostnames 注册/清空扩展精确主机名名单（不传即清空）。
// 整体替换而非累加：重复调用覆盖上一次，故「清空」就是再调一次空参。
//
// 归一化与判定侧同口径（小写 + 去尾点 + 去空白），调用方不必自己保证大小写——
// 与默认名单一样，`Foo.Example.` 和 `foo.example` 是同一个名字。空项直接丢弃。
func SetExtraBlockedHostnames(names ...string) {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = normalizeHost(strings.TrimSpace(n)); n != "" {
			m[n] = struct{}{}
		}
	}
	extraBlockedHostnames.Store(&m)
}

// ErrInvalidBlockedSuffix 扩展后缀非法（注入期拒绝）。errors.Is 判定。
var ErrInvalidBlockedSuffix = errors.New("扩展拦截后缀非法")

// SetExtraBlockedHostSuffixes 注册/清空扩展后缀名单（不传即清空）。
//
// ## 归一化与拒绝规则，以及为什么都在**注入时**做
//
// 名单「不命中」是这个拦截面最坏的失败模式：`.CORP` 静默不匹配，等于挂了一个
// 看着在拦、实际不拦的后缀，而且没有任何信号。所以：
//
//   - 大小写 / 空白：**一律在这里归一**，匹配侧只做 HasSuffix，不做任何清洗。
//   - **无前导点的后缀：拒绝**（返回 ErrInvalidBlockedSuffix，不安装）。
//     默认表里每个后缀都带点，因为 `internal` 是顶级域，不该因为 `foo.internal`
//     被拦就整片拒掉；`corp` 这种写法若照单全收，语义会从「拦 foo.corp」悄悄
//     变成「拦一切以 corp 结尾的名字」——比不拦更糟。与其在匹配时补点，不如
//     在调用点当场报错。
//   - **归一化后永不匹配的后缀：拒绝**（裸 `.`、以 `.` 结尾）。主机名判定前已去
//     尾点，这两种写法进去了也一条都拦不到，属于同一类静默失效。
//
// 拒绝发生在注入处而非匹配处：调用方在装配期就能拿到 error；等到某天发现
// 「这批主机名一直没被拦」再回头查，已经无从追溯是哪次注册写错了。
//
// 全量校验通过后才替换：任一项非法则**旧名单原样保留**，不做部分应用——
// 半套名单比旧名单更难推理。
func SetExtraBlockedHostSuffixes(suffixes ...string) error {
	norm := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		switch {
		case s == "":
			return errors.Wrap(ErrInvalidBlockedSuffix, "扩展拦截后缀为空")
		case !strings.HasPrefix(s, "."):
			return errors.Wrap(ErrInvalidBlockedSuffix,
				"扩展拦截后缀必须带前导点（无点会把顶级域整片拒掉）", "suffix", s)
		case s == "." || strings.HasSuffix(s, "."):
			return errors.Wrap(ErrInvalidBlockedSuffix,
				"扩展拦截后缀归一化后永不匹配（尾点已去）", "suffix", s)
		}
		norm = append(norm, s)
	}
	extraBlockedHostSuffixes.Store(&norm)
	return nil
}

// ErrBlockedName 主机名命中名字层拦截名单。
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
// 扩展位一律走原子取快照：读到的 map/切片在本次判定内**不被任何写侧触碰**
// （写侧只整体替换新容器），故拿到指针后可以安全遍历，无需再加锁。
func classifyHostName(h string) (blocked, loopbackFamily bool, reason string) {
	if h == "" {
		return false, false, ""
	}
	if _, ok := defaultBlockedHostnames[h]; ok {
		return true, false, "精确名单"
	}
	if names := extraBlockedHostnames.Load(); names != nil {
		if _, ok := (*names)[h]; ok {
			return true, false, "扩展精确名单"
		}
	}
	if _, ok := loopbackHostnames[h]; ok {
		return true, true, "回环族名字"
	}
	for _, sfx := range defaultBlockedHostSuffixes {
		if strings.HasSuffix(h, sfx) {
			return true, isLoopbackSuffix(sfx), "后缀 " + sfx
		}
	}
	if sfx := extraBlockedHostSuffixes.Load(); sfx != nil {
		for _, s := range *sfx {
			if strings.HasSuffix(h, s) {
				return true, false, "扩展后缀 " + s
			}
		}
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
