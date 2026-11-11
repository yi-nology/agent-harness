module github.com/yi-nology/goagent-kernel

// 最低工具链 1.27.4：不是风格选择，是安全地板。
//
// 2026-11-06 首次跑通 govulncheck 抓到 6 个漏洞，**全部落在标准库**
// （crypto/x509 GO-2026-6809 需 1.27.3；net/http 三连 GO-2026-6617/6613/6611/6603
// 与 crypto/tls GO-2026-6607 需 1.27.2）。也就是说：用 go1.27.1 编出来的二进制，
// 无论本仓代码多干净都带着这 6 个洞。
//
// 2026-11-11 抬到 1.27.4：Go 官方 2026-11-10 安全发布（1.27.4 / 1.26.11，
// 修复标准库 CVE-2026-97033…97054 一批）后，govulncheck 当天即报本仓命中
// 其中 1 条——安全地板**随安全发布当天抬升**，不等下一轮发版。
//
// 为什么写在 go 指令而不是 toolchain 指令：**依赖模块的 toolchain 指令会被忽略**，
// 只有 go 指令会把下游的主模块工具链顶上去（GOTOOLCHAIN=auto 自动取新版）。
// 写成 toolchain 只会让本仓自己升，下游照样用旧版编出带洞的二进制。
go 1.27.4

// goagent-kernel：多智能体编排的通用运行时内核。
//
// 定位：**编排是代码，LLM 只做单环节**——本仓只提供确定性编排的机制面
// （报告协议、环节状态机、事件总线、派发守卫、审计策略、溢出存档、PII 脱敏、
// 出站守卫），**不认识任何运维领域概念**。
//
// 领域资产（专家包/技能/诊断链/运维词表）属于宿主，harness 侧一律不得出现：
// 该纪律由 architecture-policy.yaml 的 harness-no-domain-vocabulary 门禁强制。

require (
	github.com/cloudwego/eino v0.9.23
	github.com/dop251/goja v0.0.0-20261110143652-a4433846ae5b
	github.com/oagentkit/agent v0.14.8
	github.com/oagentkit/guardx v0.24.1
	github.com/oagentkit/runtime v0.12.6
	github.com/oagentkit/toolgate v0.17.0
	github.com/oagentkit/tools v0.14.3
	github.com/oekit/concurrency v0.32.0
	github.com/oekit/logx v0.9.2
	github.com/oekit/pkg v0.42.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.5 // indirect
	github.com/bytedance/sonic/loader v0.5.3 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dlclark/regexp2/v2 v2.13.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/eino-contrib/jsonschema v1.0.3 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20261110164715-8bb1ee682bd8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/goph/emperror v0.17.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/mailru/easyjson v0.9.2 // indirect
	github.com/mark3labs/mcp-go v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/nikolalohinski/gonja v1.5.3 // indirect
	github.com/oagentkit/mcpkit v0.12.8 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/slongfield/pyfmt v0.0.0-20220222012616-ea85ff4c361f // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/yargevad/filepathx v1.0.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/arch v0.33.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/exp v0.0.0-20261109185158-a52e3aef5920 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	mvdan.cc/sh/v3 v3.14.1 // indirect
)
