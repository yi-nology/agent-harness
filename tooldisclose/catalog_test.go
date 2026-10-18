package tooldisclose

// 目录与披露面的行为钉住。合成工具集：alpha_lookup / beta_report / gamma_send / delta_diff。

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func tools() []Tool {
	return []Tool{
		{Name: "alpha_lookup", Description: "按标识查条目", Schema: fullSchema()},
		{Name: "beta_report", Description: "导出汇总报表", Schema: fullSchema()},
		{Name: "gamma_send", Description: "投递一条消息", Schema: fullSchema()},
		{Name: "delta_diff", Description: "比较两段文本差异", Schema: fullSchema()},
	}
}

// TestCatalogDuplicateNameFirstWins 重名目录条目只保留首次出现处（first wins 规则）。
func TestCatalogDuplicateNameFirstWins(t *testing.T) {
	ts := tools()
	ts = append(ts, Tool{Name: "alpha_lookup", Description: "后来的同名条目", Schema: fullSchema()})
	ts = append(ts, Tool{Name: "alpha_lookup", Description: "再后来的同名条目", Schema: fullSchema()})

	c := NewCatalog(ts)
	if c.Len() != len(tools()) {
		t.Fatalf("重名应被去重，实得目录 %d 项: %v", c.Len(), c.Names())
	}
	got, ok := c.Lookup("alpha_lookup")
	if !ok {
		t.Fatal("alpha_lookup 应在目录内")
	}
	if got.Description != "按标识查条目" {
		t.Fatalf("应保留首次出现处，实得 %q", got.Description)
	}
}

// TestCatalogEmptyNameDropped 空名条目不入目录：既检索不到也调不到，留着只虚增计数。
func TestCatalogEmptyNameDropped(t *testing.T) {
	c := NewCatalog([]Tool{
		{Name: "", Description: "无名条目"},
		{Name: "alpha_lookup", Description: "按标识查条目"},
	})
	if c.Len() != 1 {
		t.Fatalf("空名应被剔除，实得目录 %v", c.Names())
	}
	if c.Has("") {
		t.Fatal("空名不应视为在目录内")
	}
}

// TestCatalogNamesSorted 目录名出口一律升序（map 序不得外泄）。
func TestCatalogNamesSorted(t *testing.T) {
	want := []string{"alpha_lookup", "beta_report", "delta_diff", "gamma_send"}
	for i := 0; i < 20; i++ {
		if got := NewCatalog(tools()).Names(); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次目录名不一致: %v", i, got)
		}
	}
}

// TestCatalogLookupNotFound 不在目录内的名字返回 (零值, false)，不猜。
func TestCatalogLookupNotFound(t *testing.T) {
	got, ok := NewCatalog(tools()).Lookup("epsilon_missing")
	if ok || got.Name != "" {
		t.Fatalf("未知工具应返回零值，实得 %+v ok=%v", got, ok)
	}
}

// TestCatalogTruncatesDescriptionAtConstruction 超长描述在**存进目录时**就被截断。
//
// 钉的是「存储值」而不是渲染结果：检索打分读的就是这份存储值，若只在渲染时截断，
// 打分面对全文、模型面对摘要，两边看到的不是同一件事，且目录仍是唯一无界内存点。
func TestCatalogTruncatesDescriptionAtConstruction(t *testing.T) {
	long := strings.Repeat("描", MaxDescriptionLen*2) + "尾巴"
	c := NewCatalog([]Tool{
		{Name: "alpha_lookup", Description: long, Schema: fullSchema()},
		{Name: "beta_report", Description: "短描述不受影响"},
	})

	got, ok := c.Lookup("alpha_lookup")
	if !ok {
		t.Fatal("alpha_lookup 应在目录内")
	}
	if n := utf8.RuneCountInString(got.Description); n != MaxDescriptionLen {
		t.Fatalf("存储值应恰被截到 %d 字符，实得 %d", MaxDescriptionLen, n)
	}
	if strings.Contains(got.Description, "尾巴") {
		t.Fatalf("截断应砍掉尾部内容，实得 %q", got.Description)
	}
	// 截断不得吞掉边界上的多字节字符（按字节切会产出乱码并直接进模型上下文）。
	if !strings.HasSuffix(got.Description, "描") {
		t.Fatalf("截断点必须落在 rune 边界上，实得 %q", got.Description)
	}
	if short, _ := c.Lookup("beta_report"); short.Description != "短描述不受影响" {
		t.Fatalf("未超长描述应原样保留，实得 %q", short.Description)
	}

	// 截断后检索照常可用：名字命中不受描述长度影响。
	if ms := c.Search("alpha_lookup"); len(ms) != 1 || ms[0].Name != "alpha_lookup" {
		t.Fatalf("截断后检索应照常命中，实得 %v", names(ms))
	}
	// 披露面暴露的也是同一份截断值，两个出口不得分叉。
	ex, ok := c.Expose("alpha_lookup")
	if !ok || !strings.Contains(ex.Description, "描") || strings.Contains(ex.Description, "尾巴") {
		t.Fatalf("披露面应复用同一份截断值，实得 %q", ex.Description)
	}
}

// TestLoadIsIdempotent Load 取回完整 schema 并标记已加载，重复调用不改状态。
func TestLoadIsIdempotent(t *testing.T) {
	c := NewCatalog(tools())
	if c.IsLoaded("alpha_lookup") {
		t.Fatal("初始不应有已加载工具")
	}
	for i := 0; i < 3; i++ {
		tg, ok := c.Load("alpha_lookup")
		if !ok || tg.Name != "alpha_lookup" {
			t.Fatalf("第 %d 次取回应拿到完整定义，实得 %+v ok=%v", i, tg, ok)
		}
	}
	if !c.IsLoaded("alpha_lookup") {
		t.Fatal("取回后应标记已加载")
	}
	if got := c.Loaded(); !reflect.DeepEqual(got, []string{"alpha_lookup"}) {
		t.Fatalf("已加载集应只含该项且升序，实得 %v", got)
	}
}

// TestLoadUnknown 未知工具 Load 返回 (零值, false)，不产生已加载痕迹。
func TestLoadUnknown(t *testing.T) {
	c := NewCatalog(tools())
	if _, ok := c.Load("epsilon_missing"); ok {
		t.Fatal("未知工具不应取回成功")
	}
	if got := c.Loaded(); got != nil {
		t.Fatalf("取回失败不应留下已加载痕迹，实得 %v", got)
	}
}

// TestExposeNotLoadedIsPlaceholder 未加载 → 占位面（退化 schema + 取回指令）。
func TestExposeNotLoadedIsPlaceholder(t *testing.T) {
	c := NewCatalog(tools())
	ex, ok := c.Expose("alpha_lookup")
	if !ok {
		t.Fatal("alpha_lookup 应在目录内")
	}
	if ex.Loaded || !ex.Schema.IsDegenerate() {
		t.Fatalf("未加载应给退化占位面，实得 %+v", ex)
	}
	if !strings.Contains(ex.Description, "取回") {
		t.Fatalf("占位描述应带取回指令，实得 %q", ex.Description)
	}
}

// TestExposeLoadedIsFull 已加载 → 完整面（真 schema，且不再带取回指令）。
func TestExposeLoadedIsFull(t *testing.T) {
	c := NewCatalog(tools())
	c.Load("alpha_lookup")
	ex, ok := c.Expose("alpha_lookup")
	if !ok {
		t.Fatal("alpha_lookup 应在目录内")
	}
	if !ex.Loaded || ex.Schema.IsDegenerate() {
		t.Fatalf("已加载应给完整面，实得 %+v", ex)
	}
	if strings.Contains(ex.Description, "取回") {
		t.Fatalf("完整面不应再带取回指令，实得 %q", ex.Description)
	}
	if len(ex.Schema.Properties) != 1 {
		t.Fatalf("完整面应带真参数定义，实得 %+v", ex.Schema)
	}
}

// TestExposeUnknownHasNoPlaceholder 不在目录内没有占位面：占位只对「已存在未取回」有意义。
func TestExposeUnknownHasNoPlaceholder(t *testing.T) {
	if _, ok := NewCatalog(tools()).Expose("epsilon_missing"); ok {
		t.Fatal("未知工具不应有披露面")
	}
}

// TestExposeAllMixedSorted 全目录披露面：升序 + 按加载状态分流出占位/完整两种形态。
func TestExposeAllMixedSorted(t *testing.T) {
	c := NewCatalog(tools())
	c.Load("beta_report")
	all := c.ExposeAll()
	if len(all) != len(tools()) {
		t.Fatalf("全目录面应有 %d 项，实得 %d", len(tools()), len(all))
	}
	want := []string{"alpha_lookup", "beta_report", "delta_diff", "gamma_send"}
	for i, ex := range all {
		if ex.Name != want[i] {
			t.Fatalf("第 %d 项应为 %s，实得 %s", i, want[i], ex.Name)
		}
	}
	if !all[1].Loaded || all[0].Loaded {
		t.Fatalf("已加载项应给完整面、未加载项给占位，实得 %+v", all)
	}
}

// TestDegenerateSchema 真 schema 不被误判为退化。
func TestDegenerateSchema(t *testing.T) {
	if !Degenerate().IsDegenerate() {
		t.Fatal("Degenerate 应自认退化")
	}
	if fullSchema().IsDegenerate() {
		t.Fatal("真 schema 不应被认成退化")
	}
	if Degenerate().Properties != nil || Degenerate().Required != nil {
		t.Fatal("退化 schema 不应带任何参数定义")
	}
}

// TestPlaceholderCarriesRetrieveInstruction 占位构造：原文描述保留 + 取回指令 + 退化 schema。
func TestPlaceholderCarriesRetrieveInstruction(t *testing.T) {
	ex := Placeholder("alpha_lookup", "按标识查条目")
	if ex.Loaded {
		t.Fatal("占位面不得自称已加载")
	}
	if !ex.Schema.IsDegenerate() {
		t.Fatalf("占位面必须退化，实得 %+v", ex.Schema)
	}
	if !strings.Contains(ex.Description, "按标识查条目") {
		t.Fatalf("占位描述应保留原文，实得 %q", ex.Description)
	}
	if !strings.Contains(ex.Description, "取回") || !strings.Contains(ex.Description, "schema") {
		t.Fatalf("占位描述应带取回指令，实得 %q", ex.Description)
	}
}

// TestLoadConcurrent 无数据竞争，且并发加载后已加载集仍是并集升序。
func TestLoadConcurrent(t *testing.T) {
	ts := tools()
	c := NewCatalog(ts)
	var wg sync.WaitGroup
	for _, tg := range ts {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			c.Load(name)
			c.Expose(name)
			c.Loaded()
		}(tg.Name)
	}
	wg.Wait()
	want := []string{"alpha_lookup", "beta_report", "delta_diff", "gamma_send"}
	if got := c.Loaded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("并发加载后应得并集升序，实得 %v", got)
	}
}

func fullSchema() Schema {
	return Schema{
		Type:       "object",
		Properties: map[string]Param{"id": {Type: "string", Description: "标识"}},
		Required:   []string{"id"},
	}
}
