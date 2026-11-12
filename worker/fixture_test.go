package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/pkg/errors"
)

// memRegistry 测试夹具：Registry 的进程内实现（真实现见 examples/worker-runtime）。
// 过期判定读可注入时钟——注册资格的判定归注册表侧（契约要求）。
type memRegistry struct {
	mu   sync.Mutex
	now  func() time.Time
	recs map[string]*regRec
}

type regRec struct {
	token     string
	labels    map[string]string
	expiresAt time.Time
}

func newMemRegistry(now func() time.Time) *memRegistry {
	return &memRegistry{now: now, recs: map[string]*regRec{}}
}

func (m *memRegistry) Register(_ context.Context, spec Spec) (Registration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := spec.Validate(); err != nil {
		return Registration{}, err
	}
	token := fmt.Sprintf("reg-%s-%d-%d", spec.ID, m.now().UnixNano(), suffix.Add(1))
	m.recs[spec.ID] = &regRec{token: token, labels: spec.Labels, expiresAt: m.now().Add(spec.TTL)}
	return Registration{ID: spec.ID, Token: token, ExpiresAt: m.recs[spec.ID].expiresAt}, nil
}

func (m *memRegistry) Heartbeat(_ context.Context, id, token string, ttl time.Duration) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	if !ok || r.token != token || !m.now().Before(r.expiresAt) {
		return time.Time{}, errors.Wrap(ErrNotRegistered, "worker: 心跳被拒（替身）", "id", id)
	}
	if ttl <= 0 {
		return time.Time{}, errors.Wrap(ErrInvalidConfig, "worker: 心跳 TTL 必须为正（替身）")
	}
	r.expiresAt = m.now().Add(ttl)
	return r.expiresAt, nil
}

func (m *memRegistry) Deregister(_ context.Context, id, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	if !ok || r.token != token {
		return errors.Wrap(ErrNotRegistered, "worker: 注销被拒（替身）", "id", id)
	}
	delete(m.recs, id)
	return nil
}

func (m *memRegistry) List(_ context.Context) ([]Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.recs))
	for id, r := range m.recs {
		out = append(out, Status{ID: id, Labels: r.labels, ExpiresAt: r.expiresAt, Alive: m.now().Before(r.expiresAt)})
	}
	return out, nil
}

// suffix 注册令牌的独立序号（原子——多驱动并发注册时不串号）。
var suffix atomic.Int64

// testClock 测试时钟：过期场景要能确定性触发，不靠 sleep 撞时序。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *testClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }
