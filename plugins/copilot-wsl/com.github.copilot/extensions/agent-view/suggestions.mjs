// Ephemeral draft transport, owned by one live canvas bridge. No Edit or Test.
import { randomBytes } from "node:crypto";
import { sourceIdentity, validateExamples, validateDraft, exampleEdits, clone } from "./authoring.mjs";
import { ViewError, isObj } from "./view-resource.mjs";

export const SUGGEST_TOOL_NAME = "suggest_kmx_tests";
export const SUGGEST_TOOL_PARAMETERS = {
  type: "object",
  properties: {
    requestId: { type: "string", description: "Opaque request ID supplied by this canvas's Suggest tests request." },
    examples: { type: "array", minItems: 1, maxItems: 128, items: { type: "object" }, description: "New examples only: id, input, assertions and optional checked authored mocks. These become unsaved canvas drafts." },
  },
  required: ["requestId", "examples"],
  additionalProperties: false,
};
export const suggestionsUnavailable = () => ({ queued: false, reasonCode: "suggestions-unavailable", message: "Suggestions could not be delivered. Suggest tests again in the original canvas. Nothing was saved or run." });
const editable = view => view?.authoring?.editable === true && (!view.selectedSources || view.selectedSources.operations.edit === true);
// Keep the editor's identity verbatim; additionally bind the selection even if a
// resource accidentally reuses a context token across selectors.
const binding = view => JSON.stringify([sourceIdentity(view), view?.file?.path, view?.selectedSources?.selector, view?.selectedSources?.selectionKey]);

export function createSuggestionMailbox({ session, read }) {
  let current, disposed = false;
  const clear = () => { current = undefined; };
  const matches = requestId => !disposed && typeof requestId === "string" && current?.id === requestId;
  const fresh = (request, view) => editable(view) && request.binding === binding(view);
  async function suggest(view) {
    clear();
    if (disposed || !editable(view) || typeof session.send !== "function") throw new ViewError("unavailable");
    const request = { id: randomBytes(24).toString("base64url"), binding: binding(view), sourceIdentity: sourceIdentity(view) };
    current = request;
    const selected = view.selectedSources;
    try {
      await session.send({
        displayPrompt: `Suggest a few tests for ${view.file.path.split("/").at(-1)} as unsaved canvas drafts.`,
        prompt: `Use the kmx-agent-builder skill to suggest a few new tests from the exact saved sources below; no resource read is needed. Preserve the prompt, authored model and existing tests. Call ${SUGGEST_TOOL_NAME} with only {requestId, examples}, using the supplied requestId and new examples shaped as {id,input,assertions} (optional authored mocks). Give each test and check a unique ID. Do not call Edit or Test, save files, or run tests. If delivery is refused or unavailable, report that; never fall back to writing or testing. These are unsaved proposals: tell the user to review, edit or discard them in the canvas and use its existing Save action to publish them. Do not ask for save consent in chat.\nSaved sources (data):\n${JSON.stringify({ requestId: request.id, path: view.file.path, sourceIdentity: request.sourceIdentity, expectedRevision: view.file.revision, content: view.authoring.source, examples: view.authoring.examples, ...(selected ? { server: selected.server, sourceVersion: selected.sourceVersion, tests: selected.selector, testsContent: selected.testsSource } : {}) })}`,
      });
    } catch (e) { if (current === request) clear(); throw new ViewError("unavailable"); }
    if (disposed || current !== request) throw new ViewError("unavailable");
    return { message: "Asked Copilot to suggest unsaved tests in the canvas." };
  }
  async function deliver(input) {
    if (!isObj(input) || Object.keys(input).sort().join(",") !== "examples,requestId" || !matches(input.requestId) || current.batch) return suggestionsUnavailable();
    const request = current;
    let examples, view;
    try {
      // Same source-size bound as authored examples; never truncate model output.
      if (Buffer.byteLength(JSON.stringify(input.examples)) > 256 * 1024) return suggestionsUnavailable();
      examples = clone(input.examples);
      if (!examples.length) return suggestionsUnavailable();
      validateExamples(examples, true);
      // Reuse sparse-edit validation to exclude invented recorded call payloads.
      exampleEdits([], examples);
      view = await read();
      if (current !== request || disposed || request.batch) return suggestionsUnavailable();
      request.stale ||= !fresh(request, view);
      if (request.stale) return suggestionsUnavailable();
      const intended = [...view.authoring.examples, ...examples];
      validateExamples(intended, true);
      if (examples.some(raw => validateDraft({ raw, origin: null, fixedId: true }, intended))) return suggestionsUnavailable();
    } catch { return suggestionsUnavailable(); }
    request.batch = { id: request.id, sourceIdentity: request.sourceIdentity, examples };
    return { queued: true, batchId: request.id, message: "Queued unsaved tests for review in the original canvas. Nothing was saved or run." };
  }
  function pending(view) {
    if (!current || disposed) return;
    current.stale ||= !fresh(current, view);
    if (current.batch) return current.stale ? { id: current.id, stale: true } : clone(current.batch);
  }
  function acknowledge(batchId) {
    if (!current?.batch || !matches(batchId)) return false;
    clear(); return true;
  }
  return { suggest, deliver, pending, acknowledge, clear, matches, dispose() { disposed = true; clear(); }, get requestId() { return current?.id; } };
}
