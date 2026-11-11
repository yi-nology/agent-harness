package trajectory_test

import (
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/trajectory"
)

var t0 = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func turnRow() trajectory.Row {
	return trajectory.Row{
		Seq: 1, Kind: trajectory.KindTurn, ID: "turn#1", Turn: 1,
		StartAt: t0, EndAt: t0.Add(2 * time.Second),
		Status: trajectory.StatusClosed, Summary: "turn 1",
	}
}

func TestSpanOfTurn(t *testing.T) {
	s := trajectory.SpanOf(turnRow())
	if s.Name != "turn" {
		t.Fatalf("span 名应为低基数的 kind，实际 %q", s.Name)
	}
	if s.Status != trajectory.SpanStatusOK {
		t.Fatalf("已收口且无问题应为 ok，实际 %v", s.Status)
	}
	if got, want := s.Attributes[trajectory.AttrRowID], "turn#1"; got != want {
		t.Fatalf("row_id = %v, want %v", got, want)
	}
	if got := s.Attributes[trajectory.AttrTurnSeq]; got != int64(1) {
		t.Fatalf("turn.seq = %v", got)
	}
	ms, ok := s.Attributes[trajectory.AttrDurationMs].(float64)
	if !ok || ms != 2000 {
		t.Fatalf("duration_ms = %v（应为实测 2000）", s.Attributes[trajectory.AttrDurationMs])
	}
	if got, want := s.Attributes[trajectory.AttrSummary], "turn 1"; got != want {
		t.Fatalf("summary = %v", got)
	}
}

// 没有实测就没有**这个属性**：不是写 0，也不是估一个。
func TestNoDurationAttributeWithoutMeasurement(t *testing.T) {
	r := trajectory.Row{Kind: trajectory.KindStep, ID: "step#1", StartAt: t0, Status: trajectory.StatusOpen}
	s := trajectory.SpanOf(r)
	if _, has := s.Attributes[trajectory.AttrDurationMs]; has {
		t.Fatal("未收口的行不得写出 duration_ms（估算值不许冒充实测）")
	}
	if s.Status != trajectory.SpanStatusUnset {
		t.Fatalf("未收口应为 unset，实际 %v", s.Status)
	}

	// 事件倒挂（结果早于调用）：负时长不是实测值。
	inv := trajectory.Row{Kind: trajectory.KindStep, StartAt: t0, EndAt: t0.Add(-time.Second)}
	if _, has := trajectory.SpanOf(inv).Attributes[trajectory.AttrDurationMs]; has {
		t.Fatal("负时长不得记成实测")
	}
}

func TestOrphanRowIsUnset(t *testing.T) {
	r := trajectory.Row{Kind: trajectory.KindStep, EndAt: t0, Status: trajectory.StatusOrphan}
	s := trajectory.SpanOf(r)
	if s.Status != trajectory.SpanStatusUnset {
		t.Fatalf("起点缺失不是 error（那是数据缺口，不是事故），实际 %v", s.Status)
	}
}

func TestToolRowAttributes(t *testing.T) {
	r := trajectory.Row{
		Kind: trajectory.KindToolCall, ID: "tool#c1", Turn: 1, HasStep: true, StepID: 0,
		CallID: "c1", Tool: "read_file", Pairing: trajectory.PairingPaired,
		StartAt: t0, EndAt: t0.Add(1500 * time.Millisecond), Status: trajectory.StatusClosed,
	}
	s := trajectory.SpanOf(r)
	if s.Status != trajectory.SpanStatusOK {
		t.Fatalf("已配对应为 ok，实际 %v", s.Status)
	}
	if got, want := s.Attributes[trajectory.AttrToolName], "read_file"; got != want {
		t.Fatalf("tool.name = %v", got)
	}
	if got, want := s.Attributes[trajectory.AttrToolCallID], "c1"; got != want {
		t.Fatalf("tool.call_id = %v", got)
	}
	if got, want := s.Attributes[trajectory.AttrToolPairing], string(trajectory.PairingPaired); got != want {
		t.Fatalf("tool.pairing = %v", got)
	}
	// 环节号 0 是**合法值**：HasStep 为真就必须写出 0，不能因为零值当没写。
	if got, has := s.Attributes[trajectory.AttrStepID]; !has || got != int64(0) {
		t.Fatalf("step.id = %v (has=%v)，环节号 0 必须照样写出", got, has)
	}
	// 工具名进属性、不进 span 名（低基数纪律）。
	if s.Name == "read_file" {
		t.Fatal("工具名不得拼进 span 名")
	}
}

func TestToolPairingStatus(t *testing.T) {
	cases := []struct {
		pairing trajectory.Pairing
		want    trajectory.SpanStatus
	}{
		{trajectory.PairingDuplicateResult, trajectory.SpanStatusError},
		{trajectory.PairingDuplicateCall, trajectory.SpanStatusError},
		{trajectory.PairingUnpairedResult, trajectory.SpanStatusError},
		{trajectory.PairingMissingResult, trajectory.SpanStatusUnset},
	}
	for _, c := range cases {
		r := trajectory.Row{
			Kind: trajectory.KindToolCall, CallID: "c", Pairing: c.pairing,
			StartAt: t0, EndAt: t0.Add(time.Second), Status: trajectory.StatusClosed,
		}
		if got := trajectory.SpanOf(r).Status; got != c.want {
			t.Fatalf("pairing %s ⇒ %v, want %v", c.pairing, got, c.want)
		}
	}
}

// 非工具行不得携带工具属性：Row 上那几个字段只在工具行有效，
// 而「结构上字段存在」不等于「语义上适用」。
func TestNonToolRowCarriesNoToolAttributes(t *testing.T) {
	r := turnRow()
	r.Tool = "read_file"
	r.CallID = "c1"
	r.Pairing = trajectory.PairingPaired
	s := trajectory.SpanOf(r)
	for _, k := range []string{trajectory.AttrToolName, trajectory.AttrToolCallID, trajectory.AttrToolPairing} {
		if _, has := s.Attributes[k]; has {
			t.Fatalf("轮次行不得写出 %s", k)
		}
	}
}

func TestZeroValuedRowMinimalAttributes(t *testing.T) {
	s := trajectory.SpanOf(trajectory.Row{})
	for _, k := range []string{
		trajectory.AttrRowID, trajectory.AttrTurnSeq, trajectory.AttrStepID,
		trajectory.AttrSummary, trajectory.AttrDurationMs,
	} {
		if _, has := s.Attributes[k]; has {
			t.Fatalf("零值行不得写出 %s", k)
		}
	}
	// kind 与 status 恒在：缺了它们这条 span 无从归类。
	if _, has := s.Attributes[trajectory.AttrKind]; !has {
		t.Fatal("kind 必写")
	}
	if _, has := s.Attributes[trajectory.AttrRowStatus]; !has {
		t.Fatal("行状态必写")
	}
}

func TestSpansOfKeepsOrderAndLength(t *testing.T) {
	rows := []trajectory.Row{turnRow(), {Kind: trajectory.KindStep, Seq: 2}, turnRow()}
	spans := trajectory.SpansOf(rows)
	if len(spans) != 3 {
		t.Fatalf("len = %d", len(spans))
	}
	if spans[2].Attributes[trajectory.AttrRowID] != "turn#1" {
		t.Fatal("顺序必须与行序一致")
	}
	if len(trajectory.SpansOf(nil)) != 0 {
		t.Fatal("nil 输入应为空")
	}
}
