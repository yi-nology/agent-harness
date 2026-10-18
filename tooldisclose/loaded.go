package tooldisclose

// MergeLoaded 合并多份已加载集：并集、去重、升序。
//
// 为什么排序是**语义要求**而不是格式偏好：编排可能从多个环节并发汇流各自的已加载集。
// 若保留各自的追加顺序，同一份事实在不同并发时序下会呈现成不同的序列——
// 留痕、回放、比对全都不再可复现。并集 + 排序把并发时序从结果里彻底抹掉：
// 无论谁先谁后，合并结果逐字节相同。
//
// 空名成员被丢弃：空名进不了任何目录（见 NewCatalogWithLimit），留着只会让
// 「已加载了什么」这个集合凭空多一项。
func MergeLoaded(sets ...[]string) []string {
	seen := make(map[string]bool)
	for _, s := range sets {
		for _, n := range s {
			if n != "" {
				seen[n] = true
			}
		}
	}
	return namesOf(seen)
}
