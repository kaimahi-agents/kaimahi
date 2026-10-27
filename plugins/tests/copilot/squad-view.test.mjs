import test from "node:test";
import assert from "node:assert/strict";
import { renderSquadView } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-app.js";
import { projectMember } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-backend.mjs";
import { fieldServiceProposal, STORAGE_FIXED_PROMPT } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-examples.mjs";
import { createSquadViewController } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-view.mjs";

function fakeDoc() {
  const doc = { activeElement: null };
  const make = tag => ({
    tag, children: [], attrs: {}, listeners: {}, className: "", style: {}, _text: "",
    get nodeType() { return tag === "#text" ? 3 : 1; }, get tagName() { return tag.toUpperCase(); },
    get childNodes() { return this._text ? [Object.assign(make("#text"), { _text: this._text }), ...this.children] : this.children; },
    get textContent() { return this._text + this.children.map(n => n.textContent).join(""); },
    set textContent(v) { this._text = String(v); this.children = []; },
    append(...nodes) { this.children.push(...nodes.map(n => typeof n === "string" ? Object.assign(make("#text"), { _text: n }) : n)); },
    replaceChildren(...nodes) { this._text = ""; this.children = []; this.append(...nodes); },
    setAttribute(k, v) { this.attrs[k] = String(v); }, getAttribute(k) { return this.attrs[k] ?? null; },
    addEventListener(k, fn) { (this.listeners[k] ??= []).push(fn); }, focus() { doc.activeElement = this; },
    contains(node) { return all(this).includes(node); },
    querySelector(selector) { return this.querySelectorAll(selector)[0]; },
    querySelectorAll(selector) { return all(this).filter(n => selector.split(",").some(s => s.startsWith(".") ? n.className.split(" ").includes(s.slice(1)) : n.tag === s)); },
    getBoundingClientRect() { return { top: 0, bottom: 32, height: 32 }; },
  });
  return Object.assign(doc, { createElement: make, createElementNS: (_, tag) => make(tag), createTextNode: text => Object.assign(make("#text"), { _text: text }) });
}
const all = node => [node, ...node.children.flatMap(all)];
const click = async (root, label) => {
  const node = all(root).find(n => n.tag === "button" && n.textContent === label);
  assert.ok(node, label); assert.ok(!node.disabled, label);
  for (const fn of node.listeners.click ?? []) await fn();
};
const edit = (root, label, value) => {
  const node = all(root).find(n => n.attrs["aria-label"] === label);
  assert.ok(node, label); node.value = value;
  for (const fn of node.listeners.input ?? []) fn();
};
const revision = "sha256:" + "a".repeat(64), nextRevision = "sha256:" + "b".repeat(64);
const member = { schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: "lead", state: "ready", profile: { role: "Lead / Architect" }, file: { path: ".squad/agents/lead/charter.md", revision }, taskInstructions: "Reply with exactly `Ready`.", model: "fake", instructions: "Native", examples: "", tests: [{ id: "ready", input: "Hello", assertions: [{ id: "check", type: "contains", value: "Ready" }] }], brief: { text: "Saved brief", revision } };
const team = { root: "/project", state: "ready", access: ["Tool-free member previews"], members: [member], brief: member.brief };

test("live example uses the discovered model and added roles remain reviewable drafts", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const app = renderSquadView(doc, root, { team: { ...team, members: [], previewProvider: "copilot" } }, async () => ({ defaultModel: "real-model", models: ["real-model"] }));
  await click(root, "Use field-service app example");
  assert.ok(all(root).filter(n => n.attrs["aria-label"] === "Preferred model").every(n => n.value === "real-model"));
  assert.doesNotMatch(root.textContent, /deterministic fake model/);
  await click(root, "+ Add role");
  assert.equal(all(root).filter(n => n.attrs["aria-label"] === "Preferred model").at(-1).value, "real-model");
  assert.equal(all(root).filter(n => n.attrs["aria-label"] === "Task instructions / persona").at(-1).value, "");
  const proposal = fieldServiceProposal("real-model"), storage = proposal.specialists[0];
  assert.doesNotMatch(storage.taskInstructions, /Reply with exactly/);
  assert.match(STORAGE_FIXED_PROMPT, /preserve-both/);
  assert.equal(storage.tests[1].assertions[0].pattern, '"conflictStrategy"\\s*:\\s*"preserve-both"');
  app.dispose();
});

test("a generated proposal is reviewed before replacing manual edits", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), suggested = fieldServiceProposal("real-model");
  const calls = [];
  const app = renderSquadView(doc, root, { team: { ...team, members: [] } }, async input => { calls.push(input); return { acceptedProposal: suggested }; });
  await click(root, "Use field-service app example"); edit(root, "Shared team brief", "Keep my edited brief");
  app.observeOffer({ id: "offer-1", proposal: suggested });
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "Keep my edited brief");
  assert.equal(calls.length, 0);
  await click(root, "Use suggested team");
  assert.deepEqual(calls, [{ operation: "accept-proposal", offerId: "offer-1" }]);
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, suggested.brief);
  app.dispose();
});

test("reopening the same canvas offers a generated proposal without replacing the current draft", async () => {
  const controller = createSquadViewController({ extensionId: "plugin:test:agent-view", getSession: () => ({ rpc: { mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify({ ...team, schemaVersion: 2, kind: "kmx.squad-view", members: [] }) }] }) } } } }) });
  const context = { instanceId: "proposal-review", input: {} };
  try {
    const first = await controller.open(context);
    const proposal = fieldServiceProposal("real-model");
    const reopened = await controller.open({ ...context, input: { proposal } });
    assert.equal(reopened.url, first.url);
    await assert.rejects(controller.open({ ...context, input: { proposal: {} } }));
    assert.equal((await controller.open(context)).url, first.url, "a rejected offer keeps the existing canvas and draft");
    let data = await (await fetch(first.url + "view.json")).json();
    assert.equal(data.proposal, undefined);
    assert.deepEqual(data.proposalOffer.proposal, proposal);
    const result = await (await fetch(first.url + "actions", { method: "POST", headers: { "Content-Type": "application/json", Origin: new URL(first.url).origin }, body: JSON.stringify({ operation: "accept-proposal", offerId: data.proposalOffer.id }) })).json();
    assert.deepEqual(result.acceptedProposal, proposal);
    data = await (await fetch(first.url + "view.json")).json();
    assert.deepEqual(data.proposal, proposal);
    assert.equal(data.proposalOffer, undefined);
  } finally { await controller.close(context); }
});

test("typing into an active native preview requests stop and preserves the newer prompt", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  let release, runId;
  const app = renderSquadView(doc, root, { team: { ...team, previewControl: "run-id-v1" } }, async input => {
    calls.push(input);
    if (input.operation === "member") return { view: projectMember(member) };
    const a = input.action;
    if (a.operation === "progress") return { run: { id: runId, revision: nextRevision, contextRevision: revision, model: "fake", caseIds: ["ready"], totalCases: 1, cases: [], state: "running", cancellable: true } };
    if (a.operation === "stop") return { stopping: true };
    if (a.operation === "save-and-test") { runId = a.runId; return new Promise(resolve => { release = resolve; }); }
    throw new Error("Unexpected action");
  });
  await click(root, "Open member →");
  let prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  prompt.textContent = "First saved instructions";
  for (const fn of prompt.listeners.input) fn();
  for (const fn of prompt.listeners.keydown) fn({ key: "Enter", preventDefault() {} });
  await Promise.resolve();
  t.mock.timers.tick(200); await Promise.resolve(); await Promise.resolve();
  assert.equal(prompt.attrs.contenteditable, "plaintext-only");
  prompt.textContent = "Keep this newer draft";
  for (const fn of prompt.listeners.input) fn();
  assert.ok(calls.some(c => c.action?.operation === "stop"));
  release({ stopped: true, saved: true, run: { id: runId, revision: nextRevision, contextRevision: revision, model: "fake", caseIds: ["ready"], totalCases: 1, cases: [], state: "stopped" }, view: projectMember({ ...member, taskInstructions: "First saved instructions", file: { ...member.file, revision: nextRevision } }) });
  await new Promise(resolve => setImmediate(resolve));
  prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  assert.match(prompt.textContent, /Keep this newer draft/);
  assert.equal(calls.filter(c => ["save-and-test", "test"].includes(c.action?.operation)).length, 1);
  app.dispose();
});

test("brief drafts survive member navigation and retain the original revision after an outside refresh", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  const app = renderSquadView(doc, root, { team }, async input => {
    calls.push(input);
    if (input.operation === "member") return { view: projectMember(member) };
    if (input.operation === "reload") return { team: { ...team, brief: { text: "Outside change", revision: nextRevision } } };
    if (input.operation === "save-brief") return { error: "stale_revision", message: "Changed elsewhere" };
    throw new Error("Unexpected action");
  });
  edit(root, "Shared team brief", "My unsaved brief");
  await click(root, "Open member →");
  assert.ok(all(root).some(n => n.attrs["aria-label"] === "Prompt"));
  await click(root, "← Squad");
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "My unsaved brief");
  await click(root, "Refresh team");
  await click(root, "Save brief");
  assert.deepEqual(calls.at(-1), { operation: "save-brief", expectedRevision: revision, text: "My unsaved brief" });
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "My unsaved brief");
  app.dispose();
});

test("partial Create renders acknowledged member outcomes even when refresh fails", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const result = { effectsUnknown: true, setup: "initialized", changedPaths: [".squad"], members: [{ id: "lead", state: "registered_unenriched" }] };
  const app = renderSquadView(doc, root, { team: { ...team, members: [] } }, async () => ({ creation: { status: "unknown", result }, outcome: result, refreshUnavailable: true, message: "Refresh unavailable" }));
  await click(root, "Use field-service app example"); await click(root, "Create squad");
  assert.match(root.textContent, /lead: registered_unenriched/);
  assert.match(root.textContent, /Refresh unavailable/);
  assert.equal(all(root).find(n => n.tag === "button" && n.textContent === "Create squad").disabled, true);
  app.dispose();
});

test("a saved shared-brief change invalidates the cached member editor result", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"); let current = team;
  const preview = { schemaVersion: 1, kind: "kmx.agent-file-preview", file: member.file, state: "complete", verdict: "pass", fullPass: true, operation: "test", modelRoute: { provider: "fake", model: "fake" }, selection: { scope: "full", totalCases: 1, caseIds: ["ready"] }, examples: [{ id: "ready", state: "complete", verdict: "pass", assertions: [{ id: "check", type: "contains", verdict: "pass" }] }] };
  const observation = { file: member.file, briefRevision: revision, preview };
  const app = renderSquadView(doc, root, { team }, async input => {
    if (input.operation === "member") return { view: projectMember({ ...member, brief: current.brief }, observation) };
    if (input.operation === "save-brief") { current = { ...team, brief: { text: input.text, revision: nextRevision } }; return { team: current }; }
    throw new Error("Unexpected action");
  });
  await click(root, "Open member →");
  assert.match(root.textContent, /passing/i);
  await click(root, "← Squad"); edit(root, "Shared team brief", "A new requirement"); await click(root, "Save brief");
  await click(root, "Open member →");
  assert.match(root.textContent, /Out of date/);
  app.dispose();
});

test("no-effect refusal keeps edited proposal available without automatic resubmission", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"); let calls = 0;
  const result = { effectsUnknown: false, changedPaths: [], members: [{ id: "lead", state: "not_attempted" }] };
  const app = renderSquadView(doc, root, { team: { ...team, members: [] } }, async () => { calls++; return { creation: { status: "refused", result }, outcome: result }; });
  await click(root, "Use field-service app example"); edit(root, "Shared team brief", "Edited proposal");
  await click(root, "Create squad");
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "Edited proposal");
  assert.equal(all(root).find(n => n.tag === "button" && n.textContent === "Create squad").disabled, false);
  assert.equal(calls, 1); app.dispose();
});
