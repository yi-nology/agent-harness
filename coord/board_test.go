package coord

import (
	"testing"

	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/taskboard/boardtest"
)

// BoardAdapter 接入共享契约套件（硬失败不跳过——接入即声明满足契约）。
func TestBoardAdapterContractSuite(t *testing.T) {
	boardtest.Run(t, func(t *testing.T, clock *boardtest.FakeClock) boardtest.ClockedBoard {
		t.Helper()
		b := NewBoardAdapter(NewMemoryBackend())
		b.UseClock(clock.Now)
		return b
	})
}

func TestBoardAdapterListEmpty(t *testing.T) {
	b := NewBoardAdapter(NewMemoryBackend())
	tasks, err := b.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if tasks != nil {
		t.Fatalf("空任务板必须返回 nil（实得 %v）", tasks)
	}
}

func TestBoardAdapterSeedSkipsEmptyID(t *testing.T) {
	b := NewBoardAdapter(NewMemoryBackend())
	b.Seed(taskboard.Task{ID: ""}, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	tasks, err := b.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("空 id 任务必须被跳过（实得 %v）", tasks)
	}
}

func TestBoardAdapterConcurrentClaimJudgment(t *testing.T) {
	// 判据 taskboard.ConcurrentClaim 直接跑在适配器上（契约条款的可跑形态）。
	b := NewBoardAdapter(NewMemoryBackend())
	b.Seed(taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	n, err := taskboard.ConcurrentClaim(t.Context(), b, "t1", []taskboard.AgentID{"a1", "a2", "a3"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("并发认领先成功者必须恰好 1 个（实得 %d）", n)
	}
}
