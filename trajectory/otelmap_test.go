package trajectory

// 映射契约的判据单独成文件：本文件只关心「属性有没有主」与「父子连不连得上」这两族。
// 夹具一律合成数据（alpha_fn / beta_fn / sess-a），不带任何领域词。

import (
	"reflect"
	"testing"

	"github.com/yi-nology/agent-harness/eventbus"
)

// specSamples 覆盖全部属性的样本行（每个属性至少在一行里被写出）。
func specSamples() []Row {
	return []Row{
		{Seq: 1, Kind: KindTurn, ID: "turn#1", Turn: 1, StartAt: at(0), EndAt: at(600),
			Status: StatusClosed, Summary: "turn 1"},
		{Seq: 2, Kind: KindStep, ID: "step#1", Turn: 1, HasStep: true, StepID: 1,
			StartAt: at(10), EndAt: at(500), Status: StatusClosed, Summary: "step 1"},
		{Seq: 3, Kind: KindStep, ID: "step#-3", Turn: 1, StartAt: at(20),
			Status: StatusOpen, Summary: "step step#-3"},
		{Seq: 4, Kind: KindToolCall, ID: "tool#c1", Turn: 1, HasStep: true, StepID: 1,
			CallID: "c1", Tool: "alpha_fn", Pairing: PairingDuplicateResult, DupResults: 2,
			StartAt: at(30), EndAt: at(400), Status: StatusClosed, Summary: "tool_call:alpha_fn"},
		{Seq: 5, Kind: KindToolResult, ID: "tool#c2", Turn: 1, HasStep: true, StepID: 1,
			CallID: "c2", Tool: "beta_fn", Pairing: PairingUnpairedResult,
			EndAt: at(450), Status: StatusOrphan, Summary: "tool_result:beta_fn"},
	}
}

// TestAttrSpecsCoverEveryAttribute 每个 Attr* 常量都必须有一条契约，且表里不许有多余的。
func TestAttrSpecsCoverEveryAttribute(t *testing.T) {
	// 这张清单是判据的判据：新增 Attr* 常量必须同步这里与契约表，
	// 否则「每个属性都有主」这件事就没人判了。
	all := []string{
		AttrRowID, AttrKind, AttrRowStatus, AttrTurnSeq, AttrStepID,
		AttrToolName, AttrToolCallID, AttrToolPairing, AttrToolDupResults,
		AttrDurationMs, AttrSummary,
	}
	if len(AttrSpecs) != len(all) {
		t.Fatalf("契约表 %d 条、常量 %d 个：多与少都算没对上", len(AttrSpecs), len(all))
	}
	for _, name := range all {
		if _, ok := AttrSpecOf(name); !ok {
			t.Errorf("%s 没有映射契约（来源字段/取值域/基数上界都缺）", name)
		}
	}
}

// goTypeNames 取值形态的受限集合（与 Span.Attributes 里实际能出现的 Go 类型一致）。
var goTypeNames = map[string]bool{"string": true, "int64": true, "float64": true, "bool": true}

// TestAttrSpecsAreOwned 每条契约的四件事都得写出：来源、取值域、基数上界（含依据）、名字唯一。
func TestAttrSpecsAreOwned(t *testing.T) {
	names := map[string]bool{}
	for _, s := range AttrSpecs {
		if s.Name == "" {
			t.Fatal("契约表里有空属性名")
		}
		if names[s.Name] {
			t.Errorf("属性名重复：%s", s.Name)
		}
		names[s.Name] = true
		if s.Source == "" {
			t.Errorf("%s：没写来源字段", s.Name)
		}
		if !goTypeNames[s.Domain.Kind] {
			t.Errorf("%s：取值形态 %q 不在受限集合里（宿主导出按类型选属性类型）", s.Name, s.Domain.Kind)
		}
		if s.Card.Why == "" {
			t.Errorf("%s：基数上界没写依据", s.Name)
		}
		if len(s.Domain.Values) > 0 {
			uniq := map[string]bool{}
			for _, v := range s.Domain.Values {
				if v == "" {
					t.Errorf("%s：枚举取值里有空串", s.Name)
				}
				if uniq[v] {
					t.Errorf("%s：枚举取值重复 %q", s.Name, v)
				}
				uniq[v] = true
			}
			if s.Card.Max < len(s.Domain.Values) {
				t.Errorf("%s：基数上界 %d 小于枚举取值数 %d（声明自相矛盾）",
					s.Name, s.Card.Max, len(s.Domain.Values))
			}
			continue
		}
		if s.Domain.Note == "" {
			t.Errorf("%s：非枚举属性必须写出取值域说明", s.Name)
		}
	}
}

// TestMappingSurfaceMatchesSpecTable 双向：映射函数写出的每个属性都在表里；表里每条都能被映射出来。
func TestMappingSurfaceMatchesSpecTable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range specSamples() {
		for k := range SpanOf(r).Attributes {
			seen[k] = true
			if _, ok := AttrSpecOf(k); !ok {
				t.Errorf("映射写出了表里没有的属性 %q（契约表必须跟上）", k)
			}
		}
	}
	for _, s := range AttrSpecs {
		if !seen[s.Name] {
			t.Errorf("表里列了 %q，但映射函数从不产出它（表不是许愿池）", s.Name)
		}
	}
}

// TestAttributeGoTypesMatchSpec 契约写的形态必须与映射函数实际放进 map 的 Go 类型逐条相同。
//
// 这条是表与代码之间唯一不会漂的对拍：类型不符的属性在多数后端里**静默丢弃**，
// 表现为「上行成功、看板上就是没有」——靠读文档发现不了。
func TestAttributeGoTypesMatchSpec(t *testing.T) {
	for _, r := range specSamples() {
		for k, v := range SpanOf(r).Attributes {
			s, ok := AttrSpecOf(k)
			if !ok {
				continue
			}
			if got := reflect.TypeOf(v).String(); got != s.Domain.Kind {
				t.Errorf("%s 实际类型 %s，契约写的是 %s", k, got, s.Domain.Kind)
			}
		}
	}
}

// TestLabelSafeRequiresBoundedEnum 只有「枚举 + 本仓给得出上界」的属性才许进 metric label。
func TestLabelSafeRequiresBoundedEnum(t *testing.T) {
	safe := 0
	for _, s := range AttrSpecs {
		if !s.LabelSafe {
			continue
		}
		safe++
		if s.Card.Max <= 0 {
			t.Errorf("%s 标了 label-safe 却没有上界：无界 label 会让时序数按取值数膨胀", s.Name)
		}
		if len(s.Domain.Values) == 0 {
			t.Errorf("%s 标了 label-safe 但取值域不是枚举：上界无从核对", s.Name)
		}
	}
	// 全 false 也能让上面的循环全过——那样这条判据等于没生效。
	if safe == 0 {
		t.Fatal("表里没有任何 label-safe 属性：这条判据退化成空循环")
	}
}

// TestForbiddenLabelsAreDisjoint 禁区清单与契约表不得矛盾，且每条都要写理由。
func TestForbiddenLabelsAreDisjoint(t *testing.T) {
	if len(ForbiddenLabels) == 0 {
		t.Fatal("禁区清单为空：这张表的用处就是列出无界看似维度的那几个字段")
	}
	for _, f := range ForbiddenLabels {
		if f.Name == "" || f.Why == "" {
			t.Errorf("禁区项 %q 没写理由", f.Name)
		}
		if s, ok := AttrSpecOf(f.Name); ok {
			t.Errorf("%q 既在禁区清单里又在契约表里（LabelSafe=%v）", f.Name, s.LabelSafe)
		}
	}
}

// TestSpanParentLinks 父子链：根恰好是轮次行，环节挂轮、工具挂环节。
func TestSpanParentLinks(t *testing.T) {
	if TraceBoundary != TracePerTurn {
		t.Fatalf("trace 边界必须是一轮，实得 %v", TraceBoundary)
	}
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		ev(eventbus.EventTurnStarted, 0, nil),
		stepEv(eventbus.EventStepStarted, 10, 7),
		toolEv(eventbus.EventToolCall, 20, 7, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolResult, 30, 7, "c1", "alpha_fn"),
		stepEv(eventbus.EventStepFinished, 40, 7),
		ev(eventbus.EventTurnEnded, 50, nil),
		ev(eventbus.EventTurnStarted, 100, nil),
		stepEv(eventbus.EventStepStarted, 110, 7),
		ev(eventbus.EventTurnEnded, 160, nil),
	})

	spans := SpansOf(p.Rows())
	if len(spans) != 5 {
		t.Fatalf("应投影出 5 行（两轮各一 turn，第一轮 step+tool，第二轮 step），得到 %d", len(spans))
	}
	roots := 0
	for i, s := range spans {
		if s.ParentID != "" {
			continue
		}
		roots++
		if s.Name != string(KindTurn) {
			t.Errorf("第 %d 个 span 是根但不是轮次行（name=%s）：根必须是轮次行", i, s.Name)
		}
	}
	if roots != 2 {
		t.Fatalf("两轮应有两个根 span，实得 %d", roots)
	}
	if got := spans[1].ParentID; got != "turn#1" {
		t.Errorf("环节行应挂在所属轮次下，实得 %q", got)
	}
	if got := spans[2].ParentID; got != "step#7" {
		t.Errorf("工具行应挂在环节下，实得 %q", got)
	}
	if got := spans[4].ParentID; got != "turn#2" {
		t.Errorf("第二轮的环节行挂在第二轮下，实得 %q", got)
	}
}

// TestSpansOfClearsDanglingParent 父不在本批行里时清成根；而「声明属于哪个环节」这层事实不丢。
func TestSpansOfClearsDanglingParent(t *testing.T) {
	rows := []Row{{
		Seq: 1, Kind: KindToolCall, ID: "tool#c9", Turn: 1, HasStep: true, StepID: 9,
		CallID: "c9", Tool: "alpha_fn", Pairing: PairingPaired,
		StartAt: at(10), EndAt: at(20), Status: StatusClosed,
	}}
	// 单行映射只**声明**父，不做存在性判定（它看不到全局）。
	if got := SpanOf(rows[0]).ParentID; got != "step#9" {
		t.Fatalf("单行映射应声明父 step#9，实得 %q", got)
	}
	// 批量映射做解析：父不在本批内 ⇒ 清成根，否则导出的是断链 span。
	if got := SpansOf(rows)[0].ParentID; got != "" {
		t.Fatalf("悬空父必须清成根（断链 span 在后端里表现为 trace 少一段且不报错），实得 %q", got)
	}
	if got, has := SpansOf(rows)[0].Attributes[AttrStepID]; !has || got != int64(9) {
		t.Fatalf("声明属于哪个环节的事实不能丢：step.id = %v (has=%v)", got, has)
	}
	// 对照：把父行一起导出 ⇒ 父子关系保留。
	full := append([]Row{{
		Seq: 0, Kind: KindStep, ID: "step#9", Turn: 1, HasStep: true, StepID: 9,
		StartAt: at(0), Status: StatusOpen,
	}}, rows...)
	if got := SpansOf(full)[1].ParentID; got != "step#9" {
		t.Fatalf("父在本批内时必须保留父子关系，实得 %q", got)
	}
}

// TestRowIDsAreUniqueWithinTurnOnly 这是 TraceBoundary 选「轮」的**前提**钉子。
//
// 环节号不经轮次限定，故同一个行标识会在两轮里各出现一次。若哪天标识改成全局唯一，
// 这条会红——那时该回来重读 otel.go 头注的边界依据，而不是顺手改断言。
func TestRowIDsAreUniqueWithinTurnOnly(t *testing.T) {
	p := NewProjector(Options{})
	for turn := 0; turn < 2; turn++ {
		base := turn * 100
		p.ApplyAll([]eventbus.Event{
			ev(eventbus.EventTurnStarted, base, nil),
			stepEv(eventbus.EventStepStarted, base+10, 7),
			stepEv(eventbus.EventStepFinished, base+20, 7),
			ev(eventbus.EventTurnEnded, base+30, nil),
		})
	}
	var ids []string
	for _, r := range p.Rows() {
		if r.Kind == KindStep {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("前提变了：两轮里同一个环节号应产出相同行标识，实得 %v", ids)
	}
	var parents []string
	for _, s := range SpansOf(p.Rows()) {
		if s.Name == string(KindStep) {
			parents = append(parents, s.ParentID)
		}
	}
	if len(parents) != 2 || parents[0] == parents[1] {
		t.Fatalf("同标识的两个环节行必须挂在不同的父下（这正是 trace 按轮切分的理由），实得 %v", parents)
	}
}
