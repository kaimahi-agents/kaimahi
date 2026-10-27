import assert from "node:assert/strict";
import test from "node:test";
import { instructionSections, replaceInstructions, replaceExamples, replaceTools, readSnapshot, hasFrontmatter, testsNeedAcknowledgement } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";

// The sample from GitHub's custom-agent documentation, plus unknown fields.
const HEADER = "---\nname: Test Specialist\ndescription: Focuses on test coverage\n# keep this comment\nfuture: {keep: 'as written'}\ntools: ['read', 'search', 'edit']\n---\n";
const BODY = "\nYou are a testing specialist.\n\n# Not a title\n## Tools\nProse, not a section.\n\n```md\n## Examples\n```\n";
const EXAMPLES = "\n## Examples\n```yaml\nexamples:\n  - id: \"one\"\n    input: \"Ada\"\n    assertions:\n      - id: \"a\"\n        type: \"contains\"\n        value: \"Hello\"\n```\n";
const FREE = HEADER + BODY;
const crlf = (s) => s.replace(/\n/g, "\r\n");
const PROMPT = "You are a testing specialist.\n\n# Not a title\n## Tools\nProse, not a section.\n\n```md\n## Examples\n```";
const one = (source) => instructionSections(source).map((s) => [s.key, s.text]);

test("a frontmatter file has one Prompt span; headings in its body are prose", () => {
  assert.deepEqual(one(FREE), [["Prompt", PROMPT]]);
  assert.deepEqual(one(FREE + EXAMPLES), [["Prompt", PROMPT]]);
  assert.equal(hasFrontmatter(FREE), true);
  assert.equal(hasFrontmatter(crlf(FREE)), true);
  assert.equal(hasFrontmatter("# Title\n\nPrompt\n"), false);
  const snap = readSnapshot({ authoring: { editable: true, source: FREE, examples: [] } });
  assert.equal(snap.main.key, "Prompt");
});

test("prose Examples and fenced lookalikes stay prompt", () => {
  for (const tail of ["\n## Examples\nNot structured.\n", "\n## Examples\n\n```yaml\nexamples: []\n```\ntrailing prose\n", "\n```yaml\n## Examples\n```\n"]) {
    assert.match(instructionSections(HEADER + "\nPrompt\n" + tail)[0].text, /Examples/);
  }
});

test("prompt edits change only the prompt and no-ops return the same bytes", () => {
  for (const source of [FREE, FREE + EXAMPLES, crlf(FREE + EXAMPLES)]) {
    assert.equal(replaceInstructions(source, []), source);
    assert.equal(replaceInstructions(source, [{ key: "Prompt", text: PROMPT }]), source);
    const next = replaceInstructions(source, [{ key: "Prompt", text: "Short.\n\nSecond." }]);
    const eol = source.includes("\r\n") ? "\r\n" : "\n";
    assert.equal(next, source.replace(PROMPT.replaceAll("\n", eol), "Short.\n\nSecond.".replaceAll("\n", eol)));
    assert.ok(next.startsWith(source.slice(0, source.indexOf("You are"))), "header bytes unchanged");
    assert.ok(next.endsWith(source.slice(source.indexOf("```", source.indexOf("```md") + 5) + 3)), "Examples/trailing bytes unchanged");
  }
  assert.throws(() => replaceInstructions(FREE, [{ key: "Identity", text: "x" }]));
});

test("a minimal prompt with or without a title is editable without inventing headings", () => {
  assert.deepEqual(one("Just a prompt.\n"), [["Prompt", "Just a prompt."]]);
  assert.deepEqual(one("# Greeter\n\nSay hello.\n\n## Anything\nElse.\n"), [["Prompt", "Say hello.\n\n## Anything\nElse."]]);
  assert.equal(replaceInstructions("# Greeter\n\nSay hello.\n", [{ key: "Prompt", text: "Say hi." }]), "# Greeter\n\nSay hi.\n");
  assert.equal(replaceInstructions("Just a prompt.", [{ key: "Prompt", text: "Other" }]), "Other");
  // Model and Tools controls are not part of the prompt.
  assert.deepEqual(one("# Greeter\n\nSay hello.\n\n## Model\n- **Preferred:** fake\n\n## Tools\n- lookup\n" + EXAMPLES), [["Prompt", "Say hello."]]);
});

test("old charters keep section editing and bytes", () => {
  const charter = "# Greeter\n\n## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n\n## Accountable Responsibilities\nReply.\n\n## Non-Responsibilities\nNone.\n\n## Collaboration and Review Authority\nReturn.\n\n## Examples\n```yaml\nexamples: []\n```\n";
  assert.deepEqual(one(charter).map(([k]) => k), ["Accountable Responsibilities", "Non-Responsibilities", "Collaboration and Review Authority"]);
  assert.equal(replaceInstructions(charter, []), charter);
  assert.equal(hasFrontmatter(charter), false);
});

test("the first test appends only an Examples section and never touches the prompt", () => {
  const added = replaceExamples(FREE, [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }]);
  assert.ok(added.startsWith(FREE));
  assert.equal(added.slice(FREE.length), EXAMPLES);
  assert.equal(instructionSections(added)[0].text, PROMPT);
  const crlfAdded = replaceExamples(crlf(FREE), [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }]);
  assert.ok(crlfAdded.startsWith(crlf(FREE)) && !/[^\r]\n/.test(crlfAdded));
  const noNewline = replaceExamples("Prompt", [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }]);
  assert.match(noNewline, /^Prompt\n\n## Examples\n```yaml\nexamples:/);
});

test("editing tests replaces only the final Examples section", () => {
  const next = replaceExamples(FREE + EXAMPLES, [{ id: "two", input: "Bo", assertions: [{ id: "b", type: "contains", value: "Hi" }] }]);
  assert.ok(next.startsWith(FREE));
  assert.match(next, /id: "two"/); assert.doesNotMatch(next, /id: "one"/);
  assert.equal(replaceExamples(FREE + EXAMPLES, []).slice(FREE.length), "\n## Examples\n```yaml\nexamples: []\n```\n");
});

test("tools splice only the tools property and keep every other header byte", () => {
  const added = replaceTools(HEADER.replace("tools: ['read', 'search', 'edit']\n", "") + BODY, ["lookup"]);
  assert.equal(added, HEADER.replace("tools: ['read', 'search', 'edit']\n", "tools: [lookup]\n") + BODY);
  assert.doesNotMatch(added, /\n## Tools\n- lookup/);
  // Unmapped aliases stay; only mapped (exact) names change.
  const swapped = replaceTools(FREE, ["lookup"], []);
  assert.equal(swapped, FREE.replace("['read', 'search', 'edit']", "['read', 'search', 'edit', lookup]"));
  assert.equal(replaceTools(FREE.replace("'search', ", ""), ["lookup"], ["edit"]), FREE.replace("'search', ", "").replace("'edit']", "lookup]"));
  assert.equal(replaceTools(FREE, ["read", "search", "edit"]), FREE, "unchanged selection is a no-op");
  const cleared = replaceTools(HEADER + BODY, []);
  assert.equal(cleared, HEADER.replace("['read', 'search', 'edit']", "[]") + BODY);
  assert.equal(replaceTools(HEADER.replace("tools: ['read', 'search', 'edit']\n", "") + BODY, []), HEADER.replace("tools: ['read', 'search', 'edit']\n", "") + BODY, "omitted stays omitted");
});

test("block lists keep their style, CRLF and trailing keys", () => {
  const source = crlf("---\ndescription: x\ntools:\n  - read\n  - lookup\nmodel: gpt-5\n---\nPrompt\n");
  const next = replaceTools(source, ["lookup", "other"], ["lookup"]);
  assert.equal(next, crlf("---\ndescription: x\ntools:\n  - read\n  - lookup\n  - other\nmodel: gpt-5\n---\nPrompt\n"));
  assert.equal(replaceTools(source, [], ["lookup"]), crlf("---\ndescription: x\ntools:\n  - read\nmodel: gpt-5\n---\nPrompt\n"));
  assert.equal(replaceTools(crlf("---\ntools:\n- a\n---\nP\n"), ["a", "b"], ["a"]), crlf("---\ntools:\n- a\n- b\n---\nP\n"));
});

test("tools in an unsupported shape are refused, never guessed", () => {
  for (const tools of ["tools: *anchor", "tools: [a, # c\n  b]", "tools:\n  - a # note", "tools: read"]) {
    assert.throws(() => replaceTools(`---\n${tools}\n---\nP\n`, ["x"]), /Unsupported/);
  }
  assert.throws(() => replaceTools(FREE, ["bad name"]));
  // A nested key named tools is not the top-level property.
  const nested = "---\nmcp:\n  tools: [x]\n---\nP\n";
  assert.equal(replaceTools(nested, ["a"]), "---\nmcp:\n  tools: [x]\ntools: [a]\n---\nP\n");
});

test("escaped tool names are decoded before matching, so a removed tool cannot survive in another spelling", () => {
  // Go decodes "\u0070ing" to ping, so removing the mapped ping must remove this item.
  assert.equal(replaceTools('---\ntools: ["\\u0070ing", \'read\']\n---\nP\n', [], ["ping"]), "---\ntools: ['read']\n---\nP\n");
  assert.equal(replaceTools('---\ntools:\n  - "\\u0070ing"\n  - "mcp/x"\n---\nP\n', [], ["ping"]), '---\ntools:\n  - "mcp/x"\n---\nP\n');
  // A kept mapped tool keeps its original lexeme; an unmapped item is never rewritten.
  const kept = '---\ntools: ["\\u0070ing", "a\\/b", \'it\'\'s\', mcp.*]\n---\nP\n';
  assert.equal(replaceTools(kept, ["ping"], ["ping"]), kept);
  assert.equal(replaceTools(kept, ["ping", "other"], ["ping"]), kept.replace("mcp.*]", "mcp.*, other]"));
  // A quote escaped inside an item does not end it early.
  assert.equal(replaceTools('---\ntools: ["a\\"b, c", ping]\n---\nP\n', [], ["ping"]), '---\ntools: ["a\\"b, c"]\n---\nP\n');
  // Escapes only YAML defines cannot be decoded safely here: refuse before any write.
  for (const item of ['"\\x70ing"', '"p\\_ing"', '"\\Uu0000070ing"']) assert.throws(() => replaceTools(`---\ntools: [${item}]\n---\nP\n`, [], ["ping"]), /Unsupported/, item);
});

test("quoted top-level tools keys are the tools property, not a second one", () => {
  for (const key of ['"tools"', "'tools'"]) {
    assert.equal(replaceTools(`---\n${key}: [read, ping]\n---\nP\n`, ["lookup"], ["ping"]), `---\n${key}: [read, lookup]\n---\nP\n`, key);
    assert.equal(replaceTools(`---\n${key} : [ping]\nmodel: x\n---\nP\n`, [], ["ping"]), `---\n${key}: []\nmodel: x\n---\nP\n`.replace(": []", ": []"), key);
    assert.equal(replaceTools(`---\n${key}:\n  - ping\n---\nP\n`, ["ping", "b"], ["ping"]), `---\n${key}:\n  - ping\n  - b\n---\nP\n`, key);
    assert.equal(replaceTools(`---\n${key}: [ping]\n---\nP\n`, ["ping"], ["ping"]), `---\n${key}: [ping]\n---\nP\n`, "unchanged is a no-op");
  }
  // Only a top-level key counts; a different quoted key is untouched.
  assert.equal(replaceTools('---\n"mytools": [a]\n---\nP\n', ["x"]), '---\n"mytools": [a]\ntools: [x]\n---\nP\n');
  // An escaped spelling of the key is refused rather than guessed.
  for (const key of ['"to\\u006fls"', '"t\\x6fols"']) assert.throws(() => replaceTools(`---\n${key}: [a]\n---\nP\n`, ["x"]), /Unsupported/, key);
});

test("a minimal file with a Tools section still edits that section", () => {
  const source = "# Greeter\n\nSay hello.\n\n## Tools\n- lookup\n";
  assert.equal(replaceTools(source, ["lookup", "other"]), "# Greeter\n\nSay hello.\n\n## Tools\n- lookup\n- other\n\n");
  assert.equal(replaceTools("Say hello.\n", ["a"]), "Say hello.\n\n## Tools\n- a\n\n");
});

test("acknowledgement is needed only when a frontmatter file's tests change", () => {
  const test1 = [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }];
  assert.equal(testsNeedAcknowledgement(FREE, test1, test1), false);
  assert.equal(testsNeedAcknowledgement(FREE, [], test1), true);
  assert.equal(testsNeedAcknowledgement(FREE, test1, []), true);
  assert.equal(testsNeedAcknowledgement(FREE, test1, [{ ...test1[0], input: "Bo" }]), true);
  assert.equal(testsNeedAcknowledgement("# T\n\nPrompt\n", [], test1), false);
});

test("final Examples is structured only for one closed yaml fence with a root examples key", () => {
  const block = (fence) => HEADER + "\nPrompt\n\n## Examples\n" + fence;
  for (const yaml of ["examples: []", "{examples: []}", "{ 'examples': [] }", "{\"examples\": [{id: a}], other: 1}", "\"examples\": []", "'examples': []", "# note\nexamples:\n  - id: a"]) {
    const source = block("```yaml\n" + yaml + "\n```\n");
    assert.equal(instructionSections(source)[0].text, "Prompt", yaml);
    const next = replaceExamples(source, []);
    assert.equal(next.match(/## Examples/g).length, 1, "the structured block is replaced, never duplicated: " + yaml);
    assert.equal(next, source.slice(0, source.indexOf("## Examples")) + "## Examples\n```yaml\nexamples: []\n```\n");
  }
  for (const yaml of ["other: 1", "parent:\n  examples: []", "{parent: {examples: []}}", "{other: 'examples'}", "- examples: []"]) {
    const source = block("```yaml\n" + yaml + "\n```\n");
    assert.match(instructionSections(source)[0].text, /## Examples/, yaml);
  }
  for (const tail of ["```yaml\nexamples: []\n", "```yaml\nexamples: []\n```\n```yaml\nexamples: []\n```\n", "```json\n{\"examples\": []}\n```\n", "Some prose first.\n```yaml\nexamples: []\n```\n"]) {
    assert.match(instructionSections(block(tail))[0].text, /## Examples/, tail);
  }
  // A non-structured Examples section is prompt: adding the first test appends a new final section.
  const prose = HEADER + "\nPrompt\n\n## Examples\nOnly prose.\n";
  const added = replaceExamples(prose, [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }]);
  assert.ok(added.startsWith(prose)); assert.equal(added.match(/## Examples/g).length, 2);
});

test("only a --- line closes frontmatter; a YAML document-end marker stays in the header", () => {
  const source = "---\nname: a\ndescription: b\n...\n---\n\nThe prompt.\n";
  assert.deepEqual(one(source), [["Prompt", "The prompt."]]);
  assert.equal(replaceInstructions(source, []), source);
  assert.equal(replaceInstructions(source, [{ key: "Prompt", text: "Other." }]), "---\nname: a\ndescription: b\n...\n---\n\nOther.\n");
  assert.equal(replaceTools(source, ["x"]), "---\nname: a\ndescription: b\ntools: [x]\n...\n---\n\nThe prompt.\n", "tools stay inside the YAML document, before its end marker");
});

test("tools stay before YAML document end when comments follow the marker", () => {
  for (const end of ["...\n# kept", "... # end\n# kept"]) {
    const source = `---\nname: a\n${end}\n---\nPrompt\n`;
    assert.equal(replaceTools(source, ["ping"]), source.replace("name: a\n", "name: a\ntools: [ping]\n"));
  }
});

test("H1 after a minimal control remains visible editable prompt text", () => {
  const source = "# Agent\n\nPrompt\n\n## Model\n- **Preferred:** fake\n\n# More\nTail\n";
  assert.deepEqual(one(source), [["Prompt", "Prompt\n\n# More\nTail"]]);
  assert.equal(replaceInstructions(source, []), source);
  assert.ok(replaceInstructions(source, [{key: "Prompt", text: "New"}]).includes("## Model\n- **Preferred:** fake\n"));
});

const MID = "# Agent\n\nPrompt\n\n## Model\n- **Preferred:** fake\n\n## Tools\n- ping\n\n## Other\nTail\n";
test("Model and Tools controls anywhere keep their bytes; the prompt is the remaining prose", () => {
  assert.deepEqual(one(MID), [["Prompt", "Prompt\n\n## Other\nTail"]]);
  assert.equal(replaceInstructions(MID, []), MID);
  assert.equal(replaceInstructions(MID, [{ key: "Prompt", text: "Prompt\n\n## Other\nTail" }]), MID);
  const edited = replaceInstructions(MID, [{ key: "Prompt", text: "New.\n\n## Other\nTail 2" }]);
  assert.equal(edited, "# Agent\n\nNew.\n\n## Other\nTail 2\n\n## Model\n- **Preferred:** fake\n\n## Tools\n- ping\n\n");
  assert.deepEqual(one(edited), [["Prompt", "New.\n\n## Other\nTail 2"]]);
  const crlfEdited = replaceInstructions(crlf(MID), [{ key: "Prompt", text: "A" }]);
  assert.ok(crlfEdited.includes("## Model\r\n- **Preferred:** fake\r\n\r\n## Tools\r\n- ping\r\n") && !/[^\r]\n/.test(crlfEdited));
});

test("tools saves update the one existing Tools section in place", () => {
  assert.equal(replaceTools(MID, ["ping", "other"]), MID.replace("- ping\n", "- ping\n- other\n"));
  assert.equal(replaceTools(MID, ["ping"]), MID);
  assert.equal(replaceTools(MID, []), MID.replace("## Tools\n- ping\n\n", ""));
  const noTools = "# Agent\n\nPrompt\n\n## Model\n- **Preferred:** fake\n\n## Other\nTail\n";
  const added = replaceTools(noTools, ["ping"]);
  assert.equal(added.match(/## Tools/g).length, 1); assert.ok(added.startsWith(noTools));
  assert.equal(replaceTools(replaceTools(MID, ["a"]), ["a", "b"]).match(/## Tools/g).length, 1);
  assert.equal(replaceTools("# Agent\n\n## Tools\n- ping\n\n## Examples\nprose\n", ["ping", "x"]).match(/## Tools/g).length, 1);
});

const NO_EXAMPLES = "# Greeter\n\n## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n\n## Accountable Responsibilities\nReply.\n\n## Non-Responsibilities\nNone.\n\n## Collaboration and Review Authority\nReturn.\n\n## Model\n- **Preferred:** fake\n";
test("a complete old charter without Examples keeps its three editors and frozen metadata", () => {
  const keys = (source) => one(source).map(([key]) => key);
  const three = ["Accountable Responsibilities", "Non-Responsibilities", "Collaboration and Review Authority"];
  for (const source of [NO_EXAMPLES, crlf(NO_EXAMPLES), NO_EXAMPLES.replace("\n## Model\n- **Preferred:** fake\n", "")]) {
    assert.deepEqual(keys(source), three);
    assert.equal(replaceInstructions(source, []), source, "no-op keeps every byte");
    const eol = source.includes("\r\n") ? "\r\n" : "\n";
    const edited = replaceInstructions(source, [{ key: three[0], text: "Reply warmly." }]);
    assert.equal(edited, source.replace("Reply.", "Reply warmly."));
    assert.ok(edited.includes("- **ID:** greeter" + eol) && edited.includes("## Identity"));
    assert.equal(hasFrontmatter(source), false);
  }
  const test1 = [{ id: "one", input: "Ada", assertions: [{ id: "a", type: "contains", value: "Hello" }] }];
  const added = replaceExamples(NO_EXAMPLES, test1);
  assert.ok(added.startsWith(NO_EXAMPLES)); assert.equal(added.match(/## Examples/g).length, 1);
  assert.deepEqual(keys(added), three);
  assert.equal(replaceExamples(added, []).match(/## Examples/g).length, 1);
});

test("tools in a charter without Examples are appended once, then edited in place", () => {
  const added = replaceTools(NO_EXAMPLES, ["ping"]);
  assert.equal(added, NO_EXAMPLES + "\n## Tools\n- ping\n\n");
  assert.equal(replaceTools(added, ["ping", "x"]).match(/## Tools/g).length, 1);
  assert.equal(replaceTools(added, []), NO_EXAMPLES + "\n");
  assert.equal(one(added).length, 3);
});
