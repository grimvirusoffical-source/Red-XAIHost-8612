import test from 'node:test';
import assert from 'node:assert/strict';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {RedXCmsStore} from './redx-cms-store.mjs';
import {processImage} from './site-backend.mjs';
import {seedProbe,verifyProbe,verifyWriterLockReleased,SERVICE_ACTOR,SYNTHETIC_PREFS} from './cms-maintenance-probe.mjs';

const KEY='rx_live_'+'a'.repeat(43),VALUE_PATH='site-cms.red-xai.db:SiteState[ID=1].Payload';
const json=(value,status=200)=>new Response(JSON.stringify(value),{status,headers:{'content-type':'application/json'}});
async function fixture(t,options={}){
  const directory=await fs.mkdtemp(path.join(os.tmpdir(),'cms-maintenance-test-')),keyFile=path.join(directory,'fixture.key');
  await fs.writeFile(keyFile,KEY,{mode:0o600,flag:'wx'});
  const wire={value:null,seq:0,writes:0,missingReadback:false,...options};
  const fetchImpl=async(url,init)=>{
    assert.equal(new URL(url).origin,'http://127.0.0.1:17790');assert.equal(init.headers.authorization,'Bearer '+KEY);
    if(url.endsWith('/v1/ui/whoami'))return json({scope:'write',db:'website_cms_fixture',prefix:'site-cms.red-xai.db',host_admin:false,owner:false,role:'leader'});
    assert.equal(decodeURIComponent(new URL(url).pathname),'/v1/db/website_cms_fixture/path/'+VALUE_PATH);
    if(init.method==='PUT'){wire.value=init.body;wire.seq++;wire.writes++;return json({ok:true,seq:wire.seq});}
    if(wire.value===null||wire.missingReadback&&init.headers['x-redx-wait-seq'])return json({error:'not found'},404);
    return json({path:VALUE_PATH,value:wire.value});
  };
  let active;
  const open=async()=>active=await new RedXCmsStore({directory,keyFile,database:'website_cms_fixture',durability:'group',singleWriter:true,fetchImpl,testMode:true}).init();
  t.after(async()=>{await active?.close();const real=await fs.realpath(directory),parent=await fs.realpath(os.tmpdir());assert.equal(path.dirname(real),parent);assert.ok(path.basename(real).startsWith('cms-maintenance-test-'));await fs.rm(real,{recursive:true,force:false,maxRetries:5,retryDelay:100});});
  return {directory,wire,open,fetchImpl,keyFile};
}

test('real image/service fixture survives store reopen without impersonating accounts',async t=>{
  const f=await fixture(t),store=await f.open(),seed=await seedProbe(store,{processImage});
  assert.deepEqual(Object.keys(SERVICE_ACTOR),['id']);assert.match(SERVICE_ACTOR.id,/^maintenance-service:/);
  assert.equal(seed.normalAccountIsolationVerified,false);assert.equal(seed.websiteOwnerApiVerified,false);assert.equal(seed.backupRestoreVerified,false);
  assert.equal(seed.realWebpDecoded,true);assert.equal(f.wire.writes,4);
  const state=await store.read();assert.deepEqual(state.preferences,SYNTHETIC_PREFS);assert.equal(state.posts[0].status,'draft');assert.equal(state.posts[0].createdBy,SERVICE_ACTOR.id);
  assert.ok(Object.keys(SYNTHETIC_PREFS).every(key=>!/[a-f0-9]{64}/u.test(key)));
  await store.close();assert.equal(await fs.stat(path.join(f.directory,'writer.lock')).then(()=>true,()=>false),false);
  const reopened=await f.open(),verified=await verifyProbe(reopened,{processImage});assert.deepEqual(verified,seed);assert.equal(f.wire.writes,4);
  await reopened.audit('unrelated.maintenance',{id:'another-service'},'nonaccount');
  assert.deepEqual(await verifyProbe(reopened,{processImage}),seed);
});

test('existing native app data is refused before writes or service audit',async t=>{
  const state={schema:1,posts:[],media:{},preferences:{existing_fixture:{theme:'dark'}}};
  const f=await fixture(t,{value:JSON.stringify(JSON.stringify({schema:1,revision:1,state}))}),store=await f.open();
  await assert.rejects(seedProbe(store,{processImage}));assert.equal(f.wire.writes,0);
  assert.equal(await fs.stat(path.join(f.directory,'audit.jsonl')).then(()=>true,()=>false),false);
  assert.deepEqual(await store.read(),state);
});

test('prior receipt and orphan media are preserved and block reseeding',async t=>{
  for(const kind of ['receipt','media','audit']){
    const f=await fixture(t),store=await f.open();
    if(kind==='receipt')await fs.writeFile(path.join(f.directory,'maintenance-probe-v1.json'),'synthetic partial receipt',{mode:0o600});
    if(kind==='media')await fs.writeFile(path.join(f.directory,'media','orphan.webp'),'synthetic orphan',{mode:0o600});
    if(kind==='audit')await store.audit('maintenance.cms_probe.service.begin',SERVICE_ACTOR,'synthetic-service-fixture-v1');
    await assert.rejects(seedProbe(store,{processImage}));assert.equal(f.wire.writes,0);await store.close();
  }
});

test('acknowledged but unreadable write keeps partial evidence and refuses a retry',async t=>{
  const f=await fixture(t,{missingReadback:true}),store=await f.open();
  await assert.rejects(seedProbe(store,{processImage}));assert.equal(f.wire.writes,1);
  assert.equal((await fs.readdir(path.join(f.directory,'media'))).length,1);
  assert.equal(await fs.stat(path.join(f.directory,'maintenance-probe-v1.json')).then(()=>true,()=>false),false);
  await store.close();f.wire.missingReadback=false;const reopened=await f.open();
  await assert.rejects(seedProbe(reopened,{processImage}));assert.equal(f.wire.writes,1);
});

test('tampered media or audit prefix fails verification without native mutation',async t=>{
  for(const kind of ['media','audit']){
    const f=await fixture(t),store=await f.open();await seedProbe(store,{processImage});
    if(kind==='media'){const [name]=await fs.readdir(path.join(f.directory,'media'));await fs.appendFile(path.join(f.directory,'media',name),'changed');}
    else{const file=path.join(f.directory,'audit.jsonl'),bytes=await fs.readFile(file);bytes[10]^=1;await fs.writeFile(file,bytes);}
    await assert.rejects(verifyProbe(store,{processImage}));assert.equal(f.wire.writes,4);await store.close();
  }
});

test('existing writer lock is refused and never removed by a second initializer',async t=>{
  const f=await fixture(t),store=await f.open(),lock=path.join(f.directory,'writer.lock'),before=await fs.readFile(lock);
  const second=new RedXCmsStore({directory:f.directory,keyFile:f.keyFile,database:'website_cms_fixture',durability:'group',singleWriter:true,fetchImpl:f.fetchImpl,testMode:true});
  await assert.rejects(second.init(),e=>e.code==='EEXIST');assert.deepEqual(await fs.readFile(lock),before);assert.equal(f.wire.writes,0);await store.close();
});

test('a suppressed close-unlink failure cannot be reported as a released lock',async t=>{
  const f=await fixture(t),store=await f.open(),lock=path.join(f.directory,'writer.lock'),before=await fs.readFile(lock),unlink=fs.unlink;
  fs.unlink=async(file,...args)=>{if(file===lock)throw Object.assign(Error('simulated permission failure'),{code:'EACCES'});return unlink(file,...args);};
  try{await store.close();await assert.rejects(verifyWriterLockReleased(f.directory));assert.deepEqual(await fs.readFile(lock),before);}
  finally{fs.unlink=unlink;}
  assert.equal(f.wire.writes,0);
});
