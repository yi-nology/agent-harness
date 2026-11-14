package worktree

import (
	"strings"
	"testing"
)

// TestNewRejectsContradictions 声明自检：写错的声明在装载时报错。
func TestNewRejectsContradictions(t *testing.T) {
	cases := []struct {
		name string
		ws   []Workspace
	}{
		{"空 ID", []Workspace{{}}},
		{"重 ID", []Workspace{{ID: "a"}, {ID: "a"}}},
		{"绝对路径", []Workspace{{ID: "a", Writable: []string{"/etc/"}}}},
		{"含点点", []Workspace{{ID: "a", Writable: []string{"../x"}}}},
		{"空声明项", []Workspace{{ID: "a", Writable: []string{" "}}}},
		{"只读与可写重叠", []Workspace{{ID: "a", ReadOnly: []string{"src/"}, Writable: []string{"src/"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.ws); err == nil {
				t.Fatal("自相矛盾的声明必须报错")
			}
		})
	}
}

// TestCanWriteIsFailClosed 没有命中即不可写。
//
// 默认允许会让一个漏写的声明变成全域可写，而漏写在配置里看不出来。
func TestCanWriteIsFailClosed(t *testing.T) {
	// 目录前缀 + 只读前缀的组合：两者不相交，声明自洽。
	p, err := New([]Workspace{{
		ID:       "a",
		Writable: []string{"src/"},
		ReadOnly: []string{"vendor/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := p.Workspaces()[0]
	if !w.CanWrite("src/main.go") {
		t.Fatal("目录前缀必须可写")
	}
	if w.CanWrite("vendor/lib.go") {
		t.Fatal("只读区必须不可写")
	}
	if w.CanWrite("other/x.go") {
		t.Fatal("没有命中必须不可写（fail-closed）")
	}
	if !w.CanRead("vendor/lib.go") {
		t.Fatal("只读区必须可读")
	}
	if !w.CanRead("src/main.go") {
		t.Fatal("可写区天然可读")
	}
	if w.CanRead("other/x.go") {
		t.Fatal("没有命中必须不可读（fail-closed）")
	}

	// 后缀形态单列一份声明：后缀与任何目录前缀都可能相交，故不能与只读前缀
	// 写在同一个工作区里（那会被自检判成自相矛盾——这正是它该做的事）。
	sp, err := New([]Workspace{{ID: "s", Writable: []string{".md"}}})
	if err != nil {
		t.Fatal(err)
	}
	sw := sp.Workspaces()[0]
	if !sw.CanWrite("README.md") || !sw.CanWrite("docs/guide.md") {
		t.Fatal("后缀必须命中任意层的同名后缀")
	}
	if sw.CanWrite("main.go") {
		t.Fatal("后缀不该命中别的扩展名")
	}
}

// TestConflictsOnlyOnWritable 只有**可写**重叠算冲突：多个人读同一份文件
// 没有危害，而写没有第二份真相。
func TestConflictsOnlyOnWritable(t *testing.T) {
	p, err := New([]Workspace{
		{ID: "a", Writable: []string{"one/"}, ReadOnly: []string{"shared/"}},
		{ID: "b", Writable: []string{"two/"}, ReadOnly: []string{"shared/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Conflicts(); len(c) != 0 {
		t.Fatalf("只读重叠不该算冲突，实得 %+v", c)
	}

	p2, err := New([]Workspace{
		{ID: "a", Writable: []string{"src/"}},
		{ID: "b", Writable: []string{"src/sub/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := p2.Conflicts()
	if len(c) != 1 || !strings.Contains(c[0].Why, "可写区重叠") {
		t.Fatalf("嵌套目录必须算冲突（子目录也被人同时写），实得 %+v", c)
	}
	if c[0].A != "a" || c[0].B != "b" {
		t.Fatalf("冲突对必须按字典序且指明双方，实得 %+v", c[0])
	}

	// 后缀相同也冲突：两份声明都会命中 readme.md。
	p3, err := New([]Workspace{
		{ID: "a", Writable: []string{".md"}},
		{ID: "b", Writable: []string{".md"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p3.Conflicts()) != 1 {
		t.Fatalf("相同后缀必须算冲突，实得 %+v", p3.Conflicts())
	}

	// 一条后缀是另一条的字符串后缀也冲突（issue #155）：A 可写 .go、B 可写
	// .o.go，任何 x.o.go 同时命中两条（HasSuffix 双真）——只判相等会把
	// 可写区交集判成「无冲突」，两工作区并行写同一批文件互踩且判定面全绿。
	// 反向（.o.go 在前）同款，坏实现两条都红不了。
	p6, err := New([]Workspace{
		{ID: "a", Writable: []string{".go"}},
		{ID: "b", Writable: []string{".o.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p6.Conflicts()) != 1 {
		t.Fatalf("后缀包含关系必须判冲突（.go × .o.go）: %+v", p6.Conflicts())
	}
	p7, err := New([]Workspace{
		{ID: "a", Writable: []string{".o.go"}},
		{ID: "b", Writable: []string{".go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p7.Conflicts()) != 1 {
		t.Fatalf("后缀包含关系必须判冲突（反向 .o.go × .go）: %+v", p7.Conflicts())
	}

	// 互不为后缀的两条后缀不冲突（判据不能把一切都判成冲突）。
	p8, err := New([]Workspace{
		{ID: "a", Writable: []string{".go"}},
		{ID: "b", Writable: []string{".md"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p8.Conflicts()) != 0 {
		t.Fatalf("互不包含的不同后缀不该冲突: %+v", p8.Conflicts())
	}

	// 后缀与目录前缀：保守判重叠（目录下可以出现该后缀的文件）。
	p4, err := New([]Workspace{
		{ID: "a", Writable: []string{".md"}},
		{ID: "b", Writable: []string{"docs/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p4.Conflicts()) != 1 {
		t.Fatalf("后缀与目录前缀必须保守判重叠，实得 %+v", p4.Conflicts())
	}

	// 完全不相干的两条：不冲突（否则判据会把一切都判成冲突）。
	p5, err := New([]Workspace{
		{ID: "a", Writable: []string{"one/"}},
		{ID: "b", Writable: []string{"two/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p5.Conflicts()) != 0 {
		t.Fatalf("不相干的目录不该冲突，实得 %+v", p5.Conflicts())
	}
}

// TestReadOnlyWorkspaceAllowed 纯只读工作区合法（审查助手就是这种形态）。
func TestReadOnlyWorkspaceAllowed(t *testing.T) {
	p, err := New([]Workspace{{ID: "reviewer", ReadOnly: []string{"src/"}}})
	if err != nil {
		t.Fatalf("纯只读工作区应当合法: %v", err)
	}
	w := p.Workspaces()[0]
	if w.CanWrite("src/a.go") {
		t.Fatal("没有可写区时必须一律不可写")
	}
}
