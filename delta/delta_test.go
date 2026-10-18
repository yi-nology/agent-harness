package delta

// 增量节流器单测（真流式观测面）：窗口合并 / seq 单调 / 权威吸收 / 调用轮冲刷 /
// 总量帽 / 收口冲刷全残尾。now 注入免真实等待。

import (
	"testing"
	"time"
)

func TestDeltaPacerWindowMerge(t *testing.T) {
	p := New()
	t0 := time.Now()
	p.now = func() time.Time { return t0 }

	// 窗口内小分片：不发布
	for i := 0; i < 3; i++ {
		if _, ok := p.Offer("reasoning", 1, "思"); ok {
			t.Fatal("窗口内不应发布")
		}
	}
	// 超 200 字符阈值：立即发布（seq 自 1）
	big := ""
	for i := 0; i < 25; i++ {
		big += "八十字符八十字符"
	}
	d, ok := p.Offer("reasoning", 1, big)
	if !ok || d.Seq != 1 {
		t.Fatalf("阈值触发应发布 seq=1: %+v ok=%v", d, ok)
	}
	// 时间推进 400ms：小分片也发布
	t0 = t0.Add(400 * time.Millisecond)
	d, ok = p.Offer("reasoning", 1, "尾")
	if !ok || d.Seq != 2 || d.Text != "尾" {
		t.Fatalf("窗口到期应发布 seq=2: %+v ok=%v", d, ok)
	}
}

func TestDeltaPacerAbsorbAndFlushOnToolCall(t *testing.T) {
	p := New()
	t0 := time.Now()
	p.now = func() time.Time { return t0 }

	if _, ok := p.Offer("text", 1, "前导正文"); ok {
		t.Fatal("窗口内不应发布")
	}
	// 权威快照到达：缓冲吸收，后续迟到分片丢弃
	p.Absorb("text", 1)
	if d, ok := p.Flush("text", 1); ok {
		t.Fatalf("吸收后不应再发布: %+v", d)
	}
	if _, ok := p.Offer("text", 1, "迟到"); ok {
		t.Fatal("吸收后的迟到分片应丢弃")
	}

	// 调用轮正文（无权威快照）：tool_call 收口前冲刷残尾
	if _, ok := p.Offer("text", 2, "调用轮前导"); ok {
		t.Fatal("窗口内不应发布")
	}
	d, ok := p.Flush("text", 2)
	if !ok || d.Seq != 1 || d.Text != "调用轮前导" {
		t.Fatalf("调用轮残尾冲刷应发布: %+v ok=%v", d, ok)
	}
}

func TestDeltaPacerFlushAllAndCap(t *testing.T) {
	p := New()
	t0 := time.Now()
	p.now = func() time.Time { return t0 }

	// 多轮多类残尾：flushAll 按 (轮升序, reasoning→text) 冲刷
	_, _ = p.Offer("text", 1, "残1")
	_, _ = p.Offer("reasoning", 2, "残2")
	_, _ = p.Offer("text", 2, "残3")
	got := p.FlushAll()
	if len(got) != 3 {
		t.Fatalf("应冲刷 3 残尾: %+v", got)
	}
	want := []struct {
		kind string
		it   int
		text string
	}{{"text", 1, "残1"}, {"reasoning", 2, "残2"}, {"text", 2, "残3"}}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Iteration != w.it || got[i].Text != w.text {
			t.Fatalf("flushAll[%d] = %+v，期望 %v", i, got[i], w)
		}
	}

	// 总量帽：64KB 后静默停发
	p2 := New()
	p2.now = func() time.Time { return t0 }
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = 'a'
	}
	published := 0
	for i := 0; i < 100; i++ {
		// 每片 4KB、强制过窗（时间推进）
		t0 = t0.Add(400 * time.Millisecond)
		if _, ok := p2.Offer("reasoning", 1, string(chunk)); ok {
			published += 4096
		}
	}
	if published > CapBytes+4096 {
		t.Fatalf("应被总量帽拦住: published=%d cap=%d", published, CapBytes)
	}
}
