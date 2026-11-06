// spawnsites.go —— 子进程启动点的收集（出口门禁的「事实面」）。
//
// 判据（登记与否、兑现是否可见）在 egresspaths.go；这里只回答一个问题：
// **代码里到底有哪些地方起了子进程**。拆成两份是因为两类失效的形态不同：
// 判定写错会误报（有人会来修），收集写漏是**静默放行**（没人会来）——后者必须在
// 自己的文件里被逐形态钉住。
//
// # 覆盖的三种形态（每一种都是实测漏判后才补上的）
//
//  1. **限定符调用**：exec.Command(...)。别名（import ex "os/exec" ⇒ ex.Command）
//     同样命中——限定符按 **import 路径**解析，不按名字猜。
//  2. **点导入**：import . "os/exec" ⇒ Command(...) 没有限定符。按「本文件点导入了
//     哪个启动包」补齐，否则换个导入写法就能让判定面消失。
//  3. **包级初始化**：var cmd = exec.Command(...) 不在任何函数里。旧实现只走
//     *ast.FuncDecl，于是把 spawn 挪到包级变量就能让门禁变绿——而 spawnSite.fn 的
//     注释早就写着「空 = 函数外，如包级变量初始化」，即**承诺了却没人实现**。
//
// # 入口表按 import 路径归，不止 os/exec
//
// os.StartProcess 与 syscall.Exec/ForkExec/StartProcess 同样绕开出口强制面。
// 它们各自接受 Env，故「登记 + 兑现证据」这套判据对它们逐字适用——把它们留在
// 判定面之外，等于承认「换个 API 就能绕过登记」，而绕过是静默的。
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

// spawnFuncsByPath 被认定为「启动子进程」的函数，按 **import 路径** 归表。
//
// 按 import 路径而不是按限定符名：别名是调用方随手起的，改个名字就绕过；
// 而 import 路径是文件里写死的事实。
var spawnFuncsByPath = map[string]map[string]bool{
	"os/exec": {"Command": true, "CommandContext": true},
	"os":      {"StartProcess": true},
	"syscall": {"Exec": true, "ForkExec": true, "StartProcess": true},
}

// spawnSite 一个子进程启动点。
type spawnSite struct {
	path    string            // 相对仓根的文件路径
	fn      string            // 含该调用的函数名（空 = 函数外，如包级变量初始化）
	line    int               // 行号（报告用）
	body    *ast.BlockStmt    // 函数体（判兑现证据用；nil = 函数外）
	imports map[string]string // 该文件的「包名/别名 → import 路径」
}

// dotSpawnFuncs 取本文件点导入的启动入口函数名集合。
//
// 点导入把包里的名字直接搬进本文件作用域，调用形态上没有限定符 ⇒ 只看限定符的
// 收集器看不见它。缺了这一条，import . "os/exec" 就是一条静默通道。
//
// 入参是**点导入的路径集合**而不是「名字→路径」映射：后者由 importPathByName 给出，
// 而它按设计就丢掉了点导入（「不是限定符」——对限定符判定成立，对启动点判定不成立）。
func dotSpawnFuncs(dotPaths map[string]bool) map[string]bool {
	out := map[string]bool{}
	for path := range dotPaths {
		for fn := range spawnFuncsByPath[path] {
			out[fn] = true
		}
	}
	return out
}

// collectSpawnSites 扫生产代码里的子进程启动点。
func collectSpawnSites(root string, ignore map[string]bool) ([]spawnSite, error) {
	fset := token.NewFileSet()
	var out []spawnSite
	for _, dir := range packageDirs(root, ignore) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			name := de.Name()
			if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			full := filepath.Join(dir, name)
			f, perr := parser.ParseFile(fset, full, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil, fmt.Errorf("解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", full, perr)
			}
			imports := importPathByName(f)
			rel := relTo(root, full)
			out = append(out, fileSpawnSites(fset, f, imports, rel)...)
		}
	}
	return out, nil
}

// fileSpawnSites 取一个文件里的启动点。
//
// 函数内的调用记在函数名下；**包级初始化**（var / const 声明里的调用）记在空函数名下，
// 由 fnLabel 渲染成「包级初始化」。包级这一支不是补丁：spawnSite.fn 的注释一直写着
// 「空 = 函数外」，缺的是收集侧——声明与实现必须同时在场。
func fileSpawnSites(fset *token.FileSet, f *ast.File, imports map[string]string, rel string) []spawnSite {
	var out []spawnSite
	dotted := dotSpawnFuncs(dotImportPaths(f))
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Body == nil {
				continue
			}
			out = append(out, spawnSitesIn(fset, decl.Body, decl.Body, decl.Name.Name, imports, dotted, rel)...)
		case *ast.GenDecl:
			out = append(out, spawnSitesIn(fset, decl, nil, "", imports, dotted, rel)...)
		}
	}
	return out
}

// spawnSitesIn 取一个 AST 节点里的启动点（限定符形态与点导入形态各扫一遍）。
func spawnSitesIn(fset *token.FileSet, node ast.Node, body *ast.BlockStmt, fn string, imports map[string]string, dotted map[string]bool, rel string) []spawnSite {
	var out []spawnSite
	for _, call := range qualifiedCallsIn(fset, node) {
		imp, ok := imports[call.qualifier]
		if !ok || !spawnFuncsByPath[imp][call.sel] {
			continue
		}
		out = append(out, spawnSite{path: rel, fn: fn, line: call.line, body: body, imports: imports})
	}
	if len(dotted) == 0 {
		return out
	}
	for _, call := range unqualifiedCallsIn(fset, node) {
		if !dotted[call.sel] {
			continue
		}
		out = append(out, spawnSite{path: rel, fn: fn, line: call.line, body: body, imports: imports})
	}
	return out
}

// qualifiedCallsIn 取任意 AST 节点内的限定符调用（qualifiedCalls 只吃 *ast.File，
// 这里要按**函数体**取——判断「这个函数里有没有 spawn / 有没有兑现」必须按函数）。
func qualifiedCallsIn(fset *token.FileSet, node ast.Node) []qualifiedCall {
	var out []qualifiedCall
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		out = append(out, qualifiedCall{qualifier: ident.Name, sel: sel.Sel.Name, line: fset.Position(call.Pos()).Line})
		return true
	})
	return out
}

// unqualifiedCallsIn 取任意 AST 节点内**没有限定符**的调用（点导入形态）。
func unqualifiedCallsIn(fset *token.FileSet, node ast.Node) []qualifiedCall {
	var out []qualifiedCall
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		out = append(out, qualifiedCall{qualifier: "", sel: ident.Name, line: fset.Position(call.Pos()).Line})
		return true
	})
	return out
}
