package protocol

import (
	"fmt"
	"strings"
)

// 报告白名单校验（协议硬闸门）。

func (r *Report) Validate() ([]string, error) {
	var critical []string
	var warns []string

	// —— 必填与枚举 ——
	if strings.TrimSpace(r.Confidence) == "" {
		critical = append(critical, "confidence 缺失")
	} else if !confidenceAll[r.Confidence] {
		critical = append(critical, fmt.Sprintf("confidence 不在白名单: %q", r.Confidence))
	}
	// —— verdicts.stance 白名单兜底（宽容解码只挡条目畸形；直构/手工构造报告在此拦截） ——
	for _, v := range r.Verdicts {
		if !stanceAll[v.Stance] {
			critical = append(critical, fmt.Sprintf("verdicts.stance 非法: %q（agent=%s）", v.Stance, v.Agent))
		}
	}
	if r.Decision != "" && !decisionAll[r.Decision] {
		critical = append(critical, fmt.Sprintf("decision 不在白名单: %q", r.Decision))
	}
	if r.Scope != "" && !scopeAll[r.Scope] {
		critical = append(critical, fmt.Sprintf("scope 不在白名单: %q", r.Scope))
	}
	if r.Scope == "out" && strings.TrimSpace(r.Redirect) == "" {
		critical = append(critical, "scope=out 时 redirect 必填")
	}

	// —— recommendation 白名单与治理铁律 ——
	if rec := r.Recommendation; rec != nil {
		isExec := rec.RequiresExecution || (rec.Action != "" && rec.Action != ActionNone)
		switch {
		case rec.Action != "" && !actionWhitelist[rec.Action]:
			critical = append(critical, fmt.Sprintf("action 不在白名单: %q", rec.Action))
		case isExec && !rec.RequiresApproval:
			critical = append(critical, "执行类动作 requires_approval 必须为 true")
		}
		// curated_risk 声明的是「1-4 风险档（引擎基线下限）」，此前只写在字段注释里
		// 却从不校验：99 或 -1 原样放行，宿主拿它当风险下限时得不到任何信号。
		// 0 因 omitempty 与「未声明」不可区分，故 0 不参与判定。
		if rec.CuratedRisk != 0 && (rec.CuratedRisk < RiskTierMin || rec.CuratedRisk > RiskTierMax) {
			critical = append(critical, fmt.Sprintf(
				"curated_risk 越界: %d（合法区间 %d-%d）",
				rec.CuratedRisk, RiskTierMin, RiskTierMax))
		}
		// —— steps 执行契约（实弹 2026-09-08）：RealRun 逐步骤当 shell 命令执行，
		// 行动指南 JSON / 中文条目形态会"假成功"（exit=0 但什么都没执行）。一律 critical 拦截重试。
		for i, st := range rec.Steps.Std() {
			t := strings.TrimSpace(st)
			if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "【") {
				critical = append(critical,
					fmt.Sprintf("steps[%d] 非可执行命令形态（须为单条 shell 命令字符串，说明性内容放 needs_followup）", i))
			}
		}
	}

	// —— 审批决策一致性 ——
	if r.Decision == DecisionPending {
		if r.Recommendation == nil || !r.Recommendation.RequiresApproval {
			critical = append(critical, "decision=pending_approval 时 requires_approval 必须为 true")
		}
	}

	// —— severity 白名单（批次一百三十七；warn 级放行——severity 是增强面，未知值
	// 渲染按未评估处理，不因它拦整份报告） ——
	if r.Severity != "" && !severityAll[r.Severity] {
		warns = append(warns, fmt.Sprintf("severity 不在白名单: %q（按未评估处理）", r.Severity))
	}

	// —— evidence 蒸馏（warn 级：放行，留痕） ——
	// 长度按字符（rune）计——与 kyaiops-filter.js 的 JS length 同判据，AGENTS.md「snippet ≤200 字符」。
	for i, ev := range r.Evidence {
		if ev.Snippet == "" {
			continue
		}
		rlen := len([]rune(ev.Snippet))
		if rlen > SnippetMax {
			warns = append(warns, fmt.Sprintf(
				"evidence[%d].snippet 超长（%d 字符 > %d），agent 未充分蒸馏，原样保留", i, rlen, SnippetMax))
		}
		if rlen > 20 && !hasConcreteData(ev.Snippet) {
			warns = append(warns, fmt.Sprintf(
				"evidence[%d].snippet 可能缺少具体数据点（无数值/时间戳/错误码/进程名）: %.60q", i, ev.Snippet))
		}
	}

	// —— evidence 编号唯一性（warn 级：放行，留痕） ——
	//
	// 同号的两条证据让「引用证据说理」**失去指向**：verdict 引用 "E1" 时，
	// 消费方无从知道说的是哪一条；而 CheckEvidenceRefs 只查「引用了但不存在」，
	// 同号时该编号确实存在，故它**不会报缺失**——歧于是静默的，比缺失更难查。
	//
	// 引擎自造的编号已保证不撞（AssignEvidenceIDs 避开已被占用的号），
	// 但模型自报的重复编号是它自己的数据：不改写（改了会让指向它的引用失效），
	// 只在留痕面告警。理由同 severity 白名单——引用是增强面，不是数据本体。
	seenEvidenceID := make(map[string]int, len(r.Evidence))
	for i, ev := range r.Evidence {
		id := strings.TrimSpace(ev.ID)
		if id == "" {
			continue
		}
		if first, ok := seenEvidenceID[id]; ok {
			warns = append(warns, fmt.Sprintf(
				"evidence[%d].id %q 与 evidence[%d] 重复（同号引用无从指向）", i, id, first))
			continue
		}
		seenEvidenceID[id] = i
	}

	// —— executed_steps 纯 slug（warn 级） ——
	for _, s := range r.ExecutedSteps {
		if !slugRe.MatchString(s) {
			warns = append(warns, fmt.Sprintf("executed_steps 含非 slug 项（禁符号标记）: %q", s))
		}
	}

	if len(critical) > 0 {
		return warns, fmt.Errorf("%w: %s", ErrViolation, strings.Join(critical, "；"))
	}
	return warns, nil
}

func hasConcreteData(s string) bool {
	for _, re := range snippetQuality {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// flattenSnippet 把证据关键行拍平为单行（模型常把多行 JSON 快照回填进 snippet，
// 原样内插会打断 markdown 列表且形似原始 JSON dump）；协议数据层保持原样（证据保真）。
func flattenSnippet(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Render 用户可见 Markdown（大纲：标题→结论→证据要点→影响范围→方案与下一步→风险与回退→未决项与置信度）。
