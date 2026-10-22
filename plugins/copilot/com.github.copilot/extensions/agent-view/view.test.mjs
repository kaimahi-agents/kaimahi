import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import http from "node:http";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  CANVAS_ID,
  SERVER_NAME,
  ViewError,
  createViewController,
  formatView,
  normalizePath,
  readView,
  resourceUri,
  startViewBridge,
} from "./view.mjs";
import { renderView } from "./app.js";

const here = (name) => fileURLToPath(new URL(name, import.meta.url));
const MINIMAL = "greeter.agent.md";
const SQUAD = ".squad/agents/greeter/charter.md";
const REV = "sha256:" + "a".repeat(64);

function resource(path, over = {}) {
  return {
    schemaVersion: 1,
    kind: "kmx.agent-view",
    file: { path, revision: REV },
    title: "Greeter",
    instructions: "Reply with exactly `Hello, ` followed by the input name.",
    model: { provider: "fake", model: "fake" },
    examples: [
      {
        id: "hello",
        input: "Ada",
        assertions: [{ id: "exact", type: "equals", value: "Hello, Ada" }],
      },
    ],
    latest: { status: "none" },
    ...over,
  };
}

function result(over = {}) {
  return {
    schemaVersion: 1,
    kind: "kmx.agent-file-preview",
    operation: "test",
    file: { path: "greeter.agent.md", revision: REV },
    modelRoute: { provider: "fake", model: "fake" },
    selection: { scope: "full", caseIds: ["hello"], totalCases: 1 },
    state: "complete",
    verdict: "pass",
    fullPass: true,
    reasonCode: "ok",
    reason: "All checks passed.",
    examples: [
      {
        id: "hello",
        state: "complete",
        reasonCode: "ok",
        reason: "done",
        answerExcerpt: "Hello, Ada",
        verdict: "pass",
        assertions: [{ id: "exact", type: "equals", verdict: "pass", reasonCode: "ok", reason: "matched" }],
      },
    ],
    retention: { status: "none" },
    ...over,
  };
}

function stubSession(payload, { uri } = {}) {
  const calls = [];
  const logs = [];
  const session = {
    calls,
    logs,
    rpc: {
      mcp: {
        resources: {
          read: async (params) => {
            calls.push(params);
            if (payload instanceof Error) throw payload;
            const text = typeof payload === "string" ? payload : JSON.stringify(payload);
            return { contents: [{ uri: uri ?? params.uri, mimeType: "application/json", text }] };
          },
        },
      },
    },
    log: async (message, options) => logs.push({ message, options }),
    send: async () => assert.fail("must not send a model prompt"),
    sendAndWait: async () => assert.fail("must not send a model prompt"),
  };
  return session;
}

function get(url, { headers = {}, method = "GET", host } = {}) {
  const u = new URL(url);
  return new Promise((resolve, reject) => {
    const req = http.request(
      { host: "127.0.0.1", port: u.port, path: u.pathname + u.search, method, headers: host ? { ...headers, host } : headers },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks).toString("utf8") }));
      },
    );
    req.on("error", reject);
    req.end();
  });
}

test("path grammar accepts only portable agent files and Squad charters", () => {
  assert.equal(normalizePath(MINIMAL), MINIMAL);
  assert.equal(normalizePath(SQUAD), SQUAD);
  for (const bad of [
    "", "Greeter.agent.md", "../x.agent.md", "/x.agent.md", "a/b.agent.md", "x.agent.md?q=1", "x.agent.md#f",
    "x\\y.agent.md", "C:x.agent.md", "con.agent.md", "nul.agent.md", "com1.agent.md", "1x.agent.md", "x.agent.md ",
    "x\n.agent.md", "x\0.agent.md", "x%2e.agent.md", "x.md", ".squad/agents/con/charter.md", ".squad/agents/x/Charter.md",
    ".squad/agents/x/../y/charter.md", ".squad/agents/x/y/charter.md", ".squad/agent/x/charter.md", "x".repeat(64) + ".agent.md",
    null, 4, {}, ["x.agent.md"],
  ]) {
    assert.throws(() => normalizePath(bad), (e) => e instanceof ViewError && e.code === "invalid-path", JSON.stringify(bad));
  }
});

test("resource URI encodes the whole path as one component", () => {
  assert.equal(resourceUri(MINIMAL), "kmx://agent/greeter.agent.md");
  assert.equal(resourceUri(SQUAD), "kmx://agent/.squad%2Fagents%2Fgreeter%2Fcharter.md");
  assert.equal(CANVAS_ID, "kmx-agent-view");
});

for (const path of [MINIMAL, SQUAD]) {
  test(`readView reads the current kmx server resource for ${path}`, async () => {
    const session = stubSession(resource(path));
    const view = await readView(session, path);
    assert.deepEqual(session.calls, [{ serverName: "kmx-agent-builder", uri: resourceUri(path) }]);
    assert.equal(SERVER_NAME, "kmx-agent-builder");
    assert.equal(view.file.path, path);
    assert.equal(view.title, "Greeter");
    assert.match(view.model, /provider: fake/);
    assert.equal(view.examples[0].id, "hello");
    assert.deepEqual(view.examples[0].assertions[0].fields, [["value", "Hello, Ada"]]);
    assert.equal(view.latest.status, "none");
    assert.equal(view.latest.badge, "none");
  });
}

test("readView refuses invalid paths before any resource read", async () => {
  const session = stubSession(resource(MINIMAL));
  await assert.rejects(readView(session, "../x.agent.md"), { code: "invalid-path" });
  assert.equal(session.calls.length, 0);
});

test("readView rejects malformed or mismatched resources with static errors", async () => {
  const secret = "SECRET-token-path /home/user";
  const cases = [
    ["wrong kind", resource(MINIMAL, { kind: "other" })],
    ["wrong schema", resource(MINIMAL, { schemaVersion: 2 })],
    ["wrong path", resource(MINIMAL, { file: { path: "other.agent.md", revision: REV } })],
    ["missing revision", resource(MINIMAL, { file: { path: MINIMAL } })],
    ["no title", resource(MINIMAL, { title: 3 })],
    ["bad examples", resource(MINIMAL, { examples: "x" })],
    ["bad latest status", resource(MINIMAL, { latest: { status: "green" } })],
    ["none with result", resource(MINIMAL, { latest: { status: "none", result: result() } })],
    ["current without result", resource(MINIMAL, { latest: { status: "current" } })],
    ["result for other revision", resource(MINIMAL, { latest: { status: "current", result: result({ file: { path: "greeter.agent.md", revision: "sha256:" + "b".repeat(64) } }) } })],
    ["result without state", resource(MINIMAL, { latest: { status: "current", result: { ...result(), state: undefined } } })],
    ["not JSON", "not json " + secret],
    ["array", "[]"],
  ];
  for (const [name, payload] of cases) {
    await assert.rejects(readView(stubSession(payload), MINIMAL), (e) => {
      assert.ok(e instanceof ViewError, name);
      assert.equal(e.code, "invalid-resource", name);
      assert.ok(!e.message.includes("SECRET"), name);
      return true;
    }, name);
  }
});

test("readView rejects non-text, multiple, oversized and malformed-UTF-16 contents", async () => {
  const mk = (contents) => ({ rpc: { mcp: { resources: { read: async () => ({ contents }) } } } });
  const ok = JSON.stringify(resource(MINIMAL));
  await assert.rejects(readView(mk([]), MINIMAL), { code: "invalid-resource" });
  await assert.rejects(readView(mk([{ uri: "x", blob: "AAAA" }]), MINIMAL), { code: "invalid-resource" });
  await assert.rejects(readView(mk([{ uri: "x", text: ok }, { uri: "y", text: ok }]), MINIMAL), { code: "invalid-resource" });
  await assert.rejects(readView(mk([{ uri: "x", text: "x".repeat(2 * 1024 * 1024 + 1) }]), MINIMAL), { code: "too-large" });
  await assert.rejects(readView(mk([{ uri: "x", text: ok.replace("Greeter", "Gre\ud800eter") }]), MINIMAL), { code: "invalid-resource" });
  await assert.rejects(readView({ rpc: {} }, MINIMAL), { code: "unavailable" });
});

test("host read failures surface only a static message", async () => {
  const raw = new Error("ECONNRESET at C:\\Users\\someone\\secret");
  await assert.rejects(readView(stubSession(raw), MINIMAL), (e) => {
    assert.equal(e.code, "unavailable");
    assert.ok(!/ECONNRESET|someone|secret/.test(e.message));
    return true;
  });
});

test("latest result is green only when current and fully passing", async () => {
  const badge = async (latest) => (await readView(stubSession(resource(MINIMAL, { latest })), MINIMAL)).latest.badge;
  assert.equal(await badge({ status: "current", result: result() }), "pass");
  assert.equal(await badge({ status: "current", result: result({ fullPass: false, verdict: "fail" }) }), "not-passing");
  assert.equal(await badge({ status: "current", result: result({ fullPass: true, verdict: "fail" }) }), "not-passing");
  assert.equal(await badge({ status: "stale" }), "stale");
  assert.equal(await badge({ status: "current", result: result({ state: "cancelled" }) }), "not-passing");
  assert.equal(await badge({ status: "current", result: result({ selection: {scope: "partial", totalCases: 1, caseIds: ["hello"]} }) }), "not-passing");
});

test("hostile text is stripped of control and bidi characters and bounded", async () => {
  const evil = "a\u0007b\u202ec\u001b[31m\r\nd" + "z".repeat(100000);
  const view = await readView(stubSession(resource(MINIMAL, { instructions: evil, title: "T\u0000\u202e" })), MINIMAL);
  assert.ok(!/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/.test(view.instructions + view.title));
  assert.ok(view.instructions.length < 70000);
  assert.match(view.instructions, /\[truncated\]$/);
});

test("formatView is compact, plain and visibly truncated", async () => {
  const withResult = resource(MINIMAL, { latest: { status: "current", result: result() } });
  const text = formatView(await readView(stubSession(withResult), MINIMAL));
  assert.match(text, /greeter\.agent\.md/);
  assert.match(text, /Greeter/);
  assert.match(text, /hello/);
  assert.match(text, /Latest: current, PASS/);
  assert.match(text, /untrusted/i);
  assert.ok(text.length < 6200);
  const none = formatView(await readView(stubSession(resource(MINIMAL)), MINIMAL));
  assert.match(none, /Latest: none/);
  const stale = formatView(await readView(stubSession(resource(MINIMAL, { latest: { status: "stale" } })), MINIMAL));
  assert.match(stale, /Latest: STALE/);
  assert.doesNotMatch(stale, /PASS/);

  const many = Array.from({ length: 100 }, (_, i) => ({ id: `e${i}`, input: "x".repeat(500), assertions: [] }));
  const big = formatView(await readView(stubSession(resource(MINIMAL, { examples: many, instructions: "y".repeat(50000) })), MINIMAL));
  assert.ok(big.length <= 6200);
  assert.match(big, /\[output truncated\]$/);
  assert.ok(!/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u202a-\u202e]/.test(big));
});

test("text truncation never hides the latest-result summary", async () => {
  const examples = Array.from({ length: 100 }, (_, i) => ({
    id: `e${i}`, input: "Ada", assertions: [{id: "greeting", type: "contains", value: "Hello"}],
  }));
  for (const [latest, summary] of [
    [{status: "none"}, "Latest: none (no current Run/Test result)"],
    [{status: "stale"}, "Latest: STALE (from an earlier revision; not current)"],
    [{status: "current", result: result()}, "Latest: current, PASS (complete, verdict pass)"],
    [{status: "current", result: result({fullPass: false, verdict: "fail"})}, "Latest: current, NOT PASSING (complete, verdict fail)"],
  ]) {
    const view = await readView(stubSession(resource(MINIMAL, {examples, latest})), MINIMAL);
    const text = formatView(view);
    assert.ok(text.length <= 6000);
    assert.ok(text.includes("[output truncated]"));
    assert.ok(text.includes(summary), `missing ${summary}`);
  }
});

test("status badges and errors meet AA text contrast in both themes", async () => {
  const css = await readFile(new URL("./style.css", import.meta.url), "utf8");
  const declarations = (block) => Object.fromEntries([...block.matchAll(/([\w-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
  const roots = [...css.matchAll(/:root\s*\{([^}]+)\}/g)].map((m) => declarations(m[1]));
  const resolve = (color, variables, seen = new Set()) => {
    const alias = color.match(/^var\((--[\w-]+)\)$/);
    if (!alias) return color;
    assert.ok(!seen.has(alias[1]), "cyclic color alias");
    assert.ok(Object.hasOwn(variables, alias[1]), "missing color alias " + alias[1]);
    return resolve(variables[alias[1]], variables, new Set([...seen, alias[1]]));
  };
  const luminance = (color) => {
    const oklch = color.match(/^oklch\(([\d.]+)\s+([\d.]+)\s+([\d.]+)\)$/);
    if (oklch) {
      const [, L, C, h] = oklch.map(Number), angle = h * Math.PI / 180;
      const a = C * Math.cos(angle), b = C * Math.sin(angle);
      const l = (L + .3963377774 * a + .2158037573 * b) ** 3;
      const m = (L - .1055613458 * a - .0638541728 * b) ** 3;
      const s = (L - .0894841775 * a - 1.291485548 * b) ** 3;
      const [r, g, blue] = [
        4.0767416621 * l - 3.3077115913 * m + .2309699292 * s,
        -1.2684380046 * l + 2.6097574011 * m - .3413193965 * s,
        -.0041960863 * l - .7034186147 * m + 1.707614701 * s,
      ].map((v) => Math.max(0, Math.min(1, v)));
      return .2126 * r + .7152 * g + .0722 * blue;
    }
    let digits = color.replace(/^#/, "");
    if (digits.length === 3) digits = [...digits].map((v) => v + v).join("");
    assert.match(digits, /^[0-9a-f]{6}$/i);
    const channels = digits.match(/../g).map((v) => parseInt(v, 16) / 255).map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
  };
  assert.equal(roots.length, 2, "both palette roots remain covered");
  assert.equal(resolve("var(--alias)", { "--alias": "var(--base)", "--base": "#123456" }), "#123456");
  assert.ok(Math.abs(luminance("oklch(1 0 0)") - 1) < .000001);
  assert.equal(luminance("oklch(0 0 0)"), 0);
  for (const [theme, variables] of [["base", roots[0]], ["override", {...roots[0], ...roots[1]}]]) {
    for (const [foreground, background] of [
      ["--success", "--bg"], ["--error", "--bg"],
      ["--success", "--green-bg"], ["--error", "--red-bg"],
      ["--green", "--green-bg"], ["--red", "--red-bg"],
    ]) {
      const a = luminance(resolve(`var(${foreground})`, variables));
      const b = luminance(resolve(`var(${background})`, variables));
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      assert.ok(ratio >= 4.5, `${theme} ${foreground}/${background} contrast ${ratio.toFixed(2)}:1 is below 4.5:1`);
    }
    for (const [selector, tint] of [[".badge.pass", "--green-bg"], [".badge.fail", "--red-bg"], [".error", "--bg"]]) {
      assert.ok(css.includes(selector), "missing status rule " + selector);
      const block = css.slice(css.indexOf(selector)).match(/\{([^}]+)\}/)[1];
      const a = luminance(resolve(declarations(block).color, variables));
      for (const background of ["--bg", tint]) {
        const b = luminance(resolve(`var(${background})`, variables));
        const ratio = (Math.max(a, b) + .05) / (Math.min(a, b) + .05);
        assert.ok(ratio >= 4.5, `${theme} ${selector}/${background} contrast ${ratio.toFixed(2)}:1 is below 4.5:1`);
      }
    }
  }
});

test("bridge serves only token-protected same-host requests", async () => {
  const session = stubSession(resource(SQUAD));
  const bridge = await startViewBridge({ session, path: SQUAD });
  try {
    const u = new URL(bridge.url);
    assert.equal(u.hostname, "127.0.0.1");
    assert.ok(u.pathname.endsWith("/") && u.pathname.length > 20);
    const base = bridge.url;

    const page = await get(base);
    assert.equal(page.status, 200);
    assert.match(page.headers["content-type"], /text\/html/);
    assert.match(page.headers["content-security-policy"], /default-src 'none'/);
    assert.match(page.headers["content-security-policy"], /script-src 'self'/);
    assert.equal(page.headers["x-content-type-options"], "nosniff");
    assert.equal(page.headers["cache-control"], "no-store");
    assert.equal(page.headers["access-control-allow-origin"], undefined);
    assert.equal((await get(base + "app.js")).headers["content-type"].split(";")[0], "text/javascript");
    assert.equal((await get(base + "style.css")).headers["content-type"].split(";")[0], "text/css");

    const data = await get(base + "view.json");
    assert.equal(data.status, 200);
    assert.equal(JSON.parse(data.body).file.path, SQUAD);
    assert.deepEqual(session.calls, [{ serverName: "kmx-agent-builder", uri: resourceUri(SQUAD) }]);

    // token, host, origin, method and path refusals; none reach the resource
    const before = session.calls.length;
    const root = `http://127.0.0.1:${u.port}/`;
    assert.equal((await get(root + "view.json")).status, 404);
    assert.equal((await get(root + "wrong-token/view.json")).status, 404);
    assert.equal((await get(root + new URL(base).pathname.slice(1).slice(0, -1) + "x/view.json")).status, 404);
    assert.equal((await get(base + "view.json", { host: "evil.example" })).status, 403);
    assert.equal((await get(base + "view.json", { host: `localhost.evil.example:${u.port}` })).status, 403);
    assert.equal((await get(base + "view.json", { headers: { origin: "https://evil.example" } })).status, 403);
    assert.equal((await get(base + "view.json", { headers: { origin: "null" } })).status, 403);
    assert.equal((await get(base + "view.json", { method: "POST" })).status, 405);
    assert.equal((await get(base + "view.json", { method: "DELETE" })).status, 405);
    assert.equal((await get(base + "view.json", { method: "OPTIONS" })).status, 405);
    assert.equal((await get(base + "../view.json")).status, 404);
    assert.equal((await get(base + "secret.txt")).status, 404);
    assert.equal((await get(base + "view.json?path=other.agent.md")).status, 200);
    assert.equal(session.calls.length, before + 1);
    assert.equal(session.calls.at(-1).uri, resourceUri(SQUAD));

    // same-origin Origin is allowed
    assert.equal((await get(base + "view.json", { headers: { origin: `http://127.0.0.1:${u.port}` } })).status, 200);
  } finally {
    await bridge.close();
  }
});

test("bridge reports malformed resources as a static failure", async () => {
  const bridge = await startViewBridge({ session: stubSession("<script>alert(1)</script> /home/x"), path: MINIMAL });
  try {
    const res = await get(bridge.url + "view.json");
    assert.equal(res.status, 502);
    const body = JSON.parse(res.body);
    assert.equal(body.error, "invalid-resource");
    assert.ok(!/script|home/.test(res.body));
  } finally {
    await bridge.close();
  }
});

test("bridge close is idempotent and stops listening", async () => {
  const bridge = await startViewBridge({ session: stubSession(resource(MINIMAL)), path: MINIMAL });
  await bridge.close();
  await bridge.close();
  await assert.rejects(get(bridge.url));
});

test("controller opens, reopens idempotently, replaces and closes instances", async () => {
  const session = stubSession(resource(MINIMAL));
  const ctl = createViewController({ getSession: () => session });
  const ctx = { instanceId: "i1", canvasId: CANVAS_ID, input: { path: MINIMAL } };
  const [a, b] = await Promise.all([ctl.open(ctx), ctl.open(ctx)]);
  assert.equal(a.url, b.url);
  assert.equal(ctl.size, 1);
  assert.equal((await get(a.url + "view.json")).status, 200);
  assert.equal((await ctl.open(ctx)).url, a.url);

  const moved = await ctl.open({ ...ctx, input: { path: SQUAD } });
  assert.notEqual(moved.url, a.url);
  assert.equal(ctl.size, 1);
  await assert.rejects(get(a.url));

  const other = await ctl.open({ instanceId: "i2", canvasId: CANVAS_ID, input: { path: MINIMAL } });
  assert.equal(ctl.size, 2);
  await ctl.close({ instanceId: "i1" });
  await ctl.close({ instanceId: "i1" });
  assert.equal(ctl.size, 1);
  await assert.rejects(get(moved.url));
  await ctl.closeAll();
  assert.equal(ctl.size, 0);
  await assert.rejects(get(other.url));
});

test("superseded concurrent opens cannot return an obsolete view", async () => {
  const controller = createViewController({ getSession: () => stubSession(resource(MINIMAL)) });
  try {
    const old = controller.open({instanceId: "shared", input: {path: MINIMAL}});
    const current = controller.open({instanceId: "shared", input: {path: SQUAD}});
    const outcomes = await Promise.allSettled([old, current]);
    assert.equal(outcomes[0].status, "rejected");
    assert.equal(outcomes[1].status, "fulfilled");
    assert.equal(outcomes[1].value.title, "Agent view");
  } finally { await controller.closeAll(); }
});

test("controller refuses invalid canvas input and opens no listener", async () => {
  const ctl = createViewController({ getSession: () => stubSession(resource(MINIMAL)) });
  for (const input of [undefined, null, "x", [], {}, { path: "../x.agent.md" }, { path: MINIMAL, extra: 1 }, { path: MINIMAL, url: "http://x" }]) {
    await assert.rejects(ctl.open({ instanceId: "bad", canvasId: CANVAS_ID, input }), (e) => e instanceof ViewError);
  }
  assert.equal(ctl.size, 0);
});

test("agent-view command logs the same view and never sends a prompt", async () => {
  const session = stubSession(resource(MINIMAL));
  const ctl = createViewController({ getSession: () => session });
  await ctl.command({ args: `  ${MINIMAL}  ` });
  assert.equal(session.logs.length, 1);
  assert.match(session.logs[0].message, /Greeter/);
  assert.deepEqual(session.calls, [{ serverName: "kmx-agent-builder", uri: resourceUri(MINIMAL) }]);

  for (const args of ["", "a b", "../x.agent.md", `${MINIMAL} extra`]) {
    await ctl.command({ args });
  }
  assert.equal(session.calls.length, 1);
  assert.deepEqual(session.logs.slice(1).map((l) => l.options?.level), ["error", "error", "error", "error"]);

  const failing = stubSession(new Error("boom /secret"));
  await createViewController({ getSession: () => failing }).command({ args: MINIMAL });
  assert.equal(failing.logs[0].options.level, "error");
  assert.ok(!failing.logs[0].message.includes("secret"));
});

// --- DOM / source guards ---------------------------------------------------

function fakeDoc(defaultView) {
  const mk = (tag) => {
    const el = {
      tag, children: [], className: "", _text: "", attrs: {}, listeners: {},
      set textContent(v) { this._text = String(v); this.children = []; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
      append(...n) { this.children.push(...n.map((x) => (typeof x === "string" ? Object.assign(mk("#text"), { _text: x }) : x))); },
      replaceChildren(...n) { this.children = []; this._text = ""; this.append(...n); },
      setAttribute(k, v) { this.attrs[k] = v; },
      addEventListener(type, listener) {
        this.listeners[type] ??= [];
        this.listeners[type].push(listener);
      },
    };
    for (const name of ["innerHTML", "outerHTML"]) Object.defineProperty(el, name, { set() { throw new Error(name + " used"); } });
    el.insertAdjacentHTML = () => { throw new Error("insertAdjacentHTML used"); };
    return el;
  };
  return { createElement: mk, defaultView };
}
const all = (el) => [el, ...el.children.flatMap(all)];
const byClass = (root, name) => all(root).filter((e) => e.className.split(/\s+/).includes(name));
async function rendered(over = {}, path = MINIMAL, defaultView) {
  const view = await readView(stubSession(resource(path, { model: "fake", ...over })), path);
  const doc = fakeDoc(defaultView);
  const root = doc.createElement("main");
  renderView(doc, root, view);
  return root;
}

function assertTrustedControls(root) {
  const nodes = all(root), buttons = nodes.filter((e) => e.tag === "button");
  assert.equal(buttons.length, 1);
  const button = buttons[0];
  assert.equal(button.textContent, "Copy path");
  assert.deepEqual(button.attrs, { type: "button", "aria-label": "Copy agent path", "data-action": "copy-path" });
  assert.equal(byClass(byClass(root, "source-details")[0], "copy-path")[0], button);
  assert.deepEqual(Object.keys(button.listeners), ["click"]);
  assert.equal(button.listeners.click.length, 1);
  assert.equal(nodes.filter((e) => ["a", "img", "script", "input", "form", "textarea", "iframe", "object", "embed"].includes(e.tag)).length, 0);
  assert.ok(nodes.every((e) => !Object.keys(e.attrs).some((k) => /^(?:on|href$|src$|style$)/i.test(k))));
  assert.ok(nodes.filter((e) => e !== button).every((e) => Object.keys(e.listeners).length === 0));
}

test("UI gives identity and purpose priority while keeping source and full revision in details", async () => {
  const title = "Greeter — Greeting specialist";
  const root = await rendered({ title, instructions: "# " + title + "\n## Identity\n- **ID:** greeter\n- **Purpose:** Return a greeting.\n## Model\n- **Preferred:** fake" }, SQUAD);
  const header = root.children[0];
  assert.equal(header.tag, "header");
  assert.equal(header.className, "view-header author-header");
  assert.equal(byClass(header, "identity")[0].children[0].tag, "span");
  assert.equal(byClass(header, "avatar")[0].textContent, "G");
  assert.equal(byClass(header, "avatar")[0].attrs["aria-hidden"], "true");
  const top = byClass(header, "identity-top")[0];
  assert.equal(top.children[0].tag, "h1");
  assert.equal(top.children[0].textContent, "Greeter", "presentation may split the conventional role suffix");
  assert.equal(byClass(top, "model").length, 0);
  assert.equal(byClass(top, "status-inline").length, 1);
  assert.equal(byClass(header, "purpose")[0].textContent, "Return a greeting.");
  const source = byClass(header, "source")[0];
  assert.equal(byClass(source, "file-path")[0].textContent, "Squad member · Greeter");
  assert.equal(byClass(source, "file-path")[0].attrs.title, undefined);
  assert.equal(byClass(source, "model")[0].textContent, "Model fake");
  assert.deepEqual(source.children.map((e) => e.className), ["file-label file-path", "model", "source-details"]);
  const details = byClass(source, "source-details")[0];
  assert.equal(details.tag, "details");
  assert.equal(details.children[0].textContent, "Source details");
  assert.ok(byClass(details, "source-info")[0].textContent.includes(REV));
  assert.equal(byClass(details, "source-title")[0].textContent, title, "full authored title remains available");
  assert.equal(all(root).filter((e) => e.tag === "code" && e.textContent === "fake").length, 1);
  assert.ok(!all(header).filter((e) => e.tag === "p" && !all(details).includes(e)).some((e) => e.textContent.includes(REV)));
});

test("UI keeps normal status pills in the header and quiet guidance after instructions", async () => {
  for (const [latest, badge, label] of [
    [{ status: "none" }, "none", "No current result"],
    [{ status: "stale" }, "stale", "Result out of date"],
    [{ status: "current", result: result() }, "pass", "Passing 1/1"],
    [{ status: "current", result: result({ state: "cancelled", fullPass: false }) }, "warning", "Incomplete result"],
  ]) {
    const root = await rendered({ latest });
    const header = root.children[0], observation = byClass(root, "latest")[0];
    assert.equal(byClass(root, "latest").length, 1);
    assert.ok(all(header).includes(observation));
    assert.equal(observation.tag, "section");
    assert.equal(byClass(observation, "status-inline").length, 1);
    assert.equal(byClass(observation, "is-" + badge).length, 1);
    assert.equal(byClass(root, "hero").length, 0);
    const headline = byClass(observation, "badge")[0];
    assert.equal(headline.tag, "span");
    assert.equal(headline.textContent, label);
    assert.equal(byClass(observation, badge).length, 1);
    assert.equal(byClass(observation, "status-pill").length, 1);
    assert.equal(byClass(observation, "good").length, badge === "pass" ? 1 : 0);
    assert.equal(byClass(observation, "wait").length, badge === "stale" || badge === "warning" ? 1 : 0);
    assert.equal(byClass(header, "bad").length, 0);
    assert.equal(byClass(observation, "hint").length, 0);
    assert.equal(byClass(header, "hint").length, 0);
    const hint = byClass(root, "hint")[0];
    assert.equal(hint.tag, "p");
    assert.equal(byClass(hint, "quiet").length, 1);
    assert.equal(root.children[2], hint);
    assert.doesNotMatch(hint.textContent, /^Next|never run/i);
    assert.equal(root.children.length, 4);
    assert.equal(byClass(root, "instructions-section")[0], root.children[1]);
    assert.equal(root.children[1].children[0].textContent, "InstructionsRead-only");
    assert.equal(byClass(root.children[1], "section-line").length, 1);
    for (const name of ["editing-surface", "instruction", "authoring"]) assert.equal(byClass(root.children[1], name).length, 1);
    assert.equal(byClass(root.children[1], "instruction-copy").length, 1);
    assert.equal(byClass(root, "examples-section")[0], root.children[3]);
  }
});

test("UI reserves the single full status band for confirmed failed cases after instructions", async () => {
  const fixture = savedFixture();
  const r = fixture.latest.result;
  r.fullPass = false; r.verdict = "fail"; r.examples[0].verdict = "fail";
  r.examples[0].assertions[0].verdict = "fail";
  const root = await rendered(fixture), header = root.children[0], band = root.children[2];
  assert.equal(byClass(root, "latest").length, 1);
  assert.equal(byClass(header, "latest").length, 0);
  assert.equal(byClass(root, "status-inline").length, 0);
  assert.equal(byClass(root, "hero")[0], band);
  assert.equal(byClass(band, "is-fail").length, 1);
  assert.equal(byClass(band, "badge")[0].tag, "strong");
  assert.equal(byClass(band, "status").length, 1);
  assert.equal(byClass(band, "bad").length, 1);
  assert.equal(byClass(band, "badge")[0].textContent, "1 failing");
  assert.equal(byClass(band, "hero-symbol")[0].attrs["aria-hidden"], "true");
  assert.equal(root.children.length, 4);
  assert.equal(byClass(header, "hint").length, 0);
  assert.equal(byClass(root, "hint").length, 1, "only the failure band owns its next-step hint");
  assert.equal(byClass(root, "quiet").length, 0);
  assert.equal(byClass(root, "instructions-section")[0], root.children[1]);
  assert.equal(byClass(root, "examples-section")[0], root.children[3]);

  r.examples[0].state = "refused";
  const refused = await rendered(fixture);
  assert.equal(byClass(refused, "hero").length, 0);
  assert.equal(byClass(refused.children[0], "status-inline").length, 1);
});

test("UI presents canonical charter sections as readable headings, bullets and inline text", async () => {
  const root = await rendered({ instructions: [
    "# Greeter", "", "## Identity", "- **ID:** greeter", "- **Purpose:** Return a greeting.", "",
    "## Accountable Responsibilities", "Reply with exactly `Hello, ` followed by the input name.", "",
    "### Detail", "Stay **brief**.", "#### Boundary", "No tools.", "",
    "## Model", "- **Preferred:** fake", "",
  ].join("\n") }, SQUAD);
  const instructions = byClass(root, "instructions")[0];
  assert.ok(instructions, "readable instructions container");
  assert.deepEqual(byClass(instructions, "instruction-heading").map((e) => [e.tag, e.textContent]), [
    ["h3", "Accountable Responsibilities"], ["h4", "Detail"], ["h4", "Boundary"],
  ]);
  assert.deepEqual(all(instructions).filter((e) => e.tag === "li").map((e) => e.textContent), []);
  assert.deepEqual(all(instructions).filter((e) => e.tag === "code").map((e) => e.textContent), ["Hello, "]);
  assert.deepEqual(all(instructions).filter((e) => e.tag === "strong").map((e) => e.textContent), ["brief"]);
  assert.equal(all(instructions).filter((e) => e.tag === "pre").length, 0);
});

test("UI presents a minimal titled agent without a serialized charter box", async () => {
  const root = await rendered({ instructions: "# Greeter\n\nReturn `Hello, ` then the name.\nKeep its case.\n\n## Model\n- **Preferred:** fake" });
  const instructions = byClass(root, "instructions")[0];
  assert.ok(instructions);
  assert.deepEqual(instructions.children.map((e) => [e.tag, e.textContent]), [["p", "Return Hello,  then the name.\nKeep its case."]]);
});

for (const [name, instructions, headings, literal] of [
  ["leading blanks", "\n\n# Greeter\n\nText.", [], "Text."],
  ["different title", "# Another title\nText.", ["Another title"], "Text."],
  ["later matching title", "First prose.\n# Greeter\nText.", ["Greeter"], "First prose."],
  ["matching H2", "## Greeter\nText.", ["Greeter"], "Text."],
  ["indented literal title", "    # Greeter\nText.", [], "    # Greeter"],
]) {
  test(`UI only suppresses the first nonblank matching H1: ${name}`, async () => {
    const root = await rendered({ instructions });
    const box = byClass(root, "instructions")[0];
    assert.ok(box);
    assert.deepEqual(byClass(box, "instruction-heading").map((e) => e.textContent), headings);
    assert.ok(box.textContent.includes(literal));
  });
}

test("UI suppresses only a complete canonical Model body and retains the following section", async () => {
  const root = await rendered({ instructions: "# Greeter\n\n## Model\n\n- **Preferred:** fake\n \n## Notes\nStill here." });
  const box = byClass(root, "instructions")[0];
  assert.ok(box);
  assert.deepEqual(byClass(box, "instruction-heading").map((e) => e.textContent), ["Notes"]);
  assert.equal(box.textContent, "NotesStill here.");
});

for (const [name, model, instructions, headings, visible] of [
  ["different model", "fake", "## Model\n- **Preferred:** other", ["Model"], "other"],
  ["extra metadata", "fake", "## Model\n- **Preferred:** fake\n- **Cost:** low", ["Model"], "Cost: low"],
  ["duplicate sections", "fake", "## Model\n- **Preferred:** fake\n## Model\n- **Preferred:** fake", ["Model", "Model"], "Preferred: fake"],
  ["truncated body", "fake", "## Model\n- **Preferred:** fa… [truncated]", ["Model"], "fa… [truncated]"],
  ["truncated model", "fake… [truncated]", "## Model\n- **Preferred:** fake… [truncated]", ["Model"], "fake… [truncated]"],
  ["nested body", "fake", "## Model\n- **Preferred:** fake\n### Notes\nKeep this.", ["Model", "Notes"], "Keep this."],
  ["indented heading", "fake", " ## Model\n- **Preferred:** fake", ["Model"], "Preferred: fake"],
]) {
  test(`UI retains Model content when it is not redundant: ${name}`, async () => {
    const root = await rendered({ model, instructions });
    const box = byClass(root, "instructions")[0];
    assert.ok(box);
    assert.deepEqual(byClass(box, "instruction-heading").map((e) => e.textContent), headings);
    assert.ok(box.textContent.includes(visible));
  });
}

for (const [name, open, close, inside] of [
  ["backticks", "```text", "```", "# Greeter\n## Model\n- **Preferred:** fake"],
  ["tilde with backtick info", "~~~lang`info", "~~~", "## Model\n- **Preferred:** fake"],
  ["three-space indent and longer close", "   ````text", "   `````", "## Model\n- **Preferred:** fake\n```\nnot closed yet"],
  ["wrong marker and close info", "~~~~", "~~~~~", "## Model\n- **Preferred:** fake\n````\n~~~~ info\nnot closed yet"],
]) {
  test(`UI keeps fenced headings literal and ignores them for metadata suppression: ${name}`, async () => {
    const root = await rendered({ instructions: `${open}\n${inside}\n${close}\n## Model\n- **Preferred:** fake\n## Notes\nAfter the fence.` });
    const box = byClass(root, "instructions")[0];
    assert.ok(box);
    assert.deepEqual(byClass(box, "code-block").map((e) => [e.tag, e.textContent]), [["pre", inside]]);
    assert.deepEqual(byClass(box, "instruction-heading").map((e) => [e.tag, e.textContent]), [["h3", "Notes"]]);
    assert.ok(box.textContent.endsWith("After the fence."));
  });
}

for (const tail of ["## Model\n- **Preferred:** fake\n## Still code", "```\n## Model\n- **Preferred:** fa… [truncated]"]) {
  test(`UI keeps an unfinished fence literal through its tail: ${tail.slice(-20)}`, async () => {
    const root = await rendered({ instructions: `# Greeter\n\n~~~~text\n${tail}` });
    const box = byClass(root, "instructions")[0];
    assert.ok(box);
    assert.deepEqual(byClass(box, "code-block").map((e) => e.textContent), [`~~~~text\n${tail}`]);
    assert.equal(byClass(box, "instruction-heading").length, 0);
  });
}

test("UI does not open a backtick fence whose info contains a backtick", async () => {
  const root = await rendered({ instructions: "```bad`info\n## Model\n- **Preferred:** fake\n## Notes\nVisible heading." });
  const box = byClass(root, "instructions")[0];
  assert.ok(box);
  assert.equal(byClass(box, "code-block").length, 0);
  assert.deepEqual(byClass(box, "instruction-heading").map((e) => e.textContent), ["Notes"]);
  assert.ok(box.textContent.startsWith("```bad`info"));
});

test("UI preserves four-space-indented heading, bullet and fence lines as literal text", async () => {
  const literal = "    ## Model\n    - **Preferred:** fake\n    ```\n    # Greeter\n    `literal`";
  const root = await rendered({ instructions: literal });
  const box = byClass(root, "instructions")[0];
  assert.ok(box);
  assert.equal(box.textContent, literal);
  assert.equal(all(box).filter((e) => ["h3", "h4", "ul", "code", "strong", "pre"].includes(e.tag)).length, 0);
});

for (const literal of ["**unfinished", "`unfinished", "**across\nlines**", "`across\nlines`", "**balanced** and **unfinished", "``unsupported``"]) {
  test(`UI leaves unbalanced, multiline or unsupported inline markers literal: ${JSON.stringify(literal)}`, async () => {
    const root = await rendered({ instructions: literal });
    const box = byClass(root, "instructions")[0];
    assert.ok(box);
    assert.equal(box.textContent, literal);
    assert.equal(all(box).filter((e) => e.tag === "code" || e.tag === "strong").length, 0);
  });
}

test("UI keeps hostile HTML and JavaScript links inert inside instructions themselves", async () => {
  const hostile = '<img src=x onerror=alert(1)>\n<script>alert(2)</script>\n[link](javascript:alert(3))\n![image](https://example.invalid/x)\n<button>Run</button><input value=x>';
  const root = await rendered({ instructions: hostile });
  const box = byClass(root, "instructions")[0];
  assert.ok(box);
  assert.equal(box.textContent, hostile);
  assert.equal(all(box).filter((e) => ["a", "img", "script", "button", "input"].includes(e.tag)).length, 0);
});

test("UI preserves unsupported syntax, paragraph line breaks and authored ordered-list numbers in order", async () => {
  const root = await rendered({ instructions: [
    "## Notes", "First line.", "Second line.", "", "3. Third step.", "7. Seventh step.", "",
    "[link](https://example.invalid)", "<em>HTML stays literal</em>", "| A | B |", "| --- | --- |", "| a | b |", "",
    "Setext text", "---", "", "After unsupported content.",
  ].join("\n") });
  const box = byClass(root, "instructions")[0];
  assert.ok(box);
  assert.deepEqual(box.children.map((e) => [e.tag, e.textContent]), [
    ["h3", "Notes"], ["p", "First line.\nSecond line."], ["p", "3. Third step.\n7. Seventh step."],
    ["p", "[link](https://example.invalid)\n<em>HTML stays literal</em>\n| A | B |\n| --- | --- |\n| a | b |"],
    ["p", "Setext text\n---"], ["p", "After unsupported content."],
  ]);
  assert.equal(all(box).filter((e) => ["a", "em", "table", "ol"].includes(e.tag)).length, 0);
});

test("UI never formats saved inputs, expected operands or model answers", async () => {
  const exact = "  # Input\n**literal** `code`\n[link](javascript:alert(1))\n<img src=x>  ";
  const root = await rendered({
    examples: [{ id: "exact", input: exact, assertions: [{ id: "check", type: "contains", value: exact }] }],
    latest: { status: "current", result: result({ selection: { scope: "full", caseIds: ["exact"], totalCases: 1 }, examples: [{ ...result().examples[0], id: "exact", answerExcerpt: exact, assertions: [{ id: "check", type: "contains", verdict: "pass" }] }] }) },
  });
  const example = byClass(root, "example")[0];
  assert.equal(example.tag, "article");
  assert.equal(byClass(example, "case-title")[0].textContent, "Exact");
  assert.deepEqual(byClass(example, "input-value").map((e) => e.textContent), [exact]);
  assert.deepEqual(byClass(example, "assertion-value").map((e) => e.textContent), [exact]);
  assert.deepEqual(byClass(example, "answer-excerpt").map((e) => e.textContent), [exact]);
  assert.equal(all(example).filter((e) => ["code", "strong", "a", "img"].includes(e.tag)).length, 0);
});

test("UI keeps static error rendering independent of the normal view hierarchy", () => {
  const doc = fakeDoc();
  const root = doc.createElement("main");
  root.append(doc.createElement("header"));
  renderView(doc, root, { error: "unavailable", message: "Static failure." });
  assert.deepEqual(root.children.map((e) => [e.tag, e.className, e.textContent]), [["p", "error", "Static failure."]]);
});

test("UI renders hostile resource text as text nodes only", async () => {
  const hostile = '<img src=x onerror=alert(1)>**bold** [l](javascript:alert(1))';
  const view = await readView(
    stubSession(resource(MINIMAL, {
      title: hostile, instructions: hostile,
      examples: [{ id: "e", input: hostile, assertions: [{ id: "a", type: "contains", value: hostile }] }],
      latest: { status: "current", result: result({ selection: { scope: "full", caseIds: ["e"], totalCases: 1 }, examples: [{ ...result().examples[0], id: "e", answerExcerpt: hostile, assertions: [{ id: "a", type: "contains", verdict: "pass" }] }] }) },
    })),
    MINIMAL,
  );
  const doc = fakeDoc();
  const root = doc.createElement("main");
  renderView(doc, root, view);
  const text = root.textContent;
  assert.ok(text.includes(hostile));
  assert.match(text, /untrusted/i);
  assertTrustedControls(root);
  assert.ok(all(root).some((e) => /\bpass\b/.test(e.className)));

  const stale = await readView(stubSession(resource(MINIMAL, { latest: { status: "stale" } })), MINIMAL);
  const r2 = doc.createElement("main");
  renderView(doc, r2, stale);
  assert.ok(!all(r2).some((e) => /\bpass\b/.test(e.className)));
  assert.match(r2.textContent, /Result out of date/i);

  const r3 = doc.createElement("main");
  renderView(doc, r3, { error: "unavailable", message: "Static failure." });
  assert.match(r3.textContent, /Static failure\./);
  const r4 = doc.createElement("main");
  renderView(doc, r4, await readView(stubSession(resource(MINIMAL)), MINIMAL));
  assert.match(r4.textContent, /No current result/i);
});


function savedFixture(count = 1) {
  const examples = Array.from({ length: count }, (_, i) => ({
    id: i ? `greeting-${i}` : "greeting", input: i ? `Name ${i}` : "Ada",
    assertions: [{ id: "greeting-text", type: "contains", value: i ? `Hello, Name ${i}` : "Hello, Ada" }],
  }));
  const observed = examples.map((e) => ({
    id: e.id, state: "complete", verdict: "pass", answerExcerpt: e.assertions[0].value,
    assertions: [{ id: "greeting-text", type: "contains", verdict: "pass", reason: "Matched literal text." }],
  }));
  return {
    examples,
    latest: { status: "current", result: result({
      selection: { scope: "full", totalCases: count, caseIds: examples.map((e) => e.id) },
      examples: observed,
    }) },
  };
}
const headline = (root) => byClass(root, "badge")[0].textContent;
const cards = (root) => byClass(root, "example");

test("UI renders five mixed cases by ID, with one failing headline and compact passed cases", async () => {
  const fixture = savedFixture(5);
  const r = fixture.latest.result;
  r.fullPass = false; r.verdict = "fail";
  r.examples[0].verdict = "fail";
  r.examples[0].assertions[0].verdict = "fail";
  r.examples[0].assertions[0].reason = "Required greeting was absent.";
  r.examples[0].answerExcerpt = "Goodbye, Ada";
  r.examples.reverse();
  const root = await rendered(fixture);
  assert.equal(headline(root), "1 failing");
  assert.equal(byClass(root, "dense").length, 1);
  assert.equal(byClass(root, "examples-section")[0].children[0].textContent, "ExamplesSaved checks");
  assert.equal(byClass(root, "examples")[0].children.length, 5);
  assert.equal(byClass(root, "example-number").length, 0);
  assert.equal(byClass(root, "example-head").length, 5);
  assert.equal(byClass(root, "result").length, 5);
  assert.equal(byClass(root, "arrow").length, 5);
  assert.ok(byClass(root, "input").every((e) => byClass(e, "label")[0].textContent === "Input"));
  assert.deepEqual(cards(root).map((e) => e.attrs["data-example-id"]), fixture.examples.map((e) => e.id));
  assert.deepEqual(cards(root).map((e) => byClass(e, "case-status")[0].textContent), ["Failed", "Passed", "Passed", "Passed", "Passed"]);
  assert.equal(byClass(root, "hint")[0].textContent, "Ask Copilot to fix the failing example.");
  assert.equal(byClass(cards(root)[0], "answer")[0].open, true);
  assert.ok(cards(root).slice(1).every((e) => byClass(e, "answer")[0].open !== true));
  const missing = byClass(cards(root)[0], "missing");
  assert.equal(missing.length, 2);
  assert.equal(byClass(cards(root)[0], "check-status")[0].textContent, "Missing");
  assert.equal(byClass(cards(root)[0], "assertion-value")[0].textContent, "Hello, Ada");
  assert.ok(cards(root)[0].textContent.includes("Required greeting was absent."));
  assert.doesNotMatch(root.textContent, /starts with|Actual answer|View answer|reasonCode|tokenDiff/);
});

test("UI uses authored check IDs and types, never assertion array positions", async () => {
  const fixture = savedFixture();
  fixture.examples[0].assertions.push({ id: "forbidden", type: "notContains", value: "Goodbye" });
  const r = fixture.latest.result;
  r.verdict = "fail"; r.fullPass = false;
  r.examples[0].verdict = "fail";
  r.examples[0].assertions = [
    { id: "forbidden", type: "notContains", verdict: "fail", reason: "Forbidden phrase." },
    { id: "greeting-text", type: "contains", verdict: "pass" },
  ];
  const root = await rendered(fixture);
  const checks = byClass(root, "check");
  assert.equal(byClass(checks[0], "missing").length, 0);
  assert.equal(byClass(checks[1], "check-status")[0].textContent, "Check failed");
  assert.equal(byClass(checks[1], "missing").length, 0);
});

test("UI renders all authored condition fields literally without executing patterns or inventing semantics", async () => {
  const literal = " **literal**\n<svg onload=alert(1)> ";
  const root = await rendered({ examples: [{ id: "conditions", input: literal, assertions: [
    { id: "a", type: "contains", value: literal },
    { id: "b", type: "notContains", value: literal },
    { id: "c", type: "regex", pattern: "(?invalid)[" },
    { id: "d", type: "llmJudge", rubric: literal, minScore: 70, judgeModel: "judge-x", judgeVersion: "v2" },
    { id: "e", type: "future-type", custom: literal },
  ] }] });
  assert.deepEqual(byClass(root, "check-label").map((e) => e.textContent), ["Expected to contain", "Expected not to contain", "Expected to match pattern", "Expected to meet this rubric", "Condition: future-type"]);
  assert.deepEqual(byClass(root, "assertion-value").map((e) => e.textContent), [literal, literal, "(?invalid)[", literal, literal]);
  assert.ok(root.textContent.includes("Minimum score 70/100"));
  const judge = byClass(root, "judge-details")[0];
  assert.equal(judge.tag, "details");
  assert.ok(judge.textContent.includes("judge-x") && judge.textContent.includes("v2"));
  const fallback = byClass(root, "condition-details")[0];
  assert.equal(fallback.tag, "details");
  assert.ok(fallback.textContent.includes("custom"));
  assert.doesNotMatch(fallback.textContent, /equals|must equal/i);
});

for (const type of ["notContains", "regex"]) {
  test(`UI labels failed ${type} checks without a Missing claim`, async () => {
    const fixture = savedFixture();
    fixture.examples[0].assertions = [{ id: "greeting-text", type, ...(type === "regex" ? { pattern: "^Hello" } : { value: "Bye" }) }];
    const r = fixture.latest.result;
    r.fullPass = false; r.verdict = "fail"; r.examples[0].verdict = "fail";
    r.examples[0].assertions = [{ id: "greeting-text", type, verdict: "fail" }];
    const root = await rendered(fixture);
    assert.equal(headline(root), "1 failing");
    assert.equal(byClass(root, "missing").length, 0);
    assert.equal(byClass(root, "check-status")[0].textContent, "Check failed");
  });
}

for (const state of ["refused", "error", "cancelled", "not_run", "future-state"]) {
  test(`UI treats ${state} rows as neutral incomplete, not failing`, async () => {
    const fixture = savedFixture();
    const r = fixture.latest.result;
    r.state = state; r.verdict = "fail"; r.fullPass = false;
    r.examples[0].state = state; r.examples[0].verdict = "fail";
    r.examples[0].assertions[0].verdict = "fail";
    const root = await rendered(fixture);
    assert.equal(headline(root), "Incomplete result");
    assert.equal(byClass(root, "case-fail").length + byClass(root, "case-pass").length, 0);
    assert.equal(byClass(root, "missing").length, 0);
    assert.doesNotMatch(byClass(root, "hint")[0].textContent, /fix|0 failing/i);
    assert.ok(byClass(root, "result-details").some((e) => e.textContent.includes(state)));
  });
}

test("UI leaves unknown complete verdicts neutral with literal state and verdict details", async () => {
  const fixture = savedFixture();
  fixture.latest.result.examples[0].verdict = "unrecognised";
  const root = await rendered(fixture);
  assert.equal(headline(root), "Incomplete result");
  assert.equal(byClass(root, "case-pass").length + byClass(root, "case-fail").length, 0);
  assert.ok(byClass(root, "result-details").some((e) => e.textContent.includes("unrecognised")));
});

test("UI labels selected passing subsets neutrally and omitted cases Not in this run", async () => {
  const fixture = savedFixture(3);
  const r = fixture.latest.result;
  r.fullPass = false; r.selection.scope = "subset";
  r.selection.caseIds = [fixture.examples[1].id]; r.examples = [r.examples[1]];
  const root = await rendered(fixture);
  assert.equal(headline(root), "Selected examples passed");
  assert.equal(byClass(root, "latest")[0].className.includes("is-warning"), true);
  assert.deepEqual(cards(root).map((e) => byClass(e, "case-status")[0].textContent), ["Not in this run", "Passed", "Not in this run"]);
  assert.doesNotMatch(byClass(root, "hint")[0].textContent, /fix/i);
  assert.match(byClass(root, "hint")[0].textContent, /all.*examples|full test/i);
});

for (const mutation of ["duplicate authored", "duplicate result", "duplicate selection", "foreign result", "missing result", "missing selection", "foreign selection", "duplicate authored check", "duplicate observed check", "foreign check", "missing check", "wrong check type", "unknown check verdict", "wrong total"]) {
  test(`UI refuses full green for ${mutation}, despite a fullPass badge`, async () => {
    const fixture = savedFixture(2), r = fixture.latest.result;
    if (mutation === "duplicate authored") fixture.examples[1].id = fixture.examples[0].id;
    if (mutation === "duplicate result") r.examples.push(structuredClone(r.examples[0]));
    if (mutation === "duplicate selection") r.selection.caseIds.push(r.selection.caseIds[0]);
    if (mutation === "foreign result") r.examples.push({ ...structuredClone(r.examples[0]), id: "foreign" });
    if (mutation === "missing result") r.examples.pop();
    if (mutation === "missing selection") r.selection.caseIds.pop();
    if (mutation === "foreign selection") r.selection.caseIds.push("foreign");
    if (mutation === "duplicate authored check") fixture.examples[0].assertions.push(structuredClone(fixture.examples[0].assertions[0]));
    if (mutation === "duplicate observed check") r.examples[0].assertions.push(structuredClone(r.examples[0].assertions[0]));
    if (mutation === "foreign check") r.examples[0].assertions.push({ id: "foreign", type: "contains", verdict: "pass" });
    if (mutation === "missing check") r.examples[0].assertions = [];
    if (mutation === "wrong check type") r.examples[0].assertions[0].type = "notContains";
    if (mutation === "unknown check verdict") r.examples[0].assertions[0].verdict = "unknown";
    if (mutation === "wrong total") r.selection.totalCases = 9;
    const root = await rendered(fixture);
    assert.notEqual(headline(root), "Passing 2/2");
    assert.equal(byClass(byClass(root, "latest")[0], "pass").length, 0);
    assert.doesNotMatch(headline(root), /0 failing/);
    if (/duplicate (authored|result|selection|observed)/.test(mutation)) {
      assert.equal(byClass(cards(root)[0], "case-pass").length, 0);
    }
  });
}

for (const kind of ["authored", "result", "selection", "authored check", "observed check"]) {
  test(`UI recognises ${kind} collisions after IDs are clipped to 80 characters`, async () => {
    const fixture = savedFixture(2), r = fixture.latest.result, prefix = "x".repeat(80);
    if (kind === "authored") fixture.examples.forEach((e, i) => { e.id = prefix + i; });
    if (kind === "result") r.examples.forEach((e, i) => { e.id = prefix + i; });
    if (kind === "selection") r.selection.caseIds = [prefix + "a", prefix + "b"];
    if (kind === "authored check") fixture.examples[0].assertions = [0, 1].map((i) => ({ id: prefix + i, type: "contains", value: "Hello" }));
    if (kind === "observed check") r.examples[0].assertions = [0, 1].map((i) => ({ id: prefix + i, type: "contains", verdict: "pass" }));
    const root = await rendered(fixture);
    assert.equal(byClass(byClass(root, "latest")[0], "pass").length, 0);
    assert.equal(byClass(cards(root)[0], "case-pass").length, 0);
  });
}

test("UI never attaches stale, ad-hoc or top-level answer excerpts to saved cases", async () => {
  const fixture = savedFixture();
  const r = fixture.latest.result;
  delete r.examples[0].answerExcerpt;
  r.answerExcerpt = "TOP SECRET"; r.adHoc = { state: "complete", verdict: "pass", answerExcerpt: "AD HOC SECRET" };
  const root = await rendered(fixture);
  assert.equal(byClass(root, "answer").length, 0);
  assert.match(cards(root)[0].textContent, /content not included/i);
  assert.doesNotMatch(root.textContent, /TOP SECRET|AD HOC SECRET|View answer|Untrusted excerpt/);
  const stale = await rendered({ ...fixture, latest: { status: "stale" } });
  assert.equal(byClass(stale, "answer").length, 0);
});

for (const [excerpt, serverTruncated, truncated] of [["Hello", true, true], ["Hello… [truncated]", false, true], ["x".repeat(2100), false, true], ["Hello", false, false]]) {
  test(`UI discloses only the literal untrusted excerpt and both truncation signals: ${excerpt.length}/${serverTruncated}`, async () => {
    const fixture = savedFixture();
    fixture.latest.result.examples[0].answerExcerpt = excerpt;
    fixture.latest.result.examples[0].truncated = serverTruncated;
    const root = await rendered(fixture);
    const answer = byClass(root, "answer")[0];
    assert.equal(answer.tag, "details");
    assert.equal(answer.children[0].textContent, "Untrusted excerpt" + (truncated ? " (truncated)" : ""));
    assert.equal(byClass(answer, "answer-excerpt")[0].textContent, excerpt.length > 2000 ? excerpt.slice(0, 2000) + "… [truncated]" : excerpt);
    assert.equal(byClass(root, "missing").length, 0, "never infer checks from excerpt content");
  });
}

test("UI purpose skips metadata, fences, headings and attribution; long original instructions remain available", async () => {
  const prose = "Keep the greeting short. " + "Authored detail. ".repeat(60);
  const instructions = "# Greeter\n## Identity\n- **ID:** greeter\n## Model\n- **Preferred:** fake\n\n## Instructions\n```\nDecoy purpose.\n```\n> Authored by Someone\n\n" + prose;
  const root = await rendered({ instructions });
  const purpose = byClass(root, "purpose")[0].textContent;
  assert.ok(purpose.startsWith("Keep the greeting short."));
  assert.ok(purpose.length <= 180);
  assert.doesNotMatch(purpose, /Decoy|Someone|greeter|Preferred/);
  assert.equal(byClass(root, "instructions-details").length, 0);
  assert.doesNotMatch(root.textContent, /Read instructions/);
  assert.ok(byClass(root, "instructions")[0].textContent.includes(prose));
  const details = byClass(root, "authored-source")[0];
  assert.equal(details.tag, "details");
  assert.notEqual(details.open, true);
  assert.equal(details.children[0].textContent, "Original instruction text");
  assert.equal(details.children[1].tag, "pre");
  assert.equal(details.children[1].textContent, instructions);
  assert.equal(byClass(root, "source-info")[0].textContent.includes(prose), true, "unabridged purpose is available");
});

test("UI presents balanced instruction syntax as plain purpose text without changing the instructions", async () => {
  const root = await rendered({ instructions: "# Greeter\n\nReturn **one** `greeting`." });
  const purpose = byClass(root, "purpose")[0];
  assert.equal(purpose.textContent, "Return one greeting.");
  assert.equal(all(purpose).filter((e) => e.tag === "code" || e.tag === "strong").length, 0);
  const instructions = byClass(root, "instructions")[0];
  assert.deepEqual(all(instructions).filter((e) => e.tag === "code").map((e) => e.textContent), ["greeting"]);
});

test("UI only takes an exact Purpose bullet, not fenced or similarly named metadata", async () => {
  const root = await rendered({ instructions: "# Greeter\n```\n- **Purpose:** Fence fiction\n```\n- **Purposeful:** Not the purpose\n- **ID:** greeter\n- **Purpose:** Exact authored purpose.\n\nOther prose." });
  assert.equal(byClass(root, "purpose")[0].textContent, "Exact authored purpose.");
});



test("UI splits only the first conventional title separator and preserves original metadata suppression", async () => {
  for (const title of ["Greeter", "Greeter - Specialist", "Greeter—Specialist", "Greeter — Specialist — More"]) {
    const root = await rendered({ title, instructions: "# " + title + "\n\nReply plainly." });
    assert.equal(byClass(root, "agent-name")[0].textContent, title.split(" — ")[0]);
    assert.equal(byClass(root, "file-label")[0].textContent, "Agent file · " + title.split(" — ")[0]);
    assert.equal(byClass(root, "source-title")[0].textContent, title);
    assert.equal(byClass(byClass(root, "instructions")[0], "instruction-heading").length, 0);
  }
});

test("UI treats prototype-named condition types as literal unknown types", async () => {
  const root = await rendered({ examples: [{ id: "odd", input: "Ada", assertions: [
    { id: "a", type: "__proto__", value: "A" },
    { id: "b", type: "constructor", value: "B" },
  ] }] });
  assert.deepEqual(byClass(root, "check-label").map((e) => e.textContent), ["Condition: __proto__", "Condition: constructor"]);
  assert.deepEqual(byClass(root, "assertion-value").map((e) => e.textContent), ["A", "B"]);
});

test("UI distinguishes checks sharing an ID but not a type", async () => {
  const fixture = savedFixture(), r = fixture.latest.result;
  fixture.examples[0].assertions.push({ id: "greeting-text", type: "notContains", value: "Bye" });
  r.examples[0].assertions.unshift({ id: "greeting-text", type: "notContains", verdict: "pass" });
  const root = await rendered(fixture);
  assert.equal(headline(root), "Passing 1/1");
  assert.equal(byClass(cards(root)[0], "case-status")[0].textContent, "Passed");
});

test("UI counts only unique selected complete failed saved cases", async () => {
  const fixture = savedFixture(4), r = fixture.latest.result;
  r.fullPass = false; r.verdict = "fail"; r.selection.scope = "subset";
  r.selection.caseIds = ["greeting", "greeting-1", "greeting-2"];
  r.examples.forEach((e) => { e.verdict = "fail"; e.assertions[0].verdict = "fail"; });
  r.examples[1].state = "error";
  r.examples.push(structuredClone(r.examples[2]));
  r.examples.push({ ...structuredClone(r.examples[0]), id: "foreign" });
  const root = await rendered(fixture);
  assert.equal(headline(root), "1 failing");
  assert.deepEqual(cards(root).map((e) => byClass(e, "case-status")[0].textContent), ["Failed", "Incomplete", "Incomplete", "Not in this run"]);
});

test("UI preserves empty included excerpts but never creates disclosures when content is omitted", async () => {
  const fixture = savedFixture();
  fixture.latest.result.examples[0].answerExcerpt = "";
  assert.equal(byClass(await rendered(fixture), "answer").length, 1);
  delete fixture.latest.result.examples[0].answerExcerpt;
  const root = await rendered(fixture);
  assert.equal(byClass(root, "answer").length, 0);
  assert.match(cards(root)[0].textContent, /content not included/i);
});


test("UI mentions omitted answer content only when a current case observation exists", async () => {
  for (const status of ["none", "stale"]) {
    const fixture = savedFixture();
    fixture.latest = { status };
    assert.equal(byClass(await rendered(fixture), "content-unavailable").length, 0);
  }
  const fixture = savedFixture(2);
  const r = fixture.latest.result;
  r.fullPass = false;
  r.selection.scope = "subset";
  r.selection.caseIds = [r.examples[0].id];
  r.examples = [r.examples[0]];
  delete r.examples[0].answerExcerpt;
  const root = await rendered(fixture);
  assert.equal(byClass(cards(root)[0], "content-unavailable").length, 1);
  assert.equal(byClass(cards(root)[1], "content-unavailable").length, 0);
  r.selection.scope = "full";
  r.selection.caseIds = fixture.examples.map((e) => e.id);
  r.examples = [];
  assert.equal(byClass(await rendered(fixture), "content-unavailable").length, 0);
});

test("UI labels root and Squad files friendly while keeping raw paths only in Source details", async () => {
  for (const [path, label] of [[MINIMAL, "Agent file · Greeter"], [SQUAD, "Squad member · Greeter"]]) {
    const root = await rendered({}, path);
    const source = byClass(root, "source")[0], file = byClass(source, "file-label")[0];
    assert.equal(file.textContent, label);
    assert.equal(file.attrs.title, undefined);
    assert.ok(!all(source).filter((e) => !all(byClass(source, "source-details")[0]).includes(e) && e !== source).some((e) => e.textContent.includes(path)));
    const details = byClass(source, "source-details")[0];
    assert.equal(byClass(details, "path-value")[0].tag, "code");
    assert.equal(byClass(details, "path-value")[0].textContent, path);
    assert.equal(byClass(details, "source-title")[0].textContent, "Greeter");
    assert.ok(details.textContent.includes(REV));
    assert.match(details.textContent, /Path from selected root/);
    assertTrustedControls(root);
  }
});

test("UI can format a relative folder label without changing resource path acceptance", async () => {
  const view = await readView(stubSession(resource(MINIMAL)), MINIMAL);
  view.file.path = "agents/team/greeter.agent.md";
  const doc = fakeDoc(), root = doc.createElement("main");
  renderView(doc, root, view);
  assert.equal(byClass(root, "file-label")[0].textContent, "Agent file · Greeter");
  assert.equal(byClass(root, "file-label")[0].attrs.title, undefined);
  assert.equal(byClass(root, "path-value")[0].textContent, view.file.path);
  await assert.rejects(readView(stubSession(resource(view.file.path)), MINIMAL), { code: "invalid-resource" });
});

for (const path of [MINIMAL, SQUAD]) {
  test(`UI copies exactly ${path} only on click and confirms only after success`, async () => {
    const writes = [];
    let complete;
    const clipboard = { writeText(value) { assert.equal(this, clipboard); writes.push(value); return new Promise((resolve) => { complete = resolve; }); } };
    const root = await rendered({}, path, { navigator: { clipboard } });
    assertTrustedControls(root);
    const button = byClass(root, "copy-path")[0], feedback = byClass(root, "copy-status")[0];
    assert.deepEqual(writes, [], "render must not write");
    assert.equal(feedback.textContent, "");
    assert.equal(feedback.tag, "span");
    assert.equal(feedback.attrs.role, "status");
    assert.equal(feedback.attrs["aria-live"], "polite");
    const pending = button.listeners.click[0]();
    assert.deepEqual(writes, [path], "clipboard invocation precedes the first await");
    assert.equal(feedback.textContent, "", "do not report success before write resolves");
    complete(); await pending;
    assert.equal(feedback.textContent, "Path copied.");
    const again = button.listeners.click[0]();
    assert.deepEqual(writes, [path, path], "one exact write per explicit click");
    complete(); await again;
  });
}

for (const mode of ["reject", "throw"]) {
  test(`UI gives static manual-copy feedback after clipboard ${mode}, without leaking errors`, async () => {
    const writes = [];
    const root = await rendered({}, MINIMAL, { navigator: { clipboard: {
      writeText(value) {
        writes.push(value);
        if (mode === "throw") throw new Error("PRIVATE PATH /secret");
        return Promise.reject(new Error("PRIVATE PATH /secret"));
      },
    } } });
    assert.deepEqual(writes, []);
    await byClass(root, "copy-path")[0].listeners.click[0]();
    assert.deepEqual(writes, [MINIMAL]);
    assert.equal(byClass(root, "copy-status")[0].textContent, "Copy unavailable. Select the path and copy it manually.");
    assert.doesNotMatch(root.textContent, /PRIVATE PATH|\/secret/);
  });
}

test("UI supports manual copying without a clipboard API and has no copy control in error views", async () => {
  for (const defaultView of [undefined, {}, { navigator: {} }, { navigator: { clipboard: {} } }]) {
    const root = await rendered({}, MINIMAL, defaultView);
    assertTrustedControls(root);
    await byClass(root, "copy-path")[0].listeners.click[0]();
    assert.equal(byClass(root, "copy-status")[0].textContent, "Copy unavailable. Select the path and copy it manually.");
    assert.equal(byClass(root, "path-value")[0].textContent, MINIMAL);
  }
  const writes = [], doc = fakeDoc({ navigator: { clipboard: { writeText(value) { writes.push(value); } } } });
  const root = doc.createElement("main");
  renderView(doc, root, { error: true, message: "Static failure." });
  assert.equal(all(root).filter((e) => e.tag === "button").length, 0);
  assert.deepEqual(writes, []);
});

test("UI keeps hostile authored buttons inert and exposes only the fixed trusted copy command", async () => {
  const hostile = '<button type="button" data-action="copy-path" onclick="alert(1)">Copy attacker path</button><form><input></form>';
  const writes = [];
  const root = await rendered({ title: hostile, instructions: hostile,
    examples: [{ id: "odd", input: hostile, assertions: [{ id: "check", type: "contains", value: hostile }] }],
  }, MINIMAL, { navigator: { clipboard: { writeText(value) { writes.push(value); return Promise.resolve(); } } } });
  assertTrustedControls(root);
  assert.equal(byClass(root, "instructions")[0].textContent, hostile);
  assert.equal(byClass(root, "input-value")[0].textContent, hostile);
  assert.equal(byClass(root, "assertion-value")[0].textContent, hostile);
  assert.deepEqual(writes, []);
  await byClass(root, "copy-path")[0].listeners.click[0]();
  assert.deepEqual(writes, [MINIMAL]);
});

test("UI keeps a long Purpose visible when the header summary cannot show it all", async () => {
  for (const length of [160, 161]) {
    const purpose = "A".repeat(length);
    const root = await rendered({ instructions: `# Greeter\n## Identity\n- **ID:** greeter\n- **Purpose:** ${purpose}\n\n## Accountable Responsibilities\nReply briefly.` });
    assert.equal(byClass(root, "instructions")[0].textContent.includes(purpose), length > 160);
    assert.ok(byClass(root, "authored-source")[0].textContent.includes(purpose));
  }
});

test("UI hides only exact unique canonical Identity metadata and preserves original normalized text", async () => {
  for (const path of [MINIMAL, SQUAD]) {
    const instructions = "# Greeter\r\n\r\n## Identity\r\n- **ID:** greeter\r\n- **Purpose:** Return **one** `greeting`.\r\n\r\n## Instructions\r\nStay brief.\r\n## Model\r\n- **Preferred:** fake";
    const root = await rendered({ instructions }, path), main = byClass(root, "instructions")[0];
    assert.equal(byClass(root, "purpose")[0].textContent, "Return one greeting.");
    assert.equal(main.textContent, "InstructionsStay brief.");
    const details = byClass(root, "source-details")[0], original = byClass(details, "authored-source")[0];
    assert.equal(original.children[0].textContent, "Original instruction text");
    assert.equal(original.children[1].tag, "pre");
    assert.equal(original.children[1].textContent, instructions.replace(/\r\n/g, "\n"));
    assert.equal(original.children[1].children.length, 0, "original source is never formatted");
  }
});

for (const [name, instructions, visible, headings] of [
  ["ID mismatch", "## Identity\n- **ID:** other\n- **Purpose:** Greet.", "ID: other", ["Identity"]],
  ["duplicate ID", "## Identity\n- **ID:** greeter\n- **ID:** greeter\n- **Purpose:** Greet.", "ID: greeterID: greeter", ["Identity"]],
  ["duplicate Purpose", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n- **Purpose:** Greet.", "Purpose: Greet.Purpose: Greet.", ["Identity"]],
  ["duplicate Identity", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n## Identity\nKeep this.", "ID: greeterPurpose: Greet.", ["Identity", "Identity"]],
  ["empty authored Identity", "## Identity\n\n## Instructions\nReply.", "Identity", ["Identity", "Instructions"]],
  ["nested heading", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n### Notes\nKeep this.", "NotesKeep this.", ["Identity", "Notes"]],
  ["Purpose mismatch", "## Identity\n- **Purpose:** Greet.\n- **Purpose:** Other purpose.", "Purpose: Greet.Purpose: Other purpose.", ["Identity"]],
  ["extra field", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n- **Owner:** Author", "Owner: Author", ["Identity"]],
  ["extra prose", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\nKeep this.", "Keep this.", ["Identity"]],
  ["extra code", "## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n```\nliteral\n```", "literal", ["Identity"]],
  ["fenced fields", "## Identity\n```\n- **ID:** greeter\n- **Purpose:** Greet.\n```", "- **ID:** greeter\n- **Purpose:** Greet.", ["Identity"]],
  ["indented fields", "## Identity\n    - **ID:** greeter\n    - **Purpose:** Greet.", "    - **ID:** greeter\n    - **Purpose:** Greet.", ["Identity"]],
  ["one-space indent", "## Identity\n - **ID:** greeter\n - **Purpose:** Greet.", "ID: greeterPurpose: Greet.", ["Identity"]],
  ["truncated fields", "## Identity\n- **ID:** greeter… [truncated]\n- **Purpose:** Greet… [truncated]", "**ID:** greeter… [truncated]**Purpose:** Greet… [truncated]", ["Identity"]],
  ["ID in prelude", "- **ID:** greeter\n- **Purpose:** Greet.\n\nReply.", "ID: greeter", []],
  ["Purpose in other section", "## Instructions\n- **Purpose:** Greet.\nReply.", "Purpose: Greet.", ["Instructions"]],
]) {
  test(`UI retains nonredundant Identity content: ${name}`, async () => {
    const root = await rendered({ instructions }), main = byClass(root, "instructions")[0];
    assert.ok(main.textContent.includes(visible), main.textContent);
    assert.deepEqual(byClass(main, "instruction-heading").map((e) => e.textContent), headings);
    assert.equal(byClass(root, "authored-source")[0].children[1].textContent, instructions);
  });
}

test("UI hides a minimal prelude Purpose once while keeping three primary paragraphs and four saved checks", async () => {
  const instructions = "# Release writer\n\n- **Purpose:** Turn merged changes into useful release notes. Ask before posting.\n\nDraft concise, user-facing release notes from merged changes.\n\nSay ‘No user-facing changes’ for maintenance-only input.\n\nPosting always returns ‘Approval required’ before any external action.\n\n## Model\n- **Preferred:** local";
  const root = await rendered({ ...savedFixture(4), title: "Release writer", instructions, model: "local", latest: { status: "none" } });
  const main = byClass(root, "instructions")[0];
  assert.equal(main.children.length, 3);
  assert.ok(main.children.every((e) => e.tag === "p"));
  assert.doesNotMatch(main.textContent, /Purpose|Turn merged|Preferred/);
  assert.equal(byClass(root, "purpose")[0].textContent, "Turn merged changes into useful release notes. Ask before posting.");
  assert.equal(cards(root).length, 4);
  assert.equal(byClass(root, "instructions-details").length, 0);
  assertTrustedControls(root);
});

test("frontend and provider sources avoid unsafe sinks and mutation surfaces", async () => {
  const [app, html, css, ext, view] = await Promise.all(
    ["app.js", "index.html", "style.css", "extension.mjs", "view.mjs"].map((f) => readFile(here(f), "utf8")),
  );
  const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  for (const bad of [/innerHTML/, /outerHTML/, /insertAdjacentHTML/, /document\.write/, /\beval\s*\(/, /new Function/, /dangerouslySet/, /marked|markdown/i, /\bXMLHttpRequest\b/, /WebSocket/, /localStorage/, /method\s*:\s*["'](?!GET)/i, /<form|<button|<input|<textarea/i]) {
    assert.doesNotMatch(strip(app), bad, String(bad));
  }
  assert.match(app, /textContent/);
  assert.doesNotMatch(strip(app), /readText|clipboard\s*(?:\.|\?\.)\s*read\b|execCommand|\bpermissions\b/);
  assert.equal([...strip(app).matchAll(/\bel\(doc,\s*"button"/g)].length, 1, "only the fixed Copy path button may be created");
  assert.doesNotMatch(strip(app), /createElement\(\s*["']button["']/);
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>|\son[a-z]+\s*=|style\s*=|<form|<button|<input|<textarea|http-equiv/i);
  assert.doesNotMatch(css, /@import|url\(/i);
  for (const src of [strip(ext), strip(view)]) {
    assert.doesNotMatch(src, /child_process|node:net|node:dgram|\bfetch\s*\(|writeFile|appendFile|unlink|rename|mkdir|\bexec\b|spawn|sendAndWait|session\.send|\.sh\b|Access-Control-Allow|innerHTML/);
    assert.doesNotMatch(src, /listen\([^)]*0\.0\.0\.0|"::"/);
  }
  assert.match(strip(view), /127\.0\.0\.1/);
  assert.match(strip(ext), /createCanvas/);
  assert.match(strip(ext), /@github\/copilot-sdk\/extension/);
  assert.doesNotMatch(strip(ext), /actions\s*:/);
  assert.match(strip(ext), /name:\s*"agent-view"/);
});
