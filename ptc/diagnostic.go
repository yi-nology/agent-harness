// 诊断面：出错位置与面向模型的修复建议。
//
// # 为什么诊断要带位置
//
// 本包的调用方是模型。模型写的程序出了错，只回一句「invalid result」对它几乎没有
// 可用信息——它不知道是哪一行，也不知道该改什么，只能原样重写一遍碰运气。
// 而「第 12 行」这一个数字，能把重试从盲猜变成定点修。
//
// 对照 OpenCode packages/codemode：它的 Diagnostic 是
// `{ kind, message, location{line,column}, suggestions[] }`，把位置与建议列为
// 契约的一部分。本包此前只有 ErrorKind 一个类别串，是这条差距的另一半。
//
// # 位置怎么来
//
// 语法错误一条来源、运行期一条来源，两条的可得性**不一样**，这是事实不是缺陷：
//
//   - 语法错误：goja.CompilerSyntaxError 嵌的 CompilerError 带 File 与 Offset，
//     而 (*goja.File).Position 是 goja/file 包里的**导出**方法，直接得行列。
//     拿到后折算回程序内行号（包裹只多一行）。
//
//   - 运行期异常：只有当错误经 (*Runtime).RunProgram 直接返回时，
//     才会拿到 *goja.Exception，其 Stack() 里有程序帧可定位。
//
//     但本包的程序体是 **async IIFE**：函数体里抛出的异常会变成 Promise rejection，
//     RunProgram 本身正常返回，rejection 原因只是一个值，拿不到 goja 侧的历史栈。
//     goja 未提供「rejection 处回溯调用栈」的公开面。
//
// 所以运行期异常的 Location **经常是空的**。这是工具能力边界，如实空着——
// 编一个位置出来会让模型去改一行没问题的代码，比没有位置更坏。
// 宿主若有更强的定位手段（如绑定调用自带行列），可在包装层补齐 Location。
package ptc

import (
	"regexp"
	"strconv"

	"github.com/dop251/goja"
	"github.com/oekit/pkg/errors"
)

// Position 程序内的出错位置。Line/Column 均为 1-based；零值表示位置不可得。
type Position struct {
	Line   int
	Column int
}

// Valid 位置是否可得。
func (p Position) Valid() bool { return p.Line > 0 }

// programSourceName 编译时使用的源名；取栈帧时按它筛出属于程序源码的帧。
const programSourceName = "program"

// wrapLines 是 wrapProgram 在程序体之前引入的额外行数。
//
// 显式定义而不在折算处写死 1：写死等于把「源码一定被包裹过」这个前提藏进函数里，
// 调用方一改包裹方式，行号就会无声错开一行——而错一行的行号比没有行号更坏。
const wrapLines = 1

// wrapProgram 把用户程序包成 async IIFE。
//
// 单独抽出来是因为行号折算与编译必须用**同一份**字符串：包裹只加了一行，
// 故程序内行号 = 包裹后行号 - 1。
func wrapProgram(program string) string {
	return ";(async function(){\n" + program + "\n})()"
}

// lineColRe 从错误文本里兜底捞 `Line N:C`。
//
// # 为什么需要兜底
//
// goja 的 CompilerSyntaxError 在**单个**错误时有 File/Offset 可用；但解析器一次性
// 报多个错误时（消息尾部的 "(and N more errors)"），File 为 nil，位置只以文本形式
// 留在 Message 里（`program: Line 4:10 ...`）。这是 goja 内嵌 JS parser 的措辞，
// **不是** goja/file.Position 的 String()（后者是 `program:4:10`）——即它不是稳定契约。
//
// 故明确把它降级为「尽力而为」：格式一旦变化，只会让本包**失去**位置（退化成零值），
// 而不会产生假位置。假位置会让模型去改一行没问题的代码，比没有位置坏得多。
var lineColRe = regexp.MustCompile(`Line (\d+):(\d+)`)

// locationOfSyntax 语法错误的位置，折算回**程序内**行号。
// wrapLines 见上：直接编译裸程序时传 0。
func locationOfSyntax(err error, wrapLines int) Position {
	var cse *goja.CompilerSyntaxError
	if !errors.As(err, &cse) {
		return Position{}
	}
	var pos Position
	if cse.File != nil {
		// 结构化路径：用 goja 自己的 File.Position，而不是自己数换行——
		// 偏移→行列的折算规则（制表、Unicode、Base 偏移）只有 goja 知道。
		p := cse.File.Position(cse.Offset)
		pos = Position{Line: p.Line, Column: p.Column}
	} else if m := lineColRe.FindStringSubmatch(cse.Message); m != nil {
		l, err1 := strconv.Atoi(m[1])
		c, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil {
			pos = Position{Line: l, Column: c}
		}
	}
	if pos.Line <= 0 {
		return Position{}
	}
	pos.Line -= wrapLines
	if pos.Line < 1 {
		return Position{}
	}
	return pos
}

// locationOfException 从**直接返回**的运行期异常栈里取程序源码的位置。
//
// 取最靠上的 program 帧：异常可能从宿主绑定里抛出再被程序 catch，那类栈顶帧的
// SrcName 是 "<native>"，不是程序源码，报它等于让模型去改一个它改不了的地方。
//
// async IIFE 的 rejection 走不到这里（见文件头），取不到就是取不到。
func locationOfException(err error, wrapLines int) Position {
	var ex *goja.Exception
	if !errors.As(err, &ex) {
		return Position{}
	}
	for _, f := range ex.Stack() {
		if f.SrcName() != programSourceName {
			continue
		}
		pos := f.Position()
		if line := pos.Line - wrapLines; line >= 1 {
			return Position{Line: line, Column: pos.Column}
		}
	}
	return Position{}
}

// suggestionsFor 按错误类别给模型的修复建议。
//
// 写成表而不是散落在各收口分支里：建议是**给模型读的契约**，必须与类别一一对应
// 且口径一致；散落写就会随时间漂移，同一个类别两处不同措辞。
//
// 每条都写「怎么办」而不是「发生了什么」——Error 字段已经说了发生什么。
func suggestionsFor(kind string) []string {
	switch kind {
	case "deadline":
		return []string{
			"墙钟超时：把长耗时操作拆成多次调用，或先取小结果再决定是否继续",
			"程序内不要 await 一个永不落定的 Promise（VM 无定时器）",
		}
	case "budget_calls":
		return []string{
			"工具调用次数超限：把多次小调用合并成一次带筛选条件的调用",
			"必要时改用结构化查询一次取回，而不是逐条遍历",
		}
	case "budget_bytes":
		return []string{
			"返回字节超限：不要把工具原始输出原样 return，用 map/filter 聚合后只返回小结果",
			"原始数据在程序内处理，不要带过模型面",
		}
	case "pending":
		return []string{
			"await 永不落定：VM 无定时器，宿主绑定均为同步调用，程序内不存在会自己 resolve 的 Promise",
			"改造成对已 resolve 值的同步调用链",
		}
	case "bad_result":
		return []string{
			"return 值必须是 JSON 可序列化的：不能返回函数或循环引用",
			"返回数组/对象字面量即可；字符串请确保是合法 JSON 文本",
		}
	case "syntax":
		return []string{
			"检查括号、引号、模板字符串是否配对",
			"程序体是 async 函数体：顶层可直接用 await 与 return",
		}
	case "binding_collision":
		return []string{
			"这是装配期缺陷而非程序问题：两个工具名净化成同一个 host 标识符（如 get-logs 与 get_logs）",
			"请宿主重命名其中一个工具后重试，程序本身无需改动",
		}
	case "runtime":
		return []string{
			"宿主绑定抛出的错误会直接冒泡：确认调用参数形状与该工具的契约一致",
			"可以用 try/catch 包裹可疑调用，让程序带着已取得的部分结果继续",
		}
	default:
		return nil
	}
}

// describePosition 渲染位置，供人读的错误串用。
func describePosition(p Position) string {
	if !p.Valid() {
		return ""
	}
	return "（第 " + strconv.Itoa(p.Line) + " 行第 " + strconv.Itoa(p.Column) + " 列）"
}
