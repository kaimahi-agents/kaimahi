// Resource and authoring values are untrusted: render only inert DOM text.
import { instructionSections, runtimeModelSelection } from "./authoring.mjs";
import { el, inlineText, editorValue, insertPlainText, fillEditor, purposeText, instructionsContent, runtimeModelControl, pathControls } from "./editor.mjs";
const checkKey = (a) => JSON.stringify([a.id, a.type]);
function uniqueIndex(items, key) {
  const map = new Map();
  for (const item of items) {
    const id = key(item);
    map.set(id, map.has(id) ? null : item);
  }
  return map;
}
function inspectCases(payload) {
  const r = payload.latest.status === "current" ? payload.latest.result : undefined;
  const authored = uniqueIndex(payload.examples, (e) => e.id);
  const observed = uniqueIndex(r?.examples ?? [], (e) => e.id);
  const selected = uniqueIndex(r?.selection.caseIds ?? [], (id) => id);
  return payload.examples.map((e) => {
    const base = { example: e, state: "waiting", label: payload.latest.status === "stale" ? "Out of date" : "No current result" };
    if (!r) return base;
    if (!selected.has(e.id) && r.selection.scope !== "full") return { ...base, label: "Not in this run" };
    const row = authored.get(e.id) === e && selected.get(e.id) === e.id ? observed.get(e.id) : undefined;
    const unknown = { ...base, state: "unknown", label: "Incomplete", row };
    if (!row) return unknown;
    const expected = uniqueIndex(e.assertions, checkKey);
    const checks = uniqueIndex(row.assertions, checkKey);
    const ambiguous = [...expected.values(), ...checks.values()].some((v) => v === null);
    const matched = e.assertions.every((a) => checks.get(checkKey(a))?.verdict === "pass");
    const covered = expected.size === checks.size && [...checks.keys()].every((key) => expected.has(key));
    const complete = row.state === "complete";
    let state = "unknown";
    if (!ambiguous && complete && row.verdict === "fail") state = "fail";
    if (!ambiguous && complete && row.verdict === "pass" && covered && matched) state = "pass";
    return { ...unknown, state, label: state === "pass" ? "Passed" : state === "fail" ? "Failed" : "Incomplete", checks, expected };
  });
}

const friendlyName = (id) => {
  const text = id.replace(/[-_]+/g, " ");
  return text ? text[0].toUpperCase() + text.slice(1) : "Unnamed test";
};
const clone = (value) => JSON.parse(JSON.stringify(value));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const operandKey = (type) => type === "regex" ? "pattern" : type === "llmJudge" ? "rubric" : "value";
const EDITABLE_TYPES = [["contains", "Contains"], ["notContains", "Does not contain"], ["regex", "Matches regex"]];
const quoted = (text) => `“${text}”`;
const fieldValue = (a) => new Map(a.fields).get(operandKey(a.type)) ?? "";
const checkLabel = (a) => {
  const value = fieldValue(a);
  return a.type === "contains" ? `Contains ${quoted(value)}` : a.type === "notContains" ? `Does not contain ${quoted(value)}`
    : a.type === "regex" ? `Matches regex ${quoted(value)}` : a.type === "llmJudge" ? friendlyName(a.id) : `Condition: ${a.type}`;
};
// Static icon geometry copied from the approved board builder; never derived from resource text.
const SVG_NS = "http://www.w3.org/2000/svg";
const RING = ["circle", { cx: "12", cy: "12", r: "9" }];
const ICONS = {
  fail: [RING, ["path", { d: "M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6" }]],
  pass: [RING, ["path", { d: "M8.2 12.4l2.6 2.6 5-5.4" }]],
  error: [RING, ["path", { d: "M12 7.6v5.6" }], ["path", { d: "M12 16.4h.01" }]],
  none: [["path", { d: "M8 12h8" }]],
  running: [["path", { d: "M12 3a9 9 0 0 1 9 9" }]],
  enter: [["path", { d: "M20 5v7a3 3 0 0 1-3 3H5" }], ["path", { d: "M9 11l-4 4 4 4" }]],
  chevron: [["path", { d: "M9 6l6 6-6 6" }]],
  back: [["path", { d: "M15 6l-6 6 6 6" }]],
  plus: [["path", { d: "M12 5v14M5 12h14" }]],
  more: [["circle", { cx: "5", cy: "12", r: "1" }], ["circle", { cx: "12", cy: "12", r: "1" }], ["circle", { cx: "19", cy: "12", r: "1" }]],
  trash: [["path", { d: "M4 7h16" }], ["path", { d: "M10 11v6M14 11v6" }], ["path", { d: "M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12" }], ["path", { d: "M9 7V4h6v3" }]],
  sync: [["path", { d: "M21 12a9 9 0 0 1-15.5 6.2" }], ["path", { d: "M3 12A9 9 0 0 1 18.5 5.8" }], ["path", { d: "M18 2v4h-4" }], ["path", { d: "M6 22v-4h4" }]],
  warn: [["path", { d: "M12 3.5l9.5 16.5h-19z" }], ["path", { d: "M12 10v4.5" }], ["path", { d: "M12 17.4h.01" }]],
};
function icon(doc, name, size, strokeWidth, className) {
  const svg = doc.createElementNS(SVG_NS, "svg");
  for (const [key, value] of Object.entries({ width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": strokeWidth, "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true", focusable: "false" })) {
    svg.setAttribute(key, String(value));
  }
  if (className) svg.setAttribute("class", className);
  for (const [tag, attrs] of ICONS[name]) {
    const shape = doc.createElementNS(SVG_NS, tag);
    for (const [key, value] of Object.entries(attrs)) shape.setAttribute(key, value);
    svg.append(shape);
  }
  return svg;
}
function statusIcon(doc, kind, label) {
  const base = kind.replace(/^old-/, "");
  const mark = el(doc, "span", `status-icon ${kind}`);
  mark.setAttribute("role", "img"); mark.setAttribute("aria-label", label); mark.setAttribute("title", label);
  mark.append(icon(doc, base === "running" ? "running" : base, 14, 2.2, base === "running" ? "spin" : ""));
  return mark;
}

// Expected/got wording uses only the actual excerpt. A differing reference is
// named only when the answer holds exactly one same-anchored phrase, e.g. the
// expected "Fixes #218" against an answer that literally says "Closes #218".
const oneLine = (text, max = 80) => {
  const flat = text.replace(/\s+/g, " ").trim(), chars = Array.from(flat);
  return chars.length > max ? chars.slice(0, max - 1).join("") + "…" : flat;
};
export function provenActual(expected, excerpt) {
  const m = /^(\S+)\s+(\S*\d\S*)$/.exec(expected ?? "");
  if (!m || typeof excerpt !== "string") return;
  const anchor = m[2].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const found = new Set();
  for (const hit of excerpt.matchAll(new RegExp(`(?:^|\\s)(\\S+)\\s+${anchor}(?=$|[\\s.,;:!?)\\]])`, "g"))) {
    const word = hit[1].replace(/^[^\p{L}\p{N}]+/u, "");
    if (word) found.add(`${word} ${m[2]}`);
  }
  const [only] = found;
  return found.size === 1 && only !== expected && excerpt.split(only).length === 2 ? only : undefined;
}
function failureFacts(c) {
  for (const a of c.example.assertions) {
    const got = c.expected?.get(checkKey(a)) === a ? c.checks?.get(checkKey(a)) : undefined;
    if (got?.verdict !== "fail") continue;
    const value = fieldValue(a), excerpt = c.row?.answerExcerpt;
    const actual = a.type === "contains" ? provenActual(value, excerpt) : undefined;
    const tail = actual !== undefined ? `, got ${quoted(actual)}` : typeof excerpt === "string" && excerpt.trim() ? `, got ${quoted(oneLine(excerpt))}` : "";
    const text = a.type === "contains" ? `Expected ${quoted(value)}${tail}` : a.type === "notContains" ? `Expected no ${quoted(value)}${tail}`
      : a.type === "regex" ? `Expected a match for ${quoted(value)}${tail}` : `Not met: ${checkLabel(a)}`;
    return { assertion: a, text, actual, value };
  }
}

// One line per authored test. A local edit keeps the known shape and count but
// marks it old; missing, unselected or stale observations never become verdicts.
function testRow(c, local, blocked, malformed = false) {
  if (malformed) return { kind: "none", base: "none", label: "Incomplete", note: "Incomplete result", count: "incomplete" };
  if (blocked) return { kind: "none", base: "none", label: "Result unconfirmed", note: "" };
  if (!c) return { kind: "none", base: "none", label: "Not run", note: "", count: "" };
  let base = "none", label = c.label, note = "", count = "";
  if (c.state === "pass") [base, label, count] = ["pass", "Passed", "passing"];
  else if (c.state === "fail") [base, label, note, count] = ["fail", "Failed", failureFacts(c)?.text || c.row?.reason || "Failed", "failing"];
  // Only an observed unfinished row earns the amber "!"; ambiguous or uncovered complete rows stay neutral.
  else if (c.state === "unknown" && c.row?.state !== undefined && c.row.state !== "complete") {
    [base, label, count] = ["error", "Didn't finish", "didn't finish"];
    note = `Didn't finish${c.row.reason ? `: ${c.row.reason}` : ""}`;
  } else if (c.state === "unknown") [label, note, count] = ["Incomplete", "Incomplete result", "incomplete"];
  else if (c.label === "Not in this run") [note, count] = [c.label, "not run"];
  const old = local && base !== "none";
  return { kind: old ? `old-${base}` : base, base, label: old ? `${label} last run` : label, note, count };
}
function coherentSelection(payload) {
  const result = payload.latest.result;
  if (!result) return false;
  const selected = result.selection.caseIds, known = new Set(payload.examples.map((e) => e.id));
  const observed = result.examples.map((e) => e.id);
  return result.selection.totalCases === payload.examples.length && known.size === payload.examples.length
    && new Set(selected).size === selected.length && selected.every((id) => known.has(id))
    && new Set(observed).size === observed.length && observed.every((id) => selected.includes(id))
    && ["full", "subset"].includes(result.selection.scope);
}
function ambiguousPassSet(payload) {
  return payload.latest.status === "current" && !coherentSelection(payload)
    && inspectCases(payload).every((c) => c.state === "pass");
}
function countText(rows) {
  const counts = new Map();
  for (const r of rows) counts.set(r.count, (counts.get(r.count) ?? 0) + 1);
  return ["failing", "passing", "didn't finish", "incomplete", "not run"]
    .filter((k) => counts.get(k)).map((k) => `${counts.get(k)} ${k}`).join(", ");
}
const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
const modelText = (model) => model.match(/^model: (.+)$/m)?.[1] ?? model.replace(/\n/g, " · ");
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
  let showContent = false, filter = "all", newCount = 0, menuOpen = false, cleanups = [], controls = [], includeNode;
  let refresh = () => {};
  // A revision does not distinguish successive runs of unchanged source.
  let observationGeneration = 0;
  const modelChoice = runtimeModelSelection();
  base = modelChoice.view(base);
  const observation = () => ({ generation: observationGeneration, revision: base.file.revision });

  const editable = () => !!snap;
  const matches = (view) => view && !view.error && view.file?.path === payload.file.path;
  const hasDrafts = () => sectionDrafts.size > 0 || testDrafts.size > 0 || invalidEditors.size > 0;
  const rawExamples = () => snap ? base.authoring.examples : [];
  const sectionText = (key) => sectionDrafts.get(key) ?? snap.sections.find((s) => s.key === key).text;
  // Display rows and their unique raw join; ambiguous IDs stay visible but not editable.
  function entries() {
    const cases = inspectCases(base), raws = uniqueIndex(rawExamples(), (e) => e.id), ids = uniqueIndex(base.examples, (e) => e.id);
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
    if (raw.assertions.some((a) => EDITABLE_TYPES.some(([t]) => t === a.type) && !String(a[operandKey(a.type)] ?? "").trim())) return "Fill in every check.";
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
    if (includeNode) showContent = includeNode.checked === true;
    const executing = ["save-and-test", "test", "run"].includes(operation);
    if (executing) {
      action.showContent = showContent;
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
      message = "Suggestion requested in Copilot chat. Nothing here changed.";
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
    view = modelChoice.view(view);
    if (same(view, base) || (candidate && same(view, candidate))) return false;
    observationGeneration++;
    lastRun = undefined;
    if (view.file.revision === base.file.revision || !hasDrafts()) {
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
    controls = []; includeNode = undefined;
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

  function header(button) {
    const displayName = base.title.split(" — ")[0] || base.title;
    // Quiet file name and model sit beside the title. Their disclosure keeps the
    // path, revision, other canonical sections and privacy controls available.
    const fileName = base.file.path.split("/").pop(), model = base.model ? modelText(base.model) : "";
    const source = el(doc, "details", "source-details"), info = el(doc, "div", "source-info");
    const sourceSummary = el(doc, "summary", "meta agent-meta", model ? `${fileName} · ${model}` : fileName);
    sourceSummary.setAttribute("title", "Source details"); source.append(sourceSummary, info);
    info.append(el(doc, "p", "source-title", base.title), pathControls(doc, base.file.path), el(doc, "code", "revision", base.file.revision));
    const head = el(doc, "header", "agent-header"), titleLine = el(doc, "div", "title-line");
    titleLine.append(el(doc, "h1", "", displayName), source);
    if (snap) {
      modelControl.update(); controls.push({ node: modelControl.select, guard: "idle" }); titleLine.append(modelControl.node);
    }
    const purpose = purposeText(base), summary = el(doc, "p", "purpose");
    const chars = Array.from(purpose); summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : purpose;
    head.append(titleLine, summary);
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
    node.addEventListener("input", changed);
    node.addEventListener("keydown", (event) => {
      if (event.isComposing) return;
      if (event.key === "Enter" && !event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey) {
        event.preventDefault(); route.name === "detail" ? submitOne(route.key) : submitAll();
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
    if (scope === "one") return "Enter saves and runs this test only";
    if (!n) return hasDrafts() ? "Edited · Enter saves" : "Saved · add a test to run it";
    if (hasDrafts()) return `Edited · Enter saves and runs ${tests}`;
    if (runAlert?.saved && !runAlert.finished) return "Saved · tests didn't run";
    if (lastRun && !lastRun.one) return `Saved · ran ${tests} just now`;
    return `Saved · Enter runs ${tests}${base.latest.status === "current" ? " again" : ""}`;
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
    const { head, info } = header(button);
    const promptSection = el(doc, "section", "prompt-section"), promptBox = el(doc, "div", "prompt-box");
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
    const includeLabel = el(doc, "label", "include-answers"), include = el(doc, "input"); include.type = "checkbox"; include.checked = showContent;
    include.addEventListener("change", () => { showContent = include.checked; });
    includeLabel.append(include, doc.createTextNode("Include passing answers"));
    updates.push(() => { include.disabled = busy || blocked; });
    includeNode = include;
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
    box.append(includeLabel, el(doc, "small", "answer-disclosure quiet", "Failing answers are shown by default"), run,
      button("Reload and discard draft", () => act("reload"), "tb", "reload"));
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
    const summaryNode = el(doc, "span", "meta tests-summary"), list = el(doc, "ul", "test-list"), note = el(doc, "p", "filter-note");
    title.append(el(doc, "h2", "lbl", "Tests"));
    const group = el(doc, "div", "filter-group"); group.setAttribute("role", "group"); group.setAttribute("aria-label", "Show tests");
    const filterButtons = ["all", "problems"].map((value) => {
      const node = el(doc, "button", "filter-button"); node.type = "button";
      node.addEventListener("click", () => { filter = value; draw(); refresh(); });
      group.append(node); return node;
    });
    title.append(group, summaryNode); head.append(title);
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
      empty.append(text, actions, el(doc, "span", "meta", "Suggest tests asks Copilot in the chat. Nothing here changes until the agent file does."));
    }
    section.append(empty, list, note);
    const rowControls = [];
    const draw = () => {
      for (const node of rowControls.splice(0)) if (controls.includes(node)) controls.splice(controls.indexOf(node), 1);
      const all = entries(), anyDraft = hasDrafts();
      const shown = all.map((e) => ({ e, r: e.draft?.origin === null ? { kind: "none", base: "none", label: "Not run", note: "", count: "" } : testRow(e.c, anyDraft, blocked, ambiguousPassSet(base)) }));
      const counted = shown.filter(({ e }) => !e.draft?.deleted);
      const notPassing = counted.filter(({ r }) => r.base !== "pass").length;
      filterButtons.forEach((node, i) => {
        node.replaceChildren(`${i ? "Not passing" : "All"} `, el(doc, "span", "count", String(i ? notPassing : counted.length)));
        node.setAttribute("aria-pressed", String(filter === (i ? "problems" : "all")));
      });
      list.replaceChildren(); note.replaceChildren();
      let hidden = 0;
      for (const { e, r } of shown) {
        const item = el(doc, "li", "test-item"), id = e.raw?.id ?? e.c?.example.id ?? "";
        item.setAttribute("data-test-id", id); item.setAttribute("data-status", r.kind);
        item.hidden = filter === "problems" && r.base === "pass" && !e.draft; hidden += Number(item.hidden);
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
        open.append(statusIcon(doc, r.kind, r.label), el(doc, "span", "test-name", draftName(e)), el(doc, "span", e.draft ? "test-note unsaved" : "test-note", noteText));
        if (e.openable) { const chevron = el(doc, "span", "chevron"); chevron.append(icon(doc, "chevron", 14, 2)); open.append(chevron); }
        item.append(open); list.append(item);
      }
      if (hidden) {
        const show = el(doc, "button", "tb", "Show all"); show.type = "button";
        show.addEventListener("click", () => { filter = "all"; draw(); refresh(); });
        note.append(`${plural(hidden, "passing test")} hidden `, show);
      }
      empty.hidden = !snap || shown.length > 0;
      list.hidden = shown.length === 0;
      const text = countText(counted.map(({ r }) => r));
      const malformed = !!base.latest.result && !coherentSelection(base);
      summaryNode.textContent = blocked ? "Result unconfirmed" : !shown.length ? "" : busy && ["test", "save-and-test"].includes(pending?.operation) ? "Running…"
        : base.latest.status === "stale" ? "Result out of date" : base.latest.status !== "current" || !text ? "Not run yet"
        : (base.latest.result.state !== "complete" || malformed) && !anyDraft && !/didn't finish|incomplete/.test(text) ? `Incomplete run: ${text}`
        : anyDraft ? `Last run, before your edit: ${text}` : lastRun && !lastRun.one && !runAlert ? `${text} · just now` : text;
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
    route = { name: "detail", key }; render();
  }

  // `actual` is a proven unique substring of the excerpt; it is marked, never rewritten.
  function answerBlock(row, local, actual, updates) {
    if (row.answerExcerpt === undefined) return el(doc, "p", "quiet", "Answer content not included.");
    const box = el(doc, "div", "answer");
    const truncated = row.truncated || /…\s*\[truncated\]$/.test(row.answerExcerpt ?? "");
    const label = el(doc, "span", "label");
    updates.push(() => { label.textContent = `Untrusted excerpt, shown as text${truncated ? " (truncated)" : ""}${local() ? " · last run" : ""}`; });
    box.append(label);
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
    const local = hasDrafts;
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
    if (editableTest) {
      const more = button("", () => { menuOpen = !menuOpen; render(); }, "ib more", "nav");
      more.setAttribute("aria-label", "More actions"); more.setAttribute("aria-haspopup", "menu"); more.setAttribute("aria-expanded", String(menuOpen));
      more.setAttribute("title", "More actions"); more.append(icon(doc, "more", 15, 2.6));
      bar.append(more);
      if (menuOpen) {
        const menu = el(doc, "div", "menu"); menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "More actions");
        const duplicate = button("Duplicate test", () => {
          const copy = clone(current()), name = `${draftName(entry)} copy`; copy.id = portableId(name);
          const newKey = `new:${++newCount}`; testDrafts.set(newKey, { origin: null, name, raw: copy });
          route = { name: "detail", key: newKey }; menuOpen = false; render();
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
    if (editableTest && draft()?.origin === null) {
      const name = el(doc, "input", "test-title-input"); name.type = "text"; name.value = draft().name;
      name.setAttribute("aria-label", "Test name"); name.setAttribute("placeholder", "Name this test"); name.setAttribute("spellcheck", "false");
      name.addEventListener("input", () => { draft().name = name.value; draft().raw.id = portableId(name.value); refresh(); });
      fieldKeys(name); updates.push(() => { name.disabled = busy || blocked; });
      nameLine.append(name);
    } else nameLine.append(el(doc, "h1", "test-title", draftName(entry)));
    const unsaved = el(doc, "span", "unsaved-tag", "Unsaved");
    nameLine.append(unsaved);
    const resultLine = el(doc, "span", "meta detail-status");
    updates.push(() => {
      unsaved.hidden = !draft();
      const r = c ? inspectFor(c) : undefined;
      resultLine.textContent = draft()?.origin === null ? "Not run yet" : !r ? "Not run yet" : r;
    });
    function inspectFor(caseInfo) {
      if (blocked) return "Result unconfirmed";
      if (base.latest.status === "stale") return "Result out of date";
      if (base.latest.status !== "current") return "Not run yet";
      if (caseInfo.label === "Not in this run") return "Not in the last run";
      if (local()) return "Results from the last run, before your edit";
      return lastRun && !runAlert ? "Results from the last run, just now" : "Results from the last run";
    }
    top.append(bar, nameLine, resultLine);
    out.push(top);

    // The agent gets
    const gets = el(doc, "section", "detail-section");
    if (editableTest) {
      const label = el(doc, "label", "lbl", "The agent gets"), input = el(doc, "textarea", "field example-input");
      input.value = current().input; input.rows = 2; input.setAttribute("spellcheck", "false");
      label.append(input);
      input.addEventListener("input", () => mutate((d) => { d.raw.input = input.value; }));
      fieldKeys(input); updates.push(() => { input.disabled = busy || blocked; });
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
      const row = el(doc, "div", "check"), state = el(doc, "span", "check-state");
      updates.push(() => {
        let kind = "none", label = "No result";
        const completed = !ambiguousPassSet(base) && (c?.state === "pass" || c?.state === "fail");
        if (completed && !blocked && draft()?.origin !== null && observed?.verdict === "pass") [kind, label] = ["pass", "Passed"];
        else if (completed && !blocked && draft()?.origin !== null && observed?.verdict === "fail") [kind, label] = ["fail", "Failed"];
        if (kind !== "none" && local()) [kind, label] = [`old-${kind}`, `${label} last run`];
        state.replaceChildren(statusIcon(doc, kind, label));
      });
      row.append(state);
      const known = editableTest && EDITABLE_TYPES.some(([t]) => t === a.type);
      if (known) {
        const select = el(doc, "select", "token"); select.setAttribute("aria-label", `Check ${index + 1} type`);
        for (const [type, text] of EDITABLE_TYPES) { const option = el(doc, "option", "", text); option.value = type; select.append(option); }
        select.value = a.type;
        const value = el(doc, "input", "field check-value"); value.type = "text"; value.value = a[operandKey(a.type)] ?? "";
        value.setAttribute("aria-label", `Check ${index + 1} text`); value.setAttribute("spellcheck", "false");
        const apply = () => mutate((d) => {
          const target = d.raw.assertions[index], oldKey = operandKey(target.type), newKey = operandKey(select.value);
          if (oldKey !== newKey) delete target[oldKey];
          target.type = select.value; target[newKey] = value.value;
        });
        select.addEventListener("change", apply); value.addEventListener("input", apply); fieldKeys(value);
        updates.push(() => { select.disabled = value.disabled = busy || blocked; });
        row.append(select, value);
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
        body.append(details); row.append(body);
      }
      if (editableTest) {
        const remove = button("", () => { mutate((d) => { d.raw.assertions.splice(index, 1); }); render(); }, "ib remove-check");
        remove.setAttribute("aria-label", `Remove check ${index + 1}`); remove.setAttribute("title", "Remove check");
        remove.append(icon(doc, "trash", 14, 2)); row.append(remove);
      }
      checks.append(row);
      if (facts && display === facts.assertion) {
        // Repairs belong to the saved assertion they explain, not a later type
        // or operand occupying the same input. Recheck even detached handlers.
        const eligible = () => {
          const now = current().assertions[index];
          return now?.id === facts.assertion.id && now.type === facts.assertion.type && now[operandKey(now.type)] === facts.value;
        };
        checks.append(explanation(button, facts, local, editableTest ? index : -1, mutate, eligible, updates));
      }
    });
    checksSection.append(checks);
    if (editableTest) {
      const add = button("", () => {
        mutate((d) => {
          const ids = new Set(d.raw.assertions.map((a) => a.id)); let i = 1; while (ids.has(`check-${i}`)) i++;
          if (d.raw.assertions.length < 64) d.raw.assertions.push({ id: `check-${i}`, type: "contains", value: "" });
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
          ? "Answer content not included for passing tests. Turn on Include passing answers in the source details, then run this test."
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
      const cancel = button("Cancel", cancelDraft, "tb tb-quiet", "nav");
      cancel.setAttribute("title", "Discard this test's edits (Esc)");
      updates.push(() => { cancel.hidden = !draft(); });
      right.append(cancel, saveButton(button, "Save and run this test", () => submitOne(key)));
      foot.append(right); out.push(foot);
    } else {
      const foot = el(doc, "div", "detail-foot"); foot.append(status); out.push(foot);
      updates.push(() => { status.textContent = snap ? "This test can't be edited here: its name isn't unique." : footText(); });
    }
    function cancelDraft() {
      const d = draft(); if (!d) return;
      testDrafts.delete(key);
      if (d.origin === null) route = { name: "main" };
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
    box.append(el(doc, "span", "why-text", `${facts.text}.`));
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
    () => ({ authored: base.model, selected: modelChoice.override, needsRun: modelChoice.needsRun }),
    options.fetchModels ?? (() => execute({ operation: "models" })), (value) => {
      if (busy || blocked) return;
      observationGeneration++;
      if (modelChoice.choose(value, base)) {
        base = modelChoice.view(base); lastRun = undefined; runAlert = undefined;
      }
      render(); modelControl.select.focus();
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
