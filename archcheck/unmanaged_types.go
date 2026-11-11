// unmanaged_types.go —— 逃逸口赋值判定的**类型追踪层**（v0.33.0，issue #92）。
//
// 与判定层（unmanaged_assign.go）分开：这一层只回答「这个类型/变量/表达式
// 静态可辨地持有 ptc 的逃逸面吗」，不产生违规。判据是 go/types 的等价手段
// （三趟 AST 追踪），覆盖的形态与残余盲区写在 unmanaged_assign.go 头注。
package archcheck

import (
	"go/ast"
)

// escapeVarKind 一个变量持有逃逸面的形态。
type escapeVarKind int

const (
	// escapeNone 无逃逸面（零值哨兵：「这个类型/变量与逃逸口无关」）。
	// 显式立一项而不是拿第一个真形态当零值——否则「直接持有」（第一个真形态）
	// 会被所有 `kind != 0` 的哨兵检查当成「无关」。
	escapeNone escapeVarKind = iota
	// escapeDirect 变量（或其指针）本身就是 ptc 的逃逸类型。
	escapeDirect
	// escapeStruct 变量的类型是本包（被扫目录）定义的结构体，其字段带逃逸类型
	//（escapeFields 记字段名；embedded 表示直接嵌入——字段提升到外层）。
	escapeStruct
	// escapeContainer 变量是「逃逸类型的切片 / map / 数组」容器。
	escapeContainer
)

// escapeVar 一个变量/类型的静态「持有逃逸面」形态。
type escapeVar struct {
	kind     escapeVarKind
	typeName string          // escapeStruct 时的结构体类型名
	fields   map[string]bool // escapeStruct 时携带逃逸类型的字段名集
	embedded bool            // escapeStruct 时是否为直接嵌入（字段提升）
}

// escapeTracker 一个文件的类型追踪器：import 表（限定符 → 路径）+ 本包结构体形态。
//
// 逐文件持有 import 表、逐包共享结构体表——包级类型在 a.go 声明、在 b.go
// 赋值时照样认得出，而限定符按**本文件**的 import 解析（同名限定符在不同文件
// 可以指向不同包，这份表不能共享）。
type escapeTracker struct {
	imports map[string]string
	structs map[string]*escapeVar
}

// kindOfType 由类型表达式判「持有形态」（无逃逸面时 kind=0）。
func (tr *escapeTracker) kindOfType(t ast.Expr) escapeVar {
	switch ty := t.(type) {
	case *ast.SelectorExpr:
		if tr.selectorIsEscape(ty) {
			return escapeVar{kind: escapeDirect}
		}
	case *ast.StarExpr:
		return tr.kindOfType(ty.X)
	case *ast.Ident:
		if s, ok := tr.structs[ty.Name]; ok {
			return *s
		}
	case *ast.ArrayType:
		if k := tr.kindOfType(ty.Elt); k.kind == escapeDirect {
			return escapeVar{kind: escapeContainer}
		}
	case *ast.MapType:
		if k := tr.kindOfType(ty.Value); k.kind == escapeDirect {
			return escapeVar{kind: escapeContainer}
		}
	}
	return escapeVar{}
}

// kindOfExpr 由表达式判「持有形态」：逃逸类型的复合字面量（含取址）、

// structEscapeFields 结构体里带逃逸类型的字段集；无则返回 nil。
func (tr *escapeTracker) structEscapeFields(st *ast.StructType, typeName string) *escapeVar {
	ev := &escapeVar{kind: escapeStruct, typeName: typeName, fields: map[string]bool{}}
	for _, field := range st.Fields.List {
		hasEscape := false
		switch ty := field.Type.(type) {
		case *ast.SelectorExpr:
			hasEscape = tr.selectorIsEscape(ty)
		case *ast.StarExpr:
			if sel, ok := ty.X.(*ast.SelectorExpr); ok {
				hasEscape = tr.selectorIsEscape(sel)
			}
		}
		if len(field.Names) == 0 {
			// 嵌入字段：字段被提升到外层——对外的赋值形态就是 x.AllowUnmanaged。
			if hasEscape {
				ev.embedded = true
			}
			continue
		}
		if hasEscape {
			for _, nm := range field.Names {
				ev.fields[nm.Name] = true
			}
		}
	}
	if ev.embedded || len(ev.fields) > 0 {
		return ev
	}
	return nil
}

// selectorIsEscape 类型选择子是否指向 ptc 的逃逸类型。
