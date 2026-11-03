'use strict';
// Compatibility repair for the exact deployed legacy website asset. Only the
// Link component's two native-service roots stop being intercepted by the SPA.
// The browser still follows a normal same-origin anchor and the server retains
// all authentication/authorization. No account or native engine bytes change.
const crypto=require('node:crypto');
const fs=require('node:fs');
const path=require('node:path');
const EXPECTED_INPUT_SHA256='81b28895ccc85fb138bfd6a489c0aa6a5cd539c01b18c8011825ab00468f3138';
const BEFORE='Ge=C.forwardRef((n,i)=>{const o=ei(),[c,d]=Hr(o),{to:f="",href:p=f,onClick:y,asChild:m,children:g,className:j,replace:k,state:S,transition:H,...Q}=n,T=Yp(G=>{G.ctrlKey||G.metaKey||G.altKey||G.shiftKey||G.button!==0||(y?.(G),G.defaultPrevented||(G.preventDefault(),d(p,n)))}),Y=o.hrefs(p[0]==="~"?p.slice(1):o.base+p,o);return m&&C.isValidElement(g)?C.cloneElement(g,{onClick:T,href:Y}):C.createElement("a",{...Q,onClick:T,href:Y,className:j?.call?j(c===p):j,children:g,ref:i})})';
const AFTER=BEFORE.replace('G.defaultPrevented||(G.preventDefault(),d(p,n))','G.defaultPrevented||((p==="/db"||p==="/host")&&Y===p?void 0:(G.preventDefault(),d(p,n)))');
const sha256=bytes=>crypto.createHash('sha256').update(bytes).digest('hex');
function replaceNavigation(source){
  if(typeof source!=='string'||source.split(BEFORE).length!==2||source.includes(AFTER))throw Error('Reviewed legacy Link did not match exactly once');
  const start=source.indexOf(BEFORE),end=start+BEFORE.length;
  const patched=source.slice(0,start)+AFTER+source.slice(end);
  if(patched.slice(0,start)!==source.slice(0,start)||patched.slice(start+AFTER.length)!==source.slice(end))throw Error('Unexpected change outside Link component');
  return {source:patched,unchangedPrefixBytes:Buffer.byteLength(source.slice(0,start)),unchangedSuffixBytes:Buffer.byteLength(source.slice(end))};
}
function patchBundle(bytes){
  if(!Buffer.isBuffer(bytes)||sha256(bytes)!==EXPECTED_INPUT_SHA256)throw Error('Input is not the exact reviewed deployed asset');
  const source=bytes.toString('utf8');if(!Buffer.from(source).equals(bytes))throw Error('Invalid UTF-8 asset');
  const patched=replaceNavigation(source),output=Buffer.from(patched.source);
  return {output,report:{inputSha256:sha256(bytes),outputSha256:sha256(output),inputBytes:bytes.length,outputBytes:output.length,replacements:1,changedComponents:['Ge'],nativeAnchorPaths:['/db','/host'],computedHrefMustEqualPath:true,unchangedPrefixBytes:patched.unchangedPrefixBytes,unchangedSuffixBytes:patched.unchangedSuffixBytes,authenticationBackendChanged:false,accountCompletionChanged:false,registrationChanged:false,engineChanged:false}};
}
if(require.main===module){
  try{
    if(process.argv.length!==4)throw Error('Usage: node primary-native-navigation.cjs <reviewed-input.js> <new-output.js>');
    const input=path.resolve(process.argv[2]),output=path.resolve(process.argv[3]);if(input===output)throw Error('Input and output must differ');
    const result=patchBundle(fs.readFileSync(input));fs.writeFileSync(output,result.output,{flag:'wx'});process.stdout.write(JSON.stringify(result.report,null,2)+'\n');
  }catch{process.stderr.write('Legacy navigation patch refused; verify the pinned input and exclusive output path.\n');process.exitCode=1;}
}
module.exports={EXPECTED_INPUT_SHA256,BEFORE,AFTER,sha256,replaceNavigation,patchBundle};
