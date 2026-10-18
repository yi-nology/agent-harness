package dispatch

import (
	"testing"

	"github.com/oekit/pkg/errors"
)

// mapEdgeSource 测试用拓扑源：以 slug → allow 列表描述一张合成拓扑。
// 刻意**不复刻任何真实领域拓扑**——守卫机制的可测性与运维域无关。
type mapEdgeSource map[string][]string

func (m mapEdgeSource) Slugs() []string {
	out := make([]string, 0, len(m))
	for slug := range m {
		out = append(out, slug)
	}
	return out
}

func (m mapEdgeSource) DispatchAllow(slug string) []string { return m[slug] }

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: 期望构造期 fail-fast panic，实际未 panic", what)
		}
	}()
	fn()
}

// TestNormalRoute allow 边放行；Allowed 只读查询与 Assert 一致。
func TestNormalRoute(t *testing.T) {
	g := NewGuard(mapEdgeSource{
		"caller": {"callee"},
		"callee": {},
	}, 5)
	if err := g.Assert("caller", "callee", 0); err != nil {
		t.Fatalf("allow 边应放行, got: %v", err)
	}
	if !g.Allowed("caller", "callee") {
		t.Fatal("Allowed 应与 Assert 判定一致（allow 边）")
	}
	if g.Allowed("callee", "caller") {
		t.Fatal("未声明的反向边不应可达")
	}
}

// TestNotAllowed 无 allow 边即拒绝，reason=not_allowed。
func TestNotAllowed(t *testing.T) {
	g := NewGuard(mapEdgeSource{"caller": {}, "callee": {}}, 5)
	err := g.Assert("caller", "callee", 0)
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonNotAllowed {
		t.Fatalf("无 allow 边应拒绝(reason=not_allowed), got: %v", err)
	}
}

// TestSelfDispatchDenied 判定顺序：caller==callee 先于 allow 矩阵命中即拒绝。
// 拓扑刻意不含自环——自环在构造期就会 panic（见 TestSelfLoopPanics）。
func TestSelfDispatchDenied(t *testing.T) {
	g := NewGuard(mapEdgeSource{"solo": {}, "other": {}}, 5)
	err := g.Assert("solo", "solo", 1)
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonSelfDispatch {
		t.Fatalf("自派发应拒绝(reason=self_dispatch), got: %v", err)
	}
}

// TestDepthExceeded depth >= maxDepth 即拒绝（边界含等号）。
func TestDepthExceeded(t *testing.T) {
	g := NewGuard(mapEdgeSource{"a": {"b"}, "b": {}, "c": {}}, 2)

	err := g.Assert("a", "b", 2)
	var deny *DenyError
	if !errors.As(err, &deny) || deny.Reason != ReasonDepthExceeded {
		t.Fatalf("depth==maxDepth 应拒绝(reason=depth_exceeded), got: %v", err)
	}
	if err := g.Assert("a", "b", 1); err != nil {
		t.Fatalf("depth<maxDepth 的 allow 边应放行: %v", err)
	}
	if err := g.AssertDepth(2); err == nil {
		t.Fatal("AssertDepth(==maxDepth) 应报错")
	}
	if err := g.AssertDepth(1); err != nil {
		t.Fatalf("AssertDepth(<maxDepth) 应通过: %v", err)
	}
}

// TestSingleEdgeToExecutable 「全拓扑只有一条边能到达执行点」是宿主的产品级安全不变量。
// 本包只保证机制层与声明一致：唯一授权节点可达，其余节点不可达。
func TestSingleEdgeToExecutable(t *testing.T) {
	g := NewGuard(mapEdgeSource{
		"entry":      {"auditor"},
		"auditor":    {"executable"},
		"other":      {},
		"executable": {},
		"bystander":  {},
	}, 5)

	if err := g.Assert("auditor", "executable", 1); err != nil {
		t.Fatalf("唯一授权节点→执行点应放行: %v", err)
	}
	for _, caller := range []string{"entry", "other", "bystander"} {
		if err := g.Assert(caller, "executable", 2); err == nil {
			t.Fatalf("%s→executable 应不可达", caller)
		}
	}
}

// TestCyclePanics allow 矩阵含乒乓环 → 构造期 fail-fast（否则运行期无限派发）。
func TestCyclePanics(t *testing.T) {
	mustPanic(t, "乒乓环 a→b→a", func() {
		NewGuard(mapEdgeSource{"a": {"b"}, "b": {"a"}}, 5)
	})
}

// TestSelfLoopPanics 自环同样在构造期 fail-fast。
func TestSelfLoopPanics(t *testing.T) {
	mustPanic(t, "自环 a→a", func() {
		NewGuard(mapEdgeSource{"a": {"a"}}, 5)
	})
}
