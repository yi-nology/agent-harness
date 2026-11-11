// sentinels_test.go —— 哨兵/错误类型提取的判定。

package main

import (
	"strings"
	"testing"
)

func TestExtractAllSentinels(t *testing.T) {
	root := writeMiniRepo(t)
	dirs, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("DiscoverPackages: %v", err)
	}
	sents, types, err := extractAllSentinels(root, dirs)
	if err != nil {
		t.Fatalf("extractAllSentinels: %v", err)
	}

	// alpha 两个哨兵（var 块 errors.New + 显式 error 类型；别名 import 命中；
	// 非 errors.New 变量不计），beta/deep 无哨兵。
	var got []string
	for _, s := range sents {
		got = append(got, s.Pkg+"."+s.Name)
	}
	want := []string{"alpha.ErrBoom", "alpha.ErrWrap"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("哨兵 got %v, want %v", got, want)
	}

	var gotT []string
	for _, ty := range types {
		gotT = append(gotT, ty.Pkg+"."+ty.Name)
	}
	if strings.Join(gotT, ",") != "alpha.BoomErr" {
		t.Fatalf("错误类型 got %v, want [alpha.BoomErr]", gotT)
	}
}

func TestPackageSentinelsForms(t *testing.T) {
	// 六种书写形态与两类排除项（stdlib errors 别名、fmt.Errorf 不算哨兵）。
	src := `package p

import (
	stderr "errors"
	"fmt"
	oerr "github.com/oekit/pkg/errors"
)

var (
	A = oerr.New("a")
	B error = oerr.New("b")
	C = stderr.New("stdlib 也算 errors 语义包")
	D = fmt.Errorf("fmt 不算哨兵: %d", 1)
	E = "普通变量"
	F = other()
)

func other() error { return nil }
`
	// 逐形态断言走 parseFile 路径（不复用 fixture，隔离书写形态）。
	dir := t.TempDir()
	writeFile(t, dir, "p/p.go", src)
	dirs := []string{"p"}
	sents, types, err := extractAllSentinels(dir, dirs)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var got []string
	for _, s := range sents {
		got = append(got, s.Name)
	}
	// A/B/oekit 别名命中；C 的 stdlib errors 也命中（errors 语义包，本仓门禁
	// 只是不允许用它，判据面仍认它是哨兵）；D/E/F 不命中。
	if strings.Join(got, ",") != "A,B,C" {
		t.Fatalf("哨兵形态判定 got %v, want [A,B,C]", got)
	}
	if len(types) != 0 {
		t.Fatalf("不该有错误类型: %v", types)
	}
}

func TestPackageErrorTypesReceivers(t *testing.T) {
	src := `package p

type PtrErr struct{}
type ValErr struct{}
type hiddenErr struct{}
type NotErr struct{}

func (e *PtrErr) Error() string  { return "p" }
func (e ValErr) Error() string   { return "v" }
func (e *hiddenErr) Error() string { return "h" }
func (n *NotErr) Method() string { return "n" }
`
	dir := t.TempDir()
	writeFile(t, dir, "p/p.go", src)
	sents, types, err := extractAllSentinels(dir, []string{"p"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(sents) != 0 {
		t.Fatalf("不该有哨兵: %v", sents)
	}
	var got []string
	for _, ty := range types {
		got = append(got, ty.Name)
	}
	// 指针/值接收者都算；未导出类型不算；有方法但无 Error 的不算。
	if strings.Join(got, ",") != "PtrErr,ValErr" {
		t.Fatalf("错误类型 got %v, want [PtrErr,ValErr]", got)
	}
}

func TestExtractParseError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad/bad.go", "package bad\nfunc {{{\n")
	if _, _, err := extractAllSentinels(dir, []string{"bad"}); err == nil {
		t.Fatal("解析失败未报错")
	}
}

func TestIsErrorsPath(t *testing.T) {
	yes := []string{"errors", "github.com/oekit/pkg/errors", "github.com/pkg/errors", "x/y/errors"}
	no := []string{"errorx", "github.com/oekit/pkg/errorx", "fmt", "errors/v2"}
	for _, p := range yes {
		if !isErrorsPath(p) {
			t.Errorf("%q 应命中", p)
		}
	}
	for _, p := range no {
		if isErrorsPath(p) {
			t.Errorf("%q 不应命中", p)
		}
	}
}
