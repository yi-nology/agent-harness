package sandbox

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 本文件只验证**门面契约**——本包转成上游转发后，宿主的调用面必须逐字不变。
//
// 实现层的行为测试归上游（github.com/oagentkit/guardx/sandbox），不在本仓重复一份：
// 同一机制的两份测试会各自漂移，漂移时没人知道该信哪份。
//
// 这里钉的是**接口面**：
//  1. 类型同一性——别名不是「长得像」，编译期就是同一个类型；
//  2. 方法随别名转发——Registry 的 Register/Resolve/Providers 必须可用；
//  3. 核心语义不弱化——「选不到就拒绝，绝不降级」是本包存在的理由，
//     门面化若让它悄悄变成降级，本包就成装饰。

// stubProvider 测试用后端。
type stubProvider struct {
	id    string
	level Level
}

func (s stubProvider) ID() string   { return s.id }
func (s stubProvider) Level() Level { return s.level }
func (s stubProvider) Exec(ctx context.Context, req Request) (Outcome, error) {
	return Outcome{OK: true, Value: "ok"}, nil
}

// TestTypeIdentity 类型别名是编译期同一类型。
func TestTypeIdentity(t *testing.T) {
	// 本仓 ptc 的 Provider 适配器按同一套接口实现（ID/Level/Exec）——
	// 别名转发后那套实现必须仍能当作 upstream.Provider 使用。
	var p Provider = stubProvider{id: "x", level: LevelProcess}
	if p.Level() != LevelProcess {
		t.Errorf("档位=%v，应为 process", p.Level())
	}
}

// TestRegistryMethodsPresent Registry 的方法随别名一同转发。
//
// 这三个方法是宿主与本仓 ptc 真正在用的；别名不带过来就会编译不过，
// 门面的「调用面不变」承诺就落空了。
func TestRegistryMethodsPresent(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubProvider{id: "a", level: LevelProcess}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := reg.Register(stubProvider{id: "b", level: LevelOS}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	// Resolve
	p, err := reg.Resolve(LevelProcess)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.ID() != "a" {
		t.Errorf("选出 %s，应为最弱满足者 a", p.ID())
	}
	// Providers
	providers := reg.Providers()
	if len(providers) != 2 {
		t.Errorf("应有 2 个后端，实得 %d", len(providers))
	}
}

// TestLevelConstants 档位常量可导出且序即强弱。
func TestLevelConstants(t *testing.T) {
	if LevelNone >= LevelProcess || LevelProcess >= LevelOS || LevelOS >= LevelMachine {
		t.Error("档位序应递增：none < process < os < machine")
	}
}

// TestParseLevelRoundTrip 档位名解析往返。
func TestParseLevelRoundTrip(t *testing.T) {
	got, err := ParseLevel("os")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got != LevelOS {
		t.Errorf("解析得 %v，应为 os", got)
	}
	// 未知档位必须报错，不退回缺省档位。
	if _, err := ParseLevel("kernel"); err == nil {
		t.Error("未知档位必须报错")
	}
}

// TestResolveFailsClosedInsteadOfDowngrading 核心语义：选不到就拒绝，绝不降级。
//
// 这是本包存在的唯一理由——降级会让「宿主声明的强度」与「实际执行的强度」
// 之间没有信号，档位整套机制就成了装饰。
func TestResolveFailsClosedInsteadOfDowngrading(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubProvider{id: "process", level: LevelProcess}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	p, err := reg.Resolve(LevelOS)
	if err == nil {
		t.Fatal("无满足者时必须报错")
	}
	if p != nil {
		t.Error("无满足者时不得返回后端")
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("错误应可被 errors.Is(ErrNoProvider) 认出，实得 %v", err)
	}
}

// TestExecuteRoutesThroughRegistry 执行入口走登记簿。
func TestExecuteRoutesThroughRegistry(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubProvider{id: "a", level: LevelProcess}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	out, err := Execute(context.Background(), reg, LevelProcess, Request{Program: "x"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !out.OK {
		t.Error("执行应成功")
	}
}

// TestRequestOutcomeShape Request/Outcome 的字段形状不变。
//
// 字段名逐字不变是「调用面不变」的另一面：宿主构造 Request 时按字段名赋值，
// 字段一改名就是编译错误——但字段类型悄悄变了（比如 Budget 三项顺序）不会。
func TestRequestOutcomeShape(t *testing.T) {
	req := Request{
		Program: "p",
		Args:    []string{"a"},
		Dir:     "/tmp",
		Budget:  Budget{MaxCalls: 1, MaxBytes: 2},
	}
	if req.Program != "p" || req.Dir != "/tmp" || len(req.Args) != 1 {
		t.Errorf("Request 字段形状不对: %+v", req)
	}
	if req.Budget.MaxCalls != 1 || req.Budget.MaxBytes != 2 {
		t.Errorf("Budget 字段形状不对: %+v", req.Budget)
	}
	out := Outcome{OK: true, Value: "v", Detail: "d"}
	if !out.OK || out.Value != "v" || out.Detail != "d" {
		t.Errorf("Outcome 字段形状不对: %+v", out)
	}
	// Failure 是稳定失败形状（Kind 可判定 / Message 给人看）。
	f := &Failure{Kind: "compile", Message: "语法错误"}
	var err error = f
	if err.Error() != "语法错误" {
		t.Errorf("Failure 错误串=%q，应为 语法错误", err.Error())
	}
}
