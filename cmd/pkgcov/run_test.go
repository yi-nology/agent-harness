package main

import (
	"bytes"
	"strings"
	"testing"
)

// run 的用例组：这三条判据最贵的部分都只体现为「退出码非零」，
// 而 os.Exit 会把它们挡在用例之外——故 main() 只做一件事：把 run 的返回值交给 os.Exit。

// TestRunGreen 是**正向**用例：一组真实形态的输入 + 正确的豁免 ⇒ 0 且打印摘要。
//
// 它是「成对」里的另一半：没有它，一个永远 exit 1 的实现能通过下面所有红用例。
func TestRunGreen(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{
		"-min", "70",
		"-exempt", "beta",
		"-no-test-exempt", "cmd/tool,cmd/fixture,cmd/plain",
	}, strings.NewReader(syntheticLog), &out, &errOut)
	if code != 0 {
		t.Fatalf("应放行（code=0），实际 code=%d，stderr:\n%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "✓") {
		t.Fatalf("stdout 应打印绿色摘要，实际: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("放行时不该往 stderr 写东西，实际: %q", errOut.String())
	}
}

// TestRunEmptyInputFails 锁死「空输入 = 判据没跑起来」。
//
// 把「没有输入」当成「没有违规」是最典型的静默变绿：管道接错、go test 提前失败、
// 日志被别的阶段覆盖，都会让这里收到空输入，而门禁照样打印 ✓。
func TestRunEmptyInputFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, strings.NewReader(""), &out, &errOut)
	if code == 0 {
		t.Fatalf("空输入必须非零退出，实际 code=0，stdout: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "判据没跑起来") {
		t.Fatalf("应说明「判据没跑起来」，实际: %q", errOut.String())
	}
}

// TestRunBelowFloorFails 锁死「低于下限 ⇒ 非零 + stdout 保持干净」。
//
// stdout 必须是干净的：make 的成功路径靠它打印一行摘要，失败信息一律走 stderr，
// 否则「红」与「绿」在同一路输出里混着，grep 一下就会误判。
func TestRunBelowFloorFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{
		"-min", "70",
		"-no-test-exempt", "cmd/tool,cmd/fixture,cmd/plain",
	}, strings.NewReader(syntheticLog), &out, &errOut)
	if code == 0 {
		t.Fatal("beta 只有 64.5%，必须非零退出")
	}
	if !strings.Contains(errOut.String(), "低于逐包下限") {
		t.Fatalf("应指名低于下限，实际: %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("失败时 stdout 不该有输出，实际: %q", out.String())
	}
}

// TestRunBadFlagReturns2 锁死参数错误的退出码（与 flag 包的约定一致）。
//
// 这一条防的是「参数写错被当成默认值继续跑」：`-min abc` 若被静默忽略，
// 门禁就会用一个没人声明的阈值判定，而报告里看不出任何异常。
func TestRunBadFlagReturns2(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-min", "abc"}, strings.NewReader(syntheticLog), &out, &errOut)
	if code != 2 {
		t.Fatalf("参数错误应返回 2，实际 %d", code)
	}
}

// TestRunParseErrorFails 锁死「形态漂移 ⇒ 非零」。
func TestRunParseErrorFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, strings.NewReader("skip\texample.com/m/x\tcoverage: 50.0% of statements"), &out, &errOut)
	if code == 0 {
		t.Fatal("无法归类的 coverage 行必须非零退出（跳过即变绿）")
	}
	if !strings.Contains(errOut.String(), "读取 go test 输出失败") {
		t.Fatalf("应说明解析失败，实际: %q", errOut.String())
	}
}
