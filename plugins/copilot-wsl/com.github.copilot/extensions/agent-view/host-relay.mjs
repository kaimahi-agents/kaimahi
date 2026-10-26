// Runtime-only relay: private signing keys never cross MCP or process startup.
import { createPublicKey, generateKeyPairSync, randomBytes, sign, verify } from "node:crypto";
import { isDeepStrictEqual } from "node:util";
import path from "node:path";
import { spawn } from "node:child_process";

const ORIGINAL = "kmx-agent-builder", URI = "kmx://host-runtime";
const states = new WeakMap();
const object = v => v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v, max = 4096) => typeof v === "string" && v.length > 0 && v.length <= max && !v.includes("\0");
export class HostRuntimeUnavailable extends Error {}
const unavailable = () => new HostRuntimeUnavailable("Host-bound tools are unavailable for the declared names.");
export const requestPayload = (method, route, time, nonce, body) => `${method}\n${route}\n${time}\n${nonce}\n${body}`;
export const responsePayload = (status, nonce, body) => `${status}\n${nonce}\n${body}`;

function stateFor(session) {
  if (!states.has(session)) states.set(session, { leases: new Set(), runs: new Map(), calls: new Map(), observers: new Set(), child: `kmx-agent-preview-${randomBytes(12).toString("hex")}`, abort: new AbortController() });
  return states.get(session);
}
export function previewServer(session) {
  const s = states.get(session);
  if (s?.start && !s.active && !s.closed && !s.failed) throw unavailable();
  if (!s?.activated) return ORIGINAL;
  if (!s.active || s.closed || s.failed) throw unavailable();
  return s.child;
}
export function beginPreview(session) {
  const s = stateFor(session), lease = { original: !s.start && !s.activated, release: () => s.leases.delete(lease) };
  s.leases.add(lease); return lease;
}

// Only exact native outer progress plus an actual signed relay job can own an
// inner permission prompt. Neither a shared session nor an ID prefix is proof.
export function observePreview(session, outerToolCallId) {
  const s = stateFor(session), owned = new Set([outerToolCallId]), jobs = new Map(), requests = new Map();
  let closed = false, deadline = false, pendingAtClose = false, generation = 0;
  const complete = id => { generation++; owned.delete(id); for (const [request, tool] of requests) if (tool === id) requests.delete(request); };
  const observer = {
    attach(job) { if (!closed && jobs.get(job.id) === job.runId) owned.add(job.toolCallId); },
    complete,
    progress(data) {
      if (closed || data?.kind !== "kmx.host-progress" || data.schemaVersion !== 1 ||
          !/^[a-f0-9]{32}$/.test(data.runId) || !/^[a-f0-9]{32}$/.test(data.jobId)) return;
      const call = s.calls.get(data.jobId);
      // Lifetime inventory includes up to 128 cases with five calls each.
      if (data.phase === "call" && jobs.size < 128 * 5) { jobs.set(data.jobId, data.runId); if (call) observer.attach(call); }
      if (data.phase === "deadline" && call?.runId === data.runId && jobs.get(data.jobId) === data.runId &&
          [...requests.values()].includes(call.toolCallId)) deadline = true;
    },
    async refresh() {
      if (closed || typeof session.rpc?.permissions?.pendingRequests !== "function") return;
      const started = ++generation;
      let timer;
      try {
        // Joined extensions may receive completion but not requested events.
        const result = await Promise.race([session.rpc.permissions.pendingRequests(),
          new Promise((_, reject) => { timer = setTimeout(() => reject(unavailable()), 1000); })]);
        if (closed || started !== generation || !Array.isArray(result?.items) || result.items.length > 128) return;
        requests.clear();
        for (const item of result.items) {
          const id = item?.request?.toolCallId;
          if (owned.has(id) && text(item.requestId, 1024)) requests.set(item.requestId, id);
        }
      } catch { /* Optional metadata cannot settle or block the Test invocation. */ }
      finally { clearTimeout(timer); }
    },
    waiting: () => !closed && requests.size > 0,
    timedOut: () => deadline,
    pendingTool: () => closed ? pendingAtClose : owned.size > 1,
    clear() { if (closed) return; pendingAtClose = owned.size > 1; closed = true; generation++; for (const off of subscriptions) off?.(); s.observers.delete(observer); owned.clear(); jobs.clear(); requests.clear(); },
  };
  const subscriptions = typeof session.on !== "function" ? [] : [
    session.on("permission.requested", event => {
      const data = event?.data, id = data?.permissionRequest?.toolCallId;
      if (!closed && !data?.resolvedByHook && owned.has(id) && text(data.requestId, 1024) && requests.size < 128) { generation++; requests.set(data.requestId, id); }
    }),
    session.on("permission.completed", event => {
      const data = event?.data, id = requests.get(data?.requestId);
      // A completion may precede our first hydration of its request ID.
      if (text(data?.requestId, 1024)) generation++;
      if (owned.has(data?.toolCallId) || id && (data.toolCallId === undefined || data.toolCallId === id)) requests.delete(data.requestId);
    }),
  ];
  s.observers.add(observer); return observer;
}

// SDK JSON has already crossed a JS-number wire: unsafe integers might be rounded.
function safeNumbers(value) {
  return typeof value === "number" ? Number.isFinite(value) && (!Number.isInteger(value) || Number.isSafeInteger(value))
    : !object(value) && !Array.isArray(value) || Object.values(value).every(safeNumbers);
}

export function freezeTools(metadata, names, excluded, allowMissing = false) {
  if (!Array.isArray(metadata) || !Array.isArray(names) || !names.length || names.length > 32 || new Set(names).size !== names.length) throw unavailable();
  const selected = new Map();
  for (const raw of names) {
    if (!/^[A-Za-z_][A-Za-z0-9_-]{0,63}$/.test(raw)) throw unavailable();
    const candidates = metadata.filter(t => t?.mcpToolName === raw && !excluded.includes(t.mcpServerName));
    if (allowMissing && candidates.length === 0) continue;
    if (candidates.length !== 1 || !text(candidates[0].mcpServerName, 512)) throw unavailable();
    const t = candidates[0];
    if (!text(t.name, 512) || !object(t.input_schema) || !safeNumbers(t.input_schema) || typeof t.description !== "string" || t.description.length > 4096) throw unavailable();
    selected.set(raw, structuredClone({ name: t.name, mcpServerName: t.mcpServerName, mcpToolName: raw, description: t.description, input_schema: t.input_schema }));
  }
  if (Buffer.byteLength(JSON.stringify([...selected.values()])) > 64 * 1024) throw unavailable();
  return selected;
}

export function launchConfig(d, publicKey, platform = process.platform) {
  if (!object(d) || d.kind !== "kmx.host-runtime" || d.schemaVersion !== 1 || !text(d.executable) || !text(d.cwd) || !Array.isArray(d.args) || d.args[0] !== "mcp") throw unavailable();
  const flags = new Set();
  for (let i = 1; i < d.args.length; i += 2) {
    if (!["--root", "--provider", "--endpoint"].includes(d.args[i]) || flags.has(d.args[i]) || !text(d.args[i + 1])) throw unavailable();
    flags.add(d.args[i]);
  }
  const args = [...d.args, "--host-bridge-public-key", publicKey];
  if (platform === "win32" && d.goos === "linux" && text(d.distro, 256) && path.posix.isAbsolute(d.executable) && path.posix.isAbsolute(d.cwd)) {
    return { type: "stdio", command: "wsl.exe", args: ["--distribution", d.distro, "--cd", d.cwd, "--exec", d.executable, ...args], tools: ["run", "test"] };
  }
  if (({ win32: "windows", linux: "linux", darwin: "darwin" })[platform] !== d.goos || !(platform === "win32" ? path.win32 : path.posix).isAbsolute(d.executable)) throw unavailable();
  return { type: "stdio", command: d.executable, cwd: d.cwd, args, tools: ["run", "test"] };
}

async function runtimeResource(session, serverName) {
  const result = await session.rpc.mcp.resources.read({ serverName, uri: URI });
  const content = result?.contents;
  if (!Array.isArray(content) || content.length !== 1 || typeof content[0].text !== "string" || Buffer.byteLength(content[0].text) > 16 * 1024) throw unavailable();
  return JSON.parse(content[0].text);
}

// WSL loopback need not be forwarded to Windows. The trusted binary proxies one
// signed packet over stdio; no private key, credential, environment or shell.
export function wslRequest(d, address, route, options, spawnProcess = spawn) {
  return new Promise((resolve, reject) => {
    const child = spawnProcess("wsl.exe", ["--distribution", d.distro, "--cd", d.cwd, "--exec", d.executable, "mcp", "--host-bridge-request", address],
      { stdio: ["pipe", "pipe", "ignore"], windowsHide: true, signal: options.signal });
    const chunks = []; let size = 0;
    child.on("error", () => reject(unavailable()));
    child.stdin.on("error", () => reject(unavailable()));
    child.stdout.on("data", chunk => {
      size += chunk.length;
      if (size > 192 * 1024) { child.kill(); reject(unavailable()); } else chunks.push(chunk);
    });
    child.on("close", code => {
      try {
        if (code !== 0 || size > 192 * 1024) throw unavailable();
        const reply = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        resolve(new Response(Buffer.from(reply.body, "base64"), { status: reply.status, headers: { "X-Kmx-Signature": reply.signature } }));
      } catch { reject(unavailable()); }
    });
    child.stdin.end(JSON.stringify({ method: options.method, path: route, time: options.headers["X-Kmx-Time"], nonce: options.headers["X-Kmx-Nonce"],
      signature: options.headers["X-Kmx-Signature"], body: Buffer.from(options.body ?? "").toString("base64") }));
  });
}

async function send(s, method, route, value) {
  const body = value === undefined ? "" : JSON.stringify(value);
  if (Buffer.byteLength(body) > 64 * 1024) throw unavailable();
  const nonce = randomBytes(16).toString("hex"), time = String(Math.floor(Date.now() / 1000));
  const options = {
    method, redirect: "error", signal: AbortSignal.any([s.abort.signal, AbortSignal.timeout(15000)]),
    headers: { "Content-Type": "application/json", "X-Kmx-Time": time, "X-Kmx-Nonce": nonce, "X-Kmx-Signature": sign(null, Buffer.from(requestPayload(method, route, time, nonce, body)), s.privateKey).toString("base64") },
    ...(method === "POST" ? { body } : {}),
  };
  const response = s.wsl ? await wslRequest(s.wsl, s.url, route, options) : await fetch(s.url + route, options);
  const reader = response.body.getReader(), chunks = []; let size = 0;
  while (true) {
    const { done, value: chunk } = await reader.read(); if (done) break;
    size += chunk.length; if (size > 128 * 1024) { await reader.cancel(); throw unavailable(); }
    chunks.push(chunk);
  }
  const raw = Buffer.concat(chunks).toString("utf8");
  if (!verify(null, Buffer.from(responsePayload(response.status, nonce, raw)), s.serverKey, Buffer.from(response.headers.get("X-Kmx-Signature") || "", "base64"))) throw unavailable();
  if (!response.ok) { const e = unavailable(); e.stale = response.status === 410; throw e; }
  return JSON.parse(raw);
}

async function pump(session, s) {
  const excluded = [ORIGINAL, s.child];
  while (!s.closed) {
    const job = await send(s, "GET", "/next");
    if (job === null) continue;
    if (!object(job) || !/^[a-f0-9]{32}$/.test(job.id) || !/^[a-f0-9]{32}$/.test(job.runId)) throw unavailable();
    if (job.kind === "close") { s.runs.delete(job.runId); continue; }
    let answer;
    try {
      for (const [id, run] of s.runs) if (run.until < Date.now()) s.runs.delete(id);
      if (job.kind === "resolve") {
        if (s.runs.size >= 32 || s.runs.has(job.runId)) throw unavailable();
        const metadata = await session.rpc.tools.getCurrentMetadata();
        const tools = freezeTools(metadata?.tools, job.names, excluded, job.allowMissing === true);
        s.runs.set(job.runId, { tools, seen: new Set(), until: Date.now() + 5 * 60 * 1000 });
        answer = { definitions: [...tools.values()].map(t => ({ Name: t.mcpToolName, Description: t.description, InputSchema: t.input_schema })),
          ...(job.allowMissing === true ? { missing: job.names.filter(name => !tools.has(name)) } : {}) };
      } else if (job.kind === "call") {
        const run = s.runs.get(job.runId), tool = run?.tools.get(job.name);
        if (!tool || run.seen.has(job.id) || !object(job.arguments) || Buffer.byteLength(JSON.stringify(job.arguments)) > 8192 || !text(job.callId, 1024)) throw unavailable();
        const current = freezeTools((await session.rpc.tools.getCurrentMetadata())?.tools, [job.name], excluded).get(job.name);
        if (s.closed || s.failed || run.until < Date.now() || !isDeepStrictEqual(tool, current)) throw unavailable();
        run.seen.add(job.id);
        // One-shot Go-side claim commits dispatch only while this call is live.
        await send(s, "POST", `/claim/${job.id}`);
        if (s.closed || s.failed) throw unavailable();
        const call = { id: job.id, runId: job.runId, toolCallId: `kmx-preview-${job.id}` };
        s.calls.set(job.id, call); for (const observer of s.observers) observer.attach(call);
        let result;
        try { result = await session.rpc.tools.execute({ name: tool.name, arguments: job.arguments, toolCallId: call.toolCallId }); }
        finally { s.calls.delete(job.id); for (const observer of s.observers) observer.complete(call.toolCallId); }
        if (result?.resultType !== "success") throw unavailable();
        const output = result.structuredContent ?? (typeof result.textResultForLlm === "string" ? { text: result.textResultForLlm } : undefined);
        if (output === undefined || !safeNumbers(output)) throw unavailable();
        answer = { output: object(output) ? output : { value: output } };
        if (Buffer.byteLength(JSON.stringify(answer)) > 32 * 1024) throw unavailable();
      } else throw unavailable();
    } catch { answer = { error: true }; }
    if (s.closed) return;
    try { await send(s, "POST", `/result/${job.id}`, answer); }
    catch (e) { if (!e.stale) throw e; s.runs.delete(job.runId); }
  }
}

export async function ensureHostRuntime(session, lease) {
  const s = stateFor(session);
  if (s.failed || s.closed || s.leases.size > (s.leases.has(lease) ? 1 : 0)) throw unavailable();
  if (!s.start) s.start = (async () => {
    const rpc = session.rpc;
    if (typeof rpc?.mcp?.startServer !== "function" || typeof rpc.mcp.listConfigured !== "function" || typeof rpc.tools?.initializeAndValidate !== "function") throw unavailable();
    if ((await rpc.mcp.listConfigured())?.servers?.some(t => t.name === s.child) || s.closed) throw unavailable();
    const d = await runtimeResource(session, ORIGINAL); if (s.closed) throw unavailable();
    const keys = generateKeyPairSync("ed25519");
    s.privateKey = keys.privateKey;
    const publicKey = keys.publicKey.export({ format: "der", type: "spki" }).toString("base64");
    const config = launchConfig(d, publicKey); s.activated = true;
    s.wsl = process.platform === "win32" && d.goos === "linux" ? d : undefined;
    try { await rpc.mcp.startServer({ serverName: s.child, config }); }
    finally { s.started = true; } // Even ambiguous startup must clean up this reserved name.
    if (s.closed) throw unavailable();
    const bound = await runtimeResource(session, s.child), url = new URL(bound?.broker?.url);
    if (s.closed) throw unavailable();
    if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port || url.username || url.password || url.search || url.hash || !["/", ""].includes(url.pathname)) throw unavailable();
    s.url = url.origin;
    s.serverKey = createPublicKey({ key: Buffer.from(bound.broker.publicKey, "base64"), format: "der", type: "spki" });
    if (s.serverKey.asymmetricKeyType !== "ed25519") throw unavailable();
    await rpc.tools.initializeAndValidate();
    if (s.closed || [...s.leases].some(other => other !== lease && other.original)) throw unavailable();
    s.active = true;
    void pump(session, s).catch(() => { s.failed = true; });
  })().catch(async e => { s.failed = true; await closeHostRuntime(session); throw e; });
  await s.start;
}

export async function closeHostRuntime(session) {
  const s = states.get(session); if (!s) return;
  s.closed = true; s.abort.abort(); s.runs.clear(); s.calls.clear();
  for (const observer of s.observers) observer.clear();
  s.privateKey = undefined;
  if (s.started) {
    s.started = false;
    try { await session.rpc.mcp.stopServer({ serverName: s.child }); } catch { /* Session teardown also reaps this child. */ }
  }
  // Keep a weak tombstone: teardown must never revive the original process\u0027s older observation.
}
