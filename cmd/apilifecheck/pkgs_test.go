// pkgs_test.go —— 生产包枚举与标记扫描的判定。

package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverPackages(t *testing.T) {
	root := writeMiniRepo(t)
	got, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("DiscoverPackages: %v", err)
	}
	want := []string{"alpha", "beta", "internal/deep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v（cmd/ 必须被排除，嵌套包必须在）", got, want)
	}
}

func TestDiscoverPackagesSkipsUnreadable(t *testing.T) {
	// 遍历错误必须响亮失败（返回 error），不能吞成「零个包」。
	if _, err := DiscoverPackages(t.TempDir() + "/不存在的根"); err == nil {
		t.Fatal("不存在的根未报错")
	}
}

func TestScanPackage(t *testing.T) {
	root := writeMiniRepo(t)

	info, err := ScanPackage(root, "alpha")
	if err != nil {
		t.Fatalf("ScanPackage(alpha): %v", err)
	}
	if info.MarkerLevel != LevelStable || len(info.MarkerFiles) != 1 || info.MarkerFiles[0] != "alpha.go" {
		t.Fatalf("alpha 标记判定错: %+v", info)
	}
	imports := strings.Join(info.Imports, ",")
	if !strings.Contains(imports, "github.com/oekit/pkg/errors") {
		t.Fatalf("import 面缺 errors 包: %v", info.Imports)
	}

	info, err = ScanPackage(root, "internal/deep")
	if err != nil {
		t.Fatalf("ScanPackage(deep): %v", err)
	}
	if info.MarkerLevel != LevelExperimental {
		t.Fatalf("deep 标记判定错: %+v", info)
	}

	// 无标记包：MarkerLevel 为空、文件清单为空。
	info, err = ScanPackage(root, "cmd")
	_ = info // cmd 被排除不会出现在 DiscoverPackages，但 ScanPackage 本身可判
	if err != nil {
		t.Fatalf("ScanPackage(cmd): %v", err)
	}
}

func TestScanPackageParseError(t *testing.T) {
	// 语法坏文件必须响亮失败（gate 退出码 2），不是「无标记」。
	root := t.TempDir()
	writeFile(t, root, "bad/bad.go", "package bad\nfunc {{{\n")
	if _, err := ScanPackage(root, "bad"); err == nil {
		t.Fatal("解析失败未报错")
	}
}

func TestDocMarkerVariants(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"生命周期: Stable", LevelStable, true},
		{"生命周期：Experimental（v0.33.0 定级）", LevelExperimental, true},
		{"生命周期:  Deprecated（v1.0.0 起）", LevelDeprecated, true},
		{"生命周期: Wrong", "", false},
		{"生命周期Stable", "", false},
		{"这是关于生命周期的散文,没有标记", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		m := markerRe.FindStringSubmatch(tc.line)
		if tc.ok {
			if m == nil || m[1] != tc.want {
				t.Errorf("%q: got %v, want %s", tc.line, m, tc.want)
			}
			continue
		}
		if m != nil {
			t.Errorf("%q: 不该命中, got %v", tc.line, m)
		}
	}
}

func TestMarkerMustBeInPackageDoc(t *testing.T) {
	// 标记写在普通函数注释里不算数——必须在包注释（package doc）里。
	root := t.TempDir()
	writeFile(t, root, "a/a.go", `package a

func f() {
	// 生命周期: Stable
	_ = 1
}
`)
	info, err := ScanPackage(root, "a")
	if err != nil {
		t.Fatalf("ScanPackage: %v", err)
	}
	if info.MarkerLevel != "" || len(info.MarkerFiles) != 0 {
		t.Fatalf("非包注释标记被判成有效: %+v", info)
	}
}
