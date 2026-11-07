// egressprobe.go —— 出口兑现的**调用证据**判定（AST 走法）。
//
// 与 egresspaths.go 分文件是因为两个关注点：那边判「登记与事实是否一致」，
// 这里判「函数体里有没有那**两个**调用」。合并会让 egresspaths.go 撞上
// max_file_lines，而按行随便切一刀又会让「怎么认证据」这件事散落两处。
//
// # 判据认结构，不认文本
//
// 三种书写形态都能命中：包限定（\`egress.Env(...)\`）、接收者调用（\`spec.Env(...)\`，
// 前提是本文件 import 了 egress）、以及 SatisfiedBy 的同款形态。逐行正则只认第一种，
// 而绕过去不需要动机——只需换个导入写法。
package archcheck

import (
	"go/ast"
	"slices"
)

// hasEgressMethodCall 判函数体里是否调用了 names 里的方法，且接收者是 egress。
//
// 认两种形态（都是真实调用形态，不是文本匹配）：
//
//   - 包限定调用：egress.Env(...) / egress.Verify(...)；
//   - 接收者调用：spec.Env(...) / spec.Verify(...)，前提是**本文件确实 import 了
//     egress**（否则那个 .Env 可能是任何东西的 Env）。
//
// 第二种必须带 import 前提：只按方法名认人会把 re.Env 这类无关调用也算成兑现证据，
// 那样这条判据就变成「函数里随便有个 .Env( 就过」。
//
// 出口兑现的**两半**共用这一份走法，靠调用点传入不同的名字列表区分：plumbing 问
// 「这一档落成变量了吗」，capability 问「这一档有人强制吗」。两者刻意不合成一个
// 名字列表——报错要说清**缺的是哪一半**，「看不见 env 生成」与「看不见能力问询」
// 的修法完全不同（前者加 Env，后者加后端）。
func hasEgressMethodCall(body *ast.BlockStmt, imports map[string]string, names []string) bool {
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
		if !slices.Contains(names, sel.Sel.Name) {
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
