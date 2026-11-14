// issue162_test.go —— 嵌套模块排除（issue #162）的行为钉住。
//
// # 为什么必须有这条
//
// 主克隆里存在嵌套 worktree/源码副本（目录自带 go.mod）时，旧实现在
// TestEgressGateOnThisRepo 误报「子进程启动点未登记」——报的是**嵌套拷贝里**
// 的同名函数。CI 恒绿（runner 无嵌套目录）、宿主克隆恒红：同一份代码两种裁决。
//
// # 成对用例
//
// 嵌套模块里的启动点**不报**（排除生效）；同一棵树里外层模块的未登记启动点
// **照报**（排除没有把本仓判定面一起放掉——防「永远不报」退化）。
package archcheck

import (
	"path/filepath"
	"testing"
)

func TestNestedModuleSpawnSitesExcluded(t *testing.T) {
	root := t.TempDir()
	// 外层模块：已登记的启动点。
	writeFixture(t, root, map[string]string{
		"app/app.go": spawnSrc,
		"go.mod":     "module example.com/outer\n\ngo 1.26\n",
		// 嵌套模块：linked worktree / 源码副本形态——自带 go.mod，
		// 里面的启动点**未登记**，但属于另一棵模块树，不归本仓门禁管。
		"nested/go.mod":     "module example.com/nested\n\ngo 1.26\n",
		"nested/app/app.go": spawnSrc,
	})

	cfg := EgressPaths{Entries: []EgressEntry{{Path: "app/app.go", Fn: "Spawn", Mode: "unrestricted"}}}
	vs, err := egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("嵌套模块里的启动点不得算进本仓判定面（issue #162），实得 %v", textsOf(vs))
	}

	// 防退化：外层模块自己的未登记启动点照报。
	writeFixture(t, root, map[string]string{
		"app/other.go": spawnSrc, // 未登记的第二个启动点
	})
	vs, err = egressPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vs {
		// spawnSite.rel 按 repo 惯例 ToSlash 归一（Windows 下 Join 出反斜杠，
		// 直接比会假阴）——比对前同样归一。
		if v.Kind == "egress_path_unregistered" && v.Path == filepath.ToSlash(filepath.Join("app", "other.go")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("排除嵌套模块不得连本仓未登记启动点一起放掉，实得 %v", textsOf(vs))
	}
}

func TestPackageDirsSkipsNestedModule(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"inner/inner.go": "package inner\n",
		// 嵌套模块根：里面有 go.mod 和生产 Go 文件——不得出现在 packageDirs。
		"nested/go.mod": "module example.com/nested\n\ngo 1.26\n",
		"nested/p/p.go": "package p\n",
	})
	dirs, err := packageDirs(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)
		joined += rel + "\n"
	}
	if !containsRel(dirs, root, "inner") {
		t.Fatalf("外层包目录应保留，实得 %s", joined)
	}
	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)
		if rel == "nested" || rel == filepath.Join("nested", "p") {
			t.Fatalf("嵌套模块目录不得进 packageDirs（issue #162），实得含 %s", rel)
		}
	}
}

// containsRel 判断 dirs（绝对路径）里是否含相对 root 为 relDir 的目录。
func containsRel(dirs []string, root, relDir string) bool {
	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)
		if rel == relDir {
			return true
		}
	}
	return false
}
