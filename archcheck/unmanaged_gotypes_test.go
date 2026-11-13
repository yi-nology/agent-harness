// unmanaged_gotypes_test.go —— 类型感知严格档的对照用例（issue #103）。
//
// # 对照的三件事
//
//  1. **盲区命中**：AST 档自述的静态不可溯形态（函数返回值 / 容器元素 /
//     指针经调用流转）在严格档下必须命中——同一份夹具先跑 AST 档（0 命中，
//     正是 issue #103 说的「字段赋值形态仍会漏判」），再跑严格档（全命中）。
//     这个「旧判据 0 / 新判据 N」的对照就是新规则的执行体证明：没有执行体的
//     规则在这组用例下两档都是 0，当场红。
//  2. **不误伤**：宿主自建的同名同型字段不是本仓的逃逸口——严格档锚在
//     types 的具名类型上，不能因为字段叫 AllowUnmanaged 就报。
//  3. **失败形态显式报错**：解析不出结论（无模块、坏 import）必须返回
//     error，绝不当作「无违规」放行——「给不出结论」与「没违规」严格分开。
//
// 夹具是临时目录里的独立小模块（自建 fixture/ptc 包 + 消费方），不依赖网络：
// go/packages 只需解析本模块与 stdlib。
package archcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEscapeFixture 在临时目录摆一个独立小模块：fixture/ptc 是逃逸类型的
// 提供方（形态与本仓 ptc 同构），extra 里的文件进 fixture/app 消费方。
func writeEscapeFixture(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.24\n")
	write("ptc/ptc.go", `package ptc

// Options / Provider / SubprocessOptions 与本仓 ptc 的逃逸面同构。
type Options struct {
	AllowUnmanaged bool
}

type Provider struct {
	AllowUnmanaged bool
}

type SubprocessOptions struct {
	AllowUnmanaged bool
}
`)
	write("app/ptc.go", "package app\n")
	for name, content := range extra {
		write("app/"+name, content)
	}
	return root
}

// TestTypeAwareCatchesAssignmentBlindSpots 盲区命中对照：AST 档 0 / 严格档 3。
func TestTypeAwareCatchesAssignmentBlindSpots(t *testing.T) {
	root := writeEscapeFixture(t, map[string]string{
		"blind.go": `package app

import "fixture/ptc"

func load() ptc.Options                    { return ptc.Options{} }
func loadSlice() []ptc.Options             { return []ptc.Options{{}} }
func loadPtr() *ptc.SubprocessOptions      { return &ptc.SubprocessOptions{} }

// 三种 AST 档判不到的赋值形态：接收方类型经**函数返回值**流转，
// AST 追踪没有类型信息，追不到 opts 是谁。
func blindSpots() {
	x := load()
	x.AllowUnmanaged = true // 函数返回值

	s := loadSlice()
	s[0].AllowUnmanaged = true // 容器元素（切片可寻址；map 元素不可寻址，Go 语法本就不许这么赋）

	p := loadPtr()
	p.AllowUnmanaged = true // 指针经调用返回
}
`,
	})

	// AST 档（字面量 + 赋值追踪）：三种形态全漏——issue #103 的现状核实。
	astVs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatalf("AST 档跑失败: %v", err)
	}
	if len(astVs) != 0 {
		t.Errorf("对照前提不成立：AST 档应漏判全部盲区形态，实得 %d 条", len(astVs))
	}

	// 严格档：三种形态全命中。
	typeVs, checked, err := typeAwareViolations(root)
	if err != nil {
		t.Fatalf("严格档跑失败: %v", err)
	}
	if checked < 2 {
		t.Errorf("严格档应类型检查 app 与 ptc 两个包，实得 %d", checked)
	}
	if len(typeVs) != 3 {
		for _, v := range typeVs {
			t.Logf("  命中: %s:%d %s", v.Path, v.Line, v.Text)
		}
		t.Fatalf("严格档应命中 3 处盲区赋值，实得 %d 处", len(typeVs))
	}
	for _, v := range typeVs {
		if v.Rule != unmanagedEscapeRuleID || v.Kind != "unmanaged_escape" {
			t.Errorf("违规应归未受管逃逸规则（同根同豁免键），实得 rule=%s kind=%s", v.Rule, v.Kind)
		}
		if !strings.Contains(v.Text, "类型感知档") {
			t.Errorf("违规文案应标明判定档位（宿主要知道是哪档抓的）：%s", v.Text)
		}
	}
}

// TestTypeAwareNoFalsePositiveOnSameNameField 不误伤对照：宿主自建的同名
// 同型字段不报；持有 ptc 逃逸类型的包装结构体字段照报（与 AST 档同一语义）。
func TestTypeAwareNoFalsePositiveOnSameNameField(t *testing.T) {
	root := writeEscapeFixture(t, map[string]string{
		"local.go": `package app

import "fixture/ptc"

// Local 是宿主自建的同名同型字段：不是本仓的逃逸口（unmanaged.go 头注同款边界）。
type Local struct {
	AllowUnmanaged bool
}

// Config 持有 ptc 的逃逸类型：对它嵌着字段的赋值就是逃逸（AST 档也判）。
type Config struct {
	O ptc.Options
}

func mixed() {
	l := Local{}
	l.AllowUnmanaged = true // 不报：字段名相同，类型不是 ptc 的

	cfg := Config{}
	cfg.O.AllowUnmanaged = true // 报：Config 持有 ptc.Options
}
`,
	})
	typeVs, _, err := typeAwareViolations(root)
	if err != nil {
		t.Fatalf("严格档跑失败: %v", err)
	}
	if len(typeVs) != 1 {
		for _, v := range typeVs {
			t.Logf("  命中: %s:%d %s", v.Path, v.Line, v.Text)
		}
		t.Fatalf("严格档应恰好命中包装结构体那一处，实得 %d 处", len(typeVs))
	}
	if !strings.Contains(typeVs[0].Text, "ptc.Options") {
		t.Errorf("违规文案应点出接收方类型（types 推导的可见性收益）：%s", typeVs[0].Text)
	}
}

// TestTypeAwareDedupWithASTTier 两档重叠形态只报一次：opts 由复合字面量
// 持有、赋值形态两档都看得见——不去重会报两条，豁免登记也得登记两次。
func TestTypeAwareDedupWithASTTier(t *testing.T) {
	root := writeEscapeFixture(t, map[string]string{
		"overlap.go": `package app

import "fixture/ptc"

func overlap() {
	opts := ptc.Options{}
	opts.AllowUnmanaged = true // AST 档与严格档都命中
}
`,
	})
	astVs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatalf("AST 档跑失败: %v", err)
	}
	typeVs, _, err := typeAwareViolations(root)
	if err != nil {
		t.Fatalf("严格档跑失败: %v", err)
	}
	if len(astVs) != 1 || len(typeVs) != 1 {
		t.Fatalf("对照前提不成立：两档各应命中 1 条，实得 AST=%d types=%d", len(astVs), len(typeVs))
	}
	if got := dedupUnmanagedEscape(append(astVs, typeVs...)); len(got) != 1 {
		t.Errorf("去重后应剩 1 条（同一行同一规则），实得 %d 条", len(got))
	}
}

// TestTypeAwareResolutionFailureErrors 解析失败必须显式报错（两种形态：
// 无模块的散文件目录；import 解析不了的模块）。「给不出结论」不是「无违规」。
func TestTypeAwareResolutionFailureErrors(t *testing.T) {
	t.Run("无模块目录", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "loose.go"), []byte("package loose\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := typeAwareViolations(root)
		if err == nil {
			t.Fatal("解析失败必须返回 error，不得当作无违规放行")
		}
	})
	t.Run("import 解析不了", func(t *testing.T) {
		root := writeEscapeFixture(t, map[string]string{
			"broken.go": `package app

import "example.com/no-such/module"

func use() { _ = module.Name }
`,
		})
		_, _, err := typeAwareViolations(root)
		if err == nil {
			t.Fatal("坏 import 的类型解析失败必须返回 error，不得当作无违规放行")
		}
		if !strings.Contains(err.Error(), "解析失败") {
			t.Errorf("报错应指明是类型解析失败（与「有违规」区分）：%v", err)
		}
	})
}
