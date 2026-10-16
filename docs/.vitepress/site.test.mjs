import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../..', import.meta.url))
const dist = path.join(root, 'docs', '.vitepress', 'dist')
const sourceRef = process.env.GITHUB_SHA || 'main'
const withAgentSuite = process.env.KMX_DOCS_WITH_AGENTSUITE !== '0' && existsSync(path.join(root, 'agentsuite', 'spec.md'))
const agentSuiteDocsURL = process.env.KMX_AGENTSUITE_DOCS_URL || 'https://github.com/kaimahi-agents/kaimahi/issues/491'

/** Recursively list generated HTML paths relative to the output directory. */
async function htmlFiles(directory, prefix = '') {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const relative = path.posix.join(prefix, entry.name)
    if (entry.isDirectory()) files.push(...await htmlFiles(path.join(directory, entry.name), relative))
    if (entry.isFile() && entry.name.endsWith('.html')) files.push(relative)
  }
  return files.sort()
}

test('every maintained Markdown page is rendered exactly once', async () => {
  const docs = (await readdir(path.join(root, 'docs'), { withFileTypes: true }))
    .filter((entry) => entry.isFile() && entry.name.endsWith('.md'))
    .map((entry) => entry.name === 'README.md' ? 'docs/index.html' : `docs/${entry.name.replace(/\.md$/, '.html')}`)
  const expected = ['404.html', 'index.html', ...(withAgentSuite ? ['agentsuite/spec.html'] : []), ...docs].sort()
  assert.deepEqual(await htmlFiles(dist), expected)
})

test('the homepage renders the interactive journey and channel boundary', async () => {
  const homepage = await readFile(path.join(dist, 'index.html'), 'utf8')
  for (const marker of [
    'Build, Prove and Lift your agents',
    'Development docs',
    'Stable v0.7.0',
    'Choose your path',
    'role="tablist"',
    'Get your first answer'
  ]) {
    assert.match(homepage, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
})

test('checkout-only references point to source while guide links stay local', async () => {
  const development = await readFile(path.join(dist, 'docs', 'development.html'), 'utf8')
  assert.ok(development.includes(`https://github.com/kaimahi-agents/kaimahi/blob/${sourceRef}/CONTRIBUTING.md`))
  assert.match(development, /href="\.\/getting-started"/)
  assert.doesNotMatch(development, /href="\.\.\/scripts\//)

  const releases = await readFile(path.join(dist, 'docs', 'releases.html'), 'utf8')
  assert.ok(releases.includes(`https://github.com/kaimahi-agents/kaimahi/tree/${sourceRef}/internal/kmx/toolchain`))

  if (!withAgentSuite) {
    const index = await readFile(path.join(dist, 'docs', 'index.html'), 'utf8')
    assert.ok(index.includes(`href="${agentSuiteDocsURL}"`))
  }
})
