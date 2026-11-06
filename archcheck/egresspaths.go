// egresspaths.go —— 子进程出口的**声明面一致性**判据（P0-2 的后半）。
//
// # 它拦的失效形态
//
// @egress@ 给的是出口策略的**词表与判据**（env 生成、Verify、后端能力问询），
// 但词表本身不会跑到任何一个子进程上去。子进程一旦被 spawn，它的出站流量就
// 完全在本进程的 netguard 之外——「声明了受限出口」在那条路径上**没有任何东西
// 兑现**，而报告是全绿的，因为没有任何判据在问「这个 spawn 点在强制面内吗」。
//
// 这正是本仓反复强调的「声明强度 ≠ 实际强度」在执行面上的又一次实例：
// 判据停在工具调用，工具内部起的进程没有判据。
//
// # 判据四条
//
//  1. **每个子进程启动点必须登记**（未登记即红）。方向是 fail-closed：
//     「没登记」不等于「不要求出口」，而是**没人声明过它的出口是什么**——
//     与 @sandbox_providers@ 的「有实现没登记必须报出来」同一条纪律。
//  2. **登记的兑现必须能看见**（mode >= proxy 时，含启动的那个函数里必须出现
//     出口 env 的生成/校验调用）。看不见兑现就走 env 的自我声明，那正是
//     「声明了但没人执行」的形态。
//  3. **禁网档不许落在环境变量上**（mode: denied 直接红）。环境变量拦不住
//     静态二进制、UDP 与原始 socket，登记成 denied 只是把「假装禁网」写进
//     policy——比明确不声明更危险。
//  4. **陈旧登记当天即红**（文件/函数不存在，或那个函数里没有启动调用）。
//     与「陈旧包根当天即红」同一把尺子：声明与事实偏离而无人红，是无声漂移。
//
// # 为什么按「文件 + 函数」定位启动点
//
// 按包名太宽（一个包里可能既有 spawn 也有不 spawn 的函数），按行号太脆
// （改一行注释就漂）。文件 + 函数是**改动的最小稳定单元**：函数搬走或改名，
// 登记就该跟着改，改漏了第 4 条会红。
//
// # 判定面包含哪些启动形态
//
// 收集器在 spawnsites.go，覆盖三种形态（每一种都实测过漏判）：限定符调用（含别名）、
// 点导入（无限定符）、**包级初始化**（var cmd = exec.Command(...)，不在任何函数里）。
// 入口表按 import 路径归：除 os/exec 还含 os.StartProcess 与
// syscall.Exec/ForkExec/StartProcess——它们同样绕开出口强制面，也各自接受 Env，
// 故「登记 + 兑现证据」这套判据对它们逐字适用。
//
// # 判不了什么（写明，而不是假装覆盖）
//
//   - 判不了「env 真的被子进程遵守」：环境变量只是建议，这是 egress 包
//     包头已写明的边界，本门禁不重复声明它能做到。
//   - 判不了「围绕 cmd 包了一层自己的 spawn 工具再调用」：那需要类型的推导
//     或跨函数数据流，门禁不 import 被判的包也就拿不到类型信息。
//     这类封装最终仍要落到某个 os/exec 调用上，故**启动点**这一层是收口的；
//     但如果宿主把 spawn 挪进第三方库，本门禁看不见。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// egressPathRuleID 子进程出口门禁的规则 ID。
const egressPathRuleID = "harness-subprocess-egress"

// defaultPlumbing 缺省的「兑现证据」函数名（egress.Spec 上的方法）。
var defaultPlumbing = []string{"Env", "Verify"}

// egressModeWords 出口档位词表（与 egress 包三档同义，不另立口径）。
var egressModeWords = map[string]bool{"unrestricted": true, "proxy": true, "denied": true}

// egressPathsDesc 取本门禁的说明，policy 未写时用兜底文案。
func egressPathsDesc(cfg EgressPaths) string {
	if cfg.Desc != "" {
		return cfg.Desc
	}
	return "每个子进程启动点都必须在 policy 的 egress_paths 段登记出口档位；" +
		"要求经代理的必须在启动函数里看得见 env 的生成/校验（环境变量不会自己跑到子进程上）"
}

// egressPathViolations 判定「子进程是否在出口强制面内」。
func egressPathViolations(root string, cfg EgressPaths) ([]Violation, error) {
	if !cfg.on() {
		return nil, nil
	}
	sites, err := collectSpawnSites(root, ignoredPaths(cfg.IgnoreDirs))
	if err != nil {
		return nil, err
	}

	var out []Violation
	matched := make([]bool, len(sites))

	for _, e := range cfg.Entries {
		// matched 由 judgeEgressEntry 就地标记（它知道哪些启动点被这条登记认领了）。
		vs, _ := judgeEgressEntry(root, e, sites, matched)
		out = append(out, vs...)
	}

	// 未登记的启动点：fail-closed 报红。
	// 方向必须是这样——「没登记」如果被当成「不要求」，那么加一个 spawn 点
	// 就是一次**静默扩大判定面**：越多未登记的启动点，判定面越小。
	for i, s := range sites {
		if matched[i] {
			continue
		}
		out = append(out, Violation{
			Rule: egressPathRuleID, Path: s.path, Line: s.line,
			Kind: "egress_path_unregistered",
			Text: fmt.Sprintf("子进程启动点未登记（函数 %s）：它的出站流量在本进程 netguard 之外——"+
				"请在 policy 的 egress_paths 段登记这个启动点的出口档位（unrestricted 也必须显式登记，"+
				"「没登记」不等于「不要求」）", fnLabel(s.fn)),
		})
	}
	return out, nil
}

// fnLabel 把「函数外」这一态说清楚（不输出空括号）。
func fnLabel(fn string) string {
	if fn == "" {
		return "包级初始化"
	}
	return fn
}

// judgeEgressEntry 判一条登记，并就地标记它命中的启动点。
func judgeEgressEntry(root string, e EgressEntry, sites []spawnSite, matched []bool) ([]Violation, bool) {
	if strings.TrimSpace(e.Path) == "" {
		return []Violation{{
			Rule: egressPathRuleID, Path: "architecture-policy.yaml", Line: 1,
			Kind: "egress_entry_malformed",
			Text: "egress_paths 段有一条登记没写 path（没有定位就谈不上判定）",
		}}, false
	}
	mode := e.Mode
	if mode == "" {
		mode = "unrestricted"
	}
	if !egressModeWords[mode] {
		return []Violation{{
			Rule: egressPathRuleID, Path: e.Path, Line: 1,
			Kind: "egress_entry_malformed",
			Text: fmt.Sprintf("egress_paths 登记的档位 %q 不在词表内（unrestricted / proxy / denied）", e.Mode),
		}}, false
	}

	var hits []int
	for i, s := range sites {
		if s.path != e.Path {
			continue
		}
		if e.Fn != "" && s.fn != e.Fn {
			continue
		}
		hits = append(hits, i)
	}

	// 陈旧登记：文件不存在 / 函数不存在 / 函数里没有启动调用。
	if len(hits) == 0 {
		detail := "该文件里没有子进程启动调用"
		if e.Fn != "" && !funcDeclared(root, e.Path, e.Fn) {
			detail = "该文件里没有这个函数"
		}
		if _, err := os.Stat(filepath.Join(root, e.Path)); err != nil {
			detail = "该文件不存在"
		}
		return []Violation{{
			Rule: egressPathRuleID, Path: e.Path, Line: 1,
			Kind: "egress_entry_stale",
			Text: fmt.Sprintf("egress_paths 登记 %s:%s 已与代码事实偏离（%s）——"+
				"陈旧登记会让「这条路径的出口有人管过」看起来成立", e.Path, fnLabel(e.Fn), detail),
		}}, false
	}

	for _, i := range hits {
		matched[i] = true
	}

	// 禁网档不许落在环境变量上。
	if mode == "denied" {
		return []Violation{{
			Rule: egressPathRuleID, Path: e.Path, Line: 1,
			Kind: "egress_path_denied_unenforceable",
			Text: "登记成 denied 但启动点仍在起子进程：环境变量拦不住静态二进制、UDP 与原始 socket——" +
				"禁网只能由带能力声明的执行后端强制，把它写在这里等于把「假装禁网」写进 policy",
		}}, true
	}

	if mode == "unrestricted" {
		return nil, true
	}

	// proxy：兑现证据必须在启动函数里看得见。
	names := e.Plumbing
	if len(names) == 0 {
		names = defaultPlumbing
	}
	for _, i := range hits {
		if hasPlumbing(sites[i].body, sites[i].imports, names) {
			continue
		}
		return []Violation{{
			Rule: egressPathRuleID, Path: sites[i].path, Line: sites[i].line,
			Kind: "egress_unplumbed",
			Text: fmt.Sprintf("登记要求出口经代理，但启动函数 %s 里看不见 env 的生成/校验（%s）："+
				"环境变量不会自己跑到子进程上——必须在 spawn 之前用 egress.Spec.Env 生成，"+
				"生成不出就不起子进程（fail closed）", fnLabel(sites[i].fn), strings.Join(names, " / ")),
		}}, true
	}
	return nil, true
}

// funcDeclared 报告某文件里是否声明了这个顶层函数。
func funcDeclared(root, rel, fn string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == fn {
			return true
		}
	}
	return false
}

// hasPlumbing 判函数体里是否出现了「出口 env 的生成/校验」调用。
//
// 认两种形态（都是真实调用形态，不是文本匹配）：
//
//   - 包限定调用：egress.Env(...) / egress.Verify(...)；
//   - 接收者调用：spec.Env(...) / spec.Verify(...)，前提是**本文件确实 import 了
//     egress**（否则那个 .Env 可能是任何东西的 Env）。
//
// 第二种必须带 import 前提：只按方法名认人会把 "re.Env" 这类无关调用也算成兑现证据，
// 那样这条判据就变成「函数里随便有个 .Env( 就过」。
func hasPlumbing(body *ast.BlockStmt, imports map[string]string, names []string) bool {
	if body == nil {
		return false
	}
	importsEgress := false
	for _, p := range imports {
		if pathLastSegment(p) == "egress" {
			importsEgress = true
			break
		}
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if !containsString(names, sel.Sel.Name) {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if imp, isImport := imports[ident.Name]; isImport {
			found = pathLastSegment(imp) == "egress"
			return true
		}
		// 接收者是变量：型别信息拿不到，退一步要求本文件 import 了 egress。
		found = importsEgress
		return true
	})
	return found
}

// containsString 报告切片里是否有该元素。
func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
