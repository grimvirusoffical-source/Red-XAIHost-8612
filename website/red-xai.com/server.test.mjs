import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {promises as fs} from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath} from 'node:url';
import {createSiteHandler,SITE_FILES,startServer} from './server.mjs';
import {AUTHORITY,PrivateStore,accountKey} from './site-backend.mjs';

const HERE=path.dirname(fileURLToPath(import.meta.url));
const document=(text='Fixture content')=>({type:'doc',children:[{type:'paragraph',children:[{type:'text',text}]}]});
async function temporary(t){
  const prefix=path.join(os.tmpdir(),'redx-site-fixture-'),directory=await fs.mkdtemp(prefix);
  t.after(async()=>{assert.ok(path.resolve(directory).startsWith(path.resolve(prefix)));await fs.rm(directory,{recursive:true,force:true});});
  return directory;
}
async function fixture(t,options={}){
  const directory=await temporary(t),staticDirectory=path.join(directory,'public');
  for(const name of ['index.html',...SITE_FILES]){const target=path.join(staticDirectory,name);await fs.mkdir(path.dirname(target),{recursive:true});await fs.copyFile(path.join(HERE,name),target);}
  const store=await new PrivateStore(path.join(directory,'private'),{testMode:true}).init();
  const revoked=new Set(),authorityCalls=[];
  const accounts={
    A:{id:'fixture-A',email:'fixture-a@example.test',username:'fixture-A',roles:['user']},
    B:{id:'fixture-B',email:'fixture-b@example.test',username:'fixture-B',roles:['user']},
    owner:{id:'fixture-owner',email:'fixture-owner@example.test',username:'fixture-owner',roles:['owner']},
    claimed:{id:'fixture-claimed',email:'fixture-owner@example.test',username:'fixture-owner',roles:['owner']},
  };
  let unavailable=false,site;
  const server=http.createServer((req,res)=>site.handler(req,res));
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  t.after(async()=>{server.closeAllConnections();await new Promise(resolve=>server.close(resolve));});
  const origin='http://127.0.0.1:'+server.address().port;
  const fetchImpl=async(url,init)=>{
    assert.equal(url,AUTHORITY,'fixtures must never contact configurable or external authentication destinations');authorityCalls.push(init);
    if(unavailable)throw Error('fixture outage');
    const token=init.headers.cookie.slice('redx_session='.length),account=accounts[token];
    if(!account||revoked.has(token))return Response.json({},{status:401});
    return Response.json({account,expiresMs:Date.now()+60000,amr:token==='owner'?['pwd','totp']:['pwd']});
  };
  site=await createSiteHandler({testMode:true,development:true,publicOrigin:origin,staticDirectory,store,storeMode:'development-json',fetchImpl,ownerIds:new Set(['fixture-owner']),imagesAvailable:true,
    imageProcessor:async()=>({bytes:Buffer.from('RIFFfixture-WEBP'),width:2,height:2}),...options});
  const send=async(route,{token,method='GET',json,body,headers={},sameOrigin=true}={})=>{
    const r=await fetch(origin+route,{method,headers:{...(token?{cookie:'redx_session='+token}:{}),...(method!=='GET'&&method!=='HEAD'&&sameOrigin?{origin}:{}),...(json!==undefined?{'content-type':'application/json'}:{}),...headers},body:json===undefined?body:JSON.stringify(json),redirect:'manual'});
    const text=await r.text();let data;try{data=JSON.parse(text);}catch{}
    return {status:r.status,headers:r.headers,text,data};
  };
  return {origin,send,store,staticDirectory,accounts,revoked,authorityCalls,outage:()=>{unavailable=true;}};
}

test('production entry forbids JSON storage and test handlers require explicit loopback fixture authorities',async()=>{
  await assert.rejects(()=>startServer({REDX_SITE_MODE:'development',REDX_SITE_DEV_STATE_DIR:'not-used'}),/forbids JSON/u);
  await assert.rejects(()=>startServer({REDX_SITE_DEV_STATE_DIR:'not-used'}),/forbids JSON/u);
  await assert.rejects(()=>createSiteHandler({store:{snapshot(){}}}),/native RedX/u);
  await assert.rejects(()=>createSiteHandler({testMode:true,development:true,publicOrigin:'https://red-xai.com',fetchImpl:async()=>{}}),/loopback fixture/u);
  await assert.rejects(()=>createSiteHandler({testMode:true,development:true,publicOrigin:'http://127.0.0.1:12345'}),/loopback fixture/u);
  await assert.rejects(()=>new PrivateStore('not-used').init(),/explicit local fixture/u);
});

test('sessions use canonical identities, isolate users, deny claimed owners and promptly revoke expired authority sessions',async t=>{
  const f=await fixture(t);
  assert.deepEqual((await f.send('/api/site/session')).data.signedIn,false);
  assert.equal(f.authorityCalls.length,0);
  const [a,b,owner,claimed]=await Promise.all(['A','B','owner','claimed'].map(token=>f.send('/api/site/session',{token})));
  assert.equal(a.data.account.id,'fixture-A');assert.equal(b.data.account.id,'fixture-B');assert.equal(a.data.owner,false);assert.equal(owner.data.owner,true);assert.equal(claimed.data.owner,false);
  assert.equal(a.headers.get('cache-control'),'no-store');assert.equal(a.headers.get('vary'),'Cookie');
  assert.equal((await f.send('/api/site/session',{headers:{authorization:'Bearer fixture-token','x-redx-account-id':'fixture-owner'}})).data.signedIn,false);
  assert.equal((await f.send('/api/site/session',{headers:{cookie:'redx_session=A; redx_session=B'}})).data.signedIn,false);
  f.revoked.add('A');assert.equal((await f.send('/api/site/session',{token:'A'})).data.signedIn,false);assert.equal((await f.send('/api/site/session',{token:'B'})).data.account.id,'fixture-B');
  f.outage();const down=await f.send('/api/site/session',{token:'B'});assert.equal(down.status,503);assert.equal(down.data.error,'authority_unavailable');assert.equal(down.data.account,undefined);
});

test('preferences are scoped to canonical account ID and mutations require exact Origin',async t=>{
  const f=await fixture(t);
  for(const request of [{token:'A',sameOrigin:false},{token:'A',headers:{origin:'https://invalid.example'}},{token:'A',headers:{'sec-fetch-site':'cross-site'}}])assert.equal((await f.send('/api/site/preferences',{...request,method:'PATCH',json:{theme:'light'}})).status,403);
  assert.equal((await f.send('/api/site/preferences',{method:'PATCH',json:{theme:'light'}})).status,401);
  const [a,b]=await Promise.all([f.send('/api/site/preferences',{token:'A',method:'PATCH',json:{theme:'light',locale:'en-GB'}}),f.send('/api/site/preferences',{token:'B',method:'PATCH',json:{theme:'dark',locale:'en-US'}})]);
  assert.equal(a.status,200);assert.equal(b.status,200);
  assert.equal((await f.send('/api/site/preferences',{token:'A'})).data.preferences.theme,'light');assert.equal((await f.send('/api/site/preferences',{token:'B'})).data.preferences.theme,'dark');
  assert.equal((await f.send('/api/site/preferences',{token:'A',method:'PATCH',json:{accountId:'fixture-B'}})).status,400);
  const state=f.store.snapshot();assert.deepEqual(Object.keys(state.preferences).sort(),[accountKey('fixture-A'),accountKey('fixture-B')].sort());
  assert.equal((await f.send('/api/site/preferences',{token:'A',method:'PATCH',body:'{}',headers:{'content-type':'application/json-extra'}})).status,415);
  assert.equal((await f.send('/api/site/preferences',{token:'A',method:'PATCH',body:'{',headers:{'content-type':'application/json'}})).status,400);
});

test('only the verified owner can manage drafts, public posts omit private identities, and access is audited',async t=>{
  const f=await fixture(t),post={title:'Fixture announcement',category:'announcements',status:'draft',body:document()};
  for(const token of [undefined,'A','B','claimed'])assert.equal((await f.send('/api/site/posts',{token,method:'POST',json:post})).status,token?403:401);
  assert.equal((await f.send('/api/site/posts',{token:'owner',method:'POST',sameOrigin:false,json:post})).status,403);
  const created=await f.send('/api/site/posts',{token:'owner',method:'POST',json:post});assert.equal(created.status,201);const id=created.data.post.id;
  assert.equal((await f.send('/api/site/posts')).data.posts.length,0);
  assert.equal((await f.send('/api/site/posts?scope=owner',{token:'A'})).status,403);
  assert.equal((await f.send('/api/site/posts?scope=owner',{token:'owner'})).data.posts.length,1);
  assert.equal((await f.send('/api/site/posts/'+id,{token:'B',method:'PATCH',json:{status:'published'}})).status,403);
  assert.equal((await f.send('/api/site/posts/'+id,{token:'owner',method:'PATCH',json:{status:'published'}})).status,200);
  const publicPost=(await f.send('/api/site/posts')).data.posts[0];assert.equal(publicPost.id,id);assert.equal(publicPost.createdBy,undefined);assert.doesNotMatch(JSON.stringify(publicPost),/fixture-owner@example/u);
  assert.equal((await f.send('/api/site/posts/'+id,{token:'owner',method:'DELETE'})).status,200);assert.equal((await f.send('/api/site/posts')).data.posts.length,0);
  const audit=await fs.readFile(path.join(f.store.directory,'audit.jsonl'),'utf8');assert.match(audit,/post.create.committed/u);assert.match(audit,/posts.read/u);assert.match(audit,new RegExp(accountKey('fixture-owner'),'u'));assert.doesNotMatch(audit,/fixture-owner@example|"actor":"fixture-owner"/u);
});

test('maintenance drafts can omit a schedule but publishing requires a valid start without changing existing posts on failure',async t=>{
  const f=await fixture(t),post={title:'Fixture maintenance',category:'maintenance',status:'draft',body:document()};
  const draft=await f.send('/api/site/posts',{token:'owner',method:'POST',json:post});
  assert.equal(draft.status,201);assert.equal(draft.data.post.startsAt,null);
  const route='/api/site/posts/'+draft.data.post.id;
  for(const startsAt of [undefined,null,'','not-a-date']){
    const result=await f.send(route,{token:'owner',method:'PATCH',json:{status:'published',...(startsAt===undefined?{}:{startsAt})}});
    assert.equal(result.status,400);assert.equal(result.data.error,'invalid_schedule');
    assert.equal((await f.send('/api/site/posts?scope=owner',{token:'owner'})).data.posts[0].status,'draft');
    assert.equal((await f.send('/api/site/posts')).data.posts.length,0);
  }
  const startsAt='2030-01-02T10:00:00-05:00',endsAt='2030-01-02T11:00:00-05:00';
  assert.equal((await f.send(route,{token:'B',method:'PATCH',json:{status:'published',startsAt}})).status,403);
  const published=await f.send(route,{token:'owner',method:'PATCH',json:{status:'published',startsAt,endsAt}});
  assert.equal(published.status,200);assert.equal(published.data.post.startsAt,'2030-01-02T15:00:00.000Z');
  const edited=await f.send(route,{token:'owner',method:'PATCH',json:{title:'Updated maintenance'}});
  assert.equal(edited.status,200);assert.equal(edited.data.post.startsAt,published.data.post.startsAt);
  assert.equal(edited.data.post.endsAt,published.data.post.endsAt);
  assert.equal(edited.data.post.publishedAt,published.data.post.publishedAt);
  assert.equal((await f.send(route,{token:'owner',method:'PATCH',json:{startsAt:null}})).status,400);
  assert.equal((await f.send(route,{token:'owner',method:'PATCH',json:{endsAt:'2030-01-02T14:00:00Z'}})).status,400);
  assert.equal((await f.send('/api/site/posts')).data.posts[0].startsAt,published.data.post.startsAt);
  assert.equal((await f.send('/api/site/posts',{token:'owner',method:'POST',json:{...post,status:'published'}})).status,400);
  assert.equal((await f.send('/api/site/posts',{token:'owner',method:'POST',json:{...post,category:'updates',status:'published'}})).status,201);
});

test('explicit schedule clearing persists while omitted dates remain unchanged',async t=>{
  const f=await fixture(t),post={title:'Fixture schedule clearing',category:'maintenance',status:'draft',body:document(),startsAt:'2030-01-02T15:00:00Z',endsAt:'2030-01-02T16:00:00Z'};
  for(const clear of [null,'']){
    const draft=await f.send('/api/site/posts',{token:'owner',method:'POST',json:post});assert.equal(draft.status,201);
    const route='/api/site/posts/'+draft.data.post.id;
    const start=await f.send(route,{token:'owner',method:'PATCH',json:{startsAt:clear}});
    assert.equal(start.status,200);assert.equal(start.data.post.startsAt,null);assert.equal(start.data.post.endsAt,'2030-01-02T16:00:00.000Z');
    const end=await f.send(route,{token:'owner',method:'PATCH',json:{endsAt:clear}});
    assert.equal(end.status,200);assert.equal(end.data.post.startsAt,null);assert.equal(end.data.post.endsAt,null);
    const reloaded=await new PrivateStore(f.store.directory,{testMode:true}).init(),stored=reloaded.snapshot().posts.find(p=>p.id===draft.data.post.id);
    assert.equal(stored.startsAt,null);assert.equal(stored.endsAt,null);
  }
  const created=await f.send('/api/site/posts',{token:'owner',method:'POST',json:{...post,status:'published'}}),route='/api/site/posts/'+created.data.post.id;
  assert.equal(created.status,201);
  const cleared=await f.send(route,{token:'owner',method:'PATCH',json:{startsAt:'2030-01-03T15:00:00Z',endsAt:null}});
  assert.equal(cleared.status,200);assert.equal(cleared.data.post.startsAt,'2030-01-03T15:00:00.000Z');assert.equal(cleared.data.post.endsAt,null);
  assert.equal((await f.send(route,{token:'owner',method:'PATCH',json:{endsAt:'2030-01-03T16:00:00Z'}})).status,200);
  const blanked=await f.send(route,{token:'owner',method:'PATCH',json:{endsAt:''}});assert.equal(blanked.status,200);assert.equal(blanked.data.post.endsAt,null);
  const reloaded=await new PrivateStore(f.store.directory,{testMode:true}).init(),stored=reloaded.snapshot().posts.find(p=>p.id===created.data.post.id);
  assert.equal(stored.startsAt,'2030-01-03T15:00:00.000Z');assert.equal(stored.endsAt,null);
});

test('legacy unscheduled maintenance remains readable and permits unrelated writes until it is edited',async t=>{
  const f=await fixture(t),stamp='2026-01-01T00:00:00.000Z';
  const body=document();body.children[0].children[0].marks=[];
  const legacy={id:'p_'+'a'.repeat(32),title:'Legacy maintenance',category:'maintenance',status:'published',body,startsAt:null,endsAt:null,createdAt:stamp,updatedAt:stamp,publishedAt:stamp,createdBy:accountKey('fixture-owner')};
  await f.store.mutate('fixture.legacy',{id:'fixture-owner'},legacy.id,next=>{next.posts.push(legacy);});
  const reloaded=await new PrivateStore(f.store.directory,{testMode:true}).init();
  assert.deepEqual(reloaded.snapshot().posts,[legacy]);
  assert.equal((await f.send('/api/site/posts')).data.posts[0].id,legacy.id);
  assert.equal((await f.send('/api/site/preferences',{token:'A',method:'PATCH',json:{theme:'light'}})).status,200);
  const route='/api/site/posts/'+legacy.id;
  const denied=await f.send(route,{token:'owner',method:'PATCH',json:{title:'Missing schedule'}});
  assert.equal(denied.status,400);assert.equal(denied.data.error,'invalid_schedule');
  assert.deepEqual(f.store.snapshot().posts,[legacy]);
  const scheduled=await f.send(route,{token:'owner',method:'PATCH',json:{startsAt:'2030-01-02T15:00:00Z'}});
  assert.equal(scheduled.status,200);assert.equal(scheduled.data.post.startsAt,'2030-01-02T15:00:00.000Z');
  assert.equal(scheduled.data.post.publishedAt,stamp);
});

test('unpublished images are owner-only; publishing and deletion change public media access without losing metadata',async t=>{
  const f=await fixture(t);
  const upload={method:'POST',body:Buffer.from('synthetic-image'),headers:{'content-type':'image/png'}};
  assert.equal((await f.send('/api/site/media',{...upload,token:'A'})).status,403);
  const image=await f.send('/api/site/media',{...upload,token:'owner'});assert.equal(image.status,201);const id=image.data.media.id,mediaPath='/api/site/media/'+id;
  assert.equal((await f.send(mediaPath)).status,401);assert.equal((await f.send(mediaPath,{token:'B'})).status,403);assert.equal((await f.send(mediaPath,{token:'owner'})).status,200);
  assert.equal((await f.send(mediaPath,{token:'owner',method:'HEAD'})).text,'');
  const post=await f.send('/api/site/posts',{token:'owner',method:'POST',json:{title:'Fixture image',category:'updates',status:'published',body:{type:'doc',children:[{type:'image',mediaId:id,alt:'Fixture'}]}}});assert.equal(post.status,201);
  const publicImage=await f.send(mediaPath);assert.equal(publicImage.status,200);assert.equal(publicImage.headers.get('content-type'),'image/webp');assert.equal(publicImage.headers.get('x-content-type-options'),'nosniff');
  await f.send('/api/site/posts/'+post.data.post.id,{token:'owner',method:'DELETE'});
  assert.equal((await f.send(mediaPath)).status,401);assert.ok(f.store.snapshot().media[id]);
  const audit=await fs.readFile(path.join(f.store.directory,'audit.jsonl'),'utf8');assert.match(audit,/media.read/u);
});

test('unreviewed billing URLs never activate subscriptions or checkout and cannot receive cookies',async t=>{
  const f=await fixture(t,{commerceCatalogUrl:'http://127.0.0.1:9/catalog',commerceCheckoutUrl:'https://invalid.example/checkout'});
  const config=await f.send('/api/site/config');assert.equal(config.data.capabilities.subscriptions,false);
  assert.equal((await f.send('/api/site/subscriptions')).status,503);
  assert.equal((await f.send('/api/site/checkout',{token:'A',method:'POST',json:{productId:'fixture-product'}})).data.error,'subscriptions_not_configured');
  assert.equal(f.authorityCalls.length,1);
  for(const path of ['/api/site/account/delete','/api/site/verification/resend']){const r=await f.send(path,{token:'A',method:'POST',json:{}});assert.equal(r.status,503);assert.equal(r.data.ok,undefined);}
});

test('every versioned frontend module is served coherently, required assets fail readiness closed, and source files remain private',async t=>{
  const f=await fixture(t);
  const html=await f.send('/');assert.equal(html.status,200);const match=/\/site-assets\/([a-f0-9]{64})\/app\.js/u.exec(html.text);assert.ok(match);
  const prefix='/site-assets/'+match[1]+'/';
  for(const name of SITE_FILES){const r=await f.send(prefix+name);assert.equal(r.status,200,name);assert.ok(r.text.length);if(name.endsWith('.js'))assert.match(r.headers.get('content-type'),/javascript/u);}
  for(const value of ['/server.mjs','/site-backend.mjs','/gateway-proxy.mjs','/private/state.json','/site-assets/invalid/app.js',prefix+'server.mjs'])assert.equal((await f.send(value)).status,404);
  assert.equal((await f.send('/',{method:'HEAD'})).text,'');
  const module=path.join(f.staticDirectory,'site','pages.js'),original=await fs.readFile(module);await fs.unlink(module);
  for(const route of ['/api/site/health','/',prefix+'app.js']){const r=await f.send(route);assert.equal(r.status,503);assert.equal(r.data.error,'site_assets_unavailable');}
  await assert.rejects(()=>createSiteHandler({staticDirectory:f.staticDirectory}),/site_assets_unavailable/u);
  await fs.writeFile(module,original);assert.equal((await f.send('/api/site/health')).status,200);
  const catalog=path.join(f.staticDirectory,'catalog.json'),catalogBytes=await fs.readFile(catalog);await fs.writeFile(catalog,'{');
  assert.equal((await f.send('/api/site/health')).status,503);await fs.writeFile(catalog,catalogBytes);
  await fs.appendFile(module,"\nimport './not-deployed.js';\n");
  assert.equal((await f.send('/')).status,503);await fs.writeFile(module,original);
});

test('new frontend routes never replace primary signup, activation, terms or either Quick Login contract',async t=>{
  const proxied=[];
  const f=await fixture(t,{proxy:async(req,res,target)=>{const chunks=[];for await(const chunk of req)chunks.push(chunk);proxied.push({target,url:req.url,method:req.method,body:Buffer.concat(chunks).toString()});res.writeHead(200,{'content-type':'application/json'});res.end('{"existingAuthority":true}');}});
  for(const route of ['/signup','/activate?token=fixture','/tos','/redxai-connect','/redxai-connectx','/host?embed=site','/v1/console/?embed=site'])assert.equal((await f.send(route)).data.existingAuthority,true);
  const input={username:'synthetic-fixture',terms:true};assert.equal((await f.send('/api/accounts/signup',{method:'POST',json:input})).data.existingAuthority,true);
  assert.equal(proxied.at(-1).target.kind,'legacy');assert.equal(proxied.at(-1).body,JSON.stringify(input));
  assert.equal((await f.send('/host')).status,200);assert.equal(proxied.length,8);
  const config=(await f.send('/api/site/config')).data;assert.deepEqual(config.registration,{signupPath:'/signup',activationPath:'/activate',termsPath:'/tos',authority:'existing-site'});
});
