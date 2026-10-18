package tooldisclose

// 检索打分的确定性钉住。合成目录：alpha_lookup / beta_report / gamma_send / delta_diff。

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func names(ms []Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// TestSearchNameHitOutweighsDescHit 名字命中远重于描述命中：
// 名字全中的工具必须排在只有描述命中的工具之前。
func TestSearchNameHitOutweighsDescHit(t *testing.T) {
	c := NewCatalog([]Tool{
		{Name: "alpha_lookup", Description: "与查询无关"},
		{Name: "gamma_send", Description: "这个工具的描述里塞满了查询字样 查询 查询 查询"},
	})
	got := names(c.Search("查询"))
	if len(got) != 2 {
		t.Fatalf("两项都该命中，实得 %v", got)
	}
	if got[0] != "alpha_lookup" {
		t.Fatalf("名字命中应重于描述命中，实得 %v", got)
	}
}

// TestSearchSubstringBonusDominates 整串落在名字里 → 拿到那笔大额加成，并压过同分对手。
func TestSearchSubstringBonusDominates(t *testing.T) {
	c := NewCatalog([]Tool{
		{Name: "alpha", Description: "alpha alpha alpha alpha 查询 查询"},
		{Name: "alpha_lookup", Description: "无关描述"},
	})
	ms := c.Search("alpha_lookup")
	if len(ms) != 2 {
		t.Fatalf("两项都该命中，实得 %v", names(ms))
	}
	if ms[0].Name != "alpha_lookup" {
		t.Fatalf("整串命中名字者应居首，实得 %v", names(ms))
	}
	if ms[0].Score <= ms[1].Score {
		t.Fatalf("整串加成分应大于任何逐词累加: %d vs %d", ms[0].Score, ms[1].Score)
	}
}

// TestSearchSubstringBonusVsPerTermTotal 加成与逐词累加的真实关系——把那句注释钉成断言。
//
// 5 个词全部命中名字时逐词总分就是 40，已经大过加成 32：加成**不是**
// 「压过一切逐词累加」的巨奖。写成断言，是为了日后没人照着过强的注释去改分值
// （分值一动，全部检索排序都跟着动），也没人为了「让注释成真」而无谓地抬高加成。
func TestSearchSubstringBonusVsPerTermTotal(t *testing.T) {
	terms := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	whole := strings.Join(terms, "_")

	// 整串命中：每个词都命中名字，再加一笔加成。
	wholeScore := scoreTool(whole, terms, whole, "")
	if want := len(terms)*nameHitWeight + substringBonus; wholeScore != want {
		t.Fatalf("整串命中分值应为 %d, 实得 %d", want, wholeScore)
	}

	// 同样 5 个词全部命中名字，但整串不连成一片（用 '+' 分隔：tokenizer 不认它，
	// 故拿不到归一化整串，也就拿不到加成）——只拿逐词分。
	split := strings.Join(terms, "+")
	splitScore := scoreTool(whole, terms, split, "")
	if want := len(terms) * nameHitWeight; splitScore != want {
		t.Fatalf("拆词命中分值应为 %d, 实得 %d", want, splitScore)
	}

	// 本测试的前提：逐词总分确实大过加成（注释里那句话若被改成「小于」就该重估了）。
	if splitScore <= substringBonus {
		t.Fatalf("前提不成立：逐词总分 %d 未大于加成 %d", splitScore, substringBonus)
	}

	// 注释里的分差公式也要钉住：整串命中者的下界「词数×名字权重+32」减去
	// 逐词者的上界「词数×(名字+描述)权重」，分差恒为 32−2×词数。
	withDesc := scoreTool(whole, terms, split, whole)
	if want := substringBonus - 2*len(terms); wholeScore-withDesc != want {
		t.Fatalf("两者的最坏分差应为 %d, 实得 %d", want, wholeScore-withDesc)
	}
	// 该分差在此词数下仍为正：点名式查询不会被拆词式查询挤掉（常规行为）。
	if wholeScore <= withDesc {
		t.Fatalf("常规词数下整串命中仍应居首: %d vs %d", wholeScore, withDesc)
	}
}

// TestSearchNoHitsReturnsNothing 无命中返回空，绝不兜底给任意候选。
// 兜底等于把一次检索失败变成静默的上下文膨胀，模型还会以为检索有效。
func TestSearchNoHitsReturnsNothing(t *testing.T) {
	c := NewCatalog(tools())
	for _, q := range []string{"完全查不到的词", "", "   ", "___", "- / ."} {
		if got := c.Search(q); len(got) != 0 {
			t.Fatalf("查询 %q 应返回空，实得 %v", q, names(got))
		}
	}
	if got := NewCatalog(nil).Search("任意词"); len(got) != 0 {
		t.Fatalf("空目录应返回空，实得 %v", names(got))
	}
}

// TestSearchTieBreakByName 同分按名字升序，不受构造顺序影响（排序键须是全序）。
func TestSearchTieBreakByName(t *testing.T) {
	// 三个工具描述完全一致 → 分值全同。
	c := NewCatalog([]Tool{
		{Name: "gamma_send", Description: "同样的描述"},
		{Name: "alpha_lookup", Description: "同样的描述"},
		{Name: "beta_report", Description: "同样的描述"},
	})
	want := []string{"alpha_lookup", "beta_report", "gamma_send"}
	for i := 0; i < 20; i++ {
		if got := names(c.Search("同样")); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次同分排序不一致: %v", i, got)
		}
	}
	// 换一种构造顺序，结果必须不变。
	c2 := NewCatalog([]Tool{
		{Name: "beta_report", Description: "同样的描述"},
		{Name: "gamma_send", Description: "同样的描述"},
		{Name: "alpha_lookup", Description: "同样的描述"},
	})
	if got := names(c2.Search("同样")); !reflect.DeepEqual(got, want) {
		t.Fatalf("同分排序不应依赖构造顺序: %v", got)
	}
}

// TestSearchRespectsLimit 结果按钳位后的上限截断（工具数须多于默认上限才看得出截断）。
func TestSearchRespectsLimit(t *testing.T) {
	ts := []Tool{
		{Name: "alpha_lookup", Description: "共用的词"},
		{Name: "beta_report", Description: "共用的词"},
		{Name: "gamma_send", Description: "共用的词"},
		{Name: "delta_diff", Description: "共用的词"},
		{Name: "epsilon_export", Description: "共用的词"},
		{Name: "zeta_list", Description: "共用的词"},
	}
	if len(ts) <= DefaultSearchLimit {
		t.Fatalf("夹具应多于默认上限 %d 项", DefaultSearchLimit)
	}
	if got := NewCatalog(ts).Search("共用"); len(got) != DefaultSearchLimit {
		t.Fatalf("未指定上限时应取默认 %d，实得 %d", DefaultSearchLimit, len(got))
	}
	if got := NewCatalogWithLimit(ts, 2).Search("共用"); len(got) != 2 {
		t.Fatalf("显式上限 2 应生效，实得 %d", len(got))
	}
	if got := NewCatalogWithLimit(ts, 1).Search("共用"); len(got) != 1 {
		t.Fatalf("显式上限 1 应生效，实得 %d", len(got))
	}
}

// TestSearchLimitClamped 上限钳位：非正取默认，超大钳到天花板（防退化成全量披露）。
func TestSearchLimitClamped(t *testing.T) {
	var many []Tool
	for i := 0; i < MaxSearchLimit+5; i++ {
		many = append(many, Tool{Name: string(rune('a'+i%26)) + "_tool" + strconv.Itoa(i), Description: "共用的词"})
	}
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultSearchLimit},
		{-7, DefaultSearchLimit},
		{3, 3},
		{MaxSearchLimit + 1, MaxSearchLimit},
		{1 << 20, MaxSearchLimit},
	}
	for _, tc := range cases {
		if got := clampLimit(tc.in); got != tc.want {
			t.Fatalf("上限 %d 应钳为 %d，实得 %d", tc.in, tc.want, got)
		}
		if got := len(NewCatalogWithLimit(many, tc.in).Search("共用")); got != tc.want {
			t.Fatalf("上限 %d 下检索应得 %d 项，实得 %d", tc.in, tc.want, got)
		}
	}
}

// TestSearchCeilingNeverExceedsMax 天花板不可被检索量突破：
// 命中项数远超 MaxSearchLimit 时，结果必须**恰好** MaxSearchLimit 条，一条不多。
//
// 这条是本包的存在理由。默认上限或钳位天花板一旦松到「目录大小」量级，
// 一次宽泛查询就能把首轮省下的声明费整笔吐回来——检索还在，披露成本却已经退回全量。
func TestSearchCeilingNeverExceedsMax(t *testing.T) {
	var ts []Tool
	for i := 0; i < MaxSearchLimit*4; i++ {
		ts = append(ts, Tool{Name: "tool_" + strconv.Itoa(i), Description: "共用的词"})
	}
	// 默认构造与显式传天花板两条路径都要守住，且默认应比天花板更严。
	cases := []struct {
		name string
		c    *Catalog
		want int
	}{
		{"默认", NewCatalog(ts), DefaultSearchLimit},
		{"显式天花板", NewCatalogWithLimit(ts, MaxSearchLimit*10), MaxSearchLimit},
		{"显式恰好天花板", NewCatalogWithLimit(ts, MaxSearchLimit), MaxSearchLimit},
	}
	for _, tc := range cases {
		got := tc.c.Search("共用")
		if len(got) != tc.want {
			t.Fatalf("%s：命中 %d 项时应恰好截到 %d 条，实得 %d 条: %v",
				tc.name, len(ts), tc.want, len(got), names(got))
		}
		for _, m := range got {
			if m.Score <= 0 {
				t.Fatalf("%s：截断后不应混入零分项: %+v", tc.name, m)
			}
		}
	}
	if DefaultSearchLimit >= MaxSearchLimit {
		t.Fatalf("默认上限 %d 必须严格小于天花板 %d，否则钳位形同虚设",
			DefaultSearchLimit, MaxSearchLimit)
	}
}

// TestSearchPluralQueryFindsSingularTool 复数查询命中单数工具名。
//
// 不剥词尾 s 时，查 files 匹配不到 read_file，模型会以为工具不存在——
// 渐进披露于是在最该救它的一次检索上把它推向「凭占位猜参数」。
func TestSearchPluralQueryFindsSingularTool(t *testing.T) {
	c := NewCatalog([]Tool{
		{Name: "read_file", Description: "读取条目"},
		{Name: "list_dir", Description: "列目录"},
	})
	ms := c.Search("files")
	if len(ms) == 0 || ms[0].Name != "read_file" {
		t.Fatalf("复数查询应命中单数工具名 read_file，实得 %v", names(ms))
	}
	if ms[0].Score < nameHitWeight {
		t.Fatalf("复数查询应至少拿到名字权重 %d，实得 %d", nameHitWeight, ms[0].Score)
	}
	// 短词不得被剥（否则 as/its 之类会被打散），长短各一条都钉住。
	for _, tc := range []struct{ in, want string }{
		{"files", "file"},
		{"list", "list"},
		{"dirs", "dir"},
		{"", ""},
	} {
		if got := stemTerm(tc.in); got != tc.want {
			t.Fatalf("stemTerm(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestSearchTokenizesToolName 分隔符含下划线：空格写法也应吃到名字权重。
func TestSearchTokenizesToolName(t *testing.T) {
	c := NewCatalog(tools())
	for _, q := range []string{"alpha lookup", "alpha_lookup", "alpha-lookup", "ALPHA_LOOKUP", "Alpha Lookup"} {
		ms := c.Search(q)
		if len(ms) == 0 || ms[0].Name != "alpha_lookup" {
			t.Fatalf("查询 %q 应命中 alpha_lookup 首位，实得 %v", q, names(ms))
		}
		if ms[0].Score < nameHitWeight {
			t.Fatalf("查询 %q 应至少拿到名字权重 %d，实得 %d", q, nameHitWeight, ms[0].Score)
		}
	}
}

// TestSearchDescriptionOnlyHit 描述命中也入结果（分值低但非零），检索不至于只认名字。
func TestSearchDescriptionOnlyHit(t *testing.T) {
	c := NewCatalog(tools())
	ms := c.Search("文本")
	if len(ms) != 1 || ms[0].Name != "delta_diff" {
		t.Fatalf("应只有描述含「文本」的工具命中，实得 %v", names(ms))
	}
	if ms[0].Score != descHitWeight {
		t.Fatalf("描述命中分值应为 %d，实得 %d", descHitWeight, ms[0].Score)
	}
}

// TestSearchDeterministic 同一查询反复执行必得同一结果（编排可复现的基础）。
func TestSearchDeterministic(t *testing.T) {
	c := NewCatalog(tools())
	first := c.Search("条目")
	if len(first) == 0 {
		t.Fatal("查询应至少命中一项")
	}
	for i := 0; i < 30; i++ {
		if got := c.Search("条目"); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次检索结果不一致:\n%+v\n%+v", i, first, got)
		}
	}
}

// TestSearchDoesNotActivate 检索只排序、不激活：宽泛查询不得顺手把目录变成已加载。
func TestSearchDoesNotActivate(t *testing.T) {
	c := NewCatalog(tools())
	c.Search("条目")
	if got := c.Loaded(); got != nil {
		t.Fatalf("检索不应产生已加载痕迹，实得 %v", got)
	}
}

// TestExposedMatchCarriesFullTool 命中项携带完整定义，宿主可直接取回 schema。
func TestExposedMatchCarriesFullTool(t *testing.T) {
	c := NewCatalog(tools())
	ms := c.Search("alpha_lookup")
	if len(ms) != 1 {
		t.Fatalf("应恰有一项命中，实得 %v", names(ms))
	}
	if ms[0].Tool.Schema.IsDegenerate() {
		t.Fatal("命中项应带完整 schema")
	}
	if !strings.Contains(ms[0].Description, "按标识查条目") {
		t.Fatalf("命中项应带原描述，实得 %q", ms[0].Description)
	}
}
