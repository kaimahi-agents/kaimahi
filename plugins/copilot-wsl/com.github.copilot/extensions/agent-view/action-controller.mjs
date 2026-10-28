import { sourceIdentity, effectiveModel, validateDraft as validate, testsNeedAcknowledgement, pendingCapture, hasFrontmatter } from "./authoring.mjs";
import { isRefused, plainReason, quoted, clone } from "./editor.mjs";
const CONFLICTS = ["revision_mismatch", "revision-mismatch"];
const UNKNOWN = ["outcome-unknown", "uncertain-outcome", "uncertain_outcome"];

let runSequence = 0;
export function createActionController(state, { execute, fetchProgress, drafts, matches, checkNow, render, refresh, focusName }) {
  const { sectionDrafts, testDrafts, invalidEditors, displayNames, modelChoice } = state;
  const { hasDrafts, rawExamples, entries, localExamples, draftName, checkProblem, adopt } = drafts;
  function clearUndo() { state.promptUndo = undefined; clearTimeout(state.undoTimer); }
  function offerUndo(sections) {
    state.promptUndo = { sections, revision: sourceIdentity(state.base) };
    state.undoTimer = setTimeout(() => { clearUndo(); refresh(); }, 10000);
    state.undoTimer.unref?.();
  }
  const testing = () => state.busy && ["test", "save-and-test"].includes(state.pending?.operation);
  let progressTimer, activeRun, disposed = false;
  const stopWatching = () => { clearTimeout(progressTimer); activeRun = undefined; state.saveApproval = false; };
  function watch(id) {
    if (!fetchProgress) return;
    const poll = async () => {
      try {
        const response = await fetchProgress(id);
        if (!disposed && activeRun === id && response?.run?.id === id) {
          state.run = { ...response.run, ...(state.run?.state === "stopping" ? { state: "stopping", approvalWaiting: false } : {}) }; refresh();
        } else if (!disposed && activeRun === id && !response?.run && state.saveApproval !== (response?.approvalWaiting === true)) {
          // An Edit before the Test can wait for approval in chat.
          state.saveApproval = response?.approvalWaiting === true; refresh();
        }
      } catch { /* Final Test reply remains authoritative when progress is unavailable. */ }
      if (!disposed && activeRun === id) { progressTimer = setTimeout(poll, 200); progressTimer.unref?.(); }
    };
    progressTimer = setTimeout(poll, 200); progressTimer.unref?.();
  }
  async function stop() {
    if (!activeRun || state.run?.id !== activeRun || state.run.state !== "running") return;
    const runId = activeRun;
    state.run = { ...state.run, state: "stopping", approvalWaiting: false }; refresh();
    try { await execute({ operation: "stop", runId }); } catch { /* The long action owns the final outcome. */ }
  }

  async function act(operation, values = {}, submitted = { sections: [], tests: [] }) {
    if (state.busy || state.sourceUnavailable || ((state.blocked || invalidEditors.size) && operation !== "reload")) return;
    const previousPrompt = submitted.sections.map(key => ({ key, text: state.snap.sections.find(s => s.key === key).text }));
    const submittedPrompt = new Map(submitted.sections.map(key => [key, sectionDrafts.get(key)]));
    clearUndo(); state.confirm = undefined; state.lastAdHoc = undefined;
    if (["test", "save-and-test"].includes(operation)) state.history.started(effectiveModel(state.base.model, modelChoice.override, state.base.chatModel));
    state.observationGeneration++; state.run = undefined;
    state.busy = true; state.pending = { operation, one: values.caseId !== undefined }; state.message = operation === "run" ? "Running…" : ""; state.runAlert = undefined; refresh();
    const action = { operation, ...values };
    if (values.examples && state.base.authoring?.recordingControls) action.exampleOrigins = values.examples.map(raw => entries().find(e=>e.raw?.id===raw.id)?.origin?.id ?? (rawExamples().some(e=>e.id===raw.id) ? raw.id : null));
    if (!["reload", "suggest"].includes(operation)) action.expectedRevision = state.base.file.revision;
    if (state.base.selectedSources && !["reload", "suggest"].includes(operation)) {
      action.expectedSourceVersion = state.base.selectedSources.sourceVersion;
      if (state.base.toolSettingsRevision !== undefined) action.expectedSettingsRevision = state.base.toolSettingsRevision;
    }
    const executing = ["save-and-test", "test", "run"].includes(operation);
    const hostedAttempt = executing && effectiveModel(state.base.model, modelChoice.override, state.base.chatModel) !== "fake";
    if (executing) {
      action.showContent = true;
      if (state.base.contextRevision !== undefined) action.expectedBriefRevision = state.base.contextRevision;
      if (modelChoice.override) action.model = modelChoice.override;
    }
    if (["test", "save-and-test"].includes(operation)) {
      if (state.base.authoring?.recordingControls) { const models = state.history.comparisonModels(effectiveModel(state.base.model, modelChoice.override, state.base.chatModel)); if (models.length > 1) action.comparisonModels = models; }
      action.runId = `canvas-${Date.now().toString(36)}-${++runSequence}`; activeRun = action.runId; watch(activeRun);
    }
    let response;
    try { response = await execute(action); } catch {
      if (hostedAttempt) state.hostedSent = true;
      stopWatching();
      if (disposed) return;
      state.busy = false; state.pending = undefined; state.blocked = true;
      state.message = "Connection lost. The action may have completed. Do not resubmit. Your draft is still here; reload to inspect the saved file.";
      render(); return;
    }
    stopWatching();
    if (disposed) return;
    state.busy = false; state.pending = undefined; state.message = "";
    if (state.sourceInvalidated && matches(response?.view)) {
      if (executing && !response.error && !response.stopped && response.view.latest.status === "current") state.sourceInvalidated = false;
      else response = { ...response, view: { ...response.view, latest: { status: "stale", badge: "stale" } } };
    }
    // A native preview may be interrupted by newer prompt edits while awaiting its reply.
    submitted = { ...submitted, sections: submitted.sections.filter(key => sectionDrafts.get(key) === submittedPrompt.get(key)) };
    const latest = response?.view?.latest;
    if (operation === "run" && response?.capture?.adHoc) state.lastAdHoc = response.capture;
    if (response?.capture) { state.history.noteCapture(response.capture, response.capture.savedRevision && matches(response.view) ? response.view : undefined); state.lastCapture = response.capture; }
    for (const result of response?.comparison?.results ?? []) if (matches(result)) state.history.remember(result, true);
    if (hostedAttempt && !response?.settingsIncomplete && (UNKNOWN.includes(response?.error) || CONFLICTS.includes(response?.error) || latest?.status === "current" && latest.result && !isRefused(latest.result))) state.hostedSent = true;
    if (executing && !response?.settingsIncomplete && matches(response?.view) && !response.stopped) {
      state.history.remember(response.view, true, response.run?.elapsedMs, executing ? effectiveModel(state.base.model, modelChoice.override, state.base.chatModel) : undefined);
      modelChoice.confirm(response.view);
    }
    const tested = ["test", "save-and-test"].includes(operation);
    if (response?.stopped && response.run?.id === action.runId && matches(response.view)) {
      adopt(response.view, response.saved ? submitted : { sections: [], tests: [] });
      state.history.stop(response.run, response.view); state.run = response.run; state.lastRun = undefined;
      if (hostedAttempt) state.hostedSent = true;
    } else if (operation === "reload") {
      if (matches(response?.view)) {
        state.history.reset(); state.history.remember(response.view); state.run = undefined;
        sectionDrafts.clear(); testDrafts.clear(); state.toolsDraft = undefined; state.settingsDraft.clear(); displayNames.clear(); state.blocked = false; state.savedUntested = false; state.settingsIncomplete = false; state.lastRun = undefined;
        adopt(response.view, { sections: [], tests: [] }); state.message = "Reloaded the saved agent. Drafts were discarded.";
      } else state.message = response?.message || "Reload did not complete. Your draft is still here.";
    } else if (response?.error === "no-tests" && matches(response.view)) {
      adopt(response.view, response.saved ? submitted : { sections: [], tests: [] }); state.lastRun = undefined;
    } else if (response?.error) {
      const unknown = UNKNOWN.includes(response.error) || CONFLICTS.includes(response.error);
      // Unsaved failures must still match our revision.
      const fresh = matches(response.view) && (response.saved === true || sourceIdentity(response.view) === sourceIdentity(state.base));
      if (response.settingsIncomplete && (response.saved === true || unknown)) {
        state.blocked = true; state.savedUntested = response.saved === true; state.settingsIncomplete = true;
        state.message = `${response.saved ? "Some changes were saved. " : ""}${unknown ? "The tool settings outcome is unconfirmed. " : ""}Tests did not run. Your draft is still here; reload to inspect the saved agent and settings.`;
      } else if (["stale_revision", "stale-revision", "stale_settings_revision"].includes(response.error)) {
        state.message = response.error === "stale_settings_revision" ? "Tool settings changed outside the panel. Your edits are kept." : "This agent changed outside the panel. Your edits are kept."; checkNow();
      } else if (!unknown && fresh && (response.saved || ["test", "run", "save-and-test"].includes(operation))) {
        // Only confirmed snapshots can replace the baseline after failure.
        adopt(response.view, response.saved ? submitted : { sections: [], tests: [] });
        const result = response.view.latest.result;
        state.runAlert = { text: result ? plainReason(result) : response.message || "", refused: isRefused(result), saved: response.saved === true, finished: response.view.latest.status === "current", one: values.caseId };
        state.lastRun = undefined;
      } else if (unknown || response.saved === true || (response.error === "action-failed" && ["test", "run"].includes(operation))) {
        state.blocked = true; state.savedUntested = response.saved === true;
        state.message = CONFLICTS.includes(response.error) ? "Changed elsewhere during the run. Your draft is still here. Reload and discard draft to continue."
          : response.saved ? response.approvalTimeout ? `Saved; your draft is still here. ${response.message}` : "Saved; test result unconfirmed. Your draft is still here. Reload before continuing."
          : response.message || "Outcome not confirmed. Reload deliberately to inspect the saved file.";
      } else if (response.error === "permission_required" && tested) {
        state.runAlert = { text: response.message || "", saved: false, one: values.caseId };
      } else state.message = response.message || "The action was refused. Your draft is still here.";
    } else if (operation === "suggest") {
      state.message = response?.message || "Asked Copilot to add tests in chat.";
    } else if (matches(response?.view)) {
      adopt(response.view, submitted);
      if (tested) state.lastRun = { one: values.caseId };
      if (response.capture?.savedRevision) state.message = "Recording saved. Later runs replay it.";
    } else {
      state.blocked = true; state.message = "Outcome not confirmed. Do not resubmit. Reload deliberately to inspect the saved file.";
    }
    if (previousPrompt.length && response?.saved === true && matches(response.view) && sourceIdentity(state.base) === sourceIdentity(response.view) && !state.blocked && !state.candidate && sectionDrafts.size === 0) offerUndo(previousPrompt);
    render();
  }

  // New or changed tests in a Copilot file become part of its prompt: ask first,
  // and only a fresh go-ahead (never a stale one) adds the acknowledgement.
  // The same go-ahead covers a first live capture, which writes recorded calls into the tests.
  function acknowledged(values, again, ack, ids) {
    if (state.base.selectedSources && state.base.selectedSources.testsState !== "inline") return true;
    const source = state.base.authoring.source, saved = rawExamples(), intended = values.examples ?? saved;
    const settings = drafts.toolNames().map(name => ({ name, state: drafts.toolState(name) }));
    if (!testsNeedAcknowledgement(source, saved, intended) && !(state.base.authoring.recordingControls && hasFrontmatter(source) && pendingCapture(intended, settings, ids))) return true;
    if (ack !== true) { state.confirm = { go: again }; render(); return false; }
    values.acknowledgeCopilotTests = true; return true;
  }
  // Try another input: a first live capture writes recorded calls into a Copilot file's tests,
  // so it takes the same inline go-ahead; any other Run goes straight through.
  function runInput(input, ack = false) {
    if (!state.snap || state.busy || state.blocked || state.candidate || invalidEditors.size) return;
    const saved = rawExamples(), settings = drafts.toolNames().map(name => ({ name, state: drafts.toolState(name) })), values = { input };
    if ((!state.base.selectedSources || state.base.selectedSources.testsState === "inline") && state.base.authoring?.recordingControls && hasFrontmatter(state.base.authoring.source) && pendingCapture(saved, settings, saved.map(e => e.id))) {
      if (ack !== true) { state.confirm = { go: () => runInput(input, true) }; render(); return; }
      values.acknowledgeCopilotTests = true;
    }
    act("run", values);
  }
  function submitSettings(values, submitted) {
    if (!state.settingsDraft.size) return;
    values.toolSettings = drafts.toolNames().filter(name => state.settingsDraft.has(name)).map(name => ({ name, state: state.settingsDraft.get(name) }));
    if (state.base.toolSettingsRevision !== undefined) values.expectedSettingsRevision = state.base.toolSettingsRevision;
    submitted.settings = true;
  }
  // Enter saves all drafts once, then runs the suite.
  function submitAll(ack = false) {
    if (!state.snap || state.busy || state.blocked || state.candidate || invalidEditors.size) return;
    const list = entries(), intended = localExamples();
    if ((state.toolsDraft?.length ?? 0) > 32) { state.message = "Keep at most 32 tools for this agent."; refresh(); return; }
    for (const e of list) {
      if (!e.draft || e.draft.deleted) continue;
      const problem = validate(e.draft, intended);
      if (problem) { if (problem.check && checkProblem(e.key, e.raw)) return; state.message = `Finish or cancel the unsaved test ${quoted(draftName(e))}: ${problem}`; refresh(); return; }
    }
    const values = {}, submitted = { sections: [...sectionDrafts.keys()], tests: [...testDrafts.keys()] };
    if (sectionDrafts.size) values.sections = [...sectionDrafts].map(([key, text]) => ({ key, text }));
    if (testDrafts.size) values.examples = localExamples();
    if (state.toolsDraft !== undefined) { values.tools = state.toolsDraft; submitted.tools = true; }
    submitSettings(values, submitted);
    if (!hasDrafts() && !intended.length) { state.message = "No tests yet — add or suggest one"; refresh(); return; }
    if (!acknowledged(values, () => submitAll(true), ack, intended.map(e => e.id))) return;
    act(hasDrafts() ? "save-and-test" : "test", values, submitted);
  }
  // Detail Enter saves the prompt and this test, then runs its caseId.
  function submitOne(key, ack = false) {
    if (!state.snap || state.busy || state.blocked || state.candidate || invalidEditors.size) return;
    const entry = entries().find((e) => e.key === key);
    if (!entry?.raw || entry.draft?.deleted) return;
    if (entry.draft?.origin === null && !entry.draft.fixedId && !entry.draft.name.trim()) {
      state.nameHintKey = key; state.message = ""; refresh(); focusName(); return;
    }
    let intended = clone(rawExamples());
    if (entry.draft) {
      const raw = entry.raw;
      if (entry.draft.origin === null) intended.push(raw); else intended = intended.map((e) => e.id === entry.draft.origin ? raw : e);
      const problem = validate(entry.draft, intended);
      if (problem) { if (problem.check && checkProblem(key, entry.raw)) return; state.message = problem; refresh(); return; }
      intended = clone(intended);
    }
    const values = { caseId: entry.raw.id }, submitted = { sections: [...sectionDrafts.keys()], tests: entry.draft ? [key] : [] };
    if (sectionDrafts.size) values.sections = [...sectionDrafts].map(([k, text]) => ({ key: k, text }));
    if (entry.draft) values.examples = intended;
    if (state.toolsDraft !== undefined) { values.tools = state.toolsDraft; submitted.tools = true; }
    submitSettings(values, submitted);
    if (!acknowledged(values, () => submitOne(key, true), ack, [entry.raw.id])) return;
    act(state.toolsDraft !== undefined || state.settingsDraft.size || sectionDrafts.size || entry.draft ? "save-and-test" : "test", values, submitted);
  }

  return { act, runInput, submitAll, submitOne, testing, clearUndo, stop, dispose() { disposed = true; stopWatching(); } };
}
