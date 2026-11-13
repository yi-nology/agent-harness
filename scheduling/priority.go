// priority.go —— 优先级排序的确定性判据。
//
// 优先级本身只是一维数字；判据的本体是**排序的确定性**：
// 同优先级内必须有名字层平局裁决，且不依赖输入的排列顺序。
// 输入顺序通常来自配置文件书写次序或事件到达次序——一次无关的重排
// 就换掉执行次序，宿主无从解释「为什么它先跑」。
package scheduling

import (
	"cmp"
	"slices"
)

// Priority 优先级：数值越大越优先。
//
// 只定义相对次序，不定义刻度含义（「多少算高」是宿主的策略）。
// 缺省零值即最低档——未声明优先级的请求排在所有声明过的后面，
// 方向取「漏声明的拿到最弱的那档」，与 sandbox.Level 同一条口径。
type Priority int

// Order 把 items 按 (优先级降序, key 升序) 排出确定性次序，返回新切片
// （不改输入——排序是判据，不是对调用方数据的原地改写）。
//
// keyFunc 必须给出稳定的标识（同优先级内的平局裁决依据）。两个条目
// key 相同则次序由稳定排序保留输入次序——但那是宿主的错误（同一 key
// 两次出现，标识已经不唯一），本函数不替它纠偏。
//
// prioFunc 为 nil 视为全部 Priority(0)——只有一组时次序由 key 决定。
func Order[T any](items []T, keyFunc func(T) string, prioFunc func(T) Priority) []T {
	if len(items) == 0 {
		return nil
	}
	type ranked struct {
		item T
		key  string
		prio Priority
	}
	rankedItems := make([]ranked, 0, len(items))
	for _, it := range items {
		var p Priority
		if prioFunc != nil {
			p = prioFunc(it)
		}
		rankedItems = append(rankedItems, ranked{item: it, key: keyFunc(it), prio: p})
	}
	// 稳定排序：同 (prio, key) 的条目保持输入相对次序（那是宿主的标识错误，不替它纠偏）。
	slices.SortStableFunc(rankedItems, func(a, b ranked) int {
		if c := cmp.Compare(b.prio, a.prio); c != 0 { // 优先级降序
			return c
		}
		return cmp.Compare(a.key, b.key) // 名字升序平局裁决
	})
	out := make([]T, 0, len(rankedItems))
	for _, r := range rankedItems {
		out = append(out, r.item)
	}
	return out
}

// TopK 从 items 里按 Order 的同一口径取前 n 个（确定性）。
//
// n<=0 返回空——「要 0 个」是合法请求，不是错误；负值同样按 0 处理
// （调用方算错了配额，不该由排序判据替它 panic）。
func TopK[T any](items []T, keyFunc func(T) string, prioFunc func(T) Priority, n int) []T {
	if n <= 0 {
		return nil
	}
	ordered := Order(items, keyFunc, prioFunc)
	return ordered[:min(n, len(ordered))]
}
