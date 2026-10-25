import assert from "node:assert/strict";
import test from "node:test";
import { resultHistory } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/editor.mjs";
import { instructionSections, replaceInstructions, replaceExamples, effectiveModel, runtimeModelSelection } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";

const KEYS = ["Accountable Responsibilities", "Non-Responsibilities", "Collaboration and Review Authority"];
const SOURCE = `# Greeter — Greeting 🌱

## Identity
- **ID:** greeter
- **Purpose:** Greet a name.

## Accountable Responsibilities
Reply with exactly Hello.

## Non-Responsibilities
Do not call tools.

## Collaboration and Review Authority
Return the greeting.

## Model
- **Preferred:** fake

## Examples

` + "```yaml\n# Keep this comment verbatim on instruction saves.\nexamples:\n  - id: greeting\n    input: Ada\n    assertions: [{id: greeting-text, type: contains, value: Hello}]\n```\n";
test("omitted authored Model stays omitted through instruction and example edits", () => {
  const source = SOURCE.replace("## Model\n- **Preferred:** fake\n\n", "");
  assert.equal(instructionSections(source).length, 3);
  assert.equal(replaceInstructions(source, []), source);
  assert.doesNotMatch(replaceExamples(source, []), /## Model|gpt-5/);
});

test("effective runtime model preserves legacy display metadata and fixed precedence", () => {
  assert.equal(effectiveModel("provider: fake\nmodel: fake", ""), "fake");
  assert.equal(effectiveModel("authored", "picked"), "picked");
  assert.equal(effectiveModel("authored", ""), "authored");
  assert.equal(effectiveModel("", ""), "gpt-5.6-terra");
});

test("known chat model is the exact default below authored and canvas choices", () => {
  assert.equal(effectiveModel("", "", "uncatalogued-chat"), "uncatalogued-chat");
  assert.equal(effectiveModel("authored", "", "chat"), "authored");
  assert.equal(effectiveModel("authored", "picked", "chat"), "picked");
});

test("chat metadata changes fence A-B-A without invalidating matching late metadata", () => {
  const selection = runtimeModelSelection();
  const view = { model: "", latest: { status: "current", result: { modelRoute: "copilot/chat-a" } } };
  selection.view(view);
  assert.equal(selection.view({ ...view, chatModel: "chat-a" }).latest.status, "current");
  assert.equal(selection.view({ ...view, chatModel: "chat-b" }).latest.status, "stale");
  assert.equal(selection.view({ ...view, chatModel: "chat-a" }).latest.status, "stale");
  selection.confirm({ ...view, chatModel: "chat-a" });
  assert.equal(selection.view({ ...view, chatModel: "chat-a" }).latest.status, "current");
});

test("an attested pre-route refusal is current but never restores an old model pass", () => {
  const selection = runtimeModelSelection();
  const view = { model: "fake", latest: { status: "current", result: { state: "refused", verdict: "unknown", fullPass: false, reasonCode: "no_tests", modelRoute: "/" } } };
  assert.equal(selection.view(view).latest.status, "current");
  selection.choose("other", view); assert.equal(selection.view(view).latest.status, "stale");
  selection.confirm(view); assert.equal(selection.view(view).latest.status, "current");
  selection.choose("", view); assert.equal(selection.view(view).latest.status, "stale");
});

const BODY = "Reply with exactly Hello.";
const examples = () => [{ id: "hello", input: "Ada", assertions: [{ id: "text", type: "contains", value: "Hello" }] }];
const clone = (value) => JSON.parse(JSON.stringify(value));

function historyView(model, scope = "full") {
  return {
    file: { revision: "same-revision" }, authoring: { examples: examples() }, examples: examples(),
    latest: { status: "current", result: {
      modelRoute: `copilot/${model}`, state: "complete", fullPass: scope === "full",
      selection: { scope, caseIds: ["hello"], totalCases: 1 },
      examples: [{ id: "hello", state: "complete", verdict: "pass",
        assertions: [{ id: "text", type: "contains", verdict: "pass" }] }],
    } },
  };
}
function stoppedAttempt(history, model) {
  const view = historyView(model);
  const run = { id: `stop-${model}`, model, revision: view.file.revision, state: "stopped",
    caseIds: ["hello"], totalCases: 1, cases: [], elapsedMs: 100 };
  history.stop(run, view);
  return run;
}

test("stopped attempts for both retained models survive transient stop eviction", () => {
  const a = historyView("A"), b = historyView("B"), history = resultHistory(a);
  history.remember(b);
  const stoppedA = stoppedAttempt(history, "A"), stoppedB = stoppedAttempt(history, "B");
  stoppedAttempt(history, "C");
  assert.equal(history.selected(a, "A"), undefined, "a newer stop must mask the old full pass");
  assert.equal(history.attempt("A").run, stoppedA);
  assert.equal(history.attempt("B").run, stoppedB);
  assert.ok(history.attempt("C"));

  stoppedAttempt(history, "D");
  assert.equal(history.attempt("C"), undefined, "evict the unretained transient, not a retained model's stop");
  assert.ok(history.attempt("D"));
  assert.equal(history.attempt("A").run, stoppedA);
  assert.equal(history.attempt("B").run, stoppedB);
  assert.equal(history.selected(a, "A"), undefined);
  assert.equal(history.selected(b, "B"), undefined);
  assert.deepEqual(history.pair(), [a, b]);
  assert.equal(["A", "B", "C", "D"].filter(model => history.attempt(model)).length, 3);
});

test("remembering transient partial results preserves retained models' stopped markers", () => {
  const a = historyView("A"), b = historyView("B"), history = resultHistory(a);
  history.remember(b);
  const stoppedA = stoppedAttempt(history, "A"), stoppedB = stoppedAttempt(history, "B");
  history.remember(historyView("C", "subset"), true);
  history.remember(historyView("D", "subset"), true);
  assert.equal(history.attempt("A")?.run, stoppedA);
  assert.equal(history.attempt("B")?.run, stoppedB);
  assert.equal(history.selected(a, "A"), undefined);
  assert.equal(history.selected(b, "B"), undefined);
  assert.equal(history.attempt("C"), undefined);
  assert.equal(history.selected(a, "D").latest.result.selection.scope, "subset");
  assert.deepEqual(history.pair(), [a, b]);
});

test("a third full result stays bounded when both retained routes share the focused model ID", () => {
  const first = historyView("shared"), second = historyView("shared"), third = historyView("C");
  first.latest.result.modelRoute = "first/shared";
  second.latest.result.modelRoute = "second/shared";
  const history = resultHistory(first);
  history.remember(second); history.prefer("shared"); history.focus("shared");
  history.remember(third);
  assert.deepEqual(history.pair(), [second, third]);
});

test("a new full completion clears the retained model's stopped marker", () => {
  const a = historyView("A"), history = resultHistory(a);
  history.remember(historyView("B"));
  stoppedAttempt(history, "A"); stoppedAttempt(history, "B"); stoppedAttempt(history, "C");
  assert.ok(history.attempt("A"));
  const completion = historyView("A");
  history.remember(completion, true);
  assert.equal(history.attempt("A"), undefined);
  assert.equal(history.selected(completion, "A").latest.result, completion.latest.result);
  assert.ok(history.attempt("B"), "completing A must not clear B's stop");
});

// Wrong offsets, stripping untouched whitespace, or reserializing YAML must fail.
test("instructions extract only canonical bodies and preserve an unchanged save byte-for-byte", () => {
  assert.deepEqual(instructionSections(SOURCE), [
    { key: KEYS[0], text: BODY }, { key: KEYS[1], text: "Do not call tools." },
    { key: KEYS[2], text: "Return the greeting." },
  ]);
  assert.equal(replaceInstructions(SOURCE, []), SOURCE);
  assert.equal(replaceInstructions(SOURCE, instructionSections(SOURCE)), SOURCE);
  const changed = replaceInstructions(SOURCE, [{ key: KEYS[0], text: "Greet Māori 🌱.\nKeep it short." }]);
  assert.equal(changed, SOURCE.replace(BODY, "Greet Māori 🌱.\nKeep it short."));
});

test("CRLF and Unicode offsets retain every unedited byte and blank-line boundary", () => {
  const source = SOURCE.replace(BODY, "\n\t\n" + BODY + "  \n \n").replaceAll("\n", "\r\n");
  const sections = instructionSections(source);
  assert.equal(sections[0].text, BODY + "  ");
  assert.equal(replaceInstructions(source, sections), source);
  assert.equal(replaceInstructions(source, [{ key: KEYS[0], text: "Kia ora 🌱\nAgain" }]),
    source.replace(BODY + "  ", "Kia ora 🌱\r\nAgain"));
  const multiline = SOURCE.replace(BODY, "First\nSecond").replaceAll("\n", "\r\n");
  assert.equal(replaceInstructions(multiline, [{ key: KEYS[0], text: "First\nSecond" }]), multiline);
});

test("fenced heading lookalikes are prose, including longer and indented fences", () => {
  for (const [open, close] of [["```md", "```"], ["   ~~~~text", "  ~~~~~"], ["````", "`````"]]) {
    const shorterClose = open === "````" ? "```\n" : "";
    const body = `${open}\n## Examples\n## ${KEYS[0]}\n${shorterClose}${close}\nKeep this.`;
    // A shorter backtick close cannot terminate a four-backtick fence.
    const source = SOURCE.replace(BODY, body);
    assert.equal(instructionSections(source)[0].text, body);
    assert.equal(replaceInstructions(source, [{ key: KEYS[1], text: "No tools." }]), source.replace("Do not call tools.", "No tools."));
  }
});

test("multiple instruction changes protect headings, identity, model and original YAML", () => {
  assert.equal(replaceInstructions(SOURCE, [{ key: KEYS[2], text: "Ask the caller." }, { key: KEYS[0], text: "Say hello." }]),
    SOURCE.replace(BODY, "Say hello.").replace("Return the greeting.", "Ask the caller."));
});

test("instruction updates reject unknown/duplicate keys, nonstrings and empty bodies", () => {
  for (const changes of [null, {}, [null], [{ key: "Identity", text: "overwrite" }],
    [{ key: KEYS[0], text: 1 }], [{ key: KEYS[0], text: " \n\t" }],
    [{ key: KEYS[0], text: "a" }, { key: KEYS[0], text: "b" }], [{ key: KEYS[0], text: "a", extra: true }]]) {
    assert.throws(() => replaceInstructions(SOURCE, changes));
  }
});

test("minimal, preamble, duplicated/indented headings, empty bodies and unclosed fences refuse", () => {
  const bad = ["# Minimal\n\nGreet a name.\n## Model\nfake\n## Examples\n```yaml\nexamples: []\n```\n",
    SOURCE.replace("## Identity", "Unscoped preamble.\n\n## Identity"),
    SOURCE.replace("## Model", `## ${KEYS[0]}\nMore\n\n## Model`),
    SOURCE.replace(`## ${KEYS[0]}`, ` ## ${KEYS[0]}`),
    SOURCE.replace(BODY, " \t"), SOURCE.replace(BODY, "```md\nUnclosed")];
  for (const source of bad) {
    assert.throws(() => instructionSections(source));
    assert.throws(() => replaceInstructions(source, []));
    assert.throws(() => replaceExamples(source, examples()));
  }
});

test("source and resulting documents are bounded by UTF-8 bytes, not UTF-16 length", () => {
  for (const source of [null, 42, SOURCE + "x".repeat(256 * 1024), SOURCE.replace(BODY, "🌱".repeat(70000))]) {
    assert.throws(() => instructionSections(source));
    assert.throws(() => replaceExamples(source, examples()));
  }
  const exact = SOURCE.replace(BODY, "x".repeat(256 * 1024 - new TextEncoder().encode(SOURCE).length + BODY.length));
  assert.equal(replaceInstructions(exact, []), exact);
  assert.throws(() => replaceInstructions(exact, [{ key: KEYS[0], text: instructionSections(exact)[0].text + "x" }]));
  const large = examples(); large[0].input = "x".repeat(256 * 1024);
  assert.throws(() => replaceExamples(SOURCE, large));
});

test("example save rewrites only Examples to EOF with quoted full strings and judge zero", () => {
  const source = SOURCE.replaceAll("\n", "\r\n");
  const original = [{ id: "hello-1", input: "Ada\nMāori 🌱: \"yes\"\\path", assertions: [
    { id: "contains", type: "contains", value: "Hello: # quoted" },
    { id: "absent", type: "notContains", value: "Goodbye" },
    { id: "shape", type: "regex", pattern: "^Hello\\s+.*$" },
    { id: "judge", type: "llmJudge", rubric: "Greet well.\nBe clear.", minScore: 0, judgeModel: "exact-model", judgeVersion: "v1" },
  ] }];
  const before = clone(original);
  const prefix = source.slice(0, source.indexOf("## Examples"));
  const suffix = '## Examples\n```yaml\nexamples:\n  - id: "hello-1"\n    input: "Ada\\nMāori 🌱: \\"yes\\"\\\\path"\n    assertions:\n      - id: "contains"\n        type: "contains"\n        value: "Hello: # quoted"\n      - id: "absent"\n        type: "notContains"\n        value: "Goodbye"\n      - id: "shape"\n        type: "regex"\n        pattern: "^Hello\\\\s+.*$"\n      - id: "judge"\n        type: "llmJudge"\n        rubric: "Greet well.\\nBe clear."\n        minScore: 0\n        judgeModel: "exact-model"\n        judgeVersion: "v1"\n```\n';
  assert.equal(replaceExamples(source, original), prefix + suffix.replaceAll("\n", "\r\n"));
  assert.deepEqual(original, before);
});

test("example serialization never clips full operands, inputs or judge metadata", () => {
  const rows = examples(); rows[0].input = "Māori".repeat(1200);
  rows[0].assertions = [{ id: "judge", type: "llmJudge", rubric: "A".repeat(4000), minScore: 100, judgeModel: "model-" + "m".repeat(200), judgeVersion: "version-" + "v".repeat(200) }];
  const output = replaceExamples(SOURCE, rows);
  for (const value of [rows[0].input, rows[0].assertions[0].rubric, rows[0].assertions[0].judgeModel, rows[0].assertions[0].judgeVersion]) assert.ok(output.includes(JSON.stringify(value)));
});

test("example and assertion count/ID bounds accept maxima and reject overflow or duplicates", () => {
  const checks = Array.from({ length: 64 }, (_, i) => ({ id: `check.${i}`, type: "contains", value: "yes" }));
  const rows = Array.from({ length: 128 }, (_, i) => ({ id: `case_${i}`, input: "Ada", assertions: i === 0 ? checks : examples()[0].assertions }));
  assert.ok(replaceExamples(SOURCE, rows).includes('id: "case_127"'));
  for (const invalid of [rows.concat({ ...rows[0], id: "extra" }),
    [{ ...rows[0], assertions: checks.concat({ ...checks[0], id: "extra" }) }],
    [rows[0], rows[0]], [{ ...rows[0], id: "-bad" }], [{ ...rows[0], id: "a".repeat(64) }],
    [{ ...rows[0], assertions: [] }], [{ ...rows[0], assertions: [checks[0], checks[0]] }],
    [{ ...rows[0], assertions: [{ ...checks[0], id: "bad id" }] }]]) assert.throws(() => replaceExamples(SOURCE, invalid));
});

test("unknown fields/types, tool assertions, wrong scalar types and incomplete judges refuse", () => {
  for (const invalid of [null, {}, [null], [{ ...examples()[0], unknown: "keep?" }],
    [{ ...examples()[0], input: 1 }], [{ ...examples()[0], input: " " }]]) assert.throws(() => replaceExamples(SOURCE, invalid));
  for (const check of [null, { id: "a", type: ["contains"], value: "Hello" }, { id: "a", type: "equals", value: "Hello" },
    { id: "a", type: "toolCalled", tool: "exec" }, { id: "a", type: "contains", value: "Hello", unknown: "no" },
    { id: "a", type: "contains", value: "Hello", pattern: "" }, { id: "a", type: "contains", value: null },
    { id: "a", type: "regex", pattern: 2 }, { id: "a", type: "llmJudge", rubric: "Judge" }]) {
    assert.throws(() => replaceExamples(SOURCE, [{ ...examples()[0], assertions: [check] }]));
  }
  const judge = { id: "judge", type: "llmJudge", rubric: "Judge", minScore: 0, judgeModel: "exact", judgeVersion: "v1" };
  for (const minScore of [null, "0", -1, 101, 1.5, NaN]) assert.throws(() => replaceExamples(SOURCE, [{ ...examples()[0], assertions: [{ ...judge, minScore }] }]));
});

test("an omitted Judge model follows the run without serializing a guessed pin", () => {
  for (const eol of ["\n", "\r\n"]) for (const model of [undefined, ""]) {
    const judge = { id: "judge", type: "llmJudge", rubric: "Judge well.", minScore: 0, judgeVersion: "v1", ...(model !== undefined ? { judgeModel: model } : {}) };
    const source = replaceExamples(SOURCE.replaceAll("\n", eol), [{ ...examples()[0], assertions: [judge] }]);
    assert.doesNotMatch(source, /judgeModel:|undefined/); assert.match(source, /judgeVersion: "v1"/);
    assert.deepEqual(instructionSections(source), instructionSections(SOURCE.replaceAll("\n", eol)));
    for (const version of [undefined, "", " "]) assert.throws(() => replaceExamples(SOURCE, [{ ...examples()[0], assertions: [{ ...judge, judgeVersion: version }] }]));
    for (const judgeModel of [null, 2, " "]) assert.throws(() => replaceExamples(SOURCE, [{ ...examples()[0], assertions: [{ ...judge, judgeModel }] }]));
  }
});

test("zero examples serialize as an explicit empty sequence", () => {
  const source = SOURCE.replaceAll("\n", "\r\n");
  const prefix = source.slice(0, source.indexOf("## Examples"));
  assert.equal(replaceExamples(source, []), prefix + "## Examples\r\n```yaml\r\nexamples: []\r\n```\r\n");
  assert.deepEqual(instructionSections(replaceExamples(SOURCE, [])), instructionSections(SOURCE));
});
