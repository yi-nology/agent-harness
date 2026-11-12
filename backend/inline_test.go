package backend

import (
	"context"
	"slices"
	"testing"

	kerrors "github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sandbox"
)

// TestInline 兜底后端：契约可满足性的证明件。
func TestInline(t *testing.T) {
	in := NewInline()
	if in.ID() != "inline" || in.Isolation() != IsolationNone {
		t.Fatalf("自报错误：id=%s iso=%s", in.ID(), in.Isolation())
	}

	ran := ""
	if err := in.Register("demo", func(_ context.Context, j Job) (Result, error) {
		ran = j.Image
		return Result{OK: true, ExitCode: 0, Stdout: "ok"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("查得到就执行", func(t *testing.T) {
		res, err := in.Exec(context.Background(), Job{Image: "demo"})
		if err != nil || !res.OK || ran != "demo" {
			t.Fatalf("res=%+v err=%v ran=%q", res, err, ran)
		}
	})

	t.Run("查不到就拒绝（ErrUnknownProgram）", func(t *testing.T) {
		_, err := in.Exec(context.Background(), Job{Image: "nope"})
		if !kerrors.Is(err, ErrUnknownProgram) {
			t.Fatalf("期望 ErrUnknownProgram，得到 %v", err)
		}
	})

	t.Run("不合法作业先于查表拒绝", func(t *testing.T) {
		_, err := in.Exec(context.Background(), Job{Image: " "})
		if err == nil || kerrors.Is(err, ErrUnknownProgram) {
			t.Fatalf("期望作业校验错，得到 %v", err)
		}
	})

	t.Run("判据视角：高于 none 的要求不会被它满足", func(t *testing.T) {
		spec := Spec{MinIsolation: IsolationProcess}
		if err := spec.SatisfiedBy(in); !kerrors.Is(err, ErrNotSatisfied) {
			t.Fatalf("兜底后端不应满足更高要求，得到 %v", err)
		}
		spec = Spec{Egress: sandbox.EgressProxy}
		if err := spec.SatisfiedBy(in); !kerrors.Is(err, ErrSilent) {
			t.Fatalf("未声明出站能力面应报 ErrSilent，得到 %v", err)
		}
	})

	t.Run("登记表自检", func(t *testing.T) {
		if got := in.Names(); !slices.Equal(got, []string{"demo"}) {
			t.Fatalf("Names=%v", got)
		}
		if err := in.Register("demo", func(context.Context, Job) (Result, error) { return Result{}, nil }); err == nil {
			t.Fatal("重复登记应被拒")
		}
		if err := in.Register("", func(context.Context, Job) (Result, error) { return Result{}, nil }); err == nil {
			t.Fatal("空名应被拒")
		}
		if err := in.Register("nilfn", nil); err == nil {
			t.Fatal("nil 处理函数应被拒")
		}
	})

	t.Run("Exec 返回的基础设施故障与作业失败分层", func(t *testing.T) {
		boom := kerrors.New("boom")
		_ = in.Register("fail", func(_ context.Context, _ Job) (Result, error) {
			return Result{OK: false, ExitCode: 3, Stderr: "程序自己失败"}, boom
		})
		_, err := in.Exec(context.Background(), Job{Image: "fail"})
		if !kerrors.Is(err, boom) {
			t.Fatalf("处理函数的 error 应原样透传（基础设施故障），得到 %v", err)
		}
	})
}
