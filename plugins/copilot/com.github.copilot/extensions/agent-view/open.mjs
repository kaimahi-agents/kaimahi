// Read-only native UI tool; independent of skill text and slash registration.
import { CANVAS_ID, ViewError, normalizePath, readView, formatView } from "./view.mjs";

export const OPEN_TOOL_NAME = "open_kmx_canvas";
export const OPEN_TOOL_PARAMETERS = {
  type: "object",
  properties: { path: { type: "string", description: "Existing root-relative <id>.agent.md or .squad/agents/<id>/charter.md under the already-bound root." } },
  required: ["path"],
  additionalProperties: false,
};
// Instance IDs are identifiers, not file paths. Separate layout prefixes keep
// flat agents and Squad charters distinct without punctuation rejected by host.
export function viewInstanceId(rawPath) {
  const path = normalizePath(rawPath);
  return path.startsWith(".squad/")
    ? "kmx-squad-" + path.split("/")[2]
    : "kmx-agent-" + path.slice(0, -".agent.md".length);
}

const refused = (error) => ({
  opened: false,
  reasonCode: error instanceof ViewError ? error.code : "canvas-unavailable",
  message: error instanceof ViewError ? error.message : "The kmx agent view could not be opened. Use /agent-view PATH for a text view.",
});

// Both entry points use the same confinement check. In particular, built-in
// open_canvas reaches the provider directly rather than the native UI tool.
export async function readOpenView(session, input) {
  if (!input || typeof input !== "object" || Array.isArray(input)
      || Object.keys(input).length !== 1 || !Object.hasOwn(input, "path")) {
    throw new ViewError("invalid-path");
  }
  const path = normalizePath(input.path);
  if (!session) throw new ViewError("unavailable");
  return { path, view: await readView(session, path) };
}

export function createOpenHandler({ getSession, extensionId }) {
  return async (input) => {
    try {
      const session = getSession();
      const { path, view } = await readOpenView(session, input);
      if (session.capabilities?.ui?.canvases !== true) return { opened: false, mode: "text", path, text: formatView(view) };
      await session.rpc.canvas.open({ extensionId, canvasId: CANVAS_ID, instanceId: viewInstanceId(path), input: { path } });
      // The host response contains the bridge's private URL; do not expose it
      // in this tool's model-facing result.
      return { opened: true, mode: "canvas", path };
    } catch (error) {
      return refused(error);
    }
  };
}
