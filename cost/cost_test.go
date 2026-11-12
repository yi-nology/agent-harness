package cost

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

func TestEventValidate(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  error
	}{
		{"正常事件", Event{Kind: KindInputTokens, Amount: 100}, nil},
		{"缺种类", Event{Amount: 100}, ErrEmptyKind},
		{"负量", Event{Kind: KindToolCalls, Amount: -1}, ErrNegativeAmount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.event.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEventClone(t *testing.T) {
	src := Event{Kind: KindWallTime, Amount: 5, Dims: map[string]string{DimRun: "r1", DimTask: "t1"}}
	cp := src.Clone()
	cp.Dims[DimRun] = "改写"
	if src.Dims[DimRun] != "r1" {
		t.Fatalf("Clone 后改写副本牵动了原事件：src.Dims[%s]=%q", DimRun, src.Dims[DimRun])
	}
	// nil 维度保持 nil，不凭空造空 map
	if (Event{Kind: KindToolCalls, Amount: 1}).Clone().Dims != nil {
		t.Fatal("nil Dims 被 Clone 成了空 map")
	}
}

func TestLedgerRecordRejectsInvalid(t *testing.T) {
	var l Ledger
	if err := l.Record(Event{Amount: 10}); !errors.Is(err, ErrEmptyKind) {
		t.Fatalf("空 Kind 应拒收 ErrEmptyKind，got %v", err)
	}
	if err := l.Record(Event{Kind: KindInputTokens, Amount: -5}); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("负量应拒收 ErrNegativeAmount，got %v", err)
	}
	if l.Count() != 0 {
		t.Fatalf("坏账被入账了：Count=%d", l.Count())
	}
}

func TestLedgerRecordCopiesDims(t *testing.T) {
	var l Ledger
	dims := map[string]string{DimRun: "r1"}
	if err := l.Record(Event{Kind: KindInputTokens, Amount: 100, Dims: dims}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	dims[DimRun] = "入账后改写" // 调用方复用 map，不得牵动账面
	for e := range l.Events() {
		if e.Dims[DimRun] != "r1" {
			t.Fatalf("账面被调用方改写：Dims[%s]=%q", DimRun, e.Dims[DimRun])
		}
	}
}

func TestLedgerEventsYieldCopies(t *testing.T) {
	var l Ledger
	_ = l.Record(Event{Kind: KindOutputTokens, Amount: 7, Dims: map[string]string{DimTask: "t1"}})
	for e := range l.Events() {
		e.Dims[DimTask] = "聚合侧改写" // 迭代值是副本，改它不得动账面
		e.Amount = 999
	}
	if l.Count() != 1 {
		t.Fatalf("Count=%d, want 1", l.Count())
	}
	for e := range l.Events() {
		if e.Dims[DimTask] != "t1" || e.Amount != 7 {
			t.Fatalf("账面被聚合侧改写： %+v", e)
		}
	}
}

func TestLedgerEventsEarlyStop(t *testing.T) {
	var l Ledger
	for i := range 5 {
		_ = l.Record(Event{Kind: KindToolCalls, Amount: int64(i)})
	}
	seen := 0
	for range l.Events() {
		seen++
		if seen == 2 {
			break // 提前终止安全
		}
	}
	if seen != 2 {
		t.Fatalf("提前终止后 seen=%d, want 2", seen)
	}
	if l.Count() != 5 {
		t.Fatalf("提前终止动了账面：Count=%d", l.Count())
	}
}

// 聚合的事件源夹具：三条租户 A / 两条租户 B（其中一条缺任务维度）。
func fixtureEvents() []Event {
	return []Event{
		{Kind: KindInputTokens, Amount: 100, Dims: map[string]string{DimTenant: "acme", DimRun: "r1", DimTask: "t1"}},
		{Kind: KindOutputTokens, Amount: 50, Dims: map[string]string{DimTenant: "acme", DimRun: "r1", DimTask: "t1"}},
		{Kind: KindToolCalls, Amount: 2, Dims: map[string]string{DimTenant: "acme", DimRun: "r2"}},
		{Kind: KindInputTokens, Amount: 30, Dims: map[string]string{DimTenant: "glob", DimRun: "r3"}},
		{Kind: KindWallTime, Amount: int64(time.Second), Dims: map[string]string{DimTenant: "glob"}},
	}
}

func TestAggregateByTenant(t *testing.T) {
	totals, err := ByTenant(slices.Values(fixtureEvents()))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(totals) != 2 {
		t.Fatalf("按租户应聚出 2 组，got %d", len(totals))
	}
	// 确定性排序：字典序 acme < glob
	if totals[0].Dims[DimTenant] != "acme" || totals[1].Dims[DimTenant] != "glob" {
		t.Fatalf("分组顺序非确定： %+v", totals)
	}
	acme := totals[0]
	if acme.Events != 3 {
		t.Fatalf("acme 组事件数 = %d, want 3", acme.Events)
	}
	if acme.Amount(KindInputTokens) != 100 || acme.Amount(KindOutputTokens) != 50 || acme.Amount(KindToolCalls) != 2 {
		t.Fatalf("acme 组合计漂移： %+v", acme.Amounts)
	}
	if acme.Amount(KindConcurrency) != 0 {
		t.Fatal("未出现的 Kind 应返回 0")
	}
	// 分组维度恰为分组键集合
	if len(acme.Dims) != 1 {
		t.Fatalf("Dims 应只含分组键，got %+v", acme.Dims)
	}
}

func TestAggregateMultiDims(t *testing.T) {
	totals, err := Aggregate(slices.Values(fixtureEvents()), DimTenant, DimRun)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	// 组：acme/r1（2 事件）、acme/r2（1）、glob/空 run（1）、glob/r3（1）
	// ——「glob\x00」是「glob\x00r3」的前缀，空 run 组排在前（字典序确定）。
	if len(totals) != 4 {
		t.Fatalf("双维度应聚出 4 组，got %d：%+v", len(totals), totals)
	}
	empty := totals[2]
	if empty.Dims[DimTenant] != "glob" || empty.Dims[DimRun] != "" {
		t.Fatalf("缺 run 维度的事件应落到空 run 组： %+v", empty)
	}
	// 墙钟量可直接转回 Duration（同刻度）
	if time.Duration(empty.Amount(KindWallTime)) != time.Second {
		t.Fatalf("墙钟合计刻度漂移： %v", empty.Amount(KindWallTime))
	}
}

func TestAggregateNoDims(t *testing.T) {
	totals, err := Aggregate(slices.Values(fixtureEvents()))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(totals) != 1 {
		t.Fatalf("不分组应合成一组，got %d", len(totals))
	}
	sum := int64(0)
	for _, a := range totals[0].Amounts {
		sum += a
	}
	if totals[0].Events != 5 {
		t.Fatalf("组内事件数 = %d, want 5", totals[0].Events)
	}
	_ = sum
}

func TestAggregateEmptySource(t *testing.T) {
	totals, err := Aggregate(slices.Values([]Event{}), DimTenant)
	if err != nil {
		t.Fatalf("空事件源应返回空结果而非错误： %v", err)
	}
	if len(totals) != 0 {
		t.Fatalf("空事件源应聚出 0 组，got %d", len(totals))
	}
}

func TestAggregateRejectsInvalidEvent(t *testing.T) {
	bad := Event{Amount: 10, Dims: map[string]string{DimTenant: "acme"}} // 缺 Kind
	if _, err := Aggregate(slices.Values([]Event{bad}), DimTenant); !errors.Is(err, ErrEmptyKind) {
		t.Fatalf("口径外事件应被拒收（ErrEmptyKind），got %v", err)
	}
	neg := Event{Kind: KindToolCalls, Amount: -1}
	if _, err := ByTask(slices.Values([]Event{neg})); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("口径外事件应被拒收（ErrNegativeAmount），got %v", err)
	}
}

func TestLedgerAggregateComposition(t *testing.T) {
	var l Ledger
	for _, e := range fixtureEvents() {
		if err := l.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	byTask, err := ByTask(l.Events())
	if err != nil {
		t.Fatalf("ByTask: %v", err)
	}
	// 组：空任务（3 事件——工具调用与无任务维度的输入）+ t1（2 事件）；
	// 空串字典序在前（确定性排序）。
	if len(byTask) != 2 {
		t.Fatalf("按任务应聚出 2 组，got %d", len(byTask))
	}
	if byTask[1].Dims[DimTask] != "t1" {
		t.Fatalf("确定性排序漂移： %+v", byTask)
	}
	if byTask[1].Amount(KindInputTokens) != 100 || byTask[1].Amount(KindOutputTokens) != 50 {
		t.Fatalf("t1 组合计漂移： %+v", byTask[1].Amounts)
	}
}

func TestLedgerConcurrentRecord(t *testing.T) {
	var l Ledger
	const workers, perWorker = 8, 50
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				err := l.Record(Event{Kind: KindToolCalls, Amount: 1,
					Dims: map[string]string{DimAgent: "agent-" + string(rune('a'+w)), DimTask: string(rune('0' + i%10))}})
				if err != nil {
					t.Errorf("Record: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if l.Count() != workers*perWorker {
		t.Fatalf("并发入账丢账：Count=%d, want %d", l.Count(), workers*perWorker)
	}
	totals, err := Aggregate(l.Events(), DimAgent)
	if err != nil {
		t.Fatalf("ByAgentAll: %v", err)
	}
	if len(totals) != workers {
		t.Fatalf("按 agent 应聚出 %d 组，got %d", workers, len(totals))
	}
	for _, total := range totals {
		if total.Amount(KindToolCalls) != perWorker {
			t.Fatalf("agent %v 合计漂移： %d", total.Dims, total.Amount(KindToolCalls))
		}
	}
}
