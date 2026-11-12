package coord

import (
	"testing"

	"github.com/yi-nology/goagent-kernel/sidetest"
)

// StoreAdapter 接入共享契约套件（硬失败不跳过——接入即声明满足契约）。
//
// 29 条子测试（apply_once / intent / uncertain / holder / reconcile 各族）
// 全部跑在这份适配器 + 内存后端上：判定逻辑那份实现由此验证；
// 跨进程原子性由真实后端保证（形态约定见包文档，内存后端不上生产）。
func TestStoreAdapterContractSuite(t *testing.T) {
	sidetest.Run(t, func(t *testing.T, clock *sidetest.FakeClock) sidetest.ClockedStore {
		t.Helper()
		s := NewStoreAdapter(NewMemoryBackend())
		s.UseClock(clock.Now)
		return s
	})
}
