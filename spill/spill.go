// Package spill 溢出存档平面单源（批三百零一）：budgetTool 截断落盘（engine/runner）/
// read_spill 工具取回（engine/tools）/控制台取回面（biz/handler）/写侧清理共用同一套
// 文件名净化、会话隔离与分页语义——LLM 面与控制台面各写一份必漂移（会话隔离三重守卫
// 的判据只应存在一处）。纯 stdlib 叶子层（kbgroup 先例）。
//
// 提示文案契约单源：截断提示文本由 TruncHint 生成、ParseHint 解析——前端
// web/src/utils/timeline/mappers.ts spillHintOf 按同一契约解析（双侧钉档：文案变更
// 须同步该文件与其单测）。
package spill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oekit/pkg/errors"
)

// 契约锚点（与前端 spillHintOf 同判据）：截断标记「输出超预算已截断」+ 定位符
// 「spill:<name>」（name 限定安全字符集，解析面与净化面同集）。
var hintLocatorRe = regexp.MustCompile(`spill:([A-Za-z0-9._-]+)`)

// TruncNote 参数：省略字节数/全长/存档定位与路径（retrievable=read_spill 已授予）。
type TruncNote struct {
	Omit        int
	Total       int
	Retrievable bool
	Name        string // 存档文件名（spill 定位符）
	Path        string // 服务器路径（retrievable=false 时的落盘提示）
}

// TruncHint 生成截断提示（自 engine/runner/output.go 迁出逐字保契约）。
func TruncHint(n TruncNote) string {
	if n.Name == "" && n.Path == "" {
		return fmt.Sprintf("\n…【输出超预算已截断：省略中段 %d 字节（全长 %d），建议改用工具的过滤/分页参数缩小范围重查】…\n", n.Omit, n.Total)
	}
	if n.Name != "" && n.Retrievable {
		return fmt.Sprintf("\n…【输出超预算已截断：省略中段 %d 字节（全长 %d）；全文已存档 spill:%s——需要中段原文时用 read_spill 工具分页取回（name=%q，offset/limit 续页）；优先仍建议用采集工具的过滤/分页参数缩小范围重查】…\n", n.Omit, n.Total, n.Name, n.Name)
	}
	return fmt.Sprintf("\n…【输出超预算已截断：省略中段 %d 字节（全长 %d）；全文已落盘: %s；建议改用工具的过滤/分页参数缩小范围重查】…\n", n.Omit, n.Total, n.Path)
}

// ParseHint 解析截断提示：返回定位符与截断旗标（无提示=零值）。Go 侧消费者
// （tool_result 事件结构化字段）与前端 spillHintOf 同判据——改契约双侧同步。
func ParseHint(text string) (locator string, truncated bool) {
	if !strings.Contains(text, "输出超预算已截断") {
		return "", false
	}
	if m := hintLocatorRe.FindStringSubmatch(text); len(m) == 2 {
		return m[1], true
	}
	return "", true
}

// Item 会话存档清单条目（控制台取回面列表行）。
type Item struct {
	Name    string
	Bytes   int64
	ModTime time.Time
}

// ErrBadName 定位符非法（含分隔符/点段/空名）。
var ErrBadName = errors.New("定位符非法（只允许本会话存档目录下的裸文件名）")

// sanitizeName 定位符净化（read_spill 三重守卫之一/二）：单段裸文件名，
// filepath.Base 剥目录，含分隔符/点段即拒——LLM 面与控制台面同一判据。
func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	base := filepath.Base(name)
	if name == "" || base != name || base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
		return "", ErrBadName
	}
	return base, nil
}

// sessionDir 会话目录拼装+前缀守卫（三重守卫之三）：净化后理论出不去，仍显式
// 校验落点（防御纵深，read_spill 原语义）。
func sessionDir(root, sessionID string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("spill 面未装配（mcp.spill_dir 空），无存档可取")
	}
	sess := strings.TrimSpace(sessionID)
	if sess == "" {
		return "", errors.New("无法归属会话（缺 session 标识），拒绝执行")
	}
	base := filepath.Base(sess)
	if base != sess || strings.ContainsAny(sess, `/\`) {
		return "", errors.New("会话标识非法")
	}
	return filepath.Join(root, sess), nil
}

// resolve 会话隔离取文件路径（净化+前缀校验）。
func resolve(root, sessionID, name string) (dir, p string, err error) {
	dir, err = sessionDir(root, sessionID)
	if err != nil {
		return "", "", err
	}
	base, err := sanitizeName(name)
	if err != nil {
		return "", "", err
	}
	p = filepath.Join(dir, base)
	if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("定位符越界（不在本会话存档目录内）: %q", name)
	}
	return dir, p, nil
}

// List 会话存档清单（mtime 降序，最新在前）。
func List(root, sessionID string) ([]Item, error) {
	dir, err := sessionDir(root, sessionID)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Item{}, nil // 无存档=空清单不是错误
		}
		return nil, err
	}
	items := make([]Item, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		items = append(items, Item{Name: e.Name(), Bytes: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ModTime.After(items[j].ModTime) })
	return items, nil
}

// Page 分页取回结果（read_spill 回包与控制台取回面同构）。
type Page struct {
	Name       string `json:"name"`
	Offset     int64  `json:"offset"`
	Bytes      int    `json:"bytes"`
	Total      int64  `json:"total"`
	EOF        bool   `json:"eof"`
	Content    string `json:"content"`
	NextOffset int64  `json:"next_offset,omitempty"`
}

// ReadPage 会话隔离分页取回：off<0 视作 0、>total 收到 total；limit<=0 用缺省，
// 超上限收上限（LLM 面与控制台面同一缺省/上限）。
func ReadPage(root, sessionID, name string, off int64, limit int) (Page, error) {
	_, p, err := resolve(root, sessionID, name)
	if err != nil {
		return Page{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return Page{}, errors.Wrap(err, "存档不存在或不可读")
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Page{}, errors.Wrap(err, "存档状态读取失败")
	}
	total := st.Size()
	if off < 0 {
		off = 0
	}
	if off > total {
		off = total
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	n := total - off
	if n > int64(limit) {
		n = int64(limit)
	}
	buf := make([]byte, n)
	got, rerr := f.ReadAt(buf, off)
	if rerr != nil && got == 0 {
		return Page{}, errors.Wrap(rerr, "存档读取失败")
	}
	pg := Page{
		Name: filepath.Base(p), Offset: off, Bytes: got, Total: total,
		EOF: off+int64(got) >= total, Content: string(buf[:got]),
	}
	if !pg.EOF {
		pg.NextOffset = off + int64(got)
	}
	return pg, nil
}

// 单次取回字节缺省/上限（自 read_spill 迁出：取回文本直接进上下文，上限与输出
// 预算同数量级——防「分页取回」退化成「全文重塞」）。
const (
	DefaultLimit = 8192
	MaxLimit     = 16384
)

// Prune 写侧清理（best-effort，清理失败不影响本次存档）：keepNewest 数护栏（≤0=不按数）
// + ttl 按龄（0=不按龄）——「按龄清理」挂账的执行体；无后台任务纪律不变（写侧顺手清）。
func Prune(dir string, keepNewest int, ttl time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type mf struct {
		name string
		mod  time.Time
	}
	files := make([]mf, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, mf{e.Name(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) }) // 最旧在前

	// 按龄：超龄即删（keepNewest 数护栏另算，不互相豁免——量纲不同各管各）
	if ttl > 0 {
		cut := time.Now().Add(-ttl)
		kept := files[:0]
		for _, f := range files {
			if f.mod.Before(cut) {
				_ = os.Remove(filepath.Join(dir, f.name))
				continue
			}
			kept = append(kept, f)
		}
		files = kept
	}
	// 按数：保留最新 keepNewest 份
	if keepNewest > 0 && len(files) > keepNewest {
		for _, f := range files[:len(files)-keepNewest] {
			_ = os.Remove(filepath.Join(dir, f.name))
		}
	}
}
