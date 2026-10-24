import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { instructionSections, replaceExamples, replaceInstructions } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";
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
} from "../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs";
import { renderView } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/app.js";
import { modelLabel, editorValue, insertPlainText, runtimeModelControl } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/editor.mjs";

const here = (name) => fileURLToPath(new URL("../../copilot-wsl/com.github.copilot/extensions/agent-view/" + name, import.meta.url));
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

test("agent paths preserve ordinary names in any directory", () => {
  for (const path of [MINIMAL, SQUAD, "/repo one/agents/Release Notes.agent.md", "/repo Māori/.squad/agents/greeter/charter.md",
    "../other/greeter.agent.md", "/Bob's (agents) + notes/@release=2026/report.agent.md", "a/b.agent.md", "con.agent.md", "x%2e.agent.md", "x#draft.agent.md", "x".repeat(64) + ".agent.md"]) {
    assert.equal(normalizePath(path), path);
    assert.equal(resourceUri(path), `kmx://agent/${encodeURIComponent(path)}`);
  }
  for (const bad of ["", ".agent.md", "x.agent.md?q=1", "x.agent.md#f", "x.agent.md ", "x\0.agent.md", "x.md",
    ".squad/agents/x/Charter.md", ".squad/agents/x/y/charter.md", ".squad/agent/x/charter.md", null, 4, {}, ["x.agent.md"]]) {
    assert.throws(() => normalizePath(bad), (e) => e instanceof ViewError && e.code === "invalid-path", JSON.stringify(bad));
  }
});

test("resource URI encodes the whole path as one component", () => {
  assert.equal(resourceUri(MINIMAL), "kmx://agent/greeter.agent.md");
  assert.equal(resourceUri(SQUAD), "kmx://agent/.squad%2Fagents%2Fgreeter%2Fcharter.md");
  assert.equal(CANVAS_ID, "kmx-agent-view");
});

for (const path of [MINIMAL, SQUAD, "/repo one/greeter.agent.md", "/repo Māori/.squad/agents/greeter/charter.md"]) {
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

// Typed SDK-shaped fixtures, not Copilot-app qualification.
test("optional host model metadata validates, retains last valid and fences overlapping reads", async () => {
  const session = stubSession(resource(MINIMAL));
  let current = { modelId: "chat-a" };
  session.rpc.model = { getCurrent: async () => current };
  assert.equal((await readView(session, MINIMAL)).chatModel, "chat-a");
  session.rpc.model.getCurrent = async () => { throw Error("transient private error"); };
  assert.equal((await readView(session, MINIMAL)).chatModel, "chat-a");
  session.rpc.model.getCurrent = async () => current;
  for (const invalid of [{}, { modelId: "auto" }, { modelId: "bad\nID" }, { modelId: "https://secret.invalid" }]) {
    current = { modelId: "chat-a" }; assert.equal((await readView(session, MINIMAL)).chatModel, "chat-a");
    current = invalid;
    assert.equal((await readView(session, MINIMAL)).chatModel, undefined);
  }
  let release;
  session.rpc.model.getCurrent = () => new Promise(resolve => { release = resolve; });
  const older = readView(session, MINIMAL);
  while (!release) await new Promise(resolve => setImmediate(resolve));
  session.rpc.model.getCurrent = async () => ({ modelId: "chat-b" });
  assert.equal((await readView(session, MINIMAL)).chatModel, "chat-b");
  release({ modelId: "chat-old" });
  assert.equal((await older).chatModel, "chat-b");
  assert.equal((await readView(stubSession(resource(MINIMAL)), MINIMAL)).chatModel, undefined);
  for (const response of [{}, { modelId: "auto" }, { modelId: 42 }]) {
    const missing = stubSession(resource(MINIMAL)); missing.rpc.model = { getCurrent: async () => response };
    assert.equal((await readView(missing, MINIMAL)).chatModel, undefined);
  }
});

test("canvas titles use agent name once, distinguish folders and stay fixed per instance", async () => {
  let title = "Greeter — Greeting specialist", reads = 0;
  const session = stubSession(resource(MINIMAL));
  session.rpc.mcp.resources.read = async ({ uri }) => { reads++; return { contents: [{ text: JSON.stringify(resource(decodeURIComponent(uri.slice("kmx://agent/".length)), { title })) }] }; };
  const ctl = createViewController({ getSession: () => session });
  try {
    const a = await ctl.open({ instanceId: "a", input: { path: "/one/greeter.agent.md" } });
    const b = await ctl.open({ instanceId: "b", input: { path: "/two/greeter.agent.md" } });
    assert.equal(a.title, "Greeter · one"); assert.equal(b.title, "Greeter · two");
    title = "Changed";
    assert.equal((await ctl.open({ instanceId: "a", input: { path: "/one/greeter.agent.md" } })).title, a.title);
    assert.equal(reads, 2);
    session.rpc.mcp.resources.read = async () => { throw Error("unavailable"); };
    assert.equal((await ctl.open({ instanceId: "fallback", input: { path: "plain.agent.md" } })).title, "plain.agent.md");
  } finally { await ctl.closeAll(); }
});

test("readView refuses invalid paths before any resource read", async () => {
  const session = stubSession(resource(MINIMAL));
  await assert.rejects(readView(session, "../README.md"), { code: "invalid-path" });
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

test("text fallback distinguishes authored model from the current override route", async () => {
  for (const [authored, provider, model] of [["gpt-5.6-terra", "fake", "fake"], ["fake", "copilot", "gpt-5.6-terra"], ["", "copilot", "gpt-5.6-terra"]]) {
    const raw = resource(MINIMAL, { model: authored, latest: { status: "current", result: result({ modelRoute: { provider, model } }) } });
    const text = formatView(await readView(stubSession(raw), MINIMAL));
    assert.ok(text.includes(`Authored model: ${authored || "(not set)"}`));
    const summary = text.split("\n").find(line => line.startsWith("Latest:"));
    assert.ok(summary.includes("current, PASS"));
    assert.ok(summary.includes(`runtime model: ${provider}/${model}`));
  }
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

test("status, error and quiet text tokens meet AA contrast in both themes", async () => {
  const css = await readFile(new URL("../../copilot-wsl/com.github.copilot/extensions/agent-view/style.css", import.meta.url), "utf8");
  // oklch -> linear sRGB luminance (CSS Color 4), so the check follows the actual tokens.
  const lum = (value) => {
    const [L, C, H] = value.match(/oklch\(([\d.]+) ([\d.]+) ([\d.]+)/).slice(1).map(Number);
    const a = C * Math.cos(H * Math.PI / 180), b = C * Math.sin(H * Math.PI / 180);
    const l = (L + .3963377774 * a + .2158037573 * b) ** 3, m = (L - .1055613458 * a - .0638541728 * b) ** 3, s = (L - .0894841775 * a - 1.291485548 * b) ** 3;
    const rgb = [4.0767416621 * l - 3.3077115913 * m + .2309699292 * s, -1.2684380046 * l + 2.6097574011 * m - .3413193965 * s, -.0041960863 * l - .7034186147 * m + 1.707614701 * s];
    return rgb.map((v) => Math.min(1, Math.max(0, v))).reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
  };
  const themes = [css.match(/^:root\{([^}]*)\}/m)[1], css.match(/prefers-color-scheme: light\)\{:root\{([^}]*)\}/)[1]];
  for (const block of themes) {
    const token = (name) => block.match(new RegExp(`--${name}:(oklch\\([^)]*\\))`))[1];
    for (const [fg, bgs, min] of [["text", ["bg", "surface"], 4.5], ["text-2", ["bg", "surface"], 4.5], ["muted", ["bg", "surface"], 4.5],
      ["accent", ["bg", "surface"], 4.5], ["danger", ["bg", "surface"], 4.5], ["success", ["bg", "surface"], 3], ["warn", ["bg"], 3],
      ["warn-text", ["warn-tint"], 4.5], ["text", ["info-tint"], 4.5], ["control-border", ["surface"], 3]]) {
      for (const bg of bgs) {
        const x = lum(token(fg)), y = lum(token(bg));
        assert.ok((Math.max(x, y) + .05) / (Math.min(x, y) + .05) >= min, `${fg} on ${bg}`);
      }
    }
  }
  assert.match(css, /\.error\{[^}]*var\(--warn-text\)/, "errors are amber text, not the old red");
  assert.match(css, /prefers-reduced-motion:\s*reduce\)\s*\{\s*\.spin\s*\{\s*animation:\s*none/);
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
    assert.equal(outcomes[1].value.title, "charter.md · greeter");
  } finally { await controller.closeAll(); }
});

test("controller refuses invalid canvas input and opens no listener", async () => {
  const ctl = createViewController({ getSession: () => stubSession(resource(MINIMAL)) });
  for (const input of [undefined, null, "x", [], {}, { path: "../README.md" }, { path: MINIMAL, extra: 1 }, { path: MINIMAL, url: "http://x" }]) {
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

  for (const args of ["", "a b", "../README.md", `${MINIMAL} extra`]) {
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
  const doc = { activeElement: null };
  const mk = (tag) => {
    const el = {
      tag, children: [], className: "", _text: "", attrs: {}, listeners: {}, style: {},
      get scrollHeight() { return Math.max(3, (this.value ?? "").split("\n").length) * 21; },
      get nodeType() { return tag === "#text" ? 3 : 1; },
      get tagName() { return tag.toUpperCase(); },
      get childNodes() { return this._text ? [Object.assign(mk("#text"), { _text: this._text }), ...this.children] : this.children; },
      get classList() { return { add: (...names) => { this.className += " " + names.join(" "); } }; },
      querySelector(selector) { return all(this).find((e) => selector.startsWith(".") ? e.className.split(/\s+/).includes(selector.slice(1)) : e.tag === selector); },
      querySelectorAll(selector) { return all(this).filter(e => selector.split(",").some(s => s.startsWith(".") ? e.className.split(/\s+/).includes(s.slice(1)) : e.tag === s)); },
      focus() { doc.activeElement = this; },
      // Minimal layout: a list is 41px per visible row; everything else is one 32px line.
      getBoundingClientRect() { const h = tag === "ul" ? this.children.filter((c) => !c.hidden).length * 41 : 32; return { top: 0, bottom: h, height: h }; },
      contains(node) { return all(this).includes(node); },
      set textContent(v) { this._text = String(v); this.children = []; },
      get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
      append(...n) { this.children.push(...n.map((x) => (typeof x === "string" ? Object.assign(mk("#text"), { _text: x }) : x))); },
      replaceChildren(...n) { this.children = []; this._text = ""; this.append(...n); },
      setAttribute(k, v) { this.attrs[k] = v; },
      getAttribute(k) { return this.attrs[k] ?? null; },
      addEventListener(type, listener) {
        this.listeners[type] ??= [];
        this.listeners[type].push(listener);
      },
    };
    for (const name of ["innerHTML", "outerHTML"]) Object.defineProperty(el, name, { set() { throw new Error(name + " used"); } });
    el.insertAdjacentHTML = () => { throw new Error("insertAdjacentHTML used"); };
    return el;
  };
  const mkNS = (ns, tag) => Object.assign(mk(tag), { namespaceURI: ns });
  return Object.assign(doc, { createElement: mk, createElementNS: mkNS, createTextNode: (text) => Object.assign(mk("#text"), { _text: text }), defaultView });
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

// Read-only views expose the fixed copy command plus fixed navigation only: a
// row button per openable test, Back inside a test, and the list filter.
function assertTrustedControls(root) {
  const nodes = all(root), buttons = nodes.filter((e) => e.tag === "button");
  const copy = byClass(root, "copy-path"), inTest = byClass(root, "back").length > 0;
  // Main keeps exactly one copy command in Source details; an open test has none.
  assert.equal(copy.length, inTest ? 0 : 1);
  if (!inTest) {
    assert.equal(copy[0].textContent, "Copy path");
    assert.deepEqual(copy[0].attrs, { type: "button", "aria-label": "Copy agent path", "data-action": "copy-path" });
    assert.equal(byClass(byClass(root, "source-details")[0], "copy-path")[0], copy[0]);
  }
  for (const button of buttons) {
    assert.ok(["copy-path", "test-row", "back", "filter-button"].some((name) => button.className.split(/\s+/).includes(name)), button.className);
    assert.deepEqual(Object.keys(button.listeners), ["click"]);
    assert.equal(button.listeners.click.length, 1);
  }
  assert.equal(nodes.filter((e) => ["a", "img", "script", "input", "form", "textarea", "iframe", "object", "embed"].includes(e.tag)).length, 0);
  assert.ok(nodes.every((e) => !Object.keys(e.attrs).some((k) => /^(?:on|href$|src$|style$)/i.test(k))));
  assert.ok(nodes.filter((e) => e.tag !== "button").every((e) => Object.keys(e.listeners).length === 0));
  // Every SVG is a fixed decorative icon, never authored markup.
  assert.ok(nodes.filter((e) => e.tag === "svg").every((e) => e.attrs["aria-hidden"] === "true" && e.namespaceURI === "http://www.w3.org/2000/svg"));
}
// Main shows one line per test; a test's details live behind its row.
const summaryOf = (root) => byClass(root, "tests-summary")[0].textContent;
const items = (root) => byClass(root, "test-item");
const rowLabels = (root) => items(root).map((i) => byClass(i, "test-verdict")[0].textContent);
const noteOf = (item) => byClass(item, "test-note")[0].textContent;
function openTest(root, id) {
  const item = items(root).find((i) => i.attrs["data-test-id"] === id);
  assert.ok(item, `row ${id}`);
  byClass(item, "test-row")[0].listeners.click[0]();
  assert.equal(byClass(root, "test-item").length, 0, "the test opens in place of the list");
  return root;
}
const goBack = (root) => { byClass(root, "back")[0].listeners.click[0](); return root; };
const checkLabels = (root) => byClass(root, "check").map((c) => byClass(c, "status-icon")[0].attrs["aria-label"]);

test("UI gives identity and purpose priority while keeping source and full revision in details", async () => {
  const title = "Greeter — Greeting specialist";
  const root = await rendered({ title, instructions: "# " + title + "\n## Identity\n- **ID:** greeter\n- **Purpose:** Return a greeting.\n## Model\n- **Preferred:** fake" }, SQUAD);
  const header = byClass(root, "agent-header")[0], source = byClass(root, "source-details")[0];
  assert.equal(root.children[0].className, "content");
  assert.equal(all(header).find((e) => e.tag === "h1").textContent, "Greeter");
  assert.equal(byClass(header, "purpose")[0].textContent, "Return a greeting.");
  assert.equal(byClass(header, "agent-meta")[0].textContent, "charter.md · in greeter");
  assert.equal(byClass(header, "agent-eyebrow")[0].textContent, "Your agent");
  assert.equal(source.tag, "details"); assert.notEqual(source.open, true);
  assert.ok(source.textContent.includes(title) && source.textContent.includes(REV));
  assert.equal(byClass(source, "path-value")[0].textContent, SQUAD);
  assert.ok(![byClass(header, "agent-meta")[0], byClass(header, "purpose")[0]].some((e) => e.textContent.includes(REV)));
});

test("UI keeps the latest result in the Tests summary and quiet read-only guidance", async () => {
  for (const [latest, label] of [
    [{ status: "none" }, "Not run yet"],
    [{ status: "stale" }, "Out of date"],
    [{ status: "current", result: result() }, "1 passing"],
    [{ status: "current", result: result({ state: "cancelled", fullPass: false }) }, "Incomplete run: 1 passing"],
  ]) {
    const root = await rendered({ latest });
    assert.equal(summaryOf(root), label);
    assert.equal(byClass(root, "status-pill").length + byClass(root, "hero").length, 0);
    assert.equal(byClass(root, "instructions").length, 1);
    assert.equal(byClass(root, "tests-section").length, 1);
    assert.match(root.textContent, /Read-only inspection/); assertTrustedControls(root);
  }
});

test("UI explains a confirmed failed case once in its row and once at the failed check", async () => {
  const fixture = savedFixture(), r = fixture.latest.result;
  r.fullPass = false; r.verdict = "fail";
  r.examples[0].verdict = "fail"; r.examples[0].assertions[0].verdict = "fail"; r.examples[0].answerExcerpt = "Hi, Ada";
  const root = await rendered(fixture);
  assert.equal(summaryOf(root), "1 failing");
  assert.equal(items(root)[0].attrs["data-status"], "fail");
  assert.equal(noteOf(items(root)[0]), 'Missing "Hello, Ada"');
  openTest(root, "greeting");
  assert.equal(byClass(root, "why").length, 1);
  assert.equal(byClass(root, "answer-excerpt").length, 1);
  assertTrustedControls(root);
  r.examples[0].state = "refused";
  const refused = await rendered(fixture);
  assert.equal(summaryOf(refused), "1 didn't finish");
  assert.equal(items(refused)[0].attrs["data-status"], "error");
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
  openTest(root, "exact");
  assert.equal(byClass(root, "test-title")[0].textContent, "Exact");
  assert.equal(byClass(root, "example-input")[0].textContent, exact);
  assert.ok(byClass(root, "check-label")[0].textContent.includes(exact));
  assert.deepEqual(byClass(root, "answer-excerpt").map((e) => e.textContent), [exact]);
  const sections = byClass(root, "detail-section");
  assert.equal(sections.flatMap(all).filter((e) => ["code", "strong", "a", "img"].includes(e.tag)).length, 0);
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
  assert.ok(all(root).some((e) => /\bpass\b/.test(e.className)));
  assertTrustedControls(root);
  openTest(root, "e");
  const text = root.textContent;
  assert.ok(text.includes(hostile));
  assert.doesNotMatch(text, /Untrusted excerpt/);
  assertTrustedControls(root);

  const stale = await readView(stubSession(resource(MINIMAL, { latest: { status: "stale" } })), MINIMAL);
  const r2 = doc.createElement("main");
  renderView(doc, r2, stale);
  assert.ok(!all(r2).some((e) => /\bpass\b/.test(e.className)));
  assert.match(r2.textContent, /Out of date/i);

  const r3 = doc.createElement("main");
  renderView(doc, r3, { error: "unavailable", message: "Static failure." });
  assert.match(r3.textContent, /Static failure\./);
  const r4 = doc.createElement("main");
  renderView(doc, r4, await readView(stubSession(resource(MINIMAL)), MINIMAL));
  assert.match(r4.textContent, /Not run yet/i);
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



test("UI renders five mixed cases by ID in authored order, with one failing summary and a quiet passing list", async () => {
  const fixture = savedFixture(5), r = fixture.latest.result;
  r.fullPass = false; r.verdict = "fail";
  r.examples[0].verdict = "fail"; r.examples[0].assertions[0].verdict = "fail";
  r.examples[0].answerExcerpt = "Goodbye, Ada"; r.examples.reverse();
  const root = await rendered(fixture);
  assert.equal(summaryOf(root), "1 failing, 4 passing");
  assert.equal(byClass(root, "test-list")[0].children.length, 5);
  assert.deepEqual(items(root).map((e) => e.attrs["data-test-id"]), fixture.examples.map((e) => e.id));
  assert.deepEqual(rowLabels(root), ["Failed", "Passed", "Passed", "Passed", "Passed"]);
  assert.equal(noteOf(items(root)[0]), 'Missing "Hello, Ada"');
  assert.deepEqual(items(root).slice(1).map(noteOf), ["", "", "", ""]);
  assert.equal(byClass(root, "answer-excerpt").length, 0, "answers live in the test, not the list");
  openTest(root, "greeting");
  assert.equal(byClass(root, "answer-excerpt")[0].textContent, "Goodbye, Ada");
  assert.equal(byClass(root, "why")[0].textContent, 'Missing "Hello, Ada"');
  assert.equal(byClass(root, "why").length, 1);
});

test("UI uses authored check IDs and types, never assertion array positions", async () => {
  const fixture = savedFixture(), r = fixture.latest.result;
  fixture.examples[0].assertions.push({ id: "forbidden", type: "notContains", value: "Goodbye" });
  r.verdict = "fail"; r.fullPass = false; r.examples[0].verdict = "fail";
  r.examples[0].assertions = [{ id: "forbidden", type: "notContains", verdict: "fail" }, { id: "greeting-text", type: "contains", verdict: "pass" }];
  const root = openTest(await rendered(fixture), "greeting");
  assert.deepEqual(checkLabels(root), ["Passed", "Failed"]);
  assert.match(byClass(root, "why")[0].textContent, /^Found "Goodbye"/);
  assert.doesNotMatch(root.textContent, /Missing/);
});

test("UI renders all authored condition fields literally without executing patterns or inventing semantics", async () => {
  const literal = " **literal**\n<svg onload=alert(1)> ";
  const root = openTest(await rendered({ examples: [{ id: "conditions", input: literal, assertions: [
    { id: "a", type: "contains", value: literal }, { id: "b", type: "notContains", value: literal },
    { id: "c", type: "regex", pattern: "(?invalid)[" },
    { id: "d", type: "llmJudge", rubric: literal, minScore: 70, judgeModel: "judge-x", judgeVersion: "v2" },
    { id: "e", type: "future-type", custom: literal },
  ] }] }), "conditions");
  assert.ok(root.textContent.includes(literal) && root.textContent.includes("(?invalid)["));
  const details = byClass(root, "check-details").map((e) => e.textContent).join("\n");
  for (const value of ["rubric:", "minScore: 70", "judge-x", "v2", "custom:", "future-type"]) assert.ok(details.includes(value), value);
  assertTrustedControls(root);
});

for (const type of ["notContains", "regex"]) {
  test(`UI labels failed ${type} checks without a Missing claim`, async () => {
    const fixture = savedFixture();
    fixture.examples[0].assertions = [{ id: "greeting-text", type, ...(type === "regex" ? { pattern: "^Hello" } : { value: "Bye" }) }];
    const r = fixture.latest.result;
    r.fullPass = false; r.verdict = "fail"; r.examples[0].verdict = "fail";
    r.examples[0].assertions = [{ id: "greeting-text", type, verdict: "fail" }];
    const root = await rendered(fixture);
    assert.equal(summaryOf(root), "1 failing");
    assert.match(noteOf(items(root)[0]), type === "regex" ? /^No match for \^Hello/ : /^Found "Bye"/);
    openTest(root, "greeting");
    assert.doesNotMatch(root.textContent, /Missing/);
    assert.deepEqual(checkLabels(root), ["Failed"]);
  });
}

for (const state of ["refused", "error", "cancelled", "not_run", "future-state"]) {
  test(`UI treats ${state} rows as amber didn't-finish, never failing`, async () => {
    const fixture = savedFixture();
    const r = fixture.latest.result;
    r.state = state; r.verdict = "fail"; r.fullPass = false;
    r.examples[0].state = state; r.examples[0].verdict = "fail";
    r.examples[0].assertions[0].verdict = "fail";
    const root = await rendered(fixture);
    assert.equal(summaryOf(root), "1 didn't finish");
    assert.deepEqual(rowLabels(root), ["Didn't finish"]);
    assert.doesNotMatch(summaryOf(root), /failing|passing/i);
    openTest(root, "greeting");
    assert.equal(byClass(root, "fail").length + byClass(root, "pass").length, 0);
    assert.doesNotMatch(root.textContent, /Missing/);
    assert.ok(byClass(root, "result-details").some((e) => e.textContent.includes(state)));
  });
}

test("UI leaves unknown complete verdicts neutral with literal state and verdict details", async () => {
  const fixture = savedFixture();
  fixture.latest.result.examples[0].verdict = "unrecognised";
  const root = await rendered(fixture);
  assert.equal(summaryOf(root), "1 incomplete");
  assert.equal(items(root)[0].attrs["data-status"], "none");
  openTest(root, "greeting");
  assert.equal(byClass(root, "fail").length + byClass(root, "pass").length, 0);
  assert.ok(byClass(root, "result-details").some((e) => e.textContent.includes("unrecognised")));
});

test("UI labels selected passing subsets neutrally and omitted cases Not in this run", async () => {
  const fixture = savedFixture(3);
  const r = fixture.latest.result;
  r.fullPass = false; r.selection.scope = "subset";
  r.selection.caseIds = [fixture.examples[1].id]; r.examples = [r.examples[1]];
  const root = await rendered(fixture);
  assert.equal(summaryOf(root), "1 passing, 2 not run");
  assert.deepEqual(rowLabels(root), ["Not in this run", "Passed", "Not in this run"]);
  assert.deepEqual(items(root).map((e) => e.attrs["data-status"]), ["none", "pass", "none"]);
  assert.doesNotMatch(summaryOf(root), /fix/i);
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
    assert.notEqual(summaryOf(root), "2 passing");
    assert.notEqual(rowLabels(root).join(), "Passed,Passed");
    assert.doesNotMatch(summaryOf(root), /0 failing/);
    if (/duplicate (authored|result|selection|observed)/.test(mutation)) {
      assert.notEqual(items(root)[0].attrs["data-status"], "pass");
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
    assert.notEqual(summaryOf(root), "2 passing");
    assert.notEqual(items(root)[0].attrs["data-status"], "pass");
  });
}

test("UI never attaches stale, ad-hoc or top-level answer excerpts to saved cases", async () => {
  const fixture = savedFixture(), r = fixture.latest.result;
  delete r.examples[0].answerExcerpt;
  r.answerExcerpt = "TOP SECRET"; r.adHoc = { state: "complete", verdict: "pass", answerExcerpt: "AD HOC SECRET" };
  const root = await rendered(fixture);
  assert.match(byClass(root, "ad-hoc-result")[0].textContent, /AD HOC SECRET/);
  openTest(root, "greeting");
  const detail = byClass(root, "detail-section").map((e) => e.textContent).join("\n");
  assert.equal(byClass(root, "answer").length, 0);
  assert.doesNotMatch(detail, /TOP SECRET|AD HOC SECRET/);
  assert.doesNotMatch(root.textContent, /TOP SECRET/);
  const stale = openTest(await rendered({ ...fixture, latest: { status: "stale" } }), "greeting");
  assert.equal(byClass(stale, "answer").length, 0);
});

for (const [excerpt, serverTruncated, truncated] of [["Hello", true, true], ["Hello… [truncated]", false, true], ["x".repeat(2100), false, true], ["Hello", false, false]]) {
  test(`UI discloses only the literal untrusted excerpt and both truncation signals: ${excerpt.length}/${serverTruncated}`, async () => {
    const fixture = savedFixture();
    fixture.latest.result.examples[0].answerExcerpt = excerpt;
    fixture.latest.result.examples[0].truncated = serverTruncated;
    const root = openTest(await rendered(fixture), "greeting");
    const answer = byClass(root, "answer")[0];
    assert.equal(answer.tag, "div");
    assert.deepEqual(byClass(answer, "label").map(e => e.textContent), truncated ? ["Answer truncated"] : []);
    assert.doesNotMatch(answer.textContent, /Untrusted excerpt/);
    assert.equal(byClass(answer, "answer-excerpt")[0].textContent, excerpt.length > 2000 ? excerpt.slice(0, 2000) + "… [truncated]" : excerpt);
    assert.doesNotMatch(root.textContent, /Missing/, "never infer checks from excerpt content");
  });
}

test("UI purpose skips metadata, fences, headings and attribution; long original instructions remain available", async () => {
  const prose = "Keep the greeting short. " + "Authored detail. ".repeat(60);
  const root = await rendered({ instructions: "# Greeter\n## Identity\n- **ID:** greeter\n## Model\n- **Preferred:** fake\n\n## Instructions\n```\nDecoy purpose.\n```\n> Authored by Someone\n\n" + prose });
  const purpose = byClass(root, "purpose")[0].textContent;
  assert.ok(purpose.startsWith("Keep the greeting short.") && Array.from(purpose).length <= 160);
  assert.doesNotMatch(purpose, /Decoy|Someone|greeter|Preferred/);
  assert.ok(byClass(root, "instructions")[0].textContent.includes(prose));
  assert.ok(byClass(root, "source-info")[0].textContent.includes(prose));
});

test("UI presents balanced instruction syntax as plain purpose text without changing the instructions", async () => {
  const root = await rendered({ instructions: "# Greeter\n\nReturn **one** `greeting`." });
  assert.equal(byClass(root, "purpose")[0].textContent, "Return **one** `greeting`.");
  assert.deepEqual(all(byClass(root, "instructions")[0]).filter((e) => e.tag === "code").map((e) => e.textContent), ["greeting"]);
});

test("UI only takes an exact Purpose bullet, not fenced or similarly named metadata", async () => {
  const root = await rendered({ instructions: "# Greeter\n```\n- **Purpose:** Fence fiction\n```\n- **Purposeful:** Not the purpose\n- **ID:** greeter\n- **Purpose:** Exact authored purpose.\n\nOther prose." });
  assert.equal(byClass(root, "purpose")[0].textContent, "Exact authored purpose.");
});



test("UI preserves authored compound names and suppresses conventional or repeated role suffixes", async () => {
  for (const [title, name] of [["Greeter", "Greeter"], ["Code review - strict", "Code review - strict"], ["Greeter – Specialist", "Greeter – Specialist"], ["Greeter - Greeter specialist", "Greeter"], ["Greeter—Specialist", "Greeter—Specialist"], ["Greeter — Specialist — More", "Greeter"]]) {
    const root = await rendered({ title, instructions: "# " + title + "\n\nReply plainly." });
    assert.equal(all(byClass(root, "agent-header")[0]).find((e) => e.tag === "h1").textContent, name);
    assert.ok(byClass(root, "source-info")[0].textContent.includes(title));
    assert.equal(byClass(byClass(root, "instructions")[0], "instruction-heading").length, 0);
  }
});

test("readonly canvases keep model information without adding an editing control", async () => {
  for (const [model, label] of [["fake", "fake · from the agent file"], [{}, "GPT-5.6 Terra · default"]]) {
    const root = await rendered({ model }), values = byClass(root, "model-readonly");
    assert.equal(values.length, 1, "readonly model metadata remains visible");
    assert.equal(values[0].textContent, label);
    assertTrustedControls(root);
  }
});

test("readonly v1 resources with an empty authored model still open a usable canvas", async () => {
  const root = await rendered({ model: {} });
  assert.equal(byClass(root, "agent-header").length, 1);
  assert.equal(byClass(root, "model-picker").length, 0);
  assert.equal((await readView(stubSession(resource(MINIMAL, { model: {} })), MINIMAL)).model, "");
});

test("UI treats prototype-named condition types as literal unknown types", async () => {
  const root = openTest(await rendered({ examples: [{ id: "odd", input: "Ada", assertions: [
    { id: "a", type: "__proto__", value: "A" }, { id: "b", type: "constructor", value: "B" },
  ] }] }), "odd");
  assert.deepEqual(byClass(root, "check-label").map((e) => e.textContent), ["Condition: __proto__", "Condition: constructor"]);
  assert.match(byClass(root, "check-details").map((e) => e.textContent).join(" "), /value: A.*value: B/);
});

test("UI distinguishes checks sharing an ID but not a type", async () => {
  const fixture = savedFixture(), r = fixture.latest.result;
  fixture.examples[0].assertions.push({ id: "greeting-text", type: "notContains", value: "Bye" });
  r.examples[0].assertions.unshift({ id: "greeting-text", type: "notContains", verdict: "pass" });
  const root = await rendered(fixture);
  assert.equal(summaryOf(root), "1 passing");
  assert.deepEqual(rowLabels(root), ["Passed"]);
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
  assert.equal(summaryOf(root), "1 failing, 1 didn't finish, 1 incomplete, 1 not run");
  assert.deepEqual(rowLabels(root), ["Failed", "Didn't finish", "Incomplete", "Not in this run"]);
});

test("UI preserves empty included excerpts but never creates disclosures when content is omitted", async () => {
  const fixture = savedFixture();
  fixture.latest.result.examples[0].answerExcerpt = "";
  assert.equal(byClass(openTest(await rendered(fixture), "greeting"), "answer").length, 1);
  delete fixture.latest.result.examples[0].answerExcerpt;
  const root = openTest(await rendered(fixture), "greeting");
  assert.equal(byClass(root, "answer").length, 0);
  assert.equal(byClass(root, "answer-excerpt").length, 0);
});


test("UI mentions omitted answer content only when a current case observation exists", async () => {
  for (const status of ["none", "stale"]) {
    const fixture = savedFixture(); fixture.latest = { status };
    const root = openTest(await rendered(fixture), "greeting");
    assert.equal(byClass(root, "answered").length, 0);
    assert.doesNotMatch(root.textContent, /not included/);
  }
  const fixture = savedFixture(2), r = fixture.latest.result;
  r.fullPass = false; r.selection.scope = "subset"; r.selection.caseIds = [r.examples[0].id];
  r.examples = [r.examples[0]]; delete r.examples[0].answerExcerpt;
  const root = await rendered(fixture);
  assert.deepEqual(rowLabels(root), ["Passed", "Not in this run"]);
  assert.match(openTest(root, "greeting").textContent, /Run this test to see its answer/);
  assert.equal(byClass(openTest(goBack(root), "greeting-1"), "answered").length, 0);
  r.examples[0].verdict = "fail"; r.examples[0].assertions[0].verdict = "fail";
  assert.match(openTest(await rendered(fixture), "greeting").textContent, /Answer content not included/);
});

test("UI labels root and Squad files friendly while keeping raw paths only in Source details", async () => {
  for (const path of [MINIMAL, SQUAD]) {
    const root = await rendered({}, path), details = byClass(root, "source-details")[0];
    assert.notEqual(details.open, true);
    assert.equal(byClass(details, "path-value")[0].tag, "code");
    assert.equal(byClass(details, "path-value")[0].textContent, path);
    assert.ok(details.textContent.includes(REV));
    assert.match(details.textContent, /Agent file path/); assertTrustedControls(root);
  }
});

test("UI can format a relative folder label without changing resource path acceptance", async () => {
  const view = await readView(stubSession(resource(MINIMAL)), MINIMAL);
  view.file.path = "agents/team/greeter.agent.md";
  const doc = fakeDoc(), root = doc.createElement("main");
  renderView(doc, root, view);
  assert.match(byClass(root, "agent-meta")[0].textContent, /^greeter.agent.md/);
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
  openTest(root, "odd");
  assertTrustedControls(root);
  assert.ok(byClass(root, "example-input")[0].textContent.includes(hostile));
  assert.deepEqual(writes, []);
  goBack(root);
  await byClass(root, "copy-path")[0].listeners.click[0]();
  assert.deepEqual(writes, [MINIMAL]);
});

test("UI keeps a long Purpose visible when the header summary cannot show it all", async () => {
  for (const length of [160, 161]) {
    const purpose = "A".repeat(length);
    const root = await rendered({ instructions: `# Greeter\n## Identity\n- **ID:** greeter\n- **Purpose:** ${purpose}\n\n## Accountable Responsibilities\nReply briefly.` });
    assert.equal(byClass(root, "instructions")[0].textContent.includes(purpose), length > 160);
    assert.ok(root.textContent.includes(purpose));
  }
});

test("UI hides only exact unique canonical Identity metadata and preserves original normalized text", async () => {
  for (const path of [MINIMAL, SQUAD]) {
    const root = await rendered({ instructions: "# Greeter\r\n\r\n## Identity\r\n- **ID:** greeter\r\n- **Purpose:** Greet.\r\n\r\n## Instructions\r\nStay brief.\r\n## Model\r\n- **Preferred:** fake" }, path);
    assert.equal(byClass(root, "purpose")[0].textContent, "Greet.");
    assert.equal(byClass(root, "instructions")[0].textContent, "InstructionsStay brief.");
    assert.match(root.textContent, /Read-only inspection/);
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
    assert.equal(byClass(root, "instructions").length, 1);
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
  assert.equal(items(root).length, 4);
  assert.equal(byClass(root, "instructions-details").length, 0);
  assertTrustedControls(root);
});

test("frontend and provider sources avoid unsafe sinks and mutation surfaces", async () => {
  const [controller, html, css, ext, view, editor] = await Promise.all(
    ["app.js", "index.html", "style.css", "extension.mjs", "view.mjs", "editor.mjs"].map((f) => readFile(here(f), "utf8")),
  );
  const app = controller + "\n" + editor;
  const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
  for (const bad of [/innerHTML/, /outerHTML/, /insertAdjacentHTML/, /document\.write/, /\beval\s*\(/, /new Function/, /dangerouslySet/, /marked|markdown/i, /\bXMLHttpRequest\b/, /WebSocket/, /localStorage/, /<form|<button|<input|<textarea/i]) {
    assert.doesNotMatch(strip(app), bad, String(bad));
  }
  assert.match(app, /textContent/);
  assert.doesNotMatch(strip(app), /readText|clipboard\s*(?:\.|\?\.)\s*read\b|execCommand|\bpermissions\b/);
  assert.match(strip(app), /method: "POST"/);
  assert.match(strip(app), /fetch\("actions"/);
  const operations = strip(view).match(/const allowed = \{([\s\S]*?)\n  \};/)[1];
  assert.deepEqual([...operations.matchAll(/(?:"([a-z-]+)"|\b([a-z]+))\s*:\s*\[/g)].map((m) => m[1] ?? m[2]).sort(), ["models", "reload", "run", "save-and-test", "save-examples", "suggest", "test"]);
  assert.doesNotMatch(strip(app), /createElement\(\s*["']button["']/);
  assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>|\son[a-z]+\s*=|style\s*=|<form|<button|<input|<textarea|http-equiv/i);
  assert.doesNotMatch(css, /@import|url\(/i);
  for (const src of [strip(ext), strip(view)]) {
    assert.doesNotMatch(src, /child_process|node:net|node:dgram|\bfetch\s*\(|writeFile|appendFile|unlink|rename|mkdir|\bexec\b|spawn|sendAndWait|\.sh\b|Access-Control-Allow|innerHTML/);
    assert.doesNotMatch(src, /listen\([^)]*0\.0\.0\.0|"::"/);
  }
  assert.match(strip(view), /127\.0\.0\.1/);
  assert.match(strip(ext), /createCanvas/);
  assert.match(strip(ext), /@github\/copilot-sdk\/extension/);
  assert.doesNotMatch(strip(ext), /actions\s*:/);
  assert.match(strip(ext), /name:\s*"agent-view"/);
});
async function viewFor(source, examples, latest = { status: "none" }, model = "fake") {
  const revision = "sha256:" + createHash("sha256").update(source).digest("hex");
  return readView(stubSession(resource(MINIMAL, {
    file: { path: MINIMAL, revision }, instructions: source, model, examples,
    latest: typeof latest === "function" ? latest(revision, examples) : latest,
    authoring: { source, editable: true },
  })), MINIMAL);
}
async function editableCanvas(body, eol, options = {}) {
  let original = await readFile(new URL("../../../internal/kmx/agentfile/testdata/greeter.agent.md", import.meta.url), "utf8");
  if (options.omitModel) original = original.replace(/## Model\r?\n- \*\*Preferred:\*\* fake\r?\n\r?\n/, "");
  if (eol) original = original.replace(/\r?\n/g, eol);
  const sections = instructionSections(original); sections[0].text = body;
  const source = replaceInstructions(original, sections);
  const revision = "sha256:" + createHash("sha256").update(source).digest("hex");
  let latest = { status: "none" };
  const examples = options.examples ?? [{ id: "greeting", input: "Ada", assertions: [{ id: "greeting-text", type: "contains", value: "Hello, Ada" }] }];
  if (options.latest) latest = options.latest(revision, examples);
  if (options.currentPass) {
    const result = savedFixture().latest.result;
    result.file = { path: MINIMAL, revision }; result.operation = "test";
    result.selection = { scope: "full", totalCases: 1, caseIds: ["greeting"] };
    result.examples = [{ id: "greeting", state: "complete", verdict: "pass", assertions: [{ id: "greeting-text", type: "contains", verdict: "pass" }] }];
    latest = { status: "current", result };
  }
  const view = await viewFor(source, examples, latest, options.omitModel ? "" : "fake");
  if (options.chatModel) view.chatModel = options.chatModel;
  const doc = fakeDoc(options.defaultView), root = doc.createElement("main"), calls = [];
  const app = renderView(doc, root, view, async (action) => {
    calls.push(action);
    return options.respond ? options.respond(action, calls) : options.response ?? { error: "refused", message: "Draft kept." };
  }, { fetchView: options.fetchView, fetchModels: options.fetchModels });
  const clickTest = async () => {
    byClass(root, "save-run")[0].listeners.click[0]();
    await Promise.resolve(); await Promise.resolve();
  };
  return { doc, root, calls, source, view, app, examples, sections: instructionSections(source), clickTest };
}

async function chooseModel(h, id) {
  await byClass(h.root, "model-picker")[0].listeners.click[0]();
  const option = byClass(h.root, "model-option").find(o => o.attrs["data-model"] === id);
  assert.ok(option, `model option ${id}`); click(option);
}

test("known Sol model IDs get the app's display spelling without changing unknown IDs", () => {
  assert.equal(modelLabel("copilot/gpt-6.1-sol"), "GPT-6.1 Sol");
  assert.equal(modelLabel("copilot/custom-sol"), "custom-sol");
});

test("a disabled model menu closes even when an option had focus", async () => {
  const doc = fakeDoc(); let disabled = false;
  const control = runtimeModelControl(doc, () => ({ authored: "fake", selected: "", disabled }), async () => ({ models: [] }), () => {});
  await control.trigger.listeners.click[0]();
  byClass(control.node, "model-option")[0].focus(); disabled = true; control.update();
  assert.equal(control.trigger.attrs["aria-expanded"], "false");
  assert.equal(byClass(control.node, "model-menu")[0].hidden, true);
});

test("a valid host reply is usable while a newer metadata read is still pending", async () => {
  const session = stubSession(resource(MINIMAL)), replies = [];
  session.rpc.model = { getCurrent: () => new Promise(resolve => replies.push(resolve)) };
  const first = readView(session, MINIMAL), second = readView(session, MINIMAL);
  replies[0]({ modelId: "chat-a" });
  try { assert.equal((await first).chatModel, "chat-a", "do not substitute the old default for a known model"); }
  finally { replies[1]({ modelId: "chat-b" }); }
  assert.equal((await second).chatModel, "chat-b");
});

test("grouped model menu deduplicates raw IDs, marks chat and restores trigger focus", async () => {
  let catalogs = 0;
  const h = await editableCanvas("Say hello.", undefined, { omitModel: true, chatModel: "gpt-5.6-terra", fetchModels: async () => {
    catalogs++; return { models: ["gpt-5.6-terra", "claude-sonnet-5.5", "gpt-5.6-terra", "unknown/raw-id"] };
  } });
  const trigger = byClass(h.root, "model-picker")[0];
  assert.equal(catalogs, 0); assert.equal(trigger.tag, "button");
  await trigger.listeners.click[0]();
  const choices = byClass(h.root, "model-option");
  assert.equal(choices.length, 3); assert.deepEqual(byClass(h.root, "model-group").map(e=>e.textContent), ["OpenAI", "Claude", "Other models"]);
  assert.match(choices[0].textContent, /GPT-5.6 Terra.*Current chat model/);
  assert.equal(choices[2].textContent, "unknown/raw-id");
  const key = (node,key) => node.listeners.keydown[0]({key,preventDefault(){}});
  key(trigger,"ArrowDown"); assert.equal(h.doc.activeElement,choices[0]);
  key(choices[0],"ArrowUp"); assert.equal(h.doc.activeElement,byClass(h.root,"model-default")[0]);
  key(h.doc.activeElement,"ArrowUp"); assert.equal(h.doc.activeElement,choices[2]);
  key(choices[2],"Escape"); assert.equal(h.doc.activeElement,trigger); assert.equal(trigger.attrs["aria-expanded"],"false");
  await chooseModel(h,"claude-sonnet-5.5");
  assert.equal(h.doc.activeElement, trigger); assert.equal(trigger.textContent,"Claude Sonnet 5.5 · this canvas");
  await chooseModel(h,"gpt-5.6-terra"); assert.equal(trigger.textContent,"GPT-5.6 Terra · this canvas");
  await trigger.listeners.click[0](); click(byClass(h.root,"model-default")[0]);
  assert.equal(trigger.textContent,"GPT-5.6 Terra · default");
  assert.equal(catalogs,1); assert.equal(h.calls.length,0); await h.clickTest(); assert.equal(Object.hasOwn(h.calls[0],"model"),false);
});

test("choosing the current default model pins its exact ID across chat changes", async () => {
  let changed;
  const h = await editableCanvas("Say hello.", undefined, { omitModel: true, chatModel: "fake", currentPass: true,
    fetchModels: async () => ({ models: [] }), respond: async () => ({ view: changed }) });
  await chooseModel(h, "fake");
  assert.equal(byClass(h.root, "model-picker")[0].textContent, "fake · this canvas");
  changed = structuredClone(h.view); changed.chatModel = "chat-b"; h.app.observe(changed);
  assert.deepEqual(c18Status(h.root), ["pass"]);
  await h.clickTest(); assert.equal(h.calls[0].model, "fake");
  await byClass(h.root, "model-picker")[0].listeners.click[0]();
  const follow = byClass(h.root, "model-default")[0]; assert.ok(follow); click(follow);
  assert.equal(byClass(h.root, "model-picker")[0].textContent, "chat-b · default");
  assert.deepEqual(c18Status(h.root), ["old-pass"]);
});

test("the following action keeps keyboard focus through a late catalog response", async () => {
  const doc = fakeDoc(); let release;
  const control = runtimeModelControl(doc, () => ({ authored: "fake", selected: "" }), () => new Promise(resolve => { release = resolve; }), () => {});
  const opened = control.trigger.listeners.click[0](); byClass(control.node, "model-default")[0].focus();
  release({ models: ["other-model"] }); await opened;
  assert.equal(doc.activeElement, byClass(control.node, "model-default")[0]);
  assert.equal(control.trigger.attrs["aria-expanded"], "true");
});

test("chat metadata changes preserve prompt and test drafts and never revive A-B-A results", async () => {
  const h = await editableCanvas("Say hello.", undefined, { omitModel: true, chatModel: "fake", currentPass:true });
  const prompt = c18Prompt(h.root); prompt.textContent = "Draft prompt."; prompt.listeners.input[0]();
  openTest(h.root,"greeting"); const input = byClass(h.root,"example-input")[0]; input.value="Draft input"; input.listeners.input[0](); goBack(h.root);
  const changed = structuredClone(h.view); changed.chatModel="different-chat";
  assert.equal(h.app.observe(changed),true); assert.equal(c18Prompt(h.root).textContent,"Draft prompt.");
  assert.deepEqual(c18Status(h.root),["old-pass"]);
  h.app.observe(h.view); assert.deepEqual(c18Status(h.root),["old-pass"]);
  openTest(h.root,"greeting"); assert.equal(byClass(h.root,"example-input")[0].value,"Draft input");
});

test("omitted Model shows the default while remaining editable without source injection", async () => {
  const h = await editableCanvas("Say hello.", undefined, { omitModel: true });
  const picker = byClass(h.root, "model-picker")[0]; assert.ok(picker);
  assert.match(picker.textContent, /GPT-5\.6 Terra · default/);
  assert.doesNotMatch(h.source, /## Model|gpt-5/); await h.clickTest();
  assert.equal(h.calls[0].operation, "test"); assert.equal(Object.hasOwn(h.calls[0], "model"), false);
  assert.equal(Object.hasOwn(h.calls[0], "sections"), false); assert.equal(h.view.model, "");
});
test("catalog failure does not block prompt edits or fake/offline tests", async () => {
  const h = await editableCanvas("Say hello.", undefined, { fetchModels: async () => ({ error: "models-unavailable" }) });
  const picker = byClass(h.root, "model-picker")[0]; await picker.listeners.click[0]();
  assert.equal(picker.disabled, false);
  assert.equal(c18Text(h.root, "model-status"), "Choices unavailable; model selection unchanged.");
  assert.equal(picker.attrs.title, "fake · from the agent file");
  const prompt = c18Prompt(h.root); prompt.textContent = "Keep it short."; prompt.listeners.input[0]();
  await h.clickTest(); assert.equal(h.calls[0].operation, "save-and-test");
  assert.equal(Object.hasOwn(h.calls[0], "model"), false);
});

test("runtime model picker stays out of drafts and is sent through Enter", async () => {
  let catalogs = 0;
  const h = await editableCanvas("Say hello.", undefined, { fetchModels: async () => { catalogs++; return { defaultModel: "gpt-5.6-terra", models: ["choice-a", "choice-b"] }; } });
  const picker = byClass(h.root, "model-picker")[0];
  assert.ok(picker, "runtime model button is missing"); assert.equal(picker.tag, "button");
  assert.match(picker.attrs["aria-label"], /^Runtime model:/); assert.equal(catalogs, 0);
  await picker.listeners.click[0]();
  assert.equal(catalogs, 1); assert.deepEqual(byClass(h.root, "model-option").map(o => o.attrs["data-model"]), ["fake", "choice-a", "choice-b"]);
  click(byClass(h.root, "model-option").find(o => o.attrs["data-model"] === "choice-b"));
  assert.equal(c18Text(h.root, "prompt-status"), "Saved · Enter runs 1 test");
  assert.equal(h.calls.length, 0); await h.clickTest();
  assert.equal(h.calls[0].model, "choice-b"); assert.equal(h.calls[0].operation, "test");
  assert.equal(Object.hasOwn(h.calls[0], "sections"), false); assert.equal(h.view.model, "fake");
});

test("only a new result from the picked model restores current pass styling", async () => {
  let response;
  const h = await editableCanvas("Say hello.", undefined, { currentPass: true, fetchModels: async () => ({ models: ["choice-b"], defaultModel: "gpt-5.6-terra" }), respond: async () => response });
  await chooseModel(h, "choice-b"); assert.deepEqual(c18Status(h.root), ["old-pass"]);
  const view = structuredClone(h.view); view.latest.result.modelRoute = "copilot/choice-b"; response = { view };
  await h.clickTest(); assert.deepEqual(c18Status(h.root), ["pass"]);
  h.app.observe(h.view); assert.deepEqual(c18Status(h.root), ["old-pass"], "another model's result must remain historical");
});

test("A-B-A model changes fence old observations even when revision and model match again", async () => {
  const h = await editableCanvas("Say hello.", undefined, { currentPass: true, fetchModels: async () => ({ models: ["fake", "choice-b"], defaultModel: "gpt-5.6-terra" }) });
  assert.deepEqual(c18Status(h.root), ["pass"]);
  const started = h.app.observation();
  await chooseModel(h, "choice-b"); assert.deepEqual(c18Status(h.root), ["old-pass"]);
  await chooseModel(h, "fake");
  assert.equal(h.app.observe(h.view, started), false); h.app.observe(h.view);
  assert.deepEqual(c18Status(h.root), ["old-pass"], "a late A observation must not make the old result current");
});

for (const eol of ["\n", "\r\n"]) test(`editable prose renders inline code and strong without a raw-mode focus swap; no-op tests do not save (${eol === "\n" ? "LF" : "CRLF"})`, async () => {
  const body = "Use `users` and **benefits**.\n\nKeep original lines.\n```text\n`literal` **fence**\n```\nLast line.";
  const h = await editableCanvas(body, eol);
  const editors = [c18Prompt(h.root), ...byClass(byClass(h.root, "source-details")[0], "instruction-body")];
  assert.deepEqual(all(editors[0]).filter((e) => e.tag === "code").map((e) => e.textContent), ["users"]);
  assert.deepEqual(all(editors[0]).filter((e) => e.tag === "strong").map((e) => e.textContent), ["benefits"]);
  assert.equal(editors[0].attrs.contenteditable, "plaintext-only");
  assert.equal(editors[0].attrs.spellcheck, "false");
  assert.equal(editors[0].listeners.focus, undefined);
  assert.equal(c18Text(h.root, "prompt-status"), "Saved · Enter runs 1 test");
  await h.clickTest();
  assert.equal(h.calls[0].operation, "test"); assert.equal(h.calls[0].showContent, true);
  assert.equal(Object.hasOwn(h.calls[0], "sections"), false);
  assert.equal(Object.hasOwn(h.calls[0], "showFailedContent"), false);
  const line = (text) => { const node = h.doc.createElement("div"); node.append(text); return node; };
  editors[2].append(line("Another boundary.")); editors[2].listeners.input[0]();
  await h.clickTest();
  assert.equal(h.calls[1].operation, "save-and-test");
  assert.deepEqual(h.calls[1].sections, [{ key: h.sections[2].key, text: h.sections[2].text + "\nAnother boundary." }], "only changed sections are sent");
  editors[0].append(line("Added.")); editors[0].listeners.input[0]();
  await h.clickTest();
  const action = h.calls[2];
  assert.equal(action.sections.find((s) => s.key === h.sections[0].key).text, body + "\nAdded.", "untouched inline delimiters, fences and LFs survive serialization");
  const saved = replaceInstructions(h.source, action.sections);
  for (const heading of ["Identity", "Model", "Examples"]) {
    const section = (source) => {
      const start = source.indexOf("## " + heading);
      assert.notEqual(start, -1, `${heading} is present`);
      const end = source.indexOf("\n## ", start + 1);
      return source.slice(start, end < 0 ? source.length : end);
    };
    assert.equal(section(saved), section(h.source), heading + " is protected");
  }
});

test("editing prose retains formatted source and literal hostile HTML cannot create controls", async () => {
  const body = "Explain `users` and **value**.\n\n```\n`quoted`\n```";
  const h = await editableCanvas(body), editor = c18Prompt(h.root);
  const line = h.doc.createElement("div"); line.append('<button onclick="run()">Not a command</button>');
  editor.append(line); editor.listeners.input[0](); await h.clickTest();
  assert.equal(h.calls[0].sections[0].text, body + '\n<button onclick="run()">Not a command</button>');
  assert.equal(all(editor).filter((e) => ["button", "input", "script", "a"].includes(e.tag)).length, 0);
});

test("browser BR and paragraph containers serialize newlines; unknown formatting fails closed", async () => {
  const h = await editableCanvas("Original."), editor = c18Prompt(h.root);
  const p = h.doc.createElement("p"); p.append("Second.");
  const blank = h.doc.createElement("div"); blank.append(h.doc.createElement("br"));
  const last = h.doc.createElement("div"); last.append("Third.");
  editor.replaceChildren("First.", h.doc.createElement("br"), p, blank, last);
  editor.listeners.input[0](); await h.clickTest();
  assert.equal(h.calls[0].sections[0].text, "First.\nSecond.\n\nThird.");
  const unsupported = h.doc.createElement("span"); unsupported.append("Do not drop this.");
  editor.append(unsupported); editor.listeners.input[0](); await h.clickTest();
  assert.equal(h.calls.length, 1);
  assert.match(byClass(h.root, "action-message")[0].textContent, /Unsupported editor formatting/);
  assert.ok(editor.textContent.includes("Do not drop this."));
});

test("terminal newline owns only its caret BR, preserves repeat insertion and foreign BRs", async () => {
  const h = await editableCanvas("Old."), editor = c18Prompt(h.root), line = editor.childNodes[0];
  const range = { commonAncestorContainer: line, startContainer: line, startOffset: line.childNodes.length, collapsed: true,
    deleteContents() {}, collapse() {},
    setStart(node, offset) { this.startContainer = node; this.startOffset = offset; },
    setStartAfter(node) { this.setStart(node.parentNode, node.parentNode.childNodes.indexOf(node) + 1); },
    insertNode(node) { const parent = this.startContainer; node.parentNode = parent; parent.children.splice(this.startOffset, 0, node); } };
  h.doc.getSelection = () => ({ rangeCount: 1, getRangeAt: () => range, removeAllRanges() {}, addRange() {} });
  insertPlainText(h.doc, editor, "\n");
  const placeholder = line.childNodes.at(-1);
  assert.equal(placeholder.tagName, "BR");
  assert.equal(editorValue(editor), "Old.\n");
  assert.equal(range.startOffset, line.childNodes.indexOf(placeholder), "caret precedes the rendered placeholder");
  insertPlainText(h.doc, editor, "\n");
  insertPlainText(h.doc, editor, "Next.");
  assert.equal(editorValue(editor), "Old.\n\nNext.");
  assert.equal(line.childNodes.filter(node => node.tagName === "BR").length, 1);
  line.append(h.doc.createElement("br"));
  assert.equal(editorValue(editor), "Old.\n\nNext.\n", "foreign BR remains authored content");
  line.append(h.doc.createElement("strong"));
  assert.throws(() => editorValue(editor), /Unsupported editor formatting/);
});

test("owned inline terminal LF keeps its caret without admitting foreign inline BRs", async () => {
  const h = await editableCanvas("`Old.`"), editor = c18Prompt(h.root), code = editor.querySelector("code");
  code.parentNode = editor.childNodes[0];
  const range = { commonAncestorContainer: code, startContainer: code, startOffset: code.childNodes.length, collapsed: true,
    deleteContents() {}, collapse() {},
    setStart(node, offset) { this.startContainer = node; this.startOffset = offset; },
    setStartAfter(node) { this.setStart(node.parentNode, node.parentNode.childNodes.indexOf(node) + 1); },
    insertNode(node) { const parent = this.startContainer; node.parentNode = parent; parent.children.splice(this.startOffset, 0, node); } };
  h.doc.getSelection = () => ({ rangeCount: 1, getRangeAt: () => range, removeAllRanges() {}, addRange() {} });
  insertPlainText(h.doc, editor, "\n"); insertPlainText(h.doc, editor, "Next.");
  assert.equal(editorValue(editor), "`Old.\nNext.`");
  code.append(h.doc.createElement("br"));
  assert.throws(() => editorValue(editor), /Unsupported inline formatting/);
});

test("mid-line inline LF does not add a second visual break before following text", async () => {
  const h = await editableCanvas("`Old.` tail"), editor = c18Prompt(h.root), code = editor.querySelector("code");
  code.parentNode = editor.childNodes[0];
  const range = { commonAncestorContainer: code, startContainer: code, startOffset: code.childNodes.length, collapsed: true,
    deleteContents() {}, collapse() {},
    setStart(node, offset) { this.startContainer = node; this.startOffset = offset; },
    setStartAfter(node) { this.setStart(node.parentNode, node.parentNode.childNodes.indexOf(node) + 1); },
    insertNode(node) { const parent = this.startContainer; node.parentNode = parent; parent.children.splice(this.startOffset, 0, node); } };
  h.doc.getSelection = () => ({ rangeCount: 1, getRangeAt: () => range, removeAllRanges() {}, addRange() {} });
  insertPlainText(h.doc, editor, "\n");
  assert.equal(editorValue(editor), "`Old.\n` tail");
  assert.equal(code.childNodes.some(node => node.tagName === "BR"), false);
});

test("noncancelable native line breaks remain browser-owned instead of inserting twice", async () => {
  const h = await editableCanvas("First sentence."), editor = c18Prompt(h.root);
  editor.listeners.beforeinput[0]({ inputType: "insertLineBreak", cancelable: false, preventDefault() { assert.fail("noncancelable input must remain browser-owned"); } });
  assert.equal(editorValue(editor), "First sentence.");
  assert.equal(h.calls.length, 0);
});

test("Shift+Enter and native insertLineBreak insert one literal LF; composition and Enter stay separate", async () => {
  const h = await editableCanvas("First sentence."), editor = c18Prompt(h.root), line = editor.childNodes[0];
  const range = { commonAncestorContainer:line, startContainer:line, startOffset:line.childNodes.length, collapsed:true,
    deleteContents(){},
    insertNode(node){node.parentNode=this.startContainer;this.startContainer.children.splice(this.startOffset,0,node)},
    setStartAfter(node){this.setStart(node.parentNode,node.parentNode.childNodes.indexOf(node)+1)},
    collapse(){}, setStart(node,offset){this.startContainer=node;this.startOffset=offset} };
  h.doc.getSelection = () => ({rangeCount:1,getRangeAt:()=>range,removeAllRanges(){},addRange(){}});
  let prevented=0;
  editor.listeners.keydown[0]({key:"Enter",shiftKey:true,preventDefault(){prevented++}});
  assert.equal(prevented,1); assert.equal(h.calls.length,0);
  assert.equal(editorValue(editor),"First sentence.\n");
  editor.listeners.beforeinput[0]({inputType:"insertLineBreak",cancelable:true,preventDefault(){prevented++}});
  assert.equal(editorValue(editor),"First sentence.\n\n");
  editor.listeners.beforeinput[0]({inputType:"insertLineBreak",isComposing:true,preventDefault(){throw Error("composition intercepted")}});
  editor.listeners.keydown[0]({key:"Enter",isComposing:true,preventDefault(){throw Error("composition intercepted")}});
  assert.equal(h.calls.length,0);
  editor.listeners.paste[0]({clipboardData:{getData:type=>{assert.equal(type,"text/plain");return "<plain>\ntext"}},preventDefault(){}});
  assert.equal(editorValue(editor),"First sentence.\n\n<plain>\ntext");
  editor.listeners.keydown[0]({key:"Enter",preventDefault(){prevented++}});
  editor.listeners.keydown[0]({key:"Enter",preventDefault(){prevented++}}); await tick();
  assert.equal(h.calls.length,1); assert.equal(h.calls[0].sections[0].text,"First sentence.\n\n<plain>\ntext");
});

test("successful main runs keep the saved Enter instruction; observed time belongs only to Tests", async () => {
  let response;
  const h = await editableCanvas("Reply briefly.",undefined,{currentPass:true,respond:async()=>response});
  response={view:h.view}; await h.clickTest();
  assert.equal(c18Text(h.root,"prompt-status"),"Saved · Enter runs 1 test");
  assert.match(c18Text(h.root,"tests-meta"),/just now/);
});

test("canvas requests passing answers explicitly without a hidden setting or failure-disclosure key", async () => {
  const h = await editableCanvas("Reply briefly.");
  assert.equal(byClass(h.root, "include-answers").length, 0);
  assert.doesNotMatch(h.root.textContent, /Include last answers|Include passing answers/);
  await h.clickTest();
  assert.equal(h.calls[0].showContent, true);
  assert.equal(Object.hasOwn(h.calls[0], "showFailedContent"), false);
});

for (const operation of ["test", "run"]) test(`${operation} execution failure invalidates the previous passing display`, async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { currentPass: true, response: { error: "action-failed", message: "Execution did not complete." } });
  assert.equal(summaryOf(h.root), "1 passing");
  if (operation === "test") await h.clickTest();
  else {
    all(h.root).find((node) => node.tag === "button" && node.textContent === "Run").listeners.click[0]();
    await Promise.resolve(); await Promise.resolve();
  }
  assert.equal(h.calls[0].operation, operation);
  assert.equal(summaryOf(h.root), "Result unconfirmed");
  assert.equal(byClass(h.root, "pass").length, 0);
  assert.doesNotMatch(byClass(h.root, "tests-section")[0].textContent, /Passed/);
  assert.equal(byClass(byClass(h.root, "message-line")[0], "tb")[0].hidden, false, "only a deliberate reload clears the lock");
});

test("saved preview with an unknown test outcome keeps drafts and requires reload", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { response: { error: "outcome-unknown", saved: true } });
  const prompt = c18Prompt(h.root); prompt.textContent = "Draft prompt."; prompt.listeners.input[0]();
  await h.clickTest();
  assert.equal(h.calls[0].operation, "save-and-test");
  assert.equal(summaryOf(h.root), "Result unconfirmed");
  assert.equal(byClass(h.root, "action-message")[0].textContent, "Saved; test result unconfirmed. Your draft is still here. Reload before continuing.");
  assert.equal(c18Text(h.root, "prompt-status"), "Saved · test result unconfirmed");
  assert.equal(c18Prompt(h.root).textContent, "Draft prompt.");
  assert.equal(reloadButton(h.root).hidden, false);
  assert.equal(byClass(h.root, "save-run")[0].disabled, true);
  await h.clickTest(); assert.equal(h.calls.length, 1, "an unknown outcome must not resend inference");
});

test("pre-execution permission refusal retains the previous valid observation", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { currentPass: true, response: { error: "permission_required", message: "Not permitted before execution." } });
  await h.clickTest();
  assert.equal(summaryOf(h.root), "1 passing");
  assert.equal(byClass(h.root, "pass").length, 1);
  assert.match(byClass(h.root, "run-alert")[0].textContent, /The run didn't start: Not permitted before execution\./);
  assert.equal(byClass(h.root, "run-alert")[0].attrs.role, "alert");
  assert.equal(byClass(byClass(h.root, "message-line")[0], "tb")[0].hidden, true, "a refusal before execution is not an unknown outcome");
});

test("readonly judge uses its actual rubric label and retains safe inline formatting in details", async () => {
  const f = savedFixture(), r = f.latest.result;
  f.examples[0].assertions = [
    { id: "literal", type: "contains", value: "`users`" },
    { id: "says-why-it-matters-to-users", type: "llmJudge", rubric: "The notes say why it matters to `users`.", minScore: 70 },
  ];
  r.fullPass = false; r.verdict = "fail"; r.examples[0].verdict = "fail";
  r.examples[0].assertions = [{ id: "literal", type: "contains", verdict: "fail" }, { id: "says-why-it-matters-to-users", type: "llmJudge", verdict: "pass" }];
  const root = openTest(await rendered(f), "greeting"), labels = byClass(root, "check-label").map((e) => e.textContent);
  assert.equal(labels[0], "Contains “`users`”");
  assert.equal(labels[1], "The notes say why it matters to `users`. · judge");
  assert.deepEqual(checkLabels(root), ["Failed", "Passed"]);
  const details = byClass(root, "check-details")[1];
  assert.match(details.textContent, /rubric: The notes say why it matters to users\./);
  assert.equal(all(details).find((e) => e.tag === "code").textContent, "users");
  assert.match(details.textContent, /minScore: 70/);
});

// --- C18 milestone: main view and edited state ------------------------------
const c18Examples = (ids) => ids.map((id) => ({ id, input: `Name ${id}`, assertions: [{ id: "says-hello", type: "contains", value: `Hello, ${id}` }] }));
function c18Latest(outcomes, { scope = "full", selected } = {}) {
  return (revision, examples) => {
    const ids = selected ?? examples.map((e) => e.id);
    const observed = examples.filter((e) => ids.includes(e.id)).map((e) => outcomes[e.id] === "error"
      ? { id: e.id, state: "error", verdict: "unknown", reasonCode: "timeout", reason: "no answer in time", assertions: [] }
      : { id: e.id, state: "complete", verdict: outcomes[e.id], assertions: [{ id: "says-hello", type: "contains", verdict: outcomes[e.id], reason: "checked" }] });
    const fail = observed.some((o) => o.verdict === "fail"), complete = observed.every((o) => o.state === "complete");
    return { status: "current", result: result({
      file: { path: MINIMAL, revision }, selection: { scope, totalCases: examples.length, caseIds: ids }, examples: observed,
      state: complete ? "complete" : "incomplete", verdict: fail ? "fail" : complete ? "pass" : "unknown",
      fullPass: !fail && complete && scope === "full" && observed.length === examples.length,
    }) };
  };
}
const c18Items = (root) => byClass(root, "test-item");
const c18Status = (root) => c18Items(root).map((i) => i.attrs["data-status"]);
const c18Shape = (item) => byClass(item, "verdict-word")[0].textContent;
const c18Text = (root, name) => byClass(root, name)[0].textContent;
const c18Prompt = (root) => byClass(byClass(root, "prompt-box")[0], "instruction-body")[0];
const c18Edit = (root) => { const editor = c18Prompt(root); editor.append(" More."); editor.listeners.input[0](); };
const tick = async () => { await Promise.resolve(); await Promise.resolve(); };

test("boards A/G show one name, separate file and model, Prompt and Tests, and preserve source access", async () => {
  const h = await editableCanvas("Reply briefly.");
  for (const name of ["breadcrumb", "wordmark", "eyebrow", "status-pill", "model-pill", "toolbar"]) assert.equal(byClass(h.root, name).length, 0, name);
  assert.equal(byClass(h.root,"agent-eyebrow")[0].textContent,"Your agent");
  assert.equal(all(h.root).find((e) => e.tag === "h1").textContent, "Greeter");
  assert.equal(c18Text(h.root, "agent-meta"), "greeter.agent.md");
  assert.equal(c18Text(h.root,"model-picker"),"fake · from the agent file");
  assert.equal(c18Text(h.root, "purpose"), "Return a short greeting for the supplied name.");
  assert.deepEqual(all(h.root).filter((e) => e.tag === "h2").map((e) => e.textContent), ["Prompt", "Tests"]);
  const prompt = byClass(h.root, "prompt-box")[0], source = byClass(h.root, "source-details")[0];
  assert.equal(byClass(prompt, "instruction-body").length, 1);
  assert.equal(c18Prompt(h.root).textContent, "Reply briefly.");
  assert.equal(byClass(source, "instruction-body").length, 2, "additional canonical bodies stay available, collapsed");
  assert.equal(byClass(source, "copy-path").length, 1);
  assert.equal(byClass(source, "include-answers").length, 0);
  assert.equal(c18Text(prompt, "prompt-status"), "Saved · Enter runs 1 test");
  const enter = byClass(prompt, "save-run")[0];
  assert.equal(enter.attrs["aria-label"], "Save and run all tests");
  assert.equal(enter.children[0].namespaceURI, "http://www.w3.org/2000/svg");
  const actions = all(byClass(h.root, "tests-section")[0]).filter((e) => e.tag === "button").map((e) => e.textContent);
  assert.ok(actions.includes("Suggest tests") && actions.includes("Add test"), actions.join());
  c18Edit(h.root);
  assert.equal(c18Text(prompt, "prompt-status"), "Edited · Enter saves and runs 1 test");
  assert.equal(h.calls.length, 0, "typing never saves or runs");
});

test("C18 milestone rows keep authored order and a local edit keeps known shapes and counts in grey", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["zeta", "alpha", "mid"]), latest: c18Latest({ zeta: "pass", alpha: "fail", mid: "pass" }) });
  assert.deepEqual(c18Items(h.root).map((i) => i.attrs["data-test-id"]), ["zeta", "alpha", "mid"]);
  assert.deepEqual(c18Items(h.root).map((i) => c18Text(i, "test-name")), ["Zeta", "Alpha", "Mid"]);
  assert.deepEqual(c18Status(h.root), ["pass", "fail", "pass"]);
  assert.equal(c18Text(c18Items(h.root)[1], "test-note"), 'Missing "Hello, alpha"');
  assert.equal(c18Text(h.root, "tests-summary"), "1 failing, 2 passing");
  const shapes = c18Items(h.root).map(c18Shape);
  assert.notEqual(shapes[0], shapes[1]);
  c18Edit(h.root);
  assert.deepEqual(c18Items(h.root).map((i) => i.attrs["data-test-id"]), ["zeta", "alpha", "mid"]);
  assert.deepEqual(c18Status(h.root), ["old-pass", "old-fail", "old-pass"]);
  assert.deepEqual(c18Items(h.root).map(c18Shape), shapes, "verdict words survive the edit");
  assert.equal(c18Text(c18Items(h.root)[1], "test-note"), 'Missing "Hello, alpha"');
  assert.equal(c18Text(h.root, "tests-summary"), "Last run, before your edit: 1 failing, 2 passing");
  assert.equal(c18Text(h.root, "prompt-status"), "Edited · Enter saves and runs 3 tests");
});

test("C18 milestone unknown, unselected and stale cases stay neutral and never invent verdicts", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b", "c"]), latest: c18Latest({ a: "pass", b: "error" }, { scope: "selected", selected: ["a", "b"] }) });
  assert.deepEqual(c18Status(h.root), ["pass", "error", "none"]);
  assert.equal(c18Text(c18Items(h.root)[1], "test-note"), "Didn't finish: no answer in time");
  assert.equal(c18Text(c18Items(h.root)[2], "test-note"), "Not in this run");
  assert.equal(c18Text(h.root, "tests-summary"), "1 passing, 1 didn't finish, 1 not run");
  c18Edit(h.root);
  assert.deepEqual(c18Status(h.root), ["old-pass", "old-error", "none"]);
  assert.equal(c18Text(h.root, "tests-summary"), "Last run, before your edit: 1 passing, 1 didn't finish, 1 not run");
  const ambiguous = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: (revision, examples) => {
    const latest = c18Latest({ a: "pass" })(revision, examples); latest.result.examples[0].assertions = [];
    return latest;
  } });
  assert.deepEqual(c18Status(ambiguous.root), ["none"], "complete but uncovered stays a neutral dash, not amber");
  assert.equal(c18Text(c18Items(ambiguous.root)[0], "test-note"), "Incomplete result");
  const stale = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: () => ({ status: "stale" }) });
  assert.deepEqual(c18Status(stale.root), ["none", "none"]);
  assert.doesNotMatch(c18Text(stale.root, "tests-summary"), /failing|passing/);
  c18Edit(stale.root);
  assert.deepEqual(c18Status(stale.root), ["none", "none"]);
  assert.doesNotMatch(c18Text(stale.root, "tests-summary"), /failing|passing|Last run/);
});

test("C18 milestone All and Not passing filter hides rows without reordering, and appears only when the list overflows", async () => {
  const outcomes = { a: "pass", b: "fail", c: "pass", d: "error", e: "pass", f: "pass" }, six = c18Examples(["a", "b", "c", "d", "e", "f"]);
  const h = await editableCanvas("Reply briefly.", undefined, { examples: six, latest: c18Latest(outcomes), defaultView: { innerHeight: 200 } });
  const group = (root) => byClass(root, "filter-group")[0];
  assert.equal(group(h.root).hidden, false, "six rows overflow a 200px panel");
  const roomy = await editableCanvas("Reply briefly.", undefined, { examples: six, latest: c18Latest(outcomes), defaultView: { innerHeight: 2000 } });
  assert.equal(group(roomy.root).hidden, true, "no magic count: a list that fits needs no filter");
  const [allButton, notPassing] = byClass(h.root, "filter-button");
  assert.equal(allButton.textContent, "All 6"); assert.equal(notPassing.textContent, "Not passing 2");
  assert.equal(allButton.attrs["aria-pressed"], "true");
  notPassing.listeners.click[0]();
  assert.deepEqual(c18Items(h.root).map((i) => i.attrs["data-test-id"]), ["a", "b", "c", "d", "e", "f"]);
  assert.deepEqual(c18Items(h.root).map((i) => i.hidden === true), [true, false, true, false, true, true]);
  assert.equal(byClass(h.root, "filter-button")[1].attrs["aria-pressed"], "true");
  assert.equal(group(h.root).hidden, false, "an active filter stays reachable even when the rest fits");
  assert.match(c18Text(h.root, "filter-note"), /^4 passing tests hidden/);
  c18Edit(h.root);
  assert.deepEqual(c18Items(h.root).map((i) => i.hidden === true), [true, false, true, false, true, true], "the filter survives a local edit");
  byClass(h.root, "filter-button")[0].listeners.click[0]();
  assert.deepEqual(c18Items(h.root).map((i) => i.hidden === true), [false, false, false, false, false, false]);
  const short = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b", "c"]), latest: c18Latest({ a: "pass", b: "fail", c: "pass" }), defaultView: { innerHeight: 600 } });
  assert.equal(group(short.root).hidden, true, "three tests match the board without a filter");
});

test("C18 milestone Enter on main always runs every test without a caseId", async () => {
  const h = await editableCanvas("Reply briefly.");
  const key = (over) => c18Prompt(h.root).listeners.keydown[0]({ key: "Enter", preventDefault() {}, ...over });
  key({ isComposing: true }); await tick();
  assert.equal(h.calls.length, 0);
  key(); await tick();
  assert.equal(h.calls[0].operation, "test");
  assert.equal(Object.hasOwn(h.calls[0], "caseId"), false);
  assert.equal(Object.hasOwn(h.calls[0], "sections"), false);
  c18Edit(h.root); key(); await tick();
  assert.equal(h.calls[1].operation, "save-and-test");
  assert.equal(Object.hasOwn(h.calls[1], "caseId"), false);
  byClass(h.root, "save-run")[0].listeners.click[0](); await tick();
  assert.equal(h.calls[2].operation, "save-and-test");
  assert.equal(Object.hasOwn(h.calls[2], "caseId"), false);
});


// --- Final UI regressions: drafts, scope, zero tests, outside changes ---------
const btn = (root, text) => all(root).find((e) => e.tag === "button" && e.textContent === text);
const click = (node) => node.listeners.click[0]();
const edited = (node, value) => { node.value = value; node.listeners[node.tag === "select" ? "change" : "input"][0](); };
const enterOn = (node) => node.listeners.keydown[0]({ key: "Enter", preventDefault() {} });
const promptEnter = (root) => enterOn(c18Prompt(root));
const reloadButton = (root) => byClass(byClass(root, "message-line")[0], "tb")[0];

test("drafts survive A → B → prompt → A navigation without any RPC and never lock other editing", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b", "c"]) });
  openTest(h.root, "a"); edited(byClass(h.root, "example-input")[0], "Name a, edited"); goBack(h.root);
  openTest(h.root, "b"); edited(byClass(h.root, "check-value")[0], "Hello, b!"); goBack(h.root);
  assert.equal(c18Prompt(h.root).attrs.contenteditable, "plaintext-only", "other unsaved tests never lock the prompt");
  c18Edit(h.root);
  assert.deepEqual(items(h.root).map((i) => noteOf(i).startsWith("Unsaved")), [true, true, false]);
  assert.ok(items(h.root).every((i) => byClass(i, "test-row")[0].disabled === false));
  openTest(h.root, "a");
  assert.equal(byClass(h.root, "example-input")[0].value, "Name a, edited");
  assert.equal(byClass(h.root, "unsaved-tag")[0].hidden, false);
  assert.equal(byClass(h.root, "example-input")[0].disabled, false);
  goBack(h.root);
  assert.match(c18Prompt(h.root).textContent, /More\.$/, "the prompt draft survives re-rendering");
  openTest(h.root, "b");
  assert.equal(byClass(h.root, "check-value")[0].value, "Hello, b!");
  // Cancel discards only the active test's draft.
  click(all(h.root).find(e => e.tag === "button" && e.attrs["aria-label"] === "Cancel")); goBack(h.root);
  assert.deepEqual(items(h.root).map((i) => noteOf(i).startsWith("Unsaved")), [true, false, false]);
  assert.match(c18Prompt(h.root).textContent, /More\.$/);
  assert.equal(h.calls.length, 0, "navigation and drafting never call the bridge");
});

test("a test's Enter saves only it with its caseId and clears only what it sent; main Enter then saves the rest and runs all", async () => {
  const ex = c18Examples(["a", "b", "c"]), mainKey = "Accountable Responsibilities";
  let reply;
  const h = await editableCanvas("Reply briefly.", undefined, { examples: ex, respond: () => reply });
  openTest(h.root, "a"); edited(byClass(h.root, "example-input")[0], "Name a, edited"); goBack(h.root);
  openTest(h.root, "b"); edited(byClass(h.root, "check-value")[0], "Hello, b!"); goBack(h.root);
  c18Edit(h.root);
  openTest(h.root, "a");
  const savedExamples = structuredClone(ex); savedExamples[0].input = "Name a, edited";
  const savedSource = replaceExamples(replaceInstructions(h.source, [{ key: mainKey, text: "Reply briefly.\n More." }]), savedExamples);
  const savedView = await viewFor(savedSource, savedExamples, c18Latest({ a: "pass" }, { scope: "subset", selected: ["a"] }));
  let release; reply = new Promise((resolve) => { release = resolve; });
  enterOn(byClass(h.root, "example-input")[0]);
  assert.equal(h.calls.length, 1);
  const one = h.calls[0];
  assert.equal(one.operation, "save-and-test");
  assert.equal(one.caseId, "a");
  assert.equal(one.expectedRevision, h.view.file.revision);
  assert.equal(one.showContent, true, "the passing-answer opt-in follows the user across views");
  assert.deepEqual(one.sections, [{ key: mainKey, text: "Reply briefly.\n More." }]);
  assert.deepEqual(one.examples, savedExamples, "only this test's draft is applied; B stays as saved");
  // Busy: read-only and honest; nothing claims Saved before the bridge answers.
  assert.doesNotMatch(c18Text(h.root, "prompt-status"), /^Saved/);
  assert.match(c18Text(h.root, "prompt-status"), /this test/);
  assert.equal(byClass(h.root, "example-input")[0].disabled, true);
  enterOn(byClass(h.root, "example-input")[0]);
  assert.equal(h.calls.length, 1, "no second action while one is running");
  release({ view: savedView, saved: true }); await tick(); await tick();
  assert.equal(byClass(h.root, "unsaved-tag")[0].hidden, true);
  assert.equal(c18Text(h.root, "detail-status"), "Passed · before your edit on fake", "B's unsaved draft also stales A's result");
  goBack(h.root);
  assert.deepEqual(items(h.root).map((i) => noteOf(i).startsWith("Unsaved")), [false, true, false], "B's draft is retained");
  assert.deepEqual(rowLabels(h.root), ["Passed last run", "Not in this run", "Not in this run"], "scope stays honest and B's draft greys the known result");
  reply = { error: "refused", message: "Kept." };
  promptEnter(h.root); await tick();
  const all3 = h.calls[1];
  assert.equal(all3.operation, "save-and-test");
  assert.equal(Object.hasOwn(all3, "caseId"), false, "main always runs every test");
  assert.equal(all3.expectedRevision, savedView.file.revision);
  assert.equal(Object.hasOwn(all3, "sections"), false, "the saved prompt is not resent");
  assert.deepEqual(all3.examples.map((e) => [e.id, e.input, e.assertions[0].value]), [["a", "Name a, edited", "Hello, a"], ["b", "Name b", "Hello, b!"], ["c", "Name c", "Hello, c"]]);
  assert.equal(byClass(h.root, "action-message")[0].textContent, "Kept.");
  assert.ok(noteOf(items(h.root)[1]).startsWith("Unsaved"), "an error preserves drafts");
});

function bLatest(outcomes, answers = {}) {
  return (revision, examples) => {
    const latest = c18Latest(outcomes)(revision, examples);
    for (const row of latest.result.examples) {
      row.assertions = examples.find(e => e.id === row.id).assertions.map(a => ({ id: a.id, type: a.type, verdict: row.verdict }));
      if (Object.hasOwn(answers, row.id)) row.answerExcerpt = answers[row.id];
    }
    return latest;
  };
}

test("Boards C/D checks rest as lines, edit on click, and every failed condition explains itself", async () => {
  const examples = [{ id: "a", input: "Input", assertions: [
    { id: "missing", type: "contains", value: "CSV" },
    { id: "found", type: "notContains", value: "secret" },
    { id: "pattern", type: "regex", pattern: "^Draft" },
  ] }];
  const h = await editableCanvas("Reply briefly.", undefined, { examples, latest: bLatest({ a: "fail" }, { a: "secret answer" }) });
  assert.equal(noteOf(items(h.root)[0]), 'Missing "CSV"');
  openTest(h.root, "a");
  assert.match(c18Text(h.root, "detail-status"), /^Failed/);
  assert.deepEqual(byClass(h.root, "why-text").map(e => e.textContent), ['Missing "CSV"', 'Found "secret"', 'No match for ^Draft']);
  assert.equal(byClass(h.root, "check-line").length, 3);
  assert.ok(byClass(h.root, "check-edit").every(e => e.hidden));
  click(byClass(h.root, "check-line")[0]);
  assert.equal(byClass(h.root, "check-edit")[0].hidden, false);
  assert.equal(h.doc.activeElement, byClass(h.root, "check-value")[0]);
  assert.ok(byClass(h.root, "check-edit")[0].children[0].attrs["aria-label"].startsWith("Remove check"));
  assert.equal(byClass(h.root, "check-verdict")[0].textContent, "Failed");
  assert.equal(all(h.root).filter(e => e.tag === "select").length, 0, "check type is a canvas menu");
  assert.equal(byClass(h.root, "answer-excerpt")[0].textContent, "secret answer");
  assert.doesNotMatch(h.root.textContent, /Untrusted excerpt|Expected .*got/);
  assert.equal(h.calls.length, 0, "opening a field does not save or run");
});

test("Board C passing answer is requested explicitly without a hidden checkbox", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: bLatest({ a: "pass" }, { a: "Hello, a" }) });
  assert.equal(byClass(h.root, "include-answers").length, 0);
  await h.clickTest(); assert.equal(h.calls[0].showContent, true);
  assert.equal(Object.hasOwn(h.calls[0], "showFailedContent"), false);
  openTest(h.root, "a");
  assert.match(c18Text(h.root, "detail-status"), /^Passed/);
  assert.equal(byClass(h.root, "answer-excerpt")[0].textContent, "Hello, a");
  assert.doesNotMatch(h.root.textContent, /Untrusted excerpt/);
});

const judgeField = (root, index, label) => all(root).find(e => e.attrs?.["aria-label"] === `Check ${index} ${label}`);
const judgeAssertion = () => ({ id: "why", type: "llmJudge", rubric: "Says why.", minScore: 0, judgeModel: "fake", judgeVersion: "v1" });

test("Judge conversion authors four explicit fields and scoped Enter preserves raw source model absence", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { omitModel: true }); openTest(h.root, "greeting");
  click(byClass(h.root, "check-line")[0]); click(byClass(h.root, "check-type-option")[3]);
  const rubric = judgeField(h.root, 1, "rubric");
  assert.ok(rubric, "Judge has a rubric field"); assert.equal(rubric.tag, "textarea");
  assert.equal(rubric.value, "Hello, Ada");
  for (const field of ["minimum score", "judge model", "judge version"]) assert.equal(judgeField(h.root, 1, field).value, "");
  const score = judgeField(h.root, 1, "minimum score");
  assert.equal(score.type, "number"); assert.equal(String(score.min), "0"); assert.equal(String(score.max), "100"); assert.equal(String(score.step), "1");
  edited(rubric, "Says why.\nKeeps detail.");
  let prevented = false; rubric.listeners.keydown[0]({ key: "Enter", shiftKey: true, preventDefault() { prevented = true; } });
  assert.equal(prevented, false); assert.equal(h.calls.length, 0);
  edited(score, "0"); edited(judgeField(h.root, 1, "judge model"), "fake"); edited(judgeField(h.root, 1, "judge version"), "v1");
  enterOn(rubric); await tick();
  assert.equal(h.calls.length, 1); assert.equal(h.calls[0].operation, "save-and-test"); assert.equal(h.calls[0].caseId, "greeting");
  assert.equal(Object.hasOwn(h.calls[0], "sourceModel"), false);
  assert.deepEqual(h.calls[0].examples[0].assertions, [{ id: "greeting-text", type: "llmJudge", rubric: "Says why.\nKeeps detail.", minScore: 0, judgeModel: "fake", judgeVersion: "v1" }]);
});

for (const score of ["0", "100", "", "1.5", "101"]) test(`Judge minimum score ${JSON.stringify(score)} is validated before dispatch`, async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }] });
  openTest(h.root, "a"); edited(judgeField(h.root, 1, "minimum score"), score); enterOn(judgeField(h.root, 1, "rubric")); await tick();
  if (score === "0" || score === "100") { assert.equal(h.calls.length, 1); assert.equal(h.calls[0].examples?.[0]?.assertions[0].minScore ?? 0, Number(score)); }
  else { assert.equal(h.calls.length, 0); assert.match(h.root.textContent, /Fill in every check\./); }
});

for (const field of ["judge version"]) test(`Judge requires ${field} before dispatch`, async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }] });
  openTest(h.root, "a"); edited(judgeField(h.root, 1, field), ""); enterOn(judgeField(h.root, 1, "rubric")); await tick();
  assert.equal(h.calls.length, 0); assert.match(h.root.textContent, /Fill in every check\./);
});

test("an unpinned Judge is runnable and states that it follows the run model", async () => {
  const assertion = judgeAssertion(); delete assertion.judgeModel;
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [assertion] }] });
  openTest(h.root, "a"); assert.match(byClass(h.root, "judge-binding")[0].textContent, /Run model/);
  edited(judgeField(h.root, 1, "rubric"), "Says exactly why.");
  enterOn(judgeField(h.root, 1, "rubric")); await tick();
  assert.equal(h.calls.length, 1); assert.equal(h.calls[0].examples[0].assertions[0].judgeModel, undefined);
  assert.equal(h.calls[0].examples[0].assertions[0].judgeVersion, "v1");
});

for (const prefix of [[], [{ id: "text", type: "contains", value: "Hello" }]]) test(`removing a trailing Judge survives the intermediate refresh (${prefix.length} preceding checks)`, async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [...prefix, judgeAssertion()] }] });
  openTest(h.root, "a"); click(byClass(h.root, "check-line").at(-1));
  assert.doesNotThrow(() => click(byClass(h.root, "remove-check").at(-1)));
  assert.equal(byClass(h.root, "check-type-button").length, prefix.length);
  assert.equal(byClass(h.root, "judge-binding").length, 0); assert.equal(h.calls.length, 0);
});

test("clearing an existing Judge pin keeps its explicit version and permits the next run", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }] });
  openTest(h.root, "a"); edited(judgeField(h.root, 1, "judge model"), "");
  assert.match(byClass(h.root, "judge-binding")[0].textContent, /Run model/);
  enterOn(judgeField(h.root, 1, "rubric")); await tick();
  assert.equal(h.calls.length, 1); assert.equal(h.calls[0].examples[0].assertions[0].judgeVersion, "v1");
  assert.equal(h.calls[0].examples[0].assertions[0].judgeModel, "");
});

test("optional Judge display drops malformed or contradictory scores without invalidating the observation", async () => {
  for (const judge of [
    { scores: [20], minScore: 70 }, { scores: [-1, 20], minScore: 70 },
    { scores: [20, 30], minScore: 101 }, { scores: [80, 90], minScore: 70 },
    { scores: ["20", 30], minScore: 70 },
  ]) {
    const r = result({ verdict: "fail", fullPass: false });
    r.examples[0].assertions[0] = { id: "exact", type: "llmJudge", verdict: "fail", judge };
    const view = await readView(stubSession(resource(MINIMAL, { latest: { status: "current", result: r } })), MINIMAL);
    assert.equal(view.latest.status, "current"); assert.equal(view.latest.badge, "not-passing");
    assert.equal(view.latest.result.examples[0].assertions[0].judge, undefined);
  }
});

test("Judge detail renders both observed scores and safe reasons, never in the list", async () => {
  const assertion = { ...judgeAssertion(), minScore: 70 }, reasons = ['Missing the reason for users.', '<img src=x onerror="bad()"> Not explained.'];
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [assertion] }], latest: (revision, examples) => {
    const latest = bLatest({ a: "fail" })(revision, examples);
    latest.result.examples[0].assertions[0].judge = { scores: [40, 55], minScore: 70, reasons };
    return latest;
  } });
  assert.doesNotMatch(h.root.textContent, /Missing the reason|Not explained/);
  openTest(h.root, "a");
  assert.match(byClass(h.root, "judge-binding")[0].textContent, /fake.*pinned/);
  assert.equal(byClass(h.root, "judge-score")[0].textContent, "Scores: 40, 55 · minimum 70");
  assert.equal(byClass(h.root, "judge-reason")[0].textContent, reasons.join("\n\n"));
  assert.equal(all(h.root).some(e => e.tag === "img"), false);
  edited(judgeField(h.root, 1, "judge version"), "v2");
  assert.match(byClass(h.root, "judge-result")[0].className, /old/);
  assert.match(byClass(h.root, "check-verdict")[0].textContent, /last run/);
});

for (const verdict of ["pass", "fail", "unknown"]) test(`Judge renders observed ${verdict} and safe reason without scores`, async () => {
  const reason = '<img src=x onerror="bad()"> A short reason.';
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }], latest: (revision, examples) => {
    const latest = bLatest({ a: verdict })(revision, examples);
    latest.result.examples[0].assertions[0] = { id: "why", type: "llmJudge", verdict, reasonCode: "judge_verdict", reason };
    return latest;
  } });
  openTest(h.root, "a");
  assert.equal(byClass(h.root, "check-label")[0].textContent, "Says why. · judge");
  assert.equal(byClass(h.root, "check-verdict")[0].textContent, `Judge ${verdict === "pass" ? "Passed" : verdict === "fail" ? "Failed" : "Inconclusive"}`);
  assert.equal(byClass(h.root, "judge-reason")[0].textContent, reason);
  assert.equal(all(h.root).some(e => e.tag === "img"), false);
  assert.doesNotMatch(h.root.textContent, /score:|reasoning:|Not met:/i);
  assert.equal(byClass(h.root, "why-actions").length, 0);
  if (verdict === "unknown") { assert.doesNotMatch(byClass(h.root, "result-pill")[0]?.textContent ?? "", /1 of 1 passing/); assert.deepEqual(checkLabels(h.root), ["Inconclusive"]); }
  edited(judgeField(h.root, 1, "judge version"), "v2");
  assert.equal(byClass(h.root, "check-verdict")[0].attrs["data-tone"], verdict === "unknown" ? "old-error" : `old-${verdict}`);
  assert.match(byClass(h.root, "check-verdict")[0].textContent, /last run/, "old judge results need visible text, not opacity alone");
});

for (const [label, key, value] of [["rubric", "rubric", "New\nRubric"], ["minimum score", "minScore", "100"], ["judge model", "judgeModel", "other"], ["judge version", "judgeVersion", "opaque-revision"]]) test(`Judge changing only ${label} preserves other raw fields and dims verdict`, async () => {
  const original = judgeAssertion();
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [original] }], latest: bLatest({ a: "pass" }) });
  openTest(h.root, "a"); edited(judgeField(h.root, 1, label), value);
  assert.deepEqual(checkLabels(h.root), ["Passed last run"]);
  enterOn(judgeField(h.root, 1, label)); await tick();
  assert.deepEqual(h.calls[0].examples[0].assertions[0], { ...original, [key]: key === "minScore" ? Number(value) : value });
});

for (const fromJudge of [false, true]) test(`Judge type conversion rebinds results and preserves chooser focus (${fromJudge ? "from" : "to"} Judge)`, async () => {
  const assertion = fromJudge ? judgeAssertion() : { id: "why", type: "contains", value: "Says why." };
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [assertion] }], latest: bLatest({ a: "pass" }) });
  openTest(h.root, "a"); click(byClass(h.root, "check-line")[0]);
  click(byClass(h.root, "check-type-button")[0]); click(byClass(h.root, "check-type-option")[fromJudge ? 0 : 3]);
  const trigger = byClass(h.root, "check-type-button")[0];
  assert.equal(trigger.textContent, fromJudge ? "Contains" : "Judge");
  assert.equal(h.doc.activeElement === trigger, true, "the rebuilt chooser retains keyboard focus");
  assert.deepEqual(checkLabels(h.root), ["No result"], "a verdict for a different assertion type cannot attach to the new form");
  assert.equal(byClass(h.root, "check-verdict")[0].textContent, "");
  assert.equal(h.calls.length, 0);
});

test("Judge unknown verdict stays visible when another check fails", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion(), { id: "text", type: "contains", value: "Missing" }] }], latest: (revision, examples) => {
    const latest = bLatest({ a: "fail" })(revision, examples);
    latest.result.examples[0].assertions[0] = { id: "why", type: "llmJudge", verdict: "unknown", reasonCode: "judge_unknown", reason: "Judge could not decide." };
    return latest;
  } });
  openTest(h.root, "a");
  assert.deepEqual(checkLabels(h.root), ["Inconclusive", "Failed"]);
  assert.equal(byClass(h.root, "judge-result")[0].hidden, false);
  assert.equal(byClass(h.root, "judge-reason")[0].textContent, "Judge could not decide.");
  assert.match(c18Text(h.root, "detail-status"), /^Failed/);
});

test("Judge conversion in both directions preserves ID and operand, drops only incompatible known fields; cancel restores", async () => {
  const original = judgeAssertion();
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [original] }] });
  openTest(h.root, "a"); click(byClass(h.root, "check-line")[0]);
  click(byClass(h.root, "check-type-option")[2]);
  assert.equal(byClass(h.root, "check-value")[0].value, "Says why.");
  enterOn(byClass(h.root, "check-value")[0]); await tick();
  assert.deepEqual(h.calls[0].examples[0].assertions[0], { id: "why", type: "regex", pattern: "Says why." });
  click(byClass(h.root, "check-type-option")[3]);
  assert.equal(judgeField(h.root, 1, "minimum score").value, "");
  judgeField(h.root, 1, "rubric").listeners.keydown[0]({ key: "Escape", preventDefault() {} });
  assert.equal(String(judgeField(h.root, 1, "minimum score").value), "0");
  assert.equal(judgeField(h.root, 1, "judge model").value, "fake");
});

test("Judge duplicates and deletes without changing metadata", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }] });
  openTest(h.root, "a"); click(byClass(h.root, "more")[0]); click(btn(h.root, "Duplicate test"));
  enterOn(judgeField(h.root, 1, "rubric")); await tick();
  assert.deepEqual(h.calls[0].examples.map(e => e.assertions[0]), [judgeAssertion(), judgeAssertion()]);
  click(byClass(h.root, "add-check")[0]); click(byClass(h.root, "remove-check")[0]);
  edited(byClass(h.root, "check-value")[0], "remaining"); enterOn(byClass(h.root, "check-value")[0]); await tick();
  assert.deepEqual(h.calls[1].examples[1].assertions, [{ id: "check-1", type: "contains", value: "remaining" }]);
});

for (const fence of ["stale", "wrong type", "duplicate observation"]) test(`Judge result respects ${fence} fence`, async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: [{ id: "a", input: "Name a", assertions: [judgeAssertion()] }], latest: (revision, examples) => {
    if (fence === "stale") return { status: "stale" };
    const latest = bLatest({ a: "pass" })(revision, examples), row = latest.result.examples[0];
    row.assertions[0].reason = "Must not show";
    if (fence === "wrong type") row.assertions[0].type = "contains";
    else row.assertions.push({ ...row.assertions[0] });
    return latest;
  } });
  openTest(h.root, "a");
  assert.deepEqual(checkLabels(h.root), ["No result"]);
  assert.equal(byClass(h.root, "judge-result")[0].hidden, true);
  assert.doesNotMatch(h.root.textContent, /Must not show/);
});

test("readonly Judge keeps rubric and metadata inert without editable fields", async () => {
  const fixture = savedFixture(), literal = '<img src=x onerror="bad()">';
  fixture.examples[0].assertions = [{ ...judgeAssertion(), rubric: literal, judgeVersion: literal, future: literal }];
  fixture.latest.result.examples[0].assertions = [{ id: "why", type: "llmJudge", verdict: "pass", reason: literal }];
  const root = openTest(await rendered(fixture), "greeting");
  assert.equal(byClass(root, "check-type-button").length, 0);
  assert.equal(byClass(root, "check-value").length, 0);
  assert.equal(byClass(root, "check-label")[0].textContent, literal + " · judge");
  assert.match(byClass(root, "check-details")[0].textContent, /minScore: 0/);
  assert.ok(byClass(root, "check-details")[0].textContent.includes("future: " + literal));
  assert.equal(byClass(root, "judge-reason")[0].textContent, literal);
  assert.equal(all(root).some(e => e.tag === "img"), false);
});

test("Judge is the fourth check type and opening its chooser does not dispatch", async () => {
  const h = await editableCanvas("Reply briefly."); openTest(h.root, "greeting");
  click(byClass(h.root, "check-line")[0]); click(byClass(h.root, "check-type-button")[0]);
  assert.equal(h.calls.length, 0);
  assert.deepEqual(byClass(h.root, "check-type-option").map(option => option.textContent),
    ["Contains", "Not contains", "Regex", "Judge"]);
});

test("check-type menu supports choice and Escape without dispatching", async () => {
  const h = await editableCanvas("Reply briefly."); openTest(h.root, "greeting");
  click(byClass(h.root, "check-line")[0]);
  const trigger = byClass(h.root, "check-type-button")[0];
  click(trigger); assert.equal(trigger.attrs["aria-expanded"], "true");
  const choices = byClass(h.root, "check-type-option");
  assert.deepEqual(choices.map(e => e.textContent), ["Contains", "Not contains", "Regex", "Judge"]);
  click(choices[1]); assert.equal(trigger.textContent, "Not contains");
  assert.equal(trigger.attrs["aria-expanded"], "false"); assert.equal(h.doc.activeElement, trigger);
  click(trigger); trigger.listeners.keydown[0]({ key: "Escape", preventDefault() {} });
  assert.equal(trigger.attrs["aria-expanded"], "false"); assert.equal(h.calls.length, 0);
  enterOn(byClass(h.root, "check-value")[0]); await tick();
  assert.equal(h.calls[0].examples[0].assertions[0].type, "notContains");
});

test("Board F keeps previously observed unchanged results dimmed, but never lends them to new/changed tests", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: bLatest({ a: "pass", b: "fail" }), fetchModels: async () => ({models:["other-model"]}) });
  assert.equal(c18Text(h.root, "result-pill"), "1 of 2 passing");
  await chooseModel(h, "other-model");
  assert.deepEqual(c18Status(h.root), ["old-pass", "old-fail"]);
  assert.equal(c18Text(h.root, "result-pill"), "Out of date");
  assert.match(c18Text(h.root, "tests-summary"), /Out of date.*last run on fake/);
  const examples = [...h.examples.map(e => structuredClone(e)), ...c18Examples(["c"])];
  examples[1].input += " edited";
  const source = replaceExamples(h.source, examples);
  assert.equal(h.app.observe(await viewFor(source, examples, { status: "stale" })), true);
  assert.deepEqual(c18Status(h.root), ["old-pass", "none", "none"]);
  assert.equal(byClass(items(h.root)[2], "test-verdict")[0].textContent, "Not run yet");
});

test("never-run edited agent stays never-run and has the first-run Tests action", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]) });
  assert.equal(c18Text(h.root, "result-pill"), "Not run yet");
  assert.equal(byClass(h.root, "first-run")[0].textContent, "Run 2 tests");
  const examples = [...h.examples, ...c18Examples(["c"])], source = replaceExamples(h.source, examples);
  h.app.observe(await viewFor(source, examples));
  assert.equal(c18Text(h.root, "result-pill"), "Not run yet");
  assert.equal(byClass(h.root, "first-run")[0].textContent, "Run 3 tests");
  assert.ok(c18Status(h.root).every(status => status === "none"));
  click(byClass(h.root, "first-run")[0]); await tick();
  assert.equal(h.calls.length, 1); assert.equal(h.calls[0].operation, "test");
});

test("batch pending state never invents per-case progress and list/detail use the same answer", async () => {
  let finish;
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: bLatest({ a: "fail", b: "pass" }, { a: "answer A", b: "answer B" }), respond: () => new Promise(resolve => { finish = resolve; }) });
  openTest(h.root, "a"); assert.equal(c18Text(h.root, "answer-excerpt"), "answer A");
  goBack(h.root); promptEnter(h.root);
  assert.match(c18Text(h.root, "result-pill"), /Running 2 tests/);
  assert.deepEqual(c18Status(h.root), ["old-fail", "old-pass"]);
  assert.doesNotMatch(c18Text(h.root, "result-pill"), /1 of|2 of/);
  const next = await viewFor(h.source, h.examples, bLatest({ a: "fail", b: "pass" }, { a: "answer C", b: "answer D" }));
  finish({ view: next, saved: false }); await tick(); await tick();
  const note = noteOf(items(h.root)[0]);
  assert.equal(note, 'Missing "Hello, a"');
  assert.equal(c18Text(h.root, "result-pill"), "1 of 2 passing");
  openTest(h.root, "a"); assert.equal(c18Text(h.root, "answer-excerpt"), "answer C");
  goBack(h.root); openTest(h.root, "a");
  assert.equal(c18Text(h.root, "answer-excerpt"), "answer C"); assert.equal(h.calls.length, 1);
});

test("result pill never promotes partial, contradictory or unfinished observations to a full pass", async () => {
  for (const kind of ["subset", "contradictory", "unfinished"]) {
    const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: (revision, examples) => {
      const latest = bLatest({ a: "pass", b: "pass" })(revision, examples);
      latest.result.fullPass = false;
      if (kind === "subset") {
        latest.result.selection.scope = "subset"; latest.result.selection.caseIds = ["a"]; latest.result.examples.pop();
      } else if (kind === "unfinished") latest.result.state = "incomplete";
      return latest;
    } });
    const pill = byClass(h.root, "result-pill")[0];
    assert.notEqual(pill.attrs["data-tone"], "pass");
    assert.notEqual(pill.textContent, "2 of 2 passing", kind);
  }
});

test("retained result matching uses full authored input, not its truncated display prefix", async () => {
  const examples = c18Examples(["a"]); examples[0].input = "x".repeat(2100) + " old";
  const h = await editableCanvas("Reply briefly.", undefined, { examples, latest: bLatest({ a: "pass" }) });
  const changed = structuredClone(examples); changed[0].input = "x".repeat(2100) + " new";
  const view = await viewFor(replaceExamples(h.source, changed), changed, { status: "stale" });
  assert.equal(view.examples[0].input, h.view.examples[0].input, "display strings are identical");
  h.app.observe(view);
  assert.deepEqual(c18Status(h.root), ["none"], "different full input must not inherit the old verdict");
});

test("same-revision rerun correlates row failure and detail answer, and a delayed earlier observation cannot roll it back", async () => {
  const examples = [{ id: "a", input: "Input", assertions: [{ id: "csv", type: "contains", value: "CSV" }, { id: "ref", type: "contains", value: "#101" }] }];
  const observation = (answer, missing) => (revision, cases) => {
    const latest = bLatest({ a: "fail" }, { a: answer })(revision, cases);
    for (const a of latest.result.examples[0].assertions) a.verdict = a.id === missing ? "fail" : "pass";
    return latest;
  };
  let reply;
  const h = await editableCanvas("Reply briefly.", undefined, { examples, latest: observation("#101 answer A", "csv"), respond: () => reply });
  const started = h.app.observation();
  assert.equal(noteOf(items(h.root)[0]), 'Missing "CSV"');
  reply = { view: await viewFor(h.source, examples, observation("CSV answer B", "ref")), saved: false };
  promptEnter(h.root); await tick(); await tick();
  assert.equal(h.calls.length, 1); assert.equal(noteOf(items(h.root)[0]), 'Missing "#101"');
  assert.equal(h.app.observe(h.view, started), false);
  openTest(h.root, "a"); assert.equal(c18Text(h.root, "answer-excerpt"), "CSV answer B");
  goBack(h.root); assert.equal(noteOf(items(h.root)[0]), 'Missing "#101"');
  assert.equal(h.calls.length, 1, "navigation never reruns the model");
});

test("Suggest does not claim that tests are running or dim their valid result", async () => {
  let finish;
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: bLatest({ a: "pass" }), respond: () => new Promise(resolve => { finish = resolve; }) });
  click(btn(h.root, "Suggest tests"));
  assert.equal(c18Text(h.root, "result-pill"), "1 of 1 passing");
  assert.deepEqual(c18Status(h.root), ["pass"]);
  finish({ message: "Asked in chat." }); await tick(); await tick();
});

test("last-run model labels leave unknown IDs literal, including inherited property names", async () => {
  for (const id of ["constructor", "__proto__", "unknown-model"]) assert.equal(modelLabel(`provider/${id}`), id);
  assert.equal(modelLabel("copilot/gpt-5.6-terra"), "GPT-5.6 Terra");
});

test("a cleared resource during another preview does not relabel observed tests as never run", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: bLatest({ a: "pass" }) });
  h.app.observe(await viewFor(h.source, h.examples, { status: "none" }));
  assert.equal(c18Text(h.root, "result-pill"), "Out of date");
  assert.match(summaryOf(h.root), /^Out of date/);
  assert.deepEqual(c18Status(h.root), ["old-pass"]);
});

test("a new scoped run keeps unchanged earlier cases dimmed, with their own model provenance and honest current counts", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: (revision, examples) => {
    const latest = bLatest({ a: "pass", b: "fail" })(revision, examples);
    latest.result.modelRoute = { provider: "provider", model: "old-model" }; return latest;
  } });
  assert.deepEqual(c18Status(h.root), ["old-pass", "old-fail"], "fixture has real earlier observations");
  const examples = [...h.examples, ...c18Examples(["c"])], source = replaceExamples(h.source, examples);
  const latest = c18Latest({ c: "pass" }, { scope: "subset", selected: ["c"] });
  h.app.observe(await viewFor(source, examples, latest));
  assert.deepEqual(c18Status(h.root), ["old-pass", "old-fail", "pass"]);
  assert.deepEqual(rowLabels(h.root), ["Passed last run", "Failed last run", "Passed"]);
  assert.equal(c18Text(h.root, "result-pill"), "1 of 3 passing");
  assert.notEqual(byClass(h.root, "result-pill")[0].attrs["data-tone"], "pass");
  assert.equal(summaryOf(h.root), "1 passing, 2 not run", "past verdicts do not count as this run's results");
  openTest(h.root, "b");
  assert.match(c18Text(h.root, "detail-status"), /Failed.*Out of date.*old-model/);
});

test("retained-case memory drops removed identities instead of growing across revisions", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: bLatest({ a: "pass", b: "pass" }) });
  const kept = [h.examples[1]];
  h.app.observe(await viewFor(replaceExamples(h.source, kept), kept, { status: "stale" }));
  h.app.observe(await viewFor(h.source, h.examples, { status: "stale" }));
  assert.deepEqual(c18Status(h.root), ["none", "old-pass"]);
});

test("live summary changes actual text, not just its accessible-name attribute", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: bLatest({ a: "pass" }) });
  const summary = byClass(h.root, "tests-summary")[0];
  assert.equal(summary.attrs.role, "status");
  assert.equal(summary.textContent, "1 passing");
  assert.equal(summary.attrs["aria-label"], undefined);
  c18Edit(h.root);
  assert.equal(summary.textContent, "Last run, before your edit: 1 passing");
});

test("approved result pills mirror current results, while Tests uses its run action or stale metadata", async () => {
  const h = await editableCanvas("Reply briefly.");
  let pills = byClass(h.root, "result-pill");
  assert.equal(pills.length, 2); assert.equal(pills[0].textContent, "Not run yet");
  assert.notEqual(pills[0].hidden, true); assert.equal(pills[1].hidden, true);
  assert.equal(byClass(h.root, "first-run")[0].hidden, false);
  const current = await editableCanvas("Reply briefly.", undefined, { currentPass: true, fetchModels: async () => ({models:["other"]}) });
  pills = byClass(current.root, "result-pill");
  assert.equal(pills[0].textContent, "1 of 1 passing"); assert.equal(pills[1].textContent, pills[0].textContent);
  assert.equal(pills[1].hidden, false);
  await chooseModel(current, "other");
  pills = byClass(current.root, "result-pill");
  assert.equal(pills[0].textContent, "Out of date"); assert.equal(pills[1].hidden, true);
  assert.match(c18Text(current.root, "tests-meta"), /^Out of date/);
  const verdict = byClass(current.root, "test-verdict")[0];
  assert.equal(verdict.textContent, "Passed last run");
  assert.equal(verdict.attrs["aria-label"], undefined);
  assert.equal(byClass(verdict, "verdict-context")[0].textContent, " last run");
});

test("ambiguous normalized IDs cannot inherit remembered verdicts in a stale read-only view", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: bLatest({ a: "pass" }) });
  const examples = [h.examples[0], { ...h.examples[0], id: "a\u202e", input: "Different input" }];
  const view = await readView(stubSession(resource(MINIMAL, {
    file: h.view.file, instructions: h.source, model: "fake", examples, latest: { status: "stale" },
    authoring: { source: h.source, editable: false },
  })), MINIMAL);
  assert.deepEqual(view.examples.map(e => e.id), ["a", "a"]);
  h.app.observe(view);
  assert.deepEqual(c18Status(h.root), ["none", "none"]);
});

test("Run's observed saved tests drive the result pill, while an ad-hoc run without tests stays never-run", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: (revision, examples) => {
    const latest = bLatest({ a: "pass", b: "fail" })(revision, examples);
    latest.result.operation = "run"; latest.result.adHoc = { state: "complete", verdict: "pass", answerExcerpt: "Hello, Kai" };
    return latest;
  } });
  assert.equal(c18Text(h.root, "result-pill"), "1 of 2 passing");
  assert.equal(byClass(h.root, "result-pill")[0].attrs["data-tone"], "fail");
  const empty = await editableCanvas("Reply briefly.", undefined, { examples: [], latest: (revision, examples) => {
    const latest = bLatest({})(revision, examples); latest.result.operation = "run"; latest.result.fullPass = false;
    latest.result.reasonCode = "completion_ready"; latest.result.adHoc = { state: "complete", verdict: "pass", answerExcerpt: "Hello" };
    return latest;
  } });
  assert.equal(c18Text(empty.root, "result-pill"), "Not run yet");
});

test("Not passing counts and shows retained old passes rather than treating them as current passes", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a", "b"]), latest: bLatest({ a: "pass", b: "pass" }), defaultView: { innerHeight: 100 } });
  h.app.observe(await viewFor(h.source, h.examples, c18Latest({ b: "pass" }, { scope: "subset", selected: ["b"] })));
  assert.deepEqual(c18Status(h.root), ["old-pass", "pass"]);
  const filter = byClass(h.root, "filter-button")[1];
  assert.equal(filter.textContent, "Not passing 1");
  click(filter);
  assert.deepEqual(items(h.root).map(item => item.hidden), [false, true]);
  assert.equal(summaryOf(h.root), "1 passing, 1 not run");
});

test("Board E blank test focuses its visibly named field without saved-test chrome", async () => {
  const h = await editableCanvas("Reply briefly.");
  click(btn(h.root, "Add test"));
  const name = byClass(h.root, "test-title-input")[0];
  assert.equal(h.doc.activeElement, name);
  assert.equal(name.attrs.placeholder, "Name this test");
  assert.equal(byClass(h.root, "more").length, 0);
  assert.equal(byClass(h.root, "unsaved-tag")[0].hidden, true);
  assert.equal(c18Text(h.root, "detail-status"), "");
  assert.equal(byClass(h.root, "example-input")[0].attrs.placeholder, 'What the agent is given, e.g. Merged: "Fix slow search", closes #218');
  assert.equal(c18Text(h.root, "prompt-status"), "Enter saves and runs this test");
  const cancel = byClass(h.root, "cancel-test")[0], run = byClass(h.root, "save-run")[0];
  assert.equal(cancel.attrs["aria-label"], "Cancel");
  assert.equal(cancel.textContent, ""); assert.equal(cancel.children[0].tag, "svg");
  assert.equal(run.attrs["aria-label"], "Save and run this test");
  assert.equal(h.calls.length, 0);
});

test("Board E Enter or save without a name focuses the name and gives one inline hint", async () => {
  for (const via of ["Enter", "button"]) {
    const h = await editableCanvas("Reply briefly.");
    click(btn(h.root, "Add test"));
    const name = byClass(h.root, "test-title-input")[0], input = byClass(h.root, "example-input")[0];
    edited(input, "Ada"); edited(byClass(h.root, "check-value")[0], "Hello");
    input.focus();
    if (via === "Enter") enterOn(input); else click(byClass(h.root, "save-run")[0]);
    await tick();
    assert.equal(h.calls.length, 0); assert.equal(h.doc.activeElement, name);
    assert.equal(name.attrs["aria-invalid"], "true");
    const hint = byClass(h.root, "name-hint")[0];
    assert.equal(hint.hidden, false); assert.equal(hint.textContent, "Name this test.");
    assert.equal(name.attrs["aria-describedby"], hint.attrs.id);
    assert.equal(c18Text(h.root, "action-message"), "", "do not duplicate the inline hint");
    edited(name, "Greets Ada");
    assert.equal(name.attrs["aria-invalid"], "false"); assert.equal(hint.hidden, true);
    enterOn(name); await tick();
    assert.equal(h.calls.length, 1); assert.equal(h.calls[0].caseId, "greets-ada");
    assert.equal(h.calls[0].operation, "save-and-test");
  }
});

test("Board E one Enter dispatches once and exposes pending state even if Enter/click repeat", async () => {
  let finish;
  const h = await editableCanvas("Reply briefly.", undefined, { respond: () => new Promise(resolve => { finish = resolve; }) });
  click(btn(h.root, "Add test"));
  const name = byClass(h.root, "test-title-input")[0];
  edited(name, "Greets Ada"); edited(byClass(h.root, "example-input")[0], "Ada");
  edited(byClass(h.root, "check-value")[0], "Hello");
  enterOn(name); enterOn(name); click(byClass(h.root, "save-run")[0]);
  assert.equal(h.calls.length, 1); assert.equal(h.root.attrs["aria-busy"], "true");
  assert.match(c18Text(h.root, "prompt-status"), /Saving and running this test/);
  finish({ error: "refused", message: "Kept." }); await tick(); await tick();
  assert.equal(h.root.attrs["aria-busy"], "false");
});

test("Board C/D saved detail footer says Enter runs this test", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { currentPass: true });
  openTest(h.root, "greeting");
  assert.equal(c18Text(h.root, "prompt-status"), "Saved · Enter runs this test");
});

test("zero tests: first-run teaching, local refusal, delete-all saves zero and a known no-tests refusal stays Saved; ad-hoc Run still works", async () => {
  const empty = await editableCanvas("Reply briefly.", undefined, { examples: [] });
  assert.match(empty.root.textContent, /No tests yet/);
  assert.ok(btn(empty.root, "Suggest tests") && btn(empty.root, "Add a test yourself"));
  assert.equal(c18Text(empty.root, "prompt-status"), "Saved · add a test to run it");
  assert.doesNotMatch(empty.root.textContent, /Keep all|Discard all/, "no invented suggestion review");
  promptEnter(empty.root); await tick();
  assert.equal(empty.calls.length, 0);
  assert.equal(byClass(empty.root, "action-message")[0].textContent, "No tests yet — add or suggest one");
  click(btn(empty.root, "Add a test yourself"));
  assert.equal(byClass(empty.root, "test-title-input").length, 1, "a local draft, not a save");
  assert.equal(empty.calls.length, 0);

  let reply;
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), respond: () => reply });
  const zeroSource = replaceExamples(h.source, []);
  const noTests = (extra = {}) => (revision) => ({ status: "current", result: result({
    file: { path: MINIMAL, revision }, selection: { scope: "full", totalCases: 0, caseIds: [] }, examples: [],
    state: "refused", verdict: "unknown", fullPass: false, modelRoute: {}, reasonCode: "no_tests", reason: "No tests yet — add or suggest one", ...extra,
  }) });
  reply = { error: "no-tests", message: "No tests yet — add or suggest one", saved: true, view: await viewFor(zeroSource, [], noTests()) };
  openTest(h.root, "a"); click(byClass(h.root, "more")[0]); click(btn(h.root, "Delete test"));
  assert.equal(noteOf(items(h.root)[0]), "Deleted · unsaved");
  promptEnter(h.root); await tick(); await tick();
  assert.equal(h.calls[0].operation, "save-and-test");
  assert.deepEqual(h.calls[0].examples, []);
  assert.equal(Object.hasOwn(h.calls[0], "caseId"), false);
  assert.equal(c18Text(h.root, "prompt-status"), "Saved · add a test to run it");
  assert.equal(reloadButton(h.root).hidden, true, "a known no-tests refusal is not an unknown outcome");
  assert.equal(byClass(h.root, "empty-tests")[0].hidden, false);
  assert.doesNotMatch(h.root.textContent, /Out of date/);
  assert.equal(byClass(h.root, "pass").length, 0, "no green for zero tests");
  reply = { view: await viewFor(zeroSource, [], noTests({ operation: "run", state: "complete", reasonCode: "completion_ready", modelRoute: { provider: "fake", model: "fake" }, adHoc: { state: "complete", verdict: "pass", answerExcerpt: "Hi there" } })), saved: false };
  click(btn(h.root, "Run")); await tick(); await tick();
  assert.equal(h.calls[1].operation, "run");
  assert.match(byClass(h.root, "ad-hoc-result")[0].textContent, /Hi there/);
});

test("Suggest acknowledges a request truthfully and subsequent saved additions refresh without losing drafts", async () => {
  for (const withDraft of [false, true]) {
    const ex = c18Examples(["a"]);
    const h = await editableCanvas("Reply briefly.", undefined, { examples: ex, response: { message: "Asked Copilot to add tests in chat." } });
    if (withDraft) c18Edit(h.root);
    click(btn(h.root, "Suggest tests")); await tick(); await tick();
    assert.deepEqual(h.calls, [{ operation: "suggest" }], "click starts chat, not a save or run");
    assert.equal(byClass(h.root, "action-message")[0].textContent, "Asked Copilot to add tests in chat.");
    assert.doesNotMatch(h.root.textContent, /Nothing here change/);
    const added = [...ex, ...c18Examples(["extra"])];
    const saved = await viewFor(replaceExamples(h.source, added), added);
    h.app.observe(saved);
    assert.doesNotMatch(h.root.textContent, /Nothing here change/);
    if (withDraft) {
      assert.match(c18Prompt(h.root).textContent, /More\./, "unsaved prompt is retained");
      assert.equal(byClass(h.root, "notice-info").length, 1, "existing outside-change reconciliation remains");
      click(btn(h.root, "Keep my edits"));
      promptEnter(h.root); await tick();
      assert.deepEqual(h.calls[1].sections, [{ key: h.sections[0].key, text: "Reply briefly.\n More." }]);
      assert.equal(Object.hasOwn(h.calls[1], "examples"), false, "prompt save leaves the newly saved tests alone");
      assert.deepEqual(items(h.root).map(i => i.attrs["data-test-id"]), ["a", "extra"], "added tests survive draft reconciliation");
    } else {
      assert.deepEqual(items(h.root).map(i => i.attrs["data-test-id"]), ["a", "extra"]);
      assert.equal(byClass(h.root, "notice-info").length, 0);
    }
  }
});

test("outside change with drafts: literal Compare, Keep my edits preserves remote parts, a second change refuses again, Use discards", async () => {
  const ex = c18Examples(["a", "b"]);
  const h = await editableCanvas("Reply briefly.", undefined, { examples: ex, response: { error: "refused", message: "Kept." } });
  const [mainKey, secondKey, thirdKey] = h.sections.map((s) => s.key);
  const theirsEx = structuredClone(ex); theirsEx[1].input = "Name b, remote"; theirsEx.push(...c18Examples(["extra"]));
  const theirsSource = replaceExamples(replaceInstructions(h.source, [{ key: secondKey, text: "Changed elsewhere." }]), theirsEx);
  const theirs = await viewFor(theirsSource, theirsEx);
  c18Edit(h.root);
  openTest(h.root, "a"); edited(byClass(h.root, "example-input")[0], "Name a, mine"); goBack(h.root);
  assert.equal(h.app.observe(theirs), true);
  const notice = byClass(h.root, "notice-info")[0];
  assert.equal(notice.attrs.role, "alert");
  assert.match(notice.textContent, /changed outside the panel/);
  assert.doesNotMatch(notice.textContent, /Copilot|ago|PM|AM/, "no invented attribution or time");
  assert.equal(byClass(h.root, "save-run")[0].disabled, true);
  assert.equal(c18Text(h.root, "prompt-status"), "Edited · settle the outside change before saving");
  promptEnter(h.root); await tick();
  assert.equal(h.calls.length, 0, "saving is blocked until a choice");
  click(btn(h.root, "Compare"));
  const texts = byClass(h.root, "compare-text").map((e) => e.textContent);
  for (const literal of ["Reply briefly.", "Reply briefly.\n More.", "Changed elsewhere.", h.sections[1].text, "(not present)"]) assert.ok(texts.includes(literal), literal);
  assert.ok(texts.some((t) => t.includes("Name a, mine")) && texts.some((t) => t.includes("Name b, remote")) && texts.some((t) => t.includes('"extra"')));
  click(btn(h.root, "Keep my edits"));
  assert.equal(byClass(h.root, "notice-info").length, 0);
  assert.equal(byClass(h.root, "save-run")[0].disabled, false);
  promptEnter(h.root); await tick();
  const saved = h.calls[0];
  assert.equal(saved.expectedRevision, theirs.file.revision, "the explicit choice is the new baseline");
  assert.deepEqual(saved.sections, [{ key: mainKey, text: "Reply briefly.\n More." }], "the remote section edit is not overwritten");
  assert.deepEqual(saved.examples.map((e) => [e.id, e.input]), [["a", "Name a, mine"], ["b", "Name b, remote"], ["extra", "Name extra"]]);
  const third = await viewFor(replaceInstructions(theirsSource, [{ key: thirdKey, text: "Third change." }]), theirsEx);
  assert.equal(h.app.observe(third), true);
  assert.equal(byClass(h.root, "notice-info").length, 1, "a later outside change refuses again");
  assert.equal(byClass(h.root, "save-run")[0].disabled, true);
  click(btn(h.root, "Use the new version"));
  assert.equal(byClass(h.root, "notice-info").length, 0);
  assert.equal(c18Prompt(h.root).textContent, "Reply briefly.");
  assert.ok(items(h.root).every((i) => !noteOf(i).startsWith("Unsaved")));
  assert.match(c18Text(h.root, "prompt-status"), /^Saved/);
  assert.equal(h.calls.length, 1);
  // With no drafts, an outside change simply refreshes.
  const fourthEx = structuredClone(theirsEx); fourthEx.pop();
  assert.equal(h.app.observe(await viewFor(replaceExamples(theirsSource, fourthEx), fourthEx)), true);
  assert.equal(byClass(h.root, "notice-info").length, 0);
  assert.deepEqual(items(h.root).map((i) => i.attrs["data-test-id"]), ["a", "b"]);
});

test("Keep my edits refuses an ambiguous test identity without dropping data; observation never clears an unknown outcome", async () => {
  const ex = c18Examples(["a"]);
  const h = await editableCanvas("Reply briefly.", undefined, { examples: ex, response: { error: "outcome-unknown", message: "Not confirmed." } });
  click(btn(h.root, "Add test"));
  edited(byClass(h.root, "test-title-input")[0], "Extra");
  edited(byClass(h.root, "example-input")[0], "Name extra, mine");
  edited(byClass(h.root, "check-value")[0], "Hello");
  goBack(h.root);
  const theirsEx = [...structuredClone(ex), ...c18Examples(["extra"])];
  assert.equal(h.app.observe(await viewFor(replaceExamples(h.source, theirsEx), theirsEx)), true);
  click(btn(h.root, "Keep my edits"));
  assert.match(byClass(h.root, "action-message")[0].textContent, /Can't keep your edits automatically/);
  assert.equal(byClass(h.root, "notice-info").length, 1, "the decision is still open");
  assert.ok(items(h.root).some((i) => noteOf(i).startsWith("Unsaved")), "the draft is kept");
  click(btn(h.root, "Use the new version"));
  promptEnter(h.root); await tick(); await tick();
  assert.equal(summaryOf(h.root), "Result unconfirmed");
  assert.equal(reloadButton(h.root).hidden, false);
  const later = await viewFor(replaceInstructions(h.source, [{ key: h.sections[1].key, text: "Later." }]), ex);
  assert.equal(h.app.observe(later), false, "polling never clears an unknown outcome");
  assert.equal(summaryOf(h.root), "Result unconfirmed");
  assert.equal(h.app.idle(), false);
});

test("expected/got uses only the actual answer; a differing reference is named only when the answer has it once", async () => {
  const ex = [{ id: "issue", input: "Merged: Fix slow search, issue #218", assertions: [{ id: "ref", type: "contains", value: "Fixes #218" }] }];
  const answered = (answer) => (revision, examples) => {
    const latest = c18Latest({ issue: "fail" })(revision, examples);
    latest.result.examples[0].answerExcerpt = answer;
    latest.result.examples[0].assertions[0].id = "ref";
    return latest;
  };
  const real = await editableCanvas("Keep every issue reference as Closes #218 in notes.", undefined, { examples: ex, latest: answered("Search is faster. Closes #218.") });
  assert.equal(noteOf(items(real.root)[0]), 'Missing "Fixes #218"');
  openTest(real.root, "issue");
  assert.equal(all(real.root).filter((e) => e.tag === "mark").map((e) => e.textContent).join(), "Closes #218");
  assert.equal(byClass(real.root, "answer-excerpt")[0].textContent, "Search is faster. Closes #218.", "the answer stays literal");
  assert.equal(byClass(real.root, "why").length, 1, "explained once, at the failed check");
  assert.equal(byClass(real.root, "why-text")[0].textContent, 'Missing "Fixes #218"');
  const apply = await editableCanvas("Keep every issue reference as Closes #218 in notes.", undefined, { examples: ex, latest: answered("Search is faster. Closes #218.") });
  openTest(apply.root, "issue"); click(btn(apply.root, "Apply to prompt")); goBack(apply.root);
  assert.equal(c18Prompt(apply.root).textContent, "Keep every issue reference as Fixes #218 in notes.");
  assert.equal(apply.calls.length, 0);
  click(btn(real.root, "Or expect “Closes #218” in this test"));
  assert.equal(byClass(real.root, "check-value")[0].value, "Closes #218");
  assert.equal(byClass(real.root, "unsaved-tag")[0].hidden, false);
  assert.equal(Boolean(btn(real.root, "Apply to prompt")), false, "an edited operand invalidates the saved-result repair");
  goBack(real.root);
  assert.equal(c18Prompt(real.root).textContent, "Keep every issue reference as Closes #218 in notes.");
  assert.equal(real.calls.length, 0, "suggested fixes are local drafts only");

  for (const answer of ["Fixed slow search.", "Closes #218 and Resolves #218."]) {
    const h = await editableCanvas("Reply briefly.", undefined, { examples: ex, latest: answered(answer) });
    assert.equal(noteOf(items(h.root)[0]), 'Missing "Fixes #218"');
    openTest(h.root, "issue");
    assert.ok(!all(h.root).some((e) => e.tag === "button" && /^Or expect|Apply to prompt/.test(e.textContent)), answer);
    assert.equal(all(h.root).filter((e) => e.tag === "mark").length, 0);
    if (!answer.includes("Closes")) assert.doesNotMatch(h.root.textContent, /Closes/, "never invent the other reference");
  }
});

for (const eol of ["\n", "\r\n"]) test(`prompt paragraphs keep blank lines as their own block and round-trip exactly (${eol === "\n" ? "LF" : "CRLF"})`, async () => {
  const body = 'Use "quoted" words, it\'s fine.\n\nSecond paragraph.\nSame paragraph.\n\n```\n"code"\n\nkept\n```';
  const h = await editableCanvas(body, eol, { examples: c18Examples(["a"]) });
  const blanks = (root) => c18Prompt(root).children.map((line) => line.children.length === 1 && line.children[0].tag === "br");
  assert.deepEqual(blanks(h.root), [false, true, false, false, true, false], "blank source lines are separate short blocks; the fence stays whole");
  assert.ok(c18Prompt(h.root).textContent.includes('"quoted"'), "source ASCII quotes are never presented as typographic");
  openTest(h.root, "a"); goBack(h.root);
  assert.deepEqual(blanks(h.root), [false, true, false, false, true, false]);
  await h.clickTest();
  assert.equal(h.calls[0].operation, "test");
  assert.equal(Object.hasOwn(h.calls[0], "sections"), false, "re-rendering is a byte-exact no-op");
  const line = h.doc.createElement("div"); line.append("Added.");
  c18Prompt(h.root).append(line); c18Prompt(h.root).listeners.input[0]();
  await h.clickTest();
  assert.deepEqual(h.calls[1].sections, [{ key: h.sections[0].key, text: body + "\nAdded." }]);
  const saved = replaceInstructions(h.source, h.calls[1].sections);
  assert.equal(saved, h.source.replace(body.replaceAll("\n", eol), (body + "\nAdded.").replaceAll("\n", eol)));
});

test("editing a plain check keeps raw authored fields, including an editable judge's minScore 0", async () => {
  const judge = { id: "why", type: "llmJudge", rubric: "Says why.", minScore: 0, judgeModel: "judge-x", judgeVersion: "v1" };
  const ex = [{ id: "a", input: "Name a", assertions: [{ id: "says-hello", type: "contains", value: "Hello, a" }, judge] }];
  const h = await editableCanvas("Reply briefly.", undefined, { examples: ex });
  openTest(h.root, "a");
  assert.equal(byClass(h.root, "check-type-button").length, 2, "the judge is editable too");
  assert.deepEqual(byClass(h.root, "check-type-option").slice(0, 4).map(e => e.textContent), ["Contains", "Not contains", "Regex", "Judge"]);
  assert.doesNotMatch(h.root.textContent, /Model validated/);
  assert.equal(String(judgeField(h.root, 2, "minimum score").value), "0");
  assert.deepEqual(byClass(h.root, "remove-check").map((b) => b.attrs["aria-label"]), ["Remove check 1", "Remove check 2"]);
  edited(byClass(h.root, "check-value")[0], "Hello there, a");
  enterOn(byClass(h.root, "check-value")[0]); await tick();
  assert.equal(h.calls[0].caseId, "a");
  assert.deepEqual(h.calls[0].examples, [{ id: "a", input: "Name a", assertions: [{ id: "says-hello", type: "contains", value: "Hello there, a" }, judge] }]);
});

test("duplicate followed by main Enter saves and tests successfully on the first attempt", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]) });
  openTest(h.root, "a"); click(byClass(h.root, "more")[0]); click(btn(h.root, "Duplicate test"));
  goBack(h.root); promptEnter(h.root); await tick(); await tick();
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].operation, "save-and-test");
  assert.deepEqual(h.calls[0].examples.map((e) => e.id), ["a", "a-copy"]);
  assert.equal(Object.hasOwn(h.calls[0], "caseId"), false);
});

for (const expected of ["Fix$& #218", "Fix$$ #218", "Fix$' #218", "Fix$` #218"]) test(`Apply to prompt keeps the literal operand ${expected}`, async () => {
  const ex = [{ id: "issue", input: "Issue #218", assertions: [{ id: "ref", type: "contains", value: expected }] }];
  const h = await editableCanvas("Keep Closes #218 in notes.", undefined, { examples: ex, latest: (revision, examples) => {
    const latest = c18Latest({ issue: "fail" })(revision, examples);
    latest.result.examples[0].answerExcerpt = "Closes #218.";
    latest.result.examples[0].assertions[0].id = "ref";
    return latest;
  } });
  openTest(h.root, "issue"); click(btn(h.root, "Apply to prompt")); goBack(h.root);
  assert.equal(c18Prompt(h.root).textContent, `Keep ${expected} in notes.`);
});

test("an unsupported prompt edit also greys every known result before repair", async () => {
  const h = await editableCanvas("Original text.", undefined, { currentPass: true });
  const editor = c18Prompt(h.root), unknown = h.doc.createElement("span");
  unknown.textContent = "Unserializable local edit"; editor.append(unknown); editor.listeners.input[0]();
  assert.deepEqual(c18Status(h.root), ["old-pass"]);
  assert.equal(byClass(h.root, "save-run")[0].disabled, true);
  assert.equal(h.calls.length, 0);
});

test("unsupported editor DOM is retained against observation and navigation until repaired", async () => {
  const h = await editableCanvas("Original text.");
  const editor = c18Prompt(h.root), unknown = h.doc.createElement("span");
  unknown.textContent = "Unsupported draft text"; editor.append(unknown);
  editor.listeners.input[0]();
  const external = await viewFor(replaceInstructions(h.source, [{ key: "Accountable Responsibilities", text: "External text." }]), h.examples);
  assert.equal(h.app.idle(), false);
  assert.equal(h.app.observe(external), false);
  assert.equal(c18Prompt(h.root), editor);
  assert.match(editor.textContent, /Unsupported draft text/);
  assert.equal(byClass(h.root, "test-row")[0].disabled, true);
  assert.equal(h.calls.length, 0);
  assert.equal(btn(h.root, "Suggest tests").disabled, true);
  click(btn(h.root, "Suggest tests")); await tick(); await tick();
  assert.equal(h.calls.length, 0, "even a direct action attempt cannot replace the unsupported draft");
  assert.equal(c18Prompt(h.root), editor);
});

test("frontend never relies on array methods of live DOM collections", async () => {
  const app = await readFile(here("app.js"), "utf8");
  assert.doesNotMatch(app, /\.(?:children|childNodes)\??\.(?:map|filter|find|some|every|forEach|flatMap|reduce)\(/);
});

test("ADVERSARIAL detail edit immediately greys a passing check without navigation", async () => {
  const h = await editableCanvas("Reply briefly.", undefined, { examples: c18Examples(["a"]), latest: c18Latest({a: "pass"}) });
  openTest(h.root, "a");
  assert.deepEqual(checkLabels(h.root), ["Passed"]);
  const input = byClass(h.root,"example-input")[0];
  edited(input, "Different question");
  assert.equal(byClass(h.root,"example-input")[0], input, "typing must not replace the focused field");
  assert.equal(byClass(h.root,"unsaved-tag")[0].hidden,false);
  assert.match(c18Text(h.root,"detail-status"), /before your edit/);
  assert.deepEqual(checkLabels(h.root), ["Passed last run"]);
  assert.equal(byClass(h.root, "pass").length, 0);
});
test("ADVERSARIAL detail operand edit immediately greys failure and answer mark", async () => {
  const ex = [{id:"issue",input:"Issue #218",assertions:[{id:"ref",type:"contains",value:"Fixes #218"}]}];
  const h = await editableCanvas("Keep Closes #218.", undefined, {examples:ex, latest:(revision, examples) => {
    const r=c18Latest({issue:"fail"})(revision, examples);
    r.result.examples[0].assertions[0].id="ref"; r.result.examples[0].answerExcerpt="Closes #218."; return r;
  }});
  openTest(h.root,"issue");
  edited(byClass(h.root,"check-value")[0],"Closes #218");
  assert.deepEqual(checkLabels(h.root),["Failed last run"]);
  assert.equal(byClass(h.root,"fail").length,0);
  assert.ok(all(h.root).filter(e=>e.tag==="mark").every(e=>e.className==="old"));
});
test("ADVERSARIAL stale expected/got action cannot rewrite a different check type", async () => {
  const ex = [{id:"issue",input:"Issue #218",assertions:[{id:"ref",type:"contains",value:"Fixes #218"}]}];
  const h = await editableCanvas("Keep Closes #218.", undefined, {examples:ex, latest:(revision, examples) => {
    const r=c18Latest({issue:"fail"})(revision, examples);
    r.result.examples[0].assertions[0].id="ref"; r.result.examples[0].answerExcerpt="Closes #218."; return r;
  }});
  openTest(h.root,"issue");
  const repair = btn(h.root,"Or expect “Closes #218” in this test");
  const apply = btn(h.root,"Apply to prompt");
  edited(byClass(h.root,"example-input")[0],"Already drafted input");
  const select = byClass(h.root,"check-type-button")[0];
  click(select); click(byClass(h.root,"check-type-option")[1]);
  assert.equal(byClass(h.root,"check-type-button")[0],select);
  // The old contains-only repair must not now invert the newly chosen prohibition.
  assert.equal(Boolean(btn(h.root,"Or expect “Closes #218” in this test")), false);
  assert.equal(Boolean(btn(h.root,"Apply to prompt")),false);
  click(repair); click(apply);
  assert.equal(byClass(h.root,"check-value")[0].value,"Fixes #218", "a detached repair cannot mutate its replacement condition");
  assert.equal(select.textContent,"Not contains");
  goBack(h.root); assert.equal(c18Prompt(h.root).textContent,"Keep Closes #218.");
});
test("ADVERSARIAL delayed idle poll cannot overwrite the rerun failure with an older pass", async () => {
  const seed = await editableCanvas("Reply briefly.", undefined, {examples:c18Examples(["a"]),latest:c18Latest({a:"pass"})});
  const oldView=seed.view;
  const failedView=await viewFor(seed.source,seed.examples,c18Latest({a:"fail"}));
  const doc=fakeDoc(), root=doc.createElement("main");
  doc.getElementById=()=>root;doc.visibilityState="visible";
  const before={document:globalThis.document,fetch:globalThis.fetch,setInterval:globalThis.setInterval};
  let poll, release, reads=0;
  globalThis.document=doc;
  globalThis.setInterval=(fn)=>{poll=fn;return 1;};
  globalThis.fetch=async (url)=>{
    if(url==="actions")return {json:async()=>({view:failedView})};
    if(++reads===1)return {json:async()=>oldView};
    return new Promise(resolve=>{release=()=>resolve({json:async()=>oldView});});
  };
  try {
    await import(new URL("../../copilot-wsl/com.github.copilot/extensions/agent-view/app.js?delayed-poll-probe", import.meta.url));
    for(let i=0;i<8;i++)await tick();
    assert.equal(typeof poll,"function");
    assert.equal(summaryOf(root),"1 passing");
    const pendingPoll=poll();await tick();
    click(byClass(root,"save-run")[0]);for(let i=0;i<8;i++)await tick();
    assert.match(summaryOf(root),/^1 failing/);
    release();await pendingPoll;
    assert.match(summaryOf(root),/^1 failing/, "late pre-action GET must not roll the latest result back to passing");
  } finally {
    if(before.document===undefined)delete globalThis.document;else globalThis.document=before.document;
    globalThis.fetch=before.fetch;globalThis.setInterval=before.setInterval;
  }
});
test("ADVERSARIAL any test draft greys other tests from the old file revision", async () => {
  const h=await editableCanvas("Reply briefly.",undefined,{examples:c18Examples(["a","b"]),latest:c18Latest({a:"pass",b:"pass"})});
  openTest(h.root,"b");edited(byClass(h.root,"example-input")[0],"New input");goBack(h.root);
  assert.deepEqual(c18Status(h.root),["old-pass","old-pass"]);
  openTest(h.root,"a");
  assert.deepEqual(checkLabels(h.root),["Passed last run"]);
  assert.equal(byClass(h.root,"unsaved-tag")[0].hidden,true,"A has no draft of its own");
  assert.match(c18Text(h.root,"detail-status"),/before your edit/);
});
for (const operation of ["add", "delete"]) test(`any ${operation} draft greys the other saved tests`, async () => {
  const h=await editableCanvas("Reply briefly.",undefined,{examples:c18Examples(["a","b"]),latest:c18Latest({a:"pass",b:"pass"})});
  if(operation==="add") { click(btn(h.root,"Add test")); goBack(h.root); }
  else { openTest(h.root,"b");click(byClass(h.root,"more")[0]);click(btn(h.root,"Delete test")); }
  assert.equal(items(h.root)[0].attrs["data-status"],"old-pass");
  openTest(h.root,"a");assert.deepEqual(checkLabels(h.root),["Passed last run"]);
});
test("an accepted same-revision observation rebuilds result DOM without losing drafts or claiming just now", async () => {
  const h=await editableCanvas("Reply briefly.",undefined,{examples:c18Examples(["a"]),latest:c18Latest({a:"pass"})});
  openTest(h.root,"a");edited(byClass(h.root,"example-input")[0],"Draft retained");
  const failed=await viewFor(h.source,h.examples,c18Latest({a:"fail"}));
  assert.equal(h.app.observe(failed,h.app.observation()),true);
  assert.equal(byClass(h.root,"example-input")[0].value,"Draft retained");
  assert.deepEqual(checkLabels(h.root),["Failed last run"]);
  assert.doesNotMatch(c18Text(h.root,"detail-status"),/just now/);
});
test("ADVERSARIAL two canvas controllers cannot silently overwrite the first saved revision", async () => {
  const {createActionHandler}=await import("../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs");
  const seed=await editableCanvas("Original prompt.",undefined,{examples:c18Examples(["a"])});
  let source=seed.source,latest={status:"none"};const calls=[];
  const rev=()=>"sha256:"+createHash("sha256").update(source).digest("hex");
  const raw=()=>resource(MINIMAL,{file:{path:MINIMAL,revision:rev()},model:"fake",instructions:source,examples:seed.examples,authoring:{source,editable:true},latest});
  const session={rpc:{mcp:{resources:{read:async({uri})=>({contents:[{uri,text:JSON.stringify(raw())}]})}},tools:{
    getCurrentMetadata:async()=>({tools:["edit","test"].map(op=>({name:op,mcpServerName:SERVER_NAME,mcpToolName:op}))}),
    execute:async req=>{calls.push(req);if(req.name==="edit"){
      if(req.arguments.expectedRevision!==rev())return {resultType:"failure",error:"stale_revision"};
      source=req.arguments.content;latest={status:"stale"};return {resultType:"success"};
    }latest=c18Latest({a:"pass"})(rev(),seed.examples);return {resultType:"success"};}
  }}};
  const panels=["A","B"].map(()=>{const doc=fakeDoc(),root=doc.createElement("main");renderView(doc,root,seed.view,createActionHandler({session,path:MINIMAL}));return root;});
  for(const [i,root] of panels.entries()){const p=c18Prompt(root);p.append(" Draft "+i);p.listeners.input[0]();}
  promptEnter(panels[0]);for(let i=0;i<20;i++)await tick();
  assert.equal(calls.filter(c=>c.name==="edit").length,1);const saved=source;
  promptEnter(panels[1]);for(let i=0;i<20;i++)await tick();
  assert.equal(source,saved);assert.equal(calls.filter(c=>c.name==="edit").length,1);
  assert.match(c18Prompt(panels[1]).textContent,/Draft 1/);
});
test("ADVERSARIAL explicit Keep my edits retains a remotely removed edited test", async () => {
  const h=await editableCanvas("Reply briefly.",undefined,{examples:c18Examples(["a","b"])});
  openTest(h.root,"b");edited(byClass(h.root,"example-input")[0],"My retained input");goBack(h.root);
  const remote=c18Examples(["a"]);
  h.app.observe(await viewFor(replaceExamples(h.source,remote),remote));
  click(btn(h.root,"Keep my edits"));promptEnter(h.root);await tick();
  assert.deepEqual(h.calls[0].examples.map(e=>[e.id,e.input]),[["a","Name a"],["b","My retained input"]]);
});
test("ADVERSARIAL Esc discards only the current test and never the prompt or another draft", async () => {
  const h=await editableCanvas("Reply briefly.",undefined,{examples:c18Examples(["a","b"])});
  c18Edit(h.root);openTest(h.root,"a");edited(byClass(h.root,"example-input")[0],"My A");goBack(h.root);
  openTest(h.root,"b");edited(byClass(h.root,"example-input")[0],"My B");
  byClass(h.root,"example-input")[0].listeners.keydown[0]({key:"Escape",preventDefault(){}});
  goBack(h.root);promptEnter(h.root);await tick();
  assert.equal(h.calls[0].sections.length,1);
  assert.deepEqual(h.calls[0].examples.map(e=>e.input),["My A","Name b"]);
});
