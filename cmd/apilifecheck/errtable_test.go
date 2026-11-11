// errtable_test.go —— 错误登记表解析与双向集合判定。

package main

import (
	"strings"
	"testing"
)

func TestParseErrorTable(t *testing.T) {
	root := writeMiniRepo(t)
	sents, types, problems, err := parseErrorTable(root + "/" + errorSemanticsPath)
	if err != nil {
		t.Fatalf("parseErrorTable: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("不该有结构问题: %v", problems)
	}
	if got := strings.Join(sents["alpha"], ","); got != "ErrBoom,ErrWrap" {
		t.Fatalf("alpha 哨兵 got %q", got)
	}
	if !types["alpha.BoomErr"] {
		t.Fatal("errors.As 表缺 alpha.BoomErr")
	}
}

func TestParseErrorTableSectionDrift(t *testing.T) {
	// 两类结构漂移必须可见：errors.As 表里不带点号的符号；重复小节。
	root := t.TempDir()
	writeFile(t, root, errorSemanticsPath, `# 表

## 哨兵总表（按包）

### a

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `+"`ErrX`"+` | x | x |

### a

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `+"`ErrY`"+` | y | y |

## 结构化错误类型（errors.As 判定）

| 符号 | 含义 | 调用方处置 |
|---|---|---|
| `+"`NoDot`"+` | x | x |
`)
	_, _, problems, err := parseErrorTable(root + "/" + errorSemanticsPath)
	if err != nil {
		t.Fatalf("parseErrorTable: %v", err)
	}
	joined := strings.Join(problems, ";")
	if !strings.Contains(joined, "两个小节") || !strings.Contains(joined, "不是「包.类型」形态") {
		t.Fatalf("结构漂移判定错: %v", problems)
	}
}

func TestCheckErrorRegistryBothDirections(t *testing.T) {
	root := writeMiniRepo(t)
	m := &Manifest{Packages: []Entry{{Pkg: "alpha", Level: LevelStable, Reason: "r"}}}
	sents := []Sentinel{{Pkg: "alpha", Name: "ErrBoom"}, {Pkg: "alpha", Name: "ErrWrap"}}
	typs := []ErrType{{Pkg: "alpha", Name: "BoomErr"}}
	if probs := checkErrorRegistry(root, m, sents, typs); len(probs) != 0 {
		t.Fatalf("全绿夹具不该报登记问题: %v", probs)
	}

	// ① 源码有、表没有 ⇒ 未登记。
	probs := checkErrorRegistry(root, m, append(sents, Sentinel{Pkg: "alpha", Name: "ErrNew"}), typs)
	if len(probs) != 1 || !strings.Contains(probs[0], "ErrNew") || !strings.Contains(probs[0], "未登记") {
		t.Fatalf("未登记判定错: %v", probs)
	}

	// ② 表有、源码没有 ⇒ 幽灵。
	probs = checkErrorRegistry(root, m, sents[:1], typs) // 撤掉 ErrWrap 的声明
	if len(probs) != 1 || !strings.Contains(probs[0], "ErrWrap") || !strings.Contains(probs[0], "幽灵") {
		t.Fatalf("幽灵条目判定错: %v", probs)
	}
	// 基线（全部声明在场）不红。
	if probs = checkErrorRegistry(root, m, sents, typs); len(probs) != 0 {
		t.Fatalf("基线不该红: %v", probs)
	}

	// ③ errors.As 类型双向：新类型未登记 + 旧类型成幽灵，两条一起报。
	writeFile(t, root, errorSemanticsPath, miniErrorTable)
	probs = checkErrorRegistry(root, m, sents, []ErrType{{Pkg: "alpha", Name: "NewErr"}})
	joined := strings.Join(probs, ";")
	if !strings.Contains(joined, "结构化错误类型 alpha.NewErr 未登记") ||
		!strings.Contains(joined, "alpha.BoomErr 在代码里不存在") {
		t.Fatalf("类型双向判定错: %v", probs)
	}
	writeFile(t, root, errorSemanticsPath,
		strings.Replace(miniErrorTable, "| `alpha.BoomErr` | 爆炸现场 | 读 Code |",
			"| `alpha.BoomErr` | 爆炸现场 | 读 Code |\n| `alpha.GhostErr` | 无 | 无 |", 1))
	probs = checkErrorRegistry(root, m, sents, typs)
	if len(probs) != 1 || !strings.Contains(probs[0], "alpha.GhostErr") {
		t.Fatalf("类型幽灵判定错: %v", probs)
	}
}

func TestCheckErrorRegistrySectionNotInManifest(t *testing.T) {
	// 小节挂错包名（不是清单登记的包）⇒ 红。
	root := t.TempDir()
	writeFile(t, root, errorSemanticsPath, `## 哨兵总表（按包）

### alfa

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| `+"`ErrBoom`"+` | x | x |
`)
	m := &Manifest{Packages: []Entry{{Pkg: "alpha", Level: LevelStable, Reason: "r"}}}
	probs := checkErrorRegistry(root, m, nil, nil)
	if len(probs) != 1 || !strings.Contains(probs[0], "不是清单登记的包") {
		t.Fatalf("小节挂错包判定错: %v", probs)
	}
}

func TestCheckErrorRegistryMissingDoc(t *testing.T) {
	// 判据面被删必须红，不能静默绿。
	root := t.TempDir()
	m := &Manifest{Packages: []Entry{{Pkg: "alpha", Level: LevelStable, Reason: "r"}}}
	probs := checkErrorRegistry(root, m, nil, nil)
	if len(probs) != 1 || !strings.Contains(probs[0], "不存在") {
		t.Fatalf("判据面缺失判定错: %v", probs)
	}
}

func TestFirstCellName(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"| `ErrBoom` | 爆了 | 修声明 |", "ErrBoom"},
		{"| 哨兵 | 触发条件 | 调用方处置 |", ""},
		{"|---|---|---|", ""},
		{"| no backtick | x |", ""},
		{"| `unclosed | x |", ""},
		{"plain text", ""},
	}
	for _, tc := range cases {
		got, _ := firstCellName(tc.line)
		if got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.line, got, tc.want)
		}
	}
}
