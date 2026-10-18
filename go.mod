module github.com/yi-nology/agent-harness

go 1.27

// agent-harness：多智能体编排的通用运行时内核。
//
// 定位：**编排是代码，LLM 只做单环节**——本仓只提供确定性编排的机制面
// （报告协议、环节状态机、事件总线、派发守卫、审计策略、溢出存档、PII 脱敏、
// 出站守卫），**不认识任何运维领域概念**。
//
// 领域资产（专家包/技能/诊断链/运维词表）属于宿主，harness 侧一律不得出现：
// 该纪律由 architecture-policy.yaml 的 harness-no-domain-vocabulary 门禁强制。

require (
	github.com/cloudwego/eino v0.9.21
	github.com/dop251/goja v0.0.0-20261010130053-73dc7edc278f
	github.com/oagentkit/agent v0.2.1
	github.com/oagentkit/guardx v0.1.1
	github.com/oagentkit/runtime v0.3.1
	github.com/oagentkit/toolgate v0.2.1
	github.com/oekit/concurrency v0.15.0
	github.com/oekit/logx v0.7.0
	github.com/oekit/pkg v0.14.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.4 // indirect
	github.com/bytedance/sonic/loader v0.5.2 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/dlclark/regexp2/v2 v2.9.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/eino-contrib/jsonschema v1.0.3 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/goph/emperror v0.17.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.9 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mark3labs/mcp-go v1.1.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/nikolalohinski/gonja v1.5.3 // indirect
	github.com/oagentkit/mcpkit v0.1.0 // indirect
	github.com/pelletier/go-toml/v2 v2.0.9 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/slongfield/pyfmt v0.0.0-20220222012616-ea85ff4c361f // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/yargevad/filepathx v1.0.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/arch v0.14.0 // indirect
	golang.org/x/exp v0.0.0-20230713183714-613f0c0eb8a1 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
