// Package cmdpolicy —— 分词面（判据见 cmdpolicy.go）。
//
// 分词与判据分成两个文件，不是风格选择：判据（前缀规则 / 三态裁决 / 样例自检）
// 与分词（shell 引号与转义口径）是**两个各自可独立出错的判定面**。
// 分开后各自的行数与测试都能单独看；合在一起时，分词那一段只是「判据文件的头尾」，
// 改一次转义口径要在判据的散文里翻找，而它恰恰是最容易写错的那一段。
//
// # 分词面已收编上游（2026-11-11）
//
// 本包分词是上游 oekit/pkg/shellx.Split 的转发。收编前本仓自研过一份
// （85 行分词 + 17 行分隔符合并），那份与上游差四处，**四处都是本仓那份错**：
//
//  1. 双引号内的反斜杠：本仓丢弃**全部**反斜杠（"a\b" → ab），
//     上游按 POSIX 只转义 " \ $ 反引号 与换行（"a\b" → a\b）。
//     bash 实测与上游一致——本仓那份把文件名里的反斜杠吃掉，
//     判据看到的于是**另一个文件名**。
//  2. 分隔符合并：本仓按 **token 相邻**合并（a & & b → a && b），
//     上游按**字符相邻**合并（a & & b → a & & b）。前者把两条独立的后台命令
//     伪装成一条 && 链。
//  3. 回车：上游把 CR 归入空白（从配置/工单粘贴的 CRLF 命令行，
//     行尾 CR 不粘进最后一个 token），本仓当普通字符。
//  4. 空参数：上游明确保留并有往返用例钉住，本仓当时也保留（但无契约、无用例）。
//
// 收编后本包不再维护第二份分词口径——口径归上游一处。
// 本包保留的是**上游没有的那一层**：按分隔符切段（SplitCommands）。
//
// v0.35.0：导出的 Split 转发已删——需要裸分词的宿主直连
// oekit/pkg/shellx.Split；本包哨兵（ErrUnclosedQuote/ErrTrailingEscape）
// 仍在，由 SplitCommands/DecideCommand 的失败路径带出。
package cmdpolicy

import (
	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/shellx"
)

// 分词失败的两个哨兵。它们**包装**上游哨兵而非另立一套，
// 于是下游两种判法都成立：
//
// errors.Is(err, cmdpolicy.ErrUnclosedQuote)      —— 判本包语义
// errors.Is(err, shellx.ErrUnclosedQuote)          —— 判上游语义
var (
	// ErrUnclosedQuote 命令行里有未闭合的引号（判不了就不放行）。
	ErrUnclosedQuote = errors.Wrap(shellx.ErrUnclosedQuote, "cmdpolicy: 引号未闭合，无法判定")
	// ErrTrailingEscape 命令行以悬挂反斜杠结尾。
	ErrTrailingEscape = errors.Wrap(shellx.ErrTrailingEscape, "cmdpolicy: 命令以转义符结尾，无法判定")
)

// split 把命令行按 shell 口径分词并包上本包哨兵（供 SplitCommands 内部用）。
//
// 分词本体是上游 oekit/pkg/shellx.Split（口径归上游一处，本仓不维护第二份）；
// 包内保留的只有**错误语义**：未闭合引号 / 尾随反斜杠 → 本包哨兵
// （**判不了就不放行**），且哨兵包装上游哨兵——下游两种判法都成立。
// 需要裸分词的宿主直接用 shellx.Split。
func split(cmdline string) ([]string, error) {
	argv, err := shellx.Split(cmdline)
	if err == nil {
		return argv, nil
	}
	switch {
	case errors.Is(err, shellx.ErrUnclosedQuote):
		return nil, ErrUnclosedQuote
	case errors.Is(err, shellx.ErrTrailingEscape):
		return nil, ErrTrailingEscape
	default:
		return nil, errors.Wrap(err, "cmdpolicy: 命令行分词失败")
	}
}

// SplitCommands 把命令行按未加引号的分隔符（; && || | & 与换行）切成多段。
//
// ⭐ 为什么必须分开判：git status; rm -rf / 的前缀是 git，若只对整串判一次，
// 一条 git 的 allow 规则就把后半段一起放行了——这是前缀判据最典型的绕过。
// 调用方应逐段 Decide，并对每段取**最严**结论。
//
// 段文本是**可往返的**：段再 Split 一次得到与原段逐项相同的 token。
// 这一点是判据正确性的前提，不是美观问题——段若被摊平成空格分隔，
// token 边界就变了：git 'a b' 摊平后是 git a b（三个 token），
// 含空格的备选前缀（如 {{"git"}, {"a b"}}）从此永不命中。
//
// 可往返由上游 oekit/pkg/shellx.QuoteArgv 保证（它是分词的逆运算：
// Split(QuoteArgv(args...)) 必须逐项还原 args，含空参数与特殊字符）。
func SplitCommands(cmdline string) ([]string, error) {
	argv, err := split(cmdline)
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
				segs = append(segs, shellx.QuoteArgv(cur...))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, tok)
	}
	if len(cur) > 0 {
		segs = append(segs, shellx.QuoteArgv(cur...))
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
