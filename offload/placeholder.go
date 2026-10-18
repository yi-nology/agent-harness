// 占位文本契约：外置对象在消息历史里留下的那一小段替身文本。
//
// ## 为什么要有格式与解析两件套
//
// 占位文本是**跨轮复用的字符串**：内容寻址保证同一份内容逐轮得到逐字节相同的
// 占位文本，历史前缀因此能命中提示缓存——这正是外置收益的一半（另一半是不重发本体）。
// 一旦它可被机械解析，宿主才能在不重新计算摘要的前提下回答「这条历史上外置过什么」
// （对账、回收、审计）。故本文件与 spill 的 TruncHint/ParseHint 同纪律：格式单源，
// 改格式即改契约。
//
// ## 字段取舍
//
// 每个字段都有存在理由，缺一个就会有一类问题变成不可排查：
//
//   - kind/id：模型据此判断「这是什么东西、同一 id 是否同一份字节」；
//   - path：取回入口。**必须与真实落盘键逐字一致**，否则模型按图索骥必然落空；
//   - bytes：模型据此估算取回成本，决定要不要取；
//   - mime：决定取回后如何解释；缺失时模型只能盲解。
//
// 解析面要求各字段不含空格（path 已由 StorePath 净化保证），这是格式契约的一部分。
package offload

import (
	"strconv"
	"strings"
)

const (
	// markerPrefix 占位文本的定长前缀（解析面的定位锚）。
	markerPrefix = "[外置对象 "

	// markerSuffix 字段区结束符。
	markerSuffix = "]"

	// DefaultRetrieveHint 缺省取回指引。
	//
	// 指引不写死具体工具名：取回入口由宿主决定（本地文件、对象存储、专用工具……），
	// 内核写死某个名字只会在宿主换实现后变成误导——指向一个不存在的工具
	// 比不给指引更糟。宿主有自定义时经 Request.RetrieveHint 覆盖。
	DefaultRetrieveHint = "原文不在本条消息内；需要时按 path 取回（id 相同即字节相同）。"
)

// Ref 占位文本的可解析部分（取回指引不在契约内，由宿主自定故不解析）。
type Ref struct {
	// Kind 对象类型。
	Kind string

	// ID 摘要前缀（见 TruncatedHash）。
	ID string

	// Path 逻辑落盘键，须与实际落盘键逐字一致。
	Path string

	// Bytes 原始字节数。
	Bytes int

	// MimeType 媒体类型；空则不渲染该字段。
	MimeType string
}

// Render 生成占位文本。
//
// 缺省形态 191 字节（由 TestPlaceholderBodySmallerThanThreshold 钉住上限）——这是 MinBytes 取 4 KiB 的前提：小到占位体量的若干分之一，
// 跨过阈值后替换一定是净赚（见 offload.go 包注释「关键取舍一」）。
// 同一 (Ref, hint) 必须产出**逐字节相同**的文本，故渲染内不含时间戳、序号、
// 随机数等任何随轮变化的成分。
func Render(r Ref, hint string) string {
	h := strings.TrimSpace(hint)
	if h == "" {
		h = DefaultRetrieveHint
	}
	var b strings.Builder
	b.WriteString(markerPrefix)
	b.WriteString("kind=" + r.Kind)
	b.WriteString(" id=" + r.ID)
	b.WriteString(" path=" + r.Path)
	b.WriteString(" bytes=" + strconv.Itoa(r.Bytes))
	if r.MimeType != "" {
		b.WriteString(" mime=" + r.MimeType)
	}
	b.WriteString(markerSuffix)
	b.WriteString(" ")
	b.WriteString(h)
	return b.String()
}

// Parse 解析占位文本：命中定长前缀且字段齐全才返回 true，否则返回 (零值, false)。
//
// 反向的宽松度要求与写入侧对称：宁可漏判（当作普通文本）也不误判——
// 把一段普通文本错认成外置占位会让宿主去取一个并不存在的键。
func Parse(text string) (Ref, bool) {
	i := strings.Index(text, markerPrefix)
	if i < 0 {
		return Ref{}, false
	}
	rest := text[i+len(markerPrefix):]
	j := strings.Index(rest, markerSuffix)
	if j < 0 {
		return Ref{}, false
	}
	var r Ref
	var n int
	for _, kv := range strings.Split(rest[:j], " ") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "kind":
			r.Kind = v
		case "id":
			r.ID = v
		case "path":
			r.Path = v
		case "bytes":
			b, err := strconv.Atoi(v)
			if err != nil {
				return Ref{}, false // 字节数不可解析即契约破损，不猜
			}
			r.Bytes = b
			n++
		case "mime":
			r.MimeType = v
		}
	}
	if r.Kind == "" || r.ID == "" || r.Path == "" || n == 0 {
		return Ref{}, false
	}
	return r, true
}
