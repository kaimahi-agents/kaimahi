import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../..', import.meta.url))
const dist = path.join(root, 'docs', '.vitepress', 'dist')
const sourceRef = process.env.GITHUB_SHA || 'main'
const agentSuiteDocsURL = 'https://github.com/agent-primitives/suite/blob/main/spec.md'

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
  const expected = ['404.html', 'index.html', ...docs].sort()
  assert.deepEqual(await htmlFiles(dist), expected)
})

test('AgentSuite links retain their standalone destination and section fragment', async () => {
  const guide = await readFile(path.join(dist, 'docs', 'suite-workspace.html'), 'utf8')
  assert.ok(guide.includes('href="https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions"'))
  assert.doesNotMatch(guide, /href="[^"]*(?:issues\/491|\/agentsuite\/spec)(?:["#])/)
})

test('the homepage renders the interactive journey and channel boundary', async () => {
  const homepage = await readFile(path.join(dist, 'index.html'), 'utf8')
  for (const marker of [
    'Build, Prove and Lift your agents',
    'Development docs',
    'Stable v0.8.1',
    'Choose your path',
    'role="tablist"',
    'Get your first answer'
  ]) {
    assert.match(homepage, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
})

/** Inspect rendered instructions without syntax-highlighter markup. */
function plainText(html) {
  return html.replace(/<\/(?:p|pre|h[1-6])>/g, '\n').replace(/<[^>]*>/g, '').replaceAll('&gt;', '>').replaceAll('&lt;', '<').replaceAll('&amp;', '&')
}

test('the front doors lead with composed Lift and its prerequisites', async () => {
  const homepage = await readFile(path.join(dist, 'index.html'), 'utf8')
  const firstPanel = homepage.match(/<article[^>]*id="journey-panel-0"[\s\S]*?<\/article>/)?.[0]
  assert.ok(firstPanel, 'the default journey is rendered')
  const chooser = plainText(firstPanel)
  assert.match(chooser, /kmx create[^\n]*kmx lift --to NAME/)
  for (const prerequisite of ['prepared runtime', 'inference', 'registry authentication', 'kmx.yaml', 'publishRepository', 'evaluationTarget']) {
    assert.ok(chooser.includes(prerequisite), `chooser prerequisite: ${prerequisite}`)
  }
  for (const page of ['index.html', 'docs/getting-started.html']) {
    const text = plainText(await readFile(path.join(dist, page), 'utf8'))
    const create = text.indexOf('kmx create ')
    const lift = text.indexOf('kmx lift --to ')
    assert.ok(create >= 0 && lift > create, `${page}: create precedes composed Lift`)
    const quickstart = text.indexOf('kmx quickstart')
    assert.ok(quickstart > lift, `${page}: composed path precedes quickstart`)
    const introduction = text.slice(0, lift)
    for (const prerequisite of ['prepared', 'inference', 'registry authentication', 'kmx.yaml', 'publishRepository', 'evaluationTarget']) {
      assert.ok(introduction.includes(prerequisite), `${page} prerequisite: ${prerequisite}`)
    }
    assert.ok(text.includes('source-only promotion'), `${page}: promotion disclosure`)
  }
})

test('active installation guidance pins the stable CLI without rewriting history', async () => {
  for (const [page, heading] of [
    ['index.html', 'getting-started-get-your-first-answer'],
    ['docs/getting-started.html', 'install-kmx'],
    ['docs/kmx.html', 'install']
  ]) {
    const html = await readFile(path.join(dist, page), 'utf8')
    const start = html.indexOf(`<h2 id="${heading}"`)
    assert.ok(start >= 0, `${page}: active install section`)
    const end = html.indexOf('<h2 ', start + 1)
    const section = plainText(html.slice(start, end < 0 ? undefined : end))
    const pins = [...section.matchAll(/go install github\.com\/kaimahi-agents\/kaimahi\/cmd\/kmx@(\S+)/g)].map((match) => match[1])
    assert.deepEqual(pins, ['v0.8.1'], `${page}: repeatable Go installation`)
    if (page === 'docs/kmx.html') assert.ok(section.includes('--version=v0.8.1'))
  }
  const help = execFileSync('sh', [path.join(root, 'install.sh'), '--help'], { encoding: 'utf8' })
  assert.match(help, /Pin @v0\.8\.1 for a repeatable build/)
  const reference = plainText(await readFile(path.join(dist, 'docs/kmx.html'), 'utf8'))
  assert.match(reference, /historical v0\.5\.1 uses the/)
})

test('checkout-only references point to source while guide links stay local', async () => {
  const development = await readFile(path.join(dist, 'docs', 'development.html'), 'utf8')
  assert.ok(development.includes(`https://github.com/kaimahi-agents/kaimahi/blob/${sourceRef}/CONTRIBUTING.md`))
  assert.match(development, /href="\.\/getting-started"/)
  assert.doesNotMatch(development, /href="\.\.\/scripts\//)

  const releases = await readFile(path.join(dist, 'docs', 'releases.html'), 'utf8')
  assert.ok(releases.includes(`https://github.com/kaimahi-agents/kaimahi/tree/${sourceRef}/internal/kmx/toolchain`))

  const index = await readFile(path.join(dist, 'docs', 'index.html'), 'utf8')
  assert.ok(index.includes(`href="${agentSuiteDocsURL}"`))
})
