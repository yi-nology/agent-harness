package protocol

// 输入形态 → 解析通道判定：本文件集合「模型输出长什么样」对「走哪条通道 / 是否
// 拒收」的判据，与 report_test.go 的字段校验用例分开——前者判**通道选择**，后者
// 判**内容合规**，混在一个文件里会让两条判据的失败互相淹没。
//
// 三条成功通道（report_repair.go）：直解（Normalize 标量归一）→ 修复（Repair，
// 产物**必降置信 low**，markRepaired）→ 提取（ExtractObject，先散文后修复）。
// 本文件逐条钉住「哪条通道救回」以及「救回后置信是否被正确下调」——后者是这个
// 包真正的安全承诺；漏掉它的话，「修复通道把一切放行」的错实现也能全绿。

import (
	"testing"

	"github.com/oekit/pkg/errors"
)

// T17 散文包裹的报告 → 提取通道救回（真实形态：前缀散文 + JSON）。
// 降级路径（ErrNotReport）仍由 TestParsePlainText 覆盖（无大括号的纯文本）。
func TestParseProseWrappedRescued(t *testing.T) {
	raw := "以下是诊断结果：\n{\"agent_name\":\"X\",\"confidence\":\"high\"}\n以上供参考"
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("散文包裹报告应被提取救回: %v", err)
	}
	if r.AgentName != "X" {
		t.Fatalf("字段丢失: %+v", r)
	}
}

// T18 缺值非法 JSON（`"confidence":}`）→ 修复通道救回，且**必降置信 low**。
//
// 旧契约写的是「形如报告但解析失败 → ErrNotReport」，钉的是 oekit/pkg v0.37 时代
// 的 Repair 边界（当时只补括号，`{"a":}` 是非法 JSON 的稳定不动点）。oekit/pkg
// v0.38 起 jsonrepair.Repair 的输出契约明写「`:` 后缺值补 null」（pkg#268），该
// 形态因此成为可修复输入 ⇒ 本条转红。
//
// 判定为「规则该改」而非「代码错了」的依据：救回点**落在 markRepaired 的既有
// 设计内**——修复产物整体降置信 low，下游 verify/needs_review 承接（正是那条
// 实弹审计 Critical 的处置）。故此处钉住的不变量从「失败即拒收」改为「凡经修复
// 通道放行必为 low」：后者才是这个包真正的承诺，前者只是它在某个上游版本下的
// 投影，上游一扩宽就失真。
func TestParseInvalidJSONRepairedLow(t *testing.T) {
	raw := "{\"agent_name\":\"X\",\"confidence\":}"
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("修复通道应救回缺值形态: %v", err)
	}
	if r.Confidence != ConfidenceLow {
		t.Fatalf("修复通道产物必须降置信 low, got %q", r.Confidence)
	}
	if r.AgentName != "X" {
		t.Fatalf("字段丢失: %+v", r)
	}
}

// T18b 有协议关键字段但结构不可修复 → ErrNotReport（与 T18 成对的反向判据）。
//
// 成对的理由：T18 证明修复通道会救回**语法级缺失值**，T18b 证明它不会凭空造
// 对象——「缺失的冒号」与「没有大括号」都不在 Repair 的修复清单里（全角标点 /
// 非法转义 / 连续尾逗号 / 缺值 / 未闭合括号），故必须仍走拒收。
// 两条必须同时在场：只留 T18 的话，「修复通道把一切都放行」这个错实现照样全绿。
func TestParseUnrepairableRejected(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"键值间缺冒号", `{"agent_name""X"}`},
		{"有键名但无 JSON 结构", `输出里有 "agent_name" 这个字段`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseReport(c.raw); !errors.Is(err, ErrNotReport) {
				t.Fatalf("期望 ErrNotReport, got: %v", err)
			}
		})
	}
}

// T19 普通文本（无协议关键字段）→ ErrNotReport（脚本/命令类输出不受影响）。
func TestParsePlainText(t *testing.T) {
	if _, err := ParseReport("#!/bin/bash\necho hello"); !errors.Is(err, ErrNotReport) {
		t.Fatalf("期望 ErrNotReport, got: %v", err)
	}
}
