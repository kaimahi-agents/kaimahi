import assert from "node:assert/strict";
import test from "node:test";
import { defaultToolMode, toolMode, setToolMode, toolUseText, renderTestTools } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/test-tools.mjs";

test("duplicating keeps modes but explicitly makes copied Recorded tools pending", async () => {
  const { duplicateTest } = await import("../../copilot-wsl/com.github.copilot/extensions/agent-view/test-tools.mjs");
  const raw={id:"a",input:"x",assertions:[],mocks:{tools:[{name:"read",mode:"recorded",calls:[{arguments:{},result:{value:1}}]},{name:"clock",mode:"live"}]}};
  const copy=duplicateTest(raw);assert.equal(copy.mocks.tools[0].mode,"recorded");assert.deepEqual(copy.mocks.tools[0].calls,[]);assert.equal(raw.mocks.tools[0].calls.length,1);assert.equal(copy.mocks.tools[1].mode,"live");
});

// Minimal inert DOM, following tools-view tests; markup sinks fail immediately.
function fakeDoc() {
  const mk = tag => {
    const node = {
      tag, children: [], className: "", _text: "", attrs: {}, listeners: {}, disabled: false,
      set textContent(value) { this._text = String(value); this.children = []; },
      get textContent() { return this._text + this.children.map(child => child.textContent).join(""); },
      append(...children) { this.children.push(...children); },
      setAttribute(key, value) { this.attrs[key] = String(value); },
      addEventListener(type, listener) { (this.listeners[type] ??= []).push(listener); },
    };
    for (const key of ["innerHTML", "outerHTML"]) Object.defineProperty(node, key, { set() { throw new Error(`${key} used`); } });
    node.insertAdjacentHTML = () => { throw new Error("insertAdjacentHTML used"); };
    return node;
  };
  return { createElement: mk };
}
const all = node => [node, ...node.children.flatMap(all)];
const byTag = (node, tag) => all(node).filter(child => child.tag === tag);
const buttons = root => byTag(root, "button");
const click = node => { if (!node.disabled) for (const listener of node.listeners.click ?? []) listener({}); };
function fixture() {
  return {
    id: "first", input: "Read", assertions: [{ id: "a", type: "contains", value: "Ada" }],
    mocks: { tools: [
      { name: "read", mode: "recorded", description: "Read", parameters: { type: "object", properties: { path: { type: "string" } } }, calls: [{ arguments: { path: "a" }, result: { items: ["Ada"] } }] },
      { name: "other", mode: "recorded", parameters: { type: "object" }, calls: [{ arguments: {}, result: { nested: [1, { keep: true }] } }] },
      { name: "pending", mode: "recorded", calls: [] },
      { name: "live", mode: "live" },
    ] },
  };
}
function render(overrides = {}) {
  const changes = [], resets = [];
  const root = renderTestTools(fakeDoc(), {
    names: ["read", "other", "pending", "live"], raw: fixture(), tools: [],
    onChange: (...args) => changes.push(args), onReset: name => resets.push(name), ...overrides,
  });
  return { root, changes, resets };
}

test("only actual literal matching assertion types default new tools to Recorded", () => {
  for (const type of ["contains", "notContains", "regex"]) {
    assert.equal(defaultToolMode([{ type: "llmJudge" }, { type }]), "recorded", type);
  }
  for (const assertions of [undefined, [], [{ type: "llmJudge", rubric: "Quote Ada" }], [{ type: "toolCalled", tool: "read" }], [{ type: "toolNotCalled", tool: "read" }], [{ type: "equals" }], [{ type: "startsWith" }], [{ type: "endsWith" }], [{ type: "matches" }]]) {
    assert.equal(defaultToolMode(assertions), "live");
  }
});

test("existing tools use explicit mode and never re-default from assertions", () => {
  assert.equal(toolMode(fixture(), "read"), "recorded");
  assert.equal(toolMode(fixture(), "live"), "live");
  assert.equal(toolMode(fixture(), "missing"), "live");
  assert.equal(toolMode({ assertions: [{ type: "contains" }] }, "read"), "live");
});

test("Recorded no-op preserves recording but detaches every nested draft value", () => {
  const raw = fixture(), original = structuredClone(raw), draft = setToolMode(raw, "read", "recorded");
  assert.deepEqual(draft, raw);
  draft.mocks.tools[0].calls[0].result.items.push("changed");
  draft.mocks.tools[0].parameters.properties.path.type = "number";
  draft.mocks.tools[1].calls[0].result.nested[1].keep = false;
  draft.assertions[0].value = "changed";
  assert.deepEqual(raw, original);
});

test("explicit Live removes only its own fixture and remains explicit", () => {
  const raw = fixture(), draft = setToolMode(raw, "read", "live");
  assert.deepEqual(draft.mocks.tools[0], { name: "read", mode: "live" });
  assert.deepEqual(draft.mocks.tools.slice(1), raw.mocks.tools.slice(1));
  assert.equal(raw.mocks.tools[0].calls.length, 1);
  assert.deepEqual(setToolMode({ id: "bare" }, "new", "live"), { id: "bare", mocks: { tools: [{ name: "new", mode: "live" }] } });
});

test("reset and Live-to-Recorded create pending drafts without altering other fixtures", () => {
  for (const [name, reset] of [["read", true], ["live", false], ["missing", false]]) {
    const raw = fixture(), draft = setToolMode(raw, name, "recorded", reset);
    assert.deepEqual(draft.mocks.tools.find(tool => tool.name === name), { name, mode: "recorded", calls: [] });
    assert.deepEqual(draft.mocks.tools.filter(tool => tool.name !== name), raw.mocks.tools.filter(tool => tool.name !== name));
    assert.equal(raw.mocks.tools[0].calls.length, 1);
  }
});

test("tool use summary names actual sources and multiplies only repeated calls", () => {
  assert.equal(toolUseText([
    { name: "raw_<img>", mode: "recorded", source: "recorded", callCount: 2, arguments: { secret: "ARG_CANARY" }, result: "RESULT_CANARY" },
    { name: "other", mode: "live", source: "live", callCount: 1 },
  ]), "Tools: ◉ raw_<img> 2×, ⚡ other");
  assert.equal(toolUseText([]), "");
});

test("summary distinguishes no call, missing source, unavailable and new capture", () => {
  assert.match(toolUseText([{ name: "quiet", mode: "live", source: "live", callCount: 0 }]), /quiet.*not called/i);
  assert.match(toolUseText([{ name: "legacy", mode: "recorded", callCount: 1 }]), /legacy.*source not recorded/i);
  assert.doesNotMatch(toolUseText([{ name: "legacy", mode: "recorded", callCount: 1 }]), /◉|⚡/);
  assert.match(toolUseText([{ name: "gone", mode: "live", source: "unavailable", callCount: 1 }]), /gone.*not available in this test/i);
  assert.match(toolUseText([{ name: "fresh", mode: "recorded", source: "live", callCount: 1, recordingSaved: true }]), /◉ fresh.*recorded just now/i);
});

test("only separately counted disabled requests establish an attempted Disabled fact", () => {
  const settings = [{ name: "read", state: "Disabled" }];
  for (const extra of [{}, { requestCount: 0, settingDisabled: true }, { requestCount: 2, settingDisabled: false }]) {
    assert.doesNotMatch(toolUseText([{ name: "read", mode: "live", source: "live", callCount: 0, ...extra }], settings), /disabled for this test/i);
  }
  assert.equal(toolUseText([{ name: "read", mode: "live", source: "live", callCount: 0, requestCount: 2, settingDisabled: true }], []), "Tools: read, disabled for this test");
  assert.doesNotMatch(toolUseText([{ name: "read", source: "live", callCount: 0 }], [{ name: "read", state: "Enabled" }]), /disabled/i);
});

test("per-tool native mode buttons expose names and report draft changes only", () => {
  const { root, changes, resets } = render({ names: ["read", "live"] });
  assert.equal(root.tag, "section");
  const modes = buttons(root).filter(node => node.className.split(/\s+/).includes("tool-mode"));
  assert.equal(modes.length, 2);
  assert.deepEqual(modes.map(node => node.textContent), ["◉ Recorded", "⚡ Live"]);
  for (const [index, node] of modes.entries()) {
    assert.equal(node.type, "button");
    assert.match(node.attrs["aria-label"], new RegExp(index ? "live" : "read"));
    assert.equal(node.attrs.role, undefined);
    assert.equal(byTag(node, "button").length, 1);
    click(node);
  }
  assert.deepEqual(changes, [["read", "live"], ["live", "recorded"]]);
  assert.deepEqual(resets, []);
  assert.equal(byTag(root, "input").length, 0);
  assert.equal((root.textContent.match(/Recorded keeps output with the test\./g) ?? []).length, 1);
});

test("Record again exists only for populated Recorded tools and reports one reset", () => {
  const { root, changes, resets } = render();
  const again = buttons(root).filter(node => node.textContent === "Record again");
  assert.equal(again.length, 2);
  assert.match(again[0].attrs["aria-label"], /read/);
  click(again[0]);
  assert.deepEqual(resets, ["read"]);
  assert.deepEqual(changes, []);
});

test("disabled state disables mode and reset native buttons", () => {
  const { root, changes, resets } = render({ disabled: true });
  for (const node of buttons(root)) { assert.equal(node.disabled, true); click(node); }
  assert.deepEqual(changes, []);
  assert.deepEqual(resets, []);
});

test("saved details are inert exact supplied text, never serialized fixture objects", () => {
  const text = '{"integer":9007199254740993123,"text":"</pre><script>unsafe()</script>"}\n';
  const { root } = render({ names: ["read", "live", "pending"], details: [
    { name: "read", text }, { name: "live", text: "LIVE_PAYLOAD_CANARY" }, { name: "pending", text: "PENDING_PAYLOAD_CANARY" }, { name: "unknown", text: "UNKNOWN_PAYLOAD_CANARY" },
  ] });
  const details = byTag(root, "details");
  assert.equal(details.length, 1);
  assert.equal(details[0].open === true, false);
  assert.equal(byTag(details[0], "summary").length, 1);
  assert.equal(byTag(details[0], "pre")[0].textContent, text);
  assert.equal(byTag(root, "script").length, 0);
  assert.doesNotMatch(root.textContent, /LIVE_PAYLOAD_CANARY|PENDING_PAYLOAD_CANARY|UNKNOWN_PAYLOAD_CANARY|"items"|"path"/);
});

test("rows expose factual run counts and source but never run arguments or results", () => {
  const { root } = render({ names: ["read", "live"], tools: [
    { name: "read", mode: "recorded", source: "recorded", callCount: 2, arguments: "ARG_CANARY", result: "RESULT_CANARY" },
    { name: "live", mode: "live", callCount: 0 },
  ] });
  assert.match(root.textContent, /2 calls/);
  assert.match(root.textContent, /replay/i);
  assert.match(root.textContent, /not called/i);
  assert.match(root.textContent, /source not recorded/i);
  assert.doesNotMatch(root.textContent, /ARG_CANARY|RESULT_CANARY/);
});

test("untrusted names remain text in labels and names without enabling details", () => {
  const name = '<img src=x onerror="unsafe()">';
  const { root } = render({ names: [name], raw: { id: "a" }, tools: [{ name, mode: "live", source: "live", callCount: 1 }] });
  assert.match(root.textContent, /<img src=x/);
  assert.match(buttons(root)[0].attrs["aria-label"], /<img src=x/);
  assert.equal(byTag(root, "img").length, 0);
  assert.equal(byTag(root, "details").length, 0);
});

test("live disabled state guards mode and reset buttons until refresh releases them", () => {
  let busy = false; const updates = [];
  const { root, changes, resets } = render({ disabled: () => busy, updates });
  const nodes = buttons(root); assert.ok(nodes.length > 0);
  busy = true; for (const update of updates) update();
  for (const node of nodes) { assert.equal(node.disabled, true); for (const listener of node.listeners.click ?? []) listener({}); }
  assert.deepEqual(changes, []); assert.deepEqual(resets, []);
  busy = false; for (const update of updates) update();
  click(nodes[0]); assert.equal(changes.length, 1);
});

test("mode change and reset keep later input and check edits with shown default modes", async () => {
  const { withToolModes } = await import("../../copilot-wsl/com.github.copilot/extensions/agent-view/test-tools.mjs");
  const shown = { id: "new", input: "old", assertions: [], mocks: { tools: [{ name: "read", mode: "recorded", calls: [] }, { name: "clock", mode: "live" }] } };
  const edited = { id: "new", input: "typed later", assertions: [{ id: "a", type: "contains", value: "Ada" }] };
  const changed = setToolMode(withToolModes(edited, shown), "clock", "recorded");
  assert.equal(changed.input, "typed later"); assert.deepEqual(changed.assertions, edited.assertions);
  assert.deepEqual(changed.mocks.tools.map(t => [t.name, t.mode]), [["read", "recorded"], ["clock", "recorded"]]);
  const reset = setToolMode(withToolModes({ ...changed, input: "typed again" }, shown), "read", "recorded", true);
  assert.equal(reset.input, "typed again"); assert.equal(reset.mocks.tools[1].mode, "recorded");
  assert.equal(shown.mocks.tools[1].mode, "live");
});

test("Enter on a Live/Recorded switch is handed to the test's save-and-run keys", () => {
  const keyed = [];
  const { root } = render({ keys: node => keyed.push(node) });
  const modes = buttons(root).filter(b => b.className.includes("tool-mode"));
  assert.equal(keyed.length, 4);
  assert.deepEqual(keyed, modes);
  assert.ok(buttons(root).filter(b => b.className.includes("tool-reset")).every(b => !keyed.includes(b)));
});
