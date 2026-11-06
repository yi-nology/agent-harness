// egressenv_test.go —— 「子进程环境不得全量继承」这条判据的行为钉住。
//
// 配对必须两头都在：坏形态报红（否则判据是装饰）、好形态不报（否则它会把
// 正确的工作判成错的）。两种导入写法各钉一次——别名与点导入是绕过文本正则的
// 两条现成通道，只钉原样写法等于没钉住。
package archcheck

import "testing"

// fullEnvSrc 把 os.Environ() 当成子进程环境（问题形态本身）。
const fullEnvSrc = "package app\n\nimport (\n\t\"os\"\n\t\"os/exec\"\n)\n\n" +
	"func Spawn() error {\n\tcmd := exec.Command(\"sh\")\n\tcmd.Env = append(os.Environ(), \"A=1\")\n\treturn cmd.Run()\n}\n"

// aliasedEnvironSrc 别名导入：限定符叫 o 而不是 os。
const aliasedEnvironSrc = "package app\n\nimport (\n\to \"os\"\n\t\"os/exec\"\n)\n\n" +
	"func Spawn() error {\n\tcmd := exec.Command(\"sh\")\n\tcmd.Env = o.Environ()\n\treturn cmd.Run()\n}\n"

// dottedEnvironSrc 点导入：Environ 直接进本文件作用域，没有限定符。
const dottedEnvironSrc = "package app\n\nimport (\n\t. \"os\"\n\t\"os/exec\"\n)\n\n" +
	"func Spawn() error {\n\tcmd := exec.Command(\"sh\")\n\tcmd.Env = Environ()\n\treturn cmd.Run()\n}\n"

// allowlistedEnvSrc 好形态：白名单生成 + 出口声明叠加。
const allowlistedEnvSrc = "package app\n\nimport (\n\t\"os/exec\"\n\n\t\"github.com/oagentkit/runtime/procx\"\n" +
	"\t\"github.com/yi-nology/agent-harness/egress\"\n)\n\n" +
	"func Spawn(spec egress.Spec) error {\n\tenv, err := spec.Env(procx.ChildEnv([]string{\"MODE=1\"}))\n" +
	"\tif err != nil {\n\t\treturn err\n\t}\n\tcmd := exec.Command(\"sh\")\n\tcmd.Env = env\n\treturn cmd.Run()\n}\n"

// TestEgressGateCatchesFullEnvInherit 钉住：os.Environ() 作为子进程环境即红。
//
// 与登记无关：这里**登记成 unrestricted**（最宽松的一档），判据仍然要红——
// 「出站策略不管」不等于「可以把宿主的秘密交出去」。
func TestEgressGateCatchesFullEnvInherit(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": fullEnvSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "unrestricted"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsKind(vs, "egress_full_env_inherit") {
		t.Fatalf("全量继承环境必须报红，实得 %v", textsOf(vs))
	}
}

// TestEgressGateCatchesFullEnvInheritVariants 钉住别名与点导入两种写法。
func TestEgressGateCatchesFullEnvInheritVariants(t *testing.T) {
	for name, src := range map[string]string{
		"别名导入": aliasedEnvironSrc,
		"点导入":  dottedEnvironSrc,
	} {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"app/app.go": src})
		vs, err := egressPathViolations(root, EgressPaths{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !containsKind(vs, "egress_full_env_inherit") {
			t.Fatalf("%s 形态必须报红（换个导入写法就绕过 = 判定面消失），实得 %v", name, textsOf(vs))
		}
	}
}

// TestEgressGatePassesAllowlistedEnv 钉住好形态不报：白名单生成 + egress 叠加。
func TestEgressGatePassesAllowlistedEnv(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"app/app.go": allowlistedEnvSrc})
	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "proxy"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("白名单形态不该报任何违反，实得 %v", textsOf(vs))
	}
}

// containsKind 判是否出现指定 kind 的违反。
func containsKind(vs []Violation, kind string) bool {
	for _, v := range vs {
		if v.Kind == kind {
			return true
		}
	}
	return false
}
