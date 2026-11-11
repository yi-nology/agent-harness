// manifest_test.go —— 清单装载与字段级判定的正反例。

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestKnownFields(t *testing.T) {
	root := writeMiniRepo(t)
	m, err := LoadManifest(filepath.Join(root, manifestName))
	if err != nil {
		t.Fatalf("合法清单装载失败: %v", err)
	}
	if m.Version != 1 || len(m.Packages) != 3 {
		t.Fatalf("version=%d packages=%d, 期望 1/3", m.Version, len(m.Packages))
	}
	if m.Packages[0].Pkg != "alpha" || m.Packages[0].Level != LevelStable {
		t.Fatalf("首条登记解析错: %+v", m.Packages[0])
	}
}

func TestLoadManifestUnknownField(t *testing.T) {
	// 拼错字段名必须在装载处变红，而不是被静默忽略。
	root := t.TempDir()
	writeFile(t, root, manifestName, "version: 1\npackages:\n  - pkg: a\n    lvel: Stable\n")
	if _, err := LoadManifest(filepath.Join(root, manifestName)); err == nil {
		t.Fatal("未知字段未报错——KnownFields 未生效")
	}
}

func TestLoadManifestBrokenYAML(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, manifestName, "version: [1\n")
	if _, err := LoadManifest(filepath.Join(root, manifestName)); err == nil {
		t.Fatal("坏 YAML 未报错")
	}
}

func TestValidateProblems(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // 期望出现在问题里的子串；空 = 期望无问题
	}{
		{"合法", miniManifest, ""},
		{"版本不符", strings.Replace(miniManifest, "version: 1", "version: 2", 1), "version = 2"},
		{"未知级别", strings.Replace(miniManifest, "level: Stable", "level: Stabel", 1), "不在词表"},
		{"坏 since", strings.Replace(miniManifest, "since: v0.33.0", "since: 0.33.0", 1), "不是 vX.Y.Z 形态"},
		{"缺理由", strings.Replace(miniManifest, "    reason: 夹具——契约面示例\n", "", 1), "reason 为空"},
		{
			"Deprecated 缺替代",
			"version: 1\npackages:\n  - pkg: a\n    level: Deprecated\n    since: v0.33.0\n    reason: 退役\n",
			"Deprecated 必须给 replacement",
		},
		{
			"非 Deprecated 带替代",
			strings.Replace(miniManifest, "    reason: 夹具——契约面示例\n",
				"    reason: 夹具\n    replacement: github.com/yi-nology/goagent-kernel/x.Y\n", 1),
			"只有 Deprecated 条目",
		},
		{
			"重复登记",
			miniManifest + "  - pkg: alpha\n    level: Stable\n    since: v0.33.0\n    reason: dup\n",
			"重复登记",
		},
		{
			"缺 pkg",
			"version: 1\npackages:\n  - level: Stable\n    since: v0.33.0\n    reason: r\n",
			"没有 pkg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, manifestName, tc.yaml)
			m, err := LoadManifest(filepath.Join(root, manifestName))
			if err != nil {
				t.Fatalf("装载失败: %v", err)
			}
			probs := m.Validate()
			if tc.want == "" {
				if len(probs) != 0 {
					t.Fatalf("期望无问题, 得 %v", probs)
				}
				return
			}
			found := false
			for _, p := range probs {
				if strings.Contains(p, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("问题里找不到 %q: %v", tc.want, probs)
			}
		})
	}
}

func TestFindRoot(t *testing.T) {
	root := writeMiniRepo(t)
	t.Chdir(filepath.Join(root, "alpha")) // 从子目录向上找（Go 1.24+ t.Chdir）
	got, err := findRoot(".")
	if err != nil {
		t.Fatalf("findRoot: %v", err)
	}
	if got != root {
		t.Fatalf("findRoot = %s, 期望 %s", got, root)
	}

	// 仓库外必须报错，而不是返回某个巧合目录。
	outside := t.TempDir()
	t.Chdir(outside)
	if _, err := findRoot("."); err == nil {
		t.Fatal("仓库外 findRoot 未报错")
	}
}

func TestLoadManifestMissing(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadManifest(filepath.Join(root, manifestName)); err == nil {
		t.Fatal("清单缺失未报错")
	}
	if _, err := os.Stat(filepath.Join(root, manifestName)); !os.IsNotExist(err) {
		t.Fatal("清单不应被创建")
	}
}
