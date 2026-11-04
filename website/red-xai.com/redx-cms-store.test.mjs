import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,writeFile,rm,readFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {RedXCmsStore} from './redx-cms-store.mjs';

// Disposable first-party protocol fixtures only; no production listeners/keys.
const ID={id:'fixture-author'};
const KEY='rx_live_'+'a'.repeat(43);
const PREFIX='site-cms.red-xai.db';
const json=(v,status=200)=>new Response(JSON.stringify(v),{status,headers:{'content-type':'application/json'}});
async function fixture(t,overrides={}){
  const directory=await mkdtemp(path.join(os.tmpdir(),'redx-cms-test-'));
  const keyFile=path.join(directory,'fixture.key');await writeFile(keyFile,KEY,{mode:0o600});
  const state={value:null,seq:0,requests:[],key:{scope:'write',db:'website_cms_fixture',prefix:PREFIX,host_admin:false,owner:false,role:'leader'},...overrides};
  const fetchImpl=async(url,init={})=>{
    assert.equal(new URL(url).origin,'http://127.0.0.1:17790');
    assert.equal(init.headers.authorization,'Bearer '+KEY);assert.equal(init.redirect,'manual');
    state.requests.push({url,init});
    if(url.endsWith('/v1/ui/whoami'))return json(state.key);
    assert.equal(decodeURIComponent(new URL(url).pathname),'/v1/db/website_cms_fixture/path/'+PREFIX+':SiteState[ID=1].Payload');
    if(init.method==='PUT'){
      assert.equal(init.headers['content-type'],'text/x-redx-box');
      // GET returns the Box scalar's textual representation, including quotes.
      assert.equal(typeof JSON.parse(init.body),'string');state.value=init.body;state.seq++;
      return json({ok:true,seq:state.seq});
    }
    if(state.missingReadback&&init.headers['x-redx-wait-seq'])return json({error:'not found'},404);
    if(state.value===null)return json({error:'not found'},404);
    return json({path:PREFIX+':SiteState[ID=1].Payload',value:state.value});
  };
  const store=new RedXCmsStore({directory,database:'website_cms_fixture',keyFile,durability:'sync',singleWriter:true,fetchImpl,testMode:true});
  t.after(async()=>{await store.close();await rm(directory,{recursive:true,force:true});});
  return {store,state,directory,keyFile,fetchImpl};
}
test('native Box string round trip, read-your-write sequence and persistent audit',async t=>{
  const {store,state,directory}=await fixture(t);await store.init();
  await store.mutate('preferences.update',ID,'self',next=>{next.preferences.fixture={theme:'dark',locale:'en-US'};});
  assert.equal((await store.read()).preferences.fixture.theme,'dark');
  assert.equal(state.requests.some(r=>r.init.headers['x-redx-wait-seq']==='1'),true);
  assert.equal(state.value.includes(KEY),false);
  const log=await readFile(path.join(directory,'audit.jsonl'),'utf8');
  assert.match(log,/preferences.update.committed/);assert.equal(log.includes(ID.id),false);assert.equal(log.includes(KEY),false);
});
test('rejects an overprivileged or wrongly scoped native key',async t=>{
  for(const bad of [{host_admin:true},{owner:true},{scope:'admin'},{db:'redx_accounts'},{prefix:''},{role:'follower'}]){
    const f=await fixture(t);Object.assign(f.state.key,bad);
    await assert.rejects(f.store.init(),e=>e.code==='cms_scope_mismatch');
  }
});
test('serializes changes and does not lose independently queued writes',async t=>{
  const {store}=await fixture(t);await store.init();
  await Promise.all(Array.from({length:5},(_,i)=>store.mutate('preferences.update',ID,'self',next=>{next.preferences['fixture'+i]={theme:'dark'};})));
  assert.equal(Object.keys((await store.read()).preferences).length,5);assert.equal(store.envelope.revision,5);
});
test('refuses external writer conflict and loss of an existing CMS record',async t=>{
  const {store,state}=await fixture(t);await store.init();
  await store.mutate('preferences.update',ID,'self',next=>{next.preferences.fixture={theme:'dark'};});
  const external=JSON.parse(JSON.parse(state.value));external.revision++;
  state.value=JSON.stringify(JSON.stringify(external));
  await assert.rejects(store.mutate('preferences.update',ID,'self',()=>{}),e=>e.code==='cms_writer_conflict');
  state.value=null;await assert.rejects(store.read(),e=>e.code==='cms_storage_missing');
});
test('does not report success when acknowledged data cannot be read back',async t=>{
  const {store,state}=await fixture(t,{missingReadback:true});await store.init();
  await assert.rejects(store.mutate('preferences.update',ID,'self',next=>{next.preferences.fixture={theme:'dark'};}),e=>e.code==='cms_storage_missing');
  assert.equal(store.envelope.revision,0);assert.equal(state.seq,1);
});
test('requires private dedicated native storage and an explicit single writer',()=>{
  const base={directory:os.tmpdir(),database:'website_cms_fixture',keyFile:path.resolve('fixture.key'),durability:'sync',singleWriter:true};
  for(const bad of [{database:'redx_accounts'},{database:'default'},{keyFile:'relative.key'},{durability:'async'},{singleWriter:false}])assert.throws(()=>new RedXCmsStore({...base,...bad}));
});
test('writer lock prevents another gateway from taking the same local namespace',async t=>{
  const f=await fixture(t);await f.store.init();
  const other=new RedXCmsStore({directory:f.directory,database:'website_cms_fixture',keyFile:f.keyFile,durability:'sync',singleWriter:true,fetchImpl:f.fetchImpl,testMode:true});
  await assert.rejects(other.init(),e=>e.code==='EEXIST');
});
