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
	"strings"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/slicex"

	"github.com/yi-nology/goagent-kernel/sandbox"
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
		slices.Sort(bypass) // 确定性：同一份声明必须产出同一份 env
		out = append(out, "NO_PROXY="+strings.Join(bypass, ","))
		out = append(out, "no_proxy="+strings.Join(bypass, ","))
	}
	return out, nil
}

// Verify 判据：实际下发给子进程的 env 是否真的兑现了声明。
//
// 这是本包存在的理由——**声明与实际的对照**。env 由调用方在真正 spawn 之前取到，
// 判不过即不执行（而不是执行完再补一句「好像没生效」）。
//
// 两项判据（绕过名单 / 代理变量）各只有一个实现、各覆盖全部大小写变体——
// 「同一机制两份实现必然漂移」的教训就来自这里：绕过名单曾按 Bypass 空不空
// 走两份只有大小写口径不同的实现，小写 no_proxy=* 从只查大写的那份底下绕过
// （issue #63）；代理变量曾判「六个里命中一个就算兑现」，只配了 HTTP_PROXY
// 的环境验成 enforced 而 HTTPS 裸奔（issue #62）。
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
	if err := s.verifyBypass(env); err != nil {
		return err
	}
	return s.verifyProxyVars(env)
}

// bypassVars 绕过名单变量的两个大小写变体：客户端读哪个不可知（issue #42 起
// 空名单分支就在同查两个变体），任何只查一个变体的判据都留了另一条绕过道。
var bypassVars = []string{"NO_PROXY", "no_proxy"}

// verifyBypass 绕过名单判据（issue #63 收口为**单实现**）：对两个大小写变体
// 取**并集**后判与声明的语义等价——
//
//   - 两个变体都缺席：声明了绕过 ⇒ 红（名单没兑现）；声明无绕过 ⇒ 绿；
//   - 任一变体在场：并集必须与声明语义等价（顺序与空白无语义——判字面会把
//     自己拼 env 的调用方误判红，那是假红），否则红并点名变体与实际值。
//
// 并集口径同时封住两个方向的洞：大写正确而小写携带 `*`（并集多出 `*` ⇒ 红，
// 此前只比大写验成绿——小写读者对所有目标直连）；只有小写且正确（并集=声明
// ⇒ 绿，此前只读大写 env["NO_PROXY"] 判空误红）。空串变体折成空集（视同缺席，
// 与 Env 生成的「整条不出现」形态兼容）。Bypass 为空时并集须为空集，即 #42
// 的「无绕过声明不得携带 NO_PROXY」——同一份实现，不再两份口径各查一半。
func (s Spec) verifyBypass(env map[string]string) error {
	want := normList(s.Bypass)
	var present, union []string
	for _, k := range bypassVars {
		v, ok := env[k]
		if !ok {
			continue
		}
		present = append(present, fmt.Sprintf("%s=%q", k, v))
		union = append(union, strings.Split(v, ",")...)
	}
	if slices.Equal(want, normList(union)) {
		return nil
	}
	if len(want) == 0 {
		return errors.Errorf("egress: 声明无绕过但环境携带 %s——代理将被绕过架空", strings.Join(present, "、"))
	}
	if len(present) == 0 {
		return errors.Errorf("egress: 绕过名单未兑现（要求 %v，实际 NO_PROXY/no_proxy 两个变体都缺席）", want)
	}
	return errors.Errorf("egress: 绕过名单未兑现（要求 %v，实际 %v）", want, present)
}

// verifyProxyVars 代理变量判据（issue #62）：六个变量**一个不缺、一个不歪**。
//
// Env 自己写的就是全部六个（proxyVars），而客户端各读各的——curl 读
// http_proxy、多数 SDK 读 HTTPS_PROXY、Go 的 ProxyFromEnvironment 小写优先。
// 于是「缺一个」意味着某一类流量根本没代理（HTTPS 裸奔），「指到别处」意味着
// 流量走了一条声明从未授权的链路（HTTP_PROXY 对、HTTPS_PROXY 指向别的代理）。
// 两种形态都点名列出：缺失与异值的处置不同（补变量 vs 查污染源），合成一句
// 「实际 [...]」会让排障的人对着正确的变量找错。
func (s Spec) verifyProxyVars(env map[string]string) error {
	var missing, wrong []string
	for _, k := range proxyVars {
		v, ok := env[k]
		switch {
		case !ok:
			missing = append(missing, k)
		case v != s.Proxy:
			wrong = append(wrong, fmt.Sprintf("%s=%s", k, v))
		}
	}
	if len(missing) == 0 && len(wrong) == 0 {
		return nil
	}
	return errors.Errorf("egress: 子进程环境未兑现代理声明（要求 %s；缺失 %v；异值 %v）", s.Proxy, missing, wrong)
}

// normList 折成「去空白、去空条目、排序、去重」的形态（比较目标清单前先归一）。
//
// 去重是并集判据（issue #63）的前提：同一目标出现在两个大小写变体里时，
// 折半后的清单须与单次声明的清单等价，重复项会把等价判成失配。
func normList(items []string) []string {
	out := slicex.Filter(slicex.Map(items, strings.TrimSpace), func(x string) bool { return x != "" })
	slices.Sort(out)
	return slices.Compact(out)
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
