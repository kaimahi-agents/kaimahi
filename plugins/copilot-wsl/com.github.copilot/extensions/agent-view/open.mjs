// Read-only native UI tool; independent of skill text and slash registration.
import { createHash } from "node:crypto";
import { CANVAS_ID, ViewError, normalizePath, readView, formatView } from "./view.mjs";
import { MAX_RESOURCE_BYTES, normalizeTests } from "./view-resource.mjs";

export const OPEN_TOOL_NAME = "open_kmx_canvas";
export const OPEN_TOOL_PARAMETERS = {
  type: "object",
  properties: { path: { type: "string", description: "Existing .agent.md or .squad/agents/<member>/charter.md path, including an absolute WSL path." }, tests: { type: "string", description: "Optional selected companion tests path for an .agent.md file." } },
  required: ["path"],
  additionalProperties: false,
};
// Preserve legacy IDs; full paths need distinct portable IDs across repos.
export function viewInstanceId(rawPath, tests) {
  const path = normalizePath(rawPath);
  if (tests !== undefined) return "kmx-agent-" + createHash("sha256").update(JSON.stringify([path, normalizeTests(tests)])).digest("hex").slice(0, 24);
  const flat = /^([a-z][a-z0-9-]{0,62})\.agent\.md$/.exec(path);
  if (flat) return "kmx-agent-" + flat[1];
  const squad = /^\.squad\/agents\/([a-z][a-z0-9-]{0,62})\/charter\.md$/.exec(path);
  if (squad) return "kmx-squad-" + squad[1];
  return "kmx-agent-" + createHash("sha256").update(path).digest("hex").slice(0, 24);
}

const refused = (error) => ({
  opened: false,
  reasonCode: error instanceof ViewError ? error.code : "canvas-unavailable",
  message: error instanceof ViewError ? error.message : "The kmx agent view could not be opened. Use /agent-view PATH for a text view.",
});

// Text is bounded display only. Exact edit authority comes only from the
// resource's hash-verified full source snapshot and full revision.
function textView(path, view, extra = {}) {
  const authoring = view.authoring
    ? { source: view.authoring.source, editable: view.authoring.editable,
        ...(!view.authoring.editable ? { reasonCode: "source_not_editable" } : {}) }
    : { editable: false, reasonCode: "complete_source_unavailable" };
  const result = {
    opened: false, mode: "text", ...extra, path, text: formatView(view),
    file: { path: view.file.path, revision: view.file.revision }, authoring,
  };
  if (view.selectedSources) {
    const { source, ...selectedSources } = view.selectedSources;
    if (!result.authoring.source) result.authoring.source = source;
    result.selectedSources = selectedSources;
    if (Buffer.byteLength(JSON.stringify(result)) > MAX_RESOURCE_BYTES) result.text = "Selected sources available; detailed display omitted for size.";
    if (Buffer.byteLength(JSON.stringify(result)) > MAX_RESOURCE_BYTES) throw new ViewError("too-large");
  }
  return result;
}

// Both entry points validate the same agent resource. In particular, built-in
// open_canvas reaches the provider directly rather than the native UI tool.
export async function readOpenView(session, input) {
  if (!input || typeof input !== "object" || Array.isArray(input)
      || Object.keys(input).some(k => !["path", "tests"].includes(k)) || !Object.hasOwn(input, "path")) {
    throw new ViewError("invalid-path");
  }
  const path = normalizePath(input.path);
  if (!session) throw new ViewError("unavailable");
  const tests = normalizeTests(input.tests);
  return { path, tests, view: await readView(session, path, tests) };
}

export function createOpenHandler({ getSession, extensionId }) {
  return async (input) => {
    try {
      const session = getSession();
      const { path, tests, view } = await readOpenView(session, input);
      const advertised = session.capabilities?.ui?.canvases === true;
      if (typeof session.rpc?.canvas?.open !== "function") {
        if (advertised) throw new ViewError("unavailable");
        return textView(path, view);
      }
      // Try even when not advertised: a resumed app session can omit the
      // capability although its canvases work. Text only if opening fails.
      try {
        await session.rpc.canvas.open({ extensionId, canvasId: CANVAS_ID, instanceId: viewInstanceId(path, tests), input: { path, ...(tests !== undefined ? { tests } : {}) } });
      } catch (error) {
        if (advertised) throw error;
        return textView(path, view, { canvasFailed: true });
      }
      // The host response contains the bridge's private URL; do not expose it
      // in this tool's model-facing result.
      return { opened: true, mode: "canvas", path };
    } catch (error) {
      return refused(error);
    }
  };
}

// A typed relative path means the session folder, not the kmx server's own
// directory. Without the host's folder the path is used as typed.
async function sessionPath(session, path) {
  if (/^(?:\/|[A-Za-z]:[\\/])/.test(path)) return path;
  let folder;
  try { folder = (await session.rpc?.metadata?.snapshot?.())?.workingDirectory; } catch { return path; }
  if (typeof folder !== "string" || !/^(?:\/|[A-Za-z]:[\\/])/.test(folder)) return path;
  // kmx reads C:\x/y as C:\x\y; keeping "/" lets Squad charter paths pass the path check.
  return folder.replace(/[\\/]+$/, "") + "/" + path;
}

export function createOpenCommand(options) {
  const open = createOpenHandler(options);
  return async ({ args }) => {
    const session = options.getSession();
    // Consume one horizontal delimiter; every following path byte is literal.
    const match = /^[ \t]*open[ \t]([\s\S]+)$/.exec(args);
    // The app shows only error-level command output, so usage and problems use it.
    if (!match) {
      await session.log("Usage: /kmx-canvas open <path>\nExample: /kmx-canvas open greeter.agent.md", { level: "error" });
      return;
    }
    const result = await open({ path: await sessionPath(session, match[1]) });
    if (result.canvasFailed) await session.log("The canvas couldn't open here, so the agent is shown as text.", { level: "error" });
    if (result.mode === "text") await session.log(result.text);
    else if (result.opened) await session.log(`Opened ${result.path.split(/[\\/]/).at(-1)} in the agent canvas.`);
    else if (result.reasonCode === "not-found") await session.log(`File not found: ${match[1]}. Check the name, or give the full path.`, { level: "error" });
    else await session.log(result.message, { level: "error" });
  };
}
