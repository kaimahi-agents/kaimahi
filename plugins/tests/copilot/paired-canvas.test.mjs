import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { createActionHandler, readView } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs";
import { createOpenHandler, viewInstanceId } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/open.mjs";
import { createSuggestionMailbox } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/suggestions.mjs";
import { resultHistory } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/results.mjs";
import { runtimeModelSelection } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs";

const path = "a.agent.md", server = "kmx-agent-builder";
const hash = source => "sha256:" + createHash("sha256").update(source).digest("hex");
const source = "---\nmodel: fake\n---\nPrompt\n";
const suite = "---\nschemaVersion: 1\n---\n\n```yaml agent-test\nid: greeting\ninput: Ada\nassertions: [{id: text, type: contains, value: Ada}]\n```\n";
const examples = [{ id: "greeting", input: "Ada", assertions: [{ id: "text", type: "contains", value: "Ada" }] }];
function fixture(over = {}) {
  const canvas = { schemaVersion: 1, kind: "kmx.agent-view", file: { path, revision: hash(source) }, title: "Agent", instructions: "Prompt", model: "fake", examples, latest: { status: "none" }, authoring: { source, editable: true, recordingControls: true } };
  return { schemaVersion: 1, kind: "kmx.agent-sources", readOnly: true, validation: "valid", file: { path: "/work/" + path, revision: hash(source) }, source,
    tests: { state: "external", path: "/work/a.agent.test.md", revision: hash(suite), source: suite, selectionKey: hash("selection") }, snapshotRevision: hash("snapshot"), sourceVersion: "context-original",
    sourceHistory: { schemaVersion: 1, read: true, execute: false, edit: false, capture: false, restore: false }, history: { status: "available", versions: [] },
    pairOperations: { schemaVersion: 1, toolSchemaVersion: 3, execute: true, create: true, edit: true, capture: true, restore: true }, canvas, ...over };
}
function sessionFor(initial = fixture()) {
  let document = initial;
  const calls = [];
  const session = { rpc: { mcp: { resources: { read: async ({ uri }) => ({ contents: [{ text: JSON.stringify(uri.startsWith("kmx://agent-sources/") ? document : { schemaVersion: 1, kind: "kmx.agent-view-error", reasonCode: "paired_source_required" }) }] }) } }, tools: {
    getCurrentMetadata: async () => ({ tools: ["edit", "run", "test", "set_tool_state"].map(name => ({ name, mcpToolName: name, mcpServerName: server })) }),
    execute: async call => { calls.push(call); return { resultType: "success", structuredContent: {} }; },
  } } };
  return { session, calls, get document() { return document; }, set document(value) { document = value; } };
}

test("paired text fallback retains both exact sources and checked context without starting a relay", async () => {
  const f=sessionFor(), result=await createOpenHandler({getSession:()=>f.session,extensionId:"fixture"})({path});
  assert.equal(result.mode,"text");assert.equal(result.authoring.source,source);assert.equal(result.selectedSources.testsSource,suite);assert.equal(result.selectedSources.sourceVersion,"context-original");assert.equal(f.calls.length,0);
});

test("explicit tests selection is held by the canvas opener and passed only to selected-source resources", async () => {
  const f=sessionFor(),tests="/selected/other.agent.test.md",uris=[];const read=f.session.rpc.mcp.resources.read;
  f.session.rpc.mcp.resources.read=async params=>{uris.push(params.uri);return read(params)};
  f.session.rpc.canvas={open:async input=>{assert.deepEqual(input.input,{path,tests});assert.notEqual(input.instanceId,viewInstanceId(path))}};
  const response=await createOpenHandler({getSession:()=>f.session,extensionId:"fixture"})({path,tests});
  assert.equal(response.opened,true);assert.ok(uris.every(uri=>uri.includes("?tests="+encodeURIComponent(tests))));
});

test("paired reader retains exact sources, checked context and normalized external examples without inline conversion", async () => {
  const f = sessionFor(), view = await readView(f.session, path);
  assert.equal(view.file.revision, hash(source));
  assert.equal(view.authoring.source, source);
  assert.deepEqual(view.authoring.examples, examples);
  assert.equal(view.selectedSources.testsSource, suite);
  assert.equal(view.selectedSources.sourceVersion, "context-original");
  assert.equal(view.latest.status, "none");
});

for (const [name, mutate] of Object.entries({
  "agent hash": d => { d.source += "changed"; },
  "tests hash": d => { d.tests.source += "changed"; },
  "tool version": d => { d.pairOperations.toolSchemaVersion = 2; },
  "missing context": d => { delete d.sourceVersion; },
  "wrong canvas path": d => { d.canvas.file.path = "other.agent.md"; },
})) test("paired reader refuses mismatched " + name, async () => {
  const d = fixture(); mutate(d);
  await assert.rejects(() => readView(sessionFor(d).session, path));
});

test("cross-invalid selected sources remain editable without execution authority", async () => {
  const d = fixture({ validation: "invalid_pair" }); d.pairOperations.execute = false; d.pairOperations.capture = false;
  const view = await readView(sessionFor(d).session, path);
  assert.equal(view.authoring.editable, true);
  assert.equal(view.selectedSources.operations.execute, false);
});

test("omitted optional canvas projection is complete-source inspection only", async () => {
  const d = fixture(); delete d.canvas;
  const view = await readView(sessionFor(d).session, path);
  assert.notEqual(view.authoring?.editable, true);
  assert.equal(view.selectedSources.testsSource, suite);
});

test("same-byte replacement or expired checked context is stale before any tool call", async () => {
  const f = sessionFor(fixture({ sourceVersion: "context-replaced" }));
  const act = createActionHandler({ session: f.session, path });
  const response = await act({ operation: "test", expectedRevision: hash(source), expectedSourceVersion: "context-original", model: "fake" });
  assert.equal(response.error, "stale_revision"); assert.equal(f.calls.length, 0);
});

function preview(d, over = {}) {
  return { schemaVersion: 3, kind: "kmx.agent-pair-preview", sourceVersion: d.sourceVersion, preview: { schemaVersion: 1, kind: "kmx.agent-file-preview", file: { path: d.file.path, revision: d.file.revision }, tests: { state: d.tests.state, path: d.tests.path, revision: d.tests.revision, selectionKey: d.tests.selectionKey }, snapshotRevision: d.snapshotRevision, operation: "test", state: "complete", verdict: "pass", fullPass: true, modelRoute: { provider: "fake", model: "fake" }, selection: { scope: "full", caseIds: ["greeting"], totalCases: 1 }, examples: [{ id: "greeting", state: "complete", verdict: "pass", assertions: [] }], recordedReplayOnly: true, ...over } };
}
function completeSave(d, agent = "unchanged", tests = "saved") {
  return { schemaVersion: 3, kind: "kmx.agent-pair-save", state: "complete", file: { path, revision: d.file.revision }, agent: { status: agent, revision: d.file.revision }, tests: { status: tests, revision: d.tests.revision }, snapshotRevision: d.snapshotRevision, sourceVersion: d.sourceVersion };
}
const expected = { expectedRevision: hash(source), expectedSourceVersion: "context-original" };

function withTools() {
  const d=fixture();d.source=d.source.replace("model: fake","model: fake\ntools: [ping]");d.file.revision=hash(d.source);
  d.canvas.file.revision=d.file.revision;d.canvas.authoring.source=d.source;d.canvas.tools=["ping"];d.canvas.toolSettings=[{name:"ping",state:"Enabled"}];d.canvas.toolSettingsRevision="absent";
  return d;
}

test("paired malformed comparison is refused before a draft save",async()=>{
  const f=sessionFor();const result=await createActionHandler({session:f.session,path})({operation:"save-and-test",...expected,examples:[{...examples[0],input:"Grace"}],exampleOrigins:["greeting"],comparisonModels:["fake","fake"]});
  assert.equal(result.error,"invalid-action");assert.equal(f.calls.length,0);
});

test("paired Test fences the page's tool-settings observation before any send",async()=>{
  const d=withTools();d.canvas.toolSettingsRevision=hash("foreign settings");const f=sessionFor(d);
  const result=await createActionHandler({session:f.session,path})({operation:"test",expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,expectedSettingsRevision:"absent"});
  assert.equal(result.error,"stale_settings_revision");assert.equal(f.calls.length,0);
});

test("paired source Save cannot adopt a foreign settings token before applying a settings draft",async()=>{
  const d=withTools(),f=sessionFor(d);
  f.session.rpc.tools.execute=async call=>{f.calls.push(call);const next=structuredClone(d);next.source=call.arguments.content;next.file.revision=hash(next.source);next.canvas.file.revision=next.file.revision;next.canvas.authoring.source=next.source;next.sourceVersion="saved-source";next.snapshotRevision=hash("saved source");next.canvas.toolSettingsRevision=hash("foreign settings");f.document=next;return {resultType:"success",structuredContent:completeSave(next,"saved","unchanged")}};
  const result=await createActionHandler({session:f.session,path})({operation:"save-and-test",expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,expectedSettingsRevision:"absent",sections:[{key:"Prompt",text:"Changed prompt"}],toolSettings:[{name:"ping",state:"Disabled"}]});
  assert.equal(result.error,"stale_settings_revision");assert.equal(result.saved,true);assert.equal(result.settingsIncomplete,true);assert.equal(f.calls.length,1);
});

test("paired tests-only Save forwards sparse intents, checks both role ACKs and does not Test", async () => {
  const f = sessionFor();
  f.session.rpc.tools.execute = async call => {
    f.calls.push(call);
    assert.equal(call.arguments.schemaVersion, 3);
    assert.equal(call.arguments.expectedSourceVersion, "context-original");
    assert.equal(call.arguments.content, undefined);
    assert.equal(call.arguments.testsContent, undefined);
    assert.deepEqual(call.arguments.exampleEdits, [{ beforeId: "greeting", input: "Grace" }]);
    const next = fixture({ sourceVersion: "context-saved", snapshotRevision: hash("saved") });
    next.tests.source = suite.replace("input: Ada", "input: Grace"); next.tests.revision = hash(next.tests.source);
    next.canvas.examples = [{ ...examples[0], input: "Grace" }]; f.document = next;
    return { resultType: "success", structuredContent: completeSave(next) };
  };
  const response = await createActionHandler({ session: f.session, path })({ operation: "save-examples", ...expected, examples: [{ ...examples[0], input: "Grace" }], exampleOrigins: ["greeting"] });
  assert.equal(response.error, undefined); assert.equal(response.saved, true); assert.equal(response.view.selectedSources.sourceVersion, "context-saved"); assert.equal(f.calls.length, 1);
});

for (const state of ["partial", "uncertain", "not_saved"]) test("paired " + state + " save preserves drafts and never starts Test", async () => {
  const f = sessionFor();
  f.session.rpc.tools.execute = async call => { f.calls.push(call); return { resultType: "failure", structuredContent: { schemaVersion: 3, kind: "kmx.agent-pair-save", state, agent: { status: state === "partial" ? "saved" : "unchanged" }, tests: { status: state === "uncertain" ? "uncertain" : "not_saved" } } }; };
  const response = await createActionHandler({ session: f.session, path })({ operation: "save-and-test", ...expected, examples: [{ ...examples[0], input: "Grace" }], exampleOrigins: ["greeting"] });
  assert.equal(response.error, "outcome-unknown"); assert.equal(response.saved, false); assert.equal(response.view, undefined); assert.equal(f.calls.length, 1);
  assert.match(response.message, /Agent|Tests/);
});

test("paired Test attributes the direct v3 envelope, never a later legacy latest cache", async () => {
  const f = sessionFor();
  f.session.rpc.tools.execute = async call => { f.calls.push(call); return { resultType: "success", structuredContent: preview(f.document) }; };
  const response = await createActionHandler({ session: f.session, path })({ operation: "test", ...expected, model: "fake", runId: "paired-run" });
  assert.equal(response.error, undefined); assert.equal(response.view.latest.status, "current"); assert.equal(response.view.latest.badge, "pass");
  assert.equal(f.calls[0].arguments.expectedRevision, undefined); assert.equal(f.calls[0].arguments.expectedSourceVersion, "context-original"); assert.equal(f.calls[0].arguments.schemaVersion, 3);
});

for (const name of ["source context", "snapshot", "selection key", "source path", "model", "redaction"]) test("paired Test rejects mismatched " + name, async () => {
  const f = sessionFor(), response = preview(f.document);
  if (name === "source context") response.sourceVersion = "foreign";
  if (name === "snapshot") response.preview.snapshotRevision = hash("foreign");
  if (name === "selection key") response.preview.tests.selectionKey = hash("foreign");
  if (name === "source path") response.preview.file.path = "/foreign/other.agent.md";
  if (name === "model") response.preview.modelRoute.model = "other";
  if (name === "redaction") { delete response.sourceVersion; delete response.preview.tests; delete response.preview.snapshotRevision; }
  f.session.rpc.tools.execute = async () => ({ resultType: "success", structuredContent: response });
  const result = await createActionHandler({ session: f.session, path })({ operation: "test", ...expected, model: "fake" });
  assert.equal(result.error, "outcome-unknown"); assert.equal(result.view, undefined);
});

for (const fault of ["none", "uncertain role", "agent changed", "same snapshot", "untrusted role"]) test("paired capture keeps executed and saved identity separate: " + fault,async()=>{
  const d=withTools();d.tests.source=suite.replace("assertions:","mocks:\n  tools: [{name: ping, mode: recorded, calls: []}]\nassertions:");d.tests.revision=hash(d.tests.source);
  d.canvas.examples=[{...examples[0],mocks:{tools:[{name:"ping",mode:"recorded",calls:[]}]}}];
  const next=structuredClone(d);next.sourceVersion="saved-capture";next.snapshotRevision=hash("captured snapshot");next.tests.source=d.tests.source.replace("calls: []","description: ping, parameters: {type: object}, calls: [{arguments: {}, result: {ok: true}}]");next.tests.revision=hash(next.tests.source);Object.assign(next.canvas.examples[0].mocks.tools[0],{description:"ping",parameters:{type:"object"},calls:[{arguments:{},result:{ok:true}}]});
  if(fault==="agent changed"){next.source+="Foreign prompt change\n";next.file.revision=hash(next.source);next.canvas.file.revision=next.file.revision;next.canvas.authoring.source=next.source}
  if(fault==="same snapshot")next.snapshotRevision=d.snapshotRevision;
  const envelope=preview(d,{recordedReplayOnly:false,toolSettings:d.canvas.toolSettings,savedRevision:next.tests.revision,savedSnapshotRevision:next.snapshotRevision,recordingTarget:{role:"tests",path:d.tests.path,selectionKey:d.tests.selectionKey}});
  envelope.savedSourceVersion=next.sourceVersion;envelope.capture={state:"complete",agent:{status:"unchanged"},tests:{status:fault==="uncertain role"?"uncertain":fault==="untrusted role"?"private-role-canary":"saved",revision:next.tests.revision}};
  envelope.preview.examples[0].tools=[{name:"ping",mode:"recorded",source:"live",callCount:1,recordingSaved:true}];
  const f=sessionFor(d);f.session.rpc.tools.execute=async call=>{f.calls.push(call);f.document=next;return {resultType:"success",structuredContent:envelope}};
  const result=await createActionHandler({session:f.session,path})({operation:"test",expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,expectedSettingsRevision:"absent",model:"fake"});
  if(fault==="none"){assert.equal(result.error,undefined);assert.equal(result.capture.sourceVersion,d.sourceVersion);assert.equal(result.capture.savedSourceVersion,next.sourceVersion);assert.equal(result.view.latest.status,"none");assert.equal(result.saved,true)}else{assert.equal(result.error,"outcome-unknown");assert.equal(result.view,undefined);assert.doesNotMatch(result.message,/private-role-canary/)}
});

test("an uncalled pending recording is not captured again automatically when comparison starts",async()=>{
  const d=withTools();d.canvas.examples=[{...examples[0],mocks:{tools:[{name:"ping",mode:"recorded",calls:[]}]}}];
  const f=sessionFor(d);f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"success",structuredContent:preview(d,{recordedReplayOnly:false,toolSettings:d.canvas.toolSettings,modelRoute:{provider:"fake",model:call.arguments.model}})}};
  const act=createActionHandler({session:f.session,path}), held={expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,expectedSettingsRevision:"absent",model:"fake"};
  const first=await act({operation:"test",...held});assert.equal(first.error,undefined);
  const compared=await act({operation:"test",...held,comparisonModels:["fake","other"]});
  assert.equal(compared.error,"recording_missing");assert.equal(f.calls.length,1);
});

for (const state of ["denied","rejected"]) for (const operation of ["save-examples","restore","run","test"]) test(`paired ${operation} ${state} is a confirmed refusal, not an uncertainty lock`,async()=>{
  const f=sessionFor();f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:state,textResultForLlm:"private-denial-canary"}};
  const input={operation,...expected,...(operation==="save-examples"?{examples:[{...examples[0],input:"Grace"}],exampleOrigins:["greeting"]}:{}),...(operation==="restore"?{restoreVersionId:"retained-version"}:{}),...(operation==="run"?{input:"Ada"}:{})};
  const act=createActionHandler({session:f.session,path});const first=await act(input),again=await act(input);
  assert.equal(first.error,"permission_required");assert.equal(again.error,"permission_required");assert.equal(f.calls.length,2,"a declined permission must not require reload");assert.doesNotMatch(first.message,/private-denial-canary/);
});

for (const fault of ["missing fullPass","null example","post-Test read","post-Save read"]) test(`unconfirmed paired ${fault} locks until deliberate reload`,async()=>{
  const f=sessionFor(),read=f.session.rpc.mcp.resources.read;let failRead=false;
  f.session.rpc.mcp.resources.read=async params=>{if(failRead){failRead=false;throw Error("private-read-canary")}return read(params)};
  f.session.rpc.tools.execute=async call=>{
    f.calls.push(call);
    if(fault==="post-Save read"){failRead=true;return {resultType:"success",structuredContent:completeSave(f.document)}}
    const result=preview(f.document);if(fault==="missing fullPass")delete result.preview.fullPass;if(fault==="null example")result.preview.examples=[null];if(fault==="post-Test read")failRead=true;
    return {resultType:"success",structuredContent:result};
  };
  const input={operation:fault==="post-Save read"?"save-examples":"test",...expected,...(fault==="post-Save read"?{examples:[{...examples[0],input:"Grace"}],exampleOrigins:["greeting"]}:{})};
  const act=createActionHandler({session:f.session,path}),first=await act(input),again=await act(input);
  assert.equal(f.calls.length,1,"the unconfirmed operation must not be sent twice");assert.equal(first.error,"outcome-unknown");assert.equal(again.error,"outcome-unknown");assert.equal(first.view,undefined);assert.doesNotMatch(first.message,/private-read-canary/);
  assert.equal((await act({operation:"reload"})).error,undefined);
});

for(const resultType of ["success","failure"])for(const reasonCode of ["stale_revision","confirmation_required"])test(`paired ${reasonCode} preflight under ${resultType} remains a confirmed refusal`,async()=>{
  const f=sessionFor();f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType,structuredContent:{schemaVersion:3,reasonCode,reason:"private-preflight-canary"}}};
  const act=createActionHandler({session:f.session,path}),first=await act({operation:"test",...expected}),again=await act({operation:"test",...expected});
  assert.equal(first.error,reasonCode);assert.equal(again.error,reasonCode);assert.equal(f.calls.length,2);assert.doesNotMatch(first.message,/private-preflight-canary/);
});

for(const fault of ["post-set typed resource error","setter refusal","SDK settings denial"])test(`paired settings distinguish receipt from ${fault}`,async()=>{
  const d=fixture(),agent=source.replace("model: fake\n","model: fake\ntools: [ping]\n");d.source=agent;d.file.revision=hash(agent);d.tests={state:"absent",path:d.tests.path,selectionKey:d.tests.selectionKey,source:"",revision:""};
  Object.assign(d.canvas,{file:{path,revision:hash(agent)},tools:["ping"],toolSettings:[{name:"ping",state:"Enabled"}],toolSettingsRevision:"absent",examples:[],authoring:{source:agent,editable:true,recordingControls:true}});
  const f=sessionFor(d),read=f.session.rpc.mcp.resources.read;let fail=false;
  f.session.rpc.mcp.resources.read=async params=>{if(fail){fail=false;return {contents:[{text:JSON.stringify({schemaVersion:1,kind:"kmx.agent-view-error",reasonCode:"invalid_tool_settings"})}]}}return read(params)};
  f.session.rpc.tools.execute=async call=>{f.calls.push(call);assert.equal(call.name,"set_tool_state");
    if(fault==="SDK settings denial")return {resultType:"denied",textResultForLlm:"private-settings-canary"};
    if(fault==="setter refusal")return {resultType:"failure",structuredContent:{schemaVersion:1,operation:"set_tool_state",state:"refused",changed:false,mayHaveWritten:false,reasonCode:"invalid_tool_settings"}};
    fail=true;return {resultType:"success",structuredContent:{schemaVersion:1,operation:"set_tool_state",state:"complete",changed:false,mayHaveWritten:false,file:{path,revision:hash(agent)},toolSettingsRevision:"absent",toolSettings:[{name:"ping",state:"Enabled"}]}};
  };
  const act=createActionHandler({session:f.session,path}),input={operation:"save-and-test",expectedSettingsRevision:"absent",expectedRevision:hash(agent),expectedSourceVersion:d.sourceVersion,toolSettings:[{name:"ping",state:"Enabled"}]},first=await act(input),again=await act(input);
  assert.equal(first.error,fault==="post-set typed resource error"?"outcome-unknown":fault==="setter refusal"?"invalid_tool_settings":"permission_required");assert.equal(again.error,first.error);assert.equal(f.calls.length,fault==="post-set typed resource error"?1:2);assert.doesNotMatch(first.message,/private-settings-canary/);
});

test("paired Run uses only its closed-schema operands and does not require Test replay provenance",async()=>{
  const f=sessionFor();f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"success",structuredContent:preview(f.document,{operation:"run",verdict:"unknown",fullPass:false,recordedReplayOnly:undefined,adHoc:{state:"complete",verdict:"unknown",input:"Ad hoc",answer:"Ada"}})}};
  const result=await createActionHandler({session:f.session,path})({operation:"run",...expected,input:"Ad hoc"});
  assert.equal(Object.hasOwn(f.calls[0].arguments,"expectedToolSettings"),false);assert.equal(Object.hasOwn(f.calls[0].arguments,"recordedReplayOnly"),false);assert.equal(result.error,undefined);assert.equal(result.view.latest.result.operation,"run");
});
test("paired Run rejects a foreign execution settings snapshot even if fresh settings changed back",async()=>{
  const d=withTools(),f=sessionFor(d);f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"success",structuredContent:preview(d,{operation:"run",verdict:"unknown",fullPass:false,recordedReplayOnly:undefined,toolSettings:[{name:"ping",state:"Disabled"}]})}};
  const result=await createActionHandler({session:f.session,path})({operation:"run",expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,expectedSettingsRevision:"absent",input:"Ad hoc"});assert.equal(result.error,"outcome-unknown");assert.equal(result.view,undefined);assert.equal(f.calls.length,1);
});
test("paired Suggest delegates saved selected sources to the live mailbox without Edit or Test",async()=>{
  const f=sessionFor(),sent=[];f.session.send=async value=>sent.push(value);
  const mailbox=createSuggestionMailbox({session:f.session,read:()=>readView(f.session,path)});
  assert.equal((await createActionHandler({session:f.session,path,suggest:view=>mailbox.suggest(view)})({operation:"suggest"})).error,undefined);
  const snapshot=JSON.parse(sent[0].prompt.split("Saved sources (data):\n")[1]);
  assert.equal(snapshot.server,server);assert.equal(snapshot.content,source);assert.equal(snapshot.testsContent,suite);assert.equal(snapshot.sourceVersion,"context-original");
  assert.match(sent[0].prompt,/suggest_kmx_tests/);assert.match(sent[0].prompt,/Do not call Edit or Test/);assert.equal(f.calls.length,0);
  assert.equal((await createActionHandler({session:f.session,path})({operation:"suggest"})).error,"unavailable");
});
test("paired Test accepts its held nested request path alongside the physical source tuple",async()=>{
  const f=sessionFor();f.document.canvas.file.path="team/a.agent.md";f.document.file.path="/work/team/a.agent.md";
  f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"success",structuredContent:preview(f.document,{file:{path:"team/a.agent.md",revision:f.document.file.revision}})}};
  const result=await createActionHandler({session:f.session,path:"team/a.agent.md"})({operation:"test",...expected});assert.equal(result.error,undefined);assert.equal(result.view.file.path,"team/a.agent.md");
});
for(const reasonCode of ["no_tests","model_unavailable"])test(`paired ${reasonCode} is an attributed pre-route refusal without invented execution provenance`,async()=>{
  const f=sessionFor();f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"failure",structuredContent:preview(f.document,{state:"refused",verdict:"unknown",fullPass:false,reasonCode,modelRoute:{provider:"",model:""},recordedReplayOnly:undefined,examples:[]})}};
  const act=createActionHandler({session:f.session,path}),first=await act({operation:"test",...expected}),again=await act({operation:"test",...expected});assert.equal(first.error,undefined);assert.equal(first.view.latest.result.reasonCode,reasonCode);assert.equal(again.error,undefined);assert.equal(f.calls.length,2);
});
for(const state of ["error","cancelled"])for(const capturing of [false,true])test(`paired comparison stops after an attributed ${state} with capture=${capturing}`,async()=>{
  const d=capturing?withTools():fixture();if(capturing)d.canvas.examples=[{...examples[0],mocks:{tools:[{name:"ping",mode:"recorded",calls:[]}]}}];
  const f=sessionFor(d);f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"failure",structuredContent:preview(f.document,{state,verdict:"unknown",fullPass:false,reasonCode:"deadline_exceeded",modelRoute:{provider:"fake",model:call.arguments.model},toolSettings:d.canvas.toolSettings,recordedReplayOnly:!capturing,examples:[{id:"greeting",state,verdict:"unknown",assertions:[]}]})}};
  const result=await createActionHandler({session:f.session,path})({operation:"test",expectedRevision:d.file.revision,expectedSourceVersion:d.sourceVersion,...(capturing?{expectedSettingsRevision:"absent"}:{}),model:"fake",comparisonModels:["fake","other"]});assert.equal(f.calls.length,1);assert.equal(result.error,undefined);assert.equal(result.view.latest.result.state,state);assert.equal(result.comparison,undefined);
});
test("paired comparison still compares completed assertion failures",async()=>{
  const f=sessionFor();f.session.rpc.tools.execute=async call=>{f.calls.push(call);return {resultType:"success",structuredContent:preview(f.document,{verdict:"fail",fullPass:false,modelRoute:{provider:"fake",model:call.arguments.model},examples:[{id:"greeting",state:"complete",verdict:"fail",assertions:[]}]})}};
  const result=await createActionHandler({session:f.session,path})({operation:"test",...expected,model:"fake",comparisonModels:["fake","other"]});assert.equal(f.calls.length,2);assert.equal(result.error,undefined);assert.equal(result.comparison.results.length,2);
});

test("source history and model picker cannot revive a result after a tests-only context change", () => {
  const result = { operation: "test", state: "complete", verdict: "pass", fullPass: true, modelRoute: "fake/fake", selection: { scope: "full", caseIds: ["greeting"], totalCases: 1 }, examples: [{ id: "greeting", state: "complete", verdict: "pass", assertions: [] }] };
  const old = { file: { path, revision: hash(source) }, model: "fake", examples, authoring: { examples }, selectedSources: { sourceVersion: "old", server }, latest: { status: "current", badge: "pass", result } };
  const next = { ...old, selectedSources: { sourceVersion: "new", server }, latest: { status: "none", badge: "none" } };
  const history = resultHistory(old);
  assert.equal(history.selected(next, "fake"), undefined);
  assert.equal(runtimeModelSelection().view(next, old).latest.status, "none");
});
