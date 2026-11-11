// manifest.go —— api-lifecycle.yaml 的装载与字段级校验。
//
// 用 gopkg.in/yaml.v3 解码（archcheck 已在用的既有依赖，本命令不新增任何
// go.mod 条目），并开 KnownFields：清单里拼错字段名（`lvel:`）必须在装载处
// 变红，而不是被静默忽略后让整条判据空转。

package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	manifestName = "api-lifecycle.yaml"

	LevelExperimental = "Experimental"
	LevelStable       = "Stable"
	LevelDeprecated   = "Deprecated"
)

// levels 分级词表。level 字段必须命中其一——拼错级别名是声明缺陷，
// 不是「未知级别按最松处理」：静默放宽等于替承诺人做决定。
var levels = map[string]bool{
	LevelExperimental: true,
	LevelStable:       true,
	LevelDeprecated:   true,
}

// Entry 清单里一行包登记。
type Entry struct {
	// Pkg 包目录（相对仓库根，如 `sideeffect`、`internal/subhub`、
	// `examples/reference-runtime`）。
	Pkg string `yaml:"pkg"`
	// Level 分级：Experimental / Stable / Deprecated（词表见 levels）。
	Level string `yaml:"level"`
	// Since 定级版本（vX.Y.Z）。记录「从哪个版本起承诺」，供宿主判断
	// 升级跨越了哪些定级点。
	Since string `yaml:"since"`
	// Reason 定级理由。**每条必填**：定级是一份承诺，没有理由的承诺
	// 无从复核，半年后也没人敢动它。
	Reason string `yaml:"reason"`
	// Replacement Deprecated 条目的替代指向，完整符号路径
	// （`github.com/yi-nology/goagent-kernel/<pkg>.<Symbol>`）。
	// 门禁会解析该符号并确认真实存在——指到不存在的符号等于没有迁移路径。
	Replacement string `yaml:"replacement"`
	// FollowsUpstream 门面包跟随的上游 module。非空时门禁校验该包真的
	// import 这个 module——「跟随上游」若指着一个不 import 的上游，
	// 声明就是假的。
	FollowsUpstream string `yaml:"follows_upstream"`
}

// Manifest 清单整体。
type Manifest struct {
	Version  int     `yaml:"version"`
	Packages []Entry `yaml:"packages"`
}

var sinceRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// LoadManifest 读入并解码清单。
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return &m, nil
}

// Validate 字段级判定。集合级判定（覆盖、标记一致、替代指向存在）在
// checkAll 里，这里只判单条自身能不能自洽。
func (m *Manifest) Validate() []string {
	var probs []string
	if m.Version != 1 {
		probs = append(probs, fmt.Sprintf("清单 version = %d，本门禁只认 1（格式演进必须换判据，不能靠旧门禁静默兼容）", m.Version))
	}
	seen := map[string]bool{}
	for _, e := range m.Packages {
		if e.Pkg == "" {
			probs = append(probs, "清单里有一条登记没有 pkg")
			continue
		}
		if seen[e.Pkg] {
			probs = append(probs, e.Pkg+": 清单里重复登记")
		}
		seen[e.Pkg] = true

		if !levels[e.Level] {
			probs = append(probs, e.Pkg+": level "+quote(e.Level)+" 不在词表（Experimental/Stable/Deprecated）")
		}
		if !sinceRe.MatchString(e.Since) {
			probs = append(probs, e.Pkg+": since "+quote(e.Since)+" 不是 vX.Y.Z 形态")
		}
		if e.Reason == "" {
			probs = append(probs, e.Pkg+": reason 为空——定级理由每条必填，没有理由的承诺无从复核")
		}
		if e.Level == LevelDeprecated && e.Replacement == "" {
			probs = append(probs, e.Pkg+": Deprecated 必须给 replacement（完整符号路径），否则调用方没有迁移路径")
		}
		if e.Level != LevelDeprecated && e.Replacement != "" {
			probs = append(probs, e.Pkg+": 只有 Deprecated 条目才能带 replacement")
		}
	}
	return probs
}

func quote(s string) string { return fmt.Sprintf("%q", s) }
