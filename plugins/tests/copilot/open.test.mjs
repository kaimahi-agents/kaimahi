import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import { createOpenHandler, createOpenCommand, readOpenView, viewInstanceId, OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/open.mjs";

const PATH = "greeter.agent.md";
const PROVIDER = "plugin:kmx-agent-builder:agent-view";
const SOURCE = "# Greeter\n\nReply hello.\n";
const REVISION = "sha256:" + createHash("sha256").update(SOURCE).digest("hex");
function setup({ renderer = true, readError, badResource = false, resourceError, openError, path = PATH, authoring = true, editable = true } = {}) {
  const calls = [];
  const session = {
    log: async (message, options) => { assert.equal(typeof message, "string", "SDK log takes a string"); calls.push(["log", message, options]); },
    send: () => { throw new Error("Native opening must not send a model message"); },
    capabilities: { ui: { canvases: renderer } }, rpc: {
    mcp: { resources: { read: async (input) => {
      calls.push(["read", input]); if (readError) throw readError;
      if (resourceError) return { contents: [{ text: JSON.stringify(resourceError) }] };
      return { contents: [{ text: JSON.stringify({ schemaVersion: 1, kind: "kmx.agent-view", file: { path: badResource ? "other.agent.md" : path, revision: REVISION }, title: "Greeter", instructions: "Reply hello.", model: "fake", examples: [], ...(authoring ? { authoring: { source: SOURCE, editable } } : {}), latest: { status: "none" } }) }] };
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
  const ids = paths.map(path => viewInstanceId(path));
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

test("native UI tool accepts path and optional selected tests, never a root or additional argument", () => {
  assert.equal(OPEN_TOOL_NAME, "open_kmx_canvas");
  assert.deepEqual(OPEN_TOOL_PARAMETERS.required, ["path"]);
  assert.deepEqual(Object.keys(OPEN_TOOL_PARAMETERS.properties), ["path", "tests"]);
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
  assert.equal(result.file, undefined, "canvas success must not duplicate source authority");
  assert.equal(result.authoring, undefined, "canvas success must not return source");
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
test("invalid check resource explains the file needs fixing without exposing parser details", async () => {
  const f = setup({ resourceError: { schemaVersion: 1, kind: "kmx.agent-view-error", reasonCode: "invalid_file", message: "PRIVATE SOURCE /private/path" } });
  const result = await f.run({ path: PATH });
  assert.equal(result.opened, false);
  assert.equal(result.reasonCode, "invalid-file");
  assert.equal(result.message, "This agent file needs fixing before it can open. Check its format and test fields.");
  assert.deepEqual(f.calls.map(c => c[0]), ["read"]);
  assert.doesNotMatch(JSON.stringify(result), /PRIVATE|private|MCP server/);
  const malformed = setup({ resourceError: { schemaVersion: 2, kind: "kmx.agent-view-error", reasonCode: "invalid_file" } });
  assert.equal((await malformed.run({ path: PATH })).reasonCode, "invalid-resource");
});

test("an unadvertised renderer is still tried; text only when the canvas can't open", async () => {
  // A resumed app session can omit the canvas capability while canvas.open works.
  for (const renderer of [false, undefined]) {
    const f = setup({ renderer });
    if (renderer === undefined) delete f.session.capabilities.ui;
    assert.equal((await f.run({ path: PATH })).mode, "canvas");
    assert.deepEqual(f.calls.map(c => c[0]), ["read", "open"]);
    const failing = setup({ renderer, openError: new Error("no renderer") });
    if (renderer === undefined) delete failing.session.capabilities.ui;
    const result = await failing.run({ path: PATH });
    assert.equal(result.mode, "text"); assert.equal(result.canvasFailed, true); assert.match(result.text, /Title: Greeter/);
    assert.deepEqual(result.file, { path: PATH, revision: REVISION });
    assert.deepEqual(result.authoring, { source: SOURCE, editable: true });
    assert.match(result.text, new RegExp(REVISION));
    assert.match(result.text, /Latest: none/); assert.deepEqual(failing.calls.map(c => c[0]), ["read", "open"]);
    const missing = setup({ renderer }); delete missing.session.rpc.canvas;
    if (renderer === undefined) delete missing.session.capabilities.ui;
    const text = await missing.run({ path: PATH });
    assert.equal(text.mode, "text");
    assert.deepEqual(text.file, { path: PATH, revision: REVISION });
    assert.deepEqual(text.authoring, { source: SOURCE, editable: true });
    assert.deepEqual(missing.calls.map(c => c[0]), ["read"]);
  }
});
test("text fallback never manufactures edit authority from bounded display text", async () => {
  for (const [options, want] of [
    [{ authoring: false }, { editable: false, reasonCode: "complete_source_unavailable" }],
    [{ editable: false }, { source: SOURCE, editable: false, reasonCode: "source_not_editable" }],
  ]) {
    const f = setup({ renderer: false, ...options }); delete f.session.rpc.canvas;
    const result = await f.run({ path: PATH });
    assert.equal(result.mode, "text");
    assert.deepEqual(result.file, { path: PATH, revision: REVISION });
    assert.deepEqual(result.authoring, want);
    assert.match(result.text, new RegExp(REVISION));
  }
});
test("text fallback keeps exact complete source separate from visibly truncated text", async () => {
  const source = "# Large\n\n" + "x".repeat(12000) + "\n";
  const revision = "sha256:" + createHash("sha256").update(source).digest("hex");
  const f = setup({ renderer: false }); delete f.session.rpc.canvas;
  f.session.rpc.mcp.resources.read = async (input) => {
    f.calls.push(["read", input]);
    return { contents: [{ text: JSON.stringify({
      schemaVersion: 1, kind: "kmx.agent-view", file: { path: PATH, revision },
      title: "Large", instructions: source, model: "fake", examples: [],
      authoring: { source, editable: true }, latest: { status: "none" },
    }) }] };
  };
  const result = await f.run({ path: PATH });
  assert.equal(result.authoring.source, source);
  assert.deepEqual(result.file, { path: PATH, revision });
  assert.ok(result.text.length <= 6000);
  assert.match(result.text, /\[truncated\]/);
  assert.notEqual(result.text, source);
});
test("advertised renderer failure is a safe refusal, never a retry or text substitute", async () => {
  const f = setup({ openError: Object.assign(new Error("http://secret/token"), { code: -32603 }) });
  const result = await f.run({ path: PATH });
  assert.equal(result.opened, false); assert.equal(result.reasonCode, "canvas-unavailable");
  assert.equal(result.text, undefined);
  assert.equal(result.authoring, undefined);
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

for (const [variant, provider] of [["copilot-wsl", PROVIDER], ["copilot", "plugin:kmx-agent-builder-path:agent-view"]])
test(`actual ${variant} extension opens its own provider and registers the kmx skill, with no load-time open`,
  { skip: variant === "copilot" && process.platform === "win32" }, async () => {
  const f = setup(); let registration;
  globalThis.__kmxJoin = async (config) => { registration = config; return f.session; };
  const stub = 'data:text/javascript,' + encodeURIComponent('export class CanvasError extends Error { constructor(code,message){super(message);this.code=code;} }; export const createCanvas=(config)=>config; export const joinSession=(config)=>globalThis.__kmxJoin(config);');
  const hooks = registerHooks({ resolve(specifier, context, next) { return specifier === "@github/copilot-sdk/extension" ? { url: stub, shortCircuit: true } : next(specifier, context); } });
  try {
    await import(`../../${variant}/com.github.copilot/extensions/agent-view/extension.mjs`);
    assert.deepEqual(f.calls, [], "joining must not read an agent or open a canvas");
    assert.deepEqual(registration.tools.map(t => t.name), ["open_kmx_canvas", "suggest_kmx_tests"]);
    assert.equal((await registration.tools[1].handler({requestId:"no-live-request",examples:[]})).queued,false);
    assert.deepEqual(f.calls,[],"failed draft delivery never reads, opens, saves or tests");
    assert.deepEqual(registration.commands.map(c => c.name), ["squad-view", "agent-view", "kmx-canvas"]);
    assert.equal(registration.canvases[0].id, "kmx-agent-view");
    assert.equal(registration.canvases[1].id, "kmx-squad-view");
    assert.equal((await registration.tools[0].handler({ path: PATH })).opened, true);
    assert.deepEqual(f.calls.map(c => c[0]), ["read", "open"]);
    assert.equal(f.calls[1][1].extensionId, provider);
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
    const command = registration.commands.find(c => c.name === "kmx-canvas").handler;
    for (const args of ["", " \t", "help", " \thelp\t ", "open", "open ", "openfoo.agent.md", "run greeter.agent.md"]) {
      f.calls.length = 0;
      await command({ args });
      assert.equal(f.calls.length, 1, "help and malformed commands must not read or open");
      assert.equal(f.calls[0][0], "log");
      assert.match(f.calls[0][1], /Usage: \/kmx-canvas open <path>/);
      assert.deepEqual(f.calls[0][2], { level: "error" }, "the app shows only error-level command output");
    }
    f.calls.length = 0;
    await command({ args: "open greeter.agent.md" });
    await command({ args: "open greeter.agent.md" });
    assert.deepEqual(f.calls.map(c => c[0]), ["read", "open", "log", "read", "open", "log"]);
    assert.deepEqual(f.calls[1][1], { extensionId: provider, canvasId: "kmx-agent-view", instanceId: "kmx-agent-greeter", input: { path: PATH } });
    assert.deepEqual(f.calls[1], f.calls[4], "native reopen focuses the same instance");
    assert.equal(f.calls[2][1], "Opened greeter.agent.md in the agent canvas.");
    const nativeRead = f.session.rpc.mcp.resources.read;
    try {
      for (const path of ["/repo Māori/Release Notes.agent.md", " greeter.agent.md", "\tgreeter.agent.md"]) {
        const literal = setup({ path });
        f.session.rpc.mcp.resources.read = literal.session.rpc.mcp.resources.read;
        f.calls.length = 0;
        await command({ args: "\t open\t" + path });
        assert.deepEqual(literal.calls, [["read", { serverName: "kmx-agent-builder", uri: "kmx://agent/" + encodeURIComponent(path) }]]);
        assert.equal(f.calls[0][0], "open");
        assert.equal(f.calls[0][1].input.path, path, "capture is not trimmed or split");
      }
    } finally { f.session.rpc.mcp.resources.read = nativeRead; }
    for (const args of ["open greeter.agent.md ", 'open "greeter.agent.md"', "open README.md"]) {
      f.calls.length = 0;
      await command({ args });
      assert.equal(f.calls.length, 1, "invalid literal path must not be repaired or read");
      assert.deepEqual([f.calls[0][0], f.calls[0][2]], ["log", { level: "error" }]);
    }
    f.session.capabilities.ui.canvases = false;
    const advertisedOpen = f.session.rpc.canvas.open;
    f.session.rpc.canvas.open = async () => { throw new Error("no renderer"); };
    f.calls.length = 0;
    await command({ args: "open greeter.agent.md" });
    assert.deepEqual(f.calls.map(c => c[0]), ["read", "log", "log"]);
    assert.deepEqual(f.calls[1].slice(1), ["The canvas couldn't open here, so the agent is shown as text.", { level: "error" }]);
    assert.match(f.calls[2][1], /Title: Greeter/);
    f.session.rpc.canvas.open = advertisedOpen;
    f.session.capabilities.ui.canvases = true;
    const nativeOpen = f.session.rpc.canvas.open;
    try {
      for (const failure of ["read", "open"]) {
        const denied = async () => { throw new Error("http://private/token"); };
        f.session.rpc.mcp.resources.read = failure === "read" ? denied : nativeRead;
        f.session.rpc.canvas.open = failure === "open" ? denied : nativeOpen;
        f.calls.length = 0;
        await command({ args: "open greeter.agent.md" });
        assert.deepEqual(f.calls.map(c => c[0]), failure === "read" ? ["log"] : ["read", "log"]);
        assert.deepEqual(f.calls.at(-1)[2], { level: "error" });
        assert.doesNotMatch(f.calls.at(-1)[1], /private|token|http/);
      }
    } finally { f.session.rpc.mcp.resources.read = nativeRead; f.session.rpc.canvas.open = nativeOpen; }
    const skill = await readFile(new URL(`../../${variant}/skills/kmx/SKILL.md`, import.meta.url), "utf8");
    for (const eol of ["\n", "\r\n"]) {
      assert.match(skill.replace(/\r?\n/g, eol), /^---\r?\nname: kmx\r?\n/);
    }
    assert.match(skill, /built-in `open_canvas` exactly once/);
    const payload = JSON.parse(skill.match(/```json\s*([\s\S]*?)```/)[1]);
    assert.deepEqual(payload, { extensionId: provider, canvasId: "kmx-agent-view", instanceId: "<INSTANCE>", input: { path: "<PATH>" } });
    assert.match(skill, /`<id>\.agent\.md`[^\n]*`kmx-agent-<id>`/);
    assert.match(skill, /`\.squad\/agents\/<id>\/charter\.md`[^\n]*`kmx-squad-<id>`/);
    assert.doesNotMatch(skill, /\"root\"\s*:/);
    const builder = await readFile(new URL(`../../${variant}/skills/kmx-agent-builder/SKILL.md`, import.meta.url), "utf8");
    assert.match(builder, /After (?:a )?successful Create/);
    assert.match(builder, /structuredContent\.file\.path/);
    assert.match(builder, /unbound server uses Copilot/);
    assert.match(builder, /MCP server `kmx-agent-builder`/);
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

for (const variant of ["copilot-wsl", "copilot"]) test(`${variant} skill help distinguishes a missing extension from a text fallback`, async () => {
  const opener = await readFile(new URL(`../../${variant}/skills/kmx/SKILL.md`, import.meta.url), "utf8");
  const entry = variant === "copilot" ? "kmx-agent-builder-path" : "kmx-agent-builder";
  assert.ok(opener.includes(`Use /${entry}:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`));
  assert.doesNotMatch(opener, /`\/kmx(?: help| open|`)/);
  assert.match(opener, /If `open_kmx_canvas` is offered/);
  assert.match(opener, /successful text view/);
  assert.match(opener, /authoring\.source/);
  assert.match(opener, /Never edit from the\s+bounded `text` field/);
  const absent = opener.split("## No opener available")[1];
  assert.ok(absent?.includes("Agent not opened: this session has no kmx canvas or text opener. Use a Copilot host with the kmx extension loaded."));
  assert.doesNotMatch(absent, /\/agent-view/);
  const builder = await readFile(new URL(`../../${variant}/skills/kmx-agent-builder/SKILL.md`, import.meta.url), "utf8");
  assert.match(builder, /description: .*running an input.*saved tests/);
  assert.match(builder, /Created <basename> and opened it in the text view/);
  assert.match(builder, /Saved tests \(not run\)/);
  assert.match(builder, /Added 1 test to <file> \(not run\)/);
  assert.match(builder, /Added <count> tests to <file> \(not run\)/);
  assert.match(builder, /After an ordinary\s+full-content Edit/);
  assert.match(builder, /Edit with `exampleEdits`/);
  assert.match(builder, /obtain a checked complete authoring\s+snapshot/);
  assert.match(builder, /withhold further edits/);
  assert.match(builder, /returned file path and revision/);
  assert.match(builder, /returned provider\/model/);
  assert.match(builder, /identity mismatch is \*\*Not current\*\*/);
  assert.match(builder, /result includes `savedRevision`/);
  assert.match(builder, /held source and revision as stale/);
  assert.match(builder, /Before any later Run, Test or Edit, read a\s+checked agent resource for the same path/);
  assert.match(builder, /never pair the old source with\s+the saved revision/);
  assert.match(builder, /MCP preflight refusal/);
  assert.match(builder, /exactly\s+`schemaVersion`, `reasonCode` and `reason`, with no `kind`/);
  assert.match(builder, /never\s+report a pass/);
  assert.match(builder, /require `kind: kmx\.agent-file-preview`/);
  assert.ok(builder.indexOf("MCP preflight refusal") < builder.indexOf("Require the returned file path and revision"));
  for (const marker of ["structuredContent", "Full pass", "Partial pass", "Inconclusive", "Refused", "Cancelled", "Did not finish", "showContent: true", "never rerun"]) assert.ok(builder.includes(marker), marker);
});

for (const variant of ["copilot-wsl", "copilot"]) test(`${variant} builder waits for a saved revision without using execution as discovery`, async () => {
  const skill = (await readFile(new URL(`../../${variant}/skills/kmx-agent-builder/SKILL.md`, import.meta.url), "utf8")).replaceAll("`", "").replace(/\s+/g, " ");
  assert.ok(skill.includes("Never fabricate expectedRevision, including an all-zero hash, or call Run/Test solely to learn a revision."));
  assert.ok(skill.includes("If the saved source or current revision is unavailable, do not call Edit; report that nothing was saved."));
  assert.ok(skill.includes("For an add-tests request that cannot be saved, include this conditional next step: If your kmx canvas offers Suggest tests, use it to send the saved source and revision to chat."));
  assert.ok(skill.includes("describe missing saved source and revision, not host implementation terms"));
  assert.ok(skill.includes("If no agent-resource reader is available, follow the no-save recovery above."));
});

for (const variant of ["copilot-wsl", "copilot"]) test(`${variant} defaults new tests to paired Create and keeps checked contexts distinct from raw hashes`,async()=>{
  const builder=await readFile(new URL(`../../${variant}/skills/kmx-agent-builder/SKILL.md`,import.meta.url),"utf8");
  const create=builder.split("## Create, then open")[1].split("## Paired saved sources")[0];
  assert.match(create,/`create` with `schemaVersion: 3`/);assert.match(create,/`testsContent`/);
  assert.doesNotMatch(create,/Optional final `## Examples`/);
  assert.match(builder,/## Paired saved sources/);assert.match(builder,/`expectedSourceVersion`/);assert.match(builder,/kmx\.agent-pair-preview/);assert.match(builder,/`savedSourceVersion`/);
  assert.match(builder,/`yaml agent-test`/);assert.doesNotMatch(builder,/MCP operations stay single-source/);
  const opener=await readFile(new URL(`../../${variant}/skills/kmx/SKILL.md`,import.meta.url),"utf8");
  assert.match(opener,/selectedSources/);assert.match(opener,/sourceVersion/);
});

test("/kmx-canvas open resolves a relative path in the session folder and says what happened", async () => {
  for (const [folder, want] of [["C:\\Users\\ada\\walk", "C:\\Users\\ada\\walk/team/count.agent.md"], ["/home/ada/walk/", "/home/ada/walk/team/count.agent.md"]]) {
    const f = setup({ path: want });
    f.session.rpc.metadata = { snapshot: async () => ({ workingDirectory: folder }) };
    const command = createOpenCommand({ getSession: () => f.session, extensionId: PROVIDER });
    await command({ args: "open team/count.agent.md" });
    assert.deepEqual(f.calls[0], ["read", { serverName: "kmx-agent-builder", uri: "kmx://agent/" + encodeURIComponent(want) }]);
    assert.equal(f.calls[1][1].input.path, want);
    assert.deepEqual(f.calls[2], ["log", "Opened count.agent.md in the agent canvas.", undefined]);
  }
  // Squad charters keep forward slashes, which the path check requires.
  const squad = setup({ path: "C:\\walk/.squad/agents/leader/charter.md" });
  squad.session.rpc.metadata = { snapshot: async () => ({ workingDirectory: "C:\\walk" }) };
  await createOpenCommand({ getSession: () => squad.session, extensionId: PROVIDER })({ args: "open .squad/agents/leader/charter.md" });
  assert.deepEqual(squad.calls.map(c => c[0]), ["read", "open", "log"]);
  const full = setup({ path: "/mnt/c/walk/count.agent.md" });
  full.session.rpc.metadata = { snapshot: async () => ({ workingDirectory: "C:\\walk" }) };
  await createOpenCommand({ getSession: () => full.session, extensionId: PROVIDER })({ args: "open /mnt/c/walk/count.agent.md" });
  assert.equal(full.calls[1][1].input.path, "/mnt/c/walk/count.agent.md", "a full path is used as typed");
});

test("/kmx-canvas open says file not found only when the kmx server says so", async () => {
  const f = setup({ resourceError: { schemaVersion: 1, kind: "kmx.agent-view-error", reasonCode: "not_found" } });
  f.session.rpc.metadata = { snapshot: async () => { throw new Error("not supported"); } };
  await createOpenCommand({ getSession: () => f.session, extensionId: PROVIDER })({ args: "open missing.agent.md" });
  assert.deepEqual(f.calls.at(-1), ["log", "File not found: missing.agent.md. Check the name, or give the full path.", { level: "error" }]);
  assert.equal((await f.run({ path: "missing.agent.md" })).reasonCode, "not-found");
  // A transport or permission failure is not a missing file.
  const g = setup({ readError: new Error("io_failed") });
  await createOpenCommand({ getSession: () => g.session, extensionId: PROVIDER })({ args: "open missing.agent.md" });
  assert.match(g.calls.at(-1)[1], /MCP server is running/);
});
