import { existsSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { slug } from 'github-slugger'
import { defineConfig } from 'vitepress'

const repositoryRoot = fileURLToPath(new URL('../..', import.meta.url))
const docsNodeModules = path.join(repositoryRoot, 'docs', 'node_modules')
const repositoryURL = 'https://github.com/kaimahi-agents/kaimahi'
const sourceRef = process.env.GITHUB_SHA || 'main'
const agentSuiteDocsURL = 'https://github.com/agent-primitives/suite/blob/main/spec.md'

const sitePages = new Set([
  'README.md',
  ...readdirSync(path.join(repositoryRoot, 'docs'), { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith('.md'))
    .map((entry) => `docs/${entry.name}`)
])

/** Return the repository-relative Markdown path used to resolve a page's links. */
function sourcePath(env: Record<string, unknown>): string {
  for (const key of ['relativePath', 'path', 'filePath']) {
    const value = env[key]
    if (typeof value === 'string' && value.endsWith('.md')) {
      const normalized = value.replaceAll('\\', '/')
      const relative = path.posix.normalize(normalized.replace(/^\/+/, ''))
      const docsAt = relative.lastIndexOf('/docs/')
      if (docsAt >= 0) return relative.slice(docsAt + 1)
      return relative
    }
  }
  return 'README.md'
}

/** Keep site pages local and send checkout-only references to immutable source. */
function rewriteRepositoryLinks(md: any): void {
  const render = md.renderer.rules.link_open ?? ((tokens: any[], index: number, options: unknown, _env: unknown, self: any) => self.renderToken(tokens, index, options))
  md.renderer.rules.link_open = (tokens: any[], index: number, options: unknown, env: Record<string, unknown>, self: any) => {
    const token = tokens[index]
    const href = token.attrGet('href')
    if (!href || href.startsWith('#') || href.startsWith('/') || /^[a-z][a-z0-9+.-]*:/i.test(href)) {
      return render(tokens, index, options, env, self)
    }

    const matched = href.match(/^([^?#]*)([?#].*)?$/)
    const target = matched?.[1]
    if (!target) return render(tokens, index, options, env, self)

    const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(sourcePath(env)), target))
    if (resolved === '..' || resolved.startsWith('../')) {
      return render(tokens, index, options, env, self)
    }
    if (resolved === 'README.md') {
      token.attrSet('href', `/${matched?.[2] ?? ''}`)
      return render(tokens, index, options, env, self)
    }
    if (resolved === 'docs/README.md') {
      token.attrSet('href', `/docs/${matched?.[2] ?? ''}`)
      return render(tokens, index, options, env, self)
    }
    if (sitePages.has(resolved)) return render(tokens, index, options, env, self)

    const absolute = path.join(repositoryRoot, ...resolved.split('/'))
    if (!existsSync(absolute)) return render(tokens, index, options, env, self)

    const location = statSync(absolute).isDirectory() ? 'tree' : 'blob'
    token.attrSet('href', `${repositoryURL}/${location}/${sourceRef}/${resolved}${matched?.[2] ?? ''}`)
    return render(tokens, index, options, env, self)
  }
}

const userGuides = [
  { text: 'Getting started', link: '/docs/getting-started' },
  { text: 'Workspace lifecycle', link: '/docs/suite-workspace' },
  { text: 'Runtime support', link: '/docs/runtime-adapters' },
  { text: 'Orka', link: '/docs/orka' },
  { text: 'Models and inference', link: '/docs/models' },
  { text: 'Interactive chat', link: '/docs/interactive-chat' },
  { text: 'AKS', link: '/docs/aks' },
  { text: 'Releases and upgrades', link: '/docs/releases' },
  { text: 'FAQ', link: '/docs/FAQ' }
]

const reference = [
  { text: 'kmx command reference', link: '/docs/kmx' },
  { text: 'AgentSuite specification', link: agentSuiteDocsURL },
  { text: 'Format boundaries', link: '/docs/bundle-format' },
  { text: 'Kubernetes Tool', link: '/docs/orka-k8s-tool' }
]

const maintainers = [
  { text: 'Development', link: '/docs/development' },
  { text: 'Documentation site', link: '/docs/docs-site' },
  { text: 'Codebase overview', link: '/docs/codebase-overview' },
  { text: 'Interface baseline', link: '/docs/interface-baseline' },
  { text: 'Repository map', link: '/docs/repository-map' },
  { text: 'Command conventions', link: '/docs/command-conventions' },
  { text: 'Command alignment', link: '/docs/command-semantics' },
  { text: 'Naming', link: '/docs/NAMING' }
]

const designNotes = [
  { text: 'Suite-first lifecycle', link: '/docs/suite-first-lifecycle' },
  { text: 'Lifecycle interfaces', link: '/docs/kmx-lifecycle-interfaces' },
  { text: 'Application API', link: '/docs/kmx-application-api' },
  { text: 'Public interface', link: '/docs/kmx-public-interface' },
  { text: 'CLI presentation', link: '/docs/cli-ux-plan' },
  { text: 'Interactive console', link: '/docs/interactive-agent-tui-plan' },
  { text: 'Creation wizard boundary', link: '/docs/charm-ux-followup-plan' },
  { text: 'Chat performance', link: '/docs/chat-performance-profile' },
  { text: 'Orka latency', link: '/docs/orka-latency' },
  { text: 'Orka startup', link: '/docs/orka-startup-performance' },
  { text: 'Azure discovery', link: '/docs/azure-discovery-performance' },
  { text: 'Copilot performance', link: '/docs/copilot-performance' },
  { text: 'Foundry inference', link: '/docs/local-foundry-inference' },
  { text: 'Copilot inference', link: '/docs/copilot-inference' }
]

export default defineConfig({
  title: 'Kaimahi',
  titleTemplate: ':title · Kaimahi',
  description: 'Build, prove, and lift agents for Kubernetes.',
  lang: 'en-US',
  srcDir: '..',
  cleanUrls: true,
  lastUpdated: true,
  base: process.env.KMX_DOCS_BASE || '/',
  rewrites: {
    'README.md': 'index.md',
    'docs/README.md': 'docs/index.md'
  },
  srcExclude: [
    'AGENTS.md',
    'CHANGELOG.md',
    'CODE_OF_CONDUCT.md',
    'CONTRIBUTING.md',
    'LOCAL-TEST-PLAN.md',
    'PITCH-*.md',
    'SECURITY.md',
    'testdata/**',
    'ax-harness/**',
    'brand/**',
    'internal/**',
    'node_modules/**'
  ],
  head: [
    ['meta', { name: 'theme-color', content: '#071827' }],
    ['meta', { name: 'color-scheme', content: 'light dark' }]
  ],
  markdown: {
    anchor: { slugify: slug },
    image: { lazyLoading: true },
    config: rewriteRepositoryLinks
  },
  vite: {
    publicDir: 'docs/public',
    resolve: {
      alias: {
        'brand/ketu.svg': path.join(repositoryRoot, 'brand/ketu.svg'),
        'vue/server-renderer': path.join(docsNodeModules, 'vue', 'server-renderer', 'index.mjs'),
        'vue': path.join(docsNodeModules, 'vue', 'dist', 'vue.runtime.esm-bundler.js')
      }
    }
  },
  transformPageData(pageData) {
    if (pageData.relativePath === 'README.md' || pageData.relativePath === 'index.md') {
      pageData.frontmatter.layout = 'home'
      pageData.frontmatter.hero = {
        name: 'Kaimahi',
        text: 'Build, Prove and Lift your agents',
        tagline: 'A Kubernetes-native path from editable workspace to evidence-bound deployment.',
        actions: [
          { theme: 'brand', text: 'Get your first answer', link: '/docs/getting-started' },
          { theme: 'alt', text: 'Explore the lifecycle', link: '/docs/suite-workspace' }
        ]
      }
    }
  },
  themeConfig: {
    nav: [
      { text: 'Start', link: '/docs/getting-started' },
      { text: 'Create · Prove · Lift', link: '/docs/suite-workspace' },
      { text: 'Runtimes', link: '/docs/runtime-adapters' },
      { text: 'Reference', link: '/docs/kmx' },
      {
        text: 'main · development',
        items: [
          { text: 'These docs follow main', link: '/docs/docs-site#version-boundary' },
          { text: 'Stable release v0.7.0', link: `${repositoryURL}/tree/v0.7.0` }
        ]
      }
    ],
    sidebar: {
      '/docs/': [
        { text: 'Start here', items: userGuides },
        { text: 'Reference', items: reference },
        { text: 'Maintainers', collapsed: true, items: maintainers },
        { text: 'Design and assessments', collapsed: true, items: designNotes }
      ]
    },
    search: {
      provider: 'local',
      options: {
        detailedView: true
      }
    },
    outline: { level: [2, 3], label: 'On this page' },
    editLink: {
      pattern: `${repositoryURL}/edit/main/:path`,
      text: 'Edit this page on GitHub'
    },
    socialLinks: [{ icon: 'github', link: repositoryURL }],
    footer: {
      message: 'Documentation generated directly from the repository Markdown.',
      copyright: 'Kaimahi is experimental and released under the MIT License.'
    },
    lastUpdated: { text: 'Updated' },
    docFooter: { prev: 'Previous', next: 'Next' },
    returnToTopLabel: 'Back to top',
    sidebarMenuLabel: 'Documentation',
    darkModeSwitchLabel: 'Appearance',
    lightModeSwitchTitle: 'Use light theme',
    darkModeSwitchTitle: 'Use dark theme',
    externalLinkIcon: true
  }
})
