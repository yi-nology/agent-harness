package tooldisclose

// 归一化查询的加成语义。合成夹具：工具名用下划线分词，查询用空格/连字符。

import "testing"

func cat(tools ...Tool) *Catalog { return NewCatalog(tools) }

// TestSearchMultiWordEarnsSubstringBonus 多词查询「alpha lookup」必须能吃到整串加成。
//
// 这是本文件存在的理由：工具名按约定是 alpha_lookup，模型多半写「alpha lookup」。
// 拿原始查询串去 Contains，加成永远不触发——而多词正是该加成被设计出来服务的情形。
func TestSearchMultiWordEarnsSubstringBonus(t *testing.T) {
	c := cat(
		Tool{Name: "alpha_lookup", Description: "does the alpha thing"},
		Tool{Name: "beta_report", Description: "unrelated"},
	)

	hits := c.Search("alpha lookup")
	if len(hits) != 1 {
		t.Fatalf("应只命中 alpha_lookup，实得 %d 条", len(hits))
	}
	// 名字两词各命中 = 2×nameHitWeight；描述再命中 alpha 一次 = descHitWeight；
	// 整串归一化后命中名字 = substringBonus。三者缺一都会让分数明显偏低。
	want := 2*nameHitWeight + descHitWeight + substringBonus
	if hits[0].Score != want {
		t.Fatalf("整串加成未生效: got %d want %d", hits[0].Score, want)
	}
}

// TestSearchHyphenAndUnderscoreEquivalent 三种书写形态应等价。
func TestSearchHyphenAndUnderscoreEquivalent(t *testing.T) {
	c := cat(Tool{Name: "alpha_lookup", Description: "x"})

	for _, q := range []string{"alpha_lookup", "alpha-lookup", "alpha lookup", "  ALPHA   Lookup  "} {
		hits := c.Search(q)
		if len(hits) != 1 || hits[0].Score < nameHitWeight {
			t.Fatalf("查询 %q 应等价命中并拿到名字权重，实得 %+v", q, hits)
		}
	}
}

// TestSearchPartialWordsStillScore 单个词查询不受归一化影响。
func TestSearchPartialWordsStillScore(t *testing.T) {
	c := cat(Tool{Name: "alpha_lookup", Description: "does the alpha thing"})
	hits := c.Search("alpha")
	if len(hits) != 1 {
		t.Fatalf("应命中，实得 %d 条", len(hits))
	}
	if hits[0].Score < nameHitWeight {
		t.Fatalf("单词查询应拿到名字权重，实得 %d", hits[0].Score)
	}
}

// TestNormalizeQuery 归一化本身。
func TestNormalizeQuery(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alpha lookup", "alpha_lookup"},
		{"alpha-lookup", "alpha_lookup"},
		{"alpha_lookup", "alpha_lookup"},
		{"Alpha Lookup", "alpha_lookup"},
		{"a", "a"},
	}
	for _, tc := range cases {
		if got := normalizeQuery(tokenize(tc.in)); got != tc.want {
			t.Fatalf("normalizeQuery(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
	if got := normalizeQuery(nil); got != "" {
		t.Fatalf("空词应归一为空串，实得 %q", got)
	}
}

// TestSearchEmptyQueryStillReturnsNothing 归一化不得让空查询变成有结果。
func TestSearchEmptyQueryStillReturnsNothing(t *testing.T) {
	c := cat(Tool{Name: "alpha_lookup", Description: "x"})
	for _, q := range []string{"", "   ", "_", "---"} {
		if hits := c.Search(q); len(hits) != 0 {
			t.Fatalf("查询 %q 应返回空，实得 %+v", q, hits)
		}
	}
}
