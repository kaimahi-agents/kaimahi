// Pure source transforms shared by the canvas and extension. MCP Edit remains
// the full-document validator; these helpers grant no parser/write authority.
const INSTRUCTIONS = [
  "Accountable Responsibilities",
  "Non-Responsibilities",
  "Collaboration and Review Authority",
];
const HEADINGS = ["Identity", ...INSTRUCTIONS, "Model", "Tools", "Examples"];
export const DEFAULT_MODEL = "gpt-5.6-terra";
export const effectiveModel = (authored, override, chatModel) => override || authored.match(/^model: (.+)$/m)?.[1] || authored || chatModel || DEFAULT_MODEL;
export const agentName = title => {
  const conventional = title.split(" — ")[0] || title;
  if (conventional !== title) return conventional;
  // Other dashes can belong to an authored name; only strip repeated names.
  const [name, suffix] = title.split(/ [–-] /), lower = name.toLowerCase();
  return suffix && (suffix.toLowerCase() === lower || suffix.toLowerCase().startsWith(lower + " ")) ? name : title;
};
export const fileLocation = path => { const parts = path.split(/[\\/]/); return { name: parts.pop(), folder: parts.pop() || "" }; };
// These owned refusals happen before route selection and contain no completion.
export const preRouteRefusal = (result) => result?.state === "refused" && result.verdict === "unknown" && result.fullPass === false
  && result.modelRoute === "/" && ["no_tests", "model_unavailable", "invalid_selection"].includes(result.reasonCode);

// Runtime state is separate from authored source and survives only this canvas.
// Raw file.revision remains agent-only; controller freshness binds the selected sources.
export const sourceIdentity = view => view?.selectedSources ? JSON.stringify([view.selectedSources.server, view.selectedSources.sourceVersion]) : view?.file?.revision;

export function runtimeModelSelection() {
  let override = "", needsRun = false, previous;
  const selected = (view) => effectiveModel(view.model, override, view.chatModel);
  const actual = (view) => {
    const route = view.latest.result?.modelRoute ?? "";
    return route.slice(route.indexOf("/") + 1);
  };
  return {
    get override() { return override; },
    get needsRun() { return needsRun; },
    choose(value, view) {
      const previous = selected(view); override = value;
      const changed = previous !== selected(view);
      if (changed) needsRun = true;
      return changed;
    },
    confirm(view) { if (actual(view) === selected(view) || preRouteRefusal(view.latest.result)) needsRun = false; },
    view(view, retained) {
      const next = selected(view);
      // Only controller-owned, revision-matched history can restore a picker result.
      // Callers still fence the observation generation before projecting it.
      const restored = retained && sourceIdentity(retained) === sourceIdentity(view) && retained.latest.status === "current"
        && (actual(retained) === next || preRouteRefusal(retained.latest.result));
      if (restored) { view = { ...view, latest: retained.latest }; needsRun = false; }
      // Late metadata matching the actual run is not a model change. Once a
      // different model is seen, A-B-A needs an explicit run, never a poll.
      if (!restored && previous !== undefined && previous !== next && actual(view) !== next) needsRun = true;
      previous = next;
      return view.latest.status === "current" && (needsRun || !preRouteRefusal(view.latest.result) && actual(view) !== selected(view))
        ? { ...view, latest: { status: "stale", badge: "stale" } } : view;
    },
  };
}
const MAX_BYTES = 256 * 1024;
const encoder = new TextEncoder();
const ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/;
const ASSERTION_FIELDS = {
  contains: ["value"],
  notContains: ["value"],
  regex: ["pattern"],
  toolCalled: ["tool"], toolNotCalled: ["tool"],
  llmJudge: ["rubric", "minScore", "judgeModel", "judgeVersion"],
};

function require(condition, message) {
  if (!condition) throw new Error(message);
}

function bounded(source) {
  require(typeof source === "string", "Source must be a string.");
  require(encoder.encode(source).length <= MAX_BYTES, "Source exceeds 256 KiB.");
  return source;
}

function fence(line) {
  const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
  if (!match || (match[1][0] === "`" && match[2].includes("`"))) return null;
  return { marker: match[1][0], count: match[1].length, info: match[2].trim() };
}

// All offsets index the original UTF-16 string, never normalized Markdown.
// Only canonical unindented H2s outside Go-compatible fences are boundaries.
function locateCharter(source) {
  const sections = new Map();
  let offset = 0, title = false, current = null, open = null;
  while (offset < source.length) {
    const newline = source.indexOf("\n", offset);
    const next = newline < 0 ? source.length : newline + 1;
    const line = source.slice(offset, newline < 0 ? next : newline).replace(/\r$/, "");
    const marker = fence(line);
    if (open) {
      if (marker && marker.marker === open.marker && marker.count >= open.count && !marker.info) open = null;
      offset = next;
      continue;
    }
    if (!line.trim()) { offset = next; continue; }
    if (!title) {
      require(line.startsWith("# ") && line.slice(2).trim(), "Canonical source needs one title.");
      title = true;
      offset = next;
      continue;
    }
    require(!line.startsWith("# "), "Canonical source needs one title.");
    if (/^[ \t]*##(?:[ \t]|$)/.test(line)) {
      const key = line.slice(3);
      require(HEADINGS.includes(key) && line === `## ${key}`, "Unsupported section heading.");
      require(!sections.has(key) && current?.key !== "Examples" && (current?.key !== "Tools" || key === "Examples"), "Duplicate or misplaced section.");
      if (current) current.end = offset;
      current = { key, heading: offset, start: next, end: source.length };
      sections.set(key, current);
    } else {
      require(current, "Unscoped preamble is not editable.");
    }
    if (marker) open = marker;
    offset = next;
  }
  require(!open && HEADINGS.every((key) => key === "Model" || key === "Tools" || key === "Examples" || sections.has(key)), "Incomplete canonical source.");
  for (const key of INSTRUCTIONS) {
    const section = tighten(source, sections.get(key));
    require(section.text.trim(), "Canonical instruction bodies cannot be empty.");
  }
  return { sections, eol: /\r?\n/.exec(source)?.[0] || "\n" };
}

// Keep separator newlines/blank lines (including their spaces) outside edits.
function tighten(source, section) {
  const raw = source.slice(section.start, section.end);
  const leading = /^(?:[ \t]*\r?\n)*/.exec(raw)[0].length;
  let contentEnd = raw.length;
  while (contentEnd > 0 && " \t\r\n".includes(raw[contentEnd - 1])) contentEnd--;
  const newline = raw.indexOf("\n", contentEnd);
  // Scan once rather than retrying a repeated trailing-blank regex at every
  // line; a bounded document can still contain thousands of blank lines.
  const end = Math.max(leading, newline < 0 ? raw.length : newline - (raw[newline - 1] === "\r" ? 1 : 0));
  const start = section.start;
  section.start = start + leading;
  section.end = start + end;
  section.text = source.slice(section.start, section.end).replaceAll("\r\n", "\n");
  return section;
}

// Bodies are LF-normalized for editing. A no-op save retains the original bytes.
export function instructionSections(source) {
  const { sections, free } = locate(source);
  return (free ? ["Prompt"] : INSTRUCTIONS).map((key) => ({ key, text: sections.get(key).text }));
}


function* eachLine(source, from = 0) {
  for (let offset = from; offset < source.length;) {
    const newline = source.indexOf("\n", offset), next = newline < 0 ? source.length : newline + 1;
    yield { offset, next, line: source.slice(offset, newline < 0 ? next : newline).replace(/\r$/, "") };
    offset = next;
  }
}

// Copilot's header is the first `---` block; everything after it is the prompt.
function frontmatter(source) {
  const first = /^---[ \t]*\r?(?:\n|$)/.exec(source);
  if (!first) return;
  for (const { offset, next, line } of eachLine(source, first[0].length)) {
    if (/^---[ \t]*$/.test(line)) return { yamlStart: first[0].length, yamlEnd: offset, end: next };
  }
}
export const hasFrontmatter = (source) => frontmatter(source) !== undefined;

// Only a final yaml fence with an examples root key is structured tests.
function structuredExamples(source, from) {
  let state = 0, open; const body = [];
  for (const { line } of eachLine(source, from)) {
    const marker = fence(line);
    if (state === 2) { if (line.trim()) return false; continue; }
    if (state === 0) {
      if (!line.trim()) continue;
      if (marker?.info !== "yaml") return false;
      [open, state] = [marker, 1]; continue;
    }
    if (marker && marker.marker === open.marker && marker.count >= open.count && !marker.info) state = 2;
    else body.push(line);
  }
  return state === 2 && hasRootExamples(body);
}

// Root `examples` key of the fenced YAML, in block form (plain or quoted) or as
// a flow mapping such as `{examples: []}`; nested or prose mentions never count.
function hasRootExamples(rows) {
  const text = rows.filter((l) => !/^\s*#/.test(l)).join("\n");
  if (rows.some((l) => /^(?:examples|"examples"|'examples')[ \t]*:/.test(l))) return true;
  if (!text.trimStart().startsWith("{")) return false;
  let depth = 0, quote = "", token = "";
  for (const ch of text) {
    if (quote) { token += ch; if (ch === quote) quote = ""; continue; }
    if (ch === "'" || ch === '"') { quote = ch; token += ch; } else if ("[{".includes(ch)) { depth++; token = ""; }
    else if ("]}".includes(ch)) { depth--; token = ""; } else if (ch === "," ) token = "";
    else if (ch === ":" && depth === 1) { if (/^(?:examples|"examples"|'examples')$/.test(token.trim())) return true; token = ""; }
    else token += ch;
  }
  return false;
}

// A file without the complete charter structure is one free-form prompt. Its
// own headings, comments and fences are prose; only a final Examples block and
// (without frontmatter) Model/Tools controls are carved out.
function locateFree(source, header) {
  const sections = new Map(), eol = /\r?\n/.exec(source)?.[0] || "\n";
  let from = header?.end ?? 0;
  if (!header) {
    const title = eachLine(source).find((l) => l.line.trim());
    if (title && title.line.startsWith("# ") && title.line.slice(2).trim()) from = title.next;
  }
  const headings = [];
  let open = null;
  for (const entry of eachLine(source, from)) {
    const marker = fence(entry.line);
    if (open) {
      if (marker && marker.marker === open.marker && marker.count >= open.count && !marker.info) open = null;
    } else if (marker) open = marker;
    else if (/^#{1,2} /.test(entry.line)) headings.push({ key: entry.line.startsWith("## ") ? entry.line.slice(3) : null, ...entry });
  }
  const last = headings.findLast((h) => h.key === "Examples");
  let limit = source.length;
  if (last && structuredExamples(source, last.next)) {
    sections.set("Examples", { key: "Examples", heading: last.offset, start: last.next, end: source.length });
    headings.length = headings.indexOf(last); limit = last.offset;
  }
  // Without frontmatter, Model and Tools sections may sit anywhere; each runs to
  // the next heading. The prompt is the prose around them.
  const controls = [];
  if (!header) for (const key of ["Model", "Tools"]) {
    const i = headings.findIndex((h) => h.key === key);
    if (i < 0) continue;
    const control = { key, heading: headings[i].offset, start: headings[i].next, end: headings[i + 1]?.offset ?? limit };
    sections.set(key, control); controls.push(control);
  }
  const gaps = []; let at = from;
  for (const c of controls.sort((a, b) => a.heading - b.heading)) { gaps.push({ key: "Prompt", start: at, end: c.heading }); at = c.end; }
  gaps.push({ key: "Prompt", start: at, end: limit });
  const spans = gaps.map((g) => tighten(source, g)), live = spans.filter((s) => s.text.trim());
  // The projection joins the prose spans; an edit lands in the first and drops the rest.
  const prompt = { ...(live[0] ?? spans[0]), text: live.map((s) => s.text).join("\n\n"), spans: live.length ? live : [spans[0]] };
  return { sections: new Map([["Prompt", prompt], ...sections]), eol, free: true, header };
}

function locate(source) {
  bounded(source);
  const header = frontmatter(source);
  if (!header) try { return locateCharter(source); } catch { /* Not a complete charter: free-form prompt. */ }
  return locateFree(source, header);
}
export const hasExamples = (source) => locate(source).sections.has("Examples");

function objectFields(value, allowed) {
  require(value !== null && typeof value === "object" && !Array.isArray(value), "Expected an object.");
  require(Object.keys(value).every((key) => allowed.includes(key)), "Unknown authoring field.");
}

export function replaceInstructions(source, changes) {
  const { sections, eol, free } = locate(source);
  require(Array.isArray(changes), "Instruction changes must be an array.");
  const seen = new Set(), patches = [];
  for (const change of changes) {
    objectFields(change, ["key", "text"]);
    require((free ? change.key === "Prompt" : INSTRUCTIONS.includes(change.key)) && !seen.has(change.key), "Unknown or duplicate instruction key.");
    require(typeof change.text === "string" && change.text.trim(), "Instruction text cannot be empty.");
    seen.add(change.key);
    const text = change.text.replaceAll("\r\n", "\n");
    const section = sections.get(change.key);
    if (text === section.text) continue;
    const [first, ...rest] = section.spans ?? [section];
    // Into a prompt with nothing after it yet, keep a blank line before what follows.
    const gap = first.start === first.end && source.length > first.end ? eol + eol : "";
    patches.push({ start: first.start, end: first.end, text: text.replaceAll("\n", eol) + gap });
    for (const span of rest) patches.push({ start: span.start, end: span.end + (source.startsWith("\r\n", span.end) ? 2 : source[span.end] === "\n" ? 1 : 0), text: "" });
  }
  // Work backwards so each patch still uses offsets from the original source.
  patches.sort((a, b) => b.start - a.start);
  for (const patch of patches) source = source.slice(0, patch.start) + patch.text + source.slice(patch.end);
  return bounded(source);
}

const TOOL_NAME = /^[A-Za-z_][A-Za-z0-9_-]{0,63}$/;

// Blank-line separation before a section appended at the end of the source.
const separator = (source, eol) => !source ? "" : (source.match(/(?:\r?\n)*$/)[0].match(/\n/g)?.length ?? 0) >= 2 ? "" : source.endsWith("\n") ? eol : eol + eol;

const TOOLS_KEY = /^(?:tools|"tools"|'tools')[ \t]*:/;
// The value of a double-quoted YAML scalar body, or undefined when it uses an escape
// that is not also valid JSON (YAML-only escapes are not decoded here).
function decodeDouble(body) {
  if (/\\/.test(body.replace(/\\(?:["\\/bfnrt]|u[0-9a-fA-F]{4})/g, ""))) return undefined;
  try { return JSON.parse(`"${body}"`); } catch { return undefined; }
}

// Only the top-level `tools` property changes. Items this kmx did not map
// (aliases, wildcards, namespaced sets) are kept exactly as written, as is every
// other header line; anything beyond plain lists is refused rather than guessed.
function replaceHeaderTools(source, header, names, mapped, eol) {
  const rows = [...eachLine(source, header.yamlStart)].filter((l) => l.offset < header.yamlEnd);
  const unsupported = () => { throw new Error("Unsupported Tools formatting."); };
  // An escaped spelling of the key ("to\u006fls") is the tools property to a YAML reader: refuse, never guess.
  for (const l of rows) {
    const escaped = /^"((?:[^"\\]|\\.)*\\(?:[^"\\]|\\.)*)"[ \t]*:/.exec(l.line)?.[1];
    if (escaped !== undefined && (decodeDouble(escaped) ?? "tools") === "tools") unsupported();
  }
  const at = rows.findIndex((l) => TOOLS_KEY.test(l.line));
  const text = (name) => /^(?:true|false|null|yes|no|on|off|y|n)$/i.test(name) ? `'${name}'` : name;
  if (at < 0) {
    // New properties belong inside the YAML document, before a trailing `...` marker.
    const end = rows.find((l) => /^\.\.\.(?:[ \t]+#.*)?[ \t]*$/.test(l.line)), insert = end?.offset ?? header.yamlEnd;
    return names.length ? source.slice(0, insert) + `tools: [${names.map(text).join(", ")}]` + eol + source.slice(insert) : source;
  }
  let last = at;
  while (rows[last + 1] && /^(?:[ \t]+\S|-(?:[ \t]|$))/.test(rows[last + 1].line)) last++;
  // Items cut off from the property by a blank or comment line are not guessed at.
  const after = rows.slice(last + 1).find((l) => l.line.trim() && !/^[ \t]*#/.test(l.line));
  if (after && after !== rows[last + 1] && /^(?:[ \t]+\S|-(?:[ \t]|$))/.test(after.line)) unsupported();
  const span = rows.slice(at, last + 1), key = /^(?:tools|"tools"|'tools')/.exec(span[0].line)[0], head = span[0].line.replace(TOOLS_KEY, "").replace(/^[ \t]+/, "");
  if (span.some((l) => l.line.includes("#"))) unsupported();
  let items;
  if (head) {
    const flow = [head, ...span.slice(1).map((l) => l.line.trim())].join(" ").trim();
    if (!/^\[.*\]$/.test(flow)) unsupported();
    items = []; let quote = "", item = "";
    const chars = [...flow.slice(1, -1) + ","];
    for (let i = 0; i < chars.length; i++) {
      const ch = chars[i];
      // A backslash escapes the next character inside double quotes.
      if (quote === '"' && ch === "\\" && i + 1 < chars.length) { item += ch + chars[++i]; continue; }
      if (quote) { item += ch; if (ch === quote) quote = ""; } else if (ch === "'" || ch === '"') { quote = ch; item += ch; }
      else if (ch === ",") { if (item.trim()) items.push(item.trim()); item = ""; } else item += ch;
    }
    if (quote) unsupported();
  } else {
    items = span.slice(1).map((l) => /^[ \t]*-[ \t]+(\S.*?)[ \t]*$/.exec(l.line)?.[1] ?? unsupported());
    if (!items.length) unsupported();
  }
  if (items.some((i) => /^[*&!\[{|>]/.test(i))) unsupported();
  // Compare by the value a YAML reader sees; an escape this cannot decode safely refuses the edit.
  const value = (raw) => {
    const quoted = /^(["'])(.*)\1$/.exec(raw);
    if (!quoted) return raw;
    return quoted[1] === "'" ? quoted[2].replaceAll("''", "'") : decodeDouble(quoted[2]) ?? unsupported();
  };
  const known = new Set(mapped ?? items.map(value));
  const keep = items.filter((raw) => !known.has(value(raw)) || names.includes(value(raw)));
  const next = [...keep, ...names.filter((n) => !keep.some((raw) => value(raw) === n)).map(text)];
  if (next.length === items.length && next.every((raw, i) => raw === items[i])) return source;
  const indent = /^([ \t]*)-/.exec(span[1]?.line ?? "")?.[1] ?? "  ";
  const body = !next.length || head ? `${key}: ${next.length ? `[${next.join(", ")}]` : "[]"}${eol}` : `${key}:${eol}${next.map((i) => `${indent}- ${i}${eol}`).join("")}`;
  return source.slice(0, span[0].offset) + body + source.slice(rows[last].next);
}

// `mapped` is the exact tool set the resource reported; with it, other header
// items are preserved. Without it every item is treated as a mapped name.
export function replaceTools(source, names, mapped) {
  const { sections, eol, header } = locate(source), section = sections.get("Tools");
  require(Array.isArray(names) && names.length <= 32 && new Set(names).size === names.length && names.every(name => typeof name === "string" && TOOL_NAME.test(name)), "Provide at most 32 unique tool names.");
  if (header) return bounded(replaceHeaderTools(source, header, names, mapped, eol));
  const current = section ? source.slice(section.start, section.end).split(/\r?\n/).filter(line => line.trim()).map(line => {
    require(/^- [A-Za-z_][A-Za-z0-9_-]{0,63}$/.test(line), "Unsupported Tools formatting.");
    return line.slice(2);
  }) : [];
  if (same(current, names)) return source;
  const examples = sections.get("Examples"), start = section?.heading ?? examples?.heading ?? source.length, end = section?.end ?? start;
  const body = names.length ? ["## Tools", ...names.map(name => `- ${name}`), "", ""].join(eol) : "";
  const gap = start === source.length && !section && body ? separator(source, eol) : "";
  return bounded(source.slice(0, start) + gap + body + source.slice(end));
}

function nonblank(value) {
  require(typeof value === "string" && value.trim(), "Expected a nonblank string.");
}

function uniqueID(id, seen) {
  require(typeof id === "string" && ID.test(id) && !seen.has(id), "Invalid or duplicate ID.");
  seen.add(id);
}

export function validateExamples(examples, recordings = false) {
  require(Array.isArray(examples) && examples.length <= 128, "Provide 0–128 examples.");
  const seen = new Set();
  for (const example of examples) {
    objectFields(example, recordings ? ["id", "input", "assertions", "mocks"] : ["id", "input", "assertions"]);
    if (example.mocks !== undefined) {
      objectFields(example.mocks, ["tools"]);
      require(Array.isArray(example.mocks.tools) && example.mocks.tools.length <= 32, "Invalid tool modes.");
      const names = new Set();
      for (const t of example.mocks.tools) {
        objectFields(t, ["name", "mode", "description", "parameters", "calls"]);
        require(typeof t.name === "string" && /^[A-Za-z_][A-Za-z0-9_-]{0,63}$/.test(t.name) && !names.has(t.name) && ["live", "recorded"].includes(t.mode), "Invalid tool mode."); names.add(t.name);
      }
    }
    uniqueID(example.id, seen);
    nonblank(example.input);
    require(Array.isArray(example.assertions) && example.assertions.length >= 1 && example.assertions.length <= 64, "Provide 1–64 checks per example.");
    const checkIDs = new Set();
    for (const check of example.assertions) {
      objectFields(check, ["id", "type", "value", "pattern", "rubric", "minScore", "judgeModel", "judgeVersion", "tool"]);
      uniqueID(check.id, checkIDs);
      require(!check.id.startsWith("expectContains."), "Reserved assertion ID.");
      require(typeof check.type === "string" && Object.hasOwn(ASSERTION_FIELDS, check.type), "Unsupported assertion type.");
      require(recordings || !["toolCalled", "toolNotCalled"].includes(check.type), "Tool checks require sparse edits.");
      const operands = ASSERTION_FIELDS[check.type];
      require(Object.keys(check).every((key) => key === "id" || key === "type" || operands.includes(key)), "Operand is not allowed for this assertion type.");
      for (const key of operands) {
        if (key === "minScore") {
          require(Number.isInteger(check[key]) && check[key] >= 0 && check[key] <= 100, "minScore must be an integer from 0 to 100.");
        } else if (key !== "judgeModel" || check[key] !== undefined && check[key] !== "") {
          nonblank(check[key]);
        }
      }
    }
  }
}

// Editing examples intentionally reformats that section (including comments).
// Instruction-only saves never pass through this serializer. JSON-quoted scalar
// strings are valid YAML and cannot introduce YAML keys, aliases or fences.
export function replaceExamples(source, examples) {
  const { sections, eol } = locate(source), existing = sections.get("Examples");
  validateExamples(examples);
  // Zero tests stay an explicit empty sequence, never YAML null.
  const lines = ["## Examples", "```yaml", examples.length ? "examples:" : "examples: []"];
  for (const example of examples) {
    lines.push(`  - id: ${JSON.stringify(example.id)}`, `    input: ${JSON.stringify(example.input)}`, "    assertions:");
    for (const check of example.assertions) {
      lines.push(`      - id: ${JSON.stringify(check.id)}`, `        type: ${JSON.stringify(check.type)}`);
      for (const key of ASSERTION_FIELDS[check.type]) {
        if (key === "judgeModel" && (check[key] === undefined || check[key] === "")) continue;
        lines.push(`        ${key}: ${JSON.stringify(check[key])}`);
      }
    }
  }
  lines.push("```", "");
  // A free-form file may have no Examples yet; the first test only appends one.
  return bounded(existing ? source.slice(0, existing.heading) + lines.join(eol) : source + separator(source, eol) + lines.join(eol));
}

export function uniqueIndex(items, key) {
  const map = new Map();
  for (const item of items) {
    const id = key(item);
    map.set(id, map.has(id) ? null : item);
  }
  return map;
}
export const clone = (value) => JSON.parse(JSON.stringify(value));
// Only sparse mode intents cross Edit. Existing fixture payload never makes a
// numeric round trip, and a changed payload cannot impersonate a new capture.
export function exampleEdits(before, after, origins) {
  validateExamples(after, true);
  const prior = new Map(before.map(e => [e.id, e])), edits = [];
  origins ??= after.map(e => prior.has(e.id) ? e.id : null);
  require(Array.isArray(origins) && origins.length === after.length && origins.every(id => id === null || prior.has(id)) && new Set(origins.filter(id=>id!==null)).size === origins.filter(id=>id!==null).length, "Invalid test origins.");
  for (const old of before) if (!origins.includes(old.id)) edits.push({ beforeId: old.id, delete: true });
  for (const [index,e] of after.entries()) {
    const old = prior.get(origins[index]), change = old ? { beforeId: old.id } : { id: e.id };
    if (old && e.id !== old.id) change.id = e.id;
    for (const key of ["input", "assertions"]) if (!old || !same(old[key], e[key])) change[key] = e[key];
    const tools = [];
    for (const t of e.mocks?.tools ?? []) {
      const prev = old?.mocks?.tools.find(x => x.name === t.name);
      if (same(prev, t)) continue;
      const pending = { name: t.name, mode: "recorded", calls: [] }, live = { name: t.name, mode: "live" };
      require(same(t, t.mode === "live" ? live : pending), "Fixture payload is not an editable operand.");
      tools.push({ name: t.name, mode: t.mode, ...(prev?.mode === "recorded" && t.mode === "recorded" ? { reset: true } : {}) });
    }
    require(!(old?.mocks?.tools ?? []).some(t => !e.mocks?.tools.some(x => x.name === t.name)), "Keep existing tool modes or explicitly choose Live.");
    if (tools.length) change.tools = tools;
    if (!old || Object.keys(change).length > 1) edits.push(change);
  }
  return edits;
}

export const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
export const operandKey = (type) => ["toolCalled", "toolNotCalled"].includes(type) ? "tool" : type === "regex" ? "pattern" : type === "llmJudge" ? "rubric" : "value";
export const EDITABLE_TYPES = [["contains", "Contains"], ["notContains", "Not contains"], ["regex", "Regex"], ["llmJudge", "Judge"], ["toolCalled", "Calls"], ["toolNotCalled", "Doesn't call"]];
export const quoted = (text) => `“${text}”`;

export function checkIssue(a) {
  if (!EDITABLE_TYPES.some(([type]) => type === a.type)) return;
  const field = operandKey(a.type);
  if (!String(a[field] ?? "").trim()) return { field, message: field === "rubric" ? "Needs a rubric" : field === "pattern" ? "Needs a pattern" : "Needs text to check" };
  if (a.type !== "llmJudge") return;
  if (typeof a.minScore !== "number" || !Number.isInteger(a.minScore) || a.minScore < 0 || a.minScore > 100) return { field: "minScore", message: "Needs a minimum score, 0 to 100" };
  if (!(a.judgeModel === undefined || a.judgeModel === "" || typeof a.judgeModel === "string" && !!a.judgeModel.trim())) return { field: "judgeModel", message: "Choose a Judge model" };
  if (typeof a.judgeVersion !== "string" || !a.judgeVersion.trim()) return { field: "judgeVersion", message: "Needs a Judge version" };
}
export const checkComplete = a => !checkIssue(a);
export function convertCheck(target, type) {
  const oldKey = operandKey(target.type), newKey = operandKey(type), value = target[oldKey] ?? "";
  if (oldKey !== newKey) delete target[oldKey];
  if (target.type === "llmJudge" && type !== "llmJudge") for (const key of ["minScore", "judgeModel", "judgeVersion"]) delete target[key];
  target.type = type; target[newKey] = value;
}

export const readSnapshot = (view) => {
  try {
    if (view.authoring?.editable === true && typeof view.authoring.source === "string" && Array.isArray(view.authoring.examples)) {
      const sections = instructionSections(view.authoring.source);
      const main = sections.find((s) => s.key === "Prompt" || /accountable/i.test(s.key));
      if (main) return { sections, main };
    }
  } catch { /* Old or noncanonical resources remain inspection-only. */ }
};

const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;
// IDs keep typed capitals ("CI"); only a sentence-case first letter is lowered,
// since the shown name capitalizes it again. Uniqueness ignores case.
export const portableId = (name) => name.trim().replace(/\s+/g, "-").replace(/^[A-Z](?![A-Z])/, c => c.toLowerCase());

export function validateDraft(draft, intended) {
  const raw = draft.raw;
  if (draft.origin === null && !draft.fixedId) {
    raw.id = portableId(draft.name);
    if (!/^[A-Za-z][A-Za-z0-9-]{0,62}$/.test(raw.id) || RESERVED.test(raw.id)) return "Use a name that starts with a letter, then letters, numbers, spaces or hyphens.";
  }
  if (intended.filter((e) => e.id.toLowerCase() === raw.id.toLowerCase()).length !== 1) return "Another test already has this name.";
  if (!raw.input.trim()) return "Say what the agent gets.";
  if (!raw.assertions.length) return "Keep at least one check.";
  if (raw.assertions.some(a => !checkComplete(a))) return { check: true };
}

export function compareDrafts(base, candidate, snap, sectionDrafts, testDrafts, toolsDraft, settingsDraft = new Map()) {
  const sectionText = (key) => sectionDrafts.get(key) ?? snap.sections.find((s) => s.key === key).text;
  const rawExamples = () => snap ? base.authoring.examples : [];
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
  if (toolsDraft !== undefined || !same(base.tools ?? [], candidate.tools ?? [])) items.push(["Tools", show(base.tools ?? []), show(toolsDraft ?? base.tools ?? []), show(candidate.tools ?? [])]);
  const value = (view, name) => view.toolSettings?.find(s => s.name === name)?.state ?? "Enabled";
  if (settingsDraft.size || (base.tools ?? []).some(name => candidate.tools?.includes(name) && value(base, name) !== value(candidate, name))) {
    const mine = (toolsDraft ?? base.tools ?? []).map(name => ({ name, state: settingsDraft.get(name) ?? value(base, name) }));
    items.push(["Tool settings", show(base.toolSettings), show(mine), show(candidate.toolSettings)]);
    if (base.toolSettingsRevision !== candidate.toolSettingsRevision) items.push(["Tool settings revision", base.toolSettingsRevision, base.toolSettingsRevision, candidate.toolSettingsRevision]);
  }
  if (candidate.model !== base.model) items.push(["Model", base.model, base.model, candidate.model]);
  if (candidate.title !== base.title) items.push(["Title", base.title, base.title, candidate.title]);
  return items;
}

// A Recorded tool without calls writes them into the tests on its first live run.
export const pendingCapture = (examples, settings, ids) => examples.filter(e => ids.includes(e.id)).some(e => e.mocks?.tools.some(t => t.mode === "recorded" && !t.calls?.length && settings.find(s => s.name === t.name)?.state !== "Disabled"));
export const COPILOT_TESTS_NOTICE = "Tests added here become part of this agent's prompt in Copilot.";
// Matches the server rule: any change to a frontmatter file's tests asks first;
// prompt-only and header-only saves never do.
export const testsNeedAcknowledgement = (source, before, after) => hasFrontmatter(source) && !same(before, after);
