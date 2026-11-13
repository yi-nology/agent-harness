// Package ticket 一次性短时授权票据（机制面）。
//
// 生命周期: Stable（v0.33.0 定级）——只允许 additive 变更（新增导出符号 /
// 新增方法且给零值默认实现 / 新增错误值）；改签名、改语义、改默认值、
// 删导出符号都算 breaking，判定规则与变更流程见 docs/api-compat.md，
// 逐包理由见 api-lifecycle.yaml。
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
// 状态在 Backend 接缝之后（issue #145）：缺省是进程内存储（重启即失效）——
// 对「只活几十秒的票据」这是**正确**的默认，宁可让用户重取一次，也不要把
// 凭据持久化到磁盘（落盘即多一份要擦的痕迹）。多副本宿主实现 Backend 经
// NewWithBackend 注入：判定逻辑（TTL / 单次消费 / 容量调用时机）留在本包，
// 跨进程原子性归存储——原子性契约见 backend.go 文件头。本包**不提供**
// 分布式实现，也不做任何集群语义（复制、选主、租约一概不认识）。
package ticket

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// Store 票据机制面：判定逻辑 + 状态接缝（Backend；进程内参考实现见 backend.go）。
//
// 零值不可用，用 New 构造。
type Store struct {
	backend Backend
	now     func() time.Time
}

// New 构造票据存储（进程内参考实现，容量上限取 DefaultMaxEntries）。
func New() *Store {
	return NewWithClock(DefaultMaxEntries, time.Now)
}

// NewWithClock 构造**进程内**票据存储并**注入时钟**。
//
// ⭐ 为什么要注入时钟：过期判定是本包的核心语义之一，若测试只能靠 sleep 等过期，
// 一则慢、二则把「等到了」当成了「判对了」——真把判定写反了，测试照样绿
// （只是慢一点）。注入时钟 ⇒ 过期语义可以被**确定性**地测。
//
// maxSize <= 0 ⇒ 取 DefaultMaxEntries。
func NewWithClock(maxSize int, now func() time.Time) *Store {
	return NewWithBackend(NewMemoryBackend(maxSize), now)
}

// NewWithBackend 构造票据存储：判定逻辑在本包，状态在注入的 Backend。
//
// 多副本宿主用它接自己的跨进程存储（DB / 协调后端）；Backend 的原子性契约
// 见 backend.go 文件头（Take 必须原子——多副本并发消费同一张票恰好一个赢）。
// b 为 nil ⇒ 退化为进程内参考实现（DefaultMaxEntries）。
func NewWithBackend(b Backend, now func() time.Time) *Store {
	if b == nil {
		b = NewMemoryBackend(DefaultMaxEntries)
	}
	if now == nil {
		now = time.Now
	}
	return &Store{backend: b, now: now}
}

// Issue 签发一张票据：返回**票据 id** 与**过期时刻**。
//
// ⭐ 过期时刻由**签发方给的 TTL** 决定（机制面不替宿主定策略）。
// 随机源失败 ⇒ 显式报错（不降级成「可预测的 id」——票据 id 一旦可预测，
// 单次消费语义就没有意义了）。
//
// 容量已满 ⇒ 显式报错（见 DefaultMaxEntries 的说明）。
//
// ⚠️ 绑定载荷**零校验**（含零值 Binding{}——机制面不解释字段语义）。
// 零绑定票据消费成功后返回全空 Binding，消费方拿到「授权成立」却答不出
// 「授权给谁、干什么、对什么对象」，审计面同样无从追溯。多数宿主应改用
// IssueStrict（签发前校验绑定完整性）；确有「字段语义宿主自定义、允许留空」
// 的场景再用本函数。
func (s *Store) Issue(b Binding, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("票据随机源不可用: %w", err)
	}
	id := hex.EncodeToString(raw)
	exp := s.now().Add(ttl)
	// 容量策略在 Backend 内自判（内存参考实现满了报「票据空间已满」）——
	// 判定时机（签发前拒）留在这里，判定依据（当前占用数）归存储。
	if err := s.backend.Put(id, b, exp); err != nil {
		return "", time.Time{}, err
	}
	return id, exp, nil
}

// IssueStrict 签发前校验**绑定完整性**：Binding 三字段（Subject/Purpose/
// Target）任一为空 ⇒ 显式报错，不签发。
//
// 为什么默认建议走这里：空绑定不是策略，是**绑定不完整**——宿主策略无论
// 如何都没法在一张「无主体/无用途/无目标」的票据上做判定，而消费侧拿到的
// 是「授权成立 + 全空载荷」的假安全感（转用/误签发无从追溯）。与包内既有
// 防线同哲学：随机源失败显式报错、容量满显式拒发，语义层面的非法值同样
// 不静默放行。字段语义仍归宿主解释，本函数只校验「非空」这一完整性底线。
func (s *Store) IssueStrict(b Binding, ttl time.Duration) (string, time.Time, error) {
	var missing []string
	if b.Subject == "" {
		missing = append(missing, "Subject")
	}
	if b.Purpose == "" {
		missing = append(missing, "Purpose")
	}
	if b.Target == "" {
		missing = append(missing, "Target")
	}
	if len(missing) > 0 {
		return "", time.Time{}, fmt.Errorf("票据绑定不完整，缺 %v（Subject/Purpose/Target 任一为空都拒发）", missing)
	}
	return s.Issue(b, ttl)
}

// Consume 消费一张票据：取出绑定载荷并**立即作废**。
//
// ⭐ 语义：未知 id ⇒ 失败；已过期 ⇒ 失败；已被消费过 ⇒ 失败（同一路径，
// 因为消费即删除，第二次根本找不到）。成功 ⇒ 返回绑定载荷。
//
// 过期与未知**不区分**（都返回 false）：区分开等于给「枚举 id」开了一扇
// 能区分「存在但过期」的窗，而票据 id 是 192 bit 随机，枚举本来就不可能——
// 但没必要的差别就不留。
//
// Backend 故障 ⇒ 按消费失败处理（fail-closed：票没被取走，宿主侧表现为
// 「这次没通过」，重试即可；绝不放行一张没验成的票）。本包入口没有 ctx
// 可传，跨进程实现的超时/截止由实现内部管理。
func (s *Store) Consume(id string) (Binding, bool) {
	if id == "" {
		return Binding{}, false
	}
	binding, ok, err := s.backend.Take(id, s.now())
	if err != nil {
		return Binding{}, false
	}
	return binding, ok
}

// GC 清掉过期票据，返回清理张数。
//
// 过期票据**已不可消费**（Consume 判过期即失败），但仍占着空间 ⇒ 需要有人
// 定期收。宿主可挂定时器调它，也可只在签发前顺手调。
// Backend 故障 ⇒ 返回 0（本轮没清成，下轮再试；清理是尽力面不是判定面）。
func (s *Store) GC() int {
	n, err := s.backend.GC(s.now())
	if err != nil {
		return 0
	}
	return n
}

// Len 当前未消费票据数（过期未清理的也算——它们只是不可消费，仍占空间）。
// Backend 故障 ⇒ 返回 0（观测面；容量判定在 Backend 内部，不依赖这里）。
func (s *Store) Len() int {
	n, err := s.backend.Len()
	if err != nil {
		return 0
	}
	return n
}
