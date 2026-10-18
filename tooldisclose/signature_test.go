package tooldisclose

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

func toolWith(name string, required []string, props ...string) Tool {
	p := make(map[string]Param, len(props))
	for _, k := range props {
		p[k] = Param{Type: "string"}
	}
	return Tool{Name: name, Schema: Schema{Type: "object", Properties: p, Required: required}}
}

// TestSignatureOfOrdersRequiredByDeclaration 必填参数按 Required 的声明顺序排。
//
// 这条不是审美问题：Required 是宿主显式表达的顺序，打乱它等于告诉模型
// 「参数顺序和你的声明不一致」——模型照序传参就会传错位。
func TestSignatureOfOrdersRequiredByDeclaration(t *testing.T) {
	got, err := SignatureOf(toolWith("get_logs", []string{"host", "count"}, "count", "host"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "get_logs(host, count)" {
		t.Fatalf("必填应按声明顺序，实得 %q", got)
	}
}

// TestSignatureOfMarksOptional 可选参数带 ? 且排在必填之后。
func TestSignatureOfMarksOptional(t *testing.T) {
	got, err := SignatureOf(toolWith("get_logs", []string{"host"}, "host", "count", "since"))
	if err != nil {
		t.Fatal(err)
	}
	// 可选部分按名称升序（map 无序，必须确定化）
	if got != "get_logs(host, count?, since?)" {
		t.Fatalf("实得 %q", got)
	}
}

// TestSignatureOfIsDeterministic 同一份 schema 多次派生必须逐字相同。
//
// Schema.Properties 是 map，Go 的迭代顺序随机；签名是**给模型读的文本**，
// 顺序漂移会让模型每次看到不同的签名。这条钉的是确定性。
func TestSignatureOfIsDeterministic(t *testing.T) {
	tl := toolWith("search", nil, "zeta", "alpha", "mu", "beta")
	first, err := SignatureOf(tl)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		got, err := SignatureOf(tl)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("第 %d 次派生结果漂移：%q vs %q", i, got, first)
		}
	}
	if first != "search(alpha?, beta?, mu?, zeta?)" {
		t.Fatalf("可选参数应按名称升序，实得 %q", first)
	}
}

// TestSignatureOfRejectsNonIdentifierParam 参数名不是合法标识符时**报错**而不是硬凑。
//
// 改名会改掉 JSON 键名——那等于把一个参数悄悄换掉，让模型调的是另一个参数。
func TestSignatureOfRejectsNonIdentifierParam(t *testing.T) {
	for _, bad := range []string{"get-logs", "2nd", "with space", "", "a.b"} {
		tl := toolWith("t", nil, bad)
		_, err := SignatureOf(tl)
		if err == nil {
			t.Fatalf("参数名 %q 非法时应报错", bad)
		}
		if !errors.Is(err, ErrSchemaNotDerivable) {
			t.Fatalf("应归口 ErrSchemaNotDerivable，实得 %v", err)
		}
	}
}

// TestSignatureOfRejectsInconsistentRequired Required 点名了不存在的参数时报错。
//
// 静默忽略的话，这个参数对模型就是不存在的，而宿主以为它必填——两边的认知
// 分叉且都没有信号。
func TestSignatureOfRejectsInconsistentRequired(t *testing.T) {
	_, err := SignatureOf(toolWith("t", []string{"missing"}, "present"))
	if !errors.Is(err, ErrSchemaNotDerivable) {
		t.Fatalf("应报错，实得 %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("错误应点名问题参数，实得 %q", err)
	}
}

// TestSignatureOfRejectsDuplicateRequired Required 重名报错。
func TestSignatureOfRejectsDuplicateRequired(t *testing.T) {
	_, err := SignatureOf(toolWith("t", []string{"a", "a"}, "a"))
	if !errors.Is(err, ErrSchemaNotDerivable) {
		t.Fatalf("应报错，实得 %v", err)
	}
}

// TestSignatureOfRejectsDegenerateSchema 退化占位（无参数定义）必须报错。
//
// 此时派生空签名会让程序照跑、宿主收不到任何参数，表现为「工具调了但什么都没传」，
// 比当场报错难查得多。
func TestSignatureOfRejectsDegenerateSchema(t *testing.T) {
	for _, tl := range []Tool{
		{Name: "t", Schema: Schema{Type: DegenerateType}},
		{Name: "t", Schema: Schema{Type: "object"}},
		{Name: ""},
	} {
		if _, err := SignatureOf(tl); err == nil {
			t.Fatalf("退化/空 schema 应报错，实得 nil：%+v", tl)
		}
	}
}

// TestSignatureOfOutputIsParsable 派生结果必须能被 ptc 解析。
//
// 两处口径若不一致，会出现「这里派生得出、那边解析不了」的夹缝——
// 表现是宿主照着 schema 派生，却在装配期被拒，且两边都看不出原因。
func TestSignatureOfOutputIsParsable(t *testing.T) {
	got, err := SignatureOf(toolWith("get_logs", []string{"host"}, "host", "count"))
	if err != nil {
		t.Fatal(err)
	}
	// ptc 的解析规则：name(ident, ident?, ...)
	if got != "get_logs(host, count?)" {
		t.Fatalf("实得 %q", got)
	}
	open := strings.IndexByte(got, '(')
	body := got[open+1 : len(got)-1]
	for _, part := range strings.Split(body, ",") {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), "?"))
		if !isJSIdent(part) {
			t.Fatalf("派生出的形参 %q 不是合法标识符", part)
		}
	}
}

// TestSignatureOfOptionalAlwaysAfterRequired 派生结果里可选形参一定在必填之后。
func TestSignatureOfOptionalAlwaysAfterRequired(t *testing.T) {
	tl := tooldiscloseTool(t, "f", "required", "optA", "optB")
	got, err := SignatureOf(tl)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "?") != 2 || !strings.HasSuffix(got, "optB?)") {
		t.Fatalf("可选形参应在末尾且都带 ?，实得 %q", got)
	}
}

// tooldiscloseTool 造一个必填 + 两个可选的工具。
func tooldiscloseTool(t *testing.T, name, required string, optional ...string) Tool {
	t.Helper()
	props := append([]string{required}, optional...)
	return toolWith(name, []string{required}, props...)
}
