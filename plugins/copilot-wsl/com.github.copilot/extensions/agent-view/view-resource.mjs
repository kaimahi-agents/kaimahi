// The kmx agent resource: bounded parsing of views and results, plus shared errors and limits.
import { createHash } from "node:crypto";
import { instructionSections, replaceExamples, validateExamples, preRouteRefusal, hasExamples, COPILOT_TESTS_NOTICE } from "./authoring.mjs";
import { previewServer, HostRuntimeUnavailable } from "./host-relay.mjs";

export const CANVAS_ID = "kmx-agent-view";
export const SERVER_NAME = "kmx-agent-builder";
const KIND = "kmx.agent-view";
export const MAX_RESOURCE_BYTES = 2 * 1024 * 1024;
const MAX_ITEMS = 128;
export const MAX_TEXT = 6000;
const TRUNCATED = "… [truncated]";

export const MESSAGES = {
  "invalid-path": "Choose an .agent.md file or a .squad/agents/<member>/charter.md file.",
  unavailable: "The kmx agent builder could not be read. Check that its MCP server is running.",
  "invalid-resource": "The kmx agent view was not in the supported format.",
  "invalid-file": "This agent file needs fixing before it can open. Check its format and test fields.",
  "not-found": "File not found. Check the name, or give the full path.",
  "models-unavailable": "Model choices are unavailable. Your model selection is unchanged.",
  "too-large": "The kmx agent view was too large to show.",
  "invalid-action": "This canvas action is not supported.",
  "read-only": "This file is available for inspection only. The builder did not provide an editable snapshot of it.",
  busy: "Another canvas action is still running.",
  stale_revision: "Changed elsewhere. Your draft is still here; reload before saving or testing.",
  stale_settings_revision: "Tool settings changed elsewhere. Your draft is still here; compare the new settings before saving.",
  "settings-failed": "The tool settings could not be saved. Your draft is still here; tests did not run.",
  revision_mismatch: "The file changed during testing. These results do not test your draft; reload to continue.",
  permission_required: "Copilot did not permit this action. No permission settings were changed.",
  "action-failed": "The action couldn't be confirmed. Reload the saved file before trying again.",
  "outcome-unknown": "The action outcome could not be confirmed. Do not retry; reload the source first.",
  "no-tests": "No tests yet — add or suggest one",
  tool_settings_changed: "Tool settings changed. Reload before comparing.",
  confirmation_required: COPILOT_TESTS_NOTICE,
  invalid_tool_settings: "The tool settings in kmx.yaml are invalid. Fix them before running this agent.",
  paired_source_required: "Paired test files are not supported in this canvas.",
};

export class ViewError extends Error {
  constructor(code, message = MESSAGES[code] ?? MESSAGES.unavailable) {
    super(message);
    this.name = "ViewError";
    this.code = MESSAGES[code] ? code : "unavailable";
  }
}
export const invalid = () => {
  throw new ViewError("invalid-resource");
};
export const safeError = (e) => e instanceof ViewError ? e : /\binvalid_tool_settings\b/.test(e?.message) ? new ViewError("invalid_tool_settings") : new ViewError("unavailable",
  e instanceof HostRuntimeUnavailable ? "Host tools are unavailable in this session. Start a new Copilot session." : MESSAGES.unavailable);

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
export function clean(value, max = 500, multiline = false) {
  let s = String(value).replace(/\r\n?/g, "\n").replace(CONTROL, "");
  if (!multiline) s = s.replace(/[\n\t]+/g, " ");
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  return (/[\ud800-\udbff]$/.test(cut) ? cut.slice(0, -1) : cut) + TRUNCATED;
}
export const text = (v, max = 300) => (typeof v === "string" ? clean(v, max) : "");
export const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
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

// Per-run state of each declared folder tool; only these two values are known.
const TOOL_NAME = /^[A-Za-z_][A-Za-z0-9_-]{0,63}$/;
export function cleanSettings(value) {
  if (!Array.isArray(value) || value.length > 32) invalid();
  const seen = new Set();
  return value.map((s) => {
    if (!isObj(s) || Object.keys(s).length !== 2 || typeof s.name !== "string" || !TOOL_NAME.test(s.name)
      || !["Enabled", "Disabled"].includes(s.state) || seen.has(s.name)) invalid();
    seen.add(s.name);
    return { name: s.name, state: s.state };
  });
}
const settingsMatch = (settings, tools) => settings.length === tools.length && settings.every((s, i) => s.name === tools[i]);

function cleanJudge(value, verdict) {
  if (!isObj(value) || !Number.isInteger(value.minScore) || value.minScore < 0 || value.minScore > 100
    || !Array.isArray(value.scores) || value.scores.length !== 2 || !value.scores.every(s => Number.isInteger(s) && s >= 0 && s <= 100)) return;
  const expected = value.scores.every(s => s >= value.minScore) ? "pass" : value.scores.every(s => s < value.minScore) ? "fail" : "unknown";
  if (verdict !== expected) return;
  const reasons = Array.isArray(value.reasons) && value.reasons.length <= 2 && value.reasons.every(r => typeof r === "string")
    ? value.reasons.map(r => clean(r, 2000, true)) : [];
  return { scores: [...value.scores], minScore: value.minScore, reasons };
}

export function cleanResult(r, status, revision) {
  if (!isObj(r) || typeof r.state !== "string" || typeof r.verdict !== "string" || typeof r.fullPass !== "boolean") invalid();
  if (r.schemaVersion !== 1 || r.kind !== "kmx.agent-file-preview" || !isObj(r.file) || r.file.revision !== revision) invalid();
  const completion = (c) => ({
    state: text(c.state), verdict: text(c.verdict), reasonCode: text(c.reasonCode), reason: text(c.reason, c.reasonCode === "copilot_model_unavailable" ? MAX_RESOURCE_BYTES : 500),
    ...(typeof c.answerExcerpt === "string" ? { answerExcerpt: clean(c.answerExcerpt, 2000, true) } : {}),
    truncated: c.truncated === true,
    ...(c.toolCalls !== undefined ? { toolCalls: c.toolCalls } : {}),
    ...(Array.isArray(c.tools) ? { tools: c.tools.map(t => {
      if (!isObj(t) || !TOOL_NAME.test(t.name) || !["live", "recorded"].includes(t.mode) || !["live", "recorded", "unavailable"].includes(t.source) || !Number.isInteger(t.callCount) || t.callCount < 0) invalid();
      return { name: t.name, mode: t.mode, source: t.source, callCount: t.callCount, ...(t.recordingSaved === true ? { recordingSaved: true } : {}), ...(Number.isInteger(t.requestCount) && t.requestCount >= t.callCount ? { requestCount: t.requestCount } : {}) };
    }) } : {}),
  });
  const sel = isObj(r.selection) ? r.selection : {};
  const route = isObj(r.modelRoute) ? r.modelRoute : {};
  return {
    ...completion(r),
    fullPass: r.fullPass,
    ...(r.savedRevision !== undefined ? { savedRevision: /^sha256:[a-f0-9]{64}$/.test(r.savedRevision) ? r.savedRevision : invalid() } : {}),
    ...(r.recordedReplayOnly === true ? { recordedReplayOnly: true } : {}),
    operation: text(r.operation),
    modelRoute: `${text(route.provider, 80)}/${text(route.model, 256)}`,
    selection: { scope: text(sel.scope, 40), totalCases: Number.isInteger(sel.totalCases) ? sel.totalCases : 0, caseIds: list(sel.caseIds).map((i) => text(i, 80)) },
    examples: list(r.examples).map((e) => {
      if (!isObj(e)) invalid();
      return {
        id: text(e.id, 80), ...completion(e),
        assertions: list(e.assertions).map((a) => {
          if (!isObj(a)) invalid();
          const judge = a.type === "llmJudge" ? cleanJudge(a.judge, a.verdict) : undefined;
          return { id: text(a.id, 80), type: text(a.type, 80), verdict: text(a.verdict), reasonCode: text(a.reasonCode), reason: text(a.reason, 500), ...(judge ? { judge } : {}) };
        }),
      };
    }),
    ...(isObj(r.adHoc) ? { adHoc: completion(r.adHoc) } : {}),
    // Absent means an older result that recorded no settings; never invent them.
    ...(r.toolSettings !== undefined ? { toolSettings: cleanSettings(r.toolSettings) } : {}),
  };
}

function parseView(json, path) {
  let raw;
  try {
    raw = JSON.parse(json);
  } catch {
    invalid();
  }
  if (raw?.schemaVersion === 1 && raw.kind === "kmx.agent-view-error" && ["invalid_tool_settings", "paired_source_required"].includes(raw.reasonCode)) throw new ViewError(raw.reasonCode);
  if (raw?.schemaVersion === 1 && raw.kind === "kmx.agent-view-error" && raw.reasonCode === "invalid_file") throw new ViewError("invalid-file");
  if (raw?.schemaVersion === 1 && raw.kind === "kmx.agent-view-error" && raw.reasonCode === "not_found") throw new ViewError("not-found");
  if (!isObj(raw) || raw.schemaVersion !== 1 || raw.kind !== KIND) invalid();
  const file = raw.file;
  if (!isObj(file) || file.path !== path || typeof file.revision !== "string" || !/^sha256:[0-9a-f]{64}$/.test(file.revision)) invalid();
  if (typeof raw.title !== "string" || typeof raw.instructions !== "string") invalid();
  let model;
  if (typeof raw.model === "string") model = clean(raw.model, 300);
  else if (isObj(raw.model)) model = fields(raw.model).map(([k, v]) => `${k}: ${v}`).join("\n");
  else invalid();
  const tools = raw.tools ?? [];
  if (!Array.isArray(tools) || tools.length > 32 || new Set(tools).size !== tools.length || tools.some(t => typeof t !== "string" || !TOOL_NAME.test(t))) invalid();
  // An older server has no settings: every declared tool is Enabled by default.
  const toolSettings = raw.toolSettings === undefined ? tools.map((name) => ({ name, state: "Enabled" })) : cleanSettings(raw.toolSettings);
  if (!settingsMatch(toolSettings, tools)) invalid();
  const token = raw.toolSettingsRevision;
  if (token !== undefined && (typeof token !== "string" || !/^(?:absent|sha256:[a-f0-9]{64})$/.test(token))) invalid();
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
  // Raw, complete Copilot header (shown read-only) and the omitted-tools flag.
  if (raw.frontmatter !== undefined && typeof raw.frontmatter !== "string" || raw.copilotToolsOmitted !== undefined && typeof raw.copilotToolsOmitted !== "boolean") invalid();
  const latest = raw.latest;
  if (!isObj(latest) || !["none", "current", "stale"].includes(latest.status)) invalid();
  if ((latest.status === "current") !== (latest.result !== undefined)) invalid();
  const result = latest.status === "current" ? cleanResult(latest.result, latest.status, file.revision) : undefined;
  if (result?.toolSettings && !settingsMatch(result.toolSettings, tools)) invalid();
  const badge = latest.status !== "current" ? latest.status : result.fullPass && result.state === "complete" && result.selection.scope === "full" && result.verdict === "pass" ? "pass" : "not-passing";
  const authoring = authoringSnapshot(raw.authoring, raw.examples, file.revision);
  return {
    file: { path, revision: file.revision },
    title: clean(raw.title, 200),
    instructions: clean(raw.instructions, 60000, true),
    model, tools, toolSettings,
    ...(raw.frontmatter ? { frontmatter: clean(raw.frontmatter, 20000, true) } : {}),
    ...(raw.copilotToolsOmitted ? { copilotToolsOmitted: true } : {}),
    ...(token !== undefined ? { toolSettingsRevision: token } : {}),
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
      // Tests the editor cannot find in the source must never be rewritten blind.
      if ((examples ?? []).length && !hasExamples(snapshot.source)) return;
      // A hash-verified canonical source may hold zero tests, which Go emits as null.
      if (snapshot.recordingControls === true) validateExamples(examples ?? [], true);
      else replaceExamples(snapshot.source, examples ?? []);
    }
    return { source: snapshot.source, editable: snapshot.editable, examples: structuredClone(examples ?? []), ...(snapshot.recordingControls === true ? { recordingControls: true, recordingDetails: list(snapshot.recordingDetails, MAX_ITEMS * 32).map(d => { if (!isObj(d) || typeof d.exampleId !== "string" || !TOOL_NAME.test(d.name) || typeof d.text !== "string") invalid(); return {exampleId:d.exampleId,name:d.name,text:d.text}; }) } : {}) };
  } catch { return; } // A malformed authoring snapshot cannot enable writes.
}

export async function readView(session, rawPath) {
  const path = normalizePath(rawPath);
  const read = session?.rpc?.mcp?.resources?.read;
  if (typeof read !== "function") throw new ViewError("unavailable");
  const chat = readChatModel(session);
  let response;
  try {
    response = await session.rpc.mcp.resources.read({ serverName: previewServer(session), uri: resourceUri(path) });
  } catch (e) {
    throw safeError(e); // never surface raw transport errors
  }
  const contents = response?.contents;
  if (!Array.isArray(contents) || contents.length !== 1 || typeof contents[0]?.text !== "string") invalid();
  const body = contents[0].text;
  if (Buffer.byteLength(body, "utf8") > MAX_RESOURCE_BYTES) throw new ViewError("too-large");
  if (typeof body.isWellFormed === "function" && !body.isWellFormed()) invalid();
  const view = parseView(body, path), metadata = await chat;
  return { ...view, ...(metadata.model ? { chatModel: metadata.model } : {}), ...(metadata.name ? { chatModelName: metadata.name } : {}) };
}

// Optional joined-session SDK metadata, not catalog/auth discovery. Keep the
// last valid observation through throws; an explicit unknown clears it. The
// newest completed observation wins, including explicit invalid/auto values.
const chatModels = new WeakMap();
async function readChatModel(session) {
  let state = chatModels.get(session);
  if (!state) { state = { generation: 0, resolved: 0 }; chatModels.set(session, state); }
  const generation = ++state.generation;
  let timer;
  try {
    // Optional metadata must not stall file reads or leave an action locked.
    const current = typeof session.rpc?.model?.getCurrent === "function" ? await Promise.race([
      session.rpc.model.getCurrent(),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("Model metadata timed out.")), 2000); }),
    ]) : undefined;
    if (generation > state.resolved) {
      state.model = validModel(current?.modelId) ? current.modelId : undefined;
      state.name = current?.modelId === "auto" ? "Copilot Fusion" : undefined; state.resolved = generation;
    }
  } catch { /* Transient metadata failures keep the last known valid model. */ }
  finally { clearTimeout(timer); }
  return { model: state.model, name: state.name };
}

export const validModel = (model) => typeof model === "string" && /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$/.test(model) && model.toLowerCase() !== "auto" && !model.includes("://");
export async function readModels(session) {
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
export function observed(view, operation, ids, caseId, complete, model) {
  const r = view.latest.status === "current" ? view.latest.result : undefined;
  const want = caseId === undefined ? ids : [caseId], got = r?.selection.caseIds ?? [], rows = r?.examples.map((e) => e.id) ?? [];
  return r?.operation === operation && (preRouteRefusal(r) || r.modelRoute.slice(r.modelRoute.indexOf("/") + 1) === model) && r.selection.scope === (caseId === undefined ? "full" : "subset") && r.selection.totalCases === ids.length &&
    new Set(got).size === got.length && got.every((id) => want.includes(id)) && (!complete || got.length === want.length) &&
    new Set(rows).size === rows.length && rows.every((id) => got.includes(id));
}
