// registry.go —— worker.Registry 的参考实现（内存版，**单进程演示用**）。
//
// 原子性来自进程内互斥锁，只在单进程内成立；真实宿主把同一套判据落到
// 跨进程存储上（Redis SET NX PX / PostgreSQL 条件 UPDATE / etcd txn）。
// 心跳的「资格校验 + 推后有效期」必须是一次原子操作——这是 Registry
// 契约的硬要求，拆成「查-判-写」三步，顶替者的心跳就会延长别人的注册。
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/worker"
)

// memRegistry worker 注册表的内存实现（演示与端到端用例）。
type memRegistry struct {
	mu   sync.Mutex
	now  func() time.Time
	recs map[string]*regRec
}

// regRec 一条注册记录（令牌由注册表铸造——凭据出自判据面）。
type regRec struct {
	token     string
	labels    map[string]string
	expiresAt time.Time
}

func newMemRegistry(now func() time.Time) *memRegistry {
	return &memRegistry{now: now, recs: map[string]*regRec{}}
}

// Register 注册（同 ID 顶替 + 铸新令牌：崩溃重启是正常路径，
// 先到者的心跳从此被拒——它由此得知自己已被顶替）。
func (m *memRegistry) Register(_ context.Context, spec worker.Spec) (worker.Registration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := spec.Validate(); err != nil {
		return worker.Registration{}, err
	}
	token := fmt.Sprintf("reg-%s-%d", spec.ID, m.now().UnixNano())
	m.recs[spec.ID] = &regRec{token: token, labels: spec.Labels, expiresAt: m.now().Add(spec.TTL)}
	return worker.Registration{ID: spec.ID, Token: token, ExpiresAt: m.recs[spec.ID].expiresAt}, nil
}

// Heartbeat 心跳 = 资格检查 + 延长有效期（同一把锁内完成——原子）。
func (m *memRegistry) Heartbeat(_ context.Context, id, token string, ttl time.Duration) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	if !ok || r.token != token || !m.now().Before(r.expiresAt) {
		return time.Time{}, errors.Wrap(worker.ErrNotRegistered, "注册已失效或令牌不匹配", "id", id)
	}
	if ttl <= 0 {
		return time.Time{}, errors.Wrap(worker.ErrInvalidConfig, "心跳 TTL 必须为正")
	}
	r.expiresAt = m.now().Add(ttl)
	return r.expiresAt, nil
}

// Deregister 注销（凭令牌）。
func (m *memRegistry) Deregister(_ context.Context, id, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[id]
	if !ok || r.token != token {
		return errors.Wrap(worker.ErrNotRegistered, "注销被拒：注册已失效或令牌不匹配", "id", id)
	}
	delete(m.recs, id)
	return nil
}

// List 全量快照（Alive 用注册表自己的时钟判——判据归存储侧）。
func (m *memRegistry) List(_ context.Context) ([]worker.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]worker.Status, 0, len(m.recs))
	for id, r := range m.recs {
		out = append(out, worker.Status{
			ID: id, Labels: r.labels, ExpiresAt: r.expiresAt,
			Alive: m.now().Before(r.expiresAt),
		})
	}
	return out, nil
}
