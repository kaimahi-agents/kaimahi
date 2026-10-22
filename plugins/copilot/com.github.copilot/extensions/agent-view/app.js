// Renders the read-only agent view. Every value from the resource is untrusted
// and is only ever assigned through textContent; there is no markup parsing.
const el = (doc, tag, className, text) => {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
const section = (doc, title, ...children) => {
  const s = el(doc, "section");
  s.append(el(doc, "h2", "", title), ...children);
  return s;
};

const BADGES = {
  none: ["none", "No current result. Run or Test the agent in chat to produce one."],
  stale: ["stale", "Stale: from an earlier revision of this file. Not current; do not rely on it."],
  pass: ["pass", "Current: full pass."],
  "not-passing": ["fail", "Current: not a full pass."],
};

function latestSection(doc, latest) {
  const [cls, label] = BADGES[latest.badge] ?? BADGES.none;
  const box = section(doc, "Latest result", el(doc, "p", `badge ${cls}`, label));
  const r = latest.result;
  if (!r || latest.status === "stale") return box;
  box.append(el(doc, "p", "meta", `${r.operation} on ${r.modelRoute}; state ${r.state}, verdict ${r.verdict}; ${r.selection.scope} (${r.selection.caseIds.length}/${r.selection.totalCases} cases)`));
  if (r.reason) box.append(el(doc, "p", "meta", r.reason));
  for (const e of r.examples) {
    const row = el(doc, "div", "result");
    row.append(el(doc, "h3", "", `${e.id}: ${e.verdict} (${e.state})`));
    if (e.reason) row.append(el(doc, "p", "meta", e.reason));
    for (const a of e.assertions) row.append(el(doc, "p", "meta", `${a.id} [${a.type}]: ${a.verdict}${a.reason ? ` - ${a.reason}` : ""}`));
    if (e.answerExcerpt !== undefined) {
      row.append(el(doc, "p", "label", `Model answer excerpt (untrusted${e.truncated ? ", truncated" : ""}):`), el(doc, "pre", "", e.answerExcerpt));
    }
    box.append(row);
  }
  return box;
}

export function renderView(doc, root, payload) {
  if (!payload || payload.error || !payload.file) {
    root.replaceChildren(el(doc, "p", "error", payload?.message || "The agent view could not be loaded."));
    return;
  }
  const examples = payload.examples.map((e) => {
    const box = el(doc, "div", "example");
    box.append(el(doc, "h3", "", e.id), el(doc, "p", "label", "Input"), el(doc, "pre", "", e.input), el(doc, "p", "label", "Expected"));
    const ul = el(doc, "ul");
    for (const a of e.assertions) {
      const extra = a.fields.map(([k, v]) => `${k}: ${v}`).join("; ");
      ul.append(el(doc, "li", "", `${a.id} [${a.type}]${extra ? ` ${extra}` : ""}`));
    }
    box.append(ul);
    return box;
  });
  root.replaceChildren(
    el(doc, "h1", "", payload.title),
    el(doc, "p", "meta", `${payload.file.path} (${payload.file.revision})`),
    el(doc, "p", "meta", "Read-only view."),
    section(doc, "Model", el(doc, "pre", "", payload.model)),
    section(doc, "Instructions", el(doc, "pre", "", payload.instructions)),
    section(doc, `Examples (${payload.examples.length})`, ...examples),
    latestSection(doc, payload.latest),
  );
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
