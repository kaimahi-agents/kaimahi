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
  const luminance = (hex) => {
    let digits = hex.replace(/^#/, "");
    if (digits.length === 3) digits = [...digits].map((v) => v + v).join("");
    assert.match(digits, /^[0-9a-f]{6}$/i);
    const channels = digits.match(/../g).map((v) => parseInt(v, 16) / 255).map((v) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
    return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
  };
  for (const [theme, variables] of [["light", roots[0]], ["dark", {...roots[0], ...roots[1]}]]) {
    for (const selector of [".badge.pass", ".badge.fail", ".error"]) {
      const block = css.slice(css.indexOf(selector)).match(/\{([^}]+)\}/)[1];
      const color = declarations(block).color;
      const foreground = color.startsWith("var(") ? variables[color.slice(4, -1)] : color;
      const a = luminance(foreground), b = luminance(variables["--bg"]);
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      assert.ok(ratio >= 4.5, `${theme} ${selector} contrast ${ratio.toFixed(2)}:1 is below 4.5:1`);
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
    assert.match(outcomes[1].value.title, /charter\.md/);
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

function fakeDoc() {
  const mk = (tag) => {
    const el = {
      tag, children: [], className: "", _text: "", attrs: {},
      set textContent(v) { this._text = String(v); this.children = []; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
      append(...n) { this.children.push(...n.map((x) => (typeof x === "string" ? Object.assign(mk("#text"), { _text: x }) : x))); },
      replaceChildren(...n) { this.children = []; this._text = ""; this.append(...n); },
      setAttribute(k, v) { this.attrs[k] = v; },
    };
    for (const name of ["innerHTML", "outerHTML"]) Object.defineProperty(el, name, { set() { throw new Error(name + " used"); } });
    el.insertAdjacentHTML = () => { throw new Error("insertAdjacentHTML used"); };
    return el;
  };
  return { createElement: mk };
}
const all = (el) => [el, ...el.children.flatMap(all)];

test("UI renders hostile resource text as text nodes only", async () => {
  const hostile = '<img src=x onerror=alert(1)>**bold** [l](javascript:alert(1))';
  const view = await readView(
    stubSession(resource(MINIMAL, {
      title: hostile, instructions: hostile,
      examples: [{ id: "e", input: hostile, assertions: [{ id: "a", type: "contains", value: hostile }] }],
      latest: { status: "current", result: result({ examples: [{ ...result().examples[0], answerExcerpt: hostile }] }) },
    })),
    MINIMAL,
  );
  const doc = fakeDoc();
  const root = doc.createElement("main");
  renderView(doc, root, view);
  const text = root.textContent;
  assert.ok(text.includes(hostile));
  assert.match(text, /untrusted/i);
  assert.ok(!all(root).some((e) => e.tag === "img" || e.tag === "a" || e.tag === "script" || e.tag === "button" || e.tag === "input"));
  assert.ok(all(root).some((e) => /\bpass\b/.test(e.className)));

  const stale = await readView(stubSession(resource(MINIMAL, { latest: { status: "stale" } })), MINIMAL);
  const r2 = doc.createElement("main");
  renderView(doc, r2, stale);
  assert.ok(!all(r2).some((e) => /\bpass\b/.test(e.className)));
  assert.match(r2.textContent, /stale/i);

  const r3 = doc.createElement("main");
  renderView(doc, r3, { error: "unavailable", message: "Static failure." });
  assert.match(r3.textContent, /Static failure\./);
  const r4 = doc.createElement("main");
  renderView(doc, r4, await readView(stubSession(resource(MINIMAL)), MINIMAL));
  assert.match(r4.textContent, /No current result/i);
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
