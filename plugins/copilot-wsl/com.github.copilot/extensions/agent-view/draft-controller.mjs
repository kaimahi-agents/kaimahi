import { readSnapshot, effectiveModel } from "./authoring.mjs";
import { uniqueIndex, isRefused, clone, same, friendlyName, checkIssue } from "./editor.mjs";

import { defaultToolMode, setToolMode } from "./test-tools.mjs";

export function createDraftController(state, { clearUndo, navigate, render, refresh }) {
  const { sectionDrafts, testDrafts, invalidEditors, history, editingChecks, displayNames, modelChoice } = state;
  state.settingsDraft ??= new Map();
  const hasDrafts = () => state.toolsDraft !== undefined || state.settingsDraft.size > 0 || sectionDrafts.size > 0 || testDrafts.size > 0 || invalidEditors.size > 0;
  const toolNames = () => state.toolsDraft ?? state.base.tools ?? [];
  function noteTools(names) {
    clearUndo();
    state.toolsDraft = same([...names].sort(), [...(state.base.tools ?? [])].sort()) ? undefined : [...names];
    pruneSettings();
  }
  const toolState = name => state.settingsDraft.get(name) ?? state.base.toolSettings?.find(s => s.name === name)?.state ?? "Enabled";
  function pruneSettings() {
    for (const [name, value] of state.settingsDraft) if (!toolNames().includes(name)
      || (state.base.tools ?? []).includes(name) && value === state.base.toolSettings?.find(s => s.name === name)?.state) state.settingsDraft.delete(name);
  }
  function noteSetting(name, value) {
    clearUndo(); state.settingsDraft.set(name, value); pruneSettings();
  }
  const rawExamples = () => state.snap ? state.base.authoring.examples : [];
  const sectionText = (key) => sectionDrafts.get(key) ?? state.snap.sections.find((s) => s.key === key).text;
  // Ambiguous IDs stay visible but cannot be edited.
  function entries() {
    const cases = history.cases(state.base, effectiveModel(state.base.model, modelChoice.override, state.base.chatModel)), raws = uniqueIndex(rawExamples(), (e) => e.id), ids = uniqueIndex(state.base.examples, (e) => e.id);
    const result = !state.blocked && state.base.latest.status === "current" ? state.base.latest.result : undefined;
    const list = cases.map((c) => {
      if (isRefused(result) && result.selection.caseIds.includes(c.example.id)) c = { ...c, state: "refused", label: "Refused", row: result, previous: false };
      const raw = state.snap && ids.get(c.example.id) === c.example ? raws.get(c.example.id) : undefined;
      const key = raw ? `id:${raw.id}` : undefined, draft = key ? testDrafts.get(key) : undefined;
      return { key, c, raw: draft ? draft.raw : raw, origin: raw, draft, openable: !!raw || !state.snap && ids.get(c.example.id) === c.example };
    });
    for (const [key, draft] of testDrafts) if (draft.origin === null) {
      const raw = !draft.raw.mocks && state.base.authoring?.recordingControls ? toolNames().reduce((r, name) => setToolMode(r, name, defaultToolMode(r.assertions)), draft.raw) : draft.raw;
      list.push({ key, raw, origin: undefined, draft, openable: true });
    }
    return list;
  }
  const localExamples = () => entries().filter((e) => e.raw && !e.draft?.deleted).map((e) => clone(e.raw));
  const draftName = (e) => e.draft?.origin === null ? (e.draft.name.trim() || "New test") : displayNames.get(e.raw?.id ?? e.c.example.id)?.name ?? friendlyName(e.raw?.id ?? e.c.example.id);

  function checkProblem(key, raw) {
    const a = raw.assertions.find(a => checkIssue(a));
    if (!a) return false;
    navigate({ name: "detail", key }); state.invalidCheck = { id: a.id, issue: checkIssue(a) };
    editingChecks.add(`${key}/${a.id}`); state.focusIssue = true; render(); return true;
  }
  function noteDraft(key, draft) {
    clearUndo(); state.invalidCheck = undefined;
    const origin = draft.origin && rawExamples().find((e) => `id:${e.id}` === key);
    if (draft.origin && origin && same(origin, draft.raw)) testDrafts.delete(key); else testDrafts.set(key, draft);
  }
  function pruneDrafts() {
    if (!state.snap) { sectionDrafts.clear(); testDrafts.clear(); state.toolsDraft = undefined; state.settingsDraft.clear(); return; }
    pruneSettings();
    if (state.toolsDraft !== undefined && same([...state.toolsDraft].sort(), [...(state.base.tools ?? [])].sort())) state.toolsDraft = undefined;
    for (const [key, text] of sectionDrafts) if (state.snap.sections.find((s) => s.key === key)?.text === text) sectionDrafts.delete(key);
    const raws = uniqueIndex(rawExamples(), (e) => e.id);
    for (const [id, record] of displayNames) if (!same(raws.get(id), record.raw)) displayNames.delete(id);
    for (const [key, draft] of testDrafts) {
      if (draft.origin === null) continue;
      const raw = raws.get(draft.origin);
      if (raw === undefined) {
        // Keep a remotely removed edited test as a new draft.
        testDrafts.delete(key);
        if (!draft.deleted) {
          const next = `new:${++state.newCount}`;
          testDrafts.set(next, { origin: null, fixedId: true, name: draft.raw.id, raw: draft.raw });
          if (state.route.key === key) state.route = { name: "detail", key: next };
        }
      } else if (raw && !draft.deleted && same(raw, draft.raw)) testDrafts.delete(key);
    }
    if (state.route.name === "detail" && !entries().some((e) => e.key === state.route.key)) state.route = { name: "main" };
  }
  function adopt(view, submitted) {
    state.invalidCheck = undefined; state.observationGeneration++;
    for (const key of submitted.tests) {
      const draft = testDrafts.get(key);
      if (draft?.origin === null && !draft.deleted) displayNames.set(draft.raw.id, { name: draft.name.trim(), raw: clone(draft.raw) });
    }
    const renamed = state.route.name === "detail" && submitted.tests.includes(state.route.key) ? testDrafts.get(state.route.key)?.raw?.id : undefined;
    history.remember(view); editingChecks.clear();
    state.base = modelChoice.view(view); state.snap = readSnapshot(state.base); state.candidate = undefined; state.compareOpen = false;
    if (submitted.tools) state.toolsDraft = undefined;
    if (submitted.settings) state.settingsDraft.clear();
    for (const key of submitted.sections) sectionDrafts.delete(key);
    for (const key of submitted.tests) testDrafts.delete(key);
    if (renamed) state.route = { name: "detail", key: `id:${renamed}` };
    pruneDrafts();
  }

  function addTest() {
    if (!state.snap) return;
    clearUndo();
    if (localExamples().length >= 128) { state.message = "Keep at most 128 tests. Edit or delete an existing test first."; refresh(); return; }
    const key = `new:${++state.newCount}`;
    testDrafts.set(key, { origin: null, name: "", raw: { id: "", input: "", assertions: [{ id: "check-1", type: "contains", value: "" }] } });
    navigate({ name: "detail", key }); state.focusTestName = true; render();
  }

  return { hasDrafts, toolNames, toolState, noteTools, noteSetting, rawExamples, sectionText, entries, localExamples, draftName, checkProblem, noteDraft, pruneDrafts, adopt, addTest };
}
