// Canvas header, notices and footers. Re-exports the canvas view modules for existing imports.
import { effectiveModel, agentName, fileLocation, COPILOT_TESTS_NOTICE } from "./authoring.mjs";
import { el, icon } from "./dom.mjs";
import { inspectCases, isRefused, resultTone, testRow, coherentSelection, countText, plural } from "./results.mjs";
import { modelPresentation } from "./check-controls.mjs";
import { purposeText } from "./instruction-text.mjs";
export { clone, same, uniqueIndex, operandKey, EDITABLE_TYPES, quoted, checkIssue, checkComplete, convertCheck } from "./authoring.mjs";
export { el, icon, statusIcon } from "./dom.mjs";
export { checkKey, inspectCases, friendlyName, checkLabel, oneLine, provenActual, failureFacts, matchedAnswer, plainReason, isRefused, resultTone, testRow, coherentSelection, ambiguousPassSet, countText, plural, resultHistory } from "./results.mjs";
export { modelPresentation, modelLabel, compareLabels, growTextarea, checkFields, inconclusiveJudge, judgeDetails, checkTypeControl, runtimeModelControl } from "./check-controls.mjs";
export { instructionEditor, instructionBlocks, inlineText, editorValue, insertPlainText, fillEditor, purposeText, instructionsContent } from "./instruction-text.mjs";

export function resultPill(doc, settings) {
  const node = el(doc, "span", "result-pill"), dot = el(doc, "span", "result-dot"), text = el(doc, "span");
  dot.setAttribute("aria-hidden", "true"); node.append(dot, text);
  const update = () => { const state = settings(); node.setAttribute("data-tone", resultTone(state.tone)); text.textContent = state.text; };
  update(); return { node, update };
}
// The controller supplies proven substrings; this renderer never rewrites answers.
export function answerBlock(doc, row, local, actual, updates, passed = false) {
  if (row.answerExcerpt === undefined) return el(doc, "p", "quiet", "Answer content not included.");
  const box = el(doc, "div", "answer"), truncated = row.truncated || /…\s*\[truncated\]$/.test(row.answerExcerpt ?? "");
  if (truncated) box.append(el(doc, "span", "label", "Answer truncated"));
  const pre = el(doc, "pre", "answer-excerpt", actual === undefined ? row.answerExcerpt : undefined);
  if (actual !== undefined) {
    const [before, after] = row.answerExcerpt.split(actual), mark = el(doc, "mark", "", actual);
    updates.push(() => { mark.className = local() ? "old" : passed ? "matched" : ""; });
    pre.append(before, mark, after);
  }
  box.append(pre); return box;
}
export function refusalContext(view, selected) {
  if (!["model_unavailable", "copilot_model_unavailable"].includes(view.latest.result?.reasonCode)) return "";
  const pins = [...new Set((view.authoring?.examples ?? []).flatMap(e => e.assertions.filter(a => a.type === "llmJudge" && a.judgeModel).map(a => a.judgeModel)))];
  return ` Run model: ${modelPresentation(effectiveModel(view.model, selected, view.chatModel)).name}.${pins.length ? ` Pinned Judge models: ${pins.join(", ")}.` : ""}`;
}
export function canvasResultSummary(view, { n, testing, one, blocked, dirty, last }) {
  const result = view.latest.result;
  if (testing) return { tone: "running", text: `Running ${one ? "1 test" : plural(n, "test")}…` };
  if (blocked) return { tone: "none", text: "Result unconfirmed" };
  if (view.latest.status === "stale" || (dirty || view.latest.status !== "current") && last) return { tone: "none", text: "Out of date" };
  if (!n || !result || result.reasonCode === "no_tests") return { tone: "none", text: "Not run yet" };
  if (isRefused(result)) return { tone: "error", text: "Refused" };
  if (result.state !== "complete" || !coherentSelection(view)) return { tone: "error", text: result.state === "complete" ? "Incomplete run" : "Didn't finish" };
  const cases = inspectCases(view), passing = cases.filter(c => c.state === "pass").length;
  const full = result.fullPass && result.verdict === "pass" && result.selection.scope === "full" && passing === n;
  if (!full && (passing === n || cases.some(c => c.state === "unknown"))) return { tone: "error", text: "Incomplete run" };
  return { tone: full ? "pass" : cases.some(c => c.state === "fail") ? "fail" : "none", text: result.selection.scope === "subset" ? countText(cases.map(c => testRow(c, false, false))) : `${passing} of ${n} passing` };
}

export function canvasFooterText({ editable, n, busy, pending, blocked, savedUntested, settingsIncomplete, invalid, candidate, scope, promptDirty, testDirty, dirty, runAlert }) {
  if (!editable) return "Read-only inspection. This engine or agent file does not provide a complete editable snapshot.";
  const tests = plural(n, "test");
  if (busy) {
    if (!pending?.operation || !["save-and-test", "test"].includes(pending.operation)) return "Working…";
    const what = pending.one ? "this test" : tests;
    return pending.operation === "save-and-test" ? `Saving and running ${what}…` : `Saved · running ${what}`;
  }
  if (blocked && settingsIncomplete) return `${savedUntested ? "Some changes saved" : "Reload required"} · tests did not run`;
  if (blocked) return savedUntested ? "Saved · test result unconfirmed" : "Reload required · result unconfirmed";
  if (invalid) return "Edited · unsupported formatting, reload before saving";
  if (candidate) return "Edited · settle the outside change before saving";
  if (scope === "one") return promptDirty || testDirty
    ? "Enter saves and runs this test" : "Saved · Enter runs this test";
  if (!n) return dirty ? `${promptDirty ? "Edited" : "Prompt saved"} · Enter saves` : "Saved · add a test to run it";
  if (dirty) return `${promptDirty ? "Edited" : "Prompt saved"} · Enter saves and runs ${tests}`;
  if (runAlert?.saved && !runAlert.finished) return "Saved · tests didn't run";
  return `Saved · Enter runs ${tests}`;
}

export function canvasHeader(doc, view, actionNodes, readOnly) {
  const displayName = agentName(view.title), file = fileLocation(view.file.path);
  const source = el(doc, "details", "source-details"), info = el(doc, "div", "source-info");
  const sourceSummary = el(doc, "summary", "meta agent-meta", `${file.name}${file.folder ? ` · in ${file.folder}` : ""}`);
  sourceSummary.setAttribute("title", "Source details"); source.append(sourceSummary, info);
  info.append(el(doc, "p", "source-title", view.title), pathControls(doc, view.file.path), el(doc, "code", "revision", view.file.revision));
  if (view.selectedSources) {
    const s = view.selectedSources;
    info.append(el(doc, "p", "", "Agent source (prompt)"), el(doc, "p", "", `Tests source · ${s.testsState}`), pathControls(doc, s.testsPath, "tests"));
    if (s.testsRevision) info.append(el(doc, "code", "revision", s.testsRevision));
    if (s.validation === "invalid_pair") info.append(el(doc, "p", "", "These sources need repair before Test. Your drafts can still be saved."));
  }
  const head = el(doc, "header", "agent-header"), identity = el(doc, "div", "agent-identity"), actions = el(doc, "div", "header-controls");
  identity.append(el(doc, "span", "agent-eyebrow", "Your agent"), el(doc, "h1", "", displayName));
  actions.append(...actionNodes);
  if (readOnly) {
    const id = effectiveModel(view.model, "", view.chatModel);
    const model = el(doc, "span", "model-readonly", `${modelPresentation(id).name} · ${view.model ? "from the agent file" : "default"}`);
    model.setAttribute("title", id); actions.append(model);
  }
  const purpose = purposeText(view), summary = el(doc, "p", "purpose");
  const chars = Array.from(purpose); summary.textContent = chars.length > 160 ? chars.slice(0, 159).join("") + "…" : purpose;
  identity.append(summary, source); head.append(identity, actions);
  if (chars.length > 160) info.append(el(doc, "p", "", purpose));
  return { head, info };
}

export function saveButton(doc, button, label, action, className = "ib save-run") {
  const node = button("", action, className, "save");
  node.setAttribute("aria-label", label); node.setAttribute("title", `${label} (Enter)`);
  node.append(icon(doc, "enter", 16, 2.2));
  return node;
}
export function runNotice(doc, button, alert, dirty, modelContext, retry) {
  const box = el(doc, "div", "notice notice-warn run-alert"); box.setAttribute("role", "alert");
  const reason = alert.text.replace(/\.\s*$/, "");
  const text = alert.refused ? `Refused: ${reason}.` : alert.finished ? `The run didn't finish: ${reason}.` : `The run didn't start: ${reason}.`;
  const tail = alert.saved ? " Your changes are saved." : dirty ? " Your edits are kept." : "";
  const line = el(doc, "span", "notice-text"); line.append(icon(doc, "warn", 15, 2), el(doc, "span", "", text + tail + (alert.refused ? modelContext : "")));
  box.append(line);
  if (!alert.refused) box.append(button("Try again", retry, "sb", "save"));
  return box;
}

export function conflictNotice(doc, button, items, compareOpen, toggleCompare, keepMine, useTheirs) {
  const box = el(doc, "div", "notice notice-info"); box.setAttribute("role", "alert");
  const parts = [];
  if (items.some(([name]) => name === "Prompt")) parts.push("the prompt");
  const tests = items.filter(([name]) => name.startsWith("Test ")).length;
  if (tests) parts.push(plural(tests, "test"));
  const others = items.length - tests - (parts[0] === "the prompt" ? 1 : 0);
  if (others) parts.push(plural(others, "other part"));
  const head = el(doc, "span", "notice-text"); head.append(icon(doc, "sync", 15, 2),
    el(doc, "span", "", `This agent changed outside the panel${parts.length ? `: ${parts.join(", ")}` : ""}. You have unsaved edits.`));
  const actions = el(doc, "div", "notice-actions");
  const compare = button("Compare", toggleCompare, "sb", "nav");
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

// The whole Copilot header, as written. It is shown only; none of it is editable here.
export function preservedSettings(doc, header) {
  const box = el(doc, "section", "preserved-settings");
  box.append(el(doc, "h2", "lbl", "Preserved settings (read-only)"),
    el(doc, "p", "quiet", "Kept exactly as written in the agent file. Tests use the model and tools shown in this panel; the other settings are kept but not applied here."),
    el(doc, "pre", "code-block preserved-header", header));
  return box;
}

// Inline go-ahead before tests are written into a Copilot file's prompt.
export function copilotTestsNotice(doc, button, goAhead, cancel) {
  const box = el(doc, "div", "notice notice-warn copilot-tests"); box.setAttribute("role", "alert");
  const text = el(doc, "span", "notice-text"); text.append(icon(doc, "warn", 15, 2), el(doc, "span", "", COPILOT_TESTS_NOTICE));
  const actions = el(doc, "div", "notice-actions"), no = button("Cancel", cancel, "sb");
  actions.append(button("Go ahead", goAhead, "sb"), no); box.append(text, actions);
  return { node: box, focus: () => no.focus() };
}

export function testNameControl(doc, settings, change) {
  const input = el(doc, "input", "test-title-input"), hint = el(doc, "p", "name-hint", "Add a name to save and run this test.");
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

export function pathControls(doc, path, role = "agent") {
  const box = el(doc, "div", "source-path");
  const button = el(doc, "button", "copy-path", "Copy path");
  button.setAttribute("type", "button");
  button.setAttribute("aria-label", `Copy ${role} path`);
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
  box.append(el(doc, "p", "path-label", role === "tests" ? "Tests file path" : "Agent file path"), el(doc, "code", "path-value", path), button, feedback);
  return box;
}
