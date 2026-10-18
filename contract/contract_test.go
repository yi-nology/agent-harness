package contract

// 契约自检：一个最小宿主替身，验证接口**在无领域依赖的前提下可被实现**——
// 这是本包存在的意义（若实现必须碰到 Session/CaseStep，说明契约没设计对）。

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"
)

type fakeRun struct {
	id, caseID, status, step string
	fields                   map[string]string
	rejectUnknown            bool
}

func (f *fakeRun) ID() string          { return f.id }
func (f *fakeRun) CaseID() string      { return f.caseID }
func (f *fakeRun) Status() string      { return f.status }
func (f *fakeRun) CurrentStep() string { return f.step }
func (f *fakeRun) SetStatus(s string) error {
	f.status = s
	return nil
}
func (f *fakeRun) SetCurrentStep(s string) error {
	f.step = s
	return nil
}
func (f *fakeRun) Field(k string) (string, bool) { v, ok := f.fields[k]; return v, ok }
func (f *fakeRun) SetField(k, v string) error {
	if f.rejectUnknown {
		if _, ok := f.fields[k]; !ok {
			return errors.New("未知键，拒绝")
		}
	}
	if f.fields == nil {
		f.fields = map[string]string{}
	}
	f.fields[k] = v
	return nil
}

type fakeSink struct {
	opened  []string
	closed  map[uint64]string
	nextRef uint64
}

func (s *fakeSink) Open(name, label string) (StepRef, error) {
	s.nextRef++
	s.opened = append(s.opened, name+"|"+label)
	return StepRef{ID: s.nextRef, Name: name}, nil
}
func (s *fakeSink) Finish(r StepRef, status string) error {
	if s.closed == nil {
		s.closed = map[uint64]string{}
	}
	s.closed[r.ID] = status
	return nil
}

type fakeTools struct{ names []string }

func (t fakeTools) Names() []string { return t.names }
func (t fakeTools) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

type fakeBinder struct{}

func (fakeBinder) Tools(_ context.Context, server string, allow []string) (Tools, error) {
	if server == "" {
		return nil, errors.New("server 空")
	}
	return fakeTools{names: append([]string{server}, allow...)}, nil
}

// TestMinimalHostSatisfiesContract 最小替身即可满足全部接口——
// 无领域依赖是本包的硬指标。
func TestMinimalHostSatisfiesContract(t *testing.T) {
	var (
		_ RunContext = (*fakeRun)(nil)
		_ StepSink   = (*fakeSink)(nil)
		_ ToolBinder = fakeBinder{}
	)
	r := &fakeRun{id: "s1", caseID: "c1"}
	s := &fakeSink{}
	b := fakeBinder{}

	if err := r.SetStatus(StatusRunning); err != nil || r.Status() != StatusRunning {
		t.Fatalf("状态迁移失败: %v %q", err, r.Status())
	}
	ref, err := s.Open("agent/foo", "专家环节")
	if err != nil || ref.ID == 0 {
		t.Fatalf("建环节失败: %v %+v", err, ref)
	}
	if err := s.Finish(ref, StatusSucceeded); err != nil || s.closed[ref.ID] != StatusSucceeded {
		t.Fatalf("收口失败: %v", err)
	}
	tl, err := b.Tools(context.Background(), "ask-ops", []string{"get_snapshot"})
	if err != nil || len(tl.Names()) != 2 {
		t.Fatalf("工具绑定失败: %v %v", err, tl)
	}
}

// TestFieldFailClosed 宿主拒绝未知键时 SetField 必须报错，**不得静默吞**——
// 静默吞会让编排以为写成功了，状态在重启后消失。
func TestFieldFailClosed(t *testing.T) {
	r := &fakeRun{fields: map[string]string{FieldQueuedInput: "x"}, rejectUnknown: true}
	if err := r.SetField(FieldQueuedInput, "y"); err != nil {
		t.Fatalf("已知键应可写: %v", err)
	}
	if err := r.SetField("tenant_specific", "z"); err == nil {
		t.Fatal("宿主拒绝未知键时，SetField 应报错（fail-closed）")
	}
	// 未知键读取返回 (false) 而非报错——调用方自行降级。
	if _, ok := r.Field("tenant_specific"); ok {
		t.Fatal("未知键读取应返回 ok=false")
	}
}
