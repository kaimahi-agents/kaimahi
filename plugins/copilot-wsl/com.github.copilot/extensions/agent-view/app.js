// Resource and authoring values are untrusted: render only inert DOM text.
import { instructionSections, runtimeModelSelection, agentName, fileLocation, effectiveModel } from "./authoring.mjs";
import { el, inlineText, editorValue, insertPlainText, fillEditor, purposeText, instructionsContent, runtimeModelControl, testNameControl, pathControls, icon, statusIcon,
  checkKey, uniqueIndex, inspectCases, friendlyName, clone, same, operandKey, EDITABLE_TYPES, quoted, checkLabel, oneLine, failureFacts, testRow, coherentSelection, ambiguousPassSet, countText, plural, resultHistory, resultPill, modelLabel, modelPresentation, growTextarea, checkTypeControl, checkFields, checkComplete, convertCheck, inconclusiveJudge } from "./editor.mjs";
export { provenActual } from "./editor.mjs";
const CONFLICTS = ["revision_mismatch", "revision-mismatch"];
const UNKNOWN = ["outcome-unknown", "uncertain-outcome", "uncertain_outcome"];
const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/;
const portableId = (name) => name.trim().toLowerCase().replace(/\s+/g, "-");

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
const NO_HANDLE = { observe: () => false, idle: () => false, dispose() {} };

export function renderView(doc, root, payload, execute = postAction, options = {}) {
  if (!payload || payload.error || !payload.file) {
    root.replaceChildren(el(doc, "p", "error", payload?.message || "The agent view could not be loaded."));
    return NO_HANDLE;
  }
  const readSnapshot = (view) => {
    try {
      if (view.authoring?.editable === true && typeof view.authoring.source === "string" && Array.isArray(view.authoring.examples)) {
        const sections = instructionSections(view.authoring.source);
        const main = sections.find((s) => /accountable/i.test(s.key));
        if (main && sections.length === 3) return { sections, main };
      }
    } catch { /* Old or noncanonical resources remain inspection-only. */ }
  };
  // All drafts live here, outside the DOM, so navigation never loses or saves them.
  let base = payload, snap = readSnapshot(payload), candidate, compareOpen = false;
  const sectionDrafts = new Map(), testDrafts = new Map(), invalidEditors = new Set();
  let route = { name: "main" }, busy = false, pending, blocked = false, savedUntested = false, message = "", runAlert, lastRun;
  let filter = "all", newCount = 0, menuOpen = false, cleanups = [], controls = [];
  const history = resultHistory(payload), editingChecks = new Set();
  let refresh = () => {};
  let focusTestName = false, nameHintKey, activeTestName;
  // A revision does not distinguish successive runs of unchanged source.
  let observationGeneration = 0;
  const modelChoice = runtimeModelSelection();
  base = modelChoice.view(base);
  const observation = () => ({ generation: observationGeneration, revision: base.file.revision });

  const editable = () => !!snap;
  const matches = (view) => view && !view.error && view.file?.path === payload.file.path;
  const hasDrafts = () => sectionDrafts.size > 0 || testDrafts.size > 0 || invalidEditors.size > 0;
  const testing = () => busy && ["test", "save-and-test"].includes(pending?.operation);
  const rawExamples = () => snap ? base.authoring.examples : [];
  const sectionText = (key) => sectionDrafts.get(key) ?? snap.sections.find((s) => s.key === key).text;
  // Display rows and their unique raw join; ambiguous IDs stay visible but not editable.
  function entries() {
    const cases = history.cases(base), raws = uniqueIndex(rawExamples(), (e) => e.id), ids = uniqueIndex(base.examples, (e) => e.id);
    const list = cases.map((c) => {
      const raw = snap && ids.get(c.example.id) === c.example ? raws.get(c.example.id) : undefined;
      const key = raw ? `id:${raw.id}` : undefined, draft = key ? testDrafts.get(key) : undefined;
      return { key, c, raw: draft ? draft.raw : raw, origin: raw, draft, openable: !!raw || !snap && ids.get(c.example.id) === c.example };
    });
    for (const [key, draft] of testDrafts) if (draft.origin === null) list.push({ key, raw: draft.raw, origin: undefined, draft, openable: true });
    return list;
  }
  const localExamples = () => entries().filter((e) => e.raw && !e.draft?.deleted).map((e) => clone(e.raw));
  const draftName = (e) => e.draft?.origin === null ? (e.draft.name.trim() || "New test") : friendlyName(e.raw?.id ?? e.c.example.id);

  function validate(draft, intended) {
    const raw = draft.raw;
    if (draft.origin === null && !draft.fixedId) {
      raw.id = portableId(draft.name);
      if (!/^[a-z][a-z0-9-]{0,62}$/.test(raw.id) || RESERVED.test(raw.id)) return "Use a name that starts with a letter, then letters, numbers, spaces or hyphens.";
    }
    if (intended.filter((e) => e.id === raw.id).length !== 1) return "Another test already has this name.";
    if (!raw.input.trim()) return "Say what the agent gets.";
    if (!raw.assertions.length) return "Keep at least one check.";
    if (raw.assertions.some(a => !checkComplete(a))) return "Fill in every check.";
  }
  function noteDraft(key, draft) {
    const origin = draft.origin && rawExamples().find((e) => `id:${e.id}` === key);
    if (draft.origin && origin && same(origin, draft.raw)) testDrafts.delete(key); else testDrafts.set(key, draft);
  }
  function pruneDrafts() {
    if (!snap) { sectionDrafts.clear(); testDrafts.clear(); return; }
    for (const [key, text] of sectionDrafts) if (snap.sections.find((s) => s.key === key)?.text === text) sectionDrafts.delete(key);
    const raws = uniqueIndex(rawExamples(), (e) => e.id);
    for (const [key, draft] of testDrafts) {
      if (draft.origin === null) continue;
      const raw = raws.get(draft.origin);
      if (raw === undefined) {
        // The test vanished from the new baseline: keep an edited copy rather than drop it.
        testDrafts.delete(key);
        if (!draft.deleted) {
          const next = `new:${++newCount}`;
          testDrafts.set(next, { origin: null, fixedId: true, name: draft.raw.id, raw: draft.raw });
          if (route.key === key) route = { name: "detail", key: next };
        }
      } else if (raw && !draft.deleted && same(raw, draft.raw)) testDrafts.delete(key);
    }
    if (route.name === "detail" && !entries().some((e) => e.key === route.key)) route = { name: "main" };
  }
  function adopt(view, submitted) {
    observationGeneration++;
    const renamed = route.name === "detail" && submitted.tests.includes(route.key) ? testDrafts.get(route.key)?.raw?.id : undefined;
    history.remember(view); editingChecks.clear();
    base = modelChoice.view(view); snap = readSnapshot(base); candidate = undefined; compareOpen = false;
    for (const key of submitted.sections) sectionDrafts.delete(key);
    for (const key of submitted.tests) testDrafts.delete(key);
    if (renamed) route = { name: "detail", key: `id:${renamed}` };
    pruneDrafts();
  }

  async function act(operation, values = {}, submitted = { sections: [], tests: [] }) {
    if (busy || ((blocked || invalidEditors.size) && operation !== "reload")) return;
    observationGeneration++;
    busy = true; pending = { operation, one: values.caseId !== undefined }; message = operation === "run" ? "Running…" : ""; runAlert = undefined; refresh();
    const action = { operation, ...values };
    if (!["reload", "suggest"].includes(operation)) action.expectedRevision = base.file.revision;
    const executing = ["save-and-test", "test", "run"].includes(operation);
    if (executing) {
      action.showContent = true;
      if (modelChoice.override) action.model = modelChoice.override;
    }
    let response;
    try { response = await execute(action); } catch {
      busy = false; pending = undefined; blocked = true;
      message = "Connection lost. The action may have completed. Do not resubmit. Your draft is still here; reload to inspect the saved file.";
      render(); return;
    }
    busy = false; pending = undefined; message = "";
    if (executing && matches(response?.view)) modelChoice.confirm(response.view);
    const tested = ["test", "save-and-test"].includes(operation);
    if (operation === "reload") {
      if (matches(response?.view)) {
        sectionDrafts.clear(); testDrafts.clear(); blocked = false; savedUntested = false; lastRun = undefined;
        adopt(response.view, { sections: [], tests: [] }); message = "Reloaded the saved agent. Drafts were discarded.";
      } else message = response?.message || "Reload did not complete. Your draft is still here.";
    } else if (response?.error === "no-tests" && matches(response.view)) {
      adopt(response.view, response.saved ? submitted : { sections: [], tests: [] }); lastRun = undefined;
    } else if (response?.error) {
      const unknown = UNKNOWN.includes(response.error) || CONFLICTS.includes(response.error);
      // Saved: the bridge confirmed the new revision. Unsaved: it must still be ours.
      const fresh = matches(response.view) && (response.saved === true || response.view.file.revision === base.file.revision);
      if (response.error === "stale_revision" || response.error === "stale-revision") {
        message = "This agent changed outside the panel. Your edits are kept."; checkNow();
      } else if (!unknown && fresh && (response.saved || ["test", "run", "save-and-test"].includes(operation))) {
        // A confirmed snapshot after the failure: saved and untested, or a fresh non-passing observation.
        adopt(response.view, response.saved ? submitted : { sections: [], tests: [] });
        runAlert = { text: response.message || "", saved: response.saved === true, finished: response.view.latest.status === "current", one: values.caseId };
        lastRun = undefined;
      } else if (unknown || response.saved === true || (response.error === "action-failed" && ["test", "run"].includes(operation))) {
        blocked = true; savedUntested = response.saved === true;
        message = CONFLICTS.includes(response.error) ? "Changed elsewhere during the run. Your draft is still here. Reload and discard draft to continue."
          : response.saved ? "Saved, but not tested for this revision. Your draft is still here. Reload before continuing."
          : response.message || "Outcome not confirmed. Reload deliberately to inspect the saved file.";
      } else if (response.error === "permission_required" && tested) {
        runAlert = { text: response.message || "", saved: false, one: values.caseId };
      } else message = response.message || "The action was refused. Your draft is still here.";
    } else if (operation === "suggest") {
      message = response?.message || "Asked Copilot to add tests in chat.";
    } else if (matches(response?.view)) {
      adopt(response.view, submitted);
      if (tested) lastRun = { one: values.caseId };
    } else {
      blocked = true; message = "Outcome not confirmed. Do not resubmit. Reload deliberately to inspect the saved file.";
    }
    render();
  }

  // Main Enter: every pending draft once, then the full suite (even unchanged).
  function submitAll() {
    if (!snap || busy || blocked || candidate || invalidEditors.size) return;
    const list = entries(), intended = localExamples();
    for (const e of list) {
      if (!e.draft || e.draft.deleted) continue;
      const problem = validate(e.draft, intended);
      if (problem) { message = `Finish or cancel the unsaved test ${quoted(draftName(e))}: ${problem}`; refresh(); return; }
    }
    const values = {}, submitted = { sections: [...sectionDrafts.keys()], tests: [...testDrafts.keys()] };
    if (sectionDrafts.size) values.sections = [...sectionDrafts].map(([key, text]) => ({ key, text }));
    if (testDrafts.size) values.examples = localExamples();
    if (!hasDrafts() && !intended.length) { message = "No tests yet — add or suggest one"; refresh(); return; }
    act(hasDrafts() ? "save-and-test" : "test", values, submitted);
  }
  // Detail Enter: the prompt plus only this test, scoped to its caseId.
  function submitOne(key) {
    if (!snap || busy || blocked || candidate || invalidEditors.size) return;
    const entry = entries().find((e) => e.key === key);
    if (!entry?.raw || entry.draft?.deleted) return;
    if (entry.draft?.origin === null && !entry.draft.fixedId && !entry.draft.name.trim()) {
      nameHintKey = key; message = ""; refresh(); activeTestName?.focus(); return;
    }
    let intended = clone(rawExamples());
    if (entry.draft) {
      const raw = entry.draft.raw;
      if (entry.draft.origin === null) intended.push(raw); else intended = intended.map((e) => e.id === entry.draft.origin ? raw : e);
      const problem = validate(entry.draft, intended);
      if (problem) { message = problem; refresh(); return; }
      intended = clone(intended);
    }
    const values = { caseId: entry.raw.id }, submitted = { sections: [...sectionDrafts.keys()], tests: entry.draft ? [key] : [] };
    if (sectionDrafts.size) values.sections = [...sectionDrafts].map(([k, text]) => ({ key: k, text }));
    if (entry.draft) values.examples = intended;
    act(sectionDrafts.size || entry.draft ? "save-and-test" : "test", values, submitted);
  }

  // Background observation: never during an action, never clears a lock, never writes.
  function observe(view, started = observation()) {
    if (started.generation !== observationGeneration || started.revision !== base.file.revision
      || !matches(view) || busy || blocked || invalidEditors.size) return false;
    const observed = view;
    view = modelChoice.view(view);
    if (same(view, base) || (candidate && same(view, candidate))) return false;
    observationGeneration++;
    lastRun = undefined;
    if (view.file.revision === base.file.revision || !hasDrafts()) {
      history.remember(observed);
      base = view; snap = readSnapshot(view); candidate = undefined; compareOpen = false;
      // A newly accepted run needs new result DOM; ordinary edits only refresh
      // its styling, leaving the focused input in place.
      pruneDrafts(); render();
      return true;
    }
    candidate = view; compareOpen = false; render();
    return true;
  }
  async function checkNow() {
    if (typeof options.fetchView !== "function") return;
    const started = observation();
    try { observe(await options.fetchView(), started); } catch { /* Polling is best effort and never changes state on failure. */ }
  }
  function useTheirs() {
    if (!candidate) return;
    observationGeneration++;
    sectionDrafts.clear(); testDrafts.clear(); invalidEditors.clear();
    history.remember(candidate);
    base = candidate; snap = readSnapshot(base); candidate = undefined; compareOpen = false; lastRun = undefined;
    pruneDrafts(); message = "Using the new version. Your edits were discarded."; render();
  }
  // Keep my edits adopts the chosen snapshot as the new baseline and reapplies only
  // changed section keys and test identities; anything ambiguous is refused intact.
  function keepMine() {
    if (!candidate || invalidEditors.size) return;
    observationGeneration++;
    const next = readSnapshot(candidate);
    if (!next) { message = "The new version can't be edited here. Compare it, or use the new version."; refresh(); return; }
    const theirs = candidate.authoring.examples, ids = uniqueIndex(theirs, (e) => e.id);
    const idOf = (d) => d.fixedId ? d.raw.id : portableId(d.name);
    const clash = [...testDrafts.values()].find((d) => d.origin === null ? theirs.some((e) => e.id === idOf(d)) : ids.get(d.origin) === null);
    if (clash) {
      message = `Can't keep your edits automatically: the new version also has a test named ${quoted(clash.raw?.id ?? clash.origin)}. Compare, or use the new version.`;
      refresh(); return;
    }
    history.remember(candidate);
    base = candidate; snap = next; candidate = undefined; compareOpen = false; lastRun = undefined;
    pruneDrafts(); message = "Kept your edits on top of the new version. Enter saves them."; render();
  }
  function comparison() {
    const items = [], next = readSnapshot(candidate);
    if (snap) for (const s of snap.sections) {
      const mine = sectionText(s.key), theirs = next?.sections.find((t) => t.key === s.key)?.text;
      if (mine !== s.text || theirs !== s.text) items.push([s.key === snap.main.key ? "Prompt" : s.key, s.text, mine, theirs]);
    }
    const show = (raw) => raw === undefined ? undefined : JSON.stringify(raw, null, 2);
    const theirsRaw = uniqueIndex(candidate.authoring?.examples ?? [], (e) => e.id), baseRaw = uniqueIndex(rawExamples(), (e) => e.id);
    const ids = new Set([...baseRaw.keys(), ...theirsRaw.keys()]);
    for (const d of testDrafts.values()) if (d.origin === null) ids.add(`\u0000${d.name}`);
    for (const id of ids) {
      const draft = id.startsWith("\u0000") ? [...testDrafts.values()].find((d) => d.origin === null && `\u0000${d.name}` === id) : testDrafts.get(`id:${id}`);
      const b = baseRaw.get(id) ?? undefined, t = theirsRaw.get(id) ?? undefined, m = draft ? (draft.deleted ? undefined : draft.raw) : b;
      if (!same(b, m) || !same(b, t)) items.push([`Test ${quoted(draft?.origin === null ? draft.name : id)}`, show(b), show(m), show(t)]);
    }
    if (candidate.model !== base.model) items.push(["Model", base.model, base.model, candidate.model]);
    if (candidate.title !== base.title) items.push(["Title", base.title, base.title, candidate.title]);
    return items;
  }

  // --- DOM ----------------------------------------------------------------
  function render() {
    for (const cleanup of cleanups.splice(0)) cleanup();
    invalidEditors.clear();
    const content = el(doc, "div", route.name === "detail" ? "content detail-view" : "content");
    controls = []; activeTestName = undefined;
    const button = (label, action, className = "", guard = "idle") => {
      const node = el(doc, "button", className, label); node.type = "button";
      node.addEventListener("click", action); controls.push({ node, guard }); return node;
    };
    const status = el(doc, "span", "meta prompt-status"); status.setAttribute("role", "status");
    const messageNode = el(doc, "p", "action-message"); messageNode.setAttribute("role", "status"); messageNode.setAttribute("aria-live", "polite");
    const updates = [];
    refresh = () => {
      root.setAttribute("aria-busy", String(busy));
      messageNode.textContent = message;
      for (const u of updates) u();
      for (const { node, guard } of controls) {
        node.disabled = guard === "reload" ? busy : invalidEditors.size > 0 || (guard === "nav" ? false
          : busy || blocked || (guard === "save" && !!candidate) || (guard === "clean" && hasDrafts()));
      }
    };
    const detail = route.name === "detail";
    const parts = detail ? detailView(button, status, updates) : mainView(button, status, updates);
    const line = el(doc, "div", "message-line");
    line.append(messageNode);
    if (snap) {
      const reloadNow = button("Reload and discard draft", () => act("reload"), "tb", "reload");
      updates.push(() => { reloadNow.hidden = !blocked; });
      line.append(reloadNow);
    }
    // Messages sit under what you were editing: the prompt on main, the header in a test.
    parts.splice(route.name === "detail" ? 1 : Math.min(2, parts.length), 0, line);
    if (candidate) parts.splice(detail ? 1 : 1, 0, conflictNotice(button));
    content.append(...parts);
    root.replaceChildren(content);
    refresh();
    if (focusTestName) { activeTestName?.focus(); focusTestName = false; }
  }

  function conflictNotice(button) {
    const box = el(doc, "div", "notice notice-info"); box.setAttribute("role", "alert");
    const items = comparison(), parts = [];
    if (items.some(([name]) => name === "Prompt")) parts.push("the prompt");
    const tests = items.filter(([name]) => name.startsWith("Test ")).length;
    if (tests) parts.push(plural(tests, "test"));
    const others = items.length - tests - (parts[0] === "the prompt" ? 1 : 0);
    if (others) parts.push(plural(others, "other part"));
    const head = el(doc, "span", "notice-text"); head.append(icon(doc, "sync", 15, 2),
      el(doc, "span", "", `This agent changed outside the panel${parts.length ? `: ${parts.join(", ")}` : ""}. You have unsaved edits.`));
    const actions = el(doc, "div", "notice-actions");
    const compare = button("Compare", () => { compareOpen = !compareOpen; render(); }, "sb", "nav");
    compare.setAttribute("aria-expanded", String(compareOpen));
    actions.append(compare, button("Keep my edits", keepMine, "sb"), button("Use the new version", useTheirs, "sb"));
    box.append(head, actions);
    if (compareOpen) {
      const list = el(doc, "div", "compare");
      for (const [name, b, m, t] of items) {
        const item = el(doc, "section", "compare-item");
        item.append(el(doc, "h3", "lbl", name));
        const cols = el(doc, "div", "compare-cols");
        for (const [label, text] of [["Before your edits", b], ["Your edits", m], ["New version", t]]) {
          const col = el(doc, "div", "compare-col");
          col.append(el(doc, "span", "meta", label), el(doc, "pre", "compare-text", text === undefined ? "(not present)" : text));
          cols.append(col);
        }
        item.append(cols); list.append(item);
      }
      if (!items.length) list.append(el(doc, "p", "quiet", "No differences in the prompt or tests."));
      box.append(list);
    }
    return box;
  }

  function resultSummary() {
    const n = entries().filter(e => !e.draft?.deleted).length, result = base.latest.result;
    if (testing()) return { tone: "running", text: `Running ${pending.one ? "1 test" : plural(n, "test")}…` };
    if (blocked) return { tone: "none", text: "Result unconfirmed" };
    if (base.latest.status === "stale" || (hasDrafts() || base.latest.status !== "current") && history.last) return { tone: "none", text: "Out of date" };
    if (!n || !result || result.reasonCode === "no_tests") return { tone: "none", text: "Not run yet" };
    if (result.state !== "complete" || !coherentSelection(base)) return { tone: "error", text: result.state === "complete" ? "Incomplete run" : "Didn't finish" };
    const cases = inspectCases(base), passing = cases.filter(c => c.state === "pass").length;
    const full = result.fullPass && result.verdict === "pass" && result.selection.scope === "full" && passing === n;
    if (!full && (passing === n || cases.some(c => c.state === "unknown"))) return { tone: "error", text: "Incomplete run" };
    return { tone: full ? "pass" : cases.some(c => c.state === "fail") ? "fail" : "none", text: `${passing} of ${n} passing` };
  }

  function header(button, updates) {
    const displayName = agentName(base.title), file = fileLocation(base.file.path);
    // One name and model, with source details still available from the file line.
    const source = el(doc, "details", "source-details"), info = el(doc, "div", "source-info");
    const sourceSummary = el(doc, "summary", "meta agent-meta", `${file.name}${file.folder ? ` · in ${file.folder}` : ""}`);
    sourceSummary.setAttribute("title", "Source details"); source.append(sourceSummary, info);
    info.append(el(doc, "p", "source-title", base.title), pathControls(doc, base.file.path), el(doc, "code", "revision", base.file.revision));
    const head = el(doc, "header", "agent-header"), identity = el(doc, "div", "agent-identity"), actions = el(doc, "div", "header-controls");
    identity.append(el(doc, "span", "agent-eyebrow", "Your agent"), el(doc, "h1", "", displayName));
    const pill = resultPill(doc, resultSummary); actions.append(pill.node); updates.push(pill.update);
    if (snap) {
      updates.push(modelControl.update); controls.push({ node: modelControl.trigger, guard: "idle" }); actions.append(modelControl.node);
    } else {
      const id = effectiveModel(base.model, "", base.chatModel);
      const model = el(doc, "span", "model-readonly", `${modelPresentation(id).name} · ${base.model ? "from the agent file" : "default"}`);
      model.setAttribute("title", id); actions.append(model);
    }
    const purpose = purposeText(base), summary = el(doc, "p", "purpose");
    const chars = Array.from(purpose); summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : purpose;
    identity.append(summary, source); head.append(identity, actions);
    if (chars.length > 160) info.append(el(doc, "p", "", purpose));
    return { head, info };
  }

  function makeEditor(section, label, updates) {
    const node = el(doc, "div", "instruction-body");
    fillEditor(doc, node, sectionText(section.key));
    node.setAttribute("role", "textbox"); node.setAttribute("aria-multiline", "true");
    node.setAttribute("aria-label", label); node.setAttribute("contenteditable", "plaintext-only");
    node.setAttribute("spellcheck", "false");
    const changed = () => {
      try {
        const value = editorValue(node); invalidEditors.delete(section.key);
        if (value === section.text) sectionDrafts.delete(section.key); else sectionDrafts.set(section.key, value);
      } catch {
        invalidEditors.add(section.key);
        message = "Unsupported editor formatting. Your draft is kept; reload before saving.";
      }
      refresh();
    };
    const lineBreak = () => { if (!busy && !blocked) { insertPlainText(doc, node, "\n"); changed(); } };
    node.addEventListener("input", changed);
    node.addEventListener("beforeinput", event => {
      if (event.inputType === "insertLineBreak" && event.cancelable && !event.isComposing) { event.preventDefault(); lineBreak(); }
    });
    node.addEventListener("keydown", (event) => {
      if (event.isComposing) return;
      if (event.key === "Enter" && !event.ctrlKey && !event.altKey && !event.metaKey) {
        event.preventDefault();
        if (event.shiftKey) lineBreak(); else route.name === "detail" ? submitOne(route.key) : submitAll();
      } else if (event.key === "Escape" && sectionDrafts.has(section.key)) {
        event.preventDefault(); sectionDrafts.delete(section.key); invalidEditors.delete(section.key); render();
      }
    });
    for (const kind of ["paste", "drop"]) node.addEventListener(kind, (event) => {
      event.preventDefault(); if (busy || blocked) return;
      const transfer = kind === "paste" ? event.clipboardData : event.dataTransfer;
      insertPlainText(doc, node, transfer?.getData("text/plain") ?? ""); changed();
    });
    updates.push(() => {
      node.setAttribute("contenteditable", busy || blocked ? "false" : "plaintext-only");
      node.setAttribute("aria-readonly", String(busy || blocked));
    });
    return node;
  }

  const total = () => localExamples().length;
  function footText(scope) {
    if (!snap) return "Read-only inspection. This engine or agent file does not provide a complete editable snapshot.";
    const n = total(), tests = plural(n, "test");
    if (busy) {
      if (!pending?.operation || !["save-and-test", "test"].includes(pending.operation)) return "Working…";
      const what = pending.one ? "this test" : tests;
      return pending.operation === "save-and-test" ? `Saving and running ${what}…` : `Saved · running ${what}`;
    }
    if (blocked) return savedUntested ? "Saved · tests didn't run" : "Reload required · result unconfirmed";
    if (invalidEditors.size) return "Edited · unsupported formatting, reload before saving";
    if (candidate) return "Edited · settle the outside change before saving";
    if (scope === "one") return sectionDrafts.size || testDrafts.has(route.key)
      ? "Enter saves and runs this test" : "Saved · Enter runs this test";
    if (!n) return hasDrafts() ? "Edited · Enter saves" : "Saved · add a test to run it";
    if (hasDrafts()) return `Edited · Enter saves and runs ${tests}`;
    if (runAlert?.saved && !runAlert.finished) return "Saved · tests didn't run";
    return `Saved · Enter runs ${tests}`;
  }
  function saveButton(button, label, action, className = "ib save-run") {
    const node = button("", action, className, "save");
    node.setAttribute("aria-label", label); node.setAttribute("title", `${label} (Enter)`);
    node.append(icon(doc, "enter", 16, 2.2));
    return node;
  }
  function spinnerStatus(status, scope, updates) {
    const spin = el(doc, "span", "busy-spinner"); spin.append(icon(doc, "running", 12, 2.6, "spin"));
    const box = el(doc, "span", "status-line"); box.append(spin, status);
    updates.push(() => { status.textContent = footText(scope); spin.hidden = !busy; });
    return box;
  }

  function mainView(button, status, updates) {
    const { head, info } = header(button, updates);
    const promptSection = el(doc, "section", "prompt-section"), promptBox = el(doc, "div", "prompt-box main-prompt");
    const promptLabel = el(doc, "h2", "lbl", "Prompt");
    const foot = el(doc, "div", "prompt-foot");
    if (snap) {
      promptBox.append(makeEditor(snap.main, "Prompt", updates));
      const additional = el(doc, "details", "additional");
      additional.append(el(doc, "summary", "", "Additional instructions"));
      for (const section of snap.sections.filter((s) => s !== snap.main)) additional.append(el(doc, "h3", "", section.key), makeEditor(section, section.key, updates));
      if ([...sectionDrafts.keys()].some((k) => k !== snap.main.key)) additional.open = true;
      info.append(additional);
    } else promptBox.append(instructionsContent(doc, base));
    foot.append(spinnerStatus(status, "all", updates));
    const saveRun = snap ? saveButton(button, "Save and run all tests", submitAll) : undefined;
    if (saveRun) foot.append(saveRun);
    promptBox.append(foot); promptSection.append(promptLabel, promptBox);

    sourceActions(button, info, updates);
    const tests = testsSection(button, updates);
    const out = [head, promptSection, tests];
    if (snap) out.push(pinnedBar(button, foot, tests, updates));
    return out;
  }

  function sourceActions(button, info, updates) {
    const adHoc = base.latest.status === "current" ? base.latest.result?.adHoc : undefined;
    if (adHoc) {
      const result = el(doc, "section", "ad-hoc-result");
      result.append(el(doc, "h3", "", "Last ad-hoc run"), el(doc, "p", "", `${adHoc.state} · ${adHoc.verdict}`), answerBlock(adHoc, hasDrafts, undefined, updates));
      updates.push(() => { result.hidden = hasDrafts() || blocked; });
      info.append(result);
    }
    if (!snap) return;
    const runInput = el(doc, "textarea"); runInput.setAttribute("aria-label", "Ad-hoc input"); runInput.rows = 2;
    const box = el(doc, "div", "source-actions"), run = el(doc, "details", "run-disclosure");
    run.append(el(doc, "summary", "", "Try another input"), runInput, button("Run", () => act("run", { input: runInput.value }), "sb", "clean"));
    updates.push(() => { runInput.disabled = busy || blocked || hasDrafts(); });
    box.append(run, button("Reload and discard draft", () => act("reload"), "tb", "reload"));
    info.append(box);
  }

  function runAlertNode(button, scope) {
    const box = el(doc, "div", "notice notice-warn run-alert"); box.setAttribute("role", "alert");
    const reason = runAlert.text.replace(/\.\s*$/, "");
    const text = runAlert.finished ? `The run didn't finish: ${reason}.` : `The run didn't start: ${reason}.`;
    const tail = runAlert.saved ? " Your changes are saved." : hasDrafts() ? " Your edits are kept." : "";
    const line = el(doc, "span", "notice-text"); line.append(icon(doc, "warn", 15, 2), el(doc, "span", "", text + tail));
    box.append(line, button("Try again", () => scope === "one" ? submitOne(route.key) : submitAll(), "sb", "save"));
    return box;
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
    const pill = resultPill(doc, resultSummary), firstRun = snap ? button("", submitAll, "tb first-run", "save") : undefined;
    updates.push(pill.update);
    title.append(group, pill.node, meta, summaryNode, ...(firstRun ? [firstRun] : [])); head.append(title);
    if (snap) {
      const actions = el(doc, "div", "tests-actions"), add = button("", addTest, "tb", "nav");
      const plus = el(doc, "span", "plus"); plus.append(icon(doc, "plus", 12, 2.6));
      add.append(plus, "Add test");
      actions.append(button("Suggest tests", () => act("suggest"), "tb tb-quiet"), add);
      head.append(actions);
    }
    section.append(head);
    if (runAlert && !runAlert.one) section.append(runAlertNode(button, "all"));
    const empty = el(doc, "div", "empty-tests");
    if (snap) {
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
      const all = entries(), anyDraft = hasDrafts(), oldResults = anyDraft || testing() || base.latest.status !== "current";
      const shown = all.map((e) => ({ e, r: e.draft?.origin === null ? { kind: "none", base: "none", label: "Not run", note: "", count: "" } : testRow(e.c, oldResults || e.c?.previous, blocked, ambiguousPassSet(base)) }));
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
        const open = e.openable ? button("", () => { route = { name: "detail", key: e.key ?? `view:${id}` }; menuOpen = false; render(); }, "test-row", "nav") : el(doc, "div", "test-row");
        if (e.openable) rowControls.push(controls[controls.length - 1]);
        const noteText = e.draft ? "Unsaved" + (r.note ? ` · ${r.note}` : "") : r.note;
        const example = e.c?.example, summary = example ? `${oneLine(example.input, 64)} → ${example.assertions.map(checkLabel).join(" · ")}` : "";
        const word = r.base === "pass" ? "Passed" : r.base === "fail" ? "Failed" : r.base === "error" ? "Didn't finish" : r.label === "No current result" || r.label === "Out of date" && history.last ? "Not run yet" : r.label;
        const verdict = el(doc, "span", `test-verdict ${r.kind}`);
        verdict.append(el(doc, "span", "verdict-word", word));
        if (r.kind.startsWith("old-")) verdict.append(el(doc, "span", "sr-only verdict-context", " last run"));
        open.append(el(doc, "span", "test-name", draftName(e)), el(doc, "span", "test-input-summary", summary), verdict);
        const line = el(doc, "span", e.draft ? "test-note unsaved" : "test-note", noteText); line.hidden = !noteText;
        open.append(line);
        item.append(open); list.append(item);
      }
      if (hidden) {
        const show = el(doc, "button", "tb", "Show all"); show.type = "button";
        show.addEventListener("click", () => { filter = "all"; draw(); refresh(); });
        note.append(`${plural(hidden, "passing test")} hidden `, show);
      }
      empty.hidden = !snap || shown.length > 0;
      list.hidden = shown.length === 0;
      const text = countText(counted.map(({ e, r }) => base.latest.status === "current" && e.c?.previous ? { count: "not run" } : r));
      const malformed = !!base.latest.result && !coherentSelection(base);
      const stale = base.latest.status === "stale" || (anyDraft || base.latest.status !== "current") && !!history.last;
      const previous = modelLabel(history.last?.latest.result.modelRoute);
      const accessible = blocked ? "Result unconfirmed" : !shown.length ? "" : testing() ? "Running…"
        : stale && base.latest.status !== "current" ? `Out of date${previous ? ` · last run on ${previous}` : ""}` : base.latest.status !== "current" || !text ? "Not run yet"
        : (base.latest.result.state !== "complete" || malformed) && !anyDraft && !/didn't finish|incomplete/.test(text) ? `Incomplete run: ${text}`
        : anyDraft ? `Last run, before your edit: ${text}` : lastRun && !lastRun.one && !runAlert ? `${text} · just now` : text;
      // A live region needs changed text, not only a changed accessible name.
      if (summaryNode.textContent !== accessible) summaryNode.textContent = accessible;
      meta.textContent = stale ? `Out of date${previous ? ` · last run on ${previous}` : ""}` : !busy && lastRun && !runAlert ? "· just now" : "";
      if (firstRun) {
        firstRun.hidden = !counted.length || blocked || busy || !!history.last || base.latest.status === "stale";
        firstRun.textContent = `Run ${plural(counted.length, "test")}`;
      }
      pill.node.hidden = firstRun && !firstRun.hidden || stale || !counted.length;
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

  // When a long prompt pushes its own footer below the fold, pin the same controls.
  function pinnedBar(button, foot, tests, updates) {
    const bar = el(doc, "div", "pinned-bar"), inner = el(doc, "div", "pinned-inner");
    const status = el(doc, "span", "meta"); status.setAttribute("aria-hidden", "true");
    const jump = button("", () => tests.scrollIntoView?.({ block: "start" }), "tb tb-quiet pinned-tests", "nav");
    const right = el(doc, "div", "pinned-actions");
    right.append(jump, saveButton(button, "Save and run all tests", submitAll));
    inner.append(status, right); bar.append(inner); bar.hidden = true;
    updates.push(() => {
      status.textContent = footText("all");
      const rows = entries().filter((e) => !e.draft?.deleted).map((e) => testRow(e.c, true, blocked, ambiguousPassSet(base)));
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

  function addTest() {
    if (!snap) return;
    if (localExamples().length >= 128) { message = "Keep at most 128 tests. Edit or delete an existing test first."; refresh(); return; }
    const key = `new:${++newCount}`;
    testDrafts.set(key, { origin: null, name: "", raw: { id: "", input: "", assertions: [{ id: "check-1", type: "contains", value: "" }] } });
    route = { name: "detail", key }; focusTestName = true; render();
  }

  // `actual` is a proven unique substring of the excerpt; it is marked, never rewritten.
  function answerBlock(row, local, actual, updates) {
    if (row.answerExcerpt === undefined) return el(doc, "p", "quiet", "Answer content not included.");
    const box = el(doc, "div", "answer");
    const truncated = row.truncated || /…\s*\[truncated\]$/.test(row.answerExcerpt ?? "");
    if (truncated) box.append(el(doc, "span", "label", "Answer truncated"));
    const pre = el(doc, "pre", "answer-excerpt", actual === undefined ? row.answerExcerpt : undefined);
    if (actual !== undefined) {
      const [before, after] = row.answerExcerpt.split(actual);
      const mark = el(doc, "mark", "", actual);
      updates.push(() => { mark.className = local() ? "old" : ""; });
      pre.append(before, mark, after);
    }
    box.append(pre);
    return box;
  }

  function detailView(button, status, updates) {
    const entry = entries().find((e) => (e.key ?? `view:${e.c?.example.id}`) === route.key);
    if (!entry) { route = { name: "main" }; return mainView(button, status, updates); }
    const key = entry.key, editableTest = !!snap && !!key, c = entry.c, draft = () => key ? testDrafts.get(key) : undefined;
    const local = () => hasDrafts() || testing() || base.latest.status !== "current" || c?.previous === true;
    const current = () => draft()?.raw ?? entry.raw;
    const mutate = (fn) => {
      const d = draft() ?? { origin: entry.origin.id, raw: clone(entry.origin) };
      fn(d); noteDraft(key, d); refresh();
    };
    const out = [];
    // Header: back, more, name, result line
    const top = el(doc, "div", "detail-head"), bar = el(doc, "div", "detail-bar");
    const back = button("", () => { route = { name: "main" }; menuOpen = false; render(); }, "back", "nav");
    back.setAttribute("aria-label", "Back to tests"); back.append(icon(doc, "back", 14, 2), "Tests");
    bar.append(back);
    if (editableTest && draft()?.origin !== null) {
      const more = button("", () => { menuOpen = !menuOpen; render(); }, "ib more", "nav");
      more.setAttribute("aria-label", "More actions: Delete test"); more.setAttribute("aria-haspopup", "menu"); more.setAttribute("aria-expanded", String(menuOpen));
      more.setAttribute("title", "More actions: Delete test"); more.append(icon(doc, "more", 15, 2.6));
      bar.append(more);
      if (menuOpen) {
        const menu = el(doc, "div", "menu"); menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "More actions");
        const duplicate = button("Duplicate test", () => {
          const copy = clone(current()), name = `${draftName(entry)} copy`; copy.id = portableId(name);
          const newKey = `new:${++newCount}`; testDrafts.set(newKey, { origin: null, name, raw: copy });
          route = { name: "detail", key: newKey }; menuOpen = false; focusTestName = true; render();
        }, "mi");
        const remove = button("Delete test", () => {
          if (entry.origin) testDrafts.set(key, { origin: entry.origin.id, raw: null, deleted: true }); else testDrafts.delete(key);
          route = { name: "main" }; menuOpen = false; message = "Test deleted locally. Enter saves; Restore brings it back."; render();
        }, "mi mi-danger");
        for (const node of [duplicate, remove]) node.setAttribute("role", "menuitem");
        menu.append(duplicate, remove); bar.append(menu);
      }
    }
    const nameLine = el(doc, "div", "name-line");
    let nameHint;
    if (editableTest && draft()?.origin === null) {
      const control = testNameControl(doc,
        () => ({ name: draft().name, disabled: busy || blocked, invalid: nameHintKey === key }), value => {
          draft().name = value; draft().raw.id = portableId(value);
          if (value.trim()) nameHintKey = undefined;
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
      resultLine.setAttribute("data-tone", local() ? "old" : c?.state ?? "none");
      resultLine.setAttribute("data-result", c?.state ?? "none");
    });
    function inspectFor(caseInfo) {
      if (blocked) return "Result unconfirmed";
      if (testing()) return "Tests running…";
      if (caseInfo.label === "Not in this run") return "Not in the last run";
      if (!["pass", "fail"].includes(caseInfo.state)) return caseInfo.row ? caseInfo.label : base.latest.status === "stale" && !history.last ? "Out of date" : "Not run yet";
      const model = modelLabel(caseInfo.previous ? caseInfo.previousModel : base.latest.result?.modelRoute);
      const when = local() ? caseInfo.previous ? "Out of date · last run" : "before your edit" : lastRun && !runAlert ? "just now" : "last run";
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
      fieldKeys(input); updates.push(() => { input.disabled = busy || blocked; growTextarea(input); });
      gets.append(label);
    } else gets.append(el(doc, "h2", "lbl", "The agent gets"), el(doc, "pre", "example-input literal", c.example.input));
    out.push(gets);

    // Passes if
    const checksSection = el(doc, "section", "detail-section checks-section");
    checksSection.append(el(doc, "h2", "lbl", "Passes if"));
    const checks = el(doc, "div", "checks");
    const facts = c && !blocked && draft()?.origin !== null ? failureFacts(c) : undefined;
    const displayChecks = uniqueIndex(c?.example.assertions ?? [], checkKey);
    const assertions = editableTest ? current().assertions : c.example.assertions;
    assertions.forEach((a, index) => {
      const display = editableTest ? displayChecks.get(checkKey(a)) : a;
      const observed = display && c?.expected?.get(checkKey(display)) === display ? c.checks?.get(checkKey(display)) : undefined;
      const row = el(doc, "div", "check"), state = el(doc, "span", "check-state"), verdict = el(doc, "span", "check-verdict");
      const judge = a.type === "llmJudge", reason = judge ? el(doc, "div", "judge-reason") : undefined;
      const judgeResult = judge ? el(doc, "div", "judge-result") : undefined;
      updates.push(() => {
        let kind = "none", label = "No result";
        const completed = !ambiguousPassSet(base) && (c?.state === "pass" || c?.state === "fail");
        if (completed && !blocked && draft()?.origin !== null && observed?.verdict === "pass") [kind, label] = ["pass", "Passed"];
        else if (completed && !blocked && draft()?.origin !== null && observed?.verdict === "fail") [kind, label] = ["fail", "Failed"];
        else if (judge && !ambiguousPassSet(base) && !blocked && draft()?.origin !== null && inconclusiveJudge(c, observed)) [kind, label] = ["error", "Inconclusive"];
        const text = kind === "none" ? "" : label;
        if (kind !== "none" && local()) [kind, label] = [`old-${kind}`, `${label} last run`];
        state.replaceChildren(statusIcon(doc, kind, label));
        verdict.textContent = text ? `${judge ? "Judge " : ""}${text}${judge && local() ? " · last run" : ""}` : "";
        verdict.setAttribute("data-tone", kind);
        if (reason) { reason.textContent = kind === "none" ? "" : observed.reason; judgeResult.hidden = kind === "none"; judgeResult.className = local() ? "judge-result old" : "judge-result"; }
      });
      const known = editableTest && EDITABLE_TYPES.some(([t]) => t === a.type);
      if (known) {
        const editKey = `${key}/${a.id}`, edit = el(doc, "div", "check-edit");
        const fields = checkFields(doc, a, index, (field, value) => {
          editingChecks.add(editKey); mutate(d => { d.raw.assertions[index][field] = value; });
        }, fieldKeys);
        const line = button("", () => { editingChecks.add(editKey); refresh(); fields.focus(); }, "check-line");
        line.append(state, el(doc, "span", "check-label", checkLabel({ type: a.type, fields: [[operandKey(a.type), a[operandKey(a.type)] ?? ""]] })));
        const choice = checkTypeControl(doc, a.type, chosen => {
          const wasJudge = current().assertions[index].type === "llmJudge";
          editingChecks.add(editKey); mutate(d => convertCheck(d.raw.assertions[index], chosen));
          if (wasJudge || chosen === "llmJudge") {
            render(); root.querySelectorAll(".check-type-button")[index]?.focus();
          } else { fields.show(current().assertions[index]); refresh(); }
        }, cancelDraft);
        const remove = button("", () => { mutate(d => { d.raw.assertions.splice(index, 1); }); render(); }, "ib remove-check");
        remove.setAttribute("aria-label", `Remove check ${index + 1}`); remove.append(icon(doc, "none", 14, 2));
        edit.append(remove, choice.node, fields.node, ...(judge ? [] : [verdict])); row.append(line, edit);
        updates.push(() => {
          const editing = draft()?.origin === null || editingChecks.has(editKey);
          line.hidden = editing; edit.hidden = !editing;
          fields.update(busy || blocked); choice.update(busy || blocked);
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
        body.append(details); row.append(state, body);
      }
      if (editableTest && !known) {
        const remove = button("", () => { mutate((d) => { d.raw.assertions.splice(index, 1); }); render(); }, "ib remove-check");
        remove.setAttribute("aria-label", `Remove check ${index + 1}`); remove.setAttribute("title", "Remove check");
        remove.append(icon(doc, "trash", 14, 2)); row.append(remove);
      }
      checks.append(row);
      if (judgeResult) { judgeResult.append(verdict, reason); checks.append(judgeResult); }
      const checkFacts = display && c && !blocked && draft()?.origin !== null ? failureFacts(c, display) : undefined;
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
    if (c && !blocked && draft()?.origin !== null && c.row) {
      const answered = el(doc, "section", "detail-section answered");
      answered.append(el(doc, "h2", "lbl", "The agent answered"));
      // Unfinished or unrecognised observations keep their literal state and verdict.
      if (c.state === "unknown") answered.append(el(doc, "p", "meta result-details", `State: ${c.row.state || "unknown"} · verdict: ${c.row.verdict || "unknown"}`));
      if (c.row.answerExcerpt !== undefined) {
        answered.append(answerBlock(c.row, local, facts?.actual, updates));
      } else if (c.row.state !== "complete") {
        answered.append(el(doc, "p", "answer-error", `No answer${c.row.reason ? `: ${c.row.reason}` : ""}`));
      } else {
        answered.append(el(doc, "p", "quiet", c.state === "pass"
          ? "Run this test to see its answer."
          : "Answer content not included. Run this test again to see it."));
      }
      out.push(answered);
    }
    if (runAlert?.one !== undefined) out.push(runAlertNode(button, "one"));

    // Footer: scoped Enter
    if (editableTest) {
      const foot = el(doc, "div", "detail-foot");
      foot.append(spinnerStatus(status, "one", updates));
      const right = el(doc, "div", "foot-actions");
      const cancel = button("", cancelDraft, "ib cancel-test", "nav");
      cancel.setAttribute("aria-label", "Cancel"); cancel.append(icon(doc, "close", 16, 2.2));
      cancel.setAttribute("title", "Discard this test's edits (Esc)");
      updates.push(() => { cancel.hidden = !draft(); });
      right.append(cancel, saveButton(button, "Save and run this test", () => submitOne(key)));
      foot.append(right); out.push(foot);
    } else {
      const foot = el(doc, "div", "detail-foot"); foot.append(status); out.push(foot);
      updates.push(() => { status.textContent = snap ? "This test can't be edited here: its name isn't unique." : footText(); });
    }
    function cancelDraft() {
      const d = draft();
      testDrafts.delete(key);
      for (const id of editingChecks) if (id.startsWith(`${key}/`)) editingChecks.delete(id);
      if (d?.origin === null) route = { name: "main" };
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

  function explanation(button, facts, local, index, mutate, eligible, updates) {
    const box = el(doc, "div", "why");
    updates.push(() => { box.className = local() ? "why old" : "why"; });
    box.append(el(doc, "span", "why-text", facts.text));
    if (facts.actual !== undefined && index >= 0) {
      const actions = el(doc, "div", "why-actions");
      const prompt = snap ? sectionText(snap.main.key) : "";
      // Only a literal, unique occurrence in the prompt can be swapped in place.
      if (prompt.split(facts.actual).length === 2) {
        actions.append(button("Apply to prompt", () => {
          if (!eligible()) return;
          sectionDrafts.set(snap.main.key, prompt.replace(facts.actual, () => facts.value));
          if (sectionDrafts.get(snap.main.key) === snap.main.text) sectionDrafts.delete(snap.main.key);
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

  const modelControl = runtimeModelControl(doc,
    () => ({ authored: base.model, selected: modelChoice.override, chatModel: base.chatModel, disabled: busy || blocked || invalidEditors.size > 0 }),
    options.fetchModels ?? (() => execute({ operation: "models" })), (value) => {
      if (busy || blocked) return;
      observationGeneration++;
      if (modelChoice.choose(value, base)) {
        base = modelChoice.view(base); lastRun = undefined; runAlert = undefined;
      }
      render(); modelControl.trigger.focus();
    });
  render();
  return {
    observe,
    observation,
    idle: () => !busy && !blocked && invalidEditors.size === 0,
    dispose() { observationGeneration++; for (const cleanup of cleanups.splice(0)) cleanup(); },
  };
}

async function main() {
  const root = document.getElementById("root");
  let app;
  try {
    app = renderView(document, root, await fetchView(), postAction, { fetchView });
  } catch {
    renderView(document, root, { error: true, message: "The agent view could not be loaded." });
    return;
  }
  // Watch the agent file only while the panel is visible and idle. Observation is
  // a GET of the same view; it never writes, runs a model or clears a lock.
  // One GET at a time: a slow read is never stacked with another.
  let reading = false;
  setInterval(async () => {
    if (reading || document.visibilityState !== "visible" || !app.idle()) return;
    reading = true;
    const started = app.observation();
    try { app.observe(await fetchView(), started); } catch { /* Try again on the next tick. */ } finally { reading = false; }
  }, 4000);
}
if (typeof document !== "undefined" && document.getElementById) main();
