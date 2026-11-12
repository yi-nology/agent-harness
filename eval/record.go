// record.go —— 录制：事件流 → JSONL 定格；重放：定格 → 同一条轨迹。
//
// 录制格式刻意朴素：一行一个事件、标准 encoding/json——录制文件是**数据**
// 不是协议，朴素使它可被任何宿主直接读写。字段顺序由结构体定义固定，
// 载荷 map 的键序由 encoding/json 排序固定，同一事件序列永远编出同一份
// 字节（可 diff 的录制，不是黑盒快照）。
package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/eventbus"
	"github.com/yi-nology/goagent-kernel/trajectory"
)

// ErrBadRecord 录制文件不合法（不是合法的 JSONL 事件流）。
var ErrBadRecord = errors.New("eval: 录制文件不合法")

// Record 把事件流定格成 JSONL 字节（一行一个事件，按输入顺序）。
func Record(evs []eventbus.Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, ev := range evs {
		if err := enc.Encode(ev); err != nil {
			return nil, errors.Wrap(ErrBadRecord, "eval: 事件 "+fmt.Sprint(ev.ID)+" 编码失败: "+err.Error())
		}
	}
	return buf.Bytes(), nil
}

// Load 解码 JSONL 录制（顺序即回放顺序）。
func Load(data []byte) ([]eventbus.Event, error) {
	var out []eventbus.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // 长行事件（大载荷）不断
	line := 0
	for sc.Scan() {
		line++
		row := bytes.TrimSpace(sc.Bytes())
		if len(row) == 0 {
			continue
		}
		var ev eventbus.Event
		if err := json.Unmarshal(row, &ev); err != nil {
			return nil, errors.Wrap(ErrBadRecord, fmt.Sprintf("eval: 第 %d 行解码失败: %s", line, err))
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.Wrap(ErrBadRecord, "eval: 读取录制失败: "+err.Error())
	}
	return out, nil
}

// RecordProblem 一条录制适航问题。
type RecordProblem struct {
	// Seq 事件序号（1 起，按输入顺序）。
	Seq int
	// Text 人读的说明。
	Text string
}

// Verify 判事件流是否**录制适航**：逐事件经 JSON 往返必须落进稳定形态。
//
// 判据（两层，都在纯函数里）：
//
//   - 头部标量字段（id / session / case / step / type / level / 时刻）往返后
//     必须与原值相等——这些是投影的骨架；
//   - 载荷（map[string]any）往返**一次**后必须**幂等**（再往返一次不再变）：
//     整数会落成 float64，这是 JSON 的固有形变；只要形变只发生一次，
//     「录制 → 重载」拿到的就是同一份输入，重放仍然确定。往返两轮还在变
//     的载荷（比如不可比较的类型混在里面）录了也录不稳，显式报出。
//
// 返回问题清单（空 = 全部适航）。
func Verify(evs []eventbus.Event) []RecordProblem {
	var out []RecordProblem
	for i, ev := range evs {
		seq := i + 1
		loaded, err := roundTrip(ev)
		if err != nil {
			out = append(out, RecordProblem{Seq: seq, Text: "往返失败: " + err.Error()})
			continue
		}
		if !headerEqual(ev, loaded) {
			out = append(out, RecordProblem{Seq: seq, Text: "头部字段经往返发生了变化（投影骨架失真）"})
			continue
		}
		again, err := roundTrip(loaded)
		if err != nil {
			out = append(out, RecordProblem{Seq: seq, Text: "二次往返失败: " + err.Error()})
			continue
		}
		if !reflect.DeepEqual(loaded.Payload, again.Payload) {
			out = append(out, RecordProblem{Seq: seq, Text: "载荷往返不幂等（重载输入将不可复现）"})
		}
	}
	return out
}

// Replay 把事件流投进全新投影器并收口，返回轨迹行快照。
//
// 双方（录制侧与重放侧）都以 Finalize 收口再比对——「不会再有新事件」
// 是显式事实，两侧口径一致差异才有意义。
func Replay(evs []eventbus.Event, opts trajectory.Options) []trajectory.Row {
	p := trajectory.NewProjector(opts)
	p.ApplyAll(evs)
	p.Finalize()
	return p.Rows()
}

// roundTrip 一次 JSON 往返（录制与重载的唯一形态变换）。
func roundTrip(ev eventbus.Event) (eventbus.Event, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return eventbus.Event{}, err
	}
	var out eventbus.Event
	if err := json.Unmarshal(raw, &out); err != nil {
		return eventbus.Event{}, err
	}
	return out, nil
}

// headerEqual 头部标量字段的等值判定（时刻用 Equal：单调钟读数不进录制）。
func headerEqual(a, b eventbus.Event) bool {
	if a.ID != b.ID || a.SessionID != b.SessionID || a.CaseID != b.CaseID {
		return false
	}
	if a.Type != b.Type || a.Level != b.Level {
		return false
	}
	if (a.StepID == nil) != (b.StepID == nil) {
		return false
	}
	if a.StepID != nil && *a.StepID != *b.StepID {
		return false
	}
	return a.OccurredAt.Equal(b.OccurredAt)
}
