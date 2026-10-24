// Pure source transforms shared by the canvas and extension. MCP Edit remains
// the full-document validator; these helpers grant no parser/write authority.
const INSTRUCTIONS = [
  "Accountable Responsibilities",
  "Non-Responsibilities",
  "Collaboration and Review Authority",
];
const HEADINGS = ["Identity", ...INSTRUCTIONS, "Model", "Examples"];
export const DEFAULT_MODEL = "gpt-5.6-terra";
export const effectiveModel = (authored, override) => override || authored.match(/^model: (.+)$/m)?.[1] || authored || DEFAULT_MODEL;
// These owned refusals happen before route selection and contain no completion.
export const preRouteRefusal = (result) => result?.state === "refused" && result.verdict === "unknown" && result.fullPass === false
  && result.modelRoute === "/" && ["no_tests", "model_unavailable", "invalid_selection"].includes(result.reasonCode);

// Runtime state is separate from authored source and survives only this canvas.
export function runtimeModelSelection() {
  let override = "", needsRun = false;
  const selected = (view) => effectiveModel(view.model, override);
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
    view(view) {
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
function locate(source) {
  bounded(source);
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
      require(!sections.has(key) && current?.key !== "Examples", "Duplicate or misplaced section.");
      if (current) current.end = offset;
      current = { key, heading: offset, start: next, end: source.length };
      sections.set(key, current);
    } else {
      require(current, "Unscoped preamble is not editable.");
    }
    if (marker) open = marker;
    offset = next;
  }
  require(!open && HEADINGS.every((key) => key === "Model" || sections.has(key)), "Incomplete canonical source.");
  for (const key of INSTRUCTIONS) {
    const section = sections.get(key);
    const raw = source.slice(section.start, section.end);
    // Keep separator newlines/blank lines (including their spaces) outside edits.
    const leading = /^(?:[ \t]*\r?\n)*/.exec(raw)[0].length;
    let contentEnd = raw.length;
    while (contentEnd > 0 && " \t\r\n".includes(raw[contentEnd - 1])) contentEnd--;
    const newline = raw.indexOf("\n", contentEnd);
    // Scan once rather than retrying a repeated trailing-blank regex at every
    // line; a bounded document can still contain thousands of blank lines.
    const end = newline < 0 ? raw.length : newline - (raw[newline - 1] === "\r" ? 1 : 0);
    section.start += leading;
    section.end = section.start - leading + end;
    section.text = source.slice(section.start, section.end).replaceAll("\r\n", "\n");
    require(section.text.trim(), "Canonical instruction bodies cannot be empty.");
  }
  return { sections, eol: /\r?\n/.exec(source)?.[0] || "\n" };
}

// Bodies are LF-normalized for editing. A no-op save retains the original bytes.
export function instructionSections(source) {
  const { sections } = locate(source);
  return INSTRUCTIONS.map((key) => ({ key, text: sections.get(key).text }));
}

function objectFields(value, allowed) {
  require(value !== null && typeof value === "object" && !Array.isArray(value), "Expected an object.");
  require(Object.keys(value).every((key) => allowed.includes(key)), "Unknown authoring field.");
}

export function replaceInstructions(source, changes) {
  const { sections, eol } = locate(source);
  require(Array.isArray(changes), "Instruction changes must be an array.");
  const seen = new Set(), patches = [];
  for (const change of changes) {
    objectFields(change, ["key", "text"]);
    require(INSTRUCTIONS.includes(change.key) && !seen.has(change.key), "Unknown or duplicate instruction key.");
    require(typeof change.text === "string" && change.text.trim(), "Instruction text cannot be empty.");
    seen.add(change.key);
    const text = change.text.replaceAll("\r\n", "\n");
    const section = sections.get(change.key);
    if (text !== section.text) patches.push({ ...section, text: text.replaceAll("\n", eol) });
  }
  // Work backwards so each patch still uses offsets from the original source.
  patches.sort((a, b) => b.start - a.start);
  for (const patch of patches) source = source.slice(0, patch.start) + patch.text + source.slice(patch.end);
  return bounded(source);
}

function nonblank(value) {
  require(typeof value === "string" && value.trim(), "Expected a nonblank string.");
}

function uniqueID(id, seen) {
  require(typeof id === "string" && ID.test(id) && !seen.has(id), "Invalid or duplicate ID.");
  seen.add(id);
}

function validateExamples(examples) {
  require(Array.isArray(examples) && examples.length <= 128, "Provide 0–128 examples.");
  const seen = new Set();
  for (const example of examples) {
    objectFields(example, ["id", "input", "assertions"]);
    uniqueID(example.id, seen);
    nonblank(example.input);
    require(Array.isArray(example.assertions) && example.assertions.length >= 1 && example.assertions.length <= 64, "Provide 1–64 checks per example.");
    const checkIDs = new Set();
    for (const check of example.assertions) {
      objectFields(check, ["id", "type", "value", "pattern", "rubric", "minScore", "judgeModel", "judgeVersion"]);
      uniqueID(check.id, checkIDs);
      require(!check.id.startsWith("expectContains."), "Reserved assertion ID.");
      require(typeof check.type === "string" && Object.hasOwn(ASSERTION_FIELDS, check.type), "Unsupported assertion type.");
      const operands = ASSERTION_FIELDS[check.type];
      require(Object.keys(check).every((key) => key === "id" || key === "type" || operands.includes(key)), "Operand is not allowed for this assertion type.");
      for (const key of operands) {
        if (key === "minScore") {
          require(Number.isInteger(check[key]) && check[key] >= 0 && check[key] <= 100, "minScore must be an integer from 0 to 100.");
        } else {
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
  const { sections, eol } = locate(source);
  validateExamples(examples);
  // Zero tests stay an explicit empty sequence, never YAML null.
  const lines = ["## Examples", "```yaml", examples.length ? "examples:" : "examples: []"];
  for (const example of examples) {
    lines.push(`  - id: ${JSON.stringify(example.id)}`, `    input: ${JSON.stringify(example.input)}`, "    assertions:");
    for (const check of example.assertions) {
      lines.push(`      - id: ${JSON.stringify(check.id)}`, `        type: ${JSON.stringify(check.type)}`);
      for (const key of ASSERTION_FIELDS[check.type]) {
        lines.push(`        ${key}: ${JSON.stringify(check[key])}`);
      }
    }
  }
  lines.push("```", "");
  return bounded(source.slice(0, sections.get("Examples").heading) + lines.join(eol));
}
