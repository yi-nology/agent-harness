// 从工具的 schema 派生位置调用形态（ptc 的 Binding.Signature）。
//
// # 为什么要有这个函数，而不是让宿主手写字符串
//
// Signature 是「这个工具有哪些参数、各自叫什么」的第二份描述。手写就意味着它与
// Schema 是两份需要同步的数据：宿主给工具加了参数却忘了改签名，模型就会按旧的
// 签名调用，多出来的参数传不进去、少传的直接被元数校验拦下，两种都是「工具明明
// 支持，模型却调不对」，而排查时极容易先怀疑模型。
//
// 单一事实源是 Schema。派生则把同步这件事从「靠人记得」变成「不可能忘记」。
//
// # 为什么参数顺序必须确定
//
// Schema.Properties 是 **map**，Go 的 map 迭代顺序是随机的。签名是**给模型读的文本**，
// 同一份 schema 两次派生得到两种顺序，模型每次看到的都不一样。故顺序取：
//   - 必填参数按 Schema.Required 的**声明顺序**（宿主已经表达过意图，不打乱它）；
//   - 可选参数按名称**升序**（Required 里没有顺序可言）。
//
// 这个顺序同时满足 ptc 侧「可选形参必须排在必填之后」的约束。
//
// # 派生不出来时怎么办：返回错误，不硬凑
//
// 常见的三种「派生不出来」：
//
//   - 参数名不是合法 JS 标识符（如带 `-` 或以数字开头）。位置形参没法表达它，
//     而**改名会改变 JSON 键名**——那等于把一个参数悄悄换掉。故报错。
//   - Required 里点名了一个 Properties 中不存在的参数。schema 本身不自洽。
//     故报错——静默忽略的话，这个参数对模型就是不存在的，而宿主以为它必填。
//   - 退化占位 schema（Properties 为 nil）。那是「尚未取回 schema」的形态，
//     此时派生出一个空签名会让程序跑起来却什么都不传，比报错更坏。故报错。
//
// 三者都返回错误而不是产出一个看似能用的字符串：本包的纪律是宁可装配失败，
// 也不让一个「看起来能用、实际不对」的东西进到模型面前。

package tooldisclose

import (
	"sort"
	"strings"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/toolname"
)

// ErrSchemaNotDerivable 该工具的 schema 派不出合法的调用形态。errors.Is 判定。
var ErrSchemaNotDerivable = errors.New("工具 schema 派不出合法的位置调用形态")

// SignatureOf 从工具 schema 派生位置调用形态，形如 `get_logs(host, count?)`。
//
// 返回的字符串直接可作为 ptc.Binding.Signature 使用；两者由本函数保证同一口径，
// 不需要调用方再对一次。
//
// # 函数名是**净化后**的名字，不是目录里的原名
//
// 程序里真正挂上去的是净化后的名字：ptc 把绑定挂在 `host.<净化名>` 上，
// 而净化规则（`toolname.Normalize`）会改掉名字的形状——`get-logs` 挂上去
// 叫 `host.get_logs`。所以形态串里的函数名也必须是净化后的那个。
//
// 旧实现照抄目录里的原名，于是对任何名字带非法字符的工具（`get-logs`、
// `restart-service` 这类带横线的名字极常见）派出的形态**在 ptc 侧必被拒**：
// ptc 校验「形态里的函数名 == 净化后的绑定名」，而这里是原名。
//
// 而本函数的文档却写着「直接可用、不需要调用方再对一次」——**文档与代码
// 自相矛盾**，症状是「工具明明在目录里，程序就是调不通」，排查时极容易
// 先怀疑模型。同一份文档的上一版甚至把这条矛盾**用测试钉住了**（钉的是
// 「会被拒」），于是两边的错一起被固化成了「预期行为」。
//
// 现在两侧共用同一条规则（`toolname` 包），派生态与消费端**不可能**再漂移。
func SignatureOf(t Tool) (string, error) {
	name := t.Name
	if name == "" {
		return "", errors.Wrap(ErrSchemaNotDerivable, "工具名为空")
	}
	// 函数名必须是净化后的合法标识符：它是模型在程序里实际调用的那个名字。
	// 净化不出合法标识符（全非法字符）时报错而不是产出一个空壳形态——
	// 空壳会让程序里 `host.<空名>` 根本不存在，症状是「调用了一个不存在
	// 的函数」，远不如「这个名字用不了」直观。
	ident := toolname.Normalize(name)
	if ident == "" {
		return "", errors.Wrap(ErrSchemaNotDerivable, "工具名规整不出合法标识符", "tool", name)
	}
	// 退化占位：schema 尚未取回。此时派生空签名会让程序照跑、宿主收不到任何参数，
	// 表现为「工具调了但什么都没传」——比当场报错难查得多。
	if len(t.Schema.Properties) == 0 {
		return "", errors.Wrap(ErrSchemaNotDerivable, "schema 无参数定义（退化占位或本就无参）", "tool", name)
	}

	inProps := func(p string) bool {
		_, ok := t.Schema.Properties[p]
		return ok
	}

	var required []string
	seen := make(map[string]bool, len(t.Schema.Required))
	for _, p := range t.Schema.Required {
		if seen[p] {
			return "", errors.Wrap(ErrSchemaNotDerivable, "Required 里参数重名", "tool", name, "param", p)
		}
		seen[p] = true
		if !inProps(p) {
			return "", errors.Wrap(ErrSchemaNotDerivable,
				"Required 点名的参数不在 Properties 里（schema 不自洽）", "tool", name, "param", p)
		}
		if !toolname.IsJSIdent(p) {
			return "", errors.Wrap(ErrSchemaNotDerivable,
				"参数名不是合法 JS 标识符，无法作为位置形参（改名会改掉 JSON 键名）",
				"tool", name, "param", p)
		}
		required = append(required, p)
	}

	var optional []string
	for p := range t.Schema.Properties {
		if seen[p] {
			continue
		}
		if !toolname.IsJSIdent(p) {
			return "", errors.Wrap(ErrSchemaNotDerivable,
				"参数名不是合法 JS 标识符，无法作为位置形参（改名会改掉 JSON 键名）",
				"tool", name, "param", p)
		}
		optional = append(optional, p)
	}
	sort.Strings(optional) // map 迭代顺序随机，而签名是给模型读的文本

	// 带空格分隔：这段文本是给模型读的，`host, count` 比 `host,count` 更易读，
	// 而多出的字节在任何预算口径下都可忽略。
	parts := make([]string, 0, len(required)+len(optional))
	parts = append(parts, required...)
	for _, p := range optional {
		parts = append(parts, p+"?")
	}
	return ident + "(" + strings.Join(parts, ", ") + ")", nil
}
