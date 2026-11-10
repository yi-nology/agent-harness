import { viteBundler } from '@vuepress/bundler-vite'
import { defaultTheme } from '@vuepress/theme-default'
import { defineUserConfig } from 'vuepress'

// 站点配置。base 必须是 /agent-harness/——GitHub Pages 项目站挂在子路径下，
// 写错根路径整站资源 404。
export default defineUserConfig({
  lang: 'zh-CN',
  title: 'agent-harness',
  description: '编排是代码，LLM 只做单环节——多智能体编排的通用内核：机制面，不认识任何领域概念。',
  base: '/agent-harness/',
  head: [['link', { rel: 'icon', href: '/agent-harness/favicon.svg' }]],
  bundler: viteBundler(),
  theme: defaultTheme({
    repo: 'yi-nology/agent-harness',
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
          { text: '本站 llms.txt', link: 'https://yi-nology.github.io/agent-harness/llms.txt' },
          { text: 'oekit llms.txt', link: 'https://oekit.github.io/llms.txt' },
          { text: 'oagentkit llms.txt', link: 'https://oagentkit.github.io/llms.txt' },
        ],
      },
      {
        text: '参考',
        children: [
          { text: '包清单与机器事实', link: '/reference/packages.html' },
          { text: '门禁体系', link: '/reference/gates.html' },
          { text: '版本记录', link: '/reference/releases.html' },
        ],
      },
    ],
    footer: 'MIT License · github.com/yi-nology/agent-harness · <a href="https://yi-nology.github.io/agent-harness/llms.txt">AI 直读 llms.txt</a>',
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
            '/reference/releases.md',
            '/reference/pitfalls.md',
          ],
        },
      ],
    },
  }),
})
