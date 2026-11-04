'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const os=require('node:os');
const vm=require('node:vm');
const {spawnSync}=require('node:child_process');
const patch=require('./primary-host-vendor-surface.cjs');
// Keep public production artifacts outside Git. CI always runs the component
// and strict-refusal checks; full-asset checks require an explicit local fixture.
const fixturePath=process.env.REDX_LEGACY_HOST_FIXTURE;
const input=fixturePath?fs.readFileSync(path.resolve(fixturePath)):null;
function component(source){
  let hooks=0;
  const context={D:{useState:()=>{hooks++;return [false,()=>{}];}},d3:'fixture-badge-image',u3:'fixture-badge-icon',o:{jsx:(type,props)=>({type,props}),jsxs:(type,props)=>({type,props})}};
  vm.runInNewContext(source+';var result=f3();',context);
  return {value:context.result,hooks};
}
test('the reviewed production component is exactly the external branding anchor',()=>{
  if(input)assert.equal(patch.sha256(input),patch.EXPECTED_INPUT_SHA256);
  const {value,hooks}=component(patch.BEFORE);
  assert.equal(hooks,1);assert.equal(value.type,'a');assert.equal(value.props.href,'https://runable.com');assert.equal(value.props['data-runable-badge'],true);assert.equal(value.props.target,'_blank');
  const nested=value.props.children[2].props.children[1];assert.equal(nested.type,'span');assert.equal(nested.props.children,'Made with Runable');
});
test('replacement renders nothing and allocates no branding state or handlers',()=>{
  const result=component(patch.AFTER);assert.equal(result.value,null);assert.equal(result.hooks,0);
  assert.doesNotMatch(patch.AFTER,/window|document|fetch|cookie|localStorage|sessionStorage|password|token|href/);
});
test('actual bundle changes only that exact component and keeps its router mount intact',{skip:!input},()=>{
  const result=patch.patchBundle(input),before=input.toString('utf8'),after=result.output.toString('utf8'),start=before.indexOf(patch.BEFORE);
  assert.equal(before.split(patch.BEFORE).length,2);assert.equal(after.slice(0,start),before.slice(0,start));assert.equal(after.slice(start+patch.AFTER.length),before.slice(start+patch.BEFORE.length));
  assert.equal(before.split('o.jsx(f3,{}').length-1,1);assert.equal(after.split('o.jsx(f3,{}').length-1,1);
  assert.equal(result.output.length,519351);assert.equal(result.report.outputSha256,patch.EXPECTED_OUTPUT_SHA256);
  assert.equal(after.includes('Made with Runable'),false);assert.equal(after.includes('https://runable.com'),false);
  assert.equal(result.report.authenticationChanged,false);assert.equal(result.report.analyticsLoaderChanged,false);assert.equal(result.report.vendorByteRemovalComplete,false);
});
test('actual original and patched bundles both parse without executing app code',{skip:!input},()=>{
  for(const bytes of [input,patch.patchBundle(input).output]){
    const result=spawnSync(process.execPath,['--input-type=module','--check'],{input:bytes,encoding:'utf8'});
    // A failed syntax check must never dump the minified application bytes.
    assert.equal(result.status===0,true,'The reviewed ES module parses');
  }
});
test('changed, truncated and previously patched production inputs are refused',()=>{
  const refused=[Buffer.from('unexpected source'),Buffer.from(patch.BEFORE),Buffer.from(patch.AFTER)];
  if(input)refused.push(input.subarray(1),Buffer.concat([input,Buffer.from('\n')]),patch.patchBundle(input).output);
  for(const bytes of refused)assert.throws(()=>patch.patchBundle(bytes));
});
test('low-level replacement refuses missing, duplicate and reapplied components',()=>{
  for(const source of ['',patch.BEFORE+patch.BEFORE,patch.AFTER,patch.BEFORE+patch.AFTER])assert.throws(()=>patch.replaceBadge(source));
  const fragment='/* application and auth prefix */'+patch.BEFORE+'/* router suffix */',result=patch.replaceBadge(fragment);
  assert.equal(result.source,'/* application and auth prefix */'+patch.AFTER+'/* router suffix */');
});
test('CLI only creates an exclusive artifact and preserves the exact original',{skip:!input},()=>{
  const tempRoot=fs.realpathSync(os.tmpdir()),directory=fs.mkdtempSync(path.join(tempRoot,'redx-host-badge-'));
  try{
    const source=path.join(directory,'source.js'),destination=path.join(directory,'patched.js'),helper=path.join(__dirname,'primary-host-vendor-surface.cjs');fs.writeFileSync(source,input);
    let result=spawnSync(process.execPath,[helper,source,destination],{encoding:'utf8'});assert.equal(result.status,0);
    assert.equal(patch.sha256(fs.readFileSync(source)),patch.EXPECTED_INPUT_SHA256);assert.equal(patch.sha256(fs.readFileSync(destination)),patch.EXPECTED_OUTPUT_SHA256);
    result=spawnSync(process.execPath,[helper,source,destination],{encoding:'utf8'});assert.equal(result.status,1);assert.equal(patch.sha256(fs.readFileSync(destination)),patch.EXPECTED_OUTPUT_SHA256);
    result=spawnSync(process.execPath,[helper,source,source],{encoding:'utf8'});assert.equal(result.status,1);assert.equal(patch.sha256(fs.readFileSync(source)),patch.EXPECTED_INPUT_SHA256);
  }finally{
    const resolved=fs.realpathSync(directory);if(path.dirname(resolved)!==tempRoot||!path.basename(resolved).startsWith('redx-host-badge-'))throw Error('Refused cleanup outside the guarded temporary directory');fs.rmSync(resolved,{recursive:true,force:true});
  }
});
