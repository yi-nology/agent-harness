package protocol

import "testing"

func TestUnwrapMCPText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"信封单条", `{"content":[{"type":"text","text":"inner data"}]}`, "inner data"},
		{"信封多条取首个非空", `{"content":[{"type":"text","text":""},{"type":"text","text":"second"}]}`, "second"},
		{"非信封 JSON 原样", `{"loadavg":"0.52"}`, `{"loadavg":"0.52"}`},
		{"纯文本原样", "plain output", "plain output"},
		{"信封全空回原样", `{"content":[{"type":"text","text":""}]}`, `{"content":[{"type":"text","text":""}]}`},
		{"坏 JSON 原样", `{broken`, `{broken`},
	}
	for _, c := range cases {
		if got := UnwrapMCPText(c.in); got != c.want {
			t.Errorf("%s: UnwrapMCPText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestStructuredPayload(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"双写信封取 structured", `{"content":[{"type":"text","text":"{\"success\":true}"}],"structuredContent":{"success":true}}`, `{"success":true}`, true},
		{"structured 在前含嵌套", `{"structuredContent":{"data":{"exit_code":0}},"content":[]}`, `{"data":{"exit_code":0}}`, true},
		{"旧形态无字段", `{"content":[{"type":"text","text":"x"}]}`, "", false},
		{"空 structuredContent 视为缺省", `{"structuredContent":null}`, "", false},
		{"纯文本", "plain", "", false},
		{"坏 JSON", `{broken`, "", false},
	}
	for _, c := range cases {
		got, ok := StructuredPayload(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: StructuredPayload = (%q,%t), want (%q,%t)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestPayloadCandidates(t *testing.T) {
	dual := `{"content":[{"type":"text","text":"{\"success\":true,\"data\":{}}"}],"structuredContent":{"success":true,"data":{}}}`
	legacy := `{"content":[{"type":"text","text":"{\"success\":false}"}]}`
	// structured 优先：谓词只需在首个候选上成立即可，text 镜像不再先被撞上
	got := PayloadCandidates(dual, false)
	if len(got) != 3 || got[0] != `{"success":true,"data":{}}` {
		t.Errorf("双写信封候选序 = %v, want structured 在首", got)
	}
	// 旧形态退化为 EnvelopeCandidates(envelopeFirst=false)：裸串在前、内层兜底
	got = PayloadCandidates(legacy, false)
	if len(got) != 2 || got[0] != legacy || got[1] != `{"success":false}` {
		t.Errorf("旧形态候选序 = %v", got)
	}
	// envelopeFirst=true：structured → 内层 → 裸串
	got = PayloadCandidates(dual, true)
	if len(got) != 3 || got[0] != `{"success":true,"data":{}}` || got[1] != `{"success":true,"data":{}}` || got[2] != dual {
		t.Errorf("envelopeFirst 候选序 = %v", got)
	}
	// 纯文本无信封：仅裸串
	if got := PayloadCandidates("plain", false); len(got) != 1 || got[0] != "plain" {
		t.Errorf("纯文本候选 = %v", got)
	}
}
