// errtable.go —— docs/error-semantics.md 的登记表 ↔ 源码哨兵集合判定。
//
// 判据是**双向集合相等**（按包）：
//   - 代码里有哨兵、表里没有 ⇒ 「新增错误未登记」——issue #104 要拦的主体；
//   - 表里有名字、代码里没有 ⇒ 幽灵哨兵——错误删了表没删，调用方按表
//     errors.Is 一个不存在的哨兵，判定永远不中。
//
// 表是文档、代码是事实，两边都必须向对方负责：文档多写与少写都是漂移。

package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const errorSemanticsPath = "docs/error-semantics.md"

// parseErrorTable 解析错误语义总表。
//
// 文档结构约定（strict，结构漂移要让门禁看得见）：
//
//	## 哨兵总表（按包）
//	### <pkg>
//	| 哨兵 | 触发条件 | 调用方处置 |
//	|---|---|---|
//	| `ErrX` | ... | ... |
//
//	## 结构化错误类型（errors.As 判定）
//	| 符号 | 含义 | 调用方处置 |
//	|---|---|---|
//	| `<pkg>.ErrTypeX` | ... | ... |
//
// 只在上述两段里收表行，其余章节的表格不参与判定——判据面必须封闭，
// 否则文档里任何一张新表都会意外扩大判定范围。
func parseErrorTable(path string) (sents map[string][]string, types map[string]bool, problems []string, err error) {
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, nil, nil, rerr
	}
	sents = map[string][]string{}
	types = map[string]bool{}
	section := "" // 当前二级章节
	pkg := ""     // 哨兵总表里的当前三级小节（包名）
	seen := map[string]bool{}

	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			pkg = ""
		case strings.HasPrefix(line, "### ") && section == "哨兵总表（按包）":
			pkg = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			if seen[pkg] {
				problems = append(problems, path+": 哨兵总表里包 "+pkg+" 出现两个小节")
			}
			seen[pkg] = true
		case strings.HasPrefix(line, "|"):
			name, ok := firstCellName(line)
			if !ok {
				continue // 表头/分隔行/无反引号首列的行
			}
			switch {
			case pkg != "" && section == "哨兵总表（按包）":
				sents[pkg] = append(sents[pkg], name)
			case section == "结构化错误类型（errors.As 判定）":
				if !strings.Contains(name, ".") {
					problems = append(problems, path+": errors.As 表的符号 "+name+" 不是「包.类型」形态")
					continue
				}
				types[name] = true
			}
		}
	}
	for pkg := range sents {
		sort.Strings(sents[pkg])
	}
	return sents, types, problems, nil
}

// firstCellName 取表行第一列的反引号名。`| `ErrX` | ...` → ErrX。
// 表头行（首列是「哨兵」「符号」等纯文本）与分隔行（---）自然不命中。
func firstCellName(line string) (string, bool) {
	rest := strings.TrimPrefix(line, "|")
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "`") {
		return "", false
	}
	end := strings.Index(rest[1:], "`")
	if end < 0 {
		return "", false
	}
	name := rest[1 : 1+end]
	return name, name != ""
}

// checkErrorRegistry 错误登记表 ↔ 源码哨兵/类型集合。
func checkErrorRegistry(root string, m *Manifest, sents []Sentinel, types []ErrType) []string {
	path := filepath.Join(root, filepath.FromSlash(errorSemanticsPath))
	if !fileRegular(path) {
		return []string{errorSemanticsPath +
			" 不存在——它是本门禁错误登记判据的判据面，删掉它等于让判据静默失效"}
	}
	table, tableTypes, docProbs, err := parseErrorTable(path)
	if err != nil {
		// 读不了判据面 = 判不了 = 门禁变红（而不是跳过）。
		return []string{errorSemanticsPath + " 无法解析: " + err.Error()}
	}
	probs := append([]string{}, docProbs...)

	// 源码侧集合（哨兵按包分组；类型拼成 pkg.Name）。
	codeByPkg := map[string][]string{}
	for _, s := range sents {
		codeByPkg[s.Pkg] = append(codeByPkg[s.Pkg], s.Name)
	}
	for pkg := range codeByPkg {
		sort.Strings(codeByPkg[pkg])
	}
	codeTypes := map[string]bool{}
	for _, t := range types {
		codeTypes[t.Pkg+"."+t.Name] = true
	}

	manifestSet := map[string]bool{}
	for _, e := range m.Packages {
		manifestSet[e.Pkg] = true
	}

	// ① 每个有哨兵的包：表与代码双向判差。
	pkgs := make([]string, 0, len(codeByPkg))
	for pkg := range codeByPkg {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		tableSet := map[string]bool{}
		for _, n := range table[pkg] {
			tableSet[n] = true
		}
		for _, n := range codeByPkg[pkg] {
			if !tableSet[n] {
				probs = append(probs, pkg+": 哨兵 "+n+" 未登记进 "+errorSemanticsPath+
					"——新增错误必须登记（触发条件 + 调用方处置）")
			}
		}
		for _, n := range table[pkg] {
			if !sentinelDeclared(sents, pkg, n) {
				probs = append(probs, pkg+": 表里登记的哨兵 "+n+" 在代码里不存在——幽灵条目")
			}
		}
	}

	// ② 表里出现的小节但包不在清单：段落挂错包名。
	for pkg := range table {
		if !manifestSet[pkg] {
			probs = append(probs, errorSemanticsPath+": 哨兵总表的小节 "+pkg+" 不是清单登记的包——段落名与清单 pkg 必须一致")
		}
	}

	// ③ errors.As 类型表双向判差。
	for name := range codeTypes {
		if !tableTypes[name] {
			probs = append(probs, "结构化错误类型 "+name+" 未登记进 "+errorSemanticsPath+
				" 的 errors.As 表——调用方按类型判定的错误必须给出 As 判据")
		}
	}
	for name := range tableTypes {
		if !codeTypes[name] {
			probs = append(probs, "errors.As 表里的 "+name+" 在代码里不存在——幽灵条目")
		}
	}
	return probs
}

// sentinelDeclared 代码里是否声明过该包的该哨兵。
func sentinelDeclared(sents []Sentinel, pkg, name string) bool {
	for _, s := range sents {
		if s.Pkg == pkg && s.Name == name {
			return true
		}
	}
	return false
}
