package protocol

import "testing"

// engineFieldReport 构造一份携带全部五个引擎侧字段的 JSON（模拟落库后的
// FinalReportJSON：宿主在解析后按自己的判定回填，再序列化落库）。
const engineFieldReport = `{"agent_name":"a","domain":"d","conclusion":"c","confidence":"high",` +
	`"followups":[{"by":"wf/verify","what":"补采","why":"存疑"}],` +
	`"meta":{"duration_ms":1500,"tool_calls":3},` +
	`"profile":"answer","awaiting_user":true,"guidance":true}`

// TestParseReportStripsEngineFields 钉住「外部输入不得携带引擎侧字段」。
//
// 这五个字段在定义处就标注「引擎侧确定性填写 / 非 LLM 输出字段」，且直接决定
// 呈现：profile 切答案视图会丢掉影响范围 / 风险回退 / 合议 / severity 渲染；
// guidance 置位走轻装视图（不出 agent 标题与置信度）。让模型能写它们，等于让它
// 自己决定「哪些治理信息不必呈现」——与本包 fail-closed 的口径相反。
func TestParseReportStripsEngineFields(t *testing.T) {
	r, err := ParseReport(engineFieldReport)
	if err != nil {
		t.Fatalf("解析应成功: %v", err)
	}
	if r.Profile != "" {
		t.Errorf("profile 是引擎侧字段，外部输入不应生效，实得 %q", r.Profile)
	}
	if r.AwaitingUser {
		t.Error("awaiting_user 是引擎侧字段，外部输入不应生效")
	}
	if r.Guidance {
		t.Error("guidance 是引擎侧字段，外部输入不应置位——否则模型能让渲染走轻装视图")
	}
	if len(r.Followups) != 0 {
		t.Errorf("followups 是引擎回填，外部输入不应带出，实得 %v", r.Followups)
	}
	if r.Meta != nil {
		t.Errorf("meta 是引擎回填，外部输入不应带出，实得 %+v", r.Meta)
	}
	// 剥离不能殃及真正的 LLM 输出字段。
	if r.AgentName != "a" || r.Confidence != ConfidenceHigh {
		t.Errorf("剥离应只清引擎字段，LLM 输出字段必须保留: %+v", r)
	}
}

// TestParsePersistedReportKeepsEngineFields 钉住往返路径必须保留引擎字段。
//
// 宿主两处用 ParseReport 回读已落库的 FinalReportJSON（handler 渲染、session
// 元信息）。若这一路径也剥离，历史报告会静默丢掉 followups/meta。
func TestParsePersistedReportKeepsEngineFields(t *testing.T) {
	r, err := ParsePersistedReport(engineFieldReport)
	if err != nil {
		t.Fatalf("往返解析应成功: %v", err)
	}
	if r.Profile != ProfileAnswer {
		t.Errorf("往返解析应保留 profile，实得 %q", r.Profile)
	}
	if !r.AwaitingUser || !r.Guidance {
		t.Errorf("往返解析应保留 awaiting_user/guidance，实得 %v/%v", r.AwaitingUser, r.Guidance)
	}
	if len(r.Followups) != 1 || r.Followups[0].By != "wf/verify" {
		t.Errorf("往返解析应保留 followups，实得 %+v", r.Followups)
	}
	if r.Meta == nil || r.Meta.DurationMS != 1500 || r.Meta.ToolCalls != 3 {
		t.Errorf("往返解析应保留 meta，实得 %+v", r.Meta)
	}
}

// TestParseReportRoundTripIsExplicit 钉住两条路径的差异只在这五个字段上：
// 同一份 JSON，走 ParseReport 得零值、走 ParsePersistedReport 得全值，其余完全一致。
func TestParseReportRoundTripIsExplicit(t *testing.T) {
	untrusted, err := ParseReport(engineFieldReport)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	trusted, err := ParsePersistedReport(engineFieldReport)
	if err != nil {
		t.Fatalf("ParsePersistedReport: %v", err)
	}

	if untrusted.AgentName != trusted.AgentName ||
		untrusted.Domain != trusted.Domain ||
		untrusted.Conclusion != trusted.Conclusion ||
		untrusted.Confidence != trusted.Confidence {
		t.Errorf("两条路径在 LLM 输出字段上应完全一致，实得 %+v vs %+v", untrusted, trusted)
	}
	if trusted.Profile == untrusted.Profile {
		t.Error("两条路径本应在 profile 上不同——否则说明剥离没生效或往返没保留")
	}
}
