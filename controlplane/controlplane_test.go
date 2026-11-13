package controlplane

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/tenant"
)

// tenantCtx 介入写路径的租户上下文夹具（scope 词面是合成数据，词表归宿主）。
func tenantCtx() context.Context {
	return tenant.WithContext(context.Background(),
		tenant.Context{Tenant: "acme", Subject: "op-1", Scopes: []string{"control.intervene"}})
}

// emptyCtx 未注入租户上下文的请求（写路径必须被拒）。
func emptyCtx() context.Context { return context.Background() }

// seededPlane 场景夹具：run r1 挂两个任务（t1 可认领，t2 依赖 t1），
// 一个未知的副作用键。
func seededPlane(t *testing.T) (*MemPlane, *MemAuditor) {
	t.Helper()
	aud := &MemAuditor{}
	m := NewMemPlane(aud)
	err := m.SeedRun("r1", contract.StatusRunning, []taskboard.Task{
		{ID: "t1", Title: "第一个环节", State: taskboard.StateOpen},
		{ID: "t2", Title: "第二个环节", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t1"}},
	})
	if err != nil {
		t.Fatalf("SeedRun: %v", err)
	}
	if err := m.SeedEffect("effect-1", false); err != nil {
		t.Fatalf("SeedEffect: %v", err)
	}
	return m, aud
}

func TestValidateInterventions(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"强释缺 operator", ForceRelease{TaskID: "t1", Basis: "b"}.Validate(), ErrInvalidRequest},
		{"强释缺 basis", ForceRelease{TaskID: "t1", Operator: "op"}.Validate(), ErrInvalidRequest},
		{"强释缺 target", ForceRelease{Operator: "op", Basis: "b"}.Validate(), ErrInvalidRequest},
		{"强释完整", ForceRelease{TaskID: "t1", Operator: "op", Basis: "b"}.Validate(), nil},
		{"裁决缺 basis", Adjudication{EffectKey: "k", Verdict: VerdictApplied, Operator: "op"}.Validate(), ErrInvalidRequest},
		{"裁决结论不在词表", Adjudication{EffectKey: "k", Verdict: "maybe", Operator: "op", Basis: "b"}.Validate(), ErrInvalidRequest},
		{"裁决完整", Adjudication{EffectKey: "k", Verdict: VerdictNotApplied, Operator: "op", Basis: "b"}.Validate(), nil},
		{"终止缺 operator", Termination{RunID: "r1", ExpectedGen: 1, Basis: "b"}.Validate(), ErrInvalidRequest},
		{"终止完整", Termination{RunID: "r1", ExpectedGen: 1, Operator: "op", Basis: "b"}.Validate(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", tc.err, tc.want)
			}
		})
	}
}

func TestViews(t *testing.T) {
	m, _ := seededPlane(t)
	ctx := context.Background()

	view, err := m.RunView(ctx, "r1")
	if err != nil {
		t.Fatalf("RunView: %v", err)
	}
	if view.Status != contract.StatusRunning || view.Gen != 1 {
		t.Fatalf("RunView 字段漂移： %+v", view)
	}
	if len(view.Tasks) != 2 || view.Tasks[0].ID != "t1" {
		t.Fatalf("任务快照未按 id 排序： %+v", view.Tasks)
	}
	if len(view.Problems) != 0 {
		t.Fatalf("干净场景不应有不变量违反： %+v", view.Problems)
	}

	// 不变量投影：认领态但没写租约（seed 数据自身矛盾）→ 投到该任务身上
	if err := m.SeedRun("r2", contract.StatusRunning, []taskboard.Task{
		{ID: "t3", State: taskboard.StateClaimed, Owner: "a1"}, // seed 自身矛盾：认领态没写租约
	}); err != nil {
		t.Fatalf("SeedRun r2: %v", err)
	}
	tv, err := m.TaskView(ctx, "t3")
	if err != nil {
		t.Fatalf("TaskView: %v", err)
	}
	if len(tv.Problems) != 1 || tv.Problems[0].Code != taskboard.CodeClaimedNoLease {
		t.Fatalf("不变量投影漂移： %+v", tv.Problems)
	}
	// 干净任务投不到任何问题
	tv, err = m.TaskView(ctx, "t1")
	if err != nil || len(tv.Problems) != 0 {
		t.Fatalf("干净任务应零问题： %v %+v", err, tv.Problems)
	}

	// 目标不存在
	if _, err := m.RunView(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知 run 应 ErrNotFound，got %v", err)
	}
	if _, err := m.TaskView(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知任务应 ErrNotFound，got %v", err)
	}
}

func TestInterventionGates(t *testing.T) {
	m, _ := seededPlane(t)

	// 留痕面缺位 → 介入一律拒绝（fail-closed）
	naive := NewMemPlane(nil)
	err := naive.ForceRelease(tenantCtx(), ForceRelease{TaskID: "t1", Operator: "op", Basis: "b"})
	if !errors.Is(err, ErrNoAuditor) {
		t.Fatalf("无留痕面应拒绝 ErrNoAuditor，got %v", err)
	}
	err = naive.Adjudicate(tenantCtx(), Adjudication{EffectKey: "effect-1", Verdict: VerdictApplied, Operator: "op", Basis: "b"})
	if !errors.Is(err, ErrNoAuditor) {
		t.Fatalf("无留痕面应拒绝 ErrNoAuditor，got %v", err)
	}
	err = naive.Terminate(tenantCtx(), Termination{RunID: "r1", ExpectedGen: 1, Operator: "op", Basis: "b"})
	if !errors.Is(err, ErrNoAuditor) {
		t.Fatalf("无留痕面应拒绝 ErrNoAuditor，got %v", err)
	}

	// 请求不完整 → ErrInvalidRequest（无归责不介入）
	err = m.ForceRelease(tenantCtx(), ForceRelease{TaskID: "t1", Operator: "op"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("缺 basis 应拒绝，got %v", err)
	}

	// 未注入租户上下文 → ErrNoContext（介入是写路径，必须可归责）
	err = m.ForceRelease(emptyCtx(), ForceRelease{TaskID: "t1", Operator: "op", Basis: "b"})
	if !errors.Is(err, tenant.ErrNoContext) {
		t.Fatalf("未注入租户上下文应拒绝，got %v", err)
	}

	// 目标不存在 → ErrNotFound（同样留痕）
	err = m.ForceRelease(tenantCtx(), ForceRelease{TaskID: "nope", Operator: "op", Basis: "b"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知任务应 ErrNotFound，got %v", err)
	}
}
