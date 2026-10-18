package statemachine

import "testing"

// 全转移表覆盖（DONE_CHECKLIST 2.6：合法转移每条 ≥1 正用例、非法转移 ≥1 负用例）。
func TestLegalTransitions(t *testing.T) {
	legal := [][2]StepStatus{
		{StatusCreated, StatusRunning},
		{StatusCreated, StatusSkipped},
		{StatusRunning, StatusSucceeded},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusWaitingApproval},
		{StatusWaitingApproval, StatusApproved},
		{StatusWaitingApproval, StatusRejected},
		{StatusWaitingApproval, StatusExpired},
		{StatusApproved, StatusRunning},
		{StatusFailed, StatusRolledBack},
	}
	for _, pair := range legal {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("期望合法: %s → %s", pair[0], pair[1])
		}
	}
}

func TestIllegalTransitions(t *testing.T) {
	illegal := [][2]StepStatus{
		{StatusCreated, StatusSucceeded},         // 跳过运行
		{StatusCreated, StatusApproved},          // 跳过审批
		{StatusSucceeded, StatusRunning},         // 终态复活
		{StatusRejected, StatusRunning},          // 驳回后直接运行（必须走修订重提）
		{StatusExpired, StatusApproved},          // 超时后批准
		{StatusRunning, StatusApproved},          // 运行态直接批
		{StatusApproved, StatusSucceeded},        // 批准未执行即成功
		{StatusRolledBack, StatusRunning},        // 回退后复活
		{StatusWaitingApproval, StatusSucceeded}, // 待批直接成功
	}
	for _, pair := range illegal {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("期望非法: %s → %s", pair[0], pair[1])
		}
		if err := MustTransition(pair[0], pair[1]); err == nil {
			t.Errorf("MustTransition 应报错: %s → %s", pair[0], pair[1])
		}
	}
}

func TestRequiresApproval(t *testing.T) {
	cases := map[StepType]bool{
		TypeReadonly: false,
		TypePlan:     true,
		TypeExecute:  true,
		TypeApproval: false,
		TypeVerify:   false,
	}
	for typ, want := range cases {
		if got := RequiresApproval(typ); got != want {
			t.Errorf("RequiresApproval(%s)=%v, want %v", typ, got, want)
		}
	}
}

func TestTerminal(t *testing.T) {
	for _, s := range []StepStatus{StatusRejected, StatusExpired, StatusSucceeded, StatusSkipped, StatusRolledBack} {
		if !IsTerminal(s) {
			t.Errorf("%s 应为终态", s)
		}
	}
	for _, s := range []StepStatus{StatusCreated, StatusRunning, StatusWaitingApproval, StatusApproved} {
		if IsTerminal(s) {
			t.Errorf("%s 不应为终态", s)
		}
	}
}
