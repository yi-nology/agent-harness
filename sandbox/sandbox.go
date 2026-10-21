// Package sandbox 执行沙箱的档位语义与后端协议（门面）。
//
// 核心已下沉 `github.com/oagentkit/guardx/sandbox`：隔离档位与执行后端的协议
// 是通用的执行环境原语，与编排无关，故归 guardx（tier 0 安全层）。本包只做
// 一件事——保住调用路径稳定，宿主与本仓 `ptc` 的 `sandbox.Level` /
// `sandbox.Provider` / `sandbox.Registry` 调用点一行不动。
//
// # 为什么档位语义归上游而不是留在本仓
//
// 本仓 invoke 包的立论之一是「信任边界移动了，沙箱没跟着移动」——而它指出的
// 正是缺少一个**可声明的档位概念**：此前只有「有没有沙箱」的二元说法，没有
// 「够不够强」的说法，于是「隔离降级了」这类事故无法在装配期被发现，只能靠
// 事后审计。档位语义与判定语义同族（都是「要求 vs 实际」的对照），故与
// invoke / permgate 同处安全层。
//
// # 核心语义（在上游，此处只转发）
//
//   - **选不到满足档位的后端即拒绝，绝不降级**：降级会让「宿主声明的强度」与
//     「实际执行的强度」之间没有信号，档位整套机制就成了装饰。
//   - **档位序即强弱**：要求档位是下限，满足下限者取最弱（够用且最省）。
//   - **档位是后端自报的，上游不校验其真实性**：宿主若不信任某后端，唯一的
//     办法是不登记它。
//
// 本包不认识任何领域概念，也不替宿主决定档位要求——要求是宿主按自己的风险面
// 声明的（那是判断，不是机制）。
package sandbox

import (
	"context"

	upstream "github.com/oagentkit/guardx/sandbox"
)

// 类型别名：编译期同一类型，故本仓 `ptc` 的 Provider 适配器与宿主的实现
// 继续按同一套接口工作——**API 逐字兼容**。
type (
	// Level 隔离档位（序即强弱）。
	Level = upstream.Level
	// Provider 执行后端协议。
	Provider = upstream.Provider
	// Request 一次执行请求。
	Request = upstream.Request
	// Budget 资源预算：墙钟 / 调用次数 / 输出字节数。
	Budget = upstream.Budget
	// Outcome 一次执行的结果。
	Outcome = upstream.Outcome
	// Failure 执行失败：Kind 是分类串，Message 是可读原因。
	Failure = upstream.Failure
	// Registry 后端登记簿。
	Registry = upstream.Registry
	// Capable 可选能力面：后端能否兑现某组预算。
	//
	// **不实现 Capable 的后端只兑现基本三闸、不兑现内存**——漏声明得到的是
	// 「兜不住」，不是「兜得住」。方向与安全语义一致。
	Capable = upstream.Capable
	// CapableFunc 把普通函数适配成 Capable。
	CapableFunc = upstream.CapableFunc
)

// 档位常量别名。
const (
	LevelNone    = upstream.LevelNone
	LevelProcess = upstream.LevelProcess
	LevelOS      = upstream.LevelOS
	LevelMachine = upstream.LevelMachine
)

// 哨兵错误别名。
var (
	ErrNoProvider        = upstream.ErrNoProvider
	ErrDuplicateProvider = upstream.ErrDuplicateProvider
)

// NewRegistry 空登记簿。
func NewRegistry() *Registry { return upstream.NewRegistry() }

// ParseLevel 解析档位名；未知档位即报错，不退回某个缺省档位。
func ParseLevel(s string) (Level, error) { return upstream.ParseLevel(s) }

// Execute 按要求档位选后端并执行一次程序。
//
// 预算要求从 req.Budget 取——它**同时用于选后端**：否则「要求了内存兜底而后端
// 兜不住」这件事就只在执行期暴露，而选后端这一步本可以拦下它。
func Execute(ctx context.Context, reg *Registry, min Level, req Request) (Outcome, error) {
	return upstream.Execute(ctx, reg, min, req)
}

// SupportsBasic 是一个 Capable：只兑现基本三闸（墙钟 / 调用数 / 输出字节），
// **不兑现内存**。
//
// 进程内解释器后端用它——Go 运行时没有「只给一个 VM 限内存」的开关，
// 解释器本身也没有内存限制 API，故这类后端**如实**只能兑现三闸。
func SupportsBasic(b Budget) bool { return upstream.SupportsBasic(b) }

// SupportsAll 是一个 Capable：兑现全部预算项（含内存）。
//
// 只有**真能**兜住内存的后端（子进程 + 内核 rlimit 之类）才能用它。
func SupportsAll(b Budget) bool { return upstream.SupportsAll(b) }
