// host_reuse_test.go —— 宿主仓复用这条门禁的端到端用例。
//
// # 为什么必须是端到端，而不是各测自己那段
//
// 这套用例走的是**宿主会走的完整路径**：自己的 policy 文件 → archcheck.Load →
// archcheck.Run → 拿结果。只测内部函数（buildModuleIndex / allows / extMatch）
// 会漏掉「生产侧派得出、消费侧认不出」的断裂——那正是跨包契约最贵的失效形态。
//
// # 夹具为什么是「假宿主仓」而不是本仓的目录
//
// 夹具是契约的**示例**。示例带本仓的味道，就等于把契约钉死在「本仓的包根长这样」
// 上——而宿主仓的包根是嵌套的（internal/xxx），本仓是平铺的（xxx/）。故这里造一个
// 结构不同的假宿主仓：嵌套包根 + 跨仓依赖 + 陈旧包根 + 未登记包，一样齐。

package archcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHostRepo 造一个假宿主仓：go.mod + policy + 包目录。
//
// module 路径刻意与本仓不同（example.test/host），确保门禁读的是 go.mod 而不是
// 任何写死的假设。
func writeHostRepo(t *testing.T, root string, policy string, files map[string]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.test/host\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "architecture-policy.yaml"),
		[]byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// hostPolicy 是一份「宿主形状」的 policy：嵌套包根 + 一条跨仓依赖。
//
// 两条判定路径在它身上同时生效：
//   - 内部边：service → base（已登记，放行）
//   - 跨仓边：service → 外部 netguard 包面（用 ext: 登记，放行）
const hostPolicy = `version: 1
modules:
  - id: service
    roots: [internal/service]
    requires: [base, "ext:github.com/yi-nology/agent-harness/netguard"]
  - id: base
    roots: [internal/base]
    requires: []
dependency_direction:
  enabled: true
  severity: error
  ignore_dirs: [cmd]
  # 受管外部包面（policy 级）：外仓的哪些包纳入本门禁的判定面。
  ext_prefixes: ["github.com/yi-nology/agent-harness"]
`

// TestHostReuseEndToEnd 宿主仓复用：走完整条链，只该报「未登记包」这一条。
//
// 判据是「该报的报了 + 不该报的一条没报」——只断言前者的话，一个把 ext: 当成
// 未登记 id 硬报的实现也能过（它报得更多）。
func TestHostReuseEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeHostRepo(t, root, hostPolicy, map[string]string{
		"internal/service/svc.go": "package service\n\nimport (\n\t_ \"example.test/host/internal/base\"\n\t_ \"github.com/yi-nology/agent-harness/netguard\"\n)\n",
		"internal/base/base.go":   "package base\n",
		"internal/legacy/old.go":  "package legacy\n",
		"cmd/tool/main.go":        "package main\n",
	})

	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatal(err)
	}

	// 唯一该报的：internal/legacy 这个包目录没有被任何 root 覆盖。
	if len(res.Violations) != 1 {
		t.Fatalf("应恰好报一处（未登记包 internal/legacy），实得 %d 处: %+v", len(res.Violations), res.Violations)
	}
	v := res.Violations[0]
	if v.Kind != "module_unregistered" || v.Path != "internal/legacy" {
		t.Fatalf("实得 %+v，应指向未登记的 internal/legacy 包目录", v)
	}
	if v.Rule != depRuleID {
		t.Fatalf("违规应挂在依赖方向规则下，实得 %q", v.Rule)
	}
	if !res.HasBlocking() {
		t.Fatal("未登记包是阻断级违反，结果必须判为阻断")
	}
}

// TestHostReuseUndeclaredExternalIsViolation 反向：撤掉 ext: 登记后，
// 跨仓依赖必须变成越界违反。
//
// 这条是 ext: 机制的反向验证——若实现把「未登记的跨仓依赖」一律放行（把
// 外部包当成不归本表管），它会全绿，而宿主仓的跨仓边就永远不受管。
func TestHostReuseUndeclaredExternalIsViolation(t *testing.T) {
	root := t.TempDir()
	policy := strings.Replace(hostPolicy,
		`requires: [base, "ext:github.com/yi-nology/agent-harness/netguard"]`,
		`requires: [base]`, 1)
	writeHostRepo(t, root, policy, map[string]string{
		"internal/service/svc.go": "package service\n\nimport (\n\t_ \"example.test/host/internal/base\"\n\t_ \"github.com/yi-nology/agent-harness/netguard\"\n)\n",
		"internal/base/base.go":   "package base\n",
	})

	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatal(err)
	}
	var extViolations []Violation
	for _, v := range res.Violations {
		if v.Kind == "module_dep_ext" {
			extViolations = append(extViolations, v)
		}
	}
	if len(extViolations) != 1 {
		t.Fatalf("未登记跨仓依赖必须报一处越界，实得 %d 处: %+v", len(extViolations), extViolations)
	}
	if !strings.Contains(extViolations[0].Text, "agent-harness/netguard") {
		t.Fatalf("违反说明必须点名受管包面，实得 %q", extViolations[0].Text)
	}
}

// TestHostReuseStaleRootIsRed 陈旧包根必须当天即红。
//
// 这正是 issue 的验收口径：机制包迁出后，policy 里那条 root 指向空目录，
// 而没有任何东西会读它——于是声明与事实的偏离一直累积。接上执行体的第一件事
// 就是让它红。
func TestHostReuseStaleRootIsRed(t *testing.T) {
	root := t.TempDir()
	policy := `version: 1
modules:
  - id: service
    roots: [internal/service]
    requires: []
  - id: moved
    roots: [internal/moved]
    requires: []
dependency_direction:
  enabled: true
  severity: error
  ignore_dirs: [cmd]
`
	writeHostRepo(t, root, policy, map[string]string{
		"internal/service/svc.go": "package service\n",
	})

	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatal(err)
	}
	var stale []Violation
	for _, v := range res.Violations {
		if v.Rule == selfCheckRuleID && strings.Contains(v.Text, "internal/moved") {
			stale = append(stale, v)
		}
	}
	if len(stale) != 1 {
		t.Fatalf("陈旧包根必须报一处，实得 %d 处: %+v", len(stale), res.Violations)
	}
	if !res.HasBlocking() {
		t.Fatal("陈旧包根必须阻断")
	}
}

// TestHostReuseNestedRootsAreNotFalsePositives 嵌套包根的宿主仓不该被误报。
//
// 判据：包目录粒度判定只报**真正没被任何 root 覆盖**的包目录。
// internal/ 本身不是包（它下面没有 .go 文件），internal/service 与 internal/base
// 都已登记——三者都不该出现在违反清单里。
//
// 误报是门禁最致命的失败形态：维护者学会忽略它，规则随即名存实亡。
func TestHostReuseNestedRootsAreNotFalsePositives(t *testing.T) {
	root := t.TempDir()
	writeHostRepo(t, root, hostPolicy, map[string]string{
		"internal/service/svc.go": "package service\n",
		"internal/base/base.go":   "package base\n",
	})

	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Violations) != 0 {
		t.Fatalf("全部包都已登记，不得有任何违反，实得 %+v", res.Violations)
	}
}
