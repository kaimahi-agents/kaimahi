import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {createActionHandler,readView,startViewBridge} from '../../../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs';
import {instructionSections,replaceInstructions,replaceExamples} from '../../../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs';
const original=await readFile(new URL('../../../../../internal/kmx/agentfile/testdata/greeter.agent.md',import.meta.url),'utf8');
const revision=s=>'sha256:'+createHash('sha256').update(s).digest('hex');
function harness({omitModel=false,fixture=original}={}){
 let source=omitModel?fixture.replace(/## Model\r?\n[\s\S]*?(?=## Examples)/,''):fixture,latest={status:'none'};
 const calls=[],o={};let race=false,denied=false,unavailable=false,release;
 let examples=[{id:'greeting',input:'Ada',assertions:[{id:'greeting-text',type:'contains',value:'Hello, Ada'}]}];
 const raw=()=>({schemaVersion:1,kind:'kmx.agent-view',file:{path:'greeter.agent.md',revision:revision(source)},title:'Greeter',instructions:source,model:omitModel?'':'fake',examples,authoring:{source,editable:true},latest});
 const session={rpc:{mcp:{resources:{read:async({uri})=>({contents:[{uri,text:JSON.stringify(raw())}]})}},tools:{getCurrentMetadata:async()=>({tools:['edit','test','run'].map(op=>({name:'owned-'+op,mcpServerName:'kmx-agent-builder',mcpToolName:op}))}),execute:async request=>{
  calls.push(request);assert.equal(request.arguments.schemaVersion,1);assert.equal(request.arguments.path,'greeter.agent.md');
  if(unavailable)throw Error('private transport secret');
  if(release)await new Promise(resolve=>{release.resolve=resolve});
  if(denied)return{resultType:'rejected',error:'Permission rejected private secret'};
  const op=request.name.slice(6),a=request.arguments;
  if(op==='edit'){if(o.beforeEdit)source=o.beforeEdit(source);if(a.expectedRevision!==revision(source))return{resultType:'failure',error:'stale_revision'};source=a.content;if('nextExamples' in o)examples=o.nextExamples;latest={status:'stale'};return{resultType:'success'}}
  assert.equal(Object.hasOwn(a,'expectedRevision'),false,'MCP previews have no revision argument');
  if(race)source=source.replace('## Non-Responsibilities','\n## Non-Responsibilities');
  const all=examples??[],ids=all.map(e=>e.id).filter(id=>a.caseId===undefined||id===a.caseId);
  const result={schemaVersion:1,kind:'kmx.agent-file-preview',file:{path:'greeter.agent.md',revision:revision(source)},operation:op,state:'complete',verdict:'pass',fullPass:a.caseId===undefined,modelRoute:{provider:'fake',model:'fake'},selection:{scope:a.caseId===undefined?'full':'subset',totalCases:all.length,caseIds:ids},examples:ids.map(id=>({id,state:'complete',verdict:'pass',assertions:all.find(e=>e.id===id).assertions.map(c=>({id:c.id,type:c.type,verdict:'pass'}))}))};
  const none=op==='test'&&!all.length;
  if(none)Object.assign(result,{state:'refused',verdict:'unknown',fullPass:false,reasonCode:'no_tests',reason:'No tests yet — add or suggest one'});
  if(o.failTest)Object.assign(result,{state:'error',verdict:'unknown',fullPass:false,reasonCode:'model_unavailable'});
  latest={status:'current',result:o.shape?o.shape(result):result};
  return o.failTest||none?{resultType:'failure',error:'model unavailable private detail',structuredContent:latest.result}:{resultType:'success'};
 }}},send:async prompt=>{calls.push({chat:prompt})}};
 return{o,session,calls,raw,source:()=>source,setRace:()=>{race=true},setDenied:()=>{denied=true},setUnavailable:()=>{unavailable=true},hold:()=>{release={};return()=>release.resolve()},act:createActionHandler({session,path:'greeter.agent.md'})};
}
test('runtime model override reaches Run/Test and save-and-test without changing the source model', async()=>{
 for(const operation of ['run','test','save-and-test']){
  const h=harness();h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model}});
  const r=await h.act({operation,expectedRevision:revision(original),model:'choice-b',...(operation==='run'?{input:'Ada'}:{})});
  assert.equal(r.error,undefined);assert.equal(h.calls.at(-1).arguments.model,'choice-b');
  assert.equal(h.source(),original);assert.equal(r.view.model,'fake');assert.equal(r.view.latest.result.modelRoute,'copilot/choice-b');
 }
});
test('missing-Model runtime defaults exercise actual LF and CRLF source snapshots',async()=>{
 for(const eol of ['\n','\r\n']){
  const h=harness({omitModel:true,fixture:original.replace(/\r?\n/g,eol)});
  assert.doesNotMatch(h.source(),/^## Model\r?$/m);
  h.session.rpc.model={getCurrent:async()=>({modelId:'chat-model'})};
  h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model}});
  const response=await h.act({operation:'test',expectedRevision:revision(h.source())});
  assert.equal(response.error,undefined);assert.equal(h.calls.at(-1).arguments.model,'chat-model');
  assert.doesNotMatch(response.view.authoring.source,/^## Model\r?$/m);
 }
});

test('known uncatalogued chat default is sent exactly without eager catalog or source Model',async()=>{
 const h=harness({omitModel:true}),source=h.source(),read=h.session.rpc.mcp.resources.read;
 h.session.rpc.model={getCurrent:async()=>({modelId:'known-unlisted'})};
 h.session.rpc.mcp.resources.read=async args=>{assert.notEqual(args.uri,'kmx://models');return read(args)};
 h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model}});
 const r=await h.act({operation:'test',expectedRevision:revision(source)});
 assert.equal(r.error,undefined);assert.equal(h.calls.at(-1).arguments.model,'known-unlisted');
 assert.doesNotMatch(r.view.authoring.source,/## Model/);assert.equal(h.source(),source);
 h.o.failTest=true;h.o.shape=r=>({...r,state:'refused',modelRoute:{}});
 assert.equal((await h.act({operation:'test',expectedRevision:revision(source)})).error,'action-failed');
 assert.equal(h.calls.at(-1).arguments.model,'known-unlisted');
});
test('optional metadata deadline preserves the known model and ignores late replies',async t=>{
 t.mock.timers.enable({apis:['setTimeout']});
 for(const known of [undefined,'chat-a']){
  const h=harness({omitModel:true});let release;
  if(known){h.session.rpc.model={getCurrent:async()=>({modelId:known})};await readView(h.session,'greeter.agent.md')}
  h.session.rpc.model={getCurrent:()=>new Promise(resolve=>{release=resolve})};
  let settled=false;const pending=readView(h.session,'greeter.agent.md').then(view=>{settled=true;return view});
  t.mock.timers.tick(2000);await new Promise(setImmediate);
  assert.equal(settled,true,'optional host metadata must not stall an otherwise valid resource');
  const view=await pending;assert.equal(view.chatModel,known);assert.equal(view.authoring.source,h.source());
  h.session.rpc.model.getCurrent=async()=>({modelId:'chat-b'});
  assert.equal((await readView(h.session,'greeter.agent.md')).chatModel,'chat-b');
  release({modelId:'late-chat'});await new Promise(setImmediate);
  assert.equal((await readView(h.session,'greeter.agent.md')).chatModel,'chat-b');assert.equal(h.calls.length,0);
 }
});

test('explicit auto/invalid host metadata clears known chat forwarding, while throws retain it',async()=>{
 for(const next of [undefined,'auto','bad\nID',42,'throw']){
  const h=harness({omitModel:true});let current='chat-a';
  h.session.rpc.model={getCurrent:async()=>{if(current==='throw')throw Error('transient');return{modelId:current}}};
  assert.equal((await readView(h.session,'greeter.agent.md')).chatModel,'chat-a');current=next;
  h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model??'gpt-5.6-terra'}});
  const response=await h.act({operation:'test',expectedRevision:revision(h.source())});
  assert.equal(response.error,undefined);assert.equal(h.calls.at(-1).arguments.model,next==='throw'?'chat-a':undefined);
  assert.equal(response.view.chatModel,next==='throw'?'chat-a':undefined);
 }
});
test('default follows chat at action start rather than the last displayed observation',async()=>{
 const h=harness({omitModel:true});let current='chat-a';
 h.session.rpc.model={getCurrent:async()=>({modelId:current})};
 const displayed=await readView(h.session,'greeter.agent.md');current='chat-b';
 h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model}});
 const response=await h.act({operation:'test',expectedRevision:displayed.file.revision});
 assert.equal(displayed.chatModel,'chat-a');assert.equal(response.error,undefined);
 assert.equal(h.calls.at(-1).arguments.model,'chat-b');
 assert.equal(response.view.latest.result.modelRoute,'copilot/chat-b');
 assert.doesNotMatch(h.source(),/## Model/);
});

test('a save freezes its selected chat model even if later host metadata changes',async()=>{
 const h=harness({omitModel:true});let current='chat-a';
 h.session.rpc.model={getCurrent:async()=>({modelId:current})};
 const execute=h.session.rpc.tools.execute;h.session.rpc.tools.execute=async request=>{const response=await execute(request);if(request.name==='owned-edit')current='chat-b';return response};
 h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:h.calls.at(-1).arguments.model}});
 const sections=instructionSections(h.source());sections[0].text+='\nKeep it short.';
 const response=await h.act({operation:'save-and-test',expectedRevision:revision(h.source()),sections});
 assert.equal(response.error,undefined);assert.equal(h.calls.at(-1).arguments.model,'chat-a');
 assert.equal(response.view.chatModel,'chat-b');assert.equal(response.view.latest.result.modelRoute,'copilot/chat-a');
 assert.doesNotMatch(h.source(),/## Model/);
});
test('same-revision observation from a different model cannot confirm this invocation',async()=>{
 const h=harness();const r=await h.act({operation:'test',expectedRevision:revision(original),model:'choice-b'});
 assert.equal(r.error,'outcome-unknown');assert.equal(r.view,undefined);
});
test('model catalog reads only its owned resource, with no tool or chat invocation',async()=>{
 const h=harness(),reads=[];
 h.session.rpc.mcp.resources.read=async args=>{reads.push(args);return{contents:[{text:JSON.stringify({schemaVersion:1,kind:'kmx.models',defaultModel:'gpt-5.6-terra',models:['choice-a','choice-b']})}]}};
 const r=await h.act({operation:'models'});assert.equal(r.error,undefined);assert.deepEqual(r.models,['choice-a','choice-b']);
 assert.deepEqual(reads,[{serverName:'kmx-agent-builder',uri:'kmx://models'}]);assert.equal(h.calls.length,0);
});
test('unknown model exposes the attested one-line choice list, not raw SDK text',async()=>{
 const h=harness(),reason='Available Copilot models: '+Array.from({length:40},(_,i)=>'available-model-'+i).join(', ');
 h.o.failTest=true;h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:'missing-model'},reasonCode:'copilot_model_unavailable',reason});
 const r=await h.act({operation:'test',expectedRevision:revision(original),model:'missing-model'});
 assert.equal(r.error,'action-failed');assert.equal(r.message,reason);assert.doesNotMatch(JSON.stringify(r),/private detail/);
});
test('pre-route no-tests refusal remains known without fabricating a model identity',async()=>{
 const h=harness();h.o.nextExamples=[];h.o.shape=r=>({...r,modelRoute:{}});
 const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),examples:[]});
 assert.equal(r.error,'no-tests');assert.ok(r.view);assert.equal(r.saved,true);
});
test('known pre-route model refusal does not invent identity or lock the next action',async()=>{
 const h=harness();h.o.failTest=true;h.o.shape=r=>({...r,state:'refused',modelRoute:{}});
 const input={operation:'test',expectedRevision:revision(original)};
 const first=await h.act(input);assert.equal(first.error,'action-failed');assert.ok(first.view);
 assert.equal((await h.act(input)).error,'action-failed');assert.equal(h.calls.length,2);
});
test('malformed catalog data stays nonblocking and never exposes provider text',async()=>{
 for(const id of ['https://private.invalid','auto','bad\nmodel']){
  const h=harness(),read=h.session.rpc.mcp.resources.read;
  h.session.rpc.mcp.resources.read=async a=>a.uri==='kmx://models'?{contents:[{text:JSON.stringify({schemaVersion:1,kind:'kmx.models',defaultModel:'gpt-5.6-terra',models:[id]})}]}:read(a);
  const r=await h.act({operation:'models'});assert.equal(r.error,'models-unavailable');assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,undefined);
 }
});
test('full authoring snapshot is hash-bound while display values remain bounded',async()=>{const h=harness();const view=await readView(h.session,'greeter.agent.md');assert.equal(view.authoring.source,original);assert.equal(view.authoring.editable,true);assert.equal(view.authoring.examples[0].assertions[0].value,'Hello, Ada')});
test('save-and-test uses original Edit revision, schemaVersion and explicit answer opt-in',async()=>{const h=harness();const sections=instructionSections(original);sections[0].text+='\nKeep it short.';const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections});assert.equal(r.error,undefined);assert.equal(r.saved,true);assert.deepEqual(h.calls.map(c=>c.name),['owned-edit','owned-test']);assert.equal(h.calls[0].arguments.expectedRevision,revision(original));assert.deepEqual(h.calls[1].arguments,{schemaVersion:1,path:'greeter.agent.md',showContent:false,showFailedContent:true});assert.equal(h.calls.some(c=>c.chat),false);assert.equal(r.view.file.revision,revision(h.source()))});
test('Test and Run default to failure-only arguments without a public revision parameter',async()=>{for(const operation of ['test','run']){const h=harness();const r=await h.act({operation,expectedRevision:revision(original),...(operation==='run'?{input:'Ada'}:{})});assert.equal(r.error,undefined);assert.deepEqual(h.calls[0].arguments,{schemaVersion:1,path:'greeter.agent.md',...(operation==='run'?{input:'Ada'}:{}),showContent:false,showFailedContent:true});assert.equal(h.calls.length,1)}});
test('stale draft refuses before any Edit or Test',async()=>{const h=harness();const r=await h.act({operation:'save-and-test',expectedRevision:'sha256:'+'0'.repeat(64),sections:instructionSections(original)});assert.equal(r.error,'stale_revision');assert.equal(h.calls.length,0)});
test('file change between Edit and Test is never attributed to the saved draft',async()=>{const h=harness();h.setRace();const sections=instructionSections(original);sections[0].text+='\nKeep it short.';const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections});assert.equal(r.error,'revision_mismatch');assert.equal(r.view,undefined);assert.equal(r.saved,true)});
test('permission refusal stops execution and leaks no native error',async()=>{const h=harness();h.setDenied();const sections=instructionSections(original);sections[0].text+='\nKeep it short.';const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections});assert.equal(r.error,'permission_required');assert.equal(h.calls.length,1);assert.doesNotMatch(JSON.stringify(r),/private secret/)});
test('unknown transport outcome locks retry until explicit reload',async()=>{const h=harness();h.setUnavailable();const input={operation:'test',expectedRevision:revision(original)};assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal(h.calls.length,1);assert.ok((await h.act({operation:'reload'})).view)});
test('acknowledged Edit followed by failed refresh locks retry instead of treating a saved draft as unsaved',async()=>{
 const h=harness(),read=h.session.rpc.mcp.resources.read;let fail=true;
 h.session.rpc.mcp.resources.read=async args=>{if(fail&&h.calls.some(c=>c.name==='owned-edit')){fail=false;throw Error('private refresh secret')}return read(args)};
 const sections=instructionSections(original);sections[0].text+='\nKeep it short.';
 const input={operation:'save-and-test',expectedRevision:revision(original),sections};
 assert.equal((await h.act(input)).error,'outcome-unknown');
 assert.equal((await h.act(input)).error,'outcome-unknown');
 assert.deepEqual(h.calls.map(c=>c.name),['owned-edit']);
 assert.ok((await h.act({operation:'reload'})).view);
});
test('acknowledged Test followed by failed refresh locks another inference until reload',async()=>{
 const h=harness(),read=h.session.rpc.mcp.resources.read;
 h.session.rpc.mcp.resources.read=async args=>{if(h.calls.length)throw Error('private read error');return read(args)};
 const input={operation:'test',expectedRevision:revision(original)};
 assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal(h.calls.length,1);
});
test('successful tool without a matching observation is unknown, not a false file-change claim',async()=>{
 const h=harness(),read=h.session.rpc.mcp.resources.read;
 h.session.rpc.mcp.resources.read=async args=>{const r=await read(args);if(h.calls.length){const raw=JSON.parse(r.contents[0].text);raw.latest={status:'none'};r.contents[0].text=JSON.stringify(raw)}return r};
 const input={operation:'test',expectedRevision:revision(original)};
 const r=await h.act(input);assert.equal(r.error,'outcome-unknown');assert.doesNotMatch(r.message,/file changed/);
 assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal(h.calls.length,1);
});
for(const [resultType,code]of[['failure','outcome-unknown'],['rejected','permission_required'],['denied','permission_required'],['timeout','outcome-unknown']])test('SDK '+resultType+' uses execution classification, not model error wording',async()=>{
 const h=harness();let calls=0;
 h.session.rpc.tools.execute=async()=>{calls++;return{resultType,error:'model denied or rejected this answer'}};
 const input={operation:'test',expectedRevision:revision(original)};
 assert.equal((await h.act(input)).error,code);
 if(resultType==='timeout'||resultType==='failure'){assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal(calls,1)}
});
test('page cannot supply paths, servers, generic tools or implicit answer disclosure',async()=>{const h=harness();for(const input of [{operation:'test',path:'else.agent.md'},{operation:'execute',name:'shell'},{operation:'test',showContent:'true'},{operation:'test',showFailedContent:false},{operation:'toString'}])assert.equal((await h.act(input)).error,'invalid-action');assert.equal(h.calls.length,0)});
test('Suggest requests saved tests through chat without running or claiming additions completed',async()=>{
 const h=harness();const r=await h.act({operation:'suggest'});
 assert.match(r.message,/Asked Copilot/);assert.doesNotMatch(r.message,/Added|Nothing here/);
 assert.equal(h.calls.length,1);const prompt=h.calls[0].chat;
 assert.match(prompt,/kmx-agent-builder skill/);assert.match(prompt,/add.*tests/i);
 assert.match(prompt,/"greeter.agent.md"/);assert.match(prompt,/resource/);
 assert.match(prompt,/Edit/);assert.match(prompt,/current revision/);
 assert.match(prompt,/prompt, model and existing tests/);assert.match(prompt,/Do not run tests/);
 assert.doesNotMatch(prompt,/examples|proposals|sha256:|do not edit/);
 assert.equal(h.source(),original,'only chat is dispatched by the bridge');
 const ran=await h.act({operation:'run',expectedRevision:revision(original),input:'Ada',showContent:true});
 assert.equal(ran.error,undefined);assert.deepEqual(h.calls.at(-1).arguments,{schemaVersion:1,path:'greeter.agent.md',input:'Ada',showContent:true,showFailedContent:true});
});
test('action POST requires exact same origin and cannot be a form submission',async()=>{const h=harness();const b=await startViewBridge({session:h.session,path:'greeter.agent.md'});try{assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,403);assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{Origin:'https://untrusted.test','Content-Type':'application/json'},body:'{}'})).status,403);assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{Origin:new URL(b.url).origin,'Content-Type':'application/json'},body:JSON.stringify({operation:'reload'})})).status,200);assert.equal(h.calls.length,0)}finally{await b.close()}});
const farewell={id:'farewell',input:'Bo',assertions:[{id:'farewell-text',type:'contains',value:'Bye, Bo'}]};
const greeting={id:'greeting',input:'Ada',assertions:[{id:'greeting-text',type:'contains',value:'Hello, Ada'}]};
const edited=()=>{const s=instructionSections(original);s[0].text+='\nKeep it short.';return s};
test('combined sections and examples are one original-revision Edit, then a single-case Test of a draft-added case',async()=>{
 const h=harness(),sections=edited(),examples=[greeting,farewell];h.o.nextExamples=examples;
 const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections,examples,caseId:'farewell'});
 assert.equal(r.error,undefined);assert.equal(r.saved,true);
 assert.deepEqual(h.calls.map(c=>c.name),['owned-edit','owned-test']);
 assert.deepEqual(h.calls[0].arguments,{schemaVersion:1,path:'greeter.agent.md',expectedRevision:revision(original),content:replaceExamples(replaceInstructions(original,sections),examples)});
 assert.deepEqual(h.calls[1].arguments,{schemaVersion:1,path:'greeter.agent.md',caseId:'farewell',showContent:false,showFailedContent:true});
 assert.equal(r.view.file.revision,revision(h.source()));assert.deepEqual(r.view.latest.result.selection,{scope:'subset',totalCases:2,caseIds:['farewell']});
});
test('absent optional drafts are no-ops: unchanged save-and-test runs the full suite and examples-only keeps prompt bytes',async()=>{
 let h=harness();let r=await h.act({operation:'save-and-test',expectedRevision:revision(original)});
 assert.equal(r.error,undefined);assert.equal(r.saved,false);assert.deepEqual(h.calls.map(c=>c.arguments),[{schemaVersion:1,path:'greeter.agent.md',showContent:false,showFailedContent:true}]);
 assert.deepEqual(r.view.latest.result.selection,{scope:'full',totalCases:1,caseIds:['greeting']});
 h=harness();const examples=[greeting,farewell];h.o.nextExamples=examples;r=await h.act({operation:'save-and-test',expectedRevision:revision(original),examples});
 assert.equal(r.error,undefined);assert.equal(h.calls[0].arguments.content,replaceExamples(original,examples));assert.equal(Object.hasOwn(h.calls[1].arguments,'caseId'),false);
 h=harness();r=await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'});
 assert.equal(r.error,undefined);assert.deepEqual(h.calls[0].arguments,{schemaVersion:1,path:'greeter.agent.md',caseId:'greeting',showContent:false,showFailedContent:true});
});
test('missing, arbitrary, removed or duplicate case IDs are refused before any Edit or Test',async()=>{
 for(const input of [
  {operation:'test',caseId:'missing'},{operation:'test',caseId:''},{operation:'test',caseId:5},{operation:'test',caseId:null},
  {operation:'save-and-test',sections:edited(),caseId:'farewell'},
  {operation:'save-and-test',sections:edited(),examples:[farewell],caseId:'greeting'},
  {operation:'save-and-test',examples:[greeting,{...farewell,id:'greeting'}],caseId:'greeting'},
  {operation:'save-and-test',sections:null},
  {operation:'run',input:'Ada',caseId:'greeting'},{operation:'save-examples',examples:[greeting],caseId:'greeting'},
 ]){const h=harness();const r=await h.act({expectedRevision:revision(original),...input});assert.equal(r.error,'invalid-action',JSON.stringify(input));assert.equal(h.calls.length,0)}
});
test('the original expected revision is never upgraded when the file changes before Edit',async()=>{
 let h=harness();let r=await h.act({operation:'save-and-test',expectedRevision:'sha256:'+'1'.repeat(64),sections:edited(),examples:[greeting,farewell],caseId:'farewell'});
 assert.equal(r.error,'stale_revision');assert.equal(h.calls.length,0);
 h=harness();h.o.beforeEdit=s=>s.replace('## Non-Responsibilities','\n## Non-Responsibilities');
 r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections:edited(),examples:[greeting,farewell],caseId:'farewell'});
 assert.equal(r.error,'stale_revision');assert.equal(r.saved,false);assert.equal(r.view,undefined);
 assert.deepEqual(h.calls.map(c=>[c.name,c.arguments.expectedRevision]),[['owned-edit',revision(original)]]);
});
for(const [name,input,shape]of[
 ['subset reported as full',{caseId:'greeting'},r=>({...r,selection:{...r.selection,scope:'full'},fullPass:true})],
 ['full reported as subset',{},r=>({...r,selection:{...r.selection,scope:'subset'},fullPass:false})],
 ['duplicate selected IDs',{},r=>({...r,selection:{...r.selection,caseIds:['greeting','greeting']}})],
 ['missing selected IDs',{},r=>({...r,selection:{...r.selection,caseIds:[]},examples:[]})],
 ['extra selected IDs',{},r=>({...r,selection:{...r.selection,caseIds:['greeting','other']}})],
 ['different single case',{caseId:'greeting'},r=>({...r,selection:{...r.selection,caseIds:['other']}})],
 ['wrong total',{},r=>({...r,selection:{...r.selection,totalCases:2}})],
 ['unselected result row',{caseId:'greeting'},r=>({...r,examples:[...r.examples,{...r.examples[0],id:'other'}]})],
])test('actual Test selection mismatch is unknown, never a result: '+name,async()=>{
 const h=harness();h.o.shape=shape;const r=await h.act({operation:'test',expectedRevision:revision(original),...input});
 assert.equal(r.error,'outcome-unknown');assert.equal(r.view,undefined);
 assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,'outcome-unknown');assert.equal(h.calls.length,1);
});
test('failed Test returns only a fresh, matching, non-passing observation at the known revision',async()=>{
 let h=harness();h.o.failTest=true;let r=await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'});
 assert.equal(r.error,'action-failed');assert.equal(r.view.latest.result.state,'error');assert.equal(r.view.latest.badge,'not-passing');assert.doesNotMatch(JSON.stringify(r),/private detail/);
 h=harness();h.o.failTest=true;h.o.shape=x=>({...x,state:'complete',verdict:'pass',fullPass:true,selection:{...x.selection,scope:'full'}});
 r=await h.act({operation:'test',expectedRevision:revision(original)});assert.equal(r.error,'outcome-unknown');assert.equal(r.view,undefined);
 h=harness();h.o.failTest=true;h.o.shape=x=>({...x,selection:{...x.selection,scope:'full'}});
 r=await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'});assert.equal(r.error,'outcome-unknown');assert.equal(r.view,undefined);
 h=harness();h.o.failTest=true;h.o.nextExamples=[greeting,farewell];
 r=await h.act({operation:'save-and-test',expectedRevision:revision(original),examples:[greeting,farewell],caseId:'farewell'});
 assert.equal(r.error,'action-failed');assert.equal(r.saved,true);assert.equal(r.view.file.revision,revision(h.source()));assert.deepEqual(r.view.latest.result.selection.caseIds,['farewell']);
 assert.equal((await h.act({operation:'test',expectedRevision:revision(h.source())})).error,'action-failed','action failure is not an uncertainty lock');
});
test('failed dispatch without an invocation payload cannot reuse an unchanged prior non-passing Test',async()=>{
 const h=harness();h.o.failTest=true;
 const first=await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'});
 assert.equal(first.view.latest.result.state,'error');
 let attempts=0;h.session.rpc.tools.execute=async()=>{attempts++;return{resultType:'failure',error:'dispatch failed before MCP'}};
 const failed=await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'});
 assert.equal(failed.error,'outcome-unknown');assert.equal(failed.view,undefined);
 assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,'outcome-unknown');assert.equal(attempts,1);
 await h.act({operation:'reload'});
 assert.equal((await h.act({operation:'test',expectedRevision:revision(original),caseId:'greeting'})).error,'outcome-unknown');assert.equal(attempts,2);
});
test('zero tests: save, combined save, ad-hoc Run and a known no-tests Test refusal',async()=>{
 let h=harness();h.o.nextExamples=null;
 let r=await h.act({operation:'save-examples',expectedRevision:revision(original),examples:[]});
 assert.equal(r.error,undefined);assert.equal(r.saved,true);assert.equal(h.calls[0].arguments.content,replaceExamples(original,[]));
 assert.deepEqual(r.view.examples,[]);assert.deepEqual(r.view.authoring.examples,[]);assert.equal(r.view.authoring.editable,true);
 const zero=revision(h.source());
 r=await h.act({operation:'run',expectedRevision:zero,input:'Ada'});assert.equal(r.error,undefined);assert.deepEqual(r.view.latest.result.selection,{scope:'full',totalCases:0,caseIds:[]});
 r=await h.act({operation:'test',expectedRevision:zero});
 assert.deepEqual([r.error,r.message,r.saved],['no-tests','No tests yet — add or suggest one',false]);assert.equal(r.view.latest.result.reasonCode,'no_tests');assert.equal(r.view.latest.badge,'not-passing');
 assert.equal((await h.act({operation:'test',expectedRevision:zero,caseId:'greeting'})).error,'invalid-action','a selected case still refuses without tests');
 assert.equal((await h.act({operation:'test',expectedRevision:zero})).error,'no-tests','a known refusal is not an uncertainty lock');
 h=harness();h.o.nextExamples=null;
 assert.equal((await h.act({operation:'save-and-test',expectedRevision:revision(original),examples:[],caseId:'greeting'})).error,'invalid-action');assert.equal(h.calls.length,0);
 r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections:edited(),examples:[]});
 assert.deepEqual([r.error,r.message,r.saved],['no-tests','No tests yet — add or suggest one',true]);
 assert.deepEqual(h.calls.map(c=>c.name),['owned-edit','owned-test']);assert.equal(r.view.file.revision,revision(h.source()));assert.equal(r.view.latest.result.reasonCode,'no_tests');
});
