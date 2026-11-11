import { viteBundler } from '@vuepress/bundler-vite'
import { defaultTheme } from '@vuepress/theme-default'
import { defineUserConfig } from 'vuepress'
import { readFileSync } from 'node:fs'

// ADR 清单由 site/gen.mjs 生成（页面本体也在生成物里）。
//
// 为什么清单不写死在本文件里：每加一篇 ADR 就要手改一次侧栏配置，
// 而那一次手改总会有人忘——侧栏与实际页数就此漂移，且没有任何东西会红。
// 故清单从生成物读：页数变了，配置自动跟着变。
//
// 清单文件不存在时直接报错而不是静默用空数组：那是构建顺序错了
// （npm run docs:build 会先跑 node gen.mjs），不是「本仓没有 ADR」。
let adrManifest = []
try {
  adrManifest = JSON.parse(readFileSync(new URL('./adr-manifest.json', import.meta.url), 'utf8'))
} catch (e) {
  console.error('config.js: 读不到 adr-manifest.json —— 先跑 node gen.mjs（npm run docs:build 会自动先跑）')
  throw e
}

// 站点配置。base 必须是 /goagent-kernel/——GitHub Pages 项目站挂在子路径下，
// 写错根路径整站资源 404。
export default defineUserConfig({
  lang: 'zh-CN',
  title: 'goagent-kernel',
  description: '编排是代码，LLM 只做单环节——多智能体编排的通用内核：机制面，不认识任何领域概念。',
  base: '/goagent-kernel/',
  head: [['link', { rel: 'icon', href: '/goagent-kernel/favicon.svg' }]],
  bundler: viteBundler(),
  theme: defaultTheme({
    repo: 'yi-nology/goagent-kernel',
    repoLabel: 'GitHub',
    editLink: false,
    lastUpdated: false,
    contributors: false,
    // 纯深色（旧站风格），不提供明暗切换
    colorMode: 'dark',
    colorModeSwitch: false,
    // 关掉 changelog 挂件：它会在页面上浮出「commit/<hash>」胶囊（半截裁在
    // 边缘），对读者是噪声——版本史看仓内 CHANGELOG，入口在参考区。
    changelog: false,
    navbar: [
      { text: '介绍', link: '/guide/introduction.html' },
      { text: '设计初衷', link: '/guide/philosophy.html' },
      { text: '怎么用', link: '/guide/getting-started.html' },
      { text: '能力', link: '/guide/capabilities.html' },
      { text: '坑速查', link: '/reference/pitfalls.html' },
      {
        text: 'AI 直读',
        children: [
          { text: '本站 llms.txt', link: 'https://yi-nology.github.io/goagent-kernel/llms.txt' },
          { text: 'oekit llms.txt', link: 'https://oekit.github.io/llms.txt' },
          { text: 'oagentkit llms.txt', link: 'https://oagentkit.github.io/llms.txt' },
        ],
      },
      {
        text: '参考',
        children: [
          { text: '包清单与机器事实', link: '/reference/packages.html' },
          { text: '门禁体系', link: '/reference/gates.html' },
          { text: '决策记录（ADR）', link: '/reference/adr.html' },
          { text: '版本记录', link: '/reference/releases.html' },
        ],
      },
    ],
    footer: 'MIT License · github.com/yi-nology/goagent-kernel · <a href="https://yi-nology.github.io/goagent-kernel/llms.txt">AI 直读 llms.txt</a>',
    sidebar: {
      '/guide/': [
        {
          text: '指南',
          children: [
            '/guide/introduction.md',
            '/guide/philosophy.md',
            '/guide/getting-started.md',
            '/guide/capabilities.md',
          ],
        },
      ],
      '/reference/': [
        {
          text: '参考',
          children: [
            '/reference/packages.md',
            '/reference/gates.md',
            '/reference/adr.md',
            '/reference/releases.md',
            '/reference/pitfalls.md',
          ],
        },
      ],
      '/adr/': [
        {
          text: '决策记录',
          children: adrManifest.map((a) => '/adr/' + a.file),
        },
      ],
    },
  }),
})
