// 扫描与判定：目录遍历、规则匹配、注释中性化、超长文件与 god object 报告。

package archcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// iterFiles 列出 root 下的候选文件。
//
// 返回 error 而不是「尽力而为的清单」：遍历途中任何一次 Lstat / Readdir 失败，
// 都意味着**那棵子树没有被判定**。旧实现写的是 `_ = filepath.Walk(...)`，把
// 失败吞成「零个文件」——于是「读不了这个目录」与「这个目录里没有违规」在
// 调用方看来完全一样，而门禁照常报「无阻断级违反」。
//
// 这是本仓纪律里最贵的失败形态（门禁变绿）：真正的风险从来不是误报，是
// **给不出结论却显示成通过**。同一条纪律在 importViolations 上已经落地过
// （语法坏掉的文件不得静默跳过），这里补齐的是遍历侧。
func iterFiles(root string, includes []string, ignoreTest bool) ([]string, error) {
	var out []string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 回传而不是吞掉：交给 Walk 终止并把错误带出。
			return err
		}
		if info.IsDir() {
			base := info.Name()
			// 跳过隐藏目录与构建产物目录（与宿主仓 iterFiles 同口径）。
			if path != root && (strings.HasPrefix(base, ".") || base == "node_modules" || base == "dist") {
				return filepath.SkipDir
			}
			// 嵌套模块（issue #162）：与 packageDirs 同口径——目录里有自己的
			// go.mod 就是另一棵模块树（linked worktree 拷贝/源码副本），
			// 规则扫描不应越过模块边界，否则嵌套拷贝里的代码被算进本仓判定面。
			if path != root && isNestedModule(path) {
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
			// 坏模式（"*.[go"）与含分隔符的模式（"**/*.go"）在这里恒不匹配——前者
			// 返回 ErrBadPattern、后者是因为这里只拿 Base 去比。两种都让规则**一个文件
			// 都不扫**却继续绿，故它们是**声明缺陷**：判决在 policy 自校验（includecheck.go，
			// 恒开、error 级、能指出该改哪一行），这里静默跳过。与 ruleFiles 对不存在
			// scope 的处置同一条纪律。
			ok, _ := filepath.Match(pat, filepath.Base(path))
			if ok {
				out = append(out, path)
				break
			}
		}
		return nil
	})
	if walkErr != nil {
		// 与 importViolations 对解析失败的处置同一条：门禁给不出结论时不能
		// 装作通过。调用方（ruleFiles / dependencyViolations）必须据此返回 error。
		return nil, fmt.Errorf("扫描 %s 失败（该子树未被判定，门禁给不出结论）: %w", root, walkErr)
	}
	return out, nil
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
			// 这里刻意不报错，但**不是**静默：scope 指向不存在的目录是
			// 声明与事实的偏离，由 policy 自校验统一判（selfcheck.go 的
			// 「规则 scope 项必须存在」），那里能给出规则 id 与修法。
			//
			// 也就是说：这条 continue 的唯一依据是那条自校验恒开。
			// 若哪天它被摘掉，这里必须改成返回 error——否则这条规则
			// 一行都不判，而报告里仍计入「N 条规则」。
			continue
		}
		files, err := iterFiles(p, r.Include, r.IgnoreTest && !r.OnlyTest)
		if err != nil {
			return nil, fmt.Errorf("规则 %s: %w", r.ID, err)
		}
		// only_test=true 时不能按 ignore_test 提前滤掉测试文件——那正是要扫的。
		for _, f := range files {
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
		rel, _ := filepath.Rel(root, f)
		out = append(out, regexViolationsIn(r, re, rel, string(data))...)
	}
	return out, nil
}

// regexViolationsIn 是 forbid_regex 的**判定核**：判一段内容里有没有违规行。
//
// 两个调用方共用它：文件遍历（checkRule）与加载期样例（examples.go 的 sampleHits）。
//
// # 为什么必须共用（而不是让样例另写一套近似判定）
//
// 样例的用途是证明「这条规则还判得动」。若样例走的是另一套实现，那么它证明的只是
// **那套实现**还判得动——而两套实现各自漂移，样例那套通常漂得更慢（它只看几个字符串），
// 于是绿灯更久：这正是「从没见过它触发的规则等于没有规则」最隐蔽的形态。
//
// 共用之后，样例与生产判定在结构上不可能不一致：同一条正则、同一条注释豁免、
// 同一段 negative_follow 中和逻辑。
func regexViolationsIn(r Rule, re *regexp.Regexp, rel, content string) []Violation {
	var out []Violation
	for i, line := range strings.Split(content, "\n") {
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
		out = append(out, Violation{Rule: r.ID, Path: rel, Line: i + 1, Text: trimmed, Kind: "forbid_regex"})
	}
	return out
}

// firstLine 取多行说明的首行（例外回显用，避免刷屏）。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func longFilesReport(root string, scanRoots []string, limit int) ([]string, error) {
	var out []string
	for _, sr := range scanRoots {
		p := filepath.Join(root, sr)
		if _, err := os.Stat(p); err != nil {
			// 声明问题，由 policy 自校验判（thresholds.scan_roots 项必须存在）；
			// 遍历失败（读不了那棵子树）才是 error，见下。
			continue
		}
		walkErr := filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 与 Go 工具链同口径：忽略 `.` / `_` 前缀文件。
			if name := info.Name(); strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := strings.Count(string(data), "\n"); n > limit {
				rel, _ := filepath.Rel(root, path)
				out = append(out, fmt.Sprintf("%s (%d 行 > %d)", rel, n, limit))
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("阈值扫描 %s 失败（该子树未被判定，门禁给不出结论）: %w", sr, walkErr)
		}
	}
	slices.Sort(out)
	return out, nil
}
