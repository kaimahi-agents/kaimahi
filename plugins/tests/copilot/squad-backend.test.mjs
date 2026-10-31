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
  const input = { operation: "member-action", memberId: "lead", action: { operation: "test", expectedRevision: revision, expectedBriefRevision: revision, ...(caseId ? { caseId } : {}) } };
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

function memberHarness() {
  const h = { current: structuredClone(raw), calls: [], sent: [], unavailable: false };
  h.backend = createNativeSquadBackend({ send: async message => { assert.equal(typeof message, "object"); h.sent.push(message); }, rpc: {
    mcp: { resources: { read: async ({ uri }) => {
      if (h.read) return h.read(uri);
      if (h.unavailable) throw new Error("read unavailable");
      return { contents: [{ text: JSON.stringify(uri === "kmx://squad" ? { ...team, state: "ready", members: [h.current], brief: h.current.brief } : h.current) }] };
    } } },
    tools: {
      getCurrentMetadata: async () => ({ tools: ["create", "edit", "test", "run"].map(name => ({ name, mcpToolName: name, mcpServerName: "kmx-agent-builder" })) }),
      execute: async input => { h.calls.push(input); return h.execute(input); },
    },
  } });
  return h;
}
const nextRevision = "sha256:" + "b".repeat(64);
const pinnedTest = { operation: "member-action", memberId: "lead", action: { operation: "test", expectedRevision: revision, expectedBriefRevision: revision } };
const nativeReply = structuredContent => ({ resultType: "success", structuredContent });

test("member or brief changes before Test refuse before native execution", async () => {
  for (const change of [h => { h.current.file.revision = nextRevision; }, h => { h.current.brief.revision = nextRevision; }]) {
    const h = memberHarness();
    await h.backend.member("lead"); change(h);
    await assert.rejects(h.backend.action(pinnedTest), { code: "stale_revision" });
    assert.equal(h.calls.length, 0);
    h.backend.dispose();
  }
  const h = memberHarness();
  const unpinned = structuredClone(pinnedTest); delete unpinned.action.expectedBriefRevision;
  await assert.rejects(h.backend.action(unpinned), { code: "stale_revision" });
  assert.equal(h.calls.length, 0); h.backend.dispose();
});

test("native execution carries both reviewed revisions and a late stale refusal stays recoverable", async () => {
  const h = memberHarness();
  h.execute = async ({ arguments: args }) => {
    assert.equal(args.expectedRevision, revision); assert.equal(args.expectedBriefRevision, revision);
    return { resultType: "failure", structuredContent: { schemaVersion: 2, reasonCode: "stale_revision" } };
  };
  for (const operation of ["test", "run"]) {
    await assert.rejects(h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, operation, ...(operation === "run" ? { input: "Hello" } : {}) } }), { code: "stale_revision" });
  }
  assert.equal(h.calls.length, 2);
  assert.equal((await h.backend.team()).uncertain, false);
  h.backend.dispose();
});

test("Save and Test pins the acknowledged new member revision and refuses a changed brief", async () => {
  for (const briefChanged of [false, true]) {
    const h = memberHarness();
    h.execute = async ({ name, arguments: args }) => {
      if (name === "edit") {
        h.current.file.revision = nextRevision; h.current.taskInstructions = args.taskInstructions;
        if (briefChanged) h.current.brief.revision = nextRevision;
        return nativeReply({ schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: "lead", file: h.current.file });
      }
      assert.equal(args.expectedRevision, nextRevision); assert.equal(args.expectedBriefRevision, revision);
      return { resultType: "failure", structuredContent: { schemaVersion: 2, reasonCode: "stale_revision" } };
    };
    const response = await h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, operation: "save-and-test", sections: [{ key: "Task Instructions", text: "Revised prompt" }] } });
    assert.equal(response.saved, true); assert.equal(response.error, "stale_revision");
    assert.deepEqual(h.calls.map(c => c.name), briefChanged ? ["edit"] : ["edit", "test"]);
    h.backend.dispose();
  }
});

test("acknowledged brief Save survives readback failure without another write", async () => {
  const h = memberHarness();
  h.execute = async () => nativeReply({ schemaVersion: 2, kind: "kmx.squad-create", result: complete });
  await h.backend.action({ operation: "create", proposal });
  h.execute = async () => {
    h.current.brief = { text: "Saved new brief", revision: nextRevision }; h.unavailable = true;
    return nativeReply({ schemaVersion: 2, kind: "kmx.squad-brief", file: { path: ".squad/kmx-brief.md", revision: nextRevision } });
  };
  const response = await h.backend.action({ operation: "save-brief", expectedRevision: revision, text: "Saved new brief" });
  assert.equal(response.saved, true); assert.equal(response.refreshUnavailable, true);
  assert.deepEqual(response.brief, h.current.brief); assert.equal(response.team, undefined);
  assert.deepEqual((await h.backend.snapshot()).team.brief, response.brief);
  h.unavailable = false;
  assert.deepEqual((await h.backend.snapshot()).team.brief, response.brief);
  assert.deepEqual(h.calls.map(c => c.name), ["create", "edit"]); h.backend.dispose();
});

test("removing the final test saves an editable empty member and makes no preview call", async () => {
  const h = memberHarness();
  h.execute = async ({ name, arguments: args }) => {
    assert.equal(name, "edit"); assert.match(args.examples, /examples: \[\]/);
    h.current = { ...h.current, file: { ...h.current.file, revision: nextRevision }, tests: [], examples: args.examples };
    return nativeReply({ schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: "lead", file: h.current.file });
  };
  const response = await h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, operation: "save-and-test", examples: [] } });
  assert.equal(response.saved, true); assert.equal(response.error, "no-tests");
  assert.deepEqual(response.view.authoring.examples, []); assert.equal(response.view.authoring.editable, true);
  const again = await h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, expectedRevision: nextRevision } });
  assert.equal(again.error, "no-tests"); assert.equal(h.calls.length, 1);
  h.backend.dispose();
});

test("member test suggestions use the SDK message object without native writes", async () => {
  const h = memberHarness();
  await h.backend.action({ operation: "member-action", memberId: "lead", action: { operation: "suggest" } });
  assert.equal(h.sent.length, 1);
  assert.match(h.sent[0].prompt, /native Squad member "lead"/);
  assert.ok(h.sent[0].prompt.includes(revision));
  assert.equal(h.calls.length, 0); h.backend.dispose();
});

function passingReply(current, operation = "test") {
  return nativeReply({ schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: current.memberId, file: current.file, briefRevision: current.brief.revision,
    preview: { schemaVersion: 1, kind: "kmx.agent-file-preview", operation, file: current.file, state: "complete", verdict: "pass", fullPass: true,
      modelRoute: { provider: "fake", model: "fake" }, selection: { scope: "full", totalCases: 1, caseIds: ["one"] },
      examples: [{ id: "one", state: "complete", verdict: "pass", assertions: [{ id: "check", type: "contains", verdict: "pass" }] }] },
  });
}

for (const failedRead of ["member", "team"]) test(`failed ${failedRead} reads cannot revive cached passes on read recovery`, async () => {
  const h = memberHarness(); h.execute = async () => passingReply(h.current);
  assert.equal((await h.backend.action(pinnedTest)).view.latest.status, "current");
  h.unavailable = true;
  await assert.rejects(failedRead === "member" ? h.backend.member("lead") : h.backend.team(), { code: "native_source_unavailable" });
  h.unavailable = false;
  assert.equal((await h.backend.member("lead")).latest.status, "stale");
  assert.equal(h.calls.length, 1, "recovering reads never run tests");
  assert.equal((await h.backend.action(pinnedTest)).view.latest.status, "current");
  assert.equal(h.calls.length, 2); h.backend.dispose();
});

test("invalid brief refusals are actionable, recoverable and invalidate old passes", async () => {
  const h = memberHarness(); h.execute = async () => passingReply(h.current);
  await h.backend.action(pinnedTest);
  h.execute = async () => ({ resultType: "failure", structuredContent: { schemaVersion: 2, reasonCode: "invalid_squad_brief", reason: "PRIVATE SOURCE" } });
  await assert.rejects(h.backend.action(pinnedTest), error => error.code === "invalid_squad_brief" && /16 KiB/.test(error.message) && !/PRIVATE/.test(error.message));
  assert.equal((await h.backend.team()).uncertain, false);
  assert.equal((await h.backend.member("lead")).latest.status, "stale");
  h.backend.dispose();
});

test("an unqualified roster member cannot regain a cached pass after restoration", async () => {
  const h = memberHarness(); h.execute = async () => passingReply(h.current);
  await h.backend.action(pinnedTest);
  h.current.state = "not_setup";
  await h.backend.team();
  h.current.state = "ready";
  assert.equal((await h.backend.member("lead")).latest.status, "stale");
  assert.equal(h.calls.length, 1); h.backend.dispose();
});

test("an older failed roster read cannot invalidate a newer preview in the backend cache", async () => {
  const h = memberHarness(); h.execute = async () => passingReply(h.current);
  await h.backend.action(pinnedTest);
  let reject;
  h.read = async uri => {
    assert.equal(uri, "kmx://squad");
    return new Promise((_, fail) => { reject = fail; });
  };
  const older = h.backend.team();
  const refused = assert.rejects(older, { code: "native_source_unavailable" });
  h.read = undefined;
  await h.backend.action(pinnedTest);
  reject(new Error("old read failed")); await refused;
  assert.equal((await h.backend.member("lead")).latest.status, "current");
  assert.equal(h.calls.length, 2); h.backend.dispose();
});

for (const operation of ["test", "run"]) for (const failure of ["transport", "envelope", "selection", "preview", "subject"]) test(`read-only ${operation} recovers from ${failure} without replaying or restoring an old pass`, async () => {
  const h = memberHarness();
  const input = { ...pinnedTest, action: { ...pinnedTest.action, operation, ...(operation === "run" ? { input: "Hello" } : {}) } };
  h.execute = async () => passingReply(h.current, operation);
  await h.backend.action(input);
  h.execute = async () => {
    if (failure === "transport") throw new Error("PRIVATE TRANSPORT DATA");
    const reply = passingReply(h.current, operation), data = reply.structuredContent;
    if (failure === "envelope") data.kind = "wrong-kind";
    if (failure === "selection") data.preview.selection.caseIds = ["unexpected"];
    if (failure === "preview") data.preview.fullPass = "true";
    if (failure === "subject") data.memberId = "other";
    return reply;
  };
  await assert.rejects(h.backend.action(input), error => error.code === (failure === "subject" ? "revision_mismatch" : "outcome-unknown") && !/PRIVATE/.test(error.message));
  assert.equal((await h.backend.action({ operation: "reload" })).team.uncertain, false);
  const reload = await h.backend.action({ operation: "member-action", memberId: "lead", action: { operation: "reload" } });
  assert.equal(reload.view.latest.status, "stale");
  assert.equal(h.calls.length, 2, "reads do not retry an uncertain preview");
  h.execute = async () => passingReply(h.current, operation);
  assert.equal((await h.backend.action(input)).view.latest.status, "current");
  assert.equal(h.calls.length, 3); h.backend.dispose();
});

for (const operation of ["create", "save-brief", "save-model"]) for (const failure of ["transport", "envelope"]) test(`uncertain ${operation} remains locked after ${failure}`, async () => {
  const h = memberHarness();
  h.execute = async () => { if (failure === "transport") throw new Error("lost reply"); return nativeReply({ schemaVersion: 2, kind: "wrong-kind" }); };
  const input = operation === "create" ? { operation, proposal }
    : operation === "save-brief" ? { operation, expectedRevision: revision, text: "Updated brief" }
      : { operation: "member-action", memberId: "lead", action: { operation, expectedRevision: revision, model: "new-model" } };
  await assert.rejects(h.backend.action(input), { code: "outcome-unknown" });
  assert.equal((await h.backend.action({ operation: "reload" })).team.uncertain, true);
  await assert.rejects(h.backend.action(pinnedTest), { code: "outcome-unknown" });
  assert.equal(h.calls.length, 1); h.backend.dispose();
});

for (const failure of ["transport", "selection", "subject"]) test(`an edit before an uncertain preview retains write protection (${failure})`, async () => {
  const h = memberHarness();
  h.execute = async ({ name, arguments: args }) => {
    if (name === "edit") {
      h.current.file.revision = nextRevision; h.current.taskInstructions = args.taskInstructions;
      return nativeReply({ schemaVersion: 2, kind: "kmx.squad-member-operation", memberId: "lead", file: h.current.file });
    }
    if (failure === "transport") throw new Error("lost preview reply");
    const reply = passingReply(h.current);
    if (failure === "selection") reply.structuredContent.preview.selection.caseIds = ["unexpected"];
    else reply.structuredContent.memberId = "other";
    return reply;
  };
  const response = await h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, operation: "save-and-test", sections: [{ key: "Task Instructions", text: "Updated instructions" }] } });
  assert.equal(response.error, failure === "subject" ? "revision_mismatch" : "outcome-unknown"); assert.equal(response.saved, true); assert.equal(response.view, undefined);
  assert.equal((await h.backend.action({ operation: "reload" })).team.uncertain, true);
  await assert.rejects(h.backend.action(pinnedTest), { code: "outcome-unknown" });
  assert.deepEqual(h.calls.map(call => call.name), ["edit", "test"]); h.backend.dispose();
});

test("Save and Test without an actual edit retains read-only recovery", async () => {
  const h = memberHarness(); h.execute = async () => { throw new Error("lost preview reply"); };
  await assert.rejects(h.backend.action({ ...pinnedTest, action: { ...pinnedTest.action, operation: "save-and-test", sections: [{ key: "Task Instructions", text: raw.taskInstructions }] } }), { code: "outcome-unknown" });
  assert.equal((await h.backend.action({ operation: "reload" })).team.uncertain, false);
  assert.deepEqual(h.calls.map(call => call.name), ["test"]);
  h.backend.dispose();
});
