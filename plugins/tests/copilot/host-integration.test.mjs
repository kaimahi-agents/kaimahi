// Opt-in cross-language fixture, not live Copilot qualification.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { readView, createActionHandler } from '../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs';
import { closeHostRuntime, ensureHostRuntime, previewServer } from '../../copilot-wsl/com.github.copilot/extensions/agent-view/host-relay.mjs';

async function connect(command, args, options) {
  const child = spawn(command, args, { ...options, stdio: ['pipe', 'pipe', 'pipe'] });
  const pending = new Map(); let id = 0, buffer = '', diagnostics = '';
  child.stderr.on('data', b => { diagnostics += b; });
  child.stdout.on('data', b => {
    buffer += b;
    while (buffer.includes('\n')) {
      const end = buffer.indexOf('\n'), line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
      if (!line) continue;
      const message = JSON.parse(line), waiter = pending.get(message.id);
      if (waiter) { pending.delete(message.id); message.error ? waiter.reject(Error('MCP fixture refused')) : waiter.resolve(message.result); }
    }
  });
  child.on('error', e => { for (const p of pending.values()) p.reject(e); pending.clear(); });
  child.on('exit', () => { for (const p of pending.values()) p.reject(Error('MCP fixture exited')); pending.clear(); });
  const request = (method, params) => new Promise((resolve, reject) => { const key = ++id; pending.set(key, { resolve, reject }); child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: key, method, params }) + '\n'); });
  await request('initialize', { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 'runtime-fixture', version: '1' } });
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');
  return { request, cancelLast: () => child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/cancelled', params: { requestId: id } }) + '\n'), close: () => child.stdin.end(), get diagnostics() { return diagnostics; } };
}

const binary = process.env.KMX_HOST_BRIDGE_TEST_BINARY;
for (const mode of ['round trip', 'denial', 'cancelled', 'expired', 'batch', 'batch denial', 'batch limit']) test(`Go MCP plus Node runtime fixture: native tool ${mode}`, { skip: !binary, timeout: 60000 }, async t => {
  const batch = mode.startsWith('batch'), denied = mode.includes('denial'), overLimit = mode === 'batch limit', stopping = mode === 'cancelled' || mode === 'expired';
  let modelServer, modelError, sends = 0;
  if (batch) {
    modelServer = createServer(async (req, res) => {
      try {
        const chunks = []; for await (const chunk of req) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks)); sends++;
        assert.equal(req.url, '/chat/completions');
        const count = overLimit ? 6 : 5;
        if (sends === 2) {
          assert.equal(body.messages.length, 8);
          assert.equal(body.messages[2].tool_calls.length, 5);
          for (let i = 0; i < 5; i++) assert.equal(body.messages[3+i].tool_call_id, `call_${i}`);
        }
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ model: 'unit', choices: [{ finish_reason: sends === 1 ? 'tool_calls' : 'stop', message: sends === 1
          ? { role: 'assistant', tool_calls: Array.from({ length: count }, (_, i) => ({ id: `call_${i}`, type: 'function', function: { name: 'ping', arguments: '{}' } })) }
          : { role: 'assistant', content: 'done' } }] }));
      } catch (error) { modelError = error; res.writeHead(500); res.end(); }
    });
    await new Promise(resolve => modelServer.listen(0, '127.0.0.1', resolve));
  }
  let enter, release, observe, reads = 0;
  const entered = new Promise(r => { enter = r; }), gate = new Promise(r => { release = r; }), dispatch = new Promise(r => { observe = r; });
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'kmx-host-fixture-')));
  const state = path.join(root, 'state'); await mkdir(state, { mode: 0o700 });
  const env = { ...process.env, KMX_HOME: state };
  const source = '# Host fixture\n\n## Identity\n- **ID:** fixture\n- **Purpose:** One host read.\n\n## Accountable Responsibilities\nCall the tool `ping`. Reply with exactly `done`.\n\n## Non-Responsibilities\nNo other calls.\n\n## Collaboration and Review Authority\nReturn the answer.\n\n## Model\n- **Preferred:** fake\n\n## Tools\n- ping\n\n## Examples\n```yaml\nexamples:\n  - id: read\n    input: read\n    assertions:\n      - id: complete\n        type: contains\n        value: done\n```\n';
  await writeFile(path.join(root, 'fixture.agent.md'), batch ? source.replace('Preferred:** fake', 'Preferred:** unit') : mode === 'round trip' ? source.replace('## Tools\n- ping\n\n', '') : source, { mode: 0o600 });
  const original = await connect(binary, ['mcp', '--root', root, ...(batch ? ['--provider', 'compatible', '--endpoint', `http://127.0.0.1:${modelServer.address().port}`] : [])], { env });
  const servers = new Map([['kmx-agent-builder', original]]), calls = [], previews = [];
  const session = { rpc: { mcp: {
    resources: { read: ({ serverName, uri }) => servers.get(serverName).request('resources/read', { uri }) },
    listConfigured: async () => ({ servers: [...servers.keys()].map(name => ({ name })) }),
    startServer: async ({ serverName, config }) => { assert.ok(!config.env); servers.set(serverName, await connect(config.command, config.args, { cwd: config.cwd, env })); },
    stopServer: async ({ serverName }) => { assert.notEqual(serverName, 'kmx-agent-builder'); servers.get(serverName)?.close(); },
  }, tools: {
    initializeAndValidate: async () => {},
    getCurrentMetadata: async () => { if (stopping && ++reads === 2) { enter(); await gate; } return { tools: [
      ...[...servers].flatMap(([serverName]) => ['create', 'edit', 'run', 'test'].map(name => ({ name: `${serverName}:${name}`, mcpServerName: serverName, mcpToolName: name, description: name, input_schema: { type: 'object' } }))),
      { name: 'actual-host-ping', mcpServerName: 'actual-host', mcpToolName: 'ping', description: 'Actual host fixture ping', input_schema: { type: 'object', additionalProperties: false } },
    ] }; },
    execute: async input => {
      if (input.name === 'actual-host-ping') { calls.push(input); observe({ phase: 'executed' }); return denied && (!batch || calls.length === 3) ? { resultType: 'denied', error: 'private-canary' } : { resultType: 'success', structuredContent: { pong: true } }; }
      const [serverName, name] = input.name.split(':');
      const result = await servers.get(serverName).request('tools/call', { name, arguments: input.arguments }); previews.push({ serverName, result });
      return { resultType: result.isError ? 'failure' : 'success', structuredContent: result.structuredContent };
    },
  } } };
  t.after(async () => { release(); await closeHostRuntime(session); for (const server of servers.values()) server.close(); if (modelServer) await new Promise(resolve => modelServer.close(resolve)); await new Promise(r => setTimeout(r, 150)); await rm(root, { recursive: true, force: true }); });
  if (stopping) {
    const fetch = globalThis.fetch;
    t.mock.method(globalThis, 'fetch', async (url, options) => {
      const response = await fetch(url, options);
      if (options?.method === 'POST' && new URL(url).pathname.startsWith('/claim/')) observe({ phase: 'claim', status: response.status });
      return response;
    });
    await ensureHostRuntime(session);
    const child = servers.get(previewServer(session));
    const running = child.request('tools/call', { name: 'test', arguments: { schemaVersion: 1, path: 'fixture.agent.md' } });
    await entered;
    if (mode === 'cancelled') child.cancelLast();
    const stopped = (await running).structuredContent;
    assert.equal(stopped.fullPass, false);
    if (mode === 'cancelled') { assert.equal(stopped.state, 'cancelled'); assert.equal(stopped.reasonCode, 'cancelled'); }
    else { assert.equal(stopped.state, 'error'); assert.equal(stopped.reasonCode, 'execution_failed'); }
    assert.equal(calls.length, 0, 'no native effect when cancellation/expiry has completed');
    release();
    assert.deepEqual(await dispatch, { phase: 'claim', status: 410 });
    await new Promise(setImmediate);
    assert.equal(calls.length, 0, 'metadata returning later must not initiate an effect');
    return;
  }
  const before = await readView(session, 'fixture.agent.md'); assert.deepEqual(before.tools, mode === 'round trip' ? [] : ['ping']);
  const result = await createActionHandler({ session, path: 'fixture.agent.md' })({ operation: mode === 'round trip' ? 'save-and-test' : 'test', expectedRevision: before.file.revision, ...(mode === 'round trip' ? { tools: ['ping'] } : {}) });
  if (mode === 'round trip') {
    assert.equal(result.saved, true); assert.deepEqual(result.view.tools, ['ping']);
    assert.notEqual(result.view.file.revision, before.file.revision);
    assert.equal(previews[0].serverName, 'kmx-agent-builder', 'Edit stays on the original checked agent-file server');
  }
  assert.ifError(modelError);
  assert.equal(calls.length, overLimit ? 0 : batch ? denied ? 3 : 5 : 1);
  for (const call of calls) assert.equal(call.name, 'actual-host-ping');
  if (batch) assert.equal(sends, denied || overLimit ? 1 : 2);
  const preview = previews.find(p => p.result.structuredContent?.kind === 'kmx.agent-file-preview');
  assert.notEqual(preview.serverName, 'kmx-agent-builder');
  const payload = preview.result.structuredContent;
  assert.equal(payload.fullPass, !denied && !overLimit, JSON.stringify(payload));
  assert.equal(result.error, denied || overLimit ? 'action-failed' : undefined);
  assert.ok(!JSON.stringify(result).includes('private-canary'));
  if (!denied && !overLimit) { assert.equal(payload.examples[0].toolCalls, batch ? 5 : 1); assert.equal(result.view.latest.result.fullPass, true); }
});
