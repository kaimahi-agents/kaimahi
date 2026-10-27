import test from "node:test";
import assert from "node:assert/strict";
import { validateProposal, projectMember, createNativeSquadBackend } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-backend.mjs";
import { readSnapshot } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";
import { settingsGuard } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/run-views.mjs";

const revision = "sha256:" + "a".repeat(64);
const tests = [{ id: "one", input: "Hello", assertions: [{ id: "check", type: "contains", value: "Hello" }] }];
const lead = { memberId: "lead", role: "lead", displayRole: "Lead / Architect", taskInstructions: "Reply with exactly `Hello`.", model: "fake", tests };
const proposal = { brief: "Support offline work.", lead, specialists: [] };
const raw = { schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: "lead", state: "ready", profile: { role: "Lead / Architect" }, file: { path: ".squad/agents/lead/charter.md", revision }, taskInstructions: lead.taskInstructions, model: "fake", instructions: "Native instructions", examples: "```yaml\nexamples: []\n```", tests, brief: { text: "Offline work", revision } };
const team = { schemaVersion: 2, kind: "kmx.squad-view", root: "/project", state: "uninitialized", members: [], brief: { text: "", revision: "none" } };
const complete = { effectsUnknown: false, changedPaths: [raw.file.path, ".squad/kmx-brief.md"], setup: "initialized", members: [{ id: "lead", state: "enriched", file: raw.file }], brief: { revision } };

test("native proposal preserves reviewed fields and refuses duplicate or extra authority", () => {
  assert.equal(validateProposal(proposal).lead.taskInstructions, lead.taskInstructions);
  assert.match(validateProposal(proposal).lead.examples, /contains/);
  assert.throws(() => validateProposal({ ...proposal, root: "/other" }));
  assert.throws(() => validateProposal({ ...proposal, specialists: [{ ...lead }] }));
  assert.throws(() => validateProposal({ ...proposal, lead: { ...lead, tests: [] } }));
});
test("native projection is editable without a canonical source and rejects false identity", () => {
  const view = projectMember(raw);
  assert.equal(view.authoring.taskInstructions, lead.taskInstructions);
  assert.equal(view.authoring.source, "");
  assert.equal(view.latest.status, "none");
  assert.equal(readSnapshot(view).main.text, lead.taskInstructions);
  assert.throws(() => projectMember({ ...raw, file: { ...raw.file, path: "other.agent.md" } }));
  assert.throws(() => projectMember({ ...raw, state: "not_setup" }));
});
test("shared brief revision changes invalidate an otherwise identical member result", () => {
  const preview = { schemaVersion: 1, kind: "kmx.agent-file-preview", file: raw.file, operation: "test", state: "complete", verdict: "pass", fullPass: true, selection: { scope: "full", totalCases: 1, caseIds: ["one"] }, examples: [] };
  const observation = { file: raw.file, briefRevision: revision, preview };
  assert.equal(projectMember(raw, observation).latest.status, "current");
  assert.equal(projectMember({ ...raw, brief: { ...raw.brief, revision: "sha256:" + "b".repeat(64) } }, observation).latest.status, "stale");
});
test("uncertain Create is never replayed by reload", async () => {
  let calls = 0;
  const backend = createNativeSquadBackend({ rpc: {
    mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify(team) }] }) } },
    tools: { getCurrentMetadata: async () => ({ tools: [{ name: "create", mcpServerName: "kmx-agent-builder", mcpToolName: "create" }] }), execute: async () => { calls++; throw new Error("lost transport"); } },
  } });
  await assert.rejects(backend.action({ operation: "create", proposal }), /outcome/);
  await backend.action({ operation: "reload" });
  await assert.rejects(backend.action({ operation: "create", proposal }), /outcome/);
  assert.equal(calls, 1);
});

test("canvas opened before a chat turn initializes offered tools before execution", async () => {
  let initialized = false, executed = 0;
  const backend = createNativeSquadBackend({ rpc: {
    mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify(team) }] }) } },
    tools: {
      getCurrentMetadata: async () => ({ tools: initialized ? [{ name: "create", mcpServerName: "kmx-agent-builder", mcpToolName: "create" }] : null }),
      initializeAndValidate: async () => { initialized = true; },
      execute: async () => { executed++; return { resultType: "success", structuredContent: { schemaVersion: 2, kind: "kmx.squad-create", result: complete } }; },
    },
  } });
  await backend.action({ operation: "create", proposal });
  assert.equal(initialized, true); assert.equal(executed, 1);
});

function harness(outcomes) {
  let readFails = false; const calls = [];
  const session = { rpc: {
    mcp: { resources: { read: async () => {
      if (readFails) throw new Error("unavailable");
      return { contents: [{ text: JSON.stringify(team) }] };
    } } },
    tools: {
      getCurrentMetadata: async () => ({ tools: ["create", "edit"].map(name => ({ name, mcpServerName: "kmx-agent-builder", mcpToolName: name })) }),
      execute: async (input) => { calls.push(input); const data = outcomes.shift(); if (data.failRead) readFails = true; return { resultType: data.reasonCode ? "failure" : "success", structuredContent: { schemaVersion: 2, kind: "kmx.squad-create", ...data } }; },
    },
  } };
  return { backend: createNativeSquadBackend(session), calls, recover: () => { readFails = false; } };
}

test("partial Create survives a failed readback and remains visible after reload", async () => {
  const partial = { effectsUnknown: true, changedPaths: [raw.file.path], setup: "initialized", members: [{ id: "lead", state: "registered_unenriched", file: raw.file }] };
  const h = harness([{ result: partial, reasonCode: "squad_enrichment_failed", failRead: true }]);
  await h.backend.team();
  const result = await h.backend.action({ operation: "create", proposal });
  assert.deepEqual(result.outcome, partial);
  assert.equal(result.creation.status, "unknown");
  assert.equal(result.refreshUnavailable, true);
  assert.deepEqual((await h.backend.snapshot()).outcome, partial, "page reads retain the acknowledged outcome too");
  h.recover();
  assert.deepEqual((await h.backend.action({ operation: "reload" })).outcome, partial);
  await assert.rejects(h.backend.action({ operation: "create", proposal }), /outcome/);
  assert.equal(h.calls.length, 1);
});

test("known no-effect Create refusal leaves the proposal available for an explicit corrected submission", async () => {
  const refusal = { effectsUnknown: false, changedPaths: null, members: [{ id: "lead", state: "not_attempted", file: { path: raw.file.path } }] };
  const h = harness([{ result: refusal, reasonCode: "already_exists" }, { result: complete }]);
  const rejected = await h.backend.action({ operation: "create", proposal });
  assert.equal(rejected.creation.status, "refused");
  assert.equal(rejected.team.uncertain, false);
  assert.equal(rejected.team.createBlocked, false);
  await h.backend.action({ operation: "reload" });
  assert.equal(h.calls.length, 1);
  const corrected = await h.backend.action({ operation: "create", proposal: { ...proposal, brief: "Reviewed again." } });
  assert.equal(corrected.creation.status, "complete");
  assert.equal(h.calls.length, 2);
});

test("initialization without a member cast is a partial effect, not a retryable no-op", async () => {
  const h = harness([{ reasonCode: "already_exists", result: { effectsUnknown: false, changedPaths: [".squad/config.json"], setup: "initialized", members: [{ id: "lead", state: "not_attempted", file: { path: raw.file.path } }] } }]);
  const result = await h.backend.action({ operation: "create", proposal });
  assert.equal(result.creation.status, "partial");
  assert.equal(result.team.createBlocked, true);
  await assert.rejects(h.backend.action({ operation: "create", proposal }), /already changed/);
  assert.equal(h.calls.length, 1);
});

test("confirmed creation with unavailable refresh remains complete and cannot be repeated", async () => {
  const h = harness([{ result: complete, failRead: true }]);
  const result = await h.backend.action({ operation: "create", proposal });
  assert.equal(result.creation.status, "complete");
  assert.deepEqual(result.outcome, complete);
  assert.equal(result.refreshUnavailable, true);
  await assert.rejects(h.backend.action({ operation: "create", proposal }), /already changed/);
});

test("shared per-model history cannot revive a pass from an older brief", () => {
  const old = { latest: { result: { contextRevision: revision } } };
  const history = settingsGuard({ selected: () => old });
  assert.equal(history.selected({ contextRevision: revision }, "fake"), old);
  assert.equal(history.selected({ contextRevision: "sha256:" + "b".repeat(64) }, "fake"), undefined);
});

for (const caseId of [undefined, "one"]) test(`pre-selection model refusal is readable and recoverable (${caseId ?? "full"})`, async () => {
  const member = { ...raw, model: "real-model" }; let calls = 0;
  const backend = createNativeSquadBackend({ rpc: {
    mcp: { resources: { read: async () => ({ contents: [{ text: JSON.stringify(member) }] }) } },
    tools: { getCurrentMetadata: async () => ({ tools: [{ name: "test", mcpToolName: "test", mcpServerName: "kmx-agent-builder" }] }), execute: async () => {
      calls++;
      return { resultType: "failure", structuredContent: { schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: member.memberId, file: member.file, briefRevision: revision,
        preview: { schemaVersion: 1, kind: "kmx.agent-file-preview", operation: "test", file: member.file, state: "refused", verdict: "unknown", fullPass: false, reasonCode: "model_unavailable", selection: { scope: "full", totalCases: 1, caseIds: [] }, examples: [] } } };
    } },
  } });
  const input = { operation: "member-action", memberId: "lead", action: { operation: "test", expectedRevision: revision, ...(caseId ? { caseId } : {}) } };
  const response = await backend.action(input);
  assert.equal(response.view.latest.result.reasonCode, "model_unavailable");
  assert.equal(response.view.latest.result.fullPass, false);
  assert.equal(calls, 1);
  await backend.action(input);
  assert.equal(calls, 2, "only the next explicit action tries again");
});

test("proposal generation uses the current provider and instance without native mutations", async () => {
  const sent = [];
  const backend = createNativeSquadBackend({ send: async value => { sent.push(value); }, rpc: {
    mcp: { resources: { read: async ({ uri }) => ({ contents: [{ text: JSON.stringify(uri === "kmx://models" ? { schemaVersion: 1, kind: "kmx.models", defaultModel: "real-model", models: ["real-model"] } : team) }] }) } },
    tools: { execute: () => { throw new Error("Proposal generation cannot mutate native state"); } },
  } }, { extensionId: "plugin:custom:agent-view", instanceId: "reviewed-team" });
  const response = await backend.action({ operation: "propose", goal: "Design sync", draft: proposal });
  assert.match(response.message, /current draft is kept/);
  assert.equal(sent.length, 1);
  assert.equal(sent[0].displayPrompt, "Design sync");
  assert.match(sent[0].prompt, /plugin:custom:agent-view/);
  assert.match(sent[0].prompt, /reviewed-team/);
  assert.match(sent[0].prompt, /real-model/);
});
