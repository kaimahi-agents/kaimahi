import assert from 'node:assert/strict';
import test from 'node:test';
import * as catalog from '../../copilot-wsl/com.github.copilot/extensions/agent-view/view.mjs';

const tool = (server, name, over = {}) => ({name:server+'-'+name, mcpServerName:server, mcpToolName:name, description:'Read '+name, input_schema:{type:'object'}, ...over});
function session(metadata, inventory = {}) {
  return {rpc:{tools:{getCurrentMetadata:async()=>({tools:metadata})},mcp:{
    listConfigured:async()=>({servers:[{name:'github',displayName:'GitHub',enabled:true,live:{status:'connected',toolsLoaded:true}},{name:'other',enabled:true,live:{status:'connected'}}]}),
    listTools:async ({serverName})=>({tools:inventory[serverName] ?? metadata.filter(t=>t.mcpServerName===serverName).map(t=>({name:t.mcpToolName,description:t.description}))}),
  }}};
}
test('catalog counts complete connected inventory but only offers runtime-resolvable names', async()=>{
  assert.equal(typeof catalog.readToolCatalog,'function');
  const s=session([tool('github','search'),tool('kmx-agent-builder','edit'),tool('kmx-agent-preview-test','test')],{github:[{name:'search',description:'Find pull requests'},{name:'deferred',description:'Not currently offered'}]});
  const answer=await catalog.readToolCatalog(s,['kmx-agent-builder','kmx-agent-preview-test']);
  assert.deepEqual(answer,{servers:[{name:'github',label:'GitHub',tools:[{name:'search',description:'Find pull requests',selectable:true},{name:'deferred',description:'Not currently offered',selectable:false}]}]});
});
test('catalog preserves raw ambiguous names without making either server selectable', async()=>{
  const answer=await catalog.readToolCatalog(session([tool('github','ping'),tool('other','ping'),tool('github','invalid.name'),tool('github','no_schema',{input_schema:undefined})]),[]);
  assert.equal(answer.servers.length,2);
  assert.ok(answer.servers.flatMap(s=>s.tools).every(t=>!t.selectable));
});
test('catalog never exposes private host errors or schemas and rejects oversized/malformed inventories', async()=>{
  for(const metadata of [null,Array(5000).fill(tool('github','ping'))]) {
    await assert.rejects(()=>catalog.readToolCatalog(session(metadata),[]),{message:'Tools are unavailable in this session.'});
  }
  const s=session([tool('github','ping')]);
  s.rpc.mcp.listTools=async()=>{throw Error('PRIVATE endpoint token')};
  await assert.rejects(()=>catalog.readToolCatalog(s,[]),{message:'Tools are unavailable in this session.'});
});
test('catalog does not start servers or expose configured but invisible tool-less sources', async()=>{
  const s=session([]); s.rpc.mcp.listTools=async()=>assert.fail('no visible server to list');
  assert.deepEqual(await catalog.readToolCatalog(s,[]),{servers:[]});
});
