package delegate

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

func offer(name string, caps ...Capability) Offer {
	return Offer{Name: name, Provides: caps}
}

// TestAcceptRejectsMissingCapability 能力越界即拒：不许「尽力而为」。
func TestAcceptRejectsMissingCapability(t *testing.T) {
	o := offer("r1", "read", "grep")
	err := o.Accept(Request{Goal: "g", Needs: []Capability{"read", "write"}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("缺能力必须报 ErrUnsupported，实得 %v", err)
	}
	if !strings.Contains(err.Error(), "write") {
		t.Fatalf("错误必须指出缺哪个能力，实得 %v", err)
	}
	if err := o.Accept(Request{Goal: "g", Needs: []Capability{"read"}}); err != nil {
		t.Fatalf("能力满足时必须接受: %v", err)
	}
}

// TestAcceptRejectsBadGoal 空目标与超长目标都拒。
func TestAcceptRejectsBadGoal(t *testing.T) {
	o := offer("r1", "read")
	if err := o.Accept(Request{Goal: "  "}); err == nil {
		t.Fatal("空目标必须拒（无法判定完成）")
	}
	small := Offer{Name: "r1", Provides: []Capability{"read"}, MaxGoalBytes: 3}
	if err := small.Accept(Request{Goal: "toolong"}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超长目标必须拒并说明上限，实得 %v", err)
	}
}

// TestRoutePicksLeastCapable 最小能力原则：多个都能接时取能力最少者。
//
// 「按顺序取第一个」是错的——顺序通常来自配置文件书写次序，
// 一次无关的重排就会换掉执行方。
func TestRoutePicksLeastCapable(t *testing.T) {
	offers := []Offer{
		offer("wide", "read", "write", "net", "exec"),
		offer("narrow", "read"),
		offer("mid", "read", "write"),
	}
	got, err := Route(offers, Request{Goal: "g", Needs: []Capability{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "narrow" {
		t.Fatalf("必须选能力最少的执行方，实得 %s", got.Name)
	}
	// 打乱顺序后仍必须选同一方（确定性）。
	reordered := []Offer{offers[1], offers[0], offers[2]}
	got2, _ := Route(reordered, Request{Goal: "g", Needs: []Capability{"read"}})
	if got2.Name != got.Name {
		t.Fatalf("结果不该依赖 offer 的排列顺序，实得 %s vs %s", got2.Name, got.Name)
	}
	// 同能力数按名字。
	tie := []Offer{offer("zeta", "read"), offer("alpha", "read")}
	got3, _ := Route(tie, Request{Goal: "g", Needs: []Capability{"read"}})
	if got3.Name != "alpha" {
		t.Fatalf("同能力数必须按名字，实得 %s", got3.Name)
	}
}

// TestRouteNoRunner 没人能接必须报 ErrNoRunner，而不是返回一个空 offer。
func TestRouteNoRunner(t *testing.T) {
	_, err := Route([]Offer{offer("r1", "read")}, Request{Goal: "g", Needs: []Capability{"write"}})
	if !errors.Is(err, ErrNoRunner) {
		t.Fatalf("没有能接的执行方必须报 ErrNoRunner，实得 %v", err)
	}
}

// TestReceiptValidate 回执三值自洽性。
//
// 成对：每条坏形态都必须报，且三种合法形态都必须过——只钉坏形态的话，
// 一个「永远报错」的实现也能全绿。
func TestReceiptValidate(t *testing.T) {
	bad := []struct {
		name string
		r    Receipt
	}{
		{"状态不在词表", Receipt{Status: "ok", Summary: "s"}},
		{"completed 无结论", Receipt{Status: StatusCompleted}},
		{"completed 带 Failure", Receipt{Status: StatusCompleted, Summary: "s", Failure: "f"}},
		{"failed 无原因", Receipt{Status: StatusFailed}},
		{"refused 无原因", Receipt{Status: StatusRefused}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if err := c.r.Validate(); err == nil {
				t.Fatal("不自洽的回执必须报错")
			}
		})
	}
	good := []Receipt{
		{Status: StatusCompleted, Summary: "改完了", Artifacts: []string{"a.patch"}},
		{Status: StatusFailed, Failure: "编译不过"},
		{Status: StatusRefused, Failure: "超出授权范围"},
	}
	for _, r := range good {
		if err := r.Validate(); err != nil {
			t.Fatalf("合法回执不该报错: %+v -> %v", r, err)
		}
	}
}
