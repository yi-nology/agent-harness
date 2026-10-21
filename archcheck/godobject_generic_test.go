package archcheck

import (
	"strings"
	"testing"
)

// TestGenericInstanceStillDetectedAsGodObject 真 god object 在新键形态下**仍被检出**。
//
// # 为什么这条必须单独验
//
// 改了聚合键之后，「泛型实例化各自成条」——若实现顺手把计数口径也改了
// （比如按实例化拆开后，每个实例化的方法数都变少），真 god object 可能
// 反而检不出来 ⇒ **门禁变绿**（最贵的失败形态）。
//
// 故这里钉住：一个实例化挂 30 个实质方法 ⇒ 必须被点名，且方法数是 30。
func TestGenericInstanceStillDetectedAsGodObject(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\ntype Manager[T any] struct{}\n\n")
	for i := range 30 {
		b.WriteString("func (m *Manager[int]) M")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(itoa(i))
		b.WriteString("() { x := 1; _ = x; y := 2; _ = y }\n")
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pa/a.go": b.String()})
	got, err := godObjects(root, []string{"pa"}, 25, defaultForwardingShellMaxChars)
	if err != nil {
		t.Fatalf("godObjects 不得报错: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("真 god object 必须被检出（门禁对它失明 = 最贵的失败形态）")
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "pa.Manager[int] (30 个方法") {
		t.Fatalf("应点名 pa.Manager[int] 且方法数为 30，实得 %v", got)
	}
}

// itoa 极简整数转字符串（避免为测试引入 fmt 依赖）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
