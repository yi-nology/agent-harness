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
	skills []entry
	byName map[string]Skill
}

// entry 目录成员 + 打分会用到的小写形态。
//
// # 为什么把派生形态存下来
//
// 披露是大小写无关的，而打分在**内层循环**里取值：描述要对着每一个查询词判一次包含。
// 现算的写法是 strings.ToLower(s.Description) 写在 `for w := range words` 里——
// 同一份描述被小写化 len(words) 次/技能。而含非 ASCII 的串走 ToLower 的 rune 映射
// 慢路径（unicode.lookupCaseRange），实测 100 技能 × 8 词的中文目录每次披露要
// 1.6–2.4 ms，改造后降到几十微秒。
//
// 目录成员在 New 之后不再变化（没有任何增删入口），故这份派生值算一次即与真相同寿。
// 内存代价：只有**真含大写**的串才多占一份（strings.ToLower 在无字符改变时原样
// 返回入参），中文为主的描述因此与原文共用同一份字节。
type entry struct {
	skill          Skill
	lowerName      string
	lowerDesc      string
	lowerTags      []string
	lowerResources []string
}

// lowerEach 逐项小写化（nil 入参原样返回 nil，避免把 nil 变成空切片）。
//
// 供**标签**与**查询侧资源路径**用：原实现对这两者只做 ToLower，不 TrimSpace。
// 「顺手 trim 一下更稳」在这里不是改进而是改语义——带首尾空格的标签因此会从不命中
// 变成命中，而这类改动不会报错，只会悄悄改变披露结果。
func lowerEach(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

// lowerDeclared 逐项「TrimSpace + 小写化」。供**技能声明的资源项**用：
// 原实现正是这么规范化的（声明是宿主手写的，允许留空格；查询路径不是）。
func lowerDeclared(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return out
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
		c.skills = append(c.skills, entry{
			skill:          s,
			lowerName:      strings.ToLower(name),
			lowerDesc:      strings.ToLower(s.Description),
			lowerTags:      lowerEach(s.Tags),
			lowerResources: lowerDeclared(s.Resources),
		})
		c.byName[name] = s
	}
	sort.Slice(c.skills, func(i, j int) bool { return c.skills[i].skill.Name < c.skills[j].skill.Name })
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
	// 点名词与资源路径都是**查询级**的不变量：小写化一次，别在技能循环里重复。
	mentionLower := strings.ToLower(mention)
	pathsLower := lowerEach(q.Resources)

	var scored []Scored
	for _, e := range c.skills {
		if excluded[e.skill.Name] {
			continue
		}
		score := scoreOf(e, mention, mentionLower, words, pathsLower)
		if score == 0 {
			continue
		}
		scored = append(scored, Scored{Skill: e.skill, Score: score})
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
//
// 入参全是**已经小写化**的形态：它们要么来自目录构造期（entry 上的 lower* 字段），
// 要么来自查询级预计算（mentionLower / pathsLower）。这条口径必须写在签名上而不是
// 靠注释约定——一旦有人在循环里传回原样，结果不会报错，只会**静默漏命中**：
// 查询「Alpha」再也匹配不到描述里的「ALPHA」。
func scoreOf(e entry, mention, mentionLower string, words map[string]bool, pathsLower []string) int {
	s := e.skill
	if mention != "" && mention == s.Name {
		return scoreMention
	}
	score := 0
	if mentionLower != "" && strings.Contains(e.lowerName, mentionLower) {
		score += scoreNameInText
	}
	if words[e.lowerName] {
		score += scoreNameInText
	}
	for _, p := range pathsLower {
		if resourceHitLower(e.lowerResources, p) {
			score += scoreResource
			break
		}
	}
	for _, tag := range e.lowerTags {
		if words[tag] {
			score += scoreTag
			break
		}
	}
	descHits := 0
	for w := range words {
		// len(w) 按字节判即可：tokenize 只产出 ASCII 字母数字，字节数即字符数。
		if w == "" || len(w) < 2 {
			continue
		}
		if strings.Contains(e.lowerDesc, w) {
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
// resourceHitLower 资源匹配的**唯一**判定实现；两侧入参须已是小写形态。
//
// 为什么不提供「入参原样、内部小写化」的包装版本：包装层一旦存在，它和主路径就
// 成了两套实现（一套在循环里、一套在循环外），而两边漂移不会报错，只会让某些声明
// 悄悄不再命中。主路径的小写化位置是明确的——声明在构造期（lowerDeclared）、
// 查询路径在查询级（lowerEach）——规则就只剩这一份。
func resourceHitLower(declaredLower []string, pathLower string) bool {
	for _, d := range declaredLower {
		switch {
		case d == "":
			continue
		case strings.HasSuffix(d, "/"):
			if strings.HasPrefix(pathLower, d) {
				return true
			}
		case strings.HasPrefix(d, "."):
			if strings.HasSuffix(pathLower, d) {
				return true
			}
		default:
			if pathLower == d || strings.Contains(pathLower, d) {
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
