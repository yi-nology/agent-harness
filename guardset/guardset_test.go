package guardset

// 守卫装配契约的行为钉住。夹具用合成工具名（alpha_lookup）与合成门名，不带领域味。

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/toolgate/invoke"
	"github.com/oekit/pkg/errors"
)

func allow() invoke.Verdict { return invoke.Verdict{Allow: true} }

func deny(reason string, retryable bool) invoke.Verdict {
	return invoke.Verdict{Allow: false, Reason: reason, Retryable: retryable}
}

func call() invoke.Call { return invoke.Call{Name: "alpha_lookup"} }

// TestAllDenialsCollectedNotShortCircuited 本包存在的理由：invoke.Chain 在第一个
// 拒绝处就收口，后面的守卫永远没机会说话，模型一次只听得到一个理由。
//
// 这条用例是整个包的立身之本——若将来有人「顺手优化」成短路，本用例会立刻变红。
func TestAllDenialsCollectedNotShortCircuited(t *testing.T) {
	run := 0
	s, err := New(
		Guard{Name: "披露门", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			run++
			return deny("工具 alpha_lookup 的说明尚未取回", true)
		}},
		Guard{Name: "出站门", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			run++
			return deny("目标地址不在允许清单内", false)
		}},
		Guard{Name: "配额门", Rank: 3, Check: func(context.Context, invoke.Call) invoke.Verdict {
			run++
			return allow()
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	out := s.Evaluate(context.Background(), call())
	if out.Allowed {
		t.Fatal("有拒绝时不得放行")
	}
	// 短路会让后一条永远跑不到：run 会是 1 而不是 3。
	if run != 3 {
		t.Fatalf("全部守卫都应被求值，实得 %d 次——出现短路即本包前提被破坏", run)
	}
	if len(out.Denials) != 2 {
		t.Fatalf("两条拒绝都应被收集，实得 %d 条", len(out.Denials))
	}
	if out.Denials[0].Guard != "披露门" || out.Denials[1].Guard != "出站门" {
		t.Fatalf("拒绝应按优先级序收集，实得 %v", out.Denials)
	}
	if out.Primary.Guard != "披露门" {
		t.Fatalf("主拒绝应是优先级最高者，实得 %q", out.Primary.Guard)
	}
	// 留痕必须能看出「不止一处拒过」，否则排查的人会以为只有第一处。
	if r := out.Reason(); !strings.Contains(r, "披露门") || !strings.Contains(r, "出站门") {
		t.Fatalf("留痕应包含全部拒绝，实得 %q", r)
	}
}

// TestRetryableRequiresEveryDenialRetryable 合成重试结论取保守侧：
// 只要有一条不可重试，整个调用就绝无可能靠重试成功。
//
// 取「任一可重试」的话，一条不可恢复的拒绝会被无关的可重试拒绝洗白，
// 退避层于是把一次策略否定拖成无穷重试。
func TestRetryableRequiresEveryDenialRetryable(t *testing.T) {
	mk := func(a, b bool) Outcome {
		s, err := New(
			Guard{Name: "甲", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
				return deny("甲的拒绝", a)
			}},
			Guard{Name: "乙", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
				return deny("乙的拒绝", b)
			}},
		)
		if err != nil {
			t.Fatal(err)
		}
		return s.Evaluate(context.Background(), call())
	}
	if !mk(true, true).Retryable {
		t.Fatal("全部可重试时合成结论应为可重试")
	}
	if mk(true, false).Retryable {
		t.Fatal("含一条不可重试时不得判为可重试")
	}
	if mk(false, true).Retryable {
		t.Fatal("含一条不可重试时不得判为可重试（与次序无关）")
	}

	// 短路版的对照：若按 invoke.Chain 的短路语义，这里只会看到第一条，
	// 结论会随装配次序在「可重试」与「不可重试」之间翻转——那正是要消除的漂移。
	if mk(false, true).Retryable != mk(true, false).Retryable {
		t.Fatal("合成结论不得随装配次序翻转")
	}
}

// TestRankOrdersStably 优先级小者先说话；Rank 相同按装配序（稳定）。
func TestRankOrdersStably(t *testing.T) {
	mk := func(r1, r2, r3 int) *Set {
		s, err := New(
			Guard{Name: "甲", Rank: r1, Check: func(context.Context, invoke.Call) invoke.Verdict { return deny("甲", false) }},
			Guard{Name: "乙", Rank: r2, Check: func(context.Context, invoke.Call) invoke.Verdict { return deny("乙", false) }},
			Guard{Name: "丙", Rank: r3, Check: func(context.Context, invoke.Call) invoke.Verdict { return deny("丙", false) }},
		)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := mk(3, 1, 2).Guards(); got[0] != "乙" || got[1] != "丙" || got[2] != "甲" {
		t.Fatalf("应按 Rank 升序，实得 %v", got)
	}
	// 同 Rank 必须保持装配序，且反复构造结果一致（编排可复现）。
	for i := 0; i < 5; i++ {
		got := mk(1, 1, 1).Guards()
		if got[0] != "甲" || got[1] != "乙" || got[2] != "丙" {
			t.Fatalf("同 Rank 应保持装配序，实得 %v", got)
		}
	}
}

// TestAssemblyConstraintsFailEarly 三条装配期硬约束都在构造期失败，不留到运行期。
func TestAssemblyConstraintsFailEarly(t *testing.T) {
	if _, err := New(); !errors.Is(err, ErrEmptySet) {
		t.Fatalf("空集合应构造失败，实得 %v", err)
	}
	if _, err := New(Guard{Name: "  ", Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }}); !errors.Is(err, ErrUnnamedGuard) {
		t.Fatalf("空名守卫应构造失败，实得 %v", err)
	}
	dup := func(context.Context, invoke.Call) invoke.Verdict { return allow() }
	if _, err := New(Guard{Name: "甲", Check: dup}, Guard{Name: "甲", Check: dup}); !errors.Is(err, ErrDuplicateGuard) {
		t.Fatalf("重名守卫应构造失败，实得 %v", err)
	}
}

// TestBrokenGuardDeniesInsteadOfPassing 守卫自身出问题一律按拒绝收口，
// 且不可重试——重试同一个坏环节没有意义。
func TestBrokenGuardDeniesInsteadOfPassing(t *testing.T) {
	cases := []struct {
		why   string
		guard Guard
	}{
		{"未装配判定函数", Guard{Name: "空环"}},
		{"判定函数 panic", Guard{Name: "炸环", Check: func(context.Context, invoke.Call) invoke.Verdict {
			panic("环内部炸了")
		}}},
		{"拒绝但不给理由", Guard{Name: "哑环", Check: func(context.Context, invoke.Call) invoke.Verdict {
			return invoke.Verdict{Allow: false}
		}}},
	}
	for _, c := range cases {
		c.guard.Rank = 1
		s, err := New(c.guard)
		if err != nil {
			t.Fatal(err)
		}
		out := s.Evaluate(context.Background(), call())
		if out.Allowed {
			t.Fatalf("%s：守卫坏掉不得放行（放行等于静默不设防）", c.why)
		}
		if len(out.Denials) != 1 {
			t.Fatalf("%s：应恰好一条拒绝，实得 %d 条", c.why, len(out.Denials))
		}
		if out.Retryable {
			t.Fatalf("%s：守卫自身出问题不得判为可重试", c.why)
		}
		// 拒绝却无理由时必须补出可读文本，否则模型收到的是一条没有内容的否定。
		if strings.TrimSpace(out.Reason()) == "" {
			t.Fatalf("%s：拒绝必须带可读依据", c.why)
		}
		if strings.TrimSpace(out.Guidance()) == "" {
			t.Fatalf("%s：拒绝必须能合成模型指引", c.why)
		}
	}
	// panic 不得逃出 Evaluate：调用方不该为了用守卫集合而补 recover。
	t.Run("panic 不逃逸", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic 逃出了 Evaluate：%v", r)
			}
		}()
		s, _ := New(Guard{Name: "炸环", Check: func(context.Context, invoke.Call) invoke.Verdict {
			panic("又炸")
		}})
		_ = s.Evaluate(context.Background(), call())
	})
}

// TestUnassembledSetDenies 未构造的 Set 被拿来求值属运行期装配遗漏，
// 按拒绝而不是放行（与构造期拒绝空集合同一条纪律）。
func TestUnassembledSetDenies(t *testing.T) {
	var s *Set
	out := s.Evaluate(context.Background(), call())
	if out.Allowed {
		t.Fatal("未装配的集合不得放行")
	}
	if out.Retryable {
		t.Fatal("未装配不得判为可重试")
	}
	if len(out.Denials) != 1 || out.Primary.Guard != stageUnassembled {
		t.Fatalf("应留下一条归属明确的拒绝，实得 %+v", out)
	}
}

// TestGuidancePointsAtTheBlockingOne 指引要告诉模型**先处理哪一条**：
// 那条不可重试的才是真正卡住的地方。
func TestGuidancePointsAtTheBlockingOne(t *testing.T) {
	s, err := New(
		Guard{Name: "披露门", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("说明尚未取回，请重新调用同一工具", true)
		}},
		Guard{Name: "出站门", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("目标地址不在允许清单内", false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	g := s.Evaluate(context.Background(), call()).Guidance()
	if !strings.Contains(g, "2 项检查") {
		t.Fatalf("指引应说明未通过几项，实得 %q", g)
	}
	if !strings.Contains(g, "说明尚未取回") || !strings.Contains(g, "目标地址不在允许清单内") {
		t.Fatalf("指引应逐条列出，实得 %q", g)
	}
	// 不可重试的那条是第 2 项，指引必须指向它而不是优先级更高的第 1 项。
	if !strings.Contains(g, "第 2 项无法通过重试解决") {
		t.Fatalf("指引应指向真正卡住的那一条，实得 %q", g)
	}
	if strings.Contains(g, "可通过重试恢复") {
		t.Fatalf("含不可重试项时不得说「可通过重试恢复」，实得 %q", g)
	}

	// 全部可重试时反过来要说清「整体可恢复」。
	s2, _ := New(
		Guard{Name: "甲", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict { return deny("甲", true) }},
		Guard{Name: "乙", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict { return deny("乙", true) }},
	)
	g2 := s2.Evaluate(context.Background(), call()).Guidance()
	if !strings.Contains(g2, "可通过重试恢复") {
		t.Fatalf("全部可重试时应说明整体可恢复，实得 %q", g2)
	}
	if strings.Contains(g2, "无法通过重试解决") {
		t.Fatalf("全部可重试时不应出现「无法通过重试解决」，实得 %q", g2)
	}
}

// TestReasonJoinsWithoutDoublePunctuation 留痕拼接不得产出双标点。
//
// 各环理由本身多以句号收尾，直接用分号连接会渲染成「……。；……」——
// 这不是排版洁癖：留痕是要长期归档给人查的，双标点每次出现都在污染可读性，
// 而没人会去清。
func TestReasonJoinsWithoutDoublePunctuation(t *testing.T) {
	s, err := New(
		Guard{Name: "甲", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("第一条理由。", true)
		}},
		Guard{Name: "乙", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("第二条理由。", false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Evaluate(context.Background(), call()).Reason()
	if strings.Contains(r, "。；") || strings.Contains(r, "；。") {
		t.Fatalf("拼接处出现双标点，实得 %q", r)
	}
	if strings.Count(r, "；") != 1 {
		t.Fatalf("两条拒绝应恰有一个分隔符，实得 %q", r)
	}
	// 去掉句尾标点不得吃掉理由正文。
	if !strings.Contains(r, "第一条理由") || !strings.Contains(r, "第二条理由") {
		t.Fatalf("拼接不得吃掉理由正文，实得 %q", r)
	}
}

// TestAllowedOutcomeIsQuiet 放行时不该留下任何拒绝文本——
// 否则宿主会把「一切正常」当成「有问题」去留痕。
func TestAllowedOutcomeIsQuiet(t *testing.T) {
	s, err := New(
		Guard{Name: "甲", Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
		Guard{Name: "乙", Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
	)
	if err != nil {
		t.Fatal(err)
	}
	out := s.Evaluate(context.Background(), call())
	if !out.Allowed || out.Retryable {
		t.Fatalf("全部放行时应为放行且无需重试，实得 %+v", out)
	}
	if len(out.Denials) != 0 {
		t.Fatalf("放行时不应有拒绝，实得 %v", out.Denials)
	}
	if out.Reason() != "" || out.Guidance() != "" {
		t.Fatal("放行时不应产出拒绝文本")
	}
}
