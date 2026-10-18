package ptc

import "context"

// RunEnv 一次运行的 PTC 环境（批三百零五 ctx 缝）：runner 在装配完成点（全部工具追加
// 之后）把本环节模型可见的最终工具表快照进 ctx——「程序内可达=本环节模型可达」恒等式
// 由快照时点保证，不在组合根持全局表（per-run 表只活在当次 Run 里）。run_program 于
// 调用期取出构绑定；OnRun=ptc_run 明细事件回调（nil=不发）。
type RunEnv struct {
	Table []ToolTableEntry
	OnRun func(Result)
}

// ToolTableEntry 一个可绑定工具：Name=工具名（JS 侧 host.<净化名>），Run=调用
// （args=JSON 文本，回工具输出原文）。
type ToolTableEntry struct {
	Name string
	Run  func(ctx context.Context, args string) (string, error)
}

type envKey struct{}

// WithEnv 注入 PTC 环境（runner 装配点调用）。
func WithEnv(ctx context.Context, env RunEnv) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

// EnvOf 取 PTC 环境（零值=未装配——run_program 如实回包不假跑）。
func EnvOf(ctx context.Context) RunEnv {
	env, _ := ctx.Value(envKey{}).(RunEnv)
	return env
}
