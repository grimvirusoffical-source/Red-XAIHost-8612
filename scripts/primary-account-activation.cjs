'use strict';
// Reviewed future activation-completion adapter. Apply only after the account
// authority's optional-MFA contract and migration pass, never as an auth bypass.
const fs=require('node:fs');
const path=require('node:path');
const {sha256}=require('./primary-account-landing.cjs');
const EXPECTED_INPUT_SHA256='f531bead2e2fd314ea1fc5a05e19ae248695d603d57bc1a985eb04018832c327';
const BEFORE='f.status===200?i({k:"setup",token:f.json.token,methods:f.json.methods,username:f.json.username}):c(f.json.message??"This activation link did not work.")';
const AFTER='f.status===200?f.json.signInRequired===true&&f.json.token===void 0?window.location.replace("/signin"):i({k:"setup",token:f.json.token,methods:f.json.methods,username:f.json.username}):c(f.json.message??"This activation link did not work.")';
function replaceActivation(source){
  if(typeof source!=='string'||source.split(BEFORE).length!==2||source.includes(AFTER))throw Error('Reviewed activation completion did not match exactly once');
  const start=source.indexOf(BEFORE),end=start+BEFORE.length;
  return {source:source.slice(0,start)+AFTER+source.slice(end),unchangedPrefixBytes:Buffer.byteLength(source.slice(0,start)),unchangedSuffixBytes:Buffer.byteLength(source.slice(end))};
}
function patchBundle(bytes){
  if(!Buffer.isBuffer(bytes)||sha256(bytes)!==EXPECTED_INPUT_SHA256)throw Error('Input is not the reviewed Hub-completion account asset');
  const source=bytes.toString('utf8');if(!Buffer.from(source).equals(bytes))throw Error('Invalid UTF-8 asset');
  const result=replaceActivation(source),output=Buffer.from(result.source);
  return {output,report:{inputSha256:sha256(bytes),outputSha256:sha256(output),inputBytes:bytes.length,outputBytes:output.length,changedFunctions:['vN'],replacements:1,unchangedPrefixBytes:result.unchangedPrefixBytes,unchangedSuffixBytes:result.unchangedSuffixBytes,registrationFieldsChanged:false,authenticationBackendChanged:false,productionAuthorityContractVerified:false,productionDeployed:false}};
}
if(require.main===module){
  try{
    if(process.argv.length!==4)throw Error();
    const input=path.resolve(process.argv[2]),output=path.resolve(process.argv[3]);if(input===output)throw Error();
    const patched=patchBundle(fs.readFileSync(input));fs.writeFileSync(output,patched.output,{flag:'wx'});process.stdout.write(JSON.stringify(patched.report,null,2)+'\n');
  }catch{process.stderr.write('Activation presentation patch refused; verify the reviewed asset and exclusive output path.\n');process.exitCode=1;}
}
module.exports={EXPECTED_INPUT_SHA256,BEFORE,AFTER,replaceActivation,patchBundle};
