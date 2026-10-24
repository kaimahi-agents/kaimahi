// Inert DOM editing and exact source serialization. Loaders and actions are caller-owned.
import { effectiveModel } from "./authoring.mjs";
export const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
export const checkKey = (a) => JSON.stringify([a.id, a.type]);
export function uniqueIndex(items, key) {
  const map = new Map();
  for (const item of items) {
    const id = key(item);
    map.set(id, map.has(id) ? null : item);
  }
  return map;
}
export function inspectCases(payload) {
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

export const friendlyName = (id) => {
  const text = id.replace(/[-_]+/g, " ");
  return text ? text[0].toUpperCase() + text.slice(1) : "Unnamed test";
};
export const clone = (value) => JSON.parse(JSON.stringify(value));
export const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
export const operandKey = (type) => type === "regex" ? "pattern" : type === "llmJudge" ? "rubric" : "value";
export const EDITABLE_TYPES = [["contains", "Contains"], ["notContains", "Not contains"], ["regex", "Regex"]];
export const quoted = (text) => `“${text}”`;
const fieldValue = (a) => new Map(a.fields).get(operandKey(a.type)) ?? "";
export const checkLabel = (a) => {
  const value = fieldValue(a);
  return a.type === "contains" ? `Contains ${quoted(value)}` : a.type === "notContains" ? `Not contains ${quoted(value)}`
    : a.type === "regex" ? `Matches regex ${quoted(value)}` : a.type === "llmJudge" ? friendlyName(a.id) : `Condition: ${a.type}`;
};
// Repair suggestions use only the actual excerpt. A differing reference is
// named only when the answer holds exactly one same-anchored phrase, e.g. the
// expected "Fixes #218" against an answer that literally says "Closes #218".
export const oneLine = (text, max = 80) => {
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
export function failureFacts(c, assertion) {
  if (c.state !== "fail") return;
  for (const a of assertion ? [assertion] : c.example.assertions) {
    const got = c.expected?.get(checkKey(a)) === a ? c.checks?.get(checkKey(a)) : undefined;
    if (got?.verdict !== "fail") continue;
    const value = fieldValue(a), excerpt = c.row?.answerExcerpt;
    const actual = a.type === "contains" ? provenActual(value, excerpt) : undefined;
    const text = a.type === "contains" ? `Missing "${value}"` : a.type === "notContains" ? `Found "${value}"`
      : a.type === "regex" ? `No match for ${value}` : `Not met: ${checkLabel(a)}`;
    return { assertion: a, text, actual, value };
  }
}

// One line per authored test. A local edit keeps the known shape and count but
// marks it old; missing, unselected or stale observations never become verdicts.
export function testRow(c, local, blocked, malformed = false) {
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
export function coherentSelection(payload) {
  const result = payload.latest.result;
  if (!result) return false;
  const selected = result.selection.caseIds, known = new Set(payload.examples.map((e) => e.id));
  const observed = result.examples.map((e) => e.id);
  return result.selection.totalCases === payload.examples.length && known.size === payload.examples.length
    && new Set(selected).size === selected.length && selected.every((id) => known.has(id))
    && new Set(observed).size === observed.length && observed.every((id) => selected.includes(id))
    && ["full", "subset"].includes(result.selection.scope);
}
export function ambiguousPassSet(payload) {
  return payload.latest.status === "current" && !coherentSelection(payload)
    && inspectCases(payload).every((c) => c.state === "pass");
}
export function countText(rows) {
  const counts = new Map();
  for (const r of rows) counts.set(r.count, (counts.get(r.count) ?? 0) + 1);
  return ["failing", "passing", "didn't finish", "incomplete", "not run"]
    .filter((k) => counts.get(k)).map((k) => `${counts.get(k)} ${k}`).join(", ");
}
export const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
// Last-run display is controller-local, never a current resource or write input.
export function resultHistory(initial) {
  let last;
  const records = new Map();
  const remember = view => {
    const definitions = uniqueIndex(view.authoring?.examples ?? [], e => e.id);
    // Bound memory to this file's remaining case IDs, not every past revision.
    for (const id of records.keys()) if (!definitions.get(id)) records.delete(id);
    if (view.latest.status !== "current" || view.latest.result.modelRoute === "/" || !coherentSelection(view)) return;
    for (const c of inspectCases(view)) {
      const definition = definitions.get(c.example.id);
      if (definition && c.row) records.set(c.example.id, { definition, c, model: view.latest.result.modelRoute });
    }
    if (view.latest.result.examples.length) last = view;
  };
  remember(initial);
  return {
    get last() { return last; }, remember,
    cases(view) {
      const current = view.latest.status === "current", coherent = coherentSelection(view);
      const definitions = uniqueIndex(view.authoring?.examples ?? [], e => e.id), identities = uniqueIndex(view.examples, e => e.id);
      return inspectCases(view).map(c => {
        if (identities.get(c.example.id) !== c.example || current && (!coherent || c.label !== "Not in this run")) return c;
        const prior = records.get(c.example.id), definition = definitions.get(c.example.id);
        return prior && definition && same(prior.definition, definition)
          ? { ...prior.c, example: c.example, expected: uniqueIndex(c.example.assertions, checkKey), previous: true, previousModel: prior.model } : c;
      });
    },
  };
}
// Presentation inferred from familiar ID shapes, never catalog metadata or
// execution aliases. Unknown IDs remain literal, including provider prefixes.
export function modelPresentation(id) {
  let match = /^(gpt-\d+(?:\.\d+)*)(?:-(terra|sol|mini|nano|codex))?$/.exec(id);
  if (match) return { name: match[1].replace("gpt", "GPT") + (match[2] ? " " + friendlyName(match[2]) : ""), group: "OpenAI" };
  match = /^claude-(sonnet|haiku|opus)-(\d+(?:\.\d+)*)$/.exec(id);
  if (match) return { name: `Claude ${friendlyName(match[1])} ${match[2]}`, group: "Claude" };
  match = /^gemini-(\d+(?:\.\d+)*)-(pro|flash)$/.exec(id);
  return match ? { name: `Gemini ${match[1]} ${friendlyName(match[2])}`, group: "Gemini" } : { name: id, group: "Other models" };
}
export const modelLabel = route => modelPresentation(route?.slice(route.indexOf("/") + 1) ?? "").name;
export function resultPill(doc, settings) {
  const node = el(doc, "span", "result-pill"), dot = el(doc, "span", "result-dot"), text = el(doc, "span");
  dot.setAttribute("aria-hidden", "true"); node.append(dot, text);
  const update = () => { const state = settings(); node.setAttribute("data-tone", state.tone); text.textContent = state.text; };
  update(); return { node, update };
}
export function growTextarea(input) {
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight + 2}px`;
}
export function checkTypeControl(doc, initial, change, cancel) {
  const node = el(doc, "div", "check-type"), trigger = el(doc, "button", "check-type-button"), menu = el(doc, "div", "check-type-menu");
  trigger.type = "button"; trigger.setAttribute("aria-haspopup", "menu"); menu.setAttribute("role", "menu");
  const open = value => { menu.hidden = !value; trigger.setAttribute("aria-expanded", String(value)); };
  const show = type => {
    trigger.replaceChildren(EDITABLE_TYPES.find(([id]) => id === type)[1], icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `Check type: ${trigger.textContent}`);
  };
  const choices = EDITABLE_TYPES.map(([type, label], index) => {
    const option = el(doc, "button", "check-type-option", label); option.type = "button"; option.setAttribute("role", "menuitemradio");
    option.setAttribute("aria-checked", String(type === initial));
    option.addEventListener("click", () => {
      show(type); open(false);
      choices.forEach((item, i) => { item.setAttribute("aria-checked", String(i === index)); });
      change(type); trigger.focus();
    });
    option.addEventListener("keydown", event => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault(); choices[(index + (event.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length].focus();
      } else if (event.key === "Escape") { event.preventDefault(); open(false); trigger.focus(); }
    });
    return option;
  });
  trigger.addEventListener("click", () => open(menu.hidden));
  trigger.addEventListener("keydown", event => {
    if (event.key === "Escape") { event.preventDefault(); if (menu.hidden) cancel(); else open(false); }
    else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); open(true); choices[event.key === "ArrowDown" ? 0 : choices.length - 1].focus();
    }
  });
  node.addEventListener("focusout", event => { if (!node.contains(event.relatedTarget)) open(false); });
  menu.append(...choices); node.append(trigger, menu); show(initial); open(false);
  return { node, update(disabled) { for (const control of [trigger, ...choices]) control.disabled = disabled; if (disabled) open(false); } };
}

// Runtime selection only emits values to its owner; it cannot read or write an agent.
export function runtimeModelControl(doc, settings, load, change) {
  const node = el(doc, "div", "runtime-model"), trigger = el(doc, "button", "model-picker"), menu = el(doc, "div", "model-menu");
  const status = el(doc, "div", "quiet model-status"); status.setAttribute("role", "status");
  trigger.type = "button"; trigger.setAttribute("aria-haspopup", "menu");
  menu.setAttribute("role", "menu"); menu.setAttribute("aria-label", "Runtime models");
  let models = [], state = "idle", choices = [], opened = false;
  const open = value => { opened = value; menu.hidden = !value; trigger.setAttribute("aria-expanded", String(value)); };
  const bind = (item, value) => {
    item.addEventListener("click", () => { if (trigger.disabled) return; open(false); change(value); trigger.focus(); });
    item.addEventListener("keydown", event => {
      const index = choices.indexOf(item);
      if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault(); choices[event.key === "Home" ? 0 : event.key === "End" ? choices.length - 1 : (index + (event.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length].focus();
      } else if (event.key === "Escape") { event.preventDefault(); open(false); trigger.focus(); }
      else if (event.key === "Tab") open(false);
    });
    choices.push(item);
  };
  const update = () => {
    const { authored, selected, chatModel, disabled } = settings();
    const fallback = effectiveModel(authored, "", chatModel), current = selected || fallback;
    const origin = selected ? "this canvas" : authored ? "from the agent file" : "default";
    trigger.replaceChildren(el(doc, "span", "model-name", modelPresentation(current).name), el(doc, "span", "model-origin", ` · ${origin}`), icon(doc, "down", 12, 2));
    trigger.setAttribute("aria-label", `Runtime model: ${modelPresentation(current).name} · ${origin}`);
    trigger.setAttribute("title", `${current} · ${origin}`); trigger.disabled = !!disabled;
    const focused = choices.find(option => option === doc.activeElement)?.getAttribute("data-model"), wasOpen = opened;
    const ids = [...new Set([fallback, chatModel, selected, ...models].filter(Boolean))];
    const groups = new Map();
    for (const id of ids) {
      const group = modelPresentation(id).group;
      if (!groups.has(group)) groups.set(group, []);
      groups.get(group).push(id);
    }
    choices = []; menu.replaceChildren();
    for (const [name, ids] of groups) {
      const group = el(doc, "div", "model-options"); group.setAttribute("role", "group"); group.setAttribute("aria-label", name);
      group.append(el(doc, "div", "model-group", name));
      for (const id of ids) {
        const option = el(doc, "button", "model-option", modelPresentation(id).name); option.type = "button";
        option.setAttribute("role", "menuitemradio"); option.setAttribute("aria-checked", String(id === current));
        option.setAttribute("data-model", id); option.setAttribute("title", id); option.disabled = !!disabled;
        if (id === chatModel) option.append(el(doc, "span", "model-chat", "Current chat model"));
        bind(option, id); group.append(option);
      }
      menu.append(group);
    }
    status.textContent = state === "loading" ? "Loading models…" : state === "failed" ? "Choices unavailable; file/default still works." : "";
    // Following is a mode, not a duplicate entry for the fallback's model ID.
    const follow = el(doc, "button", "model-default", authored ? "Use agent file model" : chatModel ? "Follow chat model" : "Use default model");
    follow.type = "button"; follow.disabled = !!disabled; follow.setAttribute("role", "menuitem"); follow.setAttribute("data-model", "");
    bind(follow, ""); menu.append(follow, status);
    if (disabled) open(false);
    if (focused !== undefined && !disabled) { open(wasOpen); choices.find(option => option.getAttribute("data-model") === focused)?.focus(); }
  };
  const discover = async () => {
    if (state === "ready" || state === "loading") return;
    state = "loading"; update();
    try {
      const response = await load();
      if (response?.error || !Array.isArray(response?.models)) throw new Error("unavailable");
      models = response.models; state = "ready";
    } catch { state = "failed"; }
    update();
  };
  trigger.addEventListener("click", async () => { if (trigger.disabled) return; open(!opened); if (opened) await discover(); });
  trigger.addEventListener("keydown", event => {
    if (event.key === "Escape") { event.preventDefault(); open(false); }
    else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); open(true); discover(); choices[event.key === "ArrowDown" ? 0 : choices.length - 1]?.focus();
    }
  });
  node.addEventListener("focusout", event => { if (!node.contains(event.relatedTarget)) open(false); });
  node.append(trigger, menu); open(false); update(); return { node, trigger, update };
}

// Static icon geometry copied from the approved board builder; never derived from resource text.
const SVG_NS = "http://www.w3.org/2000/svg";
const RING = ["circle", { cx: "12", cy: "12", r: "9" }];
const ICONS = {
  fail: [["path", { d: "M6 6l12 12M18 6L6 18" }]],
  pass: [["path", { d: "M5 12.5l4.5 4.5L19 7.5" }]],
  error: [RING, ["path", { d: "M12 7.6v5.6" }], ["path", { d: "M12 16.4h.01" }]],
  none: [["path", { d: "M8 12h8" }]],
  running: [["path", { d: "M12 3a9 9 0 0 1 9 9" }]],
  enter: [["path", { d: "M20 5v7a3 3 0 0 1-3 3H5" }], ["path", { d: "M9 11l-4 4 4 4" }]],
  down: [["path", { d: "M6 9l6 6 6-6" }]],
  back: [["path", { d: "M15 6l-6 6 6 6" }]],
  close: [["path", { d: "M6 6l12 12M18 6L6 18" }]],
  plus: [["path", { d: "M12 5v14M5 12h14" }]],
  more: [["circle", { cx: "5", cy: "12", r: "1" }], ["circle", { cx: "12", cy: "12", r: "1" }], ["circle", { cx: "19", cy: "12", r: "1" }]],
  trash: [["path", { d: "M4 7h16" }], ["path", { d: "M10 11v6M14 11v6" }], ["path", { d: "M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12" }], ["path", { d: "M9 7V4h6v3" }]],
  sync: [["path", { d: "M21 12a9 9 0 0 1-15.5 6.2" }], ["path", { d: "M3 12A9 9 0 0 1 18.5 5.8" }], ["path", { d: "M18 2v4h-4" }], ["path", { d: "M6 22v-4h4" }]],
  warn: [["path", { d: "M12 3.5l9.5 16.5h-19z" }], ["path", { d: "M12 10v4.5" }], ["path", { d: "M12 17.4h.01" }]],
};
export function icon(doc, name, size, strokeWidth, className) {
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
export function statusIcon(doc, kind, label) {
  const base = kind.replace(/^old-/, "");
  const mark = el(doc, "span", `status-icon ${kind}`);
  mark.setAttribute("role", "img"); mark.setAttribute("aria-label", label); mark.setAttribute("title", label);
  mark.append(icon(doc, base === "running" ? "running" : base, 14, 2.2, base === "running" ? "spin" : ""));
  return mark;
}

export function testNameControl(doc, settings, change) {
  const input = el(doc, "input", "test-title-input"), hint = el(doc, "p", "name-hint", "Name this test.");
  input.type = "text"; input.value = settings().name;
  input.setAttribute("aria-label", "Test name"); input.setAttribute("placeholder", "Name this test");
  input.setAttribute("spellcheck", "false"); input.setAttribute("aria-describedby", "test-name-hint");
  hint.setAttribute("id", "test-name-hint");
  input.addEventListener("input", () => change(input.value));
  const update = () => {
    const { disabled, invalid } = settings();
    input.disabled = disabled; input.setAttribute("aria-invalid", String(invalid)); hint.hidden = !invalid;
  };
  update(); return { input, hint, update };
}

export function pathControls(doc, path) {
  const box = el(doc, "div", "source-path");
  const button = el(doc, "button", "copy-path", "Copy path");
  button.setAttribute("type", "button");
  button.setAttribute("aria-label", "Copy agent path");
  button.setAttribute("data-action", "copy-path");
  const feedback = el(doc, "span", "copy-status");
  feedback.setAttribute("role", "status");
  feedback.setAttribute("aria-live", "polite");
  button.addEventListener("click", async () => {
    const unavailable = "Copy unavailable. Select the path and copy it manually.";
    try {
      const clipboard = doc.defaultView?.navigator?.clipboard;
      if (typeof clipboard?.writeText !== "function") {
        feedback.textContent = unavailable;
        return;
      }
      await clipboard.writeText(path);
      feedback.textContent = "Path copied.";
    } catch {
      feedback.textContent = unavailable;
    }
  });
  box.append(el(doc, "p", "path-label", "Agent file path"), el(doc, "code", "path-value", path), button, feedback);
  return box;
}

// The resource already bounds instructions. Keep fences opaque before looking
// for duplicate metadata, so quoted headings never hide authored content.
export function instructionBlocks(source) {
  const lines = source.split("\n");
  const blocks = [];
  for (let i = 0; i < lines.length; i++) {
    const open = lines[i].match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
    if (!open || (open[1][0] === "`" && open[2].includes("`"))) {
      blocks.push({ raw: lines[i] });
      continue;
    }
    let end = i + 1;
    for (; end < lines.length; end++) {
      const close = lines[end].match(/^ {0,3}(`{3,}|~{3,})[ \t]*$/);
      if (close && close[1][0] === open[1][0] && close[1].length >= open[1].length) break;
    }
    const closed = end < lines.length;
    blocks.push({
      code: true,
      raw: lines.slice(i, closed ? end + 1 : end).join("\n"),
      // An incomplete block keeps even its opener visible, including truncation.
      text: lines.slice(closed ? i + 1 : i, end).join("\n"),
    });
    i = closed ? end : end - 1;
  }
  return blocks;
}

const inlineNodes = new WeakSet();
const caretPlaceholders = new WeakSet();
export function inlineText(doc, node, text) {
  for (const [index, line] of text.split("\n").entries()) {
    if (index) node.append("\n");
    // Unsupported syntax and indentation stay wholly literal. Do not pair
    // delimiters across lines or partially format a line with unmatched ones.
    const parts = line.split(/(`[^`\n]+`|\*\*[^*`\n]+\*\*)/g);
    if (/^(?: {4}|\t)/.test(line) || /[<>\[\]|]|``|\*{3}/.test(line) || parts.some((part, i) => i % 2 === 0 && /`|\*\*/.test(part))) {
      node.append(line);
      continue;
    }
    for (const [i, part] of parts.entries()) {
      if (i % 2 === 0) node.append(part);
      else {
        const code = part.startsWith("`");
        const span = el(doc, code ? "code" : "strong", "", part.slice(code ? 1 : 2, code ? -1 : -2));
        inlineNodes.add(span); node.append(span);
      }
    }
  }
}

// Only our inline spans and browser plain-text line containers are accepted.
// Unknown formatting blocks saving rather than silently losing source content.
export function editorValue(root) {
  let count = 0, size = 0;
  const read = (node, depth) => {
    if (++count > 8192 || depth > 32) throw new Error("Editor structure is too large.");
    if (node.nodeType === 3) {
      size += node.textContent.length;
      if (size > 262144) throw new Error("Editor text is too large.");
      return node.textContent;
    }
    const tag = node.tagName?.toLowerCase();
    if (node !== root && !["br", "div", "p"].includes(tag) && !inlineNodes.has(node)) throw new Error("Unsupported editor formatting.");
    if (tag === "br") return caretPlaceholders.has(node) ? "" : "\n";
    const children = [...node.childNodes];
    if (["div", "p"].includes(tag) && node !== root && children.length === 1 && children[0].tagName?.toLowerCase() === "br") return "";
    if (inlineNodes.has(node) && children.some((child) => child.nodeType !== 3 && !caretPlaceholders.has(child))) throw new Error("Unsupported inline formatting.");
    let value = "", previousBlock = false;
    for (const [index, child] of children.entries()) {
      const block = ["div", "p"].includes(child.tagName?.toLowerCase());
      if (index && (block || previousBlock) && (previousBlock || !value.endsWith("\n"))) value += "\n";
      value += read(child, depth + 1); previousBlock = block;
    }
    return inlineNodes.has(node) ? (tag === "code" ? "`" + value + "`" : "**" + value + "**") : value;
  };
  return read(root, 0);
}

export function insertPlainText(doc, node, text) {
  const selection = doc.getSelection();
  let range = selection?.rangeCount ? selection.getRangeAt(0) : undefined;
  if (!range || !node.contains(range.commonAncestorContainer)) {
    range = doc.createRange(); range.selectNodeContents(node); range.collapse(false);
  }
  range.deleteContents();
  // A collapsed root boundary belongs inside its adjacent line, not between
  // block containers (which the serializer already separates with an LF).
  if (range.startContainer === node && range.collapsed && node.childNodes.length) {
    const index = range.startOffset, line = node.childNodes[index === 0 ? 0 : index - 1];
    if (["div", "p"].includes(line.tagName?.toLowerCase())) range.setStart(line, index === 0 ? 0 : line.childNodes.length);
  }
  const container = range.startContainer;
  if (["div", "p"].includes(container?.tagName?.toLowerCase()) && container.childNodes.length === 1 && container.childNodes[0].tagName?.toLowerCase() === "br") {
    container.replaceChildren(); range.setStart(container, 0);
  }
  // Keep insertion before an existing terminal caret placeholder, including
  // selections supplied at the line/root end rather than by the browser.
  const tail = container.childNodes?.[container.childNodes.length - 1];
  if (caretPlaceholders.has(tail) && range.startOffset === container.childNodes.length) range.setStart(container, range.startOffset - 1);
  const inserted = doc.createTextNode(text);
  range.insertNode(inserted); range.setStartAfter(inserted);
  // Chromium collapses a terminal LF without a following rendered caret row.
  // This owned BR supplies that row without contributing authored source text.
  const following = [];
  let cursor = inserted;
  for (;;) {
    const parent = cursor.parentNode, siblings = [...parent.childNodes];
    following.push(...siblings.slice(siblings.indexOf(cursor) + 1));
    if (!inlineNodes.has(parent)) break;
    cursor = parent;
  }
  if (text.endsWith("\n")
      && following.every(child => caretPlaceholders.has(child) || child.nodeType === 3 && !child.textContent)
      && !following.some(child => caretPlaceholders.has(child))) {
    const placeholder = el(doc, "br"); caretPlaceholders.add(placeholder);
    range.insertNode(placeholder); range.setStartAfter(inserted);
  }
  range.collapse(true);
  selection?.removeAllRanges(); selection?.addRange(range);
}
// Each source line is its own block so a blank line can be drawn as a short
// paragraph gap; the serializer still reads it back as exactly one LF.
export function fillEditor(doc, node, text) {
  node.replaceChildren();
  for (const block of instructionBlocks(text)) {
    const line = el(doc, "div", block.code ? "line code-line" : "line");
    if (block.code) line.append(block.raw);
    else if (block.raw === "") line.append(el(doc, "br"));
    else inlineText(doc, line, block.raw);
    node.append(line);
  }
}

export function purposeText(payload) {
  const blocks = instructionBlocks(payload.instructions);
  for (const b of blocks) {
    if (b.code) continue;
    const match = b.raw.match(/^ {0,3}[-+*][ \t]+(?:\*\*Purpose:\*\*|Purpose:)[ \t]+(.+)$/);
    if (match) return match[1];
  }
  let inModel = false;
  for (const b of blocks) {
    if (b.code) continue;
    const heading = b.raw.match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*$/);
    if (heading) {
      if (heading[1].length <= 2) inModel = heading[2] === "Model";
      continue;
    }
    const line = b.raw.trim();
    if (inModel || !line || /^(?: {4}|\t)/.test(b.raw) ||
      /^(?:[-+*]>?|>|[\[<!]|(?:ID|Model|Preferred|Authored|Created|Written|By|Attribution|Co-authored)(?:\s|:))/i.test(line)) continue;
    return b.raw;
  }
  return "";
}

export function instructionsContent(doc, payload) {
  const blocks = instructionBlocks(payload.instructions);
  const first = blocks.findIndex((b) => b.raw.trim());
  const title = first >= 0 && !blocks[first].code && blocks[first].raw.match(/^ {0,3}#[ \t]+(.+?)\s*$/);
  if (title && title[1] === payload.title) blocks.splice(first, 1);
  const models = blocks.flatMap((b, i) => !b.code && b.raw === "## Model" ? [i] : []);
  if (models.length === 1 && !payload.model.endsWith("[truncated]")) {
    const start = models[0];
    let end = start + 1;
    while (end < blocks.length && (blocks[end].code || !/^#{1,2}[ \t]+/.test(blocks[end].raw))) end++;
    const body = blocks.slice(start + 1, end).map((b) => b.raw).join("\n").trim();
    if (body === `- **Preferred:** ${payload.model}`) blocks.splice(start, end - start);
  }
  const identities = blocks.flatMap((b, i) => !b.code && b.raw === "## Identity" ? [i] : []);
  const start = identities[0];
  let end = start === undefined ? blocks.findIndex((b) => !b.code && /^ {0,3}#{1,6}[ \t]+/.test(b.raw)) : start + 1;
  if (start !== undefined) {
    while (end < blocks.length && (blocks[end].code || !/^ {0,3}#{1,2}[ \t]+/.test(blocks[end].raw))) end++;
  } else if (end < 0) end = blocks.length;
  const id = payload.file.path.match(/^([a-z][a-z0-9-]*)\.agent\.md$/)?.[1] ??
    payload.file.path.match(/^\.squad\/agents\/([a-z][a-z0-9-]*)\/charter\.md$/)?.[1];
  const hidden = new Set();
  for (const [field, value] of [["ID", id], ["Purpose", purposeText(payload)]]) {
    const candidates = blocks.flatMap((b, i) => !b.code && b.raw.startsWith(`- **${field}:** `) ? [i] : []);
    if (!value || value.includes("[truncated]") || candidates.length !== 1 || (field === "Purpose" && Array.from(value).length > 160)) continue;
    const index = candidates[0];
    const inIdentity = identities.length === 1 && index > start && index < end;
    const inPrelude = identities.length === 0 && field === "Purpose" && index < end;
    if ((inIdentity || inPrelude) && blocks[index].raw === `- **${field}:** ${value}`) hidden.add(index);
  }
  // An Identity heading still owns any prose, code, subheadings or other fields.
  if (identities.length === 1 && [...hidden].some((i) => i > start && i < end) && blocks.slice(start + 1, end).every((b, i) => hidden.has(start + 1 + i) || (!b.code && !b.raw.trim()))) hidden.add(start);
  const box = el(doc, "div", "instructions instruction-copy");
  let paragraph = [], list;
  const flush = () => {
    if (paragraph.length) {
      const p = el(doc, "p");
      inlineText(doc, p, paragraph.join("\n"));
      box.append(p);
      paragraph = [];
    }
    list = undefined;
  };
  for (const [index, block] of blocks.entries()) {
    if (hidden.has(index)) continue;
    if (block.code) {
      flush();
      box.append(el(doc, "pre", "code-block", block.text));
      continue;
    }
    const line = block.raw;
    const heading = line.match(/^ {0,3}(#{1,6})[ \t]+(.+?)\s*$/);
    const bullet = line.match(/^ {0,3}[-+*][ \t]+(.+)$/);
    if (!line.trim()) flush();
    else if (heading) {
      flush();
      box.append(el(doc, heading[1].length <= 2 ? "h3" : "h4", "instruction-heading", heading[2]));
    } else if (bullet) {
      if (paragraph.length) flush();
      if (!list) {
        list = el(doc, "ul");
        box.append(list);
      }
      const item = el(doc, "li");
      inlineText(doc, item, bullet[1]);
      list.append(item);
    } else {
      list = undefined;
      paragraph.push(line);
    }
  }
  flush();
  return box;
}
