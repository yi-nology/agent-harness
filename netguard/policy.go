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
// 但代次只说「变了」，说不出「变成了什么」：两次比对之间若换了两次，
// 代次只能说明换过。subscribe.go 把这半句接上——变更发生的那一刻由写侧
// 直接把新快照推给订阅者。两者不是二选一：代次回答「换过没有」（可追溯到
// 日志），订阅回答「换成了什么」（当场生效）。
//
// # 代次只有一个真相
//
// 代次只存在于策略指针内部（Policy.gen），PolicyGeneration() 是它的投影，
// **没有第二个全局计数器**。这不是洁癖：两份记账在并发下必然漂移——
// 旧实现是「next := policyGen.Load() + 1; curPolicy.Store(p); policyGen.Store(next)」
// 三步，policyGen 来自一次非原子的读-改-写，于是并发写者会读到同一个旧代次、
// 各自算出同一个 next，两次交换拿到**同一个代次**（实测 64 次并发交换只推进
// 了 54 代）。代次一重复，「换过没有」就给不出答案：轮询方看到 6 → 6，
// 以为没换过，而策略确实换了一次。
//
// 交换因此走 CAS 循环（swapPolicy）：代次从**观察到的**旧策略推导，CAS 保证
// 同一时刻只有一个写者胜出，代次天然唯一、单调、不回卷。语义与同族的
// pii.updatePolicy 一致——那边早就是 CAS，本包是后改的。
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
//
// 代次只有一个真相：策略指针内部的 gen。**不再有独立的全局计数器**——
// 两者并存时它们会短暂不一致，而「快照代次 == 全局代次」这条承诺
// 恰恰是调用方据以判断「我读的这段时间里换过没有」的依据。
func PolicyGeneration() uint64 {
	if p := curPolicy.Load(); p != nil {
		return p.gen
	}
	return 0
}

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
// ## 订阅者只在成功时收到通知
//
// 通知发生在本次交换真正落地之后：校验失败的路径直接 return，一次通知都不发。
// 发了就等于告诉订阅者「策略换过了」，而它此刻根本没换——订阅者据此重建的派生
// 视图会对应一份从未生效过的策略，且没有任何东西能指出这一点。
//
// ## 相对内置默认名单的关系
//
// 扩展位与内置默认名单**并集**生效，默认名单不在这张表里、也不可被覆盖：
// 一个 `=` 赋值就能把元数据名拦截悄悄摘掉，而这种改动没有任何编译期或运行期信号。
// 本函数只能增补扩展位，不能削减内置默认。
func SetPolicy(hostnames, suffixes []string) error {
	normNames := normalizeHostnames(hostnames)
	normSuffixes, err := normalizeSuffixes(suffixes)
	if err != nil {
		return err
	}

	// 校验全过才交换：否则旧策略原样保留。
	//
	// 交换走 CAS 循环：代次从**观察到的**旧策略推导，CAS 保证同一时刻只有
	// 一个写者胜出，于是代次天然唯一、单调、不回卷。
	//
	// 旧实现是「next := policyGen.Load() + 1; curPolicy.Store(p); policyGen.Store(next)」
	// 三步——两个独立变量记同一件事，且代次来自一个**非原子的读-改-写**。
	// 并发写者于是会读到同一个旧代次，各自算出同一个 next：两次交换拿到
	// **同一个代次**。实测 64 次并发交换只推进了 54 代——代次一旦重复，
	// 轮询方看到 6 → 6 就以为「没换过」，而策略确实换了一次。
	// 那正是本包要防的那类失效：「换过没有」给不出答案。
	// 附带还破坏了两条承诺：代次可能回退（非原子读改写的交错写），
	// 且 curPolicy.gen 与 policyGen 之间存在窗口，读者能同时看到
	// 「快照 gen=6、全局 gen=5」这种自相矛盾的一对。
	//
	// 语义与同族的 pii.updatePolicy 一致——那边早就是 CAS，本包是后改的。
	return swapPolicy(func(*Policy) *Policy {
		return &Policy{
			hostnames: normNames,
			suffixes:  normSuffixes,
		}
	})
}

// normalizeHostnames 归一化精确主机名名单（去空白 + 小写 + 去尾点），空项丢弃。
func normalizeHostnames(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = normalizeHost(strings.TrimSpace(n)); n != "" {
			out[n] = struct{}{}
		}
	}
	return out
}

// normalizeSuffixes 归一化扩展后缀名单，并执行注入期校验（任一项非法即整批拒绝）。
//
// 拒绝规则见 SetPolicy 的「后缀的拒绝规则」一节：大小写/空白归一，无前导点
// 拒绝，归一化后永不匹配的拒绝——「名单不命中」是这个拦截面最坏的失效模式。
func normalizeSuffixes(suffixes []string) ([]string, error) {
	out := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		s = strings.ToLower(strings.TrimSpace(s))
		switch {
		case s == "":
			return nil, errors.Wrap(ErrInvalidBlockedSuffix, "扩展拦截后缀为空")
		case !strings.HasPrefix(s, "."):
			return nil, errors.Wrap(ErrInvalidBlockedSuffix,
				"扩展拦截后缀必须带前导点（无点会把顶级域整片拒掉）", "suffix", s)
		case s == "." || strings.HasSuffix(s, "."):
			return nil, errors.Wrap(ErrInvalidBlockedSuffix,
				"扩展拦截后缀归一化后永不匹配（尾点已去）", "suffix", s)
		}
		out = append(out, s)
	}
	return out, nil
}

// swapPolicy 以 CAS 循环整份换策略；CAS 失败（有别的写者抢先）则重来一次。
//
// mutate 从当前策略算出下一份；它只在 CAS 真正落地时产出结果，故
// 「基于最新值修改」这条读改写链是原子的，不会丢更新。
// 代次由本函数统一赋予（从观察到的旧策略 +1），调用方不得自己填。
func swapPolicy(mutate func(old *Policy) *Policy) error {
	for {
		old := curPolicy.Load()
		next := mutate(old)
		next.gen = policyGenOf(old) + 1
		if curPolicy.CompareAndSwap(old, next) {
			// 交换已落地，此刻起通知才是「如实」。通知在锁外、且订阅者 panic 被 recover：
			// 一次成功的交换不因某个订阅者抛异常而被说成失败（见 subscribe.go 头注）。
			notifyPolicy(next)
			return nil
		}
	}
}

// policyGenOf 取一份策略的代次（nil 视为 0，即「从未换过」）。
func policyGenOf(p *Policy) uint64 {
	if p == nil {
		return 0
	}
	return p.gen
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
//
// 保留后缀的读发生在 CAS 重试循环**内部**：并发写者若同时改了后缀，
// 本函数基于最新值重算，不会用陈旧副本把它覆盖回去。旧实现是
// 「先 CurrentPolicy() 读出来、再 SetPolicy 写回」，两步之间有窗口，
// 并发下会静默丢掉对方的改动。
func SetExtraBlockedHostnames(names ...string) {
	normNames := normalizeHostnames(names)
	_ = swapPolicy(func(old *Policy) *Policy {
		return &Policy{
			hostnames: normNames,
			suffixes:  suffixesOf(old),
		}
	})
}

// SetExtraBlockedHostSuffixes 注册/清空扩展后缀名单（不传即清空）。
//
// 全量校验通过后才替换：任一项非法则**旧策略原样保留**，不做部分应用——
// 半套名单比旧名单更难推理。校验规则与理由见 SetPolicy。
//
// 本函数只改后缀、保留当前主机名，整体仍是一次原子交换；需要同时改两项请用 SetPolicy。
// 保留主机名的读同样在 CAS 重试循环内部（并发下不丢更新），理由见
// SetExtraBlockedHostnames。
func SetExtraBlockedHostSuffixes(suffixes ...string) error {
	normSuffixes, err := normalizeSuffixes(suffixes)
	if err != nil {
		return err
	}
	return swapPolicy(func(old *Policy) *Policy {
		return &Policy{
			hostnames: hostnamesOf(old),
			suffixes:  normSuffixes,
		}
	})
}

// hostnamesOf 取一份策略的精确主机名名单（快照拷贝，可安全改写）。
func hostnamesOf(p *Policy) map[string]struct{} {
	out := make(map[string]struct{})
	if p == nil {
		return out
	}
	for n := range p.hostnames {
		out[n] = struct{}{}
	}
	return out
}

// suffixesOf 取一份策略的扩展后缀名单（快照拷贝，可安全改写）。
func suffixesOf(p *Policy) []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.suffixes))
	copy(out, p.suffixes)
	return out
}
