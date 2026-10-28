// Package ticket 一次性短时授权票据（机制面）。
//
// # 这件事为什么要有个原语
//
// 宿主要把控制台、IM、长连接这类「先给个凭据、稍后凭凭据进来」的面暴露出去时，
// 绕不开三件事：**凭据短命**、**只能用一次**、**只能用于某一个用途**。
// 各宿主各写一套，于是每次都重犯同一种错：把**长期令牌塞进 query**——
// 令牌会落进访问日志、代理日志、浏览器历史、Referer 头，于是「短时凭据」
// 名义上短时，实际是长期有效且到处留痕。
//
// 本包把机制面收成一处：**签发（Issue）/ 消费（Consume）/ 过期（TTL）/ 用途绑定**。
//
// # 三条纪律
//
//  1. **单次消费**：票据消费即作废（用过即焚）。同一 id 第二次消费**必然失败**——
//     不是「尽量不重复」，是结构上不可能重复。
//
//  2. **过期即失效**：消费时刻晚于过期时刻 ⇒ 失败。过期判定与消费判定**同一处**
//     （不要两处各判一次，两处判就会有一处漂）。
//
//  3. **机制与策略分离**：本包**不决定**谁能签、票据能干什么、过期多久——
//     那是宿主的治理决策。本包只提供机制：宿主拿到 id 与过期时刻，自己决定
//     拿它做什么。绑定载荷（Binding）里的字段语义也归宿主解释。
//
// # 边界
//
// 进程内存储：重启即失效。对「只活几十秒的票据」这是**正确**的默认——
// 宁可让用户重取一次，也不要把凭据持久化到磁盘（落盘即多一份要擦的痕迹）。
// 需要跨实例共享的宿主，应自己在前面加一层共享存储，本包不提供分布式语义。
package ticket

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// DefaultMaxEntries 默认容量上限：票据空间有界，超出即**拒发**（报错）。
//
// ⭐ 为什么要有上限：票据是「发了就占一份内存」的凭据，若宿主无限制签发，
// 一次误用的批量签发就能把进程内存吃干。本包的立场是**满了就说满**，
// 而不是默默淘汰（默默淘汰会让宿主以为票据还在，实际已失效 ⇒ 表现为
// 「偶发地连不上」，极难查）。
const DefaultMaxEntries = 1024

// idBytes 票据 id 的随机字节数（24 字节 = 192 bit，hex 后 48 字符）。
const idBytes = 24

// Binding 票据携带的**用途绑定**（字段语义由宿主解释）。
//
// 本包只负责**原样携带与原样取回**，不解释、不校验、不推断。
type Binding struct {
	// Subject 主体标识（宿主自解释，例如某个身份）。
	Subject string
	// Purpose 用途标识（宿主自解释，例如某个端点或某个能力）。
	Purpose string
	// Target 目标标识（宿主自解释，例如某个会话或某个对象的引用）。
	Target string
}

// entry 一张未消费的票据。
type entry struct {
	binding   Binding
	expiresAt time.Time
}

// Store 票据存储（进程内；并发安全）。
//
// 零值不可用，用 New 构造。
type Store struct {
	mu      sync.Mutex
	m       map[string]entry
	now     func() time.Time
	maxSize int
}

// New 构造票据存储（容量上限取 DefaultMaxEntries）。
func New() *Store {
	return NewWithClock(DefaultMaxEntries, time.Now)
}

// NewWithClock 构造票据存储并**注入时钟**。
//
// ⭐ 为什么要注入时钟：过期判定是本包的核心语义之一，若测试只能靠 sleep 等过期，
// 一则慢、二则把「等到了」当成了「判对了」——真把判定写反了，测试照样绿
// （只是慢一点）。注入时钟 ⇒ 过期语义可以被**确定性**地测。
//
// maxSize <= 0 ⇒ 取 DefaultMaxEntries。
func NewWithClock(maxSize int, now func() time.Time) *Store {
	if maxSize <= 0 {
		maxSize = DefaultMaxEntries
	}
	if now == nil {
		now = time.Now
	}
	return &Store{m: map[string]entry{}, now: now, maxSize: maxSize}
}

// Issue 签发一张票据：返回**票据 id** 与**过期时刻**。
//
// ⭐ 过期时刻由**签发方给的 TTL** 决定（机制面不替宿主定策略）。
// 随机源失败 ⇒ 显式报错（不降级成「可预测的 id」——票据 id 一旦可预测，
// 单次消费语义就没有意义了）。
//
// 容量已满 ⇒ 显式报错（见 DefaultMaxEntries 的说明）。
func (s *Store) Issue(b Binding, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("票据随机源不可用: %w", err)
	}
	id := hex.EncodeToString(raw)
	exp := s.now().Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= s.maxSize {
		return "", time.Time{}, fmt.Errorf("票据空间已满（上限 %d 张）", s.maxSize)
	}
	s.m[id] = entry{binding: b, expiresAt: exp}
	return id, exp, nil
}

// Consume 消费一张票据：取出绑定载荷并**立即作废**。
//
// ⭐ 语义：未知 id ⇒ 失败；已过期 ⇒ 失败；已被消费过 ⇒ 失败（同一路径，
// 因为消费即删除，第二次根本找不到）。成功 ⇒ 返回绑定载荷。
//
// 过期与未知**不区分**（都返回 false）：区分开等于给「枚举 id」开了一扇
// 能区分「存在但过期」的窗，而票据 id 是 192 bit 随机，枚举本来就不可能——
// 但没必要的差别就不留。
func (s *Store) Consume(id string) (Binding, bool) {
	if id == "" {
		return Binding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tk, ok := s.m[id]
	if !ok {
		return Binding{}, false
	}
	delete(s.m, id)
	if s.now().After(tk.expiresAt) {
		return Binding{}, false
	}
	return tk.binding, true
}

// GC 清掉过期票据，返回清理张数。
//
// 过期票据**已不可消费**（Consume 判过期即失败），但仍占着内存 ⇒ 需要有人定期收。
// 宿主可挂定时器调它，也可只在签发前顺手调。
func (s *Store) GC() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for id, tk := range s.m {
		if now.After(tk.expiresAt) {
			delete(s.m, id)
			n++
		}
	}
	return n
}

// Len 当前未消费票据数（过期未清理的也算——它们只是不可消费，仍占空间）。
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
