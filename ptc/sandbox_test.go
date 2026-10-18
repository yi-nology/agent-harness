package ptc

import (
	"context"
	"strings"
	"testing"
)

// TestSandboxHasNoNetworkPrimitives 沙箱里不得出现任何网络/逃逸原语。
//
// # 为什么这条要钉成用例，而不是写进注释
//
// 出站守卫（netguard）**不在**判定环上，这是有意的：判定环判的是「这次调用要不要
// 放行」，而 netguard 判的是「这个 URL 能不能发出去」——两个执行点，把 URL 守卫硬塞进
// 逐调用的判定环就得去猜「哪个参数字段是 URL」，那是领域知识，内核不该有。
//
// 两者能这么分工，靠的是一条沙箱性质：**程序本身够不着网络**。出站只可能发生在宿主
// 绑定的实现里，而那是宿主自己的代码，宿主自己接 netguard。
//
// 而这条性质此前只是一句注释——谁哪天给 VM 加个 `fetch`，或者 goja 升级后自带了某个
// 网络全局，就出现了一条「程序能自己出站、绕过 netguard」的路径，而没有任何用例会红。
// 故钉成用例：加了就得重新回答「那 netguard 该接在哪」，而不是静默多出一条路。
func TestSandboxHasNoNetworkPrimitives(t *testing.T) {
	// 正控：host 必须存在。否则本用例会因 VM 整体坏掉而「假绿」——
	// 一个什么都没挂的沙箱当然没有 fetch。
	hostType := Run(context.Background(), `return typeof host`, nil, Options{})
	if !hostType.OK || strings.TrimSpace(hostType.Value) != `"object"` {
		t.Fatalf("正控失效：host 应可用，实得 OK=%v value=%s err=%q", hostType.OK, hostType.Value, hostType.Error)
	}

	for _, id := range []string{
		// 网络
		"fetch", "XMLHttpRequest", "WebSocket", "EventSource", "navigator", "Deno",
		// 模块/宿主逃逸
		"require", "process", "importScripts", "module", "exports", "globalThis.process",
		// 定时器（VM 无定时器是既有设计；出现即说明有人给 VM 加了异步能力）
		"setTimeout", "setInterval", "queueMicrotask",
	} {
		r := Run(context.Background(), `return typeof `+id, nil, Options{})
		if !r.OK {
			t.Fatalf("求值 typeof %q 本身失败: %q", id, r.Error)
		}
		if got := strings.TrimSpace(r.Value); got != `"undefined"` {
			t.Fatalf("沙箱里出现了 %s（%s）。出站只能经宿主绑定，netguard 的分工前提就变了——"+
				"请重新决定 netguard 接在哪，别让程序自己就能出站。", id, got)
		}
	}
}

// TestDiscoverIsTheOnlyExtraGlobal 装配了 Discover 之后，沙箱里多出来的全局**只有**
// discover 一个。
//
// 与上一条同源：判定链之外我们只承诺挂 host 与可选的 discover；多挂任何一个都意味着
// 有人往沙箱里放了新能力，而那需要重新回答「它怎么被约束」。
func TestDiscoverIsTheOnlyExtraGlobal(t *testing.T) {
	r := Run(context.Background(), `return typeof discover`, nil, Options{
		Discover: func(string) []Discovered { return nil },
	})
	if !r.OK || strings.TrimSpace(r.Value) != `"function"` {
		t.Fatalf("装配后 discover 应为 function，实得 OK=%v value=%s", r.OK, r.Value)
	}

	// 未装配时不挂。
	r = Run(context.Background(), `return typeof discover`, nil, Options{})
	if strings.TrimSpace(r.Value) != `"undefined"` {
		t.Fatalf("未装配时不应存在 discover，实得 %s", r.Value)
	}
}

// TestSandboxCannotReachHostBindingInternals 绑定传出去的是 JSON 文本，不是 Go 值。
//
// 逃逸的常见形态不是「多一个全局」，而是「把宿主对象递回给程序」——程序若能拿到
// Go 结构体或函数，就能通过反射/方法调用摸到宿主内部。绑定面必须只传可序列化的值。
func TestSandboxCannotReachHostBindingInternals(t *testing.T) {
	hit := 0
	bind := Binding{Name: "t", Call: func(_ context.Context, argsJSON string) (string, error) {
		hit++
		// 宿主侧回一个「不该被程序拿到」的东西：argsJSON 是文本，宿主自己造的 Go 值出不去。
		return "ok", nil
	}}
	r := Run(context.Background(), `return typeof host.t`, []Binding{bind}, Options{})
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	// 程序只能看到 JS 侧的包装函数，看不到 Go 侧 anything。
	if got := strings.TrimSpace(r.Value); got != `"function"` {
		t.Fatalf("host.t 应为 JS 函数，实得 %s", got)
	}
	if hit != 0 {
		t.Fatalf("求值 typeof 不应触达执行体，实得 %d 次", hit)
	}
}
