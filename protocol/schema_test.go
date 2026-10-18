package protocol

import "testing"

// schemaShell 双写 shell 信封的最小测试 schema（键面取自 serverkit SchemaShellExec
// 的 _ok 臂；此处关注 ValidateStructured 的分派语义而非 schema 全谱——全谱对拍在
// serverkit/askops/terminalx 各包 conformance 测试）。
const schemaShell = `{
  "type": "object",
  "anyOf": [
    {
      "type": "object",
      "additionalProperties": false,
      "required": ["success", "data"],
      "properties": {
        "success": {"const": true},
        "data": {"type": "object", "additionalProperties": false,
          "required": ["stdout", "stderr", "exit_code"],
          "properties": {"stdout": {"type": "string"}, "stderr": {"type": "string"}, "exit_code": {"type": "integer"}}}
      }
    },
    {
      "type": "object",
      "additionalProperties": false,
      "required": ["success", "error"],
      "properties": {
        "success": {"const": false},
        "error": {"type": "object", "additionalProperties": false,
          "required": ["code", "message", "suggestion"],
          "properties": {"code": {"type": "string"}, "message": {"type": "string"}, "suggestion": {"type": "string"}}}
      }
    }
  ]
}`

func TestValidateStructured(t *testing.T) {
	okEnvelope := `{"content":[{"type":"text","text":"{\"success\":true}"}],"structuredContent":{"success":true,"data":{"stdout":"o","stderr":"","exit_code":0}}}`
	badEnvelope := `{"content":[{"type":"text","text":"x"}],"structuredContent":{"success":true,"data":{"stdout":"o","junk":1}}}`
	noStructured := `{"content":[{"type":"text","text":"{\"success\":true}"}]}`

	cases := []struct {
		name      string
		schemaSrc string
		envelope  string
		wantErr   bool
	}{
		{"符合声明", schemaShell, okEnvelope, false},
		{"违反声明（多余键）", schemaShell, badEnvelope, true},
		{"未声明 schema 放行", "", badEnvelope, false},
		{"无 structuredContent 放行", schemaShell, noStructured, false},
		{"旧形态纯文本放行", schemaShell, "plain output", false},
		{"坏信封放行", schemaShell, "{broken", false},
		{"坏 schema 报错", "{broken", okEnvelope, true},
	}
	for _, c := range cases {
		err := ValidateStructured(c.schemaSrc, c.envelope)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wantErr=%t", c.name, err, c.wantErr)
		}
	}
}

// TestValidateStructuredCache 编译缓存：同 schema 二次调用走缓存（行为等价断言，
// 缓存命中本身不可观测——两次结果一致即语义无漂移）。
func TestValidateStructuredCache(t *testing.T) {
	okEnvelope := `{"structuredContent":{"success":true,"data":{"stdout":"o","stderr":"","exit_code":0}}}`
	for i := 0; i < 2; i++ {
		if err := ValidateStructured(schemaShell, okEnvelope); err != nil {
			t.Fatalf("第 %d 次校验失败: %v", i+1, err)
		}
	}
}
