// Package sessionref 跨会话引用快照：把**另一个会话的结论**以**有界、带来源、
// 带失效点**的片段注入当前上下文。
//
// # 它补的是哪一段
//
// 本仓已有三件管上下文的机制，但它们都不管这件事：
//
//   - @budget@ 管「窗口内怎么压」（分段计量 + 压实）；
//   - @spill@ 管「输出超预算怎么溢出到别处」；
//   - @offload@ 管「大对象内联不下就外置」。
//
// 三者回答的都是「**同一份上下文**里内容放不下怎么办」。而「引用另一个会话的
// 结论」是另一个问题：那份内容**不在当前上下文里**，它的边界与保鲜度没有任何
// 机制面管。
//
// 不管的后果不是「引用得不好」，是**把当时的结论当成现在的结论用**：
// 引用了一条「构建是绿的」，而那次之后源会话又跑了三次、最后一次红了——
// 当前会话据此判定「可以发布」。这类错误在报告里读起来完全正常，
// 因为引用的那段话当时确实是真的。
//
// 故本包只做三件事，且都是**可机械判定**的：
//
//  1. **有界**：裁剪上限必须在采集时给定，且裁剪了要显式标出来（@Truncated@ +
//     @OriginalBytes@）。静默截断会让模型以为看到的是全文。
//  2. **带来源与失效点**：@Source@ / @SourceVersion@ / @CapturedAt@ / @ExpiresAt@
//     随快照一起走。**失效判定只需要这两个量**（现在几点、源现在是第几版）。
//  3. **状态可见**：渲染时把 @state=fresh|expired|stale@ 写进包裹体。
//
// # 为什么「源版本」是必要的一维
//
// 只判过期时间不够：TTL 是**采集者猜的**，而源会话是否已经前进是**事实**。
// 源前进了而 TTL 没过——这是最常见的形态（长 TTL + 源会话高频更新），
// 只靠时间判会一路判「有效」。
//
// 版本**回退**同样判 stale：回退意味着源被重建/换了一个（同一个 id 指向的
// 已经不是同一段历史），拿旧快照当新结论用与前进时一样危险。
//
// # 渲染不拒绝，只标注
//
// 过期时依然渲染，但 @state@ 写 @expired@。拒不渲染会让「引用过期」变成
// 「引用消失」——而宿主无从区分「本来就没引用」与「引用了但过期」，
// 于是它会去补一条新引用，或者干脆不问。标注则把决定权交回模型与宿主。
//
// # 防伪：标签由 reminder 统一包裹
//
// 快照体来自**别的会话**，是本包意义上的不可信内容。故渲染走 @reminder.Wrap@
// 统一包裹（不在这里另写一套转义——防伪实现只允许一份）。
//
// 已知边界（写明而不是假装覆盖）：包裹**标签**不可伪造（@<@ 已被转义），
// 但 header 行是纯文本，body 里若出现形状相同的 header 行，读者无法从文本上
// 分辨。故判定一律读**标签**的 source，不读 body 里的任何自称。
package sessionref

import (
	"fmt"
	"strings"
	"time"

	"github.com/oekit/pkg/encoding"
	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/stringx"

	"github.com/yi-nology/goagent-kernel/reminder"
)

// Bounds 采集时的裁剪上限。
//
// 两个上限同时生效（先按行、后按字节）：只限字节时，一个超长的单行仍会撑爆；
// 只限行时，一行可以是几兆。
type Bounds struct {
	// MaxBytes 正文字节上限（0 = 不限制）。
	MaxBytes int
	// MaxLines 正文行数上限（0 = 不限制）。
	MaxLines int
}

// Ref 一份跨会话引用快照（值类型，可安全复制与比较）。
type Ref struct {
	// Source 来源会话标识（渲染时进包裹标签，宿主的留痕键）。
	Source string
	// SourceVersion 采集时**源会话的版本号**（单调计数或等价序号）。
	//
	// 它是「快照新不新」的唯一事实来源：TTL 是采集者的猜测，源是否前进是事实。
	SourceVersion uint64
	// CapturedAt 采集时刻。
	CapturedAt time.Time
	// ExpiresAt 失效时刻（必须晚于 CapturedAt）。
	ExpiresAt time.Time
	// Body 已裁剪的正文字。
	Body string
	// Truncated 正文是否被裁剪过（静默截断会让模型以为看到了全文）。
	Truncated bool
	// OriginalBytes 裁剪前的正文字节数（未裁剪时等于 len(Body)）。
	OriginalBytes int
}

// State 快照相对「现在」的状态。
type State string

const (
	// StateFresh 未过期且源未变。
	StateFresh State = "fresh"
	// StateExpired 已过失效时刻。
	StateExpired State = "expired"
	// StateStale 源版本与采集时不一致（前进或回退）。
	StateStale State = "stale"
)

// Capture 采集一份快照。
//
// 自检项（任一不满足即报错，不静默降级）：
//
//   - Source 非空（没有来源的快照无法留痕，也无法判失效）；
//   - ExpiresAt 必须**晚于** CapturedAt（一出生就过期的快照是声明写错，
//     而它会一路渲染成 expired，看起来像「源那边有问题」）；
//   - Bounds 不得为负。
func Capture(source string, version uint64, at, expires time.Time, body string, b Bounds) (Ref, error) {
	if strings.TrimSpace(source) == "" {
		return Ref{}, errors.New("sessionref: 来源会话标识为空（没有来源的快照既无法留痕，也无法判失效）")
	}
	if !expires.After(at) {
		return Ref{}, errors.Errorf("sessionref: 失效时刻 %s 不晚于采集时刻 %s（这条声明一出生就过期）",
			expires.Format(time.RFC3339), at.Format(time.RFC3339))
	}
	if b.MaxBytes < 0 || b.MaxLines < 0 {
		return Ref{}, errors.New("sessionref: 裁剪上限不得为负（0 表示不限制，不是「全裁掉」）")
	}
	trimmed, truncated := cut(body, b)
	return Ref{
		Source:        source,
		SourceVersion: version,
		CapturedAt:    at,
		ExpiresAt:     expires,
		Body:          trimmed,
		Truncated:     truncated,
		OriginalBytes: len(body),
	}, nil
}

// cut 按行、再按字节裁剪，且**不切断 UTF-8 字符**。
//
// 切在半个字符上会产出非法 UTF-8：多数 LLM 客户端会把它替换成 U+FFFD 或直接
// 报错，而错误信息不会提「是你的裁剪切坏了」。
func cut(body string, b Bounds) (string, bool) {
	truncated := false
	out := body
	if b.MaxLines > 0 {
		lines := strings.Split(out, "\n")
		if len(lines) > b.MaxLines {
			out = strings.Join(lines[:b.MaxLines], "\n")
			truncated = true
		}
	}
	if b.MaxBytes > 0 && len(out) > b.MaxBytes {
		// 边界回退委托 oekit/pkg/stringx.TruncBytes：它从预算处**向前**扫到最近的
		// rune 起始字节（最多 3 步）。旧实现是「先按字节切、再反复用
		// utf8.ValidString 验证整段」，最坏四次全串扫描，且正确性依赖
		// 「往回退最多 3 字节」这条隐含前提——真踩到更长的坏边界时会静默留下
		// 非法 UTF-8。
		out, _ = stringx.TruncBytes(out, b.MaxBytes)
		truncated = true
	}
	return out, truncated
}

// State 计算快照相对 now 与源当前版本的状态。
//
// 判定顺序：**先判过期，再判版本**。两者同时成立时报 expired——
// 因为「时间已过」是宿主必须无条件重新采集的那一条，而版本不一致可能只是
// 源侧做了一次无关的重建。
func (r Ref) State(now time.Time, currentVersion uint64) State {
	if !now.Before(r.ExpiresAt) {
		return StateExpired
	}
	if currentVersion != r.SourceVersion {
		return StateStale
	}
	return StateFresh
}

// Digest 正文的 sha256（前 12 位十六进制）。
//
// 用途是**同一份快照的身份**：宿主把它写进日志后，事后能证明「当时渲染的是
// 这一版正文」，而不是只留一句「引用了一个快照」。
func (r Ref) Digest() string {
	// 上游 encoding.Sha256 就是 sha256.Sum256 + hex.EncodeToString（小写十六进制、全长），
	// 与本包对外承诺的指纹格式逐字一致，故这里不需要自己拼。
	return encoding.Sha256(r.Body)[:12]
}

// Render 把快照渲染成可直接注入 prompt 的包裹体。
//
// state 与来源都写在标签与 header 里：**标签不可伪造**（body 的 @<@ 已被统一
// 转义），header 是提示性文本（见包头「已知边界」）。
func (r Ref) Render(now time.Time, currentVersion uint64) string {
	state := r.State(now, currentVersion)
	var b strings.Builder
	fmt.Fprintf(&b, "[session-ref captured=%s expires=%s state=%s version=%d digest=%s",
		r.CapturedAt.UTC().Format(time.RFC3339), r.ExpiresAt.UTC().Format(time.RFC3339),
		state, r.SourceVersion, r.Digest())
	if r.Truncated {
		// 裁剪必须在**注入面**就说明白：模型据此知道结论可能不完整。
		fmt.Fprintf(&b, " truncated=true original_bytes=%d", r.OriginalBytes)
	}
	fmt.Fprintf(&b, " size=%d]", len(r.Body))
	if state != StateFresh {
		fmt.Fprintf(&b, "\n[请重新采集：这份快照 state=%s，其中的结论可能已不成立]", state)
	}
	b.WriteString("\n")
	b.WriteString(r.Body)
	return reminder.Wrap("session-ref:"+r.Source, b.String())
}
