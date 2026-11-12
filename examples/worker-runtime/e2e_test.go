package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// 端到端：六段生命周期（注册 → 心跳 → 领取 → 执行 → 恢复 → 优雅退出）
// 在参考实现上真跑一遍。runDemo 的每步输出都是断言面。
func TestRunDemoEndToEnd(t *testing.T) {
	var buf bytes.Buffer
	if err := runDemo(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"[1] 建任务板与注册表",
		"[2] worker-a 认领 t1 后崩溃",
		"[3] worker-b 恢复接管: [t1]",
		"执行 t1",
		"执行 t2",
		"执行 t3",
		"[4] 终态 t1 = done",
		"[4] 终态 t2 = done",
		"[4] 终态 t3 = done",
		"[5] 注册表余留 0 条",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("演示输出缺少 %q\n完整输出：\n%s", want, out)
		}
	}
}
