package trajectory

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/agent-harness/eventbus"
)

// TestMeasuredDurationNeverHoldsEstimate 实测与估算必须单源。
//
// 载荷里刻意带了「看起来可以拿来估时长」的量（字符数/字节数/token 数），
// 且事件流里**没有任何结束事件**——此时实测值就是不存在。
// 若实现拿载荷体积折一个时长填进来，这条会红。
func TestMeasuredDurationNeverHoldsEstimate(t *testing.T) {
	p := NewProjector(Options{})
	p.Apply(ev(eventbus.EventToolCall, 0, map[string]any{
		"call_id": "c1",
		"tool":    "alpha_fn",
		"chars":   4096,
		"bytes":   8192,
		"tokens":  1024,
	}))

	rows := p.Rows()
	if len(rows) != 1 {
		t.Fatalf("应投影出 1 行，得到 %d 行", len(rows))
	}
	d, ok := rows[0].Duration()
	if ok {
		t.Fatalf("没有结束事件就没有实测时长，却报出了 %v", d)
	}
	if d != 0 {
		t.Fatalf("无实测值时时长应为 0，得到 %v", d)
	}

	// 补上结果后，实测时长必须**恰好**等于事件时刻之差——与载荷体积无关。
	p.Apply(ev(eventbus.EventToolResult, 250, map[string]any{
		"call_id": "c1",
		"tool":    "alpha_fn",
		"chars":   4096,
	}))
	rows = p.Rows()
	d, ok = rows[0].Duration()
	if !ok {
		t.Fatal("配对成功后应有实测时长")
	}
	if d != 250*time.Millisecond {
		t.Fatalf("实测时长应等于事件时刻之差 250ms，得到 %v", d)
	}
}

// TestRowHasNoDurationField 时长在结构上只能有一个源：起止时刻相减。
//
// 这条是「实测 vs 估算」的**结构性**钉子：若 Row 里出现时长字段，
// 估算值就有地方可藏（字段会被填成估算值，而调用方无从分辨）。
// 于是本包的核心理由（口径单源）就退化成一句靠自觉的口号。
func TestRowHasNoDurationField(t *testing.T) {
	typ := reflect.TypeOf(Row{})
	dur := reflect.TypeOf(time.Duration(0))
	durPtr := reflect.TypeOf((*time.Duration)(nil))
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type == dur || f.Type == durPtr {
			t.Fatalf("Row 不得有时长字段（实测与估算必须单源）: %s %s", f.Name, f.Type)
		}
		if strings.Contains(strings.ToLower(f.Name), "estim") {
			t.Fatalf("Row 不得有估算字段: %s %s", f.Name, f.Type)
		}
	}
}

// TestDurationRejectsInvertedTimestamps 结束早于开始不是实测值，不报负时长。
func TestDurationRejectsInvertedTimestamps(t *testing.T) {
	r := Row{Kind: KindToolCall, StartAt: at(500), EndAt: at(100)}
	if d, ok := r.Duration(); ok || d != 0 {
		t.Fatalf("倒挂的起止不该报出实测时长，得到 %v (ok=%v)", d, ok)
	}
}
