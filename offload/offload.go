// Package offload 内联大对象外置：把躺在消息历史里的二进制本体换成占位文本。
//
// ## 为什么需要这个包
//
// 大对象（base64 图片/音频/归档）随消息进历史后，会在**每一轮**被原样重发。
// 代价与对象大小成正比、与轮数相乘，而模型在该对象上真正需要的信息量通常只有
// 一次——第一轮看完之后，剩下的 N-1 份纯属浪费。本包把本体外置为内容寻址的落盘对象，
// 历史里只留一段占位文本，模型需要时按路径取回。
//
// 与 spill 是**姊妹机制而非重复**：spill 管「输出超预算被截断」（文本尾部溢出），
// offload 管「二进制本体躺在消息里」（输入侧上下文计量）。二者判定时机、
// 触发条件、替换语义都不同，故不合并。
//
// ## 关键取舍一：阈值——小对象外置必亏
//
// 占位文本本身有体量（缺省形态 191 字节，见 placeholder.go）。对一个小对象，
// 「写占位 + 多一次取回往返」的总成本**高于**直接内联，外置反而是负优化。
// 故低于 MinBytes 的对象一律原样保留；阈值取 4 KiB 远高于占位体量，
// 保证跨过阈值后收益一定为正。
//
// ## 关键取舍二：两阶段——首次绝不替换（正确性核心）
//
// 首次见到某对象时：落盘 + 标记，**但内容原地保留**。只有后续轮才换成占位文本。
//
// 理由只有一个，但足够致命：若首次就替换，模型这一轮**从未真正看到过该对象**，
// 后续轮的占位文本又把它指向一个「模型没看过的东西」——等于凭空丢失输入。
// 两阶段让「至少完整出现过一次」成为硬保证，压缩只发生在安全的那一侧。
//
// 连带一条：首次落盘失败时**不得标记**。否则下一轮会放出一个指向从未写入的路径的
// 占位文本（悬空引用），比不外置更糟。
//
// 连带二条：**标记按轮提交，不按条目提交**。「首次」的判据只能是「进入本轮时
// 已可见的标记」。若在轮内循环里就写 State，同一份内容出现两次时，第二份会被
// 自己刚写的标记判成「后续轮」而当场替换——那一轮模型从未完整看到过其中任何一份，
// 恰恰是上面那条保证要禁止的动作。参考实现逐块独立处理（标记挂在块自身上），
// 同一消息里两份相同图片都保持内联，本包与之对齐。
//
// ## 关键取舍三：状态归调用方
//
// 「已见过哪些摘要」必须跨轮存活，但**不属于本包**：它随会话存储、随宿主重启策略
// 变化。本包除调用方传入的 *State 与 Store 外无任何状态，纯函数式，
// 故可被任意编排层并发复用而不串扰。
//
// ## 落盘面是接缝不是实现
//
// 内核不碰存储布局（与 checkpoint.Store、contract 同一纪律）：Store 由宿主实现，
// 本包只推导一个**逻辑键** StorePath 并交给它落盘。宿主须保证在 State 存活期内
// 该键可取回——本包不负责保留期与清理（spill 侧有 Prune，offload 侧的回收由宿主决定）。
package offload

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/oekit/pkg/errors"
)

// MinBytes 外置阈值（缺省 4 KiB）。低于此尺寸的对象原样内联——占位文本的体量
// 加上一次取回往返的成本高于直接内联，小对象外置是负优化。
// Request.MinBytes 可逐调用覆盖（≤0 时取本缺省）。
const MinBytes = 4096

// 派生哨兵。均为**条目级**失败：触发时该条目保持原内容内联，不影响本轮其余条目。
var (
	// ErrNoStore 未装配落盘面（Request.Store 为 nil），无从外置。
	ErrNoStore = errors.New("未装配外置落盘面，无法外置")

	// ErrBadExt 扩展名非法。扩展名会拼进逻辑键，必须先净化——
	// 未经净化的扩展名可以让同一份内容落进不同键，使去重与占位复用同时失效。
	ErrBadExt = errors.New("外置扩展名非法（只允许字母数字与短横线/下划线）")

	// ErrBadHash 摘要长度不足，无法推导分片目录。
	ErrBadHash = errors.New("内容摘要非法（至少 2 位十六进制）")
)

// State 已外置对象的摘要集合（**调用方持有**，须跨轮存活以兑现第二轮起的压缩）。
//
// 零值与 nil 均可用：nil 视作「什么都没见过」，后果是每轮都按首次处理——
// 内容永远完整可见、只是省不下上下文。保守方向，永不丢输入。
//
// 并发安全：编排可能从多个环节并发推进，共享同一个 State 是常态而非意外。
// 未经保护的 map 在并发读写下是**不可 recover 的致命错误**，故这里加锁，
// 与 checkpoint.SeqGate / budget.Meter / tooldisclose.Catalog 同一口径。
type State struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewState 构造空状态。
func NewState() *State { return &State{seen: make(map[string]struct{})} }

// Seen 报告该摘要此前是否已外置（两阶段判据）。
func (s *State) Seen(hash string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[hash]
	return ok
}

// Mark 记入摘要。调用方须仅在**落盘成功后**调用（见包注释「关键取舍二」的连带约束）。
func (s *State) Mark(hash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	s.seen[hash] = struct{}{}
}

// Len 已记入的摘要数。
func (s *State) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// Blob 一条待处置的内联对象。
type Blob struct {
	// Kind 对象类型（如 image/attachment）。参与占位文本，同一对象跨轮须给同一值，
	// 否则占位文本不可复用、缓存不命中。
	Kind string

	// MimeType 媒体类型；空则占位文本省略该字段。
	MimeType string

	// Ext 落盘扩展名（调用方给，内核不猜——猜错会写出一个后端解不开的键）。
	// 不给时用 ExtForMimeType(b.MimeType) 推导：那是本包的单源实现，宿主各写
	// 一套映射会让同一份内容在不同宿主上落进不同的键（见 mimeext.go）。
	Ext string

	// Data 原始字节。
	Data []byte
}

// Store 外置落盘边界（唯一接缝；宿主实现）。
//
// 实现须保证：
//   - Put 对同一 relPath 重复调用**幂等**（内容寻址下重复写必然是同一份字节）；
//   - relPath 是逻辑键而非本机路径，宿主自行决定落点；
//   - 在 State 存活期内该键可取回（保留期与回收由宿主负责，本包不清理）。
type Store interface {
	Put(relPath string, data []byte) error
}

// Request 一轮的外置请求。
type Request struct {
	// Blobs 本轮内联的对象（按调用方顺序处理，互不影响）。
	Blobs []Blob

	// State 跨轮状态；nil 时每轮都按首次处理（内容始终完整，压缩不生效）。
	State *State

	// Store 落盘面；nil 时全部条目保持内联并给出 ErrNoStore 留痕。
	Store Store

	// MinBytes 覆盖缺省阈值；≤0 取 MinBytes。
	MinBytes int

	// RetrieveHint 占位文本里的取回指引（宿主自述其取回方式）；空用缺省指引。
	RetrieveHint string
}

// Item 单条对象的处置结果。
type Item struct {
	// Index 在 Request.Blobs 中的下标，调用方据此回填消息。
	Index int

	// Kind / Hash / Path 该对象的类型、全量摘要、逻辑落盘键。
	Kind string
	Hash string
	Path string

	// Bytes 原始字节数。
	Bytes int

	// Offloaded 该对象已有落盘副本（本次写的或此前写的）。
	Offloaded bool

	// Replaced 是否已用占位文本替换内容。
	//
	// **仅在后续轮为真**：首次见到时恒为 false，内容必须原地保留
	// （见包注释「关键取舍二」）。把它做成可判定的字段而不是隐含约定，
	// 是为了让「首轮替换」这类错误在测试里直接暴露。
	Replaced bool

	// Placeholder 占位文本；仅在 Replaced 为真时非空。
	Placeholder string

	// Reason 未处置/保持内联的原因（空=按预期处置）；仅供留痕，不可据以重试。
	Reason string
}

// Report 一轮的外置报告（条目数与 Blobs 对齐）。
type Report struct {
	Items []Item

	// Stored 本次实际落盘的条数（去重的直接证据：同内容第二轮不再写）。
	Stored int

	// Replaced 本轮替换为占位文本的条数。
	Replaced int

	// Inline 保持原内容内联的条数（含低于阈值与条目级失败）。
	Inline int
}

// Apply 处置一轮的内联对象。
//
// 签名**不返回 error**——这是刻意的：条目级失败在机制上就无法中断本轮，
// 调用方不存在「忘了处理 err 于是整轮报废」的可能。失败一律落到 Item.Reason
// 与 Report.Inline 上，内容原样保留。
func Apply(req Request) Report {
	rep := Report{Items: make([]Item, 0, len(req.Blobs))}
	threshold := req.MinBytes
	if threshold <= 0 {
		threshold = MinBytes
	}
	// fresh 收录**本轮内**新标记的摘要，循环结束后才统一提交给 State。
	// 为什么延迟提交：两阶段判据必须以「进入本轮时已可见的标记」为准，否则
	// 同一轮的第二份相同对象会被自己刚写的标记判成后续轮而当场替换
	// （见包注释「关键取舍二」的连带二条）。
	fresh := make(map[string]struct{}, len(req.Blobs))
	for i, b := range req.Blobs {
		it := Item{Index: i, Kind: b.Kind, Bytes: len(b.Data)}
		storedNow := false
		switch {
		case len(b.Data) == 0:
			it.Reason = "空内容"
		case len(b.Data) < threshold:
			it.Reason = "低于外置阈值"
		default:
			storedNow = applyOne(&it, b, req, fresh)
		}
		if it.Replaced {
			rep.Replaced++
		} else {
			rep.Inline++
		}
		if storedNow {
			rep.Stored++
		}
		rep.Items = append(rep.Items, it)
	}
	// 本轮标记统一提交：调用方的**下一次** Apply 才看得到，从而兑现「按轮」语义。
	// State 为 nil 时 Mark 是空操作——无状态即每轮按首次处理，保守方向不变。
	for h := range fresh {
		req.State.Mark(h)
	}
	return rep
}

// applyOne 单条对象的主流程（阈值判定已在外层完成）。返回本次是否实际落了盘。
//
// fresh 是**本轮内**已标记的摘要集合（尚未提交给 State）。命中它说明这份内容在
// 本轮早些时候已落过盘，但模型这一轮仍未在历史里见过任何一份被替换，故仍按
// 「首次」处理并原样保留（与参考实现逐块独立的语义一致）。
func applyOne(it *Item, b Blob, req Request, fresh map[string]struct{}) (storedNow bool) {
	sum := sha256.Sum256(b.Data)
	it.Hash = hex.EncodeToString(sum[:])

	// 键推导内含扩展名净化：一次调用即完成「校验 + 推导」，避免两处判据漂移。
	rel, err := StorePath(it.Hash, b.Ext)
	if err != nil {
		it.Reason = err.Error()
		return false
	}
	it.Path = rel

	// 本轮内刚标记过的一律不算「后续轮」：那是本轮第一次见到它。
	if _, markedNow := fresh[it.Hash]; !markedNow && req.State.Seen(it.Hash) {
		// 后续轮：内容此前已完整出现在历史里（两阶段保证），此刻替换是安全的。
		// 不重复落盘——内容寻址下重写同一键是纯浪费，也正是去重的收益所在。
		it.Offloaded = true
		it.Replaced = true
		it.Placeholder = Render(Ref{
			Kind:     b.Kind,
			ID:       TruncatedHash(it.Hash),
			Path:     rel,
			Bytes:    len(b.Data),
			MimeType: b.MimeType,
		}, req.RetrieveHint)
		return false
	}

	if req.Store == nil {
		it.Reason = ErrNoStore.Error()
		return false
	}
	if err := req.Store.Put(rel, b.Data); err != nil {
		// 落盘失败**不标记**：标记了下一轮就会放出指向空路径的占位文本。
		it.Reason = err.Error()
		return false
	}
	// 同轮重复内容仍各自落盘一次（Put 内容寻址、幂等），与参考实现逐块上传一致；
	// 去重收益体现在**跨轮**——第二轮起本分支不再走。不在本轮内做跨条目去重：
	// 那要另立一套机制换来的只是一次幂等写，而键与占位文本的稳定性并不依赖它。
	fresh[it.Hash] = struct{}{}
	it.Offloaded = true
	// Replaced 恒为 false：首次见到必须让模型看到本体。
	return true
}

// sanitizeExt 扩展名净化：补前导点、限字符集、拒分隔符与纯点串。
// 返回带点形态（如 ".png"），便于直接拼键。
func sanitizeExt(ext string) (string, error) {
	e := strings.TrimSpace(ext)
	e = strings.TrimPrefix(e, ".")
	if e == "" || strings.Trim(e, ".") == "" {
		return "", ErrBadExt
	}
	if strings.ContainsAny(e, `/\`) {
		return "", ErrBadExt
	}
	for _, r := range e {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return "", ErrBadExt
		}
	}
	return "." + e, nil
}

// StorePath 由内容摘要与扩展名推导逻辑落盘键：blobs/<前两位>/<摘要><扩展名>。
//
// 纯函数，且**同一 (hash, ext) 恒得同一键**——这是去重的全部机制：键一致，
// 占位文本才可能逐字节一致，历史前缀才能命中提示缓存。按摘要前两位分片只是
// 避免单目录堆积，宿主可忽略目录层级自行映射。
//
// 返回的键以 path 分隔（与宿主无关的逻辑键，不是本机文件路径）。
func StorePath(hash, ext string) (string, error) {
	h := strings.TrimSpace(hash)
	if len(h) < 2 {
		return "", ErrBadHash
	}
	e, err := sanitizeExt(ext)
	if err != nil {
		return "", err
	}
	return "blobs/" + h[:2] + "/" + h + e, nil
}

// TruncatedHash 取占位文本里暴露的摘要前缀。
//
// 为什么只暴露前缀：全量摘要进了消息就等于给内容做了一次可跨库比对的指纹，
// 没必要让每一轮都带它在上下文里飘；前缀够用于人眼定位与占位文本复用。
func TruncatedHash(hash string) string {
	const keep = 12
	if len(hash) <= keep {
		return hash
	}
	return hash[:keep]
}
