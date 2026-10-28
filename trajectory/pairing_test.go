package trajectory

import (
	"testing"
	"time"

	"github.com/yi-nology/agent-harness/eventbus"
)

// 配对面的用例单独成文件：本文件只关心「调用与结果按 call_id 配对」这一族，
// 缺口必须显形（不静默丢弃），且配对状态变化不得改写实测时刻。

// TestToolPairingByCallID 配对按 call_id 走，不是按到达顺序。
func TestToolPairingByCallID(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		toolEv(eventbus.EventToolCall, 100, 1, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolCall, 150, 1, "c2", "beta_fn"),
		toolEv(eventbus.EventToolResult, 300, 1, "c2", "beta_fn"),
		toolEv(eventbus.EventToolResult, 400, 1, "c1", "alpha_fn"),
	})

	rows := p.Rows()
	if len(rows) != 2 {
		t.Fatalf("两个调用应投影出 2 行，得到 %d 行", len(rows))
	}
	c1 := findTool(t, rows, "c1")
	c2 := findTool(t, rows, "c2")
	if !c1.EndAt.Equal(at(400)) || !c2.EndAt.Equal(at(300)) {
		t.Fatalf("结束时刻应按 call_id 配对: c1=%v c2=%v", c1.EndAt, c2.EndAt)
	}
	if d, ok := c1.Duration(); !ok || d != 300*time.Millisecond {
		t.Fatalf("c1 的实测时长应为 300ms，得到 %v (ok=%v)", d, ok)
	}
	if d, ok := c2.Duration(); !ok || d != 150*time.Millisecond {
		t.Fatalf("c2 的实测时长应为 150ms，得到 %v (ok=%v)", d, ok)
	}
}

// TestPairingMissingResultIsExplicit 只有调用没有结果：显式状态 + 显式缺口。
func TestPairingMissingResultIsExplicit(t *testing.T) {
	p := NewProjector(Options{})
	p.Apply(toolEv(eventbus.EventToolCall, 100, 1, "c1", "alpha_fn"))

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("应投影出 1 行，得到 %d 行", len(rows))
	}
	row := rows[0]
	if row.Pairing != PairingMissingResult {
		t.Fatalf("没有结果的调用行应是 missing_result，得到 %q", row.Pairing)
	}
	if _, ok := row.Duration(); ok {
		t.Fatal("没有结果就没有实测时长，不该报得出")
	}

	// 收口之后才确认为「等不到」：缺口进清单，未完成行原样报出。
	open := p.Finalize()
	if len(open) != 1 || open[0].CallID != "c1" {
		t.Fatalf("收口应报出 1 条未完成行，得到 %+v", open)
	}
	if !hasIssue(p.Issues(), PairingMissingResult, "c1") {
		t.Fatalf("收口后应有 missing_result 缺口: %+v", p.Issues())
	}
}

// TestDuplicateResultIsExplicit 结果重复：显式状态，且不重写实测时长。
func TestDuplicateResultIsExplicit(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		toolEv(eventbus.EventToolCall, 100, 1, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolResult, 300, 1, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolResult, 500, 1, "c1", "alpha_fn"),
	})

	row := findTool(t, p.Rows(), "c1")
	if row.DupResults != 1 {
		t.Fatalf("重复结果计数应为 1，得到 %d", row.DupResults)
	}
	if row.Pairing != PairingDuplicateResult {
		t.Fatalf("重复结果的行应是 duplicate_result，得到 %q", row.Pairing)
	}
	if !row.EndAt.Equal(at(300)) {
		t.Fatalf("重复结果不该重写结束时刻（第一次观测是真相），得到 %v", row.EndAt)
	}
	if d, ok := row.Duration(); !ok || d != 200*time.Millisecond {
		t.Fatalf("实测时长应仍取第一次配对的结果（200ms），得到 %v (ok=%v)", d, ok)
	}
	if !hasIssue(p.Issues(), PairingDuplicateResult, "c1") {
		t.Fatalf("应有 duplicate_result 缺口: %+v", p.Issues())
	}
}

// TestUnpairedResultIsExplicit 结果没有对应调用：不静默丢弃。
func TestUnpairedResultIsExplicit(t *testing.T) {
	p := NewProjector(Options{})
	p.Apply(toolEv(eventbus.EventToolResult, 300, 1, "c9", "gamma_fn"))

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("无对应的结果应落成 1 行（不得丢弃），得到 %d 行", len(rows))
	}
	row := rows[0]
	if row.Kind != KindToolResult {
		t.Fatalf("无对应的结果行类目应是 tool_result，得到 %q", row.Kind)
	}
	if row.Pairing != PairingUnpairedResult {
		t.Fatalf("无对应的结果行应是 unpaired_result，得到 %q", row.Pairing)
	}
	if row.Status != StatusOrphan {
		t.Fatalf("无对应的结果行起点缺失，应是 orphan，得到 %q", row.Status)
	}
	if _, ok := row.Duration(); ok {
		t.Fatal("无对应的结果没有实测起点，不该报得出时长")
	}
	if !hasIssue(p.Issues(), PairingUnpairedResult, "c9") {
		t.Fatalf("应有 unpaired_result 缺口: %+v", p.Issues())
	}
}

// TestDuplicateCallKeepsFirstObservation 同号重复声明：第一次观测是真相。
func TestDuplicateCallKeepsFirstObservation(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		toolEv(eventbus.EventToolCall, 100, 1, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolCall, 150, 1, "c1", "alpha_fn"),
	})

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("同一 call_id 只应有一行，得到 %d 行", len(rows))
	}
	if !rows[0].StartAt.Equal(at(100)) {
		t.Fatalf("起点应取第一次观测（at(100)），得到 %v", rows[0].StartAt)
	}
	if !hasIssue(p.Issues(), PairingDuplicateCall, "c1") {
		t.Fatalf("应有 duplicate_call 缺口: %+v", p.Issues())
	}
}

// TestDuplicateUnpairedResultsKeepUnpairedState 两个结果都没有对应调用：
// 「没配对」这层事实不能被「结果重复」盖掉（两件事都进缺口清单）。
func TestDuplicateUnpairedResultsKeepUnpairedState(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		toolEv(eventbus.EventToolResult, 300, 1, "c9", "gamma_fn"),
		toolEv(eventbus.EventToolResult, 400, 1, "c9", "gamma_fn"),
	})

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("同一 call_id 的孤儿结果应落成 1 行，得到 %d 行", len(rows))
	}
	if rows[0].Pairing != PairingUnpairedResult {
		t.Fatalf("孤儿结果行的配对状态应仍是 unpaired_result，得到 %q", rows[0].Pairing)
	}
	if rows[0].DupResults != 1 {
		t.Fatalf("重复结果计数应为 1，得到 %d", rows[0].DupResults)
	}
	if !hasIssue(p.Issues(), PairingUnpairedResult, "c9") {
		t.Fatalf("应有 unpaired_result 缺口: %+v", p.Issues())
	}
	if !hasIssue(p.Issues(), PairingDuplicateResult, "c9") {
		t.Fatalf("应有 duplicate_result 缺口: %+v", p.Issues())
	}
}

// TestResultBeforeCallPairsInPlace 结果先到、调用后到：就地配对，不另起一行。
func TestResultBeforeCallPairsInPlace(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		toolEv(eventbus.EventToolResult, 300, 1, "c1", "alpha_fn"),
		toolEv(eventbus.EventToolCall, 400, 1, "c1", "alpha_fn"),
	})

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("结果先到再补调用应仍是一行，得到 %d 行", len(rows))
	}
	row := rows[0]
	if row.Kind != KindToolCall {
		t.Fatalf("补上调用后行类目应是 tool_call，得到 %q", row.Kind)
	}
	if row.Pairing != PairingPaired {
		t.Fatalf("补上调用后应是 paired，得到 %q", row.Pairing)
	}
	if !row.StartAt.Equal(at(400)) || !row.EndAt.Equal(at(300)) {
		t.Fatalf("起止时刻应取自各自事件: %v → %v", row.StartAt, row.EndAt)
	}
	// 事件顺序反了 ⇒ 两时刻倒挂 ⇒ 没有可信的实测时长（不拿绝对值糊过去）。
	if _, ok := row.Duration(); ok {
		t.Fatal("起止倒挂时不该报出实测时长")
	}
}
