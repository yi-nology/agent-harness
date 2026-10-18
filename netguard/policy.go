// 名字层策略快照：整份策略一次原子交换，杜绝撕裂读。
//
// # 为什么不是两个 atomic.Pointer
//
// 本文件之前用两个独立的 atomic.Pointer 分别装主机名与后缀扩展位。逐个字段看，
// 每一次读写都原子的，看起来没毛病——但**策略是两份数据合起来的**：
//
//	classifyHostName 里两处 Load() 发生在不同时刻（先主机名、若干次比较后再后缀）。
//	热重载若「先换主机名、再换后缀」，这两步之间的并发读者就会读到
//	**新主机名 + 旧后缀**的撕裂策略。
//
// 这正是 OpenCode 2.0 issue #52333 的形状（该 issue 至 2026-10 仍未修）：
// 每个字段各自都是正确的，**组合起来的那一瞬间不是**。名单「不命中」是这个拦截面
// 最坏的失效模式——挂着一个看着在拦、实际没拦的策略，且没有任何信号。
//
// 故合并成一张 Policy，整体替换：读者一次 Load 拿到的就是**同一份**策略，
// 不存在读出半套的时点。
//
// # 换过没有、换成什么，得能看见
//
// 「闸被重置后没人告知」是本仓 checkpoint.SeqGate 记录过的事（进程重启后闸是空的，
// 而上一进程写下的陈旧值畅通无阻）。同一类失效在这里同样成立：热重载失败、
// 策略半路被换掉，事后没有任何东西能回答「那一刻用的是哪份策略」。
//
// 故每次交换递增 Generation，并经 CurrentPolicy / PolicyGeneration 暴露。
// 调用方据此在观察到 Generation 变化时留痕——**可观测性是这层的兜底**，
// 因为策略生效与否最终只能由它是否被记录来证明。
//
// # 只能整份换，不能半份换
//
// 旧的 SetExtraBlockedHostnames / SetExtraBlockedHostSuffixes 各自只改一项，
// 两次调用之间天然存在撕裂窗口，且后一次校验失败时前一次已经生效（半应用）。
// 二者现在都改为「取出当前策略 → 改一项 → 整份换回」，因此单独调用依然安全；
// 需要同时更新两项时用 SetPolicy，一次校验、一次交换，不存在中间态。

package netguard

import (
	"strings"
	"sync/atomic"

	"github.com/oekit/pkg/errors"
)

// Policy 名字层拦截策略的一份**不可变**快照。
//
// 构造后不再被任何写侧触碰（写侧只整体替换新实例），故取出后可安全长期持有与
// 并发遍历，无需再加锁。
type Policy struct {
	gen       uint64
	hostnames map[string]struct{}
	suffixes  []string
}

// Generation 本策略的代次。单调递增，每次成功交换 +1；0 表示从未换过（即内置默认）。
func (p *Policy) Generation() uint64 {
	if p == nil {
		return 0
	}
	return p.gen
}

// BlocksHostname 该精确主机名是否在扩展精确名单内（已归一化比较）。
func (p *Policy) BlocksHostname(h string) bool {
	if p == nil || len(p.hostnames) == 0 {
		return false
	}
	_, ok := p.hostnames[normalizeHost(h)]
	return ok
}

// BlocksSuffix 返回命中的扩展后缀与是否命中。命中即说明该主机名被该后缀覆盖。
func (p *Policy) BlocksSuffix(h string) (string, bool) {
	if p == nil || len(p.suffixes) == 0 {
		return "", false
	}
	h = normalizeHost(h)
	for _, s := range p.suffixes {
		if strings.HasSuffix(h, s) {
			return s, true
		}
	}
	return "", false
}

// curPolicy 当前生效的扩展策略。**整份策略只有一个原子指针**——这是「不可撕裂读」
// 的全部机制，也是与旧版两个指针的根本差别。
var curPolicy atomic.Pointer[Policy]

// policyGen 当前策略代次。与 curPolicy 同步递增，供可观测性使用。
var policyGen atomic.Uint64

// CurrentPolicy 取当前生效策略的快照。
//
// 返回的指针在整个进程生命周期内**只读**（没有任何写侧会 mutate 它），因此可以
// 安全地长期持有、并发读，甚至与后续的交换并存。零值状态（从未注册过扩展）返回
// 一个空策略而非 nil——调用方不必判空。
func CurrentPolicy() *Policy {
	if p := curPolicy.Load(); p != nil {
		return p
	}
	return &Policy{}
}

// PolicyGeneration 当前策略代次。调用方可在前后各取一次，变化即说明期间换过策略。
//
// 代次**单调且不回卷**——清空策略也不归零。若清空会回卷，那么
// 「换上一份 → 清空 → 再换上一份」与「什么都没换」在代次上不可区分，
// 可观测性就白做了。
func PolicyGeneration() uint64 { return policyGen.Load() }

// ErrInvalidBlockedSuffix 扩展后缀非法（注入期拒绝）。errors.Is 判定。
var ErrInvalidBlockedSuffix = errors.New("扩展拦截后缀非法")

// SetPolicy 整份替换扩展策略：**先全量校验，再一次性交换**。
//
// ## 为什么不拆成两次调用
//
// 分别调用 SetExtraBlockedHostnames 与 SetExtraBlockedHostSuffixes 时，
// 后者校验失败不会回滚前者——策略停在半应用状态，而且这个状态没有任何信号。
// 需要同时改两项时走这里：要么整份生效，要么原样保留。
//
// ## 后缀的拒绝规则
//
// 名单「不命中」是这个拦截面最坏的失败模式：`.CORP` 静默不匹配，等于挂了一个
// 看着在拦、实际不拦的后缀，而且没有任何信号。故校验一律发生在**注入期**：
//
//   - 大小写 / 空白：归一，匹配侧只做 HasSuffix，不再清洗；
//   - 无前导点的后缀：拒绝（`corp` 会把语义从「拦 foo.corp」悄悄变成
//     「拦一切以 corp 结尾的名字」——比不拦更糟）；
//   - 归一化后永不匹配的后缀（裸 `.`、以 `.` 结尾）：拒绝。
//
// 任一项非法则**旧策略原样保留**，不做部分应用。
//
// ## 相对内置默认名单的关系
//
// 扩展位与内置默认名单**并集**生效，默认名单不在这张表里、也不可被覆盖：
// 一个 `=` 赋值就能把元数据名拦截悄悄摘掉，而这种改动没有任何编译期或运行期信号。
// 本函数只能增补扩展位，不能削减内置默认。
func SetPolicy(hostnames, suffixes []string) error {
	normNames := make(map[string]struct{}, len(hostnames))
	for _, n := range hostnames {
		if n = normalizeHost(strings.TrimSpace(n)); n != "" {
			normNames[n] = struct{}{}
		}
	}
	normSuffixes := make([]string, 0, len(suffixes))
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
		normSuffixes = append(normSuffixes, s)
	}

	// 校验全过才交换：否则旧策略原样保留。
	// 代次在交换成功后递增，读者据此判断「期间换过策略」。
	next := policyGen.Load() + 1
	curPolicy.Store(&Policy{
		gen:       next,
		hostnames: normNames,
		suffixes:  normSuffixes,
	})
	policyGen.Store(next)
	return nil
}

// SetExtraBlockedHostnames 注册/清空扩展精确主机名名单（不传即清空）。
//
// 与默认名单**并集**生效，只能加不能减；默认名单刻意不做成可写变量：一个 `=` 赋值
// 就能把元数据名拦截悄悄摘掉，而这种改动没有任何编译期或运行期信号。
//
// 归一化与判定侧同口径（小写 + 去尾点 + 去空白），调用方不必自己保证大小写——
// 与默认名单一样，`Foo.Example.` 和 `foo.example` 是同一个名字。空项直接丢弃。
//
// 本函数只改扩展名单、保留当前后缀，整体仍是一次原子交换；需要同时改两项请用
// SetPolicy（两次调用之间存在撕裂窗口，且后一次校验失败不会回滚前一次）。
func SetExtraBlockedHostnames(names ...string) {
	p := CurrentPolicy()
	_ = SetPolicy(names, p.suffixes)
}

// SetExtraBlockedHostSuffixes 注册/清空扩展后缀名单（不传即清空）。
//
// 全量校验通过后才替换：任一项非法则**旧策略原样保留**，不做部分应用——
// 半套名单比旧名单更难推理。校验规则与理由见 SetPolicy。
//
// 本函数只改后缀、保留当前主机名，整体仍是一次原子交换；需要同时改两项请用 SetPolicy。
func SetExtraBlockedHostSuffixes(suffixes ...string) error {
	p := CurrentPolicy()
	names := make([]string, 0, len(p.hostnames))
	for n := range p.hostnames {
		names = append(names, n)
	}
	return SetPolicy(names, suffixes)
}
