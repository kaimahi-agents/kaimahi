import assert from 'node:assert/strict'
import { cp, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const docs = fileURLToPath(new URL('..', import.meta.url))
const assets = path.join(docs, 'public/schemas/agentsuite/v1')
const dist = path.join(docs, '.vitepress/dist/schemas/agentsuite/v1')
const manifest = () => readFile(path.join(docs, 'agentsuite-schemas.json'), 'utf8').then(JSON.parse)
const checker = () => import('./schemas.mjs')

test('published schema assets and provenance match the immutable release', async () => {
  const { checkSchemas } = await checker()
  await checkSchemas(assets, await manifest())
  await checkSchemas(dist, await manifest())
})

test('schema integrity rejects changed, missing, extra and misidentified files', async () => {
  const { checkSchemas } = await checker()
  const temp = await mkdtemp(path.join(os.tmpdir(), 'schema-integrity-'))
  try {
    const provenance = await manifest()
    for (const kind of ['changed', 'missing', 'extra', 'id', 'provenance']) {
      const directory = path.join(temp, kind)
      await cp(assets, directory, { recursive: true })
      const file = path.join(directory, 'agent.schema.json')
      const altered = structuredClone(provenance)
      if (kind === 'changed') await writeFile(file, (await readFile(file)) + '\n')
      if (kind === 'missing') await rm(file)
      if (kind === 'extra') await writeFile(path.join(directory, 'extra.json'), '{}')
      if (kind === 'id') {
        const { createHash } = await import('node:crypto')
        const document = JSON.parse(await readFile(file, 'utf8'))
        document.$id = 'https://wrong.invalid/agent.schema.json'
        const bytes = JSON.stringify(document)
        await writeFile(file, bytes)
        altered.files['agent.schema.json'] = createHash('sha256').update(bytes).digest('hex')
      }
      if (kind === 'provenance') altered.commit = 'wrong'
      await assert.rejects(checkSchemas(directory, altered), /schema (inventory|hash|ID|provenance) mismatch/, kind)
    }
  } finally {
    await rm(temp, { recursive: true, force: true })
  }
})
