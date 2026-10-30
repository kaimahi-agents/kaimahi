import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { createSuggestionMailbox, SUGGEST_TOOL_NAME, SUGGEST_TOOL_PARAMETERS } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/suggestions.mjs";
import { createActionHandler, createViewController, readView, startViewBridge } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs";
import { sourceIdentity } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";

const source = "---\nmodel: fake\n---\nGreet the user.\n";
const hash = text => "sha256:" + createHash("sha256").update(text).digest("hex");
const example = { id: "new-test", input: "Ada", assertions: [{ id: "greet", type: "contains", value: "Hello" }] };
const savedView = () => ({ file: { path: "a.agent.md", revision: hash(source) }, authoring: { source, editable: true, examples: [] }, model: "fake", tools: [], toolSettings: [] });
function mailboxFixture(view = savedView()) {
  const sent = [], session = { send: async value => sent.push(value) };
  let current = view;
  const mailbox = createSuggestionMailbox({ session, read: async () => current });
  return { mailbox, session, sent, get view() { return current; }, set view(value) { current = value; }, async request() { await mailbox.suggest(current); return mailbox.requestId; } };
}
const delivery = requestId => ({ requestId, examples: [structuredClone(example)] });

for (const paired of [false, true]) test(`mailbox queues only new drafts with exact source identity (${paired ? "paired" : "legacy"})`, async () => {
  const view = savedView();
  if (paired) view.selectedSources = { server: "owning-server", sourceVersion: "context", selector: "chosen.agent.test.md", testsSource: "Exact tests prose", operations: { edit: true } };
  const f = mailboxFixture(view), id = await f.request();
  assert.match(id, /^[A-Za-z0-9_-]{32}$/);
  assert.equal(f.sent.length, 1);
  const prompt = f.sent[0].prompt;
  assert.match(prompt, /suggest_kmx_tests/); assert.ok(prompt.includes(id));
  assert.match(prompt, /Do not call Edit or Test/); assert.match(prompt, /Save/);
  const snapshot = JSON.parse(prompt.split("Saved sources (data):\n")[1]);
  assert.equal(snapshot.content, source);
  if (paired) { assert.equal(snapshot.testsContent, view.selectedSources.testsSource); assert.equal(snapshot.server, "owning-server"); }
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, true);
  const expected = { id, sourceIdentity: sourceIdentity(view), examples: [example] };
  assert.deepEqual(f.mailbox.pending(view), expected);
  assert.deepEqual(f.mailbox.pending(view), expected, "GET is non-destructive");
  assert.equal(f.mailbox.acknowledge("wrong-id"), false); assert.deepEqual(f.mailbox.pending(view), expected);
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, false, "tool cannot overwrite a delivered batch");
  assert.equal(f.mailbox.acknowledge(id), true); assert.equal(f.mailbox.pending(view), undefined);
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, false);
});

test("tool schema permits exactly requestId and examples, no destination authority", () => {
  assert.equal(SUGGEST_TOOL_NAME, "suggest_kmx_tests");
  assert.deepEqual(Object.keys(SUGGEST_TOOL_PARAMETERS.properties).sort(), ["examples", "requestId"]);
  assert.equal(SUGGEST_TOOL_PARAMETERS.additionalProperties, false);
  assert.deepEqual(SUGGEST_TOOL_PARAMETERS.required, ["requestId", "examples"]);
});

for (const mutate of [
  x => ({ ...x, path: "other.agent.md" }), x => ({ ...x, server: "other" }), x => ({ ...x, requestId: "wrong" }),
  x => ({ ...x, examples: [] }), x => ({ ...x, examples: [example, example] }),
  x => ({ ...x, examples: [{ ...example, unexpected: true }] }),
  x => ({ ...x, examples: [{ ...example, input: " " }] }),
  x => ({ ...x, examples: [{ ...example, input: "x".repeat(256 * 1024) }] }),
  x => ({ ...x, examples: [{ ...example, mocks: { tools: [{ name: "ping", mode: "recorded", calls: [{ output: "fabricated" }] }] } }] }),
]) test("malformed delivery refuses without consuming a valid request", async () => {
  const f = mailboxFixture(), id = await f.request();
  assert.equal((await f.mailbox.deliver(mutate(delivery(id)))).queued, false);
  assert.equal(f.mailbox.pending(f.view), undefined);
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, true);
});

test("existing ID collision, case collision and total capacity refuse atomically", async () => {
  for (const existing of [[example], [{ ...example, id: "NEW-TEST" }], Array.from({ length: 128 }, (_, i) => ({ ...example, id: "old-" + i }))]) {
    const view = savedView(); view.authoring.examples = existing;
    const f = mailboxFixture(view), id = await f.request();
    assert.equal((await f.mailbox.deliver(delivery(id))).queued, false); assert.equal(f.mailbox.pending(view), undefined);
  }
});

test("checked pending recorded and live mocks are accepted, not fabricated fixture payloads", async () => {
  const f = mailboxFixture(), id = await f.request(), input = delivery(id);
  input.examples[0].mocks = { tools: [{ name: "ping", mode: "recorded", calls: [] }, { name: "pong", mode: "live" }] };
  assert.equal((await f.mailbox.deliver(input)).queued, true);
  assert.deepEqual(f.mailbox.pending(f.view).examples, input.examples);
});

for (const change of [
  view => { view.file.revision = hash("changed"); },
  view => { view.authoring.editable = false; },
  view => { view.selectedSources.sourceVersion = "tests-only-save"; },
  view => { view.selectedSources.server = "other-owner"; },
  view => { view.selectedSources.selector = "other-tests"; },
  view => { view.selectedSources.operations.edit = false; },
]) test("source/owner/selection/editability changes reject delivery and expose only a stale marker after queue", async () => {
  const view = savedView(); view.selectedSources = { server: "owner", sourceVersion: "original", selector: "selected", operations: { edit: true } };
  // Paired identity deliberately ignores raw revision, so exercise that separately.
  if (change.toString().includes("file.revision")) delete view.selectedSources;
  const pristine = structuredClone(view), f = mailboxFixture(view), id = await f.request();
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, true);
  change(view);
  assert.deepEqual(f.mailbox.pending(view), { id, stale: true });
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, false);
  const g = mailboxFixture(pristine), old = await g.request(); change(g.view);
  assert.equal((await g.mailbox.deliver(delivery(old))).queued, false); assert.equal(g.mailbox.pending(g.view), undefined);
});

test("supersede, clear, close, failed send and in-flight read invalidate tokens", async () => {
  const f = mailboxFixture(), old = await f.request(), current = await f.request(); assert.notEqual(old, current);
  assert.equal((await f.mailbox.deliver(delivery(old))).queued, false);
  f.mailbox.clear(); assert.equal((await f.mailbox.deliver(delivery(current))).queued, false);
  f.session.send = async () => { throw Error("private send failure"); };
  await assert.rejects(() => f.request()); const failed = f.mailbox.requestId;
  assert.equal(failed, undefined);
  f.session.send = async () => {}; const closing = await f.request(); f.mailbox.dispose();
  assert.equal((await f.mailbox.deliver(delivery(closing))).queued, false);
  await assert.rejects(() => f.request());
  let release;
  const m = createSuggestionMailbox({ session: { send: async () => {} }, read: () => new Promise(resolve => { release = resolve; }) });
  await m.suggest(savedView()); const pending = m.deliver(delivery(m.requestId)); m.clear(); release(savedView());
  assert.equal((await pending).queued, false);
});

test("observing a source change invalidates a request even if legacy bytes later return", async () => {
  const f = mailboxFixture(), id = await f.request(), original = structuredClone(f.view);
  f.view.file.revision = hash("changed");
  assert.equal(f.mailbox.pending(f.view), undefined, "no batch before delivery");
  f.view = original;
  assert.equal((await f.mailbox.deliver(delivery(id))).queued, false);
});

test("concurrent deliveries cannot replace an already queued batch", async () => {
  const readers = [], view = savedView();
  const mailbox = createSuggestionMailbox({ session: { send: async () => {} }, read: () => new Promise(resolve => readers.push(resolve)) });
  await mailbox.suggest(view); const id = mailbox.requestId;
  const first = mailbox.deliver(delivery(id)), changed = delivery(id); changed.examples[0].input = "Replacement";
  const second = mailbox.deliver(changed);
  readers[0](view); assert.equal((await first).queued, true);
  readers[1](view); assert.equal((await second).queued, false);
  assert.equal(mailbox.pending(view).examples[0].input, "Ada");
});

test("failed send invalidates even a batch delivered while send was pending", async () => {
  let reject;
  const view = savedView(), mailbox = createSuggestionMailbox({ session: { send: () => new Promise((resolve, fail) => { reject = fail; }) }, read: async () => view });
  const sending = mailbox.suggest(view), id = mailbox.requestId;
  assert.equal((await mailbox.deliver(delivery(id))).queued, true);
  reject(Error("failed send")); await assert.rejects(() => sending);
  assert.equal(mailbox.pending(view), undefined);
});

test("inspection-only cannot start a suggestion request", async () => {
  const view = savedView(); view.authoring.editable = false;
  const f = mailboxFixture(view); await assert.rejects(() => f.request()); assert.equal(f.sent.length, 0);
});

function bridgeFixture() {
  let content = source;
  const session = { send: async value => sent.push(value), rpc: { mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify({ schemaVersion: 1, kind: "kmx.agent-view", file: { path: "a.agent.md", revision: hash(content) }, title: "A", instructions: content, model: "fake", examples: [], authoring: { source: content, editable: true }, latest: { status: "none" } }) }] }) } }, tools: { getCurrentMetadata: async () => ({ tools: [] }), execute: async () => { throw Error("must not execute"); } } } }, sent = [];
  return { session, sent, change() { content += "Changed\n"; } };
}
async function post(bridge, input) {
  return (await fetch(bridge.url + "actions", { method: "POST", headers: { Origin: new URL(bridge.url).origin, "Content-Type": "application/json" }, body: JSON.stringify(input) })).json();
}

test("bridge GET repeats until exact raw ACK; reload/close invalidate, URL token is not requestId", async t => {
  const f = bridgeFixture(), bridge = await startViewBridge({ session: f.session, path: "a.agent.md" }); t.after(() => bridge.close());
  assert.equal((await post(bridge, { operation: "suggest" })).error, undefined);
  const requestId = JSON.parse(f.sent[0].prompt.split("Saved sources (data):\n")[1]).requestId;
  assert.notEqual(requestId, new URL(bridge.url).pathname.split("/")[1]);
  assert.equal(bridge.matchesSuggestions(requestId), true);
  assert.equal((await bridge.deliverSuggestions(delivery(requestId))).queued, true);
  const get = async () => (await (await fetch(bridge.url + "view.json")).json()).suggestedTests;
  const batch = await get(); assert.equal(batch.id, requestId); assert.deepEqual(await get(), batch);
  assert.equal((await post(bridge, { operation: "acknowledge-suggestions", batchId: "wrong" })).acknowledged, false);
  assert.deepEqual(await get(), batch);
  assert.equal((await post(bridge, { operation: "acknowledge-suggestions", batchId: requestId, path: "no" })).error, "invalid-action");
  f.change(); assert.deepEqual(await get(), { id: requestId, stale: true });
  assert.equal((await post(bridge, { operation: "acknowledge-suggestions", batchId: requestId })).acknowledged, true);
  assert.equal(await get(), undefined);
  await post(bridge, { operation: "suggest" }); const reloadId = JSON.parse(f.sent[1].prompt.split("Saved sources (data):\n")[1]).requestId;
  await post(bridge, { operation: "reload" }); assert.equal((await bridge.deliverSuggestions(delivery(reloadId))).queued, false);
  await bridge.close(); assert.equal(bridge.matchesSuggestions(reloadId), false);
});

test("bridge raw ACK succeeds while an execution action is busy", async t => {
  const f = bridgeFixture(), bridge = await startViewBridge({ session: f.session, path: "a.agent.md" }); t.after(() => bridge.close());
  await post(bridge, { operation: "suggest" });
  const id = JSON.parse(f.sent[0].prompt.split("Saved sources (data):\n")[1]).requestId;
  assert.equal((await bridge.deliverSuggestions(delivery(id))).queued, true);
  let release, started;
  const waiting = new Promise(resolve => { started = resolve; }), read = f.session.rpc.mcp.resources.read;
  f.session.rpc.mcp.resources.read = () => { started(); return new Promise(resolve => { release = () => resolve(read()); }); };
  const running = post(bridge, { operation: "run", expectedRevision: hash(source), input: "Ada" });
  await waiting;
  assert.equal((await post(bridge, { operation: "suggest" })).error, "busy");
  assert.equal((await post(bridge, { operation: "acknowledge-suggestions", batchId: id })).acknowledged, true);
  f.session.rpc.mcp.resources.read = read; release(); await running;
  assert.equal((await (await fetch(bridge.url + "view.json")).json()).suggestedTests, undefined);
});

test("controller routes only to live owning bridge without waiting for unrelated startup", async t => {
  const f = bridgeFixture(), controller = createViewController({ getSession: () => f.session }); t.after(() => controller.closeAll());
  const bridge = await controller.open({ instanceId: "a", input: { path: "a.agent.md" } });
  await post(bridge, { operation: "suggest" }); const requestId = JSON.parse(f.sent[0].prompt.split("Saved sources (data):\n")[1]).requestId;
  let release; const read = f.session.rpc.mcp.resources.read;
  f.session.rpc.mcp.resources.read = () => new Promise(resolve => { release = () => resolve(read()); });
  const unrelated = controller.open({ instanceId: "b", input: { path: "b.agent.md" } });
  // Resume reads for the owning bridge while b remains pending.
  f.session.rpc.mcp.resources.read = read;
  assert.equal((await controller.deliverSuggestions(delivery(requestId))).queued, true);
  release(); await unrelated;
  await controller.close({ instanceId: "a" });
  assert.equal((await controller.deliverSuggestions(delivery(requestId))).queued, false);
});

test("standalone Suggest refuses unavailable without a handoff, injected Suggest receives saved view", async () => {
  const f = bridgeFixture();
  assert.equal((await createActionHandler({ session: f.session, path: "a.agent.md" })({ operation: "suggest" })).error, "unavailable");
  let received;
  const result = await createActionHandler({ session: f.session, path: "a.agent.md", suggest: async view => { received = view; return { message: "Requested drafts" }; } })({ operation: "suggest" });
  assert.equal(result.message, "Requested drafts"); assert.equal(received.authoring.source, source); assert.equal(f.sent.length, 0);
});

test("extension registers the narrow non-writing tool", async () => {
  const extension = await readFile(new URL("../../copilot-wsl/com.github.copilot/extensions/agent-view/extension.mjs", import.meta.url), "utf8");
  assert.match(extension, /name: SUGGEST_TOOL_NAME/); assert.match(extension, /parameters: SUGGEST_TOOL_PARAMETERS/); assert.match(extension, /controller\.deliverSuggestions\(input\)/);
});
