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
    navbar: [
      { text: '介绍', link: '/guide/introduction.html' },
      { text: '设计初衷', link: '/guide/philosophy.html' },
      { text: '怎么用', link: '/guide/getting-started.html' },
      { text: '能力', link: '/guide/capabilities.html' },
      { text: '坑速查', link: '/reference/pitfalls.html' },
      {
        text: '参考',
        children: [
          { text: '包清单与机器事实', link: '/reference/packages.html' },
          { text: '门禁体系', link: '/reference/gates.html' },
        ],
      },
    ],
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
            '/reference/pitfalls.md',
          ],
        },
      ],
    },
  }),
})
