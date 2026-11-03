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
const deliveryFailures = {
  "suggestions-unavailable": "Suggestions could not be delivered. Suggest tests again in the original canvas.",
  "suggestions-invalid-delivery": "The suggestion delivery has an invalid shape. Copilot must send only requestId and examples.",
  "suggestions-inactive-request": "The suggestion request is no longer active. Use Suggest tests again in the original canvas.",
  "suggestions-already-queued": "Suggestions are already queued. Review the existing proposals in the original canvas.",
  "suggestions-examples-too-large": "The suggested examples exceed the delivery size limit. Copilot must return fewer or shorter examples.",
  "suggestions-empty-examples": "The suggestion delivery contains no examples. Copilot must return at least one example.",
  "suggestions-invalid-examples": "The suggested examples do not match the supported format. Copilot must supply test IDs, inputs and typed checks with their required fields.",
  "suggestions-invalid-mocks": "The editor rejected the suggested tool modes or fixture format. Add these tests manually in the canvas.",
  "suggestions-source-unavailable": "The saved sources could not be read. Check the original canvas is available before using Suggest tests again.",
  "suggestions-source-changed": "The saved sources, selection, owner or editability changed. Review the current sources and use Suggest tests again in the original canvas.",
  "suggestions-examples-conflict": "The suggested examples conflict with saved examples or exceed the 128-test limit. Use new test IDs and fewer tests.",
  "suggestions-draft-incompatible": "The suggested examples are not compatible with canvas drafts. Use unique test names and complete checks.",
};
export function suggestionsUnavailable(reasonCode) {
  // Unknown callers cannot turn model data or thrown errors into diagnostics.
  if (typeof reasonCode !== "string" || !Object.hasOwn(deliveryFailures, reasonCode)) reasonCode = "suggestions-unavailable";
  return { queued: false, reasonCode, message: deliveryFailures[reasonCode] + " Nothing was saved or run." };
}
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
    if (!isObj(input) || Object.keys(input).sort().join(",") !== "examples,requestId") return suggestionsUnavailable("suggestions-invalid-delivery");
    if (!matches(input.requestId)) return suggestionsUnavailable("suggestions-inactive-request");
    if (current.batch) return suggestionsUnavailable(current.stale ? "suggestions-source-changed" : "suggestions-already-queued");
    const request = current;
    let examples, view, failure = "suggestions-invalid-examples";
    try {
      // Same source-size bound as authored examples; never truncate model output.
      if (Buffer.byteLength(JSON.stringify(input.examples)) > 256 * 1024) return suggestionsUnavailable("suggestions-examples-too-large");
      examples = clone(input.examples);
      if (Array.isArray(examples) && !examples.length) return suggestionsUnavailable("suggestions-empty-examples");
      validateExamples(examples, true);
      // Classify failures by the held stage, never by thrown or model content.
      failure = "suggestions-invalid-mocks";
      // Reuse sparse-edit validation to exclude invented recorded call payloads.
      exampleEdits([], examples);
      failure = "suggestions-source-unavailable";
      view = await read();
      if (current !== request || disposed) return suggestionsUnavailable("suggestions-inactive-request");
      if (request.batch) return suggestionsUnavailable(request.stale ? "suggestions-source-changed" : "suggestions-already-queued");
      failure = "suggestions-source-changed";
      request.stale ||= !fresh(request, view);
      if (request.stale) return suggestionsUnavailable(failure);
      failure = "suggestions-examples-conflict";
      const intended = [...view.authoring.examples, ...examples];
      validateExamples(intended, true);
      failure = "suggestions-draft-incompatible";
      if (examples.some(raw => validateDraft({ raw, origin: null, fixedId: true }, intended))) return suggestionsUnavailable(failure);
    } catch {
      if (current !== request || disposed) return suggestionsUnavailable("suggestions-inactive-request");
      if (request.batch) return suggestionsUnavailable(request.stale ? "suggestions-source-changed" : "suggestions-already-queued");
      return suggestionsUnavailable(failure);
    }
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
