package ptc

// thrownRejection 分类随值走的拒绝载体（issue #44/#45）。
//
// 此前两条类型化分类通道都是**结果级单槽**或**包装丢失**形态：
//   - 判定环拒绝走 res.stageErr 单槽——程序把拒绝 try/catch 掉后再抛
//     自己的错误，收口处读到的是**陈旧**的 stageErr，程序自己的 bug 被
//     误归为 stage_denied（调用方去质问守卫而不是改程序，#44）；
//   - 子进程侧 remoteFailure 携带的 toolfail 分类（父侧判定环显式附加）
//     在 vm.NewGoError 的裸 error 包装里丢失，落进 runtime→transient——
//     「重试也永远不会满足」的策略否定被报成「值得重试」（#45）。
//
// 修法：每次抛出都把分类装进载体，并在 Result.lastThrown 记下「最近一次
// 本包抛出的载体」；收口处用**文本锚定**（被拒值的消息必须包含载体消息）
// 确认「这次拒绝就是我们抛的那次」再采信分类——程序捕获后再抛自己的错误
// 时锚定失配，陈值无从毒化。文本锚定的边界：程序故意抛出与判定环文案
// 逐字节相同的错误可伪造归因（对抗面不在威胁模型内；判定环文案含
// invoke 包内部措辞，正常程序不会构造）。
//
// 载体本身仍经 vm.NewGoError 包装进 JS 抛出（goja 对非 goja 值的 panic
// 原样穿透成 host panic，不能当 JS 异常被 try/catch）——分类的存取都在
// Go 侧，不依赖 goja 值语义。
type thrownRejection struct {
	err  error
	kind string // ErrorKind 词汇：stage_denied / stage_failed / toolfail 分类值；"" = 程序自身运行时错误
}

func (e *thrownRejection) Error() string { return e.err.Error() }

// rejectionKindOf 探测调用错误自带的分类（toolfail 口径）：子进程侧的
// remoteFailure 与宿主侧实现该接口的错误都带。
func rejectionKindOf(err error) string {
	if k, ok := err.(interface{ Kind() string }); ok {
		return k.Kind()
	}
	return ""
}
