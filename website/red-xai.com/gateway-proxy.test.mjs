import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import {attachUpgrades,forwardHeaders,proxyHttp,proxyTarget,requestURL,requireOrigin} from './gateway-proxy.mjs';
import {SiteError} from './site-backend.mjs';

const config={publicOrigin:'https://red-xai.com',trustCloudflared:false};
const request=(headers={})=>({headers,socket:{remoteAddress:'127.0.0.1'}});
const route=value=>proxyTarget(requestURL(value,config.publicOrigin));
async function listen(t,handler){
  const server=http.createServer(handler);await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve);});
  t.after(async()=>{server.closeAllConnections();await new Promise(resolve=>server.close(resolve));});
  return {server,port:server.address().port,origin:'http://127.0.0.1:'+server.address().port};
}

test('gateway routes exact legacy, Host and native DB boundaries and preserves both verified Quick Login prefixes',()=>{
  for(const value of ['/signup','/activate?token=fixture','/tos','/vortex/cloudpc/','/vortex/signin','/api/accounts/login','/api/accounts/activate','/api/rpc/account','/assets/existing.js'])assert.deepEqual(route(value),{port:4200,kind:'legacy'});
  for(const prefix of ['/redxai-connect','/redxai-connectx'])for(const rest of ['','/','/redeem','/refresh','/app.js'])assert.deepEqual(route(prefix+rest),{port:17774,kind:'quick'});
  for(const value of ['/host?embed=site','/host/nodes','/host/api/rpc','/host/assets/app.js'])assert.deepEqual(route(value),{port:14202,kind:'host'});
  for(const value of ['/v1','/v1/console/?embed=site','/v1/db/fixture/path/a%2Fb','/ui','/ui/app.js'])assert.deepEqual(route(value),{port:17790,kind:'db'});
  for(const value of ['/host','/vortex','/vortex/','/cloud-pc','/hostile','/v10','/uix','/signup-extra','/api/accounts-other/login','/redxai-connectevil','/redxai-connectxevil'])assert.equal(route(value),null);
});

test('routing rejects raw path ambiguity while preserving encoded BoxQL path data and queries',()=>{
  for(const value of ['https://invalid.example/host','//invalid.example/host','/host\\nodes','/a/../signup','/%2e%2e/signup','/host#fragment','/host bad'])assert.throws(()=>requestURL(value,config.publicOrigin),e=>e instanceof SiteError&&e.status===400);
  assert.equal(requestURL('/v1/db/fixture/path/Box%3ASet%5BID%3D1%5D%2FName?durability=sync',config.publicOrigin).pathname,'/v1/db/fixture/path/Box%3ASet%5BID%3D1%5D%2FName');
});

test('upstream Host is fixed to public origin and browser identity/forwarded headers are never trusted',()=>{
  const req=request({host:'attacker.example',cookie:'other=private; redx_session=fixture-A; __Host-redx_cli_browser=fixture-browser',origin:config.publicOrigin,
    authorization:'Bearer fixture-token','x-redxai-client-key':'fixture-client-key','x-redx-flow':'fixture-flow',
    'x-redx-account-id':'fixture-owner','x-user-id':'fixture-owner','x-forwarded-host':'attacker.example','x-forwarded-proto':'http','cf-connecting-ip':'192.0.2.12'});
  const quick=forwardHeaders(req,{port:17774,kind:'quick'},config);
  assert.equal(quick.host,'red-xai.com');assert.equal(quick['x-forwarded-host'],'red-xai.com');assert.equal(quick['x-forwarded-proto'],'https');
  assert.equal(quick['x-redxai-client-key'],'fixture-client-key');assert.equal(quick.cookie,'redx_session=fixture-A; __Host-redx_cli_browser=fixture-browser');
  for(const name of ['x-redx-account-id','x-user-id','cf-connecting-ip','x-redx-flow'])assert.equal(quick[name],undefined);
  assert.equal(forwardHeaders(req,{port:14202,kind:'host'},config).cookie,'redx_session=fixture-A');
  assert.equal(forwardHeaders(req,{port:14202,kind:'host'},config)['x-redx-flow'],'fixture-flow');
  assert.equal(forwardHeaders(req,{port:4200,kind:'legacy'},config).cookie,req.headers.cookie);
  assert.equal(forwardHeaders(req,{port:4200,kind:'legacy'},config)['x-redx-flow'],'fixture-flow');
  assert.equal(forwardHeaders(req,{port:4200,kind:'legacy'},{...config,trustCloudflared:true})['cf-connecting-ip'],'192.0.2.12');
  assert.equal(forwardHeaders({...req,socket:{remoteAddress:'192.0.2.99'}},{port:4200,kind:'legacy'},{...config,trustCloudflared:true})['cf-connecting-ip'],undefined);
  assert.throws(()=>forwardHeaders(request({cookie:'redx_session=one; redx_session=two'}),{port:17790,kind:'db'},config),SiteError);
  assert.throws(()=>forwardHeaders(request({authorization:'Basic fake'}),{port:17790,kind:'db'},config),SiteError);
});

test('native DB confirmation, durability and signed webhook headers retain their protocol',()=>{
  const headers={'x-redx-confirm':'createKey','x-redx-wait-seq':'12','x-redx-wait-replicas':'2','x-redx-sha256':'fixture-hash','x-hub-signature-256':'sha256=fixture','x-github-delivery':'fixture-delivery','x-github-event':'push','x-redx-node':'untrusted-node'};
  const forwarded=forwardHeaders(request(headers),{port:17790,kind:'db'},config);
  for(const [key,value]of Object.entries(headers))assert.equal(forwarded[key],key==='x-redx-node'?undefined:value);
});

test('gateway preserves Quick Login request bytes, client key, status, cookies and service security headers',async t=>{
  let received;
  const upstream=await listen(t,async(req,res)=>{
    const chunks=[];for await(const chunk of req)chunks.push(chunk);received={method:req.method,url:req.url,headers:req.headers,body:Buffer.concat(chunks).toString()};
    res.writeHead(201,{'content-type':'application/json','set-cookie':['first=fixture; HttpOnly; Path=/','second=fixture; Secure; Path=/'],'content-security-policy':"default-src 'none'",'x-frame-options':'DENY','x-redx-seq':'12','connection':'close'});res.end('{"ok":true}');
  });
  const front=await listen(t,async(req,res)=>{try{await proxyHttp(req,res,{port:upstream.port,kind:'quick'},config);}catch(e){res.writeHead(e.status||503,{'content-type':'application/json'});res.end(JSON.stringify({error:e.code}));}});
  const body=JSON.stringify({client_id:'fixture-client',code:'fixture-code'});
  const response=await fetch(front.origin+'/redxai-connect/redeem',{method:'POST',headers:{'content-type':'application/json','x-redxai-client-key':'fixture-client-key'},body,redirect:'manual'});
  assert.equal(response.status,201);assert.deepEqual(await response.json(),{ok:true});
  assert.equal(received.url,'/redxai-connect/redeem');assert.equal(received.method,'POST');assert.equal(received.body,body);assert.equal(received.headers.host,'red-xai.com');assert.equal(received.headers['x-redxai-client-key'],'fixture-client-key');
  assert.equal(response.headers.getSetCookie().length,2);assert.equal(response.headers.get('x-frame-options'),'DENY');assert.equal(response.headers.get('content-security-policy'),"default-src 'none'");assert.equal(response.headers.get('x-redx-seq'),'12');
});

test('cookie writes require exact same origin while native token clients and legacy signup retain existing contracts',async t=>{
  let calls=0;
  const upstream=await listen(t,async(req,res)=>{calls++;req.resume();res.writeHead(204);res.end();});
  const front=await listen(t,async(req,res)=>{try{await proxyHttp(req,res,{port:upstream.port,kind:req.url.startsWith('/api/accounts/')?'legacy':'db'},config);}catch(e){res.writeHead(e.status||503);res.end();}});
  for(const headers of [{cookie:'redx_session=fixture-A'},{cookie:'redx_session=fixture-A',origin:'https://invalid.example'},{cookie:'redx_session=fixture-A',origin:config.publicOrigin,'sec-fetch-site':'cross-site'}]){
    const r=await fetch(front.origin+'/v1/keys',{method:'POST',headers,body:'{}'});assert.equal(r.status,403);await r.arrayBuffer();
  }
  assert.equal(calls,0);
  assert.equal((await fetch(front.origin+'/v1/keys',{method:'POST',headers:{authorization:'Bearer fixture-token'},body:'{}'})).status,204);
  assert.equal((await fetch(front.origin+'/v1/keys',{method:'POST',headers:{cookie:'redx_session=fixture-A',origin:config.publicOrigin},body:'{}'})).status,204);
  assert.equal((await fetch(front.origin+'/api/accounts/signup',{method:'POST',headers:{cookie:'legacy=fixture','content-type':'application/json'},body:'{"terms":true}'})).status,204);
  assert.equal(calls,3);assert.throws(()=>requireOrigin(request({origin:config.publicOrigin+'/' }),config.publicOrigin),SiteError);
});

test('gateway leaves relative and external authority redirects intact and rewrites only absolute loopback locations',async t=>{
  const upstream=await listen(t,(req,res)=>{req.resume();res.writeHead(302,{location:req.url==='/relative'?'next?token=fixture':req.url==='/external'?'https://example.test/callback':'http://127.0.0.1:4200/activate?token=fixture'});res.end();});
  const front=await listen(t,(req,res)=>proxyHttp(req,res,{port:upstream.port,kind:'legacy'},config));
  for(const [path,location]of [['/relative','next?token=fixture'],['/external','https://example.test/callback'],['/local','/activate?token=fixture']]){
    const r=await fetch(front.origin+path,{redirect:'manual'});assert.equal(r.status,302);assert.equal(r.headers.get('location'),location);await r.arrayBuffer();
  }
});

test('WebSocket routes and browser origins fail closed before contacting a backend',async t=>{
  const front=await listen(t,(_req,res)=>res.end());attachUpgrades(front.server,config);
  for(const [path,extra]of [['/host/api/live',{cookie:'redx_session=fixture-A'}],['/v1/live',{origin:'https://invalid.example'}],['/unknown/live',{}],['/a/../host/api/live',{}]]){
    const status=await new Promise((resolve,reject)=>{
      const req=http.request(front.origin,{path,headers:{connection:'Upgrade',upgrade:'websocket','sec-websocket-version':'13','sec-websocket-key':'Zml4dHVyZS13ZWJzb2NrZXQ=',...extra}});
      req.setTimeout(2000,()=>req.destroy(new Error('Fixture timeout')));req.on('error',reject);
      req.on('response',res=>{res.resume();resolve(res.statusCode);});req.end();
    });
    assert.equal(status,403);
  }
});
