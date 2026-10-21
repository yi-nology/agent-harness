// without.go —— 「撤销一条守卫」的纯函数：产出一份**新的**集合。
//
// # 为什么它不跟换份放在一起
//
// 换份是**装配面**的动作（把哪一份换上去）；`Without` 是**纯函数**
// （从一份算出另一份，不碰任何全局状态）。两者混在一起会让人以为
// 「撤销一条」是一个可以直接改变现状的操作——而它其实只是**算**出一份新的，
// 生效还得靠换份（见 assembly.go 的 `SwapFunc`）。
//
// # 为什么要纯
//
// 因为 `Set` 不可变是它的不变量。删一条若能改到原集合，就有了「当前有哪些守卫」
// 两个真相，且并发读它就是竞态。

package guardset

import (
	"github.com/oekit/pkg/errors"
)

// Without 返回一份**去掉指定守卫**的新集合。原集合**不变**。
//
// # 为什么是「返回新集合」而不是「删掉这一条」
//
// 因为 `Set` 不可变是它的不变量。删一条若能改到原集合，就有了「当前有哪些守卫」
// 两个真相，且并发读它就是竞态。故撤销一条守卫的机械路径是：
//
//	err = asm.SwapFunc(ctx, func(cur guardset.Snapshot) (*guardset.Set, error) {
//		return cur.Set.Without("rate_limiter")
//	})
//
// # 两条拒绝
//
//   - 守卫名不在集合里 ⇒ 拒绝（**不静默忽略**）。静默忽略会让宿主以为
//     「撤掉了某条守卫」，实际那份集合里根本没有它——撤销的是个不存在的东西。
//   - 去掉之后集合为空 ⇒ 拒绝（`ErrEmptySet`）。**全部撤销不是降级，是失守**。
func (s *Set) Without(names ...string) (*Set, error) {
	if s == nil {
		return nil, ErrUnassembled
	}
	if len(names) == 0 {
		return s, nil
	}
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := make([]Guard, 0, len(s.guards))
	for _, g := range s.guards {
		if !drop[g.Name] {
			kept = append(kept, g)
		}
	}
	for _, n := range names {
		if !s.hasGuard(n) {
			return nil, errors.Wrap(ErrUnknownGuard, "guardset: 撤销不存在的守卫", "guard", n)
		}
	}
	// New 会重新校验（空集合拒绝构造、名字不得为空/重复）并按 (Rank, 装配序) 排序。
	return New(kept...)
}

// hasGuard 判断集合里有没有这个守卫名。
func (s *Set) hasGuard(name string) bool {
	for _, g := range s.guards {
		if g.Name == name {
			return true
		}
	}
	return false
}
