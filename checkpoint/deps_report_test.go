package checkpoint

// 失序**报告口径**的钉：前驱「为什么没满足」的三种情形必须分开说。
//
// 分开的原因是**排查方向不同**：前驱没跑、前驱跑了但自己不可信（连带受害）、
// 前驱 ID 写错（悬空依赖），宿主要查的东西完全不同。混成一句「前驱未完成」，
// 宿主就会照着错方向查——报告在制造噪音，比不报更费时间。

import (
	"strings"
	"testing"

	"github.com/yi-nology/agent-harness/contract"
)

// TestUntrustedReportDistinguishesPredState 失序报告要分清前驱「为什么没满足」。
//
// 三种情形的**处置方向完全不同**，混成一句「前驱未完成」会让宿主照着错方向查：
//   - 前驱压根没完成 ⇒ 去查前驱为什么没跑；
//   - 前驱**记了完成但不可信** ⇒ 前驱自己被上游卡住（本步是连带受害者）；
//   - 悬空依赖 ⇒ 编排图写错了 ID。
func TestUntrustedReportDistinguishesPredState(t *testing.T) {
	cases := []struct {
		name    string
		graph   []StepDef
		done    []string
		want    []string
		notWant []string
	}{
		{
			name:    "前驱没完成",
			graph:   depGraph(isolated("plug-a"), depends("plug-b", "plug-a")),
			done:    []string{"plug-b"},
			want:    []string{"plug-a 未完成"},
			notWant: []string{"虽已完成仍不可信", "悬空依赖"},
		},
		{
			name:    "前驱完成但不可信（连带受害）",
			graph:   depGraph(StepDef{ID: "step-a"}, StepDef{ID: "step-b"}, depends("step-c", "step-b")),
			done:    []string{"step-b", "step-c"},
			want:    []string{"step-b 虽已完成仍不可信"},
			notWant: []string{"step-b 未完成", "悬空依赖"},
		},
		{
			name:    "悬空依赖",
			graph:   depGraph(depends("step-a", "step-ghost")),
			done:    []string{"step-a"},
			want:    []string{"悬空依赖"},
			notWant: []string{"未完成"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := snap("run", 1, contract.StatusRunning, c.done...)
			p := BuildPlan(c.graph, &s)
			var detail string
			for _, a := range p.Anomalies {
				if a.Kind == AnomalyDoneOutOfOrder {
					detail = a.Detail
				}
			}
			if detail == "" {
				t.Fatalf("应报失序，实得 %+v", p.Anomalies)
			}
			for _, w := range c.want {
				if !strings.Contains(detail, w) {
					t.Fatalf("报告应含 %q，实得 %q", w, detail)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(detail, w) {
					t.Fatalf("报告不该含 %q，实得 %q", w, detail)
				}
			}
		})
	}
}
