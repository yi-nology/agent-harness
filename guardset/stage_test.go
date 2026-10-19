package guardset

// 链装配的行为钉住：与 invoke.Chain 是**叠加**关系，不是替代。
//
// 与 guardset_test.go 分文件：那边钉的是合成语义（顺序/裁决/合成），
// 这边钉的是它与上游链的边界——不重复实现链、也不绕开链。

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
)

// TestStageComposesIntoInvokeChain 集合接成链上一环后：链照旧短路在集合这一环，
// 而模型在这一环就已经拿到全部拒绝——正是 invoke.Chain 单靠自身做不到的。
func TestStageComposesIntoInvokeChain(t *testing.T) {
	s, err := New(
		Guard{Name: "披露门", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("工具 alpha_lookup 的说明尚未取回", true)
		}},
		Guard{Name: "出站门", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("目标地址不在允许清单内", false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	executed := false
	chain, err := invoke.NewChain(
		func(context.Context, invoke.Call) (invoke.Result, error) {
			executed = true
			return invoke.Result{Text: "ok"}, nil
		},
		[]invoke.Stage{Stage(s)},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, gotErr := chain.Do(context.Background(), call())
	if gotErr == nil {
		t.Fatal("有拒绝时链必须收口")
	}
	if executed {
		t.Fatal("被拒的调用不得到达执行体")
	}
	if invoke.Retryable(gotErr) {
		t.Fatal("含不可重试拒绝时链不得报可重试")
	}
	var de *invoke.DenyError
	if !errors.As(gotErr, &de) {
		t.Fatalf("应拿到结构化拒绝，实得 %T", gotErr)
	}
	if de.Stage != StageName {
		t.Fatalf("留痕应指向本环 %q，实得 %q", StageName, de.Stage)
	}
	if !strings.Contains(de.Reason, "披露门") || !strings.Contains(de.Reason, "出站门") {
		t.Fatalf("链上的拒绝依据应含全部守卫，实得 %q", de.Reason)
	}
}

// TestStageRecordsExactlyOneRecordPerCall 不变式沿用上游：无论结局如何恰好一条留痕。
// 多次重试不得让审计把同一次调用算成两次。
func TestStageRecordsExactlyOneRecordPerCall(t *testing.T) {
	s, err := New(
		Guard{Name: "披露门", Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("说明尚未取回", true)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var recs []invoke.Record
	chain, err := invoke.NewChain(
		func(context.Context, invoke.Call) (invoke.Result, error) { return invoke.Result{}, nil },
		[]invoke.Stage{Stage(s)},
		invoke.WithRecorder(func(r invoke.Record) { recs = append(recs, r) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = chain.Do(context.Background(), call())
	}
	if len(recs) != 3 {
		t.Fatalf("每次调用应恰好一条留痕，实得 %d 条", len(recs))
	}
	for i, r := range recs {
		if r.Allowed {
			t.Fatalf("第 %d 次不应记为放行", i)
		}
		if r.Err == nil {
			t.Fatalf("第 %d 次留痕必须带收口原因，否则与成功记录无法区分", i)
		}
	}
}

// TestStageWithGuidanceServesModelsNotAudits 两个出口服务两种用途：
// Stage 的 Reason 供留痕（要全且准），StageWithGuidance 的 Reason 供模型（要能照着做）。
func TestStageWithGuidanceServesModelsNotAudits(t *testing.T) {
	s, err := New(
		Guard{Name: "披露门", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("工具 alpha_lookup 的说明尚未取回，请重新调用同一工具", true)
		}},
		Guard{Name: "出站门", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("目标地址不在允许清单内", false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := call()

	audit, err := Stage(s).Check(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	model, err := StageWithGuidance(s).Check(ctx, c)
	if err != nil {
		t.Fatal(err)
	}

	// 留痕面：逐条带门名，便于事后定位是哪几环拒的。
	if !strings.Contains(audit.Reason, "[披露门]") || !strings.Contains(audit.Reason, "[出站门]") {
		t.Fatalf("留痕面应逐条带门名，实得 %q", audit.Reason)
	}
	// 模型面：给出可执行的下一步与优先处理项。
	if !strings.Contains(model.Reason, "请优先处理它") {
		t.Fatalf("模型面应指出先处理哪一条，实得 %q", model.Reason)
	}
	if audit.Retryable != model.Retryable {
		t.Fatal("两个出口的重试结论必须一致——用途不同不该让判定分叉")
	}
	if audit.Allow || model.Allow {
		t.Fatal("两个出口都不得放行")
	}
}

// TestUnassembledStageStillDenies 没装配集合时，链上这一环仍必须拒绝，
// 不能因为「集合是 nil」就让整条链放行。
func TestUnassembledStageStillDenies(t *testing.T) {
	v, err := Stage(nil).Check(context.Background(), call())
	if err != nil {
		t.Fatalf("拒绝应走 Verdict 而非 error（error 在链上会被归口成「这环坏了」），实得 %v", err)
	}
	if v.Allow || v.Retryable {
		t.Fatalf("未装配必须 fail-closed，实得 %+v", v)
	}
	if strings.TrimSpace(v.Reason) == "" {
		t.Fatal("未装配也要给出可读的拒绝依据")
	}
}
