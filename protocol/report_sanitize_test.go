package protocol

import "testing"

// 实弹形态（sess-1013-d58e4c6v 链终报「现象」字段）：symptom 前缀正文后整段
// system-reminder 跨轮上下文块被模型复述——净化后只留正文前缀。
func TestSanitizeScaffoldReminderInline(t *testing.T) {
	raw := `{"agent_name":"a","domain":"d","symptom":"检测对象：localhost。<system-reminder source=\"conversation_history\">\n以下为系统注入的跨轮上下文：仅供本轮分析参考，禁止原样复述或摘引进报告/结论。【历史对话】\n用户：rd2 集群后端有哪些 pod\n【上一轮系统结论】\n## imports/kylin-insight-analyst\n**结论**：两副本 Running\n</system-reminder>","conclusion":"c","confidence":"low"}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Symptom != "检测对象：localhost。" {
		t.Fatalf("symptom 未净化: %q", r.Symptom)
	}
}

// 未闭合 reminder：剥到串尾（宁多勿漏）。
func TestSanitizeScaffoldReminderUnclosed(t *testing.T) {
	raw := `{"agent_name":"a","domain":"d","symptom":"检测对象：localhost。<system-reminder source=\"conversation_history\">【历史对话】用户：x","conclusion":"c","confidence":"low"}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Symptom != "检测对象：localhost。" {
		t.Fatalf("未闭合 reminder 未剥净: %q", r.Symptom)
	}
}

// 无 reminder 包裹的裸骨架段：空行分段剔除 + 同段混排从标记截断（保留其后真结论）。
func TestSanitizeScaffoldMarkerParagraphs(t *testing.T) {
	raw := `{"agent_name":"a","domain":"d","symptom":"s","conclusion":"结论正文。\n\n【历史对话】\n用户：上一轮问题\n【上一轮系统结论】\n旧结论全文","confidence":"low"}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Conclusion != "结论正文。" {
		t.Fatalf("骨架段未剔除: %q", r.Conclusion)
	}
	// 同段混排：标记与前缀正文同段，从标记处截到下一字段标记/空行
	raw2 := `{"agent_name":"a","domain":"d","symptom":"s","conclusion":"检测对象：h。【调度上下文】目标=主机\n\n**建议**：观察","confidence":"low"}`
	r2, err := ParseReport(raw2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Conclusion != "检测对象：h。\n\n**建议**：观察" {
		t.Fatalf("同段混排截断异常: %q", r2.Conclusion)
	}
}

// 干净报告零变化（含合法【】强调标记不误杀）。
func TestSanitizeScaffoldNoop(t *testing.T) {
	raw := `{"agent_name":"a","domain":"d","symptom":"检测对象：localhost。服务正常","conclusion":"【重要】无异常","confidence":"high","needs_followup":["观察一周"]}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Symptom != "检测对象：localhost。服务正常" || r.Conclusion != "【重要】无异常" {
		t.Fatalf("干净报告被误改: %q / %q", r.Symptom, r.Conclusion)
	}
	if len(r.NeedsFollowup) != 1 || r.NeedsFollowup[0] != "观察一周" {
		t.Fatalf("needs_followup 被误改: %+v", r.NeedsFollowup)
	}
}

// 列表与证据面：污染项净化、整项脚手架剔除、snippet 净化。
func TestSanitizeScaffoldListsAndEvidence(t *testing.T) {
	raw := `{"agent_name":"a","domain":"d","symptom":"s","conclusion":"c","confidence":"low","needs_followup":["【此前诊断摘要】旧摘要全文","正常跟进项"],"evidence":[{"tool":"t","snippet":"p95=20ms <system-reminder source=\"x\">脚手架</system-reminder>"}]}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.NeedsFollowup) != 1 || r.NeedsFollowup[0] != "正常跟进项" {
		t.Fatalf("needs_followup 净化异常: %+v", r.NeedsFollowup)
	}
	if len(r.Evidence) != 1 || r.Evidence[0].Snippet != "p95=20ms" {
		t.Fatalf("evidence snippet 净化异常: %+v", r.Evidence)
	}
}
