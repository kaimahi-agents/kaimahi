// Native authoring canvas plus the read-only /agent-view text fallback.
// File changes and previews use only the session's existing kmx MCP tools.
import { CanvasError, createCanvas, joinSession } from "@github/copilot-sdk/extension";
import { CANVAS_ID, ViewError, createViewController } from "./view.mjs";

let session;
const controller = createViewController({ getSession: () => session });
const asCanvasError = (e) => (e instanceof ViewError ? new CanvasError(e.code, e.message) : e);

const canvas = createCanvas({
  id: CANVAS_ID,
  displayName: "kmx agent view",
  description: "Author one rooted agent's instructions and examples, then test it in the canvas.",
  inputSchema: {
    type: "object",
    properties: { path: { type: "string", description: "Root-relative <id>.agent.md or .squad/agents/<id>/charter.md" } },
    required: ["path"],
    additionalProperties: false,
  },
  open: (ctx) => controller.open(ctx).catch((e) => {
    throw asCanvasError(e);
  }),
  onClose: (ctx) => controller.close(ctx),
});

session = await joinSession({
  canvases: [canvas],
  commands: [
    {
      name: "agent-view",
      description: "Show a kmx agent charter as text: /agent-view <root-relative path>",
      handler: (ctx) => controller.command(ctx),
    },
  ],
});
