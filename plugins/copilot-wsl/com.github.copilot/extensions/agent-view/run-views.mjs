// The canvas's run rows, summaries and notices, created per view with its live state.
import { sourceIdentity, effectiveModel, same, preRouteRefusal } from "./authoring.mjs";
import { el, checkKey, operandKey, quoted, failureFacts, plural, plainReason, isRefused, answerBlock, refusalContext, canvasResultSummary, canvasFooterText, runNotice } from "./editor.mjs";

// Folder tool settings change a result's meaning without changing the file revision.
export const defaultSettings = (view) => (view.tools ?? []).map((name) => ({ name, state: "Enabled" }));
// A result without recorded settings predates them, when every tool ran Enabled.
// Owned pre-route refusals never executed, so no settings vector applies to them.
export const ranWith = (view, result) => !view.settingsInvalid && (preRouteRefusal(result) && result.toolSettings === undefined || same(result?.toolSettings ?? defaultSettings(view), view.toolSettings ?? defaultSettings(view)));
// Retained results are current only if they ran with today's settings.
export const settingsGuard = (history) => Object.create(history, { selected: { value(view, model) {
  const retained = history.selected(view, model);
  return retained && ranWith(view, retained.latest.result) ? retained : undefined;
} } });

export function createRunViews(state, { act, actions, doc, entries, hasDrafts, history, invalidEditors, localExamples, modelChoice, render, sectionDrafts, sectionText, submitAll, submitOne, testDrafts, testing }) {
  const selectedModel = () => effectiveModel(state.base.model, modelChoice.override, state.base.chatModel);
  // Partial runs use request-start settings, not the possibly changed Stop readback.
  // Legacy snapshots had no policy switches and therefore requested Enabled.
  const runFresh = (run) => {
    const attempt = history.attempt(run.model), frozen = run.toolSettings ?? (attempt?.run === run ? defaultSettings(attempt.view) : undefined);
    return !state.base.settingsInvalid && (!frozen || same(frozen, state.base.toolSettings ?? defaultSettings(state.base)));
  };
  const currentRun = () => testing() ? state.run : history.attempt(selectedModel())?.run;
  function runSummary(run) {
    if (run.state === "stopping") return { tone: "running", text: "Stopping…" };
    if (run.approvalWaiting) return { tone: "running", text: "Copilot approval is waiting in chat." };
    const n = run.caseIds.length, done = run.cases.length;
    if (run.state !== "stopped") return { tone: "running", text: `Running · ${done} of ${n} done` };
    const passing = run.cases.filter(c => c.verdict === "pass").length, failing = run.cases.filter(c => c.verdict === "fail").length;
    const unknown = done - passing - failing;
    const counts = [`${passing} passing`, ...(failing ? [`${failing} failing`] : []), ...(unknown ? [`${unknown} didn't finish`] : [])];
    return { tone: failing ? "fail" : unknown ? "error" : "none", text: `${done ? counts.join(", ") + " · " : ""}${done} of ${n} ran` };
  }
  function runRow(id, fallback, run = currentRun(), partner = false) {
    if (!run || !run.caseIds.includes(id)) return fallback;
    const attempt = history.attempt(run.model);
    if ((!testing() || partner) && !same(attempt?.view.authoring?.examples.find(e => e.id === id), state.base.authoring?.examples.find(e => e.id === id))) return fallback;
    const old = partner || !testing() && (hasDrafts() || run.revision !== sourceIdentity(state.base) || !runFresh(run));
    const c = run.cases.find(c => c.caseId === id);
    let base = c?.verdict === "pass" ? "pass" : c?.verdict === "fail" ? "fail" : c ? "error" : "none";
    let label = c?.verdict === "pass" ? "Passed" : c?.verdict === "fail" ? "Failed" : c ? "Didn't finish" : "Not run";
    if (!c && run.state !== "stopped") {
      const next = !run.cases.some(c => c.verdict === "unknown") && run.caseIds.find(id => !run.cases.some(c => c.caseId === id));
      if (next === id && run.state !== "stopping") { base = "running"; label = "Running"; }
      else if (run.state !== "stopping" && fallback.base !== "none") return { ...fallback, kind: `old-${fallback.base}`, tone: "none" };
      else label = "Waiting";
    }
    let note = c?.verdict !== "pass" ? c?.reason ?? "" : "";
    const definitions = testing() ? localExamples() : attempt?.view.authoring?.examples;
    const checks = definitions?.find(e => e.id === id)?.assertions.filter(a => a.id === c?.failedAssertionId);
    if (c?.verdict === "fail" && checks?.length === 1) {
      const raw = checks[0], a = { ...raw, fields: raw.fields ?? Object.entries(raw).filter(([key]) => !["id", "type"].includes(key)) };
      const key = checkKey(a);
      note = failureFacts({ state: "fail", example: { assertions: [a] }, expected: new Map([[key, a]]), checks: new Map([[key, { verdict: "fail" }]]) })?.text ?? note;
    }
    const kind = old && base !== "none" ? `old-${base}` : base;
    return { base, kind, tone: old ? "none" : base, label, note, count: c?.verdict === "pass" ? "passing" : c?.verdict === "fail" ? "failing" : "" };
  }
  function resultSummary() {
    const run = currentRun();
    if (run && !state.blocked && state.route.name !== "detail") return !testing() && (hasDrafts() || run.revision !== sourceIdentity(state.base) || !runFresh(run))
      ? { tone: "none", text: "Out of date" } : runSummary(run);
    return canvasResultSummary(state.base, { n: entries().filter(e => !e.draft?.deleted).length, testing: testing(), one: state.pending?.one,
      blocked: state.blocked, dirty: hasDrafts(), last: history.last });
  }

  const total = () => localExamples().length;
  function footText(scope) {
    if (testing() && state.run) return state.run.state === "stopping" ? "Saved · stopping Copilot turn…"
      : state.run.approvalWaiting ? "Copilot approval is waiting in chat." : `Saved · running ${state.pending.one ? "this test" : plural(total(), "test")}`;
    if (state.busy && state.saveApproval) return "Copilot approval is waiting in chat.";
    return canvasFooterText({ editable: !!state.snap, n: state.snap ? total() : 0, busy: state.busy, pending: state.pending, blocked: state.blocked, savedUntested: state.savedUntested, settingsIncomplete: state.settingsIncomplete,
      invalid: invalidEditors.size, candidate: state.candidate, scope, promptDirty: sectionDrafts.size, testDirty: testDrafts.has(state.route.key), dirty: hasDrafts(), runAlert: state.runAlert });
  }
  function hostedNotice(updates) {
    const notice = el(doc, "span", "meta hosted-notice", "Testing sends the agent prompt and test inputs to the selected hosted model.");
    updates.push(() => { notice.hidden = state.hostedSent || effectiveModel(state.base.model, modelChoice.override, state.base.chatModel) === "fake"; });
    return notice;
  }

  function refusedAlert() {
    const result = !state.blocked && state.base.latest.status === "current" ? state.base.latest.result : undefined;
    return isRefused(result) ? { text: plainReason(result), refused: true } : undefined;
  }
  function runAlertNode(button, scope, alert = state.runAlert) {
    return runNotice(doc, button, alert, hasDrafts(), alert.refused ? refusalContext(state.base, modelChoice.override) : "",
      () => scope === "one" ? submitOne(state.route.key) : submitAll());
  }

  function sourceActions(button, info, updates) {
    // A Run that saved its capture ran the previous revision; show its answer only while that saved revision is the one open.
    const capture = state.lastAdHoc;
    const adHoc = state.base.latest.status === "current" ? state.base.latest.result?.adHoc : (capture?.savedSourceIdentity ?? capture?.savedRevision) === sourceIdentity(state.base) && capture.model === selectedModel() && ranWith(state.base, capture) ? capture.adHoc : undefined;
    if (adHoc) {
      const result = el(doc, "section", "ad-hoc-result");
      result.append(el(doc, "h3", "", "Last ad-hoc run"), el(doc, "p", "", `${adHoc.state} · ${adHoc.verdict}`), answerBlock(doc, adHoc, hasDrafts, undefined, updates));
      updates.push(() => { result.hidden = hasDrafts() || state.blocked; });
      info.append(result);
    }
    const sources = state.base.selectedSources;
    if (sources) {
      const history = el(doc, "details", "source-history"); history.append(el(doc, "summary", "", "Source history"));
      history.append(el(doc, "p", "", "Source snapshots only, not saved verdicts."));
      if (sources.history?.status !== "available") history.append(el(doc, "p", "", "Source history unavailable."));
      for (const version of sources.history?.versions ?? []) {
        const row = el(doc, "div", "history-row"); row.append(el(doc, "span", "", version.label));
        if (state.snap && version.sourceKind === "pair" && sources.operations.restore) row.append(button("Restore sources", () => act("restore", { restoreVersionId: version.versionId }), "tb", "clean"));
        history.append(row);
      }
      info.append(history);
    }
    if (!state.snap) { if (sources) info.append(button("Reload sources", () => act("reload"), "tb", "reload")); return; }
    const runInput = el(doc, "textarea"); runInput.setAttribute("aria-label", "Ad-hoc input"); runInput.rows = 2;
    const box = el(doc, "div", "source-actions"), run = el(doc, "details", "run-disclosure");
    run.append(el(doc, "summary", "", "Try another input"), runInput, button("Run", () => actions.runInput(runInput.value), "sb", "clean"));
    updates.push(() => { runInput.disabled = state.busy || state.blocked || hasDrafts(); });
    box.append(run, button("Reload and discard draft", () => act("reload"), "tb", "reload"));
    info.append(box);
  }

  function explanation(button, facts, local, index, mutate, eligible, updates) {
    const box = el(doc, "div", "why");
    updates.push(() => { box.className = local() ? "why old" : "why"; });
    box.append(el(doc, "span", "why-text", facts.text));
    if (facts.actual !== undefined && index >= 0) {
      const actions = el(doc, "div", "why-actions");
      const prompt = state.snap ? sectionText(state.snap.main.key) : "";
      // Replace only literal unique prompt text.
      if (prompt.split(facts.actual).length === 2) {
        actions.append(button("Apply to prompt", () => {
          if (!eligible()) return;
          sectionDrafts.set(state.snap.main.key, prompt.replace(facts.actual, () => facts.value));
          if (sectionDrafts.get(state.snap.main.key) === state.snap.main.text) sectionDrafts.delete(state.snap.main.key);
          render();
        }, "sb"));
      }
      actions.append(button(`Or expect ${quoted(facts.actual)} in this test`, () => {
        if (!eligible()) return;
        mutate((d) => { const target = d.raw.assertions[index]; target[operandKey(target.type)] = facts.actual; }); render();
      }, "tb"));
      const repairs = [...actions.childNodes];
      updates.push(() => { actions.replaceChildren(...(eligible() ? repairs : [])); });
      box.append(actions);
    }
    return box;
  }

  return { selectedModel, runFresh, currentRun, runSummary, runRow, resultSummary, total, footText, hostedNotice, refusedAlert, runAlertNode, sourceActions, explanation };
}
