package ptc

import (
	"context"
	"fmt"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oagentkit/guardx/wire"
)

// handler 父侧应答一次工具调用：判定环 → 执行绑定 → 回帧。
//
// 判定环在**父侧**：宿主工具在父侧，判定环也必须在父侧——子进程里只有一个
// 发请求的桩。若判定环放到子进程，「程序里的 host.<tool>() 与普通工具调用
// 走同一套判定环」这条纪律就断了。
func (p *SubprocessProvider) handler() wire.Handler {
	byName := make(map[string]Binding, len(p.opts.Bindings))
	for _, b := range p.opts.Bindings {
		byName[b.Name] = b
	}
	return func(ctx context.Context, req wire.Request) (wire.Response, error) {
		// 判定环：与普通工具调用同一套。
		//
		// 归因与进程内路径**同一份实现**（issue #65）：此前这里把一切
		// EvaluateStages 的错误（含守卫 panic/故障）都映射成 permission_denied
		// ——「宿主门禁坏了」被上报为「策略否定」，排障方向被引向守卫配置，
		// 正是 #59 在进程内路径修掉的误归因换了一条执行路径复发。现在经
		// stageOutcome 分类：stage_denied（正常裁决）/ stage_failed（守卫自身
		// 故障，宿主要修的不是程序）；非 DenyError 的错误不得冠判定环词汇，
		// 按 unknown 走。Kind 用本包 ErrorKind 词表——子进程侧的收口对它
		// 逐字采信（remoteFailure.Kind → Result.ErrorKind），两个后端对同一
		// 次拒绝报同一个词。
		if serr := invoke.EvaluateStages(ctx, p.opts.Stages, invoke.Call{Name: req.Tool, Args: req.Args}); serr != nil {
			kind, prefix := stageOutcome(serr)
			msg := serr.Error()
			if prefix != "" {
				msg = prefix + ": " + msg
			}
			if kind == "" {
				kind = string(toolfail.ClassUnknown)
			}
			return wire.Response{
				OK:    false,
				Kind:  kind,
				Error: msg,
			}, nil
		}
		b, ok := byName[req.Tool]
		if !ok {
			return wire.Response{
				OK:    false,
				Kind:  string(toolfail.ClassNotFound),
				Error: fmt.Sprintf("工具 %q 未装配", req.Tool),
			}, nil
		}
		out, err := b.Call(ctx, req.Args)
		if err != nil {
			// 分类随值走（issue #150）：与进程内路径（binding.go 的
			// thrownRejection）同一份判据——rejectionKindOf 链感知探测错误
			// 自带的分类（*toolfail.Failure、宿主自定义 Kind() 接口，%w 包装
			// 穿透）。此前这里硬编码 unknown：宿主绑定返回 permission_denied/
			// rate_limited/… 的规范失败，子进程形态一律报 unknown，收口的
			// Result.ErrorKind 与重试/升级策略按后端分叉——child.go 声称的
			// 「父侧已把失败归类，原样带回去」不成立。
			kind := rejectionKindOf(err)
			if kind == "" {
				kind = string(toolfail.ClassUnknown)
			}
			return wire.Response{
				OK:    false,
				Kind:  kind,
				Error: err.Error(),
			}, nil
		}
		return wire.Response{OK: true, Value: out}, nil
	}
}

// startBindings 把绑定清单转成协议形态：**只给名字与调用形态，不给实现**。
//
// 实现留在父侧——这正是跨进程的意义：宿主工具只在宿主进程里存在。
func startBindings(bindings []Binding) []wire.StartBinding {
	out := make([]wire.StartBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, wire.StartBinding{Name: b.Name, Signature: b.Signature})
	}
	return out
}
