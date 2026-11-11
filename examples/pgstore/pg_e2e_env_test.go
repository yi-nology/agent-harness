//go:build unix

// pg_e2e_env_test.go —— 故障恢复 e2e 的**父进程侧基础设施**（issue #95）。
//
// e2eEnv 造一套独立 schema + 三份存储 + 观测连接；childProc 封 re-exec 子进程
// 的起、读（判定结论）、杀（SIGKILL）；其余是轮询与断言助手。用例本体在
// pg_e2e_test.go（三条：死在施加之前 / 死在施加之后 / 双进程竞争）。
package pgstore

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// ── 父进程侧 ────────────────────────────────────────────────────────────

// e2eEnv 一条用例的完整环境：独立 schema + 三份存储 + 观测连接。
type e2eEnv struct {
	dsn      string
	schema   string
	key      string
	resource string
	ttl      time.Duration
	pool     *pgxpool.Pool
	eff      *EffectStore
	ds       *Downstream
}

// freshE2E 建一套全新环境（含 e2e 观测表）。
func freshE2E(t *testing.T, ttl time.Duration) *e2eEnv {
	t.Helper()
	dsn := requireDSN(t)
	ctx := context.Background()
	admin := basePool(t, dsn)
	schema, err := NewSchema(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DropSchema(ctx, admin, schema) })
	if err := EnsureSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	pool := schemaPool(t, dsn, schema)
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS pgref_e2e_calls (
		key   TEXT PRIMARY KEY,
		calls BIGINT NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("建 e2e 观测表失败: %v", err)
	}
	// Key 带纳秒后缀：测试重跑不撞上一轮的行（schema 本身已隔离，双保险）。
	suffix := time.Now().UnixNano()
	return &e2eEnv{
		dsn:      dsn,
		schema:   schema,
		key:      fmt.Sprintf("e2e-%d", suffix),
		resource: fmt.Sprintf("res-%d", suffix),
		ttl:      ttl,
		pool:     pool,
		eff:      NewEffectStore(pool),
		ds:       NewDownstream(pool),
	}
}

// action 该环境的动作定义（父子进程必须构造**同一个**逻辑动作：同 Key 同资源）。
func (e *e2eEnv) action() sideeffect.Action {
	return sideeffect.Action{
		Key:         sideeffect.Key(e.key),
		Resource:    e.resource,
		Payload:     []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnknown,
	}
}

// childProc 一个被 re-exec 的子进程。
type childProc struct {
	cmd    *exec.Cmd
	lines  chan string
	closed chan struct{}
}

// startChild 起子进程：跑同一个测试二进制的子进程入口（指定模式）。
func (e *e2eEnv) startChild(t *testing.T, mode string) *childProc {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-test.run", "^TestPGRefChildProcess$",
		"-test.timeout", "90s")
	cmd.Env = append(os.Environ(),
		dsnEnv+"="+e.dsn,
		envSchema+"="+e.schema,
		envChildMode+"="+mode,
		envKey+"="+e.key,
		envResource+"="+e.resource,
		envTTLMS+"="+strconv.Itoa(int(e.ttl/time.Millisecond)),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("建子进程 stdout 管道失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起子进程失败（re-exec %s）: %v", os.Args[0], err)
	}
	c := &childProc{cmd: cmd, lines: make(chan string, 16), closed: make(chan struct{})}
	go func() {
		defer close(c.closed)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	// 兜底清理：用例失败路径上子进程可能还挂着——杀掉，不留孤儿。
	t.Cleanup(func() {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	})
	return c
}

// outcome 读取子进程的判定结论（阻塞到子进程退出）。
//
// 注意 select 的随机性：closed 与「已缓冲的行」同时就绪时可能先选中 closed
// ——必须把缓冲的行**排空**再下结论，否则会丢掉 OUTCOME 行（真红被误报成
// 「未报告判定结论」）。
func (c *childProc) outcome(t *testing.T) string {
	t.Helper()
	out := ""
	for {
		select {
		case line := <-c.lines:
			if len(line) > 8 && line[:8] == "OUTCOME=" {
				out = line[8:]
			}
		case <-c.closed:
			for {
				select {
				case line := <-c.lines:
					if len(line) > 8 && line[:8] == "OUTCOME=" {
						out = line[8:]
					}
				default:
					if out == "" {
						t.Fatalf("子进程未报告判定结论（OUTCOME= 缺失）")
					}
					return out
				}
			}
		}
	}
}

// kill SIGKILL：真实进程死亡，不是函数返回错误。
func (c *childProc) kill(t *testing.T) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL 子进程失败: %v", err)
	}
	_, _ = c.cmd.Process.Wait()
}

// waitFor 轮询到条件成立（超时即红——故障注入用例的等待不许静默降级）。
func waitFor(t *testing.T, timeout time.Duration, what string, probe func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := probe()
		if err == nil && ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时（%v）", what, timeout)
}

// effectCalls 读效果体调用次数。
func (e *e2eEnv) effectCalls(ctx context.Context) (int, error) {
	var calls int
	err := e.pool.QueryRow(ctx,
		`SELECT calls FROM pgref_e2e_calls WHERE key = $1`, e.key).Scan(&calls)
	if err != nil {
		return 0, err
	}
	return calls, nil
}

// waitEffectCalls 等效果体被调用至少 n 次（子进程真的开始干活了）。
func (e *e2eEnv) waitEffectCalls(t *testing.T, n int) {
	t.Helper()
	waitFor(t, 20*time.Second, fmt.Sprintf("效果体被调用 %d 次", n), func() (bool, error) {
		calls, err := e.effectCalls(context.Background())
		return calls >= n, err
	})
}

// waitDownstream 等下游出现指定业务值（施加真的完成了）。
func (e *e2eEnv) waitDownstream(t *testing.T, value string) {
	t.Helper()
	waitFor(t, 20*time.Second, "下游出现施加标记", func() (bool, error) {
		got, err := e.ds.Marker(context.Background(), e.resource)
		return got == value, err
	})
}

// waitTTLPass 等意图（与资源租约）过期：判据读的是库里的绝对时刻，
// 这里等的就是那次持有的自然到期，加 300ms 余量。
func (e *e2eEnv) waitTTLPass(t *testing.T) {
	t.Helper()
	var until *time.Time
	err := e.pool.QueryRow(context.Background(),
		`SELECT intent_expires FROM pgref_effect_action WHERE key = $1`, e.key).Scan(&until)
	if err != nil {
		t.Fatalf("读意图到期时刻失败: %v", err)
	}
	if until == nil {
		return
	}
	remain := time.Until(*until) + 300*time.Millisecond
	if remain > 0 {
		time.Sleep(remain)
	}
}

// requireUncertain 断言「重启后的进程」拿到的是需对账，而不是重新授予。
func requireUncertain(t *testing.T, runner *sideeffect.Runner, e *e2eEnv, callsBefore int) {
	t.Helper()
	outcome, _, err := runner.Execute(context.Background(), e.action(), func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		t.Fatal("结果未知的 Key 不该执行动作体（重放 = 判据失效）")
		return sideeffect.Result{}, nil
	})
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("SIGKILL + TTL 过期后同一 Key 必须转入需对账态（不得重新授予执行权），实得 %v", outcome)
	}
	if !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("需对账态应可判 ErrOutcomeUnknown，实得 %v", err)
	}
	calls, err := e.effectCalls(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != callsBefore {
		t.Fatalf("动作体不得被重放：调用次数应仍为 %d，实得 %d", callsBefore, calls)
	}
}
