// recorder.go —— 指标投递接缝（骨架）+ 进程内参考实现。
//
// # 分工（与 trajectory 的 OTel 映射同一条）
//
// 内核只产出「名称 + 标签 + 数值」；编码与投递归宿主的 Recorder 实现。
// 本包不 import 任何 OTel / Prometheus SDK——接了谁，「导出失败算不算执行失败」
// 就得由谁回答，而那个问题不该出现在内核里。
//
// # 埋点形态
//
// 生产代码经包级便利入口埋（一行，不持有任何后端引用）：
//
//	metrics.Count(metrics.NameLeaseContended, 1, map[string]string{
//	    metrics.LabelLeaseSubsystem: metrics.LeaseSubsystemSideEffect,
//	    metrics.LabelLeaseOutcome:   metrics.LeaseOutcomeContended,
//	})
//
// 后端未装配时缺省是 Nop——「还没接看板」不该让执行路径变红；宿主装配期调
// SetDefault 后，全部埋点即刻生效（logx.SetDefault 同款装配语义）。
//
// # 测试期校验
//
// 埋错标签（越界取值、禁区键、未登记指标名）不会报错——数照常上行，只是看板
// 拼不起来。把这类错拦在测试期：宿主测试用 [Mem] 接进 SetDefault，对自己的
// 埋点跑 [Validate]（Mem.Collect 已内置校验开关，见其头注）。
package metrics

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/oekit/pkg/errors"
)

// Labels 指标标签。值必须落在登记的取值域内（Validate 会判）；禁区清单里的键
// 不得出现（ForbiddenLabels）。
type Labels = map[string]string

// Recorder 指标投递接缝。三个方法对应三类指标（counter / gauge / histogram）。
//
// 实现纪律：**非阻塞、不 panic、不返回错误**。指标是旁路观测，投递层的任何
// 故障都不该反噬执行路径（buffer 满了就丢——丢了是观测损失，堵住是可用性事故）。
type Recorder interface {
	// Count 计数器加 delta（NameRetryTotal / NameLeaseContended / NameRecoveryTotal）。
	Count(name string, delta int64, labels Labels)
	// Observe 直方图观测（NameExecDuration；ms 实测值，只记实测不记估算）。
	Observe(name string, ms float64, labels Labels)
	// Set 仪表设值（NameUncertainBacklog；整数量）。
	Set(name string, v int64, labels Labels)
}

// nop 零开销缺省实现。
type nop struct{}

func (nop) Count(string, int64, Labels)     {}
func (nop) Observe(string, float64, Labels) {}
func (nop) Set(string, int64, Labels)       {}

// Nop 零开销实现：不投递任何数。
func Nop() Recorder { return nop{} }

// 全局后端（logx.SetDefault 同款装配语义：缺省 Nop，宿主装配期换一次）。
var (
	currentMu sync.RWMutex
	current   Recorder = nop{}
)

// SetDefault 换全局投递后端。传 nil 等于换回 Nop（不 panic，也不当「没配置」静默）。
// 装配期调用一次；运行期换后端是宿主自己的并发决定，本包只保证调用安全。
func SetDefault(r Recorder) {
	currentMu.Lock()
	defer currentMu.Unlock()
	if r == nil {
		current = nop{}
		return
	}
	current = r
}

// Default 返回当前全局后端。
func Default() Recorder {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

// ── 包级便利入口（埋点一行化）────────────────────────────────────────────────

// Count 经全局后端记一条计数。
func Count(name string, delta int64, labels Labels) { Default().Count(name, delta, labels) }

// Observe 经全局后端记一次直方图观测（毫秒）。
func Observe(name string, ms float64, labels Labels) { Default().Observe(name, ms, labels) }

// Set 经全局后端设一次仪表值。
func Set(name string, v int64, labels Labels) { Default().Set(name, v, labels) }

// ── 校验 ────────────────────────────────────────────────────────────────────

// ErrUnregisteredMetric 埋了没登记的指标名（先登记 Specs 再埋点）。
var ErrUnregisteredMetric = errors.New("未登记的指标名（先在 metrics.specs 登记再埋点）")

// Validate 校验一次埋点是否在契约面内：指标名已登记、标签键都在登记的维度里、
// 取值落在封闭枚举内、禁区键不出现。宿主测试用它把「埋错标签」拦在测试期
// ——这类错在生产里不报错，只会在看板上静默拼不起来。
func Validate(name string, labels Labels) error {
	m, ok := SpecOf(name)
	if !ok {
		return errors.Wrap(ErrUnregisteredMetric, fmt.Sprintf("埋点 %q", name))
	}
	declared := make(map[string]LabelDim, len(m.Labels))
	for _, d := range m.Labels {
		declared[d.Key] = d
	}
	for k, v := range labels {
		if f, bad := forbiddenSet[k]; bad {
			return fmt.Errorf("标签 %q 在禁区清单里（%s）", k, f.Why)
		}
		d, ok := declared[k]
		if !ok {
			return fmt.Errorf("指标 %s 未声明标签 %q（登记表是口径的唯一来源）", name, k)
		}
		if !slices.Contains(d.Values, v) {
			return fmt.Errorf("标签 %s=%q 越界：登记的取值域是 %v", k, v, d.Values)
		}
	}
	return nil
}

// ── 进程内参考实现 ──────────────────────────────────────────────────────────

// Mem 进程内参考 Recorder：计数 / 观测 / 设值全部收进内存。
//
// 两个用途：宿主测试把它接进 SetDefault 断言「埋点真的响了」；自写 OTel /
// Prometheus 适配器时照它的形状抄（标签规范化成排序键、并发安全、采集不阻塞埋点）。
type Mem struct {
	mu     sync.Mutex
	counts map[string]int64
	gauges map[string]int64
	hist   map[string][]float64
	// Strict 为真时 Collect 对每次埋点跑 Validate，违规计入 Bad（缺省 false：
	// 生产语义是旁路不反噬，严格档留给测试）。
	Strict bool
	// Bad 累计的违规埋点数（Strict 模式下测试断言它为 0）。
	Bad int
}

func (m *Mem) key(name string, labels Labels) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte('|')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// Count 实现 Recorder。
func (m *Mem) Count(name string, delta int64, labels Labels) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Strict && Validate(name, labels) != nil {
		m.Bad++
		return
	}
	if m.counts == nil {
		m.counts = map[string]int64{}
	}
	m.counts[m.key(name, labels)] += delta
}

// Observe 实现 Recorder。
func (m *Mem) Observe(name string, ms float64, labels Labels) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Strict && Validate(name, labels) != nil {
		m.Bad++
		return
	}
	if m.hist == nil {
		m.hist = map[string][]float64{}
	}
	k := m.key(name, labels)
	m.hist[k] = append(m.hist[k], ms)
}

// Set 实现 Recorder。
func (m *Mem) Set(name string, v int64, labels Labels) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Strict && Validate(name, labels) != nil {
		m.Bad++
		return
	}
	if m.gauges == nil {
		m.gauges = map[string]int64{}
	}
	m.gauges[m.key(name, labels)] = v
}

// Counts 返回计数快照：键 = 指标名 + "|" + 排序后的 k=v 对（与 key() 同一规范），
// 无标签时就是指标名。副本——调用方改不动内部账本。
func (m *Mem) Counts() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.counts))
	for k, v := range m.counts {
		out[k] = v
	}
	return out
}

// Gauges 返回仪表快照（键规范同 Counts）。
func (m *Mem) Gauges() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.gauges))
	for k, v := range m.gauges {
		out[k] = v
	}
	return out
}

// Observations 返回某键（规范同 Counts）的直方图观测副本。
func (m *Mem) Observations(key string) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hist == nil {
		return nil
	}
	return slices.Clone(m.hist[key])
}

// Sum 返回某键的观测总和（耗时看板最常用的聚合；键规范同 Counts）。
func (m *Mem) Sum(key string) float64 {
	obs := m.Observations(key)
	var total float64
	for _, v := range obs {
		total += v
	}
	return total
}
