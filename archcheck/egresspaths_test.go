// egresspaths_test.go —— 子进程出口门禁（egresspaths.go）的行为钉住。
//
// # 成对用例的理由
//
// 这条门禁的两个方向都必须钉住：**坏形态报红**（否则门禁只是装饰）与
// **好形态不报**（否则它会把正确的工作判成错的，维护者只能拆掉判据）。
// 只钉一半的用例组经不起变异：退化成「永远报红」或「永远不报」都能全绿。
//
// # 夹具
//
// 合成包名（app）+ 合成程序（sh），不带领域味。
package archcheck

import (
	"testing"
)

// spawnSrc 一个**未挂出口**的启动点（最朴素的 exec.Command）。
const spawnSrc = "package app\n\nimport \"os/exec\"\n\nfunc Spawn() error { return exec.Command(\"sh\").Run() }\n"

// plumbedSrc 两半都齐的启动点：先问后端能不能兑现，再把声明生成成 env。
const plumbedSrc = "package app\n\nimport (\n\t\"os/exec\"\n\n\t\"github.com/yi-nology/goagent-kernel/egress\"\n)\n\n" +
	"func Spawn(spec egress.Spec, backend egress.Enforcer) error {\n" +
	"\tif err := spec.SatisfiedBy(backend); err != nil {\n\t\treturn err\n\t}\n" +
	"\tcmd := exec.Command(\"sh\")\n\tenv, err := spec.Env(nil)\n" +
	"\tif err != nil {\n\t\treturn err\n\t}\n\tcmd.Env = env\n\treturn cmd.Run()\n}\n"

// envOnlySrc 只做了一半：env 生成齐全，但**从没问过后端能不能兑现**。
//
// 这一态是「声明→兑现」这条链最像完成的样子：环境变量真的生成了、真的挂到了
// 子进程上，而报告里那一条看起来就是「已兑现」。可环境变量只是**建议**——
// 一个能改 env 却没有任何强制面的后端照样让流量自由出网。旧判据对它静默放行。
const envOnlySrc = "package app\n\nimport (\n\t\"os/exec\"\n\n\t\"github.com/yi-nology/goagent-kernel/egress\"\n)\n\n" +
	"func Spawn(spec egress.Spec) error {\n\tcmd := exec.Command(\"sh\")\n\tenv, err := spec.Env(nil)\n" +
	"\tif err != nil {\n\t\treturn err\n\t}\n\tcmd.Env = env\n\treturn cmd.Run()\n}\n"

// fakeProbeSrc 有一个名叫 SatisfiedBy 的调用，但限定符**不是** egress：
// 这是「按方法名认人」会把无关调用算成能力问询的形态。
const fakeProbeSrc = "package app\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/pick\"\n" +
	"\t\"github.com/yi-nology/goagent-kernel/egress\"\n)\n\n" +
	"func Spawn(spec egress.Spec) error {\n\tif err := pick.SatisfiedBy(nil); err != nil {\n\t\treturn err\n\t}\n" +
	"\tcmd := exec.Command(\"sh\")\n\tenv, err := spec.Env(nil)\n" +
	"\tif err != nil {\n\t\treturn err\n\t}\n\tcmd.Env = env\n\treturn cmd.Run()\n}\n"

// fakePlumbingSrc 只出现了同名的 .Env( 但**没有 import egress**：这是「按方法名认人」
// 会误判成兑现证据的形态。
const fakePlumbingSrc = "package app\n\nimport \"os/exec\"\n\ntype re struct{}\n\nfunc (re) Env() []string { return nil }\n\n" +
	"func Spawn(r re) error {\n\tcmd := exec.Command(\"sh\")\n\tcmd.Env = r.Env()\n\treturn cmd.Run()\n}\n"

// TestEgressGateCatchesUnregisteredSpawn 钉住：未登记的启动点必须报红。
//
// 方向是 fail-closed：「没登记」不等于「不要求出口」，而是「没人声明过它的
// 出口是什么」。放成宽松侧的话，加一个 spawn 点就是一次静默扩大判定面。
func TestEgressGateCatchesUnregisteredSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": spawnSrc})
	vs, err := egressPathViolations(root, EgressPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未登记") {
		t.Fatalf("未登记的启动点必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGatePassesWhenRegisteredAndPlumbed 钉住好形态不报（否则判据会把
// 正确的工作判成错的）。
func TestEgressGatePassesWhenRegisteredAndPlumbed(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": plumbedSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("已登记且看得见兑现的启动点不该报，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesMissingPlumbing 钉住：**登记了但看不见兑现**同样报红。
//
// 这是「声明了但没人执行」的形态：policy 写着要经代理，代码里没有任何一处
// 用声明去生成 env——而声明本身不会跑到子进程上。
func TestEgressGateCatchesMissingPlumbing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": spawnSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "看不见") {
		t.Fatalf("要求经代理却没兑现必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesMissingCapabilityProbe 钉住：**只生成 env 不够**。
//
// 这是本轮补上的那一半判据。旧判据（只看有没有 Env/Verify）对下面这个 fixture
// 静默放行——env 生成了、挂上了，于是「要求经代理」在报告里显示已兑现；而后端
// 能不能强制这一档，从来没有人问过。
func TestEgressGateCatchesMissingCapabilityProbe(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": envOnlySrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKind(vs, "egress_unprobed") {
		t.Fatalf("只生成 env 却没问后端能力，必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGateRejectsFakeCapabilityProbe 钉住：能力问询也按 import 路径认人。
func TestEgressGateRejectsFakeCapabilityProbe(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": fakeProbeSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKind(vs, "egress_unprobed") {
		t.Fatalf("非 egress 的 SatisfiedBy 不是能力问询证据，实得 %v", textsOf(vs))
	}
}

// TestEgressGateRejectsFakePlumbing 钉住：只按方法名认人不行。
//
// 不 import egress 的 .Env( 可能是任何东西的方法，把它当兑现证据等于
// 「函数里随便有个 .Env( 就过」。
func TestEgressGateRejectsFakePlumbing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": fakePlumbingSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "看不见") {
		t.Fatalf("没 import egress 的同名方法不是兑现证据，实得 %v", textsOf(vs))
	}
}

// TestEgressGateRejectsDeniedSpawn 钉住：**禁网档不能落在环境变量上**。
//
// 环境变量拦不住静态二进制、UDP 与原始 socket ⇒ 登记成 denied 只是把
// 「假装禁网」写进 policy，而报告会显示「已登记、已管住」。
func TestEgressGateRejectsDeniedSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": plumbedSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "denied"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "假装禁网") {
		t.Fatalf("禁网档落在 env 上必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesStaleEntry 钉住：陈旧登记即红。
//
// 声明与事实偏离而无人红，是无声漂移——「这条路径的出口有人管过」会
// 看起来成立，实际上那个文件/函数早就没了。
func TestEgressGateCatchesStaleEntry(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": spawnSrc})
	cases := []struct {
		name string
		ent  EgressEntry
	}{
		{"文件不存在", EgressEntry{Path: "gone/gone.go", Fn: "Spawn", Mode: "proxy"}},
		{"函数不存在", EgressEntry{Path: "app/app.go", Fn: "Nope", Mode: "proxy"}},
		{"函数里没有启动调用", EgressEntry{Path: "app/app.go", Fn: "Other", Mode: "proxy"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs, err := egressPathViolations(root, EgressPaths{Entries: []EgressEntry{c.ent}})
			if err != nil {
				t.Fatal(err)
			}
			if !containsText(vs, "偏离") {
				t.Fatalf("陈旧登记必须报红，实得 %v", textsOf(vs))
			}
		})
	}
}

// TestEgressGateUnrestrictedNeedsNoPlumbing 钉住：**显式登记不限制**是合法声明。
//
// 这条与「未登记即红」配成对：它证明红的是「没人声明过」，不是「起了子进程」。
// 没有它，判据可能退化成「凡 spawn 一律红」而用例照样全绿。
func TestEgressGateUnrestrictedNeedsNoPlumbing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": spawnSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "unrestricted"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("显式登记不限制时不该要求兑现证据，实得 %v", textsOf(vs))
	}
}

// TestEgressGateRejectsUnknownMode 钉住：档位写错不许当成 unrestricted。
//
// 静默按缺省处理会让一个拼错的档位（proxxy）无声退化成「不限制」——
// 而 policy 的读者看到的是「登记过了」。
func TestEgressGateRejectsUnknownMode(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": spawnSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "词表") {
		t.Fatalf("档位拼错必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGateOnThisRepo 钉住**本仓自己的启动点**（验收要求：ptc 子进程路径先过）。
//
// 直接拿真实 policy 跑：它证明①登记不陈旧 ②兑现证据在启动函数里看得见。
// 用临时树是钉不住的——那只证明判据会跑，不证明本仓真的接上了。
func TestEgressGateOnThisRepo(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	pol, err := Load(root + "/architecture-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if pol.EgressPaths == nil || len(pol.EgressPaths.Entries) == 0 {
		t.Fatal("本仓 policy 必须有 egress_paths 登记（否则这条门禁在本仓是空转）")
	}
	vs, err := egressPathViolations(root, *pol.EgressPaths)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("本仓的子进程启动点必须全部登记且挂上兑现：%v", textsOf(vs))
	}
}

// ── 三种「换个写法就消失」的形态（每一种都是实测漏判后才补上的）──

// pkgLevelSpawnSrc 包级初始化里的启动点：不在任何函数里。
const pkgLevelSpawnSrc = "package app\n\nimport \"os/exec\"\n\nvar cmd = exec.Command(\"sh\")\n"

// dotSpawnSrc 点导入形态：调用没有限定符。
const dotSpawnSrc = "package app\n\nimport . \"os/exec\"\n\nfunc Spawn() error { return Command(\"sh\").Run() }\n"

// aliasSpawnSrc 别名形态：限定符改了名字。
const aliasSpawnSrc = "package app\n\nimport ex \"os/exec\"\n\nfunc Spawn() error { return ex.Command(\"sh\").Run() }\n"

// otherEntranceSrc 别的启动入口：os.StartProcess 与 syscall.ForkExec。
const otherEntranceSrc = "package app\n\nimport (\n\t\"os\"\n\t\"syscall\"\n)\n\n" +
	"func Spawn() {\n\t_, _ = os.StartProcess(\"/bin/sh\", nil, nil)\n\t_, _ = syscall.ForkExec(\"/bin/sh\", nil, nil)\n}\n"

// TestEgressGateCatchesPackageLevelSpawn 钉住：**把 spawn 挪到包级变量也要被看见**。
//
// 旧实现只走 *ast.FuncDecl，于是 `var cmd = exec.Command(...)` 让门禁整条失明——
// 而 spawnSite.fn 的注释一直写着「空 = 函数外，如包级变量初始化」，即承诺了没实现。
func TestEgressGateCatchesPackageLevelSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": pkgLevelSpawnSrc})
	vs, err := egressPathViolations(root, EgressPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "包级初始化") {
		t.Fatalf("包级初始化里的启动点必须被看见并标明位置，实得 %v", textsOf(vs))
	}
}

// TestEgressGatePassesRegisteredPackageLevelSpawn 配对：包级 spawn 也能被**登记**。
//
// 没有这条，上面那条可能与「凡包级声明一律红」的退化实现同时成立：
// 后者会让正确的代码无从登记，维护者只能拆掉判据。
func TestEgressGatePassesRegisteredPackageLevelSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": pkgLevelSpawnSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Mode: "unrestricted"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("显式登记为不限制的包级启动点不该报，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesDotImportSpawn 钉住：点导入不改判定面。
func TestEgressGateCatchesDotImportSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": dotSpawnSrc})
	vs, err := egressPathViolations(root, EgressPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未登记") {
		t.Fatalf("点导入的启动点必须被判出来，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesAliasedImportSpawn 钉住：别名不改判定面。
func TestEgressGateCatchesAliasedImportSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": aliasSpawnSrc})
	vs, err := egressPathViolations(root, EgressPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "未登记") {
		t.Fatalf("别名的启动点必须被判出来，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesOtherSpawnEntrances 钉住：启动子进程不止 os/exec。
//
// os.StartProcess / syscall.ForkExec 同样绕开出口强制面，也各自接受 Env——
// 把它们留在判定面外，等于承认「换个 API 就能绕过登记」，而绕过是静默的。
func TestEgressGateCatchesOtherSpawnEntrances(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": otherEntranceSrc})
	vs, err := egressPathViolations(root, EgressPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("两个启动入口都应被看见，实得 %d 条：%v", len(vs), textsOf(vs))
	}
}
