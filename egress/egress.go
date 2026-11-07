// Package egress 出口策略的**声明面与兑现判据**（机制面）。
//
// # 它补的是哪个洞
//
// 本仓的 netguard 判定挂在 Go 的 http.Client / Transport 上，覆盖面是
// 「本进程内走 netguard 的 HTTP 出站」。而工具执行的两类主流形态都在它之外：
//
//   - 子进程（ptc 的子进程 Provider 用 exec.Command 重新拉起自己）；
//   - 宿主自己起的命令行工具（curl / git / 各种 SDK 的 native 部分）。
//
// 于是「声明了受限出口」在那一侧**没有任何东西兑现**：策略写得很严，实际流量
// 自由出网。这与本仓反复强调的「声明强度 ≠ 实际强度」是同一条纪律的又一次实例。
//
// # 本包给什么、不给什么
//
// **给**：① 出口策略的**词表**（三档，可比较强弱）；② 子进程环境的**生成**；
// ③ 「声明 vs 实际」的**判据**（Verify）；④ 后端能力声明接口（Enforcer）——
// 词表与契约的真相在上游 guardx/sandbox，本包只做 alias（理由见 Mode 的注释）。
//
// **不给**：代理本体与网络强制实现——那是执行环境的事（上游安全层 / 宿主）。
// 本包只在**声明面**与**判据面**上保证一致，不造第二套网络栈。
//
// # 三档为什么这样排序
//
//	ModeUnrestricted(0) < ModeProxy(1) < ModeDenied(2)
//
// 「禁网」严格强于「经代理」：禁网的后端天然满足「经代理」的要求（没有流量能出），
// 反之不成立。排序让「后端能力 ≥ 要求」可以用一个整数比较表达，不需要枚举矩阵。
//
// # 为什么环境变量这条路对「禁网」不成立
//
// 环境变量只是**建议**：HTTP_PROXY 可以被子进程忽略，静态链接的二进制、
// UDP、原始 socket 都绕得过去。故 ModeDenied 的 Env 与 Verify **直接报错**，
// 而不是伪造一组「看起来禁了」的变量——伪造成功比拒绝更危险：它会让
// 「声明了禁网」在报告里显示为已兑现。
package egress

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/slicex"

	"github.com/yi-nology/agent-harness/sandbox"
)

// 档位词表与能力声明契约**不在本包定义**，而是 type alias 指向上游
// guardx/sandbox（本仓 sandbox 门面同源）：
//
//	type Mode = sandbox.EgressMode
//	type Enforcer = sandbox.EgressEnforcer
//
// # 为什么必须这样（这不是风格选择）
//
// 本包的 Spec.SatisfiedBy 要问一个后端「你能兑现哪一档」，而 Go 的接口一致性
// 要求**返回值类型同一**。词表曾经就定义在本包，于是上游任何执行后端若要实现
// Enforcer，都得 import 本仓——而本仓依赖 guardx，成环。后果不是「写起来别扭」，
// 是**那条接口在结构上只可能由宿主实现**：上游提供一个能兑现的后端这条路被类型
// 系统堵死，且编译期一声不响（不报错，只是永远没人能满足它）。
//
// 实测代价：一整条出口声明链（档位 / 自检 / env 生成 / Verify / 能力问询）全齐，
// 却没有任何后端能声明「我能兑现」——声明面看起来完整，兑现面是空的。
//
// 故词表下沉到 tier 0 的 guardx/sandbox，与 Level 同层同理：跨模块共享的**词表**
// 必须低于所有实现方。本包保留同名 alias，调用点一行不动。
type (
	// Mode 出口策略档位（值越大越严）。
	Mode = sandbox.EgressMode
	// Enforcer 后端能力声明：它能**强制**哪一档出站。
	//
	// 未实现它的后端一律判不满足（见 SatisfiedBy）：它没有做出任何声明，而
	// 「没声明」不等于「不受限」——安全判据里沉默必须按最弱处理。
	Enforcer = sandbox.EgressEnforcer
)

const (
	// ModeUnrestricted 不要求（默认；零值即此档，故旧调用点行为不变）。
	ModeUnrestricted = sandbox.EgressUnrestricted
	// ModeProxy 要求出站经代理（可配合 Allow 做目标白名单）。
	ModeProxy = sandbox.EgressProxy
	// ModeDenied 要求完全禁网（只能由执行后端强制，环境变量做不到）。
	ModeDenied = sandbox.EgressDenied
)

// Spec 一次执行对出口策略的要求。
type Spec struct {
	// Mode 要求的档位。
	Mode Mode
	// Proxy 代理地址（ModeProxy 时必填），形如 "http://127.0.0.1:3128"。
	Proxy string
	// Allow 允许访问的目标（由**代理**判定，本包只原样携带给兑现者）。
	//
	// 刻意**不**写进 NO_PROXY：NO_PROXY 的语义是「绕过代理直连」，与「允许经
	// 代理访问」正好相反。写进去会让允许名单变成豁免名单——代理的拒绝名单、端口
	// 白名单、只读档对它全部失效，而报告里那条声明仍然显示「已兑现」。需要「不经
	// 代理」的目标请写 Bypass，别借 Allow 表达相反的语义。
	Allow []string
	// Bypass 明确**不经代理直连**的目标（写进 NO_PROXY / no_proxy）。
	//
	// 默认空：整条变量都不出现。空值 "NO_PROXY=" 在部分客户端里会被读成「所有
	// 主机都不经代理」，那是比不写更危险的一种形式。
	Bypass []string
}

// Validate 自检声明本身是否可兑现（配置写错即报错，不静默降级）。
func (s Spec) Validate() error {
	if !s.Mode.Valid() {
		return errors.Errorf("egress: 未知档位 %d", int(s.Mode))
	}
	if s.Mode != ModeProxy {
		return nil
	}
	if strings.TrimSpace(s.Proxy) == "" {
		return errors.New("egress: 要求经代理但没给代理地址（这条声明兑现不了）")
	}
	if !strings.Contains(s.Proxy, "://") {
		return errors.Errorf("egress: 代理地址 %q 缺少 scheme（形如 http://host:port）", s.Proxy)
	}
	return nil
}

// proxyVars 是代理类环境变量的完整清单（大小写各一份：不同运行时的读法不同）。
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}

// Env 生成子进程应携带的环境（在 base 之上叠加）。
//
// 为什么必须**通过环境显式下发**而不是靠子进程自觉：这是「声明 → 兑现」
// 的唯一可判定通道——Verify 判的就是这份 env。
func (s Spec) Env(base []string) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch s.Mode {
	case ModeUnrestricted:
		return base, nil
	case ModeDenied:
		// 不伪造「看起来禁网」的变量：环境变量拦不住静态二进制与原始 socket。
		return nil, errors.New("egress: 禁网无法用环境变量兑现，必须由执行后端强制（见 Enforcer）")
	}
	out := make([]string, 0, len(base)+len(proxyVars)+1)
	for _, kv := range base {
		if isProxyVar(kv) {
			continue // 旧值一律覆盖，避免「声明换了、env 还是旧的」
		}
		out = append(out, kv)
	}
	for _, k := range proxyVars {
		out = append(out, k+"="+s.Proxy)
	}
	if len(s.Bypass) > 0 {
		bypass := append([]string(nil), s.Bypass...)
		sort.Strings(bypass) // 确定性：同一份声明必须产出同一份 env
		out = append(out, "NO_PROXY="+strings.Join(bypass, ","))
		out = append(out, "no_proxy="+strings.Join(bypass, ","))
	}
	return out, nil
}

// Verify 判据：实际下发给子进程的 env 是否真的兑现了声明。
//
// 这是本包存在的理由——**声明与实际的对照**。env 由调用方在真正 spawn 之前取到，
// 判不过即不执行（而不是执行完再补一句「好像没生效」）。
func (s Spec) Verify(env map[string]string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	switch s.Mode {
	case ModeUnrestricted:
		return nil
	case ModeDenied:
		return errors.New("egress: 禁网无法从环境变量验证（必须由带能力声明的后端强制）")
	}
	if len(s.Bypass) > 0 {
		// 判**语义等价**而不是字面相同：NO_PROXY 的顺序与空白无语义，
		// 判字面会把自己拼 env 的调用方误判红（那是假红——判据的前提是
		// 「兑现了没有」，不是「是不是用本包的 Env 生成的」）。
		want := normList(s.Bypass)
		got := normList(strings.Split(env["NO_PROXY"], ","))
		if !slices.Equal(want, got) {
			return errors.Errorf("egress: 绕过名单未兑现（要求 %v，实际 %q）", want, env["NO_PROXY"])
		}
	}
	var seen []string
	for _, k := range proxyVars {
		if v, ok := env[k]; ok && v == s.Proxy {
			return nil
		}
		if v, ok := env[k]; ok {
			seen = append(seen, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return errors.Errorf("egress: 子进程环境未兑现代理声明（要求 %s，实际 %v）", s.Proxy, seen)
}

// normList 折成「去空白、去空条目、排序」的形态（比较目标清单前先归一）。
func normList(items []string) []string {
	out := slicex.Filter(slicex.Map(items, strings.TrimSpace), func(x string) bool { return x != "" })
	sort.Strings(out)
	return out
}

func isProxyVar(kv string) bool {
	i := strings.IndexByte(kv, '=')
	if i <= 0 {
		return false
	}
	name := kv[:i]
	for _, v := range proxyVars {
		if name == v {
			return true
		}
	}
	return name == "NO_PROXY" || name == "no_proxy"
}

// SatisfiedBy 判一个后端能否兑现本声明。
//
// 未实现 Enforcer 的后端一律判**不满足**：它没有做出任何声明，
// 而「没声明」不等于「不受限」——在安全判据里，沉默必须按最弱处理。
func (s Spec) SatisfiedBy(provider any) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Mode == ModeUnrestricted {
		return nil
	}
	enf, ok := provider.(Enforcer)
	if !ok {
		return errors.Errorf("egress: 后端未声明出口能力，无法兑现 %s（沉默按最弱处理）", s.Mode)
	}
	got := enf.Egress()
	if !got.Satisfies(s.Mode) {
		return errors.Errorf("egress: 后端只能兑现 %s，低于要求的 %s", got, s.Mode)
	}
	return nil
}
