package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/coord"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// 测试夹具全部合成数据（AGENTS.md 红线）。任务板用 coord 的适配器 +
// 内存后端（判定逻辑那份实现已被契约套件验证过），注册表用本包替身。

func newTestBoard(clock *testClock) *coord.BoardAdapter {
	b := coord.NewBoardAdapter(coord.NewMemoryBackend())
	b.UseClock(clock.Now)
	return b
}

func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr error
	}{
		{"合法", Spec{ID: "w-1", TTL: time.Minute}, nil},
		{"缺 ID", Spec{TTL: time.Minute}, ErrInvalidConfig},
		{"零 TTL", Spec{ID: "w-1"}, ErrInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr == nil && err != nil {
				t.Fatalf("合法声明不得报错（实得 %v）", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("必须报 %v（实得 %v）", tc.wantErr, err)
			}
		})
	}
}

func TestNewLoopValidatesConfig(t *testing.T) {
	clock := newTestClock()
	board := newTestBoard(clock)
	reg := newMemRegistry(clock.Now)
	handler := HandlerFunc(func(context.Context, taskboard.Task) error { return nil })

	cases := []struct {
		name string
		cfg  Config
	}{
		{"缺 Agent", Config{TTL: time.Minute}},
		{"零 TTL", Config{Agent: "w-1"}},
		{"心跳不短于 TTL", Config{Agent: "w-1", TTL: time.Minute, HeartbeatInterval: 2 * time.Minute}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLoop(board, reg, handler, tc.cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("必须报 ErrInvalidConfig（实得 %v）", err)
			}
		})
	}
	if _, err := NewLoop(nil, reg, handler, Config{Agent: "w-1", TTL: time.Minute}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("缺任务板必须报 ErrInvalidConfig（实得 %v）", err)
	}
	// 缺省值补齐：PollInterval 0 → 缺省；HeartbeatInterval 0 → TTL/3。
	loop, err := NewLoop(board, reg, handler, Config{Agent: "w-1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if loop.cfg.PollInterval != DefaultPollInterval || loop.cfg.HeartbeatInterval != 20*time.Second {
		t.Fatalf("缺省值未补齐：poll=%v heartbeat=%v", loop.cfg.PollInterval, loop.cfg.HeartbeatInterval)
	}
}

func TestLoopHappyPathLifecycle(t *testing.T) {
	clock := newTestClock()
	board := newTestBoard(clock)
	reg := newMemRegistry(clock.Now)

	// 依赖链：t1 ← t2 ← t3。驱动按 ID 序认领，依赖完成度挡住乱序。
	board.Seed(
		taskboard.Task{ID: "t2", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t1"}},
		taskboard.Task{ID: "t1", State: taskboard.StateOpen},
		taskboard.Task{ID: "t3", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t2"}},
	)

	var (
		mu       sync.Mutex
		executed []taskboard.TaskID
	)
	ran := make(chan taskboard.TaskID, 3)
	handler := HandlerFunc(func(_ context.Context, task taskboard.Task) error {
		mu.Lock()
		executed = append(executed, task.ID)
		mu.Unlock()
		ran <- task.ID
		return nil
	})

	loop, err := NewLoop(board, reg, handler, Config{
		Agent: "w-1", TTL: time.Minute, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	for range 3 {
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Fatal("等待任务执行超时")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("优雅退出必须返回 nil（实得 %v）", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 3 || executed[0] != "t1" || executed[1] != "t2" || executed[2] != "t3" {
		t.Fatalf("执行序必须沿依赖链（实得 %v）", executed)
	}
	tasks, _ := board.List(context.Background())
	for _, task := range tasks {
		if task.State != taskboard.StateDone {
			t.Fatalf("任务 %s 必须已完成（实得 %s）", task.ID, task.State)
		}
	}
	// 注销已发生：注册表空。
	if st, _ := reg.List(context.Background()); len(st) != 0 {
		t.Fatalf("退出后注册表必须干净（实得 %v）", st)
	}
}

func TestLoopLeaseLostCancelsInFlight(t *testing.T) {
	// 注册表用独立时钟（不推进）：续期失败要由**任务租约**单独触发，
	// 不与注册过期混在一起——两种资格的处置不同，测试就得分开构造。
	boardClock := newTestClock()
	regClock := newTestClock()
	board := newTestBoard(boardClock)
	reg := newMemRegistry(regClock.Now)
	board.Seed(taskboard.Task{ID: "t1", State: taskboard.StateOpen})

	// handler 停在闸上但**同时监听取消**：租约易主时驱动会取消在途 ctx，
	// handler 必须能从闸上醒来（这正是「施加之前封口」要求宿主写的形态）。
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	var sawCancel atomic.Bool
	handler := HandlerFunc(func(ctx context.Context, _ taskboard.Task) error {
		entered <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
			sawCancel.Store(true)
		}
		if ctx.Err() != nil {
			sawCancel.Store(true)
		}
		return errors.New("worker: 资格检查失败后的收场（替身）")
	})

	loop, err := NewLoop(board, reg, handler, Config{
		Agent: "w-1", TTL: time.Minute, PollInterval: 5 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("等待在途执行超时")
	}
	boardClock.Advance(2 * time.Minute) // 租约过期：下一拍 Renew 必报 ErrLeaseLost

	deadline := time.After(5 * time.Second)
	for !sawCancel.Load() {
		select {
		case <-deadline:
			t.Fatal("租约易主后必须取消在途执行——危险窗口要在施加之前封口")
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(gate)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("本次任务按失败收场后应优雅退出（实得 %v）", err)
	}
	tasks, _ := board.List(context.Background())
	if tasks[0].State != taskboard.StateOpen {
		t.Fatalf("失败收场的任务必须回到可重做（实得 %s）", tasks[0].State)
	}
}

func TestLoopRegistrationLossStopsClaiming(t *testing.T) {
	// 注册表时钟推进、任务板时钟不动：身份丢失要单独触发（任务租约仍活）。
	boardClock := newTestClock()
	regClock := newTestClock()
	board := newTestBoard(boardClock)
	reg := newMemRegistry(regClock.Now)
	board.Seed(taskboard.Task{ID: "t1", State: taskboard.StateOpen})

	handler := HandlerFunc(func(ctx context.Context, _ taskboard.Task) error {
		<-ctx.Done() // 在途直到被取消
		return ctx.Err()
	})
	loop, err := NewLoop(board, reg, handler, Config{
		Agent: "w-1", TTL: time.Minute, PollInterval: 5 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	// 等在途进入（轮询认领需要几拍）。
	deadline := time.After(5 * time.Second)
	for {
		tasks, _ := board.List(context.Background())
		if tasks[0].State == taskboard.StateClaimed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("等待认领超时")
		case <-time.After(5 * time.Millisecond):
		}
	}
	regClock.Advance(2 * time.Minute) // 注册过期：下一次心跳报 ErrNotRegistered
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegistered) {
			t.Fatalf("身份丢失必须以 ErrNotRegistered 收刀（实得 %v）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待驱动收刀超时")
	}
	tasks, _ := board.List(context.Background())
	if tasks[0].State != taskboard.StateOpen {
		t.Fatalf("收刀时在途任务必须被释放（实得 %s）", tasks[0].State)
	}
}
func TestRecoverTakesOverExpiredClaims(t *testing.T) {
	clock := newTestClock()
	board := newTestBoard(clock)

	// 前任认领 t1 后「崩溃」（时钟推过租约）：恢复者接管。
	// t2 活租约不抢；t3 open 本来就能被正常领取环拿走，不算恢复范畴。
	board.Seed(
		taskboard.Task{ID: "t1", State: taskboard.StateOpen},
		taskboard.Task{ID: "t2", State: taskboard.StateOpen},
		taskboard.Task{ID: "t3", State: taskboard.StateOpen},
	)
	if _, err := board.Claim(context.Background(), "t1", "前任", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Claim(context.Background(), "t2", "现任", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute) // t1 过期；t2 同刻认领、同刻过期——需要区分开

	// t2 重新认领续命（模拟现任还活着），t1 保持过期。
	if _, err := board.Claim(context.Background(), "t2", "现任", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}

	took, err := Recover(context.Background(), board, "rescuer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(took) != 1 || took[0] != "t1" {
		t.Fatalf("只接管过期的 t1（实得 %v）——活租约不得被抢", took)
	}
	tasks, _ := board.List(context.Background())
	if tasks[0].Owner != "rescuer" {
		t.Fatalf("接管的任务应归恢复者（实得 %s）", tasks[0].Owner)
	}
}

func TestRecoverValidatesArgs(t *testing.T) {
	clock := newTestClock()
	board := newTestBoard(clock)
	if _, err := Recover(context.Background(), nil, "r", time.Minute); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("缺任务板必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if _, err := Recover(context.Background(), board, "", time.Minute); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("缺接管者必须报 ErrInvalidConfig（实得 %v）", err)
	}
	if _, err := Recover(context.Background(), board, "r", 0); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("零 TTL 必须报 ErrInvalidConfig（实得 %v）", err)
	}
}

func TestCandidatesOrdering(t *testing.T) {
	tasks := []taskboard.Task{
		{ID: "b", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"a"}},
		{ID: "c", State: taskboard.StateFailed},
		{ID: "a", State: taskboard.StateOpen},
		{ID: "d", State: taskboard.StateClaimed},
	}
	got := candidates(tasks)
	// b 的依赖 a 未完成 ⇒ 不在候选；failed 的 c 可重做；claimed 的 d 排除。
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("候选 = [a c]（实得 %v）", got)
	}
}

// TestLoopConcurrentClaimSingleWinner 多驱动并发抢同一任务：恰好一个赢家
// （领取的原子性归任务板，驱动只负责认领动作）。
func TestLoopConcurrentClaimSingleWinner(t *testing.T) {
	clock := newTestClock()
	board := newTestBoard(clock)
	board.Seed(taskboard.Task{ID: "t1", State: taskboard.StateOpen})

	var executed atomic.Int64
	handler := HandlerFunc(func(context.Context, taskboard.Task) error {
		executed.Add(1)
		return nil
	})
	reg := newMemRegistry(clock.Now)

	// 三个驱动同跑：t1 只能被认领一次，其余驱动空轮询。
	loops := make([]*Loop, 0, 3)
	for i := range 3 {
		loop, err := NewLoop(board, reg, handler, Config{
			Agent: taskboard.AgentID("w-" + string(rune('a'+i))),
			TTL:   time.Minute, PollInterval: 5 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		loops = append(loops, loop)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 3)
	for _, l := range loops {
		l := l
		go func() { done <- l.Run(ctx) }()
	}
	deadline := time.After(5 * time.Second)
	for executed.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("等待执行超时")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	for range 3 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("驱动应优雅退出（实得 %v）", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("等待驱动退出超时")
		}
	}
	// 收刀后的硬断言：恰好一次执行，任务恰好一次完成（并发认领的原子性
	// 归任务板——>1 个执行者的驱动形态在这里必红）。
	if got := executed.Load(); got != 1 {
		t.Fatalf("并发认领必须恰好执行一次（实得 %d）", got)
	}
	tasks, _ := board.List(context.Background())
	if tasks[0].State != taskboard.StateDone {
		t.Fatalf("任务必须恰好完成一次（实得 %s）", tasks[0].State)
	}
}
