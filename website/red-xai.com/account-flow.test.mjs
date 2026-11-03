import test from 'node:test';
import assert from 'node:assert/strict';
import {AccountFlowCancelled,createAccountFlow,loginOutcome} from './site/api.js';

const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};

test('challenge transport preserves legacy endpoint, body and in-memory token',async()=>{
  const calls=[],body={method:'totp',code:'123456'},response={ok:true};
  const flow=createAccountFlow({transport:async(...args)=>{calls.push(args);return {status:200,json:response};}});
  flow.begin('fixture-flow');assert.equal(await flow.step('login/2fa',body),response);
  assert.deepEqual(calls,[['login/2fa',body,'fixture-flow']]);
  flow.clear();await assert.rejects(()=>flow.step('login/2fa',body),AccountFlowCancelled);assert.equal(calls.length,1);
});

test('Back/navigation and a newer flow discard late responses without expiring the new flow',async()=>{
  const old=deferred(),tokens=[],expirations=[];
  const flow=createAccountFlow({transport:async(path,body,token)=>{tokens.push(token);return token==='old-flow'?old.promise:{status:200,json:{ok:true}};},onExpired:()=>expirations.push(true)});
  flow.begin('old-flow');const pending=flow.step('login/2fa',{});flow.clear();flow.begin('new-flow');
  old.resolve({status:401,json:{error:'tries'}});await assert.rejects(()=>pending,AccountFlowCancelled);
  assert.equal(expirations.length,0);assert.deepEqual(await flow.step('login/2fa',{}),{ok:true});assert.deepEqual(tokens,['old-flow','new-flow']);
});

test('disposed flow discards a late transport rejection',async()=>{
  const pending=deferred();const flow=createAccountFlow({transport:()=>pending.promise});
  flow.begin('fixture-flow');const result=flow.step('2fa/totp/start',{});flow.clear();pending.reject(new Error('late request failure'));
  await assert.rejects(()=>result,AccountFlowCancelled);
});

test('authority expiry and exhausted attempts reset the token exactly once',async()=>{
  for(const result of [{status:401,json:{message:'Expired'}},{status:403,json:{error:'tries'}}]){
    let calls=0,expired=0;const flow=createAccountFlow({transport:async()=>{calls++;return result;},onExpired:()=>expired++});
    flow.begin('fixture-flow');await assert.rejects(()=>flow.step('login/2fa',{}),AccountFlowCancelled);
    await assert.rejects(()=>flow.step('login/2fa',{}),AccountFlowCancelled);assert.equal(expired,1);assert.equal(calls,1);
  }
});

test('incorrect code and outages never become accepted challenge responses',async()=>{
  for(const result of [{status:400,json:{message:'Invalid code'}},{status:503,json:{message:'Account service unavailable'}},{status:200,json:{error:'invalid_code',message:'Invalid code'}},{status:200,json:null},{status:200,json:[]}]){
    let expired=0;const flow=createAccountFlow({transport:async()=>result,onExpired:()=>expired++});flow.begin('fixture-flow');
    await assert.rejects(()=>flow.step('login/2fa',{}),e=>e instanceof Error&&!(e instanceof AccountFlowCancelled));assert.equal(expired,0);
  }
});

test('MFA setup and unresolved steps cannot be treated as completed sessions',async()=>{
  for(const json of [{next:'2fa_setup',token:'fixture-flow'},{next:'2fa',token:'fixture-flow'},{pending:['totp']},{challenge:'challenge'},{token:'fixture-flow'}])assert.notEqual(loginOutcome(200,json),'complete');
  for(const token of ['',null,'line\nbreak','space token','x'.repeat(8193)]){
    const flow=createAccountFlow({transport:async()=>{throw new Error('must not dispatch');}});assert.throws(()=>flow.begin(token));await assert.rejects(()=>flow.step('login/2fa',{}),AccountFlowCancelled);
  }
});

test('activation routing requires a specific authority denial and never turns an outage into email verification',()=>{
  for(const json of [{error:'email_activation_required'},{error:'email_not_verified'},{message:'Email needs activated.'},{message:'Your email is not verified.'},{message:'Email verification is required.'}])assert.equal(loginOutcome(403,json),'verification');
  for(const [status,json]of [[503,{message:'Email activation service unavailable'}],[429,{error:'email_activation_required'}],[401,{message:'Invalid username or password.'}],[403,{message:'Account deactivated.'}],[403,{message:'Email activation failed.'}],[403,{error:{message:'email_activation_required'}}]])assert.equal(loginOutcome(status,json),'error');
});
