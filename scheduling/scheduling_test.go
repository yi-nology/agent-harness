package scheduling

import (
	"slices"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 测试夹具全部合成数据（AGENTS.md 红线：夹具是契约的示例，不带领域词面）。

func TestOrderDeterministicAndStable(t *testing.T) {
	type req struct{ id string }
	// 三个优先级 + 同优先级多键：断言「优先级降序、key 升序、输入次序不影响结果」。
	input := []req{
		{"gamma"}, {"beta"}, {"alpha"},
		{"mid-2"}, {"mid-1"},
		{"top-1"},
	}
	prio := func(r req) Priority {
		switch r.id {
		case "top-1":
			return 3
		case "mid-1", "mid-2":
			return 2
		default:
			return 1
		}
	}
	want := []string{"top-1", "mid-1", "mid-2", "alpha", "beta", "gamma"}

	got := Order(input, func(r req) string { return r.id }, prio)
	var gotIDs []string
	for _, r := range got {
		gotIDs = append(gotIDs, r.id)
	}
	if !slices.Equal(gotIDs, want) {
		t.Fatalf("排序口径不符：want %v got %v", want, gotIDs)
	}

	// 输入排列顺序不得影响结果：逐次旋转输入，输出必须逐字相同。
	rotated := slices.Clone(input)
	for range len(input) {
		rotated = append(rotated[1:], rotated[0])
		var ids []string
		for _, r := range Order(rotated, func(r req) string { return r.id }, prio) {
			ids = append(ids, r.id)
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("输入重排后输出漂移：want %v got %v", want, ids)
		}
	}
}

func TestOrderZeroPriorityDefaultsLast(t *testing.T) {
	type req struct{ id string }
	// 未声明优先级（零值）排在所有声明过的后面：漏声明的拿最弱一档，不是最强。
	input := []req{{"plain"}, {"high"}}
	got := Order(input, func(r req) string { return r.id }, func(r req) Priority {
		if r.id == "high" {
			return 9
		}
		return 0
	})
	if len(got) != 2 || got[0].id != "high" || got[1].id != "plain" {
		t.Fatalf("零值优先级必须排在最后（实得 %v %v）", got[0].id, got[1].id)
	}
}

func TestTopKBoundaries(t *testing.T) {
	type req struct{ id string }
	input := []req{{"a"}, {"b"}, {"c"}}
	key := func(r req) string { return r.id }
	if got := TopK(input, key, nil, 0); got != nil {
		t.Fatalf("n=0 必须返回空（实得 %v）", got)
	}
	if got := TopK(input, key, nil, -1); got != nil {
		t.Fatalf("n<0 必须返回空（实得 %v）", got)
	}
	if got := TopK(input, key, nil, 99); len(got) != 3 {
		t.Fatalf("n 超过元素数时取全集（实得 %d 个）", len(got))
	}
	// 输入为空：不 panic，返回 nil。
	if got := TopK(nil, key, nil, 2); got != nil {
		t.Fatalf("空输入必须返回 nil（实得 %v）", got)
	}
}

func TestFairPickerEqualWeightsRoundRobin(t *testing.T) {
	p, err := NewFairPicker(map[string]int{"node-a": 1, "node-b": 1, "node-c": 1})
	if err != nil {
		t.Fatal(err)
	}
	// 等权重：连续 3 轮必须严格轮转（公平判据最硬的形态）。
	want := []string{"node-a", "node-b", "node-c", "node-a", "node-b", "node-c"}
	var got []string
	for range len(want) {
		k := p.Pick()
		got = append(got, k)
		p.Record(k)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("等权重必须严格轮转：want %v got %v", want, got)
	}
	if p.MaxDebt() != 0 {
		t.Fatalf("轮转完成时落差必须为 0（实得 %v）", p.MaxDebt())
	}
}

func TestFairPickerWeightedShares(t *testing.T) {
	// 3:1 权重：9 次服务后应得 6.75/2.25 → 允许 ±1 的整数量化误差，
	// 但必须明显成比例（公平判据不是绝对等分）。
	p, err := NewFairPicker(map[string]int{"heavy": 3, "light": 1})
	if err != nil {
		t.Fatal(err)
	}
	for range 9 {
		k := p.Pick()
		p.Record(k)
	}
	s := p.Served()
	if s["heavy"] != 7 || s["light"] != 2 {
		t.Fatalf("3:1 权重 9 次服务应得 7/2（实得 %v）", s)
	}
	// 确定性：同一起点（零计数）两次构造，选择序列逐字相同。
	seq1, seq2 := make([]string, 0, 4), make([]string, 0, 4)
	pA, _ := NewFairPicker(map[string]int{"heavy": 3, "light": 1})
	pB, _ := NewFairPicker(map[string]int{"heavy": 3, "light": 1})
	for range 4 {
		k := pA.Pick()
		seq1 = append(seq1, k)
		pA.Record(k)
		k = pB.Pick()
		seq2 = append(seq2, k)
		pB.Record(k)
	}
	if !slices.Equal(seq1, seq2) {
		t.Fatalf("同状态两次构造的选择序列必须一致：%v vs %v", seq1, seq2)
	}
	// 换整份重建（带历史计数）：从 p 的落点继续，下一步与 p 自己的下一步一致。
	p2, err := NewFairPickerWithServed(map[string]int{"heavy": 3, "light": 1}, p.Served())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p2.Pick(), p.Pick(); got != want {
		t.Fatalf("带历史重建后选择漂移：want %s got %s", want, got)
	}
}

func TestFairPickerRejectsBadWeights(t *testing.T) {
	if _, err := NewFairPicker(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("空集必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if _, err := NewFairPicker(map[string]int{"node-a": 0}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("零权重必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if _, err := NewFairPicker(map[string]int{"node-a": 1, "node-b": -2}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("负权重必须报 ErrInvalidConfig（实得 %v）", err)
	}
}

func TestFairPickerIgnoresUnknownRecord(t *testing.T) {
	p, _ := NewFairPicker(map[string]int{"node-a": 1, "node-b": 1})
	p.Record("ghost") // 未登记：忽略，不进计数、不 panic
	if got := p.Served(); got["ghost"] != 0 {
		t.Fatalf("未登记键不得进入计数（实得 %v）", got)
	}
}

func TestQuotaLedger(t *testing.T) {
	t.Run("占用与释放守恒", func(t *testing.T) {
		q, err := NewQuota(5)
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Acquire(2); err != nil {
			t.Fatal(err)
		}
		if q.Used() != 2 || q.Remaining() != 3 {
			t.Fatalf("占用后账目不符：used=%d remaining=%d", q.Used(), q.Remaining())
		}
		if err := q.Release(2); err != nil {
			t.Fatal(err)
		}
		if q.Used() != 0 {
			t.Fatalf("释放后必须归零（实得 %d）", q.Used())
		}
	})
	t.Run("整单拒绝不做部分占用", func(t *testing.T) {
		q, _ := NewQuota(5)
		if err := q.Acquire(4); err != nil {
			t.Fatal(err)
		}
		if err := q.Acquire(2); !errors.Is(err, ErrQuotaExhausted) {
			t.Fatalf("余额不足必须报 ErrQuotaExhausted（实得 %v）", err)
		}
		if q.Used() != 4 {
			t.Fatalf("整单拒绝后不得部分占用（实得 used=%d）", q.Used())
		}
	})
	t.Run("超释放必须拒且不改账", func(t *testing.T) {
		q, _ := NewQuota(5)
		if err := q.Release(1); !errors.Is(err, ErrOverRelease) {
			t.Fatalf("零占用时释放必须报 ErrOverRelease（实得 %v）", err)
		}
		if err := q.Acquire(1); err != nil {
			t.Fatal(err)
		}
		if err := q.Release(2); !errors.Is(err, ErrOverRelease) {
			t.Fatalf("超释放必须报 ErrOverRelease（实得 %v）", err)
		}
		if q.Used() != 1 {
			t.Fatalf("被拒的释放不得改账（实得 used=%d）", q.Used())
		}
	})
	t.Run("非法量拒绝", func(t *testing.T) {
		q, _ := NewQuota(5)
		if err := q.Acquire(0); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("占用 0 必须报 ErrInvalidConfig（实得 %v）", err)
		}
		if err := q.Release(-1); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("释放负数必须报 ErrInvalidConfig（实得 %v）", err)
		}
	})
	t.Run("上限零与负", func(t *testing.T) {
		q0, err := NewQuota(0) // 0 合法：显式「完全不许占用」
		if err != nil {
			t.Fatal(err)
		}
		if err := q0.Acquire(1); !errors.Is(err, ErrQuotaExhausted) {
			t.Fatalf("零上限必须拒绝一切占用（实得 %v）", err)
		}
		if _, err := NewQuota(-1); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("负上限必须报 ErrInvalidConfig（实得 %v）", err)
		}
	})
}

func TestBackpressureDecide(t *testing.T) {
	cases := []struct {
		name       string
		policy     Policy
		queue      int
		inFlight   int
		wantAdmit  bool
		wantReason Reason
	}{
		{"不设限全放行", Policy{}, 999, 999, true, ""},
		{"在途满优先于队列满", Policy{MaxQueue: 2, MaxInFlight: 2}, 99, 2, false, ReasonInFlightFull},
		{"仅队列满", Policy{MaxQueue: 2, MaxInFlight: 0}, 2, 0, false, ReasonQueueFull},
		{"队列未满放行", Policy{MaxQueue: 2}, 1, 0, true, ""},
		{"在途未满放行", Policy{MaxInFlight: 4}, 0, 3, true, ""},
		{"在途恰达上限即拒", Policy{MaxInFlight: 4}, 0, 4, false, ReasonInFlightFull},
		{"队列恰达上限即拒", Policy{MaxQueue: 1}, 1, 0, false, ReasonQueueFull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := tc.policy.Decide(tc.queue, tc.inFlight)
			if d.Admit != tc.wantAdmit || d.Reason != tc.wantReason {
				t.Fatalf("裁决不符：want (admit=%v, reason=%s) got (admit=%v, reason=%s)",
					tc.wantAdmit, tc.wantReason, d.Admit, d.Reason)
			}
		})
	}
}

func TestBackpressureValidate(t *testing.T) {
	if err := (Policy{MaxQueue: -1}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("负队列上限必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if err := (Policy{MaxInFlight: -3}).Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("负在途上限必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if err := (Policy{MaxQueue: 1, MaxInFlight: 1}).Validate(); err != nil {
		t.Fatalf("合法配置不得报错（实得 %v）", err)
	}
	// 零值合法：不设限是显式语义（Validate 首行案例的反向）。
	if err := (Policy{}).Validate(); err != nil {
		t.Fatalf("零值（不设限）必须合法（实得 %v）", err)
	}
}
