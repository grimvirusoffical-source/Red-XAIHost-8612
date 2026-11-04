import test from 'node:test';
import assert from 'node:assert/strict';
import {createPreviewHandler,startPreview} from './preview.mjs';

test('guest harness handler is isolated, signed out and read-only even with account headers',async()=>{
  const handler=createPreviewHandler();
  const invoke=async(url,{method='GET',headers={}}={})=>{const response={statusCode:200,headers:new Map(),setHeader(name,value){this.headers.set(name.toLowerCase(),String(value));},end(value){this.body=String(value||'');}};await handler({url,method,headers,resume(){}},response);return {status:response.statusCode,headers:{get:name=>response.headers.get(name)},json:async()=>JSON.parse(response.body),text:async()=>response.body};};
    const session=await invoke('/api/site/session',{headers:{cookie:'redx_session=pretend',authorization:'Bearer pretend'}});assert.deepEqual(await session.json(),{signedIn:false,owner:false,capabilities:{cms:false,media:false,resendVerification:false,deleteAccount:false,subscriptions:false},preview:true});
    for(const route of ['/api/accounts/login','/api/rpc','/v1/console/','/api/site/checkout','/api/site/posts']){
      const response=await invoke(route,{method:'POST',headers:{'content-type':'application/json'}});assert.equal(response.status,503);assert.match((await response.json()).message,/disabled/u);
    }
    for(const route of ['/','/host','/db','/products/software','/legal/user-agreement']){const response=await invoke(route);assert.equal(response.status,200);assert.match(response.headers.get('content-security-policy'),/frame-src 'none'/u);assert.match(await response.text(),/LOCAL GUEST PREVIEW/u);}
    for(const route of ['/signup','/activate','/tos']){const response=await invoke(route);assert.match(await response.text(),/unchanged primary account service/u);}
    assert.deepEqual(await (await invoke('/api/site/posts?category=announcements&scope=owner')).json(),{posts:[],preview:true});
    assert.equal((await invoke('/server.mjs')).status,404);assert.equal((await invoke('/site/auth.js')).status,200);
});
test('guest harness refuses privileged or invalid ports',async()=>{for(const port of [80,0,65536,NaN,14310.5])await assert.rejects(()=>startPreview({port}));});
