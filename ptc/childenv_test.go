package ptc

import (
	"os"
	"strings"
	"testing"
)

// 子进程环境必须是**白名单**，不是全量继承：宿主进程里的凭据类变量不得递出去。
func TestChildBaseEnvIsAllowlist(t *testing.T) {
	const secret = "HARNESS_TEST_PROVIDER_KEY"
	t.Setenv(secret, "deadbeef")

	got := envMap(childBaseEnv())
	if _, hit := got[secret]; hit {
		t.Fatalf("凭据类变量被透传给了子进程（全量继承又回来了）: %v", got)
	}
	// 协议三件必须**字面注入**：子进程靠它们认出自己是被 ptc 拉起来的。
	for _, want := range [][2]string{
		{childModeEnv, "1"},
		{childCallReqFDEnv, "3"},
		{childCallRespFDEnv, "4"},
	} {
		if got[want[0]] != want[1] {
			t.Fatalf("协议变量 %s 应为 %q，实得 %q", want[0], want[1], got[want[0]])
		}
	}
	// 对照：基础集仍在。全丢光同样「不含凭据」，但子进程会起不来。
	if os.Getenv("PATH") != "" {
		if _, ok := got["PATH"]; !ok {
			t.Fatal("基础集里的 PATH 被一并丢掉了（白名单退化成空表）")
		}
	}
}

// 字面注入优先于父进程同名值：父进程恰好有同名变量时，协议仍必须成立。
func TestChildBaseEnvLiteralWinsOverParent(t *testing.T) {
	t.Setenv(childCallReqFDEnv, "9")
	got := envMap(childBaseEnv())
	if got[childCallReqFDEnv] != "3" {
		t.Fatalf("父进程同名值盖掉了字面注入：%q", got[childCallReqFDEnv])
	}
}

// envMap 把 KEY=VALUE 列表摊成 map（重复键后者胜——与 procx 的字面注入口径一致）。
func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if ok {
			out[name] = val
		}
	}
	return out
}
