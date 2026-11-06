// Package cmdpolicy —— 分词面（判据见 cmdpolicy.go）。
//
// 分词与判据分成两个文件，不是风格选择：判据（前缀规则 / 三态裁决 / 样例自检）
// 与分词（shell 引号与转义口径）是**两个各自可独立出错的判定面**。
// 分开后各自的行数与测试都能单独看；合在一起时，分词那一段只是「判据文件的头尾」，
// 改一次转义口径要在判据的散文里翻找，而它恰恰是最容易写错的那一段。

package cmdpolicy

import (
	"strings"

	"github.com/oekit/pkg/errors"
)

// Split 把命令行按 shell 口径分词（单引号内原样、双引号内支持反斜杠转义）。
//
// 本包**不执行**命令，分词只为判定；不做变量展开、不做命令替换——
// 那两件事一旦做，判据就会被「展开后的样子」欺骗（`$RM -rf` 在判定时展开，
// 与真正执行时的展开时点不同）。这里的立场是：**判据只看字面**，
// 需要展开的宿主应先把命令展开成 argv 再调 Decide。
func Split(cmdline string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inWord  bool
		quote   string // "" 表示不在引号内；"'" 或 "\"" 表示当前引号种类
		escaped bool
	)
	flush := func() {
		if inWord {
			out = append(out, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for _, r := range cmdline {
		s := string(r)
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
			inWord = true
		case quote == "'":
			// 单引号内一切原样（含反斜杠）——与 shell 口径一致。
			if s == "'" {
				quote = ""
			} else {
				cur.WriteRune(r)
			}
		case quote == "\"":
			switch s {
			case "\"":
				quote = ""
			case "\\":
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case s == "'" || s == "\"":
			quote = s
			inWord = true
		case s == "\\":
			escaped = true
		case s == " " || s == "\t" || s == "\n":
			flush()
		case s == ";" || s == "|" || s == "&":
			// 分隔符**单独成 token**：若把它粘进前一个词（a&&b → a&&b），
			// SplitCommands 就切不开，于是「前半段得到 allow」会把后半段一起放行。
			flush()
			out = append(out, s)
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if escaped {
		return nil, errors.New("cmdpolicy: 命令以转义符结尾，无法判定")
	}
	if quote != "" {
		return nil, errors.New("cmdpolicy: 引号未闭合，无法判定")
	}
	flush()
	return mergeSeparators(out), nil
}

// mergeSeparators 把相邻的同类分隔符合成一个 token（& + & → &&）。
func mergeSeparators(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) == 1 && (tok == "&" || tok == "|") && i+1 < len(tokens) && tokens[i+1] == tok {
			out = append(out, tok+tok)
			i++
			continue
		}
		out = append(out, tok)
	}
	return out
}

// SplitCommands 把命令行按未加引号的分隔符（`;` `&&` `||` `|` `&\n`）切成多段。
//
// ⭐ 为什么必须分开判：`git status; rm -rf /` 的前缀是 `git`，若只对整串判一次，
// 一条 `git` 的 allow 规则就把后半段一起放行了——这是前缀判据最典型的绕过。
// 调用方应逐段 Decide，并对每段取**最严**结论。
func SplitCommands(cmdline string) ([]string, error) {
	argv, err := Split(cmdline)
	if err != nil {
		return nil, err
	}
	var (
		segs []string
		cur  []string
	)
	for _, tok := range argv {
		if isSeparator(tok) {
			if len(cur) > 0 {
				segs = append(segs, strings.Join(cur, " "))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, tok)
	}
	if len(cur) > 0 {
		segs = append(segs, strings.Join(cur, " "))
	}
	return segs, nil
}

func isSeparator(tok string) bool {
	switch tok {
	case ";", "|", "&", "&&", "||":
		return true
	}
	return false
}
