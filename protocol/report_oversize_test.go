package protocol

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 构造一个字段合法的小报告，作为超限场景的「头部对象」。
// 夹具刻意用中性合成数据（probe-01 / 合成结论），不带任何领域词面。
const oversizeHeadReport = `{"agent_name":"probe-01","conclusion":"合成结论","confidence":"high"}`

// TestOversizeInputRejectedNotTruncated 反向验证「超限直接判非报告」这条意图。
//
// 这条用例钉的是一个**曾经写成截断、因而完全失效**的上限：
// 截断后的 candidate = 完整对象 + 巨量尾巴，ExtractObject 通道会把对象原样捞出、
// 丢弃其余部分，于是超限输入以 confidence=high「解析成功」放行——
// 既没拦住，也没走到 markRepaired 的降置信兜底（主解析通道就成功了）。
// 方向比不设上限更坏，故用本用例钉死：超限必须返回 ErrNotReport。
func TestOversizeInputRejectedNotTruncated(t *testing.T) {
	tails := []struct {
		name string
		tail func() string
	}{
		{
			name: "非 JSON 垃圾尾巴",
			tail: func() string { return strings.Repeat("\x00\xff\x01<<<NOT-JSON>>>", 200000) },
		},
		{
			name: "纯空白填充",
			tail: func() string { return strings.Repeat(" ", 3<<20) },
		},
		{
			name: "第二个合法对象",
			tail: func() string { return strings.Repeat(oversizeHeadReport, 60000) },
		},
	}

	for _, tc := range tails {
		t.Run(tc.name, func(t *testing.T) {
			raw := oversizeHeadReport + tc.tail()
			if len(raw) <= maxReportInput {
				t.Fatalf("夹具本身没超限：len=%d max=%d", len(raw), maxReportInput)
			}

			// 两个入口共享同一解析本体，信任级别不同但上限判定必须一致。
			for _, entry := range []struct {
				name string
				fn   func(string) (*Report, error)
			}{
				{"ParseLLMReport", ParseLLMReport},
				{"ParseReport", ParseReport},
			} {
				r, err := entry.fn(raw)
				if !errors.Is(err, ErrNotReport) {
					t.Fatalf("%s 超限输入未判非报告：err=%v", entry.name, err)
				}
				if r != nil {
					t.Fatalf("%s 超限输入却返回了报告对象：agent=%q conclusion=%q confidence=%q",
						entry.name, r.AgentName, r.Conclusion, r.Confidence)
				}
			}
		})
	}
}

// TestOversizeBoundaryNotOffByOne 守住边界：恰好等于上限的输入仍须正常解析。
// 上限写错一字节就会出现两种坏结果——把合法报告拒掉，或让上限形同虚设。
func TestOversizeBoundaryNotOffByOne(t *testing.T) {
	raw := oversizeHeadReport + strings.Repeat(" ", maxReportInput-len(oversizeHeadReport))
	if len(raw) != maxReportInput {
		t.Fatalf("夹具长度=%d，应恰为 %d", len(raw), maxReportInput)
	}

	r, err := ParseLLMReport(raw)
	if err != nil {
		t.Fatalf("恰好等于上限的合法报告被拒：%v", err)
	}
	if r.AgentName != "probe-01" || r.Confidence != ConfidenceHigh {
		t.Fatalf("边界输入解析结果不对：agent=%q confidence=%q", r.AgentName, r.Confidence)
	}

	// 再多一个字节就必须拒——证明边界是「> 上限」而不是「>= 上限」。
	if _, err := ParseLLMReport(raw + " "); !errors.Is(err, ErrNotReport) {
		t.Fatalf("超上限一个字节却未判非报告：err=%v", err)
	}
}
