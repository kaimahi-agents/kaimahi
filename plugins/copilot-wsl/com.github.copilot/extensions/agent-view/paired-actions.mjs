// Selected-source operations use the existing MCP tools, never page-selected
// paths or the legacy latest-result cache. Raw agent hashes stay agent-only.
import { createHash } from "node:crypto";
import { replaceInstructions, replaceTools, exampleEdits, effectiveModel, pendingCapture, hasFrontmatter, sourceIdentity, preRouteRefusal } from "./authoring.mjs";
import { ViewError, safeError, cleanResult, cleanSettings, observed, SERVER_NAME, MAX_RESOURCE_BYTES, validModel } from "./view-resource.mjs";
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const hash = source => "sha256:" + createHash("sha256").update(source).digest("hex");
const paths = (actual, expected) => actual === expected || actual === expected.split("/").at(-1);
const roleStatus = status => ["unchanged", "saved", "not_saved", "uncertain"].includes(status) ? status : "unconfirmed";
const unknown = () => { throw new ViewError("outcome-unknown"); };
function payload(reply) {
  refused(reply);
  if (reply?.structuredContent) return reply.structuredContent;
  if (typeof reply?.textResultForLlm === "string" && Buffer.byteLength(reply.textResultForLlm) <= MAX_RESOURCE_BYTES) { try { return JSON.parse(reply.textResultForLlm); } catch {} }
  unknown();
}
function refused(reply, raw) {
  const decline = code => { const error = new ViewError(code); error.confirmedRefusal = true; throw error; };
  if (["denied", "rejected"].includes(reply?.resultType)) decline("permission_required");
  if (raw?.schemaVersion === 3 && typeof raw.reasonCode === "string" && typeof raw.reason === "string" && Object.keys(raw).sort().join(",") === "reason,reasonCode,schemaVersion") {
    decline(["stale_revision", "confirmation_required", "invalid_tool_settings"].includes(raw.reasonCode) ? raw.reasonCode : raw.reasonCode === "invalid_file" ? "invalid-file" : "action-failed");
  }
}

export async function selectedAction(initial, input, { invoke, read, block, snapshot, settingsReceipt, session, suggest, certified, captureAttempt, noteAttempt }) {
  let view = initial, saved = false, settingsIncomplete = false, phase;
  const readFresh = () => read(view.selectedSources.selector);
  const argumentsFor = () => ({ schemaVersion: 3, expectedSourceVersion: view.selectedSources.sourceVersion, ...(view.selectedSources.selector !== undefined ? { tests: view.selectedSources.selector } : {}) });
  try {
    if (!view.authoring?.editable || !view.selectedSources.operations.edit) throw new ViewError("read-only");
    if (input.operation === "suggest") {
      if (!suggest) throw new ViewError("unavailable");
      return await suggest(view);
    }
    if (input.expectedRevision !== view.file.revision) throw new ViewError("stale_revision");
    if (input.operation === "restore" && (typeof input.restoreVersionId !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(input.restoreVersionId) || !view.selectedSources.operations.restore)) throw new ViewError("invalid-action");
    const operation = input.operation === "run" ? "run" : "test", model = effectiveModel(view.model, input.model, view.chatModel);
    if (!validModel(model) || operation === "run" && (typeof input.input !== "string" || Buffer.byteLength(input.input) > 16384)) throw new ViewError("invalid-action");
    const models = input.comparisonModels ?? [model];
    if (!Array.isArray(models) || !models.length || models.length > 2 || new Set(models).size !== models.length || !models.includes(model) || !models.every(validModel)) throw new ViewError("invalid-action");
    const originalToken = view.toolSettingsRevision;
    if (view.tools.length && (originalToken === undefined || input.expectedSettingsRevision !== originalToken)) throw new ViewError("stale_settings_revision");
    const changes = input.toolSettings === undefined ? [] : cleanSettings(input.toolSettings), names = input.tools ?? view.tools;
    if (changes.some(s => !names.includes(s.name))) throw new ViewError("invalid-action");
    if (changes.length) {
      const metadata = await session.rpc.tools.getCurrentMetadata();
      if (metadata.tools?.filter(t => t.mcpServerName === SERVER_NAME && t.mcpToolName === "set_tool_state").length !== 1) throw new ViewError("unavailable");
      if (view.tools.length && input.expectedSettingsRevision !== view.toolSettingsRevision) throw new ViewError("stale_settings_revision");
      settingsIncomplete = true;
    }
    let content = view.authoring.source;
    if (input.sections !== undefined) content = replaceInstructions(content, input.sections);
    if (input.tools !== undefined) content = replaceTools(content, input.tools, view.tools);
    const patches = input.examples === undefined ? [] : exampleEdits(view.authoring.examples, input.examples, input.exampleOrigins);
    const intended = input.examples ?? view.authoring.examples, ids = intended.map(e => e.id), selected = input.caseId === undefined ? ids : [input.caseId];
    if (input.caseId !== undefined && ids.filter(id => id === input.caseId).length !== 1) throw new ViewError("invalid-action");
    if (Buffer.byteLength(content) > 256 * 1024) throw new ViewError("too-large");
    if (view.selectedSources.testsState === "inline" && hasFrontmatter(content) && (patches.length || ["test", "save-and-test", "run"].includes(input.operation) && pendingCapture(intended, names.map(name => changes.find(s => s.name === name) ?? view.toolSettings.find(s => s.name === name) ?? { name, state: "Enabled" }), selected)) && input.acknowledgeCopilotTests !== true) throw new ViewError("confirmation_required");
    const saving = ["save-prompt", "save-examples", "save-and-test", "restore"].includes(input.operation);
    if (saving && (content !== view.authoring.source || patches.length || input.restoreVersionId)) {
      const args = { ...argumentsFor(), ...(input.restoreVersionId ? { restoreVersionId: input.restoreVersionId } : { ...(content !== view.authoring.source ? { content } : {}), ...(patches.length ? { exampleEdits: patches } : {}) }), ...(input.acknowledgeCopilotTests === true ? { acknowledgeCopilotTests: true } : {}) };
      phase = "save";
      const reply = await invoke("edit", args, input.runId ? { id: input.runId } : undefined), ack = payload(reply);
      if (ack.schemaVersion !== 3 || ack.kind !== "kmx.agent-pair-save") { refused(reply, ack); unknown(); }
      if (!["unchanged", "saved", "not_saved", "uncertain"].includes(ack.agent?.status) || !["unchanged", "saved", "not_saved", "uncertain"].includes(ack.tests?.status)) unknown();
      if (ack.state !== "complete" || reply.resultType !== "success" || [ack.agent.status, ack.tests.status].some(s => !["saved", "unchanged"].includes(s))) {
        block(); return { error: "outcome-unknown", message: `Agent: ${ack.agent.status}. Tests: ${ack.tests.status}. Tests did not run. Your drafts are kept; reload deliberately to inspect both sources.`, saved: false, ...(settingsIncomplete ? { settingsIncomplete: true } : {}) };
      }
      const fresh = await readFresh();
      if (fresh.selectedSources?.sourceVersion !== ack.sourceVersion || fresh.selectedSources.snapshotRevision !== ack.snapshotRevision || fresh.file.revision !== ack.file?.revision || !paths(ack.file?.path ?? "", view.selectedSources.agentPath) && ack.file?.path !== view.file.path) unknown();
      if (content !== view.authoring.source && view.selectedSources.testsState !== "inline" && fresh.file.revision !== hash(content)) unknown();
      view = fresh; saved = true; phase = undefined;
    }
    if (changes.length && originalToken !== undefined && view.toolSettingsRevision !== originalToken) throw new ViewError("stale_settings_revision");
    for (const change of changes) {
      phase = "settings";
      const reply = await invoke("set_tool_state", { expectedRevision: view.file.revision, expectedSettingsRevision: view.toolSettingsRevision, ...change });
      const ack = settingsReceipt(reply, view, change), fresh = await readFresh();
      if (fresh.file.revision !== view.file.revision || fresh.selectedSources.snapshotRevision !== view.selectedSources.snapshotRevision || fresh.toolSettingsRevision !== ack.token || !same(fresh.toolSettings, ack.states)) unknown();
      view = fresh; saved ||= ack.changed; phase = undefined;
    }
    settingsIncomplete = false;
    if (["save-prompt", "save-examples", "restore"].includes(input.operation)) return { view, saved };
    if (!view.selectedSources.operations.execute) throw new ViewError("invalid-file");
    const settings = view.toolSettings.map(s => ({ ...s })), results = [];
    let capture;
    const cacheKey = chosen => JSON.stringify([sourceIdentity(view), selected, settings, input.showContent === true, operation, chosen]);
    async function run(chosen, replay) {
      certified.delete(cacheKey(chosen));
      const executed = view, sources = view.selectedSources;
      phase = "preview";
      const reply = await invoke(operation, { ...argumentsFor(), model: chosen, ...(operation === "test" ? { expectedToolSettings: settings, recordedReplayOnly: replay } : {}), ...(input.caseId !== undefined ? { caseId: input.caseId } : {}), ...(operation === "run" ? { input: input.input } : {}), ...(input.acknowledgeCopilotTests === true ? { acknowledgeCopilotTests: true } : {}), showContent: input.showContent === true, showFailedContent: true }, { id: input.runId, revision: sourceIdentity(view), model: chosen, examples: view.authoring.examples, caseIds: selected, totalCases: ids.length, toolSettings: settings });
      if (reply.stopped) { const fresh = await readFresh(); if (sourceIdentity(fresh) !== sourceIdentity(view)) unknown(); return { stopped: true, run: { ...snapshot(), state: "stopped" }, view: fresh, saved }; }
      const envelope = payload(reply), raw = envelope.preview;
      if (envelope.schemaVersion !== 3 || envelope.kind !== "kmx.agent-pair-preview") { refused(reply, envelope); unknown(); }
      if (envelope.sourceVersion !== sources.sourceVersion || raw?.snapshotRevision !== sources.snapshotRevision || raw?.file?.revision !== view.file.revision || !paths(raw.file.path, sources.agentPath) && raw.file.path !== view.file.path
        || raw.tests?.state !== sources.testsState || raw.tests.revision !== sources.testsRevision || raw.tests.selectionKey !== sources.selectionKey || !paths(raw.tests.path, sources.testsPath)) unknown();
      const result = { ...cleanResult(raw, "current", view.file.revision), sourceVersion: envelope.sourceVersion, snapshotRevision: raw.snapshotRevision, ...(envelope.savedSourceVersion ? { savedSourceVersion: envelope.savedSourceVersion } : {}) };
      if (!preRouteRefusal(result) && (!same(result.toolSettings ?? [], settings) || result.modelRoute.slice(result.modelRoute.indexOf("/") + 1) !== chosen || operation === "test" && replay && result.recordedReplayOnly !== true)) unknown();
      if (!observed({ ...view, latest: { status: "current", result } }, operation, ids, input.caseId, result.state === "complete", chosen)) unknown();
      const receipt = envelope.capture, flags = result.examples.flatMap(e => e.tools ?? []).filter(t => t.recordingSaved);
      if (receipt && (receipt.state !== "complete" || !["unchanged", "saved"].includes(receipt.agent?.status) || !["unchanged", "saved"].includes(receipt.tests?.status))) {
        block(); return { error: "outcome-unknown", message: `${saved ? "Sources were saved before capture. " : ""}Recording save: Agent ${roleStatus(receipt.agent?.status)}; Tests ${roleStatus(receipt.tests?.status)}. Reload to inspect both sources.`, saved };
      }
      const fresh = await readFresh();
      if (raw.savedRevision !== undefined) {
        const target = sources.testsState === "external" ? fresh.selectedSources.testsRevision : fresh.file.revision;
        if (replay || !flags.length || flags.some(t => t.mode !== "recorded" || t.source !== "live") || receipt?.state !== "complete" || raw.savedSnapshotRevision === raw.snapshotRevision || sources.testsState === "external" && (fresh.file.revision !== executed.file.revision || receipt.agent.status !== "unchanged" || receipt.tests.status !== "saved") || sources.testsState !== "external" && (receipt.agent.status !== "saved" || receipt.tests.status !== "unchanged") || envelope.savedSourceVersion !== fresh.selectedSources.sourceVersion || raw.savedSnapshotRevision !== fresh.selectedSources.snapshotRevision || raw.savedRevision !== target || raw.recordingTarget?.role !== (sources.testsState === "external" ? "tests" : "agent") || raw.recordingTarget.selectionKey !== sources.selectionKey || !paths(raw.recordingTarget.path, sources.testsState === "external" ? sources.testsPath : sources.agentPath)) unknown();
        saved = true; capture = { ...result, savedRevision: raw.savedRevision, savedSourceVersion: envelope.savedSourceVersion, savedSourceIdentity: sourceIdentity(fresh), executedRevision: raw.file.revision, model: chosen };
      } else if (flags.length || envelope.savedSourceVersion || receipt || sourceIdentity(fresh) !== sourceIdentity(executed)) unknown();
      if (!same(fresh.toolSettings, settings)) throw new ViewError("tool_settings_changed");
      view = fresh; phase = undefined;
      const attributed = { ...fresh, latest: { status: "current", badge: result.fullPass && result.state === "complete" && result.verdict === "pass" && result.selection.scope === "full" ? "pass" : "not-passing", result } };
      if (replay && result.state === "complete") { certified.set(cacheKey(chosen), { view: attributed, saved: false }); while (certified.size > 2) certified.delete(certified.keys().next().value); }
      return { view: raw.savedRevision ? fresh : attributed, saved, ...(capture ? { capture } : {}) };
    }
    const pending = () => pendingCapture(view.authoring.examples, settings, selected);
    const incomplete = row => row.error || row.stopped || (row.view?.latest.result ?? row.capture)?.state !== "complete";
    if (models.length > 1 && pending()) { if (captureAttempt?.key !== cacheKey("capture")) { const first = await run(model, false); if (first.error || first.stopped) return first; noteAttempt({ key: cacheKey("capture"), model }); if (incomplete(first)) return first; } if (pending()) return { error: "recording_missing", message: "A Recorded tool has no saved call. Change the test or tool mode before comparing.", view, saved, capture }; }
    for (const chosen of models) { const firstCapture = pending(); const row = chosen !== model && certified.get(cacheKey(chosen)) || await run(chosen, !firstCapture); if (!row.error && !row.stopped && models.length === 1 && firstCapture) noteAttempt({ key: cacheKey("capture"), model }); if (incomplete(row)) return row; results.push(row); }
    const selectedRow = results.find(r => r.view.latest.result?.modelRoute.endsWith("/" + model)) ?? results[0];
    return { ...selectedRow, saved, ...(capture ? { capture } : {}), ...(models.length > 1 ? { comparison: { results: results.map(r => r.view) } } : {}) };
  } catch (e) {
    let err = safeError(e);
    if (phase && err.code !== "outcome-unknown" && !err.confirmedRefusal) err = new ViewError("outcome-unknown");
    if (err.code === "outcome-unknown") block();
    return { error: err.code, message: err.message, saved, ...(err.approvalTimeout ? { approvalTimeout: true } : {}), ...(settingsIncomplete ? { settingsIncomplete: true } : {}), ...(saved && err.code !== "outcome-unknown" ? { view } : {}) };
  }
}
