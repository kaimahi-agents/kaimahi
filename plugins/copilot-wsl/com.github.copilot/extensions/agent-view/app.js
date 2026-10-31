// Resource and authoring values are untrusted: render only inert DOM text.
import { renderTestTools, setToolMode, withToolModes, toolUseText, duplicateTest } from "./test-tools.mjs";
import { sourceIdentity, runtimeModelSelection, effectiveModel, readSnapshot, portableId, compareDrafts, same } from "./authoring.mjs";
import { el, inlineText, editorValue, instructionsContent, runtimeModelControl, testNameControl, icon, statusIcon,
  inspectCases, compareLabels, checkKey, uniqueIndex, clone, operandKey, EDITABLE_TYPES, quoted, checkLabel, oneLine, failureFacts, testRow, coherentSelection, ambiguousPassSet, countText, plural, resultHistory, resultPill, modelLabel, growTextarea, checkTypeControl, checkFields, convertCheck, inconclusiveJudge, judgeDetails, plainReason, isRefused, matchedAnswer, answerBlock, refusalContext, canvasHeader, instructionEditor, conflictNotice, saveButton, preservedSettings, copilotTestsNotice } from "./editor.mjs";
import { createDraftController } from "./draft-controller.mjs";
import { createActionController } from "./action-controller.mjs";
import { createObservationController } from "./observation-controller.mjs";
import { toolsSection, serverToolsView } from "./tools-view.mjs";
import { createRunViews, ranWith, settingsGuard } from "./run-views.mjs";
export { provenActual } from "./editor.mjs";

async function postAction(action) {
  const response = await fetch("actions", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(action),
  });
  return response.json();
}
async function fetchView() {
  const res = await fetch("view.json", { cache: "no-store", credentials: "omit" });
  return res.json();
}
const NO_HANDLE = { observe: () => false, observeFailure: () => false, idle: () => false, dispose() {} };

export function renderView(doc, root, payload, execute = postAction, options = {}) {
  if (!payload || payload.error || !payload.file) {
    root.replaceChildren(el(doc, "p", "error", payload?.message || "The agent view could not be loaded."));
    return NO_HANDLE;
  }
  const { suggestedTests: initialSuggestions, ...savedPayload } = payload;
  payload = savedPayload;
  // Navigation neither saves nor drops these controller-owned drafts.
  const state = {
    base: payload, snap: readSnapshot(payload), candidate: undefined, compareOpen: false,
    sourceUnavailable: false, sourceInvalidated: false,
    sectionDrafts: new Map(), testDrafts: new Map(), invalidEditors: new Set(),
    route: { name: "main" }, busy: false, pending: undefined, blocked: false, savedUntested: false, settingsIncomplete: false, message: "", runAlert: undefined, lastRun: undefined,
    newCount: 0, history: settingsGuard(resultHistory(payload)), editingChecks: new Set(),
    focusTestName: false, nameHintKey: undefined, promptUndo: undefined, undoTimer: undefined, hostedSent: false,
    displayNames: new Map(),
    // Revisions alone cannot fence reruns of unchanged source.
    observationGeneration: 0, modelChoice: runtimeModelSelection(),
    invalidCheck: undefined, focusIssue: undefined,
  };
  const { sectionDrafts, testDrafts, invalidEditors, history, editingChecks, modelChoice } = state;
  let filter = "all", menuOpen = false, cleanups = [], controls = [];
  let toolServers = [], toolsLoading = false, toolsError = "", toolsMenu = false, toolsLoaded = false, toolsFor, disposed = false;
  let refresh = () => {}, activeTestName, issueInput;
  state.base = modelChoice.view(state.base);
  history.focus(effectiveModel(state.base.model, modelChoice.override, state.base.chatModel));
  let actions;
  const drafts = createDraftController(state, { clearUndo: () => actions.clearUndo(), navigate, render, refresh: () => refresh() });
  const { hasDrafts, toolNames, toolState, noteTools, noteSetting, sectionText, entries, localExamples, draftName, noteDraft, addTest } = drafts;
  const { observation, matches, observe, observeFailure, checkNow, useTheirs, keepMine } = createObservationController(state,
    { path: payload.file.path, options: { ...options, onSuggestions: receiveSuggestions }, drafts, clearUndo: () => actions.clearUndo(), render, refresh: () => refresh() });
  actions = createActionController(state, { execute, fetchProgress: options.fetchProgress, drafts, matches, checkNow, observeFailure, render, refresh: () => refresh(), focusName: () => activeTestName?.focus() });
  const { act, submitAll, submitOne, testing, clearUndo } = actions;
  const { selectedModel, runFresh, currentRun, runSummary, runRow, resultSummary, total, footText, hostedNotice, refusedAlert, runAlertNode, sourceActions, explanation } = createRunViews(state, { act, actions, doc, entries, hasDrafts, history, invalidEditors, localExamples, modelChoice, render, sectionDrafts, sectionText, submitAll, submitOne, testDrafts, testing });

  const importedBatches = new Set(), acknowledging = new Set();
  function acknowledgeSuggestions(id) {
    if (acknowledging.has(id)) return;
    acknowledging.add(id);
    // Receipt-only transport must not disturb Save/Test busy state or revisions.
    Promise.resolve().then(() => execute({ operation: "acknowledge-suggestions", batchId: id }))
      .catch(() => {}).finally(() => acknowledging.delete(id));
  }
  function receiveSuggestions(batch, view) {
    if (!batch || typeof batch.id !== "string" || disposed || state.confirm) return false;
    if (importedBatches.has(batch.id)) { acknowledgeSuggestions(batch.id); return false; }
    if (state.busy || state.blocked || invalidEditors.size) return false;
    if (batch.stale === true) {
      state.message = "The saved sources changed. Suggest tests again to review new drafts.";
    } else {
      if (!state.snap || !view.authoring?.editable || state.candidate || sourceIdentity(view) !== sourceIdentity(state.base)
        || batch.sourceIdentity !== sourceIdentity(state.base)) return false;
      try { drafts.importSuggestions(batch.examples); }
      catch (error) { state.message = error.message; refresh(); return false; }
      state.message = "Suggested tests are unsaved. Review, edit or cancel them before saving.";
    }
    importedBatches.add(batch.id);
    acknowledgeSuggestions(batch.id);
    return true;
  }

  function navigate(next) { state.route = next; state.invalidCheck = undefined; state.confirm = undefined; if (!state.blocked && !state.savedUntested) { state.message = ""; state.runAlert = undefined; } }

  // DOM
  function render() {
    const active = state.route.name === "tools" && root.contains(doc.activeElement) ? doc.activeElement : undefined;
    const focusedTool = active?.getAttribute?.("data-tool-name"), focusedState = active?.getAttribute?.("data-tool-state");
    for (const cleanup of cleanups.splice(0)) cleanup();
    invalidEditors.clear();
    const content = el(doc, "div", state.route.name === "detail" ? "content detail-view" : "content");
    controls = []; activeTestName = undefined; issueInput = undefined;
    const button = (label, action, className = "", guard = "idle") => {
      const node = el(doc, "button", className, label); node.type = "button";
      node.addEventListener("click", action); controls.push({ node, guard }); return node;
    };
    const status = el(doc, "span", "meta prompt-status"); status.setAttribute("role", "status");
    const messageNode = el(doc, "p", "action-message"); messageNode.setAttribute("role", "status"); messageNode.setAttribute("aria-live", "polite");
    const updates = [];
    refresh = () => {
      root.setAttribute("aria-busy", String(state.busy));
      messageNode.textContent = state.message;
      for (const u of updates) u();
      for (const { node, guard } of controls) {
        node.disabled = guard === "stop" ? !testing() || !state.run?.cancellable || state.run.state !== "running" : guard === "reload" ? state.busy || state.sourceUnavailable : invalidEditors.size > 0 || (guard === "nav" ? false
          : state.busy || state.blocked || (["save", "settings"].includes(guard) && !!state.candidate) || (guard === "clean" && hasDrafts()));
      }
      options.onView?.(state.base, !!state.snap && !state.busy && !state.blocked && !state.candidate && invalidEditors.size === 0);
    };
    const declaredTools = JSON.stringify(state.base.tools ?? []);
    if (declaredTools !== toolsFor) { toolsFor = declaredTools; toolsLoaded = false; toolsError = ""; }
    const detail = state.route.name === "detail", tools = state.route.name === "tools";
    const parts = tools ? toolPage(button, updates) : detail ? detailView(button, status, updates) : mainView(button, status, updates);
    if (tools) content.addEventListener("keydown", event => {
      if (event.defaultPrevented || event.isComposing || event.shiftKey || event.ctrlKey || event.altKey || event.metaKey) return;
      if (event.key === "Enter" && event.target?.tagName !== "BUTTON" && !event.target?.closest?.("button")) { event.preventDefault(); submitAll(); }
      else if (event.key === "Escape" && !state.busy && !state.blocked && !state.candidate) { event.preventDefault(); state.toolsDraft = undefined; state.settingsDraft.clear(); render(); }
    });
    const line = el(doc, "div", "message-line");
    line.append(messageNode);
    if (state.snap) {
      const reloadNow = button("Reload and discard draft", () => act("reload"), "tb", "reload");
      updates.push(() => { reloadNow.hidden = !state.blocked; });
      line.append(reloadNow);
    }
    // Keep Suggest feedback with Tests.
    const tests = parts.find(part => part.className === "tests-section");
    if (tests && state.message === "Asked Copilot to add tests in chat.") tests.append(line);
    else parts.splice(detail || tools ? 1 : Math.min(2, parts.length), 0, line);
    if (state.confirm) {
      const notice = copilotTestsNotice(doc, button, () => { const go = state.confirm.go; state.confirm = undefined; go(); }, () => { state.confirm = undefined; render(); });
      parts.splice(1, 0, notice.node); state.confirm.focus = notice.focus;
    }
    if (state.candidate) parts.splice(detail ? 1 : 1, 0, conflictNotice(doc, button, compareDrafts(state.base, state.candidate, state.snap, sectionDrafts, testDrafts, state.toolsDraft, state.settingsDraft), state.compareOpen,
      () => { state.compareOpen = !state.compareOpen; render(); }, keepMine, useTheirs));
    content.append(...parts);
    root.replaceChildren(content);
    refresh();
    if (state.focusTestName) { activeTestName?.focus(); state.focusTestName = false; }
    if (state.focusIssue) { issueInput?.focus(); state.focusIssue = false; }
    if (state.confirm?.focus) { state.confirm.focus(); state.confirm.focus = undefined; }
    if (focusedTool || focusedState) {
      const replacement = Array.from(root.querySelectorAll(focusedState ? ".tool-state" : ".tool-choice")).find(node => node.getAttribute(focusedState ? "data-tool-state" : "data-tool-name") === (focusedState ?? focusedTool));
      (replacement && !replacement.disabled ? replacement : root.querySelector(".tool-filter"))?.focus();
    }
    if ((state.base.tools ?? []).length && !toolsLoaded && !toolsLoading && !toolsError) loadTools();
  }

  function header(updates) {
    const pill = resultPill(doc, resultSummary), actions = [pill.node]; updates.push(pill.update);
    if (state.snap && options.modelSelection !== false) {
      updates.push(modelControl.update); controls.push({ node: modelControl.trigger, guard: "idle" }); actions.push(modelControl.node);
    }
    return canvasHeader(doc, state.base, actions, !state.snap);
  }

  function makeEditor(section, label, updates) {
    const control = instructionEditor(doc, label, sectionText(section.key), {
      disabled: () => state.blocked || state.busy && !(options.interruptOnEdit && testing() && state.run?.cancellable && ["running", "stopping"].includes(state.run.state)),
      hasDraft: () => sectionDrafts.has(section.key),
      change(node) {
        clearUndo();
        if (options.interruptOnEdit && testing()) void actions.stop();
        try {
          const value = editorValue(node); invalidEditors.delete(section.key);
          if (value === section.text && !testing()) sectionDrafts.delete(section.key); else sectionDrafts.set(section.key, value);
        } catch {
          invalidEditors.add(section.key);
          state.message = "Unsupported editor formatting. Your draft is kept; reload before saving.";
        }
        refresh();
      },
      submit: () => state.route.name === "detail" ? submitOne(state.route.key) : submitAll(),
      cancel() { sectionDrafts.delete(section.key); invalidEditors.delete(section.key); render(); },
    });
    updates.push(control.update);
    return control.node;
  }

  function spinnerStatus(status, scope, updates) {
    const spin = el(doc, "span", "busy-spinner"); spin.append(icon(doc, "running", 12, 2.6, "spin"));
    const box = el(doc, "span", "status-line"), undo = state.snap ? el(doc, "button", "tb prompt-undo", "Undo") : undefined;
    if (undo) { undo.type = "button"; undo.addEventListener("click", () => {
      const checkpoint = state.promptUndo;
      if (!checkpoint || checkpoint.revision !== sourceIdentity(state.base) || sectionDrafts.size || state.busy || state.blocked || state.candidate) return;
      act("save-prompt", { sections: checkpoint.sections });
    });
    controls.push({ node: undo, guard: "save" }); }
    box.append(spin, status, ...(undo ? [undo] : []));
    updates.push(() => { status.textContent = footText(scope); spin.hidden = !state.busy; if (undo) undo.hidden = !state.promptUndo; });
    return box;
  }

  function mainView(button, status, updates) {
    const { head, info } = header(updates);
    const promptSection = el(doc, "section", "prompt-section"), promptBox = el(doc, "div", "prompt-box main-prompt");
    const promptLabel = el(doc, "h2", "lbl", "Prompt");
    const foot = el(doc, "div", "prompt-foot");
    if (state.snap) {
      promptBox.append(makeEditor(state.snap.main, "Prompt", updates));
      const additional = el(doc, "details", "additional");
      additional.append(el(doc, "summary", "", "Additional instructions"));
      for (const section of state.snap.sections.filter((s) => s !== state.snap.main)) additional.append(el(doc, "h3", "", section.key), makeEditor(section, section.key, updates));
      if ([...sectionDrafts.keys()].some((k) => k !== state.snap.main.key)) additional.open = true;
      // A free-form Copilot prompt has no further sections.
      if (state.snap.sections.length > 1) info.append(additional);
    } else promptBox.append(instructionsContent(doc, state.base));
    foot.append(spinnerStatus(status, "all", updates));
    const saveRun = state.snap ? saveButton(doc, button, "Save and run all tests", submitAll) : undefined;
    if (saveRun) { foot.append(saveRun); updates.push(() => { saveRun.hidden = testing() && !!state.run; }); }
    if (options.interruptOnEdit) {
      const stop = button("Stop tests", () => actions.stop(), "sb", "stop");
      updates.push(() => { stop.hidden = !testing() || !state.run?.cancellable; });
      foot.append(stop);
    }
    if (state.snap) foot.append(hostedNotice(updates));
    promptBox.append(foot); promptSection.append(promptLabel, promptBox);

    sourceActions(button, info, updates);
    const tests = testsSection(button, updates);
    const tools = toolsSection(doc, button, { servers: toolServers, names: state.base.tools ?? [], settings: state.base.settingsInvalid ? null : state.base.toolSettings, editable: !!state.snap && options.toolsSelection !== false,
      omitted: state.base.copilotToolsOmitted === true && !(state.base.tools ?? []).length,
      loading: toolsLoading, loaded: toolsLoaded, error: toolsError, menuOpen: toolsMenu, onToggleMenu: () => {
        toolsMenu = !toolsMenu;
        if (toolsMenu) loadTools(); else render();
      }, onOpen: openTools });
    const out = [head, promptSection, ...(state.base.frontmatter ? [preservedSettings(doc, state.base.frontmatter)] : []), tools, tests];
    if (state.snap) out.push(pinnedBar(button, foot, tests, updates));
    return out;
  }

  async function loadTools() {
    if (toolsLoading) return;
    toolsLoading = true; toolsError = ""; render();
    try {
      const response = await (options.fetchTools ? options.fetchTools() : execute({ operation: "tools" }));
      if (disposed) return;
      if (response?.error || !Array.isArray(response?.servers)) throw new Error();
      toolServers = response.servers; toolsLoaded = true;
    } catch { toolsError = "Tools are unavailable in this session."; }
    finally { toolsLoading = false; if (!disposed) render(); }
  }
  function openTools(server) {
    const selected = toolNames(); toolsMenu = false;
    navigate({ name: "tools", server, filter: "", rows: [...server.tools].sort((a, b) => Number(selected.includes(b.name)) - Number(selected.includes(a.name))) });
    render();
  }
  function toolPage(button, updates) {
    const route = state.route, status = el(doc, "span", "meta prompt-status");
    updates.push(() => { status.textContent = !state.snap || state.busy ? footText("all") : !total()
      ? hasDrafts() ? "Edited · Enter saves" : "Saved · add a test to run it"
      : `${hasDrafts() ? "Edited · Enter saves and runs" : "Saved · Enter runs"} ${plural(total(), "test")}`; });
    const parts = serverToolsView(doc, button, { agentName: state.base.title.split(" — ")[0], server: route.server, servers: toolServers, rows: route.rows, names: toolNames(), settings: state.base.settingsInvalid ? null : toolNames().map(name => ({ name, state: toolState(name) })), filter: route.filter, editable: !!state.snap,
      settingsEditable: !!state.snap && (state.base.toolSettingsRevision !== undefined || !(state.base.tools ?? []).length),
      onFilter: value => { route.filter = value; }, busy: !state.snap || state.busy || state.blocked || !!state.candidate,
      onBack: () => { navigate({ name: "main" }); render(); },
      onToggle: (name, checked) => {
        if (!state.snap || state.busy || state.blocked || state.candidate) return;
        const current = toolNames();
        if (checked && current.length >= 32 && !current.includes(name)) { state.message = "Keep at most 32 tools for this agent."; render(); return; }
        noteTools(checked ? [...new Set([...current, name])] : current.filter(value => value !== name)); render();
      }, onStateToggle: name => {
        if (!state.snap || state.busy || state.blocked || state.candidate || !toolNames().includes(name)) return;
        noteSetting(name, toolState(name) === "Enabled" ? "Disabled" : "Enabled"); render();
      }, onDiscard: () => { if (state.busy || state.blocked || state.candidate) return; state.toolsDraft = undefined; state.settingsDraft.clear(); state.message = ""; render(); }, onSubmit: submitAll,
      dirty: state.toolsDraft !== undefined || state.settingsDraft.size > 0, footer: status });
    for (const node of parts.flatMap(part => Array.from(part.querySelectorAll("input")))) updates.push(() => {
      const name = node.getAttribute("data-tool-name");
      node.disabled = !state.snap || state.busy || state.blocked || !!state.candidate || (name !== null && !node.checked && !route.rows.find(tool => tool.name === name)?.selectable);
    });
    return parts;
  }

  function testsSection(button, updates) {
    const section = el(doc, "section", "tests-section"), head = el(doc, "div", "tests-head"), title = el(doc, "div", "tests-title");
    const summaryNode = el(doc, "span", "sr-only tests-summary"), meta = el(doc, "span", "meta tests-meta");
    const list = el(doc, "ul", "test-list"), note = el(doc, "p", "filter-note");
    title.append(el(doc, "h2", "lbl", "Tests"));
    const group = el(doc, "div", "filter-group"); group.setAttribute("role", "group"); group.setAttribute("aria-label", "Show tests");
    const filterButtons = ["all", "problems"].map((value) => {
      const node = el(doc, "button", "filter-button"); node.type = "button";
      node.addEventListener("click", () => { filter = value; draw(); refresh(); });
      group.append(node); return node;
    });
    summaryNode.setAttribute("role", "status");
    const firstRun = state.snap ? button("", submitAll, "tb first-run", "save") : undefined;
    title.append(group, meta, summaryNode, ...(firstRun ? [firstRun] : [])); head.append(title);
    if (state.snap) {
      const actions = el(doc, "div", "tests-actions"), add = button("", addTest, "tb", "nav");
      const plus = el(doc, "span", "plus"); plus.append(icon(doc, "plus", 12, 2.6));
      add.append(plus, "Add test");
      actions.append(button("Suggest tests", () => act("suggest"), "tb tb-quiet"), add);
      head.append(actions);
    }
    section.append(head);
    if (state.base.settingsInvalid) section.append(el(doc, "p", "meta tool-settings-error", plainReason({ reasonCode: "invalid_tool_settings" })));
    if (state.runAlert && (!state.runAlert.one || state.runAlert.refused)) section.append(runAlertNode(button, "all"));
    else if (!state.runAlert && refusedAlert()) section.append(runAlertNode(button, "all", refusedAlert()));
    const empty = el(doc, "div", "empty-tests");
    if (state.snap) {
      const add = button("", addTest, "tb", "nav"); const plus = el(doc, "span", "plus"); plus.append(icon(doc, "plus", 12, 2.6));
      add.append(plus, "Add a test yourself");
      const text = el(doc, "div", "empty-text");
      text.append(el(doc, "span", "", "No tests yet"),
        el(doc, "span", "quiet", "A test gives the agent an input and checks its answer. After each change, Enter saves the prompt and runs every test."));
      const actions = el(doc, "div", "empty-actions");
      actions.append(button("Suggest tests", () => act("suggest"), "sb sb-accent"), add);
      empty.append(text, actions, el(doc, "span", "meta", "Ask Copilot to add tests to the saved agent."));
    }
    section.append(empty, list, note);
    const rowControls = [];
    const draw = () => {
      for (const node of rowControls.splice(0)) if (controls.includes(node)) controls.splice(controls.indexOf(node), 1);
      const all = entries(), anyDraft = hasDrafts(), oldResults = anyDraft || testing() || state.base.latest.status !== "current";
      const shown = all.map((e) => ({ e, r: e.draft?.origin === null ? { kind: "none", base: "none", label: "Not run", note: "", count: "" }
        : state.blocked ? testRow(e.c, oldResults, true) : runRow(e.raw?.id ?? e.c?.example.id, testRow(e.c, oldResults || e.c?.previous, false, ambiguousPassSet(state.base))) }));
      const counted = shown.filter(({ e }) => !e.draft?.deleted);
      const currentPass = ({ e, r }) => r.base === "pass" && !e.c?.previous;
      const notPassing = counted.filter(row => !currentPass(row)).length;
      filterButtons.forEach((node, i) => {
        node.replaceChildren(`${i ? "Not passing" : "All"} `, el(doc, "span", "count", String(i ? notPassing : counted.length)));
        node.setAttribute("aria-pressed", String(filter === (i ? "problems" : "all")));
      });
      list.replaceChildren(); note.replaceChildren();
      let hidden = 0;
      for (const { e, r } of shown) {
        const item = el(doc, "li", "test-item"), id = e.raw?.id ?? e.c?.example.id ?? "";
        item.setAttribute("data-test-id", id); item.setAttribute("data-status", r.kind);
        item.hidden = filter === "problems" && currentPass({ e, r }) && !e.draft; hidden += Number(item.hidden);
        if (e.draft?.deleted) {
          item.className += " deleted";
          const row = el(doc, "div", "test-row");
          row.append(statusIcon(doc, r.kind, r.label), el(doc, "span", "test-name", draftName(e)), el(doc, "span", "test-note", "Deleted · unsaved"));
          const restore = button("Restore", () => { testDrafts.delete(e.key); render(); }, "tb", "nav");
          rowControls.push(controls[controls.length - 1]);
          row.append(restore); item.append(row); list.append(item); continue;
        }
        const open = e.openable ? button("", () => { navigate({ name: "detail", key: e.key ?? `view:${id}` }); menuOpen = false; render(); }, "test-row", "nav") : el(doc, "div", "test-row");
        if (e.openable) rowControls.push(controls[controls.length - 1]);
        const rowNote = currentRun() ? r.note : e.c?.state === "refused" ? "" : e.c?.previous && state.base.latest.status === "current" ? "Not in this run" : r.note;
        const noteText = e.draft ? "Unsaved" + (rowNote ? ` · ${rowNote}` : "") : rowNote;
        const example = e.c?.example, summary = example ? `${oneLine(example.input, 64)} → ${example.assertions.map(checkLabel).join(" · ")}` : "";
        const word = !state.blocked && e.c?.state === "refused" ? "Refused" : r.base === "pass" ? "Passed" : r.base === "fail" ? "Failed" : r.base === "error" ? "Didn't finish" : r.label === "No current result" || r.label === "Out of date" && history.last ? "Not run yet" : r.label;
        const verdict = el(doc, "span", `test-verdict ${r.kind}`); verdict.setAttribute("data-tone", r.tone ?? "none");
        const pair = history.pair(), labels = compareLabels(pair.map(v => v.latest.result.modelRoute));
        const selectedInPair = pair.some(v => v.latest.result.modelRoute.slice(v.latest.result.modelRoute.indexOf("/") + 1) === selectedModel());
        if (pair.length === 2 && !state.blocked && (selectedInPair || !testing() && !currentRun() && state.base.latest.status !== "current")) {
          verdict.className += " verdict-pair";
          pair.forEach((view, index) => {
            const route = view.latest.result.modelRoute, selected = route.slice(route.indexOf("/") + 1) === selectedModel();
            const attempt = history.attempt(route.slice(route.indexOf("/") + 1)), latestView = !attempt?.run && attempt?.view || view;
            const c = inspectCases(latestView).find(c => c.example.id === id);
            const definition = latestView.authoring?.examples.find(c => c.id === id), current = state.base.authoring?.examples.find(c => c.id === id);
            const old = !selected || testing() || hasDrafts() || sourceIdentity(view) !== sourceIdentity(state.base) || !ranWith(state.base, view.latest.result);
            const prior = definition && same(definition, current) ? testRow(c, old, false) : { kind: "none", label: "Not run yet" };
            const foreign = selected && e.c?.previous && e.c.previousModel !== route && !currentRun();
            const own = selected && (currentRun() || state.base.latest.result?.modelRoute === route);
            const slot = own ? foreign ? { kind: "none", label: "Not run" } : r : attempt?.run ? runRow(id, prior, attempt.run, !selected) : prior;
            const text = own && !foreign ? word : slot.label.replace(/ last run$/, "");
            if (index) verdict.append(" · ");
            const phrase = el(doc, "span", `verdict-slot ${slot.kind}`, `${labels[index]} ${text}`);
            phrase.setAttribute("title", modelLabel(route)); phrase.setAttribute("aria-label", `${modelLabel(route)}: ${text}`);
            verdict.append(phrase);
          });
        } else {
          if (r.kind === "running") verdict.append(statusIcon(doc, "running", "Running"));
          verdict.append(el(doc, "span", "verdict-word", word));
          if (r.kind.startsWith("old-")) verdict.append(el(doc, "span", "sr-only verdict-context", " last run"));
        }
        open.append(el(doc, "span", "test-name", draftName(e)), el(doc, "span", "test-input-summary", summary), verdict);
        const line = el(doc, "span", e.draft ? "test-note unsaved" : "test-note", noteText); line.hidden = !noteText;
        open.append(line);
        const toolText = toolUseText((e.c?.row?.tools ?? []).filter(t=>t.callCount>0||t.requestCount>0));
        if (toolText) { const partner = pair.find(v => !v.latest.result.modelRoute.endsWith("/" + selectedModel())); const other = partner?.latest.result.examples.find(x=>x.id===id)?.tools; const different = other && toolUseText(other) !== toolText ? ` · ${modelLabel(partner.latest.result.modelRoute)}: ${toolUseText(other) || "used none"}` : ""; open.append(el(doc,"span","test-note",toolText + different)); }
        item.append(open); list.append(item);
      }
      if (hidden) {
        const show = el(doc, "button", "tb", "Show all"); show.type = "button";
        show.addEventListener("click", () => { filter = "all"; draw(); refresh(); });
        note.append(`${plural(hidden, "passing test")} hidden `, show);
      }
      empty.hidden = !state.snap || shown.length > 0;
      list.hidden = shown.length === 0;
      const text = countText(counted.map(({ e, r }) => state.base.latest.status === "current" && e.c?.previous ? { count: "not run" } : r));
      const malformed = !!state.base.latest.result && !coherentSelection(state.base);
      const stale = state.base.latest.status === "stale" || (anyDraft || state.base.latest.status !== "current") && !!history.last;
      const previous = modelLabel(history.last?.latest.result.modelRoute);
      const run = currentRun(), pair = history.pair();
      const selected = pair.find(v => v.latest.result.modelRoute.slice(v.latest.result.modelRoute.indexOf("/") + 1) === selectedModel());
      const partner = selected && pair.find(v => v !== selected);
      const elapsed = history.elapsed(state.base), duration = Number.isFinite(elapsed) ? ` · ${Math.round(elapsed / 1000)} s` : "";
      const partnerAttempt = partner && history.attempt(partner.latest.result.modelRoute.slice(partner.latest.result.modelRoute.indexOf("/") + 1));
      const partnerRun = partnerAttempt?.run, partnerView = !partnerRun && partnerAttempt?.view || partner;
      const partnerResult = partnerView?.latest.result;
      const partnerSummary = !partner ? "" : partnerRun ? `Stopped · ${partnerRun.cases.length} of ${partnerRun.caseIds.length} ran` : isRefused(partnerResult) ? "Refused" : partnerResult.state !== "complete" ? "Didn't finish"
        : partnerResult.selection.scope === "subset" ? `${partnerResult.selection.caseIds.length} of ${partner.examples.length} tests ran`
        : inspectCases(partnerView).some(c => c.state === "unknown") ? "Incomplete run" : `${inspectCases(partnerView).filter(c => c.state === "pass").length} of ${partner.examples.length} passing`;
      const partnerText = partner ? ` · ${modelLabel(partner.latest.result.modelRoute)} ${(partnerRun?.revision ?? sourceIdentity(partnerView)) === sourceIdentity(state.base) && !anyDraft && (partnerRun ? runFresh(partnerRun) : ranWith(state.base, partnerResult)) ? "earlier" : "before your edit"}, ${partnerSummary}` : "";
      const runText = run ? run.state === "stopping" ? "Stopping Copilot turn…" : run.state === "stopped" ? `Stopped · ${run.cases.length} of ${run.caseIds.length} ran` : "Running" : "";
      const accessible = state.blocked ? "Result unconfirmed" : run && (testing() || !anyDraft && run.revision === sourceIdentity(state.base) && runFresh(run)) ? runText + partnerText : !shown.length ? "" : testing() ? "Running…"
        : stale && state.base.latest.status !== "current" ? `Out of date${previous ? ` · last run on ${previous}` : ""}` : state.base.latest.status !== "current" || !text ? "Not run yet"
        : (state.base.latest.result.state !== "complete" || malformed) && !isRefused(state.base.latest.result) && !anyDraft && !/didn't finish|incomplete/.test(text) ? `Incomplete run: ${text}`
        : anyDraft ? `Last run, before your edit: ${text}` : state.lastRun && !state.lastRun.one && !state.runAlert ? `${text} · just now` : text;
      // Update live-region text for screen readers.
      if (summaryNode.textContent !== accessible) summaryNode.textContent = accessible;
      meta.textContent = !state.blocked && run ? (anyDraft && !testing() || (run.revision !== sourceIdentity(state.base) || !runFresh(run)) && !testing() ? "Out of date · " : "") + runText + partnerText
        : !state.blocked && partner ? (stale ? `Out of date · ${modelLabel(selected.latest.result.modelRoute)} before your edit, ${inspectCases(selected).filter(c => c.state === "pass").length} of ${selected.examples.length} passing` : `Ran ${state.lastRun ? "just now" : "earlier"} on ${modelLabel(state.base.latest.result?.modelRoute) || selectedModel()}${duration}`) + partnerText
        : !oldResults && !state.busy && !state.blocked && isRefused(state.base.latest.result) ? text : stale ? `Out of date${previous ? ` · last run on ${previous}` : ""}` : !state.busy && state.lastRun && !state.runAlert ? duration ? `Ran just now${duration}` : "· just now" : "";
      if (firstRun) {
        firstRun.hidden = !counted.length || state.blocked || state.busy || !!history.last && !stale;
        firstRun.textContent = `${anyDraft ? "Save and run" : "Run"} ${plural(counted.length, "test")}`;
      }
      fitFilter();
    };
    // The filter appears only when the list doesn't fit on screen (or is in use).
    const fitFilter = () => {
      const win = doc.defaultView, rect = list.getBoundingClientRect?.(), headRect = head.getBoundingClientRect?.();
      const overflows = !!(win?.innerHeight && rect && rect.height > win.innerHeight - (headRect?.height ?? 0));
      group.hidden = !(overflows || filter === "problems");
    };
    const win = doc.defaultView;
    if (win?.addEventListener) {
      win.addEventListener("resize", fitFilter);
      cleanups.push(() => win.removeEventListener?.("resize", fitFilter));
    }
    updates.push(draw);
    return section;
  }

  // Pin the same controls when a long prompt hides its footer.
  function pinnedBar(button, foot, tests, updates) {
    const bar = el(doc, "div", "pinned-bar"), inner = el(doc, "div", "pinned-inner");
    const status = el(doc, "span", "meta"); status.setAttribute("aria-hidden", "true");
    const jump = button("", () => tests.scrollIntoView?.({ block: "start" }), "tb tb-quiet pinned-tests", "nav");
    const right = el(doc, "div", "pinned-actions");
    right.append(jump, saveButton(doc, button, "Save and run all tests", submitAll));
    inner.append(status, right); bar.append(inner); bar.hidden = true;
    updates.push(() => {
      status.textContent = footText("all");
      const rows = entries().filter((e) => !e.draft?.deleted).map((e) => testRow(e.c, true, state.blocked, ambiguousPassSet(state.base)));
      const failing = rows.filter((r) => r.base === "fail").length;
      jump.textContent = failing ? `Tests: ${failing} failing, last run` : "Tests";
    });
    const IO = doc.defaultView?.IntersectionObserver;
    if (IO) {
      const observer = new IO(([entry]) => {
        bar.hidden = entry.isIntersecting || entry.boundingClientRect.top < (entry.rootBounds?.top ?? 0);
      });
      observer.observe(foot);
      cleanups.push(() => observer.disconnect());
    }
    return bar;
  }


  function detailView(button, status, updates) {
    const entry = entries().find((e) => (e.key ?? `view:${e.c?.example.id}`) === state.route.key);
    if (!entry) { navigate({ name: "main" }); return mainView(button, status, updates); }
    const key = entry.key, editableTest = !!state.snap && !!key, c = entry.c, draft = () => key ? testDrafts.get(key) : undefined;
    const local = () => hasDrafts() || testing() || state.base.latest.status !== "current" || c?.previous === true;
    const current = () => draft()?.raw ?? entry.raw;
    const mutate = (fn) => {
      const d = draft() ?? { origin: entry.origin.id, raw: clone(entry.origin) };
      fn(d); noteDraft(key, d); refresh();
    };
    const out = [];
    // Header: back, more, name, result line
    const top = el(doc, "div", "detail-head"), bar = el(doc, "div", "detail-bar");
    const back = button("", () => { navigate({ name: "main" }); menuOpen = false; render(); }, "back", "nav");
    back.setAttribute("aria-label", "Back to tests"); back.append(icon(doc, "back", 14, 2), "Tests");
    bar.append(back);
    if (editableTest && draft()?.origin !== null) {
      const more = button("", () => { menuOpen = !menuOpen; render(); }, "ib more", "nav");
      more.setAttribute("aria-label", "More actions: Duplicate or delete test"); more.setAttribute("aria-haspopup", "menu"); more.setAttribute("aria-expanded", String(menuOpen));
      more.setAttribute("title", "More actions: Duplicate or delete test"); more.append(icon(doc, "more", 15, 2.6));
      bar.append(more);
      if (menuOpen) {
        const menu = el(doc, "div", "menu"); menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "More actions");
        const duplicate = button("Duplicate test", () => {
          clearUndo();
          const copy = duplicateTest(current()), name = `${draftName(entry)} copy`; copy.id = portableId(name);
          const newKey = `new:${++state.newCount}`; testDrafts.set(newKey, { origin: null, name, raw: copy });
          navigate({ name: "detail", key: newKey }); menuOpen = false; state.focusTestName = true; state.message = "Copied test. Its Recorded tools will capture on the next run."; render();
        }, "mi");
        const remove = button("Delete test", () => {
          clearUndo();
          if (entry.origin) testDrafts.set(key, { origin: entry.origin.id, raw: null, deleted: true }); else testDrafts.delete(key);
          navigate({ name: "main" }); menuOpen = false; state.message = "Test deleted locally. Enter saves; Restore brings it back."; render();
        }, "mi mi-danger");
        for (const node of [duplicate, remove]) node.setAttribute("role", "menuitem");
        menu.append(duplicate, remove); bar.append(menu);
      }
    }
    const nameLine = el(doc, "div", "name-line");
    let nameHint;
    if (editableTest && draft()?.origin === null) {
      const control = testNameControl(doc,
        () => ({ name: draft().name, disabled: state.busy || state.blocked, invalid: state.nameHintKey === key }), value => {
          clearUndo(); draft().name = value; draft().raw.id = portableId(value);
          if (value.trim()) state.nameHintKey = undefined;
          refresh();
        });
      activeTestName = control.input; nameHint = control.hint;
      fieldKeys(control.input); updates.push(control.update); nameLine.append(control.input);
    } else nameLine.append(el(doc, "h1", "test-title", draftName(entry)));
    const unsaved = el(doc, "span", "unsaved-tag", "Unsaved");
    nameLine.append(unsaved);
    const resultLine = el(doc, "span", "meta detail-status");
    updates.push(() => {
      unsaved.hidden = !draft() || draft().origin === null;
      const r = c ? inspectFor(c) : "Not run yet";
      const [word, ...context] = (draft()?.origin === null ? "" : r).split(" · ");
      resultLine.replaceChildren(el(doc, "span", "result-word", word), el(doc, "span", "detail-result-meta", context.length ? ` · ${context.join(" · ")}` : ""));
      resultLine.setAttribute("data-tone", testRow(c, local(), state.blocked, ambiguousPassSet(state.base)).tone ?? "none");
      resultLine.setAttribute("data-result", c?.state ?? "none");
    });
    function inspectFor(caseInfo) {
      if (state.blocked) return "Result unconfirmed";
      if (testing()) return "Tests running…";
      if (caseInfo.label === "Not in this run") return "Not in the last run";
      if (!["pass", "fail"].includes(caseInfo.state)) return caseInfo.row ? caseInfo.label : state.base.latest.status === "stale" && !history.last ? "Out of date" : "Not run yet";
      const model = modelLabel(caseInfo.previous ? caseInfo.previousModel : state.base.latest.result?.modelRoute);
      const when = caseInfo.capturedRevision === sourceIdentity(state.base) && !hasDrafts() && !testing() ? "recording run"
        : local() ? caseInfo.previous ? "Out of date · last run" : "before your edit" : state.lastRun && !state.runAlert ? "just now" : "last run";
      return `${caseInfo.state === "pass" ? "Passed" : "Failed"} · ${when}${model ? ` on ${model}` : ""}`;
    }
    top.append(bar, nameLine, ...(nameHint ? [nameHint] : []), resultLine);
    out.push(top);

    // The agent gets
    const gets = el(doc, "section", "detail-section");
    if (editableTest) {
      const label = el(doc, "label", "lbl", "The agent gets"), input = el(doc, "textarea", "field example-input");
      input.value = current().input; input.rows = 3; input.setAttribute("spellcheck", "false");
      input.setAttribute("placeholder", 'What the agent is given, e.g. Merged: "Fix slow search", closes #218');
      label.append(input);
      input.addEventListener("input", () => mutate((d) => { d.raw.input = input.value; }));
      fieldKeys(input); updates.push(() => {
        input.disabled = state.busy || state.blocked;
        let json = false; try { JSON.parse(input.value); json = true; } catch { /* Prose stays proportional. */ }
        input.className = `field example-input${json ? " json-input" : ""}`; growTextarea(input);
      });
      gets.append(label);
    } else gets.append(el(doc, "h2", "lbl", "The agent gets"), el(doc, "pre", "example-input literal", c.example.input));
    out.push(gets);
    if (state.base.authoring?.recordingControls && editableTest && state.base.tools.length) {
      const raw = current()?.mocks ? current() : entry.raw;
      const observed = c?.row?.tools ?? state.lastCapture?.examples?.find(e => e.id === raw.id)?.tools ?? [];
      out.push(renderTestTools(doc, { names: state.base.tools, raw, tools: observed.map(t => ({...t,settingDisabled:state.base.toolSettings.find(s=>s.name===t.name)?.state === "Disabled"})), details: state.base.authoring.recordingDetails?.filter(d=>d.exampleId===raw.id), disabled:()=>state.busy||state.blocked||!!state.candidate, updates, keys: fieldKeys,
        onChange:(name,mode)=>{mutate(d=>{d.raw=setToolMode(withToolModes(d.raw,raw),name,mode)});render()}, onReset:name=>{mutate(d=>{d.raw=setToolMode(withToolModes(d.raw,raw),name,"recorded",true)});render()} }));
    }

    // Passes if
    const checksSection = el(doc, "section", "detail-section checks-section");
    checksSection.append(el(doc, "h2", "lbl", "Passes if"));
    const checks = el(doc, "div", "checks");
    const facts = c && !state.blocked && draft()?.origin !== null ? failureFacts(c) : undefined;
    const displayChecks = uniqueIndex(c?.example.assertions ?? [], checkKey);
    const assertions = editableTest ? current().assertions : c.example.assertions;
    assertions.forEach((a, index) => {
      const display = editableTest ? displayChecks.get(checkKey(a)) : a;
      const observed = display && c?.expected?.get(checkKey(display)) === display ? c.checks?.get(checkKey(display)) : undefined;
      const row = el(doc, "div", "check"), checkState = el(doc, "span", "check-state"), verdict = el(doc, "span", "check-verdict");
      const judge = a.type === "llmJudge", details = judge ? judgeDetails(doc) : undefined;
      const judgeResult = details?.node;
      updates.push(() => {
        let kind = "none", label = "No result";
        const completed = !ambiguousPassSet(state.base) && (c?.state === "pass" || c?.state === "fail");
        if (completed && !state.blocked && draft()?.origin !== null && observed?.verdict === "pass") [kind, label] = ["pass", "Passed"];
        else if (completed && !state.blocked && draft()?.origin !== null && observed?.verdict === "fail") [kind, label] = ["fail", "Failed"];
        else if (judge && !ambiguousPassSet(state.base) && !state.blocked && draft()?.origin !== null && inconclusiveJudge(c, observed)) [kind, label] = ["error", "Inconclusive"];
        const text = kind === "none" ? "" : label;
        if (kind !== "none" && local()) [kind, label] = [`old-${kind}`, `${label} last run`];
        checkState.replaceChildren(statusIcon(doc, kind, label));
        verdict.textContent = text ? `${text}${judge && local() ? " · last run" : ""}` : "";
        verdict.setAttribute("data-tone", kind);
        if (details) details.update(editableTest ? current().assertions[index] : a, observed, kind !== "none", local());
      });
      const known = editableTest && EDITABLE_TYPES.some(([t]) => t === a.type);
      if (known) {
        const editKey = `${key}/${a.id}`, edit = el(doc, "div", "check-edit");
        const fields = checkFields(doc, a, index, (field, value) => {
          editingChecks.add(editKey); mutate(d => { d.raw.assertions[index][field] = value; });
        }, fieldKeys, modelControl, state.base.tools);
        const line = button("", () => { editingChecks.add(editKey); refresh(); fields.focus(); }, "check-line");
        line.append(checkState, el(doc, "span", "check-label", checkLabel({ type: a.type, fields: [[operandKey(a.type), a[operandKey(a.type)] ?? ""]] })));
        const choice = checkTypeControl(doc, a.type, chosen => {
          const wasJudge = current().assertions[index].type === "llmJudge";
          editingChecks.add(editKey); mutate(d => convertCheck(d.raw.assertions[index], chosen));
          if (wasJudge || chosen === "llmJudge") {
            render(); root.querySelectorAll(".check-type-button")[index]?.focus();
          } else { fields.show(current().assertions[index]); refresh(); }
        }, cancelDraft, state.base.authoring?.recordingControls ? state.base.tools : []);
        const remove = button("", () => { mutate(d => { d.raw.assertions.splice(index, 1); }); render(); }, "ib remove-check");
        remove.setAttribute("aria-label", `Remove check ${index + 1}`); remove.append(icon(doc, "none", 14, 2));
        edit.append(remove, choice.node, fields.node, ...(judge ? [] : [verdict])); row.append(line, edit);
        updates.push(() => {
          const editing = draft()?.origin === null || editingChecks.has(editKey);
          line.hidden = editing; edit.hidden = !editing;
          fields.update(state.busy || state.blocked); choice.update(state.busy || state.blocked);
          const target = fields.markIssue(state.invalidCheck?.id === a.id ? state.invalidCheck.issue : undefined);
          if (target) issueInput = target;
        });
      } else {
        const body = el(doc, "div", "check-readonly");
        const shown = display ?? { id: a.id, type: a.type, fields: Object.entries(a).filter(([k]) => k !== "id" && k !== "type").map(([k, v]) => [k, typeof v === "string" ? v : JSON.stringify(v)]) };
        body.append(el(doc, "span", "check-label", checkLabel(shown)));
        const details = el(doc, "details", "check-details"); details.append(el(doc, "summary", "", editableTest ? "Read-only check details" : "Check details"));
        details.append(el(doc, "pre", "", `id: ${shown.id}`), el(doc, "pre", "", `type: ${shown.type}`));
        for (const [k, v] of shown.fields) {
          const line = el(doc, "pre", "");
          if (shown.type === "llmJudge" && k === "rubric") { line.append(`${k}: `); inlineText(doc, line, v); } else line.textContent = `${k}: ${v}`;
          details.append(line);
        }
        body.append(details); row.append(checkState, body);
      }
      if (editableTest && !known) {
        const remove = button("", () => { mutate((d) => { d.raw.assertions.splice(index, 1); }); render(); }, "ib remove-check");
        remove.setAttribute("aria-label", `Remove check ${index + 1}`); remove.setAttribute("title", "Remove check");
        remove.append(icon(doc, "trash", 14, 2)); row.append(remove);
      }
      checks.append(row);
      if (judgeResult) { judgeResult.append(details.binding, verdict, details.score, details.reason); row.append(judgeResult); }
      const checkFacts = display && c && !state.blocked && draft()?.origin !== null ? failureFacts(c, display) : undefined;
      if (checkFacts) {
        // Repairs belong to the saved assertion they explain, not a later type
        // or operand occupying the same input. Recheck even detached handlers.
        const eligible = () => {
          const now = current().assertions[index];
          return now?.id === checkFacts.assertion.id && now.type === checkFacts.assertion.type && now[operandKey(now.type)] === checkFacts.value;
        };
        checks.append(explanation(button, checkFacts, local, editableTest ? index : -1, mutate, eligible, updates));
      }
    });
    checksSection.append(checks);
    if (editableTest) {
      const add = button("", () => {
        mutate((d) => {
          const ids = new Set(d.raw.assertions.map((a) => a.id)); let i = 1; while (ids.has(`check-${i}`)) i++;
          if (d.raw.assertions.length < 64) {
            d.raw.assertions.push({ id: `check-${i}`, type: "contains", value: "" }); editingChecks.add(`${key}/check-${i}`);
          }
        });
        render();
      }, "tb add-check");
      const plus = el(doc, "span", "plus"); plus.append(icon(doc, "plus", 12, 2.6)); add.append(plus, "Add check");
      checksSection.append(add);
    }
    out.push(checksSection);

    // The agent answered
    if (c && !state.blocked && draft()?.origin !== null && c.row) {
      const answered = el(doc, "section", "detail-section answered");
      answered.append(el(doc, "h2", "lbl", "The agent answered"));
      // Unknown observations never acquire a verdict.
      if (c.state === "unknown") answered.append(el(doc, "p", "meta result-details", plainReason(c.row)));
      if (c.row.answerExcerpt !== undefined) {
        answered.append(answerBlock(doc, c.row, local, facts?.actual ?? matchedAnswer(c), updates, c.state === "pass"));
      } else if (c.row.state !== "complete") {
        answered.append(el(doc, "p", "answer-error", c.state === "refused" ? "No answer: this run was refused." : `No answer: ${plainReason(c.row)}`));
      } else {
        answered.append(el(doc, "p", "quiet", c.state === "pass"
          ? "Run this test to see its answer."
          : "Answer content not included. Run this test again to see it."));
      }
      out.push(answered);
    }
    if (state.runAlert?.one !== undefined) out.push(runAlertNode(button, "one"));
    else if (c?.state === "refused" && refusedAlert()) out.push(runAlertNode(button, "one", refusedAlert()));

    // Footer: scoped Enter
    if (editableTest) {
      const foot = el(doc, "div", "detail-foot");
      foot.append(spinnerStatus(status, "one", updates));
      const right = el(doc, "div", "foot-actions");
      const cancel = button("", cancelDraft, "ib cancel-test", "nav");
      cancel.setAttribute("aria-label", "Cancel"); cancel.append(icon(doc, "close", 16, 2.2));
      cancel.setAttribute("title", "Discard this test's edits (Esc)");
      updates.push(() => { cancel.hidden = !draft(); });
      right.append(cancel, saveButton(doc, button, "Save and run this test", () => submitOne(key)));
      if (options.interruptOnEdit) {
        const stop = button("Stop tests", () => actions.stop(), "sb", "stop");
        updates.push(() => { stop.hidden = !testing() || !state.run?.cancellable; });
        right.append(stop);
      }
      foot.append(right, hostedNotice(updates)); out.push(foot);
    } else {
      const foot = el(doc, "div", "detail-foot"); foot.append(status); out.push(foot);
      updates.push(() => { status.textContent = state.snap ? "This test can't be edited here: its name isn't unique." : footText(); });
    }
    function cancelDraft() {
      const d = draft(); state.invalidCheck = undefined;
      testDrafts.delete(key);
      for (const id of editingChecks) if (id.startsWith(`${key}/`)) editingChecks.delete(id);
      if (d?.origin === null) navigate({ name: "main" });
      render();
    }
    function fieldKeys(node) {
      node.addEventListener("keydown", (event) => {
        if (event.isComposing) return;
        if (event.key === "Enter" && !event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey) { event.preventDefault(); submitOne(key); }
        else if (event.key === "Escape") { event.preventDefault(); if (menuOpen) { menuOpen = false; render(); } else cancelDraft(); }
      });
    }
    return out;
  }

  const modelControl = runtimeModelControl(doc,
    () => ({ authored: state.base.model, selected: modelChoice.override, chatModel: state.base.chatModel, chatModelName: state.base.chatModelName, disabled: state.busy || state.blocked || invalidEditors.size > 0 }),
    options.fetchModels ?? (() => execute({ operation: "models" })), (value) => {
      if (state.busy || state.blocked) return;
      clearUndo();
      state.observationGeneration++;
      history.prefer(selectedModel());
      if (modelChoice.choose(value, state.base)) {
        history.focus(selectedModel());
        state.base = modelChoice.view(state.base, history.selected(state.base, selectedModel())); state.lastRun = undefined; state.runAlert = undefined;
      }
      render(); modelControl.trigger.focus();
    });
  receiveSuggestions(initialSuggestions, state.base);
  render();
  return {
    observe,
    observeFailure,
    observation,
    idle: () => !state.busy && (!state.blocked || state.sourceUnavailable) && invalidEditors.size === 0,
    dirty: hasDrafts,
    dispose() { disposed = true; clearUndo(); actions.dispose(); state.observationGeneration++; for (const cleanup of cleanups.splice(0)) cleanup(); },
  };
}

async function main() {
  const root = document.getElementById("root");
  let app;
  try {
    app = renderView(document, root, await fetchView(), postAction, { fetchView, fetchProgress: runId => postAction({ operation: "progress", runId }) });
  } catch {
    renderView(document, root, { error: true, message: "The agent view could not be loaded." });
    return;
  }
  // Poll visible idle panels one GET at a time; never write, run or clear locks.
  let reading = false;
  setInterval(async () => {
    if (reading || document.visibilityState !== "visible" || !app.idle()) return;
    reading = true;
    const started = app.observation();
    try { app.observe(await fetchView(), started); } catch (e) { app.observeFailure(e, started); /* Other failures: try again on the next tick. */ } finally { reading = false; }
  }, 4000);
}
if (typeof document !== "undefined" && document.getElementById && document.getElementById("root")) main();
