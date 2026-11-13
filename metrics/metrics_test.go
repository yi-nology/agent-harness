// metrics_test.go —— 指标契约面的自校验（与 trajectory/otelmap_test 同一条纪律）。
//
// 「规范」若只当文档读，半年后就与代码事实脱节。这里把登记表的可机器判定面
// 全部判掉：名字唯一、前缀统一、类型/单位合法、每个维度有界且写明依据、
// 禁区键不进任何维度、与上游枚举（trajectory 行类目 / retryplan 路径）逐字
// 对齐。对齐测试的意义与「规则没有执行体」自校验同源：两份枚举各自长大而不
// 被发现，看板就在某个无声的 Tuesday 拼不起来了。
package metrics

import (
	"slices"
	"testing"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/retryplan"
	"github.com/yi-nology/goagent-kernel/trajectory"
)

// TestSpecsSelfCheck 登记表自洽：名称唯一、前缀统一、类型与单位合法、
// 每个维度有界且依据非空、禁区键不进维度。
func TestSpecsSelfCheck(t *testing.T) {
	all := Specs()
	if len(all) == 0 {
		t.Fatal("登记表为空：契约面没有登记任何指标")
	}
	seen := map[string]bool{}
	for _, m := range all {
		if seen[m.Name] {
			t.Errorf("指标名 %q 重复：看板与查询按名字对应，重名等于两个口径", m.Name)
		}
		seen[m.Name] = true

		if !startsWithHarnessPrefix(m.Name) {
			t.Errorf("指标名 %q 不带 harness. 前缀：与 trajectory 属性命名同一纪律（不在 OTel 官方命名空间）", m.Name)
		}
		switch m.Kind {
		case KindCounter, KindGauge, KindHistogram:
		default:
			t.Errorf("指标 %s 类型 %q 非法（counter/gauge/histogram 三选一）", m.Name, m.Kind)
		}
		if m.Unit == "" {
			t.Errorf("指标 %s 没写单位：计数/仪表写 \"1\"，耗时写 ms——空单位让看板自猜", m.Name)
		}
		if m.Desc == "" {
			t.Errorf("指标 %s 没写语义说明", m.Name)
		}
		for _, d := range m.Labels {
			if d.Card() == 0 {
				t.Errorf("指标 %s 的标签 %s 没有取值域：无界基数就是这里漏进来的（otelmap 同源纪律）", m.Name, d.Key)
			}
			if d.Why == "" {
				t.Errorf("指标 %s 的标签 %s 基数上界没写依据：声明自相矛盾（otelmap 同源判据）", m.Name, d.Key)
			}
			vals := map[string]bool{}
			for _, v := range d.Values {
				if vals[v] {
					t.Errorf("指标 %s 的标签 %s 取值 %q 重复", m.Name, d.Key, v)
				}
				vals[v] = true
			}
			if _, bad := forbiddenSet[d.Key]; bad {
				t.Errorf("指标 %s 的标签 %s 自己就在禁区清单里：声明自相矛盾", m.Name, d.Key)
			}
		}
	}
}

// startsWithHarnessPrefix 指标名前缀检查。
func startsWithHarnessPrefix(name string) bool {
	const p = "harness."
	return len(name) > len(p) && name[:len(p)] == p
}

// TestExecKindAlignsWithTrajectory 执行类目与 trajectory 行类目逐字对齐。
// 两份枚举各自长大会让看板在某个无声的时刻拼不起来——比对让它当场红。
func TestExecKindAlignsWithTrajectory(t *testing.T) {
	trajKinds := []string{
		string(trajectory.KindTurn),
		string(trajectory.KindStep),
		string(trajectory.KindToolCall),
		string(trajectory.KindToolResult),
		string(trajectory.KindSideEffect),
		string(trajectory.KindCheckpoint),
	}
	spec, ok := SpecOf(NameExecDuration)
	if !ok {
		t.Fatalf("耗时指标 %s 未登记", NameExecDuration)
	}
	var dim LabelDim
	for _, d := range spec.Labels {
		if d.Key == LabelExecKind {
			dim = d
		}
	}
	if dim.Card() == 0 {
		t.Fatalf("耗时指标没有 %s 维度", LabelExecKind)
	}
	for _, want := range trajKinds {
		if !slices.Contains(dim.Values, want) {
			t.Errorf("trajectory 行类目 %q 不在耗时指标的类目维度里：trajectory 加了类目、metrics 没跟上", want)
		}
	}
	if dim.Card() != len(trajKinds) {
		t.Errorf("类目维度基数 %d 与 trajectory 行类目数 %d 不一致：两边必须同步加", dim.Card(), len(trajKinds))
	}
}

// TestExecDurationAlignsWithTrajectoryDurationMs 耗时口径对齐：单位 ms、
// 直方图、语义说明点名 trajectory 的属性名（人读的部分也要对上）。
func TestExecDurationAlignsWithTrajectoryDurationMs(t *testing.T) {
	spec, ok := SpecOf(NameExecDuration)
	if !ok {
		t.Fatalf("耗时指标 %s 未登记", NameExecDuration)
	}
	if spec.Kind != KindHistogram {
		t.Errorf("耗时指标类型应为 histogram，实为 %s", spec.Kind)
	}
	if spec.Unit != "ms" {
		t.Errorf("耗时单位应为 ms（与 harness.trajectory.duration_ms 同口径），实为 %s", spec.Unit)
	}
}

// TestRetryPathAlignsWithRetryPlan 重试路径与 retryplan.Path 逐字对齐。
func TestRetryPathAlignsWithRetryPlan(t *testing.T) {
	spec, ok := SpecOf(NameRetryTotal)
	if !ok {
		t.Fatalf("重试指标 %s 未登记", NameRetryTotal)
	}
	var dim LabelDim
	for _, d := range spec.Labels {
		if d.Key == LabelRetryPath {
			dim = d
		}
	}
	want := []string{
		retryplan.PathNone.String(),
		retryplan.PathDirect.String(),
		retryplan.PathReplayed.String(),
	}
	if dim.Card() != len(want) {
		t.Fatalf("路径维度基数 %d 与 retryplan.Path 取值数 %d 不一致", dim.Card(), len(want))
	}
	for _, w := range want {
		if !slices.Contains(dim.Values, w) {
			t.Errorf("retryplan 路径 %q 不在重试指标的路径维度里：retryplan 加了路径、metrics 没跟上", w)
		}
	}
}

// TestValidate 埋点校验：越界取值、未声明键、禁区键、未登记名全部显式报错
// ——「埋错标签不报错、只在看板上拼不起来」是最贵的失效形态。
func TestValidate(t *testing.T) {
	okLabels := Labels{LabelLeaseSubsystem: LeaseSubsystemSideEffect, LabelLeaseOutcome: LeaseOutcomeContended}
	if err := Validate(NameLeaseContended, okLabels); err != nil {
		t.Fatalf("合法埋点被判违规: %v", err)
	}

	if err := Validate("harness.not.registered", nil); !errors.Is(err, ErrUnregisteredMetric) {
		t.Errorf("未登记指标名应报 ErrUnregisteredMetric，实得 %v", err)
	}

	err := Validate(NameLeaseContended, Labels{LabelLeaseSubsystem: "pg"})
	if err == nil {
		t.Error("越界取值（pg 不在子系统枚举里）应报错，实得 nil")
	}

	err = Validate(NameLeaseContended, Labels{"harness.tenant.id": "acme"})
	if err == nil {
		t.Error("未声明的标签键应报错（登记表是口径唯一来源），实得 nil")
	}

	for _, name := range []string{"session_id", "run_id", "key"} {
		err = Validate(NameUncertainBacklog, Labels{name: "x"})
		if err == nil {
			t.Errorf("禁区标签 %s 应报错", name)
		}
	}

	// 零维指标带标签 = 想加暗维度，拦。
	if err := Validate(NameUncertainBacklog, Labels{LabelExecStatus: ExecStatusOK}); err == nil {
		t.Error("零维指标（积压 gauge）不应接受任何标签")
	}
}

// TestMemRecorder 参考实现的记账与严格档。
func TestMemRecorder(t *testing.T) {
	m := &Mem{Strict: true}
	m.Count(NameLeaseContended, 1, Labels{LabelLeaseSubsystem: LeaseSubsystemSideEffect, LabelLeaseOutcome: LeaseOutcomeContended})
	m.Count(NameLeaseContended, 2, Labels{LabelLeaseSubsystem: LeaseSubsystemSideEffect, LabelLeaseOutcome: LeaseOutcomeUncertain})
	m.Set(NameUncertainBacklog, 7, nil)
	m.Observe(NameExecDuration, 12.5, Labels{LabelExecKind: ExecKindStep, LabelExecStatus: ExecStatusOK})
	m.Observe(NameExecDuration, 8.5, Labels{LabelExecKind: ExecKindStep, LabelExecStatus: ExecStatusOK})

	counts := m.Counts()
	if got := counts["harness.lease.contended|harness.lease.outcome=contended|harness.lease.subsystem=sideeffect"]; got != 1 {
		t.Errorf("contended 计数 = %d, 期望 1", got)
	}
	if got := m.Gauges()["harness.sideeffect.uncertain"]; got != 7 {
		t.Errorf("积压 gauge = %d, 期望 7", got)
	}
	key := "harness.exec.duration|harness.exec.kind=step|harness.exec.status=ok"
	if got := m.Sum(key); got != 21.0 {
		t.Errorf("耗时观测总和 = %v, 期望 21.0", got)
	}
	if len(m.Observations(key)) != 2 {
		t.Errorf("观测数 = %d, 期望 2", len(m.Observations(key)))
	}
	if m.Bad != 0 {
		t.Errorf("合法埋点被 Strict 档计入违规 %d 次", m.Bad)
	}

	// 违规埋点在严格档下不记账、只累计 Bad。
	m.Count(NameLeaseContended, 1, Labels{"harness.tenant": "acme"})
	m.Observe("harness.not.registered", 1, nil)
	if m.Bad != 2 {
		t.Errorf("Strict 档违规计数 = %d, 期望 2", m.Bad)
	}
	if got := counts["harness.lease.contended|harness.tenant=acme"]; got != 0 {
		t.Error("违规埋点不应记账")
	}
}

// TestDefaultNopAndSetDefault 缺省 Nop 不反噬执行路径；SetDefault 后埋点生效；
// nil 换回 Nop。测试用 t.Cleanup 恢复全局态——全局后端是装配面，用例不许互相污染。
func TestDefaultNopAndSetDefault(t *testing.T) {
	t.Cleanup(func() { SetDefault(nil) })

	if _, isNop := Default().(nop); !isNop {
		t.Fatalf("缺省后端应为 Nop，实为 %T", Default())
	}
	// Nop 下埋点不 panic（旁路纪律的最基本形态）。
	Count(NameRetryTotal, 1, Labels{LabelRetryPath: RetryPathDirect})

	mem := &Mem{}
	SetDefault(mem)
	Count(NameRetryTotal, 3, Labels{LabelRetryPath: RetryPathReplayed})
	Set(NameUncertainBacklog, 2, nil)
	if got := mem.Counts()["harness.retry.total|harness.retry.path=replayed"]; got != 3 {
		t.Errorf("换后端后计数未生效：%d, 期望 3", got)
	}

	SetDefault(nil)
	if _, isNop := Default().(nop); !isNop {
		t.Fatalf("SetDefault(nil) 应换回 Nop，实为 %T", Default())
	}
}
