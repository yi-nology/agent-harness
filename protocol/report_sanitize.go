package protocol

// 报告脚手架净化（批次三百三十九，解析出口服务端单点）：多轮接力场景模型会把组装
// prompt 的脚手架原文复述进报告字段——<system-reminder source="conversation_history">
// 跨轮上下文块与【历史对话】【调度上下文】等结构段（sess-1013-d58e4c6v 链终报
// 「现象」字段实弹；更早 sess-1005-afzbzq9q 促成批157 前端净化器）。前端净化器只管
// 展示面，落库原文仍带着脚手架流进记忆蒸馏/知识飞轮/接力注入（下一轮【上一轮系统
// 结论】原样回喂）/API 消费方——数据面卫生必须在解析出口收口。前端净化器保留
//（历史存量报告已落库不可再生，展示面仍需兜底）；纯渲染修饰（专家分段/标题降级/
// Setext 中和）属展示语义，不在本函数重复。
//
// 骨架标记词表与组装侧（scheduler/dialogue.go、dispatch.go、intent.go）及前端
// web/src/utils/reportSanitize.ts 同源——新增骨架标记时三处同步。

import (
	"regexp"
	"strings"
)

// scaffoldMarkers 跨轮/调度脚手架段标识（与前端 SCAFFOLD_MARKERS 同表）。
var scaffoldMarkers = []string{
	"【历史对话】",
	"【上一轮系统结论】",
	"【本轮用户输入】",
	"【症状标准化】",
	"【调度上下文】",
	"【此前诊断摘要】",
	"【更早结论】",
}

var (
	// system-reminder 块（含未闭合兜底：剥到串尾——闭环缺失时宁多勿漏，骨架不该进报告）。
	reminderBlockRe = regexp.MustCompile(`(?is)<system-reminder[^>]*>.*?</system-reminder>|<system-reminder[^>]*>.*$`)
	// 同段混排截断终点（与前端 markerRe 的 lookahead 边界同语义——Go regexp 无
	// 前瞻，改手写扫描，见 cutInlineMarkers）：空行/markdown 标题行/字段标记行。
	inlineBoundaryRe = regexp.MustCompile(`\n[ \t]*\n|\n#{1,6}[ \t]|\n\*\*`)
	blankParaRe      = regexp.MustCompile(`\n{2,}`)
	multiBlankRe     = regexp.MustCompile(`\n{3,}`)
)

// cutInlineMarkers 同段混排兜底：行内出现骨架标记时，从标记处截到下一空行/标题/
// 字段标记（保留边界本身）或串尾。逐处循环直至无标记。
func cutInlineMarkers(s string) string {
	for {
		idx, mlen := firstScaffoldMarker(s)
		if idx < 0 {
			return s
		}
		rest := s[idx+mlen:]
		end := len(rest)
		if loc := inlineBoundaryRe.FindStringIndex(rest); loc != nil {
			end = loc[0]
		}
		s = s[:idx] + rest[end:]
	}
}

// firstScaffoldMarker 首个骨架标记的位置与长度（无命中返回 -1）。
func firstScaffoldMarker(s string) (int, int) {
	best, bestLen := -1, 0
	for _, m := range scaffoldMarkers {
		if i := strings.Index(s, m); i >= 0 && (best < 0 || i < best) {
			best, bestLen = i, len(m)
		}
	}
	return best, bestLen
}

// sanitizeScaffoldText 单字段净化：reminder 块剔除 → 骨架段按空行分段剔除 →
// 同段混排标记截断 → 残留空行收敛。无脚手架快路径零开销。
func sanitizeScaffoldText(s string) string {
	if s == "" || (!strings.Contains(s, "<system-reminder") && !strings.Contains(s, "【")) {
		return s
	}
	out := reminderBlockRe.ReplaceAllString(s, "")
	if blankParaRe.MatchString(out) {
		paras := blankParaRe.Split(out, -1)
		kept := make([]string, 0, len(paras))
		for _, p := range paras {
			if isScaffoldPara(p) {
				continue
			}
			kept = append(kept, p)
		}
		out = strings.Join(kept, "\n\n")
	} else if isScaffoldPara(out) {
		out = ""
	}
	out = cutInlineMarkers(out)
	out = multiBlankRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// isScaffoldPara 段首（允许前导空白）命中任一骨架标记。
func isScaffoldPara(p string) bool {
	t := strings.TrimLeft(p, " \t\n\r")
	for _, m := range scaffoldMarkers {
		if strings.HasPrefix(t, m) {
			return true
		}
	}
	return false
}

// sanitizeScaffold 报告全字段净化（自由文本面）：symptom/conclusion/impact/risk/
// rollback/summary/domain + evidence snippet/interpretation + needs_followup/
// impact_scopes 列表项。枚举/slug/命令字段（decision/executed_steps/recommendation.
// steps）不动——形态由各自校验面治理，脚手架混入命令属审批闸职责。
func sanitizeScaffold(r *Report) *Report {
	if r == nil {
		return r
	}
	r.AgentName = sanitizeScaffoldText(r.AgentName)
	r.Domain = sanitizeScaffoldText(r.Domain)
	r.Symptom = sanitizeScaffoldText(r.Symptom)
	r.Conclusion = sanitizeScaffoldText(r.Conclusion)
	r.Impact = sanitizeScaffoldText(r.Impact)
	r.Risk = sanitizeScaffoldText(r.Risk)
	r.Rollback = sanitizeScaffoldText(r.Rollback)
	r.Summary = sanitizeScaffoldText(r.Summary)
	for i := range r.Evidence {
		r.Evidence[i].Snippet = sanitizeScaffoldText(r.Evidence[i].Snippet)
		r.Evidence[i].Interpretation = sanitizeScaffoldText(r.Evidence[i].Interpretation)
	}
	r.NeedsFollowup = sanitizeScaffoldList(r.NeedsFollowup)
	r.ImpactScopes = sanitizeScaffoldList(r.ImpactScopes)
	return r
}

// sanitizeScaffoldList 列表项净化：净化后空项剔除（整项都是脚手架的形态）。
func sanitizeScaffoldList(l StringList) StringList {
	if len(l) == 0 {
		return l
	}
	out := make(StringList, 0, len(l))
	for _, item := range l {
		if s := sanitizeScaffoldText(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
