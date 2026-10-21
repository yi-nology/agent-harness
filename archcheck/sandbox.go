// sandbox.go —— 执行沙箱后端的**档位声明**核对（policy 的 sandbox_providers 段的执行体）。
//
// # 这条门禁拦的失效形态
//
// 执行后端自报隔离档位（Provider.Level()）。若那份声明只活在代码里，它有三个问题：
//
//  1. **看不见**：review 时看不到「这个后端声称自己有多强」——档位是实现里的一个
//     常量，混在一堆方法之间，读者不会专门去看它。
//  2. **漂移**：改了代码里的档位，声明面没有跟着变，而**没有任何东西会响**。
//  3. **说谎没人管**：后端标了比实际更强的档位（例如「内核级」其实只是进程内解释器），
//     宿主据此做的风险判断全部落空——构建全绿、测试全绿、审计日志里只有一条
//     普通的进程记录。
//
// # 怎么把信任边界写死
//
// policy 的 sandbox_providers 段是**声明面**：每个后端一条登记（标识、档位、所在包），
// 档位填 `sandbox` 包的档位常量名（LevelNone / LevelProcess / LevelOS / LevelMachine）。
// 人 review 时看得到这一行，「它凭什么说自己有这一档」写在该条的 notes 里。
//
// 执行体核对两件事（都走 go/ast，不逐行看文本）：
//
//  1. **登记 ↔ 代码事实一致**：登记的标识与档位必须在该包的后端实现里对得上；
//     对不上就是漂移（代码改了、声明没改）。
//  2. **实现 ↔ 登记齐全**：本仓每个后端实现都必须在 policy 里登记。未登记的后端，
//     它的档位声明**不在被复核的清单里**——它可以存在，但不能假装自己被复核过。
//
// # 这条门禁判不了什么（写明，而不是假装覆盖）
//
// 它判不了「后端实际提供的隔离强度是否兑现其声明」——那要审实现，机械判不了。
// 它能保证的是三件事：声明**可见**（在 policy 里）、**不漂移**（门禁核对）、
// **有签字**（人 review 时看到那一行）。
//
// # 为什么用方法指纹判定「这是个后端实现」
//
// 判据是「实现了 ID + Level + Exec 三个方法」，**不是**「它 import 了 sandbox 包」。
// 后者只说明它见过那个包，前者才说明它实现了那个协议；而 import 路径在本仓与宿主仓
// 不同，写死路径会让复用方永远判不出来——于是这条门禁在宿主仓就成了摆设。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sandboxProviderRuleID 执行沙箱后端档位声明的规则 ID。
const sandboxProviderRuleID = "harness-sandbox-provider-declared"

// sandboxProviderDesc 取档位登记规则的说明，policy 未写时用兜底文案。
func sandboxProviderDesc(s SandboxProviders) string {
	if s.Desc != "" {
		return s.Desc
	}
	return "执行沙箱后端的隔离档位必须登记在 sandbox_providers 段，且与代码事实一致（档位声明要有人签字）"
}

// sandboxDecl 从代码里解析出的一个执行后端实现。
type sandboxDecl struct {
	pkg      string // 相对仓库根的包目录（斜杠分隔）
	typeName string
	id       string
	level    string
	file     string // 相对仓库根的文件路径
	line     int
}

// pkgIndex 一个包目录的索引：类型的方法集 + 包级字符串常量。
type pkgIndex struct {
	methods map[string]map[string]*ast.FuncDecl
	consts  map[string]string
}

// sandboxProviderViolations 核对 policy 的档位登记与代码事实。
func sandboxProviderViolations(root string, cfg SandboxProviders) ([]Violation, error) {
	if !cfg.on() {
		return nil, nil
	}
	fset := token.NewFileSet()
	dirs := packageDirs(root, ignoredPaths(cfg.IgnoreDirs))

	// 全仓扫出所有后端实现。**先扫实现、再核登记**——顺序反了的话，
	// 「policy 里少登记了一个后端」这种漂移就永远发现不了（登记面是空的，
	// 扫实现的结果没人对照）。
	found := map[string][]sandboxDecl{}
	var out []Violation
	for _, dir := range dirs {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		idx, err := indexPackage(fset, dir)
		if err != nil {
			return nil, err
		}
		for typeName, methods := range idx.methods {
			if !isProviderImpl(methods) {
				continue
			}
			lvl := methods["Level"]
			d := sandboxDecl{
				pkg:      rel,
				typeName: typeName,
				file:     relTo(root, fset.Position(lvl.Pos()).Filename),
				line:     fset.Position(lvl.Pos()).Line,
			}
			id, ok := providerIDFrom(methods["ID"], idx.consts)
			if !ok {
				out = append(out, Violation{
					Rule: sandboxProviderRuleID, Path: d.file, Line: d.line, Kind: "sandbox_provider_id_unreadable",
					Text: fmt.Sprintf("后端实现 %s.%s 的 ID() 没有返回可静态识别的字符串常量，"+
						"档位登记无从核对（ID 必须是字面量或包级字符串常量）", rel, typeName),
				})
				continue
			}
			d.id = id
			level, ok := providerLevelFrom(methods["Level"])
			if !ok {
				out = append(out, Violation{
					Rule: sandboxProviderRuleID, Path: d.file, Line: d.line, Kind: "sandbox_provider_level_unreadable",
					Text: fmt.Sprintf("后端实现 %s.%s 的 Level() 没有返回常量，档位登记无从核对", rel, typeName),
				})
				continue
			}
			d.level = level
			found[rel] = append(found[rel], d)
		}
	}

	// 登记面核对。
	registered := map[string]SandboxProvider{}
	for _, p := range cfg.Providers {
		key := p.Package + "/" + p.ID
		if prev, dup := registered[key]; dup {
			out = append(out, Violation{
				Rule: sandboxProviderRuleID, Path: p.Package, Line: 1, Kind: "sandbox_provider_dup",
				Text: fmt.Sprintf("档位登记重复：%s/%s 与 %s/%s 是同一条", p.Package, p.ID, prev.Package, prev.ID),
			})
			continue
		}
		registered[key] = p

		dir := filepath.Join(root, filepath.FromSlash(p.Package))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			out = append(out, Violation{
				Rule: sandboxProviderRuleID, Path: p.Package, Line: 1, Kind: "sandbox_provider_stale",
				Text: fmt.Sprintf("档位登记指向的包目录 %s 不存在——声明与事实已偏离，当天即红", p.Package),
			})
			continue
		}
		var matched *sandboxDecl
		for i := range found[p.Package] {
			if found[p.Package][i].id == p.ID {
				matched = &found[p.Package][i]
				break
			}
		}
		if matched == nil {
			out = append(out, Violation{
				Rule: sandboxProviderRuleID, Path: p.Package, Line: 1, Kind: "sandbox_provider_missing",
				Text: fmt.Sprintf("登记的后端 %q 在包 %s 里找不到对应实现——实现被删或改了标识，登记须同步",
					p.ID, p.Package),
			})
			continue
		}
		if matched.level != p.Level {
			out = append(out, Violation{
				Rule: sandboxProviderRuleID, Path: matched.file, Line: matched.line, Kind: "sandbox_provider_drift",
				Text: fmt.Sprintf("后端 %q 的档位漂移：policy 登记 %s，代码返回 %s——改代码必须改登记"+
					"（登记那一行要有人 review，它就是档位声明的签字处）", p.ID, p.Level, matched.level),
			})
		}
	}

	// 反向：代码里有、登记里没有。未登记的后端不是错，但它的档位声明
	// **不在被复核的清单里**——不登记就等于没人签字。
	for pkg, decls := range found {
		for _, d := range decls {
			if _, ok := registered[pkg+"/"+d.id]; !ok {
				out = append(out, Violation{
					Rule: sandboxProviderRuleID, Path: d.file, Line: d.line, Kind: "sandbox_provider_unregistered",
					Text: fmt.Sprintf("后端实现 %s.%s（标识 %q，档位 %s）未登记在 sandbox_providers 段——"+
						"档位声明要有人签字，请补登记（id/level/package）", pkg, d.typeName, d.id, d.level),
				})
			}
		}
	}
	return out, nil
}

// indexPackage 索引一个包目录：类型的方法集 + 包级字符串常量。
func indexPackage(fset *token.FileSet, dir string) (*pkgIndex, error) {
	idx := &pkgIndex{
		methods: map[string]map[string]*ast.FuncDecl{},
		consts:  map[string]string{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// 与 Go 工具链同口径：`.` / `_` 前缀文件不参与编译。
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", filepath.Join(dir, name), err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					bl, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					if v, uerr := strconv.Unquote(bl.Value); uerr == nil {
						idx.consts[n.Name] = v
					}
				}
			}
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			recv := recvTypeName(fd.Recv)
			if recv == "" {
				continue
			}
			if idx.methods[recv] == nil {
				idx.methods[recv] = map[string]*ast.FuncDecl{}
			}
			idx.methods[recv][fd.Name.Name] = fd
		}
	}
	return idx, nil
}

// isProviderImpl 判断某类型是不是执行后端的实现：**方法指纹**。
//
// 指纹 = ID() string + Level() <某个名为 Level 的类型> + Exec(1 参, 2 返回值)。
//
// 三个方法都要在：少一个就不足以把它认成一个后端（只有一个 Level() 方法的类型
// 可能是别的东西——比如某处表示「严重度等级」的 API）。
func isProviderImpl(methods map[string]*ast.FuncDecl) bool {
	idFn, ok := methods["ID"]
	if !ok || !returnsBasic(idFn, "string") {
		return false
	}
	lvlFn, ok := methods["Level"]
	if !ok || !returnsNamed(lvlFn, "Level") {
		return false
	}
	execFn, ok := methods["Exec"]
	if !ok {
		return false
	}
	// 入参**至少一个**：`(ctx context.Context, req Request)` 是两个字段
	// （不同类型各占一个），写成「恰好一个」会把真正的实现判成不是实现——
	// 判据过严与过松同样致命：前者让这条门禁对真实代码失明。
	if execFn.Type.Params == nil || len(execFn.Type.Params.List) < 1 {
		return false
	}
	if execFn.Type.Results == nil || len(execFn.Type.Results.List) != 2 {
		return false
	}
	return true
}

// providerIDFrom 取 ID() 返回的字符串：字面量或包级字符串常量。
func providerIDFrom(fn *ast.FuncDecl, consts map[string]string) (string, bool) {
	expr, ok := singleReturnExpr(fn)
	if !ok {
		return "", false
	}
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok {
			return s, true
		}
	}
	return "", false
}

// providerLevelFrom 取 Level() 返回的档位常量名（如 LevelProcess）。
//
// 取的是**常量名**而不是档位值：常量名是代码里可静态识别的那一面，而档位值
// （"process"）住在 sandbox 包的 levelNames 表里——门禁不能 import 被判的包
// （它必须是最底层的），所以比对的是常量名，policy 那一侧也填常量名。
func providerLevelFrom(fn *ast.FuncDecl) (string, bool) {
	expr, ok := singleReturnExpr(fn)
	if !ok {
		return "", false
	}
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name, true
	case *ast.Ident:
		return v.Name, true
	}
	return "", false
}

// singleReturnExpr 取函数体里唯一一条 return 语句的表达式。
func singleReturnExpr(fn *ast.FuncDecl) (ast.Expr, bool) {
	if fn == nil || fn.Body == nil || len(fn.Body.List) != 1 {
		return nil, false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil, false
	}
	return ret.Results[0], true
}

// returnsBasic 报告函数是否恰好返回一个指定名的基本类型。
func returnsBasic(fn *ast.FuncDecl, name string) bool {
	if fn == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	id, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == name
}

// returnsNamed 报告函数是否恰好返回一个指定名的类型。
//
// 同时认 `X.Level`（选择器）与裸 `Level`（同包）两种形态——判的是**类型名**，
// 不绑 import 路径。
func returnsNamed(fn *ast.FuncDecl, name string) bool {
	if fn == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	switch t := fn.Type.Results.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name == name
	case *ast.SelectorExpr:
		return t.Sel.Name == name
	}
	return false
}

// relTo 把绝对路径转成相对仓库根的斜杠路径。
func relTo(root, path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
