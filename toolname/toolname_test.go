package toolname

import "testing"

// TestIsJSIdent 判定规则：字母数字下划线美元，首位不能是数字。
func TestIsJSIdent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"get_logs", true},
		{"_x", true},
		{"$x", true},
		{"x9", true},
		{"9x", false},
		{"", false},
		{"get-logs", false},
		{"get.logs", false},
		{"get logs", false},
		{"日志", false},
	}
	for _, c := range cases {
		if got := IsJSIdent(c.in); got != c.want {
			t.Fatalf("IsJSIdent(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// TestNormalize 规整规则：非法字符归 _，首位数字补 _，前导非法字符跳过。
func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"get_logs", "get_logs"},
		{"get-logs", "get_logs"},
		{"get.logs", "get_logs"},
		{"9lives", "_9lives"},
		{"-x-", "x_"},
		{"---", ""},
		{"", ""},
		{"a-b-c", "a_b_c"},
		{"$x", "$x"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Fatalf("Normalize(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeOutputIsAlwaysIdent 规整结果必须是合法标识符。
//
// 这条是本包存在的全部理由：规整出来的名字会被挂到程序里，
// 结果不合法就等于挂了个取不到的东西。
func TestNormalizeOutputIsAlwaysIdent(t *testing.T) {
	for _, in := range []string{"get-logs", "9lives", "-x-", "a b c", "a.b.c", "日志名", "$x", "x"} {
		out := Normalize(in)
		if out == "" {
			continue // 全非法：调用方自行处理
		}
		if !IsJSIdent(out) {
			t.Fatalf("Normalize(%q) = %q，结果不是合法标识符", in, out)
		}
	}
}

// TestNormalizeIdempotent 规整一次之后再规整，结果不变。
//
// 不变才说明「规整后的名字就是程序里的名字」——否则同一个名字在两处
// 规整会得到两个不同的结果，契约照样会断。
func TestNormalizeIdempotent(t *testing.T) {
	for _, in := range []string{"get-logs", "9lives", "-x-", "a b", "$x"} {
		once := Normalize(in)
		if twice := Normalize(once); twice != once {
			t.Fatalf("Normalize 不幂等：%q → %q → %q", in, once, twice)
		}
	}
}

// TestNormalizeCollision 规整不保证唯一：调用方必须自己判撞名。
//
// 钉住这个事实，免得有人以为本包已经处理了撞名。
func TestNormalizeCollision(t *testing.T) {
	if Normalize("get-logs") != Normalize("get_logs") {
		t.Fatal("带横线与不带横线的名字应规整到同一个标识符（撞名由调用方判）")
	}
}
