'use strict';
const assert=require('node:assert/strict');
const test=require('node:test');
const vm=require('node:vm');
const fs=require('node:fs');
const {BEFORE,AFTER,REVIEWED_INPUT_SHA256,sha256,replaceCompletion,patchBundle}=require('./primary-account-landing.cjs');
test('changes only the existing completion hook, preserving registration bytes',()=>{
  const prefix='/* registration fields and validators fixture */\n',suffix='\n/* Terms and email-verification fixture */';
  const result=replaceCompletion(prefix+BEFORE+suffix);
  assert.equal(result.source,prefix+AFTER+suffix);assert.equal(result.unchangedPrefixBytes,Buffer.byteLength(prefix));assert.equal(result.unchangedSuffixBytes,Buffer.byteLength(suffix));
});
test('completed account flow navigates through original safe return-path helper',()=>{
  const events=[];const sandbox={Yt:()=>({invalidateQueries:value=>events.push(['me',JSON.parse(JSON.stringify(value))])}),Lx:()=>'/host',window:{location:{replace:value=>events.push(['navigate',value])}}};
  vm.createContext(sandbox);vm.runInContext(AFTER+';this.complete=tg();',sandbox);
  assert.deepEqual(events,[]);sandbox.complete();assert.deepEqual(events,[['me',{queryKey:['me']}],['navigate','/host']]);
});
test('does not create sessions, call account endpoints or process pending tokens',()=>{
  assert.equal(/fetch|cookie|localStorage|sessionStorage|token|password|next===/u.test(AFTER),false);
});
test('rejects changed, duplicate and already patched completion hooks',()=>{
  for(const value of ['',BEFORE+BEFORE,AFTER])assert.throws(()=>replaceCompletion(value));
  assert.throws(()=>patchBundle(Buffer.from(BEFORE)),/reviewed deployed account asset/u);
});
test('accepts exactly the two reviewed asset revisions',()=>{
  assert.deepEqual(REVIEWED_INPUT_SHA256,['81b28895ccc85fb138bfd6a489c0aa6a5cd539c01b18c8011825ab00468f3138','8144e37d5067e1bd3a4b7807bc34c9963ec4eef9adb2053bb3a5ec7403052b7b']);
});
test('whole reviewed bundle changes only tg and retains native Link and registration bytes',{skip:!process.env.REDX_ACCOUNT_LANDING_FIXTURE},()=>{
  const input=fs.readFileSync(process.env.REDX_ACCOUNT_LANDING_FIXTURE),patched=patchBundle(input),at=input.indexOf(Buffer.from(BEFORE));
  assert.equal(patched.output.subarray(0,at).equals(input.subarray(0,at)),true);
  assert.equal(patched.output.subarray(at+Buffer.byteLength(AFTER)).equals(input.subarray(at+Buffer.byteLength(BEFORE))),true);
  assert.equal(patched.report.registrationFieldsChanged,false);assert.equal(patched.report.authenticationBackendChanged,false);
  assert.equal(patched.report.changedFunctions.join(','),'tg');assert.equal(patched.report.inputSha256,sha256(input));
  if(sha256(input)===REVIEWED_INPUT_SHA256[1])assert.equal(patched.report.outputSha256,'773b99e3be5d4405ac043c3f11e5e0af50c4aa63aebfaa33aa593733aaf11ead');
  assert.throws(()=>patchBundle(patched.output));
});
