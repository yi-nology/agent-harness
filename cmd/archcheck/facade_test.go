// facade_test.go —— 门面包不可变门禁本身的用例。
//
// ## 为什么这道门禁必须自带测试
//
// 它是本仓最复杂的一条判定：走 go/ast 判「函数体是不是只有一条转发语句」，
// 而这条纪律恰恰是结构性的——正则判不出来。于是它有五处容易**静默失效**的判据：
//
//	1. 类型定义漏判（把 `type X = ...` 当成合法，于是真定义也放行）
//	2. 方法漏判（`func (T) M()` 挂类型上，绕开上游的类型演进）
//	3. 多语句漏判（转发函数里顺手加了一行 if —— 这正是「顺手写两行」）
//	4. 非上游调用漏判（调本包自己的函数，字符串完全不含上游特征）
//	5. 非允许 import 漏判（引一个新依赖，转发目标随即不可判定）
//
// 这五条任何一条失效的表现都是**门禁变绿**，不是变红——而门禁变绿意味着
// 「与上游并存的第二份实现」从此无人拦截，正是 docs/OAGENTKIT-OVERLAP.md 警告的事。
// 所以这里把反向验证从「写代码时手工注入一次」固化成每次 CI 都跑的用例。
//
// 反过来也要钉住**合法形态不得被误伤**：门禁最容易的失败不是漏判，而是把真正的
// 转发函数判成违规，于是维护者学会忽略它，规则随之名存实亡。

package main

import (
	"strings"
	"testing"
)

const facadeUpstream = "example.test/up"

func textsOf(vs []violation) string {
	var sb strings.Builder
	for _, v := range vs {
		sb.WriteString(v.text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// ── 合法形态：不得被误伤 ──

// TestFacadeLegalForms 门面包的四种合法形态必须零违规。
//
// 这一半与违规用例同等重要：门禁误伤真转发，会让人学会忽略它，规则随即名存实亡。
func TestFacadeLegalForms(t *testing.T) {
	src := `package dispatch

import (
	up "example.test/up"
	"example.test/sig"
)

// 类型别名
type Guard = up.Guard

// 常量别名（成块声明）
const (
	A = up.A
	B = up.B
)

// 变量别名
var V = up.V

// 单条 return 转发
func NewGuard(src Source) *up.Guard { return up.NewGuard(src) }

// 单条表达式转发
func Reset(g *up.Guard) { up.Reset(g) }

// 签名所需的 allowed_imports：只出现在**签名**里，不参与构造
func Wrap(n int) sig.T { return up.Wrap(n) }
`
	if vs := facadeViolationsFixture(t, src); len(vs) != 0 {
		t.Fatalf("合法门面被判违规:\n%s", textsOf(vs))
	}
}

// ── 五条判定路径逐条钉死 ──

// TestFacadeDetectsTypeDefinition 判据 1：真类型定义必须拒（别名合法，定义不合法）。
func TestFacadeDetectsTypeDefinition(t *testing.T) {
	src := `package dispatch

import up "example.test/up"

type Local struct{ N int }

func New() *Local { return &Local{} }
`
	vs := facadeViolationsFixture(t, src)
	if len(vs) == 0 {
		t.Fatal("真类型定义必须被判违规（判据 1）")
	}
	if !strings.Contains(textsOf(vs), "Local") {
		t.Errorf("违规结论应指名类型 Local，实得:\n%s", textsOf(vs))
	}
}

// TestFacadeDetectsMethod 判据 2：方法必须拒——给类型挂方法会绕开上游的类型演进。
func TestFacadeDetectsMethod(t *testing.T) {
	src := `package dispatch

import up "example.test/up"

type G = up.Guard

func (g *G) Extra() int { return 1 }
`
	vs := facadeViolationsFixture(t, src)
	if !strings.Contains(textsOf(vs), "方法") {
		t.Fatalf("方法必须被判违规（判据 2），实得:\n%s", textsOf(vs))
	}
}

// TestFacadeDetectsMultiStatement 判据 3：多语句函数体必须拒。
//
// 这是整条纪律的核心——「加真逻辑」三选一里最容易顺手犯的那一条：转发函数里
// 加一行判断。它逃过所有正则规则，只靠这道 AST 判定拦。
func TestFacadeDetectsMultiStatement(t *testing.T) {
	src := `package dispatch

import up "example.test/up"

func Reset(g *up.Guard) {
	up.Reset(g)
	up.Flush(g)
}
`
	vs := facadeViolationsFixture(t, src)
	if !strings.Contains(textsOf(vs), "条语句") {
		t.Fatalf("两条语句的转发体必须被判违规（判据 3），实得:\n%s", textsOf(vs))
	}
}

// TestFacadeDetectsBranchStatement 判据 3 的旁证：单条但不是调用的语句（if/for）同样拒。
func TestFacadeDetectsBranchStatement(t *testing.T) {
	src := `package dispatch

import up "example.test/up"

func Reset(g *up.Guard) {
	if g != nil {
		up.Reset(g)
	}
}
`
	vs := facadeViolationsFixture(t, src)
	if len(vs) == 0 {
		t.Fatal("分支语句必须被判违规")
	}
}

// TestFacadeDetectsNonUpstreamCall 判据 4：调本包自己的函数必须拒。
//
// 这条最难被察觉——字符串里完全不含上游特征，看起来「只是一次普通调用」。
func TestFacadeDetectsNonUpstreamCall(t *testing.T) {
	src := `package dispatch

import up "example.test/up"

func helper() int { return 1 }

func Compute() int { return helper() }
`
	vs := facadeViolationsFixture(t, src)
	if !strings.Contains(textsOf(vs), "helper") {
		t.Fatalf("调非上游函数必须被判违规（判据 4），实得:\n%s", textsOf(vs))
	}
}

// TestFacadeDetectsDisallowedImport 判据 5：不在允许集合内的 import 必须拒。
//
// 引一个新依赖之后，转发目标就可能绕过上游语义——门禁失去「指名道姓」这条判据。
func TestFacadeDetectsDisallowedImport(t *testing.T) {
	src := `package dispatch

import (
	up "example.test/up"
	"example.test/notallowed"
)

func F() int { return up.F() }

var _ = notallowed.X
`
	vs := facadeViolationsFixture(t, src)
	if !strings.Contains(textsOf(vs), "example.test/notallowed") {
		t.Fatalf("非允许 import 必须被判违规（判据 5），实得:\n%s", textsOf(vs))
	}
}

// ── 其余两条判据（导入形态与空壳） ──

// TestFacadeDetectsDotAndBlankImport `.` / `_` 形态导入让转发目标无从解析，
// 必须拒——否则「指名道姓」这条判据形同虚设。
func TestFacadeDetectsDotAndBlankImport(t *testing.T) {
	for _, name := range []string{".", "_"} {
		src := "package dispatch\n\nimport " + name + " \"example.test/up\"\n\nfunc F() {}\n"
		vs := facadeViolationsFixture(t, src)
		if !strings.Contains(textsOf(vs), name) {
			t.Errorf("%q 形态导入必须被判违规，实得:\n%s", name, textsOf(vs))
		}
	}
}

// TestFacadeDetectsEmptyBody 空壳不是转发——留着只会让人以为这里有实现。
func TestFacadeDetectsEmptyBody(t *testing.T) {
	src := "package dispatch\n\nimport up \"example.test/up\"\n\nfunc Reset() {}\n"
	vs := facadeViolationsFixture(t, src)
	if !strings.Contains(textsOf(vs), "空壳") {
		t.Fatalf("空函数体必须被判违规，实得:\n%s", textsOf(vs))
	}
}

// TestFacadeIgnoresTestFiles 测试文件允许违规（构造违规样例本身就是验证手段），
// 只扫生产代码——否则本文件自己就会把门禁测红。
func TestFacadeIgnoresTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// 生产文件干净，门禁零违规
		"dispatch/dispatch.go": "package dispatch\n\nimport up \"example.test/up\"\n\nfunc F() int { return up.F() }\n",
		"dispatch/sample_test.go": `package dispatch

// 测试文件里随便放真类型定义，门禁不扫——构造违规样例本身就是验证手段。
type Fixture struct{ N int }

var _ = Fixture{}
`,
	})
	fr := facadeRule{Package: "dispatch", Upstream: []string{facadeUpstream}}
	vs, err := facadeViolations(root, fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("测试文件应被跳过，实得违规:\n%s", textsOf(vs))
	}
}

// facadeViolationsFixture 造最小仓并只跑这一个包的门面判定。
func facadeViolationsFixture(t *testing.T, src string) []violation {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"dispatch/dispatch.go": src})
	fr := facadeRule{
		Package:        "dispatch",
		Upstream:       []string{facadeUpstream},
		AllowedImports: []string{"example.test/sig"},
	}
	vs, err := facadeViolations(root, fr)
	if err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	return vs
}
