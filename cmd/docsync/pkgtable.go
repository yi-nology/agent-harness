// pkgtable.go —— 包表的一侧：实测与比对（run() 的另一半见 main.go，
// 工程底座表的判定见 baseline.go）。
//
// 与 main.go 分开是因为一个文件装两件事会撞 max_file_lines（400）。拆的时候
// 按**判据的一侧**切，不是按行数切：这里处理的都是「包 → 行数 / 外部依赖」的
// 实测与集合比对，main.go 只留流程（读 README → 逐行判定 → 汇总问题）。
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func measure(pkg string) (int, []string, error) {
	matches, err := filepath.Glob(filepath.Join(pkg, "*.go"))
	if err != nil {
		return 0, nil, err
	}
	var prod []string
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		prod = append(prod, f)
	}
	if len(prod) == 0 {
		return 0, nil, fmt.Errorf("%s: 目录下没有生产 .go 文件", pkg)
	}

	loc := 0
	depSet := map[string]bool{}
	for _, f := range prod {
		b, readErr := os.ReadFile(f)
		if readErr != nil {
			return 0, nil, readErr
		}
		loc += countLines(string(b))
		// 依赖取自**真正的 import 声明**（go/ast），不是从源码文本里扫引号内容。
		//
		// 旧实现用正则扫全文，于是**文档注释里的示例路径**会被当成真依赖：
		// 写一句 `forbid_import_regex: "example.com/demo/internal/(engine|service)"`
		// 就会让 README 的依赖列多出一项「demo/internal」——那不是依赖，是举例。
		// 这与本仓 archcheck 包当年的同一个病（import 声明有六种以上书写形态，
		// 逐行正则必然漏）同源，故同样走语法树。
		paths, perr := importPaths(f)
		if perr != nil {
			return 0, nil, perr
		}
		for _, path := range paths {
			if isSelfPath(path) {
				continue // 本仓自身不算外部依赖
			}
			if !looksLikeModulePath(path) {
				continue // 纯 stdlib 标识等
			}
			depSet[normalizeDep(path)] = true
		}
	}
	deps := make([]string, 0, len(depSet))
	for d := range depSet {
		deps = append(deps, d)
	}
	slices.Sort(deps)
	return loc, deps, nil
}

// importPaths 取一个文件里真正 import 的路径。
//
// 解析失败必须响亮失败：依赖列是从 import 声明推出来的，推不出来就没有结论，
// 静默跳过等于「这个包的依赖列是空的」。
func importPaths(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败（依赖列取自 import 声明，解析不出来就没有结论）: %w", path, err)
	}
	var out []string
	for _, im := range f.Imports {
		p, uerr := strconv.Unquote(im.Path.Value)
		if uerr != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// isSelfPath 判断路径是否指向本仓自身（含其子包）。
//
// 必须带 / 边界：纯 strings.HasPrefix(path, selfModule) 会把
// goagent-kernel-fork 这类**真实的外部依赖**当成自身而静默排除——
// 那是漏判方向，与门禁职责正好相反，且无声无息。
func isSelfPath(p string) bool {
	return p == selfModule || strings.HasPrefix(p, selfModule+"/")
}

// countLines 数真实行数。
//
// 单纯 strings.Count(s, "\n") 只在**每个文件都以换行结尾**时等于行数；某个文件
// 丢了结尾换行会被少算一行，于是「改了文件并把 README 同步成少算的值」就能
// 蒙混过关——指标自指，让门禁在自己改动的量纲上失准。
//
// 末尾无换行时补计残行。对以换行结尾的文件（当前全部生产文件都是）与旧口径
// 完全一致，故本修正不改今天的任何数字，只堵住将来的那个洞。
func countLines(s string) int {
	n := strings.Count(s, "\n")
	if n > 0 && !strings.HasSuffix(s, "\n") {
		n++
	} else if n == 0 && s != "" {
		n = 1 // 整个文件就是一行且没有换行
	}
	return n
}

// looksLikeModulePath 判断一个带引号 token 是否长得像 Go module 路径。
//
// 判据（逐条都对应仓里真实存在、且必须被排除的东西）：
//   - 不能以 . 或 / 开头：排除相对路径 "../escape" 与协议相对 "//host/x"；
//   - 第一段不能含 ':'：排除带 scheme 的 URL（http://、https://），
//     否则第一段会切成 "http:" 而被误当宿主名；
//   - 第一段必须含 '.'：module 路径带 module host（github.com / gopkg.in /
//     mvdan.cc），纯 stdlib 标识（context、runtime）没有；
//   - 必须还有子路径：module 路径至少两段。
//
// 不要求全小写——Go 允许大写（代理里编码为 !x），限定小写会让含大写的依赖
// 整个漏掉，而漏判方向与门禁职责相反。
func looksLikeModulePath(p string) bool {
	if strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/") {
		return false
	}
	host, _, ok := strings.Cut(p, "/")
	if !ok || !strings.Contains(host, ".") || strings.Contains(host, ":") {
		return false
	}
	return true
}

// normalizeDep 把 module 路径归并到「owner/repo」两段——
// github.com/oekit/pkg 与 github.com/oekit/logx 同属 oekit；
// santhosh-tekuri/jsonschema/v6 没有 module host，第一段就是 owner。
//
// host 的判据是「第一段含点且至少还有两段」：github.com / gopkg.in / mvdan.cc
// 都带点，而 owner 名（santhosh-tekuri、oagentkit）不带点。
func normalizeDep(path string) string {
	parts := strings.Split(path, "/")
	rest := parts
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		rest = parts[1:] // 去掉 module host（github.com/x → x）
	}
	if len(rest) < 2 {
		return rest[0]
	}
	return rest[0] + "/" + rest[1]
}

// listPackages 列出一级子目录中的 Go 包（以含 .go 文件为准）。
func listPackages() ([]string, error) {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files, globErr := filepath.Glob(filepath.Join(e.Name(), "*.go"))
		if globErr != nil || len(files) == 0 {
			continue
		}
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out, nil
}

// splitDeps 拆依赖列：顿号或逗号分隔，「无」视为空。
//
// **不排序**：比对是按位置对齐的（README 须按实测的字典序书写），
// 排序会打乱位置对应关系，让后缀匹配失去意义。
func splitDeps(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "无" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '、' || r == ',' || r == '，'
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func join(s []string) string {
	if len(s) == 0 {
		return "无"
	}
	return strings.Join(s, "、")
}

// equalDeps 比对依赖集合。
//
// 允许 README 用人类可读的简写：「jsonschema」匹配「santhosh-tekuri/jsonschema」，
// 「goja」匹配「dop251/goja」。包表是给人看的，硬要求 owner/repo 全名只会逼着
// 写噪音。判定按**路径后缀**——README 条目是实测依赖串的后缀即算匹配，
// 既容忍简写，又不会把一个真依赖判成没有（「无」不匹配任何非空依赖）。
func equalDeps(readme, actual []string) bool {
	if len(readme) != len(actual) {
		return false
	}
	for i, want := range actual {
		if !suffixMatch(readme[i], want) {
			return false
		}
	}
	return true
}

// suffixMatch a 是否为 b 的路径后缀（a 短或相等即算）。
func suffixMatch(a, b string) bool {
	if a == b {
		return true
	}
	return len(a) < len(b) && strings.HasSuffix(b, "/"+a)
}
