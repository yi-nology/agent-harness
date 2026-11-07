// 检索前 k 选择的**差分对照**：生产实现与「全量排序后截断」逐项比对。
//
// 与 search_test.go 分开是按用途切：那边钉的是「检索语义」（名字重于描述、加成、
// 无命中不兜底、不激活……），这边钉的是「选择算法」。两种失效形态也不同——语义
// 用例红了说明打分口径变了，这里红了说明**选错了该给谁**。

package tooldisclose

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// tieTools 合成一批**分数大量并列**的目录成员：名字共用同一个词，描述分两组，
// 其中一组含大写。并列正是前 k 选择最容易出错的地方（插入位置、截断边界）。
func tieTools(n int) []Tool {
	out := make([]Tool, 0, n)
	for i := 0; i < n; i++ {
		desc := "共用描述：处理条目并返回汇总"
		if i%3 == 0 {
			desc = "大写形态 Alpha 的描述，用来验预计算的小写形态与原样现算等价"
		}
		out = append(out, Tool{Name: "共用_" + strconv.Itoa(i), Description: desc})
	}
	return out
}

// fullSortSearch 参考实现：全量收集 → 排序 → 截断。只用于对照，不参与生产路径。
//
// 两处**刻意不复用**生产代码：排序键在这里手写（不调 higher），打分走 scoreTool
// （内部现算小写形态）。若复用，同一个判据写错时两边会一起错，对照就失去意义。
func fullSortSearch(c *Catalog, query string) []Match {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	normalized := normalizeQuery(terms)
	out := make([]Match, 0, len(c.tools))
	for _, name := range c.names {
		e := c.tools[name]
		if s := scoreTool(normalized, terms, e.tool.Name, e.tool.Description); s > 0 {
			out = append(out, Match{Tool: e.tool, Score: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > c.limit {
		out = out[:c.limit]
	}
	return out
}

// TestSearchMatchesFullSortOracle 前 k 选择必须与「全量排序后截断」逐项等价。
//
// 为什么需要这条：Search 边扫边维护前 limit 名，正确性依赖两条推理——排序键是全序
// （名字唯一），以及二分插入的位置单调。推错了的表现是**少了本该命中的工具**或
// 同分项顺序颠倒；已有的端到端用例只验几个手写目录（候选数常常 <= limit），
// 覆盖不到「候选远多于 limit 且分数大量并列」这层。
func TestSearchMatchesFullSortOracle(t *testing.T) {
	const n = 40
	// 两组夹具各有分工：tieTools 全是**并列**（考验同分按名字升序与截断边界），
	// mixedTools 是**多档分值且扫描序与分数无关**（考验中段插入——只有这一层能
	// 把「右移」与「左移」区分开：分值只有两层时插入位置恒在末尾，右移左移同解）。
	dirs := []struct {
		name  string
		tools []Tool
	}{{"tie", tieTools(n)}, {"mixed", mixedTools(n)}}
	for _, d := range dirs {
		name, ts := d.name, d.tools
		for _, limit := range []int{1, 2, 3, 5} {
			c := NewCatalogWithLimit(ts, limit)
			for _, q := range []string{"共用", "alpha", "beta gamma", "alpha beta gamma", "描述", "完全查不到", ""} {
				got, want := c.Search(q), fullSortSearch(c, q)
				if len(got) != len(want) {
					t.Fatalf("%s limit=%d q=%q: 条数不符，得 %v 期望 %v", name, limit, q, names(got), names(want))
				}
				for i := range want {
					if got[i].Name != want[i].Name || got[i].Score != want[i].Score {
						t.Fatalf("%s limit=%d q=%q: 第 %d 项 (名,分值) 不符，得 (%s,%d) 期望 (%s,%d)",
							name, limit, q, i, got[i].Name, got[i].Score, want[i].Name, want[i].Score)
					}
				}
			}
		}
	}
	// 非空洞前提：「共用」命中全部 n 条，故 limit < n 时必须真的被截断到 limit。
	c := NewCatalogWithLimit(dirs[0].tools, 3)
	if got := c.Search("共用"); len(got) != 3 {
		t.Fatalf("该查询命中 %d 条应被截到 3，实得 %d——本条对照没验到截断", n, len(got))
	}
}

// mixedTools 多档分值、且「名字字典序」与「分档」互不相关：靠固定种子的 LCG
// 生成，确定性可复现（不用 math/rand——盘上不该为了夹具多一个随机源）。
func mixedTools(n int) []Tool {
	words := strings.Fields("alpha beta gamma delta epsilon")
	state := uint64(20261108)
	next := func() int {
		state = state*6364136223846793005 + 1442695040888963407
		return int(state >> 33)
	}
	out := make([]Tool, 0, n)
	for i := 0; i < n; i++ {
		k, m := next()%len(words), next()%len(words)
		desc := "处理 " + words[k] + " 与 " + words[m]
		if i%5 == 0 {
			desc += " 的大写形态 Alpha"
		}
		out = append(out, Tool{
			Name:        fmt.Sprintf("n_%02d_%s", i, words[i*7%len(words)]),
			Description: desc,
		})
	}
	return out
}

// BenchmarkSearch / BenchmarkSearchFullSort 检索热路径的一对基准。
//
// 为什么成对：单看 BenchmarkSearch 的绝对值意义不大——它只说明「这次跑了几微秒」。
// 要对着参考实现（全量收集 + sort.Slice + 现算小写形态，即改造前那条路径）跑，
// 才知道改造换来了什么。任何一次调优都该先把这一对跑出来，再谈值不值得。
//
// 量测口径：单包、固定 -benchtime、-count>=3。三包并发跑同一基准时可差 5 倍。
func BenchmarkSearch(b *testing.B) {
	c := NewCatalog(mixedTools(100))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Search("alpha beta gamma")
	}
}

func BenchmarkSearchFullSort(b *testing.B) {
	c := NewCatalog(mixedTools(100))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = fullSortSearch(c, "alpha beta gamma")
	}
}
