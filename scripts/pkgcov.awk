# pkgcov.awk —— cover-gate 的逐包覆盖率下限判定（由 Makefile 调用，不单独运行）
#
# 输入：`go test ./... -cover` 的标准输出（每行 `ok  <pkg>  <dur>  coverage: N% of statements`）。
# 参数：-v m=<逐包下限百分比>  -v ex=<豁免包路径后缀，逗号分隔>
#
# 为什么判据要落在**逐包**而不是只看全局：
#   全局 85.5% 曾同时掩盖着 cmd/docsync 23.6% 与 sandbox 60.0%。全局阈值量的是
#   「整体没退步」，它对单个包的塌方完全不敏感——大盘涨 1% 就能把一个包从 70 掉到 20
#   的塌方抵消掉，而报告里只有一行「85.5%，绿」。两个判据判的是两件事，不能互相代替。
#
# 为什么只认 `ok` 行：
#   `?  <pkg>  [no test files]` 与 `[no statements]` 不是「0% 覆盖」，是「不适用」。
#   把它们算进分母，判据就变成了「测试文件计数」，而那是另一条门禁该管的事。
#   代价要说清：**没有测试文件的包因此不受本条约束**（cmd/archcheck、cmd/ptcchild
#   即属此类），它们的存在理由写在 Makefile 的豁免注释里，不在本脚本里假装判过。
#
# 为什么豁免会「失效」即红：
#   豁免一旦写成一次性的空条文，就会永久留着——半年后没人记得它当初为什么存在，
#   也没人敢删。故豁免包自己达到下限时，本脚本报错并要求当天删除该条豁免。
#   这与 architecture-policy.yaml 的 exceptions 必须写 until 是同一套口径：
#   **豁免可以有，但不能静默，也不能无期限。**

BEGIN {
  n = split(ex, e, ",")
  for (i = 1; i <= n; i++) {
    gsub(/^[ \t]+|[ \t]+$/, "", e[i])
  }
}

# 只处理带覆盖率的测试结果行。
$1 == "ok" {
  pkg = $2
  pct = -1
  for (i = 3; i <= NF; i++) {
    if ($i == "coverage:") {
      i++
      pct = $i
      sub(/%/, "", pct)
      # `[no statements]` 的形态就长这样：`coverage: [no statements]`。
      # 不校验数值形态就 `pct+0` ⇒ 字符串转 0 ⇒ **把「不适用」判成「0% 覆盖」**。
      # 这条是反向验证抓出来的（合成日志里 contract 包当场被判 0% 红），
      # 真实输出同样会走到这条路径，故不只在合成日志里存在。
      if (pct !~ /^[0-9]+([.][0-9]+)?$/) pct = -1
    }
  }
  if (pct < 0) next
  counted++

  # 按包路径**后缀**匹配豁免：Makefile 里写 `cmd/docsync` 比写全 module 路径可读，
  # 而全路径前缀（github.com/yi-nology/agent-harness）在每条豁免里都是噪音。
  hit = 0
  for (i = 1; i <= n; i++) {
    if (e[i] != "" && substr(pkg, length(pkg) - length(e[i]) + 1) == e[i]) hit = i
  }
  if (hit) {
    if (pct + 0 >= m + 0) {
      stale = stale sprintf("\n  - %s: 已达 %.1f%%（下限 %d%%）⇒ 豁免失效，请从 PKG_COV_EXEMPT 移除", pkg, pct, m)
    } else {
      exempted++
    }
    next
  }
  if (pct + 0 < m + 0) {
    bad = bad sprintf("\n  - %s: %.1f%%", pkg, pct)
  }
}

END {
  if (bad != "") {
    printf "以下包低于逐包下限 %d%%：%s\n", m, bad
    printf "修法：补该包的用例（阈值是真阈值，不要靠调低 PKG_COV_MIN 变绿）。\n"
    exit 1
  }
  if (stale != "") {
    printf "逐包下限豁免已失效（必须当天拆除，不能静默留着）：%s\n", stale
    exit 1
  }
  printf "逐包覆盖率下限 %d%% ✓（%d 个包计入，%d 个显式豁免）\n", m, counted, exempted
}
