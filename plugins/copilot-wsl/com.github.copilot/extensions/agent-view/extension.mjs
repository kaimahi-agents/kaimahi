// Native authoring canvas plus the read-only /agent-view text fallback.
// File changes and previews use only the session's existing kmx MCP tools.
import { CanvasError, createCanvas, joinSession } from "@github/copilot-sdk/extension";
import { CANVAS_ID, ViewError, createViewController } from "./view.mjs";
import { OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS, createOpenHandler, createOpenCommand, readOpenView } from "./open.mjs";
import { SUGGEST_TOOL_NAME, SUGGEST_TOOL_PARAMETERS } from "./suggestions.mjs";

let session;
const controller = createViewController({ getSession: () => session });
const asCanvasError = (e) => {
  const safe = e instanceof ViewError ? e : new ViewError("unavailable");
  return new CanvasError(safe.code, safe.message);
};

const canvas = createCanvas({
  id: CANVAS_ID,
  displayName: "kmx agent view",
  description: "Author an agent's instructions and examples, then test it in the canvas.",
  inputSchema: {
    type: "object",
    properties: { path: { type: "string", description: ".agent.md or .squad/agents/<member>/charter.md path, including an absolute WSL path" }, tests: { type: "string", description: "Optional selected companion tests path for an .agent.md file." } },
    required: ["path"],
    additionalProperties: false,
  },
  open: async (ctx) => {
    try {
      const { view } = await readOpenView(session, ctx.input);
      return await controller.open(ctx, view);
    } catch (e) {
      throw asCanvasError(e);
    }
  },
  onClose: (ctx) => controller.close(ctx),
});

session = await joinSession({
  canvases: [canvas],
  tools: [{
    name: OPEN_TOOL_NAME,
    description: "Open an existing kmx agent canvas, or return its revision-bound text view when no renderer is available. Complete source is returned only from the checked resource snapshot. Does not edit, run or test.",
    parameters: OPEN_TOOL_PARAMETERS,
    handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),
  }, {
    name: SUGGEST_TOOL_NAME,
    description: "Deliver new tests as unsaved drafts to the exact live canvas that requested them. Never edits files or runs tests; only reports whether drafts were queued.",
    parameters: SUGGEST_TOOL_PARAMETERS,
    handler: input => controller.deliverSuggestions(input),
  }],
  commands: [
    {
      name: "agent-view",
      description: "Show a kmx agent charter as text: /agent-view <path>",
      handler: (ctx) => controller.command(ctx),
    },
    {
      name: "kmx-canvas",
      description: "Open an agent canvas without a model turn: /kmx-canvas open <path>",
      handler: createOpenCommand({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),
    },
  ],
});
