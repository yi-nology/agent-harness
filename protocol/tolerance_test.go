package protocol

// 容错解码路径的定向覆盖（StringList/Evidence/walkNormalize/coerceString）。

import (
	"encoding/json"
	"regexp"
	"testing"
)

// C1 StringList 解码全形态：null/[]/[str]/[obj]/[num+bool]/非法。
func TestStringListAllForms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`null`, nil},
		{`[]`, []string{}},
		{`"single"`, []string{"single"}},
		{`""`, nil},
		{`["a","b"]`, []string{"a", "b"}},
		{`[1,true]`, []string{"1", "true"}},
		{`[{"step":1,"description":"清理"}]`, []string{`{"description":"清理","step":1}`}},
	}
	for _, c := range cases {
		var s StringList
		if err := json.Unmarshal([]byte(c.in), &s); err != nil {
			t.Errorf("解码 %s 失败: %v", c.in, err)
			continue
		}
		if len(s) != len(c.want) {
			t.Errorf("%s → %v, 期望长度 %d", c.in, s, len(c.want))
		}
	}
	var bad StringList
	if err := bad.UnmarshalJSON([]byte(`[nope`)); err == nil {
		t.Error("非法 JSON 应报错")
	}
}

// C2 StringList 编码：nil → []；非 nil → 数组；Std 转换。
func TestStringListMarshal(t *testing.T) {
	var nilList StringList
	out, _ := json.Marshal(nilList)
	if string(out) != "[]" {
		t.Fatalf("nil 应编码为 []: %s", out)
	}
	l := StringList{"a"}
	out, _ = json.Marshal(l)
	if string(out) != `["a"]` {
		t.Fatalf("编码异常: %s", out)
	}
	if l.Std()[0] != "a" {
		t.Fatal("Std 异常")
	}
}

// C3 Evidence 键名回退链：tool > type > item > name > source。
func TestEvidenceKeyFallback(t *testing.T) {
	var e Evidence
	if err := json.Unmarshal([]byte(`{"type":"perf","snippet":"load=9","interpretation":"i"}`), &e); err != nil {
		t.Fatal(err)
	}
	if e.Tool != "perf" {
		t.Fatalf("type 回退失败: %q", e.Tool)
	}
	var e2 Evidence
	_ = e2
	var e3 Evidence
	if err := json.Unmarshal([]byte(`{"item":"proc","snippet":"s"}`), &e3); err != nil {
		t.Fatal(err)
	}
	if e3.Tool != "proc" {
		t.Fatalf("item 回退失败: %q", e3.Tool)
	}
}

// C4 walkNormalize 深层路径：evidence 项的 bool/数字字段、recommendation.action 归一、
// scope 对象形态、decision 对象形态。
func TestWalkNormalizeDeep(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"low","conclusion":"c","summary":"s","scope":{"host":"kylin"},` +
		`"decision":{"state":"none"},"evidence":[{"tool":true,"snippet":123,"interpretation":"采不到"}],` +
		`"recommendation":{"action":"tune","steps":["a"],"requires_execution":"true","requires_approval":1}}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("深层归一化失败: %v", err)
	}
	if r.Evidence[0].Tool != "true" || r.Evidence[0].Snippet != "123" {
		t.Fatalf("evidence 标量未归一: %+v", r.Evidence[0])
	}
	if r.Scope != "in" {
		t.Fatalf("scope 对象应规整为 in: %q", r.Scope)
	}
	if r.Decision != "" {
		t.Fatalf("decision 对象应删除: %q", r.Decision)
	}
	if r.Recommendation.RequiresExecution != true || r.Recommendation.RequiresApproval != true {
		t.Fatalf("布尔字段字符串/数字未归一: %+v", r.Recommendation)
	}
}

// C5 决策枚举保留：合法 decision 且带审批语义（requires_approval=true）时原样保留；
// pending_approval 而无审批语义（实弹 2026-09-10 macOS 只读检查把枚举当"待处理"用）
// 归一为无决策——不撞 Validate 一致性红线，诚实报告不再被整份拦截。
func TestDecisionEnumPreserved(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","scope":"in",` +
		`"recommendation":{"action":"tune","steps":["uptime"],"requires_execution":true,"requires_approval":true},` +
		`"decision":"pending_approval"}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != DecisionPending {
		t.Fatalf("带审批语义的合法枚举应保留: %q", r.Decision)
	}

	noApproval := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","decision":"pending_approval","scope":"in"}`
	r2, err := ParseReport(noApproval)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Decision != "" {
		t.Fatalf("pending_approval 无审批语义应归一为无决策: %q", r2.Decision)
	}
	// requires_approval 为字符串 "true"（标量容错归一前形态）同样视为有审批语义。
	strForm := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","scope":"in",` +
		`"recommendation":{"action":"tune","requires_approval":"true"},"decision":"pending_approval"}`
	r3, err := ParseReport(strForm)
	if err != nil {
		t.Fatal(err)
	}
	if r3.Decision != DecisionPending {
		t.Fatalf("requires_approval 字符串真值应保留枚举: %q", r3.Decision)
	}
}

// C6 ParseReport 空输入/非对象顶层。
func TestParseReportEdge(t *testing.T) {
	if _, err := ParseReport("   "); err == nil {
		t.Fatal("空输入应报错")
	}
	// 数组内嵌报告对象 → 提取通道救回（与散文包裹同语义）。
	if r, err := ParseReport(`[{"agent_name":"X","confidence":"high"}]`); err != nil || r.AgentName != "X" {
		t.Fatalf("数组内嵌报告应被提取: %v", err)
	}
}

// C7 对象形态字符串字段 + 散文前缀 + 栅栏 + 全角尾缀（真实 LLM 实弹形态，2026-09-08 io-analysis 案例）：
// redirect 输出为 {"to":..,"reason":..} 对象 → 扁平化；散文与栅栏走提取通道。
func TestParseReportObjectStringField(t *testing.T) {
	raw := "Now I have sufficient evidence. Let me compile the unified report.\n\n" +
		"```json\n" +
		`{"agent_name":"IO与存储分析Agent","domain":"disk","symptom":"检测对象：localhost。IO排查",` +
		`"conclusion":"满盘+swap高负荷","confidence":"low","summary":"IO侧诱因",` +
		`"redirect":{"to":"内存分析Agent","reason":"OOM主因需内存域补充数据完成闭环"}}` + "\n```）"
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("对象字段+散文+栅栏+尾缀应被宽容解析: %v", err)
	}
	if r.Redirect == "" || !regexp.MustCompile(`内存分析Agent`).MatchString(r.Redirect) || !regexp.MustCompile(`OOM主因`).MatchString(r.Redirect) {
		t.Fatalf("redirect 对象应扁平化为含 to/reason 的文本: %q", r.Redirect)
	}
	// 空对象不救（保持标准报错路径）。
	if _, err := ParseReport(`{"agent_name":"X","confidence":"high","redirect":{}}`); err == nil {
		t.Fatal("redirect 空对象不应被扁平化救回")
	}
}

// C8 实弹容错（2026-09-08）：decision 自造枚举（"hold"）→ 归一为空；全角标点结构位修复。
func TestDecisionSynonymAndFullWidth(t *testing.T) {
	r, err := ParseReport(`{"agent_name":"X","confidence":"low","conclusion":"c","summary":"s","decision":"HOLD"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != "" {
		t.Fatalf("hold 同义词应归一为空: %q", r.Decision)
	}
	// 未知自造枚举 → 一律归空（2026-09-25 结构收敛：打地鼠不可持续，decision 无治理语义；
	// Validate 的白名单检查仅作直构报告后备——见 report_test.go TestDecisionWhitelist）。
	r2, err := ParseReport(`{"agent_name":"X","confidence":"low","conclusion":"c","summary":"s","decision":"banana"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Decision != "" {
		t.Fatalf("未知枚举应归一为空: %q", r2.Decision)
	}
	if _, verr := r2.Validate(); verr != nil {
		t.Fatalf("归一后不应再被拦截: %v", verr)
	}
	// 全角标点（结构位）：，：混入 JSON 骨架 → 修复通道救回；字符串内部全角原样保留。
	raw := `{"agent_name":"X"，"confidence":"low"，"conclusion":"内存不足，需处置"，"summary":"s"}`
	r3, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("全角结构位应被修复: %v", err)
	}
	if r3.Conclusion != "内存不足，需处置" {
		t.Fatalf("字符串内部全角应原样保留: %q", r3.Conclusion)
	}
}

// C9 steps 执行契约（实弹 2026-09-08）：指南/对象形态步骤 → critical 拦截；命令形态放行。
func TestStepsContractViolation(t *testing.T) {
	mk := func(steps ...string) *Report {
		return &Report{AgentName: "X", Confidence: "high", Summary: "s",
			Recommendation: &Rec{Action: ActionTune, Steps: steps, RequiresApproval: true},
			Decision:       DecisionPending}
	}
	if _, err := mk(`{"action":"清理缓存","purpose":"释放空间"}`).Validate(); err == nil {
		t.Fatal("JSON 对象形态步骤应 critical 拦截")
	}
	if _, err := mk("【临时措施】清理缓存目录").Validate(); err == nil {
		t.Fatal("【…】条目形态步骤应 critical 拦截")
	}
	if _, err := mk("du -sh /System/Volumes/Data/* | head -20").Validate(); err != nil {
		t.Fatalf("命令形态步骤应放行: %v", err)
	}
}

// TestParseReportChangeParamsNumericValues（http_query per_page:20 同型巡检补齐）：
// LLM 把编目参数值写成数字/布尔形态——标准 map[string]string unmarshal 硬失败会
// 判整份报告 ErrNotReport（降级读 markdown 主体）。StringMap 容错后应 coerce 收录，
// 非标量丢键不炸。
func TestParseReportChangeParamsNumericValues(t *testing.T) {
	raw := `{"agent_name":"heal-plan","domain":"ops","symptom":"s","conclusion":"c","confidence":"high",` +
		`"recommendation":{"action":"tune","change_ref":"restart-policy","` +
		`change_params":{"service":"nginx","timeout":30,"dry_run":true,"tags":[1,2],"nested":{"a":1}}}}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("数值形态 change_params 不应判非报告: %v", err)
	}
	if r.Recommendation == nil || r.Recommendation.ChangeParams == nil {
		t.Fatal("change_params 应解码收录")
	}
	for k, want := range map[string]string{"service": "nginx", "timeout": "30", "dry_run": "true"} {
		if got := r.Recommendation.ChangeParams[k]; got != want {
			t.Fatalf("change_params[%s] 应 coerce 为 %q，实得 %q", k, want, got)
		}
	}
	for _, k := range []string{"tags", "nested"} {
		if _, ok := r.Recommendation.ChangeParams[k]; ok {
			t.Fatalf("非标量值 %s 应丢键", k)
		}
	}
}
