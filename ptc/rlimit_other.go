//go:build !unix

package ptc

import "github.com/oekit/pkg/errors"

// errMemoryLimitUnsupported 本平台无法设地址空间上限（内存兜底不可用）。
var errMemoryLimitUnsupported = errors.New("ptc: 本平台无法设地址空间上限，内存兜底不可用")

// applyMemoryLimit 非 Unix 平台：无法设地址空间上限。
//
// **返回错误而不是静默放行**——「以为兜住了、实际没有」正是这套机制要消灭的
// 东西。宿主若在这类平台上要求内存兜底，正确的反应是「这个平台兜不住」，
// 而不是「跑吧，反正大概没事」。
//
// Windows 上有 SetProcessWorkingSet 之类的手段，但它限制的是**工作集**
// （可被换出），不是「不许增长」——那不是内存封顶，故不用。
func applyMemoryLimit(maxMemory int64) error {
	if maxMemory <= 0 {
		return nil
	}
	return errMemoryLimitUnsupported
}
