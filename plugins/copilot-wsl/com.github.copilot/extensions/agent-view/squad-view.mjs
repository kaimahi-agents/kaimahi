import { startCanvasBridge, CANVAS_ASSETS, ViewError } from "./view.mjs";
import { createNativeSquadBackend, validateProposal } from "./squad-backend.mjs";
import { createHash } from "node:crypto";

export const SQUAD_CANVAS_ID = "kmx-squad-view";
const ASSETS = {
  ...CANVAS_ASSETS,
  "": ["squad.html", "text/html; charset=utf-8"],
  "squad-app.js": ["squad-app.js", "text/javascript; charset=utf-8"],
  "squad.css": ["squad.css", "text/css; charset=utf-8"],
  "squad-examples.mjs": ["squad-examples.mjs", "text/javascript; charset=utf-8"],
  "app.js": ["app.js", "text/javascript; charset=utf-8"],
  "editor.mjs": ["editor.mjs", "text/javascript; charset=utf-8"],
  "authoring.mjs": ["authoring.mjs", "text/javascript; charset=utf-8"],
  "style.css": ["style.css", "text/css; charset=utf-8"],
};

export async function startSquadBridge({ session, proposal, extensionId, instanceId }) {
  if (proposal !== undefined) validateProposal(proposal);
  const backend = createNativeSquadBackend(session, { extensionId, instanceId });
  await backend.team();
  let submitted = false;
  let proposalOffer;
  const act = async (input) => {
      try {
        if (["accept-proposal", "dismiss-proposal"].includes(input?.operation)) {
          if (Object.keys(input).some(k => !["operation", "offerId"].includes(k)) || !proposalOffer || input.offerId !== proposalOffer.id || submitted) throw new ViewError("invalid-action");
          const acceptedProposal = input.operation === "accept-proposal" ? proposalOffer.proposal : undefined;
          if (acceptedProposal) proposal = acceptedProposal;
          proposalOffer = undefined;
          return { acceptedProposal, proposalDismissed: !acceptedProposal };
        }
        const result = await backend.action(input);
        if (input.operation === "create" && result.creation?.status !== "refused") submitted = true;
        return result;
      } catch (error) {
        const safe = error instanceof ViewError ? error : new ViewError("action-failed");
        return { error: safe.code, message: safe.message };
      }
  };
  act.dispose = () => backend.dispose();
  const bridge = await startCanvasBridge({ assets: ASSETS, read: async () => ({ ...await backend.snapshot(), ...(!submitted && proposal ? { proposal } : {}), ...(proposalOffer ? { proposalOffer } : {}) }), act });
  return { ...bridge, offerProposal(value) {
    if (submitted) throw new ViewError("invalid-action");
    validateProposal(value);
    const encoded = JSON.stringify(value);
    proposalOffer = { id: createHash("sha256").update(encoded).digest("hex"), proposal: JSON.parse(encoded) };
  } };
}

export function createSquadViewController({ getSession, extensionId }) {
  const instances = new Map();
  return {
    async open(ctx) {
      const input = ctx.input ?? {};
      if (typeof input !== "object" || Array.isArray(input) || Object.keys(input).some((k) => k !== "proposal") || typeof ctx.instanceId !== "string" || !ctx.instanceId) throw new ViewError("invalid-action");
      const reopening = instances.has(ctx.instanceId);
      if (!reopening) instances.set(ctx.instanceId, { ready: startSquadBridge({ session: getSession(), proposal: input.proposal, extensionId, instanceId: ctx.instanceId }) });
      const entry = instances.get(ctx.instanceId);
      let bridge;
      try {
        bridge = await entry.ready;
      } catch (error) { if (instances.get(ctx.instanceId) === entry) instances.delete(ctx.instanceId); throw error; }
      if (instances.get(ctx.instanceId) !== entry) throw new ViewError("unavailable");
      if (reopening && input.proposal !== undefined) bridge.offerProposal(input.proposal);
      return { url: bridge.url, title: "Squad", status: "Native authoring · local member tests" };
    },
    async close(ctx) {
      const entry = instances.get(ctx.instanceId); instances.delete(ctx.instanceId);
      if (entry) await entry.ready.then(bridge => bridge.close(), () => {});
    },
  };
}
