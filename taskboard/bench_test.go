// bench_test.go —— TaskBoard 的性能基线（issue #101）。
//
// taskboard 包内的确定性核心是 Check（任务图校验：id 唯一 / 状态词表 /
// 认领-租约配对 / 依赖存在 / 环检测）。它是宿主装配与派发前的判定面，
// 纯函数零存储——基线量「N 任务带依赖的图」过一遍校验的成本，环检测
// （detectCycle）是其中随图形态最敏感的一段，基线要钉住它不被无意拖慢。
//
// 认领/续期本身的基线属于存储实现（本仓只给契约与判据，Board 的实现归宿主；
// PG 参考实现的竞争面在 examples/pgstore 的契约套件里真跑）。数据规模：
// 200 任务，每任务依赖 0..3 个更早的任务（无环），约 1/3 处于终态。
//
// # 怎么读这些数
//
// 基线不是门槛（口径见 docs/BENCH-BASELINE-20261113.md）。
package taskboard_test

import (
	"fmt"
	"testing"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// benchTasks 200 任务的 DAG 夹具：open / failed 任务依赖 i-1..i-3（截断到 0）；
// done 任务只依赖更早的 done 任务（Check 判「依赖未完成却已完成」——终态
// 任务的依赖必须全为终态，夹具按此构造才无问题）。约 1/3 终态、1/3 失败
// （失败非终态，不阻断 open 任务的依赖声明）。
func benchTasks(n int) []taskboard.Task {
	tasks := make([]taskboard.Task, 0, n)
	for i := range n {
		state := taskboard.StateOpen
		switch i % 3 {
		case 1:
			state = taskboard.StateDone
		case 2:
			state = taskboard.StateFailed
		}
		var deps []taskboard.TaskID
		if state == taskboard.StateDone {
			if i-3 >= 0 {
				deps = []taskboard.TaskID{taskboard.TaskID(fmt.Sprintf("t-%d", i-3))}
			}
		} else {
			for d := 1; d <= 3 && i-d >= 0; d++ {
				deps = append(deps, taskboard.TaskID(fmt.Sprintf("t-%d", i-d)))
			}
		}
		tasks = append(tasks, taskboard.Task{
			ID:    taskboard.TaskID(fmt.Sprintf("t-%d", i)),
			Title: "bench task",
			Deps:  deps,
			State: state,
			Seq:   uint64(i),
		})
	}
	return tasks
}

func BenchmarkTaskBoardCheck(b *testing.B) {
	tasks := benchTasks(200)
	for b.Loop() {
		if problems := taskboard.Check(tasks); len(problems) != 0 {
			b.Fatalf("夹具应无问题，实得 %d 条", len(problems))
		}
	}
}

// BenchmarkTaskBoardCheckChain 深链形态：200 任务串成一条链（依赖全部指向
// 前一个）——环检测在链上每次都要走到底，是最坏形态之一。
func BenchmarkTaskBoardCheckChain(b *testing.B) {
	tasks := make([]taskboard.Task, 0, 200)
	for i := range 200 {
		var deps []taskboard.TaskID
		if i > 0 {
			deps = []taskboard.TaskID{taskboard.TaskID(fmt.Sprintf("t-%d", i-1))}
		}
		tasks = append(tasks, taskboard.Task{
			ID:    taskboard.TaskID(fmt.Sprintf("t-%d", i)),
			State: taskboard.StateOpen,
			Deps:  deps,
		})
	}
	for b.Loop() {
		if problems := taskboard.Check(tasks); len(problems) != 0 {
			b.Fatalf("夹具应无问题，实得 %d 条", len(problems))
		}
	}
}
