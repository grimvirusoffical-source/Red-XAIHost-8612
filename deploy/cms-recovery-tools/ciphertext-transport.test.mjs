import test from 'node:test';
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {validateCapsule,stageOnce,CAPSULE_PATH,withMaintenanceLock} from './ciphertext-transport.mjs';
const bytes=Buffer.from('age-encryption.org/v1\n'+'synthetic-fixture-ciphertext-only'.repeat(8));
const input=()=>({kind:'cms-pair-age-ciphertext-v1',exclusiveWriterReviewed:true,sha256:createHash('sha256').update(bytes).digest('hex'),pairSha256:'a'.repeat(64),data:bytes.toString('base64')});
const key='rx_live_'+'a'.repeat(43);
function fixture({existing=false,badScope=false,changeState=false,badReadback=false}={}){
  let stored,stateReads=0;const methods=[];
  return {methods,call:async(url,options)=>{
    assert.equal(options.redirect,'manual');assert.equal(options.headers.authorization,'Bearer '+key);methods.push(options.method||'GET');
    if(url.endsWith('/v1/ui/whoami'))return Response.json({scope:'write',db:badScope?'foreign':'website_cms',prefix:'site-cms.red-xai.db',host_admin:false,owner:false,role:'leader'});
    const p=decodeURIComponent(url.split('/path/')[1]);
    if(p.includes(':SiteState'))return new Response(changeState&&stateReads++?'changed':'unchanged');
    assert.equal(p,CAPSULE_PATH);
    if(options.method==='PUT'){stored=options.body;return Response.json({ok:true,seq:2});}
    if(existing)return Response.json({path:p,value:'existing'});
    if(!stored)return new Response('',{status:404});
    return Response.json({path:p,value:badReadback?'"invalid"':stored});
  }};
}
test('ciphertext validation rejects plaintext, excess size, wrong hash and missing writer attestation',()=>{
  assert.equal(validateCapsule(input()).bytes,bytes.length);
  for(const patch of [{exclusiveWriterReviewed:false},{data:Buffer.from('plaintext data'.repeat(30)).toString('base64')},{sha256:'b'.repeat(64)},{data:'a'.repeat(1400001)},{data:input().data+'!'}])assert.throws(()=>validateCapsule({...input(),...patch}));
});
test('one-off stage retains only encrypted record and leaves SiteState unchanged',async()=>{
  const f=fixture(),r=await stageOnce(input(),{key,fetchImpl:f.call});assert.equal(r.cmsStateUnchanged,true);assert.equal(r.offServerVerified,false);assert.equal(f.methods.filter(x=>x==='PUT').length,1);
});
test('scope mismatch or existing record refuses before writes; no overwrite retry',async()=>{
  for(const options of [{badScope:true},{existing:true}]){const f=fixture(options);await assert.rejects(stageOnce(input(),{key,fetchImpl:f.call}));assert.ok(!f.methods.includes('PUT'));}
});
test('changed state or bad ciphertext readback fails after preserving evidence',async()=>{
  for(const options of [{changeState:true},{badReadback:true}]){const f=fixture(options);await assert.rejects(stageOnce(input(),{key,fetchImpl:f.call}));assert.equal(f.methods.filter(x=>x==='PUT').length,1);}
});
test('maintenance lock excludes a second writer and remains after incomplete work',async t=>{
  const root=await fs.mkdtemp(path.join(os.tmpdir(),'cms-transport-lock-'));
  t.after(async()=>{assert.equal(path.dirname(await fs.realpath(root)),await fs.realpath(os.tmpdir()));assert.ok(path.basename(root).startsWith('cms-transport-lock-'));await fs.rm(root,{recursive:true,force:false});});
  await withMaintenanceLock(root,async()=>{await assert.rejects(withMaintenanceLock(root,async()=>assert.fail('second writer ran')));});
  await assert.rejects(fs.lstat(path.join(root,'writer.lock')),{code:'ENOENT'});
  await assert.rejects(withMaintenanceLock(root,async()=>{throw Error('synthetic operation failed');}));
  assert.ok((await fs.lstat(path.join(root,'writer.lock'))).isFile());
  await assert.rejects(withMaintenanceLock(root,async()=>assert.fail('retained lock ignored')));
});
