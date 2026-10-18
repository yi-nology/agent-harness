// 溢出存档平面单源单测（批三百零一）：提示契约往返（TruncHint/ParseHint 与前端
// spillHintOf 同判据）/定位符净化与会话隔离拒绝面/分页取回钳制/清单排序/写侧清理
// 按数+按龄双维。
package spill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTruncHintRoundTrip(t *testing.T) {
	// 无存档：纯截断提示（spill 写盘失败路径），ParseHint 判截断但无定位符
	plain := TruncHint(TruncNote{Omit: 100, Total: 200})
	if !strings.Contains(plain, "输出超预算已截断") {
		t.Fatalf("截断标记缺失: %s", plain)
	}
	if name, trunc := ParseHint(plain); !trunc || name != "" {
		t.Fatalf("无存档形态解析不符: name=%q trunc=%v", name, trunc)
	}

	// retrievable：定位符可往返（前端 regex 同契约 spill:([A-Za-z0-9._-]+)）
	hint := TruncHint(TruncNote{Omit: 100, Total: 200, Retrievable: true, Name: "1760000000000000000-spill.txt"})
	name, trunc := ParseHint(hint)
	if !trunc || name != "1760000000000000000-spill.txt" {
		t.Fatalf("定位符往返不符: name=%q trunc=%v", name, trunc)
	}
	if !strings.Contains(hint, "read_spill") {
		t.Fatalf("retrievable 形态应引导 read_spill: %s", hint)
	}

	// 非 retrievable：落盘路径形态无定位符（不引导调用不存在的工具）
	disk := TruncHint(TruncNote{Omit: 100, Total: 200, Path: "/data/spills/s1/x-spill.txt"})
	if name, trunc := ParseHint(disk); !trunc || name != "" {
		t.Fatalf("落盘形态不应出定位符: name=%q trunc=%v", name, trunc)
	}

	// 普通文本（含「spill:」字样但无截断标记）不算提示——防误报
	if name, trunc := ParseHint("前文 spill:abc-def.txt 后文"); trunc || name != "" {
		t.Fatalf("无截断标记不应判提示: name=%q trunc=%v", name, trunc)
	}
}

func TestReadPageIsolation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sess-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x-spill.txt"), []byte("内容甲乙丙"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 他会话目录的同名文件（隔离验证）
	other := filepath.Join(root, "sess-b")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "x-spill.txt"), []byte("不该读到"), 0o600); err != nil {
		t.Fatal(err)
	}

	pg, err := ReadPage(root, "sess-a", "x-spill.txt", 0, 0)
	if err != nil || pg.Content != "内容甲乙丙" {
		t.Fatalf("正读失败: %+v err=%v", pg, err)
	}

	bad := []struct{ name, want string }{
		{"../sess-b/x-spill.txt", "定位符非法"},
		{filepath.Join(root, "sess-a", "x-spill.txt"), "定位符非法"},
		{"", "定位符非法"},
		{".", "定位符非法"},
		{"nope-spill.txt", "存档不存在"},
	}
	for _, c := range bad {
		if _, err := ReadPage(root, "sess-a", c.name, 0, 0); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q 应拒绝（want %s）: %v", c.name, c.want, err)
		}
	}
	if _, err := ReadPage(root, "", "x-spill.txt", 0, 0); err == nil {
		t.Fatal("空会话标识应拒绝")
	}
	if _, err := ReadPage("", "sess-a", "x-spill.txt", 0, 0); err == nil {
		t.Fatal("空根目录应拒绝")
	}
}

func TestReadPagePaging(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("a", 100)
	if err := os.WriteFile(filepath.Join(dir, "p-spill.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// 缺省 limit 超出全文：一页到尾，无 next_offset
	pg, err := ReadPage(root, "s1", "p-spill.txt", 0, 0)
	if err != nil || pg.Bytes != 100 || !pg.EOF || pg.NextOffset != 0 {
		t.Fatalf("整页形态不符: %+v err=%v", pg, err)
	}
	// 分页：40+40+20
	p1, _ := ReadPage(root, "s1", "p-spill.txt", 0, 40)
	if p1.Bytes != 40 || p1.EOF || p1.NextOffset != 40 {
		t.Fatalf("首页不符: %+v", p1)
	}
	p3, _ := ReadPage(root, "s1", "p-spill.txt", 80, 40)
	if p3.Bytes != 20 || !p3.EOF {
		t.Fatalf("尾页不符: %+v", p3)
	}
	// 越界 offset 收到 total；负 offset 归 0；limit 超上限收上限
	pg, _ = ReadPage(root, "s1", "p-spill.txt", 999, 10)
	if pg.Offset != 100 || pg.Bytes != 0 || !pg.EOF {
		t.Fatalf("越界 offset 钳制不符: %+v", pg)
	}
	pg, _ = ReadPage(root, "s1", "p-spill.txt", -5, 10)
	if pg.Offset != 0 {
		t.Fatalf("负 offset 应归 0: %+v", pg)
	}
	pg, _ = ReadPage(root, "s1", "p-spill.txt", 0, MaxLimit+1)
	if pg.Bytes != 100 { // 全文小于上限：clamp 后仍是全文
		t.Fatalf("limit 钳制不符: %+v", pg)
	}
}

func TestListAndPrune(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for i := 0; i < 5; i++ {
		mk("f"+string(rune('0'+i))+"-spill.txt", now.Add(time.Duration(i)*time.Minute))
	}

	items, err := List(root, "s1")
	if err != nil || len(items) != 5 {
		t.Fatalf("清单不符: %d err=%v", len(items), err)
	}
	if !items[0].ModTime.After(items[4].ModTime) {
		t.Fatal("清单应 mtime 降序（最新在前）")
	}
	if items, err := List(root, "sess-never"); err != nil || len(items) != 0 {
		t.Fatalf("无会话目录=空清单不是错误: %v %v", items, err)
	}

	// 按数：keep 2 → 最旧 3 份删
	Prune(dir, 2, 0)
	if left, _ := List(root, "s1"); len(left) != 2 {
		t.Fatalf("按数清理后应剩 2 份: %d", len(left))
	}
	// 按龄：把剩余两份一份改成超龄 → 只删超龄那份
	old := now.Add(-48 * time.Hour)
	mk("aged-spill.txt", old)
	Prune(dir, 32, 24*time.Hour)
	names := map[string]bool{}
	if left, _ := List(root, "s1"); len(left) != 2 {
		t.Fatalf("按龄清理后应剩 2 份（aged 删除）: %+v", left)
	} else {
		for _, it := range left {
			names[it.Name] = true
		}
	}
	if names["aged-spill.txt"] {
		t.Fatal("超龄存档应被按龄清理")
	}
	// ttl=0 不按龄：造超龄文件不动它
	mk("aged2-spill.txt", old)
	Prune(dir, 32, 0)
	if left, _ := List(root, "s1"); len(left) != 3 {
		t.Fatalf("ttl=0 不应按龄删: %+v", left)
	}
}
