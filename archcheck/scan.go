// 扫描与判定：目录遍历、规则匹配、注释中性化、超长文件与 god object 报告。

package archcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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
//
// 豁免的形态是「anchor 紧跟 forbid_follow」，命中位置（被禁词）落在 forbid_follow 里。
// 故要问的是：命中位置**之前**是否存在一处 anchor，其后紧跟 forbid_follow。
//
// 判据面是「被匹配的那段文本」：文本分支传源码行，import 分支传 import 路径。
// 两侧同构——否则同一个字段在两个分支语义不同（豁免写了一条不生效，且不报错）。
//
// 旧实现只取 anchor 在本行的**第一次**出现（strings.Index），于是同一行出现多处
// anchor 时，后面那处永远看不到——而「断言不得出现某词」的用例里，同一行常常有
// 两处断言（一正一反），漏判的表现是**门禁把合法的负向断言报成违规**。
func neutralized(text string, start, end int, nfs []NegFollow) bool {
	for _, nf := range nfs {
		off := 0
		for {
			i := strings.Index(text[off:], nf.Anchor)
			if i < 0 {
				break
			}
			a := off + i
			if a > start {
				break // 后续 anchor 更靠右，不可能覆盖本次命中
			}
			rest := text[a+len(nf.Anchor):]
			if strings.HasPrefix(rest, nf.ForbidFollow) {
				return true
			}
			off = a + 1
		}
	}
	return false
}

// inTestScope 判文件是否在规则的判定面内。
//
// 判定面是**三态**，不是二态：
//
//	only_test=true                       → 只扫测试文件
//	only_test=false 且 ignore_test=true  → 只扫生产文件
//	两者皆 false                         → 生产与测试**都**扫
//
// 第三种情形是旧实现的坑：旧代码写的是 `if r.OnlyTest != isTest { continue }`，
// 把 only_test 当成了「必须等于 isTest」。于是「两者皆 false」时**所有测试文件
// 被整体跳过**——而 ignore_test: false 的规则声明的正是「也要覆盖测试文件」。
// 失败形态是门禁**变绿**而非变红：三条声明要覆盖测试文件的规则集体失效，
// 且真实违规就躺在仓里（ptc/bytebudget_regression_test.go 里的 stdlib "errors"
// 就是这样活下来的——AGENTS.md 写着「全仓 24 个文件统一切到 pkg/errors」）。
func inTestScope(r Rule, path string) bool {
	isTest := strings.HasSuffix(path, "_test.go")
	switch {
	case r.OnlyTest:
		return isTest
	case r.IgnoreTest:
		return !isTest
	default:
		return true
	}
}

// ruleFiles 选出规则判定面内的文件：scope ∩ include ∩ scope_exclude ∩ 测试面三态。
//
// 测试面三态在 iterFiles 之前先滤一遍（少走一遍目录树），在 iterFiles 之后
// 再用 inTestScope 兜一次（口径只有一处定义，避免两处漂移）。
func ruleFiles(root string, r Rule) ([]string, error) {
	var out []string
	for _, scope := range r.Scope {
		p := filepath.Join(root, scope)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		// only_test=true 时不能按 ignore_test 提前滤掉测试文件——那正是要扫的。
		for _, f := range iterFiles(p, r.Include, r.IgnoreTest && !r.OnlyTest) {
			rel, _ := filepath.Rel(root, f)
			skip := false
			for _, ex := range r.ScopeExclude {
				if rel == ex || strings.HasPrefix(rel, ex+string(filepath.Separator)) {
					skip = true
					break
				}
			}
			if !skip && inTestScope(r, f) {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

func checkRule(root string, r Rule) ([]Violation, error) {
	if r.ForbidImport != "" {
		// import 规则走语法树判定，不走逐行正则——理由见 imports.go 头注。
		return importViolations(root, r)
	}
	if r.ForbidRegex == "" {
		return nil, nil // 声明面字段：规则只带说明，无可执行体（selfcheck.go 会把这种规则报出来）
	}
	re, err := regexp.Compile(r.ForbidRegex)
	if err != nil {
		return nil, fmt.Errorf("规则 %s forbid_regex 非法: %w", r.ID, err)
	}

	files, err := ruleFiles(root, r)
	if err != nil {
		return nil, err
	}

	var out []Violation
	for _, f := range files {
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
			out = append(out, Violation{Rule: r.ID, Path: rel, Line: i + 1, Text: strings.TrimSpace(line), Kind: "forbid_regex"})
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
//
// 聚合键是**包路径 + 类型名**，不是裸类型名。
//
// 为什么必须带包路径：同名类型在不同包里是完全无关的两个类型，而方法数超阈值
// 讲的是「这一个类型承担了过多关注点」——把两个包的同名类型并成一个数，判出的
// 是「这两个类型加起来很大」，那不是 god object，是巧合。实测本仓就有多处同名
// 类型分处不同包（各包的测试接收器 rec、两个包的 Report），一旦阈值调低，
// 误报会先于真报出现——而误报是门禁最致命的失败形态：维护者学会忽略它。
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
			pkg := filepath.Dir(rel)
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
				key := pkg + "." + recv
				b := counts[key]
				if b == nil {
					b = &bucket{file: rel}
					counts[key] = b
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
