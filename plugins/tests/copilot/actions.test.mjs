import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {createActionHandler,readView,startViewBridge} from '../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs';
import {instructionSections,replaceInstructions,replaceExamples} from '../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs';
const original=await readFile(new URL('../../../internal/kmx/agentfile/testdata/greeter.agent.md',import.meta.url),'utf8');
const revision=s=>'sha256:'+createHash('sha256').update(s).digest('hex');
function harness({omitModel=false,fixture=original}={}){
 let source=omitModel?fixture.replace(/## Model\r?\n[\s\S]*?(?=## Examples)/,''):fixture,latest={status:'none'};
 const calls=[],o={};let race=false,denied=false,unavailable=false,release;
 let examples=fixture.includes('examples: []')?[]:[{id:'greeting',input:'Ada',assertions:[{id:'greeting-text',type:'contains',value:'Hello, Ada'}]}];
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
test('runtime reads existing tool declarations without adding a tool editor', async()=>{
 const h=harness({fixture:original.replace('## Examples','## Tools\n- ping\n\n## Examples')});
 const read=h.session.rpc.mcp.resources.read;
 h.session.rpc.mcp.resources.read=async args=>{const result=await read(args);const raw=JSON.parse(result.contents[0].text);raw.tools=['ping'];result.contents[0].text=JSON.stringify(raw);return result};
 const view=await readView(h.session,'greeter.agent.md');
 assert.deepEqual(view.tools,['ping']);assert.equal(view.authoring?.editable,true);
 const changed=replaceInstructions(view.authoring.source,[{key:'Non-Responsibilities',text:'No extra calls.'}]);
 assert.match(changed,/## Tools\n- ping\n\n## Examples/);
 const result=await h.act({operation:'test',expectedRevision:revision(h.source())});
 assert.equal(result.error,undefined,'missing bridge API keeps existing native unbound path; no alternative endpoint');
 assert.equal(h.calls.at(-1).name,'owned-test');
});

for(const eol of ['\n','\r\n']) test('tools save uses the current source revision, preserves prompt/examples, and reads back before Test '+JSON.stringify(eol),async()=>{
 const source=original.replace(/\r?\n/g,eol),h=harness({fixture:source});
 const response=await h.act({operation:'save-and-test',expectedRevision:revision(h.source()),tools:['search','list_tags']});
 assert.equal(response.error,undefined);assert.equal(response.saved,true);
 assert.deepEqual(h.calls.map(c=>c.name),['owned-edit','owned-test']);
 assert.equal(h.calls[0].arguments.expectedRevision,revision(source));
 assert.match(h.source(),/## Tools\r?\n- search\r?\n- list_tags\r?\n\r?\n## Examples/);
 assert.equal(response.view.file.revision,revision(h.source()));
 assert.deepEqual(response.view.authoring.examples,h.raw().examples);
 const stale=await h.act({operation:'save-and-test',expectedRevision:revision(source),tools:[]});
 assert.equal(stale.error,'stale_revision');assert.equal(h.calls.length,2);
});
for(const eol of ['\n','\r\n']) test('tools save with zero tests acknowledges source save without claiming tests ran '+JSON.stringify(eol),async()=>{
 const {replaceExamples}=await import('../../copilot-wsl/com.github.copilot/extensions/agent-view/authoring.mjs');
 const h=harness({fixture:replaceExamples(original.replace(/\r?\n/g,eol),[])});h.o.nextExamples=[];
 const response=await h.act({operation:'save-and-test',expectedRevision:revision(h.source()),tools:['ping']});
 assert.equal(response.saved,true);assert.equal(response.view.latest.result.fullPass,false);
 assert.equal(response.view.latest.result.reasonCode,'no_tests');assert.match(h.source(),/## Tools\r?\n- ping/);
});
test('read-only catalog action cannot accept browser paths or tool-execution operands',async()=>{
 const h=harness();
 for(const extra of [{path:'else.agent.md'},{serverName:'github'},{tools:['ping']},{content:'x'}]) assert.equal((await h.act({operation:'tools',...extra})).error,'invalid-action');
 const response=await h.act({operation:'tools'});
 assert.equal(response.error,undefined);assert.deepEqual(response.servers,[]);assert.equal(h.calls.length,0);
});
function deferred(){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no});return{promise,resolve,reject}}
function nativeHarness(options){
 const h=harness(options),listeners=new Set(),retired=[],requests=[],gate=deferred(),ack=deferred(),aborts=[],dispatched=deferred(),events=new Map();
 h.session.on=(name,listener)=>{if(name==='tool.execution_progress')listeners.add(listener);const set=events.get(name)??new Set();events.set(name,set);set.add(listener);return()=>{set.delete(listener);listeners.delete(listener);if(name==='tool.execution_progress')retired.push(listener)}};
 const execute=h.session.rpc.tools.execute;
 h.session.rpc.tools.execute=async request=>{
  if(request.name!=='owned-test')return execute(request);
  assert.equal(listeners.size,1,'subscribe before dispatch');requests.push(request);dispatched.resolve();
  await gate.promise;return execute(request);
 };
 h.session.rpc.abort=args=>{aborts.push(args);return ack.promise};
 const emit=(row,toolCallId=requests.at(-1)?.toolCallId)=>{for(const listener of listeners)listener({data:{toolCallId,progressMessage:typeof row==='string'?row:'1.0: '+JSON.stringify(row)}})};
 const start=async(input={})=>{
  const pending=h.act({operation:'test',expectedRevision:revision(h.source()),runId:'run-1',...input});
  await new Promise(setImmediate);assert.equal(requests.length,1,'Test must dispatch once');
  return{pending};
 };
 return{...h,listeners,retired,requests,gate,ack,aborts,emit,start,dispatched,events,event:(name,data)=>{for(const listener of events.get(name)??[])listener({data})}};
}
test('stopped snapshots keep request settings from before a config change, not Stop readback settings',async()=>{
 const n=nativeHarness({fixture:original.replace('## Examples','## Tools\n- ping\n\n## Examples')});
 let state='Enabled';const read=n.session.rpc.mcp.resources.read;
 n.session.rpc.mcp.resources.read=async args=>{const r=await read(args),raw=JSON.parse(r.contents[0].text);raw.tools=['ping'];raw.toolSettings=[{name:'ping',state}];r.contents[0].text=JSON.stringify(raw);return r};
 const {pending}=await n.start();
 const progress=await n.act({operation:'progress',runId:'run-1'});
 assert.deepEqual(progress.run.toolSettings,[{name:'ping',state:'Enabled'}]);
 progress.run.toolSettings[0].state='Disabled';
 state='Disabled';await n.act({operation:'stop',runId:'run-1'});n.ack.resolve({success:true});n.gate.reject(Error('aborted'));
 const stopped=await pending;
 assert.equal(stopped.stopped,true);
 assert.deepEqual(stopped.run.toolSettings,[{name:'ping',state:'Enabled'}],'snapshot is detached request metadata, not execution proof');
 assert.deepEqual(stopped.view.toolSettings,[{name:'ping',state:'Disabled'}]);
});

test('native progress is correlated, bounded, ordered and provisional until the final Test reply',async()=>{
 const n=nativeHarness();n.o.nextExamples=[greeting,farewell];
 const {pending}=await n.start({operation:'save-and-test',sections:edited(),examples:[greeting,farewell]});
 const request=n.requests[0];assert.equal(typeof request.toolCallId,'string');assert.notEqual(request.toolCallId,'run-1');
 assert.deepEqual(request.arguments,{schemaVersion:1,path:'greeter.agent.md',showContent:false,showFailedContent:true});
 assert.deepEqual(await n.act({operation:'progress',runId:'foreign'}),{run:undefined});
 n.emit({caseId:'greeting',verdict:'pass',reason:''},'foreign-tool');
 for(const message of ['raw host secret','1.0: not JSON','1.0: []','1.0: null','{"caseId":"greeting","verdict":"pass","reason":""}',
  '1.0: '+JSON.stringify({caseId:'greeting',verdict:'pass',reason:'x'.repeat(20000)}),
  ...[{caseId:'foreign',verdict:'pass',reason:''},{caseId:'greeting',verdict:'passed',reason:''},{caseId:'greeting',verdict:'pass',reason:42}].map(r=>'1.0: '+JSON.stringify(r))])n.emit(message);
 assert.deepEqual((await n.act({operation:'progress',runId:'run-1'})).run.cases,[]);
 n.emit({caseId:'farewell',verdict:'fail',reason:'No\u0000\u202e\n\tmatch '+ 'x'.repeat(600)});
 n.emit({caseId:'greeting',verdict:'unknown',reason:'Judge unavailable'});
 n.emit({caseId:'greeting',verdict:'pass',reason:'duplicate'});
 const run=(await n.act({operation:'progress',runId:'run-1'})).run;
 assert.deepEqual(Object.keys(run).sort(),['id','revision','model','caseIds','totalCases','cases','state','elapsedMs'].sort());
 assert.deepEqual([run.id,run.revision,run.model,run.caseIds,run.totalCases,run.state],['run-1',revision(n.source()),'fake',['greeting','farewell'],2,'running']);
 assert.ok(Number.isFinite(run.elapsedMs)&&run.elapsedMs>=0);
 assert.deepEqual(run.cases.map(r=>[r.caseId,r.verdict]),[['greeting','unknown'],['farewell','fail']]);
 assert.match(run.cases[1].reason,/^No match /);assert.doesNotMatch(run.cases[1].reason,/[\u0000\u202e\n\t]/);assert.ok(run.cases[1].reason.length<550);
 run.cases[0].verdict='pass';assert.equal((await n.act({operation:'progress',runId:'run-1'})).run.cases[0].verdict,'unknown');
 n.gate.resolve();const result=await pending;
 assert.equal(result.error,undefined);assert.equal(result.view.latest.badge,'pass','final result overrides provisional unknown/fail');
 assert.equal(result.saved,true);assert.equal(result.run.cases[0].verdict,'unknown');assert.equal(n.listeners.size,0);
 n.retired[0]({data:{toolCallId:request.toolCallId,progressMessage:'1.0: {"caseId":"farewell","verdict":"pass","reason":"late"}'}});
 assert.equal((await n.act({operation:'progress',runId:'run-1'})).run.cases[1].verdict,'fail');
 await n.act({operation:'reload'});assert.deepEqual(await n.act({operation:'progress',runId:'run-1'}),{run:undefined});
});
test('live failure identifies only a check in this invocation saved examples',async()=>{
 const n=nativeHarness();n.o.nextExamples=[greeting,farewell];
 const {pending}=await n.start({operation:'save-and-test',sections:edited(),examples:[greeting,farewell]});
 n.emit({caseId:'greeting',verdict:'fail',reason:'Assertion not satisfied.',failedAssertionId:'greeting-text'});
 n.emit({caseId:'farewell',verdict:'fail',reason:'Assertion not satisfied.',failedAssertionId:'foreign-check'});
 const rows=(await n.act({operation:'progress',runId:'run-1'})).run.cases;
 assert.equal(rows[0].failedAssertionId,'greeting-text');
 assert.equal(rows[1].failedAssertionId,undefined);
 n.gate.resolve();await pending;
});
test('joined extensions hydrate approvals when the host does not deliver requested events',async()=>{
 const n=nativeHarness();const {pending}=await n.start(),toolCallId=n.requests[0].toolCallId;
 let items=[{requestId:'native-request',request:{kind:'mcp',toolCallId,args:{private:'approval-canary'}}},{requestId:'foreign',request:{kind:'mcp',toolCallId:'chat-call'}}];
 n.session.rpc.permissions={pendingRequests:async()=>({items})};
 const waiting=async()=>!!(await n.act({operation:'progress',runId:'run-1'})).run.approvalWaiting;
 assert.equal(await waiting(),true);
 assert.doesNotMatch(JSON.stringify(await n.act({operation:'progress',runId:'run-1'})),/native-request|chat-call|approval-canary/);
 const late=deferred();n.session.rpc.permissions.pendingRequests=()=>late.promise;
 const polling=n.act({operation:'progress',runId:'run-1'});await new Promise(setImmediate);
 n.event('permission.completed',{requestId:'native-request',toolCallId});late.resolve({items});
 assert.equal(!!(await polling).run.approvalWaiting,false,'late hydration cannot resurrect a completed prompt');
 items=[];n.session.rpc.permissions.pendingRequests=async()=>({items});assert.equal(await waiting(),false);
 n.gate.resolve();await pending;
});
test('request-only completion invalidates first hydration before its request ID is known',async()=>{
 const n=nativeHarness();const {pending}=await n.start(),toolCallId=n.requests[0].toolCallId,late=deferred();
 n.session.rpc.permissions={pendingRequests:()=>late.promise};
 const polling=n.act({operation:'progress',runId:'run-1'});await new Promise(setImmediate);
 n.event('permission.completed',{requestId:'first-request'});
 late.resolve({items:[{requestId:'first-request',request:{kind:'mcp',toolCallId}}]});
 assert.equal(!!(await polling).run.approvalWaiting,false);
 n.gate.resolve();await pending;
});
test('approval waiting requires owned native call and request IDs; completion and Stop clear it',async()=>{
 const n=nativeHarness();const {pending}=await n.start(),toolCallId=n.requests[0].toolCallId;
 const waiting=async()=>!!(await n.act({operation:'progress',runId:'run-1'})).run.approvalWaiting;
 for(const data of [{requestId:'foreign',permissionRequest:{toolCallId:'chat-call'}},{requestId:'missing',permissionRequest:{}},{requestId:'hook',permissionRequest:{toolCallId},resolvedByHook:true},{permissionRequest:{toolCallId}}]){
  n.event('permission.requested',data);assert.equal(await waiting(),false);
 }
 n.event('permission.requested',{requestId:'first',permissionRequest:{toolCallId,private:'permission-canary'}});
 n.event('permission.requested',{requestId:'second',permissionRequest:{toolCallId}});
 assert.equal(await waiting(),true);
 assert.doesNotMatch(JSON.stringify(await n.act({operation:'progress',runId:'run-1'})),/permission-canary|first|second|toolCallId/);
 n.event('permission.completed',{requestId:'first',toolCallId:'foreign'});assert.equal(await waiting(),true);
 n.event('permission.completed',{requestId:'first',toolCallId});n.event('permission.completed',{requestId:'foreign'});assert.equal(await waiting(),true);
 n.event('permission.completed',{requestId:'second'});assert.equal(await waiting(),false);
 n.event('permission.requested',{requestId:'third',permissionRequest:{toolCallId}});assert.equal(await waiting(),true);
 await n.act({operation:'stop',runId:'run-1'});assert.equal(await waiting(),false);
 n.event('permission.requested',{requestId:'late',permissionRequest:{toolCallId}});assert.equal(await waiting(),false);
 n.ack.resolve({success:true});n.gate.reject(Error('aborted'));await pending;
 assert.ok([...n.events.values()].every(set=>set.size===0));
});
for(const ending of ['finish','dispose'])test('approval observers are cleaned on '+ending+' and late requests do not revive waiting',async()=>{
 const n=nativeHarness();const {pending}=await n.start(),toolCallId=n.requests[0].toolCallId;
 n.event('permission.requested',{requestId:'waiting',permissionRequest:{toolCallId}});
 assert.equal((await n.act({operation:'progress',runId:'run-1'})).run.approvalWaiting,true);
 if(ending==='dispose')n.act.dispose();
 n.gate.resolve();await pending;
 assert.ok([...n.events.values()].every(set=>set.size===0));
 n.event('permission.requested',{requestId:'late',permissionRequest:{toolCallId}});
 assert.notEqual((await n.act({operation:'progress',runId:'run-1'})).run?.approvalWaiting,true);
});
test('run IDs validate before writes and fixed progress/Stop accept no execution selectors',async()=>{
 for(const runId of ['',null,42,'x'.repeat(129),'bad\nID']){
  const h=harness();const r=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections:edited(),runId});
  assert.equal(r.error,'invalid-action');assert.equal(h.calls.length,0);
 }
 const h=harness();
 for(const operation of ['progress','stop']){
  assert.deepEqual(await h.act({operation,runId:'absent'}),{run:undefined});
  for(const extra of [{path:'else.agent.md'},{model:'fake'},{expectedRevision:revision(original)},{caseId:'greeting'}])assert.equal((await h.act({operation,runId:'absent',...extra})).error,'invalid-action');
  assert.equal((await h.act({operation})).error,'invalid-action');
 }
 assert.equal(h.calls.length,0);
});
for(const first of ['ack','reject'])test('deliberate Stop requires local marker, success acknowledgement and execute rejection: '+first+' first',async()=>{
 const m=nativeHarness();const {pending:stopping}=await m.start({operation:'save-and-test',sections:edited()});
 m.emit({caseId:'greeting',verdict:'fail',reason:'Mismatch'});
 assert.deepEqual(await m.act({operation:'stop',runId:'foreign'}),{run:undefined});assert.equal(m.aborts.length,0);
 assert.deepEqual(await m.act({operation:'stop',runId:'run-1'}),{stopping:true});
 assert.deepEqual(await m.act({operation:'stop',runId:'run-1'}),{stopping:true});
 assert.deepEqual(m.aborts,[{reason:'remote_abort'}]);
 m.emit({caseId:'greeting',verdict:'pass',reason:'late'});
 assert.equal((await m.act({operation:'progress',runId:'run-1'})).run.state,'stopping');
 let settled=false;stopping.then(()=>{settled=true});
 if(first==='ack')m.ack.resolve({success:true});else m.gate.reject(Error('arbitrary private failure'));
 await new Promise(setImmediate);assert.equal(settled,false,'both acknowledgement and rejection are required');
 if(first==='ack')m.gate.reject(Error('not message-matched'));else m.ack.resolve({success:true});
 const result=await stopping;
 assert.equal(result.error,undefined);assert.equal(result.stopped,true);assert.equal(result.saved,true);
 assert.equal(result.view.file.revision,revision(m.source()));assert.equal(result.view.authoring.source,m.source());
 assert.deepEqual(result.view.latest,{status:'none',badge:'none'});
 assert.equal(result.run.state,'stopped');assert.deepEqual(result.run.cases,[{caseId:'greeting',verdict:'fail',reason:'Mismatch'}]);assert.equal(m.listeners.size,0);
 const before=m.aborts.length;assert.deepEqual(await m.act({operation:'stop',runId:'run-1'}),{run:undefined});assert.equal(m.aborts.length,before);
 m.session.rpc.tools.execute=async request=>({resultType:'rejected'});
 assert.equal((await m.act({operation:'test',runId:'next-run',expectedRevision:revision(m.source())})).error,'permission_required','confirmed Stop unlocks the next invocation');
 assert.deepEqual(await m.act({operation:'progress',runId:'run-1'}),{run:undefined});
});
for(const failure of ['false','throw','external'])test('unconfirmed or external Stop retains the uncertainty lock: '+failure,async()=>{
 const n=nativeHarness();const {pending}=await n.start();
 if(failure!=='external'){
  await n.act({operation:'stop',runId:'run-1'});
  if(failure==='false')n.ack.resolve({success:false});else n.ack.reject(Error('private abort secret'));
 }
 n.gate.reject(Error('Tool execution was aborted.'));
 const result=await pending;assert.equal(result.error,'outcome-unknown');assert.equal(result.stopped,undefined);assert.equal(result.view,undefined);assert.equal(n.listeners.size,0);
 assert.doesNotMatch(JSON.stringify(result),/private abort secret|Tool execution was aborted/);
 assert.equal((await n.act({operation:'test',expectedRevision:revision(original)})).error,'outcome-unknown');assert.equal(n.requests.length,1);
 assert.deepEqual(await n.act({operation:'progress',runId:'run-1'}),{run:undefined});
});
for(const refresh of ['failed-read','changed-revision'])test('Stop cannot confirm an unreadable or changed saved source: '+refresh,async()=>{
 const n=nativeHarness(),read=n.session.rpc.mcp.resources.read;const {pending}=await n.start({operation:'save-and-test',sections:edited()});
 await n.act({operation:'stop',runId:'run-1'});
 n.session.rpc.mcp.resources.read=async args=>{if(refresh==='failed-read')throw Error('private');const response=await read(args),body=JSON.parse(response.contents[0].text);body.file.revision='sha256:'+'0'.repeat(64);response.contents[0].text=JSON.stringify(body);return response};
 n.ack.resolve({success:true});n.gate.reject(Error('aborted'));
 const result=await pending;assert.equal(result.error,'outcome-unknown');assert.equal(result.saved,true);assert.equal(result.stopped,undefined);assert.equal(result.view,undefined);
 assert.equal((await n.act({operation:'test',expectedRevision:revision(n.source())})).error,'outcome-unknown');
});
for(const ack of ['success','hanging'])test('authoritative completion wins a Stop race even with '+ack+' abort acknowledgement',async()=>{
 const n=nativeHarness();const {pending}=await n.start();await n.act({operation:'stop',runId:'run-1'});
 if(ack==='success')n.ack.resolve({success:true});
 n.gate.resolve();const result=await pending;
 assert.equal(result.stopped,undefined);assert.equal(result.view.latest.badge,'pass');assert.equal(n.listeners.size,0);
 assert.deepEqual(await n.act({operation:'stop',runId:'run-1'}),{run:undefined});assert.equal(n.aborts.length,1);
 n.act.dispose();
});
test('late Stop after native settlement never aborts while final observation is pending',async()=>{
 const n=nativeHarness(),read=n.session.rpc.mcp.resources.read,reading=deferred();let finalRead=false;
 n.session.rpc.mcp.resources.read=async args=>{if(n.calls.length){finalRead=true;await reading.promise}return read(args)};
 const {pending}=await n.start();n.gate.resolve();await new Promise(setImmediate);assert.equal(finalRead,true);
 assert.deepEqual(await n.act({operation:'stop',runId:'run-1'}),{run:undefined});assert.equal(n.aborts.length,0);
 reading.resolve();assert.equal((await pending).view.latest.badge,'pass');
});
test('single-case snapshots accept only the selected expected case and native IDs never repeat',async()=>{
 const n=nativeHarness();n.o.nextExamples=[greeting,farewell];
 const {pending}=await n.start({operation:'save-and-test',examples:[greeting,farewell],caseId:'farewell'});
 n.emit({caseId:'greeting',verdict:'pass',reason:''});n.emit({caseId:'farewell',verdict:'pass',reason:''});
 const run=(await n.act({operation:'progress',runId:'run-1'})).run;
 assert.deepEqual(run.caseIds,['farewell']);assert.equal(run.totalCases,2);assert.deepEqual(run.cases,[{caseId:'farewell',verdict:'pass',reason:''}]);
 n.gate.resolve();await pending;
 await n.act({operation:'test',runId:'run-2',expectedRevision:revision(n.source())});
 assert.notEqual(n.requests[0].toolCallId,n.requests[1].toolCallId);assert.deepEqual(await n.act({operation:'progress',runId:'run-1'}),{run:undefined});
});
test('Run and Edit never subscribe or allow Stop to abort them',async()=>{
 const h=harness();h.session.on=()=>assert.fail('only Test subscribes');h.session.rpc.abort=()=>assert.fail('only pending Test aborts');
 const release=h.hold(),pending=h.act({operation:'run',expectedRevision:revision(original),input:'Ada'});await new Promise(setImmediate);
 assert.deepEqual(await h.act({operation:'stop',runId:'missing'}),{run:undefined});release();assert.equal((await pending).error,undefined);
 assert.equal(h.calls[0].toolCallId,undefined);
 const saved=harness();saved.session.on=()=>assert.fail('Edit never subscribes');
 assert.equal((await saved.act({operation:'save-prompt',expectedRevision:revision(original),sections:edited()})).error,undefined);
});
test('dispose releases a rejected Test awaiting hanging abort and unsubscribes without another abort',async()=>{
 const n=nativeHarness();const {pending}=await n.start();await n.act({operation:'stop',runId:'run-1'});n.gate.reject(Error('aborted'));
 await new Promise(setImmediate);n.act.dispose();n.act.dispose();
 assert.equal((await pending).error,'outcome-unknown');assert.equal(n.listeners.size,0);assert.equal(n.aborts.length,1);
 assert.deepEqual(await n.act({operation:'progress',runId:'run-1'}),{run:undefined});
});
test('bridge close releases progress subscriptions and never aborts an active Test',async()=>{
 const n=nativeHarness(),bridge=await startViewBridge({session:n.session,path:'greeter.agent.md'});
 const pending=fetch(bridge.url+'actions',{method:'POST',headers:{Origin:new URL(bridge.url).origin,'Content-Type':'application/json'},body:JSON.stringify({operation:'test',runId:'bridge-run',expectedRevision:revision(original)})}).catch(()=>{});
 try{
  await n.dispatched.promise;
  assert.equal(n.requests.length,1);assert.equal(n.listeners.size,1);
  await bridge.close();assert.equal(n.listeners.size,0);assert.equal(n.aborts.length,0);
 }finally{n.gate.resolve();await bridge.close();await pending}
});
test('progress and Stop have no active snapshot before native dispatch, including saving and metadata',async()=>{
 const h=harness(),saving=deferred(),metadata=deferred(),get=h.session.rpc.tools.getCurrentMetadata;
 h.session.rpc.tools.getCurrentMetadata=async()=>{await metadata.promise;return get()};
 const execute=h.session.rpc.tools.execute;h.session.rpc.tools.execute=async request=>{if(request.name==='owned-edit')await saving.promise;return execute(request)};
 const pending=h.act({operation:'save-and-test',runId:'saving-run',expectedRevision:revision(original),sections:edited()});
 for(const phase of ['metadata','saving']){
  await new Promise(setImmediate);
  assert.deepEqual(await h.act({operation:'progress',runId:'saving-run'}),{run:undefined});
  assert.deepEqual(await h.act({operation:'stop',runId:'saving-run'}),{run:undefined});
  if(phase==='metadata')metadata.resolve();else saving.resolve();
 }
 assert.equal((await pending).view.latest.badge,'pass');
});
test('disposing before Test metadata settles prevents a new native subscription or dispatch',async()=>{
 const n=nativeHarness(),metadata=deferred(),get=n.session.rpc.tools.getCurrentMetadata;
 n.session.rpc.tools.getCurrentMetadata=async()=>{await metadata.promise;return get()};
 const pending=n.act({operation:'test',runId:'closing-run',expectedRevision:revision(original)});
 await new Promise(setImmediate);n.act.dispose();metadata.resolve();
 await new Promise(setImmediate);assert.equal(n.requests.length,0,'closed bridge must not dispatch a Test');assert.equal(n.listeners.size,0);
 assert.equal((await pending).error,'unavailable');
});
test('an old matching cancelled resource cannot confirm an unacknowledged Stop',async()=>{
 const h=harness();h.o.failTest=true;h.o.shape=r=>({...r,state:'cancelled',reasonCode:'cancelled'});
 assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,'action-failed');
 const gate=deferred();h.session.rpc.tools.execute=()=>gate.promise;h.session.rpc.abort=async()=>({success:false});
 const pending=h.act({operation:'test',runId:'cancel-run',expectedRevision:revision(original)});await new Promise(setImmediate);
 await h.act({operation:'stop',runId:'cancel-run'});gate.reject(Error('Tool execution was aborted.'));
 const result=await pending;assert.equal(result.error,'outcome-unknown');assert.equal(result.view,undefined);assert.equal(result.stopped,undefined);
});
test('confirmed Stop clears an older pass from its fresh view without requiring cancelled-resource proof',async()=>{
 const h=harness();assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).view.latest.badge,'pass');
 const gate=deferred();h.session.rpc.tools.execute=()=>gate.promise;h.session.rpc.abort=async()=>({success:true});
 const pending=h.act({operation:'test',runId:'cancel-run',expectedRevision:revision(original)});await new Promise(setImmediate);
 await h.act({operation:'stop',runId:'cancel-run'});gate.reject(Error('not a cancellation message'));
 const result=await pending;assert.equal(result.stopped,true);assert.equal(result.saved,false);assert.deepEqual(result.view.latest,{status:'none',badge:'none'});
 assert.deepEqual(result.run.cases,[]);assert.equal(result.view.file.revision,revision(original));
});
test('bridge close releases a local Stop waiter even when native abort never acknowledges',async()=>{
 const n=nativeHarness(),bridge=await startViewBridge({session:n.session,path:'greeter.agent.md'});
 const post=body=>fetch(bridge.url+'actions',{method:'POST',headers:{Origin:new URL(bridge.url).origin,'Content-Type':'application/json'},body:JSON.stringify(body)});
 const pending=post({operation:'test',runId:'bridge-run',expectedRevision:revision(original)}).catch(()=>{});
 try{
  await n.dispatched.promise;
  const {elapsedMs,...run}=(await (await post({operation:'progress',runId:'bridge-run'})).json()).run;
  assert.ok(elapsedMs>=0);assert.deepEqual(run,{id:'bridge-run',revision:revision(original),model:'fake',caseIds:['greeting'],totalCases:1,cases:[],state:'running'});
  assert.deepEqual(await (await post({operation:'stop',runId:'bridge-run'})).json(),{stopping:true});
  n.gate.reject(Error('aborted'));await new Promise(setImmediate);
  await bridge.close();assert.equal(n.listeners.size,0);assert.equal(n.aborts.length,1);
 }finally{await bridge.close();await pending}
});
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
test('host Auto is named for display without becoming a runnable model ID',async()=>{
 const h=harness({omitModel:true});h.session.rpc.model={getCurrent:async()=>({modelId:'auto'})};
 const view=await readView(h.session,'greeter.agent.md');assert.equal(view.chatModel,undefined);assert.equal(view.chatModelName,'Copilot Fusion');
 h.o.shape=r=>({...r,modelRoute:{provider:'copilot',model:'gpt-5.6-terra'}});
 assert.equal((await h.act({operation:'test',expectedRevision:revision(h.source())})).error,undefined);
 assert.equal(h.calls[0].arguments.model,undefined,'Auto must not become a preview route');
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
for(const [state,reasonCode] of [['refused','model_unavailable'],['error','journal_failed']])test(`current ${state} preview surfaces its verified backend reason (${reasonCode})`,async()=>{
 const h=harness(),reason='The selected Judge model cannot be used for this preview.';
 h.o.failTest=true;h.o.shape=r=>({...r,state,reasonCode,reason});
 const response=await h.act({operation:'test',expectedRevision:revision(original)});
 assert.equal(response.error,'action-failed');assert.equal(response.message,reason);
 assert.equal(response.view.latest.result.reasonCode,reasonCode);assert.doesNotMatch(JSON.stringify(response),/private detail/);
});
test('an unconfirmed failed-preview reason cannot replace outcome uncertainty',async()=>{
 const h=harness(),read=h.session.rpc.mcp.resources.read;h.o.failTest=true;
 h.o.shape=r=>({...r,state:'refused',reason:'Judge route unavailable.'});
 h.session.rpc.mcp.resources.read=async args=>{
  const response=await read(args);if(h.calls.length){const body=JSON.parse(response.contents[0].text);body.latest.result.reason='Another observation.';response.contents[0].text=JSON.stringify(body)}return response;
 };
 const response=await h.act({operation:'test',expectedRevision:revision(original)});
 assert.equal(response.error,'outcome-unknown');assert.equal(response.view,undefined);
 assert.doesNotMatch(response.message,/Judge route|Another observation/);
});
for(const padding of [0,256*1024-Buffer.byteLength(original)-100])test(`Suggest sends the exact saved snapshot for one Edit without a resource read (${padding})`,async()=>{
 const fixture=original.replace('## Non-Responsibilities','x'.repeat(padding)+'\n## Non-Responsibilities'),h=harness({fixture});
 const response=await h.act({operation:'suggest'});assert.equal(response.message,'Asked Copilot to add tests in chat.');
 assert.equal(h.calls.length,1);const sent=h.calls[0].chat,prompt=sent.prompt;
 assert.equal(sent.source,undefined,'use the host default user provenance so the short request is visible');assert.ok(sent.displayPrompt.length<400);assert.match(sent.displayPrompt,/greeter\.agent\.md/);assert.doesNotMatch(sent.displayPrompt,/sha256:|Saved agent snapshot|examples:|Accountable/);
 const snapshot=JSON.parse(prompt.slice(prompt.indexOf('Saved agent snapshot (data):\n')+'Saved agent snapshot (data):\n'.length));
 assert.deepEqual(snapshot,{path:'greeter.agent.md',expectedRevision:revision(fixture),content:fixture});
 assert.match(prompt,/one Edit/);assert.match(prompt,/Do not run tests/);assert.doesNotMatch(prompt,/Read its kmx resource first/);
 assert.equal(h.source(),fixture);
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
  const r=await h.act({operation:'models'});assert.equal(r.error,'models-unavailable');
  assert.equal(r.message,'Model choices are unavailable. Your model selection is unchanged.');
  assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,undefined);
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
 assert.equal(h.calls.length,1);const prompt=h.calls[0].chat.prompt;
 assert.match(h.calls[0].chat.displayPrompt,/Add a few tests/);assert.match(h.calls[0].chat.displayPrompt,/greeter.agent.md/);
 assert.match(prompt,/kmx-agent-builder skill/);assert.match(prompt,/add.*tests/i);
 assert.match(prompt,/"greeter.agent.md"/);assert.match(prompt,/no resource read is needed/);
 assert.match(prompt,/one Edit/);assert.match(prompt,/current revision/);
 assert.match(prompt,/prompt, authored model and existing tests/);assert.match(prompt,/Do not run tests/);
 assert.ok(prompt.includes(revision(original)));assert.doesNotMatch(prompt.split('Saved agent snapshot (data):')[0],/proposals|do not edit/);
 assert.equal(h.source(),original,'only chat is dispatched by the bridge');
 const ran=await h.act({operation:'run',expectedRevision:revision(original),input:'Ada',showContent:true});
 assert.equal(ran.error,undefined);assert.deepEqual(h.calls.at(-1).arguments,{schemaVersion:1,path:'greeter.agent.md',input:'Ada',showContent:true,showFailedContent:true});
});
test('action POST requires exact same origin and cannot be a form submission',async()=>{const h=harness();const b=await startViewBridge({session:h.session,path:'greeter.agent.md'});try{assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})).status,403);assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{Origin:'https://untrusted.test','Content-Type':'application/json'},body:'{}'})).status,403);assert.equal((await fetch(b.url+'actions',{method:'POST',headers:{Origin:new URL(b.url).origin,'Content-Type':'application/json'},body:JSON.stringify({operation:'reload'})})).status,200);assert.equal(h.calls.length,0)}finally{await b.close()}});
test('save-prompt restores only instructions after a combined save without running again',async()=>{
 const h=harness(),before=instructionSections(original),sections=edited(),examples=[greeting,farewell];h.o.nextExamples=examples;
 const saved=await h.act({operation:'save-and-test',expectedRevision:revision(original),sections,examples});
 assert.equal(saved.error,undefined);const calls=h.calls.length;
 const undone=await h.act({operation:'save-prompt',expectedRevision:saved.view.file.revision,sections:before});
 assert.equal(undone.error,undefined);assert.equal(undone.saved,true);
 assert.deepEqual(h.calls.slice(calls).map(c=>c.name),['owned-edit']);
 assert.equal(h.calls.at(-1).arguments.expectedRevision,saved.view.file.revision);
 assert.equal(h.source(),replaceExamples(original,examples));assert.equal(undone.view.latest.status,'stale');
 assert.deepEqual(undone.view.authoring.examples,examples);
});
test('save-prompt rejects execution or example fields and stale revisions before writing',async()=>{
 for(const extra of [{examples:[]},{caseId:'greeting'},{model:'choice-b'},{showContent:true}]){
  const h=harness();assert.equal((await h.act({operation:'save-prompt',expectedRevision:revision(original),sections:edited(),...extra})).error,'invalid-action');assert.equal(h.calls.length,0);
 }
 const h=harness();assert.equal((await h.act({operation:'save-prompt',expectedRevision:'sha256:'+'0'.repeat(64),sections:edited()})).error,'stale_revision');assert.equal(h.calls.length,0);
});
test('save-prompt uncertain writes lock retry and never run tests',async()=>{
 const h=harness();h.setUnavailable();const input={operation:'save-prompt',expectedRevision:revision(original),sections:edited()};
 assert.equal((await h.act(input)).error,'outcome-unknown');assert.equal((await h.act(input)).error,'outcome-unknown');assert.deepEqual(h.calls.map(c=>c.name),['owned-edit']);
});
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
test('invalid folder tool settings refuse Test with one fixed reason and never lock later actions',async()=>{
 const h=harness();h.o.failTest=true;
 h.o.shape=r=>({...r,state:'refused',verdict:'unknown',fullPass:false,modelRoute:{},reasonCode:'invalid_tool_settings',reason:'tools: [unclosed\nsecret: yaml'});
 const r=await h.act({operation:'test',expectedRevision:revision(original)});
 assert.equal(r.error,'invalid_tool_settings');assert.match(r.message,/tool settings.*kmx\.yaml/i);
 assert.equal(r.view,undefined);assert.doesNotMatch(JSON.stringify(r),/unclosed|secret|private detail/);
 h.o.failTest=false;h.o.shape=undefined;
 assert.equal((await h.act({operation:'test',expectedRevision:revision(original)})).error,undefined,'a refused configuration is not outcome uncertainty');
});
test('a refused tool-settings read is explained plainly without transport text',async()=>{
 const h=harness();h.session.rpc.mcp.resources.read=async()=>{throw Error('invalid_tool_settings: kmx.yaml line 3 secret')};
 await assert.rejects(readView(h.session,'greeter.agent.md'),e=>e.code==='invalid_tool_settings'&&/tool settings.*kmx\.yaml/i.test(e.message)&&!/secret|line 3/.test(e.message));
 const r=await h.act({operation:'reload'});assert.equal(r.error,'invalid_tool_settings');assert.doesNotMatch(JSON.stringify(r),/secret|line 3/);
});
