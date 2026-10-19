// imports.go —— import 规则门禁（AST 判定，不是逐行正则）。
//
// # 为什么 import 规则不能用正则逐行判
//
// import 声明的书写形态远不止一种，逐行正则必然漏：
//
//	import "errors"                  单行
//	import ("errors")                单行带括号
//	import (                         块
//	    "errors"                      ← 整行就是一个裸路径
//	)
//	import e "errors"                别名
//	import _ "errors"                空白导入
//	import ( e "errors" )            块 + 别名
//
// 旧实现用 `^\s*"errors"$` 这类正则去匹配**整行文本**，于是：
//
//   - 单行写法 `import "errors"` 漏（行首不是引号）
//   - 任何带别名的写法 `stderrors "errors"` 漏（行首是别名不是引号）
//   - 块里的裸路径能匹配上，但只要加了别名就漏
//
// 漏掉的形态不是「少见写法」，而是**本仓自己在用的主流写法**：本仓大量使用
// 单行 import（`import "context"`、`import "strings"`）。也就是说这条规则
// 对相当一部分文件从来没生效过。
//
// 失败形态是门禁**变绿**：违反规则的代码就在仓里，规则却报「无阻断级违反」。
//
// # 判据
//
// forbid_import_regex 的匹配对象是 **import 路径字符串**（不含引号），不是源码行。
// 这个语义本就是字段名的意思（forbid_**import**_regex），旧实现把匹配对象
// 错记成了行文本——是实现偏离了字段名，不是字段名写错了。
//
// 路径匹配同时覆盖：单行/块、别名/空白导入、点导入，以及任何未来新增的书写形态。
// 判据一旦落到路径上，就不再随书写形态漂移。

package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
)

// importViolations 按 import 路径判定 forbid_import_regex 规则。
//
// 解析失败必须响亮失败（返回 error → 门禁退出码 2），不得静默跳过该文件：
// 静默跳过等于「该文件对本规则隐形」，而语法坏掉的文件本就编译不过，
// 真正的风险是「门禁在这个文件上给不出任何结论，却显示成通过」。
func importViolations(root string, r rule) ([]violation, error) {
	re, err := regexp.Compile(r.ForbidImport)
	if err != nil {
		return nil, fmt.Errorf("规则 %s forbid_import_regex 非法: %w", r.ID, err)
	}
	files, err := ruleFiles(root, r)
	if err != nil {
		return nil, err
	}

	var out []violation
	fset := token.NewFileSet()
	for _, path := range files {
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil, fmt.Errorf("规则 %s 解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", r.ID, path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		for _, im := range f.Imports {
			p, uerr := strconv.Unquote(im.Path.Value)
			if uerr != nil {
				continue
			}
			if !re.MatchString(p) {
				continue
			}
			out = append(out, violation{
				rule: r.ID,
				path: rel,
				line: fset.Position(im.Path.Pos()).Line,
				text: fmt.Sprintf("import %q（禁止的依赖路径）", p),
				kind: "forbid_import",
			})
		}
	}
	return out, nil
}
