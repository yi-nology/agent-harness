package backend

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/oekit/concurrency/async"
	kerrors "github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sandbox"
)

// stub 基型替身：只实现必选面，可选能力面（EgressEnforcer / Capable）**不带**——
// 「沉默」必须由类型缺席表达，带一个永远返回值的字段测不出沉默路径。
type stub struct {
	id  string
	iso Isolation
}

func (s *stub) ID() string           { return s.id }
func (s *stub) Isolation() Isolation { return s.iso }
func (s *stub) Exec(_ context.Context, _ Job) (Result, error) {
	return Result{OK: true, ExitCode: 0}, nil
}

// stubEg 带 EgressEnforcer 的替身。
type stubEg struct {
	*stub
	eg sandbox.EgressMode
}

func (s *stubEg) Egress() sandbox.EgressMode { return s.eg }

// stubCap 带 Capable 的替身。
type stubCap struct {
	*stub
	capOK bool
}

func (s *stubCap) Capable(b sandbox.Budget) bool {
	return s.capOK && b.MaxMemory > 0 // 只声明了内存这一维，够判据用
}

// stubFull 两个可选面都带的替身（用作「自报齐全」的满足者）。
type stubFull struct {
	*stub
	eg    sandbox.EgressMode
	capOK bool
}

func (s *stubFull) Egress() sandbox.EgressMode { return s.eg }

func (s *stubFull) Capable(b sandbox.Budget) bool { return s.capOK && b.MaxMemory > 0 }

func TestIsolationOrder(t *testing.T) {
	t.Run("序即强弱", func(t *testing.T) {
		if !IsolationVM.AtLeast(IsolationContainer) {
			t.Fatal("vm 必须达到 container")
		}
		if IsolationNone.AtLeast(IsolationProcess) {
			t.Fatal("none 不得达到 process")
		}
	})
	t.Run("零值即最弱档", func(t *testing.T) {
		var zero Isolation
		if zero != IsolationNone {
			t.Fatalf("零值应是最弱档，得到 %s", zero)
		}
	})
	t.Run("越界值不得当最弱档", func(t *testing.T) {
		if Isolation(9).Valid() {
			t.Fatal("越界档位必须是 invalid")
		}
	})
	t.Run("String 越界不崩", func(t *testing.T) {
		if got := Isolation(9).String(); got == "" {
			t.Fatal("越界档位应有可读文本")
		}
	})
}

func TestJobValidate(t *testing.T) {
	base := Job{Image: "example.com/img@sha256:abc"}
	cases := []struct {
		name  string
		mut   func(*Job)
		orerr bool // 期望通过（false）/ 期望报错（true）
	}{
		{"合法最小作业", func(*Job) {}, false},
		{"缺镜像", func(j *Job) { j.Image = " " }, true},
		{"env 缺等号", func(j *Job) { j.Env = []string{"KEYVALUE"} }, true},
		{"env 空键", func(j *Job) { j.Env = []string{"=v"} }, true},
		{"env 合法", func(j *Job) { j.Env = []string{"K1=v1", "K2="} }, false},
		{"挂载缺源", func(j *Job) { j.Mounts = []Mount{{Target: "/x"}} }, true},
		{"挂载缺目标", func(j *Job) { j.Mounts = []Mount{{Source: "/x"}} }, true},
		{"挂载合法", func(j *Job) { j.Mounts = []Mount{{Source: "/a", Target: "/b", ReadOnly: true}} }, false},
		{"负内存预算", func(j *Job) { j.Budget.MaxMemory = -1 }, true},
		{"负墙钟", func(j *Job) { j.Budget.WallClock = -1 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { // 逐条独立报告，避免共享 Fatal 造成假绿
			j := base
			tc.mut(&j)
			err := j.Validate()
			if tc.orerr && err == nil {
				t.Fatal("期望报错，得到 nil")
			}
			if !tc.orerr && err != nil {
				t.Fatalf("期望通过，得到 %v", err)
			}
		})
	}
}

func TestSpecSatisfiedBy(t *testing.T) {
	cases := []struct {
		name   string
		spec   Spec
		b      Backend
		wantIs error // nil = 满足；非 nil = 期望的哨兵
	}{
		{
			name:   "无要求：沉默后端也满足",
			spec:   Spec{},
			b:      &stub{id: "a", iso: IsolationNone},
			wantIs: nil,
		},
		{
			name:   "隔离不够强：ErrNotSatisfied",
			spec:   Spec{MinIsolation: IsolationContainer},
			b:      &stub{id: "a", iso: IsolationProcess},
			wantIs: ErrNotSatisfied,
		},
		{
			name:   "隔离恰好达到：满足",
			spec:   Spec{MinIsolation: IsolationContainer},
			b:      &stub{id: "a", iso: IsolationVM},
			wantIs: nil,
		},
		{
			name:   "档位越界：按声明缺陷拒（不是更强）",
			spec:   Spec{},
			b:      &stub{id: "a", iso: Isolation(9)},
			wantIs: ErrNotSatisfied,
		},
		{
			name:   "要求出站但后端沉默：ErrSilent",
			spec:   Spec{Egress: sandbox.EgressDenied},
			b:      &stub{id: "a", iso: IsolationVM},
			wantIs: ErrSilent,
		},
		{
			name:   "要求出站且声明不满足：ErrNotSatisfied",
			spec:   Spec{Egress: sandbox.EgressDenied},
			b:      &stubEg{stub: &stub{id: "a", iso: IsolationVM}, eg: sandbox.EgressProxy},
			wantIs: ErrNotSatisfied,
		},
		{
			name:   "要求出站且禁网声明满足禁网要求（禁网强于代理）",
			spec:   Spec{Egress: sandbox.EgressProxy},
			b:      &stubEg{stub: &stub{id: "a", iso: IsolationVM}, eg: sandbox.EgressDenied},
			wantIs: nil,
		},
		{
			name:   "要求内存但后端沉默：ErrSilent",
			spec:   Spec{Budget: sandbox.Budget{MaxMemory: 1 << 20}},
			b:      &stub{id: "a", iso: IsolationContainer},
			wantIs: ErrSilent,
		},
		{
			name:   "要求内存且后端自报兜不住：ErrNotSatisfied",
			spec:   Spec{Budget: sandbox.Budget{MaxMemory: 1 << 20}},
			b:      &stubCap{stub: &stub{id: "a", iso: IsolationContainer}},
			wantIs: ErrNotSatisfied,
		},
		{
			name:   "要求内存且后端自报兜得住：满足",
			spec:   Spec{Budget: sandbox.Budget{MaxMemory: 1 << 20}},
			b:      &stubCap{stub: &stub{id: "a", iso: IsolationContainer}, capOK: true},
			wantIs: nil,
		},
		{
			name:   "要求越界档位：ErrBadSpec",
			spec:   Spec{MinIsolation: Isolation(9)},
			b:      &stub{id: "a", iso: IsolationVM},
			wantIs: ErrBadSpec,
		},
		{
			name:   "nil 后端：ErrSilent",
			spec:   Spec{},
			b:      nil,
			wantIs: ErrSilent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.SatisfiedBy(tc.b)
			switch {
			case tc.wantIs == nil && err != nil:
				t.Fatalf("期望满足，得到 %v", err)
			case tc.wantIs != nil && err == nil:
				t.Fatalf("期望 %v，得到 nil", tc.wantIs)
			case tc.wantIs != nil && !kerrors.Is(err, tc.wantIs):
				t.Fatalf("期望哨兵 %v，得到 %v", tc.wantIs, err)
			}
		})
	}
}

func TestRegistryResolve(t *testing.T) {
	// strong / mild / weak：三个自报齐全的后端（都声明了禁网与内存能力），
	// 强弱序与 ID 序刻意交叉，防「按登记顺序取第一个」混过判定。
	strong := &stubFull{stub: &stub{id: "strong", iso: IsolationVM}, eg: sandbox.EgressDenied, capOK: true}
	mild := &stubFull{stub: &stub{id: "mild", iso: IsolationContainer}, eg: sandbox.EgressDenied, capOK: true}
	weak := &stub{id: "weak", iso: IsolationNone}

	t.Run("无满足者即拒绝，不降级", func(t *testing.T) {
		r := &Registry{}
		if err := r.Register(weak); err != nil {
			t.Fatal(err)
		}
		_, err := r.Resolve(Spec{MinIsolation: IsolationContainer})
		if !kerrors.Is(err, ErrNoBackend) {
			t.Fatalf("期望 ErrNoBackend，得到 %v", err)
		}
	})

	t.Run("满足者取最弱，同档按 ID 字典序", func(t *testing.T) {
		r := &Registry{}
		for _, b := range []Backend{strong, weak, mild} { // 登记顺序刻意与强弱序不同
			if err := r.Register(b); err != nil {
				t.Fatal(err)
			}
		}
		got, err := r.Resolve(Spec{MinIsolation: IsolationContainer, Egress: sandbox.EgressDenied,
			Budget: sandbox.Budget{MaxMemory: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID() != "mild" {
			t.Fatalf("期望选中最弱满足者 mild，得到 %s", got.ID())
		}
	})

	t.Run("同档按 ID 字典序（与登记顺序无关）", func(t *testing.T) {
		b1 := &stub{id: "b-second", iso: IsolationContainer}
		a1 := &stub{id: "a-first", iso: IsolationContainer}
		r := &Registry{}
		for _, b := range []Backend{b1, a1} {
			if err := r.Register(b); err != nil {
				t.Fatal(err)
			}
		}
		got, err := r.Resolve(Spec{MinIsolation: IsolationContainer})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID() != "a-first" {
			t.Fatalf("期望字典序在前者被选中，得到 %s", got.ID())
		}
	})

	t.Run("重复 ID 与不合法登记被拒", func(t *testing.T) {
		r := &Registry{}
		if err := r.Register(nil); !kerrors.Is(err, ErrBadBackend) {
			t.Fatalf("期望 ErrBadBackend，得到 %v", err)
		}
		if err := r.Register(&stub{id: " ", iso: IsolationNone}); err == nil {
			t.Fatal("空 ID 应被拒")
		}
		if err := r.Register(weak); err != nil {
			t.Fatal(err)
		}
		if err := r.Register(&stub{id: "weak", iso: IsolationNone}); !kerrors.Is(err, ErrDuplicateID) {
			t.Fatalf("期望 ErrDuplicateID，得到 %v", err)
		}
	})

	t.Run("Candidates 顺序确定", func(t *testing.T) {
		r := &Registry{}
		for _, b := range []Backend{strong, weak, mild} {
			if err := r.Register(b); err != nil {
				t.Fatal(err)
			}
		}
		cands, err := r.Candidates(Spec{})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(cands))
		for _, b := range cands {
			ids = append(ids, b.ID())
		}
		if want := []string{"weak", "mild", "strong"}; !slices.Equal(ids, want) {
			t.Fatalf("候选顺序 %v，期望 %v", ids, want)
		}
		_, err = r.Candidates(Spec{MinIsolation: Isolation(9)})
		if !kerrors.Is(err, ErrBadSpec) {
			t.Fatalf("期望 ErrBadSpec，得到 %v", err)
		}
	})

	t.Run("并发登记与解析", func(t *testing.T) {
		r := &Registry{}
		var (
			mu      sync.Mutex
			regErrs []error
			resErrs []error
		)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 8 {
			wg.Add(1)
			async.GoSafe(func() {
				defer wg.Done()
				<-start
				b := &stub{id: string(rune('a' + i)), iso: IsolationProcess}
				if err := r.Register(b); err != nil {
					mu.Lock()
					regErrs = append(regErrs, err)
					mu.Unlock()
					return
				}
				if _, err := r.Resolve(Spec{}); err != nil {
					mu.Lock()
					resErrs = append(resErrs, err)
					mu.Unlock()
				}
			})
		}
		close(start)
		wg.Wait()
		if len(regErrs) != 0 || len(resErrs) != 0 {
			t.Fatalf("并发登记/解析失败：reg=%v res=%v", regErrs, resErrs)
		}
		if len(r.list) != 8 {
			t.Fatalf("登记丢失：共 %d", len(r.list))
		}
	})
}
