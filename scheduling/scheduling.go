// Package scheduling Agent 调度的**判据面**：优先级、公平份额、资源配额、背压。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 归属结论（ADR-0008，结论写证据不写感觉）
//
// 本仓的 @dispatch@ 是上游 @oagentkit/agent/dispatch@ 的**门面**，它只判
// 三件事：allow 矩阵可达性、派发深度上限、自派发拒绝（证据：上游
// dispatch.go 的全部判定面就是 Guard.Assert 的三条分支与 DenyError.Reason
// 的三个常量，见 docs/adr/ADR-0008-调度判据归属.md 的「证据」节）。
// 上游**没有**优先级、公平性、配额、背压——这四样是上游确实不提供的部分，
// 按问题域落在本包：本包只给**判据**（纯函数面），不实现队列、不做派发执行。
//
// # 与 dispatch 的分工（本仓不出现与上游语义重复的实现）
//
//   - dispatch（门面）：这次派发**能不能**发生——可达性 + 深度 + 自派发；
//   - scheduling（本包）：派发请求**按什么次序、谁多谁少、满了怎么办**——
//     优先级排序、加权公平份额、配额账本、背压裁决。
//
// 本包不 import dispatch，也不重复实现可达性/深度判定：两组语义正交，
// 宿主把它们**串起来**用（先过 dispatch 的可达性，再进 scheduling 的排序
// 与配额）。哪天上游把四样里的某样收编了，对应的判据应当转门面而不是
// 并存第二份（AGENTS.md 的同一条纪律）。
//
// # 为什么全是纯函数 / 确定性状态机
//
// 调度判据一旦不确定，编排就不可复现：同一批请求两次调度给出不同次序，
// 宿主没法解释「为什么它先跑」。故本包两条硬口径：
//
//  1. **确定性**：平局必须有名字层裁决（不依赖 map 顺序、不依赖时钟）；
//  2. **裁决显式**：拒绝必须带机器可判的原因（Reason），不许「尽力而为」。
//
// # 不做什么
//
// 不做队列本体、不做 goroutine 池、不做执行（那是宿主与上游的活）；
// 不判可达性（dispatch 的活）；不做动态权重调整（宿主按需重建 Picker
// ——权重变更走「换整份」而不是改其中一条，与 guardset.Assembly 同一条纪律）。
package scheduling

import "github.com/oekit/pkg/errors"

// ErrInvalidConfig 配置非法（权重非正、配额上限为负、背压阈值为负等）。
//
// 装配期就要红：调度参数写错（权重 0、负上限）会让判据在运行期给出
// 无意义的结论（全部拒绝或全部放行），静默运行比拒绝启动更贵。
var ErrInvalidConfig = errors.New("scheduling: 配置非法")

// ErrQuotaExhausted 配额已用尽：本次占用被拒（可等释放后再试）。
//
// 这是**可重试**的拒绝——别人 Release 之后同一调用可能成功。把它当
// 永久失败处理会让动作永远丢失。
var ErrQuotaExhausted = errors.New("scheduling: 配额已用尽")

// ErrOverRelease 释放量超过已占用量：账不平。
//
// 释放超过占用说明调用方重复释放或释放了别人的额度——放行会把配额账本
// 变成负数，后续占用全部「看起来有余额」。拒绝并让调用方修调用点。
var ErrOverRelease = errors.New("scheduling: 释放超过已占用")
