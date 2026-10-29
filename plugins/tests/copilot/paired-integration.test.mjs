import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { pairedFixture } from "./paired-fixture.mjs";

const binary = process.env.KMX_HOST_BRIDGE_TEST_BINARY;
const options = { skip: !binary, timeout: 60000 };

test("paired real MCP canvas captures externally, replays, saves both drafts and restores exact source history", options, async t => {
  const f = await pairedFixture(t, binary), before = await f.read();
  assert.notEqual(before.selectedSources.server, "kmx-agent-builder");
  assert.equal(f.tools.length, 0, "opening resolves no tool effects");
  const captured = await f.act("test", { runId: "capture" });
  assert.equal(captured.error, undefined, JSON.stringify(captured));
  assert.equal(captured.saved, true); assert.ok(captured.capture.savedSourceVersion);
  assert.equal(captured.capture.sourceVersion, before.selectedSources.sourceVersion);
  assert.notEqual(captured.capture.savedSourceVersion, captured.capture.sourceVersion);
  assert.equal(f.tools.length, 1);
  assert.equal(await readFile(path.join(f.root, "a.agent.md"), "utf8"), f.source);
  const savedTests = await readFile(path.join(f.root, "a.agent.test.md"), "utf8");
  assert.ok(savedTests.includes("# Keep this prose.\r\n"));
  assert.ok(!savedTests.includes("calls: []"));
  const replay = await f.act("test", { runId: "replay" });
  assert.equal(replay.error, undefined, JSON.stringify(replay)); assert.equal(replay.view.latest.badge, "pass");
  assert.equal(replay.view.latest.result.recordedReplayOnly, true); assert.equal(f.tools.length, 1, "replay has no extra host call");
  const fresh = await f.read(), examples = structuredClone(fresh.authoring.examples);
  examples[0].assertions[0].value = "not present";
  const changed = await f.act("save-and-test", { runId: "both", sections: [{ key: "Prompt", text: "Call the tool `ping`. Reply with exactly `done`.\nAn extra instruction." }], examples, exampleOrigins: ["read"] });
  assert.equal(changed.error, undefined, JSON.stringify(changed)); assert.equal(changed.saved, true); assert.equal(changed.view.latest.result.verdict, "fail");
  const history = (await f.read()).selectedSources.history;
  assert.ok(history.versions.some(v => v.label === "Both changed"));
  const calls = f.previews.filter(row => row.name === "test").length;
  const restored = await f.act("restore", { restoreVersionId: history.versions[0].versionId });
  assert.equal(restored.error, undefined, JSON.stringify(restored)); assert.equal(restored.saved, true);
  assert.equal(f.previews.filter(row => row.name === "test").length, calls, "restore never starts Test");
  assert.equal(await readFile(path.join(f.root, "a.agent.md"), "utf8"), f.source);
  assert.equal(await readFile(path.join(f.root, "a.agent.test.md"), "utf8"), savedTests);
  assert.equal((await f.act("test")).view.latest.badge, "pass");
  assert.ok(f.previews.filter(row => ["edit", "test"].includes(row.name)).every(row => row.serverName === before.selectedSources.server));
});

test("initially tool-free paired canvas adds a tool and captures on its first Enter with no server handoff", options, async t => {
  const f = await pairedFixture(t, binary, { toolFree: true }), before = await f.read();
  assert.deepEqual(before.tools, []); assert.notEqual(before.selectedSources.server, "kmx-agent-builder");
  const examples = structuredClone(before.authoring.examples);
  examples[0].mocks = { tools: [{ name: "ping", mode: "recorded", calls: [] }] };
  const result = await f.act("save-and-test", { runId: "add-tool", tools: ["ping"], examples, exampleOrigins: ["read"] });
  assert.equal(result.error, undefined, JSON.stringify(result)); assert.equal(result.saved, true); assert.equal(f.tools.length, 1);
  assert.ok(result.capture.savedSourceVersion); assert.deepEqual(result.view.tools, ["ping"]);
  assert.ok(f.previews.every(row => row.serverName === before.selectedSources.server));
});

test("paired comparison reuses the captured selection and an unchanged certified partner", options, async t => {
  const f = await pairedFixture(t, binary, { comparing: true });
  const first = await f.act("test", { runId: "compare", model: "unit", comparisonModels: ["unit", "unit-other"] });
  assert.equal(first.error, undefined, JSON.stringify(first)); assert.equal(first.comparison.results.length, 2);
  assert.equal(f.tools.length, 1); assert.equal(f.sends, 6);
  for (const view of first.comparison.results) { assert.equal(view.latest.result.recordedReplayOnly, true); assert.equal(view.latest.badge, "pass"); }
  const again = await f.act("test", { runId: "compare-again", model: "unit", comparisonModels: ["unit", "unit-other"] });
  assert.equal(again.error, undefined, JSON.stringify(again)); assert.equal(f.tools.length, 1);
  assert.equal(f.sends, 8, "the already certified partner is not sent again");
});

for(const operation of ["run","test"])test(`paired real MCP ${operation} accepts a nested unrooted opener and its closed schema`,options,async t=>{
  const f=await pairedFixture(t,binary,{toolFree:true,agentPath:"team/a.agent.md",instructions:"Reply with exactly `done`."}),sent=[];f.session.send=async value=>sent.push(value);
  const held=await f.read();await f.act("suggest");
  const result=await f.act(operation,operation==="run"?{input:"another input",showContent:true}:{});
  assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.view.latest.result.state,"complete");assert.equal(result.view.latest.result.operation,operation);assert.equal(f.tools.length,0);
  if(operation==="run"){assert.equal(result.view.latest.result.verdict,"unknown");assert.equal(result.view.latest.result.fullPass,false);assert.equal(result.view.latest.result.adHoc.state,"complete")}
  assert.ok(sent[0].prompt.includes(held.selectedSources.server));
});
test("paired real MCP final-test deletion saves then returns no_tests without locking ad-hoc Run",options,async t=>{
  const f=await pairedFixture(t,binary,{toolFree:true,instructions:"Reply with exactly `done`."});const result=await f.act("save-and-test",{examples:[],exampleOrigins:[]});
  assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.saved,true);assert.equal(result.view.latest.result.reasonCode,"no_tests");assert.equal(result.view.latest.result.modelRoute,"/");
  const run=await f.act("run",{input:"another input"});assert.equal(run.error,undefined,JSON.stringify(run));assert.equal(run.view.latest.result.state,"complete");assert.equal(f.tools.length,0);
});

test("paired real MCP pre-route model_unavailable remains confirmed with no provider or host sends",options,async t=>{
  const f=await pairedFixture(t,binary,{comparing:true});const result=await f.act("test",{model:"fake"});
  assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.view.latest.result.state,"refused");assert.equal(result.view.latest.result.reasonCode,"model_unavailable");assert.equal(result.view.latest.result.modelRoute,"/");assert.equal(f.sends,0);assert.equal(f.tools.length,0);assert.equal((await f.act("reload")).error,undefined);
});

test("paired real MCP comparison stops after capture model failure without partner or host calls",options,async t=>{
  const f=await pairedFixture(t,binary,{comparing:true,failModel:true});const result=await f.act("test",{model:"unit",comparisonModels:["unit","unit-other"]});
  assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.view.latest.result.state,"error");assert.equal(result.comparison,undefined);assert.equal(f.previews.filter(row=>row.name==="test").length,1);assert.equal(f.tools.length,0);assert.ok(f.sends>0);
});

test("paired canvas holds an explicit override through capture and sparse save without touching the default suite", options, async t => {
  const f = await pairedFixture(t, binary, { tests: "selected.agent.test.md" }), before = await f.read();
  assert.equal(before.selectedSources.selector, "selected.agent.test.md");
  const result = await f.act("test"); assert.equal(result.error, undefined, JSON.stringify(result)); assert.equal(result.saved, true);
  assert.equal(await readFile(path.join(f.root, "a.agent.test.md"), "utf8"), f.suite);
  const fresh = await f.read(), examples = structuredClone(fresh.authoring.examples); examples[0].input = "another read";
  const saved = await f.act("save-examples", { examples, exampleOrigins: ["read"] });
  assert.equal(saved.error, undefined, JSON.stringify(saved)); assert.equal(saved.saved, true);
  assert.ok(f.previews.every(row => row.arguments.tests === "selected.agent.test.md"));
  assert.equal(await readFile(path.join(f.root, "a.agent.test.md"), "utf8"), f.suite);
  assert.ok((await readFile(path.join(f.root, "selected.agent.test.md"), "utf8")).includes("Selected override."));
});
