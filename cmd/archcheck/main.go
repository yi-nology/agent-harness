// Command archcheck 执行 architecture-policy.yaml 的可执行面。
//
// 存在意义：policy 文件若只当文档读，半年后就与代码事实脱节；把它做成
// `make architecture-check` 门禁，规则违反即非零退出。本仓的头号红线是
// harness-no-domain-vocabulary——内核不得长出领域知识。
//
// 正则一律 RE2（Go 原生不支持 (?!...)）；负向前置如需表达，用结构化
// negative_follow 字段，不写进正则。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// methodRe 匹配方法定义（排除普通函数与接口声明）：`func (recv T) Name(...)`
var methodRe = regexp.MustCompile(`^func\s+\(([^)]*)\)\s+[A-Za-z_]\w*`)

type negFollow struct {
	Anchor       string `yaml:"anchor"`
	ForbidFollow string `yaml:"forbid_follow"`
}

type rule struct {
	ID           string      `yaml:"id"`
	Desc         string      `yaml:"desc"`
	Severity     string      `yaml:"severity"` // error（默认，违反即非零退出）| warn（报告口径，不阻断）
	Scope        []string    `yaml:"scope"`
	ScopeExclude []string    `yaml:"scope_exclude"`
	Include      []string    `yaml:"include"`
	IgnoreTest   bool        `yaml:"ignore_test"`
	OnlyTest     bool        `yaml:"only_test"` // 只扫测试文件（配合 severity: warn 做夹具体检）
	ForbidImport string      `yaml:"forbid_import_regex"`
	ForbidRegex  string      `yaml:"forbid_regex"`
	NegFollow    []negFollow `yaml:"negative_follow"`
}

type threshold struct {
	MaxFileLines      int      `yaml:"max_file_lines"`
	MaxMethodsPerType int      `yaml:"max_methods_per_type"`
	ScanRoots         []string `yaml:"scan_roots"`
	Enforce           bool     `yaml:"enforce"` // true=超限即非零退出；false=仅报告
}

type policyException struct {
	Rule      string `yaml:"rule"`
	Path      string `yaml:"path"`
	Line      int    `yaml:"line"`
	Violation string `yaml:"violation"`
	Reason    string `yaml:"reason"`
	Until     string `yaml:"until"`
	Removal   string `yaml:"removal"`
}

type policyFile struct {
	Version    int               `yaml:"version"`
	Modules    []struct{}        `yaml:"modules"`
	Rules      []rule            `yaml:"rules"`
	Thresholds threshold         `yaml:"thresholds"`
	Exceptions []policyException `yaml:"exceptions"`
}

type violation struct {
	rule string
	path string
	line int
	text string
	kind string
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "architecture-policy.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 architecture-policy.yaml（从 %s 向上查找失败）", dir)
		}
		dir = parent
	}
}

// iterFiles 遍历 root 下匹配 include 的文件；ignoreTest 时跳过 _test.go。
func iterFiles(root string, includes []string, ignoreTest bool) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			// 跳过隐藏目录与构建产物目录（与宿主仓 iterFiles 同口径）。
			if path != root && (strings.HasPrefix(base, ".") || base == "node_modules" || base == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		// 跳过隐藏文件（编辑器/工具残留如 `.!36914!guard.go`：Go 工具链同样忽略，
		// 门禁不应为其误报——否则噪声会让人习惯性忽略门禁输出）。
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		if ignoreTest && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if len(includes) == 0 {
			out = append(out, path)
			return nil
		}
		for _, pat := range includes {
			ok, _ := filepath.Match(pat, filepath.Base(path))
			if ok {
				out = append(out, path)
				break
			}
		}
		return nil
	})
	return out
}

// neutralized 判断命中位置是否落在 negative_follow 豁免内。
func neutralized(text string, start, end int, nfs []negFollow) bool {
	for _, nf := range nfs {
		a := strings.Index(text, nf.Anchor)
		if a < 0 || a > start {
			continue
		}
		rest := text[a+len(nf.Anchor):]
		if strings.HasPrefix(rest, nf.ForbidFollow) {
			return true
		}
	}
	return false
}

func checkRule(root string, r rule) ([]violation, error) {
	var re *regexp.Regexp
	if r.ForbidImport != "" {
		var err error
		if re, err = regexp.Compile(r.ForbidImport); err != nil {
			return nil, fmt.Errorf("规则 %s forbid_import_regex 非法: %w", r.ID, err)
		}
	} else if r.ForbidRegex != "" {
		var err error
		if re, err = regexp.Compile(r.ForbidRegex); err != nil {
			return nil, fmt.Errorf("规则 %s forbid_regex 非法: %w", r.ID, err)
		}
	} else {
		return nil, nil // 声明面字段：规则只带说明，无可执行体
	}

	var files []string
	for _, scope := range r.Scope {
		p := filepath.Join(root, scope)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		for _, f := range iterFiles(p, r.Include, r.IgnoreTest) {
			rel, _ := filepath.Rel(root, f)
			skip := false
			for _, ex := range r.ScopeExclude {
				if rel == ex || strings.HasPrefix(rel, ex+string(filepath.Separator)) {
					skip = true
					break
				}
			}
			if !skip {
				files = append(files, f)
			}
		}
	}

	var out []violation
	for _, f := range files {
		isTest := strings.HasSuffix(f, "_test.go")
		if r.OnlyTest != isTest {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for i, line := range strings.Split(string(data), "\n") {
			// 注释行不判：policy 的说明性中文里出现领域词属正常。
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if neutralized(line, loc[0], loc[1], r.NegFollow) {
				continue
			}
			rel, _ := filepath.Rel(root, f)
			kind := "forbid_regex"
			if r.ForbidImport != "" {
				kind = "forbid_import"
			}
			out = append(out, violation{rule: r.ID, path: rel, line: i + 1, text: strings.TrimSpace(line), kind: kind})
		}
	}
	return out, nil
}

// firstLine 取多行说明的首行（例外回显用，避免刷屏）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func longFilesReport(root string, scanRoots []string, limit int) []string {
	var out []string
	for _, sr := range scanRoots {
		p := filepath.Join(root, sr)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 与 Go 工具链同口径：忽略 `.` / `_` 前缀文件。
			if name := info.Name(); strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if n := strings.Count(string(data), "\n"); n > limit {
				rel, _ := filepath.Rel(root, path)
				out = append(out, fmt.Sprintf("%s (%d 行 > %d)", rel, n, limit))
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// godObjects 统计「同一类型挂了多少个方法」——god object 的机械判据。
//
// 为什么需要它：一个大类型的方法数是**可自动发现**的结构债，而「大文件」不是
// （1026 行的装配函数与 1026 行的纯声明文件，性质完全不同）。方法数超阈值意味着
// 该类型同时承担了过多关注点，任何一次改动都要在它的方法全集上评估影响面。
//
// 判据：形如 `func (recv *T)` / `func (recv T)` 的方法定义，按接收者基类型名聚合。
// 只看方法定义，排除函数调用与接口声明。
func godObjects(root string, scanRoots []string, limit int) []string {
	type bucket struct {
		count int
		file  string
	}
	counts := map[string]*bucket{}
	for _, sr := range scanRoots {
		p := filepath.Join(root, sr)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 与 Go 工具链同口径：忽略 `.` / `_` 前缀文件（工具链不编译它们，
			// 门禁也不该为其统计——否则残留文件会虚增 god object 方法数）。
			if name := info.Name(); strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, line := range strings.Split(string(data), "\n") {
				m := methodRe.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				// 捕获组是完整接收者列表，形态有二：
				//   (s *Scheduler) / (svc  SessionSvc)  → 「变量名 类型」，取第 2 段
				//   (Manager[T])                          → 仅类型名，取第 1 段
				// 直接截断会得到变量名而非类型名——god object 会全部误聚到同一名字上。
				fields := strings.Fields(m[1])
				var recv string
				switch {
				case len(fields) >= 2:
					recv = fields[1]
				case len(fields) == 1:
					recv = fields[0]
				default:
					continue
				}
				recv = strings.TrimPrefix(recv, "*")
				recv = strings.TrimPrefix(recv, "[]")
				if i := strings.IndexByte(recv, '['); i > 0 { // 泛型 Manager[T] → Manager
					recv = recv[:i]
				}
				if recv == "" {
					continue
				}
				b := counts[recv]
				if b == nil {
					b = &bucket{file: rel}
					counts[recv] = b
				}
				b.count++
			}
			return nil
		})
	}
	var out []string
	for name, b := range counts {
		if b.count > limit {
			out = append(out, fmt.Sprintf("%s (%d 个方法 > %d)", name, b.count, limit))
		}
	}
	sort.Strings(out)
	return out
}

func main() {
	report := false
	for _, a := range os.Args[1:] {
		if a == "--report" {
			report = true
		}
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	data, err := os.ReadFile(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	var pol policyFile
	if err := yaml.Unmarshal(data, &pol); err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: 解析 policy 失败:", err)
		os.Exit(2)
	}

	var all []violation
	for _, r := range pol.Rules {
		vs, err := checkRule(root, r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "archcheck:", err)
			os.Exit(2)
		}
		all = append(all, vs...)
	}

	// severity 分流：error 违规阻断，warn 仅报告。
	sevOf := map[string]string{}
	for _, r := range pol.Rules {
		s := r.Severity
		if s == "" {
			s = "error"
		}
		sevOf[r.ID] = s
	}
	isBlocking := func(v violation) bool { return sevOf[v.rule] != "warn" }

	var blocking, warnings, excused int
	// exceptions 精确豁免：rule + path + line 三元组命中即放行（不做前缀模糊豁免）。
	exKey := map[string]policyException{}
	for _, ex := range pol.Exceptions {
		exKey[fmt.Sprintf("%s|%s|%d", ex.Rule, ex.Path, ex.Line)] = ex
	}
	var stillBlocked []violation
	for _, v := range all {
		if _, ok := exKey[fmt.Sprintf("%s|%s|%d", v.rule, v.path, v.line)]; ok {
			excused++
			continue
		}
		if isBlocking(v) {
			stillBlocked = append(stillBlocked, v)
			blocking++
		} else {
			warnings++
		}
	}

	if len(all) > 0 {
		if blocking > 0 {
			fmt.Fprintf(os.Stderr, "架构门禁失败：%d 处阻断级违反", blocking)
		} else {
			fmt.Fprintf(os.Stderr, "架构门禁：%d 处告警级（不阻断）", warnings)
		}
		if warnings > 0 && blocking > 0 {
			fmt.Fprintf(os.Stderr, "，另有 %d 处告警级", warnings)
		}
		if excused > 0 {
			fmt.Fprintf(os.Stderr, "，%d 处命中已登记例外（policy exceptions）", excused)
		}
		fmt.Fprintln(os.Stderr)
		byRule := map[string][]violation{}
		for _, v := range all {
			if _, ok := exKey[fmt.Sprintf("%s|%s|%d", v.rule, v.path, v.line)]; ok {
				continue
			}
			byRule[v.rule] = append(byRule[v.rule], v)
		}
		for _, r := range pol.Rules {
			vs := byRule[r.ID]
			if len(vs) == 0 {
				continue
			}
			s := sevOf[r.ID]
			fmt.Fprintf(os.Stderr, "\n规则 %s [%s]：%s（%d 处）\n", r.ID, s, r.Desc, len(vs))
			limit := len(vs)
			if !report && limit > 10 {
				limit = 10
			}
			for _, v := range vs[:limit] {
				fmt.Fprintf(os.Stderr, "  %s:%d  [%s] %s\n", v.path, v.line, v.kind, v.text)
			}
			if !report && len(vs) > limit {
				fmt.Fprintf(os.Stderr, "  … 另有 %d 处（--report 看全量）\n", len(vs)-limit)
			}
		}
		// 已登记例外回显：债是显式的，不静默。
		var shown int
		for _, ex := range pol.Exceptions {
			if shown >= 3 && !report {
				fmt.Fprintf(os.Stderr, "\n已登记例外 %d 条（--report 看全量），例：", len(pol.Exceptions))
			}
			if shown < 3 || report {
				fmt.Fprintf(os.Stderr, "\n  %s:%d [%s] %s —— %s（拆除条件：%s）",
					ex.Path, ex.Line, ex.Rule, ex.Violation, firstLine(ex.Reason), firstLine(ex.Removal))
			}
			shown++
		}
		if shown > 0 {
			fmt.Fprintln(os.Stderr)
		}
		if blocking > 0 {
			os.Exit(1)
		}
	}

	fmt.Printf("架构门禁通过（%d 条规则，policy version %d；无阻断级违反）\n", len(pol.Rules), pol.Version)

	// ── 阈值门禁（结构债）──
	th := pol.Thresholds
	var thViolations []string
	if th.MaxFileLines > 0 {
		if long := longFilesReport(root, th.ScanRoots, th.MaxFileLines); len(long) > 0 {
			thViolations = append(thViolations,
				fmt.Sprintf("超长文件（阈值 %d 行）%d 个:\n    %s", th.MaxFileLines, len(long), strings.Join(long, "\n    ")))
		}
	}
	if th.MaxMethodsPerType > 0 {
		if gods := godObjects(root, th.ScanRoots, th.MaxMethodsPerType); len(gods) > 0 {
			thViolations = append(thViolations,
				fmt.Sprintf("god object（阈值 %d 方法/类型）%d 个:\n    %s", th.MaxMethodsPerType, len(gods), strings.Join(gods, "\n    ")))
		}
	}
	if len(thViolations) > 0 {
		verb := "报告口径，不阻断"
		if th.Enforce {
			verb = "阻断"
		}
		fmt.Printf("\n结构阈值%s:\n", verb)
		for _, v := range thViolations {
			fmt.Printf("  - %s\n", v)
		}
		if th.Enforce {
			fmt.Println("\n阈值超限：结构债已超阈值，先清偿或显式上调阈值再提交。")
			os.Exit(1)
		}
	}
}
