package ticket

// 票据原语的判据用例（机制面）。
//
// ⭐ 判据钉的是**三处最容易悄悄坏掉的语义**：
//
//  1. 单次消费：同一 id 第二次消费必须失败（不是「尽量不重复」）；
//  2. 过期即失效：过期判定与消费判定同一处（不靠 sleep 测——注入时钟）；
//  3. 随机源失败**显式报错**（不降级成可预测 id——否则单次消费没意义）。

import (
	"testing"
	"time"
)

func TestIssueConsume一次消费(t *testing.T) {
	s := New()
	b := Binding{Subject: "s1", Purpose: "p1", Target: "t1"}
	id, exp, err := s.Issue(b, time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if id == "" {
		t.Fatal("票据 id 不应为空")
	}
	if exp.IsZero() {
		t.Fatal("过期时刻不应为零值")
	}
	got, ok := s.Consume(id)
	if !ok {
		t.Fatal("首次消费必须成功")
	}
	if got != b {
		t.Fatalf("绑定载荷应原样取回: %+v", got)
	}
	// ⭐ 单次消费：第二次必须失败
	if _, ok := s.Consume(id); ok {
		t.Fatal("同一票据第二次消费必须失败（单次消费语义破了）")
	}
	if s.Len() != 0 {
		t.Fatalf("消费后不应残留票据，实际 %d", s.Len())
	}
}

func TestConsume未知票据失败(t *testing.T) {
	s := New()
	if _, ok := s.Consume("deadbeef"); ok {
		t.Fatal("未知票据必须消费失败")
	}
	if _, ok := s.Consume(""); ok {
		t.Fatal("空 id 必须消费失败")
	}
}

func TestConsume过期票据失败(t *testing.T) {
	// ⭐ 注入时钟 ⇒ 过期语义被确定性地测（不靠 sleep——靠 sleep 的话，
	// 「等到了」会被当成「判对了」，判定写反了测试照样绿）。
	now := time.Unix(1700000000, 0)
	s := NewWithClock(0, func() time.Time { return now })
	id, _, err := s.Issue(Binding{Subject: "s1"}, time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	now = now.Add(2 * time.Minute) // 过期后
	if _, ok := s.Consume(id); ok {
		t.Fatal("过期票据必须消费失败")
	}
	// 过期票据被消费（尝试）后也应从空间里清掉——否则它一直占着内存
	if s.Len() != 0 {
		t.Fatalf("过期票据尝试消费后应被移除，实际残留 %d", s.Len())
	}
}

func TestIssue容量上限拒发(t *testing.T) {
	s := NewWithClock(2, time.Now)
	for i := 0; i < 2; i++ {
		if _, _, err := s.Issue(Binding{}, time.Minute); err != nil {
			t.Fatalf("第 %d 次签发不应失败: %v", i+1, err)
		}
	}
	// ⭐ 满了要说满：默默淘汰会让宿主以为票据还在，实际已失效 ⇒ 表现为
	// 「偶发地连不上」，极难查。
	if _, _, err := s.Issue(Binding{}, time.Minute); err == nil {
		t.Fatal("超出容量上限必须显式报错（不得默默淘汰）")
	}
}

func TestGC清过期票据(t *testing.T) {
	now := time.Unix(1700000000, 0)
	s := NewWithClock(0, func() time.Time { return now })
	id1, _, _ := s.Issue(Binding{Subject: "a"}, time.Minute)
	id2, _, _ := s.Issue(Binding{Subject: "b"}, time.Hour)
	now = now.Add(2 * time.Minute)
	if n := s.GC(); n != 1 {
		t.Fatalf("应清掉 1 张过期票据，实际 %d", n)
	}
	if s.Len() != 1 {
		t.Fatalf("应剩 1 张，实际 %d", s.Len())
	}
	if _, ok := s.Consume(id1); ok {
		t.Fatal("被 GC 的票据不得再消费")
	}
	if _, ok := s.Consume(id2); !ok {
		t.Fatal("未过期票据应仍可消费")
	}
}

func Test票据id互不相同(t *testing.T) {
	s := New()
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id, _, err := s.Issue(Binding{}, time.Minute)
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		if seen[id] {
			t.Fatalf("票据 id 重复: %q", id)
		}
		seen[id] = true
	}
}

func Test并发签发消费不重复放行(t *testing.T) {
	// ⭐ 并发下「单次消费」必须仍然只放行一次（锁在正确的位置）。
	s := New()
	const n = 32
	ids := make([]string, n)
	for i := range ids {
		id, _, err := s.Issue(Binding{Subject: "s"}, time.Minute)
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		ids[i] = id
	}
	okCount := make(chan bool, n)
	for _, id := range ids {
		go func(id string) {
			_, ok := s.Consume(id)
			okCount <- ok
		}(id)
	}
	success := 0
	for i := 0; i < n; i++ {
		if <-okCount {
			success++
		}
	}
	if success != n {
		t.Fatalf("每张票据各消费一次应全部成功，实际成功 %d/%d", success, n)
	}
	// 再来一轮：全部已被消费，必须全失败
	for _, id := range ids {
		if _, ok := s.Consume(id); ok {
			t.Fatalf("已消费的票据不得再放行: %q", id)
		}
	}
}
