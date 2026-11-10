"""从 agent-harness 仓的 README / go.mod 抽取事实，生成 GitHub Pages 站点。

为什么要有这个生成器：站点上的每个数字（包数、行数、依赖版本）都来自仓库，
手抄一遍就等于多一份会静默失真的真相——这正是本仓纪律反复说的那件事。
故站点从 README 与 go.mod 生成，读不到的数字**报错退出**，不静默填默认值。

用法：make site（= python3 site/build.py <仓根> <输出目录>，本地输出 _site/）
线上由 .github/workflows/pages.yml 在相关文件变更时自动重跑并部署。
"""
import os
import re
import shutil
import sys

BT = chr(96)


def load_pkgs(repo):
    src = open(os.path.join(repo, "README.md"), encoding="utf-8").read()
    sec = src.split("## 包清单", 1)[1].split("## 特性", 1)[0]
    rows = []
    for line in sec.splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 4:
            continue
        m = re.match(r"^\x60(\w+)\x60$", cells[0])
        if not m:
            continue
        rows.append({
            "name": m.group(1),
            "lines": cells[1],
            "deps": cells[2],
            "desc": cells[3],
        })
    return rows


def load_baseline(repo):
    src = open(os.path.join(repo, "README.md"), encoding="utf-8").read()
    prod = re.search(r"\|\s*生产代码\s*\|\s*([\d,]+)\s*行\s*/\s*([\d,]+)\s*文件", src)
    test = re.search(r"\|\s*测试\s*\|\s*([\d,]+)\s*个测试文件", src)
    if not prod or not test:
        raise SystemExit("README 的「工程底座」表解析失败——表结构变了？")
    return {
        "prod_lines": prod.group(1).replace(",", ""),
        "prod_files": prod.group(2).replace(",", ""),
        "test_files": test.group(1).replace(",", ""),
    }


def load_versions(repo):
    out = {}
    mod = open(os.path.join(repo, "go.mod"), encoding="utf-8").read()
    for m in re.finditer(r"^\s*(github\.com/(?:oekit|oagentkit)/[\w-]+)\s+(v[\d.]+)", mod, re.M):
        out[m.group(1)] = m.group(2)
    return out


def load_rules(repo):
    """architecture-policy.yaml 的 rules 段 → [{id, desc}]。

    规则条数与清单此前手抄在模板里——手抄数字就是本仓纪律反复说的
    「会静默失真的第二份真相」。改 policy 不改模板，页面就悄悄说谎。
    """
    src = open(os.path.join(repo, "architecture-policy.yaml"), encoding="utf-8").read()
    if "\nrules:" not in src:
        raise SystemExit("architecture-policy.yaml 找不到 rules 段——结构变了？")
    sec = src.split("\nrules:", 1)[1]
    rules = []
    for m in re.finditer(r"- id: (harness-[\w-]+)\n\s+desc: (.+)", sec):
        rules.append({"id": m.group(1), "desc": m.group(2).strip()})
    if not rules:
        raise SystemExit("rules 段解析出 0 条规则——解析失效，拒绝生成空清单")
    return rules


def load_thresholds(repo):
    """Makefile 的 COV_MIN / PKG_COV_MIN → (全局, 逐包)。读不到即报错。"""
    mk = open(os.path.join(repo, "Makefile"), encoding="utf-8").read()
    cov = re.search(r"^COV_MIN\s*:=\s*(\d+)", mk, re.M)
    pkg = re.search(r"^PKG_COV_MIN\s*:=\s*(\d+)", mk, re.M)
    if not cov or not pkg:
        raise SystemExit("Makefile 的 COV_MIN / PKG_COV_MIN 解析失败——阈值改名了？")
    return cov.group(1), pkg.group(1)


def esc(s):
    return (s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
             .replace('"', "&quot;"))


def md_inline(s):
    """README 单元格里的 markdown 行内标记 → HTML。"""
    s = esc(s)
    s = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", s)
    s = re.sub(r"\x60([^\x60]+)\x60", r"<code>\1</code>", s)
    return s


def main():
    repo, out = sys.argv[1], sys.argv[2]
    pkgs = load_pkgs(repo)
    base = load_baseline(repo)
    deps = load_versions(repo)
    rules = load_rules(repo)
    cov_min, pkg_cov_min = load_thresholds(repo)

    rows = []
    for p in pkgs:
        rows.append(
            "<tr>"
            '<td class="pkg"><code>' + esc(p["name"]) + "</code></td>"
            '<td class="num">' + esc(p["lines"]) + "</td>"
            '<td class="deps">' + md_inline(p["deps"]) + "</td>"
            '<td class="desc">' + md_inline(p["desc"]) + "</td>"
            "</tr>")

    total = sum(int(p["lines"]) for p in pkgs if p["lines"].isdigit())
    dep_items = "".join(
        "<li><code>" + esc(k.split("/", 1)[1]) + '</code><span class="v">' + esc(v) + "</span></li>"
        for k, v in sorted(deps.items()))

    here = os.path.dirname(os.path.abspath(__file__))
    html = open(os.path.join(here, "template.html"), encoding="utf-8").read()
    subs = {
        "<!--PKG_ROWS-->": "\n".join(rows),
        "<!--PKG_COUNT-->": str(len(pkgs)),
        "<!--TOTAL_LINES-->": "{:,}".format(total),
        "<!--PROD_LINES-->": base["prod_lines"],
        "<!--PROD_FILES-->": base["prod_files"],
        "<!--TEST_FILES-->": base["test_files"],
        "<!--DEP_ITEMS-->": dep_items,
        "<!--DEP_COUNT-->": str(len(deps)),
        "<!--RULE_COUNT-->": str(len(rules)),
        "<!--RULE_ITEMS-->": "\n".join(
            "          <li><code>" + esc(r["id"]) + "</code> " + md_inline(r["desc"]) + "</li>"
            for r in rules),
        "<!--COV_MIN-->": cov_min,
        "<!--PKG_COV_MIN-->": pkg_cov_min,
    }
    for k, v in subs.items():
        html = html.replace(k, v)

    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "index.html"), "w", encoding="utf-8") as f:
        f.write(html)
    shutil.copy(os.path.join(here, "style.css"), os.path.join(out, "style.css"))
    print("generated:", out, "| packages:", len(pkgs), "| deps:", len(deps))


if __name__ == "__main__":
    main()
