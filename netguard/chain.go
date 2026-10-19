package netguard

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/oekit/pkg/errors"
)

// 重定向链的逐跳重验。
//
// ## 为什么 CheckURL 单点校验必然漏
//
// CheckURL 是**纯谓词**：给它一个 URL，它判这个 URL。但出网的实际路径往往不是
// 一个 URL——`http://safe.example/redirect?to=http://169.254.169.254/` 会把我们
// 送到云元数据。
//
// octop-harness 在这一点上踩过自己的坑：`web_fetch` 用了逐跳重验
// （follow_redirects=False 手工循环，每跳重跑校验），而同一份代码里的
// `media/http.py:116` 直接 `follow_redirects=True` 全程不校验。
// **同一个进程里一处防住了、一处没防住**——这说明单点校验必然被绕过，
// 要么逐跳，要么统一在拨号处拦。
//
// 本文件提供的是**纯判定件**：它不发起任何 HTTP 请求、不解析 Location 响应头，
// 只把「起始 URL + 逐跳的 Location 值」翻译成逐跳的校验结论。
// 谁去拨号、怎么循环，由宿主决定——内核不碰 I/O。

// DefaultMaxHops 缺省最大跳转跳数。超过即拒绝（防跳转环）。
const DefaultMaxHops = 10

// ErrTooManyHops 跳转跳数超限。
var ErrTooManyHops = errors.New("重定向跳数超限")

// ErrEmptyLocation 空 Location：**「链条到此终止」信号，不是「这一跳被拒」**。
//
// 3xx 响应没有 Location 时，HTTP 语义是不跟随、就把这个响应原样返回——参考实现
// `web_fetch.py:26-28` 正是这么做的（`location = (...).strip()`；`if not location:
// return response`）。此前 ResolveLocation(current, "") 会解析成 current 自己，
// 于是 CheckChain 把它当成「跳向自己」：白吃一跳预算并把同一个 URL 重验一遍。
// 更坏的是它把一个畸形的跳转链洗成了正常链——终止点之后的 hops 本该无人跟随，
// 现在却仍被逐跳校验，结论看上去一切正常，畸形链因此隐身。
var ErrEmptyLocation = errors.New("Location 为空，重定向链在此终止")

// IsTerminalLocation 该 Location 值表示「没有下一跳」（空或全空白）。
func IsTerminalLocation(location string) bool { return strings.TrimSpace(location) == "" }

// HopDeniedError 某一跳被拒。带跳号与该跳 URL，便于留痕定位是哪一跳出的问题
// （只报「URL 不合法」而不知道跳号，等于没报）。
type HopDeniedError struct {
	Hop int    // 1 = 起始 URL；2.. = 第 n-1 次跳转的目标
	URL string // 被拒的那一跳
	Err error  // 底层原因（CheckURL 的错误，或 ErrTooManyHops）
}

func (e *HopDeniedError) Error() string {
	return fmt.Sprintf("出站第 %d 跳被拒（%s）: %v", e.Hop, e.URL, e.Err)
}

func (e *HopDeniedError) Unwrap() error { return e.Err }

// ResolveLocation 把一跳的 Location 值解析成绝对 URL。
//
// 必须解析而不是直接送 CheckURL 的理由：Location 头**允许相对地址**
// （`/next`、`../up`、`//other.host/x`）。相对地址直接送 CheckURL 会因缺 host
// 被拒——那看似 fail-closed 正确，实际上会诱导调用方「只校验第一跳就放行后续」，
// 反而把防护降级了。协议相对地址 `//host/x` 更危险：它换了 host，
// 不解析就看不出跨主机跳转。
//
// 空 Location（空串或全空白）**不解析**为 current，而是返回 ErrEmptyLocation：
// 「没有下一跳」不是一个 URL，解析成「跳向自己」等于凭空造一跳。调用方据此终止
// 跟随（参考实现即把该 3xx 响应原样返回，不当作错误）。
func ResolveLocation(current, location string) (string, error) {
	if IsTerminalLocation(location) {
		return "", ErrEmptyLocation
	}
	cur, err := url.Parse(current)
	if err != nil {
		return "", fmt.Errorf("当前跳 URL 非法: %w", err)
	}
	ref, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("跳转 Location 非法: %w", err)
	}
	return cur.ResolveReference(ref).String(), nil
}

// CheckChain 逐跳重验：start 为起始 URL，hops 为按序的 Location 值（原始值，
// 可相对）。返回第一处被拒的跳，全部通过返回 nil。
//
// 每一跳都独立过 CheckURL（故每一跳都过名字层），且每一跳都在**解析之后**判定——
// 这同时挡掉「DNS rebinding 换主机」与「协议相对地址换主机」两类绕过。
//
// **空 Location 终止链条**：不跟随、不报错、不吃跳数预算，其后的 hops 不会被校验
// （它们本就不可达）。返回值 nil 的含义是「已校验的跳全部通过」，
// 不代表整条 hops 都被校验过——调用方不该在终止点之后再传值。
//
// maxHops <= 0 时取 DefaultMaxHops。hops **超过**上限即拒，不截断继续——
// 注意是「超过」而非「达到」：maxHops=10 表示允许 10 次跳转（起始之外），
// 第 11 次才拒。反过来写成 >= 会白吃一跳预算。
//
// ## 为什么先验起始跳、再谈预算
//
// HopDeniedError 的两个字段是给留痕用的，**必须描述一个真的校验过的 URL**。
// 先判预算的话，坏起始 URL + 超长链会报出 `Hop = maxHops+1`——那一跳从未被校验过，
// 留痕上读起来像「第 11 跳有结论」，实则整条链子的第一跳就已经是坏的。
// 两条错误都成立时，先报**真实且更早**的那条：起始跳非法是链的根因，
// 跳数超限可能只是根因的次生现象。两种结论都不丢，调用方拿到起始跳的 error
// 后重试或改链即可。
func CheckChain(start string, hops []string, allowPrivate, allowLoopback bool, maxHops int) error {
	if maxHops <= 0 {
		maxHops = DefaultMaxHops
	}
	// 先切到终止点之前：终止点之后的 hops 不可达，既不校验也不占预算。
	followed := hops
	if n := firstTerminalHop(hops); n >= 0 {
		followed = hops[:n]
	}
	if err := CheckURL(start, allowPrivate, allowLoopback); err != nil {
		return &HopDeniedError{Hop: 1, URL: start, Err: err}
	}
	if len(followed) > maxHops {
		return &HopDeniedError{Hop: maxHops + 1, URL: start, Err: ErrTooManyHops}
	}
	current := start
	for i, loc := range followed {
		next, err := ResolveLocation(current, loc)
		if errors.Is(err, ErrEmptyLocation) {
			// followed 已按同一判据切过，走不到这里；留着是为了让「终止=放行」
			// 这条契约在实现里只有一处语义，不依赖两处判据恰好一致。
			return nil
		}
		if err != nil {
			return &HopDeniedError{Hop: i + 2, URL: loc, Err: err}
		}
		if err := CheckURL(next, allowPrivate, allowLoopback); err != nil {
			return &HopDeniedError{Hop: i + 2, URL: next, Err: err}
		}
		current = next
	}
	return nil
}

// firstTerminalHop 返回首个终止跳下标，没有则 -1。
func firstTerminalHop(hops []string) int {
	for i, loc := range hops {
		if IsTerminalLocation(loc) {
			return i
		}
	}
	return -1
}
