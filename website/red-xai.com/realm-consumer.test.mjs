import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createSiteHandler,startServer} from './server.mjs';
import {PrivateStore,accountKey} from './site-backend.mjs';
import {LEGACY_ISSUER,FRESH_ISSUER,FRESH_COOKIE,REALM_AUTHORITY,realmIdentity} from './identity-realm.mjs';
import {identityKey} from './site/session.js';

test('realm prototype cannot be enabled through the public runtime environment',async()=>{
  await assert.rejects(()=>startServer({REDX_WEBSITE_SESSION_REALMS:'prototype-v1'}),/not approved for runtime activation/u);
  await assert.rejects(()=>createSiteHandler({realmPrototype:true}),/isolated fixture/u);
});

test('CMS owner is legacy-only; fresh same-subject/email preferences stay separate and CSRF/revocation apply',async t=>{
  const directory=await fs.mkdtemp(path.join(os.tmpdir(),'redx-realm-cms-'));
  const store=await new PrivateStore(directory,{testMode:true}).init();
  let site;const server=http.createServer((req,res)=>site.handler(req,res));
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  const origin='http://127.0.0.1:'+server.address().port;let revoked=false;
  t.after(async()=>{server.closeAllConnections();await new Promise(resolve=>server.close(resolve));await fs.rm(directory,{recursive:true,force:true});});
  const fetchImpl=async(url,init)=>{
    assert.equal(url,REALM_AUTHORITY);assert.deepEqual(Object.keys(init.headers).sort(),['accept','cookie']);
    const c=init.headers.cookie;if(c.includes(';'))return new Response(null,{status:409});
    const legacy=c.startsWith('redx_session='),sub=c.endsWith('=b')?'other':c.endsWith('=collision')?realmIdentity(FRESH_ISSUER,'shared').id:'shared';
    if(!legacy&&revoked)return new Response(null,{status:401});
    const issuer=legacy?LEGACY_ISSUER:FRESH_ISSUER,p=realmIdentity(issuer,sub);
    return Response.json({identityRealm:{version:1,issuer,subject:sub,canonicalId:p.id},account:{id:p.id,email:'same@example.invalid',roles:legacy?['owner']:['user'],emailVerified:true,active:true},active:true,amr:legacy?['pwd','call']:['pwd'],expiresMs:Date.now()+60000});
  };
  site=await createSiteHandler({testMode:true,development:true,publicOrigin:origin,store,fetchImpl,realmPrototype:true,ownerIds:new Set(['shared',realmIdentity(FRESH_ISSUER,'shared').id])});
  const call=async(route,cookie,method='GET',body,source=origin)=>{
    const r=await fetch(origin+route,{method,headers:{cookie,...(source?{origin:source}:{}),'content-type':'application/json','x-redx-realm':LEGACY_ISSUER,'x-redx-role':'owner'},body:body?JSON.stringify(body):undefined});
    return {status:r.status,json:await r.json()};
  };
  assert.equal((await call('/api/site/session','redx_session=owner')).json.owner,true);
  assert.equal((await call('/api/site/session',`${FRESH_COOKIE}=a`)).json.owner,false);
  const dtoLegacy=(await call('/api/site/session','redx_session=collision')).json;
  const dtoFresh=(await call('/api/site/session',`${FRESH_COOKIE}=a`)).json;
  assert.equal(dtoLegacy.account.id,dtoFresh.account.id);
  assert.notEqual(identityKey({...dtoLegacy,owner:false}),identityKey(dtoFresh));
  for(const c of [`${FRESH_COOKIE}=a`,`${FRESH_COOKIE}=b`])assert.equal((await call('/api/site/posts?scope=owner',c)).status,403);
  assert.equal((await call('/api/site/preferences','redx_session=owner','PATCH',{theme:'light'})).status,200);
  assert.equal((await call('/api/site/preferences',`${FRESH_COOKIE}=a`,'PATCH',{theme:'dark'})).status,200);
  assert.equal((await call('/api/site/preferences',`${FRESH_COOKIE}=b`,'PATCH',{theme:'light'})).status,200);
  assert.equal((await call('/api/site/preferences','redx_session=collision','PATCH',{theme:'system'})).status,200);
  assert.equal((await call('/api/site/preferences','redx_session=owner')).json.preferences.theme,'light');
  assert.equal((await call('/api/site/preferences',`${FRESH_COOKIE}=a`)).json.preferences.theme,'dark');
  assert.equal((await call('/api/site/preferences','redx_session=collision')).json.preferences.theme,'system');
  const audit=(await fs.readFile(path.join(directory,'audit.jsonl'),'utf8')).trim().split('\n').map(x=>JSON.parse(x));
  assert.deepEqual(audit.filter(x=>x.event==='preferences.update.committed').map(x=>x.actor),[
    accountKey('shared'),accountKey(realmIdentity(FRESH_ISSUER,'shared').ownershipKey),accountKey(realmIdentity(FRESH_ISSUER,'other').ownershipKey),accountKey(dtoLegacy.account.id),
  ]);
  const persisted=(await new PrivateStore(directory,{testMode:true}).init()).snapshot().preferences;
  assert.equal(persisted[accountKey(dtoLegacy.account.id)].theme,'system');
  assert.equal(persisted[accountKey(realmIdentity(FRESH_ISSUER,'shared').ownershipKey)].theme,'dark');
  assert.equal((await call('/api/site/preferences',`${FRESH_COOKIE}=a`,'PATCH',{theme:'light'},'https://attacker.invalid')).status,403);
  assert.equal((await call('/api/site/session',`redx_session=owner; ${FRESH_COOKIE}=a`)).json.signedIn,false);
  revoked=true;assert.equal((await call('/api/site/session',`${FRESH_COOKIE}=a`)).json.signedIn,false);
});
