package egress

import (
	"testing"
)

// 本文件钉住 v0.32.0 新增的「兑现强度」面。
//
// # 这组用例在钉什么
//
// 钉的是一条**口径**：环境变量兑现出来的永远是**建议型**，
// 只有执行后端的能力声明（Enforcer）才谈得上**强制型**。
//
// 失效形态（此前真实存在）：判据只返回「成立/不成立」，于是
// 「代理档已兑现」这句话被读成「出站已被强制隔离」——而环境变量
// 拦不住静态链接二进制、UDP 与原始 socket。**声明强度 ≠ 实际强度**，
// 两者之间本来没有信号，现在信号在返回值上。
//
// 负对照：把 advisory 判成 enforced 的实现（把建议型说成强制型），
// 本文件的第一条用例必红——那条断言就是它的判据能力证明。

// fakeEnforcer 一个做出出口能力声明的后端（测试替身）。
type fakeEnforcer struct {
	mode Mode
}

func (f fakeEnforcer) Egress() Mode { return f.mode }

// TestVerifyVerdictReportsAdvisoryForProxy 钉住核心口径：代理档经**环境变量**
// 兑现 ⇒ 强度是**建议型**，绝不能是强制型。
//
// 这是本轮改动的全部意义：把「建议」与「强制」在判据返回值上分开。
func TestVerifyVerdictReportsAdvisoryForProxy(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	env, err := spec.Env(nil)
	if err != nil {
		t.Fatalf("Env 生成失败: %v", err)
	}
	m := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	v, err := spec.VerifyVerdict(m)
	if err != nil {
		t.Fatalf("兑现判据不该失败: %v", err)
	}
	if v.Strength == StrengthEnforced {
		t.Fatal("环境变量兑现不得报成强制型——环境变量拦不住静态二进制、UDP 与原始 socket")
	}
	if v.Strength != StrengthAdvisory {
		t.Fatalf("环境变量兑现的强度应为 advisory，实得 %v", v.Strength)
	}
	if v.Mode != ModeProxy {
		t.Fatalf("结论应带声明档位，实得 %v", v.Mode)
	}
}

// fakeVerifier 一个**能实测**出站隔离的后端（测试替身）。
type fakeVerifier struct {
	mode Mode
}

func (f fakeVerifier) VerifyEgress() (Mode, error) { return f.mode, nil }

// TestSatisfiedVerdictReportsDeclaredForSelfDeclaration 钉住「自报 ≠ 实测」：
// 后端只是**声明**能力（Enforcer）⇒ 强度是 declared，**不是** enforced。
//
// 这是本轮改动的全部意义：声明强度与实际强度之间必须有信号，
// 否则「代理档已兑现」会被读成「出站已被强制隔离」——而后端自报
// 恰恰是最容易被读成事实的那种声明。
func TestSatisfiedVerdictReportsDeclaredForSelfDeclaration(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	v, err := spec.SatisfiedVerdict(fakeEnforcer{mode: ModeProxy})
	if err != nil {
		t.Fatalf("后端已声明能力，不该判不满足: %v", err)
	}
	if v.Strength != StrengthDeclared {
		t.Fatalf("后端能力声明的兑现强度应为 declared（自报≠实测），实得 %v", v.Strength)
	}
}

// TestSatisfiedVerdictReportsEnforcedForMeasuredBackend 钉住另一半：
// 后端能**实测**（EgressVerifier）⇒ 强制型。
func TestSatisfiedVerdictReportsEnforcedForMeasuredBackend(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	v, err := spec.SatisfiedVerdict(fakeVerifier{mode: ModeProxy})
	if err != nil {
		t.Fatalf("后端已实测通过，不该判不满足: %v", err)
	}
	if v.Strength != StrengthEnforced {
		t.Fatalf("实测通过的兑现强度应为 enforced，实得 %v", v.Strength)
	}
}

// TestSatisfiedVerdictNeverReportsEnforcedWithoutEvidence 负对照：
// 只声明能力的后端**永远**不该被判成 enforced。
//
// 失效形态（此前真实存在）：自报即 enforced ⇒ 只看 Strength 的读者
// 会把「后端自称」读成「已经强制」。这条断言是那道门的钉子。
func TestSatisfiedVerdictNeverReportsEnforcedWithoutEvidence(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	for _, provider := range []any{
		fakeEnforcer{mode: ModeProxy},
		fakeEnforcer{mode: ModeDenied},
	} {
		v, err := spec.SatisfiedVerdict(provider)
		if err != nil {
			t.Fatalf("后端已声明能力，不该判不满足: %v", err)
		}
		if v.Strength == StrengthEnforced {
			t.Fatalf("只有自报、没有实测证据的后端不得被判成 enforced（自报≠实测）")
		}
	}
}

// TestSatisfiedVerdictRejectsSilentBackend 钉住「沉默按最弱处理」：
// 没实现 Enforcer 的后端一律判不满足——没声明 ≠ 不受限。
func TestSatisfiedVerdictRejectsSilentBackend(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	if _, err := spec.SatisfiedVerdict(struct{}{}); err == nil {
		t.Fatal("未声明出口能力的后端必须被判不满足")
	}
	if err := spec.SatisfiedBy(struct{}{}); err == nil {
		t.Fatal("SatisfiedBy 与 SatisfiedVerdict 的判定必须一致（同一份实现）")
	}
}

// TestVerdictStrengthNotRequiredForUnrestricted 钉住「未要求限制」的档位：
// 不是「没兑现」，是「无需兑现」。
func TestVerdictStrengthNotRequiredForUnrestricted(t *testing.T) {
	spec := Spec{}
	v, err := spec.VerifyVerdict(nil)
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if v.Strength != StrengthNotRequired {
		t.Fatalf("未要求限制时强度应为 not_required，实得 %v", v.Strength)
	}
}

// TestVerifyVerdictDeniedCannotBeEnvEnforced 钉住「禁网」这一档：
// 环境变量兑现不了它，且**不伪造**「看起来禁了」的变量。
func TestVerifyVerdictDeniedCannotBeEnvEnforced(t *testing.T) {
	spec := Spec{Mode: ModeDenied}
	if _, err := spec.VerifyVerdict(nil); err == nil {
		t.Fatal("禁网不得被环境变量判为已兑现")
	}
	if _, err := spec.Env(nil); err == nil {
		t.Fatal("禁网不得用环境变量兑现（不伪造看起来禁了的一组变量）")
	}
}

// TestVerifyAndVerifyVerdictAgree 钉住「同一份实现」：
// 老接口 Verify 与新接口 VerifyVerdict 的成立/不成立结论必须一致。
//
// 两份实现各判一套 ⇒ 某天它们会对不上，而对不上的那一刻没人说得清
// 该信谁——这正是本仓反复吃过的亏。
func TestVerifyAndVerifyVerdictAgree(t *testing.T) {
	spec := Spec{Mode: ModeProxy, Proxy: "http://127.0.0.1:3128"}
	// 空环境：两个入口都该判不成立。
	if err := spec.Verify(map[string]string{}); err == nil {
		t.Fatal("空环境不得判为已兑现")
	}
	if _, err := spec.VerifyVerdict(map[string]string{}); err == nil {
		t.Fatal("Verify 与 VerifyVerdict 的结论必须一致")
	}
}
