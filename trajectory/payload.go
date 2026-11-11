// payload.go —— 事件载荷取值助手（轨迹投影层读载荷的唯一入口）。
//
// 载荷是 map[string]any，经 JSON 往返后整数是 float64——「只认 int64」的
// 取值面会在宿主的发布路径上整片失明。三个助手把「缺失 / 类型不符」的
// 归一口诀收在一处：字符串空串、整数 0、布尔 false（布尔读成 false 是
// 保守方向，但调用方仍应发规范的 bool）。
package trajectory

import (
	"github.com/yi-nology/goagent-kernel/eventbus"
)

// payloadUint 取事件载荷里的无符号整数键（缺失或类型不符时为 0）。
//
// 载荷经 JSON 往返后整数是 float64（丢精度的是 >2^53 的值——序号与令牌
// 到不了那个量级；到得了的宿主应改发字符串载荷，本包不猜）。
func payloadUint(ev eventbus.Event, key string) uint64 {
	if ev.Payload == nil {
		return 0
	}
	switch v := ev.Payload[key].(type) {
	case uint64:
		return v
	case int64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case int:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case float64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	default:
		return 0
	}
}

// payloadBool 取事件载荷里的布尔键（缺失或类型不符时为 false——
// 「裁决说已生效」这类布尔读成 false 是保守方向，但调用方仍应发规范的 bool）。
func payloadBool(ev eventbus.Event, key string) bool {
	if ev.Payload == nil {
		return false
	}
	b, _ := ev.Payload[key].(bool)
	return b
}
