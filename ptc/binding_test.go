package ptc

import (
	"context"
	"strings"
	"testing"
)

// ok 造一个恒成功的绑定。
func okBind(name, ret string) Binding {
	return Binding{Name: name, Call: func(context.Context, string) (string, error) { return ret, nil }}
}

// TestBindingCollisionRejected 净化后撞名的绑定必须装配期就拒，不得执行。
//
// # 这条防的是「顶替」而不是「不可达」
//
// toolname.Normalize 把 `-` `.` 等非标识符字符一律映射成 `_`，于是 `get-logs` 与 `get_logs`
// 净化后同为 `get_logs`。若放任后绑覆盖前绑，程序里 `host.get_logs()` 拿到的是
// **另一个工具**：不是那个工具调不到，是**另一个工具替它执行了副作用**——
// 无报错、无日志，CallLog 里还照常记着被顶者的账。
func TestBindingCollisionRejected(t *testing.T) {
	bindings := []Binding{
		okBind("get_logs", "REAL"),  // 名字真的是 get_logs
		okBind("get-logs", "OTHER"), // 净化后撞名
	}
	r := Run(context.Background(), `return host.get_logs()`, bindings, Options{})

	if r.OK {
		t.Fatal("绑定撞名必须拒绝执行")
	}
	if r.ErrorKind != "binding_collision" {
		t.Fatalf("ErrorKind 应为 binding_collision，实得 %q（Error=%q）", r.ErrorKind, r.Error)
	}
	// 错误必须点名两个工具，否则「哪个和哪个撞了」还得回去查工具表。
	for _, want := range []string{"get_logs", "get-logs"} {
		if !strings.Contains(r.Error, want) {
			t.Fatalf("错误应点名 %q，实得 %q", want, r.Error)
		}
	}
}

// TestBindingCollisionNeverRunsSideEffects 被拒的调用**一个绑定都不能触达**。
//
// 只看「程序没拿到结果」是不够的：若实现是先建 VM 再判，被顶的那条工具的
// 构造期副作用（若有）已经发生。断言 hits 为 0 把这一点钉死。
func TestBindingCollisionNeverRunsSideEffects(t *testing.T) {
	hits := 0
	bindings := []Binding{
		{Name: "alpha_tool", Call: func(context.Context, string) (string, error) { hits++; return "A", nil }},
		{Name: "alpha.tool", Call: func(context.Context, string) (string, error) { hits++; return "B", nil }},
	}
	r := Run(context.Background(), `return host.alpha_tool()`, bindings, Options{})

	if r.OK {
		t.Fatal("撞名应拒绝")
	}
	if hits != 0 {
		t.Fatalf("被拒的程序不得触达任何绑定，实得 %d 次", hits)
	}
}

// TestBindingCollisionBeatsSyntaxError 撞名优先于语法错误暴露。
//
// 装配缺陷是根因，语法只是表象。先报语法会把人往错的方向带。
func TestBindingCollisionBeatsSyntaxError(t *testing.T) {
	// a-b 与 a_b 净化后同为 a_b；x.y 净化成 x_y，与 x 不撞，别拿它当样本。
	r := Run(context.Background(), `this is not js(((`, []Binding{
		okBind("a-b", "1"), okBind("a_b", "2"),
	}, Options{})
	if r.ErrorKind != "binding_collision" {
		t.Fatalf("程序体有语法错误时也应先报 binding_collision，实得 %q", r.ErrorKind)
	}
}

// TestNoCollisionAmongDistinctNames 常见命名形态不得被误伤——
// 这条是防「修得过头」：预检不能把本来正常的绑定表也拒掉。
func TestNoCollisionAmongDistinctNames(t *testing.T) {
	bindings := []Binding{
		okBind("read_file", "1"),
		okBind("read-file", "2"), // 与上一个同名——应当被拒，见上一条
		okBind("write_file", "3"),
		okBind("get-logs.host1", "4"),
		okBind("restart_service", "5"),
	}
	// 先只取互不冲突的一组。
	safe := []Binding{bindings[2], bindings[3], bindings[4]}
	r := Run(context.Background(), `return host.write_file() + host.restart_service()`, safe, Options{})
	if !r.OK {
		t.Fatalf("互不冲突的绑定表必须正常执行，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"35"` {
		t.Fatalf("绑定应各自可用，实得 %s", r.Value)
	}
}

// TestEmptyIdentBindingIsSkippedNotCollision 全非法字符名（挂不上 host）不算撞名。
// 该类绑定本就无处可寻，跳过是既有语义，不能被预检当成冲突拒掉整张表。
func TestEmptyIdentBindingIsSkippedNotCollision(t *testing.T) {
	r := Run(context.Background(), `return host.real_one()`, []Binding{
		okBind("---", "junk"),
		okBind("real_one", "1"),
		okBind("...", "junk2"),
	}, Options{})
	if !r.OK {
		t.Fatalf("挂不上的绑定应被跳过而非拒表，实得 %q / %q", r.ErrorKind, r.Error)
	}
}
