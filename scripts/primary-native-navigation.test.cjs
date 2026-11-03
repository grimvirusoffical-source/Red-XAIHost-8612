'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const os=require('node:os');
const vm=require('node:vm');
const {spawnSync}=require('node:child_process');
const patch=require('./primary-native-navigation.cjs');
// Only the two full-asset tests need an explicitly supplied, externally retained
// public fixture. CI runs the fragment/behavior and strict-refusal tests without it.
const fixturePath=process.env.REDX_LEGACY_NAVIGATION_FIXTURE;
const input=fixturePath?fs.readFileSync(path.resolve(fixturePath)):null;
function actualLink(component,props,base=''){
  const navigations=[],context={ei:()=>({base,hrefs:x=>x}),Hr:()=>['/',(href,options)=>navigations.push({href,options})],Yp:f=>f,C:{forwardRef:f=>f,isValidElement:x=>!!x&&x.element===true,cloneElement:(element,props)=>({type:element.type,props:{...element.props,...props}}),createElement:(type,props)=>({type,props})}};
  vm.runInNewContext('var '+component,context);
  return {link:context.Ge(props,null),navigations};
}
function event(extra={}){return {button:0,ctrlKey:false,metaKey:false,altKey:false,shiftKey:false,defaultPrevented:false,preventDefault(){this.defaultPrevented=true;},...extra};}
test('pins the actual deployed source and changes only one Link component',{skip:!input},()=>{
  assert.equal(patch.sha256(input),patch.EXPECTED_INPUT_SHA256);
  const result=patch.patchBundle(input),source=input.toString('utf8'),output=result.output.toString('utf8'),start=source.indexOf(patch.BEFORE);
  assert.equal(source.split(patch.BEFORE).length,2);
  assert.equal(output.slice(0,start),source.slice(0,start));
  assert.equal(output.slice(start+patch.AFTER.length),source.slice(start+patch.BEFORE.length));
  assert.equal(result.report.replacements,1);
  assert.equal(result.report.outputSha256,'8144e37d5067e1bd3a4b7807bc34c9963ec4eef9adb2053bb3a5ec7403052b7b');
  assert.equal(result.report.accountCompletionChanged,false);
  assert.equal(result.report.engineChanged,false);
  assert.throws(()=>patch.patchBundle(result.output));
});
test('the original exact Link reproduces the obsolete SPA interception',()=>{
  for(const href of ['/db','/host']){const {link,navigations}=actualLink(patch.BEFORE,{href}),e=event();link.props.onClick(e);assert.equal(e.defaultPrevented,true);assert.equal(navigations[0].href,href);}
});
test('native service root links use ordinary anchors without an SPA navigation',()=>{
  for(const href of ['/db','/host']){let callbacks=0;const {link,navigations}=actualLink(patch.AFTER,{href,onClick:()=>callbacks++,target:'_blank',rel:'noopener',download:undefined}),e=event();link.props.onClick(e);assert.equal(link.type,'a');assert.equal(link.props.href,href);assert.equal(link.props.target,'_blank');assert.equal(link.props.rel,'noopener');assert.equal(e.defaultPrevented,false);assert.equal(navigations.length,0);assert.equal(callbacks,1);}
});
test('modifiers and non-primary clicks keep native new-tab/window behavior',()=>{
  for(const href of ['/db','/host'])for(const extra of [{ctrlKey:true},{metaKey:true},{altKey:true},{shiftKey:true},{button:1},{button:2}]){let callbacks=0;const {link,navigations}=actualLink(patch.AFTER,{href,onClick:()=>callbacks++}),e=event(extra);link.props.onClick(e);assert.equal(e.defaultPrevented,false);assert.equal(navigations.length,0);assert.equal(callbacks,0);}
});
test('caller cancellation and already-prevented events remain cancelled',()=>{
  for(const href of ['/db','/host'])for(const already of [false,true]){const {link,navigations}=actualLink(patch.AFTER,{href,onClick:e=>e.preventDefault()}),e=event({defaultPrevented:already});link.props.onClick(e);assert.equal(e.defaultPrevented,true);assert.equal(navigations.length,0);}
});
test('unrelated, nested, query, external, tilde and base-prefixed routes remain byte-equivalent in behavior',()=>{
  for(const [href,base] of [['/',''],['/settings',''],['/news',''],['/db?advanced=1',''],['/db/records',''],['/host/projects',''],['https://example.test/db',''],['~/db',''],['/db','/different-base']]){
    const old=actualLink(patch.BEFORE,{href},base),updated=actualLink(patch.AFTER,{href},base),before=event(),after=event();old.link.props.onClick(before);updated.link.props.onClick(after);
    assert.equal(updated.link.props.href,old.link.props.href);assert.equal(after.defaultPrevented,before.defaultPrevented);assert.equal(updated.navigations.length,old.navigations.length);assert.equal(updated.navigations[0]?.href,old.navigations[0]?.href);
  }
});
test('asChild anchors retain their existing target and native root navigation',()=>{
  const {link,navigations}=actualLink(patch.AFTER,{href:'/db',asChild:true,children:{element:true,type:'a',props:{target:'_blank',rel:'noopener'}}}),e=event();link.props.onClick(e);assert.equal(link.type,'a');assert.equal(link.props.href,'/db');assert.equal(link.props.target,'_blank');assert.equal(e.defaultPrevented,false);assert.equal(navigations.length,0);
});
test('wrong, duplicated, missing and reapplied inputs fail closed',()=>{
  const prefix='/* preserved account/authentication prefix */',suffix='/* preserved account/authentication suffix */';
  const transformed=patch.replaceNavigation(prefix+patch.BEFORE+suffix);
  assert.equal(transformed.source,prefix+patch.AFTER+suffix);
  assert.equal(transformed.unchangedPrefixBytes,Buffer.byteLength(prefix));
  assert.equal(transformed.unchangedSuffixBytes,Buffer.byteLength(suffix));
  assert.throws(()=>patch.patchBundle(Buffer.from('not the reviewed bundle')));
  assert.throws(()=>patch.replaceNavigation('no reviewed Link'));
  assert.throws(()=>patch.replaceNavigation(patch.BEFORE+patch.BEFORE));
  assert.throws(()=>patch.patchBundle(Buffer.from(patch.BEFORE)));
  assert.throws(()=>patch.patchBundle(Buffer.from(patch.AFTER)));
  assert.throws(()=>patch.replaceNavigation(patch.AFTER));
});
test('CLI creates an exclusive new artifact and never overwrites source or destination',{skip:!input},()=>{
  const tempRoot=fs.realpathSync(os.tmpdir()),dir=fs.mkdtempSync(path.join(tempRoot,'redx-navigation-patch-'));
  try{const source=path.join(dir,'input.js'),output=path.join(dir,'output.js');fs.writeFileSync(source,input);let result=spawnSync(process.execPath,[path.join(__dirname,'primary-native-navigation.cjs'),source,output],{encoding:'utf8'});assert.equal(result.status,0);assert.equal(patch.sha256(fs.readFileSync(source)),patch.EXPECTED_INPUT_SHA256);const digest=patch.sha256(fs.readFileSync(output));result=spawnSync(process.execPath,[path.join(__dirname,'primary-native-navigation.cjs'),source,output],{encoding:'utf8'});assert.equal(result.status,1);assert.equal(patch.sha256(fs.readFileSync(output)),digest);result=spawnSync(process.execPath,[path.join(__dirname,'primary-native-navigation.cjs'),source,source],{encoding:'utf8'});assert.equal(result.status,1);assert.equal(patch.sha256(fs.readFileSync(source)),patch.EXPECTED_INPUT_SHA256);}finally{const resolved=fs.realpathSync(dir);if(path.dirname(resolved)!==tempRoot||!path.basename(resolved).startsWith('redx-navigation-patch-'))throw Error('Refused cleanup outside the test temporary directory');fs.rmSync(resolved,{recursive:true,force:true});}
});
