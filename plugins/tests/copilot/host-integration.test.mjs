// Opt-in cross-language fixture, not live Copilot qualification.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, rm, realpath } from 'node:fs/promises';
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
for (const mode of ['round trip', 'denial', 'cancelled', 'expired', 'batch', 'batch denial', 'batch limit', 'disabled', 'batch disabled', 'batch mixed', 'invalid settings', 'switch off', 'switch on', 'switch new', 'switch both', 'switch stale', 'switch noop', 'recording compare', 'recording uncalled']) test(`Go MCP plus Node runtime fixture: native tool ${mode}`, { skip: !binary, timeout: 60000 }, async t => {
  const recording = mode.startsWith('recording');
  const switching = mode.startsWith('switch'), newDeclaration = mode === 'round trip' || mode === 'switch new';
  const disabled = ['disabled', 'batch disabled', 'switch off', 'switch both'].includes(mode), mixed = mode === 'batch mixed', pair = mixed || mode === 'switch both' || recording, invalidSettings = mode === 'invalid settings';
  const batch = mode.startsWith('batch'), denied = mode.includes('denial'), overLimit = mode === 'batch limit', stopping = mode === 'cancelled' || mode === 'expired';
  let modelServer, modelError, sends = 0;
  if (batch || recording) {
    modelServer = createServer(async (req, res) => {
      try {
        const chunks = []; for await (const chunk of req) chunks.push(chunk);
        const body = JSON.parse(Buffer.concat(chunks)); sends++;
        assert.equal(req.url, '/chat/completions');
        if (recording) {
          const first = sends % 2 === 1 && mode !== 'recording uncalled';
          res.setHeader('Content-Type','application/json');
          res.end(JSON.stringify({model:body.model,choices:[{finish_reason:first?'tool_calls':'stop',message:first?{role:'assistant',tool_calls:Array.from({length:5},(_,i)=>({id:`call_${sends}_${i}`,type:'function',function:{name:i===4?'pong':'ping',arguments:'{}'}}))}:{role:'assistant',content:'done'}}]}));return;
        }
        const count = overLimit ? 6 : 5;
        if (sends === 2) {
          assert.equal(body.messages.length, 8);
          assert.equal(body.messages[2].tool_calls.length, 5);
          for (let i = 0; i < 5; i++) assert.equal(body.messages[3+i].tool_call_id, `call_${i}`);
        }
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ model: 'unit', choices: [{ finish_reason: sends === 1 ? 'tool_calls' : 'stop', message: sends === 1
          ? { role: 'assistant', tool_calls: Array.from({ length: count }, (_, i) => ({ id: `call_${i}`, type: 'function', function: { name: mixed && i % 2 ? 'pong' : 'ping', arguments: '{}' } })) }
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
  let configured = batch || recording ? source.replace('Preferred:** fake', 'Preferred:** unit') : newDeclaration ? source.replace('## Tools\n- ping\n\n', '') : source;
  if (pair) configured = configured.replace('- ping\n', '- ping\n- pong\n');
  if (recording) configured = configured.replace('    assertions:', '    mocks:\n      tools: [{name: ping, mode: recorded, calls: []}, {name: pong, mode: live}]\n    assertions:');
  await writeFile(path.join(root, 'fixture.agent.md'), configured, { mode: 0o600 });
  if (invalidSettings) await writeFile(path.join(root, 'kmx.yaml'), 'tools: {ping: Ask}\n', { mode: 0o600 });
  if (mode === 'disabled' || mode === 'batch disabled' || mixed || ['switch on', 'switch new'].includes(mode)) await writeFile(path.join(root, 'kmx.yaml'), 'tools:\n  ping: Disabled\n' + (mixed ? '  pong: Enabled\n' : ''), { mode: 0o600 });
  const original = await connect(binary, ['mcp', '--root', root, ...(batch || recording ? ['--provider', 'compatible', '--endpoint', `http://127.0.0.1:${modelServer.address().port}`] : [])], { env });
  const servers = new Map([['kmx-agent-builder', original]]), calls = [], previews = [];
  const session = { rpc: { mcp: {
    resources: { read: async ({ serverName, uri }) => {
      try { return await servers.get(serverName).request('resources/read', { uri }); }
      catch (e) { if (invalidSettings) throw Error('protocol_failed'); throw e; }
    } },
    listConfigured: async () => ({ servers: [...servers.keys()].map(name => ({ name })) }),
    startServer: async ({ serverName, config }) => { assert.ok(!config.env); servers.set(serverName, await connect(config.command, config.args, { cwd: config.cwd, env })); },
    stopServer: async ({ serverName }) => { assert.notEqual(serverName, 'kmx-agent-builder'); servers.get(serverName)?.close(); },
  }, tools: {
    initializeAndValidate: async () => {},
    getCurrentMetadata: async () => { if (stopping && ++reads === 2) { enter(); await gate; } return { tools: [
      ...[...servers].flatMap(([serverName]) => ['create', 'edit', 'run', 'test', ...(serverName === 'kmx-agent-builder' ? ['set_tool_state'] : [])].map(name => ({ name: `${serverName}:${name}`, mcpServerName: serverName, mcpToolName: name, description: name, input_schema: { type: 'object' } }))),
      ...['ping', 'pong'].map(name => ({ name: `actual-host-${name}`, mcpServerName: 'actual-host', mcpToolName: name, description: `Actual host fixture ${name}`, input_schema: { type: 'object', additionalProperties: false } })),
    ] }; },
    execute: async input => {
      if (['actual-host-ping', 'actual-host-pong'].includes(input.name)) { calls.push(input); observe({ phase: 'executed' }); return denied && (!batch || calls.length === 3) ? { resultType: 'denied', error: 'private-canary' } : { resultType: 'success', structuredContent: { pong: true } }; }
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
  if (invalidSettings) {
    await assert.rejects(readView(session, 'fixture.agent.md'), { code: 'invalid_tool_settings' }, 'fixed data must survive the native host discarding RPC errors');
    const act = createActionHandler({ session, path: 'fixture.agent.md' });
    const refused = await act({ operation: 'reload' });
    assert.equal(refused.error, 'invalid_tool_settings');
    assert.match(refused.message, /tool settings.*kmx\.yaml/i);
    assert.equal(calls.length, 0);
    act.dispose(); return;
  }
  const before = await readView(session, 'fixture.agent.md'); assert.deepEqual(before.tools, newDeclaration ? [] : pair ? ['ping', 'pong'] : ['ping']);
  if (mode === 'switch stale') await writeFile(path.join(root, 'kmx.yaml'), '# outside comment\ntools: {ping: Enabled}\n', { mode: 0o600 });
  if (recording) {
    const act = createActionHandler({session,path:'fixture.agent.md'});
    const result = await act({operation:'test',expectedRevision:before.file.revision,comparisonModels:['unit','unit-other']});
    assert.ifError(modelError);
    if (mode === 'recording uncalled') { assert.equal(result.error,'recording_missing',JSON.stringify(result)); assert.equal(sends,1); assert.equal(calls.length,0); }
    else {
      assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.saved,true);assert.equal(result.comparison.results.length,2);
      assert.equal(calls.filter(c=>c.name==='actual-host-ping').length,1,'Recorded executes once');assert.equal(calls.filter(c=>c.name==='actual-host-pong').length,3,'Live stays Live in capture and each model');assert.equal(sends,6,'five first-response requests and two sends per stage');
      for (const v of result.comparison.results) {assert.equal(v.file.revision,result.comparison.revision);assert.equal(v.latest.result.recordedReplayOnly,true);assert.equal(v.latest.result.examples[0].tools.find(t=>t.name==='ping').source,'recorded');assert.equal(v.latest.result.examples[0].tools.find(t=>t.name==='pong').source,'live');}
    }
    act.dispose();return;
  }
  const result = await createActionHandler({ session, path: 'fixture.agent.md' })({ operation: newDeclaration || switching ? 'save-and-test' : 'test', expectedRevision: before.file.revision,
    ...(newDeclaration ? { tools: ['ping'] } : {}), ...(switching ? { expectedSettingsRevision: before.toolSettingsRevision, toolSettings: [{ name: 'ping', state: disabled || mode === 'switch stale' ? 'Disabled' : 'Enabled' }, ...(mode === 'switch both' ? [{ name: 'pong', state: 'Disabled' }] : [])] } : {}) });
  if (mode === 'switch stale') {
    assert.equal(result.error, 'stale_settings_revision'); assert.equal(result.saved, false); assert.equal(previews.length, 0); assert.equal(calls.length, 0);
    assert.equal(await readFile(path.join(root, 'fixture.agent.md'), 'utf8'), configured); return;
  }
  if (switching) {
    assert.equal(result.error, undefined, JSON.stringify(result)); assert.equal(result.saved, mode !== 'switch noop');
    const setters = previews.filter(p => p.result.structuredContent?.operation === 'set_tool_state');
    assert.equal(setters.length, mode === 'switch both' ? 2 : 1); assert.ok(setters.every(p => p.serverName === 'kmx-agent-builder'));
    assert.equal(result.view.toolSettingsRevision, (await readView(session, 'fixture.agent.md')).toolSettingsRevision);
    if (!newDeclaration) assert.equal(await readFile(path.join(root, 'fixture.agent.md'), 'utf8'), configured, 'settings never rewrite source');
    if (mode === 'switch noop') { assert.equal(result.view.toolSettingsRevision, 'absent'); await assert.rejects(readFile(path.join(root, 'kmx.yaml')), { code: 'ENOENT' }); }
  }
  if (newDeclaration) {
    assert.equal(result.saved, true); assert.deepEqual(result.view.tools, ['ping']);
    assert.notEqual(result.view.file.revision, before.file.revision);
    assert.equal(previews[0].serverName, 'kmx-agent-builder', 'Edit stays on the original checked agent-file server');
  }
  assert.ifError(modelError);
  const liveCalls = disabled || overLimit ? 0 : mixed ? 2 : batch ? denied ? 3 : 5 : 1;
  assert.equal(calls.length, liveCalls);
  for (const call of calls) assert.equal(call.name, mixed ? 'actual-host-pong' : 'actual-host-ping');
  if (batch) assert.equal(sends, denied || overLimit ? 1 : 2);
  const preview = previews.find(p => p.result.structuredContent?.kind === 'kmx.agent-file-preview');
  assert.notEqual(preview.serverName, 'kmx-agent-builder');
  const payload = preview.result.structuredContent;
  assert.equal(payload.fullPass, !denied && !overLimit, JSON.stringify(payload));
  assert.equal(result.error, denied || overLimit ? 'action-failed' : undefined);
  assert.ok(!JSON.stringify(result).includes('private-canary'));
  if (!denied && !overLimit) { assert.equal(payload.examples[0].toolCalls ?? 0, liveCalls);
    assert.deepEqual(payload.toolSettings, [{ name: 'ping', state: disabled || mixed ? 'Disabled' : 'Enabled' }, ...(pair ? [{ name: 'pong', state: mixed ? 'Enabled' : 'Disabled' }] : [])]); assert.equal(result.view.latest.result.fullPass, true); }
});
