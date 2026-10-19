package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 报告协议类型契约：LLM 输出的结构面（StringList/Evidence/Rec/Chart/Report）。

type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		// 元素可能是对象/数字/布尔（模型常把步骤写成 {"step":1,"description":...}）——
		// 非字符串元素规整为文本（对象压缩为 JSON 文本，数据不丢）。
		var out []any
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return err
		}
		list := make([]string, 0, len(out))
		for _, item := range out {
			switch t := item.(type) {
			case string:
				list = append(list, t)
			case bool:
				list = append(list, fmt.Sprintf("%t", t))
			case float64:
				list = append(list, strconv.FormatFloat(t, 'f', -1, 64))
			default:
				b, _ := json.Marshal(t)
				list = append(list, string(b))
			}
		}
		*s = list
		return nil
	}
	var single string
	if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
		return err
	}
	if single == "" {
		*s = nil
		return nil
	}
	*s = []string{single}
	return nil
}

func (s StringList) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(s))
}

// Std 转标准 []string（strings.Join 等调用点用）。
func (s StringList) Std() []string { return []string(s) }

// Evidence 蒸馏后的单项证据：发现 + 关键行（snippet ≤200 且须含具体数据点）。
// 模型常把工具名写在 type/item/name/source 键下——解码时回退到 tool。
// ID 证据编号（批次一百三十七，结论可回溯面）：`<agent>#E<n>`，ParseReport 侧确定性
// 挂号（已有值不覆盖）；verdicts[].evidence_ids 引用它，聚合/合并只追加不重排，编号
// 全程稳定。
type Evidence struct {
	Tool           string `json:"tool"`
	Snippet        string `json:"snippet"`
	Interpretation string `json:"interpretation"`
	ID             string `json:"id,omitempty"`
}

func (e *Evidence) UnmarshalJSON(data []byte) error {
	var raw struct {
		Tool           *string `json:"tool"`
		Type           *string `json:"type"`
		Item           *string `json:"item"`
		Name           *string `json:"name"`
		Source         *string `json:"source"`
		Snippet        *string `json:"snippet"`
		Interpretation *string `json:"interpretation"`
		ID             *string `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, p := range []*string{raw.Tool, raw.Type, raw.Item, raw.Name, raw.Source} {
		if p != nil && *p != "" {
			e.Tool = *p
			break
		}
	}
	if raw.Snippet != nil {
		e.Snippet = *raw.Snippet
	}
	if raw.Interpretation != nil {
		e.Interpretation = *raw.Interpretation
	}
	if raw.ID != nil {
		e.ID = strings.TrimSpace(*raw.ID)
	}
	return nil
}

// StringMap 容错字符串参数表（change_params 专用）：编目参数声明 string_list 值
// 逗号分隔，但 LLM 偶发把数值/布尔形态写进值位——标准 unmarshal 对
// map[string]string 硬失败会炸掉整份报告解析（ErrNotReport 降级读 markdown 主体，
// 比单工具失败更疼；http_query per_page:20 同型实弹在案后对本协议面巡检补齐）。
// 数值/布尔 coerce 为文本，null/数组/对象丢键不炸；Marshal 形态与普通 map 一致。
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*m = nil
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(StringMap, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(t)
		}
	}
	*m = out
	return nil
}

// Rec recommendation 子结构（处方）。批次九十四起支持编目形态：change_ref+
// change_params 引用已装包的变更块（<包>/changes/<slug>.yaml），平台渲染模板得
// 最终 steps——LLM 只给参数不给命令本体；自由 steps（未编目）仍合法，审批卡
// 标注「未编目」，站点可用 approval.curated_only 收紧为仅编目可执行。
type Rec struct {
	Action            string     `json:"action"` // kill_and_restart|tune|none
	Steps             StringList `json:"steps"`
	RequiresExecution bool       `json:"requires_execution"`
	RequiresApproval  bool       `json:"requires_approval"`
	ChangeRef         string     `json:"change_ref,omitempty"`    // 编目变更块 slug（可选）
	ChangeParams      StringMap  `json:"change_params,omitempty"` // 编目参数（string_list 值逗号分隔；容错解码见 StringMap）
	CuratedRisk       int        `json:"curated_risk,omitempty"`  // 编目声明风险档 1-4（引擎基线下限；LLM 评估只升不降）
}

// 合议三值裁决（verdict-judge 专属；spec 2026-09-15-verdict-judge-design §6）。
const (
	VerdictAdopt   = "adopt"
	VerdictDispute = "dispute"
	VerdictReject  = "reject"
)

var stanceAll = map[string]bool{VerdictAdopt: true, VerdictDispute: true, VerdictReject: true}

// VerdictItem 合议对单份专家报告的裁决。
type VerdictItem struct {
	Agent  string `json:"agent"`
	Stance string `json:"stance"` // adopt|dispute|reject
	Reason string `json:"reason,omitempty"`
	// EvidenceIDs 裁决依据的证据编号（批次一百三十七）：引用所依据报告的 evidence.id
	//（形如 specialists/memory-analysis#E2）——「引用证据说理」从 prompt 软要求升级为
	// 结构化引用通道；容忍单字符串/数组双形态（StringList 宽容解码），引用不存在的
	// 编号由引擎侧合并后校验（CheckEvidenceRefs），只在留痕面告警不拦截。
	EvidenceIDs StringList `json:"evidence_ids,omitempty"`
}

// Verdicts 宽容解码（仿 ChartSpec）：stance 非法或 agent 为空的条目丢弃，绝不影响整报解析。
type Verdicts []VerdictItem

func (v *Verdicts) UnmarshalJSON(data []byte) error {
	var raw []VerdictItem
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // 形态不是数组等畸形：整体放弃，不炸报告
	}
	out := make(Verdicts, 0, len(raw))
	for _, it := range raw {
		if it.Agent == "" {
			continue
		}
		// stance 归一：大小写与空白收敛到白名单形态。
		//
		// 旧实现用 `!stanceAll[it.Stance]` 精确匹配，于是模型写 "Adopt" / "ADOPT"
		// 时条目被**静默丢弃**——裁决确实产生了（模型写了 agent、stance、reason），
		// 系统却当它没写过。合议记录少一条，而**没有任何信号**（不是 warn、不是
		// 报错，是看不见的丢）。
		//
		// 静默丢比整份拒更贵：整份拒至少留痕（宿主降级读 markdown 主体），
		// 而这里连留痕都没有——事后无法区分「模型没给裁决」与「裁决被大小写吃掉」。
		// 与同包的 decision / action / confidence 归一是同一类错（写法差异不该
		// 改变语义），那里已各修过一次。
		st := strings.ToLower(strings.TrimSpace(it.Stance))
		if !stanceAll[st] {
			continue
		}
		it.Stance = st
		out = append(out, it)
	}
	*v = out
	return nil
}

// Followup 结构化接力指令（批次一百二十七）：谁（By）该做什么（What）、为什么（Why）。
// 与 NeedsFollowup 字符串列表双通道过渡——引擎侧新增接力语义走本类型（注入视图/模板
// 变量 {{followups}} 消费），旧字符串字段保留供存量前端渲染；LLM 输出协议不含本字段
// （防线段字段清单不收、repair 层不游走），由引擎侧（合议否决/链终止/聚合透传）填写。
// 前端跟进作栏后撤 needs_followup 字符串双写。
type Followup struct {
	By   string `json:"by,omitempty"`
	What string `json:"what"`
	Why  string `json:"why,omitempty"`
}

// StepMeta 环节执行元数据（批次一百二十七，引擎回填非 LLM 输出）：环节耗时/工具调用
// 次数/LLM 轮次——信封级质量信号，注入视图与聚合面可引用，与报告内容分离（对标 ZCode
// AgentCompletedOutput 的 usage/耗时字段）。报告落库随 meta 序列化；LLM 产出不经此字段。
type StepMeta struct {
	DurationMS int64 `json:"duration_ms,omitempty"`
	ToolCalls  int   `json:"tool_calls,omitempty"`
	Iterations int   `json:"iterations,omitempty"`
}

// ExploreGap 勘探缺口（批次二百零八 GE2 迭代勘探，spec
// 2026-10-07-promotion-channel-generalexec-design.md §7）：综合环节自判证据不足时输出
// 的续轮勘探缺口——aspect=方向名（进环节标签「勘探·<aspect>」），query=建议勘探问题。
// 仅通用轨评估流在 generalist.iterative_explore 开（缺省关）时消费；其余报告携带本
// 字段=无消费方零影响。
type ExploreGap struct {
	Aspect string `json:"aspect"`
	Query  string `json:"query,omitempty"`
}

// ReportProfile 报告形态档（批次二百一十七）：同底座分形态——protocol.Report 契约、
// 落库双写、流式分片与受众投影全轨共用一份，仅渲染大纲按档切换（单点在
// report_render.go）。diagnosis=诊断大纲（现役缺省；零值同义，存量报告零漂移）；
// answer=通用轨直答大纲（结论先行，不硬撑诊断专属面）。确定性由 flow 侧设置——
// LLM 输出协议不含本字段（防线段字段清单不收），与 Followups/Meta 同属引擎侧填写。
type ReportProfile string

const (
	ProfileDiagnosis ReportProfile = "diagnosis"
	ProfileAnswer    ReportProfile = "answer"
)

// Report 统一报告协议（所有专业/流程/治理层 agent 的最终消息）。
type Report struct {
	AgentName      string      `json:"agent_name"`
	Domain         string      `json:"domain"`
	Symptom        string      `json:"symptom"` // 首句须含「检测对象：」标注
	Conclusion     string      `json:"conclusion"`
	Evidence       []Evidence  `json:"evidence,omitempty"`
	Charts         []ChartSpec `json:"charts,omitempty"` // 可视化附件（占比→pie、时序→line、对比→bar、部署/依赖→topo、演进/时间线→timeline），畸形由 ChartSpec 容错
	Impact         string      `json:"impact,omitempty"`
	ImpactScopes   StringList  `json:"impact_scopes,omitempty"` // 受影响对象清单（批次一百三十七，结构化补缺：host/服务/pod 级；聚合去重合并）
	Severity       string      `json:"severity,omitempty"`      // 严重度 info|low|medium|high|critical（批次一百三十七；聚合取最高、dispute 钳 ≤medium；空=未评估）
	Recommendation *Rec        `json:"recommendation,omitempty"`
	RequestType    string      `json:"request_type,omitempty"` // 细粒度动作类别（风险分级用）
	Risk           string      `json:"risk,omitempty"`
	Rollback       string      `json:"rollback,omitempty"`
	RollbackSteps  StringList  `json:"rollback_steps,omitempty"`
	NeedsFollowup  StringList  `json:"needs_followup,omitempty"`
	Confidence     string      `json:"confidence"`          // high|medium|low
	Verdicts       Verdicts    `json:"verdicts,omitempty"`  // 合议裁决（workflow/verdict-judge 专属）
	Followups      []Followup  `json:"followups,omitempty"` // 结构化接力指令（批次一百二十七；引擎侧填写，非 LLM 输出字段）
	Meta           *StepMeta   `json:"meta,omitempty"`      // 环节执行元数据（批次一百二十七；引擎回填，非 LLM 输出字段）
	// ExploreGaps 勘探缺口（批次二百零八）：GE2 迭代勘探的综合环节输出通道——LLM 在
	// iterative_explore 开时按提示词输出，引擎按缺口发起受限续轮扇出。与 Followups 同属
	// 「特定流专属通道」，其余流出现即无消费方。
	ExploreGaps []ExploreGap `json:"explore_gaps,omitempty"`
	// Profile 报告形态档（批次二百一十七，引擎侧确定性填写，非 LLM 输出字段）：flow
	// 按轨设置，空=diagnosis 存量兼容。随 FinalReportJSON 落库，handler 受众投影
	// （view=summary 重渲染）因此自动按档分支，消费面零改动。
	Profile       ReportProfile `json:"profile,omitempty"`
	Summary       string        `json:"summary"`
	Scope         string        `json:"scope,omitempty"`    // in|out（kyaiops 扩展字段）
	Redirect      string        `json:"redirect,omitempty"` // scope=out 时的目标 slug，仅一层
	Decision      string        `json:"decision,omitempty"` // pending_approval|approved|rejected
	ExecutedSteps StringList    `json:"executed_steps,omitempty"`
	// AwaitingUser 待确认态专属标记（批次三百三十四，调度器 clarify 分支持位，非 LLM
	// 输出字段）：反问轮的回答不是诊断结论——渲染器以「待确认」替代「结论」标签，
	// 消除「要诊断哪个集群？」落 **结论** 体的生硬文案（可点选卡照常承担交互）。
	AwaitingUser bool `json:"awaiting_user,omitempty"`
	// Guidance 对话直答标记（引擎侧确定性填写，非 LLM 输出字段）：置位表示本条是
	// 调度引导/元问题/寒暄一类的对话回复而非诊断报告，markdown 走轻装视图
	// （不出 agent 标题与置信度）。内核不依赖任何命名约定判定该形态——判定依据
	// 由宿主通过本字段或 SetGuidanceAgentNames 注册，二选一。
	Guidance bool `json:"guidance,omitempty"`
}

var (
	fenceRe = regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)```\\s*$")
	// reportKeyRe 形如统一报告的快速判定（与 kyaiops-filter.js 同判据）。
	reportKeyRe = regexp.MustCompile(`"(agent_name|domain|conclusion|recommendation|confidence)"`)

	actionWhitelist = map[string]bool{ActionKillAndRestart: true, ActionTune: true, ActionNone: true}
	confidenceAll   = map[string]bool{ConfidenceHigh: true, ConfidenceMedium: true, ConfidenceLow: true}
	decisionAll     = map[string]bool{DecisionPending: true, DecisionApproved: true, DecisionRejected: true}
	scopeAll        = map[string]bool{"in": true, "out": true}

	// decisionNoneSynonyms 已删（2026-09-25）：normalizeDecision 对未知值一律归"无决策"，
	// 逐词收录是打地鼠（实弹：target_scope_mismatch_no_action/redirect 两轮各一新词形）。
	// 历史词族（hold/info/escalate/platform_mismatch/host_unreachable 等）自动被新语义覆盖。

	// slugRe executed_steps 须为纯 slug（禁 ✓/✗/(命中) 等符号标记——AGENTS.md 协议规则）。
	// 命名空间白名单含 imports（包专家，批次三百三十三 kylin-insight 实弹误报：合法
	// imports/kylin-insight-analyst 被标非 slug——包机制上线以来 imports 族一直误报）；
	// 段间允许嵌套（workflow/custom/<name> NL 建链）；ops-scheduler 为无命名空间单例。
	slugRe = regexp.MustCompile(`^((workflow|specialists|platform|compliance|imports)/[\w-]+(/[\w-]+)*|ops-scheduler)$`)
)

// IsConfidenceValid 合议触发门用（scheduler 层过滤无效报告）。
func IsConfidenceValid(c string) bool { return confidenceAll[c] }

// rawRequiresApproval 从归一化前的 recommendation 原始 map 判定 requires_approval 真值
// （bool / 字符串 "true" 双形态；结构缺失或不可判 → false）。decision 归一在字段游走期
// 执行，map 迭代序不定，兄弟字段必须自带解析、不依赖先后的归一结果。
func rawRequiresApproval(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch t := m["requires_approval"].(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}
