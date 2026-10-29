import { sourceIdentity, readSnapshot, portableId, effectiveModel, preRouteRefusal } from "./authoring.mjs";
import { same, uniqueIndex, quoted } from "./editor.mjs";

export function createObservationController(state, { path, options, drafts, clearUndo, render, refresh }) {
  const { sectionDrafts, testDrafts, invalidEditors, displayNames, history, modelChoice } = state;
  const { hasDrafts, pruneDrafts } = drafts;
  const observation = () => ({ generation: state.observationGeneration, revision: sourceIdentity(state.base) });
  const matches = (view) => view && !view.error && view.file?.path === path;

  // Only fixed typed errors invalidate; other failures stay best effort.
  const settingsError = (e) => e?.code === "invalid_tool_settings" || e?.error === "invalid_tool_settings";
  const pairedError = (e) => e?.code === "paired_source_required" || e?.error === "paired_source_required";
  // Retained results stay visible as old, drafts are untouched, and a valid read recovers.
  function invalidate(started, paired = false) {
    if (started.generation !== state.observationGeneration || started.revision !== sourceIdentity(state.base)
      || state.busy || state.blocked || invalidEditors.size || !paired && state.base.settingsInvalid) return false;
    if (paired) {
      state.sourceUnavailable = true; state.sourceInvalidated = true; state.blocked = true;
      state.message = "Paired test files are not supported in this canvas. Your drafts are kept.";
      state.candidate = undefined; state.confirm = undefined; state.run = undefined; state.lastAdHoc = undefined;
      history.reset(); clearUndo(); state.lastRun = undefined; state.runAlert = undefined;
    }
    const { latest } = state.base;
    state.base = { ...state.base, ...(!paired ? { settingsInvalid: true } : {}), latest: latest.status === "current" ? { ...latest, status: "stale", badge: "stale" } : latest };
    state.observationGeneration++;
    render(); return true;
  }
  function observeFailure(error, started = observation()) { return pairedError(error) ? invalidate(started, true) : settingsError(error) ? invalidate(started) : false; }
  // Observation never writes or clears a lock.
  function observe(view, started = observation()) {
    if (pairedError(view)) return invalidate(started, true);
    if (settingsError(view)) return invalidate(started);
    if (started.generation !== state.observationGeneration || started.revision !== sourceIdentity(state.base)
      || !matches(view) || state.busy || state.blocked && !state.sourceUnavailable || invalidEditors.size) return false;
    // A valid read unlocks actions, but only a new execution can replace invalidated results.
    if (state.sourceInvalidated) view = { ...view, latest: { status: "stale", badge: "stale" } };
    const recoveringSource = state.sourceUnavailable;
    if (recoveringSource) { state.sourceUnavailable = false; state.blocked = false; state.message = ""; }
    const observed = view;
    const selected = effectiveModel(view.model, modelChoice.override, view.chatModel);
    const route = view.latest.result?.modelRoute, incomingModel = route?.slice(route.indexOf("/") + 1);
    const sameBaseline = sourceIdentity(view) === sourceIdentity(state.base) && (!state.settingsDraft.size || view.toolSettingsRevision === state.base.toolSettingsRevision);
    const adoptable = sameBaseline || !hasDrafts();
    const stopped = history.attempt(incomingModel)?.run?.revision === sourceIdentity(view);
    const previousHistory = [history.pair(), history.attempt(incomingModel)];
    if (adoptable) { history.focus(selected); if (!stopped) history.remember(observed); }
    const changedHistory = adoptable && !same(previousHistory, [history.pair(), history.attempt(incomingModel)]);
    const retained = view.selectedSources && sameBaseline ? history.selected(view, selected) : undefined;
    view = modelChoice.view(view, retained ?? (view.latest.status !== "current" || preRouteRefusal(view.latest.result) || incomingModel === selected ? undefined : history.selected(view, selected)));
    const recovering = !!state.base.settingsInvalid || recoveringSource;
    if (!changedHistory && ((!recovering && same(view, state.base)) || (state.candidate && same(view, state.candidate)))) return false;
    clearUndo(); displayNames.clear(); state.runAlert = undefined;
    state.observationGeneration++;
    state.lastRun = undefined;
    if (adoptable) {
      state.base = view; state.snap = readSnapshot(view); state.candidate = undefined; state.compareOpen = false;
      // New results need new DOM; local edits refresh styling without losing focus.
      pruneDrafts(); render();
      return true;
    }
    state.candidate = view; state.compareOpen = false; render();
    return true;
  }
  async function checkNow() {
    if (typeof options.fetchView !== "function") return;
    const started = observation();
    try { observe(await options.fetchView(), started); } catch (e) { observeFailure(e, started); /* Other failures change nothing. */ }
  }
  function useTheirs() {
    if (!state.candidate) return;
    state.invalidCheck = undefined; clearUndo(); displayNames.clear();
    state.observationGeneration++;
    sectionDrafts.clear(); testDrafts.clear(); invalidEditors.clear(); state.toolsDraft = undefined; state.settingsDraft.clear();
    history.remember(state.candidate);
    state.base = state.candidate; state.snap = readSnapshot(state.base); state.candidate = undefined; state.compareOpen = false; state.lastRun = undefined;
    pruneDrafts(); state.message = "Using the new version. Your edits were discarded."; render();
  }
  // Rebase drafts; retain ambiguous identities.
  function keepMine() {
    if (!state.candidate || invalidEditors.size) return;
    state.invalidCheck = undefined; clearUndo(); displayNames.clear();
    state.observationGeneration++;
    const next = readSnapshot(state.candidate);
    if (!next) { state.message = "The new version can't be edited here. Compare it, or use the new version."; refresh(); return; }
    const theirs = state.candidate.authoring.examples, ids = uniqueIndex(theirs, (e) => e.id);
    const idOf = (d) => d.fixedId ? d.raw.id : portableId(d.name);
    const clash = [...testDrafts.values()].find((d) => d.origin === null ? theirs.some((e) => e.id === idOf(d)) : ids.get(d.origin) === null);
    if (clash) {
      state.message = `Can't keep your edits automatically: the new version also has a test named ${quoted(clash.raw?.id ?? clash.origin)}. Compare, or use the new version.`;
      refresh(); return;
    }
    if (state.toolsDraft !== undefined) {
      const previous = state.base.tools ?? [], removed = previous.filter(name => !state.toolsDraft.includes(name));
      state.toolsDraft = [...new Set([...(state.candidate.tools ?? []).filter(name => !removed.includes(name)), ...state.toolsDraft.filter(name => !previous.includes(name))])];
    }
    // Keeping a flip also keeps its declaration if it was removed outside.
    if (state.settingsDraft.size) state.toolsDraft = [...new Set([...(state.toolsDraft ?? state.candidate.tools ?? []), ...state.settingsDraft.keys()])];
    history.remember(state.candidate);
    state.base = state.candidate; state.snap = next; state.candidate = undefined; state.compareOpen = false; state.lastRun = undefined;
    pruneDrafts(); state.message = "Kept your edits on top of the new version. Enter saves them."; render();
  }

  return { observation, matches, observe, observeFailure, checkNow, useTheirs, keepMine };
}
