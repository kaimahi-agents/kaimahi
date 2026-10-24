// Agent canvas. Files and previews are accessed only through the
// same session's existing kmx MCP resource and Edit/Run/Test operations.
import { randomBytes, timingSafeEqual, createHash } from "node:crypto";
import { instructionSections, replaceInstructions, replaceExamples, effectiveModel, preRouteRefusal } from "./authoring.mjs";
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
  "invalid-path": "Choose an .agent.md file or a .squad/agents/<member>/charter.md file.",
  unavailable: "The kmx agent builder could not be read. Check that its MCP server is running.",
  "invalid-resource": "The kmx agent view was not in the supported format.",
  "models-unavailable": "Model choices are unavailable. The file/default model can still be run.",
  "too-large": "The kmx agent view was too large to show.",
  "invalid-action": "This canvas action is not supported.",
  "read-only": "This file is available for inspection only. Editing and testing require a complete canonical charter.",
  busy: "Another canvas action is still running.",
  stale_revision: "Changed elsewhere. Your draft is still here; reload before saving or testing.",
  revision_mismatch: "The file changed during testing. These results do not test your draft; reload to continue.",
  permission_required: "Copilot did not permit this action. No permission settings were changed.",
  "action-failed": "The action did not complete. Check the agent and try again only after reviewing its state.",
  "outcome-unknown": "The action outcome could not be confirmed. Do not retry; reload the source first.",
  "no-tests": "No tests yet — add or suggest one",
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

// Preserve the requested path. The MCP server resolves it and checks the target;
// a Windows-hosted extension must not resolve WSL paths on the Windows filesystem.
export function normalizePath(path) {
  if (typeof path === "string" && !path.includes("\0")) {
    const name = path.split("/").at(-1);
    const agent = name.length > ".agent.md".length && name.endsWith(".agent.md");
    const squad = /(?:^|\/)\.squad\/agents\/[^/]+\/charter\.md$/.test(path);
    if (agent || squad) return path;
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
    state: text(c.state), verdict: text(c.verdict), reasonCode: text(c.reasonCode), reason: text(c.reason, c.reasonCode === "copilot_model_unavailable" ? MAX_RESOURCE_BYTES : 500),
    ...(typeof c.answerExcerpt === "string" ? { answerExcerpt: clean(c.answerExcerpt, 2000, true) } : {}),
    truncated: c.truncated === true,
  });
  const sel = isObj(r.selection) ? r.selection : {};
  const route = isObj(r.modelRoute) ? r.modelRoute : {};
  return {
    ...completion(r),
    fullPass: r.fullPass,
    operation: text(r.operation),
    modelRoute: `${text(route.provider, 80)}/${text(route.model, 256)}`,
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
  const authoring = authoringSnapshot(raw.authoring, raw.examples, file.revision);
  return {
    file: { path, revision: file.revision },
    title: clean(raw.title, 200),
    instructions: clean(raw.instructions, 60000, true),
    model,
    examples,
    ...(authoring ? { authoring } : {}),
    latest: { status: latest.status, badge, ...(result ? { result } : {}) },
  };
}

function authoringSnapshot(snapshot, examples, revision) {
  if (!isObj(snapshot) || typeof snapshot.source !== "string" || typeof snapshot.editable !== "boolean") return;
  if (Buffer.byteLength(snapshot.source, "utf8") > 256 * 1024 ||
      `sha256:${createHash("sha256").update(snapshot.source).digest("hex")}` !== revision) return;
  try {
    if (snapshot.editable) {
      instructionSections(snapshot.source);
      // A hash-verified canonical source may hold zero tests, which Go emits as null.
      replaceExamples(snapshot.source, examples ?? []);
    }
    return { source: snapshot.source, editable: snapshot.editable, examples: structuredClone(examples ?? []) };
  } catch { return; } // A malformed authoring snapshot cannot enable writes.
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

const validModel = (model) => typeof model === "string" && /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$/.test(model) && model.toLowerCase() !== "auto" && !model.includes("://");
async function readModels(session) {
  try {
    const response = await session.rpc.mcp.resources.read({ serverName: SERVER_NAME, uri: "kmx://models" });
    if (!Array.isArray(response?.contents) || response.contents.length !== 1 || typeof response.contents[0]?.text !== "string") invalid();
    const body = response.contents[0].text;
    if (Buffer.byteLength(body, "utf8") > MAX_RESOURCE_BYTES) invalid();
    const raw = JSON.parse(body);
    if (raw?.schemaVersion !== 1 || raw.kind !== "kmx.models" || !validModel(raw.defaultModel) || !Array.isArray(raw.models) || !raw.models.every(validModel)) invalid();
    return { defaultModel: raw.defaultModel, models: [...new Set(raw.models)] };
  } catch { throw new ViewError("models-unavailable"); }
}

// The resource keeps only the latest preview and clears it when one starts. Still
// attribute it only for the requested operation and exact full/single-case scope.
function observed(view, operation, ids, caseId, complete, model) {
  const r = view.latest.status === "current" ? view.latest.result : undefined;
  const want = caseId === undefined ? ids : [caseId], got = r?.selection.caseIds ?? [], rows = r?.examples.map((e) => e.id) ?? [];
  return r?.operation === operation && (preRouteRefusal(r) || r.modelRoute.slice(r.modelRoute.indexOf("/") + 1) === model) && r.selection.scope === (caseId === undefined ? "full" : "subset") && r.selection.totalCases === ids.length &&
    new Set(got).size === got.length && got.every((id) => want.includes(id)) && (!complete || got.length === want.length) &&
    new Set(rows).size === rows.length && rows.every((id) => got.includes(id));
}

// The page can choose a declared interaction, never a tool, path or server.
export function createActionHandler({ session, path }) {
  path = normalizePath(path);
  let busy = false, uncertain = false;
  const allowed = {
    "save-and-test": ["expectedRevision", "sections", "examples", "showContent", "caseId", "model"],
    "save-examples": ["expectedRevision", "examples"],
    test: ["expectedRevision", "showContent", "caseId", "model"], run: ["expectedRevision", "input", "showContent", "model"],
    suggest: [], reload: [], models: [],
  };
  async function invoke(operation, args) {
    let metadata;
    try { metadata = await session.rpc.tools.getCurrentMetadata(); }
    catch { throw new ViewError("unavailable"); }
    const tools = metadata.tools?.filter((t) => t.mcpServerName === SERVER_NAME && t.mcpToolName === operation);
    if (tools?.length !== 1) throw new ViewError("unavailable");
    let result;
    try { result = await session.rpc.tools.execute({ name: tools[0].name, arguments: { schemaVersion: 1, path, ...args } }); }
    catch { uncertain = true; throw new ViewError("outcome-unknown"); }
    if (result?.resultType === "timeout") { uncertain = true; throw new ViewError("outcome-unknown"); }
    if (result?.resultType !== "success") {
      const detail = typeof result === "string" ? result : [result?.textResultForLlm, result?.error].filter((s) => typeof s === "string").join("\n");
      const code = ["rejected", "denied"].includes(result?.resultType) ? "permission_required" : /stale_revision/.test(detail) ? "stale_revision" : "action-failed";
      const error = new ViewError(code);
      // Only the structured payload returned by this execute call can attest a
      // failed invocation. A matching older resource is not execution evidence.
      if (operation === "test" || operation === "run") error.preview = result?.structuredContent;
      throw error;
    }
    return result;
  }
  return async (input) => {
    if (!isObj(input) || !Object.hasOwn(allowed, input.operation) || Object.keys(input).some((k) => k !== "operation" && !allowed[input.operation].includes(k))) return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if (input.showContent !== undefined && typeof input.showContent !== "boolean") return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if (input.model !== undefined && input.model !== "" && !validModel(input.model)) return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    // Read-only discovery must not block or unlock an execution action.
    if (input.operation === "models") {
      try { return await readModels(session); } catch (e) { const error = safeError(e); return { error: error.code, message: error.message }; }
    }
    if (busy) return { error: "busy", message: MESSAGES.busy };
    busy = true;
    let saved = false, tested = false, targetRevision, ids, operation, model;
    try {
      if (uncertain && input.operation !== "reload") throw new ViewError("outcome-unknown");
      let view = await readView(session, path);
      if (input.operation === "reload") { uncertain = false; return { view }; }
      if (!view.authoring?.editable) throw new ViewError("read-only");
      if (input.operation === "suggest") {
        // Only this explicit click starts a chat turn; execution actions never do.
        if (typeof session.send !== "function") throw new ViewError("unavailable");
        await session.send(`Suggest additional examples for ${JSON.stringify(path)} at revision ${view.file.revision}. Show proposals in chat; do not edit files or run tests. Use only this agent's kmx resource if context is needed.`);
        return { message: "Asked Copilot to suggest examples in chat." };
      }
      if (typeof input.expectedRevision !== "string" || !/^sha256:[0-9a-f]{64}$/.test(input.expectedRevision)) throw new ViewError("invalid-action");
      if (input.expectedRevision !== view.file.revision) throw new ViewError("stale_revision");
      targetRevision = input.expectedRevision;
      model = effectiveModel(view.model, input.model);
      const saving = input.operation === "save-and-test" || input.operation === "save-examples";
      // Intended drafts apply to the exact current source; an absent draft is a no-op.
      let content = view.authoring.source, examples = view.authoring.examples;
      try {
        if (input.sections !== undefined) content = replaceInstructions(content, input.sections);
        if (input.examples !== undefined || input.operation === "save-examples") {
          content = replaceExamples(content, input.examples);
          examples = input.examples;
        }
        instructionSections(content);
      } catch { throw new ViewError("invalid-action"); }
      ids = examples.map((e) => e.id);
      // A selected case must exist once in the intended examples before any write.
      if (input.caseId !== undefined && (typeof input.caseId !== "string" || ids.filter((id) => id === input.caseId).length !== 1)) throw new ViewError("invalid-action");
      if (saving) {
        if (Buffer.byteLength(content, "utf8") > 256 * 1024) throw new ViewError("too-large");
        if (content !== view.authoring.source) {
          await invoke("edit", { expectedRevision: input.expectedRevision, content });
          targetRevision = `sha256:${createHash("sha256").update(content).digest("hex")}`;
          try { view = await readView(session, path); }
          catch { uncertain = true; throw new ViewError("outcome-unknown"); }
          if (view.file.revision !== targetRevision) { uncertain = true; throw new ViewError("outcome-unknown"); }
          saved = true;
        }
        if (input.operation === "save-examples") return { view, saved };
      }
      operation = input.operation === "run" ? "run" : "test";
      if (operation === "run" && (typeof input.input !== "string" || Buffer.byteLength(input.input, "utf8") > 16384)) throw new ViewError("invalid-action");
      // Test/Run have no revision parameter. Check the actual result afterward,
      // not a fabricated precondition or a refreshed draft revision.
      tested = true;
      await invoke(operation, { ...(input.model ? { model: input.model } : {}), ...(operation === "run" ? { input: input.input } : {}), ...(input.caseId !== undefined ? { caseId: input.caseId } : {}), showContent: input.showContent === true, showFailedContent: true });
      try { view = await readView(session, path); }
      catch { uncertain = true; throw new ViewError("outcome-unknown"); }
      if (view.file.revision !== targetRevision) throw new ViewError("revision_mismatch");
      if (!observed(view, operation, ids, input.caseId, true, model)) { uncertain = true; throw new ViewError("outcome-unknown"); }
      return { view, saved };
    } catch (e) {
      const err = safeError(e);
      const response = { error: err.code, message: err.message, saved };
      const failedPreview = tested && err.code === "action-failed";
      if ((saved || failedPreview) && err.code !== "revision_mismatch" && err.code !== "outcome-unknown") {
        try {
          const view = await readView(session, path);
          // Project the invocation payload through the same bounded parser as
          // the resource, then require exact agreement before attaching a view.
          const proof = err.preview?.file?.path === path ? cleanResult(err.preview, "current", targetRevision) : undefined;
          const fresh = failedPreview && proof && observed(view, operation, ids, input.caseId, false, model)
            && JSON.stringify(proof) === JSON.stringify(view.latest.result)
            && view.latest.result.verdict !== "pass" && !view.latest.result.fullPass;
          if (view.file.revision === targetRevision && (fresh || saved && !failedPreview && view.latest.status !== "current")) response.view = view;
          if (response.view && fresh && proof.reasonCode === "copilot_model_unavailable") response.message = proof.reason;
          if (response.view && fresh && !ids.length && view.latest.result.reasonCode === "no_tests") Object.assign(response, { error: "no-tests", message: MESSAGES["no-tests"] });
        } catch { /* Preserve the draft when refresh cannot be confirmed. */ }
      }
      if (failedPreview && !response.view) {
        uncertain = true;
        Object.assign(response, { error: "outcome-unknown", message: MESSAGES["outcome-unknown"] });
      }
      return response;
    } finally { busy = false; }
  };
}

// --- plain-text fallback ---------------------------------------------------
export function formatView(view) {
  const one = (s, max) => clean(String(s).replace(/\s+/g, " "), max);
  const { status, badge, result } = view.latest;
  const summary = status === "none" ? "Latest: none (no current Run/Test result)"
    : status === "stale" ? "Latest: STALE (from an earlier revision; not current)"
    : `Latest: current, ${badge === "pass" ? "PASS" : "NOT PASSING"} (${one(result.state, 40)}, verdict ${one(result.verdict, 40)}) · runtime model: ${result.modelRoute === "/" ? "not selected" : one(result.modelRoute, 340)}`;
  const out = [
    `Agent view: ${one(view.file.path, 120)} (${one(view.file.revision, 19)})`,
    `Title: ${one(view.title, 120)}`,
    `Authored model: ${one(view.model.replace(/\n/g, "; "), 160) || "(not set)"}`,
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
  "authoring.mjs": ["authoring.mjs", "text/javascript; charset=utf-8"],
  "editor.mjs": ["editor.mjs", "text/javascript; charset=utf-8"],
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
  const act = createActionHandler({ session, path });
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
      const [, candidate = "", ...rest] = (req.url ?? "").split("?")[0].split("/");
      const a = Buffer.from(candidate);
      const b = Buffer.from(token);
      if (a.length !== b.length || !timingSafeEqual(a, b) || (req.url ?? "")[0] !== "/") return refuse(404, "Not found");
      const name = rest.join("/");
      if (name === "actions" && req.method === "POST") {
        if (origin !== `http://${host}` || !/^application\/json(?:;|$)/i.test(req.headers["content-type"] ?? "")) return refuse(403, "Forbidden");
        const chunks = []; let bytes = 0;
        for await (const chunk of req) {
          bytes += chunk.length;
          if (bytes > MAX_RESOURCE_BYTES) return refuse(413, "Too large");
          chunks.push(chunk);
        }
        let input;
        try { input = JSON.parse(Buffer.concat(chunks).toString("utf8")); }
        catch { return reply(400, "application/json", JSON.stringify({ error: "invalid-action", message: MESSAGES["invalid-action"] })); }
        const result = await act(input);
        return reply(result.error ? 409 : 200, "application/json; charset=utf-8", JSON.stringify(result));
      }
      if (req.method !== "GET") return refuse(405, "Method not allowed", { Allow: "GET, POST" });
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
      return { url: bridge.url, title: "Agent view", status: "Agent file" };
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
