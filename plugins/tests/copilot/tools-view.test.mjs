import assert from "node:assert/strict";
import test from "node:test";
import { serverToolsView, toolsSection } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/tools-view.mjs";

// Inert node construction, attributes, listeners and nesting; markup injection throws.
function fakeDoc() {
  const doc = { activeElement: null };
  const mk = (tag) => {
    const node = {
      tag, children: [], className: "", _text: "", attrs: {}, listeners: {}, style: {}, hidden: false, disabled: false, value: "", checked: false,
      set textContent(v) { this._text = String(v); this.children = []; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
      append(...n) { this.children.push(...n.map((x) => (typeof x === "string" ? Object.assign(mk("#text"), { _text: x }) : x))); },
      replaceChildren(...n) { this.children = []; this._text = ""; this.append(...n); },
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return this.attrs[k] ?? null; },
      addEventListener(type, listener) { (this.listeners[type] ??= []).push(listener); },
      focus() { doc.activeElement = this; },
    };
    for (const name of ["innerHTML", "outerHTML"]) Object.defineProperty(node, name, { set() { throw new Error(name + " used"); } });
    node.insertAdjacentHTML = () => { throw new Error("insertAdjacentHTML used"); };
    return node;
  };
  return Object.assign(doc, { createElement: mk, createElementNS: (ns, tag) => mk(tag), createTextNode: (text) => Object.assign(mk("#text"), { _text: text }) });
}
const all = (node) => [node, ...node.children.flatMap(all)];
const byClass = (root, name) => all(root).filter((e) => e.className.split(/\s+/).includes(name));
const byTag = (root, tag) => all(root).filter((e) => e.tag === tag);
const fire = (node, type, event = {}) => { for (const listener of node.listeners[type] ?? []) listener({ preventDefault() { event.prevented = true; }, stopPropagation() { event.stopped = true; }, ...event }); return event; };
// Mirrors the page's own control factory: every button is registered with its guard.
function harness() {
  const doc = fakeDoc(), controls = [];
  const button = (label, action, className = "", guard = "idle") => {
    const node = doc.createElement("button"); node.className = className; node.textContent = label; node.type = "button";
    node.addEventListener("click", action); controls.push({ node, guard }); return node;
  };
  return { doc, button, controls, guard: (node) => controls.find((c) => c.node === node)?.guard };
}
const tool = (name, over = {}) => ({ name, description: `Does ${name}`, selectable: true, ...over });
const github = { name: "github", label: "GitHub", tools: ["search_issues", "list_tags", "add_comment", "get_file"].map((n) => tool(n)) };
const docs = { name: "docs", label: "Docs", tools: [tool("search_docs"), tool("get_file")] };
const base = (over = {}) => ({ servers: [github, docs], names: [], editable: true, loading: false, error: false, menuOpen: false, onToggleMenu() {}, onOpen() {}, ...over });

test("empty Tools section uses the exact words and one Add action", () => {
  const h = harness(); let toggled = 0;
  const section = toolsSection(h.doc, h.button, base({ onToggleMenu: () => toggled++ }));
  assert.ok(section.className.split(" ").includes("tools-section"));
  assert.equal(byTag(section, "h2")[0].textContent, "Tools");
  assert.match(section.textContent, /No tools yet/);
  assert.match(section.textContent, /A tool lets the agent read or change things through a server this app has\. Tests use the same tools\./);
  const adds = h.controls.filter((c) => c.node.textContent === "+ Add tools");
  assert.equal(adds.length, 1);
  assert.equal(adds[0].guard, "nav");
  assert.equal(adds[0].node.getAttribute("aria-expanded"), "false");
  fire(adds[0].node, "click"); assert.equal(toggled, 1);
  assert.doesNotMatch(section.textContent, /Live|signed in/);
  assert.equal(byClass(section, "menu").length, 0);
});

test("one counts-only source row per uniquely owning server", () => {
  const h = harness(), opened = [];
  const shared = { name: "shared", label: "Shared", tools: [tool("search_issues")] };
  const section = toolsSection(h.doc, h.button, base({ servers: [github, docs, shared], names: ["list_tags", "add_comment", "get_file", "search_docs", "gone"], onOpen: (s) => opened.push(s) }));
  const rows = byClass(section, "tool-source");
  assert.deepEqual(rows.map((r) => r.textContent), ["GitHubMCP server · 2 of 4 tools", "DocsMCP server · 1 of 2 tools"]);
  for (const row of rows) {
    assert.ok(row.className.split(" ").includes("test-row"));
    assert.equal(h.guard(row), "nav");
    assert.doesNotMatch(row.textContent, /list_tags|add_comment|search_docs|Live|signed in/);
  }
  fire(rows[0], "click"); assert.deepEqual(opened, [github]);
  // get_file is on two servers and gone is on none: neither is assigned a guessed server.
  assert.match(section.textContent, /2 tools in the agent file can’t be matched to one of this app’s servers/);
  assert.doesNotMatch(section.textContent, /No tools yet/);
  assert.equal(h.controls.filter((c) => c.node.textContent === "+ Add tools").length, 1);
});

test("Add menu lists visible servers with counts and plain settings help", () => {
  const h = harness(), opened = [];
  const section = toolsSection(h.doc, h.button, base({ names: ["list_tags"], menuOpen: true, onOpen: (s) => opened.push(s) }));
  const menu = byClass(section, "menu")[0];
  assert.equal(menu.getAttribute("role"), "menu");
  const items = byClass(menu, "tool-server-option");
  assert.ok(items.every((i) => i.className.split(" ").includes("mi")));
  assert.deepEqual(items.map((i) => i.textContent), ["GitHub1 of 4 tools", "Docs2 tools"]);
  for (const item of items) { assert.equal(h.guard(item), "nav"); assert.equal(item.getAttribute("role"), "menuitem"); }
  fire(items[0], "click"); fire(items[1], "click");
  assert.deepEqual(opened, [github, docs]);
  assert.match(menu.textContent, /Set up more servers in the app’s MCP settings\./);
  assert.equal(byTag(section, "a").length, 0);
  assert.doesNotMatch(menu.textContent, /kmx |command/i);
  assert.equal(h.controls.find((c) => c.node.textContent === "+ Add tools").node.getAttribute("aria-expanded"), "true");
});

test("catalog failure shows a fixed error and keeps the declared count", () => {
  const h = harness();
  const section = toolsSection(h.doc, h.button, base({ servers: [], names: ["list_tags", "get_file"], error: "EACCES token=secret", menuOpen: true }));
  assert.match(section.textContent, /Couldn’t read this app’s tools\./);
  assert.match(section.textContent, /2 tools in the agent file/);
  assert.doesNotMatch(section.textContent, /secret|EACCES|No servers|can’t be matched|No tools yet/);
  assert.equal(byClass(section, "tool-source").length, 0);
  const loading = toolsSection(h.doc, h.button, base({ servers: [], names: ["list_tags"], loading: true }));
  assert.match(loading.textContent, /Loading this app’s tools…/);
  assert.doesNotMatch(loading.textContent, /can’t be matched/);
});

test("read-only Tools has no Add action and metadata stays literal text", () => {
  const h = harness();
  const evil = { name: "x", label: "<img src=x onerror=alert(1)>", tools: [tool("t")] };
  const section = toolsSection(h.doc, h.button, base({ servers: [evil], names: ["t"], editable: false }));
  assert.equal(h.controls.filter((c) => c.node.textContent === "+ Add tools").length, 0);
  assert.equal(byClass(section, "test-name")[0].textContent, "<img src=x onerror=alert(1)>");
  assert.equal(byTag(section, "img").length, 0);
});

const many = { name: "github", label: "GitHub", tools: Array.from({ length: 12 }, (_, i) => tool(`tool_${i}`, { description: i === 11 ? "Find a needle" : `Does ${i}` })) };
function t2(over = {}) {
  const h = harness(), calls = { filter: [], toggle: [], back: 0, discard: 0, submit: 0 };
  const parts = serverToolsView(h.doc, h.button, {
    agentName: "Release notes drafter", server: many, rows: many.tools, names: ["tool_0", "tool_1"], filter: "",
    onFilter: (v) => calls.filter.push(v), onToggle: (n, c) => calls.toggle.push([n, c]), onBack: () => calls.back++,
    onDiscard: () => calls.discard++, onSubmit: () => calls.submit++, dirty: false, footer: "Saved · Enter runs 6 tests", busy: false, ...over,
  });
  const root = h.doc.createElement("div"); root.append(...parts);
  return { ...h, parts, root, calls, rows: () => byClass(root, "tool-row").filter((r) => !r.hidden) };
}

test("server page names its destination, counts and opening rows", () => {
  const p = t2();
  assert.ok(Array.isArray(p.parts));
  const back = byClass(p.root, "back")[0];
  assert.equal(back.textContent, "Release notes drafter");
  assert.equal(back.getAttribute("aria-label"), "Back to Release notes drafter");
  assert.equal(p.guard(back), "nav");
  fire(back, "click"); assert.equal(p.calls.back, 1);
  assert.equal(byTag(p.root, "h1")[0].textContent, "GitHub");
  assert.match(p.root.textContent, /MCP server · 2 of 12 tools/);
  assert.doesNotMatch(p.root.textContent, /Live|signed in|Enabled|Disabled/);
  const search = byClass(p.root, "tool-filter")[0];
  assert.equal(search.type, "search");
  assert.equal(search.getAttribute("placeholder"), "Filter 12 tools");
  assert.match(p.root.textContent, /Tick a tool to give it to the agent\./);
  assert.deepEqual(p.rows().map((r) => byTag(r, "code")[0].textContent), many.tools.slice(0, 9).map((t) => t.name));
  assert.match(p.rows()[0].textContent, /Does 0/);
  assert.match(p.root.textContent, /3 more · filter to find a tool/);
  const boxes = p.rows().map((r) => byClass(r, "tool-choice")[0]);
  assert.deepEqual(boxes.slice(0, 2).map((b) => [b.type, b.getAttribute("data-tool-name")]), [["checkbox", "tool_0"], ["checkbox", "tool_1"]]);
  assert.deepEqual(boxes.slice(0, 3).map((b) => b.checked), [true, true, false]);
  fire(boxes[2], "change", { target: Object.assign(boxes[2], { checked: true }) });
  assert.deepEqual(p.calls.toggle, [["tool_2", true]]);
});

test("filter matches names and descriptions without reordering", () => {
  const p = t2({ filter: "NEEDLE" });
  assert.deepEqual(p.rows().map((r) => byTag(r, "code")[0].textContent), ["tool_11"]);
  assert.doesNotMatch(p.root.textContent, /more · filter/);
  const search = byClass(p.root, "tool-filter")[0];
  assert.equal(search.value, "NEEDLE");
  search.value = "tool_1"; fire(search, "input");
  assert.deepEqual(p.calls.filter, ["tool_1"]);
  assert.deepEqual(p.rows().map((r) => byTag(r, "code")[0].textContent), ["tool_1", "tool_10", "tool_11"]);
  const enter = fire(search, "keydown", { key: "Enter" });
  assert.equal(p.calls.submit, 1); assert.ok(enter.prevented && enter.stopped);
  fire(search, "keydown", { key: "Enter", isComposing: true }); fire(search, "keydown", { key: "Enter", shiftKey: true });
  assert.equal(p.calls.submit, 1);
});

test("unavailable tools cannot be newly ticked but can be unticked", () => {
  const server = { name: "s", label: "S", tools: [tool("kept", { selectable: false }), tool("new", { selectable: false })] };
  const p = t2({ server, rows: server.tools, names: ["kept"] });
  const [kept, added] = p.rows().map((r) => byClass(r, "tool-choice")[0]);
  assert.equal(kept.disabled, false); assert.equal(kept.checked, true);
  assert.equal(added.disabled, true);
});

test("server page counts only names this server uniquely owns", () => {
  const other = { name: "o", label: "O", tools: [tool("tool_1")] };
  assert.match(t2({ servers: [many, other] }).root.textContent, /MCP server · 1 of 12 tools/);
});

test("an open server page keeps its declared count after catalog objects are refreshed", () => {
  const refreshed = structuredClone(many);
  assert.match(t2({ servers: [refreshed] }).root.textContent, /MCP server · 2 of 12 tools/);
});

test("footer discard only when dirty; busy disables inputs", () => {
  const clean = t2();
  assert.equal(byClass(clean.root, "tools-discard").length, 0);
  assert.match(clean.root.textContent, /Saved · Enter runs 6 tests/);
  const save = byClass(clean.root, "tools-save")[0];
  assert.ok(save.className.split(" ").includes("save-run"));
  assert.equal(clean.guard(save), "save");
  fire(save, "click"); assert.equal(clean.calls.submit, 1);
  const dirty = t2({ dirty: true, footer: "Edited · Enter saves and runs 6 tests", busy: true });
  const discard = byClass(dirty.root, "tools-discard")[0];
  assert.equal(discard.getAttribute("aria-label"), "Discard these edits (Esc)");
  assert.equal(dirty.guard(discard), "idle");
  fire(discard, "click"); assert.equal(dirty.calls.discard, 1);
  assert.ok(byTag(dirty.root, "input").every((i) => i.disabled));
});
