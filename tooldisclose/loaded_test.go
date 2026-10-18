package tooldisclose

// 已加载集合并的确定性钉住：并集 + 升序，与汇流顺序无关。

import (
	"reflect"
	"testing"
)

// TestMergeLoadedUnionSorted 并集 + 去重 + 升序。
func TestMergeLoadedUnionSorted(t *testing.T) {
	got := MergeLoaded(
		[]string{"gamma_send", "alpha_lookup"},
		[]string{"beta_report"},
	)
	want := []string{"alpha_lookup", "beta_report", "gamma_send"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("应得并集升序，实得 %v", got)
	}
}

// TestMergeLoadedOrderIndependent 汇流顺序不影响结果：并发时序不得留在结果里。
func TestMergeLoadedOrderIndependent(t *testing.T) {
	a := []string{"gamma_send", "alpha_lookup", "beta_report"}
	b := []string{"beta_report", "delta_diff"}
	want := MergeLoaded(a, b)
	perms := [][][]string{
		{a, b},
		{b, a},
		{b, append([]string(nil), a...)},
		{append([]string(nil), b...), append([]string(nil), a...)},
	}
	for i, sets := range perms {
		if got := MergeLoaded(sets...); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 种汇流顺序结果不一致: %v vs %v", i, got, want)
		}
	}
}

// TestMergeLoadedDedupes 同一份集合重复汇入不产生重复项。
func TestMergeLoadedDedupes(t *testing.T) {
	got := MergeLoaded(
		[]string{"alpha_lookup", "alpha_lookup"},
		[]string{"alpha_lookup", "beta_report"},
	)
	if !reflect.DeepEqual(got, []string{"alpha_lookup", "beta_report"}) {
		t.Fatalf("并集不应有重复项，实得 %v", got)
	}
}

// TestMergeLoadedEmptyInputs 无输入、全空、只含空名 → 返回空，不产出噪声项。
func TestMergeLoadedEmptyInputs(t *testing.T) {
	if got := MergeLoaded(); len(got) != 0 {
		t.Fatalf("无输入应返回空，实得 %v", got)
	}
	if got := MergeLoaded(nil, []string(nil), []string{}); len(got) != 0 {
		t.Fatalf("全空输入应返回空，实得 %v", got)
	}
	if got := MergeLoaded([]string{"", ""}); len(got) != 0 {
		t.Fatalf("空名不该进并集，实得 %v", got)
	}
	if got := MergeLoaded([]string{"", "alpha_lookup"}); !reflect.DeepEqual(got, []string{"alpha_lookup"}) {
		t.Fatalf("空名应被丢弃、其余保留，实得 %v", got)
	}
}

// TestMergeLoadedDoesNotAliasInput 合并结果不得与入参共享底层数组（调用方改了会串到已加载集）。
func TestMergeLoadedDoesNotAliasInput(t *testing.T) {
	in := []string{"alpha_lookup", "beta_report"}
	got := MergeLoaded(in)
	got[0] = "mutated"
	if in[0] != "alpha_lookup" {
		t.Fatalf("并集与入参共享了底层数组: %v", in)
	}
}

// TestMergeLoadedMatchesCatalogLoaded 目录自报已加载集与并集结果一致（两条出口同源）。
func TestMergeLoadedMatchesCatalogLoaded(t *testing.T) {
	c := NewCatalog(tools())
	c.Load("beta_report")
	c.Load("alpha_lookup")
	if got := MergeLoaded(c.Loaded(), nil); !reflect.DeepEqual(got, c.Loaded()) {
		t.Fatalf("目录自报集合应与并集一致: %v vs %v", got, c.Loaded())
	}
}
