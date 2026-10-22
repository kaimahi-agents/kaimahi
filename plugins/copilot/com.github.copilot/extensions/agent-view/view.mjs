// Read-only agent view: validates the kmx `kmx://agent/{path}` resource and
// serves it to the bundled canvas page through a token-protected loopback
// server. Nothing here writes files, runs processes or sends model prompts.
import { randomBytes, timingSafeEqual } from "node:crypto";
import { readFile } from "node:fs/promises";
import http from "node:http";

export const CANVAS_ID = "kmx-agent-view";
export const SERVER_NAME = "kmx-agent-builder";
const KIND = "kmx.agent-view";
const MAX_RESOURCE_BYTES = 2 * 1024 * 1024;
const MAX_ITEMS = 128;
const MAX_TEXT = 6000;
const TRUNCATED = "… [truncated]";

const MESSAGES = {
  "invalid-path": "Path must be <id>.agent.md or .squad/agents/<id>/charter.md under the confirmed root.",
  unavailable: "The kmx agent builder could not be read. Check that its MCP server is running.",
  "invalid-resource": "The kmx agent view was not in the supported format.",
  "too-large": "The kmx agent view was too large to show.",
};

export class ViewError extends Error {
  constructor(code) {
    super(MESSAGES[code] ?? MESSAGES.unavailable);
    this.name = "ViewError";
    this.code = MESSAGES[code] ? code : "unavailable";
  }
}
const invalid = () => {
  throw new ViewError("invalid-resource");
};
const safeError = (e) => (e instanceof ViewError ? e : new ViewError("unavailable"));

// Mirrors the server's portable-path rule; the server stays the authority.
const AGENT_ID = /^[a-z][a-z0-9-]{0,62}$/;
const RESERVED = new Set(["con", "prn", "aux", "nul", ..."123456789".split("").flatMap((n) => [`com${n}`, `lpt${n}`])]);
const safeId = (s) => AGENT_ID.test(s) && !RESERVED.has(s);

export function normalizePath(path) {
  if (typeof path === "string") {
    const parts = path.split("/");
    const flat = parts.length === 1 && path.endsWith(".agent.md") && safeId(path.slice(0, -".agent.md".length));
    const squad = parts.length === 4 && parts[0] === ".squad" && parts[1] === "agents" && safeId(parts[2]) && parts[3] === "charter.md";
    if (flat || squad) return path;
  }
  throw new ViewError("invalid-path");
}
export const resourceUri = (path) => `kmx://agent/${encodeURIComponent(normalizePath(path))}`;

// --- sanitising ------------------------------------------------------------
// Strip controls (keeping \n and \t) and bidi overrides, and bound length.
const CONTROL = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u200e\u200f\u202a-\u202e\u2066-\u2069\ufeff]/g;
function clean(value, max = 500, multiline = false) {
  let s = String(value).replace(/\r\n?/g, "\n").replace(CONTROL, "");
  if (!multiline) s = s.replace(/[\n\t]+/g, " ");
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  return (/[\ud800-\udbff]$/.test(cut) ? cut.slice(0, -1) : cut) + TRUNCATED;
}
const text = (v, max = 300) => (typeof v === "string" ? clean(v, max) : "");
const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const list = (v, max = MAX_ITEMS) => {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || v.length > max) invalid();
  return v;
};
const show = (v, max) => clean(typeof v === "string" ? v : JSON.stringify(v) ?? "", max, true);
// Authored extra fields as bounded [name, value] pairs; names are restricted.
function fields(obj, skip = []) {
  return Object.entries(obj)
    .filter(([k]) => !skip.includes(k) && /^[A-Za-z][A-Za-z0-9_.-]{0,39}$/.test(k))
    .slice(0, 12)
    .map(([k, v]) => [k, show(v, 1000)]);
}

function cleanResult(r, status, revision) {
  if (!isObj(r) || typeof r.state !== "string" || typeof r.verdict !== "string" || typeof r.fullPass !== "boolean") invalid();
  if (r.schemaVersion !== 1 || r.kind !== "kmx.agent-file-preview" || !isObj(r.file) || r.file.revision !== revision) invalid();
  const completion = (c) => ({
    state: text(c.state), verdict: text(c.verdict), reasonCode: text(c.reasonCode), reason: text(c.reason, 500),
    ...(typeof c.answerExcerpt === "string" ? { answerExcerpt: clean(c.answerExcerpt, 2000, true) } : {}),
    truncated: c.truncated === true,
  });
  const sel = isObj(r.selection) ? r.selection : {};
  const route = isObj(r.modelRoute) ? r.modelRoute : {};
  return {
    ...completion(r),
    fullPass: r.fullPass,
    operation: text(r.operation),
    modelRoute: `${text(route.provider, 80)}/${text(route.model, 120)}`,
    selection: { scope: text(sel.scope, 40), totalCases: Number.isInteger(sel.totalCases) ? sel.totalCases : 0, caseIds: list(sel.caseIds).map((i) => text(i, 80)) },
    examples: list(r.examples).map((e) => {
      if (!isObj(e)) invalid();
      return {
        id: text(e.id, 80), ...completion(e),
        assertions: list(e.assertions).map((a) => {
          if (!isObj(a)) invalid();
          return { id: text(a.id, 80), type: text(a.type, 80), verdict: text(a.verdict), reasonCode: text(a.reasonCode), reason: text(a.reason, 500) };
        }),
      };
    }),
    ...(isObj(r.adHoc) ? { adHoc: completion(r.adHoc) } : {}),
  };
}

function parseView(json, path) {
  let raw;
  try {
    raw = JSON.parse(json);
  } catch {
    invalid();
  }
  if (!isObj(raw) || raw.schemaVersion !== 1 || raw.kind !== KIND) invalid();
  const file = raw.file;
  if (!isObj(file) || file.path !== path || typeof file.revision !== "string" || !/^sha256:[0-9a-f]{64}$/.test(file.revision)) invalid();
  if (typeof raw.title !== "string" || typeof raw.instructions !== "string") invalid();
  let model;
  if (typeof raw.model === "string") model = clean(raw.model, 300);
  else if (isObj(raw.model)) model = fields(raw.model).map(([k, v]) => `${k}: ${v}`).join("\n");
  else invalid();
  const examples = list(raw.examples).map((e) => {
    if (!isObj(e) || typeof e.id !== "string") invalid();
    return {
      id: clean(e.id, 80),
      input: show(e.input ?? "", 2000),
      assertions: list(e.assertions).map((a) => {
        if (!isObj(a) || typeof a.id !== "string" || typeof a.type !== "string") invalid();
        return { id: clean(a.id, 80), type: clean(a.type, 80), fields: fields(a, ["id", "type"]) };
      }),
    };
  });
  const latest = raw.latest;
  if (!isObj(latest) || !["none", "current", "stale"].includes(latest.status)) invalid();
  if ((latest.status === "current") !== (latest.result !== undefined)) invalid();
  const result = latest.status === "current" ? cleanResult(latest.result, latest.status, file.revision) : undefined;
  const badge = latest.status !== "current" ? latest.status : result.fullPass && result.state === "complete" && result.selection.scope === "full" && result.verdict === "pass" ? "pass" : "not-passing";
  return {
    file: { path, revision: file.revision },
    title: clean(raw.title, 200),
    instructions: clean(raw.instructions, 60000, true),
    model,
    examples,
    latest: { status: latest.status, badge, ...(result ? { result } : {}) },
  };
}

export async function readView(session, rawPath) {
  const path = normalizePath(rawPath);
  const read = session?.rpc?.mcp?.resources?.read;
  if (typeof read !== "function") throw new ViewError("unavailable");
  let response;
  try {
    response = await session.rpc.mcp.resources.read({ serverName: SERVER_NAME, uri: resourceUri(path) });
  } catch {
    throw new ViewError("unavailable"); // never surface raw transport errors
  }
  const contents = response?.contents;
  if (!Array.isArray(contents) || contents.length !== 1 || typeof contents[0]?.text !== "string") invalid();
  const body = contents[0].text;
  if (Buffer.byteLength(body, "utf8") > MAX_RESOURCE_BYTES) throw new ViewError("too-large");
  if (typeof body.isWellFormed === "function" && !body.isWellFormed()) invalid();
  return parseView(body, path);
}

// --- plain-text fallback ---------------------------------------------------
export function formatView(view) {
  const one = (s, max) => clean(String(s).replace(/\s+/g, " "), max);
  const { status, badge, result } = view.latest;
  const summary = status === "none" ? "Latest: none (no current Run/Test result)"
    : status === "stale" ? "Latest: STALE (from an earlier revision; not current)"
    : `Latest: current, ${badge === "pass" ? "PASS" : "NOT PASSING"} (${one(result.state, 40)}, verdict ${one(result.verdict, 40)})`;
  const out = [
    `Agent view: ${one(view.file.path, 120)} (${one(view.file.revision, 19)})`,
    `Title: ${one(view.title, 120)}`,
    `Model: ${one(view.model.replace(/\n/g, "; "), 160)}`,
    // Keep the bounded status ahead of authored details that may be truncated.
    summary,
    "Instructions:",
    "  " + clean(view.instructions, 1200, true).replace(/\n/g, "\n  "),
    `Examples (${view.examples.length}):`,
  ];
  for (const e of view.examples) {
    out.push(`  - ${one(e.id, 60)}: input: ${one(e.input, 120)}`);
    for (const a of e.assertions) {
      out.push(`      expect ${one(a.id, 60)} [${one(a.type, 40)}] ${one(a.fields.map(([k, v]) => `${k}=${v}`).join(" "), 160)}`.trimEnd());
    }
  }
  if (status === "current") {
    for (const e of result.examples) {
      out.push(`  - ${one(e.id, 60)}: ${one(e.verdict, 20)} (${one(e.state, 30)})`);
      if (e.answerExcerpt !== undefined) out.push(`      untrusted model answer: ${one(e.answerExcerpt, 200)}`);
    }
  }
  const full = out.join("\n");
  const marker = "\n[output truncated]";
  return full.length <= MAX_TEXT ? full : full.slice(0, MAX_TEXT - marker.length) + marker;
}

// --- loopback bridge -------------------------------------------------------
const ASSETS = {
  "": ["index.html", "text/html; charset=utf-8"],
  "app.js": ["app.js", "text/javascript; charset=utf-8"],
  "style.css": ["style.css", "text/css; charset=utf-8"],
};
const SECURITY_HEADERS = {
  "Cache-Control": "no-store",
  "Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; base-uri 'none'; form-action 'none'",
  "Cross-Origin-Resource-Policy": "same-origin",
  "Referrer-Policy": "no-referrer",
  "X-Content-Type-Options": "nosniff",
};

export async function startViewBridge({ session, path }) {
  path = normalizePath(path);
  const token = randomBytes(24).toString("base64url");
  let port = 0;
  const server = http.createServer(async (req, res) => {
    const reply = (status, type, body, extra = {}) => {
      res.writeHead(status, { ...SECURITY_HEADERS, ...extra, "Content-Type": type, "Content-Length": Buffer.byteLength(body) });
      res.end(body);
    };
    const refuse = (status, message, extra) => reply(status, "text/plain; charset=utf-8", message, extra);
    try {
      const { host, origin } = req.headers;
      if (host !== `127.0.0.1:${port}` && host !== `localhost:${port}`) return refuse(403, "Forbidden");
      if (origin !== undefined && origin !== `http://127.0.0.1:${port}` && origin !== `http://localhost:${port}`) return refuse(403, "Forbidden");
      if (req.method !== "GET") return refuse(405, "Method not allowed", { Allow: "GET" });
      const [, candidate = "", ...rest] = (req.url ?? "").split("?")[0].split("/");
      const a = Buffer.from(candidate);
      const b = Buffer.from(token);
      if (a.length !== b.length || !timingSafeEqual(a, b) || (req.url ?? "")[0] !== "/") return refuse(404, "Not found");
      const name = rest.join("/");
      if (name === "view.json") {
        try {
          return reply(200, "application/json; charset=utf-8", JSON.stringify(await readView(session, path)));
        } catch (e) {
          const err = safeError(e);
          return reply(502, "application/json; charset=utf-8", JSON.stringify({ error: err.code, message: err.message }));
        }
      }
      const asset = Object.hasOwn(ASSETS, name) ? ASSETS[name] : undefined;
      if (!asset) return refuse(404, "Not found");
      return reply(200, asset[1], await readFile(new URL(asset[0], import.meta.url), "utf8"));
    } catch {
      if (!res.headersSent) refuse(500, "Unavailable");
      else res.destroy();
    }
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  port = server.address().port;
  server.unref();
  let closing;
  const close = () =>
    (closing ??= new Promise((resolve) => {
      server.close(() => resolve());
      server.closeAllConnections();
    }));
  return { url: `http://127.0.0.1:${port}/${token}/`, port, close };
}

// --- canvas/command lifecycle ----------------------------------------------
export function createViewController({ getSession }) {
  const instances = new Map(); // instanceId -> { path, ready }
  const stop = (entry) => entry.ready.then((b) => b.close(), () => {});

  async function open(ctx) {
    const input = ctx?.input;
    if (!isObj(input) || Object.keys(input).some((k) => k !== "path")) throw new ViewError("invalid-path");
    const path = normalizePath(input.path);
    const id = ctx.instanceId;
    if (typeof id !== "string" || !id) throw new ViewError("invalid-path");
    const existing = instances.get(id);
    let entry = existing;
    if (existing?.path !== path) {
      const session = getSession();
      if (!session) throw new ViewError("unavailable");
      entry = { path, ready: startViewBridge({ session, path }) };
      instances.set(id, entry);
      entry.ready.catch(() => instances.get(id) === entry && instances.delete(id));
      if (existing) await stop(existing);
    }
    try {
      const bridge = await entry.ready;
      if (instances.get(id) !== entry) throw new ViewError("unavailable");
      return { url: bridge.url, title: "Agent view", status: "Read-only" };
    } catch (e) {
      throw safeError(e);
    }
  }
  async function close(ctx) {
    const entry = instances.get(ctx?.instanceId);
    instances.delete(ctx?.instanceId);
    if (entry) await stop(entry);
  }
  async function closeAll() {
    const all = [...instances.values()];
    instances.clear();
    await Promise.all(all.map(stop));
  }
  async function command(ctx) {
    const session = getSession();
    if (!session) return;
    try {
      const view = await readView(session, normalizePath(String(ctx?.args ?? "").trim()));
      await session.log(formatView(view));
    } catch (e) {
      await session.log(safeError(e).message, { level: "error" });
    }
  }
  return { open, close, closeAll, command, get size() { return instances.size; } };
}
