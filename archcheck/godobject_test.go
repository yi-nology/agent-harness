// godobject_test.go —— god object 判据「区分转发壳与实质方法」的用例（issue #13）。
//
// # 这组用例钉的是什么
//
// 判据改成「阈值只算实质方法」之后，最该验的不是「façade 不再误报」——
// 那是新判据的**目的**，验它等于验「代码跑了我写的逻辑」。最该验的是**反向**：
//
//	真 god object（方法体全是实质逻辑）必须**仍然**判红。
//
// 理由：这次改动给判据加了一条「逃逸口」——形态像壳且够短的方法不计入阈值。
// 逃逸口是所有门禁的通病：加一条宽松规则，最容易出的事是**真违规一起被放过**。
// 「从没见过它触发」的规则等于没有规则，「没见过它放过」的逃逸口等于没有门禁。
//
// 所以下面每组用例都是成对的：**同一批代码，逃逸口开着时绿、关上时红**。
// 只有「开着绿」没有「关上红」的用例，等于没钉住。

package archcheck

import (
	"fmt"
	"strings"
	"testing"
)

// façade 源码：把实现下沉到子包、自己只做转发的一批方法。
//
// 形态取自 issue #13 的实测样本：方法体是一条 `return s.impl.X()` 转发。
func facadeMethods(n int) string {
	var sb strings.Builder
	sb.WriteString("package p\n\ntype T struct{ impl *impl }\n\ntype impl struct{}\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "func (t *T) M%d() error {\n\treturn t.impl.Do%d()\n}\n\n", i, i)
	}
	sb.WriteString("func (i *impl) Do0() error { return nil }\n")
	return sb.String()
}

// substantiveMethods 源码：方法体有多语句、有分支的**实质**方法。
//
// 形态取自 issue #13 对照组：一个方法体动辄十几行、含错误处理与状态更新。
func substantiveMethods(n int) string {
	var sb strings.Builder
	sb.WriteString("package p\n\ntype T struct{ impl *impl }\n\ntype impl struct{}\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `func (t *T) M%d(ctx int) error {
	if ctx <= 0 {
		return fmt.Errorf("bad ctx")
	}
	if err := t.impl.check(ctx); err != nil {
		return fmt.Errorf("check: %%w", err)
	}
	t.impl.state = ctx
	t.impl.count++
	return t.impl.finish(ctx)
}

`, i)
	}
	sb.WriteString("func (i *impl) check(int) error { return nil }\n" +
		"func (i *impl) finish(int) error { return nil }\n")
	return sb.String()
}

// TestGodObjectsFacadeNotCountedAsGodObject façade 的方法总数不计入阈值。
//
// 这条钉的是 issue #13 的**核心主张**：一个已经把实现下沉到子包、自己只做转发的
// 类型，方法数 ≈ 公共 API 表面规模，不是复杂度集中度——拿它判 god object 是
// 「把正确的工作判成错的」。
func TestGodObjectsFacadeNotCountedAsGodObject(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": facadeMethods(30)})
	// 阈值 25：方法总数 30 远超它，但全是转发壳 → 实质方法数 0，不该报。
	got, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("30 个纯转发方法构成的 façade 不得被判 god object（方法数是 API 表面规模，不是复杂度），实得 %v", got)
	}
}

// TestGodObjectsSubstantiveStillBlocked 实质方法仍照旧判红——**逃逸口不许放过真违规**。
//
// 与上一条成对：同一套代码，换成实质方法体就必须红。
// 少了这条，上一条就可能是因为「判据整个失灵」而绿的（假绿）。
func TestGodObjectsSubstantiveStillBlocked(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": substantiveMethods(30)})
	got, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("30 个实质方法构成的真 god object 必须被判超限，实得 %v", got)
	}
	if !strings.Contains(got[0], "0 转发壳 + 30 实质") {
		t.Fatalf("报告必须同时给出转发壳数与实质方法数（原始信号不丢），实得 %q", got[0])
	}
}

// TestGodObjectsControlGroupUnchanged issue #13 的对照组判据不得改变。
//
// 实测样本里三个真 god object 的转发壳占比是 **0%**（47 / 46 / 42 方法，
// 各有 13–20 个 >400 字符的实质方法）——即「区分壳与实质方法」在它们身上
// 不该改变任何结论。这条把该事实钉成用例：对照组的方法数一个都不能少算。
func TestGodObjectsControlGroupUnchanged(t *testing.T) {
	for _, n := range []int{47, 46, 42} {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"pa/a.go": substantiveMethods(n)})
		got, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
		if err != nil {
			t.Fatalf("godObjects 不得报错: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("%d 个实质方法的方法类型必须被判超限，实得 %v", n, got)
		}
		if !strings.Contains(got[0], fmt.Sprintf("0 转发壳 + %d 实质", n)) {
			t.Fatalf("对照组的转发壳占比必须是 0%%（判据未改变），实得 %q", got[0])
		}
	}
}

// TestForwardingShellLimitZeroRestoresLegacyBehaviour 显式写 0 = 关闭区分。
//
// 这是给宿主的**逃生口**：不想要这个区分的宿主（阈值语义要保持原样）写 0 即可，
// 判定逐字退回旧行为——所有方法都算实质方法。
func TestForwardingShellLimitZeroRestoresLegacyBehaviour(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": facadeMethods(30)})
	got, err := godObjects(root, []string{"pa"}, 25, 0)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("关闭区分后 30 个方法必须照旧判超限（旧行为），实得 %v", got)
	}
}

// TestGodObjectsMixedCountsOnlySubstantive 混合体按实质方法数判。
//
// 27 个转发壳 + 3 个实质方法，阈值 25 → 实质 3，不报；阈值 2 → 报。
// 这条钉的是「阈值比的是实质方法数」这个**算术**，而不只是「某种形态不报」。
func TestGodObjectsMixedCountsOnlySubstantive(t *testing.T) {
	src := `package p

type T struct{ impl *impl }

type impl struct{}

func (t *T) F1() error { return t.impl.A() }
func (t *T) F2() error { return t.impl.B() }
func (t *T) F3() error { return t.impl.C() }
func (t *T) F4() error { return t.impl.D() }
func (t *T) F5() error { return t.impl.E() }
func (t *T) F6() error { return t.impl.F() }
func (t *T) F7() error { return t.impl.G() }
func (t *T) F8() error { return t.impl.H() }
func (t *T) F9() error { return t.impl.I() }
func (t *T) F10() error { return t.impl.J() }
func (t *T) F11() error { return t.impl.K() }
func (t *T) F12() error { return t.impl.L() }
func (t *T) F13() error { return t.impl.M() }
func (t *T) F14() error { return t.impl.N() }
func (t *T) F15() error { return t.impl.O() }
func (t *T) F16() error { return t.impl.P() }
func (t *T) F17() error { return t.impl.Q() }
func (t *T) F18() error { return t.impl.R() }
func (t *T) F19() error { return t.impl.S() }
func (t *T) F20() error { return t.impl.T() }
func (t *T) F21() error { return t.impl.U() }
func (t *T) F22() error { return t.impl.V() }
func (t *T) F23() error { return t.impl.W() }
func (t *T) F24() error { return t.impl.X() }
func (t *T) F25() error { return t.impl.Y() }
func (t *T) F26() error { return t.impl.Z() }
func (t *T) F27() error { return t.impl.AA() }

func (t *T) S1(ctx int) error {
	if ctx <= 0 {
		return fmt.Errorf("bad")
	}
	t.impl.state = ctx
	return nil
}
func (t *T) S2(ctx int) error {
	if ctx <= 0 {
		return fmt.Errorf("bad")
	}
	t.impl.state = ctx
	return nil
}
func (t *T) S3(ctx int) error {
	if ctx <= 0 {
		return fmt.Errorf("bad")
	}
	t.impl.state = ctx
	return nil
}

func (i *impl) A() error { return nil }
`
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": src})

	got, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("27 转发壳 + 3 实质在阈值 25 下不得报（实质 3 ≤ 25），实得 %v", got)
	}
	got2, err := godObjects(root, []string{"pa"}, 2, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("同一份代码在阈值 2 下必须报（实质 3 > 2），实得 %v", got2)
	}
	if !strings.Contains(got2[0], "30 个方法 = 27 转发壳 + 3 实质") {
		t.Fatalf("报告须写明总数/壳数/实质数三个数，实得 %q", got2[0])
	}
}

// TestForwardingShellShapes 转发壳的形态判据逐条钉住。
//
// 这组用例是「逃逸口不许放过真违规」的细粒度版本：每种形态都给出
// 「该算壳」或「不该算壳」的一票预期。
func TestForwardingShellShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"单条调用转发", "return t.impl.Do()", true},
		{"带参数的调用转发", "return t.impl.Do(ctx, 1, 2)", true},
		{"访问器（取字段）", "return t.name", true},
		{"访问器（取方法值）", "return t.impl.Do", true},
		{"多语句（赋值 + 返回）", "err := t.impl.Do()\n\treturn err", false},
		{"多语句（校验 + 转发）", "if err := t.check(); err != nil {\n\t\treturn err\n\t}\n\treturn t.impl.Do()", false},
		{"复合字面量（构造而非转发）", "return Info{Name: t.name, ID: t.id}", false},
		{"带 defer", "defer t.close()\n\tt.impl.Do()", false},
		{"起 goroutine", "go t.impl.Do()", false},
		{"空方法体", "", false},
		{"无返回值的多语句", "t.impl.Do()\n\tt.impl.Flush()", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			body := tc.body
			if body != "" {
				body = "\t" + body + "\n"
			}
			src := fmt.Sprintf("package p\n\ntype T struct{ impl *impl; name string }\n\ntype impl struct{}\n\nfunc (t *T) M() error {\n%s}\n", body)
			writeFixture(t, root, map[string]string{"pa/a.go": src})
			// 阈值 0：任何「实质方法」都超限，壳不超限。
			got, err := godObjects(root, []string{"pa"}, 0, defaultForwardingShellMaxChars)
			if err != nil {
				t.Fatalf("godObjects 不得报错: %v", err)
			}
			isShell := len(got) == 0
			if isShell != tc.want {
				t.Fatalf("形态 %q 的转发壳判定 = %v，期望 %v（got=%v）", tc.name, isShell, tc.want, got)
			}
		})
	}
}

// TestEffectiveBodyLenIgnoresComments 注释不计入方法体的「有效长度」。
//
// 一行 300 字符的注释不该让一个方法看起来很重——判据问的是「方法体里真正
// 有多少内容」，不是「文件里占了多少行」。
func TestEffectiveBodyLenIgnoresComments(t *testing.T) {
	src := `package p

type T struct{ impl *impl }

type impl struct{}

// 这是一段很长的说明性注释，用来把方法体的「原始长度」撑大，
// 但它不是内容——判据量的是内容长度，不是排版长度。
func (t *T) M() error {
	// 行内注释
	return t.impl.Do()
}
`
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": src})
	got, err := godObjects(root, []string{"pa"}, 0, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("带长注释的转发方法仍应算壳（注释不是内容），实得 %v", got)
	}
}

// TestLongSingleCallIsNotShell 超长的单条调用**不算**壳。
//
// 单条调用不等于转发——`return compute(a, b, c, ...)` 写得很长时，它是实质逻辑
// 恰好只有一条语句。误判成壳就等于给真 god object 开了一个后门。
func TestLongSingleCallIsNotShell(t *testing.T) {
	long := strings.Repeat("x", 200)
	src := fmt.Sprintf("package p\n\ntype T struct{}\n\nfunc (t *T) M() int {\n\treturn compute(%s)\n}\n", long)
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": src})
	got, err := godObjects(root, []string{"pa"}, 0, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("超长的单条调用必须算实质方法（否则真 god object 有后门），实得 %v", got)
	}
}

// TestRecvTypeNameForms 接收者类型名的各种形态。
//
// 取错接收者名 = 把两个类型并成一个数 = 判出的不是 god object 是巧合。
//
// 两条既有语义在这里被钉住：
//
//  1. **多接收者按类型取，不按变量名取**：`func (a, b *Plain) E()` 归 Plain。
//     旧实现按正则切接收者列表，`fields[1]` 取到的是变量名「b」——于是这个方法
//     被归到一个名叫「b」的不存在类型上，Plain 的方法数少算一个。
//  2. **泛型实参被剥掉**：`Manager[int]` 与 `Manager[string]` 归到同一个
//     「Manager」。这是 2026-10-20 之前的既有语义，本次**刻意不动**——
//     改聚合键是另一件事（见 issue #14），不该混在「区分转发壳」这次改动里，
//     混进来会让 diff 难审、也让「本次只改了什么」说不清。
//     它的影响面：只有**按实例特化**的方法（`func (m *Manager[int]) FromInt()`）
//     会被算到基类型头上；泛型类型自身的方法（`func (m *Map[K,V]) Get()`）每个
//     FuncDecl 只数一次，无偏差。
func TestRecvTypeNameForms(t *testing.T) {
	src := `package p

type Plain struct{}
type Manager[T any] struct{}

func (p *Plain) A() {}
func (p Plain) B() {}
func (m *Manager[int]) C() {}
func (m Manager[string]) D() {}
func (a, b *Plain) E() {}
func (m *Manager[map[string]int]) F() {}
`
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": src})
	// 阈值 0：每个类型都应被单独点名。Plain 有 A/B/E 三个方法 → 一个条目；
	// Manager 按既有语义并成一个条目，含 C/D/F 三个方法。
	got, err := godObjects(root, []string{"pa"}, 0, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 Plain / Manager 各自成条，实得 %v", got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"pa.Plain", "pa.Manager"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("报告须含类型 %q，实得 %v", want, got)
		}
	}
	// 多接收者必须归到 Plain（不是变量名 b）——Plain 因此是 3 个方法。
	if !strings.Contains(joined, "pa.Plain (3 个方法") {
		t.Fatalf("多接收者 func (a, b *Plain) E() 必须归入 Plain，Plain 应为 3 个方法，实得 %v", got)
	}
}

// TestGodObjectsParseErrorIsLoud 解析失败必须响亮失败，不能默默跳过。
//
// 跳过 = 该文件的方法数没被计入 = 那个类型的方法数被低估 = 门禁对它隐形。
// 这正是「门禁变绿」最贵的形态，与 policy 解析失败同一条纪律。
func TestGodObjectsParseErrorIsLoud(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": "package p\n\nfunc (t *T) M( {\n"})
	_, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
	if err == nil {
		t.Fatal("解析失败必须返回错误（门禁给不出结论时不能装作通过）")
	}
	if !strings.Contains(err.Error(), "pa/a.go") {
		t.Fatalf("错误须点名出问题的文件，实得 %v", err)
	}
}

// TestThresholdForwardingShellLimitDefault 未声明时用缺省值，显式 0 才关闭。
//
// 判据面（policy）的这条语义是给宿主看的：老 policy 不写这个字段 = 缺省生效，
// 不需要为了不被新判据影响而改 policy。
func TestThresholdForwardingShellLimitDefault(t *testing.T) {
	var th Threshold
	if got := th.forwardingShellLimit(); got != defaultForwardingShellMaxChars {
		t.Fatalf("未声明时的缺省值 = %d，期望 %d", got, defaultForwardingShellMaxChars)
	}
	zero := 0
	th.ForwardingShellMaxChars = &zero
	if got := th.forwardingShellLimit(); got != 0 {
		t.Fatalf("显式 0 必须关闭该区分，实得 %d", got)
	}
}
