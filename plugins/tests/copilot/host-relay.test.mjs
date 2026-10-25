import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createPublicKey, generateKeyPairSync, sign, verify } from 'node:crypto';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import { createViewController, readView } from '../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs';
import { once } from 'node:events';
import { spawn } from 'node:child_process';
import { freezeTools, launchConfig, requestPayload, responsePayload, wslRequest, ensureHostRuntime, closeHostRuntime, previewServer, beginPreview } from '../../copilot-wsl/com.github.copilot/extensions/agent-view/host-relay.mjs';

const base = 'kmx-agent-builder';
const tool = { name: 'github-ping', mcpServerName: 'github', mcpToolName: 'ping', description: 'Host ping', input_schema: { type: 'object', additionalProperties: false } };
const descriptor = { schemaVersion: 1, kind: 'kmx.host-runtime', executable: '/usr/local/bin/kmx', cwd: '/work', goos: 'linux', distro: 'Ubuntu', args: ['mcp', '--provider', 'copilot'] };

test('WSL pipe uses the trusted distro/binary, signed bytes and no secret arguments or env', async () => {
  const body = JSON.stringify({ text: 'Unicode ☃ and "quotes"' });
  const options = { method: 'POST', headers: { 'X-Kmx-Time': 'time', 'X-Kmx-Nonce': 'nonce', 'X-Kmx-Signature': 'signature' }, body };
  const reply = await wslRequest(descriptor, 'http://127.0.0.1:1234', '/result/job', options, (command, args, opts) => {
    assert.equal(command, 'wsl.exe');
    assert.deepEqual(args, ['--distribution', 'Ubuntu', '--cd', '/work', '--exec', '/usr/local/bin/kmx', 'mcp', '--host-bridge-request', 'http://127.0.0.1:1234']);
    assert.equal(opts.shell, undefined); assert.equal(opts.env, undefined);
    return spawn(process.execPath, ['--input-type=module', '-e', `let b='';for await(const c of process.stdin)b+=c;const r=JSON.parse(b);if(r.path!='/result/job'||r.signature!='signature'||r.method!='POST'||r.time!='time'||r.nonce!='nonce')process.exit(1);console.log(JSON.stringify({status:200,signature:'response-proof',body:r.body}))`], opts);
  });
  assert.equal(reply.status, 200); assert.equal(reply.headers.get('X-Kmx-Signature'), 'response-proof');
  assert.equal(await reply.text(), body);
});

test('WSL pipe abort and oversized stdout fail closed without a fallback or retry', async () => {
  for (const oversized of [false, true]) {
    const abort = new AbortController(); let count = 0;
    const pending = wslRequest(descriptor, 'http://127.0.0.1:1234', '/next', { method: 'GET', headers: {}, signal: abort.signal }, (_command, _args, opts) => {
      count++; const child = spawn(process.execPath, ['-e', oversized ? `process.stdout.write('x'.repeat(193*1024))` : `process.stdin.resume();setInterval(()=>{},1000)`], opts);
      if (!oversized) child.once('spawn', () => abort.abort());
      return child;
    });
    await assert.rejects(pending, /Host-bound tools are unavailable/); assert.equal(count, 1);
  }
});

test('freeze raw host names with schemas; collision, missing schema and kmx tools refuse', () => {
  const selected = freezeTools([tool], ['ping'], [base]);
  assert.equal(selected.get('ping').name, 'github-ping');
  tool.input_schema.extra = true;
  assert.equal(selected.get('ping').input_schema.extra, undefined);
  delete tool.input_schema.extra;
  for (const metadata of [[tool, { ...tool, name: 'other-ping', mcpServerName: 'other' }], [{ ...tool, input_schema: undefined, deferLoading: true }], [{ ...tool, mcpServerName: base }], [{ ...tool, input_schema: { type: 'object', maximum: Number.MAX_SAFE_INTEGER + 1 } }]]) {
    assert.throws(() => freezeTools(metadata, ['ping'], [base]));
  }
});

test('trusted launch preserves route/root and uses only a public startup key', () => {
  const native = launchConfig(descriptor, 'public', 'linux');
  assert.deepEqual(native, { type: 'stdio', command: descriptor.executable, cwd: '/work', args: [...descriptor.args, '--host-bridge-public-key', 'public'], tools: ['run', 'test'] });
  const wsl = launchConfig(descriptor, 'public', 'win32');
  assert.equal(wsl.command, 'wsl.exe');
  assert.deepEqual(wsl.args.slice(0, 6), ['--distribution', 'Ubuntu', '--cd', '/work', '--exec', descriptor.executable]);
  assert.equal(wsl.env, undefined);
  assert.throws(() => launchConfig({ ...descriptor, distro: '' }, 'public', 'win32'));
  assert.throws(() => launchConfig({ ...descriptor, args: ['mcp', '--secret', 'bad'] }, 'public', 'linux'));
});

test('shared Go/Node signing vector verifies byte-for-byte', () => {
  const v = JSON.parse(readFileSync(new URL('../../../testdata/host-bridge-signing.json', import.meta.url), 'utf8'));
  const key = createPublicKey({ key: Buffer.from(v.publicKey, 'base64'), format: 'der', type: 'spki' });
  assert.ok(verify(null, Buffer.from(requestPayload(v.method, v.path, v.time, v.nonce, v.body)), key, Buffer.from(v.requestSignature, 'base64')));
  assert.ok(verify(null, Buffer.from(responsePayload(v.status, v.nonce, v.body)), key, Buffer.from(v.responseSignature, 'base64')));
});

test('canonical signed bytes pin exact method/path/time/nonce/body and response status', () => {
  const body = '{"output":{"ok":true}}';
  assert.equal(requestPayload('POST', '/result/abc', '1700000000', '010203', body), `POST\n/result/abc\n1700000000\n010203\n${body}`);
  assert.equal(responsePayload(200, '010203', body), `200\n010203\n${body}`);
});

async function fixture(t, { corrupt = false, stale = false, denied = false, unsafe = false, expired = false } = {}) {
  const keys = generateKeyPairSync('ed25519');
  const jobs = [{ id: 'a'.repeat(32), runId: 'b'.repeat(32), kind: 'resolve', names: ['ping'] }, { id: 'c'.repeat(32), runId: 'b'.repeat(32), kind: 'call', name: 'ping', arguments: {}, callId: 'call' }];
  const results = [], claims = [], calls = [], configs = [], stopped = []; let clientKey, reads = 0;
  const native = { ...descriptor, goos: ({ linux: 'linux', darwin: 'darwin', win32: 'windows' })[process.platform],
    ...(process.platform === 'win32' ? { executable: 'C:\\kmx.exe', cwd: 'C:\\work' } : {}) };
  const session = { rpc: { mcp: {
    resources: { read: async ({ serverName }) => ({ contents: [{ text: JSON.stringify(serverName === base ? native : { ...native, broker: { url, publicKey: keys.publicKey.export({ format: 'der', type: 'spki' }).toString('base64') } }) }] }) },
    listConfigured: async () => ({ servers: [{ name: base }] }),
    startServer: async (value) => { configs.push(value); const key = value.config.args.at(-1); clientKey = createPublicKey({ key: Buffer.from(key, 'base64'), format: 'der', type: 'spki' }); },
    stopServer: async ({ serverName }) => stopped.push(serverName),
  }, tools: {
    initializeAndValidate: async () => {},
    getCurrentMetadata: async () => ({ tools: [{ ...tool, description: stale && reads++ ? 'Changed' : tool.description }] }),
    execute: async (call) => { calls.push(call); return denied ? { resultType: 'denied', error: 'private-error-canary' } : { resultType: 'success', structuredContent: unsafe ? { id: Number.MAX_SAFE_INTEGER + 1 } : { pong: true } }; },
  } } };
  const server = http.createServer(async (req, res) => {
    let body = ''; for await (const chunk of req) body += chunk;
    const nonce = req.headers['x-kmx-nonce'];
    assert.ok(verify(null, Buffer.from(requestPayload(req.method, req.url, req.headers['x-kmx-time'], nonce, body)), clientKey, Buffer.from(req.headers['x-kmx-signature'], 'base64')));
    let answer = null, status = 200;
    if (req.url.startsWith('/claim/')) { assert.equal(body, ''); claims.push(req.url); if (expired) status = 410; }
    else if (req.url === '/next') { answer = jobs.shift() || null; if (!answer) await new Promise(r => setTimeout(r, 50)); }
    else { results.push({ path: req.url, value: JSON.parse(body) }); }
    const raw = JSON.stringify(answer);
    res.statusCode = status;
    res.setHeader('X-Kmx-Signature', sign(null, Buffer.from(responsePayload(status, nonce, raw)), corrupt ? generateKeyPairSync('ed25519').privateKey : keys.privateKey).toString('base64'));
    res.end(raw);
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening'); const url = `http://127.0.0.1:${server.address().port}`;
  t.after(async () => { await closeHostRuntime(session); server.closeAllConnections(); await new Promise(r => server.close(r)); });
  return { session, configs, stopped, results, claims, calls };
}

async function until(predicate) {
  const end = Date.now() + 3000;
  while (!predicate()) { if (Date.now() > end) assert.fail('relay did not finish'); await new Promise(r => setTimeout(r, 10)); }
}

test('session relay executes the frozen actual host tool once and strips SDK metadata', async t => {
  const f = await fixture(t); await ensureHostRuntime(f.session);
  await until(() => f.results.length === 2);
  assert.equal(f.calls.length, 1); assert.equal(f.calls[0].name, 'github-ping');
  assert.deepEqual(f.claims, ['/claim/' + 'c'.repeat(32)]);
  assert.deepEqual(f.results[0].value.definitions[0], { Name: 'ping', Description: 'Host ping', InputSchema: tool.input_schema });
  assert.deepEqual(f.results[1].value, { output: { pong: true } });
  assert.equal(f.calls[0].toolCallId, 'kmx-preview-' + 'c'.repeat(32), 'native host IDs never reuse model-chosen call IDs');
  assert.equal(f.configs[0].expectedInstallationId, undefined);
  assert.notEqual(previewServer(f.session), base);
  assert.equal(f.configs[0].config.args.filter(x => x === '--host-bridge-public-key').length, 1);
  assert.equal(createPublicKey({ key: Buffer.from(f.configs[0].config.args.at(-1), 'base64'), format: 'der', type: 'spki' }).type, 'public');
  assert.equal(Object.hasOwn(f.configs[0].config, 'env'), false);
  await closeHostRuntime(f.session); assert.deepEqual(f.stopped, [f.configs[0].serverName]);
});

for (const mode of ['stale', 'denied', 'unsafe', 'expired']) test(`${mode} host offer/result refuses without retry or raw errors`, async t => {
  const f = await fixture(t, { [mode]: true }); await ensureHostRuntime(f.session);
  await until(() => f.results.length === 2);
  assert.deepEqual(f.results[1].value, { error: true });
  assert.equal(f.calls.length, ['stale', 'expired'].includes(mode) ? 0 : 1);
  assert.ok(!JSON.stringify(f.results).includes('private-error-canary'));
});

test('failed child keeps a payload-free host recovery reason, not original-server advice', async t => {
  const f = await fixture(t);
  f.session.rpc.mcp.startServer = async () => { throw Error('private-startup-canary'); };
  await assert.rejects(ensureHostRuntime(f.session));
  await assert.rejects(readView(f.session, 'fixture.agent.md'), error => {
    assert.equal(error.code, 'unavailable');
    assert.equal(error.message, 'Host tools are unavailable in this session. Start a new Copilot session.');
    assert.ok(!error.message.includes('private-startup-canary'));
    return true;
  });
});

test('forged broker response never reaches native execution', async t => {
  const f = await fixture(t, { corrupt: true }); await ensureHostRuntime(f.session);
  await new Promise(r => setTimeout(r, 120)); assert.equal(f.calls.length, 0); assert.equal(f.results.length, 0);
});

test('closing a view keeps session observations pinned; runtime disposal never revives original results', async t => {
  const f = await fixture(t); await ensureHostRuntime(f.session); const child = previewServer(f.session);
  const controller = createViewController({ getSession: () => f.session });
  await controller.open({ instanceId: 'one', input: { path: 'fixture.agent.md' } }, { title: 'Fixture' });
  await controller.close({ instanceId: 'one' });
  assert.deepEqual(f.stopped, [], 'the session, not a canvas, owns the preview process');
  assert.equal(previewServer(f.session), child);
  await controller.open({ instanceId: 'two', input: { path: 'fixture.agent.md' } }, { title: 'Fixture' });
  assert.equal(previewServer(f.session), child);
  await controller.closeAll();
  assert.deepEqual(f.stopped, [child]);
  assert.throws(() => previewServer(f.session), /Host-bound tools are unavailable/, 'no original-server fallback after runtime teardown');
});

test('shutdown during metadata recheck cannot dispatch a new native effect', async t => {
  const f = await fixture(t), metadata = f.session.rpc.tools.getCurrentMetadata;
  let entered, release, reads = 0; const waiting = new Promise(r => { entered = r; }), gate = new Promise(r => { release = r; });
  f.session.rpc.tools.getCurrentMetadata = async () => { if (++reads === 2) { entered(); await gate; } return metadata(); };
  await ensureHostRuntime(f.session); await waiting;
  await closeHostRuntime(f.session); release(); await new Promise(setImmediate);
  assert.equal(f.calls.length, 0, 'teardown before dispatch must not start the tool');
});

test('pending activation reserves routing before another preview can choose the original server', async t => {
  const f = await fixture(t), read = f.session.rpc.mcp.resources.read;
  let entered, release; const waiting = new Promise(r => { entered = r; }), gate = new Promise(r => { release = r; });
  f.session.rpc.mcp.resources.read = async args => { if (args.serverName === base) { entered(); await gate; } return read(args); };
  const pending = ensureHostRuntime(f.session); await waiting;
  const late = beginPreview(f.session);
  try { assert.throws(() => previewServer(f.session), /Host-bound tools are unavailable/); }
  finally { late.release(); release(); await pending; }
});

test('runtime closed while startup is pending cleans up after start completes and never activates', async t => {
  const f = await fixture(t), events = [];
  let release, began; const gate = new Promise(r => { release = r; }), entered = new Promise(r => { began = r; });
  const start = f.session.rpc.mcp.startServer, stop = f.session.rpc.mcp.stopServer;
  f.session.rpc.mcp.startServer = async args => { began(); await gate; await start(args); events.push('started'); };
  f.session.rpc.mcp.stopServer = async args => { events.push('stopped'); await stop(args); };
  const pending = assert.rejects(ensureHostRuntime(f.session), /Host-bound tools are unavailable/);
  await entered; await closeHostRuntime(f.session); release(); await pending;
  assert.equal(events.at(-1), 'stopped', 'a pre-start stop cannot leave a late-started child unowned');
  assert.equal(events.filter(e => e === 'stopped').length, 1);
});

test('do not switch observation servers during another active original preview', async t => {
  const f = await fixture(t); const first = beginPreview(f.session), second = beginPreview(f.session);
  await assert.rejects(ensureHostRuntime(f.session, second)); assert.equal(f.configs.length, 0);
  first.release(); second.release();
});
