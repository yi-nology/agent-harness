// 扫描与判定：目录遍历、规则匹配、注释中性化、超长文件与 god object 报告。

package main

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
