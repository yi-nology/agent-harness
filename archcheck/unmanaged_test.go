// unmanaged_test.go —— 未受管逃逸门禁（unmanaged.go）的行为钉住。
//
// # 这条门禁钉的是什么
//
// PTC 的安全默认是 fail-closed（有绑定无判定环 ⇒ 拒绝执行），
// 但那留了一个逃逸口：AllowUnmanaged: true。
//
// 逃逸口的通病是**真违规一起被放过**——一旦它能在生产代码里随手写，
// 它就从「显式声明」退化成「默认路径」。故生产代码里出现这个字段即报。
//
// # 夹具
//
// 合成包名与合成类型，不带领域味。
package archcheck

import (
	"testing"
)

// TestUnmanagedEscapeCatchesProductionUse 钉住：生产代码显式选择未受管执行
// 必须被报出来。
func TestUnmanagedEscapeCatchesProductionUse(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

func New() ptc.Options { return ptc.Options{AllowUnmanaged: true} }
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未受管执行") {
		t.Fatalf("生产代码里的未受管逃逸必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeIgnoresAlias 钉住：**改 import 别名绕不过去**。
//
// 判据按 import 路径的**最后一段**（包名）匹配，不按限定符——
// 限定符是调用方随手起的，按它匹配等于给了一条绕过路径。
func TestUnmanagedEscapeIgnoresAlias(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import alias "github.com/yi-nology/goagent-kernel/ptc"

func New() alias.Options { return alias.Options{AllowUnmanaged: true} }
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未受管执行") {
		t.Fatalf("别名形态的逃逸也必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeIgnoresTestFiles 钉住：**测试夹具不算生产路径**。
//
// 反向验证用例要构造「未受管执行」的现场（「装上判定环才放行」这类），
// 它本身必须用逃逸口——若门禁连测试文件一起拦，负对照就写不出来。
func TestUnmanagedEscapeIgnoresTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app_test.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

func New() ptc.Options { return ptc.Options{AllowUnmanaged: true} }
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("测试夹具里的逃逸口不该被拦（它是负对照的一部分），实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeIgnoresUnrelatedTypes 钉住**不误报**：别的包自己的
// 同名字段不是本包的逃逸口。
//
// 门禁的价值一半在「报得准」：误报多了，这条规则就会被习惯性忽略，
// 于是真违规一起被放过——正是逃逸口类规则的通病。
func TestUnmanagedEscapeIgnoresUnrelatedTypes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "example.com/other/ptc"

type Opts struct{ AllowUnmanaged bool }

func New() Opts { return Opts{AllowUnmanaged: true} }
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("非本包的同名字段不该被拦，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeCatchesConfigDrivenValue 钉住**按字段出现**判，
// 而不是按「字面量 true」判：配置驱动的写法（值是变量）同样要报。
//
// 判据若只认字面量 true，AllowUnmanaged: cfg.Unmanaged 就从门禁眼里
// 溜过去了——而那恰恰是最该被看见的形态（「这个部署可能跑未受管执行」）。
func TestUnmanagedEscapeCatchesConfigDrivenValue(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

var flag bool

func New() ptc.Options { return ptc.Options{AllowUnmanaged: flag} }
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未受管执行") {
		t.Fatalf("配置驱动的逃逸也必须被报出，实得 %v", textsOf(vs))
	}
}
