import test from "node:test";
import assert from "node:assert/strict";
import { createSquadPreview } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/squad-preview.mjs";

const scope = { id: "run-1", memberId: "lead", revision: "sha256:" + "a".repeat(64), contextRevision: "sha256:" + "b".repeat(64), model: "fake", examples: [{ id: "one", assertions: [{ id: "check", type: "contains" }] }, { id: "two", assertions: [] }], caseIds: ["one", "two"], totalCases: 2 };
function setup() {
  const listeners = new Set(), cancellations = [];
  const session = { on: (name, listener) => { if (name !== "tool.execution_progress") return () => {}; listeners.add(listener); return () => listeners.delete(listener); }, rpc: { abort: () => { throw new Error("Must never abort the session"); } } };
  const preview = createSquadPreview(session, async (memberId, runId) => { cancellations.push([memberId, runId]); return { memberId, runId, cancelRequested: true }; });
  const run = preview.start(scope);
  const emit = (state, extra = {}, callId = run.toolCallId) => {
    const data = { kind: "kmx.squad-preview", runId: run.nativeId, memberId: scope.memberId, revision: scope.revision, briefRevision: scope.contextRevision, state, ...extra };
    for (const listener of listeners) listener({ data: { toolCallId: callId, progressMessage: `1: ${JSON.stringify(data)}` } });
  };
  return { preview, run, emit, listeners, cancellations };
}

test("native progress joins only the active tool call and exact member/brief revisions", () => {
  const h = setup();
  h.emit("started", {}, "foreign");
  assert.equal(h.preview.snapshot("lead", "run-1").cancellable, false);
  h.emit("started");
  h.emit("case-finished", { revision: "other", case: { caseId: "one", verdict: "pass", reason: "ok" } });
  assert.equal(h.preview.snapshot("lead", "run-1").cases.length, 0);
  h.emit("case-finished", { case: { caseId: "one", verdict: "fail", reason: "missing", failedAssertionId: "check" } });
  h.emit("case-finished", { case: { caseId: "one", verdict: "pass", reason: "duplicate" } });
  assert.deepEqual(h.preview.snapshot("lead", "run-1").cases.map(c => c.verdict), ["fail"]);
  assert.equal(h.preview.snapshot("other", "run-1"), undefined);
  h.preview.dispose(); assert.equal(h.listeners.size, 0);
});

test("stop waits for native registration, cancels only once, and preserves completed rows", async () => {
  const h = setup();
  assert.equal(h.preview.stop("other", "run-1").stopping, false);
  h.preview.stop("lead", "run-1");
  assert.equal(h.cancellations.length, 0);
  h.emit("started"); await Promise.resolve();
  h.preview.stop("lead", "run-1");
  assert.deepEqual(h.cancellations, [["lead", h.run.nativeId]]);
  assert.notEqual(h.run.nativeId, "run-1");
  h.emit("case-finished", { case: { caseId: "two", verdict: "pass", reason: "late" } });
  assert.equal(h.preview.snapshot("lead", "run-1").cases.length, 0);
  h.preview.finish(h.run, true);
  assert.equal(h.preview.snapshot("lead", "run-1").state, "stopped");
  assert.equal(h.preview.snapshot("lead", "run-1").cancellable, false);
  h.preview.dispose();
});

test("unconfirmed cancellation never fabricates a stopped result", async () => {
  let listener;
  const p = createSquadPreview({ on: (name, fn) => { if (name === "tool.execution_progress") listener = fn; return () => {}; } }, async () => { throw new Error("denied"); });
  const run = p.start(scope);
  listener({ data: { toolCallId: run.toolCallId, progressMessage: '1: ' + JSON.stringify({ kind: "kmx.squad-preview", runId: run.nativeId, memberId: "lead", revision: scope.revision, briefRevision: scope.contextRevision, state: "started" }) } });
  p.stop("lead", "run-1"); await Promise.resolve(); await Promise.resolve();
  assert.equal(p.snapshot("lead", "run-1").state, "stopping");
  assert.match(p.snapshot("lead", "run-1").stopError, /not confirmed/);
  p.finish(run); assert.equal(p.snapshot("lead", "run-1").state, "complete"); p.dispose();
});

test("only this preview's approval request is reported", async () => {
  let pending = [];
  const p = createSquadPreview({ rpc: { permissions: { pendingRequests: async () => ({ items: pending }) } } }, async () => ({}));
  const run = p.start(scope);
  pending = [{ requestId: "foreign", request: { toolCallId: "other" } }];
  assert.equal((await p.poll("lead", "run-1")).approvalWaiting, undefined);
  pending.push({ requestId: "ours", request: { toolCallId: run.toolCallId } });
  assert.equal((await p.poll("lead", "run-1")).approvalWaiting, true);
  p.finish(run); assert.equal(p.snapshot("lead", "run-1").approvalWaiting, undefined); p.dispose();
});
