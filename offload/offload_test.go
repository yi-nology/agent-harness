package offload

// 夹具一律用合成名（blob-alpha / item-01 / srv-beta），不带任何领域味——
// 夹具是契约的示例，示例带味等于把契约钉死在某个领域上。

import (
	"bytes"
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// fakeStore 最小落盘面替身：证明 Store 可在零领域依赖下实现，并记录落盘次数。
type fakeStore struct {
	keys  []string          // Put 的逻辑键序列（用于证明第二轮不再写）
	data  map[string][]byte // 键 → 落盘字节
	failK string            // 非空则对命中该键的 Put 报错
}

func newStore() *fakeStore { return &fakeStore{data: map[string][]byte{}} }

func (f *fakeStore) Put(key string, data []byte) error {
	if f.failK != "" && f.failK == key {
		return errors.New("落盘面不可写")
	}
	f.keys = append(f.keys, key)
	f.data[key] = append([]byte(nil), data...)
	return nil
}

// blob 合成对象：kind/mime 一律可预期，尺寸可调。
func blob(kind, ext string, size int) Blob {
	return Blob{Kind: kind, MimeType: "application/octet-stream", Ext: ext, Data: bytes.Repeat([]byte("a"), size)}
}

func mustApply(t *testing.T, req Request) Report {
	t.Helper()
	return Apply(req)
}

// TestBelowThresholdStaysInline 低于阈值的对象一律内联，且**第二轮也不替换**：
// 占位体量加上一次取回往返高于直接内联，对小对象外置是负优化。
func TestBelowThresholdStaysInline(t *testing.T) {
	st, fs := NewState(), newStore()
	b := blob("image", ".png", MinBytes-1)

	for turn := 1; turn <= 2; turn++ {
		rep := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})
		it := rep.Items[0]
		if it.Replaced || it.Placeholder != "" {
			t.Fatalf("第 %d 轮小对象不得被替换: %+v", turn, it)
		}
		if !strings.Contains(it.Reason, "阈值") {
			t.Fatalf("应留下阈值原因, 实得 %q", it.Reason)
		}
	}
	if len(fs.keys) != 0 || st.Len() != 0 {
		t.Fatalf("小对象不得落盘/标记: keys=%v state=%d", fs.keys, st.Len())
	}
	if rep := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs}); rep.Inline != 1 {
		t.Fatalf("应计入内联条数, 实得 %d", rep.Inline)
	}
}

// TestCustomThresholdOverride 逐调用覆盖阈值。
func TestCustomThresholdOverride(t *testing.T) {
	st, fs := NewState(), newStore()
	b := blob("audio", ".wav", 64) // 远低于缺省阈值

	rep := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs, MinBytes: 16})
	if !rep.Items[0].Offloaded || rep.Stored != 1 {
		t.Fatalf("覆盖阈值后应外置: %+v", rep.Items[0])
	}
	if rep2 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs, MinBytes: 16}); !rep2.Items[0].Replaced {
		t.Fatalf("覆盖阈值后第二轮应替换: %+v", rep2.Items[0])
	}
}

// TestTwoPhaseFirstKeepsContentThenReplaces 两阶段核心：首次必须保留本体，
// 后续轮才替换。若首次就替换，模型这一轮从未见过该对象——输入凭空丢失。
func TestTwoPhaseFirstKeepsContentThenReplaces(t *testing.T) {
	st, fs := NewState(), newStore()
	data := bytes.Repeat([]byte("blob-alpha-payload"), 300) // > MinBytes
	b := Blob{Kind: "image", MimeType: "image/png", Ext: ".png", Data: data}

	// 第一轮：落盘 + 标记，但内容原地保留。
	t1 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})
	it1 := t1.Items[0]
	if !it1.Offloaded {
		t.Fatalf("第一轮应已落盘: %+v", it1)
	}
	if it1.Replaced || it1.Placeholder != "" {
		t.Fatalf("第一轮绝不得替换内容（模型还没看到过它）: %+v", it1)
	}
	if t1.Stored != 1 || t1.Replaced != 0 || t1.Inline != 1 {
		t.Fatalf("第一轮计数应 Stored=1/Replaced=0/Inline=1, 实得 %+v", t1)
	}
	if st.Len() != 1 {
		t.Fatalf("第一轮应标记摘要, state=%d", st.Len())
	}
	if !bytes.Equal(fs.data[it1.Path], data) {
		t.Fatalf("落盘字节应与原文一致")
	}

	// 第二轮：同一份内容 → 替换为占位文本。
	t2 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})
	it2 := t2.Items[0]
	if !it2.Replaced || it2.Placeholder == "" {
		t.Fatalf("第二轮应替换为占位文本: %+v", it2)
	}
	if t2.Replaced != 1 || t2.Inline != 0 {
		t.Fatalf("第二轮计数应 Replaced=1/Inline=0, 实得 %+v", t2)
	}
	if !strings.Contains(it2.Placeholder, it2.Path) {
		t.Fatalf("占位文本必须含取回路径: %s", it2.Placeholder)
	}
}

// TestSameTurnDuplicatesBothStayInline 两阶段必须**按轮**成立，不能按条目：
// 同一轮里两份相同对象都是「首次」，参考实现逐块独立处理，两份都保持内联。
// 若在轮内循环里就提交标记，第二份会被自己刚写的标记判成后续轮而当场替换——
// 那一轮模型从未完整看到过其中任何一份，正是「关键取舍二」要禁止的动作。
func TestSameTurnDuplicatesBothStayInline(t *testing.T) {
	st, fs := NewState(), newStore()
	b := Blob{Kind: "image", MimeType: "image/png", Ext: ".png",
		Data: bytes.Repeat([]byte("blob-alpha-payload"), 300)}

	// 第一轮：两份都只落盘、不替换；同内容同摘要同键，且只占一个摘要槽位。
	t1 := mustApply(t, Request{Blobs: []Blob{b, b}, State: st, Store: fs})
	for i, it := range t1.Items {
		if it.Replaced || it.Placeholder != "" {
			t.Fatalf("第 1 轮第 %d 份不得被替换（同轮重复即首次）: %+v", i, it)
		}
		if !it.Offloaded || it.Hash != t1.Items[0].Hash || it.Path != t1.Items[0].Path {
			t.Fatalf("同内容应同摘要同键: %+v vs %+v", it, t1.Items[0])
		}
	}
	if t1.Stored != 2 || t1.Replaced != 0 || t1.Inline != 2 {
		t.Fatalf("第 1 轮计数应 Stored=2/Replaced=0/Inline=2, 实得 %+v", t1)
	}
	if st.Len() != 1 {
		t.Fatalf("同一内容只应占一个摘要槽位, state=%d", st.Len())
	}

	// 第二轮：标记已在轮末提交 → 两份都替换，且占位文本逐字节相同（键不变）。
	t2 := mustApply(t, Request{Blobs: []Blob{b, b}, State: st, Store: fs})
	for i, it := range t2.Items {
		if !it.Replaced || it.Placeholder == "" {
			t.Fatalf("第 2 轮第 %d 份应替换: %+v", i, it)
		}
		if it.Placeholder != t2.Items[0].Placeholder {
			t.Fatalf("同内容占位文本须逐字节一致:\n%s\n%s", it.Placeholder, t2.Items[0].Placeholder)
		}
	}
	if t2.Stored != 0 || t2.Replaced != 2 {
		t.Fatalf("第 2 轮不应再落盘, 实得 %+v", t2)
	}
	if len(fs.keys) != 2 {
		t.Fatalf("两轮内共只应写两次落盘面（同轮幂等、跨轮去重）, 实写 %d 次", len(fs.keys))
	}
}

// TestDedupSkipsSecondStore 同一内容跨轮不重复落盘——这既是去重本身，
// 也保证占位文本能逐字节稳定（键不变 → 文本不变 → 提示缓存可命中）。
func TestDedupSkipsSecondStore(t *testing.T) {
	st, fs := NewState(), newStore()
	b := blob("blob-alpha", ".bin", MinBytes+10)

	t1 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})
	t2 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})
	t3 := mustApply(t, Request{Blobs: []Blob{b}, State: st, Store: fs})

	if t1.Items[0].Path != t2.Items[0].Path || t2.Items[0].Path != t3.Items[0].Path {
		t.Fatalf("同一内容应恒落同一键: %s / %s / %s",
			t1.Items[0].Path, t2.Items[0].Path, t3.Items[0].Path)
	}
	if t2.Items[0].Placeholder != t3.Items[0].Placeholder {
		t.Fatalf("占位文本须逐字节稳定:\n%s\n%s", t2.Items[0].Placeholder, t3.Items[0].Placeholder)
	}
	if t2.Stored != 0 || t3.Stored != 0 {
		t.Fatalf("后续轮不得重复落盘, 实得 Stored=%d/%d", t2.Stored, t3.Stored)
	}
	if len(fs.keys) != 1 {
		t.Fatalf("三轮只应写一次落盘面, 实写 %d 次", len(fs.keys))
	}
	if t1.Items[0].Hash == mustApply(t, Request{Blobs: []Blob{blob("blob-beta", ".bin", MinBytes+11)},
		State: NewState(), Store: newStore()}).Items[0].Hash {
		t.Fatalf("不同内容不得同摘要")
	}
}

// TestFailSoftStoreError 未装配落盘面：条目级失败保留原内容，不中断本轮。
func TestFailSoftStoreError(t *testing.T) {
	st := NewState()
	rep := mustApply(t, Request{Blobs: []Blob{blob("image", ".png", MinBytes)}, State: st})
	it := rep.Items[0]
	if it.Replaced || it.Offloaded {
		t.Fatalf("未装配时不得声称已外置: %+v", it)
	}
	if it.Reason != ErrNoStore.Error() {
		t.Fatalf("应留痕未装配, 实得 %q", it.Reason)
	}
	if st.Len() != 0 {
		t.Fatalf("失败时不得标记摘要")
	}
}

// TestFailSoftPutError 落盘失败**不标记**——否则下一轮会放出一个指向从未写入的
// 路径的占位文本（悬空引用），比不外置更糟。
func TestFailSoftPutError(t *testing.T) {
	st, fs := NewState(), newStore()
	fs.failK = "blobs//x" // 必不命中：先算一次真实键
	probe := mustApply(t, Request{Blobs: []Blob{blob("image", ".png", MinBytes)}, State: NewState(), Store: newStore()})
	fs.failK = probe.Items[0].Path

	rep := mustApply(t, Request{Blobs: []Blob{blob("image", ".png", MinBytes)}, State: st, Store: fs})
	if rep.Items[0].Reason == "" || rep.Items[0].Replaced {
		t.Fatalf("落盘失败应留痕且保留内容: %+v", rep.Items[0])
	}
	if st.Len() != 0 {
		t.Fatalf("落盘失败不得标记摘要（防悬空占位）")
	}
	// 第二轮仍不得替换。
	if again := mustApply(t, Request{Blobs: []Blob{blob("image", ".png", MinBytes)}, State: st, Store: fs}); again.Items[0].Replaced {
		t.Fatalf("落盘失败过的对象下一轮不得被替换")
	}
}

// TestFailSoftBadExt 扩展名非法 → 保留内容、留痕，且该轮其余条目不受影响。
func TestFailSoftBadExt(t *testing.T) {
	st, fs := NewState(), newStore()
	rep := mustApply(t, Request{Blobs: []Blob{
		{Kind: "image", Ext: "../escape", Data: bytes.Repeat([]byte("a"), MinBytes)},
		blob("blob-beta", ".bin", MinBytes),
	}, State: st, Store: fs})

	if rep.Items[0].Reason == "" || rep.Items[0].Replaced {
		t.Fatalf("非法扩展名应留痕并保留内容: %+v", rep.Items[0])
	}
	if !rep.Items[1].Offloaded || rep.Items[1].Reason != "" {
		t.Fatalf("同批次其余条目不应受牵连: %+v", rep.Items[1])
	}
	if rep.Stored != 1 || rep.Inline != 2 {
		t.Fatalf("失败不得中断整批, 实得 Stored=%d Inline=%d", rep.Stored, rep.Inline)
	}
}

// TestBatchIndexAlignment 报告条目与请求下标对齐，调用方据此回填消息。
func TestBatchIndexAlignment(t *testing.T) {
	blobs := []Blob{blob("item-01", ".bin", 10), blob("item-02", ".bin", MinBytes), {Kind: "item-03"}}
	rep := mustApply(t, Request{Blobs: blobs, State: NewState(), Store: newStore()})
	if len(rep.Items) != len(blobs) {
		t.Fatalf("条目数应与请求一致, 实得 %d", len(rep.Items))
	}
	for i, it := range rep.Items {
		if it.Index != i || it.Kind != blobs[i].Kind || it.Bytes != len(blobs[i].Data) {
			t.Fatalf("第 %d 条错位: %+v", i, it)
		}
	}
	if rep.Items[2].Reason != "空内容" {
		t.Fatalf("空内容应留痕, 实得 %q", rep.Items[2].Reason)
	}
}

// TestNilStateNeverReplaces 未持有跨轮状态时永远按首次处理：
// 压缩不生效但内容始终完整可见——保守方向。
func TestNilStateNeverReplaces(t *testing.T) {
	fs := newStore()
	b := blob("blob-alpha", ".bin", MinBytes+1)
	for turn := 1; turn <= 2; turn++ {
		rep := mustApply(t, Request{Blobs: []Blob{b}, State: nil, Store: fs})
		if rep.Items[0].Replaced {
			t.Fatalf("无状态第 %d 轮不得替换: %+v", turn, rep.Items[0])
		}
		if !rep.Items[0].Offloaded {
			t.Fatalf("无状态仍应落盘: %+v", rep.Items[0])
		}
	}
	var nilState *State
	if nilState.Seen("x") || nilState.Len() != 0 {
		t.Fatalf("nil State 读侧应安全且恒假")
	}
	nilState.Mark("x") // 不得 panic
}

// TestStorePathContract 落盘键推导是纯函数且逐条可判。
func TestStorePathContract(t *testing.T) {
	p1, err := StorePath("abcdef0123456789", ".png")
	if err != nil {
		t.Fatalf("合法键不应报错: %v", err)
	}
	if p1 != "blobs/ab/abcdef0123456789.png" {
		t.Fatalf("键形态不符: %s", p1)
	}
	if p2, _ := StorePath("abcdef0123456789", "png"); p2 != p1 {
		t.Fatalf("扩展名前导点可选, 实得 %s vs %s", p2, p1)
	}
	if _, err := StorePath("a", ".png"); !errors.Is(err, ErrBadHash) {
		t.Fatalf("摘要过短应拒, 实得 %v", err)
	}
	if _, err := StorePath("abcdef", ".."); !errors.Is(err, ErrBadExt) {
		t.Fatalf("纯点扩展名应拒, 实得 %v", err)
	}
	for _, bad := range []string{"", " ", ".", "..", "a/b", `a\b`, "p n", "a*b"} {
		if _, err := StorePath("abcdef", bad); !errors.Is(err, ErrBadExt) {
			t.Fatalf("扩展名 %q 应拒, 实得 %v", bad, err)
		}
	}
}

// TestSanitizeExtNormalization 扩展名净化：补点、限字符集、允许嵌点。
func TestSanitizeExtNormalization(t *testing.T) {
	got, err := sanitizeExt("  tar.gz ")
	if err != nil || got != ".tar.gz" {
		t.Fatalf("净化结果不符: %q err=%v", got, err)
	}
	if got, err := sanitizeExt("A_b-9"); err != nil || got != ".A_b-9" {
		t.Fatalf("合法字符集应通过: %q err=%v", got, err)
	}
}

// TestTruncatedHash 摘要前缀：短摘要原样返回，长摘要截断。
func TestTruncatedHash(t *testing.T) {
	if got := TruncatedHash("abc"); got != "abc" {
		t.Fatalf("短摘要应原样: %s", got)
	}
	if got := TruncatedHash("abcdef0123456789abcdef"); got != "abcdef012345" {
		t.Fatalf("长摘要应截断: %s", got)
	}
}

// TestStateUnit State 的读写与零值可用性（nil map 自愈）。
func TestStateUnit(t *testing.T) {
	s := &State{}
	if s.Seen("h1") || s.Len() != 0 {
		t.Fatalf("零值 State 应为空")
	}
	s.Mark("h1")
	if !s.Seen("h1") || s.Seen("h2") || s.Len() != 1 {
		t.Fatalf("零值 State 应自愈: %+v", s)
	}
	if NewState().Seen("h1") {
		t.Fatalf("新状态不应已见任何摘要")
	}
}
