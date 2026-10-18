// 媒体类型 → 落盘扩展名的**单源**推导。
//
// ## 为什么内核要管这件事
//
// Ext 由调用方逐条给，若不给推导，N 个宿主就会写 N 套映射，彼此不一致。
// 而落盘键是内容寻址的（见 StorePath）：扩展名一变，同一份内容的键就变，
// 占位文本逐字节稳定性与去重**同时失效**（见 placeholder.go 文件注释）。
// 换句话说，这不是风格问题，是键一致性问题，故必须单源。
//
// ## 为什么不能只靠 mime.ExtensionsByType
//
// 两层不可移植性，每层都足以让同一份内容在不同机器上落进不同的键：
//
//  1. 表内容依平台而变。Go 会加载宿主机的 mime.types（macOS 的
//     /etc/apache2/mime.types、多数发行版的 /etc/mime.types），同一类型在
//     A 机器注册 .jpg/.jpe/.jpeg、在 B 机器只有 .jpeg；
//  2. 候选顺序依实现而变。平台表对同一类型可能返回多个扩展名，未显式定序时
//     「取哪个」在不同实现/版本间会漂。
//
// 故口径固定为三段：**剥参数 → 查显式覆盖表 → 平台表兜底**，兜底结果排序取首个，
// 使同一进程内反复调用恒等。常见类型由覆盖表兜住，第 1、2 条都碰不到它们。
//
// ## 未知类型为什么给中性值而不是报错
//
// 扩展名在本包只用于拼逻辑键，取值域是宿主的落盘实现，不参与内容语义判定。
// 让每个宿主自己判一次「这算不算错误」，只会得到 N 种失败方式；而报错会连带
// 丢掉「阈值已过、本可省上下文」的外置收益，中性取值则至少键稳定、行为可预期。
package offload

import (
	"mime"
	"sort"
	"strings"
)

// FallbackExt 未知（或无法解析的）媒体类型的缺省扩展名。
//
// 取 .bin 而非留空：留空会被 sanitizeExt 判为非法扩展名，条目级失败后内容内联，
// 白白越过阈值却不省任何上下文（Item.Reason 只够留痕，不够自愈）。
const FallbackExt = ".bin"

// mimeExtOverrides 常见多媒体类型的**权威映射**，优先于平台表。
//
// 为什么必须显式钉：image/jpeg 在 macOS 的 mime.types 下常得 .jpe，在 Linux 上
// 是 .jpg/.jpeg 并列，而本仓的键要跨机器一致（见文件注释）。表与参考实现的同名
// 覆盖表对齐，并补上平台表里最容易分歧的几项（image/jpg、audio/mp3、image/tiff）。
//
// 只收「媒体本体」类型，不收文档/归档：那些类型平台表里候选唯一，没有分歧面，
// 为省几行字扩大本表反而增加日后无人复核的表面积。
var mimeExtOverrides = map[string]string{
	"image/png":   ".png",
	"image/jpeg":  ".jpg",
	"image/jpg":   ".jpg",
	"image/webp":  ".webp",
	"image/gif":   ".gif",
	"image/bmp":   ".bmp",
	"image/tiff":  ".tiff",
	"audio/mpeg":  ".mp3",
	"audio/mp3":   ".mp3",
	"audio/wav":   ".wav",
	"audio/x-wav": ".wav",
	"audio/ogg":   ".ogg",
	"audio/flac":  ".flac",
}

// ExtForMimeType 由媒体类型推导落盘扩展名，返回带前导点的形态（如 ".png"），
// 可直接填入 Blob.Ext；StorePath 两种形态都接受。
//
// 三段口径：剥参数（`image/jpeg; charset=x` → `image/jpeg`）→ 查覆盖表 →
// 查平台表（排序取首个）→ 未知返回 FallbackExt。参数与大小写都要剥：覆盖表以
// 裸类型为键，而带参数的写法在真实消息里极常见（不剥则整条覆盖形同虚设）。
//
// 刻意不返回 error：推导失败不是本包的失败面，本包没有「类型不对」这种错误类型，
// 只有一个必须恒定的中性取值（见 FallbackExt）。
func ExtForMimeType(mimeType string) string {
	primary := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.IndexByte(primary, ';'); i >= 0 {
		primary = strings.TrimSpace(primary[:i])
	}
	if primary == "" {
		return FallbackExt
	}
	if ext, ok := mimeExtOverrides[primary]; ok {
		return ext
	}
	exts, err := mime.ExtensionsByType(primary)
	if err != nil || len(exts) == 0 {
		return FallbackExt
	}
	// 显式排序：确定性由本包自己兜住，不外包给 stdlib 的排序实现细节
	// （当前 stdlib 已排，但换版本时这条判据不该被静默改掉）。
	sort.Strings(exts)
	return exts[0]
}
