package main

// 本文件补的是 run() 的**端到端判定**——上一版 23.6% 的覆盖率全部来自纯函数用例
// （normalizeDep / locDeviation / suffixMatch 那些），而 run() 才是这道门禁真正
// 说话的地方：它决定「什么样的漂移会被报出来、报成哪一句」。
//
// # 为什么可以直接测 run()
//
// run() 读的是**相对路径**（`README.md` 与 `.`），所以用 `t.Chdir` 进一个临时目录、
// 在那里摆一份 README 与若干包目录，就是它眼里的整个世界。不需要为了可测而改造
// 生产代码——这是 Go 1.24+ 的 `t.Chdir` 带来的便利，也是本包**不需要**豁免逐包
// 覆盖率下限的原因。
//
// # 为什么每条都断言错误**内容**
//
// 只断言「返回了非 nil 错误」等于只测了「门禁会红」。而这道门禁的失败形态恰恰是
// 「红错了地方」：漂移确实报了，但报的是另一条问题，维护者按提示改完仍是红。
// 故每条用例都钉住「报的是不是这一条」。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo 在临时目录里摆一份 README 与若干文件，并把工作目录切过去。
//
// files 的 key 是相对路径（如 "alpha/a.go"），父目录自动创建。
func fixtureRepo(t *testing.T, readme string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if readme != "" {
		mustWriteFile(t, filepath.Join(dir, "README.md"), readme)
	}
	for name, body := range files {
		mustWriteFile(t, filepath.Join(dir, name), body)
	}
	t.Chdir(dir)
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

// pkgTable 拼一张包表（表头 + 若干行），使各用例只写差异部分。
func pkgTable(rows ...string) string {
	head := "| 包 | 生产行数 | 外部依赖 | 职责 |\n|---|---|---|---|\n"
	return head + strings.Join(rows, "\n") + "\n"
}

// TestRunAcceptsConsistentRepo 正向：一致时必须静默通过。
//
// 没有这条，其余「该红」的用例可以靠「永远红」全部通过——即判据整个失灵也绿。
func TestRunAcceptsConsistentRepo(t *testing.T) {
	fixtureRepo(t, pkgTable("| `alpha` | 3 | 无 | 示例包 |"), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
	})
	if err := run(); err != nil {
		t.Fatalf("一致的仓库不该报错: %v", err)
	}
}

// TestRunRejectsMissingReadme README 不在 = 没有可校验的声明面，必须报错。
func TestRunRejectsMissingReadme(t *testing.T) {
	fixtureRepo(t, "", map[string]string{"alpha/a.go": "package alpha\n"})
	err := run()
	if err == nil {
		t.Fatal("README.md 不存在时必须报错")
	}
}

// TestRunRejectsEmptyTable 表结构变了（或整表被删）= 解析不到任何行。
//
// 这条防的是「门禁静默变绿」：没有行就没有比对，run 会一声不吭地通过。
func TestRunRejectsEmptyTable(t *testing.T) {
	fixtureRepo(t, "# 没有包表的 README\n", map[string]string{
		"alpha/a.go": "package alpha\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "没解析到任何包表行") {
		t.Fatalf("应报「没解析到任何包表行」，实得 %v", err)
	}
}

// TestRunRejectsUnparseableLoc 行数列解析失败时必须报错，不能当 0 处理。
//
// # 为什么用「溢出」而不是「写了 abc」
//
// 实测发现：`rowRe` 的行数捕获组本身就是 `([0-9]+)`，所以「写了 abc」这类行
// **压根匹配不上表格行**，会走到「没解析到任何包表行」去——也就是说
// `strconv.Atoi` 的失败分支靠「非数字」永远进不去，唯一的入口是**溢出**
// （几十位的数字）。这条分支此前既没被任何用例覆盖，也没人知道它几乎不可达。
//
// 仍然要测它：把 Atoi 的失败当 0 处理，会让偏差以 0 为基数算出一个「看起来
// 在容差内」的值——「格式写错」于是被判成「数字对得上」。
func TestRunRejectsUnparseableLoc(t *testing.T) {
	fixtureRepo(t, pkgTable("| `alpha` | 99999999999999999999 | 无 | 示例包 |"), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "不是整数") {
		t.Fatalf("应报「不是整数」，实得 %v", err)
	}
}

// TestRunRejectsDuplicateRow 同一个包出现两行：后一行会静默覆盖前一行。
func TestRunRejectsDuplicateRow(t *testing.T) {
	fixtureRepo(t, pkgTable(
		"| `alpha` | 3 | 无 | 示例包 |",
		"| `alpha` | 3 | 无 | 重复的一行 |",
	), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "重复行") {
		t.Fatalf("应报「重复行」，实得 %v", err)
	}
}

// TestRunRejectsEmptyDescColumn 职责列被整列删掉/截断时拦下（散文内容不比对，空列可判）。
func TestRunRejectsEmptyDescColumn(t *testing.T) {
	fixtureRepo(t, pkgTable("| `alpha` | 3 | 无 |  |"), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "职责列为空") {
		t.Fatalf("应报「职责列为空」，实得 %v", err)
	}
}

// TestRunCatchesScaleMisstatement 量级误述必须报出来（这道门禁的存在理由）。
//
// 用 100 行 vs README 写 3 行：偏差 97%，远越 25% 容差。反向的那条（容差内不报）
// 由 TestLocDeviationToleratesLineLevelNoise 钉着，两者互为反面。
func TestRunCatchesScaleMisstatement(t *testing.T) {
	body := "package alpha\n" + strings.Repeat("\n// 填充\n", 50)
	fixtureRepo(t, pkgTable("| `alpha` | 3 | 无 | 示例包 |"), map[string]string{
		"alpha/a.go": body,
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "超过容差") {
		t.Fatalf("应报「超过容差」，实得 %v", err)
	}
}

// TestRunCatchesDependencyDrift 依赖列与实际 import 不符必须报出来。
//
// 两个方向都写：README 少写（实际有 import）与 README 多写（实际没有）。
// 只测一个方向时，「比对恒为真」的错实现也能过。
func TestRunCatchesDependencyDrift(t *testing.T) {
	under := "package alpha\n\nimport _ \"github.com/oekit/pkg/errors\"\n"
	fixtureRepo(t, pkgTable("| `alpha` | 3 | 无 | 示例包 |"), map[string]string{
		"alpha/a.go": under,
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "依赖写") {
		t.Fatalf("README 少写依赖时应报「依赖写」，实得 %v", err)
	}

	// 换一份：README 多写了一个并不存在的依赖。
	fixtureRepo(t, pkgTable("| `alpha` | 3 | oekit/pkg | 示例包 |"), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
	})
	err = run()
	if err == nil || !strings.Contains(err.Error(), "依赖写") {
		t.Fatalf("README 多写依赖时应报「依赖写」，实得 %v", err)
	}
}

// TestRunCatchesUnlistedPackage 反向：仓里有包、README 未收录，同样是漂移。
//
// 少了这条，往仓里加包时 README 可以一直不写——正向比对只查 README 提到的包，
// 新包根本不在它的视野里。
func TestRunCatchesUnlistedPackage(t *testing.T) {
	fixtureRepo(t, pkgTable("| `alpha` | 3 | 无 | 示例包 |"), map[string]string{
		"alpha/a.go": "package alpha\n\nvar X = 1\n",
		"beta/b.go":  "package beta\n",
		"gamma/g.go": "package gamma\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "README 包表未收录") {
		t.Fatalf("应报「README 包表未收录」，实得 %v", err)
	}
}

// TestRunRejectsPackageWithoutProductionFiles 表里提到的包只有 _test.go：
// 「没有生产文件」不能当成「0 行」通过——那会让这条记录永远绿着一条无效声明。
func TestRunRejectsPackageWithoutProductionFiles(t *testing.T) {
	fixtureRepo(t, pkgTable("| `alpha` | 0 | 无 | 只有测试的包 |"), map[string]string{
		"alpha/a_test.go": "package alpha\n",
	})
	err := run()
	if err == nil || !strings.Contains(err.Error(), "没有生产 .go 文件") {
		t.Fatalf("应报「没有生产 .go 文件」，实得 %v", err)
	}
}
