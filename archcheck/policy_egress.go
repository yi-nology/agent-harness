// policy_egress.go —— egress_paths 段的声明面类型。
//
// 单列一份是因为 policy.go 已经贴着 max_file_lines：把「声明长什么样」与
// 「其余各段的声明」拆开，改动面也更清楚（这一段与 execution_paths 同族，
// 判据在 egresspaths.go）。
package archcheck

// EgressEntry 是 egress_paths 段里的一条**子进程启动点**登记。
//
// 定位粒度是「文件 + 函数」：按包名太宽（一个包里可能既有 spawn 也有不 spawn 的
// 函数），按行号太脆（改一行注释就漂）。函数搬走或改名，登记就该跟着改，
// 改漏了判据会报陈旧。
type EgressEntry struct {
	// Path 启动点所在文件（相对仓根，正斜杠）。
	Path string `yaml:"path"`
	// Fn 含启动调用的函数名（空 = 该文件内全部启动点）。
	Fn string `yaml:"fn"`
	// Mode 该启动点的出口档位：unrestricted / proxy / denied（缺省 unrestricted，
	// 但**仍须显式登记**——「没登记」不等于「不要求」）。
	Mode string `yaml:"mode"`
	// Plumbing 兑现证据的函数名（缺省 Env / Verify）——「这一档落成 env」那一半。
	Plumbing []string `yaml:"plumbing"`
	// Capability 能力问询证据的函数名（缺省 SatisfiedBy）——「这一档有人强制」那一半。
	//
	// 两半分开可配而不是并成一个列表：它们是**两条**判据，报错要说清缺哪一半
	// （加 Env 与加后端是两种完全不同的修法）。
	Capability []string `yaml:"capability"`
	// Notes 为什么是这个档（写给人看的那一句）。
	Notes string `yaml:"notes"`
}

// EgressPaths 是 egress_paths 段的配置：开关、级别、受忽略目录与启动点清单。
//
// 形态与 execution_paths 段同构：都是「声明面 + 代码事实」的一致性判据。
// 差别在于执行路径门禁有**内置表**（记本仓自己的 API 事实），而这一段没有：
// 子进程启动点是每个仓自己的事实，本仓没有资格替宿主登记。
type EgressPaths struct {
	Enabled    *bool         `yaml:"enabled"`
	Severity   string        `yaml:"severity"`
	Desc       string        `yaml:"desc"`
	IgnoreDirs []string      `yaml:"ignore_dirs"`
	Entries    []EgressEntry `yaml:"entries"`
}

// on 报告子进程出口门禁是否启用（缺省启用）。
func (e EgressPaths) on() bool { return e.Enabled == nil || *e.Enabled }
