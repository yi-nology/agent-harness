package protocol_test

// schema 编译缓存并发共享校验（批三百零三，挂账余项）：compileSchema 的 sync.Map 按
// schemaSrc 原文缓存——多 goroutine 同 schema/异 schema 并发 ValidateStructured，
// -race 下验证缓存竞争面与结果一致性（合规恒 nil、违规恒非 nil，混跑不串味）。
// 常规 go test 即跑（不是 fuzz；并发语义靠 -race 全仓门禁 make race）。

import (
	"fmt"
	"sync"
	"testing"

	"github.com/yi-nology/agent-harness/protocol"
)

func TestValidateStructuredConcurrent(t *testing.T) {
	schemaOK := `{"type":"object","properties":{"a":{"type":"string"}}}`
	envOK := `{"structuredContent":{"a":"x"}}`
	envBad := `{"structuredContent":{"a":123}}`

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 异 schema 轮换注入：撞缓存不串味（键=schemaSrc 原文）
			other := fmt.Sprintf(`{"type":"object","properties":{"k%d":{"type":"number"}}}`, i%4)
			for j := 0; j < 200; j++ {
				if err := protocol.ValidateStructured(schemaOK, envOK); err != nil {
					t.Errorf("合规载荷不应报错: %v", err)
				}
				if err := protocol.ValidateStructured(schemaOK, envBad); err == nil {
					t.Error("违规载荷应报错（缓存混跑不得串味）")
				}
				_ = protocol.ValidateStructured(other, envOK) // 载荷对不上异 schema=报错合法，只验不崩
			}
		}(i)
	}
	wg.Wait()
}
