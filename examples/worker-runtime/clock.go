// clock.go —— 演示用的可注入时钟。
//
// 注册与租约的过期判定读它（时钟可注入 ⇒ 过期场景能被确定性测，
// 不靠 sleep 撞时序——sidetest / boardtest 同一条纪律）。
package main

import (
	"sync"
	"time"
)

// wallClock 演示时钟：真实时刻起步，测试里手动推进。
type wallClock struct {
	mu sync.Mutex
	t  time.Time
}

func newWallClock() *wallClock { return &wallClock{t: time.Now()} }

func (c *wallClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *wallClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }
