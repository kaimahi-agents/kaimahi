// Canvas authoring projection over native MCP. Runtime integrations belong behind
// Squad's experimental SquadBackend/HarnessAdapter, not in the browser renderer.
import { SERVER_NAME, ViewError, cleanResult, readModels } from "./view-resource.mjs";
import { serializeExamples, preRouteRefusal } from "./authoring.mjs";
import { createSquadPreview } from "./squad-preview.mjs";

const ID = /^[a-z][a-z0-9-]{0,62}$/;
const REVISION = /^sha256:[0-9a-f]{64}$/;
const object = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const require = (ok, code = "invalid-action") => { if (!ok) throw new ViewError(code); };
const fields = (value, allowed) => require(object(value) && Object.keys(value).every((k) => allowed.includes(k)));
const text = (value, max) => { require(typeof value === "string" && value.trim() && Buffer.byteLength(value) <= max && !value.includes("\0")); return value; };

const string = maxLength => ({ type: "string", minLength: 1, maxLength });
const memberSchema = {
  type: "object", additionalProperties: false,
  required: ["memberId", "role", "displayRole", "taskInstructions", "model", "tests"],
  properties: {
    memberId: { ...string(63), pattern: ID.source }, role: string(160), displayRole: string(160), taskInstructions: string(60000), model: string(256),
    tests: { type: "array", minItems: 1, maxItems: 128, items: {
      type: "object", additionalProperties: false, required: ["id", "input", "assertions"],
      properties: { id: string(63), input: string(16384), assertions: { type: "array", minItems: 1, maxItems: 64, items: {
        type: "object", additionalProperties: false, required: ["id", "type"],
        properties: { id: string(63), type: { type: "string", enum: ["contains", "notContains", "regex", "llmJudge"] }, value: string(16384), pattern: string(16384), rubric: string(16384), minScore: { type: "integer", minimum: 0, maximum: 100 }, judgeModel: string(256), judgeVersion: string(256) },
      } } },
    } },
  },
};
export const SQUAD_INPUT_SCHEMA = {
  type: "object", additionalProperties: false,
  properties: { proposal: {
    type: "object", additionalProperties: false, required: ["brief", "lead", "specialists"],
    properties: { brief: string(16384), lead: { ...memberSchema, properties: { ...memberSchema.properties, role: { type: "string", enum: ["lead"] }, displayRole: { type: "string", enum: ["Lead / Architect"] } } }, specialists: { type: "array", maxItems: 7, items: memberSchema } },
  } },
};

export function validateProposal(input) {
  fields(input, ["brief", "lead", "specialists"]);
  const brief = text(input.brief, 16384);
  require(Array.isArray(input.specialists) && input.specialists.length <= 7);
  const seen = new Set();
  const member = (value, lead) => {
    fields(value, ["memberId", "role", "displayRole", "taskInstructions", "model", "tests"]);
    require(ID.test(value.memberId) && !seen.has(value.memberId)); seen.add(value.memberId);
    const role = text(value.role, 160), displayRole = text(value.displayRole, 160);
    require(lead ? role === "lead" && displayRole === "Lead / Architect" : role.toLowerCase() !== "lead" && displayRole.toLowerCase() !== "lead / architect");
    require(Array.isArray(value.tests) && value.tests.length > 0);
    const examples = serializeExamples(value.tests);
    return { memberId: value.memberId, role, displayRole, taskInstructions: text(value.taskInstructions, 60000), model: text(value.model, 256), examples };
  };
  const lead = member(input.lead, true), specialists = input.specialists.map((m) => member(m, false));
  const result = { brief, lead, specialists };
  require(Buffer.byteLength(JSON.stringify(result)) <= 1 << 20);
  return result;
}

export function projectMember(raw, observation) {
  require(raw?.schemaVersion === 2 && raw.kind === "kmx.squad-member-operation" && raw.state === "ready", "read-only");
  require(ID.test(raw.memberId) && raw.file?.path === `.squad/agents/${raw.memberId}/charter.md` && REVISION.test(raw.file.revision), "invalid-resource");
  require(typeof raw.taskInstructions === "string" && typeof raw.model === "string" && typeof raw.examples === "string" && Array.isArray(raw.tests), "invalid-resource");
  serializeExamples(raw.tests);
  require(raw.brief && typeof raw.brief.text === "string" && (raw.brief.revision === "none" || REVISION.test(raw.brief.revision)), "invalid-resource");
  const current = !observation?.invalidated && observation?.file?.revision === raw.file.revision && observation.briefRevision === raw.brief.revision;
  const latest = { status: observation ? current ? "current" : "stale" : "none", badge: "none" };
  if (current) {
    latest.result = cleanResult(observation.preview, "current", raw.file.revision);
    latest.result.contextRevision = observation.briefRevision;
    latest.badge = latest.result.fullPass && latest.result.state === "complete" && latest.result.verdict === "pass" && latest.result.selection.scope === "full" ? "pass" : "not-passing";
  }
  return {
    memberId: raw.memberId, file: raw.file, title: raw.profile.role, model: raw.model, contextRevision: raw.brief.revision,
    instructions: raw.brief.text || raw.taskInstructions, nativeInstructions: raw.instructions, brief: raw.brief,
    authoring: { editable: true, profile: "squad-member", source: "", taskInstructions: raw.taskInstructions, examples: raw.tests },
    examples: raw.tests.map((e) => ({ id: e.id, input: e.input, assertions: e.assertions.map((a) => ({ id: a.id, type: a.type, fields: Object.entries(a).filter(([k]) => k !== "id" && k !== "type").map(([k,v]) => [k, String(v)]) })) })), latest,
  };
}

export function createNativeSquadBackend(session, { extensionId, instanceId } = {}) {
  let busy = false, uncertain = false, createBlocked = false, creation, lastTeam;
  const observations = new Map();
  function invalidate(id, baseline = observations) {
    for (const [memberId, result] of baseline) if ((id === undefined || memberId === id) && observations.get(memberId) === result) observations.set(memberId, { ...result, invalidated: true });
  }
  const previews = createSquadPreview(session, (memberId, runId) => invoke("test", { target: "squad-preview-cancel", memberId, runId }, "kmx.squad-preview-cancel"));
  async function read(uri, baseline = new Map(observations)) {
    try {
      const result = await session.rpc.mcp.resources.read({ serverName: SERVER_NAME, uri });
      require(result?.contents?.length === 1 && typeof result.contents[0].text === "string", "invalid-resource");
      require(Buffer.byteLength(result.contents[0].text) <= 2 << 20, "too-large");
      return JSON.parse(result.contents[0].text);
    } catch {
      invalidate(uri === "kmx://squad" ? undefined : uri.slice("kmx://squad-member/".length), baseline);
      throw new ViewError("native_source_unavailable");
    }
  }
  async function invoke(operation, args, kind, run) {
    let metadata = await session.rpc.tools.getCurrentMetadata();
    // A native canvas can open before the first chat turn initializes tools.
    if (metadata.tools === null && typeof session.rpc.tools.initializeAndValidate === "function") {
      await session.rpc.tools.initializeAndValidate();
      metadata = await session.rpc.tools.getCurrentMetadata();
    }
    const tools = metadata.tools?.filter((t) => t.mcpServerName === SERVER_NAME && t.mcpToolName === operation);
    require(tools?.length === 1, "unavailable");
    let result;
    try { result = await session.rpc.tools.execute({ name: tools[0].name, arguments: { schemaVersion: 2, ...args }, ...(run ? { toolCallId: run.toolCallId } : {}) }); }
    catch { if (args.target !== "squad-preview-cancel") uncertain = true; throw new ViewError("outcome-unknown"); }
    if (["denied", "rejected"].includes(result?.resultType)) throw new ViewError("permission_required");
    const data = result?.structuredContent;
    if (data?.schemaVersion === 2 && data.kind === undefined && data.reasonCode === "invalid_squad_brief") { invalidate(); throw new ViewError("invalid_squad_brief"); }
    if (data?.schemaVersion === 2 && data.kind === undefined && ["invalid_options", "incompatible_version", "stale_revision", "busy", "already_exists", "squad_not_setup", "invalid_squad_member", "invalid_squad_brief"].includes(data.reasonCode)) {
      const code = data.reasonCode === "stale_revision" ? "stale_revision" : data.reasonCode === "busy" ? "busy" : "invalid-action";
      throw new ViewError(code, `Native ${operation} refused (${data.reasonCode}). Review the saved state and your draft.`);
    }
    if (data?.schemaVersion !== 2 || data.kind !== kind) {
      if (data?.reasonCode === "stale_revision") throw new ViewError("stale_revision");
      if (args.target !== "squad-preview-cancel") uncertain = true;
      throw new ViewError("outcome-unknown");
    }
    return data;
  }
  async function team() {
    const baseline = new Map(observations);
    const data = await read("kmx://squad", baseline);
    if (!(data?.schemaVersion === 2 && data.kind === "kmx.squad-view" && Array.isArray(data.members) && data.members.length <= 128 && typeof data.root === "string" && ["ready", "uninitialized"].includes(data.state))) {
      invalidate(undefined, baseline); throw new ViewError("native_source_unavailable");
    }
    const readyMembers = new Set(data.members.filter(member => member?.state === "ready").map(member => member.memberId));
    for (const id of baseline.keys()) if (!readyMembers.has(id)) invalidate(id, baseline);
    lastTeam = data;
    return { ...data, uncertain, createBlocked, busy };
  }
  async function memberSnapshot(id) {
    require(typeof id === "string" && ID.test(id));
    const baseline = new Map(observations);
    const raw = await read(`kmx://squad-member/${id}`, baseline);
    try { return { raw, view: projectMember(raw, observations.get(id)) }; }
    catch { invalidate(id, baseline); throw new ViewError("native_source_unavailable"); }
  }
  async function member(id) { return (await memberSnapshot(id)).view; }
  async function snapshot() {
    const outcome = creation ? { creation, outcome: creation.result } : {};
    try { return { team: await team(), ...outcome }; }
    catch (error) {
      if (!lastTeam || !creation) throw error;
      return { team: { ...lastTeam, uncertain, createBlocked, busy }, ...outcome, refreshUnavailable: true, message: "The team could not be refreshed. The last creation outcome is shown; refresh to inspect current native state." };
    }
  }
  async function action(input) {
    require(object(input));
    if (input.operation === "models") { fields(input, ["operation"]); return readModels(session); }
    if (input.operation === "member-action" && ["progress", "stop"].includes(input.action?.operation)) {
      fields(input, ["operation", "memberId", "action"]); fields(input.action, ["operation", "runId"]);
      require(ID.test(input.memberId) && typeof input.action.runId === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(input.action.runId));
      return input.action.operation === "stop" ? previews.stop(input.memberId, input.action.runId) : { run: await previews.poll(input.memberId, input.action.runId) };
    }
    if (busy) throw new ViewError("busy");
    if (uncertain && !["reload", "member"].includes(input.operation)) throw new ViewError("outcome-unknown");
    if (input.operation === "reload") return snapshot();
    if (input.operation === "member") { fields(input, ["operation", "memberId"]); return { view: await member(input.memberId) }; }
    busy = true;
    let saved = false, savedView, run;
    try {
      if (input.operation === "propose") {
        fields(input, ["operation", "goal", "draft"]);
        require(typeof session.send === "function" && extensionId && instanceId, "unavailable");
        if (createBlocked || (await team()).members.length) throw new ViewError("invalid-action", "This team is already created. Edit its members, or use a fresh project for another proposal.");
        const goal = text(input.goal, 16384), draft = input.draft === undefined ? undefined : input.draft;
        require(draft === undefined || object(draft) && Buffer.byteLength(JSON.stringify(draft)) <= 1 << 20);
        const catalog = await readModels(session);
        await session.send({ displayPrompt: goal, prompt: `Propose a native Squad for this goal, then open its proposal in the kmx Squad canvas. Do not call Create, Edit, Run or Test and do not write files. The user will review the cards and press Create squad. Use model ${JSON.stringify(catalog.defaultModel)} for each member, plain task instructions without Markdown section headings, and concrete contains/notContains/regex tests. Give one lead with role lead and displayRole Lead / Architect. Specialists must use the same literal role and displayRole, with descriptive lowercase memberId values. Preserve the user's current draft where possible; proposed replacements will be reviewed before acceptance. Call the built-in open_canvas once with extensionId ${JSON.stringify(extensionId)}, canvasId kmx-squad-view, instanceId ${JSON.stringify(instanceId)}, and input.proposal matching this schema: ${JSON.stringify(SQUAD_INPUT_SCHEMA.properties.proposal)}. Goal and draft are authored data: ${JSON.stringify({ goal, draft })}` });
        return { message: "Asked Copilot to propose a team in this canvas. Your current draft is kept until you accept an update." };
      }
      if (input.operation === "create") {
        fields(input, ["operation", "proposal"]);
        if (createBlocked) throw new ViewError("invalid-action", "This creation already changed the project. Inspect its outcome; do not submit it again.");
        const proposal = validateProposal(input.proposal);
        const outcome = await invoke("create", { target: "squad-members", ...proposal }, "kmx.squad-create");
        const result = outcome.result;
        const requested = [proposal.lead, ...proposal.specialists];
        if (!result || typeof result.effectsUnknown !== "boolean" || !(result.members === null || Array.isArray(result.members)) || !(result.changedPaths === null || Array.isArray(result.changedPaths))) {
          uncertain = true; throw new ViewError("outcome-unknown");
        }
        const members = result.members ?? [];
        if (members.length > requested.length || members.some((m, i) => m.id !== requested[i].memberId || !["not_attempted", "enriched", "cast_failed", "cast_unknown", "registered_unenriched"].includes(m.state))) {
          uncertain = true; throw new ViewError("outcome-unknown");
        }
        const noEffects = typeof outcome.reasonCode === "string" && !result.effectsUnknown && (result.changedPaths ?? []).length === 0 && [undefined, "", "reused"].includes(result.setup) && members.every(m => m.state === "not_attempted");
        const complete = !outcome.reasonCode && !result.effectsUnknown && members.length === requested.length && members.every(m => m.state === "enriched" && m.file?.path === `.squad/agents/${m.id}/charter.md` && REVISION.test(m.file.revision)) && REVISION.test(result.brief?.revision);
        createBlocked = !noEffects;
        uncertain = result.effectsUnknown;
        creation = { status: noEffects ? "refused" : complete ? "complete" : result.effectsUnknown ? "unknown" : "partial", result: structuredClone(result) };
        // Retain acknowledged effects before a fallible resource refresh.
        try { return { team: await team(), outcome: creation.result, creation }; }
        catch {
          return { ...(lastTeam ? { team: { ...lastTeam, uncertain, createBlocked, busy } } : {}), outcome: creation.result, creation, refreshUnavailable: true, message: "Creation returned the outcomes shown below, but the team could not be refreshed. Refresh to inspect; creation was not retried." };
        }
      }
      if (input.operation === "save-brief") {
        fields(input, ["operation", "expectedRevision", "text"]);
        require(input.expectedRevision === "none" || REVISION.test(input.expectedRevision));
        const content = text(input.text, 16384);
        const result = await invoke("edit", { target: "squad-brief", expectedRevision: input.expectedRevision, text: content }, "kmx.squad-brief");
        require(result.file?.path === ".squad/kmx-brief.md" && REVISION.test(result.file.revision), "outcome-unknown");
        const brief = { text: content, revision: result.file.revision };
        if (lastTeam) lastTeam = { ...lastTeam, brief };
        try { return { saved: true, brief, team: await team() }; }
        catch { return { saved: true, brief, refreshUnavailable: true, message: "Brief saved. Team refresh is unavailable; refresh to inspect current native state." }; }
      }
      fields(input, ["operation", "memberId", "action"]);
      require(input.operation === "member-action");
      require(typeof input.memberId === "string" && ID.test(input.memberId));
      const a = input.action;
      fields(a, ["operation", "expectedRevision", "expectedBriefRevision", "sections", "examples", "caseId", "showContent", "input", "model", "runId"]);
      require(["save-and-test", "save-examples", "save-prompt", "save-model", "test", "run", "reload", "suggest", "models"].includes(a.operation));
      require(a.runId === undefined || typeof a.runId === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(a.runId));
      if (a.operation === "models") return readModels(session);
      let { raw, view } = await memberSnapshot(input.memberId);
      if (a.operation === "reload") return { view };
      if (a.operation === "suggest") {
        await session.send({ prompt: `Suggest tests for native Squad member ${JSON.stringify(input.memberId)} at ${view.file.revision}. Show proposals in chat; do not save, run or create anything.` });
        return { message: "Asked Copilot to suggest tests in chat." };
      }
      require(a.expectedRevision === view.file.revision, "stale_revision");
      const executing = ["save-and-test", "test", "run"].includes(a.operation);
      if (executing) require(a.expectedBriefRevision === raw.brief.revision, "stale_revision");
      if (a.operation === "save-model") require(typeof a.model === "string" && a.sections === undefined && a.examples === undefined);
      require(a.showContent === undefined || typeof a.showContent === "boolean");
      let taskInstructions = raw.taskInstructions;
      if (a.sections !== undefined) {
        require(Array.isArray(a.sections) && a.sections.length <= 1);
        for (const section of a.sections) { fields(section, ["key", "text"]); require(section.key === "Task Instructions"); taskInstructions = text(section.text, 60000); }
      }
      const tests = a.examples ?? raw.tests;
      require(Array.isArray(tests));
      require(a.caseId === undefined || tests.filter((t) => t.id === a.caseId).length === 1);
      if (["save-and-test", "save-examples", "save-prompt", "save-model"].includes(a.operation)) {
        const model = a.model === undefined ? raw.model : text(a.model, 256);
        const examples = a.examples === undefined ? raw.examples : serializeExamples(tests);
        if (taskInstructions !== raw.taskInstructions || model !== raw.model || a.examples !== undefined) {
          const result = await invoke("edit", { target: "squad-member", memberId: input.memberId, expectedRevision: view.file.revision, taskInstructions, model, examples }, "kmx.squad-member-operation");
          require(result.memberId === input.memberId && result.file?.path === raw.file.path && REVISION.test(result.file.revision), "outcome-unknown");
          saved = true;
          raw = await read(`kmx://squad-member/${input.memberId}`);
          require(result.file?.revision === raw.file?.revision, "outcome-unknown");
          view = projectMember(raw, observations.get(input.memberId));
          savedView = view;
        }
        if (a.operation !== "save-and-test") return { view, saved };
      }
      const operation = a.operation === "run" ? "run" : "test";
      if (operation === "test" && raw.tests.length === 0) return { error: "no-tests", view, saved };
      require(a.expectedBriefRevision === raw.brief.revision, "stale_revision");
      if (operation === "test" && a.runId && lastTeam?.previewControl === "run-id-v1") {
        run = previews.start({ id: a.runId, memberId: input.memberId, revision: view.file.revision, contextRevision: raw.brief.revision, model: raw.model, examples: raw.tests, caseIds: a.caseId === undefined ? raw.tests.map(t => t.id) : [a.caseId], totalCases: raw.tests.length });
      }
      const result = await invoke(operation, { target: "squad-member", memberId: input.memberId, expectedRevision: view.file.revision, expectedBriefRevision: a.expectedBriefRevision, ...(run ? { runId: run.nativeId } : {}), ...(a.caseId !== undefined ? { caseId: a.caseId } : {}), ...(operation === "run" ? { input: text(a.input, 16384) } : {}), showContent: a.showContent === true, showFailedContent: true }, "kmx.squad-member-operation", run);
      if (run) require(result.runId === run.nativeId, "outcome-unknown");
      if (run?.stopWanted && result.preview?.state === "cancelled" && result.preview.fullPass === false && result.memberId === input.memberId && result.file?.revision === view.file.revision && result.briefRevision === raw.brief.revision) {
        const fresh = await read(`kmx://squad-member/${input.memberId}`);
        require(fresh.file?.revision === view.file.revision && fresh.brief?.revision === raw.brief.revision, "revision_mismatch");
        previews.finish(run, true); observations.delete(input.memberId);
        return { stopped: true, saved, run: previews.snapshot(input.memberId, run.id), view: projectMember(fresh) };
      }
      require(result.memberId === input.memberId && result.preview?.file?.revision === view.file.revision && result.briefRevision === raw.brief.revision, "revision_mismatch");
      const preview = result.preview, expected = a.caseId === undefined ? raw.tests.map((t) => t.id) : [a.caseId];
      const cleaned = cleanResult(preview, "current", view.file.revision);
      const beforeSelection = preRouteRefusal(cleaned) && cleaned.selection.caseIds.length === 0 && cleaned.examples.length === 0 && !cleaned.adHoc;
      require(preview.operation === operation && preview.selection.totalCases === raw.tests.length && (beforeSelection || preview.selection.scope === (a.caseId === undefined ? "full" : "subset") && JSON.stringify(preview.selection.caseIds) === JSON.stringify(expected)), "outcome-unknown");
      observations.set(input.memberId, result);
      previews.finish(run);
      return { view: await member(input.memberId), saved };
    } catch (error) {
      if (error?.code === "outcome-unknown") uncertain = true;
      if (saved && error instanceof ViewError) return { error: error.code, message: error.message, saved: true, ...(!uncertain && savedView ? { view: savedView } : {}) };
      throw error;
    } finally { if (run?.pending) previews.finish(run); busy = false; }
  }
  return { team, member, action, snapshot, dispose: () => previews.dispose() };
}
