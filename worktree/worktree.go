// Package worktree 工作区隔离的**声明面与冲突判据**（不碰真实文件系统）。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 为什么是「声明面」而不是「实现」
//
// 本仓至今不碰 FS 语义：建目录、挂载、chroot、git worktree 都是宿主与执行后端
// 的事。但**并行工作区之间会不会互相踩**这件事没有人判——两个 agent 各自
// 「在自己的目录里工作」，而它们的可写区完全重叠时，任何一边的写入都会
// 让另一边的结论失效，报告里却只有两份「各自成功」。
//
// 故本包只做三件可机械判定的事：
//
//  1. **声明**：每个工作区声明它的可写区与只读区（@@Workspace@@）；
//  2. **自检**：声明本身不能自相矛盾（同一工作区里既只读又可写、路径穿出
//     工作区、绝对路径……）；
//  3. **冲突判据**：两个工作区的**可写区重叠**即冲突（@@Plan.Conflicts@@）。
//     只读区重叠不算——多个人读同一份文件没有危害，而**写**没有第二份真相。
//
// # fail-closed 的方向
//
// @@CanWrite@@ / @@CanRead@@ 在「没有任何声明命中」时返回 false。默认允许会让
// 一个漏写的声明变成**全域可写**，而漏写在配置里看不出来（它只是「少了一行」）。
//
// # 不支持通配（写明的局限）
//
// 只支持三种形态：**目录前缀**（以 / 结尾）、**后缀**（以 . 开头）、
// **精确路径**。不支持 * 与 **：它们的语义（是否跨层、是否匹配目录本身）
// 各实现不同，一旦进了契约就会出现「按 A 的理解写、按 B 的理解判」的夹缝。
// 需要更强表达力时，宿主自己展开成多条精确声明——展开是宿主的活，判定归本包。
package worktree

import (
	"fmt"
	"slices"
	"strings"

	"github.com/oekit/pkg/errors"
)

// Workspace 一个并行工作区的路径声明。
type Workspace struct {
	// ID 工作区标识（唯一）。
	ID string
	// ReadOnly 只读区（目录前缀 / 后缀 / 精确路径）。
	ReadOnly []string
	// Writable 可写区（形态同上）。可以为空——纯只读工作区（如审查助手）是
	// 合法且常见的形态，此时任何写操作都被拒。
	Writable []string
}

// Conflict 两个工作区之间的可写区冲突。
type Conflict struct {
	// A, B 冲突的两个工作区 id（按字典序，A < B）。
	A, B string
	// PatternA, PatternB 命中的声明项（让维护者知道该改哪一条）。
	PatternA, PatternB string
	// Why 人读的说明。
	Why string
}

// Plan 一组工作区的声明。
type Plan struct {
	workspaces []Workspace
}

// New 建计划并做**声明自检**（任一不满足即报错，不静默修正）。
//
// 自检项：
//
//   - ID 非空且唯一；
//   - 声明项非空串（空串会匹配一切）；
//   - 声明项不得以 / 开头（绝对路径让「分区」这件事失去意义）；
//   - 声明项不得含 ..（穿出工作区）；
//   - **同一工作区里可写区与只读区不得重叠**（既只读又可写是自相矛盾，
//     而两边的读取者会得到不同的结论）。
func New(ws []Workspace) (*Plan, error) {
	seen := map[string]bool{}
	out := make([]Workspace, 0, len(ws))
	for _, w := range ws {
		id := strings.TrimSpace(w.ID)
		if id == "" {
			return nil, errors.New("worktree: 工作区 ID 为空（冲突报告无从定位）")
		}
		if seen[id] {
			return nil, fmt.Errorf("worktree: 工作区 ID %q 重复", id)
		}
		seen[id] = true
		for _, group := range [][]string{w.ReadOnly, w.Writable} {
			for _, p := range group {
				if err := validatePattern(id, p); err != nil {
					return nil, err
				}
			}
		}
		for _, rp := range w.ReadOnly {
			for _, wp := range w.Writable {
				if patternsOverlap(rp, wp) {
					return nil, fmt.Errorf("worktree: 工作区 %q 的只读项 %q 与可写项 %q 重叠——"+
						"同一份路径既只读又可写，两边的读者会得出不同结论", id, rp, wp)
				}
			}
		}
		w.ID = id
		out = append(out, w)
	}
	slices.SortStableFunc(out, func(a, b Workspace) int { return strings.Compare(a.ID, b.ID) })
	return &Plan{workspaces: out}, nil
}

// validatePattern 判一条声明项是否可用。
func validatePattern(id, p string) error {
	q := strings.TrimSpace(p)
	if q == "" {
		return fmt.Errorf("worktree: 工作区 %q 有空声明项（空串会匹配一切）", id)
	}
	if strings.HasPrefix(q, "/") {
		return fmt.Errorf("worktree: 工作区 %q 的声明项 %q 是绝对路径（分区失去意义）", id, p)
	}
	if strings.Contains(q, "..") {
		return fmt.Errorf("worktree: 工作区 %q 的声明项 %q 含 ..（可穿出工作区）", id, p)
	}
	return nil
}

// Workspaces 返回全部工作区（已按 ID 排序）。
func (p *Plan) Workspaces() []Workspace { return p.workspaces }

// Conflicts 判**可写区重叠**，按 (A, B) 排序返回（确定性）。
//
// 只判写：读的重叠没有危害——多个人读同一份文件不会产生第二份真相。
func (p *Plan) Conflicts() []Conflict {
	var out []Conflict
	for i := range p.workspaces {
		for j := i + 1; j < len(p.workspaces); j++ {
			a, b := p.workspaces[i], p.workspaces[j]
			for _, pa := range a.Writable {
				hit := ""
				for _, pb := range b.Writable {
					if patternsOverlap(pa, pb) {
						hit = pb
						break
					}
				}
				if hit == "" {
					continue
				}
				out = append(out, Conflict{
					A: a.ID, B: b.ID, PatternA: pa, PatternB: hit,
					Why: "两个并行工作区的可写区重叠：任何一边的写入都会让另一边的结论失效",
				})
				break // 同一对工作区只报一条：维护者要改的是「这对工作区不能同时跑」
			}
		}
	}
	return out
}

// CanWrite 判该工作区能否写这个路径。
//
// 没有命中即 false：默认允许会让一个漏写的声明变成全域可写，
// 而漏写在配置里看不出来（它只是「少了一行」）。
func (w Workspace) CanWrite(path string) bool {
	for _, p := range w.Writable {
		if matchPattern(p, path) {
			return true
		}
	}
	return false
}

// CanRead 判该工作区能否读这个路径（可写区天然可读）。
func (w Workspace) CanRead(path string) bool {
	if w.CanWrite(path) {
		return true
	}
	for _, p := range w.ReadOnly {
		if matchPattern(p, path) {
			return true
		}
	}
	return false
}

// matchPattern 判一条声明项是否覆盖某个路径。
func matchPattern(pattern, path string) bool {
	p := strings.TrimSpace(pattern)
	q := strings.ToLower(strings.TrimPrefix(path, "./"))
	p = strings.ToLower(p)
	switch {
	case p == "":
		return false
	case strings.HasSuffix(p, "/"):
		return strings.HasPrefix(q, p) || q == strings.TrimSuffix(p, "/")
	case strings.HasPrefix(p, "."):
		return strings.HasSuffix(q, p)
	default:
		return q == p || strings.HasPrefix(q, p+"/")
	}
}

// patternsOverlap 判两条声明项覆盖的路径是否有交集。
//
// 三种形态两两组合都归结为「前缀包含」：目录前缀是标准前缀，后缀之间只要
// 相等就必然重叠（不同的后缀可以指向同一路径吗？可以——"a.md" 同时以
// ".md" 与 "d" 结尾不成立，故后缀之间只有相等才重叠），精确路径与目录前缀
// 按前缀判。这套归约让判定不需要展开成路径集合。
func patternsOverlap(a, b string) bool {
	x := strings.ToLower(strings.TrimSpace(a))
	y := strings.ToLower(strings.TrimSpace(b))
	if x == "" || y == "" {
		return false
	}
	xa, ya := strings.HasPrefix(x, "."), strings.HasPrefix(y, ".")
	switch {
	case xa && ya:
		return x == y
	case xa:
		// y 是目录前缀或精确路径：只要 y 覆盖到的路径里存在以 x 结尾的即重叠。
		// 保守取法：y 是前缀且未限定后缀 ⇒ 可以出现任意后缀，判重叠。
		return true
	case ya:
		return true
	}
	if strings.HasSuffix(x, "/") && strings.HasSuffix(y, "/") {
		return strings.HasPrefix(x, y) || strings.HasPrefix(y, x)
	}
	if strings.HasSuffix(x, "/") {
		return strings.HasPrefix(y, x) || y+"/" == x
	}
	if strings.HasSuffix(y, "/") {
		return strings.HasPrefix(x, y) || x+"/" == y
	}
	return x == y
}
