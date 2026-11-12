package skilldisclose

import (
	"strconv"
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

// rawResourceHit / scoreOfRaw 是**改造前**的实现，逐字保留下来做对照。
//
// 为什么需要它们：改造把小写化从内层循环提到构造期与查询级（entry.lower*），
// 顺带把「声明资源要 trim、标签与查询路径不 trim」这条既有口径拆成两个助手。
// 这条差异**不报错**：trim 加错了只会让带空格的标签从不命中变成命中，披露结果
// 悄悄变了而没有任何门禁会响。故两边各算一遍分数逐项比对——参考实现刻意
// 不复用生产的 resourceHitLower / lowerDeclared，否则同一个判据写错时两边一起错。
func rawResourceHit(declared []string, path string) bool {
	p := strings.ToLower(path)
	for _, d := range declared {
		d = strings.ToLower(strings.TrimSpace(d))
		switch {
		case d == "":
			continue
		case strings.HasSuffix(d, "/"):
			if strings.HasPrefix(p, d) {
				return true
			}
		case strings.HasPrefix(d, "."):
			if strings.HasSuffix(p, d) {
				return true
			}
		default:
			if p == d || strings.Contains(p, d) {
				return true
			}
		}
	}
	return false
}

func scoreOfRaw(s Skill, mention string, words map[string]bool, resources []string) int {
	if mention != "" && mention == s.Name {
		return scoreMention
	}
	score := 0
	if mention != "" && strings.Contains(strings.ToLower(s.Name), strings.ToLower(mention)) {
		score += scoreNameInText
	}
	if words[strings.ToLower(s.Name)] {
		score += scoreNameInText
	}
	for _, r := range resources {
		if rawResourceHit(s.Resources, r) {
			score += scoreResource
			break
		}
	}
	for _, tag := range s.Tags {
		if words[strings.ToLower(tag)] {
			score += scoreTag
			break
		}
	}
	descHits := 0
	for w := range words {
		if w == "" || len(w) < 2 {
			continue
		}
		if strings.Contains(strings.ToLower(s.Description), w) {
			descHits++
			if descHits >= scoreDescWordMax {
				break
			}
		}
	}
	return score + descHits
}

// TestScoreOfMatchesRawOracle 预计算形态与现算形态必须逐项同分。
//
// 夹具刻意带大小写与首尾空格：名字/描述含大写（考验 lowerName/lowerDesc），
// 标签含空格（**不该** trim）、声明资源含空格（**该** trim）、查询路径含空格
// （**不该** trim）。这几条正是最容易被「顺手更稳」改掉的。
func TestScoreOfMatchesRawOracle(t *testing.T) {
	c := mustCatalog(t,
		Skill{Name: "Alpha_Tool", Description: "处理 ALPHA 与 Beta 的差异",
			Tags: []string{"alpha", " beta "}, Resources: []string{" Deploy/ ", ".VUE", "src"}},
		Skill{Name: "beta_runner", Description: "跑 BETA 用例并汇总",
			Tags: []string{"Beta"}, Resources: []string{".Go"}},
		Skill{Name: "gamma", Description: "无关描述", Tags: nil, Resources: nil},
	)
	cases := []struct {
		mention   string
		text      string
		resources []string
	}{
		{"", "alpha beta", []string{"src/main.go"}},
		{"alpha_tool", "alpha", []string{"deploy/x.yaml"}},
		{"ALPHA_TOOL", "beta", []string{"a.vue"}},
		{"", "", []string{"x.go"}},
		{"gamma", "alpha", nil},
		{"", "gamma", []string{" SRc/x.go "}},
	}
	hit, miss := false, false
	for _, tc := range cases {
		words := tokenize(tc.text)
		mention := strings.TrimSpace(tc.mention)
		mentionLower := strings.ToLower(mention)
		pathsLower := lowerEach(tc.resources)
		for _, e := range c.skills {
			got := scoreOf(e, mention, mentionLower, words, pathsLower)
			want := scoreOfRaw(e.skill, mention, words, tc.resources)
			if got != want {
				t.Fatalf("mention=%q text=%q res=%v 技能 %s: 预计算 %d != 现算 %d",
					mention, tc.text, tc.resources, e.skill.Name, got, want)
			}
			if got == 0 {
				miss = true
			} else {
				hit = true
			}
		}
	}
	// 非空洞前提：这组用例必须同时出现「命中」与「不命中」，否则比对恒等于 0==0。
	if !hit || !miss {
		t.Fatalf("用例未覆盖命中与不命中两种结局（hit=%v miss=%v）——本条对照证明不了什么", hit, miss)
	}
}

// BenchmarkDisclose 披露热路径基准。目录成员的小写形态在 New 里算一次，
// 这里量的是「每次披露」的成本（打分 + 排序 + 预算裁剪）。
func BenchmarkDisclose(b *testing.B) {
	skills := make([]Skill, 0, 100)
	for i := 0; i < 100; i++ {
		skills = append(skills, Skill{
			Name:        "skill_" + strconv.Itoa(i),
			Description: "处理代码变更并汇总差异，产出结构化报告，供后续环节复核与归档使用。",
			Resources:   []string{"deploy/", ".vue", "src/"},
			Tags:        []string{"alpha", "beta"},
		})
	}
	c, err := New(skills)
	if err != nil {
		b.Fatal(err)
	}
	q := Query{Text: "alpha beta gamma delta epsilon zeta eta theta", Resources: []string{"src/main.go"}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Disclose(q, Budget{Max: 5}); err != nil {
			b.Fatal(err)
		}
	}
}
