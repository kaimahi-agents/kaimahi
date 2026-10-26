import { readSnapshot, portableId, effectiveModel, preRouteRefusal } from "./authoring.mjs";
import { same, uniqueIndex, quoted } from "./editor.mjs";

export function createObservationController(state, { path, options, drafts, clearUndo, render, refresh }) {
  const { sectionDrafts, testDrafts, invalidEditors, displayNames, history, modelChoice } = state;
  const { hasDrafts, pruneDrafts } = drafts;
  const observation = () => ({ generation: state.observationGeneration, revision: state.base.file.revision });
  const matches = (view) => view && !view.error && view.file?.path === path;

  // Observation never writes or clears a lock.
  function observe(view, started = observation()) {
    if (started.generation !== state.observationGeneration || started.revision !== state.base.file.revision
      || !matches(view) || state.busy || state.blocked || invalidEditors.size) return false;
    const observed = view;
    const selected = effectiveModel(view.model, modelChoice.override, view.chatModel);
    const route = view.latest.result?.modelRoute, incomingModel = route?.slice(route.indexOf("/") + 1);
    const adoptable = view.file.revision === state.base.file.revision || !hasDrafts();
    const stopped = history.attempt(incomingModel)?.run?.revision === view.file.revision;
    const previousHistory = [history.pair(), history.attempt(incomingModel)];
    if (adoptable) { history.focus(selected); if (!stopped) history.remember(observed); }
    const changedHistory = adoptable && !same(previousHistory, [history.pair(), history.attempt(incomingModel)]);
    view = modelChoice.view(view, view.latest.status !== "current" || preRouteRefusal(view.latest.result) || incomingModel === selected ? undefined : history.selected(view, selected));
    if (!changedHistory && (same(view, state.base) || (state.candidate && same(view, state.candidate)))) return false;
    clearUndo(); displayNames.clear(); state.runAlert = undefined;
    state.observationGeneration++;
    state.lastRun = undefined;
    if (view.file.revision === state.base.file.revision || !hasDrafts()) {
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
    try { observe(await options.fetchView(), started); } catch { /* Polling is best effort and never changes state on failure. */ }
  }
  function useTheirs() {
    if (!state.candidate) return;
    state.invalidCheck = undefined; clearUndo(); displayNames.clear();
    state.observationGeneration++;
    sectionDrafts.clear(); testDrafts.clear(); invalidEditors.clear(); state.toolsDraft = undefined;
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
    history.remember(state.candidate);
    state.base = state.candidate; state.snap = next; state.candidate = undefined; state.compareOpen = false; state.lastRun = undefined;
    pruneDrafts(); state.message = "Kept your edits on top of the new version. Enter saves them."; render();
  }

  return { observation, matches, observe, checkNow, useTheirs, keepMine };
}
