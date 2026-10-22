// Renders the read-only agent view. Every value from the resource is untrusted
// and becomes inert DOM text, never HTML or active content.
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
function latestSection(doc, payload, cases) {
  const [state, label, hint] = heroState(payload, cases);
  const failing = state === "fail";
  const box = el(doc, "section", `latest ${failing ? "hero status bad" : "status-inline"} is-${state}${state === "none" ? " empty" : ""}`);
  if (failing) {
    const symbol = el(doc, "span", "hero-symbol", "×");
    symbol.setAttribute("aria-hidden", "true");
    const content = el(doc, "div", "hero-content");
    content.append(el(doc, "strong", "badge fail", label), el(doc, "p", "hint", hint));
    box.append(symbol, content);
  } else {
    box.append(el(doc, "span", `badge status-pill ${state}${state === "pass" ? " good" : state === "warning" || state === "stale" ? " wait" : ""}`, label));
  }
  return box;
}

function fileLabel(path, displayName) {
  return `${path.startsWith(".squad/agents/") && path.endsWith("/charter.md") ? "Squad member" : "Agent file"} · ${displayName}`;
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
        node.append(el(doc, code ? "code" : "strong", "", part.slice(code ? 1 : 2, code ? -1 : -2)));
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

function checkContent(doc, a, c) {
  const box = el(doc, "div", "check");
  const fields = new Map(a.fields);
  const labels = { contains: "Expected to contain", notContains: "Expected not to contain", regex: "Expected to match pattern", llmJudge: "Expected to meet this rubric" };
  box.append(el(doc, "span", "check-label label", Object.hasOwn(labels, a.type) ? labels[a.type] : `Condition: ${a.type}`));
  const key = checkKey(a);
  const observed = c.expected?.get(key) === a ? c.checks?.get(key) : undefined;
  const failed = c.state === "fail" && observed?.verdict === "fail";
  const missing = failed && a.type === "contains";
  const operand = a.type === "regex" ? "pattern" : a.type === "llmJudge" ? "rubric" : "value";
  if (Object.hasOwn(labels, a.type) && fields.has(operand)) {
    box.append(el(doc, "pre", `assertion-value${missing ? " missing" : ""}`, fields.get(operand)));
  }
  if (failed) box.append(el(doc, "span", `check-status${missing ? " missing" : ""}`, missing ? "Missing" : "Check failed"));
  if (observed?.reason) box.append(el(doc, "p", "check-reason", observed.reason));
  if (a.type === "llmJudge" && fields.has("minScore")) box.append(el(doc, "p", "judge-score", `Minimum score ${fields.get("minScore")}/100`));
  const extras = a.fields.filter(([name]) => name !== operand && !(a.type === "llmJudge" && name === "minScore"));
  if (!Object.hasOwn(labels, a.type) || extras.length) {
    const details = el(doc, "details", a.type === "llmJudge" ? "judge-details" : "condition-details");
    details.append(el(doc, "summary", "", a.type === "llmJudge" ? "Judge details" : "Condition details"));
    const included = Object.hasOwn(labels, a.type) ? extras : a.fields;
    details.append(el(doc, "p", "condition-type", `Type: ${a.type}; ID: ${a.id}`));
    for (const [name, value] of included) details.append(el(doc, "label", "field-label", name), el(doc, "pre", a.type === "llmJudge" ? "field-value" : "assertion-value", value));
    box.append(details);
  }
  return box;
}
function exampleCard(doc, c, dense) {
  const e = c.example;
  const box = el(doc, "article", `example case-${c.state}${dense && c.state !== "fail" ? " compact-example" : ""}`);
  box.setAttribute("data-example-id", e.id);
  const top = el(doc, "div", "example-top example-head");
  const friendly = e.id.replace(/[-_]+/g, " ");
  const name = friendly ? friendly[0].toUpperCase() + friendly.slice(1) : "Unnamed example";
  top.append(el(doc, "h3", "case-title", name), el(doc, "span", "case-status result", c.label));
  const flow = el(doc, "div", "flow");
  const input = el(doc, "div", "input");
  input.append(el(doc, "span", "label", "Input"), el(doc, "pre", "value input-value", e.input));
  const arrow = el(doc, "span", "flow-arrow arrow", "→");
  arrow.setAttribute("aria-hidden", "true");
  const checks = el(doc, "div", "checks");
  for (const a of e.assertions) checks.append(checkContent(doc, a, c));
  flow.append(input, arrow, checks);
  box.append(top, flow);
  if (c.row && c.state === "unknown") box.append(resultDetails(doc, c.row));
  if (c.row?.answerExcerpt !== undefined) {
    const answer = el(doc, "details", "answer");
    const truncated = c.row.truncated || /…\s*\[truncated\]$/.test(c.row.answerExcerpt);
    answer.open = c.state === "fail";
    answer.append(el(doc, "summary", "", `Untrusted excerpt${truncated ? " (truncated)" : ""}`),
      el(doc, "pre", "answer-excerpt", c.row.answerExcerpt));
    box.append(answer);
  } else if (c.row) box.append(el(doc, "p", "content-unavailable", "Answer content not included."));
  return box;
}

export function renderView(doc, root, payload) {
  if (!payload || payload.error || !payload.file) {
    root.replaceChildren(el(doc, "p", "error", payload?.message || "The agent view could not be loaded."));
    return;
  }
  const header = el(doc, "header", "view-header author-header");
  const identity = el(doc, "div", "identity");
  const monogram = payload.title.match(/[\p{L}\p{N}]/u)?.[0].toUpperCase() || "?";
  const avatar = el(doc, "span", "avatar", monogram);
  avatar.setAttribute("aria-hidden", "true");
  const identityContent = el(doc, "div", "identity-content");
  const top = el(doc, "div", "identity-top");
  const model = el(doc, "p", "model");
  model.append("Model ", el(doc, "code", "", payload.model));
  const displayName = payload.title.split(" — ")[0] || payload.title;
  top.append(el(doc, "h1", "agent-name", displayName));
  const purpose = purposeText(payload);
  const summary = el(doc, "p", "purpose");
  inlineText(doc, summary, purpose);
  const chars = Array.from(summary.textContent);
  summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : chars.join("");
  identityContent.append(top, summary);
  identity.append(avatar, identityContent);
  const source = el(doc, "div", "source");
  const details = el(doc, "details", "source-details");
  const info = el(doc, "div", "source-info");
  info.append(el(doc, "p", "source-title", payload.title), pathControls(doc, payload.file.path), el(doc, "code", "revision", payload.file.revision));
  if (chars.length > 160) info.append(el(doc, "p", "source-purpose", purpose));
  const original = el(doc, "details", "authored-source");
  original.append(el(doc, "summary", "", "Original instruction text"), el(doc, "pre", "", payload.instructions));
  details.append(el(doc, "summary", "", "Source details"), info, original);
  const file = el(doc, "span", "file-label file-path", fileLabel(payload.file.path, displayName));
  source.append(file, model, details);
  header.append(identity, source);

  const cases = inspectCases(payload), dense = cases.length > 3;
  const status = latestSection(doc, payload, cases);
  const failing = cases.some((c) => c.state === "fail");
  const [state, , hint] = heroState(payload, cases);
  const guidance = failing ? status : el(doc, "p", "hint quiet", hint);
  if (!failing) {
    top.append(status);
    if (payload.latest.status === "current" && state === "warning") header.append(resultDetails(doc, payload.latest.result));
  }
  const examples = el(doc, "section", "examples-section");
  const heading = el(doc, "div", "section-line");
  heading.append(el(doc, "h2", "", "Examples"), el(doc, "small", "", "Saved checks"));
  const list = el(doc, "div", `examples${dense ? " dense" : ""}`);
  list.append(...cases.map((c) => exampleCard(doc, c, dense)));
  examples.append(heading, list);
  const instructions = el(doc, "section", "instructions-section editing-surface instruction authoring");
  const instructionHeading = el(doc, "div", "section-line");
  instructionHeading.append(el(doc, "h2", "", "Instructions"), el(doc, "small", "", "Read-only"));
  instructions.append(instructionHeading, instructionsContent(doc, payload));
  root.replaceChildren(header, instructions, guidance, examples);
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
