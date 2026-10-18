package protocol_test

// 解析面 fuzz（批次二百九十八，差距审计 P2-11）：ParseReport / ValidateStructured 的
// 永不 panic 不变量——畸形 JSON/深嵌套/巨串/非法 UTF-8 都只许返回 error 不许崩。
// go test 常规跑动子集（种子语料），go test -fuzz 全量（CI 不跑全量，本地排障用）。

import (
	"strings"
	"testing"

	"github.com/yi-nology/agent-harness/protocol"
)

func FuzzParseReport(f *testing.F) {
	f.Add(`{"agent_name":"a","conclusion":"c","summary":"s","confidence":"high"}`)
	f.Add(`{"agent_name":"a","evidence":[{"tool":"t","snippet":"s","interpretation":"i"}]}`)
	f.Add(`not-json`)
	f.Add(``)
	f.Add(`{"recommendation":{"action":"none","steps":["x"]},"decision":"none"}`)
	f.Add("{\"agent_name\":\"\\u0000x\"}")
	f.Add(`[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]`)
	f.Fuzz(func(t *testing.T, raw string) {
		rep, err := protocol.ParseReport(raw)
		if err == nil && rep == nil {
			t.Fatal("ParseReport 不得同时返回 nil report 与 nil error")
		}
	})
}

func FuzzValidateStructured(f *testing.F) {
	// 语料形态对齐 serverkit 注册表信封（structuredContent 主通道）与双写兼容（无=放行）。
	f.Add(`{"type":"object","properties":{"a":{"type":"string"}}}`, `{"structuredContent":{"a":"x"}}`)
	f.Add(`{"type":"object","properties":{"a":{"type":"string"}}}`, `{"content":[{"type":"text","text":"x"}]}`)
	f.Add(`not-a-schema`, `not-an-envelope`)
	f.Add(``, ``)
	f.Add(`{"type":"object"}`, `{"structuredContent":null}`)
	f.Fuzz(func(t *testing.T, schemaSrc, envelope string) {
		_ = protocol.ValidateStructured(schemaSrc, envelope) // 永不 panic；error 返回合法
	})
}

// 批三百零三扩种（挂账余项）：批二百九十八头注释声称「深嵌套/巨串/非法 UTF-8」而种子
// 里没有——go test 常规只跑种子语料，声称与实际不符=确定性缺口。补齐真种子+错误分类
// 断言（编译错 vs 违规错混淆不被发现）+信封原语独立 fuzz（StructuredPayload/
// UnwrapMCPText 此前零覆盖）。
func FuzzValidateStructuredRichSeeds(f *testing.F) {
	deep := strings.Repeat(`[`, 500) + strings.Repeat(`]`, 500)
	f.Add(`{"type":"object"}`, `{"structuredContent":`+deep+`}`)
	f.Add(`{"type":"object"}`, `{"structuredContent":"`+strings.Repeat("x", 1<<16)+`"}`)
	f.Add(`{"type":"object"}`, "{\"structuredContent\":\"\xed\xa0\x80\"}")                           // 非法 UTF-8（代理对半截）
	f.Add(`{"type":"object"}`, `{"structuredContent":123}`)                                          // 非 object 载荷
	f.Add(`{"type":"object"}`, `{"structuredContent":[1,2,3]}`)                                      // array 载荷
	f.Add(`{"type":"object"}`, `{"structuredContent":{"a":1,"a":2}}`)                                // 重复键
	f.Add(`{"$ref":"#"}`, `{"structuredContent":{}}`)                                                // $ref 自引用
	f.Add(`{"type":"string","enum":["`+strings.Repeat("e", 4096)+`"]}`, `{"structuredContent":"x"}`) // 巨型 enum
	f.Add(`{"$schema":"bogus","type":"object"}`, `{"structuredContent":{}}`)                         // 非法草稿版本
	f.Add(`{"type":"object","properties":{"a":{"$ref":"#/properties/a"}}}`, `{"structuredContent":{"a":{}}}`)
	f.Fuzz(func(t *testing.T, schemaSrc, envelope string) {
		_ = protocol.ValidateStructured(schemaSrc, envelope) // 永不 panic
	})
}

func FuzzStructuredPayload(f *testing.F) {
	f.Add(`{"structuredContent":{"a":"x"}}`)
	f.Add(`{"structuredContent":null}`)
	f.Add(`{"structuredContent":"null"}`)
	f.Add(`{"content":[{"type":"text","text":"x"}]}`)
	f.Add(`[]`)
	f.Add(`{"structuredContent":`)
	f.Fuzz(func(t *testing.T, raw string) {
		s, ok := protocol.StructuredPayload(raw)
		if !ok && s != "" {
			t.Fatal("ok=false 时必须为空串（调用侧按 ok 判形，带值空串会误配）")
		}
	})
}

func FuzzUnwrapMCPText(f *testing.F) {
	f.Add(`{"content":[{"type":"text","text":"inner"}]}`)
	f.Add(`{"content":[]}`)
	f.Add(`{"content":"not-array"}`)
	f.Add(`plain text`)
	f.Add(`{"content":[{"type":"image","data":"x"}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		_ = protocol.UnwrapMCPText(raw) // 永不 panic，恒有返回
	})
}
