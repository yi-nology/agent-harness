// Command docsync 校验 README 的包清单表与仓库实际状态一致。
//
// # 为什么要有这个命令
//
// README 的包表是本仓唯一的对外规模声明，历史上长期手工维护，已经漂到
// 「netguard 声称 ~86 行 / 纯 stdlib 叶子层」而实际是 412 行且依赖 guardx
// 这种程度——而 make check 完全不覆盖文档，漂移没有任何信号。
//
// 按本仓既有的门禁口径（见 architecture-policy.yaml 头注：门禁不认文档里
// 说过要迁，不写进机器约束的约定半年后必然回退），这张表也必须有机器校验。
//
// # 判定口径
//
//   - 生产行数：pkg/*.go 且不含 _test.go（表头已声明该口径）
//   - 外部依赖：生产代码 import 的仓外模块，取 module 前两段归并
//   - 本仓自身 import（github.com/yi-nology/agent-harness/...）不算外部依赖
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// row 解析出来的一行包表记录。
type row struct {
	pkg   string
	loc   int
	deps  []string
	desc  string
	table string // 原始整行，供错误信息定位
}

var (
	// 表头：| `pkg` | loc | deps | 职责 |
	rowRe = regexp.MustCompile("^\\|\\s*`([a-z]+)`\\s*\\|\\s*([0-9]+)\\s*\\|\\s*([^|]*?)\\s*\\|\\s*(.*?)\\s*\\|$")
	// 候选：任意不带空白的带引号 token。像不像 module 路径交给 looksLikeModulePath
	// 判断——用正则硬表达「排除 scheme:// 与相对路径」既写不准又难维护。
	//
	// 这里不能图省事放宽成「含点含斜杠就算」：测试与注释里全是 URL 字面量
	// （http://127.0.0.1:8900/api、//169.254.169.254/latest）与相对路径
	// （../escape），放宽即把它们全当成依赖，门禁直接失效。
	importRe = regexp.MustCompile(`"([^"\s]+)"`)
	// 外部依赖判定：第二段起表示子路径；仓内自身的 module 不算外部。
	selfModule = "github.com/yi-nology/agent-harness"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "docsync: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	readme := "README.md"
	f, err := os.Open(readme)
	if err != nil {
		return err
	}
	defer f.Close()

	var rows []row
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := rowRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		loc, convErr := strconv.Atoi(m[2])
		if convErr != nil {
			return fmt.Errorf("包 %s 的生产行数不是整数: %q", m[1], m[2])
		}
		rows = append(rows, row{
			pkg:   m[1],
			loc:   loc,
			deps:  splitDeps(m[3]),
			desc:  m[4],
			table: sc.Text(),
		})
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("README.md 里没解析到任何包表行——表结构变了？")
	}

	var problems []string
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.pkg] {
			problems = append(problems, fmt.Sprintf("%s: 包表里出现重复行", r.pkg))
			continue
		}
		seen[r.pkg] = true

		wantLoc, wantDeps, err := measure(r.pkg)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if r.loc != wantLoc {
			problems = append(problems, fmt.Sprintf(
				"%s: README 写 %d 行，实际 %d 行", r.pkg, r.loc, wantLoc))
		}
		if !equalDeps(r.deps, wantDeps) {
			problems = append(problems, fmt.Sprintf(
				"%s: README 依赖写 %q，实际 %q", r.pkg,
				join(r.deps), join(wantDeps)))
		}
	}

	// 反向：仓里有包、README 没写，同样是漂移。
	onDisk, err := listPackages()
	if err != nil {
		return err
	}
	for _, p := range onDisk {
		if !seen[p] {
			problems = append(problems, fmt.Sprintf(
				"%s: 仓里有该包，README 包表未收录", p))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("README 包表与仓库实际状态不一致（%d 项）：\n  - %s\n"+
			"修法：按各包 *.go（不含 _test.go）重算行数与外部依赖，改 README 包表。\n"+
			"本门禁不自动改文档——改表是要人判断的动作。",
			len(problems), strings.Join(problems, "\n  - "))
	}

	fmt.Printf("README 包表与仓库一致（%d 个包）✓\n", len(rows))
	return nil
}

// measure 实测一个包的生产行数与外部依赖。
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
		loc += strings.Count(string(b), "\n")
		for _, m := range importRe.FindAllStringSubmatch(string(b), -1) {
			path := m[1]
			if isSelfPath(path) {
				continue // 本仓自身不算外部依赖
			}
			if !looksLikeModulePath(path) {
				continue // URL 字面量、相对路径、纯 stdlib 标识等
			}
			depSet[normalizeDep(path)] = true
		}
	}
	deps := make([]string, 0, len(depSet))
	for d := range depSet {
		deps = append(deps, d)
	}
	sort.Strings(deps)
	return loc, deps, nil
}

// isSelfPath 判断路径是否指向本仓自身（含其子包）。
//
// 必须带 / 边界：纯 strings.HasPrefix(path, selfModule) 会把
// agent-harness-fork 这类**真实的外部依赖**当成自身而静默排除——
// 那是漏判方向，与门禁职责正好相反，且无声无息。
func isSelfPath(p string) bool {
	return p == selfModule || strings.HasPrefix(p, selfModule+"/")
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
	sort.Strings(out)
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
