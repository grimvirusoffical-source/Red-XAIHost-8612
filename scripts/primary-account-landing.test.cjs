'use strict';
const assert=require('node:assert/strict');
const test=require('node:test');
const vm=require('node:vm');
const {BEFORE,AFTER,replaceCompletion,patchBundle}=require('./primary-account-landing.cjs');
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
