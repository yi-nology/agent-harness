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

// ── 赋值路径（v0.33.0，issue #92）────────────────────────────────────────
//
// 字面量判据判不出 `opts.AllowUnmanaged = …`（没有复合字面量）——而那正是
// 生产代码最常见的写法。下列用例逐格钉住 AST 级类型追踪：注入即红；
// 宿主自建同名字段不误伤（类型感知换「报得准」）。

// TestUnmanagedEscapeCatchesAssignment 负例（issue #92 核心形态）：
// 变量赋值开启逃逸口 ⇒ 红。
func TestUnmanagedEscapeCatchesAssignment(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

var opts ptc.Options

func Setup() {
	opts.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "赋值选择了未受管执行") {
		t.Fatalf("赋值路径的逃逸必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeCatchesConfigDrivenAssignment 配置驱动路径：
// 值来自配置（非字面量）同样红——判据按赋值路径判，不按值判。
func TestUnmanagedEscapeCatchesConfigDrivenAssignment(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

type Cfg struct{ Unmanaged bool }

func Setup(cfg Cfg) {
	opts := &ptc.Options{}
	opts.AllowUnmanaged = cfg.Unmanaged
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "赋值选择了未受管执行") {
		t.Fatalf("配置驱动的赋值逃逸必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeCatchesStructFieldAndEmbedding 结构体字段链与嵌入：
// `cfg.O.AllowUnmanaged = …` 与嵌入提升后的 `cfg.AllowUnmanaged = …` 都红。
func TestUnmanagedEscapeCatchesStructFieldAndEmbedding(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

type ServerConfig struct {
	PTC ptc.Options
}

type EmbeddedConfig struct {
	ptc.Options
}

func touch(cfg *ServerConfig, emb EmbeddedConfig) {
	cfg.PTC.AllowUnmanaged = true
	emb.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("字段链与嵌入两条赋值都应被报出，实得 %d: %v", len(vs), textsOf(vs))
	}
}

// TestUnmanagedEscapeCatchesContainerElement 容器元素：`[]ptc.Options` 的
// 元素字段赋值也红。
func TestUnmanagedEscapeCatchesContainerElement(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

func Setup() []ptc.Options {
	opts := []ptc.Options{{}}
	opts[0].AllowUnmanaged = true
	return opts
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "赋值选择了未受管执行") {
		t.Fatalf("容器元素路径的逃逸必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeCatchesCrossFileAssignment 包级变量跨文件赋值：
// 声明在 a.go、赋值在 b.go——按目录（包）收变量，不漏。
func TestUnmanagedEscapeCatchesCrossFileAssignment(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/a.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

var opts ptc.Options
`,
		"app/b.go": `package app

func Setup() {
	opts.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "赋值选择了未受管执行") {
		t.Fatalf("跨文件赋值必须被报出，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeIgnoresOwnStructAssignment **不误伤**：宿主自建的
// 同名字段不是本包的逃逸口——类型感知的收益就在这里（字段名一样、类型不同）。
func TestUnmanagedEscapeIgnoresOwnStructAssignment(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

type Opts struct{ AllowUnmanaged bool }

func Setup(o *Opts) {
	o.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("宿主自建的同名字段不该被拦，实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeBlindSpotIsDocumented 盲区形态（静态不可溯的接收方）
// 不报——这条钉的是「判据的量纲」：它对不可溯形态失明是**设计选择**
// （换来的是不误伤宿主自建字段），写下来才不会有人把它当成全量覆盖。
func TestUnmanagedEscapeBlindSpotIsDocumented(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

func newOpts() *ptc.Options { return &ptc.Options{} }

func Setup() {
	o := newOpts()
	o.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	// 字面量判据仍会抓住 newOpts 里的复合字面量形态（无 AllowUnmanaged 键，
	// 不报）；赋值判据对「函数返回值」失明——盲区在案。
	if containsText(vs, "赋值选择了未受管执行") {
		t.Fatalf("赋值判据对静态不可溯的接收方应不报（盲区已写明），实得 %v", textsOf(vs))
	}
}

// TestUnmanagedEscapeAssignmentBlocksRun 门禁级反向验证（issue #92 验收）：
// 注入赋值形态的夹具 ⇒ 整个 archcheck.Run 的结果必须判为阻断
// （cmd/archcheck 对 HasBlocking 退出码 1——见 cmd/archcheck/main.go）。
func TestUnmanagedEscapeAssignmentBlocksRun(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"app/app.go": `package app

import "github.com/yi-nology/goagent-kernel/ptc"

var opts ptc.Options

func Setup() {
	opts.AllowUnmanaged = true
}
`,
	})
	vs, err := unmanagedEscapeViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) == 0 {
		t.Fatal("无违规：注入的赋值形态必须让门禁报红")
	}
	for _, v := range vs {
		if v.Rule != unmanagedEscapeRuleID {
			t.Fatalf("违规应挂在 %s 下，实得 %q", unmanagedEscapeRuleID, v.Rule)
		}
	}
	// 违规计入阻断集（severity=error，见 archcheck.go 的 sevOf），宿主 CLI
	// 据 HasBlocking 退出非 0——这里断言链路前端：违规存在且属阻断规则。
}
