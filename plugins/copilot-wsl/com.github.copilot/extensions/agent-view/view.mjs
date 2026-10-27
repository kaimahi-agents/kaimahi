// Agent canvas. Files and previews are accessed only through the
// same session's existing kmx MCP resource and Edit/Run/Test operations.
import { randomBytes, timingSafeEqual, createHash } from "node:crypto";
import { instructionSections, replaceInstructions, replaceExamples, replaceTools, exampleEdits, effectiveModel, agentName, fileLocation, hasFrontmatter, testsNeedAcknowledgement, pendingCapture, COPILOT_TESTS_NOTICE } from "./authoring.mjs";
import { readFile } from "node:fs/promises";
import http from "node:http";
import { ensureHostRuntime, previewServer, beginPreview, closeHostRuntime, observePreview } from "./host-relay.mjs";
import { readToolCatalog } from "./tool-catalog.mjs";
import { SERVER_NAME, MAX_RESOURCE_BYTES, MAX_TEXT, MESSAGES, ViewError, invalid, safeError, normalizePath, clean, text, isObj, cleanSettings, cleanResult, readView, validModel, readModels, observed } from "./view-resource.mjs";
export { CANVAS_ID, SERVER_NAME, ViewError, normalizePath, resourceUri, readView } from "./view-resource.mjs";
export { readToolCatalog };

// The page can choose a declared interaction, never a tool, path or server.
export function createActionHandler({ session, path }) {
  path = normalizePath(path);
  let busy = false, uncertain = false, current, disposed = false, captureAttempt, saving;
  const certified = new Map();
  const allowed = {
    "save-and-test": ["expectedRevision", "sections", "examples", "exampleOrigins", "acknowledgeCopilotTests", "tools", "toolSettings", "expectedSettingsRevision", "showContent", "caseId", "model", "runId", "comparisonModels"],
    "save-examples": ["expectedRevision", "examples", "exampleOrigins", "acknowledgeCopilotTests"],
    "save-prompt": ["expectedRevision", "sections"],
    test: ["expectedRevision", "showContent", "caseId", "model", "runId", "comparisonModels", "acknowledgeCopilotTests"], run: ["expectedRevision", "input", "showContent", "model", "acknowledgeCopilotTests"],
    suggest: [], reload: [], models: [], tools: [], progress: ["runId"], stop: ["runId"],
  };
  const validRunId = (id) => typeof id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(id);
  const snapshot = () => current?.id === undefined ? undefined : ({
    id: current.id, revision: current.revision, model: current.model,
    caseIds: [...current.caseIds], totalCases: current.totalCases,
    // Request metadata is for partial-run freshness, not execution provenance.
    ...(current.toolSettings ? { toolSettings: current.toolSettings.map(s => ({ ...s })) } : {}),
    cases: current.caseIds.filter(id => current.rows.has(id)).map(id => ({ ...current.rows.get(id) })),
    state: current.state, elapsedMs: current.elapsedMs ?? Math.max(0, Date.now() - current.started),
    ...(current.approval?.waiting() ? { approvalWaiting: true } : {}),
  });
  const finish = (run) => {
    if (!run) return;
    run.pending = false;
    run.elapsedMs ??= Math.max(0, Date.now() - run.started);
    run.unsubscribe?.(); run.unsubscribe = undefined; run.approval?.clear();
  };
  function progress(run, event) {
    if (disposed || current !== run || !run.pending || run.state !== "running" || event?.data?.toolCallId !== run.toolCallId) return;
    const message = event.data.progressMessage;
    if (typeof message !== "string" || message.length > 16384) return;
    // Qualified native hosts wrap the MCP case JSON in a numeric prefix, not
    // structuredContent. Never expose the host's surrounding text to the page.
    const envelope = message.match(/^\d+(?:\.\d+)?: (\{[^]*\})$/);
    if (!envelope) return;
    let row;
    try { row = JSON.parse(envelope[1]); } catch { return; }
    if (row?.kind === "kmx.host-progress") { run.approval.progress(row); return; }
    if (!isObj(row) || !run.caseIds.includes(row.caseId) || run.rows.has(row.caseId) ||
        !["pass", "fail", "unknown"].includes(row.verdict) || typeof row.reason !== "string") return;
    const checks = run.examples.find(e => e.id === row.caseId)?.assertions.filter(a => a.id === row.failedAssertionId);
    const failedAssertionId = row.verdict === "fail" && checks?.length === 1 && checks[0].type !== "llmJudge" ? row.failedAssertionId : undefined;
    run.rows.set(row.caseId, { caseId: row.caseId, verdict: row.verdict, reason: clean(row.reason, 500), ...(failedAssertionId ? { failedAssertionId } : {}) });
  }
  function stop(id) {
    const run = current;
    if (!run || run.id !== id || !run.pending || disposed) return { run: undefined };
    if (!run.stopRequested) {
      // Mark locally before abort: acknowledgement and execute rejection may
      // arrive in either order. Abort is session-wide, never a per-tool API.
      run.stopRequested = true; run.state = "stopping"; run.approval?.clear();
      run.abortAck = new Promise(resolve => { run.releaseAbort = resolve; });
      try {
        Promise.resolve(session.rpc.abort({ reason: "remote_abort" })).then(
          ack => run.releaseAbort(ack?.success === true), () => run.releaseAbort(false));
      } catch { run.releaseAbort(false); }
    }
    return { stopping: true };
  }
  async function invoke(operation, args, scope) {
    let metadata;
    try { metadata = await session.rpc.tools.getCurrentMetadata(); }
    catch { throw new ViewError("unavailable"); }
    if (disposed) throw new ViewError("unavailable");
    const serverName = ["run", "test"].includes(operation) ? previewServer(session) : SERVER_NAME;
    const tools = metadata.tools?.filter((t) => t.mcpServerName === serverName && t.mcpToolName === operation);
    if (tools?.length !== 1) throw new ViewError("unavailable");
    let result, run;
    if (operation === "test") {
      run = { ...scope, toolCallId: randomBytes(24).toString("base64url"), rows: new Map(), state: "running", started: Date.now(), pending: true };
      current = run; run.approval = observePreview(session, run.toolCallId);
      if (typeof session.on === "function") run.unsubscribe = session.on("tool.execution_progress", event => progress(run, event));
    } else if (operation === "edit" && scope?.id) {
      // Edit before a Test can wait for approval in chat too; progress reports it.
      saving = { id: scope.id, toolCallId: randomBytes(24).toString("base64url") }; saving.approval = observePreview(session, saving.toolCallId);
    }
    const call = run ?? (operation === "edit" ? saving : undefined);
    const unconfirmed = () => {
      const approvalTimeout = run?.approval?.timedOut() === true;
      const error = new ViewError("outcome-unknown", approvalTimeout
        ? "This test timed out waiting for Copilot approval. The tool may still execute if approval is granted in chat. Reload to inspect the saved file before continuing."
        : MESSAGES["outcome-unknown"]);
      if (approvalTimeout) error.approvalTimeout = true;
      return error;
    };
    try {
      result = await session.rpc.tools.execute({ name: tools[0].name, arguments: { schemaVersion: 1, path, ...args }, ...(call ? { toolCallId: call.toolCallId } : {}) });
    } catch {
      finish(run);
      if (run?.stopRequested && await run.abortAck && !disposed) return { stopped: true };
      uncertain = true; throw unconfirmed();
    } finally { finish(run); run?.releaseAbort?.(false); if (call === saving) { saving?.approval.clear(); saving = undefined; } }
    if ((run?.approval?.pendingTool() || run?.approval?.timedOut()) && result?.resultType !== "success") {
      uncertain = true; throw unconfirmed();
    }
    if (result?.resultType === "timeout") { uncertain = true; throw new ViewError("outcome-unknown"); }
    // A canonical setter refusal/unknown is an SDK error WITH an acknowledgement.
    // Do not flatten its changed/mayHaveWritten fields into a generic failure.
    if (operation === "set_tool_state") return result;
    if (result?.resultType !== "success") {
      const detail = typeof result === "string" ? result : [result?.textResultForLlm, result?.error].filter((s) => typeof s === "string").join("\n");
      const code = ["rejected", "denied"].includes(result?.resultType) ? "permission_required"
        : result?.structuredContent?.reasonCode === "invalid_tool_settings" || /\binvalid_tool_settings\b/.test(detail) ? "invalid_tool_settings" : /stale_revision/.test(detail) ? "stale_revision" : /confirmation_required/.test(detail) ? "confirmation_required" : "action-failed";
      const error = new ViewError(code);
      // Only the structured payload returned by this execute call can attest a
      // failed invocation. A matching older resource is not execution evidence.
      if (operation === "test" || operation === "run") error.preview = result?.structuredContent;
      throw error;
    }
    return result;
  }
  const sameSettings = (a, b) => a.length === b.length && a.every(s => b.find(t => t.name === s.name)?.state === s.state);
  function settingsUnknown() {
    uncertain = true;
    throw new ViewError("outcome-unknown", "The tool settings outcome could not be confirmed. Tests did not run. Do not retry; reload to inspect the saved agent and settings.");
  }
  function settingsReceipt(result, view, change) {
    if (["rejected", "denied"].includes(result?.resultType)) throw new ViewError("permission_required");
    let r = result?.structuredContent;
    if (r === undefined && typeof result?.textResultForLlm === "string" && Buffer.byteLength(result.textResultForLlm) <= 16384) {
      try { r = JSON.parse(result.textResultForLlm); } catch { settingsUnknown(); }
    }
    if (!isObj(r) || r.schemaVersion !== 1 || r.operation !== "set_tool_state") settingsUnknown();
    if (r.file !== undefined && (!isObj(r.file) || r.file.path !== path || !/^sha256:[a-f0-9]{64}$/.test(r.file.revision))) settingsUnknown();
    if (["refused", "failed"].includes(r.state) && r.changed === false && r.mayHaveWritten === false
      && !Object.hasOwn(r, "toolSettingsRevision") && !Object.hasOwn(r, "toolSettings") && typeof r.reasonCode === "string") {
      throw new ViewError(["stale_revision", "stale_settings_revision", "invalid_tool_settings"].includes(r.reasonCode) ? r.reasonCode : "settings-failed");
    }
    // Unknown (including a lost/malformed receipt) grants no settings token or
    // retry authority. The checked-agent file identity is not settings evidence.
    if (r.state !== "complete" || result.resultType !== "success" || r.file?.revision !== view.file.revision
      || typeof r.changed !== "boolean" || r.mayHaveWritten !== r.changed
      || typeof r.toolSettingsRevision !== "string" || !/^(?:absent|sha256:[a-f0-9]{64})$/.test(r.toolSettingsRevision)
      || (r.toolSettingsRevision === view.toolSettingsRevision) !== !r.changed) settingsUnknown();
    let states;
    try { states = cleanSettings(r.toolSettings); } catch { settingsUnknown(); }
    const expected = view.toolSettings.map(s => s.name === change.name ? change : s);
    if (!sameSettings(states, expected)) settingsUnknown();
    return { changed: r.changed, token: r.toolSettingsRevision, states };
  }
  const pendingRecorded = (view, ids) => pendingCapture(view.authoring.examples, view.toolSettings, ids);
  async function recordingTests(view, input, selected, ids, onPublish) {
    const models = input.comparisonModels ?? [selected], comparing = models.length > 1, results = [];
    if (!Array.isArray(models) || models.length < 1 || models.length > 2 || new Set(models).size !== models.length || !models.includes(selected) || !models.every(validModel)) throw new ViewError("invalid-action");
    const settings = view.toolSettings.map(s => ({...s})), key = v => JSON.stringify([v.file.revision, ids, settings, input.showContent === true]);
    // Only a live capture (not a replay) can write recorded calls into a Copilot file's tests.
    const captureAck = input.acknowledgeCopilotTests === true && hasFrontmatter(view.authoring.source);
    let published = false, capture;
    const run = async (model, replay) => {
      // A comparison partner that doesn't finish is a confirmed result, not an unknown one.
      const partner = comparing && model !== selected;
      let response;
      try { response = await invoke("test", { model, expectedRevision: view.file.revision, expectedToolSettings: settings, recordedReplayOnly: replay, ...(!replay && captureAck ? { acknowledgeCopilotTests: true } : {}), ...(input.caseId !== undefined ? {caseId:input.caseId} : {}), showContent: input.showContent === true, showFailedContent:true }, { id:input.runId, revision:view.file.revision,model,examples:view.authoring.examples,caseIds:ids,totalCases:view.examples.length,toolSettings:settings }); }
      catch (e) { if (e.code !== "action-failed" || e.preview?.state !== "complete" && !(partner && e.preview?.state === "error")) throw e; response = {structuredContent:e.preview}; }
      let raw = response.structuredContent;
      if (!raw && typeof response.textResultForLlm === "string" && Buffer.byteLength(response.textResultForLlm) <= MAX_RESOURCE_BYTES) { try {raw=JSON.parse(response.textResultForLlm);} catch {} }
      if (response.stopped) {
        const fresh = await readView(session,path); if (fresh.file.revision !== view.file.revision) throw new ViewError("outcome-unknown");
        current.state = "stopped"; view = {...fresh,latest:{status:"none",badge:"none"}}; return {stopped:true};
      }
      if (raw?.file?.path !== path || raw.file.revision !== view.file.revision) throw new ViewError("outcome-unknown");
      const result = cleanResult(raw,"current",view.file.revision);
      if (result.modelRoute.slice(result.modelRoute.indexOf("/")+1) !== model || result.state !== (partner && raw.state === "error" ? "error" : "complete") || !sameSettings(result.toolSettings ?? [],settings) || JSON.stringify(result.selection.caseIds) !== JSON.stringify(ids)) throw new ViewError("outcome-unknown");
      if (replay && result.recordedReplayOnly !== true) throw new ViewError("outcome-unknown");
      const fresh = await readView(session,path), flags = result.examples.flatMap(e => e.tools ?? []).filter(t => t.recordingSaved);
      if (result.savedRevision) {
        if (replay || !flags.length || fresh.file.revision !== result.savedRevision || flags.some(t => t.mode !== "recorded" || t.source !== "live")) throw new ViewError("outcome-unknown");
        published = true; onPublish(fresh);
      } else if (flags.length || fresh.file.revision !== view.file.revision) throw new ViewError("revision_mismatch");
      if (!sameSettings(fresh.toolSettings,settings)) throw new ViewError("tool_settings_changed");
      view = fresh;
      const row = { result, executedRevision: raw.file.revision, view: {...fresh,latest:{status:"current",badge:result.fullPass?"pass":"not-passing",result}} };
      if (replay && result.state === "complete") { certified.set(key(fresh)+model,row); while(certified.size>2)certified.delete(certified.keys().next().value); }
      return row;
    };
    if (!comparing) {
      const pending = pendingRecorded(view,ids), row = await run(selected,!pending);
      if (row.stopped) return {stopped:true,run:snapshot(),view,saved:false};
      if (pending) captureAttempt = {key:key(view),model:selected};
      return {view:row.result.savedRevision ? view : row.view,saved:published,...(pending ? {capture:{...row.result,executedRevision:row.executedRevision,model:selected}} : {})};
    }
    if (pendingRecorded(view,ids) && captureAttempt?.key !== key(view)) {
      capture = await run(selected,false);
      if (capture.stopped) return {stopped:true,run:snapshot(),view,saved:published};
      captureAttempt = {key:key(view),model:selected};
    }
    if (pendingRecorded(view,ids)) return {error:"recording_missing",message:"A Recorded tool has no saved call. Change the test or tool mode before comparing.",view,saved:published};
    for (const model of models) {
      const row = model !== selected && certified.get(key(view)+model) || await run(model,true); if(row.stopped)return {stopped:true,run:snapshot(),view,saved:published};results.push(row.view);
    }
    return {view:results.find(v=>v.latest.result.modelRoute.endsWith("/"+selected)),saved:published,comparison:{revision:view.file.revision,models,results}};
  }
  const act = async (input) => {
    if (!isObj(input) || !Object.hasOwn(allowed, input.operation) || Object.keys(input).some((k) => k !== "operation" && !allowed[input.operation].includes(k))) return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if (input.operation === "reload") { captureAttempt = undefined; certified.clear(); }
    if (input.acknowledgeCopilotTests !== undefined && typeof input.acknowledgeCopilotTests !== "boolean") return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if (input.showContent !== undefined && typeof input.showContent !== "boolean") return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if (input.model !== undefined && input.model !== "" && !validModel(input.model)) return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    if ((Object.hasOwn(input, "runId") || ["progress", "stop"].includes(input.operation)) && !validRunId(input.runId)) return { error: "invalid-action", message: MESSAGES["invalid-action"] };
    // These fixed operations share the POST's security checks, but must remain
    // available during Test and cannot clear an existing uncertainty lock.
    if (input.operation === "progress") {
      const run = current;
      if (saving && saving.id === input.runId) { await saving.approval.refresh(); return { run: undefined, ...(saving?.approval.waiting() ? { approvalWaiting: true } : {}) }; }
      if (run?.id !== input.runId) return { run: undefined };
      await run.approval?.refresh();
      return { run: current === run && !disposed ? snapshot() : undefined };
    }
    if (input.operation === "stop") return stop(input.runId);
    // Read-only discovery must not block or unlock an execution action.
    if (input.operation === "models") {
      try { return await readModels(session); } catch (e) { const error = safeError(e); return { error: error.code, message: error.message }; }
    }
    if (input.operation === "tools") {
      try { return await readToolCatalog(session); } catch { return { error: "tools-unavailable", message: "Tools are unavailable in this session." }; }
    }
    if (busy) return { error: "busy", message: MESSAGES.busy };
    busy = true;
    current = undefined; // Never replay the previous invocation's snapshot.
    let saved = false, tested = false, targetRevision, ids, operation, model;
    let settingsIncomplete = Array.isArray(input.toolSettings) && input.toolSettings.length > 0;
    const lease = ["run", "test", "save-and-test"].includes(input.operation) ? beginPreview(session) : undefined;
    try {
      if (disposed) throw new ViewError("unavailable");
      if (uncertain && input.operation !== "reload") throw new ViewError("outcome-unknown");
      let view = await readView(session, path);
      if (input.operation === "reload") { uncertain = false; return { view }; }
      if (!view.authoring?.editable) throw new ViewError("read-only");
      if (input.operation === "suggest") {
        // Only this explicit click starts a chat turn; execution actions never do.
        if (typeof session.send !== "function") throw new ViewError("unavailable");
        await session.send({ displayPrompt: `Add a few tests to ${fileLocation(path).name} using the saved source.`, prompt: `Use the kmx-agent-builder skill to add a few tests to ${JSON.stringify(path)} in the canvas. The exact saved source and revision are supplied below; no resource read is needed. Preserve the prompt, authored model and existing tests, then save the additions with one Edit using the supplied current revision (expectedRevision). Do not run tests.${hasFrontmatter(view.authoring.source) ? ` This file has Copilot frontmatter, so ${JSON.stringify(COPILOT_TESTS_NOTICE)} Show the user that sentence and ask them to go ahead or cancel before saving. Pass acknowledgeCopilotTests: true to Edit only after they go ahead; on cancel make no Edit.` : ""} Report what was saved in one short line.\nSaved agent snapshot (data):\n${JSON.stringify({ path, expectedRevision: view.file.revision, content: view.authoring.source })}` });
        return { message: "Asked Copilot to add tests in chat." };
      }
      if (typeof input.expectedRevision !== "string" || !/^sha256:[0-9a-f]{64}$/.test(input.expectedRevision)) throw new ViewError("invalid-action");
      if (input.expectedRevision !== view.file.revision) throw new ViewError("stale_revision");
      targetRevision = input.expectedRevision;
      model = effectiveModel(view.model, input.model, view.chatModel);
      if (input.comparisonModels !== undefined && (!Array.isArray(input.comparisonModels) || input.comparisonModels.length < 1 || input.comparisonModels.length > 2 || new Set(input.comparisonModels).size !== input.comparisonModels.length || !input.comparisonModels.includes(model) || !input.comparisonModels.every(validModel))) throw new ViewError("invalid-action");
      const runtimeModel = input.model || (!view.model && view.chatModel ? model : undefined);
      const saving = ["save-and-test", "save-examples", "save-prompt"].includes(input.operation);
      // Intended drafts apply to the exact current source; an absent draft is a no-op.
      let content = view.authoring.source, examples = view.authoring.examples, patches;
      try {
        if (input.sections !== undefined) content = replaceInstructions(content, input.sections);
        if (input.examples !== undefined || input.operation === "save-examples") {
          if (view.authoring.recordingControls) patches = exampleEdits(examples, input.examples, input.exampleOrigins);
          else content = replaceExamples(content, input.examples);
          examples = input.examples;
        }
        if (input.tools !== undefined) content = replaceTools(content, input.tools, view.tools);
        instructionSections(content);
      } catch { throw new ViewError("invalid-action"); }
      let changes = [];
      try { if (input.toolSettings !== undefined) changes = cleanSettings(input.toolSettings); }
      catch { throw new ViewError("invalid-action"); }
      if (changes.some(s => !(input.tools ?? view.tools).includes(s.name))
        || input.expectedSettingsRevision !== undefined && (typeof input.expectedSettingsRevision !== "string" || !/^(?:absent|sha256:[a-f0-9]{64})$/.test(input.expectedSettingsRevision))) throw new ViewError("invalid-action");
      const originalToken = view.toolSettingsRevision;
      if (changes.length) {
        // Availability is checked BEFORE Edit, including zero-tool legacy views.
        const metadata = await session.rpc.tools.getCurrentMetadata();
        if (metadata.tools?.filter(t => t.mcpServerName === SERVER_NAME && t.mcpToolName === "set_tool_state").length !== 1) throw new ViewError("unavailable");
        if (view.tools.length) {
          if (originalToken === undefined) throw new ViewError("unavailable");
          if (input.expectedSettingsRevision === undefined) throw new ViewError("invalid-action");
          if (input.expectedSettingsRevision !== originalToken) throw new ViewError("stale_settings_revision");
        } else if (input.expectedSettingsRevision !== undefined) throw new ViewError("invalid-action");
      }
      ids = examples.map((e) => e.id);
      // A selected case must exist once in the intended examples before any write.
      if (input.caseId !== undefined && (typeof input.caseId !== "string" || ids.filter((id) => id === input.caseId).length !== 1)) throw new ViewError("invalid-action");
      // A first live capture writes recorded calls into a Copilot file's tests: the same go-ahead covers it.
      const selected = input.caseId === undefined ? ids : [input.caseId];
      if (["test", "save-and-test", "run"].includes(input.operation) && view.authoring.recordingControls && hasFrontmatter(view.authoring.source)
        && pendingCapture(examples, view.toolSettings.map(s => changes.find(c => c.name === s.name) ?? s), selected) && input.acknowledgeCopilotTests !== true) throw new ViewError("confirmation_required");
      // The page asks the user first; the server independently enforces the same rule.
      const acknowledge = (input.examples !== undefined || input.operation === "save-examples") && hasFrontmatter(view.authoring.source) && (patches?.length > 0 || testsNeedAcknowledgement(view.authoring.source, view.authoring.examples, examples));
      if (saving) {
        if (acknowledge && input.acknowledgeCopilotTests !== true) throw new ViewError("confirmation_required");
        if (Buffer.byteLength(content, "utf8") > 256 * 1024) throw new ViewError("too-large");
        if (content !== view.authoring.source || patches?.length) {
          captureAttempt = undefined; certified.clear();
          const edit = await invoke("edit", { expectedRevision: input.expectedRevision, content, ...(patches?.length ? { exampleEdits: patches } : {}), ...(acknowledge ? { acknowledgeCopilotTests: true } : {}) }, validRunId(input.runId) ? { id: input.runId } : undefined);
          // A later settings read/write failure must retain this acknowledged Edit.
          if (changes.length) saved = true;
          if (patches?.length) {
            let ack = edit.structuredContent;
            if (!ack && typeof edit.textResultForLlm === "string") { try { ack = JSON.parse(edit.textResultForLlm); } catch {} }
            if (ack?.schemaVersion !== 1 || ack.operation !== "edit" || ack.file?.path !== path || !/^sha256:[a-f0-9]{64}$/.test(ack.file.revision)) { uncertain = true; throw new ViewError("outcome-unknown"); }
            targetRevision = ack.file.revision;
          } else targetRevision = `sha256:${createHash("sha256").update(content).digest("hex")}`;
          try { view = await readView(session, path); }
          catch { uncertain = true; throw new ViewError("outcome-unknown"); }
          if (view.file.revision !== targetRevision) { uncertain = true; throw new ViewError("outcome-unknown"); }
          saved = true;
        }
        if (["save-examples", "save-prompt"].includes(input.operation)) return { view, saved };
      }
      if (changes.length && originalToken !== undefined && view.toolSettingsRevision !== originalToken) throw new ViewError("stale_settings_revision");
      for (const change of changes) {
        if (view.toolSettingsRevision === undefined) throw new ViewError("unavailable");
        const result = await invoke("set_tool_state", { expectedRevision: targetRevision, expectedSettingsRevision: view.toolSettingsRevision, ...change });
        const receipt = settingsReceipt(result, view, change);
        saved ||= receipt.changed;
        let next;
        try { next = await readView(session, path); } catch { settingsUnknown(); }
        if (next.file.revision !== targetRevision || next.toolSettingsRevision !== receipt.token || !sameSettings(next.toolSettings, receipt.states)) settingsUnknown();
        view = next;
      }
      settingsIncomplete = false;
      operation = input.operation === "run" ? "run" : "test";
      if (operation === "run" && (typeof input.input !== "string" || Buffer.byteLength(input.input, "utf8") > 16384)) throw new ViewError("invalid-action");
      // Test/Run have no revision parameter. Check the actual result afterward,
      // not a fabricated precondition or a refreshed draft revision.
      if (view.tools.length) {
        try { await ensureHostRuntime(session, lease); } catch { /* Original server retains the plain unbound refusal; never invent a binding. */ }
      }
      tested = true;
      if (operation === "test" && view.authoring.recordingControls) { const result = await recordingTests(view,input,model,input.caseId === undefined ? ids : [input.caseId],fresh=>{saved=true;targetRevision=fresh.file.revision}); return {...result,saved:saved||result.saved}; }
      const outcome = await invoke(operation, { ...(runtimeModel ? { model: runtimeModel } : {}), ...(operation === "run" ? { input: input.input } : {}), ...(input.caseId !== undefined ? { caseId: input.caseId } : {}),
          // Only a live first capture writes into a Copilot file's tests; the go-ahead covers exactly that.
          ...(operation === "run" && input.acknowledgeCopilotTests === true && view.authoring.recordingControls && hasFrontmatter(view.authoring.source) && pendingRecorded(view, ids) ? { acknowledgeCopilotTests: true } : {}),
          showContent: input.showContent === true, showFailedContent: true },
        { id: input.runId, revision: targetRevision, model, examples, caseIds: input.caseId === undefined ? ids : [input.caseId], totalCases: ids.length,
          ...(view.tools.length ? { toolSettings: view.toolSettings.map(s => ({ ...s })) } : {}) });
      // A Run can save a first live capture: the result describes the executed (older)
      // revision, the file is now the saved one. Never attribute the result to the new revision.
      let raw = outcome.structuredContent;
      if (operation === "run" && !raw && typeof outcome.textResultForLlm === "string" && Buffer.byteLength(outcome.textResultForLlm) <= MAX_RESOURCE_BYTES) { try { raw = JSON.parse(outcome.textResultForLlm); } catch {} }
      if (operation === "run" && !outcome.stopped && raw?.savedRevision !== undefined) {
        let result, fresh;
        try { result = cleanResult(raw, "current", targetRevision); fresh = await readView(session, path); }
        catch { uncertain = true; throw new ViewError("outcome-unknown"); }
        const flags = result.examples.flatMap(e => e.tools ?? []).filter(t => t.recordingSaved);
        if (raw.file?.path !== path || !flags.length || fresh.file.revision !== result.savedRevision || flags.some(t => t.mode !== "recorded" || t.source !== "live")) { uncertain = true; throw new ViewError("outcome-unknown"); }
        saved = true; targetRevision = fresh.file.revision;
        if (!sameSettings(fresh.toolSettings, view.toolSettings)) throw new ViewError("tool_settings_changed");
        return { view: fresh, saved, capture: { ...result, executedRevision: raw.file.revision, model } };
      }
      try { view = await readView(session, path); }
      catch { uncertain = true; throw new ViewError("outcome-unknown"); }
      if (outcome.stopped) {
        if (view.file.revision !== targetRevision) { uncertain = true; throw new ViewError("outcome-unknown"); }
        current.state = "stopped";
        return { stopped: true, run: snapshot(), saved, view: { ...view, latest: { status: "none", badge: "none" } } };
      }
      if (view.file.revision !== targetRevision) throw new ViewError("revision_mismatch");
      if (!observed(view, operation, ids, input.caseId, true, model)) { uncertain = true; throw new ViewError("outcome-unknown"); }
      return { view, saved, ...(snapshot() ? { run: snapshot() } : {}) };
    } catch (e) {
      const err = safeError(e);
      if (err.code === "outcome-unknown") uncertain = true;
      const response = { error: err.code, message: err.message, saved, ...(settingsIncomplete ? { settingsIncomplete: true } : {}), ...(err.approvalTimeout ? { approvalTimeout: true } : {}) };
      const failedPreview = tested && err.code === "action-failed";
      if (!settingsIncomplete && (saved || failedPreview) && err.code !== "revision_mismatch" && err.code !== "outcome-unknown") {
        try {
          const view = await readView(session, path);
          // Project the invocation payload through the same bounded parser as
          // the resource, then require exact agreement before attaching a view.
          const proof = err.preview?.file?.path === path ? cleanResult(err.preview, "current", targetRevision) : undefined;
          const fresh = failedPreview && proof && observed(view, operation, ids, input.caseId, false, model)
            && JSON.stringify(proof) === JSON.stringify(view.latest.result)
            && view.latest.result.verdict !== "pass" && !view.latest.result.fullPass;
          if (view.file.revision === targetRevision && (fresh || saved && !failedPreview && view.latest.status !== "current")) response.view = view;
          if (response.view && fresh && proof.reason) response.message = proof.reason;
          if (response.view && fresh && !ids.length && view.latest.result.reasonCode === "no_tests") Object.assign(response, { error: "no-tests", message: MESSAGES["no-tests"] });
        } catch { /* Preserve the draft when refresh cannot be confirmed. */ }
      }
      if (failedPreview && !response.view) {
        uncertain = true;
        Object.assign(response, { error: "outcome-unknown", message: MESSAGES["outcome-unknown"] });
      }
      return response;
    } finally { lease?.release(); busy = false; }
  };
  act.dispose = () => {
    disposed = true;
    saving?.approval.clear(); saving = undefined; // The Edit itself is neither cancelled nor retried.
    finish(current); current?.releaseAbort?.(false); current = undefined;
  };
  return act;
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
  "tools-view.mjs": ["tools-view.mjs", "text/javascript; charset=utf-8"],
  "test-tools.mjs": ["test-tools.mjs", "text/javascript; charset=utf-8"],
  "editor.mjs": ["editor.mjs", "text/javascript; charset=utf-8"],
  "dom.mjs": ["dom.mjs", "text/javascript; charset=utf-8"],
  "results.mjs": ["results.mjs", "text/javascript; charset=utf-8"],
  "failure-wording.mjs": ["failure-wording.mjs", "text/javascript; charset=utf-8"],
  "check-controls.mjs": ["check-controls.mjs", "text/javascript; charset=utf-8"],
  "instruction-text.mjs": ["instruction-text.mjs", "text/javascript; charset=utf-8"],
  "run-views.mjs": ["run-views.mjs", "text/javascript; charset=utf-8"],
  "draft-controller.mjs": ["draft-controller.mjs", "text/javascript; charset=utf-8"],
  "action-controller.mjs": ["action-controller.mjs", "text/javascript; charset=utf-8"],
  "observation-controller.mjs": ["observation-controller.mjs", "text/javascript; charset=utf-8"],
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
      act.dispose();
      server.close(() => resolve());
      server.closeAllConnections();
    }));
  return { url: `http://127.0.0.1:${port}/${token}/`, port, close };
}

// --- canvas/command lifecycle ----------------------------------------------
export function createViewController({ getSession }) {
  const instances = new Map(); // instanceId -> { path, ready }
  const stop = (entry) => entry.ready.then((b) => b.close(), () => {});

  async function open(ctx, validatedView) {
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
      const title = (validatedView ? Promise.resolve(validatedView) : readView(session, path)).then(view => agentName(view.title), () => fileLocation(path).name);
      entry = { path, ready: Promise.all([startViewBridge({ session, path }), title]).then(([bridge, name]) => ({ ...bridge,
        title: name + (fileLocation(path).folder ? ` · ${fileLocation(path).folder}` : "") })) };
      instances.set(id, entry);
      entry.ready.catch(() => instances.get(id) === entry && instances.delete(id));
      if (existing) await stop(existing);
    }
    try {
      const bridge = await entry.ready;
      if (instances.get(id) !== entry) throw new ViewError("unavailable");
      return { url: bridge.url, title: bridge.title, status: "Agent file" };
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
    if (getSession()) await closeHostRuntime(getSession());
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
