'use strict';
// Exact reviewed production UI-only patch. Backend authentication, account and
// native-client/engine bytes are not changed. Unused vendor asset bytes remain.
const fs=require('node:fs');
const path=require('node:path');
const crypto=require('node:crypto');
const EXPECTED_INPUT_SHA256="fbb983596ea37b03686123b8f0e4638713cc3a0b756f8b5977aba5e78bed57c8";
const EXPECTED_OUTPUT_SHA256="dc980958bb5f1fdb4691033bfaddb7e0e5b86cfcb84e3f13fd94fef60c3cd9f8";
const BEFORE="function f3(){const[t,a]=D.useState(!1);return o.jsxs(\"a\",{href:\"https://runable.com\",target:\"_blank\",rel:\"noopener noreferrer\",\"data-runable-ignore\":!0,\"data-runable-badge\":!0,onMouseEnter:()=>a(!0),onMouseLeave:()=>a(!1),style:{position:\"fixed\",bottom:20,right:20,zIndex:1e4,display:\"inline-flex\",alignItems:\"center\",gap:6,paddingLeft:14,paddingRight:16,paddingTop:10,paddingBottom:10,borderRadius:32,backdropFilter:\"blur(16px)\",textDecoration:\"none\",cursor:\"pointer\",overflow:\"hidden\",transform:t?\"translateY(-1px)\":\"translateY(0px)\",boxShadow:t?\"0 4px 20px rgba(0, 170, 255, 0.25)\":\"none\",transition:\"transform 0.18s ease, box-shadow 0.18s ease\"},children:[o.jsx(\"div\",{style:{position:\"absolute\",inset:0,background:\"linear-gradient(90deg, rgba(0, 231, 255, 0.08) 0%, rgba(0, 231, 255, 0) 100%), #282828\",transition:\"opacity 0.18s ease\",opacity:t?0:1}}),o.jsx(\"div\",{style:{position:\"absolute\",inset:0,backgroundImage:`url(${d3})`,backgroundSize:\"cover\",backgroundPosition:\"center\",transition:\"opacity 0.18s ease\",opacity:t?1:0}}),o.jsxs(\"div\",{style:{position:\"relative\",display:\"flex\",alignItems:\"center\",gap:7},children:[o.jsx(u3,{size:17}),o.jsx(\"span\",{style:{color:\"white\",fontSize:13,fontFamily:\"'ID Grotesk Trial', sans-serif\",fontWeight:500,lineHeight:\"1\",whiteSpace:\"nowrap\",letterSpacing:\"0.01em\"},children:\"Made with Runable\"})]})]})}";
const AFTER="function f3(){return null}";
const sha256=bytes=>crypto.createHash('sha256').update(bytes).digest('hex');
function replaceBadge(source){
  if(typeof source!=='string'||source.split(BEFORE).length!==2||source.includes(AFTER))throw Error('Reviewed badge did not match exactly once');
  const start=source.indexOf(BEFORE),end=start+BEFORE.length;
  const output=source.slice(0,start)+AFTER+source.slice(end);
  if(output.slice(0,start)!==source.slice(0,start)||output.slice(start+AFTER.length)!==source.slice(end))throw Error('Unexpected non-badge change');
  return {source:output,unchangedPrefixBytes:Buffer.byteLength(source.slice(0,start)),unchangedSuffixBytes:Buffer.byteLength(source.slice(end))};
}
function patchBundle(bytes){
  if(!Buffer.isBuffer(bytes)||sha256(bytes)!==EXPECTED_INPUT_SHA256)throw Error('Input is not the exact reviewed deployed Host asset');
  const source=bytes.toString('utf8');if(!Buffer.from(source).equals(bytes))throw Error('Invalid UTF-8 asset');
  const result=replaceBadge(source),output=Buffer.from(result.source);if(sha256(output)!==EXPECTED_OUTPUT_SHA256)throw Error('Unexpected patched digest');
  return {output,report:{inputSha256:sha256(bytes),outputSha256:sha256(output),inputBytes:bytes.length,outputBytes:output.length,replacements:1,changedComponents:['f3'],visibleBadgeRemoved:true,backendChanged:false,authenticationChanged:false,engineChanged:false,analyticsLoaderChanged:false,vendorByteRemovalComplete:false,unchangedPrefixBytes:result.unchangedPrefixBytes,unchangedSuffixBytes:result.unchangedSuffixBytes}};
}
if(require.main===module){
  try{
    if(process.argv.length!==4)throw Error('Usage: node primary-host-vendor-surface.cjs <reviewed-input.js> <new-output.js>');
    const input=path.resolve(process.argv[2]),output=path.resolve(process.argv[3]);if(input===output)throw Error('Input and output must differ');
    const result=patchBundle(fs.readFileSync(input));fs.writeFileSync(output,result.output,{flag:'wx'});process.stdout.write(JSON.stringify(result.report,null,2)+'\n');
  }catch{process.stderr.write('Host UI patch refused; verify pinned input and exclusive output path.\n');process.exitCode=1;}
}
module.exports={EXPECTED_INPUT_SHA256,EXPECTED_OUTPUT_SHA256,BEFORE,AFTER,sha256,replaceBadge,patchBundle};
