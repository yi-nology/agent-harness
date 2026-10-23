package checkpoint

// 零值语义的对照面：拿旧口径的参照实现（oracle）逐字比对新实现。
//
// 这份文件与 deps_test.go 分开：前者钉「新语义对不对」，这里钉
// 「新语义在零值图上有没有偷偷改掉旧行为」。两件事的失败形态不同——
// 前者红说明功能坏了，后者红说明**兼容性破了**（下游全得重验一遍）。

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/yi-nology/agent-harness/contract"
)

// legacyPlan 旧口径的**参照实现**（oracle）：连续已完成前缀 + 单条不点名异常。
//
// 用途：把「零值语义 = 与旧口径逐字一致」这条承诺从**推理**变成**机械判定**。
// 新实现走的是「可信完成集」fixed point，旧实现走的是前缀扫描——两条不同的路，
// 只有拿同一批输入逐字比对，才能证明它们在零值图上等价。
//
// 这份实现是**快照**：它记录旧口径的语义，不跟着新实现走。
// 故意不抽公共函数——抽了就变成「拿新实现验新实现」，比对失去意义。
func legacyPlan(graph []StepDef, snap *Snapshot) Plan {
	var p Plan

	seen := make(map[string]bool, len(graph))
	ids := make([]string, 0, len(graph))
	for _, s := range graph {
		switch {
		case s.ID == "":
			p.addAnomaly(AnomalyEmptyStepID, "", "步骤 ID 为空，已从编排图剔除")
		case seen[s.ID]:
			p.addAnomaly(AnomalyDuplicateStep, s.ID, "编排图内步骤 ID 重复，只保留首次出现处")
		default:
			seen[s.ID] = true
			ids = append(ids, s.ID)
		}
	}

	if snap == nil {
		p.Run = ids
		return p
	}

	done := make(map[string]bool, len(snap.Done))
	for _, id := range snap.Done {
		if id != "" {
			done[id] = true
		}
	}
	for _, id := range snap.Done {
		if id != "" && !seen[id] {
			p.addAnomaly(AnomalyDoneNotInGraph, id, "快照记为已完成，但当前编排图无此步骤")
		}
	}

	n := 0
	for ; n < len(ids) && done[ids[n]]; n++ {
	}
	p.Skip = ids[:n:n]
	p.Run = ids[n:]

	if n < len(ids) {
		for _, id := range ids[n:] {
			if done[id] {
				p.addAnomaly(AnomalyDoneOutOfOrder, "",
					"已完成步骤不是编排图的前缀，其后的完成记录不采信")
				break
			}
		}
	}

	if isTerminal(snap.Status) && len(p.Run) > 0 {
		p.addAnomaly(AnomalyCompleteWithPending, "",
			"快照标记运行已终结但仍有未完成步骤，内核不自动重开，需宿主显式决定")
		p.Skip, p.Run = nil, nil
	}

	return p
}

// TestZeroValueMatchesLegacyOracle 随机有序链图逐例比对：零值语义与旧口径逐字一致。
//
// 这条用例是「零值 = 旧行为不变」这条承诺的**唯一机械判定面**：
// 少了它，那条承诺就只能靠人读代码确认——而人读不出来。
func TestZeroValueMatchesLegacyOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20261023))
	statuses := []string{
		contract.StatusRunning, contract.StatusSucceeded, contract.StatusFailed,
		contract.StatusCancelled, contract.StatusTimedOut, contract.StatusWaitingApproval,
	}
	for i := 0; i < 300; i++ {
		n := 1 + rng.Intn(8)
		graph := make([]StepDef, 0, n)
		for j := 0; j < n; j++ {
			graph = append(graph, StepDef{ID: fmt.Sprintf("step-%02d", j)})
		}

		var s *Snapshot
		if rng.Intn(5) != 0 { // 留出首次执行（无快照）的情形
			tmp := snap("run", uint64(i+1), statuses[rng.Intn(len(statuses))])
			for j := 0; j < n; j++ {
				if rng.Intn(2) == 0 {
					tmp.Done = append(tmp.Done, graph[j].ID)
				}
			}
			if rng.Intn(6) == 0 {
				tmp.Done = append(tmp.Done, "step-ghost") // 图外完成项
			}
			s = &tmp
		}

		got := BuildPlan(graph, s)
		want := legacyPlan(graph, s)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 例零值语义与旧口径不一致:\n图 %+v\n快照 %+v\n实得 %+v\n期望 %+v",
				i, graph, s, got, want)
		}
	}
}
