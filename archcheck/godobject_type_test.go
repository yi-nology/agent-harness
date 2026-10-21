package archcheck

import (
	"strings"
	"testing"
)

// 本文件钉住**接收者类型的识别**（issue #14）。
//
// 取错接收者名 = 把两个类型并成一个数 = 判出的不是 god object 是巧合。
//
// # 关键语义：泛型实参**保留**
//
// 按 Go 规范，按实例特化的方法（`func (m *Manager[int]) C()`）**只属于
// `Manager[int]`**，不是 `Manager` 的方法。剥掉实参会把三个不同的类型并成
// 一个计数——两个实例化各挂 20 个特化方法就被判成「Manager 有 40 个方法」，
// 而它其实不胖。**方向是虚高 ⇒ 假红**。
//
// 而**非泛型类型的键必须逐字不变**（`pa.Plain` 仍是 `pa.Plain`）——
// 否则这条变更会波及所有宿主，而不只是泛型用户。

// TestRecvTypeNameForms 接收者类型名的各种形态。
//
// 取错接收者名 = 把两个类型并成一个数 = 判出的不是 god object 是巧合。
//
// 三条语义在这里被钉住：
//
//  1. **多接收者按类型取，不按变量名取**：`func (a, b *Plain) E()` 归 Plain。
//     旧实现按正则切接收者列表，`fields[1]` 取到的是变量名「b」——于是这个方法
//     被归到一个名叫「b」的不存在类型上，Plain 的方法数少算一个。
//  2. **泛型实参保留**：按 Go 规范，按实例特化的方法（`func (m *Manager[int]) C()`）
//     只属于 `Manager[int]`，不是 `Manager` 的方法。剥掉实参会把三个不同的类型
//     并成一个计数——方向是**虚高** ⇒ 假红（issue #14）。
//  3. **非泛型类型的键逐字不变**：`pa.Plain` 仍是 `pa.Plain`——这条必须成立，
//     否则改动会波及所有宿主，而不只是泛型用户。
func TestRecvTypeNameForms(t *testing.T) {
	src := `package p

type Plain struct{}
type Manager[T any] struct{}
type Pair[K, V any] struct{}

func (p *Plain) A() {}
func (p Plain) B() {}
func (m *Manager[int]) C() {}
func (m Manager[string]) D() {}
func (a, b *Plain) E() {}
func (m *Manager[map[string]int]) F() {}
func (p *Pair[string, int]) G() {}
func (p Pair[int, string]) H() {}
`
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": src})
	// 阈值 0：每个类型都应被单独点名。
	//
	// 期望四条（issue #14 的验收口径）：
	//   pa.Plain                     —— 非泛型，键**逐字不变**，A/B/E 三个方法；
	//   pa.Manager[int]              —— C 一个方法；
	//   pa.Manager[string]           —— D 一个方法；
	//   pa.Manager[map[string]int]   —— F 一个方法。
	//
	// **三个实例化各自成条**——它们是三个不同的类型（Go 规范：按实例特化的方法
	// 只属于该实例化），并成一条就是「把两个类型并成一个数」。
	got, err := godObjects(root, []string{"pa"}, 0, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("期望 Plain + 三个 Manager 实例化 + 两个 Pair 实例化各自成条（6 条），实得 %v", got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"pa.Plain",
		"pa.Manager[int]",
		"pa.Manager[string]",
		"pa.Manager[map[string]int]",
		"pa.Pair[string, int]",
		"pa.Pair[int, string]",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("报告须含类型 %q，实得 %v", want, got)
		}
	}
	// 多接收者必须归到 Plain（不是变量名 b）——Plain 因此是 3 个方法。
	if !strings.Contains(joined, "pa.Plain (3 个方法") {
		t.Fatalf("多接收者 func (a, b *Plain) E() 必须归入 Plain，Plain 应为 3 个方法，实得 %v", got)
	}
	// 三个实例化各只挂 1 个特化方法 ⇒ 每个都是 1 个方法（不是并成 3 个）。
	for _, want := range []string{
		"pa.Manager[int] (1 个方法",
		"pa.Manager[string] (1 个方法",
		"pa.Manager[map[string]int] (1 个方法",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("报告须含 %q（各实例化只挂 1 个特化方法），实得 %v", want, got)
		}
	}
}
