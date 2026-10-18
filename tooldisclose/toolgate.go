// Package tooldisclose 渐进式工具披露 + 执行门禁。
//
// # 为什么需要这个包
//
// 工具少的时候「把全部定义塞进上下文」最省事；工具一多，它就是**头号上下文占用源**——
// tool_definitions 是每轮都要重发的固定成本。挂几十个工具，光声明就能把窗口吃满，
// 真正要推理的内容反而放不下。
//
// 但「按需给 schema」有个前提：模型得先知道**有哪些工具**，才会想到去要。故两段式披露：
//
//  1. 首轮只给「名字 + 描述」的**退化占位**，成本极低但可枚举；
//  2. 模型用关键词**检索**换完整 schema，没检索过的继续是占位。
//
// # 为什么执行门禁必须与检索同包
//
// 渐进披露制造了一个此前不存在的失败模式：**模型凭占位猜出一个调用就发过来**。
// 此时只有两种可能，处置方式相反：
//
//   - 工具在目录里、只是 schema 没取回 → 可恢复，顺手激活后重试即成；
//   - 工具压根不在目录里 → 不可恢复，再重试多少次也不会出现。
//
// 把两者混成一句「工具不可用」，后果很具体：上层的自动退避重试会把**策略否定**变成
// 无穷重试。故本包把两种结局做成两个哨兵并各自标注重试性，调用方按 errors.Is 分支。
//
// # 确定性纪律
//
// 目录构造、检索排序、已加载集合并全部无随机、无时钟、无 map 序依赖：同一输入必得同一
// 输出，编排才可复现、回归才可比对。
//
// # 内存边界
//
// 目录是本包唯一随工具数量线性增长的内存，故「单个工具最多占多少」必须是可核对的事实：
// 描述在构造时截到 MaxDescriptionLen，故目录占用有上界，宿主不必逐条自查描述长度。
//
// 反过来，**跨轮复用的缓存不归本包管**：本包没有隐式的请求作用域（参考实现靠 Python 的
// contextvar 天然拿到 thread 级缓存，Go 没有等价物），凭空造一个就是替宿主决定生命周期。
// 宿主若要跨轮持有目录，请自己按会话键持有、会话结束时释放。
//
// # 边界
//
// 本包只管「披露面」与「能不能调」：不执行工具、不认识任何具体工具、不解释参数语义。
// 参数校验、返回值语义、权限判定全归宿主。纯 stdlib，零外部依赖。
package tooldisclose

import (
	"sort"
	"sync"
	"unicode/utf8"
)

// DegenerateType 退化 schema 的顶层类型标记。
//
// 为什么不是空串：空串是「忘了填」的默认值，拿它当占位标记，任何一处遗漏构造都会
// 静默变成占位而没人发现。取一个**真实工具不会声明的类型名**，占位与真身因此不可能混淆。
const DegenerateType = "none"

// Schema 工具输入 schema 的最小载体。
//
// 为什么自定一个结构而不是收 map[string]any：占位的「退化」必须**可机械判定**，
// 否则宿主无法保证「未加载的工具不泄露真 schema」。宿主把自己的 schema 投影进来即可，
// 本包不要求与宿主的 JSON Schema 库同型。
type Schema struct {
	// Type JSON Schema 顶层类型；退化占位恒为 DegenerateType。
	Type string
	// Properties 参数定义；退化占位恒为 nil。
	Properties map[string]Param
	// Required 必填参数名；退化占位恒为 nil。
	Required []string
}

// Param 单个入参的最小定义。
type Param struct {
	Type        string
	Description string
}

// Degenerate 空 schema：声明不了任何参数，故任何调用都必然参数非法。
//
// 这一点是**故意**的：占位若碰巧还能被调用，模型就会把占位当契约继续瞎编参数，
// 门禁也就没机会把它挡回来。
func Degenerate() Schema { return Schema{Type: DegenerateType} }

// IsDegenerate 是否为空 schema 占位。
func (s Schema) IsDegenerate() bool { return s.Type == DegenerateType }

// Tool 目录里的一条完整工具定义。
type Tool struct {
	Name        string
	Description string
	Schema      Schema
}

// Exposure 一次披露投给模型的工具面。
type Exposure struct {
	Name        string
	Description string
	Schema      Schema
	// Loaded 该面是否为完整面（占位面恒为 false）。
	Loaded bool
}

const (
	// DefaultSearchLimit 检索默认上限。取 3 的理由：一次检索的价值是**把候选缩到模型读得完
	// 的一小屏**，三条已足够表达「你要的是这类工具里的哪几个」；再多只是把首轮省下的
	// 声明费原样花回去。模型检索不准时可以再查一次——窄面 + 可重复，远好过一次糊一大片。
	DefaultSearchLimit = 3

	// MaxSearchLimit 检索上限的钳位天花板。防的是宿主误传一个天文数字，把「按需披露」
	// 退化成「一次性全量披露」——那等于本包没装。天花板压到 5：单次检索能吐出的东西
	// 必须**永远少于一个工具全集**，否则「省上下文」只是把省的动作挪到了检索里。
	MaxSearchLimit = 5

	// MaxDescriptionLen 目录内单条描述的字符上限（按 rune 计），超出在构造时截断。
	//
	// 为什么截断而不是写进文档靠自觉：描述是宿主给的自由文本，长度不受本包控制，而目录是
	// 本包**唯一**按工具数量线性增长的内存。不设界，工具一多描述就能吃满整个进程；
	// 落成常量后「单个工具最多占多少」才是可核对的事实而不是一条约定。
	// 取 320 与参考实现对齐：同一份注册表在两侧应得到同一长度的目录，否则「谁更长」
	// 变成实现细节，而目录内容是要逐轮发给模型的。
	MaxDescriptionLen = 320
)

// truncateDescription 把描述截到 MaxDescriptionLen 个字符；未超长原样返回（不复制）。
//
// 按 rune 而非字节截：按字节切会从多字节字符中间劈开，产出乱码直接进了模型上下文。
func truncateDescription(s string) string {
	if utf8.RuneCountInString(s) <= MaxDescriptionLen {
		return s
	}
	return string([]rune(s)[:MaxDescriptionLen])
}

// Catalog 工具目录 + 已加载集。
//
// 不变式：**目录成员在构造后不再变化**（没有删除/新增入口），只有已加载集可变。
// 门禁正是靠这条不变式把「在目录内」判成一个稳定事实：并发加载不会让同一个名字
// 在两次调用里一会儿在、一会儿不在。
type Catalog struct {
	mu     sync.RWMutex
	tools  map[string]Tool
	loaded map[string]bool
	limit  int
}

// NewCatalog 按默认检索上限构造目录。
func NewCatalog(tools []Tool) *Catalog { return NewCatalogWithLimit(tools, DefaultSearchLimit) }

// NewCatalogWithLimit 构造目录并指定检索上限（钳位到 [1, MaxSearchLimit]；<=0 取默认值）。
//
// # 重名规则：首次出现处胜出（first wins）
//
// 目录入参是宿主给的注册表，重名意味着两个来源各声明了一个同名工具。选 first wins 而不是
// last wins，理由有三：
//
//  1. **与本仓既有去重同款**（checkpoint.BuildPlan 的编排图净化同样是保留首次出现处），
//     同一种理由在仓内只有一种答案，避免两处各自漂移。
//  2. last wins 会让「追加一条覆盖」成为事实上的默认用法，而覆盖发生在**调用方拼注册表**
//     时——那是个拼装细节，却悄悄改变了模型能调到什么；first wins 则把「重名」保持在
//     「装配问题」这一层，不外溢成运行期行为。
//  3. 两种规则都能确定，但 first wins 保留了首次声明的位置与内容，故障排查时
//     「这条工具哪来的」指向唯一来源。
//
// 重名**静默去重**：目录是可复现的装配结果，不是诊断面。宿主若需要发现冲突，
// 在调用本包前自己按名去重即可——那属于装配期的校验，不属于运行期门禁。
func NewCatalogWithLimit(tools []Tool, limit int) *Catalog {
	c := &Catalog{
		tools:  make(map[string]Tool, len(tools)),
		loaded: make(map[string]bool, len(tools)),
		limit:  clampLimit(limit),
	}
	for _, t := range tools {
		// 空名工具既检索不到也调不到，留着只会虚增目录计数。
		if t.Name == "" {
			continue
		}
		if _, dup := c.tools[t.Name]; dup {
			continue
		}
		// 在**存进目录前**截断，而不是渲染时再截：检索打分读的就是这份存储值，
		// 惰性截断会让打分面对全文、模型面对摘要，两边看到的不是同一件事。
		t.Description = truncateDescription(t.Description)
		c.tools[t.Name] = t
	}
	return c
}

// Names 目录内全部工具名，升序。
func (c *Catalog) Names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return namesOf(c.tools)
}

// Len 目录内工具数（去重后）。
func (c *Catalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tools)
}

// Has 该工具是否在目录内。
func (c *Catalog) Has(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.tools[name]
	return ok
}

// Lookup 取完整定义；不在目录内返回 (零值, false)。
func (c *Catalog) Lookup(name string) (Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	return t, ok
}

// Load 取回完整 schema 并标记为已加载，幂等（重复取回不改变任何状态）。
//
// 这是「披露」的唯一写入口：谁把工具变成完整面，在这里留痕，而不是各调用点各记一份。
func (c *Catalog) Load(name string) (Tool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tools[name]
	if !ok {
		return Tool{}, false
	}
	c.loaded[name] = true
	return t, true
}

// IsLoaded 该工具的 schema 是否已取回。
func (c *Catalog) IsLoaded(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded[name]
}

// Loaded 已加载的工具名，升序。
func (c *Catalog) Loaded() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return namesOf(c.loaded)
}

// Expose 按名取披露面：已加载给完整 schema，未加载给占位。
// 不在目录内返回 (零值, false)——不存在的东西没有占位面，占位只对「已存在但未取回」有意义。
func (c *Catalog) Expose(name string) (Exposure, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	if !ok {
		return Exposure{}, false
	}
	return exposureOf(t, c.loaded[name]), true
}

// ExposeAll 全目录披露面，按名升序。
//
// 每轮装配工具面走这一个出口，避免调用方自己拼「哪些给全、哪些给占位」而出现分叉。
func (c *Catalog) ExposeAll() []Exposure {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Exposure, 0, len(c.tools))
	for _, name := range namesOf(c.tools) {
		t := c.tools[name]
		out = append(out, exposureOf(t, c.loaded[name]))
	}
	return out
}

func exposureOf(t Tool, loaded bool) Exposure {
	if !loaded {
		return Placeholder(t.Name, t.Description)
	}
	return Exposure{Name: t.Name, Description: t.Description, Schema: t.Schema, Loaded: true}
}

// namesOf 收集集合成员并升序排列。map 序是随机化的，所有出口一律走这里排序，
// 免得「同一个集合、两个出口、两份顺序」。
func namesOf[V any](set map[string]V) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
