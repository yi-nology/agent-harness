package protocol

import (
	"strings"
	"testing"
)

// TestCuratedRiskStringDoesNotKillReport 钉住 curated_risk 的标量容错。
//
// Rec 是本包唯一没有容错 UnmarshalJSON 的结构（对照 StringMap / StringList），
// 而 curated_risk 此前也不在归一化分支里。LLM 在字符串位产出数字（"3"）时，
// 严格 unmarshal 到 int 直接失败 → 三条 tryParse 通道全灭 → 整份报告判成
// ErrNotReport，调用方降级读 markdown 主体。StringMap 的注释把这个失败模式
// 列为「比单工具失败更疼」的一种，同包却漏了兄弟字段。
func TestCuratedRiskStringDoesNotKillReport(t *testing.T) {
	cases := []struct {
		name string
		risk string
		want int
	}{
		{"纯数字串", `"3"`, 3},
		{"带空白", `" 4 "`, 4},
		{"数字本就合法", `3`, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := `{"agent_name":"a","domain":"d","conclusion":"c","confidence":"high",` +
				`"recommendation":{"action":"tune","requires_approval":true,"curated_risk":` + c.risk + `}}`
			r, err := ParseReport(raw)
			if err != nil {
				t.Fatalf("curated_risk=%s 不该炸掉整份报告: %v", c.risk, err)
			}
			if r.Recommendation.CuratedRisk != c.want {
				t.Fatalf("curated_risk=%s 解析为 %d，期望 %d", c.risk, r.Recommendation.CuratedRisk, c.want)
			}
		})
	}
}

// TestBoolFieldCaseTolerance 钉住布尔归一的大小写/空白口径。
//
// normalizeBoolField 原先精确匹配 "true"/"false"，而同包的 rawRequiresApproval
// 用 EqualFold+TrimSpace——同一份代码两套口径。"TRUE" / " true " 既没被归一化、
// 又过不了严格 unmarshal 到 bool，单个字符串的大小写就能炸掉整份报告解析。
func TestBoolFieldCaseTolerance(t *testing.T) {
	for _, v := range []string{`"true"`, `"TRUE"`, `"True"`, `" true "`, `" true"`} {
		raw := `{"agent_name":"a","domain":"d","conclusion":"c","confidence":"high",` +
			`"recommendation":{"action":"tune","requires_execution":` + v + `,"requires_approval":true}}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("requires_execution=%s 不该炸掉整份报告: %v", v, err)
		}
		if !r.Recommendation.RequiresExecution {
			t.Fatalf("requires_execution=%s 应归一为 true", v)
		}
	}
}

// TestCuratedRiskRangeEnforced 钉住文档承诺的 1-4 区间真的被校验。
// 0 不参与判定：字段带 omitempty，0 与「未声明」不可区分。
func TestCuratedRiskRangeEnforced(t *testing.T) {
	mk := func(risk int) *Report {
		return &Report{
			Confidence: ConfidenceHigh,
			Recommendation: &Rec{
				Action: ActionNone, CuratedRisk: risk,
			},
		}
	}
	for _, ok := range []int{0, 1, 2, 3, 4} {
		if _, err := mk(ok).Validate(); err != nil {
			t.Fatalf("curated_risk=%d 在合法区间内（含未声明的 0），不该报错: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 5, 99} {
		if _, err := mk(bad).Validate(); err == nil {
			t.Fatalf("curated_risk=%d 越界却通过了校验——宿主拿它当风险下限会信错值", bad)
		} else if !strings.Contains(err.Error(), "curated_risk") {
			t.Fatalf("curated_risk=%d 的错误信息应指名字段，实得 %q", bad, err.Error())
		}
	}
}
