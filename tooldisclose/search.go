package tooldisclose

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/oekit/pkg/pagination"
)

// 打分权重。为什么名字远重于描述：描述是**自由文本**，几乎每个工具的描述里都塞着
// 「查询」「报告」这类泛词，按描述命中排序接近按噪声排；名字是宿主给的稳定标识，
// 命中它几乎总是模型真想要的那个工具。
const (
	nameHitWeight  = 8
	descHitWeight  = 2
	substringBonus = 32
)

// Match 一条检索命中。Score 只用于排序与留痕/调参，**不是**置信度，调用方不应拿它做阈值决策。
type Match struct {
	Tool
	Score int
}

// Search 按关键词检索目录，返回按 (分值降序, 名字升序) 排好并已截断的命中。
//
// # 无命中返回空，不兜底给「随便几个」
//
// 这是本包最重要的一条语义。渐进披露的成本模型建立在「模型问什么、我们给什么」上；
// 无命中时兜底塞几个候选，等于把一次检索失败变成一次静默的上下文膨胀，而且模型会以为
// 检索有效，下一轮继续照着错的候选走。宁可让它看见「没查到」——它会换个词再来一次。
//
// 检索只排序、**不激活**：激活是宿主（或门禁）的显式决定。若检索顺手把命中项全部标成已加载，
// 一次宽泛的查询就能把刚省下来的上下文又吐回去，本包也就退化成了「带搜索框的全量披露」。
func (c *Catalog) Search(query string) []Match {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil // 没有检索词就没有检索意图，不猜
	}
	normalized := normalizeQuery(terms)

	c.mu.RLock()
	defer c.mu.RUnlock()

	r := ranker{limit: c.limit}
	for _, name := range c.names {
		e := c.tools[name]
		if score := scoreLowered(normalized, terms, e.lowerName, e.lowerDesc); score > 0 {
			r.add(Match{Tool: e.tool, Score: score})
		}
	}
	// 有检索词但无命中：返回**空切片而非 nil**（旧行为）。调用方拿 nil 与空切片做
	// 分支的写法在本包之外不该出现，但这条是既有契约，改它就是改语义。
	if r.top == nil {
		return []Match{}
	}
	return r.top
}

// ranker 实时维护分值前 limit 名命中。
//
// # 为什么不「先全收、再排序、再截断」
//
// 排序键 (分值, 名字) 在目录内是**全序**——名字唯一由 NewCatalogWithLimit 的
// 重名规则（first wins）保证，故「排序后取前 k」与「边扫边维护前 k」逐项等价，
// 结果连顺序都逐字相同。等价的好处是量纲：旧写法按 len(c.tools) 预分配结果切片，
// 而 Match 内嵌**完整 Tool**（含 Schema），100 个工具时每次检索先摊 ~8.8 KB 临时
// 内存、最终只留最多 5 条；且那次排序是 O(n log n)，前 k 插入是 O(n·k)。
//
// 目录成员不变这条不变式还让「扫描面」本身无需每次重建：名单在构造期定稿
// （见 Catalog.names）。
type ranker struct {
	top   []Match
	limit int
}

// higher 报告 a 是否排在 b 之前：分值降序，同分按名字升序。
func higher(a, b Match) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Name < b.Name
}

// add 把一条命中并入前 limit 名；排不进去就丢弃。
//
// 插入位置用二分：top 恒按 higher 升序（即"最好的在前"），故「m 排在 top[j] 之前」
// 这个谓词是**单调**的（真值从某个 j 起恒真），sort.Search 的前提成立。
func (r *ranker) add(m Match) {
	i := sort.Search(len(r.top), func(j int) bool { return higher(m, r.top[j]) })
	if i >= r.limit {
		return // 连末位都挤不进：更差的候选不影响结果
	}
	if len(r.top) < r.limit {
		r.top = append(r.top, Match{})
	}
	// 从 i 起整体右移一格，末位自然被挤掉（len==limit 时即"丢最后一名"）。
	copy(r.top[i+1:], r.top[i:len(r.top)-1])
	r.top[i] = m
}

// scoreTool 单条打分（按原样入参，内部小写化），恒 >= 0；0 表示无命中（不入结果）。
//
// 检索主路径不调它：目录在构造期已把小写形态算好（见 entry），主路径走
// scoreLowered 直接给形态。这里保留「入参小写化」这层包装，是为了让**打分规则**
// 与它的调用面分成两件事——规则只在 scoreLowered 里写一份，包装不做任何额外判断。
// 否则两套实现各判一套，漂移只是时间问题（同一条纪律见 scan.go 的 regexViolationsIn）。
func scoreTool(normalized string, terms []string, name, description string) int {
	return scoreLowered(normalized, terms, strings.ToLower(name), strings.ToLower(description))
}

// scoreLowered 单条打分的**唯一**实现；入参须已是小写形态。
func scoreLowered(normalized string, terms []string, ln, ld string) int {
	score := 0
	for _, term := range terms {
		if strings.Contains(ln, term) {
			score += nameHitWeight
		}
		if strings.Contains(ld, term) {
			score += descHitWeight
		}
	}
	// 整串命中给一笔大额加成。加成**不是**「压过一切逐词累加」的巨奖，如实说清关系：
	// 加成 32 = 4 次名字命中（4×nameHitWeight）的量级；5 个词全部命中名字时逐词总分
	// 就是 40，已经大过加成本身，此时「名字命中更多的一方胜出」是合理结果而非缺陷——
	// 模型把整个名字拆成多词写，逐词命中本就更强。
	//
	// 加成真正压住的是**同分**：模型点名写「alpha_lookup」与拆词写「alpha lookup」，
	// 两者在名字上命中的词数常常一样，不加成就并列；加成把这一层意图差异拉开。
	// 精确账：整串命中者至少拿「词数×名字权重+32」，只靠逐词者至多「词数×(名字+描述)权重」，
	// 分差就是 32−2×词数——常规词数（≤15）下为正，点名式查询不会被拆词式查询挤掉。
	//
	// 匹配对象是**归一化后的查询**（词以下划线相连），不是原始查询串：
	// 工具名按约定用下划线分词（alpha_lookup），而模型多半写成「alpha lookup」。
	// 拿原始串去 Contains，多词查询就永远拿不到这个加成——恰恰是多词查询最需要它。
	if normalized != "" && strings.Contains(ln, normalized) {
		score += substringBonus
	}
	return score
}

// normalizeQuery 把查询词以下划线相连，得到可与工具名直接比对的形态。
// 「alpha lookup」/「alpha-lookup」/「alpha_lookup」三者归一为同一串。
//
// 词来自 tokenize（已剥词尾 s），但这条不受影响：分词器只在 `>_` 类分隔处断词，
// 剥 s 是**词内**操作，故「alpha lookup」仍归一为 alpha_lookup。加成判的是
// 「归一化查询整串落在名字里」，模型写复数时归一化也随之带复数形状，不会因此错判。
func normalizeQuery(terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, "_")
}

// tokenize 把查询切成检索词。分隔符含下划线与连字符：工具名的实际分词就是它们
// （alpha_lookup / alpha-lookup），只按空格切的话「alpha lookup」永远吃不到名字权重。
func tokenize(query string) []string {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '_', '-', '.', ':', '/':
			return true
		default:
			return false
		}
	})
	for i, t := range terms {
		terms[i] = stemTerm(t)
	}
	return terms
}

// stemTerm 剥掉一个词尾的 s，仅当长度 > 3（按 rune 计）。
//
// 为什么要有这一步：模型查的是能力关键词，复数是最常见的错配形态——查 files 找不到
// read_file，模型会认定「没这个工具」，转而凭占位猜参数，而那正好是门禁要挡的失败。
// 为什么只做这一条规则、长度门槛 3：规则再多就变成一套谁也调不准的词法器；短词剥 s
// 会把 as/is/its 这类打散，净亏。参考实现同一条规则，故两侧对同一查询给同一形态。
//
// 按 rune 计长度：按字节计会让含多字节字符的短词越过门槛，与参考实现不一致。
func stemTerm(term string) string {
	if utf8.RuneCountInString(term) > 3 && strings.HasSuffix(term, "s") {
		return term[:len(term)-1]
	}
	return term
}

// clampLimit 钳位检索上限到 [1, MaxSearchLimit]，非正值取默认。
//
// 口径（默认条数 / 上限 / 超限钳到上限）由 oekit pagination.Policy 统一表达，
// 本仓只声明自己的两个数字，不再各写一份钳位逻辑。
func clampLimit(n int) int {
	policy := pagination.Policy{
		DefaultPageSize: DefaultSearchLimit,
		MaxPageSize:     MaxSearchLimit,
		OverMax:         pagination.OverMaxClamp,
	}
	return policy.NormalizeLimit(n)
}
