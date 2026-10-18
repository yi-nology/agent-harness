package protocol

import (
	"strings"
	"sync"
	"testing"
)

// 内核通用性回归（批次 harness 化）：以下用例刻意用**合成数据**，不含任何领域词面。
// 它们钉住的是「判定依据属于宿主、内核只提供机制」这条边界——
// 若将来有人又把具体命名/文案硬编回内核，这些用例会红。

// TestGuidanceByStructuralField 通道一：结构化置位即轻装，不看 AgentName。
func TestGuidanceByStructuralField(t *testing.T) {
	r := &Report{
		AgentName:  "any-name-at-all",
		Profile:    ProfileAnswer,
		Conclusion: "hello",
		Confidence: ConfidenceMedium,
		Guidance:   true,
	}
	out := RenderView(r, ViewFull)
	if strings.HasPrefix(out, "## ") {
		t.Fatalf("引导形态不应出 agent 标题, got: %q", out)
	}
	if strings.Contains(out, "置信度") {
		t.Fatalf("引导形态不应出置信度小节, got: %q", out)
	}
}

// TestGuidanceByRegisteredName 通道二：宿主注册名后按名判定；未注册则不误判。
func TestGuidanceByRegisteredName(t *testing.T) {
	const hostName = "host-defined-guide-voice"
	defer SetGuidanceAgentNames() // 清空，避免污染其他用例

	base := func(name string) *Report {
		return &Report{AgentName: name, Profile: ProfileAnswer, Conclusion: "hi", Confidence: ConfidenceMedium}
	}

	// 未注册：按普通 answer 渲染（出标题）。
	if out := RenderView(base(hostName), ViewFull); !strings.HasPrefix(out, "## ") {
		t.Fatalf("未注册的 agent 名应走常规 answer 渲染, got: %q", out)
	}

	// 注册后：轻装。
	SetGuidanceAgentNames(hostName)
	if !GuidanceAgentRegistered(hostName) {
		t.Fatal("注册后 GuidanceAgentRegistered 应为 true")
	}
	if out := RenderView(base(hostName), ViewFull); strings.HasPrefix(out, "## ") {
		t.Fatalf("已注册的引导名应轻装渲染, got: %q", out)
	}
	// 注册表只影响被注册的名字，其他名字不受牵连（注册不是「全局引导模式」）。
	if out := RenderView(base("unregistered"), ViewFull); !strings.HasPrefix(out, "## ") {
		t.Fatalf("未注册的名字应照常渲染（带标题）, got: %q", out)
	}
	// 清空注册即恢复默认判定。
	SetGuidanceAgentNames()
	if GuidanceAgentRegistered(hostName) {
		t.Fatal("清空注册后 GuidanceAgentRegistered 应为 false")
	}
}

// TestGuidanceRegistrationConcurrent 热更并发安全（装配期调用 + 运行期渲染并发）。
func TestGuidanceRegistrationConcurrent(t *testing.T) {
	defer SetGuidanceAgentNames()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); SetGuidanceAgentNames("g", "h") }(i)
		go func() {
			defer wg.Done()
			r := &Report{AgentName: "g", Profile: ProfileAnswer, Conclusion: "x", Confidence: ConfidenceLow}
			_ = RenderView(r, ViewFull)
		}()
	}
	wg.Wait()
}

// TestDefaultTitleIsNeutral 内核不得内置宿主品牌文案：空标题走中性兜底。
func TestDefaultTitleIsNeutral(t *testing.T) {
	r := &Report{Conclusion: "c", Confidence: ConfidenceHigh, Summary: "s"}
	out := RenderView(r, ViewFull)
	if !strings.Contains(out, defaultReportTitle) {
		t.Fatalf("空 AgentName 应走中性兜底 %q, got: %q", defaultReportTitle, out)
	}
	if strings.Contains(out, "运维") {
		t.Fatalf("内核兜底标题不得含领域/品牌词, got: %q", out)
	}
}

// TestHostSuppliedTitle 宿主可通过 RenderOptions 注入自己的标题文案。
func TestHostSuppliedTitle(t *testing.T) {
	r := &Report{Conclusion: "c", Confidence: ConfidenceHigh, Summary: "s"}
	out := RenderViewWith(r, ViewFull, RenderOptions{}.WithTitle("HOST-BRAND-REPORT"))
	if !strings.Contains(out, "HOST-BRAND-REPORT") {
		t.Fatalf("宿主注入的标题应生效, got: %q", out)
	}
	// 不带 opts 的老入口仍走中性兜底（既有调用零漂移）。
	if strings.Contains(RenderView(r, ViewFull), "HOST-BRAND-REPORT") {
		t.Fatal("RenderView 不应受其他调用的 opts 影响")
	}
}
