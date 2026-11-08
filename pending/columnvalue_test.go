package pending

import (
	"strings"
	"testing"
)

// TestColumnValueAlwaysProducesDecodableJSON 钉住「列内容恒 JSON」这条承诺。
//
// ColumnValue 原先的守卫只有 strings.HasPrefix(clarify, "{")，于是形似 JSON
// 但解不回的串原样落列：Parse 返回零值，挂起态被**静默丢掉**——不是降级，是消失。
func TestColumnValueAlwaysProducesDecodableJSON(t *testing.T) {
	cases := []string{
		"{节点A}和{节点B}", // 以 { 开头但不是 JSON（原先的原样透传陷阱）
		"{",           // 截断
		`{"foo":1}`,   // 合法 JSON 但不是 State（无 kind）
		Encode(State{Kind: "disambig", Word: "甲"}), // 真 Encode 产物，应原样透传
		"裸关键词",
		`"看起来像字符串"`,
	}
	for _, in := range cases {
		out := ColumnValue(in, "disambig")

		s := Parse(out)
		if s.Kind == "" {
			t.Fatalf("输入 %q → 列值 %q 解不出 Kind：挂起态被静默丢掉", in, out)
		}
		// 原始内容不能丢：透传产物保留原样，其余包进 Word。
		if in == Encode(State{Kind: "disambig", Word: "甲"}) {
			if out != in {
				t.Fatalf("已是 Encode 产物的输入应原样透传：%q → %q", in, out)
			}
		} else if s.Word != in {
			t.Fatalf("输入 %q 应被完整保留在 Word 里，实得 %q", in, s.Word)
		}
	}
}

// TestColumnValueEmptyUnchanged 空值语义不变：不该凭空造出一个挂起态。
func TestColumnValueEmptyUnchanged(t *testing.T) {
	if got := ColumnValue("", "disambig"); got != "" {
		t.Fatalf("空输入应原样返回空，实得 %q", got)
	}
}

// TestColumnValueEmptyKindPanics 零值 kind 必须 fail-fast，不得落列（issue #54）。
//
// 此前 kind=="" 时 Encode(State{Kind: "", Word: clarify}) 产物缺 k 键，读路径
// Parse 判 s.Kind != "" 失败返回零值——写入成功、读回为零、两头都不报错，
// 「挂起态静默消失」在本函数自己的零值入参下复现。
func TestColumnValueEmptyKindPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("kind 为空且 clarify 非空应 panic（fail-fast），不得落列")
		}
		if s, ok := r.(string); !ok || !strings.Contains(s, "kind") {
			t.Fatalf("panic 消息应指明 kind 缺失，实得 %v", r)
		}
	}()
	_ = ColumnValue("裸关键词", "")
}

// TestColumnValueEmptyKindWithEmptyClarify clarify 为空时 kind 零值无关紧要：
// 没有内容就没有挂起态，原样返回空（fail-fast 只守「有内容要落列」的路径）。
func TestColumnValueEmptyKindWithEmptyClarify(t *testing.T) {
	if got := ColumnValue("", ""); got != "" {
		t.Fatalf("空输入+空 kind 应原样返回空，实得 %q", got)
	}
}
