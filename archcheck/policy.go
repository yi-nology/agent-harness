// policy 文件的 schema：规则、阈值、例外登记的反序列化目标。
//
// 这一层是**可导入包**的第一道边界：宿主仓要用同一套判据，就得能读同一份
// policy 格式。故类型全部导出，且 policy 文件路径不写死成
// "architecture-policy.yaml"——宿主那份可能叫别的名字。

package archcheck

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// NegFollow 是负向前置的结构化表达：anchor 紧跟 forbid_follow 时，命中位置
// （被禁词）落在 forbid_follow 里即豁免。
//
// 为什么不写进正则：Go 原生只支持 RE2，没有后顾断言（(?!)）。
type NegFollow struct {
	Anchor       string `yaml:"anchor"`
	ForbidFollow string `yaml:"forbid_follow"`
}

// Rule 是一条正则式规则：匹配面由 scope/include/scope_exclude 圈定，判据由
// forbid_regex（源码文本）或 forbid_import_regex（import 路径）给出。
type Rule struct {
	ID           string      `yaml:"id"`
	Desc         string      `yaml:"desc"`
	Severity     string      `yaml:"severity"` // error（默认，违反即非零退出）| warn（报告口径，不阻断）
	Scope        []string    `yaml:"scope"`
	ScopeExclude []string    `yaml:"scope_exclude"`
	Include      []string    `yaml:"include"`
	IgnoreTest   bool        `yaml:"ignore_test"`
	OnlyTest     bool        `yaml:"only_test"` // 只扫测试文件（配合 severity: warn 做夹具体检）
	ForbidImport string      `yaml:"forbid_import_regex"`
	ForbidRegex  string      `yaml:"forbid_regex"`
	NegFollow    []NegFollow `yaml:"negative_follow"`
}

// Threshold 是结构债阈值：超长文件与 god object 的机械判据。
type Threshold struct {
	MaxFileLines      int `yaml:"max_file_lines"`
	MaxMethodsPerType int `yaml:"max_methods_per_type"`
	// ForwardingShellMaxChars 是「转发壳」的方法体长度上限（字符）。nil = 用缺省值。
	//
	// 转发壳 = 方法体只有一条转发语句、且剥掉注释与空白后长度不超过此值。
	// god object 阈值比的是**实质方法数**（方法数 − 转发壳数），因为方法总数在
	// façade 上等于**公共 API 表面规模**，不是复杂度集中度（issue #13）。
	//
	// 显式写 0 = 关闭该区分（所有方法都算实质方法），判定逐字退回旧行为。
	ForwardingShellMaxChars *int     `yaml:"forwarding_shell_max_chars"`
	ScanRoots               []string `yaml:"scan_roots"`
	Enforce                 bool     `yaml:"enforce"` // true=超限即非零退出；false=仅报告
}

// forwardingShellLimit 返回「转发壳」的长度上限。
//
// 未声明（nil）时用缺省值；显式 0 = 关闭该区分（此时没有方法会被算成壳，
// 因为任何含调用的方法体都长于 0——判定逐字退回旧行为）。
func (t Threshold) forwardingShellLimit() int {
	if t.ForwardingShellMaxChars == nil {
		return defaultForwardingShellMaxChars
	}
	return *t.ForwardingShellMaxChars
}

// Exception 是例外登记：精确豁免某条规则的某处违反。
//
// 豁免必须写 until（到期日）与 removal（拆除条件）——没有到期日的豁免等于永久豁免，
// 没有拆除条件的豁免是赖账。两者都由 policy 自校验强制。
type Exception struct {
	Rule      string `yaml:"rule"`
	Path      string `yaml:"path"`
	Line      int    `yaml:"line"`
	Violation string `yaml:"violation"`
	Reason    string `yaml:"reason"`
	Until     string `yaml:"until"`
	Removal   string `yaml:"removal"`
}

// facadeRuleID 门面包不可变规则的 ID。所有门面包共用一条规则 ID：它们守的是同一条
// 纪律（转发到上游、不得在本仓长实现），逐包差异体现在 violation 的 path 与 text 里。
const facadeRuleID = "harness-facade-stays-facade"

// Module 是 modules 段的一条登记：roots 定位包目录，requires 声明**允许**的
// 依赖方向。
//
// 为什么这段必须被真的执行：requires 是一张包之间的依赖拓扑声明，写错或漏写时
// 它不会自己报警——半年后没人知道它已经不成立，而依赖缠绕恰好是最难靠 review
// 发现的债类型（零报错、零警告、构建全绿）。
//
// requires 的条目有两种形态：
//
//   - 模块 id：本仓内部模块，必须在 modules 段登记过，否则声明自相矛盾（判为
//     policy 解析失败，不静默降级）。
//   - ext:<import 路径前缀>：外部包面，用于表达「本模块依赖某个外仓的哪个包」。
//     机制面迁到外仓之后，宿主的依赖拓扑表需要能表达跨仓依赖，而只认本仓模块 id
//     的判据表达不了。
type Module struct {
	ID       string   `yaml:"id"`
	Roots    []string `yaml:"roots"`
	Requires []string `yaml:"requires"`
	Owner    string   `yaml:"owner"`
	Managed  bool     `yaml:"managed"`
	Notes    string   `yaml:"notes"`
}

// DependencyDirection 是 dependency_direction 段的配置：依赖方向门禁的开关、
// 级别、豁免目录与**受管外部包面**。
//
// 为什么不塞进 modules 段：modules 段是**逐包**的声明，依赖方向是**跨包**的
// 规则，混在一段里会让「这条登记的字段」与「整个文件的配置」两种粒度互相干扰。
type DependencyDirection struct {
	Enabled    *bool    `yaml:"enabled"` // 指针：缺省即启用，只有显式 false 才关
	Severity   string   `yaml:"severity"`
	Desc       string   `yaml:"desc"`
	IgnoreDirs []string `yaml:"ignore_dirs"`

	// ExtPrefixes 是**受管外部包面**清单：这些 import 路径前缀下的外部包，
	// 全部纳入本门禁的判定面。
	//
	// 为什么要有这一层（policy 级），而不能只靠各模块 requires 里写的 ext: 条目：
	// 若「哪些外部包受管」本身也只由各模块自己声明，那么**最后一个声明它的模块
	// 一旦撤掉声明，整片包面就随之消失**——那些 import 重新变成不判定的。
	// 而外部包没有「未登记目录」那道兜底（它是别的仓的包，本仓没有它的目录），
	// 于是这条边就静默失管了：门禁变绿，而不是变红。
	//
	// 这一层与内部模块的结构同构：
	//   - 受管面：modules 段（内部）/ ext_prefixes（外部）
	//   - 逐模块的边：requires
	ExtPrefixes []string `yaml:"ext_prefixes"`
}

// on 报告依赖方向门禁是否启用（缺省启用）。
func (d DependencyDirection) on() bool { return d.Enabled == nil || *d.Enabled }

// SandboxProvider 是 sandbox_providers 段里的一条**档位登记**：一个执行后端
// 自称提供哪一档隔离。
//
// # 为什么档位要登记在案，而不是只写在代码里
//
// 后端自报的档位若只存在于实现中，它有三重失效：review 时看不见、改了没人会响、
// 标高了没人管。而档位是宿主做风险判断的依据——宿主据它决定「这次执行能不能
// 碰不可信输入」。于是档位声明需要一个**签字处**，policy 这一段就是那个签字处。
//
// # level 填常量名而不是档位值
//
// 填 `LevelProcess` 而不是 `process`：门禁**不能 import 被判的包**（它必须是最
// 底层的，否则「用它判别人」就变成「它自己也在被判定」，门禁的红绿会依赖它所判
// 的代码），所以它拿不到档位名表，只能比对代码里可静态识别的那一面——常量名。
// policy 侧填同一份常量名，两侧就共用一份真相。
type SandboxProvider struct {
	ID      string `yaml:"id"`
	Level   string `yaml:"level"`
	Package string `yaml:"package"`
	Notes   string `yaml:"notes"`
}

// SandboxProviders 是 sandbox_providers 段的配置：开关、级别、受忽略目录与登记清单。
//
// 形态与 dependency_direction 段同构（配置 + 清单），因为两者都是「一段跨包的
// 规则 + 一张逐条声明的表」。段缺省即启用——**未登记的后端会被报出来**，
// 而不是「没登记就当没这回事」。
type SandboxProviders struct {
	Enabled    *bool             `yaml:"enabled"`
	Severity   string            `yaml:"severity"`
	Desc       string            `yaml:"desc"`
	IgnoreDirs []string          `yaml:"ignore_dirs"`
	Providers  []SandboxProvider `yaml:"providers"`
}

// on 报告档位登记门禁是否启用（缺省启用）。
func (s SandboxProviders) on() bool { return s.Enabled == nil || *s.Enabled }

// Policy 是 architecture-policy.yaml 的反序列化结果。
type Policy struct {
	Version    int                 `yaml:"version"`
	Modules    []Module            `yaml:"modules"`
	Rules      []Rule              `yaml:"rules"`
	Facades    []FacadeRule        `yaml:"facades"`
	DepDir     DependencyDirection `yaml:"dependency_direction"`
	Thresholds Threshold           `yaml:"thresholds"`
	Exceptions []Exception         `yaml:"exceptions"`

	// SandboxProviders 是执行沙箱后端的档位登记段（声明面）。
	//
	// 指针而非值：段整体缺省时，**门禁仍然开启**——因为「本仓有后端实现却没登记」
	// 必须报出来，而不是「没登记就当没这回事」。指针只用于区分「段不存在」与
	// 「段存在但为空」，两者的判定结果相同（都要靠代码事实说话）。
	SandboxProviders *SandboxProviders `yaml:"sandbox_providers"`

	// Path 记录 policy 文件路径。自校验违规要指向它，而宿主仓的 policy
	// 不一定叫 architecture-policy.yaml——写死文件名等于让复用方永远看到
	// 一个指向不存在文件的报错位置。
	Path string `yaml:"-"`
}

// Load 读取并解析一份 policy 文件。
//
// 解析失败一律响亮失败（返回 error），不静默降级：门禁给不出结论却显示成通过，
// 是这条工具最贵的失败形态。
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pol Policy
	if err := yaml.Unmarshal(data, &pol); err != nil {
		return nil, fmt.Errorf("解析 policy 失败: %w", err)
	}
	pol.Path = path
	return &pol, nil
}

// Violation 是一处违反。
type Violation struct {
	Rule string // 规则 ID
	Path string // 相对仓库根的路径
	Line int    // 行号（0 表示不指向具体行）
	Text string // 人类可读说明
	Kind string // 违规形态（同一规则下的细分类别）
}

// FindRoot 从 start 向上查找含 architecture-policy.yaml 的目录。
//
// 为什么向上找而不是要求调用方传绝对路径：门禁要在这个目录下判定所有包，
// 而包在磁盘上的位置是随仓库克隆位置变的。
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "architecture-policy.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 architecture-policy.yaml（从 %s 向上查找失败）", dir)
		}
		dir = parent
	}
}

// repoRoot 找本仓根目录（供本包内的用例读真实 policy）。
func repoRoot() (string, error) { return FindRoot(".") }
