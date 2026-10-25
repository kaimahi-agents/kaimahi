import assert from "node:assert/strict";
import test from "node:test";
import * as editor from "../../copilot-wsl/com.github.copilot/extensions/agent-view/editor.mjs";

// These helpers only need inert node construction, attributes and nesting.
function fakeDoc() {
  const doc = { activeElement: undefined, createElement(tagName) {
    return {
      tagName, children: [], attributes: new Map(), style: {}, hidden: false,
      textContent: "", value: "",
      append(...nodes) { for (const node of nodes) { node.parentNode = this; this.children.push(node); } },
      replaceChildren(...nodes) { this.children = []; this.append(...nodes); },
      setAttribute(key, value) { this.attributes.set(key, String(value)); },
      getAttribute(key) { return this.attributes.get(key) ?? null; },
      addEventListener() {},
      focus() { doc.activeElement = this; },
    };
  }, createElementNS(ns, tagName) { return doc.createElement(tagName); } };
  return doc;
}
const descendants = node => [node, ...node.children.flatMap(descendants)];
const judge = over => ({ type: "llmJudge", rubric: "Be accurate", minScore: 90, judgeVersion: "v1", ...over });

test("checkIssue identifies the first missing operand before Judge settings", () => {
  assert.equal(typeof editor.checkIssue, "function");
  for (const [type, field] of [["contains", "value"], ["notContains", "value"], ["regex", "pattern"], ["llmJudge", "rubric"]]) {
    const issue = editor.checkIssue({ type, [field]: "  " });
    assert.equal(issue.field, field);
    assert.ok(issue.message.length > 0);
    assert.equal(editor.checkComplete({ type, [field]: "  " }), false);
  }
});

test("checkIssue rejects noninteger or out-of-range scores without filling defaults", () => {
  assert.equal(typeof editor.checkIssue, "function");
  for (const minScore of [undefined, "", "90", -1, 101, 0.5, NaN, Infinity]) {
    assert.deepEqual(editor.checkIssue(judge({ minScore })), { field: "minScore", message: "Needs a minimum score, 0 to 100" });
  }
  for (const minScore of [0, 90, 100]) assert.equal(editor.checkIssue(judge({ minScore })), undefined);
});

test("checkIssue validates model and version while leaving read-only checks alone", () => {
  assert.equal(typeof editor.checkIssue, "function");
  for (const judgeModel of [null, 42, "  "]) assert.equal(editor.checkIssue(judge({ judgeModel })).field, "judgeModel");
  for (const judgeVersion of [undefined, "", "  ", 42]) {
    assert.deepEqual(editor.checkIssue(judge({ judgeVersion })), { field: "judgeVersion", message: "Needs a Judge version" });
  }
  for (const check of [{ type: "contains", value: "hello" }, { type: "regex", pattern: "hi" }, { type: "equals" }, judge({}), judge({ judgeModel: "" }), judge({ judgeModel: "copilot/pinned" })]) {
    const before = structuredClone(check);
    assert.equal(editor.checkIssue(check), undefined);
    assert.equal(editor.checkComplete(check), true);
    assert.deepEqual(check, before);
  }
});

test("markIssue places one inert accessible hint under its input and clears markers", () => {
  const doc = fakeDoc(), changes = [];
  const fields = editor.checkFields(doc, judge({}), 0, (...args) => changes.push(args), () => {});
  const inputs = descendants(fields.node).filter(node => ["input", "textarea"].includes(node.tagName));
  assert.equal(typeof fields.markIssue, "function");
  const target = fields.markIssue({ field: "minScore", message: "Needs a minimum score, 0 to 100" });
  assert.equal(target, inputs[1]);
  assert.equal(doc.activeElement, undefined, "focus is caller-owned after render");
  assert.equal(target.getAttribute("aria-invalid"), "true");
  const hint = descendants(fields.node).find(node => node.id === target.getAttribute("aria-describedby"));
  assert.ok(hint);
  assert.equal(hint.textContent, "Needs a minimum score, 0 to 100");
  assert.equal(target.parentNode.children.indexOf(hint), target.parentNode.children.indexOf(target) + 1);
  assert.equal(hint.children.length, 0);
  const advanced = descendants(fields.node).find(node => node.tagName === "details");
  assert.equal(advanced.open, false);
  const trigger = descendants(fields.node).find(node => node.tagName === "button" && /\bmodel-picker\b/.test(node.className));
  assert.equal(fields.markIssue(editor.checkIssue(judge({ judgeModel: "  " }))), trigger, "a model issue focuses the menu button");
  assert.equal(advanced.open, false, "only the version lives in Advanced");
  assert.doesNotMatch(descendants(fields.node).find(node => node.id === trigger.getAttribute("aria-describedby")).textContent, /name|type/i);
  assert.equal(fields.markIssue({ field: "judgeVersion", message: "Needs a Judge version" }), inputs[2]);
  assert.equal(advanced.open, true);
  assert.equal(inputs[1].getAttribute("aria-invalid"), "false");
  assert.equal(inputs[1].getAttribute("aria-describedby"), "");
  assert.equal(descendants(fields.node).filter(node => !node.hidden && node.textContent.startsWith("Needs a")).length, 1);
  fields.markIssue(undefined);
  assert.ok(inputs.every(input => input.getAttribute("aria-invalid") !== "true"));
  assert.equal(descendants(fields.node).filter(node => !node.hidden && node.textContent.startsWith("Needs a")).length, 0);
  assert.deepEqual(changes, []);
  assert.equal(inputs.length, 3, "no typed Judge model input");
});

test("markIssue also annotates ordinary operands and resets on type changes", () => {
  const fields = editor.checkFields(fakeDoc(), { type: "contains", value: "" }, 2, () => {}, () => {});
  assert.equal(typeof fields.markIssue, "function");
  const target = fields.markIssue({ field: "value", message: "Needs text" });
  assert.equal(target.getAttribute("aria-invalid"), "true");
  fields.show({ type: "regex", pattern: "" });
  assert.equal(fields.markIssue({ field: "pattern", message: "Needs a pattern" }).getAttribute("aria-invalid"), "true");
});

test("resultTone neutralizes old and unknown states for rows and pills", () => {
  assert.equal(typeof editor.resultTone, "function");
  for (const kind of ["pass", "fail", "error", "running"]) assert.equal(editor.resultTone(kind), kind);
  for (const kind of ["old-pass", "old-fail", "old-error", "old-running", "none", "unknown", undefined]) assert.equal(editor.resultTone(kind), "none");
  assert.equal(editor.testRow({ state: "pass" }, false, false).tone, "pass");
  assert.equal(editor.testRow({ state: "pass" }, true, false).tone, "none");
  assert.equal(editor.testRow(undefined, false, false).tone, "none");
  assert.equal(editor.testRow(undefined, false, true).tone, "none");
  assert.equal(editor.testRow(undefined, false, false, true).tone, "none");
  let tone = "old-pass";
  const pill = editor.resultPill(fakeDoc(), () => ({ tone, text: "Passed · before your edit" }));
  assert.equal(pill.node.getAttribute("data-tone"), "none");
  tone = "running"; pill.update();
  assert.equal(pill.node.getAttribute("data-tone"), "running");
});

test("model refusals name run and pinned Judge identities without blaming pins for sign-in failures", () => {
  const view = { model: "gpt-5.6-terra", latest: { result: { reasonCode: "model_unavailable" } }, authoring: { examples: [
    { assertions: [judge({ judgeModel: "no-such-model" }), judge({ judgeModel: "no-such-model" })] }
  ] } };
  assert.equal(editor.refusalContext(view, "claude-sonnet-5.5"), " Run model: Claude Sonnet 5.5. Pinned Judge models: no-such-model.");
  for (const reasonCode of ["copilot_auth_required", "copilot_quota", "copilot_policy_denied"]) {
    view.latest.result.reasonCode = reasonCode; assert.equal(editor.refusalContext(view, ""), "");
  }
});
test("Judge details collapse matching scores and reasons but preserve distinct samples", () => {
  const details = editor.judgeDetails(fakeDoc());
  const observed = { reason: "Raw proof", judge: { scores: [0, 0], minScore: 90, reasons: ["Not accurate", "Not accurate"] } };
  const before = structuredClone(observed);
  details.update(judge({}), observed, true, false);
  assert.equal(details.score.textContent, "Score 0 · needs 90 · 2 samples");
  assert.equal(details.reason.textContent, "Not accurate");
  assert.equal(details.binding.textContent, "");
  assert.equal(details.binding.hidden, true);
  assert.deepEqual(observed, before);
  details.update(judge({ judgeModel: "copilot/pinned" }), { judge: { scores: [40, 55], minScore: 70, reasons: ["First", "Second"] } }, true, false);
  assert.equal(details.score.textContent, "Scores 40, 55 · needs 70");
  assert.equal(details.reason.textContent, "First\n\nSecond");
  assert.equal(details.binding.textContent, "Judge model: copilot/pinned · pinned");
  assert.equal(details.binding.hidden, false);
  details.update(judge({}), { reason: "Raw proof" }, true, true);
  assert.equal(details.reason.textContent, "Raw proof");
  assert.equal(details.binding.hidden, true);
});
