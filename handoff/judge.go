// judge.go —— 交接契约的**判据面**：自洽性判定与快照不变量（纯函数）。
//
// 判据长在包里而不是只在测试里（taskboard.Check 的同一理由）：宿主的存储
// 实现随时可以拿当前快照跑一遍 @@Check@@，「这一单自不自洽」不必等人肉
// review 发现。
package handoff

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/slicex"
)

// Validate 判交接单是否自洽（不满足即报错，不静默补全）。
//
// 三条必填各有出处：
//
//   - Key：没有幂等键的交接无法回答「做过没有」，重放语义无从谈起；
//   - From / To / ReplyTo：回执寻址是契约硬面——没有回执地址的交接等于
//     寄丢；指定接收方让领取有判定对象（不是「谁先看到谁领」）；
//   - TTL > 0：交接必须有界（四问之四的契约化）——无时限的跨进程交接
//     只会留下一把谁都不敢动的锁。
func (t Ticket) Validate() error {
	if strings.TrimSpace(string(t.Key)) == "" {
		return errors.New("handoff: 交接单没有幂等键（重放与去重都无从谈起）")
	}
	if strings.TrimSpace(string(t.From)) == "" {
		return errors.New("handoff: 交接单没有发起方（收回权与留痕无从定位）")
	}
	if strings.TrimSpace(string(t.To)) == "" {
		return errors.New("handoff: 交接单没有指定接收方（领取无判定对象）")
	}
	if strings.TrimSpace(t.ReplyTo) == "" {
		return errors.New("handoff: 交接单没有回执地址（回执回到谁手里没有答案）")
	}
	if t.TTL <= 0 {
		return errors.New("handoff: 交接时限必须为正（无时限的交接单是契约禁止的）")
	}
	return nil
}

// Validate 判回执是否自洽（与 delegate.Receipt.Validate 同一条纪律）。
//
//   - 状态在词表内；
//   - done 必须有 Summary，且**不得**带 Failure（带了说明双方对「完成」
//     的理解不一致，而源方会按「完成」处理）；
//   - failed / refused 必须有 Failure（没有原因就无法决定重交接还是改方案）。
func (r Receipt) Validate() error {
	switch r.Status {
	case ReceiptDone:
		if strings.TrimSpace(r.Summary) == "" {
			return errors.New("handoff: done 的回执没有结论——「完成了」但说不出做了什么，无法使用")
		}
		if strings.TrimSpace(r.Failure) != "" {
			return errors.New("handoff: done 的回执带了 Failure（双方对「完成」的理解不一致）")
		}
	case ReceiptFailed, ReceiptRefused:
		if strings.TrimSpace(r.Failure) == "" {
			return errors.New("handoff: " + string(r.Status) + " 的回执没有原因——没有原因就无从决定重交接还是改方案")
		}
	default:
		return errors.New("handoff: 回执状态 " + string(r.Status) + " 不在词表内（done / failed / refused）")
	}
	return nil
}

// 不变量类别（Problem.Code，机器可判）。
const (
	CodeEmptyKey        = "empty_key"
	CodeDuplicateKey    = "duplicate_key"
	CodeUnknownState    = "unknown_state"
	CodeNoDeadline      = "no_deadline"
	CodeHolderMismatch  = "holder_mismatch"
	CodeMissingFence    = "missing_generation"
	CodeReceiptUntimely = "receipt_without_close"
	CodeOverdueHolder   = "overdue_holder"
	CodeOverdueReply    = "overdue_reply"
	CodeTTLNonPositive  = "ttl_non_positive"
)

// Problem 一条快照不变量违反。
type Problem struct {
	// Key 相关交接单（空 = 不针对单条）。
	Key Key
	// Code 机器可判的类别。
	Code string
	// Text 人读的说明。
	Text string
}

// Check 判一份快照的**不变量**，按 (Key, Code) 排序返回（确定性）。
//
// now 由调用方注入：过期判定必须能被确定性测（boardtest.FakeClock 的同一
// 理由——靠 time.Now 的判定只能靠 sleep 测）。
//
// 本函数判**状态自身是否自相矛盾**，不判策略对错：
//
//   - 键唯一、状态在词表内；
//   - Deadline 必须有值（无界交接是契约禁止的），TTL 必须为正；
//   - 状态与负责人/代次必须同进退：accepted 必有 Holder 与代次；
//     非 accepted 不得残留 Holder；
//   - closed 必有结算时刻；未收口不得带结算时刻；
//   - 过期判定（懒式）：now 已过 Deadline 的 offered 单本应被标 Expired、
//     已过 Deadline 的 accepted 单本应被对账——发现残留即报
//     （实现方可以懒式落库，但快照要让「逾期没人管」显形）。
func Check(tickets []Ticket, now time.Time) []Problem {
	var out []Problem
	byKey := make(map[Key]Ticket, len(tickets))
	dup := map[Key]bool{}

	for _, t := range tickets {
		if t.Key == "" {
			out = append(out, Problem{Code: CodeEmptyKey, Text: "交接单没有幂等键"})
			continue
		}
		if _, ok := byKey[t.Key]; ok {
			dup[t.Key] = true
			continue
		}
		byKey[t.Key] = t
	}
	for k := range dup {
		out = append(out, Problem{Key: k, Code: CodeDuplicateKey, Text: "幂等键重复：重放与去重的判定对象不再唯一"})
	}

	for _, t := range byKey {
		if !t.State.valid() {
			out = append(out, Problem{Key: t.Key, Code: CodeUnknownState,
				Text: "状态不在词表内（offered / accepted / closed / expired / withdrawn）"})
			continue
		}
		if t.TTL <= 0 {
			out = append(out, Problem{Key: t.Key, Code: CodeTTLNonPositive, Text: "时限非正：无时限的交接单是契约禁止的"})
		}
		if t.Deadline.IsZero() {
			out = append(out, Problem{Key: t.Key, Code: CodeNoDeadline, Text: "没有截止时刻：交接单无界，谁都不敢动它"})
		}
		switch t.State {
		case StateAccepted:
			if t.Holder == "" {
				out = append(out, Problem{Key: t.Key, Code: CodeHolderMismatch, Text: "已交接但没有负责人"})
			}
			if t.Generation == 0 {
				out = append(out, Problem{Key: t.Key, Code: CodeMissingFence, Text: "已交接但没有代次（迟到者无法被判旧）"})
			}
		case StateOffered:
			if t.Holder != "" {
				out = append(out, Problem{Key: t.Key, Code: CodeHolderMismatch, Text: "待领取却残留负责人：读起来像有人在负责，实际没有"})
			}
		default: // 终态
			if t.Holder != "" {
				out = append(out, Problem{Key: t.Key, Code: CodeHolderMismatch, Text: "终态残留负责人：收口后不该还有人负责"})
			}
		}
		if t.State == StateClosed && t.ReceiptAt.IsZero() {
			out = append(out, Problem{Key: t.Key, Code: CodeReceiptUntimely, Text: "已收口但没有结算时刻：回执何时生效无从追溯"})
		}
		if t.State != StateClosed && !t.ReceiptAt.IsZero() {
			out = append(out, Problem{Key: t.Key, Code: CodeReceiptUntimely, Text: "未收口却带结算时刻：回执与状态互相矛盾"})
		}
		// 懒式过期的残留：实现方允许「读时才判过期」，但快照必须让逾期显形。
		if !t.Deadline.IsZero() && now.After(t.Deadline) {
			switch t.State {
			case StateOffered:
				out = append(out, Problem{Key: t.Key, Code: CodeOverdueHolder,
					Text: "已过截止仍是待领取：本应落 expired（实现方没有兑现懒式过期）"})
			case StateAccepted:
				out = append(out, Problem{Key: t.Key, Code: CodeOverdueReply,
					Text: "已过截止仍未收口回执：本应转对账（逾期单在快照里看起来还有人负责）"})
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Code, b.Code))
	})
	return out
}

// joinAgents 把身份列表拼成可读文本（留痕用）。
func joinAgents(as []Agent) string {
	return strings.Join(slicex.Map(as, func(a Agent) string { return string(a) }), ", ")
}
