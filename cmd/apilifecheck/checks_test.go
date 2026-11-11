// checks_test.go —— 集合级判定（覆盖 / 标记 / 替代指向 / 门面跟随）。

package main

import (
	"strings"
	"testing"
)

func loadMini(t *testing.T) (*Manifest, map[string]*PkgInfo, []string) {
	t.Helper()
	root := writeMiniRepo(t)
	m, err := LoadManifest(root + "/" + manifestName)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dirs, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("DiscoverPackages: %v", err)
	}
	scanned := map[string]*PkgInfo{}
	for _, d := range dirs {
		info, scanErr := ScanPackage(root, d)
		if scanErr != nil {
			t.Fatalf("ScanPackage(%s): %v", d, scanErr)
		}
		scanned[d] = info
	}
	return m, scanned, dirs
}

func TestCheckCoverage(t *testing.T) {
	m, _, dirs := loadMini(t)
	if probs := checkCoverage(m, dirs); len(probs) != 0 {
		t.Fatalf("全绿夹具不该报覆盖问题: %v", probs)
	}

	// 漏登记：包在、清单没有。
	probs := checkCoverage(m, append(append([]string{}, dirs...), "newpkg"))
	if len(probs) != 1 || !strings.Contains(probs[0], "newpkg") || !strings.Contains(probs[0], "未登记") {
		t.Fatalf("漏登记判定错: %v", probs)
	}

	// 幽灵登记：清单有、包不在。
	probs = checkCoverage(m, dirs[:len(dirs)-1])
	if len(probs) != 1 || !strings.Contains(probs[0], "不存在") {
		t.Fatalf("幽灵登记判定错: %v", probs)
	}
}

func TestCheckMarkers(t *testing.T) {
	m, scanned, _ := loadMini(t)
	if probs := checkMarkers(m, scanned); len(probs) != 0 {
		t.Fatalf("全绿夹具不该报标记问题: %v", probs)
	}

	// 级别不一致。
	scanned["alpha"].MarkerLevel = LevelDeprecated
	probs := checkMarkers(m, scanned)
	if len(probs) != 1 || !strings.Contains(probs[0], "不一致") {
		t.Fatalf("级别不一致判定错: %v", probs)
	}

	// 无标记。
	scanned["alpha"].MarkerLevel = ""
	scanned["alpha"].MarkerFiles = nil
	probs = checkMarkers(m, scanned)
	if len(probs) != 1 || !strings.Contains(probs[0], "没有「生命周期") {
		t.Fatalf("无标记判定错: %v", probs)
	}

	// 双标记（两个文件都带）。
	scanned["alpha"].MarkerFiles = []string{"a.go", "b.go"}
	probs = checkMarkers(m, scanned)
	if len(probs) != 1 || !strings.Contains(probs[0], "只允许一处") {
		t.Fatalf("双标记判定错: %v", probs)
	}
}

func TestCheckReplacements(t *testing.T) {
	root := writeMiniRepo(t)
	m := &Manifest{Packages: []Entry{
		{Pkg: "old", Level: LevelDeprecated, Reason: "r", Replacement: "github.com/yi-nology/goagent-kernel/alpha.ErrBoom"},
	}}
	if probs, err := checkReplacements(root, m); err != nil || len(probs) != 0 {
		t.Fatalf("指向真实符号不该红: %v %v", probs, err)
	}

	// 指向不存在的符号。
	m.Packages[0].Replacement = "github.com/yi-nology/goagent-kernel/alpha.ErrGhost"
	probs, err := checkReplacements(root, m)
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "不存在") {
		t.Fatalf("幽灵替代指向判定错: %v %v", probs, err)
	}

	// 形态错（不含 selfModule / 缺点号）。
	for _, bad := range []string{"alpha.ErrBoom", "github.com/yi-nology/goagent-kernel/alpha", "github.com/yi-nology/goagent-kernel/alpha.notExp"} {
		m.Packages[0].Replacement = bad
		probs, err = checkReplacements(root, m)
		if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "形态") {
			t.Fatalf("替代指向形态 %q 判定错: %v %v", bad, probs, err)
		}
	}

	// 解析失败给不出结论（返回 error，不是当「不存在」）。
	// 用只有坏文件的独立包：若目录里有可解析文件且先命中符号，会提前返回。
	m.Packages[0] = Entry{Pkg: "sym", Level: LevelDeprecated, Reason: "r",
		Replacement: "github.com/yi-nology/goagent-kernel/sym.Thing"}
	writeFile(t, root, "sym/broken.go", "package sym\nfunc {{{\n")
	if _, err := checkReplacements(root, m); err == nil {
		t.Fatal("解析失败未报错")
	}
}

func TestCheckFollowsUpstream(t *testing.T) {
	m, scanned, _ := loadMini(t)
	if probs := checkFollowsUpstream(m, scanned); len(probs) != 0 {
		t.Fatalf("无门面声明不该有问题: %v", probs)
	}

	// 声明了上游但 import 面没有：红。
	m.Packages = append(m.Packages, Entry{
		Pkg: "alpha", Level: LevelStable, Reason: "r",
		FollowsUpstream: "github.com/oagentkit/toolgate",
	})
	probs := checkFollowsUpstream(m, scanned)
	if len(probs) != 1 || !strings.Contains(probs[0], "与代码不符") {
		t.Fatalf("门面跟随判定错: %v", probs)
	}

	// 子包 import 也算命中（guardx/injectband 形态）。
	scanned["alpha"].Imports = []string{"github.com/oagentkit/toolgate/policy"}
	probs = checkFollowsUpstream(m, scanned)
	if len(probs) != 0 {
		t.Fatalf("上游子包应算命中: %v", probs)
	}
}

func TestSplitReplacement(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		pkg  string
		name string
	}{
		{"github.com/yi-nology/goagent-kernel/a.B", true, "github.com/yi-nology/goagent-kernel/a", "B"},
		{"github.com/yi-nology/goagent-kernel/a/b.C", true, "github.com/yi-nology/goagent-kernel/a/b", "C"},
		{"other.module/a.B", false, "", ""},
		{"nosymbol", false, "", ""},
		{"github.com/yi-nology/goagent-kernel/a.b", false, "", ""}, // 未导出符号
	}
	for _, tc := range cases {
		sym, ok := splitReplacement(tc.in)
		if ok != tc.ok {
			t.Errorf("%q ok=%v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && (sym.ImportPath != tc.pkg || sym.Name != tc.name) {
			t.Errorf("%q got %v, want %s.%s", tc.in, sym, tc.pkg, tc.name)
		}
	}
}
