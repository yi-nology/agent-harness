package handoff

import (
	"testing"
	"time"
)

// baseTicket 合成交接单（夹具只带中性词面——夹具是契约的示例）。
func baseTicket() Ticket {
	return Ticket{
		Key:     "step-7",
		From:    "proc-a",
		To:      "proc-b",
		Payload: []byte("payload-1"),
		ReplyTo: "reply://proc-a/inbox",
		TTL:     time.Minute,
	}
}

func TestTicketValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Ticket)
		pass bool
	}{
		{"合法最小单", func(*Ticket) {}, true},
		{"缺幂等键", func(t *Ticket) { t.Key = " " }, false},
		{"缺发起方", func(t *Ticket) { t.From = "" }, false},
		{"缺接收方", func(t *Ticket) { t.To = "" }, false},
		{"缺回执地址", func(t *Ticket) { t.ReplyTo = " " }, false},
		{"时限为零", func(t *Ticket) { t.TTL = 0 }, false},
		{"时限为负", func(t *Ticket) { t.TTL = -time.Second }, false},
		{"空载荷合法（交接可以是纯信号）", func(t *Ticket) { t.Payload = nil }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := baseTicket()
			tc.mut(&tk)
			err := tk.Validate()
			if tc.pass && err != nil {
				t.Fatalf("期望通过，得到 %v", err)
			}
			if !tc.pass && err == nil {
				t.Fatal("期望报错，得到 nil")
			}
		})
	}
}

func TestReceiptValidate(t *testing.T) {
	cases := []struct {
		name string
		r    Receipt
		pass bool
	}{
		{"done 带结论", Receipt{Status: ReceiptDone, Summary: "ok"}, true},
		{"done 缺结论", Receipt{Status: ReceiptDone}, false},
		{"done 带失败原因（对「完成」的理解不一致）", Receipt{Status: ReceiptDone, Summary: "ok", Failure: "x"}, false},
		{"failed 带原因", Receipt{Status: ReceiptFailed, Failure: "x"}, true},
		{"failed 缺原因", Receipt{Status: ReceiptFailed}, false},
		{"refused 带原因", Receipt{Status: ReceiptRefused, Failure: "x"}, true},
		{"refused 缺原因", Receipt{Status: ReceiptRefused}, false},
		{"状态不在词表", Receipt{Status: "maybe"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if tc.pass && err != nil {
				t.Fatalf("期望通过，得到 %v", err)
			}
			if !tc.pass && err == nil {
				t.Fatal("期望报错，得到 nil")
			}
		})
	}
}

func TestCheckInvariants(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mk := func(mut func(*Ticket)) Ticket {
		tk := baseTicket()
		tk.Deadline = now.Add(time.Minute)
		tk.State = StateOffered
		tk.Generation = 1
		mut(&tk)
		return tk
	}
	cases := []struct {
		name string
		t    Ticket
		code string
	}{
		{"缺截止时刻", mk(func(t *Ticket) { t.Deadline = time.Time{} }), CodeNoDeadline},
		{"时限非正", mk(func(t *Ticket) { t.TTL = 0 }), CodeTTLNonPositive},
		{"待领取残留负责人", mk(func(t *Ticket) { t.Holder = "proc-b" }), CodeHolderMismatch},
		{"accepted 缺负责人", mk(func(t *Ticket) { t.State = StateAccepted; t.Holder = "" }), CodeHolderMismatch},
		{"accepted 缺代次", mk(func(t *Ticket) { t.State = StateAccepted; t.Holder = "proc-b"; t.Generation = 0 }), CodeMissingFence},
		{"终态残留负责人", mk(func(t *Ticket) { t.State = StateClosed; t.ReceiptAt = now; t.Holder = "proc-b" }), CodeHolderMismatch},
		{"closed 缺结算时刻", mk(func(t *Ticket) { t.State = StateClosed }), CodeReceiptUntimely},
		{"未收口带结算时刻", mk(func(t *Ticket) { t.ReceiptAt = now }), CodeReceiptUntimely},
		{"逾期仍在待领取", mk(func(t *Ticket) { t.Deadline = now.Add(-time.Second) }), CodeOverdueHolder},
		{"逾期仍未收口回执", mk(func(t *Ticket) { t.State = StateAccepted; t.Holder = "proc-b"; t.Deadline = now.Add(-time.Second) }), CodeOverdueReply},
		{"状态不在词表", mk(func(t *Ticket) { t.State = "mystery" }), CodeUnknownState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probs := Check([]Ticket{tc.t}, now)
			if len(probs) == 0 {
				t.Fatal("期望出现不变量违反")
			}
			found := false
			for _, p := range probs {
				if p.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("期望类别 %s，得到 %v", tc.code, probs)
			}
		})
	}

	t.Run("自洽快照零问题", func(t *testing.T) {
		probs := Check([]Ticket{
			mk(func(*Ticket) {}),
			mk(func(t *Ticket) { t.Key = "step-8"; t.State = StateAccepted; t.Holder = "proc-b" }),
			mk(func(t *Ticket) { t.Key = "step-9"; t.State = StateClosed; t.ReceiptAt = now }),
		}, now)
		if len(probs) != 0 {
			t.Fatalf("自洽快照不应有问题：%v", probs)
		}
	})

	t.Run("重复键与空键", func(t *testing.T) {
		a := mk(func(*Ticket) {})
		b := mk(func(*Ticket) {})
		probs := Check([]Ticket{a, b}, now)
		if len(probs) != 1 || probs[0].Code != CodeDuplicateKey {
			t.Fatalf("期望重复键一条，得到 %v", probs)
		}
		probs = Check([]Ticket{{From: "x", To: "y", ReplyTo: "r", TTL: time.Second}}, now)
		if len(probs) != 1 || probs[0].Code != CodeEmptyKey {
			t.Fatalf("期望空键一条，得到 %v", probs)
		}
	})

	t.Run("确定性排序", func(t *testing.T) {
		a := mk(func(t *Ticket) { t.Deadline = time.Time{} })    // no_deadline
		b := mk(func(t *Ticket) { t.Key = "step-8"; t.TTL = 0 }) // ttl_non_positive
		probs1 := Check([]Ticket{a, b}, now)
		probs2 := Check([]Ticket{b, a}, now)
		if len(probs1) != len(probs2) {
			t.Fatalf("问题数量不同：%d vs %d", len(probs1), len(probs2))
		}
		for i := range probs1 {
			if probs1[i] != probs2[i] {
				t.Fatalf("排序不确定：第 %d 条 %v vs %v", i, probs1[i], probs2[i])
			}
		}
	})
}
