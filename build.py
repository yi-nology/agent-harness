"""从 agent-harness 仓的 README 抽取事实，生成 GitHub Pages 站点。

为什么要有这个生成器：站点上的每个数字（包数、行数、门禁条数）都来自仓库，
手抄一遍就等于多一份会静默失真的真相——这正是本仓纪律反复说的那件事。
故站点从 README 生成，README 改了重跑本脚本即可。

用法：python3 build.py <agent-harness 仓路径> <输出目录>
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
