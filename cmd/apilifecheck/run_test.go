// run_test.go —— 端到端判定（t.Chdir + 合成迷你仓）与文档口径块。

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGreen(t *testing.T) {
	root := writeMiniRepo(t)
	var buf bytes.Buffer
	code, err := run(root, &buf)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 0 {
		t.Fatalf("期望退出码 0, got %d:\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"共 3 个包", "Stable 1 / Experimental 2 / Deprecated 0", "全部一致"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}
}

func TestRunViolations(t *testing.T) {
	root := writeMiniRepo(t)

	// 改包注释标记（与清单不一致）⇒ 退出码 1。
	t.Chdir(root)
	beta := root + "/beta/beta.go"
	src := readAll(t, beta)
	writeFile(t, root, "beta/beta.go", strings.Replace(src, "生命周期: Experimental", "生命周期: Stable", 1))
	var buf bytes.Buffer
	code, err := run(root, &buf)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 1 {
		t.Fatalf("期望退出码 1, got %d:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "不一致") {
		t.Fatalf("输出缺不一致判定:\n%s", buf.String())
	}
}

func TestRunNewPackageUnregistered(t *testing.T) {
	root := writeMiniRepo(t)
	// 新增生产包不登记 ⇒ 红（issue #113 的「覆盖全部生产包」判据）。
	writeFile(t, root, "gamma/gamma.go", "package gamma\n\nfunc G() {}\n")
	var buf bytes.Buffer
	code, err := run(root, &buf)
	if err != nil || code != 1 {
		t.Fatalf("期望 (1, nil), got (%d, %v):\n%s", code, err, buf.String())
	}
	if !strings.Contains(buf.String(), "gamma: 生产包未登记") {
		t.Fatalf("输出缺未登记判定:\n%s", buf.String())
	}
}

func TestRunBrokenManifest(t *testing.T) {
	// 清单解析失败 ⇒ 给不出结论（退出码 2 由 runMain 换算，run 返回 error）。
	root := t.TempDir()
	writeFile(t, root, manifestName, "version: [1\n")
	if _, err := run(root, &bytes.Buffer{}); err == nil {
		t.Fatal("坏清单未报错")
	}
}

func TestRunParseBrokenSource(t *testing.T) {
	root := writeMiniRepo(t)
	writeFile(t, root, "alpha/broken.go", "package alpha\nfunc {{{\n")
	if _, err := run(root, &bytes.Buffer{}); err == nil {
		t.Fatal("源码解析失败未报错")
	}
}

func TestCheckDocsBlocks(t *testing.T) {
	root := writeMiniRepo(t)
	m := &Manifest{Packages: make([]Entry, 3)}
	if probs := checkDocsBlocks(root, m); len(probs) != 0 {
		t.Fatalf("全绿夹具不该报口径块问题: %v", probs)
	}

	// 数字漂移：清单 4 个包、文档写 3 个（消息格式是「写 got，实际 want」）。
	m2 := &Manifest{Packages: make([]Entry, 4)}
	probs := checkDocsBlocks(root, m2)
	if len(probs) != 2 ||
		!strings.Contains(strings.Join(probs, ";"), "api-compat.md: 清单登记数写 3，实际 4") ||
		!strings.Contains(strings.Join(probs, ";"), "USAGE.md: 生命周期登记写 3，实际 4") {
		t.Fatalf("口径块漂移判定错: %v", probs)
	}

	// 缺口径块行。
	writeFile(t, root, apiCompatPath, "# 无口径块\n")
	probs = checkDocsBlocks(root, m)
	joined := strings.Join(probs, ";")
	if !strings.Contains(joined, "找不到清单登记数口径块") {
		t.Fatalf("缺口径块判定错: %v", probs)
	}

	// api-compat.md 被删 ⇒ 红（判据面缺失）。
	if err := os.Remove(filepath.Join(root, apiCompatPath)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	probs = checkDocsBlocks(root, m)
	if len(probs) != 1 || !strings.Contains(probs[0], "不存在") {
		t.Fatalf("文档缺失判定错: %v", probs)
	}
}

func TestMatchCount(t *testing.T) {
	n, ok := matchCount(apiCompatCountRe, "前缀 api-lifecycle.yaml 登记 **44** 个包 后缀")
	if !ok || n != 44 {
		t.Fatalf("got %d %v", n, ok)
	}
	if _, ok := matchCount(apiCompatCountRe, "没有数字块"); ok {
		t.Fatal("缺块不应命中")
	}
}

// readAll 读文件全文（测试内的小助手）。
func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
