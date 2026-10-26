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
  async function markdownFiles(directory, prefix = 'docs') {
    const files = []
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const relative = path.posix.join(prefix, entry.name)
      if (entry.isDirectory() && !entry.name.startsWith('.') && entry.name !== 'node_modules') {
        files.push(...await markdownFiles(path.join(directory, entry.name), relative))
      }
      if (entry.isFile() && entry.name.endsWith('.md')) files.push(relative)
    }
    return files
  }
  const docs = (await markdownFiles(path.join(root, 'docs')))
    .map((file) => file.replace(/\/README\.md$/, '/index.md').replace(/\.md$/, '.html'))
  const expected = ['404.html', 'index.html', ...docs].sort()
  assert.deepEqual(await htmlFiles(dist), expected)
})

test('AgentSuite links retain their standalone destination and section fragment', async () => {
  const guide = await readFile(path.join(dist, 'docs', 'suite-workspace.html'), 'utf8')
  assert.ok(guide.includes('href="https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions"'))
  assert.doesNotMatch(guide, /href="[^"]*(?:issues\/491|\/agentsuite\/spec)(?:["#])/)
})

test('renamed headings retain legacy guide fragments', async () => {
  for (const [page, fragments] of [
    ['docs/suite-workspace.html', ['digest-lift', 'digest-deploy', 'composed-workspace-lift', 'composed-workspace-deploy']],
    ['docs/workflow-overview.html', ['create-prove-lift', 'create-prove-deploy', 'run-test-publish']],
    ['docs/aks-demo.html', ['aks-free-tier-demo-create-prove-lift', 'aks-free-tier-demo-create-prove-deploy', '5-prove-locally', '5-test-locally']],
    ['docs/azure/cpu-model/index.html', ['5-create-and-prove-the-agent-locally', '5-create-and-test-the-agent-locally']]
  ]) {
    const html = await readFile(path.join(dist, page), 'utf8')
    for (const fragment of fragments) {
      assert.ok(html.includes(`id="${fragment}"`), `${page}: #${fragment} resolves`)
    }
  }
})

test('the homepage renders the interactive journey and channel boundary', async () => {
  const homepage = await readFile(path.join(dist, 'index.html'), 'utf8')
  for (const marker of [
    'Run, Test and Publish your agents',
    'Development docs',
    'Stable v0.8.1',
    'Choose your path',
    'role="tablist"',
    'Get your first answer'
  ]) {
    assert.match(homepage, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  }
})

test('Test is canonical while compatibility and current runtime limits stay explicit', async () => {
  const runtimes = plainText(await readFile(path.join(dist, 'docs/runtime-adapters.html'), 'utf8'))
  assert.match(runtimes, /profiles support root test,/)
  assert.doesNotMatch(runtimes, /testuat/i)
  const reference = plainText(await readFile(path.join(dist, 'docs/kmx.html'), 'utf8'))
  for (const helper of ['cleanup', 'diff', 'explain', 'export', 'verify', 'from-sessions']) {
    assert.ok(reference.includes(`kmx test ${helper}`), `canonical helper: ${helper}`)
  }
  const html = await readFile(path.join(dist, 'docs/kmx.html'), 'utf8')
  assert.ok(html.includes('id="test-compatibility-unreleased"'))
  for (const marker of [
    'v0.8.1 uses kmx eval', 'at least one released version',
    'kmx eval is deprecated; use kmx test (same arguments).',
    'stdout', 'exit codes', 'state', 'evalTarget', 'evaluationTarget'
  ]) assert.ok(reference.includes(marker), `compatibility boundary: ${marker}`)
  for (const page of ['index.html', 'docs/getting-started.html', 'docs/workflow-overview.html']) {
    const text = plainText(await readFile(path.join(dist, page), 'utf8'))
    assert.match(text, /Run[^\n]*Test[^\n]*Publish/, `${page}: developer vocabulary`)
    assert.match(text, /v0\.8\.1 uses[^\n]*eval/, `${page}: stable spelling`)
    assert.match(text, /Docker/, `${page}: container prerequisite retained`)
    assert.match(text, /WSL/, `${page}: Windows boundary retained`)
    assert.doesNotMatch(text, /kmx publish|kmx test --local/)
  }
  for (const page of ['docs/azure/cpu-model/index.html', 'docs/aks-demo.html', 'docs/suite-workspace.html']) {
    const text = plainText(await readFile(path.join(dist, page), 'utf8')).replace(/\s+/g, ' ')
    assert.match(text, /v0\.8\.1 uses kmx eval and kmx lift/, `${page}: both stable substitutions`)
  }
  const azure = plainText(await readFile(path.join(dist, 'docs/azure/cpu-model/index.html'), 'utf8')).replace(/\s+/g, ' ')
  assert.match(azure, /kmx test cleanup[^.]*kmx eval cleanup/, 'Azure cleanup uses the stable helper spelling')
  const workflow = plainText(await readFile(path.join(dist, 'docs/workflow-overview.html'), 'utf8'))
  assert.match(workflow, /kmx run --agent NAME --prompt TEXT[^\n]*live Agents[^\n]*neither is workspace-local execution/)
  assert.match(workflow, /kmx push --workspace DIR --evaluation RECEIPT REF/)
})

test('agent-file CI reports retain failure status and content-free summaries', async () => {
  const app = plainText(await readFile(path.join(dist, 'docs/copilot-app.html'), 'utf8')).replace(/\s+/g, ' ')
  for (const marker of [
    'kmx test ./greeter.agent.md -o json --github-summary > kmx-test.json',
    'if: always() && hashFiles',
    'kmx-test-report',
    'Do not use continue-on-error',
    'original 0/2/1/130 result',
    'content-free table',
    'Neither output creates automatic local history'
  ]) assert.ok(app.includes(marker), `CI report contract: ${marker}`)
  assert.match(app, /--github-summary[^.]*unreleased on main/i)
  assert.match(app, /v0\.9\.0 release candidates do not contain it/i)

  const reference = plainText(await readFile(path.join(dist, 'docs/kmx.html'), 'utf8')).replace(/\s+/g, ' ')
  for (const marker of [
    'durationMs', 'effective judge identity', 'Enabled/Disabled settings',
    'live/recorded/unavailable source', 'GITHUB_STEP_SUMMARY',
    'omits file paths', '0/2/1/130 exit status', 'not suite evidence'
  ]) assert.ok(reference.includes(marker), `JSON/summary contract: ${marker}`)
})

/** Inspect rendered instructions without syntax-highlighter markup. */
function plainText(html) {
  return html.replace(/<\/(?:p|pre|h[1-6])>/g, '\n').replace(/<[^>]*>/g, '').replaceAll('&gt;', '>').replaceAll('&lt;', '<').replaceAll('&amp;', '&')
}

test('the front doors lead with composed Deploy and its prerequisites', async () => {
  const homepage = await readFile(path.join(dist, 'index.html'), 'utf8')
  const firstPanel = homepage.match(/<article[^>]*id="journey-panel-0"[\s\S]*?<\/article>/)?.[0]
  assert.ok(firstPanel, 'the default journey is rendered')
  const chooser = plainText(firstPanel)
  assert.match(chooser, /kmx create[^\n]*kmx deploy --to NAME/)
  for (const prerequisite of ['prepared runtime', 'inference', 'registry authentication', 'kmx.yaml', 'publishRepository', 'evaluationTarget']) {
    assert.ok(chooser.includes(prerequisite), `chooser prerequisite: ${prerequisite}`)
  }
  for (const page of ['index.html', 'docs/getting-started.html', 'docs/workflow-overview.html']) {
    const text = plainText(await readFile(path.join(dist, page), 'utf8'))
    const create = text.indexOf('kmx create ')
    const lift = text.indexOf('kmx deploy --to ')
    assert.ok(create >= 0 && lift > create, `${page}: create precedes composed Deploy`)
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
    ['docs/workflow-overview.html', 'getting-started-get-your-first-answer'],
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

  const azure = await readFile(path.join(dist, 'docs', 'azure', 'cpu-model', 'index.html'), 'utf8')
  assert.match(azure, /Single-node CPU inference test/)
  assert.match(azure, /href="\.\/\.\.\/\.\.\/models"/)
  assert.ok(azure.includes(`https://github.com/kaimahi-agents/kaimahi/blob/${sourceRef}/docs/azure/cpu-model/aikit-cpu.yaml`))
  const models = await readFile(path.join(dist, 'docs', 'models.html'), 'utf8')
  assert.match(models, /href="[^"]*\/docs\/azure\/cpu-model\/#single-node-cpu-inference-test"/)

  const releases = await readFile(path.join(dist, 'docs', 'releases.html'), 'utf8')
  assert.ok(releases.includes(`https://github.com/kaimahi-agents/kaimahi/tree/${sourceRef}/internal/kmx/toolchain`))

  const repositoryMap = await readFile(path.join(dist, 'docs', 'repository-map.html'), 'utf8')
  assert.ok(repositoryMap.includes(`https://github.com/kaimahi-agents/kaimahi/blob/${sourceRef}/plugins/copilot/README.md`))
  assert.ok(repositoryMap.includes(`https://github.com/kaimahi-agents/kaimahi/blob/${sourceRef}/plugins/copilot-wsl/README.md`))

  const index = await readFile(path.join(dist, 'docs', 'index.html'), 'utf8')
  assert.ok(index.includes(`href="${agentSuiteDocsURL}"`))
})
