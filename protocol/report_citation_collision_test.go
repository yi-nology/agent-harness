// report_citation_collision_test.go —— 证据编号唯一性的反向验证。
//
// # 为什么这条要单独钉
//
// 证据编号是「引用证据说理」这条结构化通道的**指向**。编号一旦重复，
// verdict 引用 "E1" 就不知道说的是哪一条——而 CheckEvidenceRefs 只查
// 「引用了但不存在」，同号时该编号确实存在，故它**一声不吭**。
// 歧于是静默的，比缺失更难查。
//
// 旧实现用「数组下标 + 1」当自动挂号的编号，于是撞号是必然的：
// 「evidence[0] 无编号（自动编成 owner#E1）」+「evidence[1] 模型自报 owner#E1」，
// 两条证据同号。模型自报重复编号的情形同样原样放过。

package protocol

import (
	"strings"
	"testing"
)

// TestAssignEvidenceIDsAvoidsTakenNumbers 自动挂号必须避开已被占用的编号。
func TestAssignEvidenceIDsAvoidsTakenNumbers(t *testing.T) {
	r := &Report{
		AgentName: "probe",
		Evidence: []Evidence{
			{Tool: "a", Snippet: "s1"},                 // 无编号 → 自动挂号
			{Tool: "b", Snippet: "s2", ID: "probe#E1"}, // 模型自报
			{Tool: "c", Snippet: "s3", ID: "probe#E3"}, // 模型自报
			{Tool: "d", Snippet: "s4"},                 // 无编号 → 自动挂号
		},
	}
	AssignEvidenceIDs(r)

	seen := map[string]int{}
	for i, ev := range r.Evidence {
		if ev.ID == "" {
			t.Fatalf("evidence[%d] 未拿到编号", i)
		}
		if first, ok := seen[ev.ID]; ok {
			t.Fatalf("evidence[%d] 与 evidence[%d] 撞号：%q", i, first, ev.ID)
		}
		seen[ev.ID] = i
	}
	// 模型自报的编号必须原样保留（不覆盖）——verdict 可能正引用着它们。
	if r.Evidence[1].ID != "probe#E1" || r.Evidence[2].ID != "probe#E3" {
		t.Fatalf("模型自报编号不得被覆盖：%q / %q", r.Evidence[1].ID, r.Evidence[2].ID)
	}
}

// TestAssignEvidenceIDsSequentialWithoutModelIDs 无模型编号时仍按序挂号。
//
// 回归：自动挂号改成「避开已占用」后，纯自动路径的编号必须仍是 E1、E2、E3……
func TestAssignEvidenceIDsSequentialWithoutModelIDs(t *testing.T) {
	r := &Report{
		AgentName: "probe",
		Evidence: []Evidence{
			{Tool: "a", Snippet: "s1"},
			{Tool: "b", Snippet: "s2"},
			{Tool: "c", Snippet: "s3"},
		},
	}
	AssignEvidenceIDs(r)
	for i, want := range []string{"probe#E1", "probe#E2", "probe#E3"} {
		if r.Evidence[i].ID != want {
			t.Fatalf("evidence[%d] 编号 = %q，期望 %q", i, r.Evidence[i].ID, want)
		}
	}
}

// TestAssignEvidenceIDsDefaultOwner 空 AgentName 退化为 report#E<n>。
func TestAssignEvidenceIDsDefaultOwner(t *testing.T) {
	r := &Report{Evidence: []Evidence{{Tool: "a", Snippet: "s1"}}}
	AssignEvidenceIDs(r)
	if r.Evidence[0].ID != "report#E1" {
		t.Fatalf("空 AgentName 应退化为 report#E1，实得 %q", r.Evidence[0].ID)
	}
}

// TestValidateWarnsOnDuplicateEvidenceID 模型自报的重复编号必须被警告。
//
// 只警告不改写：改写会让指向它的 verdict 引用失效。
func TestValidateWarnsOnDuplicateEvidenceID(t *testing.T) {
	r := &Report{
		Confidence: "high",
		AgentName:  "probe",
		Evidence: []Evidence{
			{Tool: "a", Snippet: "s1", ID: "E1"},
			{Tool: "b", Snippet: "s2", ID: "E1"},
		},
	}
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("重复编号只该警告不该拦截: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "重复") {
			found = true
		}
	}
	if !found {
		t.Fatalf("重复的 evidence 编号必须被警告，实得 %v", warns)
	}
}

// TestValidateNoWarnOnUniqueEvidenceID 编号唯一时不得误警告。
//
// 误报是门禁/校验最致命的失败形态：维护者学会忽略它，规则随即名存实亡。
func TestValidateNoWarnOnUniqueEvidenceID(t *testing.T) {
	r := &Report{
		Confidence: "high",
		AgentName:  "probe",
		Evidence: []Evidence{
			{Tool: "a", Snippet: "s1", ID: "E1"},
			{Tool: "b", Snippet: "s2", ID: "E2"},
		},
	}
	warns, err := r.Validate()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warns {
		if strings.Contains(w, "重复") {
			t.Fatalf("编号唯一时不得警告，实得 %v", warns)
		}
	}
}
