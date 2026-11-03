'use strict';
const assert=require('node:assert/strict');
const test=require('node:test');
const vm=require('node:vm');
const {BEFORE,AFTER,replaceActivation,patchBundle}=require('./primary-account-activation.cjs');
function completion(response){
  const events=[],sandbox={f:response,i:value=>events.push(['setup',JSON.parse(JSON.stringify(value))]),c:value=>events.push(['error',value]),window:{location:{replace:value=>events.push(['navigate',value])}}};
  vm.runInNewContext(AFTER,sandbox);return events;
}
test('explicit session-free activation completion returns to sign-in without creating a session or asserting MFA',()=>{
  assert.deepEqual(completion({status:200,json:{signInRequired:true}}),[['navigate','/signin']]);
  assert.equal(/cookie|localStorage|sessionStorage|fetch|history|invalidateQueries/u.test(AFTER),false);
});
test('legacy enrollment, ambiguous pending token and boolean-only opt-in retain the existing setup path',()=>{
  for(const json of [{token:'fixture-flow',methods:{totp:true},username:'fixture'},{signInRequired:true,token:'fixture-flow'},{signInRequired:'true'},{signInRequired:false}]){
    const expected=JSON.parse(JSON.stringify({k:'setup',token:json.token,methods:json.methods,username:json.username}));
    assert.deepEqual(completion({status:200,json}),[['setup',expected]]);
  }
});
test('failed activation does not navigate or begin MFA setup',()=>{
  for(const status of [400,401,403,429,503])assert.deepEqual(completion({status,json:{signInRequired:true,message:'Fixture denial'}}),[['error','Fixture denial']]);
});
test('only the reviewed completion expression changes and unknown bundles are rejected',()=>{
  const prefix='/* exact signup fields, Terms, validators */',suffix='/* exact account and MFA code */';
  const result=replaceActivation(prefix+BEFORE+suffix);assert.equal(result.source,prefix+AFTER+suffix);
  assert.equal(result.unchangedPrefixBytes,Buffer.byteLength(prefix));assert.equal(result.unchangedSuffixBytes,Buffer.byteLength(suffix));
  for(const source of ['',BEFORE+BEFORE,AFTER])assert.throws(()=>replaceActivation(source));
  assert.throws(()=>patchBundle(Buffer.from(BEFORE)));
});
