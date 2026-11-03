import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { pairedFixture } from "./paired-fixture.mjs";

const binary = process.env.KMX_HOST_BRIDGE_TEST_BINARY;
test("suggested paired tests leave exact files untouched until reviewed edited examples are saved and tested", {skip:!binary,timeout:60000}, async t=>{
  const f=await pairedFixture(t,binary,{toolFree:true,instructions:"Reply with exactly `done`."});
  let request;
  f.session.send=async ({prompt})=>{request=JSON.parse(prompt.split("Saved sources (data):\n")[1])};
  const before=await f.read(),agentPath=path.join(f.root,"a.agent.md"),testsPath=path.join(f.root,"a.agent.test.md");
  const agentBytes=await readFile(agentPath),testBytes=await readFile(testsPath);
  assert.equal((await f.act("suggest")).error,undefined);
  const examples=[{id:"keep-me",input:"before editing",assertions:[{id:"answer",type:"contains",value:"done"}]},{id:"drop-me",input:"never run this",assertions:[{id:"answer",type:"contains",value:"not present"}]}];
  assert.equal((await f.controller.deliverSuggestions({requestId:request.requestId,examples})).queued,true);
  const batch=(await f.read()).suggestedTests;
  assert.deepEqual(batch.examples,examples);
  assert.deepEqual((await f.read()).authoring.examples,before.authoring.examples,"proposals are not saved examples");
  assert.deepEqual(await readFile(agentPath),agentBytes);assert.deepEqual(await readFile(testsPath),testBytes);
  assert.equal(f.previews.length,0,"Suggest, delivery and polling cause neither Edit nor Test");
  const ack=await fetch(f.url+"actions",{method:"POST",headers:{Origin:new URL(f.url).origin,"Content-Type":"application/json"},body:JSON.stringify({operation:"acknowledge-suggestions",batchId:batch.id})});
  assert.equal(ack.ok,true);assert.equal((await f.read()).suggestedTests,undefined);
  // The existing editor's tested keep/edit/drop path submits only the kept draft.
  const edited={...examples[0],input:"edited by the user"};
  const result=await f.act("save-and-test",{examples:[...before.authoring.examples,edited],exampleOrigins:["read",null]});
  assert.equal(result.error,undefined,JSON.stringify(result));assert.equal(result.saved,true);
  const savedTests=await readFile(testsPath,"utf8");
  assert.ok(savedTests.includes("edited by the user"));assert.ok(!savedTests.includes("drop-me"));assert.ok(savedTests.includes("# Keep this prose."));
  assert.deepEqual(await readFile(agentPath),agentBytes,"test-only save preserves agent bytes");
  assert.deepEqual(result.view.latest.result.selection.caseIds,["read","keep-me"]);
  assert.ok(f.previews.some(call=>call.name==="edit"));assert.equal(f.previews.filter(call=>call.name==="test").length,1);
});
