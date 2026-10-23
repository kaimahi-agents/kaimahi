// Resource and authoring values are untrusted: render only inert DOM text.
import { instructionSections } from "./authoring.mjs";
const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
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

function heroState(payload, cases) {
  const { latest } = payload, r = latest.result;
  if (latest.status === "none") return ["none", "No current result", "Ask Copilot to test this agent."];
  if (latest.status === "stale") return ["stale", "Result out of date", "Test this revision to see a current result."];
  const failing = cases.filter((c) => c.state === "fail").length;
  if (failing) return ["fail", `${failing} failing`, failing === 1 ? "Ask Copilot to fix the failing example." : "Ask Copilot to fix the failing examples."];
  const ids = r.selection.caseIds;
  const allSelected = ids.length === cases.length && new Set(ids).size === ids.length && cases.every((c) => ids.includes(c.example.id));
  const full = latest.badge === "pass" && cases.length > 0 && allSelected &&
    r.selection.totalCases === cases.length && r.examples.length === cases.length && cases.every((c) => c.state === "pass");
  if (full) return ["pass", `Passing ${cases.length}/${cases.length}`, "Ask Copilot to try another input."];
  const selected = cases.filter((c) => ids.includes(c.example.id));
  const subset = r.selection.scope !== "full" && r.selection.scope !== "" && r.state === "complete" &&
    r.verdict === "pass" && ids.length > 0 && new Set(ids).size === ids.length &&
    selected.length === ids.length && r.examples.length === ids.length && selected.every((c) => c.state === "pass");
  if (subset) return ["warning", "Selected examples passed", "Test all saved examples for a full result."];
  return ["warning", "Incomplete result", "Test all saved examples for a complete result."];
}

function resultDetails(doc, row) {
  const details = el(doc, "details", "result-details");
  details.append(el(doc, "summary", "", "Result details"),
    el(doc, "p", "", `State: ${row.state}; verdict: ${row.verdict}`));
  if (row.reason) details.append(el(doc, "p", "result-reason", row.reason));
  for (const a of row.assertions ?? []) {
    details.append(el(doc, "p", "", `${a.id} [${a.type}]: ${a.verdict}${a.reason ? ` — ${a.reason}` : ""}`));
  }
  return details;
}
function pathControls(doc, path) {
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
  box.append(el(doc, "p", "path-label", "Path from selected root"), el(doc, "code", "path-value", path), button, feedback);
  return box;
}

function purposeText(payload) {
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

// The resource already bounds instructions. Keep fences opaque before looking
// for duplicate metadata, so quoted headings never hide authored content.
function instructionBlocks(source) {
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
function inlineText(doc, node, text) {
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

function instructionsContent(doc, payload) {
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

// Only our inline spans and browser plain-text line containers are accepted.
// Unknown formatting blocks saving rather than silently losing source content.
function editorValue(root) {
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
    if (tag === "br") return "\n";
    const children = [...node.childNodes];
    if (["div", "p"].includes(tag) && node !== root && children.length === 1 && children[0].tagName?.toLowerCase() === "br") return "";
    if (inlineNodes.has(node) && children.some((child) => child.nodeType !== 3)) throw new Error("Unsupported inline formatting.");
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

const friendlyName = (id) => {
  const text = id.replace(/[-_]+/g, " ");
  return text ? text[0].toUpperCase() + text.slice(1) : "Unnamed example";
};
const clone = (value) => JSON.parse(JSON.stringify(value));
const operandKey = (type) => type === "regex" ? "pattern" : type === "llmJudge" ? "rubric" : "value";
const checkLabel = (a) => {
  const value = new Map(a.fields).get(operandKey(a.type)) ?? "";
  return a.type === "contains" ? `Includes "${value}"` : a.type === "notContains" ? `Does not include "${value}"`
    : a.type === "regex" ? `Matches pattern "${value}"` : a.type === "llmJudge" ? friendlyName(a.id) : `Condition: ${a.type}`;
};
function frameStatus(payload, cases) {
  const [state, label] = heroState(payload, cases), r = payload.latest.result;
  const ids = r?.selection.caseIds ?? [];
  const complete = payload.latest.status === "current" && r.state === "complete" && r.verdict === "fail" && r.selection.scope === "full" &&
    cases.length > 0 && ids.length === cases.length && new Set(ids).size === ids.length &&
    r.selection.totalCases === cases.length && r.examples.length === cases.length &&
    cases.every((c) => ids.includes(c.example.id) && ["pass", "fail"].includes(c.state) &&
      c.expected?.size === c.checks?.size && [...c.expected].every(([key, a]) =>
        a && ["pass", "fail"].includes(c.checks.get(key)?.verdict)));
  if (state === "pass") return ["pass", `${cases.length} of ${cases.length} passing`];
  if (complete && state === "fail") return ["fail", `${cases.filter((c) => c.state === "pass").length} of ${cases.length} passing`];
  return ["neutral", state === "fail" ? "Partial or incomplete result" : label];
}
function answerContent(doc, row) {
  const box = el(doc, "div", "answer");
  if (row?.answerExcerpt !== undefined) {
    const truncated = row.truncated || /…\s*\[truncated\]$/.test(row.answerExcerpt);
    box.append(el(doc, "span", "label", `Last answer · untrusted excerpt${truncated ? " (truncated)" : ""}`),
      el(doc, "pre", "answer-excerpt", row.answerExcerpt));
  } else box.append(el(doc, "p", "quiet", "Answer content not included. Test again to see failing answers; select Include passing answers for passing cases."));
  return box;
}
function exampleCard(doc, c, draft) {
  const e = c.example, state = draft ? "waiting" : c.state;
  const box = el(doc, "article", `example case-${state}`);
  box.setAttribute("data-example-id", e.id);
  const top = el(doc, "div", "example-head");
  top.append(el(doc, "h3", "", friendlyName(e.id)), el(doc, "span", "case-status", draft || c.label));
  box.append(top);
  if (state !== "fail") {
    box.classList.add("compact-example");
    box.append(el(doc, "p", "example-preview", `${e.input} → ${e.assertions.map(checkLabel).join("; ")}`));
    if (state === "pass" && c.row?.answerExcerpt !== undefined) {
      const details = el(doc, "details", "last-answer");
      details.append(el(doc, "summary", "", "Last answer"), answerContent(doc, c.row)); box.append(details);
    }
  } else {
    const flow = el(doc, "div", "flow"), input = el(doc, "div"), expected = el(doc, "div");
    input.append(el(doc, "span", "label", "The agent gets"), el(doc, "pre", "value", e.input));
    expected.append(el(doc, "span", "label", "A good answer"));
    const checks = el(doc, "div", "check-surface");
    for (const a of e.assertions) {
      const matched = c.expected?.get(checkKey(a)) === a ? c.checks?.get(checkKey(a)) : undefined;
      const line = el(doc, "div", "check");
      if (matched && ["pass", "fail"].includes(matched.verdict)) {
        line.append(el(doc, "span", matched.verdict, matched.verdict === "pass" ? "✓ " : "✕ "));
      }
      line.append(el(doc, "span", "", checkLabel(a)));
      if (a.type === "llmJudge") line.append(el(doc, "small", "quiet", " · judge"));
      checks.append(line);
    }
    expected.append(checks); flow.append(input, expected); box.append(flow, answerContent(doc, c.row));
    for (const a of e.assertions) {
      const matched = c.expected?.get(checkKey(a)) === a ? c.checks?.get(checkKey(a)) : undefined;
      if (a.type === "contains" && matched?.verdict === "fail") {
        box.append(el(doc, "p", "missing", `Missing "${new Map(a.fields).get("value") ?? ""}".`));
      }
    }
  }
  if (!draft && c.row && c.state === "unknown") box.append(resultDetails(doc, c.row));
  const metadata = e.assertions.filter((a) => a.type === "llmJudge" || !["contains", "notContains", "regex"].includes(a.type));
  if (metadata.length) {
    const details = el(doc, "details", "check-details");
    details.append(el(doc, "summary", "", "Check details"));
    for (const a of metadata) {
      details.append(el(doc, "p", "", `${a.id} · ${a.type}`));
      for (const [key, value] of a.fields) {
        const field = el(doc, "pre");
        if (a.type === "llmJudge" && key === "rubric") inlineText(doc, field, `${key}: ${value}`);
        else field.textContent = `${key}: ${value}`;
        details.append(field);
      }
    }
    box.append(details);
  }
  return box;
}
async function postAction(action) {
  const response = await fetch("actions", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(action),
  });
  return response.json();
}
function insertPlainText(doc, node, text) {
  const selection = doc.getSelection();
  let range = selection?.rangeCount ? selection.getRangeAt(0) : undefined;
  if (!range || !node.contains(range.commonAncestorContainer)) {
    range = doc.createRange(); range.selectNodeContents(node); range.collapse(false);
  }
  range.deleteContents();
  const inserted = doc.createTextNode(text);
  range.insertNode(inserted); range.setStartAfter(inserted); range.collapse(true);
  selection?.removeAllRanges(); selection?.addRange(range);
}

export function renderView(doc, root, payload, execute = postAction) {
  if (!payload || payload.error || !payload.file) {
    root.replaceChildren(el(doc, "p", "error", payload?.message || "The agent view could not be loaded."));
    return;
  }
  let sections;
  try {
    if (payload.authoring?.editable === true && typeof payload.authoring.source === "string") {
      sections = instructionSections(payload.authoring.source);
    }
  } catch { /* Old or noncanonical resources remain inspection-only. */ }
  const mainSection = sections?.find((s) => /accountable/i.test(s.key));
  const editable = !!mainSection && sections.length === 3 && Array.isArray(payload.authoring?.examples);
  const cases = inspectCases(payload), [statusKind, statusLabel] = frameStatus(payload, cases);
  const controls = [], editors = [], baseline = new Map();
  let busy = false, blocked = false, dirty = false, invalidEditor = false, formOpen = false, adHocResult;
  const button = (label, action, className = "", guard = "clean") => {
    const node = el(doc, "button", className, label); node.type = "button";
    node.addEventListener("click", action); controls.push({ node, guard }); return node;
  };
  const message = el(doc, "p", "action-message"); message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
  const status = el(doc, "span", `status-pill ${statusKind}`, statusLabel);
  const editedLabel = el(doc, "span", "quiet instruction-status", editable ? "Saved instructions" : "Read-only inspection");
  const formHost = el(doc, "div", "form-host");
  const exampleList = el(doc, "div", "examples");
  const update = () => {
    root.setAttribute("aria-busy", String(busy));
    try {
      const values = editors.map(({ node, key }) => [key, editorValue(node)]);
      dirty = values.some(([key, value]) => value !== baseline.get(key)); invalidEditor = false;
    } catch {
      dirty = true; invalidEditor = true;
      message.textContent = "Unsupported editor formatting. Your draft is kept; reload before saving.";
    }
    status.textContent = dirty || formOpen ? "Edited · not tested yet" : blocked ? "Reload required · result unconfirmed" : statusLabel;
    status.className = `status-pill ${dirty || formOpen || blocked ? "neutral" : statusKind}`;
    editedLabel.textContent = busy ? "Working…" : dirty ? "Edited · not tested yet" : blocked ? "Reload required" : editable ? "Saved instructions" : "Read-only inspection";
    for (const { node, guard } of controls) {
      node.disabled = busy || (guard !== "reload" && (blocked || invalidEditor || (guard === "clean" && (dirty || formOpen)) || (guard === "test" && formOpen)));
    }
    for (const { node } of editors) {
      node.setAttribute("contenteditable", busy || blocked || formOpen ? "false" : "plaintext-only");
      node.setAttribute("aria-readonly", String(busy || blocked || formOpen));
    }
    for (const node of formHost.querySelectorAll("input, textarea, select, button")) node.disabled = busy || blocked;
    include.disabled = busy || blocked;
    runInput.disabled = busy || blocked || dirty || formOpen;
    if (adHocResult) adHocResult.hidden = dirty || formOpen || blocked;
    testButton?.setAttribute("aria-label", dirty ? "Save and test" : "Test saved examples");
  };
  const drawExamples = () => {
    for (let i = controls.length - 1; i >= 0; i--) {
      if (exampleList.contains(controls[i].node)) controls.splice(i, 1);
    }
    exampleList.replaceChildren();
    for (const c of [...cases].sort((a, b) => Number(b.state === "fail") - Number(a.state === "fail"))) {
      const card = exampleCard(doc, c, dirty || formOpen ? "Not tested yet" : blocked ? "Result unconfirmed" : false);
      if (editable) {
        const actions = el(doc, "div", "example-actions");
        // Only a unique full raw identity may be edited; display fields never become a save baseline.
        const full = payload.authoring.examples;
        const raw = uniqueIndex(full, (e) => e.id).get(c.example.id);
        if (raw) {
          actions.append(button("Edit", () => openExample(full.indexOf(raw)), "text-button"));
          if (full.length > 1) actions.append(button("Delete", () => act("save-examples", {
            examples: clone(full.filter((e) => e !== raw)),
          }), "text-button"));
        }
        card.append(actions);
      }
      exampleList.append(card);
    }
  };
  const matches = (view) => view && !view.error && view.file?.path === payload.file.path;
  async function act(operation, values = {}) {
    if (busy || ((blocked || invalidEditor) && operation !== "reload")) return;
    if (operation !== "reload" && operation !== "save-and-test" && (dirty || formOpen && operation !== "save-examples")) return;
    busy = true; message.textContent = "Working…"; update();
    const action = { operation, ...values };
    if (!["reload", "suggest"].includes(operation)) action.expectedRevision = payload.file.revision;
    if (["save-and-test", "test", "run"].includes(operation)) action.showContent = include.checked;
    try {
      const response = await execute(action);
      if (response?.error) {
        const conflict = ["revision_mismatch", "stale_revision", "revision-mismatch", "stale-revision"].includes(response.error);
        const failedPreview = response.error === "action-failed" && ["test", "run"].includes(operation);
        blocked = failedPreview || conflict || response.saved === true || ["outcome-unknown", "uncertain-outcome", "uncertain_outcome"].includes(response.error);
        message.textContent = conflict ? "Changed elsewhere. Your draft is still here. Reload and discard draft to continue."
          : response.saved ? "Saved, but not tested for this revision. Your draft is still here. Reload before continuing."
          : response.message || "The action was refused. Your draft is still here.";
      } else if (operation === "suggest") {
        message.textContent = "Suggestion requested in Copilot. No draft was changed.";
      } else if (matches(response?.view)) {
        renderView(doc, root, response.view, execute); return;
      } else {
        blocked = true; message.textContent = "Outcome not confirmed. Do not resubmit. Reload deliberately to inspect the saved file.";
      }
    } catch {
      blocked = true; message.textContent = "Connection lost. The action may have completed. Do not resubmit. Your draft is still here; reload to inspect the saved file.";
    } finally { busy = false; if (blocked) drawExamples(); update(); }
  }
  function makeEditor(section) {
    const node = el(doc, "div", "instruction-body");
    for (const [index, block] of instructionBlocks(section.text).entries()) {
      if (index) node.append("\n");
      if (block.code) node.append(block.raw);
      else inlineText(doc, node, block.raw);
    }
    node.setAttribute("role", "textbox"); node.setAttribute("aria-multiline", "true");
    node.setAttribute("aria-label", section.key); node.setAttribute("contenteditable", "plaintext-only");
    node.setAttribute("spellcheck", "true"); baseline.set(section.key, section.text); editors.push({ key: section.key, node });
    node.addEventListener("input", () => { update(); drawExamples(); update(); });
    node.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey && !event.isComposing) {
        event.preventDefault(); if (!busy && !blocked) test();
      }
    });
    for (const kind of ["paste", "drop"]) node.addEventListener(kind, (event) => {
      event.preventDefault(); if (busy || blocked || formOpen) return;
      const transfer = kind === "paste" ? event.clipboardData : event.dataTransfer;
      insertPlainText(doc, node, transfer?.getData("text/plain") ?? ""); update(); drawExamples(); update();
    });
    return node;
  }
  function test() {
    if (formOpen || invalidEditor) return;
    act(dirty ? "save-and-test" : "test", dirty ? { sections: editors.map(({ key, node }) => ({ key, text: editorValue(node) })) } : {});
  }
  function openExample(index) {
    if (busy || blocked || dirty || formOpen) return;
    if (index === undefined && payload.authoring.examples.length >= 128) {
      message.textContent = "Keep at most 128 examples. Edit or delete an existing example first."; return;
    }
    formOpen = true;
    const full = clone(payload.authoring.examples), existing = index !== undefined;
    const draft = existing ? full[index] : { id: "", input: "", assertions: [] };
    const form = el(doc, "form", "example-form"), fields = el(doc, "div", "form-fields"), checks = el(doc, "div", "form-checks");
    const error = el(doc, "p", "error"); error.setAttribute("role", "alert");
    const field = (label, tag, value, parent = fields) => {
      const wrap = el(doc, "label", "form-field"), node = el(doc, tag);
      node.value = value; wrap.append(el(doc, "span", "label", label), node); parent.append(wrap); return node;
    };
    const name = field(existing ? "Example ID" : "Name (converted to a portable ID)", "input", draft.id);
    name.readOnly = existing; name.required = true;
    const input = field("The agent gets", "textarea", draft.input); input.required = true;
    const ids = new Set(draft.assertions.map((a) => a.id));
    const nextId = () => { let i = 1; while (ids.has(`check-${i}`)) i++; const id = `check-${i}`; ids.add(id); return id; };
    const rows = [];
    function addCheck(assertion) {
      const row = el(doc, "div", "form-check"), controls = el(doc, "div", "check-fields");
      const known = ["contains", "notContains", "regex"].includes(assertion.type);
      const state = { assertion, row }; rows.push(state);
      if (known) {
        const select = field("Check type", "select", "", controls);
        for (const type of ["contains", "notContains", "regex"]) {
          const option = el(doc, "option", "", type); option.value = type; select.append(option);
        }
        select.value = assertion.type;
        const value = field("Literal value or pattern", "textarea", assertion[operandKey(assertion.type)] ?? "", controls);
        value.required = true; state.select = select; state.value = value;
      } else {
        controls.append(el(doc, "p", "", `${assertion.id} · ${assertion.type} (read-only)`));
        const details = el(doc, "details"); details.append(el(doc, "summary", "", "Existing check details"));
        for (const [key, value] of Object.entries(assertion)) details.append(el(doc, "pre", "", `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`));
        controls.append(details);
      }
      const remove = el(doc, "button", "text-button", "Remove check"); remove.type = "button";
      remove.addEventListener("click", () => { rows.splice(rows.indexOf(state), 1); row.remove(); });
      row.append(controls, remove); checks.append(row);
    }
    draft.assertions.forEach(addCheck);
    if (!existing) addCheck({ id: nextId(), type: "contains", value: "" });
    const add = el(doc, "button", "", "Add text check"); add.type = "button";
    add.addEventListener("click", () => { if (rows.length < 64) addCheck({ id: nextId(), type: "contains", value: "" }); });
    const save = el(doc, "button", "primary", "Save example"); save.type = "submit";
    const cancel = el(doc, "button", "", "Cancel"); cancel.type = "button";
    cancel.addEventListener("click", () => { formOpen = false; formHost.replaceChildren(); drawExamples(); update(); });
    form.addEventListener("submit", (event) => {
      event.preventDefault(); if (busy || blocked || dirty) return;
      const id = existing ? draft.id : name.value.trim().toLowerCase().replace(/\s+/g, "-");
      const reserved = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/;
      if (!existing && (!/^[a-z][a-z0-9-]{0,62}$/.test(id) || reserved.test(id) || full.some((e) => e.id === id))) {
        error.textContent = "Use a unique name: start with a letter, then letters, numbers or hyphens, up to 63 characters."; return;
      }
      if (!rows.length) { error.textContent = "Keep at least one check."; return; }
      draft.id = id; draft.input = input.value;
      draft.assertions = rows.map(({ assertion, select, value }) => {
        const updated = clone(assertion);
        if (select) {
          const oldKey = operandKey(updated.type), newKey = operandKey(select.value);
          if (oldKey !== newKey) delete updated[oldKey];
          updated.type = select.value; updated[newKey] = value.value;
        }
        return updated;
      });
      const examples = clone(full);
      if (existing) examples[index] = draft; else examples.push(draft);
      act("save-examples", { examples });
    });
    const actions = el(doc, "div", "form-actions"); actions.append(add, cancel, save);
    form.append(el(doc, "h3", "", existing ? "Edit example" : "Add example"), fields, checks, error, actions);
    formHost.replaceChildren(form); drawExamples(); update(); name.focus();
  }

  const displayName = payload.title.split(" — ")[0] || payload.title;
  const toolbar = el(doc, "header", "toolbar"), breadcrumb = el(doc, "div", "breadcrumb");
  breadcrumb.append(el(doc, "span", "wordmark", "kmx"), el(doc, "span", "", "Agents"), el(doc, "span", "", "/"), el(doc, "span", "breadcrumb-name", displayName));
  const source = el(doc, "details", "source-details"), info = el(doc, "div", "source-info");
  const sourceSummary = el(doc, "summary", "file-chip", `${payload.file.path.split("/").pop()} · Selected root`);
  sourceSummary.setAttribute("aria-label", "Source details"); source.append(sourceSummary);
  info.append(el(doc, "p", "", payload.title), pathControls(doc, payload.file.path), el(doc, "code", "revision", payload.file.revision));
  controls.push({ node: info.querySelector(".copy-path"), guard: "copy" });
  source.append(info); toolbar.append(breadcrumb, source);
  const content = el(doc, "div", "content"), header = el(doc, "section", "agent-header"), identity = el(doc, "div");
  identity.append(el(doc, "span", "eyebrow", "Your agent"), el(doc, "h1", "", displayName));
  const purpose = purposeText(payload), summary = el(doc, "p", "purpose");
  const chars = Array.from(purpose); summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : purpose;
  identity.append(summary);
  if (chars.length > 160) info.append(el(doc, "p", "", purpose));
  const pills = el(doc, "div", "header-pills"); pills.append(status);
  if (payload.model) pills.append(el(doc, "span", "model-pill", `Model · ${payload.model}`));
  header.append(identity, pills);

  const instructions = el(doc, "section", "instructions-section"), instructionHeading = el(doc, "div", "section-line");
  instructionHeading.append(el(doc, "h2", "", "Instructions"), editedLabel);
  const panel = el(doc, "div", "instruction-panel");
  if (editable) {
    panel.append(makeEditor(mainSection));
    const additional = el(doc, "details", "additional");
    additional.append(el(doc, "summary", "", "Additional instructions"));
    for (const section of sections.filter((s) => s !== mainSection)) {
      additional.append(el(doc, "h3", "", section.key), makeEditor(section));
    }
    panel.append(additional);
  } else {
    panel.append(instructionsContent(doc, payload));
    instructions.append(el(doc, "p", "quiet", "Read-only inspection. This engine or agent file does not provide a complete editable snapshot."));
  }
  const footer = el(doc, "div", "instruction-footer");
  const testButton = editable ? button("↵ Test", test, "primary", "test") : undefined;
  if (editable) footer.append(el(doc, "span", "quiet", "Enter saves and tests · Shift+Enter for a new line"), testButton);
  panel.append(footer); instructions.append(instructionHeading, panel);

  const includeLabel = el(doc, "label", "include-answers"), include = el(doc, "input"); include.type = "checkbox"; include.checked = false;
  includeLabel.append(include, doc.createTextNode("Include passing answers"));
  const runInput = el(doc, "textarea"); runInput.setAttribute("aria-label", "Ad-hoc input"); runInput.rows = 2;
  const actionsLine = el(doc, "div", "actions-line"), run = el(doc, "details", "run-disclosure");
  if (editable) {
    run.append(el(doc, "summary", "", "Try another input"), runInput, button("Run", () => act("run", { input: runInput.value })));
    actionsLine.append(includeLabel, el(doc, "small", "answer-disclosure quiet", "Failing answers are shown by default"), run, button("Reload and discard draft", () => act("reload"), "text-button", "reload"));
  }
  const examples = el(doc, "section", "examples-section"), heading = el(doc, "div", "section-line examples-heading"), headingText = el(doc, "div", "heading-text");
  headingText.append(el(doc, "h2", "", "Examples"), el(doc, "span", "quiet", "What the agent gets, and what a good answer does"));
  heading.append(headingText);
  if (editable) {
    const actions = el(doc, "div", "heading-actions");
    actions.append(button("Suggest more", () => act("suggest")), button("Add example", () => openExample()));
    heading.append(actions);
  }
  drawExamples(); examples.append(heading, formHost, exampleList);
  content.append(header, instructions);
  if (editable) content.append(actionsLine, message);
  const adHoc = payload.latest.status === "current" ? payload.latest.result?.adHoc : undefined;
  if (adHoc) {
    adHocResult = el(doc, "section", "ad-hoc-result");
    adHocResult.append(el(doc, "h2", "", "Last ad-hoc run"), el(doc, "p", "", `${adHoc.state} · ${adHoc.verdict}`), answerContent(doc, adHoc));
    content.append(adHocResult);
  }
  content.append(examples); root.replaceChildren(toolbar, content); update();
}

async function main() {
  const root = document.getElementById("root");
  try {
    const res = await fetch("view.json", { cache: "no-store", credentials: "omit" });
    renderView(document, root, await res.json());
  } catch {
    renderView(document, root, { error: true, message: "The agent view could not be loaded." });
  }
}
if (typeof document !== "undefined" && document.getElementById) main();
