package offload

import (
	"strings"
	"testing"
)

// 占位文本契约单测：格式逐字节稳定 / 解析往返 / 拒判面（宁可漏判不误判）/
// 占位体量与阈值的关系（阈值取舍的机械判据）。

// ref 合成占位引用。
func ref() Ref {
	return Ref{Kind: "image", ID: "abcdef012345", Path: "blobs/ab/abcdef0123456789.png", Bytes: 20480, MimeType: "image/png"}
}

// TestRenderContainsEveryField 六个要素必须在文本里齐备：类型、截断摘要、
// 取回路径、字节数、媒体类型、取回指引。缺一项就有一类问题变得不可排查。
func TestRenderContainsEveryField(t *testing.T) {
	out := Render(ref(), "")
	for _, want := range []string{
		"kind=image", "id=abcdef012345", "path=blobs/ab/abcdef0123456789.png",
		"bytes=20480", "mime=image/png", DefaultRetrieveHint,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("占位文本缺少 %q: %s", want, out)
		}
	}
}

// TestRenderOmitsEmptyMime 媒体类型缺失时该字段整体省略（不留空值噪声）。
func TestRenderOmitsEmptyMime(t *testing.T) {
	r := ref()
	r.MimeType = ""
	out := Render(r, "")
	if strings.Contains(out, "mime=") {
		t.Fatalf("空 mime 不应渲染该字段: %s", out)
	}
	if !strings.Contains(out, "kind=image") {
		t.Fatalf("其余字段不应受影响: %s", out)
	}
}

// TestRenderCustomHint 宿主可覆盖取回指引（取回入口由宿主决定，内核不写死工具名）。
func TestRenderCustomHint(t *testing.T) {
	out := Render(ref(), "取回入口 srv-beta://blob-alpha")
	if !strings.Contains(out, "srv-beta://blob-alpha") {
		t.Fatalf("自定义指引应出现: %s", out)
	}
	if strings.Contains(out, DefaultRetrieveHint) {
		t.Fatalf("自定义指引应取代缺省: %s", out)
	}
}

// TestRenderDeterministic 同一输入必须产出逐字节相同的文本——内容寻址的收益
// （历史前缀命中提示缓存）全靠这一条。
func TestRenderDeterministic(t *testing.T) {
	a, b := Render(ref(), ""), Render(ref(), "")
	if a != b {
		t.Fatalf("同输入应逐字节一致:\n%s\n%s", a, b)
	}
	if len(a) != len(b) {
		t.Fatalf("长度应一致")
	}
}

// TestPlaceholderBodySmallerThanThreshold 阈值取舍的机械判据：占位体量必须
// 远低于阈值，否则「跨过阈值即净赚」的说法不成立，MinBytes 该下调。
func TestPlaceholderBodySmallerThanThreshold(t *testing.T) {
	n := len(Render(ref(), ""))
	if n >= MinBytes/8 {
		t.Fatalf("占位文本 %d 字节相对阈值 %d 过大，阈值取舍需重估", n, MinBytes)
	}
	t.Logf("占位文本 %d 字节 / 阈值 %d 字节", n, MinBytes)
}

// TestParseRoundTrip 解析往返：消费方不重算摘要即可还原引用。
func TestParseRoundTrip(t *testing.T) {
	want := ref()
	got, ok := Parse(Render(want, "取回指引 srv-beta"))
	if !ok {
		t.Fatalf("占位文本应可解析")
	}
	if got != want {
		t.Fatalf("往返不符: %+v vs %+v", got, want)
	}
}

// TestParseEmbedsInMessage 解析面允许文本中出现占位片段（消息里不止这一段）。
func TestParseEmbedsInMessage(t *testing.T) {
	msg := "上文提到过该对象。\n" + Render(ref(), "") + "\n以上。"
	got, ok := Parse(msg)
	if !ok || got.Path != ref().Path {
		t.Fatalf("片段解析不符: %+v ok=%v", got, ok)
	}
}

// TestParseRejects 拒判面：宁可漏判（当普通文本）也不误判——把普通文本错认成
// 外置占位会让宿主去取一个并不存在的键。
func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"普通文本":  "这里没有任何占位文本",
		"未闭合":   "[外置对象 kind=image id=abc path=p bytes=1 mime=x",
		"字节数非法": "[外置对象 kind=image id=abc path=p bytes=many mime=x]",
		"缺路径":   "[外置对象 kind=image id=abc bytes=1 mime=x]",
		"缺类型":   "[外置对象 id=abc path=p bytes=1]",
		"缺摘要":   "[外置对象 kind=image path=p bytes=1]",
		"缺字节数":  "[外置对象 kind=image id=abc path=p]",
		"无等号字段": "[外置对象 kindimage idabc]",
	}
	for name, in := range cases {
		if _, ok := Parse(in); ok {
			t.Fatalf("%s: 不应判为占位文本", name)
		}
	}
	if _, ok := Parse(""); ok {
		t.Fatalf("空串不应判为占位文本")
	}
}

// TestAppliedPlaceholderIsParseable 端到端：Apply 产出的占位文本必须能被
// 本包的解析面还原——写入侧与读取侧同一契约，不靠约定。
func TestAppliedPlaceholderIsParseable(t *testing.T) {
	st, fs := NewState(), newStore()
	b := blob("image", ".png", MinBytes+512)

	if rep := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs}); rep.Items[0].Replaced {
		t.Fatalf("首轮不应有占位文本")
	}
	rep := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs, RetrieveHint: "取回入口 srv-beta://blob-alpha"})
	got, ok := Parse(rep.Items[0].Placeholder)
	if !ok {
		t.Fatalf("产出的占位文本应可解析: %s", rep.Items[0].Placeholder)
	}
	if got.Kind != "image" || got.MimeType != "application/octet-stream" || got.Bytes != MinBytes+512 {
		t.Fatalf("还原字段不符: %+v", got)
	}
	if got.Path != rep.Items[0].Path || got.ID != TruncatedHash(rep.Items[0].Hash) {
		t.Fatalf("占位文本内的路径/摘要须与条目逐字一致: %+v vs %+v", got, rep.Items[0])
	}
	// 解析出的路径必须真的落在落盘面上——占位文本若指向空键，取回必然落空。
	if len(fs.data[got.Path]) == 0 {
		t.Fatalf("占位文本指向的键在落盘面上不存在: %s", got.Path)
	}
}
