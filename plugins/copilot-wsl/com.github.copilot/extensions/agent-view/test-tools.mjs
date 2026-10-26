// Inert per-test controls. The caller owns drafts, source edits and bounded saved detail text.
import { el } from "./editor.mjs";

export const defaultToolMode = (assertions = []) => assertions.some(a => ["contains", "notContains", "regex"].includes(a.type)) ? "recorded" : "live";
export const toolMode = (raw, name) => raw?.mocks?.tools?.find(t => t.name === name)?.mode ?? "live";

export function setToolMode(raw, name, mode, reset = false) {
  const draft = structuredClone(raw);
  draft.mocks ??= {}; draft.mocks.tools ??= [];
  const entries = draft.mocks.tools, index = entries.findIndex(t => t.name === name), previous = entries[index];
  const next = mode === "live" ? { name, mode }
    : previous?.mode === "recorded" && !reset ? previous : { name, mode: "recorded", calls: [] };
  if (index < 0) entries.push(next); else entries[index] = next;
  return draft;
}

// A draft without mocks still carries the synthesized default modes shown for it.
export const withToolModes = (raw, shown) => raw?.mocks || !shown?.mocks ? raw : { ...raw, mocks: shown.mocks };
export const duplicateTest = raw => (raw.mocks?.tools ?? []).reduce((copy, t) => t.mode === "recorded" ? setToolMode(copy,t.name,"recorded",true) : copy, structuredClone(raw));

const modeLabel = mode => mode === "recorded" ? "◉ Recorded" : "⚡ Live";
const attemptedDisabled = tool => tool.settingDisabled === true && tool.requestCount > 0;
const sourceLabel = tool => tool.recordingSaved ? "recorded just now" : tool.source === "recorded" ? "Recorded replay"
  : tool.source === "live" ? "Live" : tool.source === "unavailable" ? "not available in this test" : "source not recorded";
const countLabel = tool => tool.callCount === 0 ? "Not called" : Number.isInteger(tool.callCount)
  ? `${tool.callCount} ${tool.callCount === 1 ? "call" : "calls"}` : "Call count not recorded";

// Settings alone (including zero Enabled calls) prove no attempted Disabled request.
export function toolUseText(tools = [], settings = []) {
  if (!tools.length) return "";
  return "Tools: " + tools.map(tool => {
    if (attemptedDisabled(tool)) return `${tool.name}, disabled for this test`;
    const glyph = tool.mode === "recorded" && (tool.source === "recorded" || tool.recordingSaved) ? "◉ " : tool.source === "live" ? "⚡ " : "";
    const count = tool.callCount > 1 ? ` ${tool.callCount}×` : "";
    const facts = [];
    if (tool.callCount === 0) facts.push("not called");
    if (tool.recordingSaved || !["live", "recorded"].includes(tool.source)) facts.push(sourceLabel(tool));
    return `${glyph}${tool.name}${count}${facts.length ? ` (${facts.join("; ")})` : ""}`;
  }).join(", ");
}

export function renderTestTools(doc, { names, raw, tools = [], settings = [], details = [], onChange, onReset, disabled = false, updates }) {
  const section = el(doc, "section", "detail-section test-tools");
  const isDisabled = typeof disabled === "function" ? disabled : () => disabled;
  section.append(el(doc, "h2", "lbl", "Tools"));
  const button = (text, label, action, className) => {
    const node = el(doc, "button", `tb ${className}`, text);
    node.type = "button"; node.disabled = isDisabled(); node.setAttribute("aria-label", label);
    updates?.push(() => { node.disabled = isDisabled(); });
    node.addEventListener("click", () => { if (!isDisabled()) action(); });
    return node;
  };
  for (const name of names) {
    const mode = toolMode(raw, name), label = modeLabel(mode), row = el(doc, "div", "check test-tool");
    const head = el(doc, "div", "tools-head");
    head.append(el(doc, "code", "tool-name", name), button(label, `${name}: ${label}`, () => onChange(name, mode === "live" ? "recorded" : "live"), "tool-mode"));
    row.append(head);
    const observed = tools.find(t => t.name === name);
    if (observed) row.append(el(doc, "p", "meta tool-status", attemptedDisabled(observed)
      ? `${name}, disabled for this test` : `${countLabel(observed)} · ${sourceLabel(observed)}`));
    const recording = raw?.mocks?.tools?.find(t => t.name === name);
    if (mode === "recorded" && recording?.calls?.length) {
      row.append(button("Record again", `${name}: Record again`, () => onReset(name), "tool-reset"));
      const saved = details.find(detail => detail.name === name);
      if (saved) {
        const disclosure = el(doc, "details", "check-details");
        disclosure.append(el(doc, "summary", "", "Saved recording"), el(doc, "pre", "", saved.text));
        row.append(disclosure);
      }
    }
    section.append(row);
  }
  if (names.length) section.append(el(doc, "p", "quiet", "Recorded keeps output with the test."));
  return section;
}
