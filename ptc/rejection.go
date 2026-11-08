package ptc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/oagentkit/guardx/toolfail"
	"github.com/oekit/pkg/errors"
)

// thrownRejection 分类随值走的拒绝载体（issue #44/#45/#57）。
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
// 本包抛出的载体」；收口处用**精确锚定**（被拒值必须逐字等于载体在未捕获时
// 的渲染，含每次抛出随机生成的 nonce）确认「这次拒绝就是我们抛的那次」再采信
// 分类——程序捕获后再抛自己的错误（改写消息、new Error 重包）时锚定失配，
// 陈值无从毒化。锚定的演进：#44/#45 用的单向 Contains 会被「catch(e){ throw
// new Error("failed: " + e.message) }」这类改写命中——改写后的消息包含载体
// 消息，程序自己的 bug 误继承 stage 分类，Suggestions 按错误方向引导（#57）；
// 改为 nonce + 全等后，锚文本每次抛出都不同且须逐字一致，改写过的消息不再命中。
// 精确锚定的边界：程序**原样**抛出捕获到的载体（throw e）仍正确归因——那是
// 同一次拒绝的再抛，不是新错误；对抗面（程序故意伪造载体渲染文本）不在威胁
// 模型内，且 nonce 使得伪造必须逐字节复制当前这次抛出的文本。
//
// 载体本身仍经 vm.NewGoError 包装进 JS 抛出（goja 对非 goja 值的 panic
// 原样穿透成 host panic，不能当 JS 异常被 try/catch）——分类的存取都在
// Go 侧，不依赖 goja 值语义；nonce 因此必须进 Error() 文本（JS 侧唯一能
// 带出去的通道），锚定成功后由收口处剥离，不进模型可见的最终消息。
type thrownRejection struct {
	err   error
	kind  string // ErrorKind 词汇：stage_denied / stage_failed / toolfail 分类值；"" = 程序自身运行时错误
	nonce string // 每次抛出随机生成的锚（issue #57）；"" = 旧载体（无锚，不参与精确归因）
}

func (e *thrownRejection) Error() string {
	if e.nonce == "" {
		return e.err.Error()
	}
	return e.err.Error() + e.marker()
}

// marker 载体消息尾部的锚标记。格式刻意带 ptc 前缀与括号结构：宿主在
// CallLog/留痕里看到它时能认出「这是 ptc 的锚」而不是把当次消息当脏数据。
func (e *thrownRejection) marker() string {
	return " [ptc#" + e.nonce + "]"
}

// rejectionAnchor 载体在**未捕获**时的精确渲染。收口处拿它与被拒值做全等
// 比较；用同一个 vm 重新包一层 GoError 再渲染，避免硬编码 goja 的包装格式
// （"GoError: " 前缀）——格式随上游变了，这里跟着变，锚定只会失配（退回
// runtime），不会错配。
func rejectionAnchor(vm *goja.Runtime, tr *thrownRejection) string {
	return fmt.Sprintf("%v", vm.NewGoError(tr))
}

// rejectionText 剥掉锚标记后的被拒值文本（模型可见的最终消息用）。
func (e *thrownRejection) rejectionText(rejected string) string {
	if e.nonce == "" {
		return rejected
	}
	return strings.TrimSuffix(rejected, e.marker())
}

// newRejectionNonce 生成锚 nonce：每次抛出唯一即可，不承担任何语义。
func newRejectionNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 不可用极罕见（系统熵源故障）；时间戳兜底，锚仍逐次不同。
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// rejectionKindOf 探测调用错误自带的分类（toolfail 口径）。
//
// 判定面优先走**链感知**的 toolfail.As（issue #58）：*toolfail.Failure 是本仓
// 及其矩阵唯一的规范失败类型，宿主用它表态是标准动作——而它**没有** Kind()
// 方法，裸断言（甚至 errors.As 接口探针）都取不到它的分类，进程内路径上
// permission_denied 一律降级成 runtime→transient，「重试也不会满足」的策略
// 否定被报成「值得重试」。链感知的 As 还顺带治了「套一层 %w 丢分类」——
// 单向类型断言不穿包装，规范类型包一层上下文就探不到。
// Kind() 探针退为兜底（errors.As 穿包装），保住自定义宿主类型的扩展点；
// 两头都探不到返回 ""（无分类，收口处按程序自身错误处理）。
func rejectionKindOf(err error) string {
	if f, ok := toolfail.As(err); ok && f != nil && f.Class != "" {
		return string(f.Class)
	}
	var k interface{ Kind() string }
	if errors.As(err, &k) {
		return k.Kind()
	}
	return ""
}
