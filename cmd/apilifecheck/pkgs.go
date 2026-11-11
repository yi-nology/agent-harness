// pkgs.go —— 生产包枚举与包注释生命周期标记扫描。
//
// 「生产包」口径（与 README 包表 / policy modules 刻意不完全同源）：
// 仓库内**每个**含生产 .go 文件的目录都算，包括 internal/、examples/、
// 嵌套子包（taskboard/boardtest）。唯一排除的是 cmd/——命令是交付产物
// （编译进二进制），不是宿主 import 的 API 面，生命周期承诺对它无意义；
// 可导入的门禁执行体（archcheck）不在 cmd/ 下，不受此排除影响。
// 排除一条目录必须在这里写明理由，不允许调用方传参放宽。

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// markerRe 生命周期标记行。约定：包注释（package doc）里恰好一行
// `// 生命周期: <级别>`。全角冒号同样接受——人写中文注释时顺手打全角
// 是常态，判据别跟输入法较劲。
var markerRe = regexp.MustCompile(`^生命周期[:：]\s*(Experimental|Stable|Deprecated)\b`)

// DiscoverPackages 枚举全部生产包目录（相对路径，字典序）。
func DiscoverPackages(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // 遍历失败必须响：读不了的子树等于「没判过」
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		// 统一成 / 分隔：清单键、报告与测试断言全用斜杠形态，
		// windows 的 filepath.Rel 返回 \ 分隔，不归一就是「同一个包两个名字」。
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".") || base == "node_modules" ||
			base == "dist" || base == "testdata" {
			return filepath.SkipDir
		}
		if rel == "cmd" || rel == "site" {
			return filepath.SkipDir // 理由见文件头注
		}
		prod, prodErr := hasProdGo(path)
		if prodErr != nil {
			return prodErr
		}
		if prod {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// hasProdGo 目录里是否有非 _test.go 的 .go 文件。
func hasProdGo(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		return true, nil
	}
	return false, nil
}

// PkgInfo 一个包扫描出来的结果。
type PkgInfo struct {
	Dir string
	// MarkerLevel 包注释里判到的级别；空 = 没有标记。
	MarkerLevel string
	// MarkerFiles 带标记的文件（长度必须为 1，见 checkMarkers）。
	MarkerFiles []string
	// Imports 全部生产文件的 import 路径（follows_upstream 校验用）。
	Imports []string
}

// ScanPackage 解析一个包的全部生产文件，提取包注释标记与 import 面。
//
// 解析失败必须响亮失败（gate 退出码 2）：标记扫描依赖语法树，
// 解析不出就没有结论，静默跳过等于「这个包没有标记」。
func ScanPackage(root, dir string) (*PkgInfo, error) {
	info := &PkgInfo{Dir: dir}
	files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	seenImports := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, perr := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if perr != nil {
			return nil, fmt.Errorf("解析 %s: %w", f, perr)
		}
		if af.Doc != nil {
			if marker, ok := docMarker(af.Doc); ok {
				info.MarkerLevel = marker
				info.MarkerFiles = append(info.MarkerFiles, filepath.Base(f))
			}
		}
		for _, imp := range af.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil || seenImports[p] {
				continue
			}
			seenImports[p] = true
			info.Imports = append(info.Imports, p)
		}
	}
	sort.Strings(info.Imports)
	return info, nil
}

// docMarker 在包注释里找生命周期标记行。
//
// 逐原始行判而不走 doc.Text()：Text 会剥指令、重排缩进，标记行必须
// **逐字**匹配约定，宽松重排会让「看着像、判不出」的形态混进来。
func docMarker(doc *ast.CommentGroup) (string, bool) {
	for _, c := range doc.List {
		text := strings.TrimPrefix(c.Text, "//")
		text = strings.TrimPrefix(text, "/")
		text = strings.TrimSpace(text)
		if m := markerRe.FindStringSubmatch(text); m != nil {
			return m[1], true
		}
	}
	return "", false
}
