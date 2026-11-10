#!/usr/bin/env node
// 站点数据生成器：从 README / go.mod / architecture-policy.yaml / Makefile 抽事实，
// 生成 docs/reference/packages.md。
//
// 为什么要有这个生成器：站点上的每个数字（包数、行数、依赖版本、规则清单）都来自
// 仓库，手抄一遍就等于多一份会静默失真的真相——这正是本仓纪律反复说的那件事。
// 任何一处解析不到都以退出码 1 失败，**不静默填默认值**（与旧 build.py 同一口径）。
//
// 用法：node gen.mjs（npm run docs:dev / docs:build 会自动先跑它）。

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const siteDir = dirname(fileURLToPath(import.meta.url))
const repo = join(siteDir, '..')
const read = (p) => readFileSync(join(repo, p), 'utf8')

function fail(msg) {
  console.error('gen.mjs: ' + msg)
  process.exit(1)
}

function loadPkgs(src) {
  const sec = src.split('## 包清单', 2)[1]?.split('## 特性', 1)[0]
  if (!sec) fail('README 找不到「包清单」→「特性」段——结构变了？')
  const rows = []
  for (const line of sec.split('\n')) {
    if (!line.startsWith('|')) continue
    const cells = line.replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim())
    if (cells.length < 4) continue
    const m = /^\x60(\w+)\x60$/.exec(cells[0])
    if (!m) continue
    rows.push({ name: m[1], lines: cells[1], deps: cells[2], desc: cells[3] })
  }
  if (!rows.length) fail('README 包清单解析出 0 行——解析失效，拒绝生成空表')
  return rows
}

function loadBaseline(src) {
  const prod = /\|\s*生产代码\s*\|\s*([\d,]+)\s*行\s*\/\s*([\d,]+)\s*文件/.exec(src)
  const test = /\|\s*测试\s*\|\s*([\d,]+)\s*个测试文件/.exec(src)
  if (!prod || !test) fail('README 的「工程底座」表解析失败——表结构变了？')
  return { prodLines: prod[1], prodFiles: prod[2], testFiles: test[1] }
}

function loadVersions(src) {
  const out = []
  for (const m of src.matchAll(/^\s*(github\.com\/(?:oekit|oagentkit)\/[\w-]+)\s+(v[\d.]+)/gm)) {
    out.push({ module: m[1], version: m[2] })
  }
  if (!out.length) fail('go.mod 解析出 0 个第一方依赖——解析失效')
  return out
}

function loadRules(src) {
  if (!src.includes('\nrules:')) fail('architecture-policy.yaml 找不到 rules 段——结构变了？')
  const sec = src.split('\nrules:', 2)[1]
  const rules = []
  for (const m of sec.matchAll(/- id: (harness-[\w-]+)\n\s+desc: (.+)/g)) {
    rules.push({ id: m[1], desc: m[2].trim() })
  }
  if (!rules.length) fail('rules 段解析出 0 条规则——解析失效，拒绝生成空清单')
  return rules
}

function loadThresholds(src) {
  const cov = /^COV_MIN\s*:=\s*(\d+)/m.exec(src)
  const pkg = /^PKG_COV_MIN\s*:=\s*(\d+)/m.exec(src)
  if (!cov || !pkg) fail('Makefile 的 COV_MIN / PKG_COV_MIN 解析失败——阈值改名了？')
  return { cov: cov[1], pkgCov: pkg[1] }
}

// markdown 表格单元格转义：竖线会截断表格。
const cell = (s) => s.replace(/\|/g, '\\|')
// 尖括号转义（反引号外）：VuePress 把 markdown 编译成 Vue 模板，裸 <x> 会被当
// 自定义元素解析，「Element is missing end tag」直接炸构建。反引号内不转义——
// code span 里的实体不会被解码，转了会显示出字面 &lt;。
const escAngles = (s) => s
  .split(bt)
  .map((seg, i) => (i % 2 ? seg : seg.replace(/</g, '&lt;').replace(/>/g, '&gt;')))
  .join(bt)
const bt = String.fromCharCode(96)

const src = read('README.md')
const pkgs = loadPkgs(src)
const base = loadBaseline(src)
const deps = loadVersions(read('go.mod'))
const rules = loadRules(read('architecture-policy.yaml'))
const th = loadThresholds(read('Makefile'))

const md = [
  '---',
  'title: 包清单与机器事实',
  '---',
  '',
  '# 包清单与机器事实',
  '',
  '::: warning 本页由 ' + bt + 'site/gen.mjs' + bt + ' 生成，勿手改',
  '包表 / 工程底座基线 / 依赖版本 / 规则清单分别抽取自 README、go.mod、',
  bt + 'architecture-policy.yaml' + bt + '、Makefile；每次 ' + bt + 'npm run docs:dev / docs:build' + bt + ' 自动重新生成。',
  '要改事实请改源头文件——**手抄一遍就等于多一份会静默失真的真相**。',
  ':::',
  '',
  '## 能力包清单（' + pkgs.length + ' 个包）',
  '',
  '依赖列只列仓外 import；「无」表示该包只用标准库。',
  '',
  '| 包 | 生产行数 | 外部依赖 | 职责 |',
  '|---|---|---|---|',
  ...pkgs.map((p) => '| ' + bt + p.name + bt + ' | ' + cell(p.lines) + ' | ' + cell(escAngles(p.deps)) + ' | ' + cell(escAngles(p.desc)) + ' |'),
  '',
  '## 工程底座基线',
  '',
  '| 指标 | 值 | 判定 |',
  '|---|---|---|',
  '| 生产代码 | ' + base.prodLines + ' 行 / ' + base.prodFiles + ' 文件 | ' + bt + 'make docsync' + bt + ' 判相等，不留容差 |',
  '| 测试 | ' + base.testFiles + ' 个测试文件 | 同上 |',
  '| 覆盖率门槛 | 全局 ' + th.cov + '% + 逐包下限 ' + th.pkgCov + '% | ' + bt + 'make cover-gate' + bt + ' 判（实测值随用例批次漂，不抄进散文） |',
  '',
  '## 门禁规则清单（rules 段，' + rules.length + ' 条）',
  '',
  '| 规则 | 说明 |',
  '|---|---|',
  ...rules.map((r) => '| ' + bt + r.id + bt + ' | ' + cell(r.desc) + ' |'),
  '',
  '结构性判定（门包不变 / 依赖方向 / 沙箱档位登记 / 执行路径 / 子进程出口）走 ' + bt + 'go/ast' + bt + '，',
  '声明面在 ' + bt + 'architecture-policy.yaml' + bt + ' 的 facades / dependency_direction / sandbox_providers / execution_paths 各段。',
  '',
  '## 第一方依赖（' + deps.length + ' 个模块）',
  '',
  '独立发版、版本化引入、**零 replace**（go.mod 含 replace 即门禁红）。',
  '',
  '| 模块 | 版本 |',
  '|---|---|',
  ...deps.map((d) => '| ' + bt + d.module + bt + ' | ' + bt + d.version + bt + ' |'),
  '',
  '本仓处在 oekit（基础库）→ oagentkit（agent 原语）→ agent-harness（编排内核）→ 宿主的同一条依赖链上。',
  ''
].join('\n')

// ── 坑速查页：整表从 docs/USAGE.md §6 抽取（USAGE 是唯一真相，不手抄第二份）──
function loadPitfalls(src) {
  const sec = src.split('## 6. 坑速查表', 2)[1]?.split('\n## ', 1)[0]
  if (!sec) fail('docs/USAGE.md 找不到「## 6. 坑速查表」段——结构变了？')
  const lines = sec.split('\n').filter((l) => l.trim().startsWith('|'))
  if (lines.length < 3) fail('坑速查表解析出 ' + lines.length + ' 行——解析失效')
  return lines
}

const pitfalls = loadPitfalls(read('docs/USAGE.md'))
const pitMd = [
  '---',
  'title: 坑速查',
  '---',
  '',
  '# 坑速查：症状 → 真因',
  '',
  '::: warning 本页由 ' + bt + 'site/gen.mjs' + bt + ' 从仓内 ' + bt + 'docs/USAGE.md §6' + bt + ' 生成，勿手改',
  '坑表的唯一真相是 USAGE 的坑速查表；接完线拿它自查一遍。',
  ':::',
  '',
  ...pitfalls.map(escAngles),
  ''
].join('\n')

mkdirSync(join(siteDir, 'docs', 'reference'), { recursive: true })
writeFileSync(join(siteDir, 'docs', 'reference', 'packages.md'), md)
writeFileSync(join(siteDir, 'docs', 'reference', 'pitfalls.md'), pitMd)
console.log('gen.mjs: packages.md + pitfalls.md 生成完毕（' + pkgs.length + ' 包 / ' + deps.length + ' 依赖 / ' + rules.length + ' 规则 / ' + pitfalls.length + ' 坑表行）')
