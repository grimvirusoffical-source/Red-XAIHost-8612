'use strict';
// Presentation-only completion integration for the deployed legacy account UI.
// Registration fields, validation, Terms, email and MFA contracts stay byte-identical.
const crypto=require('node:crypto');
const fs=require('node:fs');
const path=require('node:path');
const EXPECTED_INPUT_SHA256='81b28895ccc85fb138bfd6a489c0aa6a5cd539c01b18c8011825ab00468f3138';
const BEFORE='function tg(){const n=Yt();return()=>{window.history.replaceState(null,"",Lx()),n.invalidateQueries({queryKey:["me"]})}}';
const AFTER='function tg(){const n=Yt();return()=>{n.invalidateQueries({queryKey:["me"]}),window.location.replace(Lx())}}';
const sha256=bytes=>crypto.createHash('sha256').update(bytes).digest('hex');
function replaceCompletion(source){
  if(typeof source!=='string'||source.split(BEFORE).length!==2||source.includes(AFTER))throw Error('Reviewed account completion hook did not match exactly once');
  const start=source.indexOf(BEFORE),end=start+BEFORE.length;
  const result=source.slice(0,start)+AFTER+source.slice(end);
  if(result.slice(0,start)!==source.slice(0,start)||result.slice(start+AFTER.length)!==source.slice(end))throw Error('Unexpected registration UI change');
  return {source:result,unchangedPrefixBytes:Buffer.byteLength(source.slice(0,start)),unchangedSuffixBytes:Buffer.byteLength(source.slice(end))};
}
function patchBundle(bytes){
  if(!Buffer.isBuffer(bytes)||sha256(bytes)!==EXPECTED_INPUT_SHA256)throw Error('Input is not the reviewed deployed account asset');
  const source=bytes.toString('utf8');if(!Buffer.from(source).equals(bytes))throw Error('Invalid UTF-8 asset');
  const result=replaceCompletion(source),output=Buffer.from(result.source);
  return {output,report:{inputSha256:sha256(bytes),outputSha256:sha256(output),inputBytes:bytes.length,outputBytes:output.length,changedFunctions:['tg'],replacements:1,unchangedPrefixBytes:result.unchangedPrefixBytes,unchangedSuffixBytes:result.unchangedSuffixBytes,registrationFieldsChanged:false,authenticationBackendChanged:false}};
}
if(require.main===module){
  try{
    if(process.argv.length!==4)throw Error('Usage: node primary-account-landing.cjs <reviewed-input.js> <new-output.js>');
    const input=path.resolve(process.argv[2]),output=path.resolve(process.argv[3]);if(input===output)throw Error('Input and output must differ');
    const patched=patchBundle(fs.readFileSync(input));fs.writeFileSync(output,patched.output,{flag:'wx'});process.stdout.write(JSON.stringify(patched.report,null,2)+'\n');
  }catch{process.stderr.write('Account landing patch refused; verify the reviewed asset and exclusive output path.\n');process.exitCode=1;}
}
module.exports={EXPECTED_INPUT_SHA256,BEFORE,AFTER,sha256,replaceCompletion,patchBundle};
