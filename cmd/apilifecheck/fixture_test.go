// fixture_test.go —— 合成迷你仓夹具：三包（含嵌套与 cmd 排除项）+ 清单 +
// 两份文档。判定口径见各 *_test.go；夹具是判据的示例，不带任何真实领域词。

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// miniManifest 三包清单：alpha（Stable，带哨兵与错误类型）、beta（Experimental，
// 无哨兵）、internal/deep（嵌套生产包）。cmd/tool 用来验证排除判定。
const miniManifest = `version: 1
packages:
  - pkg: alpha
    level: Stable
    since: v0.33.0
    reason: 夹具——契约面示例
  - pkg: beta
    level: Experimental
    since: v0.33.0
    reason: 夹具——仍在演进示例
  - pkg: internal/deep
    level: Experimental
    since: v0.33.0
    reason: 夹具——嵌套包覆盖判定
`

// miniErrorTable 与 alpha.go 的哨兵/错误类型集合一致。
const miniErrorTable = `# 错误语义总表（夹具）

## 哨兵总表（按包）

### alpha

| 哨兵 | 触发条件 | 调用方处置 |
|---|---|---|
| ` + "`ErrBoom`" + ` | 爆了 | 修声明 |
| ` + "`ErrWrap`" + ` | 包装链爆了 | 修声明 |

## 结构化错误类型（errors.As 判定）

| 符号 | 含义 | 调用方处置 |
|---|---|---|
| ` + "`alpha.BoomErr`" + ` | 爆炸现场 | 读 Code |
`

const miniAPICompat = `# 兼容策略（夹具）

api-lifecycle.yaml 登记 **3** 个包（机器校验块）。
`

const miniUsage = `# 使用指南（夹具）

> | 口径 | 数字 | 是什么 |
> |---|---|---|
> | 生命周期登记 | **3** 个包 | ` + "`api-lifecycle.yaml`" + ` 的 ` + "`packages`" + ` 段（机器校验）|
`

// alpha.go：带标记的 Stable 包，含哨兵（var 块）、显式 error 类型变量、
// 别名 import、非哨兵变量、指针接收者错误类型。
const miniAlpha = `// Package alpha 夹具契约面。
//
// 生命周期: Stable（v0.33.0 定级）——只允许 additive 变更。
package alpha

import (
	oerr "github.com/oekit/pkg/errors"
)

var (
	// ErrBoom 哨兵。
	ErrBoom = oerr.New("boom")
	// ErrWrap 显式 error 类型声明也算哨兵。
	ErrWrap error = oerr.New("wrap")
	// notSentinel 非 errors.New 的普通变量，不得计入。
	notSentinel = "nope"
)

// BoomErr 结构化错误类型（指针接收者）。
type BoomErr struct{ Code int }

func (e *BoomErr) Error() string { return "boom" }
`

// beta.go：Experimental 包，无哨兵。
const miniBeta = `// Package beta 夹具演进面。
//
// 生命周期: Experimental（v0.33.0 定级）——仍会改删。
package beta

func Beta() int { return 1 }
`

// deep.go：嵌套生产包（internal/deep），带全角冒号标记行。
const miniDeep = `// Package deep 夹具嵌套包。
//
// 生命周期：Experimental（v0.33.0 定级）——全角冒号同样命中。
package deep

func Deep() int { return 2 }
`

// miniCmd：cmd/ 下的命令（无标记也不该被判）。
const miniCmd = `// Command tool 夹具命令。
package main

func main() {}
`

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeMiniRepo 摆一份全绿的迷你仓，返回其根目录。
func writeMiniRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "api-lifecycle.yaml", miniManifest)
	writeFile(t, root, "docs/error-semantics.md", miniErrorTable)
	writeFile(t, root, "docs/api-compat.md", miniAPICompat)
	writeFile(t, root, "docs/USAGE.md", miniUsage)
	writeFile(t, root, "alpha/alpha.go", miniAlpha)
	writeFile(t, root, "beta/beta.go", miniBeta)
	writeFile(t, root, "internal/deep/deep.go", miniDeep)
	writeFile(t, root, "cmd/tool/main.go", miniCmd)
	return root
}
