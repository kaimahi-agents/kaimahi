import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const docs = fileURLToPath(new URL('..', import.meta.url))
const commit = '6281ce09a3d7f4f2fc4b3a556d602b07eeab7ffa'
const names = ['agent', 'build-profile', 'composition', 'eval-case', 'eval-index', 'suite', 'tool-provider-composition', 'tool-provider'].map(name => `${name}.schema.json`).sort()
const hash = bytes => createHash('sha256').update(bytes).digest('hex')

export async function checkSchemas(directory, provenance) {
  assert.equal(provenance.repository, 'https://github.com/agent-primitives/suite', 'schema provenance mismatch: repository')
  assert.equal(provenance.tag, 'v0.1.0', 'schema provenance mismatch: tag')
  assert.equal(provenance.commit, commit, 'schema provenance mismatch: commit')
  assert.equal(provenance.sourceDirectory, 'schema', 'schema provenance mismatch: source directory')
  assert.deepEqual(Object.keys(provenance.files).sort(), names, 'schema inventory mismatch: provenance')
  assert.deepEqual((await readdir(directory)).sort(), names, 'schema inventory mismatch: assets')
  for (const name of names) {
    const bytes = await readFile(path.join(directory, name))
    assert.equal(hash(bytes), provenance.files[name], `schema hash mismatch: ${name}`)
    assert.equal(JSON.parse(bytes).$id, `https://kaimahi.dev/schemas/agentsuite/v1/${name}`, `schema ID mismatch: ${name}`)
  }
}

async function fetchBytes(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(15000), redirect: 'error' })
  assert.ok(response.ok, `schema download failed: HTTP ${response.status} ${url}`)
  return { bytes: Buffer.from(await response.arrayBuffer()), type: response.headers.get('content-type') }
}

async function main() {
  const option = process.argv[2]
  assert.ok(['--upstream', '--live'].includes(option), 'usage: node .vitepress/schemas.mjs --upstream|--live')
  const provenance = JSON.parse(await readFile(path.join(docs, 'agentsuite-schemas.json'), 'utf8'))
  const directory = path.join(docs, 'public/schemas/agentsuite/v1')
  await checkSchemas(directory, provenance)
  for (const name of names) {
    const url = option === '--live'
      ? `https://kaimahi.dev/schemas/agentsuite/v1/${name}`
      : `https://raw.githubusercontent.com/agent-primitives/suite/${commit}/schema/${name}`
    const { bytes, type } = await fetchBytes(url)
    assert.deepEqual(bytes, await readFile(path.join(directory, name)), `schema parity mismatch: ${url}`)
    console.log(`200 ${type} ${url} SHA256=${hash(bytes)}`)
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  await main()
}
