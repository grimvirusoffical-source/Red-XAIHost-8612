import test from 'node:test';
import assert from 'node:assert/strict';
import {AUTHORITY,SiteError,accountKey,boundedJSON,canonicalIdentity,documentHtml,normalizeDocument,processImage,resolveIdentity,sessionCookie,validatePreferences} from './site-backend.mjs';

const now=1900000000000;
const profile=(id='fixture-A',extra={})=>({account:{id,email:id+'@example.test',username:id,roles:['user'],...extra},expiresMs:now+60000,amr:['pwd']});
const hasCode=code=>error=>error instanceof SiteError&&error.code===code;

test('owner privileges require the canonical immutable ID, authority role and MFA together; normal users need no MFA',()=>{
  const options={now,ownerIds:new Set(['fixture-owner'])};
  assert.equal(canonicalIdentity(profile(),options).owner,false);
  assert.equal(canonicalIdentity(profile('fixture-owner',{roles:['owner']}),options).owner,false);
  assert.equal(canonicalIdentity({...profile('fixture-owner',{roles:['owner']}),amr:['pwd','totp']},options).owner,true);
  assert.equal(canonicalIdentity({...profile('fixture-A',{roles:['owner'],email:'fixture-owner@example.test'}),amr:['totp']},options).owner,false);
  assert.equal(canonicalIdentity({...profile('fixture-owner'),amr:['totp']},options).owner,false);
  for(const changed of [
    {expiresMs:now},{amr:[]},{amr:[{}]},{account:null},{active:false},
    {account:{...profile().account,emailVerified:false}},{account:{...profile().account,disabled:true}},
    {account:{...profile().account,status:'pending'}},{account:{...profile().account,id:'owner'}},
    {account:{...profile().account,id:'__unassigned__'}},{account:{...profile().account,id:'two accounts'}},
  ])assert.throws(()=>canonicalIdentity({...profile(),...changed},options),hasCode('signed_out'));
});

test('session parsing selects exactly the existing website cookie and rejects ambiguity',()=>{
  assert.equal(sessionCookie({cookie:'ignored=other; redx_session=fixture-A; redx_session_extra=ignored'}),'redx_session=fixture-A');
  assert.equal(sessionCookie(new Headers({cookie:'redx_session=fixture-B; host-session=ignored'})),'redx_session=fixture-B');
  assert.equal(sessionCookie({cookie:'other=ignored'}),'');
  for(const cookie of ['redx_session=','redx_session=one; redx_session=two','redx_session=has space','redx_session=bad\nvalue'])assert.throws(()=>sessionCookie({cookie}),hasCode('signed_out'));
});

test('authority requests use only the fixed private URL and canonical cookie, with fresh account separation and revocation',async()=>{
  const calls=[],revoked=new Set();
  const fetchImpl=async(url,options)=>{
    calls.push({url,options});assert.equal(url,AUTHORITY);
    const id=options.headers.cookie.split('=',2)[1];
    await new Promise(resolve=>setTimeout(resolve,id==='fixture-A'?5:1));
    return Response.json(revoked.has(id)?{}:profile(id),{status:revoked.has(id)?401:200});
  };
  const identity=id=>resolveIdentity({cookie:'other=private; redx_session='+id,authorization:'Bearer ignored','x-redx-account-id':'fixture-owner'},{fetchImpl,now:()=>now});
  const [a,b]=await Promise.all([identity('fixture-A'),identity('fixture-B')]);
  assert.equal(a.id,'fixture-A');assert.equal(b.id,'fixture-B');assert.notEqual(accountKey(a.id),accountKey(b.id));
  assert.deepEqual(Object.keys(calls[0].options.headers).sort(),['accept','cookie']);
  assert.equal(calls[0].options.redirect,'manual');assert.equal(calls[0].options.cache,'no-store');
  revoked.add('fixture-A');await assert.rejects(()=>identity('fixture-A'),hasCode('signed_out'));
  assert.equal((await identity('fixture-B')).id,'fixture-B');assert.equal(calls.length,4);
  await assert.rejects(()=>resolveIdentity({authorization:'Bearer ignored'},{fetchImpl,now:()=>now}),hasCode('signed_out'));
  assert.equal(calls.length,4);
});

test('authority outages, redirects, invalid content types and oversized responses fail closed',async()=>{
  for(const fetchImpl of [
    async()=>{throw Error('offline');},async()=>new Response('',{status:302,headers:{location:'https://invalid.example'}}),
    async()=>new Response('not json',{status:200,headers:{'content-type':'text/html'}}),
    async()=>new Response('{}',{status:200,headers:{'content-type':'application/json-extra'}}),
    async()=>new Response('{',{status:200,headers:{'content-type':'application/json'}}),
    async()=>Response.json({padding:'x'.repeat(40000)}),
  ])await assert.rejects(()=>resolveIdentity({cookie:'redx_session=fixture-A'},{fetchImpl,now:()=>now}),hasCode('authority_unavailable'));
  await assert.rejects(()=>boundedJSON(Response.json({a:'too long'}),4),hasCode('authority_unavailable'));
});

test('post documents escape text and attributes, reject remote images and executable links',()=>{
  const id='a'.repeat(32);
  const doc=normalizeDocument({type:'doc',children:[{type:'paragraph',children:[{type:'text',text:'<script>alert(1)</script>',marks:['bold']}]},{type:'image',mediaId:id,alt:'" onerror="evil',caption:'<caption>'}]},{[id]:{}});
  const html=documentHtml(doc);assert.match(html,/&lt;script&gt;/u);assert.match(html,/&quot; onerror=&quot;evil/u);assert.doesNotMatch(html,/<script|alt=""/u);
  for(const node of [{type:'iframe'},{type:'image',mediaId:'https://invalid.example/image'},{type:'paragraph',children:[{type:'link',href:'javascript:alert(1)',children:[]}]}])assert.throws(()=>normalizeDocument({type:'doc',children:[node]},{}),SiteError);
  assert.deepEqual(validatePreferences({theme:'light',reducedMotion:true,timeZone:'UTC',locale:'en'}),{theme:'light',reducedMotion:true,timeZone:'UTC',locale:'en'});
  assert.throws(()=>validatePreferences({accountId:'fixture-B'}),hasCode('invalid_preferences'));
});

test('image processing refuses untrusted SVG/HTML or mismatched image signatures',async()=>{
  await assert.rejects(()=>processImage(Buffer.from('<svg onload="evil"/>'),'image/svg+xml'),hasCode('unsupported_image'));
  await assert.rejects(()=>processImage(Buffer.from('<html>not a PNG</html>'),'image/png'),hasCode('unsupported_image'));
});
