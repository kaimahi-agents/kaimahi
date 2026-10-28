import test from "node:test";
import assert from "node:assert/strict";
import { renderSquadView } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-app.js";
import { projectMember } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-backend.mjs";
import { editorValue } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/editor.mjs";
import { createSquadViewController, startSquadBridge } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-view.mjs";

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

function proposalFixture(model = "fake") {
  const members = ["lead", "reviewer", "writer", "verifier"].map((id, index) => ({
    memberId: id, role: index ? id : "lead", displayRole: index ? id : "Lead / Architect", model,
    taskInstructions: "Follow the supplied request.",
    tests: ["first", "second"].map(id => ({ id, input: `${id} request`, assertions: [{ id: "response", type: "contains", value: "Expected response" }] })),
  }));
  return { brief: "Reviewed team goal.", lead: members[0], specialists: members.slice(1) };
}

test("an empty canvas requires an authored goal before requesting a proposal", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  const app = renderSquadView(doc, root, { team: { ...team, members: [] } }, async input => { calls.push(input); return { message: "Proposal requested" }; });
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "What should the team do?").value, "");
  const propose = all(root).find(n => n.tag === "button" && n.textContent === "Ask Copilot to propose");
  assert.equal(propose.disabled, true);
  edit(root, "What should the team do?", "   "); assert.equal(propose.disabled, true);
  assert.ok(!all(root).some(n => n.tag === "button" && /example/i.test(n.textContent)));
  assert.equal(calls.length, 0);
  edit(root, "What should the team do?", "Review our release"); assert.equal(propose.disabled, false);
  await click(root, "Ask Copilot to propose");
  assert.deepEqual(calls, [{ operation: "propose", goal: "Review our release" }]);
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "What should the team do?").value, "Review our release");
  app.dispose();
});

test("added roles inherit the reviewed model and remain incomplete editable drafts", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposal: proposalFixture("real-model") }, async () => { throw new Error("Draft changes must stay local"); });
  assert.ok(all(root).filter(n => n.attrs["aria-label"] === "Preferred model").every(n => n.value === "real-model"));
  await click(root, "+ Add role");
  assert.equal(all(root).filter(n => n.attrs["aria-label"] === "Preferred model").at(-1).value, "real-model");
  assert.equal(all(root).filter(n => n.attrs["aria-label"] === "Task instructions / persona").at(-1).value, "");
  app.dispose();
});

test("a generated proposal is reviewed before replacing manual edits", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), suggested = proposalFixture("real-model");
  const calls = [];
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposal: proposalFixture("real-model") }, async input => { calls.push(input); return { acceptedProposal: suggested }; });
  edit(root, "Shared team brief", "Keep my edited brief");
  app.observeOffer({ id: "offer-1", proposal: suggested });
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "Keep my edited brief");
  assert.equal(calls.length, 0);
  await click(root, "Use suggested team");
  assert.deepEqual(calls, [{ operation: "accept-proposal", offerId: "offer-1" }]);
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, suggested.brief);
  app.dispose();
});

test("suggested teams show readable collapsed tests without changing authored checks", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), suggestion = proposalFixture("real-model");
  const literalInput = '<img src=x onerror=alert(1)>\nRelease QA-217 has a failed installation test.';
  suggestion.lead.tests = [{ id: "holds-unsafe-release", input: literalInput, assertions: [
    { id: "hold", type: "contains", value: "HOLD" },
    { id: "no-ready", type: "notContains", value: "READY" },
    { id: "reason", type: "regex", pattern: "(?i)install" },
    { id: "judge", type: "llmJudge", rubric: "Explain the supplied blocker", minScore: 0, judgeModel: "real-model", judgeVersion: "v1" },
  ] }];
  const before = structuredClone(suggestion), calls = [];
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposalOffer: { id: "review", proposal: suggestion } }, async input => { calls.push(input); return { acceptedProposal: suggestion }; });
  const byClass = name => all(root).filter(n => n.className.split(" ").includes(name));
  assert.equal(byClass("squad-offer-member").length, 4);
  assert.ok(byClass("squad-offer-member").every(n => !n.open));
  assert.ok(byClass("squad-offer-test").every(n => !n.open));
  assert.match(root.textContent, /Holds unsafe release/);
  assert.match(root.textContent, /4 checks · Not run/);
  assert.match(root.textContent, /The agent gets/);
  assert.match(root.textContent, /Answer includesHOLD/);
  assert.match(root.textContent, /Answer does not includeREADY/);
  assert.match(root.textContent, /Answer matches this pattern\(\?i\)install/);
  assert.match(root.textContent, /Minimum score: 0 · Judge: real-model · Version: v1/);
  assert.equal(byClass("squad-offer-input")[0].textContent, literalInput);
  assert.ok(!all(root).some(n => ["img", "script"].includes(n.tag)));
  assert.doesNotMatch(root.textContent, /"assertions"\s*:/);
  assert.equal(calls.length, 0, "reviewing a suggestion does not run or save anything");
  assert.deepEqual(suggestion, before);
  await click(root, "Use suggested team");
  assert.deepEqual(calls, [{ operation: "accept-proposal", offerId: "review" }]);
  app.dispose();
});

test("reopening the same canvas offers a generated proposal without replacing the current draft", async () => {
  const controller = createSquadViewController({ extensionId: "plugin:test:agent-view", getSession: () => ({ rpc: { mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify({ ...team, schemaVersion: 2, kind: "kmx.squad-view", members: [] }) }] }) } } } }) });
  const context = { instanceId: "proposal-review", input: {} };
  try {
    const first = await controller.open(context);
    const proposal = proposalFixture("real-model");
    assert.equal((await fetch(first.url + "squad-examples.mjs")).status, 404);
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

for (const replacement of ["Keep this newer draft", member.taskInstructions]) for (const completion of ["stopped", "complete", "read-failed"]) test(`in-flight prompt edits survive ${completion}, including the old baseline (${replacement === member.taskInstructions ? "A-B-A" : "new draft"})`, async t => {
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
  t.mock.timers.tick(200); await new Promise(resolve => setImmediate(resolve));
  assert.equal(prompt.attrs.contenteditable, "plaintext-only");
  prompt.textContent = replacement;
  for (const fn of prompt.listeners.input) fn();
  assert.ok(calls.some(c => c.action?.operation === "stop"));
  assert.equal(calls.find(c => c.action?.operation === "save-and-test").action.expectedBriefRevision, revision);
  const saved = { ...member, taskInstructions: "First saved instructions", file: { ...member.file, revision: nextRevision } };
  release({ saved: true, view: projectMember(saved), ...(completion === "stopped" ? { stopped: true, run: { id: runId, revision: nextRevision, contextRevision: revision, model: "fake", caseIds: ["ready"], totalCases: 1, cases: [], state: "stopped" } } : completion === "read-failed" ? { error: "native_source_unavailable" } : {}) });
  await new Promise(resolve => setImmediate(resolve));
  prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  assert.equal(editorValue(prompt), replacement);
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
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposal: proposalFixture() }, async () => ({ creation: { status: "unknown", result }, outcome: result, refreshUnavailable: true, message: "Refresh unavailable" }));
  await click(root, "Create squad");
  assert.match(root.textContent, /lead: registered_unenriched/);
  assert.match(root.textContent, /Refresh unavailable/);
  assert.equal(all(root).find(n => n.tag === "button" && n.textContent === "Create squad").disabled, true);
  app.dispose();
});

test("a retained creation with null member outcomes still shows the native roster", () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const outcome = { effectsUnknown: false, setup: "reused", changedPaths: [], members: null };
  const app = renderSquadView(doc, root, { team, creation: { status: "refused", result: outcome }, outcome });
  assert.match(root.textContent, /Team members/);
  assert.ok(all(root).some(n => n.tag === "button" && n.textContent === "Open member →"));
  assert.doesNotMatch(root.textContent, /Creation: enriched/);
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
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposal: proposalFixture() }, async () => { calls++; return { creation: { status: "refused", result }, outcome: result }; });
  edit(root, "Shared team brief", "Edited proposal");
  await click(root, "Create squad");
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "Edited proposal");
  assert.equal(all(root).find(n => n.tag === "button" && n.textContent === "Create squad").disabled, false);
  assert.equal(calls, 1); app.dispose();
});

const passedObservation = {
  file: member.file, briefRevision: revision,
  preview: { schemaVersion: 1, kind: "kmx.agent-file-preview", file: member.file, state: "complete", verdict: "pass", fullPass: true, operation: "test", modelRoute: { provider: "fake", model: "fake" }, selection: { scope: "full", totalCases: 1, caseIds: ["ready"] }, examples: [{ id: "ready", state: "complete", verdict: "pass", assertions: [{ id: "check", type: "contains", verdict: "pass" }] }] },
};

test("background brief observation stales a passing member and preserves prompt drafts", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  let current = member;
  const app = renderSquadView(doc, root, { team }, async input => {
    calls.push(input); assert.equal(input.operation, "member");
    return { view: projectMember(current, passedObservation) };
  });
  await click(root, "Open member →"); assert.match(root.textContent, /passing/i);
  let prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  prompt.textContent = "Keep my unsaved prompt";
  for (const fn of prompt.listeners.input) fn();
  current = { ...member, brief: { text: "Brief changed in another canvas", revision: nextRevision } };
  await app.observe({ team: { ...team, brief: current.brief } }, app.observation());
  assert.match(root.textContent, /Out of date/); assert.match(root.textContent, /Brief changed in another canvas/);
  prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  assert.match(prompt.textContent, /Keep my unsaved prompt/);
  assert.deepEqual(calls.map(c => c.operation), ["member", "member"]);
  app.dispose();
});

for (const failure of ["member", "aggregate", "fallback", "transport"]) test(`native ${failure} read failures preserve drafts and history without a current pass`, async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  let unavailable = false;
  const app = renderSquadView(doc, root, { team }, async input => {
    calls.push(input);
    if (input.operation === "member") return unavailable ? { error: "native_source_unavailable", message: "PRIVATE SOURCE" } : { view: projectMember(member, passedObservation) };
    return { view: projectMember(member, passedObservation) };
  });
  await click(root, "Open member →"); assert.match(root.textContent, /passing/i);
  let prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  prompt.textContent = "Keep this draft"; for (const fn of prompt.listeners.input) fn();
  const started = app.observation();
  if (failure === "member") { unavailable = true; await app.observe({ team }, started); }
  else if (failure === "aggregate") await app.observe({ error: "native_source_unavailable", message: "PRIVATE SOURCE" }, started);
  else if (failure === "fallback") await app.observe({ team, refreshUnavailable: true }, started);
  else app.observeFailure(new Error("PRIVATE SOURCE"), started);
  assert.match(root.textContent, /Native member or shared brief unavailable/);
  assert.doesNotMatch(root.textContent, /PRIVATE SOURCE/);
  prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  assert.equal(prompt.textContent, "Keep this draft");
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Save and run all tests").disabled, true);
  assert.equal(all(root).find(n => n.className === "model-picker").disabled, true);
  unavailable = false;
  await app.observe({ team });
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Prompt").textContent, "Keep this draft");
  assert.match(root.textContent, /Out of date/);
  assert.ok(calls.every(c => c.operation === "member"), "recovery is read-only");
  assert.match(root.textContent, /Passed/);
  app.dispose();
});

test("a late native read failure cannot invalidate a newer successful test", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const app = renderSquadView(doc, root, { team }, async () => ({ view: projectMember(member, passedObservation) }));
  await click(root, "Open member →");
  const earlier = app.observation();
  const prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  for (const fn of prompt.listeners.keydown) fn({ key: "Enter", preventDefault() {} });
  await new Promise(resolve => setImmediate(resolve));
  app.observeFailure(new Error("old read failed"), earlier);
  assert.match(root.textContent, /passing/i);
  assert.doesNotMatch(root.textContent, /Out of date|shared brief unavailable/);
  app.dispose();
});

test("only an explicit new test replaces a recovered native member's old pass", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  const app = renderSquadView(doc, root, { team }, async input => { calls.push(input); return { view: projectMember(member, passedObservation) }; });
  await click(root, "Open member →");
  app.observeFailure(new Error("read failed"));
  await app.observe({ team });
  assert.match(root.textContent, /Out of date/);
  const prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  for (const fn of prompt.listeners.keydown) fn({ key: "Enter", preventDefault() {} });
  await new Promise(resolve => setImmediate(resolve));
  assert.doesNotMatch(root.textContent, /Out of date|shared brief unavailable/);
  assert.match(root.textContent, /passing/i);
  assert.equal(calls.filter(c => c.operation === "member-action").length, 1);
  app.dispose();
});

test("outside source edits turn an open pass stale and surface a draft conflict", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"); let current = member;
  const app = renderSquadView(doc, root, { team }, async () => ({ view: projectMember(current, passedObservation) }));
  await click(root, "Open member →");
  const prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  prompt.textContent = "My prompt edit"; for (const fn of prompt.listeners.input) fn();
  current = { ...member, taskInstructions: "Outside instructions", file: { ...member.file, revision: nextRevision } };
  await app.observe({ team: { ...team, members: [current] } });
  assert.match(root.textContent, /My prompt edit/);
  assert.ok(all(root).some(n => n.textContent === "Keep my edits"));
  await click(root, "← Squad");
  await app.observe({ team: { ...team, members: [{ ...current, state: "not_setup" }] } });
  assert.equal(all(root).find(n => n.tag === "button" && n.textContent === "Open member →").disabled, true);
  app.dispose();
});

test("invalid native state moves the active view back to the team without dropping its draft", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const app = renderSquadView(doc, root, { team }, async () => ({ view: projectMember(member, passedObservation) }));
  await click(root, "Open member →");
  const prompt = all(root).find(n => n.attrs["aria-label"] === "Prompt");
  prompt.textContent = "Keep my work"; for (const fn of prompt.listeners.input) fn();
  await app.observe({ team: { ...team, members: [{ ...member, state: "not_setup" }] } });
  assert.match(root.textContent, /not set up by kmx/i);
  assert.ok(!all(root).some(n => n.attrs["aria-label"] === "Prompt"));
  await app.observe({ team }); await click(root, "Open member →");
  assert.match(all(root).find(n => n.attrs["aria-label"] === "Prompt").textContent, /Keep my work/);
  assert.match(root.textContent, /Out of date/);
  app.dispose();
});

test("cached member controls follow the accepted view and reject older poll responses", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), saves = []; let current = member;
  const app = renderSquadView(doc, root, { team }, async input => {
    if (input.operation === "member") return { view: projectMember(current) };
    saves.push(input.action); return { view: projectMember(current) };
  });
  await click(root, "Open member →"); const older = app.observation();
  await click(root, "← Squad");
  current = { ...member, model: "new-model", taskInstructions: "New instructions", instructions: "Updated native context", file: { ...member.file, revision: nextRevision } };
  await click(root, "Open member →");
  assert.match(root.textContent, /New instructions/); assert.match(root.textContent, /Updated native context/);
  assert.ok(all(root).some(n => n.attrs["aria-label"]?.startsWith("Runtime model: new-model")));
  await app.observe({ team: { ...team, members: [] } }, older);
  await click(root, "Save model");
  assert.equal(saves[0].expectedRevision, nextRevision); assert.equal(saves[0].model, "new-model");
  app.dispose();
});

test("polling preserves a deliberate unsaved model selection while updating its save revision", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), saves = []; let current = member;
  const app = renderSquadView(doc, root, { team }, async input => {
    if (input.operation === "member") return { view: projectMember(current) };
    if (input.operation === "models") return { models: ["fake", "chosen-model"], defaultModel: "fake" };
    saves.push(input.action); return { view: projectMember({ ...current, model: input.action.model }) };
  });
  await click(root, "Open member →");
  const picker = all(root).find(n => n.className === "model-picker");
  for (const fn of picker.listeners.click) await fn();
  const choice = all(root).find(n => n.attrs["data-model"] === "chosen-model");
  for (const fn of choice.listeners.click) await fn();
  current = { ...member, model: "outside-model", file: { ...member.file, revision: nextRevision } };
  await app.observe({ team: { ...team, members: [current] } });
  await click(root, "Save model");
  assert.equal(saves[0].model, "chosen-model"); assert.equal(saves[0].expectedRevision, nextRevision);
  app.dispose();
});

test("acknowledged brief save updates the next edit revision even when team refresh is unavailable", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  const app = renderSquadView(doc, root, { team }, async input => {
    calls.push(input);
    return { saved: true, brief: { text: input.text, revision: nextRevision }, refreshUnavailable: true, message: "Brief saved. Refresh unavailable." };
  });
  const older = app.observation();
  edit(root, "Shared team brief", "Saved new brief"); await click(root, "Save brief");
  await app.observe({ team }, older);
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "Saved new brief");
  assert.doesNotMatch(root.textContent, /Shared brief has unsaved edits/);
  edit(root, "Shared team brief", "Next edit"); await click(root, "Save brief");
  assert.equal(calls[1].expectedRevision, nextRevision); assert.equal(calls.length, 2);
  app.dispose();
});

test("focused brief edits retain the displayed revision across background refresh", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), calls = [];
  const app = renderSquadView(doc, root, { team }, async input => { calls.push(input); return { error: "stale_revision", message: "Changed elsewhere" }; });
  const field = all(root).find(n => n.attrs["aria-label"] === "Shared team brief"); field.focus();
  await app.observe({ team: { ...team, brief: { text: "Outside brief", revision: nextRevision } } });
  assert.equal(field.value, member.brief.text);
  edit(root, "Shared team brief", "My edit of the displayed brief"); await click(root, "Save brief");
  assert.equal(calls[0].expectedRevision, revision);
  assert.equal(all(root).find(n => n.attrs["aria-label"] === "Shared team brief").value, "My edit of the displayed brief");
  app.dispose();
});

test("creating the current draft retires a pending suggestion and opens the saved team", async () => {
  const doc = fakeDoc(), root = doc.createElement("main"), proposal = proposalFixture();
  const app = renderSquadView(doc, root, { team: { ...team, members: [] }, proposal, proposalOffer: { id: "pending", proposal } }, async () => ({ team: structuredClone(team), creation: { status: "complete" }, outcome: { members: [] } }));
  await click(root, "Create squad");
  assert.match(root.textContent, /Team members/); assert.doesNotMatch(root.textContent, /Use suggested team/);
  assert.ok(all(root).some(n => n.tag === "button" && n.textContent === "Open member →"));
  app.observeOffer({ id: "late", proposal });
  assert.doesNotMatch(root.textContent, /Use suggested team/);
  app.dispose();
});

for (const status of ["complete", "partial", "refused"]) test(`bridge fences pending suggestions during Create and retains only a no-effect offer (${status})`, async () => {
  const proposal = proposalFixture();
  let release, entered;
  const started = new Promise(resolve => { entered = resolve; });
  const session = { rpc: {
    mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify({ ...team, schemaVersion: 2, kind: "kmx.squad-view", members: [] }) }] }) } },
    tools: {
      getCurrentMetadata: async () => ({ tools: [{ name: "create", mcpToolName: "create", mcpServerName: "kmx-agent-builder" }] }),
      execute: async () => { entered(); return new Promise(resolve => { release = resolve; }); },
    },
  } };
  const bridge = await startSquadBridge({ session, proposal });
  const action = input => fetch(bridge.url + "actions", { method: "POST", headers: { "Content-Type": "application/json", Origin: new URL(bridge.url).origin }, body: JSON.stringify(input) }).then(r => r.json());
  const read = () => fetch(bridge.url + "view.json").then(r => r.json());
  try {
    bridge.offerProposal({ ...proposal, brief: "A suggested replacement" });
    const offer = (await read()).proposalOffer;
    const creation = action({ operation: "create", proposal }); await started;
    assert.equal((await action({ operation: "dismiss-proposal", offerId: offer.id })).error, "invalid-action");
    assert.throws(() => bridge.offerProposal(proposal), { code: "invalid-action" });
    const complete = status === "complete", refused = status === "refused";
    release({ resultType: complete ? "success" : "failure", structuredContent: { schemaVersion: 2, kind: "kmx.squad-create", ...(complete ? {} : { reasonCode: "already_exists" }), result: {
      effectsUnknown: false, setup: refused ? "reused" : "initialized", changedPaths: refused ? [] : [".squad"],
      members: [proposal.lead, ...proposal.specialists].map(m => ({ id: m.memberId, state: complete ? "enriched" : "not_attempted", file: { path: `.squad/agents/${m.memberId}/charter.md`, revision } })),
      ...(complete ? { brief: { revision } } : {}),
    } } });
    assert.equal((await creation).creation.status, status);
    const after = await read();
    assert.equal(!!after.proposalOffer, refused);
    if (refused) assert.equal((await action({ operation: "dismiss-proposal", offerId: offer.id })).proposalDismissed, true);
    else assert.throws(() => bridge.offerProposal(proposal), { code: "invalid-action" });
  } finally { await bridge.close(); }
});

test("an empty member opens with Add test and never claims a pass", async () => {
  const doc = fakeDoc(), root = doc.createElement("main");
  const app = renderSquadView(doc, root, { team }, async () => ({ view: projectMember({ ...member, tests: [], examples: "```yaml\nexamples: []\n```" }) }));
  await click(root, "Open member →");
  assert.match(root.textContent, /Add.*test/i);
  assert.doesNotMatch(root.textContent, /1 passing|All tests passed/);
  await click(root, "Add test");
  assert.match(root.textContent, /The agent gets/);
  app.dispose();
});
