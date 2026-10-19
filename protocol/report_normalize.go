package protocol

import (
	"strings"

	"github.com/oekit/pkg/jsonrepair"
)

// 报告域的**枚举位归一**：模型写枚举值时最爱改大小写、加空白，而每个枚举位
// 背后都挂着一个「判错了要付什么代价」的判据。
//
// # 这一组存在的理由：写法差异不该改变语义
//
// 协议里有五处枚举位此前没有归一，后果各不相同，但共同点是**结局由写法决定、
// 不由内容决定**：
//
//   - action（recommendation 内）："None" → 白名单不命中 → **整份报告被拒**，
//     且「有步骤却免审」的矛盾修复不触发；
//   - confidence："High" → **整份报告被拒**；
//   - verdicts[].stance："Adopt" → 条目**被静默丢弃**（连 warn 都没有，
//     事后无法区分「模型没给裁决」与「裁决被大小写吃掉」）；
//   - scope："OUT" → 被翻成 "in"——**语义反转**：模型说「超出范围、要转人工」，
//     解析器记成「在范围内」，而它是路由信号；
//   - needs_followup：["True"] → 留下一条内容为 "True" 的**乱码跟进项**
//     （跟进项是要人去执行的东西，混进乱码比少一条更坏）。
//
// 与本文件同目录的 decision / requires_approval 早已各修过一次同类错
// （见 normalizeDecision / normalizeBoolField 的注释）——说明这不是「没想过」，
// 是**逐个字段补**的必然漏网。故本文件把五处收成一处口径：
// **ToLower + TrimSpace**。
//
// # 边界：只收拾写法差异，不改判定
//
// 未知取值**不**被映射成某个已知值（那是判据的事，交给 Validate 的白名单），
// 本文件只保证「同一个词的写法差异」不改变语义。

// normalizeAction action 归一：大小写与空白收敛到白名单形态。
//
// # 旧实现的代价：整份诚实的报告被丢
//
// action 此前是**唯一没有归一**的枚举位，于是模型写 "None" / "NONE" / " none " 时：
//
//   - fixRecommendationContradiction 的 `a == "none"` 精确比较不命中 →
//     「有步骤却免审」的矛盾**没被修**（action 原样留着）；
//   - Validate 的 `actionWhitelist["None"]` 不命中 → critical「action 不在白名单」
//     → **整份报告被拒**（宿主降级去读 markdown 主体）。
//
// 而写成 "none" 一切正常：归一为 "tune" + requires_approval=true，收口通过。
//
// **一个 ASCII 大小写之差决定「收口」还是「整份丢弃」。** 这与同文件的
// normalizeBoolField（"TRUE" / "True" / " true " 炸掉整份解析）是同一类错，
// 那里已经修过一次；action 是同一类错里**漏网的最后一个**。
//
// 归一口径与 decision / scope / confidence 一致：ToLower + TrimSpace。
// 未知取值**不**归一（那是真错，交给 Validate 的白名单判 critical），
// 本函数只收拾「同一个词的写法差异」。
func normalizeAction(m map[string]any, k string, v any) {
	s, ok := v.(string)
	if !ok {
		return
	}
	m[k] = strings.ToLower(strings.TrimSpace(s))
}

// normalizeConfidence confidence 归一：大小写与空白收敛到白名单形态。
//
// 同 normalizeAction 的那类错：模型写 "High" / "HIGH" / " high " 时，
// Validate 的 `confidenceAll[...]` 不命中 → critical → **整份报告被拒**。
// 而 "high" 一切正常。confidence 是模型最常写错大小写的枚举位（它只有三个取值，
// 却常被写成 "High"/"HIGH"），而它的判据是 critical——代价是整份丢弃。
func normalizeConfidence(m map[string]any, k string, v any) {
	s, ok := v.(string)
	if !ok {
		return
	}
	m[k] = strings.ToLower(strings.TrimSpace(s))
}

// normalizeNeedsFollowupScalar needs_followup 标量形态是布尔（无目标语义）→
// 空数组（jsonrepair ListKeys 通道产出 ["true"]/["false"] 单元素，在此收敛）。
//
// 大小写与空白一并归一：模型写 ["True"] / [" TRUE "] 是同一个意思，
// 不归一的话它会作为一条**内容为 "True" 的跟进项**留在报告里——
// 而跟进项是要人去执行的东西，混进一条乱码比少一条更坏。
func normalizeNeedsFollowupScalar(m map[string]any) {
	if arr, ok := m["needs_followup"].([]any); ok && len(arr) == 1 {
		if s, isStr := arr[0].(string); isStr {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "false":
				m["needs_followup"] = []any{}
			}
		}
	}
}

// normalizeDecision decision 归一：字符串 → 小写化；白名单（pending_approval/approved/
// rejected）保留；**其余任意值一律归空串（无决策）**。对象/数组形态（模型自造结构）→
// 删除键。2026-09-25 两轮 215 实弹（target_scope_mismatch_no_action/redirect）证明
// 逐词收录同义族是打地鼠——真模型持续自造诚实信号词（目标不匹配/转出/无判读对象…），
// 每个漏网词都让整份专项报告被拦、巡检聚合缺域。decision 本无治理语义（真正闸门在
// action+requires_approval，09-10 已裁定），未知值收敛为无决策零成本；Validate 的
// 白名单检查保留作直构报告后备防线。
func normalizeDecision(m map[string]any, k string, v any) {
	switch v.(type) {
	case map[string]any, []any:
		delete(m, k)
	default:
		if s, ok := jsonrepair.CoerceString(v); ok {
			sd := strings.ToLower(strings.TrimSpace(s))
			switch {
			case decisionAll[sd]:
				m[k] = sd
			case sd == "pending": // 常见缩写（实弹：模型输出 "pending"）→ 规范枚举
				m[k] = DecisionPending
			default:
				// 同义"无决策"词（hold/info/platform_mismatch…）与散文化表述、未知枚举形
				// ASCII 词同待遇：一律无决策（历史词族清单见 2026-09-09/10 实弹记录）。
				m[k] = ""
			}
			// 实弹 2026-09-10（macOS 只读检查）：模型把 pending_approval 当"待处理"用，
			// 而报告无审批语义（requires_approval 缺失）→ 撞 Validate 一致性红线，
			// 整份诚实的检查报告被拦。decision 本无治理语义（真正闸门在
			// action+requires_approval）：pending_approval 而无 requires_approval
			// 真值时归一为无决策，与自造枚举同待遇。Validate 的红线检查保留作
			// 直构报告的后备防线。
			if m[k] == DecisionPending && !rawRequiresApproval(m["recommendation"]) {
				m[k] = ""
			}
		}
	}
}

// normalizeScope scope 只认 in|out：枚举保留；其余任意形态（主机名/对象/数组）→ in
// （模型给出了检测对象）。
//
// # 大小写必须先归一，否则「out」会被翻成「in」
//
// 旧实现只认精确的 "in"/"out"，其余一律落到 "in"。于是模型写 "OUT" / "Out" /
// " out " 时——**它说的是「超出范围、要转人工」，解析器却记成「在范围内」**。
//
// 这不是「宽容解析的副作用」，是**语义反转**：scope 是路由信号（out = 转出/转派），
// 反了之后
//
//   - 报告呈现的口径说「在范围内」，与模型的实际判断相反；
//   - Validate 的「scope=out 时 redirect 必填」不再触发——out 形态缺 redirect
//     会被整份拒（correct），而等价的 "OUT" 形态却畅通无阻（wrong）。
//
// 与 decision / action / confidence / stance 同一类错（写法差异不该改变语义），
// 归一口径一致：ToLower + TrimSpace。
//
// 未知的**字符串**取值仍落 "in"（既定设计：模型给了检测对象）。
// 本函数只收拾「同一个词的写法差异」——"OUT" 是 out，不是未知词。
func normalizeScope(m map[string]any, k string, v any) {
	s, ok := v.(string)
	if !ok {
		m[k] = "in" // 非字符串形态（对象/数组/数字）：模型给了检测对象
		return
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "out":
		m[k] = "out"
	default:
		m[k] = "in"
	}
}
