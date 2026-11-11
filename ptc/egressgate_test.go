// egressgate_test.go —— 子进程出口声明的**兑现门**（spawn 之前的能力问询）。
//
// # 为什么这一门必须成对钉住
//
// 两个方向都得钉：**没有兑现者就拒绝**（否则这一门是装饰）与**旧调用点行为不变**
// （否则它会把「没要求出口」的正常调用判死）。只钉前一半的用例组，在实现退化成
// 「恒拒绝」时也照样全绿——而恒拒绝会把所有不要求出口的调用一并拦死。
package ptc

import (
	"strings"
	"testing"

	"github.com/yi-nology/goagent-kernel/egress"
)

// fakeEgress 一个只会自报档位的兑现者（真实实现是宿主的强制代理）。
type fakeEgress struct{ m egress.Mode }

func (f fakeEgress) Egress() egress.Mode { return f.m }

// proxySpec 一份**合法**的代理档声明（Validate 能过）。
func proxySpec() egress.Spec {
	return egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"}
}

// TestSpawnRefusedWhenNoEgressBackend 钉住：要求了限制却没有兑现者 ⇒ 不起子进程。
//
// 判据是「拒绝」不是一个「可能失败」：安全语义下必须 fail closed——否则宿主写了
// 「只经代理」，子进程照样裸奔出网，而报告里那一条显示「已声明」。
func TestSpawnRefusedWhenNoEgressBackend(t *testing.T) {
	p, err := newChildPipes("sh", proxySpec(), nil)
	if err == nil {
		p.closeAll()
		t.Fatal("没有兑现者的代理档声明必须拒绝起子进程")
	}
	if !strings.Contains(err.Error(), "无人兑现") {
		t.Fatalf("错误必须说清是「没人能兑现」，实得 %v", err)
	}
}

// TestSpawnRefusedWhenBackendTooWeak 钉住：后端能力低于要求同样拒绝。
//
// 「不要求」的后端在代理档要求下不满足——这一条是 SatisfiedBy 的档位比较在
// 子进程路径上真的被用到的证据（若这里只判「有没有实现接口」，它会被放过）。
func TestSpawnRefusedWhenBackendTooWeak(t *testing.T) {
	p, err := newChildPipes("sh", proxySpec(), fakeEgress{m: egress.ModeUnrestricted})
	if err == nil {
		p.closeAll()
		t.Fatal("后端只能不限制，而要求代理 ⇒ 必须拒绝")
	}
}

// TestSpawnAllowedWhenBackendDeclares 配对：后端声明了同档 ⇒ 正常建管道。
func TestSpawnAllowedWhenBackendDeclares(t *testing.T) {
	p, err := newChildPipes("sh", proxySpec(), fakeEgress{m: egress.ModeProxy})
	if err != nil {
		t.Fatalf("后端已声明能兑现，不该拒绝: %v", err)
	}
	defer p.closeAll()
	if p.child == nil {
		t.Fatal("应已装配好子进程（尚未启动）")
	}
	// 出口 env 真的挂上去了（两半：问了能力、也生成了 env）。
	joined := strings.Join(p.child.Env, "\n")
	if !strings.Contains(joined, "HTTPS_PROXY=http://127.0.0.1:3128") {
		t.Fatalf("代理变量没进子进程环境:\n%s", joined)
	}
}

// TestSpawnUnrestrictedIgnoresBackend 配对：不要求出口时**不问能力**（旧行为不变）。
//
// 零值 Egress 是旧调用点的常态。若这一门改成「先要求兑现者」，所有既有调用会
// 在第一行就失败——那是把一次安全收紧闭做成一次破坏性变更。
func TestSpawnUnrestrictedIgnoresBackend(t *testing.T) {
	p, err := newChildPipes("sh", egress.Spec{}, nil)
	if err != nil {
		t.Fatalf("不要求出口时不得因缺兑现者而拒绝: %v", err)
	}
	defer p.closeAll()
	if p.child == nil {
		t.Fatal("应已装配好子进程")
	}
}
