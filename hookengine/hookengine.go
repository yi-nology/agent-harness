// Package hookengine Hooks 的**判定面**：什么时候该跑、跑了算不算拒绝。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 为什么只做判定面
//
// Codex 的 hooks 是「外部进程协议 + 规则文件」，DSH 的是「动态插件包」——
// 两家形态**已经分叉**。此时由内核替宿主选边，等于把一种传输/加载方式
// 焊进契约（printf 回调那一课：焊进签名的形态是不可逆的）。
//
// 故本包只定三件跨形态恒定的事：
//
//   - **匹配**：一个 hook 在哪些事件、哪些工具上生效；
//   - **裁决**：hook 跑完算 allow 还是 deny（含超时与执行失败）；
//   - **顺序与收口**：多个阻塞 hook 的求值顺序，以及第一个拒绝之后还跑不跑。
//
// **传输**（子进程 / 插件 / HTTP）与**加载**（规则文件 / 代码包）归宿主：
// @@Runner@@ 是唯一接缝，宿主怎么跑它不管。
//
// # 三条 fail-closed 的取舍
//
//  1. **阻塞 hook 执行失败 ⇒ deny**。跑不起来不等于没问题——把「命令写错了」
//     当成放行，等于让一个失效的检查静默地变成空检查。
//  2. **阻塞 hook 超时的裁决必须显式声明**（@@Hook.OnTimeout@@，缺省 deny）。
//     没有缺省值的话，超时会被 ctx 的取消掩盖：宿主看到的是「这次没拒绝」，
//     而真相是「这次没人检查」。允许放宽，但必须写出来。
//  3. **非阻塞 hook 不参与裁决**，但它的失败要进 @@Result.Notes@@。
//     非阻塞 hook 的产物一旦能影响裁决，它就只是「慢的阻塞 hook」——
//     而宿主会以为它是旁路的，于是把耗时任务写成非阻塞，实际每次调用都被它拖住。
//
// # 没有匹配的 hook 不是静默
//
// 无匹配时裁决为 allow，但 @@Result.Notes@@ 里必须写明「没有匹配的 hook」：
// 「没有检查」与「检查通过」在日志里长得一样，而前者是配置漏了的形态。
package hookengine

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/stringx"
)

// Verdict 一次裁决。
type Verdict string

const (
	// VerdictAllow 放行。
	VerdictAllow Verdict = "allow"
	// VerdictDeny 拒绝。
	VerdictDeny Verdict = "deny"
)

// valid 报告裁决是否在词表内。
func (v Verdict) valid() bool { return v == VerdictAllow || v == VerdictDeny }

// Hook 一条 hook 的**声明**（不含执行细节：怎么跑归 Runner）。
type Hook struct {
	// Name 唯一标识（留痕与顺序用）。
	Name string
	// Event 触发事件（宿主自定词表——本包不预设事件名，那是宿主的编排语义）。
	Event string
	// ToolPrefix 只对该前缀的工具生效（空 = 全部）。
	ToolPrefix string
	// Command 交给 Runner 的命令（语义由 Runner 解释）。
	Command string
	// Args 命令参数。
	Args []string
	// Blocking 是否参与裁决。
	//
	// 阻塞 hook 在**每一次**匹配调用上都跑 ⇒ 它的成本直接落在热路径上。
	Blocking bool
	// Timeout 阻塞 hook 的墙钟上限（阻塞 hook 必填：没有上限的阻塞 hook
	// 等于把宿主挂在一个可能永不返回的进程上）。
	Timeout time.Duration
	// OnTimeout 阻塞 hook 超时时的裁决（缺省 deny）。
	OnTimeout Verdict
}

// Payload 一次事件的载荷。
type Payload struct {
	// Event 事件名。
	Event string
	// Tool 当前工具名（可空）。
	Tool string
	// Args 调用参数（原样带给 Runner；本包不解释）。
	Args []string
}

// Outcome 一次 hook 执行的结果（Runner 负责填充）。
type Outcome struct {
	// ExitCode 进程/插件退出码（0 = 成功）。
	ExitCode int
	// Stdout 标准输出（拒绝理由通常在这里）。
	Stdout string
	// Stderr 标准错误。
	Stderr string
}

// Runner 执行一条 hook 的**唯一接缝**（传输归宿主）。
type Runner interface {
	Run(ctx context.Context, h Hook, p Payload) (Outcome, error)
}

// Engine 已装载的 hook 集合。
type Engine struct {
	hooks    []Hook
	runner   Runner
	maxChars int
}

// DefaultMaxChars 拒绝理由的截断长度。
//
// 上限不是审美问题：hook 的输出可以任意长，原样塞进裁决理由会把宿主日志
// 与模型上下文一起撑爆——而那正是「一次被拒」变成「一次坏运行」的形态。
const DefaultMaxChars = 512

// nonBlockingTimeout 非阻塞 hook 的硬上限。
//
// 它们不参与裁决，但**不能无限跑**：一次派发里有十几个非阻塞 hook 时，
// 「旁路」会变成实际的墙钟成本，而宿主的超时预算里没有这一段。
const nonBlockingTimeout = 30 * time.Second

// New 装载 hook 并做**加载期自检**。
//
// 自检项（任一不满足即报错，不静默跳过）：
//
//   - Name 非空且唯一（重名会让留痕分不清是哪一条跑出来的裁决）；
//   - Event 非空（没有事件的 hook 永远不触发，登记了等于没登记）；
//   - Command 非空；
//   - 阻塞 hook 必须有正 Timeout（没有上限的阻塞 hook 会把宿主挂住）；
//   - OnTimeout 只能出现在**阻塞** hook 上（写在非阻塞 hook 上等于一条永不生效的
//     假声明：非阻塞 hook 的超时根本不参与裁决）。
func New(hooks []Hook, r Runner) (*Engine, error) {
	if r == nil {
		return nil, errors.New("hookengine: Runner 为空（没有执行接缝就谈不上 hooks）")
	}
	seen := map[string]bool{}
	out := make([]Hook, 0, len(hooks))
	for _, h := range hooks {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			return nil, errors.New("hookengine: hook 名为空（留痕无从定位）")
		}
		if seen[name] {
			return nil, errors.Errorf("hookengine: hook 名 %q 重复", name)
		}
		seen[name] = true
		if strings.TrimSpace(h.Event) == "" {
			return nil, errors.Errorf("hookengine: hook %q 没有事件（它永远不会被触发）", name)
		}
		if strings.TrimSpace(h.Command) == "" {
			return nil, errors.Errorf("hookengine: hook %q 没有命令", name)
		}
		if h.Blocking {
			if h.Timeout <= 0 {
				return nil, errors.Errorf("hookengine: 阻塞 hook %q 没有 Timeout（会把宿主挂在可能永不返回的进程上）", name)
			}
			if h.OnTimeout != "" && !h.OnTimeout.valid() {
				return nil, errors.Errorf("hookengine: hook %q 的 OnTimeout 不在词表内（allow / deny）", name)
			}
		} else if h.OnTimeout != "" {
			return nil, errors.Errorf("hookengine: hook %q 是非阻塞的，却声明了 OnTimeout——"+
				"非阻塞 hook 的超时**不参与裁决**，这条声明永远不会生效", name)
		}
		h.Name = name
		out = append(out, h)
	}
	// 求值顺序必须确定：hook 集合常常来自 map / 文件遍历，顺序不定会让
	// 「哪个理由先出现」每次都不同，报告无法比对。
	slices.SortStableFunc(out, func(a, b Hook) int { return strings.Compare(a.Name, b.Name) })
	return &Engine{hooks: out, runner: r, maxChars: DefaultMaxChars}, nil
}

// Result 一次派发的裁决结果。
type Result struct {
	// Verdict 最终裁决。
	Verdict Verdict
	// Reasons 拒绝理由（保持触发顺序）。
	Reasons []string
	// Notes 旁路留痕（非阻塞 hook 的结果与失败、无匹配的说明）。
	Notes []string
	// Ran 实际执行过的 hook 名（留痕：没跑到的也算信息）。
	Ran []string
}

// Dispatch 跑所有匹配的 hook 并裁决。
//
// 顺序语义：按名字序遍历**阻塞** hook，**第一个拒绝即刻收口**——后面的 hook
// 不再跑。理由与 guardset 一致：链在第一个拒绝处停下，模型一次只听得到一个
// 理由；继续跑只会产生一串互相叠加的拒绝理由，而其中后面的那些可能是在前提
// 已不成立的情况下判的。
func (e *Engine) Dispatch(ctx context.Context, p Payload) (Result, error) {
	var matched, blocking []Hook
	for _, h := range e.hooks {
		if !h.match(p) {
			continue
		}
		matched = append(matched, h)
		if h.Blocking {
			blocking = append(blocking, h)
		}
	}
	if len(matched) == 0 {
		return Result{Verdict: VerdictAllow, Notes: []string{
			"没有匹配的 hook：这次调用**没有经过任何检查**（不是检查通过）",
		}}, nil
	}

	res := Result{Verdict: VerdictAllow}

	// 先跑阻塞 hook（裁决），再跑非阻塞 hook（旁路）。
	for _, h := range blocking {
		res.Ran = append(res.Ran, h.Name)
		v, reason := e.runBlocking(ctx, h, p)
		if v == VerdictDeny {
			res.Verdict = VerdictDeny
			res.Reasons = append(res.Reasons, reason)
			return res, nil
		}
	}

	for _, h := range matched {
		if h.Blocking {
			continue
		}
		res.Ran = append(res.Ran, h.Name)
		hc, cancel := context.WithTimeout(ctx, nonBlockingTimeout)
		out, err := e.runner.Run(hc, h, p)
		cancel()
		if err != nil {
			res.Notes = append(res.Notes, h.Name+" 执行失败（非阻塞，不影响裁决）: "+err.Error())
			continue
		}
		if out.ExitCode != 0 {
			res.Notes = append(res.Notes, h.Name+" 退出码 "+strconv.Itoa(out.ExitCode)+"（非阻塞，不影响裁决）")
		}
	}
	return res, nil
}

// runBlocking 跑一条阻塞 hook 并给出裁决。
func (e *Engine) runBlocking(ctx context.Context, h Hook, p Payload) (Verdict, string) {
	hc, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	out, err := e.runner.Run(hc, h, p)
	if err != nil {
		// 超时的裁决由声明决定；其余执行失败一律 deny（fail-closed）。
		if errors.Is(err, context.DeadlineExceeded) {
			v := h.OnTimeout
			if v == "" {
				v = VerdictDeny
			}
			return v, h.Name + " 超时（" + h.Timeout.String() + "），按声明的 OnTimeout=" + string(v) + " 裁决"
		}
		return VerdictDeny, h.Name + " 执行失败（fail-closed）: " + err.Error()
	}
	if out.ExitCode != 0 {
		return VerdictDeny, h.Name + ": " + e.clip(reasonOf(out))
	}
	return VerdictAllow, ""
}

// clip 截断理由（hook 输出可以任意长，原样带进日志与上下文会撑爆两端）。
func (e *Engine) clip(s string) string {
	s = strings.TrimSpace(s)
	// 截断必须落在 rune 边界：旧实现直接 s[:maxChars]，切在半个多字节字符上会
	// 产出**非法 UTF-8**——而拒绝理由正是要被人读的那一行，客户端多半把它显示成
	// U+FFFD 或整条丢弃，且报错信息不会提「是你的裁剪切坏了」。
	cut, truncated := stringx.TruncBytes(s, e.maxChars)
	if !truncated {
		return cut
	}
	return cut + "…（已截断）"
}

// reasonOf 取拒绝理由：stderr 优先（工具的抱怨通常在那里），否则 stdout。
func reasonOf(out Outcome) string {
	if strings.TrimSpace(out.Stderr) != "" {
		return out.Stderr
	}
	return out.Stdout
}

// match 判一条 hook 是否适用于这次载荷。
func (h Hook) match(p Payload) bool {
	if h.Event != p.Event {
		return false
	}
	if h.ToolPrefix == "" {
		return true
	}
	return strings.HasPrefix(p.Tool, h.ToolPrefix)
}
