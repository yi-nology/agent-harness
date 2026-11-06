// Package skilldisclose 技能披露：**确定性**挑出该给模型看的那几个技能。
//
// # 与 tooldisclose 的分工
//
// @@tooldisclose@ 解决的是「工具太多，一次给全量 schema 撑爆上下文」，它的检索
// 键是**工具调用需求**（我要做什么）。技能不一样：技能是**带资源文件的工作流**，
// 触发信号有三种且强度不同——① 用户/程序**点名**了它；② 描述与当前任务相关；
// ③ 当前要读的文件落在它声明的资源里。把三者混成一个分数就会让「相关」压过
// 「点名」，而那正是用户已经明确表达过的意图。
//
// # 本包只做判定，不做投递
//
// 披露的**内容**（正文注入、文件读取、渐进式加载）归宿主。本包给的是：
// 给定目录与查询，**挑哪几个**、**为什么挑它们**、**有没有被预算裁掉**。
//
// # 三条可机械判定的判据
//
//  1. **点名优先**：命中名字的分数必须高于任何「相关度」信号，且不受描述长度影响。
//  2. **确定性**：同分按名字字典序，结果与目录的遍历顺序、map 顺序无关。
//  3. **裁剪显式**：预算裁掉了候选时必须标出来（@@Result.Truncated@）。
//     静默截断会让宿主以为「就这几个技能相关」，而实际上有更多被挤掉了。
//
// # 空结果不是静默
//
// 没命中时 @@Reason@ 必须写明「为什么是空的」。空结果与「目录里只有一个技能，
// 正好命中」在调用方看起来一样，而前者是「模型不知道有这个能力」的形态——
// 与「技能不存在」不是一回事，宿主据此要不要回退到全量披露。
package skilldisclose

import (
	"sort"
	"strings"

	"github.com/oekit/pkg/errors"
)

// Skill 一个技能的**披露元数据**（不含正文：正文归宿主投递）。
type Skill struct {
	// Name 技能名（唯一，模型按它点名）。
	Name string
	// Description 一句话描述（相关度打分用）。
	Description string
	// Resources 该技能能处理的资源路径/后缀（如 "**/*.vue"、"deploy/"）。
	Resources []string
	// Tags 自由标签（宿主可用它做粗筛）。
	Tags []string
}

// Catalog 已登记的技能集合。
type Catalog struct {
	skills []Skill
	byName map[string]Skill
}

// New 建目录并做**加载期自检**。
//
// 自检项（任一不满足即报错，不静默去重/取第一个）：
//
//   - 名字非空且唯一（重名会让「点名」指到两个技能，而判定方无从知道是哪个）；
//   - 描述非空（没有描述就无法按相关度打分，它只能靠点名命中——那等于假装登记了）；
//   - 资源项非空串（空串会匹配一切，把一个技能变成万能匹配）。
func New(skills []Skill) (*Catalog, error) {
	c := &Catalog{byName: make(map[string]Skill, len(skills))}
	for _, s := range skills {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return nil, errors.New("skilldisclose: 技能名为空（点名与留痕都无从定位）")
		}
		if _, ok := c.byName[name]; ok {
			return nil, errors.Errorf("skilldisclose: 技能名 %q 重复（点名会指到两个技能）", name)
		}
		if strings.TrimSpace(s.Description) == "" {
			return nil, errors.Errorf("skilldisclose: 技能 %q 没有描述（只能靠点名命中，等于假装登记了）", name)
		}
		for _, r := range s.Resources {
			if strings.TrimSpace(r) == "" {
				return nil, errors.Errorf("skilldisclose: 技能 %q 有空的资源项（空串会匹配一切）", name)
			}
		}
		s.Name = name
		c.skills = append(c.skills, s)
		c.byName[name] = s
	}
	sort.Slice(c.skills, func(i, j int) bool { return c.skills[i].Name < c.skills[j].Name })
	return c, nil
}

// Query 一次披露请求。
type Query struct {
	// Mention 点名（用户/程序明确提到的技能名，可空）。
	Mention string
	// Text 当前任务的自然语言描述（相关度打分用）。
	Text string
	// Resources 当前上下文里涉及的文件路径（资源匹配用）。
	Resources []string
	// Exclude 已披露过的技能（**不重复披露**：重复注入会挤占窗口且没有新信息）。
	Exclude []string
}

// Budget 披露预算。
type Budget struct {
	// Max 最多披露几个（<=0 表示不限制数量）。
	Max int
	// MaxBytes 元数据字节上限（<=0 表示不限制）。
	MaxBytes int
}

// Scored 一个候选及其分数（留着让宿主能解释「为什么是它」）。
type Scored struct {
	Skill Skill
	Score int
}

// Result 披露结果。
type Result struct {
	// Skills 按分数降序（同分按名字）挑出的技能。
	Skills []Skill
	// Scored 全部候选的完整打分（含未入选者，留痕用）。
	Scored []Scored
	// Truncated 是否因预算裁掉了候选。
	Truncated bool
	// Reason 空结果或裁剪的原因（人读；空结果时必须写）。
	Reason string
}

// 打分权重。点名必须压过一切相关度信号：用户已经明确表达过的意图不该被
// 「描述里有几个词相同」盖过去。
const (
	scoreMention     = 1000
	scoreNameInText  = 100
	scoreResource    = 30
	scoreTag         = 20
	scoreDescWordMax = 5
)

// Disclose 按查询挑技能。
func (c *Catalog) Disclose(q Query, b Budget) (Result, error) {
	excluded := make(map[string]bool, len(q.Exclude))
	for _, e := range q.Exclude {
		excluded[e] = true
	}
	words := tokenize(q.Text)
	mention := strings.TrimSpace(q.Mention)

	var scored []Scored
	for _, s := range c.skills {
		if excluded[s.Name] {
			continue
		}
		score := scoreOf(s, mention, words, q.Resources)
		if score == 0 {
			continue
		}
		scored = append(scored, Scored{Skill: s, Score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Skill.Name < scored[j].Skill.Name
	})

	res := Result{Scored: scored}
	if len(scored) == 0 {
		res.Reason = "没有技能命中本次查询（点名/描述/资源三项都没匹配）——" +
			"宿主应据此判断要不要回退到全量披露，而不是当成「没有这个能力」"
		return res, nil
	}

	picked := scored
	if b.Max > 0 && len(picked) > b.Max {
		picked = picked[:b.Max]
		res.Truncated = true
		res.Reason = "候选数超过 Max"
	}
	if b.MaxBytes > 0 {
		var kept []Scored
		used := 0
		for _, sc := range picked {
			n := len(sc.Skill.Name) + len(sc.Skill.Description)
			if used+n > b.MaxBytes {
				res.Truncated = true
				if res.Reason == "" {
					res.Reason = "元数据字节数超过 MaxBytes"
				}
				break
			}
			used += n
			kept = append(kept, sc)
		}
		picked = kept
		if len(picked) == 0 {
			res.Reason = "预算连一条元数据都放不下（MaxBytes 太小）"
		}
	}
	for _, sc := range picked {
		res.Skills = append(res.Skills, sc.Skill)
	}
	return res, nil
}

// scoreOf 单个技能的打分（0 = 不命中）。
func scoreOf(s Skill, mention string, words map[string]bool, resources []string) int {
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
		if resourceHit(s.Resources, r) {
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

// resourceHit 判当前资源路径是否落在技能的声明里。
//
// 支持两种声明形态：**目录前缀**（以 / 结尾）与**后缀**（以 . 开头）。
// 不做完整 glob：glob 的语义（** 是否跨层）各实现不同，而「按目录/按后缀」
// 这两种已经能表达全部实际用法，且判据无歧义。
func resourceHit(declared []string, path string) bool {
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

// tokenize 把一段文本切成小写词集（按非字母数字切分）。
func tokenize(s string) map[string]bool {
	out := map[string]bool{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out[strings.ToLower(cur.String())] = true
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}
