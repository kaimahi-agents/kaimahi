// Real stdio MCP/embedded Sessions with a trusted fake host. The optional model
// endpoint is scripted loopback HTTP, never live-provider qualification.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, mkdir, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { createViewController, readView } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs";

async function connect(binary, args, options) {
  const child = spawn(binary, args, { ...options, stdio: ["pipe", "pipe", "pipe"] }), pending = new Map();
  let id = 0, buffer = "";
  child.stderr.resume();
  child.stdout.on("data", chunk => {
    buffer += chunk;
    while (buffer.includes("\n")) {
      const at = buffer.indexOf("\n"), line = buffer.slice(0, at); buffer = buffer.slice(at + 1);
      if (!line) continue;
      const message = JSON.parse(line), waiter = pending.get(message.id);
      if (waiter) { pending.delete(message.id); message.error ? waiter.reject(Error("MCP fixture refused")) : waiter.resolve(message.result); }
    }
  });
  const failed = () => { for (const waiter of pending.values()) waiter.reject(Error("MCP fixture exited")); pending.clear(); };
  child.on("error", failed); child.on("exit", failed);
  const request = (method, params) => new Promise((resolve, reject) => { const key = ++id; pending.set(key, { resolve, reject }); child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: key, method, params }) + "\n"); });
  await request("initialize", { protocolVersion: "2025-11-25", capabilities: {}, clientInfo: { name: "paired-canvas-fixture", version: "1" } });
  child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
  return { request, close: () => child.stdin.end() };
}

export async function pairedFixture(t, binary, { toolFree = false, comparing = false, tests, agentPath = "a.agent.md", instructions = "Call the tool `ping`. Reply with exactly `done`.", failModel = false } = {}) {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), "kmx-paired-canvas-"))), state = path.join(root, "state");
  await mkdir(state, { mode: 0o700 });
  const env = { ...process.env, KMX_HOME: state }, tools = [], previews = [], configured = new Map();
  let sends = 0, modelServer;
  if (comparing) {
    modelServer = createServer(async (req, res) => {
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks)); sends++;
      if (failModel) { res.writeHead(500, { "Content-Type": "application/json" }); res.end(JSON.stringify({ error: { message: "Scripted model failure" } })); return; }
      const first = body.messages.length === 2;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ model: body.model, choices: [{ finish_reason: first ? "tool_calls" : "stop", message: first ? { role: "assistant", tool_calls: [{ id: "call_" + sends, type: "function", function: { name: "ping", arguments: "{}" } }] } : { role: "assistant", content: "done" } }] }));
    });
    await new Promise(resolve => modelServer.listen(0, "127.0.0.1", resolve));
  }
  const source = `---\nmodel: ${comparing ? "unit" : "fake"}\n${toolFree ? "" : "tools: [ping]\n"}---\n${instructions}\n`;
  const suite = "---\r\nschemaVersion: 1\r\n---\r\n\r\n# Keep this prose.\r\n\r\n```yaml agent-test\r\nid: read\r\ninput: read\r\n" + (toolFree ? "" : "mocks:\r\n  tools: [{name: ping, mode: recorded, calls: []}]\r\n") + "assertions: [{id: complete, type: contains, value: done}]\r\n```\r\n";
  const args = ["mcp", ...(comparing ? ["--provider", "compatible", "--endpoint", `http://127.0.0.1:${modelServer.address().port}`] : [])];
  const original = await connect(binary, args, { cwd: root, env }), servers = new Map([["kmx-agent-builder", original]]);
  const create = await original.request("tools/call", { name: "create", arguments: { schemaVersion: 3, path: agentPath, content: source, testsContent: suite } });
  assert.equal(create.isError, undefined, "paired Create must be successful");
  const session = { rpc: { mcp: {
    resources: { read: ({ serverName, uri }) => servers.get(serverName).request("resources/read", { uri }) },
    listConfigured: async () => ({ servers: [...servers.keys()].map(name => ({ name })) }),
    startServer: async ({ serverName, config }) => { configured.set(serverName, config.tools); servers.set(serverName, await connect(config.command, config.args, { cwd: config.cwd, env })); },
    stopServer: async ({ serverName }) => servers.get(serverName)?.close(),
  }, tools: {
    initializeAndValidate: async () => {},
    getCurrentMetadata: async () => ({ tools: [...servers.keys()].flatMap(serverName => (configured.get(serverName) ?? ["create", "edit", "run", "test", "set_tool_state"]).map(name => ({ name: serverName + ":" + name, mcpServerName: serverName, mcpToolName: name, description: name, input_schema: { type: "object" } }))).concat([{ name: "fixture-ping", mcpServerName: "host", mcpToolName: "ping", description: "Fixture ping", input_schema: { type: "object", additionalProperties: false } }]) }),
    execute: async call => {
      if (call.name === "fixture-ping") { tools.push(call); return { resultType: "success", structuredContent: { number: 42 } }; }
      const [serverName, name] = call.name.split(":");
      previews.push({ serverName, name, arguments: call.arguments });
      const result = await servers.get(serverName).request("tools/call", { name, arguments: call.arguments });
      return { resultType: result.isError ? "failure" : "success", structuredContent: result.structuredContent };
    },
  } } };
  const controller = createViewController({ getSession: () => session });
  t.after(async () => { await controller.closeAll(); for (const server of servers.values()) server.close(); if (modelServer) await new Promise(resolve => modelServer.close(resolve)); await new Promise(resolve => setTimeout(resolve, 150)); await rm(root, { recursive: true, force: true }); });
  if (tests) await writeFile(path.join(root, tests), suite + "\r\nSelected override.\r\n", { mode: 0o600 });
  const initial = await readView(session, agentPath, tests);
  const opened = await controller.open({ instanceId: "paired-fixture", input: { path: agentPath, ...(tests !== undefined ? { tests } : {}) } }, initial);
  const read = async () => (await fetch(opened.url + "view.json")).json();
  const act = async (operation, values = {}) => {
    const view = await read();
    const response = await fetch(opened.url + "actions", { method: "POST", headers: { Origin: new URL(opened.url).origin, "Content-Type": "application/json" }, body: JSON.stringify({ operation, ...(!["reload", "suggest"].includes(operation) ? { expectedRevision: view.file.revision, expectedSourceVersion: view.selectedSources.sourceVersion, ...(view.toolSettingsRevision !== undefined ? { expectedSettingsRevision: view.toolSettingsRevision } : {}) } : {}), ...values }) });
    return response.json();
  };
  return { root, session, controller, url: opened.url, source, suite, tools, previews, read, act, get sends() { return sends; } };
}
