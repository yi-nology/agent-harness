package protocol

// 证据编号与引用完整性（批次一百三十七，结论可回溯面）：
// Evidence.ID 确定性挂号（`<agent>#E<n>`）+ verdicts[].evidence_ids 结构化引用 +
// 合并后的引用完整性校验。协议数据层只管承载与校验，不强制 LLM 必须引用（prompt 纪律
// 是软防线，缺引用不拦截——与 charts 同级「锦上添花」语义）。

import (
	"fmt"
	"strings"
)

// AssignEvidenceIDs 为报告内缺编号的证据确定性挂号：`<AgentName>#E<序号>`。
// AgentName 为空（引擎合成报告）时退化 `report#E<n>`。已有编号不覆盖——LLM 自报编号
// 与引擎挂号同规则时天然一致；聚合/合并只追加不重排，编号全程稳定。
//
// # 自动挂号必须避开已被占用的编号
//
// 旧实现用「数组下标 + 1」当编号，于是撞号是必然的：
//
//	evidence[0] 无编号          → 自动编成 "probe#E1"
//	evidence[1] 模型自报 "probe#E1"（非空，不覆盖）
//
// 结果两条证据同号。而同号让「引用证据说理」**失去指向**：verdict 引用
// "probe#E1" 时，消费方无从知道说的是哪一条；CheckEvidenceRefs 看到该编号
// 存在，也就不会报缺失——**歧义是静默的**，比缺失更难查。
//
// 故挂号前先收齐已被占用的编号（模型自报的一律优先，不覆盖），自动编号从
// 第一个未被占用的序号起取。
func AssignEvidenceIDs(r *Report) {
	if r == nil {
		return
	}
	owner := strings.TrimSpace(r.AgentName)
	if owner == "" {
		owner = "report"
	}
	used := make(map[string]bool, len(r.Evidence))
	for i := range r.Evidence {
		if id := strings.TrimSpace(r.Evidence[i].ID); id != "" {
			used[id] = true
		}
	}
	next := 1
	for i := range r.Evidence {
		if strings.TrimSpace(r.Evidence[i].ID) != "" {
			continue
		}
		for {
			candidate := fmt.Sprintf("%s#E%d", owner, next)
			next++
			if used[candidate] {
				continue
			}
			used[candidate] = true
			r.Evidence[i].ID = candidate
			break
		}
	}
}

// CheckEvidenceRefs 引用完整性校验：返回 verdicts 引用了但本报告 evidence 集中不存在
// 的编号（去重保序）。合议裁决引用的是「输入报告」的证据编号，合并进终报后终报
// evidence 集包含全部被引条目——调用时机=合并组装完成后（finalizeWithPlan / 只读收尾）。
// 缺失只在留痕面告警（调用方 logf），不拦截不回改：引用是增强通道，不是数据本体。
func CheckEvidenceRefs(r *Report) []string {
	if r == nil {
		return nil
	}
	have := make(map[string]bool, len(r.Evidence))
	for _, ev := range r.Evidence {
		if ev.ID != "" {
			have[ev.ID] = true
		}
	}
	var missing []string
	seen := make(map[string]bool)
	for _, v := range r.Verdicts {
		for _, id := range v.EvidenceIDs {
			id = strings.TrimSpace(id)
			if id == "" || have[id] || seen[id] {
				continue
			}
			seen[id] = true
			missing = append(missing, id)
		}
	}
	return missing
}
