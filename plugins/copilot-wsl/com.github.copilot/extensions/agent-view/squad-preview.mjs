import { randomBytes } from "node:crypto";
import { clean } from "./view-resource.mjs";
import { observePreview } from "./host-relay.mjs";

// Only this canvas's correlated native Test can be stopped. No session abort.
export function createSquadPreview(session, cancel) {
  let current, disposed = false;
  const finish = run => { if (run) { run.pending = false; run.elapsedMs = Date.now() - run.started; run.unsubscribe?.(); run.approval?.clear(); } };
  async function requestStop(run) {
    if (!run.registered || run.cancelSent || !run.pending) return;
    run.cancelSent = true;
    try {
      const result = await cancel(run.memberId, run.nativeId);
      if (result.memberId !== run.memberId || result.runId !== run.nativeId || typeof result.cancelRequested !== "boolean") throw new Error("unconfirmed");
      run.cancelRequested = result.cancelRequested;
    } catch {
      run.stopError = "The stop request was not confirmed. The previous test may still finish.";
    }
  }
  function progress(run, event) {
    if (disposed || current !== run || !run.pending || event?.data?.toolCallId !== run.toolCallId) return;
    const message = event.data.progressMessage;
    if (typeof message !== "string" || message.length > 16384) return;
    const match = message.match(/^\d+(?:\.\d+)?: (\{[^]*\})$/);
    if (!match) return;
    let data; try { data = JSON.parse(match[1]); } catch { return; }
    if (data?.kind !== "kmx.squad-preview" || data.runId !== run.nativeId || data.memberId !== run.memberId || data.revision !== run.revision || data.briefRevision !== run.contextRevision) return;
    if (data.state === "started" && !run.registered) { run.registered = true; if (run.stopWanted) void requestStop(run); return; }
    const row = data.case;
    if (run.stopWanted || !run.registered || data.state !== "case-finished" || !row || row.caseId !== run.caseIds[run.rows.size] || run.rows.has(row.caseId) || !["pass", "fail", "unknown"].includes(row.verdict) || typeof row.reason !== "string") return;
    const check = run.examples.find(e => e.id === row.caseId)?.assertions.find(a => a.id === row.failedAssertionId && a.type !== "llmJudge");
    run.rows.set(row.caseId, { caseId: row.caseId, verdict: row.verdict, reason: clean(row.reason, 500), ...(row.verdict === "fail" && check ? { failedAssertionId: check.id } : {}) });
  }
  const snapshot = (memberId, id) => {
    const run = current;
    if (!run || disposed || run.memberId !== memberId || run.id !== id) return;
    return { id: run.id, revision: run.revision, contextRevision: run.contextRevision, model: run.model, caseIds: [...run.caseIds], totalCases: run.totalCases,
      cases: run.caseIds.flatMap(id => run.rows.has(id) ? [{ ...run.rows.get(id) }] : []), state: run.state, elapsedMs: run.elapsedMs ?? Date.now() - run.started,
      cancellable: run.registered && run.pending, ...(run.approval?.waiting() && !run.stopWanted ? { approvalWaiting: true } : {}), ...(run.stopError ? { stopError: run.stopError } : {}) };
  };
  return {
    start(scope) {
      finish(current);
      const nativeId = randomBytes(24).toString("base64url");
      const run = { ...scope, nativeId, rows: new Map(), state: "running", pending: true, registered: false, started: Date.now(), toolCallId: nativeId };
      current = run;
      run.approval = observePreview(session, run.toolCallId);
      if (typeof session.on === "function") run.unsubscribe = session.on("tool.execution_progress", event => progress(run, event));
      return run;
    },
    stop(memberId, id) {
      const run = current;
      if (!run || run.memberId !== memberId || run.id !== id || !run.pending || disposed) return { stopping: false };
      run.stopWanted = true; run.state = "stopping";
      void requestStop(run);
      return { stopping: true };
    },
    finish(run, stopped = false) { finish(run); if (run) run.state = stopped ? "stopped" : "complete"; },
    snapshot,
    async poll(memberId, id) {
      const run = current;
      if (run?.memberId === memberId && run.id === id && run.pending) await run.approval.refresh();
      return current === run ? snapshot(memberId, id) : undefined;
    },
    dispose() { disposed = true; finish(current); current = undefined; },
  };
}
