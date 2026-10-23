import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import { createOpenHandler, readOpenView, viewInstanceId, OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS } from "./open.mjs";

const PATH = "greeter.agent.md";
const PROVIDER = "plugin:kmx-agent-builder:agent-view";
function setup({ renderer = true, readError, badResource = false, openError, path = PATH } = {}) {
  const calls = [];
  const session = { capabilities: { ui: { canvases: renderer } }, rpc: {
    mcp: { resources: { read: async (input) => {
      calls.push(["read", input]); if (readError) throw readError;
      return { contents: [{ text: JSON.stringify({ schemaVersion: 1, kind: "kmx.agent-view", file: { path: badResource ? "other.agent.md" : path, revision: "sha256:" + "a".repeat(64) }, title: "Greeter", instructions: "Reply hello.", model: "fake", examples: [], latest: { status: "none" } }) }] };
    } } },
    canvas: { open: async (input) => {
      calls.push(["open", input]); if (openError) throw openError;
      return { url: "http://127.0.0.1:12345/private-bridge-token/", extensionId: "private-provider", ...input };
    } },
  } };
  return { calls, session, run: createOpenHandler({ getSession: () => session, extensionId: PROVIDER }) };
}

test("instance IDs are stable portable identifiers, not paths, and distinguish layouts", () => {
  assert.equal(viewInstanceId("greeter.agent.md"), "kmx-agent-greeter");
  assert.equal(viewInstanceId(".squad/agents/greeter/charter.md"), "kmx-squad-greeter");
  for (const path of ["greeter.agent.md", ".squad/agents/greeter/charter.md", "a".repeat(63) + ".agent.md"]) {
    assert.match(viewInstanceId(path), /^[a-z][a-z0-9-]{0,72}$/);
    assert.doesNotMatch(viewInstanceId(path), /[:./]/);
  }
  assert.throws(() => viewInstanceId("../README.md"), { code: "invalid-path" });
});

test("same-named agents in different repos have stable distinct canvas IDs", async () => {
  const paths = ["/repo one/greeter.agent.md", "/repo two/greeter.agent.md", "/repo Māori/.squad/agents/greeter/charter.md", "nested/Release Notes.agent.md"];
  const ids = paths.map(viewInstanceId);
  assert.equal(new Set(ids).size, paths.length);
  for (const [i, path] of paths.entries()) {
    assert.equal(viewInstanceId(path), ids[i]);
    assert.match(ids[i], /^[a-z][a-z0-9-]{0,72}$/);
    const f = setup({ path });
    assert.equal((await f.run({ path })).opened, true);
    assert.deepEqual(f.calls[0], ["read", { serverName: "kmx-agent-builder", uri: `kmx://agent/${encodeURIComponent(path)}` }]);
    assert.deepEqual(f.calls[1][1], { extensionId: PROVIDER, canvasId: "kmx-agent-view", instanceId: ids[i], input: { path } });
  }
});

test("shared preflight validates the bound resource without opening a canvas", async () => {
  const f = setup();
  const result = await readOpenView(f.session, { path: PATH });
  assert.equal(result.path, PATH); assert.equal(result.view.title, "Greeter");
  assert.deepEqual(f.calls.map(c => c[0]), ["read"]);
});

test("native UI tool accepts only path, never a root or additional argument", () => {
  assert.equal(OPEN_TOOL_NAME, "open_kmx_canvas");
  assert.deepEqual(OPEN_TOOL_PARAMETERS.required, ["path"]);
  assert.deepEqual(Object.keys(OPEN_TOOL_PARAMETERS.properties), ["path"]);
  assert.equal(OPEN_TOOL_PARAMETERS.additionalProperties, false);
});
test("one authoritative read precedes one stable canvas open and response omits token", async () => {
  const f = setup(); const result = await f.run({ path: PATH });
  assert.equal(result.opened, true); assert.equal(result.mode, "canvas");
  assert.deepEqual(f.calls, [
    ["read", { serverName: "kmx-agent-builder", uri: "kmx://agent/greeter.agent.md" }],
    ["open", { extensionId: PROVIDER, canvasId: "kmx-agent-view", instanceId: "kmx-agent-greeter", input: { path: PATH } }],
  ]);
  assert.doesNotMatch(JSON.stringify(result), /private-bridge-token|127\.0\.0\.1|private-provider/);
  assert.equal((await f.run({ path: PATH })).opened, true);
  assert.deepEqual(f.calls[1][1], f.calls[3][1]);
});
test("non-agent paths and extra arguments have zero effects", async () => {
  const f = setup();
  for (const input of [null, [], {}, { path: PATH, root: "/other" }, { path: "../README.md" }, { path: "/some/README.md" }, { path: "greeter.agent.md ignored" }]) {
    const result = await f.run(input);
    assert.equal(result.opened, false);
    assert.equal(result.reasonCode, "invalid-path");
  }
  assert.deepEqual(f.calls, []);
});
test("resource refusal and invalid projection never launch a canvas or disclose errors", async () => {
  for (const [options, reason] of [[{ readError: new Error("secret endpoint") }, "unavailable"], [{ badResource: true }, "invalid-resource"]]) {
    const f = setup(options); const result = await f.run({ path: PATH });
    assert.equal(result.opened, false); assert.equal(result.reasonCode, reason);
    assert.deepEqual(f.calls.map(c => c[0]), ["read"]);
    assert.doesNotMatch(JSON.stringify(result), /secret endpoint/);
  }
});
test("no renderer yields existing text projection with no canvas call", async () => {
  for (const renderer of [false, undefined]) {
    const f = setup({ renderer });
    if (renderer === undefined) delete f.session.capabilities.ui;
    const result = await f.run({ path: PATH });
    assert.equal(result.mode, "text"); assert.match(result.text, /Title: Greeter/);
    assert.match(result.text, /Latest: none/); assert.deepEqual(f.calls.map(c => c[0]), ["read"]);
  }
});
test("advertised renderer failure is a safe refusal, never a retry or text substitute", async () => {
  const f = setup({ openError: Object.assign(new Error("http://secret/token"), { code: -32603 }) });
  const result = await f.run({ path: PATH });
  assert.equal(result.opened, false); assert.equal(result.reasonCode, "canvas-unavailable");
  assert.equal(result.text, undefined);
  assert.doesNotMatch(JSON.stringify(result), /secret|token|-32603/);
  assert.deepEqual(f.calls.map(c => c[0]), ["read", "open"]);
});
test("no joined session is a static refusal", async () => {
  const run = createOpenHandler({ getSession: () => undefined, extensionId: PROVIDER });
  const result = await run({ path: PATH });
  assert.equal(result.opened, false); assert.equal(result.reasonCode, "unavailable");
});
test("explicit provider disambiguates two extensions with the same canvas ID", async () => {
  const f = setup();
  f.session.rpc.canvas.open = async (input) => {
    f.calls.push(["open", input]);
    if (input.extensionId !== PROVIDER) throw new Error("Canvas ID is ambiguous between providers");
    return { canvasId: input.canvasId, instanceId: input.instanceId, extensionId: PROVIDER };
  };
  assert.equal((await f.run({ path: PATH })).opened, true);
  assert.equal(f.calls[1][1].extensionId, PROVIDER);
});

test("actual extension registers one explicit opener and the kmx skill, with no load-time open", async () => {
  const f = setup(); let registration;
  globalThis.__kmxJoin = async (config) => { registration = config; return f.session; };
  const stub = 'data:text/javascript,' + encodeURIComponent('export class CanvasError extends Error { constructor(code,message){super(message);this.code=code;} }; export const createCanvas=(config)=>config; export const joinSession=(config)=>globalThis.__kmxJoin(config);');
  const hooks = registerHooks({ resolve(specifier, context, next) { return specifier === "@github/copilot-sdk/extension" ? { url: stub, shortCircuit: true } : next(specifier, context); } });
  try {
    await import("./extension.mjs");
    assert.deepEqual(f.calls, [], "joining must not read an agent or open a canvas");
    assert.deepEqual(registration.tools.map(t => t.name), ["open_kmx_canvas"]);
    assert.deepEqual(registration.commands.map(c => c.name), ["agent-view"]);
    assert.equal(registration.canvases[0].id, "kmx-agent-view");
    assert.equal((await registration.tools[0].handler({ path: PATH })).opened, true);
    assert.deepEqual(f.calls.map(c => c[0]), ["read", "open"]);
    assert.equal(f.calls[1][1].extensionId, PROVIDER);
    const canvas = registration.canvases[0];
    const ctx = { instanceId: "direct-provider-proof", input: { path: PATH } };
    f.calls.length = 0;
    const readResource = f.session.rpc.mcp.resources.read;
    try {
      const result = await canvas.open(ctx);
      assert.equal(typeof result.url, "string");
      assert.deepEqual(f.calls.map(c => c[0]), ["read"], "built-in dispatch must validate before returning the bridge");
      assert.equal((await canvas.open(ctx)).url, result.url, "reopening reuses the bridge");
      assert.deepEqual(f.calls.map(c => c[0]), ["read", "read"], "reopening still validates the resource");
      f.session.rpc.mcp.resources.read = async () => { throw new Error("private transport error"); };
      await assert.rejects(() => canvas.open(ctx), { code: "unavailable" });
      f.session.rpc.mcp.resources.read = readResource;
      assert.equal((await canvas.open(ctx)).url, result.url, "a refused reopen does not close the existing bridge");
    } finally {
      f.session.rpc.mcp.resources.read = readResource;
      await canvas.onClose(ctx);
    }
    const skill = await readFile(new URL("../../../skills/kmx/SKILL.md", import.meta.url), "utf8");
    for (const eol of ["\n", "\r\n"]) {
      assert.match(skill.replace(/\r?\n/g, eol), /^---\r?\nname: kmx\r?\n/);
    }
    assert.match(skill, /built-in `open_canvas` exactly once/);
    const payload = JSON.parse(skill.match(/```json\s*([\s\S]*?)```/)[1]);
    assert.deepEqual(payload, { extensionId: PROVIDER, canvasId: "kmx-agent-view", instanceId: "<INSTANCE>", input: { path: "<PATH>" } });
    assert.match(skill, /`<id>\.agent\.md`[^\n]*`kmx-agent-<id>`/);
    assert.match(skill, /`\.squad\/agents\/<id>\/charter\.md`[^\n]*`kmx-squad-<id>`/);
    assert.doesNotMatch(skill, /\"root\"\s*:/);
    const builder = await readFile(new URL("../../../skills/kmx-agent-builder/SKILL.md", import.meta.url), "utf8");
    assert.match(builder, /After (?:a )?successful Create/);
    assert.match(builder, /structuredContent\.file\.path/);
    assert.match(builder, /open_canvas/);
    assert.match(builder, /open_kmx_canvas/);
    assert.doesNotMatch(builder, /ask the user to\s+confirm|read \[the configured selected root\]/);
    for (const eol of ["\n", "\r\n"]) {
      const text = builder.replace(/\r?\n/g, eol);
      const native = text.split(/## Native Squad\r?\n/)[1].split("## Deterministic orientation")[0];
      assert.match(native, /Before native Create/);
      assert.match(native, /approval/);
      for (const effect of ["proposal", "MCP", "skills", ".gitignore", ".gitattributes"]) assert.ok(native.includes(effect));
      const ordinary = text.split(/## Create, then open\r?\n/)[1].split("## Native Squad")[0];
      assert.doesNotMatch(ordinary, /ask.*(?:approval|confirmation)|obtain.*approval/i);
    }
  } finally { hooks.deregister(); delete globalThis.__kmxJoin; }
});
