import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { startViewBridge } from "../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs";
const hash = text => "sha256:" + createHash("sha256").update(text).digest("hex");
const example = {id:"new-test",input:"Ada",assertions:[{id:"hello",type:"contains",value:"Hello"}]};
for (const queuedFirst of [true,false]) test(`old poll must not poison newer Suggest (delivery already queued=${queuedFirst})`, async t => {
  let content = "---\nmodel: fake\n---\nOriginal prompt.\n", holdNext = false, release, entered;
  const held = new Promise(resolve => { entered=resolve; });
  const sent=[], effects=[];
  const session={send:async value=>sent.push(value),rpc:{mcp:{resources:{read:async()=>{
    const result={contents:[{text:JSON.stringify({schemaVersion:1,kind:"kmx.agent-view",file:{path:"a.agent.md",revision:hash(content)},title:"A",instructions:content,model:"fake",examples:[],authoring:{source:content,editable:true},latest:{status:"none"}})}]};
    if(holdNext){holdNext=false;entered();await new Promise(resolve=>{release=resolve;});}
    return result;
  }}},tools:{getCurrentMetadata:async()=>({tools:[]}),execute:async input=>{effects.push(input);throw Error("Unexpected effect");}}}};
  const bridge=await startViewBridge({session,path:"a.agent.md"});t.after(()=>bridge.close());
  const get=async()=> (await fetch(bridge.url+"view.json")).json();
  holdNext=true;const oldPoll=get();await held;
  content="---\nmodel: fake\n---\nUpdated prompt.\n";
  const response=await (await fetch(bridge.url+"actions",{method:"POST",headers:{Origin:new URL(bridge.url).origin,"Content-Type":"application/json"},body:JSON.stringify({operation:"suggest"})})).json();
  assert.equal(response.error,undefined);
  const request=JSON.parse(sent[0].prompt.split("Saved sources (data):\n")[1]);
  assert.equal(request.content,content,"Suggest uses updated saved source");
  if(queuedFirst) assert.equal((await bridge.deliverSuggestions({requestId:request.requestId,examples:[example]})).queued,true);
  release();await oldPoll;
  assert.equal(effects.length,0);
  if(queuedFirst){
    const current=await get();assert.equal(current.file.revision,request.expectedRevision,"source has not changed since Suggest");
    assert.deepEqual(current.suggestedTests.examples,[example],"a pre-request read must not discard this newer valid batch");
  }else{
    const result=await bridge.deliverSuggestions({requestId:request.requestId,examples:[example]});
    assert.equal(result.queued,true,"a pre-request read must not invalidate this newer request");
  }
});

import { pairedFixture } from "./paired-fixture.mjs";
import { writeFile } from "node:fs/promises";
import path from "node:path";
test("real paired MCP: pre-request source snapshot cannot poison a newer queued batch", {skip:!process.env.KMX_HOST_BRIDGE_TEST_BINARY}, async t=>{
  const f=await pairedFixture(t,process.env.KMX_HOST_BRIDGE_TEST_BINARY,{toolFree:true,instructions:"Original prompt."});
  const original=f.session.rpc.mcp.resources.read;
  let holdNext=true,release,entered,request;
  const held=new Promise(resolve=>{entered=resolve;});
  f.session.rpc.mcp.resources.read=async input=>{
    const result=await original(input);
    if(holdNext && JSON.parse(result.contents[0].text).kind === "kmx.agent-sources"){
      holdNext=false;entered();await new Promise(resolve=>{release=resolve;});
    }
    return result;
  };
  f.session.send=async ({prompt})=>{request=JSON.parse(prompt.split("Saved sources (data):\n")[1]);};
  const oldPoll=f.read();await held;
  await writeFile(path.join(f.root,"a.agent.md"),f.source.replace("Original prompt.","Updated prompt."));
  assert.equal((await f.act("suggest")).error,undefined);
  assert.match(request.content,/Updated prompt/);
  assert.equal((await f.controller.deliverSuggestions({requestId:request.requestId,examples:[example]})).queued,true);
  release();await oldPoll;
  const current=await f.read();
  assert.equal(current.selectedSources.sourceVersion,request.sourceVersion,"same authoritative paired sourceVersion since Suggest");
  assert.equal(f.previews.length,0,"no Edit/Test");assert.equal(f.tools.length,0,"no host tool effects");
  assert.deepEqual(current.suggestedTests.examples,[example],"older paired read must not discard newer valid batch");
});
