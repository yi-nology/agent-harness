package skilldisclose

import (
	"strings"
	"testing"
)

func mustCatalog(t *testing.T, s ...Skill) *Catalog {
	t.Helper()
	c, err := New(s)
	if err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	return c
}

// TestNewRejectsBadCatalog 钉住加载期自检：坏目录必须在**加载时**报错，
// 而不是在披露时给一个「看起来正常」的结果。
func TestNewRejectsBadCatalog(t *testing.T) {
	cases := []struct {
		name   string
		skills []Skill
	}{
		{"空名", []Skill{{Name: " ", Description: "d"}}},
		{"重名", []Skill{{Name: "a", Description: "d"}, {Name: "a", Description: "d2"}}},
		{"空描述", []Skill{{Name: "a"}}},
		{"空资源项", []Skill{{Name: "a", Description: "d", Resources: []string{" "}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.skills); err == nil {
				t.Fatal("坏目录必须报错（静默修正会让「点名」与留痕都失去意义）")
			}
		})
	}
}

// TestMentionBeatsRelevance 钉住：**点名压过一切相关度信号**。
//
// 用户已经明确表达过的意图不该被「描述里有几个词相同」盖过去。
func TestMentionBeatsRelevance(t *testing.T) {
	c := mustCatalog(t,
		Skill{Name: "alpha", Description: "无关描述"},
		Skill{Name: "beta", Description: "alpha alpha alpha 反复提到名字"},
	)
	// 点名 alpha；beta 的描述里有三次 alpha，相关度分数很高。
	res, err := c.Disclose(Query{Mention: "alpha"}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) == 0 || res.Skills[0].Name != "alpha" {
		t.Fatalf("点名必须排第一，实得 %+v", res.Scored)
	}
	if res.Scored[0].Score <= scoreNameInText {
		t.Fatalf("点名分数（%d）必须高于相关度上限（%d）", res.Scored[0].Score, scoreNameInText)
	}
}

// TestDeterministicOrder 同分按名字：结果不能依赖目录的遍历顺序。
func TestDeterministicOrder(t *testing.T) {
	c := mustCatalog(t,
		Skill{Name: "zeta", Description: "x", Tags: []string{"t"}},
		Skill{Name: "alpha", Description: "y", Tags: []string{"t"}},
	)
	res, _ := c.Disclose(Query{Text: "t"}, Budget{})
	if len(res.Skills) != 2 || res.Skills[0].Name != "alpha" || res.Skills[1].Name != "zeta" {
		t.Fatalf("同分必须按名字升序，实得 %+v", res.Skills)
	}
}

// TestTruncationIsExplicit 裁剪必须显式（静默截断会让宿主以为「就这几个相关」）。
func TestTruncationIsExplicit(t *testing.T) {
	c := mustCatalog(t,
		Skill{Name: "a", Description: "x", Tags: []string{"t"}},
		Skill{Name: "b", Description: "y", Tags: []string{"t"}},
	)
	res, _ := c.Disclose(Query{Text: "t"}, Budget{Max: 1})
	if len(res.Skills) != 1 || !res.Truncated {
		t.Fatalf("超预算必须标裁掉，实得 %+v", res)
	}
	// 不超预算时 Truncated 必须为 false（否则这个标记就不再是信号）。
	res2, _ := c.Disclose(Query{Text: "t"}, Budget{Max: 5})
	if res2.Truncated {
		t.Fatal("没裁就不该标裁掉")
	}
	// 字节预算单独生效：1 字节连一条元数据（名 + 描述各 1 字节）都放不下。
	res3, _ := c.Disclose(Query{Text: "t"}, Budget{MaxBytes: 1})
	if len(res3.Skills) != 0 || !res3.Truncated || !strings.Contains(res3.Reason, "放不下") {
		t.Fatalf("字节预算必须生效并标出来，实得 %+v", res3)
	}
}

// TestEmptyResultExplainsItself 空结果必须写明原因。
func TestEmptyResultExplainsItself(t *testing.T) {
	c := mustCatalog(t, Skill{Name: "a", Description: "x", Tags: []string{"t"}})
	res, err := c.Disclose(Query{Text: "完全无关的词"}, Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) != 0 || !strings.Contains(res.Reason, "没有技能命中") {
		t.Fatalf("空结果必须给出原因，实得 %+v", res)
	}
}

// TestExcludeAndResourceMatch 已披露不重复；资源后缀与目录都能命中。
func TestExcludeAndResourceMatch(t *testing.T) {
	c := mustCatalog(t,
		Skill{Name: "vue", Description: "x", Resources: []string{".vue"}},
		Skill{Name: "docs", Description: "y", Resources: []string{"docs/"}},
	)
	res, _ := c.Disclose(Query{Resources: []string{"src/App.vue"}}, Budget{})
	if len(res.Skills) != 1 || res.Skills[0].Name != "vue" {
		t.Fatalf("后缀必须命中，实得 %+v", res.Skills)
	}
	res2, _ := c.Disclose(Query{Resources: []string{"docs/guide.md"}}, Budget{})
	if len(res2.Skills) != 1 || res2.Skills[0].Name != "docs" {
		t.Fatalf("目录前缀必须命中，实得 %+v", res2.Skills)
	}
	res3, _ := c.Disclose(Query{Text: "t", Mention: "vue"}, Budget{})
	_ = res3
	res4, _ := c.Disclose(Query{Mention: "vue", Exclude: []string{"vue"}}, Budget{})
	if len(res4.Skills) != 0 {
		t.Fatalf("已披露的必须排除，实得 %+v", res4.Skills)
	}
}
