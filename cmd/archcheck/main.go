// Command archcheck 执行 architecture-policy.yaml 的可执行面。
//
// 存在意义：policy 文件若只当文档读，半年后就与代码事实脱节；把它做成
// `make architecture-check` 门禁，规则违反即非零退出。本仓的头号红线是
// harness-no-domain-vocabulary——内核不得长出领域知识。
//
// 正则一律 RE2（Go 原生不支持 (?!...)）；负向前置如需表达，用结构化
// negative_follow 字段，不写进正则。
//
// **本命令只做装配**：找 policy → 跑门禁 → 写报告 → 给退出码。
// 判定逻辑在 archcheck/ 包里——那是可导入的执行体，宿主仓复用同一套判据时
// import 它即可，不必复制实现，也不必把本命令搬进自己的仓。
//
// 退出码：
//
//	0  无阻断级违反
//	1  有阻断级违反
//	2  门禁给不出结论（policy 解析失败 / 源码解析失败 / 声明自相矛盾）
//
// 2 与 1 必须分开：1 是「判了，有问题」，2 是「没判出来」。混成一个码，
// CI 就分不清「本仓有违规」与「门禁自己坏了」——而后者更需要人看。

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yi-nology/agent-harness/archcheck"
)

func main() {
	report := flag.Bool("report", false, "输出全量违反（默认每条规则截断到 10 条）")
	policyPath := flag.String("policy", "", "policy 文件路径（缺省为仓库根下的 architecture-policy.yaml）")
	flag.Parse()

	root, err := archcheck.FindRoot(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	pp := *policyPath
	if pp == "" {
		pp = filepath.Join(root, "architecture-policy.yaml")
	}
	pol, err := archcheck.Load(pp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}

	res, err := archcheck.Run(root, pol)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}

	// 有违反走 stderr，全通过走 stdout——与 Unix 工具的惯例一致。
	w := io.Writer(os.Stdout)
	if len(res.Violations) > 0 {
		w = os.Stderr
	}
	if err := archcheck.WriteReport(w, res, archcheck.ReportOptions{Full: *report}); err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: 写报告失败:", err)
		os.Exit(2)
	}

	if res.HasBlocking() {
		os.Exit(1)
	}
	fmt.Printf("架构门禁通过（%d 条规则 + %d 个门面包 + %d 个包的依赖方向，policy version %d；无阻断级违反）\n",
		len(pol.Rules), len(pol.Facades), len(pol.Modules), pol.Version)
}
