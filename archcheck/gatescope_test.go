// gatescope_test.go —— 「声明指向空气」与「遍历失败被吞」两类门禁变绿的反向验证。
//
// # 为什么这两类必须固化成用例
//
// 判据再准，也只在**它真的看到了东西**时才有意义。有两件事会让它看不到：
//
//  1. 声明面指向不存在的目录（rules[].scope / thresholds.scan_roots），而遍历侧
//     对不存在的路径静默跳过；
//  2. 遍历本身失败（读不了那棵子树 / 读不了那个文件），而错误被 _ = Walk(...)
//     吞成「零个文件」「零个超长文件」。
//
// 两者失效形态完全一致：报告里写着「无阻断级违反」，真实情况是**一条都没判**。
// 这不是误报（误报会有人抱怨、会被修），是假绿——没人会去看。
//
// 每条用例都写成「错误实现会红」：退回旧实现（静默跳过 / 吞掉错误）时，
// 下面每一条都必须失败。

package archcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIterFilesFailsLoudlyOnWalkError 遍历失败不得被吞成「零个文件」。
//
// 构造方式刻意**不用权限位**：把 root 指到「中间路径是普通文件」的位置，
// Lstat 必然返回 ENOTDIR。这条路在 root 用户与 CI 上表现一致，而 chmod 000
// 在 root 下会被绕过——那样的用例是假绿（它证明不了任何事）。
func TestIterFilesFailsLoudlyOnWalkError(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"blocker/a.go": "package pkg\n"})
	bad := filepath.Join(root, "blocker", "a.go", "nested") // a.go 是文件，这里当目录用

	files, err := iterFiles(bad, nil, false)
	if err == nil {
		t.Fatalf("遍历失败必须报错（否则那棵子树对门禁隐形，报告却写着通过），实得 %d 个文件、err=nil", len(files))
	}
	if !strings.Contains(err.Error(), "门禁给不出结论") {
		t.Fatalf("错误文案要说明「给不出结论」，实得 %q", err.Error())
	}

	// 反向：正常路径不得报错，也不得漏文件——改错误处理不能把 Happy path 弄坏。
	okFiles, err := iterFiles(filepath.Join(root, "blocker"), []string{"*.go"}, true)
	if err != nil {
		t.Fatalf("正常遍历不得报错: %v", err)
	}
	if len(okFiles) != 1 {
		t.Fatalf("正常遍历应拿到 1 个文件，实得 %d", len(okFiles))
	}
}

// TestStaleRuleScopeIsBlocking 规则 scope 指向不存在的目录时，门禁必须红。
//
// 这是「门禁变绿」最直接的一种：规则在、severity 是 error、报告里也计入
// 「N 条规则」，但它一行都没扫——而**它本该拦下的违规就躺在仓里**。
//
// 用例钉住两半，缺一不可：① scope 正确时那条违规真的被拦（证明夹具里的违规
// 是真的，不是「什么都没判也恰好没红」）；② scope 写错时必须由 policy 自校验
// 报出（证明「写错」不会被当成「没违规」）。
func TestStaleRuleScopeIsBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"pkg/a.go": "package pkg\n\nvar forbidden = \"bad_token\"\n",
	})
	rule := Rule{
		ID:          "t-forbidden",
		Severity:    "error",
		Scope:       []string{"pkg"},
		Include:     []string{"*.go"},
		IgnoreTest:  true,
		ForbidRegex: "bad_token",
	}

	// ① scope 正确：违规必须被拦下。
	res, err := Run(root, &Policy{Rules: []Rule{rule}})
	if err != nil {
		t.Fatalf("门禁不得报错: %v", err)
	}
	if res.Blocking == 0 {
		t.Fatal("scope 正确时那条违规必须被拦下——否则下面「scope 写错」的对照说明不了任何事")
	}

	// ② scope 指向不存在的目录：旧实现返回 0 阻断（门禁变绿），没有任何信号。
	staleRule := rule
	staleRule.Scope = []string{"ghost"}
	res2, err := Run(root, &Policy{Rules: []Rule{staleRule}})
	if err != nil {
		t.Fatalf("陈旧 scope 应被**报成违反**（可定位、点名规则 id），而不是让整轮门禁给不出结论: %v", err)
	}
	if res2.Blocking == 0 {
		t.Fatal("规则 scope 指向不存在的目录时必须阻断：它一行都不判，却仍计入「N 条规则」")
	}
	var located bool
	for _, v := range res2.Violations {
		if v.Rule == selfCheckRuleID && strings.Contains(v.Text, "ghost") {
			located = true
		}
	}
	if !located {
		t.Fatalf("违规必须由 %s 报出并点名 ghost，实得 %+v", selfCheckRuleID, res2.Violations)
	}
}

// TestStaleScanRootIsBlocking thresholds.scan_roots 指向不存在的目录时同样必须红。
//
// 与上一条互补而不是重复：modules.roots 的检查只比**字符串前缀**（「有没有人
// 该扫这个包」），路径写错 / 包迁走后它照样能过；这一条问的是「清单里的每一项
// 是不是真的扫到了东西」。
func TestStaleScanRootIsBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pkg/a.go": "package pkg\n"})

	vs := selfCheckViolations(root, &Policy{Thresholds: Threshold{ScanRoots: []string{"ghost"}, MaxFileLines: 400}})
	var found bool
	for _, v := range vs {
		if v.Rule == selfCheckRuleID && strings.Contains(v.Text, "scan_roots") && strings.Contains(v.Text, "ghost") {
			found = true
		}
	}
	if !found {
		t.Fatalf("scan_roots 指向不存在的目录必须被报出（否则那棵树对两条阈值隐形），实得 %+v", vs)
	}

	// 反向：真实目录不得误报——误报会让人关掉这条判据。
	if vs := selfCheckViolations(root, &Policy{Thresholds: Threshold{ScanRoots: []string{"pkg"}, MaxFileLines: 400}}); len(vs) != 0 {
		t.Fatalf("scan_roots 指向真实目录时不得报错，实得 %+v", vs)
	}
}

// TestThresholdsFailLoudlyOnReadError 阈值扫描读不了文件时必须报错，不得算作「没超限」。
//
// 构造用**悬空符号链接**：Lstat 成功（形态上是 .go 文件），ReadFile 失败。
// 与上一条同理，不用权限位——chmod 000 在 root 下不生效，那样的用例是假绿。
func TestThresholdsFailLoudlyOnReadError(t *testing.T) {
	prepare := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"pkg/a.go": "package pkg\n"})
		target := filepath.Join(root, "pkg", "a.go")
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "missing-target"), target); err != nil {
			t.Skipf("本环境不支持符号链接: %v", err)
		}
		return root
	}

	root := prepare(t)
	if _, err := longFilesReport(root, []string{"pkg"}, 400); err == nil {
		t.Fatal("读不了的文件必须让「超长文件」判据报错：旧实现静默返回「没有超长文件」")
	}
	if _, err := godObjects(root, []string{"pkg"}, 25, defaultForwardingShellMaxChars); err == nil {
		t.Fatal("读不了的文件必须让「god object」判据报错：旧实现静默跳过该文件（方法数被低估）")
	}
}
