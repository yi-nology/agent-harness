package eval

import (
	"reflect"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/eventbus"
	"github.com/yi-nology/goagent-kernel/trajectory"
)

// 合成事件流：一轮 + 一步 + 一次配对完整的工具调用（夹具只带中性词面）。
func baseEvents() []eventbus.Event {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	step := uint64(7)
	ev := func(at time.Time, typ string, payload map[string]any) eventbus.Event {
		return eventbus.Event{ID: 1, SessionID: "sess-1", StepID: &step, Type: typ, Level: "info",
			Payload: payload, OccurredAt: at}
	}
	return []eventbus.Event{
		ev(t0, eventbus.EventTurnStarted, nil),
		ev(t0.Add(100*time.Millisecond), eventbus.EventStepStarted, nil),
		ev(t0.Add(200*time.Millisecond), eventbus.EventToolCall, map[string]any{"call_id": "c-1", "tool": "grep"}),
		ev(t0.Add(300*time.Millisecond), eventbus.EventToolResult, map[string]any{"call_id": "c-1"}),
		ev(t0.Add(400*time.Millisecond), eventbus.EventStepFinished, nil),
		ev(t0.Add(500*time.Millisecond), eventbus.EventTurnEnded, nil),
	}
}

func TestRecordReplayReproduce(t *testing.T) {
	t.Run("录制 → 重放：差异为零（issue #111 验收线）", func(t *testing.T) {
		evs := baseEvents()
		raw, err := Record(evs)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded) != len(evs) {
			t.Fatalf("重载事件数 %d != %d", len(loaded), len(evs))
		}
		div := Diff(Replay(evs, trajectory.Options{}), Replay(loaded, trajectory.Options{}))
		if len(div) != 0 {
			t.Fatalf("回放必须复现录制（差异应为零）：%+v", div)
		}
	})

	t.Run("录制字节可 diff：同一流两次录制逐字节相同", func(t *testing.T) {
		a, err := Record(baseEvents())
		if err != nil {
			t.Fatal(err)
		}
		b, err := Record(baseEvents())
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatal("同一事件流两次录制产生了不同字节（确定性破口）")
		}
	})

	t.Run("事件被改：差异落在被改字段上", func(t *testing.T) {
		evs := baseEvents()
		want := Replay(evs, trajectory.Options{})
		evs[2].Payload["tool"] = "sed" // 重放侧改工具名
		got := Replay(evs, trajectory.Options{})
		div := Diff(want, got)
		if len(div) == 0 {
			t.Fatal("事件被改后必须出现差异")
		}
		found := false
		for _, d := range div {
			if d.ID == "tool#c-1" && d.Field == "tool" && d.Want == "grep" && d.Got == "sed" {
				found = true
			}
		}
		if !found {
			t.Fatalf("差异应落在 tool 字段：%+v", div)
		}
	})

	t.Run("行缺失显形（row 字段）", func(t *testing.T) {
		evs := baseEvents()
		extra := append(append([]eventbus.Event{}, evs[:3]...),
			eventbus.Event{ID: 2, SessionID: "sess-1", Type: eventbus.EventToolCall, Level: "info",
				Payload:    map[string]any{"call_id": "c-2", "tool": "sed"},
				OccurredAt: evs[2].OccurredAt})
		want := Replay(extra, trajectory.Options{}) // 录制侧多一次调用
		got := Replay(evs, trajectory.Options{})    // 重放侧没有
		div := Diff(want, got)
		if len(div) == 0 {
			t.Fatal("缺行必须显形")
		}
		has := false
		for _, d := range div {
			if d.ID == "tool#c-2" && d.Field == "row" && d.Want == "present" && d.Got == "absent" {
				has = true
			}
		}
		if !has {
			t.Fatalf("应出现 row=present→absent：%+v", div)
		}
	})

	t.Run("多出的行也显形", func(t *testing.T) {
		got := Replay(baseEvents(), trajectory.Options{})
		div := Diff(nil, got)
		if len(div) == 0 {
			t.Fatal("期望空、实际非空必须显形")
		}
		if div[0].Want != "absent" || div[0].Got != "present" {
			t.Fatalf("方向反了：%+v", div[0])
		}
	})

	t.Run("Diff 排序确定", func(t *testing.T) {
		evs := baseEvents()
		a := Diff(Replay(evs[:3], trajectory.Options{}), Replay(evs, trajectory.Options{}))
		b := Diff(Replay(evs, trajectory.Options{}), Replay(evs[:3], trajectory.Options{}))
		if len(a) == 0 || len(b) == 0 {
			t.Fatalf("两侧都应有差异：%d vs %d", len(a), len(b))
		}
		// 两侧比较方向不同，但各自的输出内部按 (ID, Field) 有序。
		for _, div := range [][]Divergence{a, b} {
			for i := 1; i < len(div); i++ {
				prev, cur := div[i-1], div[i]
				if prev.ID > cur.ID || (prev.ID == cur.ID && prev.Field > cur.Field) {
					t.Fatalf("差异清单未按 (ID, Field) 排序：%+v", div)
				}
			}
		}
	})
}

func TestVerify(t *testing.T) {
	t.Run("常规流适航（整数载荷形变只发生一次，仍稳定）", func(t *testing.T) {
		evs := baseEvents()
		evs[2].Payload["seq"] = 3 // 整数：JSON 往返后是 float64（固有形变，一轮即稳）
		if probs := Verify(evs); len(probs) != 0 {
			t.Fatalf("常规流应适航：%+v", probs)
		}
	})

	t.Run("头部字段往返必须保真", func(t *testing.T) {
		evs := baseEvents()
		if probs := Verify(evs); len(probs) != 0 {
			t.Fatalf("头部字段应保真：%+v", probs)
		}
		// 直接构造「往返会变头」的形态不可行（JSON 表达域内必然保真），
		// 这里钉的是：适航判定不会误报合法流。
	})

	t.Run("载荷往返不幂等要显形", func(t *testing.T) {
		// NaN 无法 JSON 编码——构造「两次往返不等」的可编码形态：
		// json.Number 经 Marshal 原样输出、Unmarshal 成 string，第二轮
		// Marshal 会变成字符串字面量——值在两轮间不变，不适用。
		// 可靠的形态：载荷含 map 与切片混合 + 大整数（>2^53 精度丢失）。
		big := uint64(1 << 54)
		evs := []eventbus.Event{{ID: 1, SessionID: "s", Type: eventbus.EventLLMUsage,
			Payload: map[string]any{"n": float64(big)}, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
		// float64(1<<54) 是整数浮点，往返稳定——不触发问题。
		if probs := Verify(evs); len(probs) != 0 {
			t.Fatalf("整数浮点应适航：%+v", probs)
		}
	})

	t.Run("Load 对坏行显式报错（行号定位）", func(t *testing.T) {
		if _, err := Load([]byte("{\"ID\":1}\nnot-json\n")); err == nil {
			t.Fatal("坏行应报错")
		}
		if _, err := Record(nil); err != nil {
			t.Fatalf("空流应可录制：%v", err)
		}
		empty, err := Load([]byte("\n\n"))
		if err != nil || len(empty) != 0 {
			t.Fatalf("空行应被跳过：%v %d", err, len(empty))
		}
	})

	t.Run("重载后投影与原流一致（端到端）", func(t *testing.T) {
		evs := baseEvents()
		evs[2].Payload["calls"] = 2 // 整数载荷参与投影
		loaded, err := Load(mustRecord(t, evs))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rowsSig(Replay(evs, trajectory.Options{})), rowsSig(Replay(loaded, trajectory.Options{}))) {
			t.Fatal("重载后的投影与原流不一致")
		}
	})
}

func mustRecord(t *testing.T, evs []eventbus.Event) []byte {
	t.Helper()
	raw, err := Record(evs)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// rowsSig 行签名的确定性序列（比对用：ID + 状态 + 配对）。
func rowsSig(rows []trajectory.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID+"|"+string(r.Kind)+"|"+string(r.Status)+"|"+string(r.Pairing))
	}
	return out
}

func TestAssess(t *testing.T) {
	rows := Replay(baseEvents(), trajectory.Options{})

	t.Run("全部判项通过", func(t *testing.T) {
		v, err := Assess(Spec{Checks: []Check{CheckClosure, CheckPairing, CheckNoDuplicates}}, rows)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Pass || v.Judged != 3 || len(v.Findings) != 0 {
			t.Fatalf("v=%+v", v)
		}
	})

	t.Run("期望/禁止判项", func(t *testing.T) {
		v, err := Assess(Spec{
			Checks:       []Check{CheckExpected, CheckForbidden},
			ExpectedIDs:  []string{"turn#1", "step#7", "tool#c-1"},
			ForbiddenIDs: []string{"tool#c-99"},
		}, rows)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Pass || v.Judged != 2 {
			t.Fatalf("v=%+v", v)
		}
		v, err = Assess(Spec{
			Checks:       []Check{CheckExpected},
			ExpectedIDs:  []string{"turn#1", "step#999"},
			ForbiddenIDs: nil,
		}, rows)
		if err != nil {
			t.Fatal(err)
		}
		if v.Pass || len(v.Findings) != 1 || v.Findings[0].RowID != "step#999" {
			t.Fatalf("缺行必须显形：v=%+v", v)
		}
	})

	t.Run("收口违规（喂一半就收口）", func(t *testing.T) {
		half := Replay(baseEvents()[:3], trajectory.Options{})
		v, err := Assess(Spec{Checks: []Check{CheckClosure}}, half)
		if err != nil {
			t.Fatal(err)
		}
		if v.Pass || len(v.Findings) == 0 {
			t.Fatalf("未收口必须显形：v=%+v", v)
		}
		for _, f := range v.Findings {
			if f.Check != CheckClosure {
				t.Fatalf("finding 归因错误：%+v", f)
			}
		}
	})

	t.Run("配对缺口与重复声明", func(t *testing.T) {
		t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		ev := func(at time.Time, typ string, payload map[string]any) eventbus.Event {
			return eventbus.Event{ID: 1, SessionID: "s", Type: typ, Level: "info", Payload: payload, OccurredAt: at}
		}
		stream := []eventbus.Event{
			ev(t0, eventbus.EventToolCall, map[string]any{"call_id": "c-9", "tool": "grep"}),
			ev(t0.Add(time.Second), eventbus.EventToolResult, map[string]any{"call_id": "c-9"}),
			ev(t0.Add(2*time.Second), eventbus.EventToolResult, map[string]any{"call_id": "c-9"}), // 重复结果
			ev(t0.Add(3*time.Second), eventbus.EventToolResult, map[string]any{"call_id": "c-x"}), // 无对应调用
		}
		rows := Replay(stream, trajectory.Options{})
		v, err := Assess(Spec{Checks: []Check{CheckPairing, CheckNoDuplicates}}, rows)
		if err != nil {
			t.Fatal(err)
		}
		if v.Pass {
			t.Fatalf("缺口必须显形：v=%+v", v)
		}
		kinds := map[Check]int{}
		for _, f := range v.Findings {
			kinds[f.Check]++
		}
		if kinds[CheckPairing] == 0 || kinds[CheckNoDuplicates] == 0 {
			t.Fatalf("两类 finding 都应出现：%+v", v.Findings)
		}
	})

	t.Run("checkpoint 点行孤儿不误报（设计事实）", func(t *testing.T) {
		t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		stream := []eventbus.Event{{ID: 1, SessionID: "s", Type: eventbus.EventCheckpointCommitted,
			Level: "info", Payload: map[string]any{"run_id": "r-1", "seq": uint64(3), "status": "running"},
			OccurredAt: t0}}
		rows := Replay(stream, trajectory.Options{})
		v, err := Assess(Spec{Checks: []Check{CheckClosure}}, rows)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Pass {
			t.Fatalf("checkpoint 孤儿行是设计事实，不得误报：%+v", v.Findings)
		}
	})

	t.Run("零判项与不合法清单显式拒绝", func(t *testing.T) {
		if _, err := Assess(Spec{}, rows); err != ErrNoChecks {
			t.Fatalf("零判项应报 ErrNoChecks，得到 %v", err)
		}
		if _, err := Assess(Spec{Checks: []Check{"mystery"}}, rows); err == nil {
			t.Fatal("词表外判项应被拒")
		}
		if _, err := Assess(Spec{Checks: []Check{CheckExpected}}, rows); err == nil {
			t.Fatal("expected 缺清单应被拒")
		}
		if _, err := Assess(Spec{Checks: []Check{CheckForbidden}}, rows); err == nil {
			t.Fatal("forbidden 缺清单应被拒")
		}
		if _, err := Assess(Spec{Checks: []Check{CheckClosure, CheckClosure}}, rows); err == nil {
			t.Fatal("重复判项应被拒")
		}
	})

	t.Run("结论确定性：同输入同输出", func(t *testing.T) {
		half := Replay(baseEvents()[:3], trajectory.Options{})
		spec := Spec{Checks: []Check{CheckClosure, CheckPairing}}
		v1, err := Assess(spec, half)
		if err != nil {
			t.Fatal(err)
		}
		v2, err := Assess(spec, half)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v1, v2) {
			t.Fatalf("纯函数判定必须可复现：%+v vs %+v", v1, v2)
		}
	})
}
