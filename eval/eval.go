// Package eval 轨迹的**录制 → 重放 → 差异比对**与**确定性评估判据**（机制面）。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是哪一段（issue #111）
//
// @@trajectory@@ 把事件流投影成轨迹行，但「给定一段录制，能否复现同一条
// 轨迹」「给定一条轨迹，能否给出可判定的结论」此前没有机制面：各宿主
// 各写一遍录制格式、各比一遍差异、各造一套「看起来跑得对不对」的口径，
// 于是「同一条轨迹」无从谈起。
//
// # 回放为什么天然确定（锚点在哪）
//
// 轨迹行的**行 ID 是确定性生成的**（trajectory 的 turnRowID / stepRowID /
// toolRowID / seqID）——同一条事件流投出的行 ID 必然相同，这就是「同一条
// 轨迹」的锚点。行上的起止时刻取自**事件时刻**（不是墙钟），录制文件里
// 就带着时刻，重放不需要也不允许重新计时。因此本包的时间观只有一条：
// **时刻是数据，不是时钟**——录制即定格，重放即复读，没有 sleep、没有
// now()。
//
// # 确定性评估是什么意思
//
// 评估判据是**纯函数**（@@Assess@@）：同一份轨迹行输入，永远得到同一份
// 判定输出。它不跑模型、不看结果像不像——「像不像」要再跑一次模型，
// 那不是评估，是抽签。判据回答的是结构事实：行收没收口、调用配没配对、
// 有没有重复声明、期望的行在不在。
//
// # 边界
//
//   - 录制格式只支持 JSON 可表达的载荷（@@Verify@@ 判适航；不适航的事件
//     录了也录不稳，显式报出而不是静默形变）；
//   - 评估判据只判**轨迹结构**，不判业务对错（业务结论归宿主的判定面）；
//   - 差异比对（@@Diff@@）是纯函数，排序确定——同一对输入永远同一份差异。
package eval

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/trajectory"
)

// Check 评估判项词表（每个判项对应一类结构事实）。
type Check string

const (
	// CheckClosure 收口完整：不存在「开了没收口」的行（open）。
	//
	// 孤儿行（只有终点、没有起点）单独看：checkpoint 点行**本来就是孤儿
	// （设计事实——点事件没有括号段）**，不在此判项内；其余类目的孤儿行
	// 意味着起点缺失，照判。
	CheckClosure Check = "closure"
	// CheckPairing 配对完整：工具调用行全部配对（无 missing_result /
	// unpaired_result / duplicate_result / duplicate_call）。
	CheckPairing Check = "pairing"
	// CheckNoDuplicates 无重复声明（重复结果 / 重复调用）。
	CheckNoDuplicates Check = "no_duplicates"
	// CheckExpected 期望行存在（Spec.ExpectedIDs 全部在轨迹里）。
	CheckExpected Check = "expected"
	// CheckForbidden 禁止行缺席（Spec.ForbiddenIDs 一条都不许在）。
	CheckForbidden Check = "forbidden"
)

// ErrNoChecks 评估清单为空——判不了就不判，不给「零判项全过」的假绿。
var ErrNoChecks = errors.New("eval: 评估清单为空（「没有检查」与「检查通过」必须分得开）")

// ErrBadSpec 评估清单不自洽（判项不在词表内 / 期望清单缺失）。
var ErrBadSpec = errors.New("eval: 评估清单不自洽")

// Spec 一份评估清单：要判哪些判项 + 期望/禁止的行清单。
//
// 零值 Spec 判不了任何东西——Assess 对它显式报 ErrNoChecks，不返回
// 「通过」。判项显式列出，判了什么一目了然（skilldisclose 的同一纪律：
// 空结果必须写明原因，「没有检查」与「检查通过」在调用方看来不一样）。
type Spec struct {
	// Checks 判项清单（词表见 Check 各常量）。
	Checks []Check
	// ExpectedIDs 必须存在的行 ID（仅 CheckExpected 使用）。
	ExpectedIDs []string
	// ForbiddenIDs 不允许存在的行 ID（仅 CheckForbidden 使用）。
	ForbiddenIDs []string
}

// Finding 一条判定结论（违规即 finding；不违规不产生条目）。
type Finding struct {
	// Check 所属判项。
	Check Check
	// RowID 相关行（不针对单行的结论为空）。
	RowID string
	// Text 人读的说明。
	Text string
}

// Verdict 一份评估结论。
type Verdict struct {
	// Pass 是否通过（无 finding 即通过）。
	Pass bool
	// Judged 实际执行的判项数（与 Spec.Checks 等长——「判了几条」是结论
	// 的一部分，不是元信息）。
	Judged int
	// Findings 违规清单，按 (Check, RowID) 排序（确定性）。
	Findings []Finding
}

// Assess 跑一份评估清单：纯函数——同一输入永远同一输出。
//
// 判据口径：
//
//   - closure：Status=open 的行即违规；非 checkpoint 类目的 orphan 行
//     （起点缺失）同样违规——checkpoint 点行的孤儿形态是投影的设计事实，
//     不判；
//   - pairing：工具行（tool_call / tool_result）的 Pairing 不为
//     paired / none 即违规；
//   - no_duplicates：DupResults > 0 或 Pairing 为 duplicate_* 即违规；
//   - expected：ExpectedIDs 逐个核对，缺一个报一个；
//   - forbidden：ForbiddenIDs 逐个核对，在一条报一条。
func Assess(spec Spec, rows []trajectory.Row) (Verdict, error) {
	if len(spec.Checks) == 0 {
		return Verdict{}, ErrNoChecks
	}
	seen := map[Check]bool{}
	for _, c := range spec.Checks {
		switch c {
		case CheckClosure, CheckPairing, CheckNoDuplicates, CheckExpected, CheckForbidden:
			if seen[c] {
				return Verdict{}, errors.Wrap(ErrBadSpec, "eval: 判项 "+string(c)+" 重复列出")
			}
			seen[c] = true
		default:
			return Verdict{}, errors.Wrap(ErrBadSpec, "eval: 判项 "+string(c)+" 不在词表内")
		}
	}
	if slices.Contains(spec.Checks, CheckExpected) && len(spec.ExpectedIDs) == 0 {
		return Verdict{}, errors.Wrap(ErrBadSpec, "eval: 判项 expected 需要非空的 ExpectedIDs")
	}
	if slices.Contains(spec.Checks, CheckForbidden) && len(spec.ForbiddenIDs) == 0 {
		return Verdict{}, errors.Wrap(ErrBadSpec, "eval: 判项 forbidden 需要非空的 ForbiddenIDs")
	}

	byID := make(map[string]trajectory.Row, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	var findings []Finding
	add := func(c Check, rowID, text string) {
		findings = append(findings, Finding{Check: c, RowID: rowID, Text: text})
	}

	for _, r := range rows {
		if seen[CheckClosure] {
			switch {
			case r.Status == trajectory.StatusOpen:
				add(CheckClosure, r.ID, "行开了没收口（缺结束事件）")
			case r.Status == trajectory.StatusOrphan && r.Kind != trajectory.KindCheckpoint:
				add(CheckClosure, r.ID, "行只有终点没有起点（起点缺失）")
			}
		}
		if seen[CheckPairing] && (r.Kind == trajectory.KindToolCall || r.Kind == trajectory.KindToolResult) {
			if r.Pairing != trajectory.PairingPaired && r.Pairing != trajectory.PairingNone {
				add(CheckPairing, r.ID, "配对缺口："+string(r.Pairing))
			}
		}
		if seen[CheckNoDuplicates] {
			if r.DupResults > 0 {
				add(CheckNoDuplicates, r.ID, fmt.Sprintf("重复结果 %d 次", r.DupResults))
			} else if r.Pairing == trajectory.PairingDuplicateResult || r.Pairing == trajectory.PairingDuplicateCall {
				add(CheckNoDuplicates, r.ID, "重复声明："+string(r.Pairing))
			}
		}
	}

	if seen[CheckExpected] {
		for _, id := range spec.ExpectedIDs {
			if _, ok := byID[id]; !ok {
				add(CheckExpected, id, "期望的行不存在")
			}
		}
	}
	if seen[CheckForbidden] {
		for _, id := range spec.ForbiddenIDs {
			if _, ok := byID[id]; ok {
				add(CheckForbidden, id, "禁止的行出现了")
			}
		}
	}

	slices.SortStableFunc(findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(a.Check, b.Check), cmp.Compare(a.RowID, b.RowID))
	})
	return Verdict{Pass: len(findings) == 0, Judged: len(spec.Checks), Findings: findings}, nil
}
