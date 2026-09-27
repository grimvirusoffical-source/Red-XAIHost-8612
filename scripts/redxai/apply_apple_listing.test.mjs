import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {blankFieldPatch,validateDraft,applyListings} from './apply_apple_listing.mjs';
const drafts=JSON.parse(fs.readFileSync('store/ios/metadata-draft.json','utf8')).apps;
test('approved drafts meet metadata boundaries',()=>drafts.forEach(validateDraft));
test('only fills blank fields, preserves existing different text',()=>{
  assert.deepEqual(blankFieldPatch({subtitle:null,description:'user text',keywords:'same'},{subtitle:'new',description:'draft',keywords:'same'}),{patch:{subtitle:'new'},preserved:['description']});
});
test('rejects unexpected apps and oversize fields',()=>{
  assert.throws(()=>validateDraft({...drafts[0],bundleId:'com.example.unrelated'}));
  assert.throws(()=>validateDraft({...drafts[0],promotionalText:'x'.repeat(171)}));
});
test('does not access API for duplicate target input',async()=>{
  await assert.rejects(()=>applyListings(()=>{throw new Error('unexpected');},[drafts[0],drafts[0]]));
});
test('identity mismatches make no writes',async()=>{
  let reads=0;
  const result=await applyListings(async(path,params,method)=>{
    assert.equal(method,'GET');assert.ok(path.startsWith('/v1/apps/'));
    reads++;return {ok:true,status:200,body:{data:{attributes:{bundleId:'com.example.unrelated'}}}};
  },drafts);
  assert.equal(reads,2);
  assert.ok(result.apps.every(a=>a.updated.length===0&&a.errors[0]==='APP_IDENTITY_MISMATCH'));
});
