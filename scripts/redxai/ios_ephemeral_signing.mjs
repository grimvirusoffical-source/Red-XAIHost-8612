import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { createPrivateKey, sign } from 'node:crypto';

const API='https://api.appstoreconnect.apple.com';
const tmp=process.env.RUNNER_TEMP;
const mode=process.argv[2]||'prepare';
const statePath=path.join(tmp,'redxai-signing-state.json');

function jwt(){
  const raw=fs.readFileSync(path.join(tmp,'asc',`AuthKey_${process.env.ASC_KEY_ID}.p8`),'utf8');
  const key=createPrivateKey(raw);
  const now=Math.floor(Date.now()/1000);
  const enc=x=>Buffer.from(JSON.stringify(x)).toString('base64url');
  const unsigned=`${enc({alg:'ES256',kid:process.env.ASC_KEY_ID,typ:'JWT'})}.${enc({iss:process.env.ASC_ISSUER_ID,iat:now-5,exp:now+115,aud:'appstoreconnect-v1'})}`;
  return `${unsigned}.${sign('sha256',Buffer.from(unsigned),{key,dsaEncoding:'ieee-p1363'}).toString('base64url')}`;
}
async function api(method,route,body){
  const r=await fetch(API+route,{method,headers:{Authorization:`Bearer ${jwt()}`,Accept:'application/json',...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});
  if(!r.ok){const t=await r.text();throw new Error(`Apple API ${method} ${route} failed HTTP ${r.status}: ${t.slice(0,500)}`)}
  if(r.status===204)return null;
  return r.json();
}
function appendEnv(k,v){fs.appendFileSync(process.env.GITHUB_ENV,`${k}=${v}\n`)}
async function prepare(){
  const dir=path.join(tmp,'signing'); fs.mkdirSync(dir,{recursive:true,mode:0o700});
  const keyPath=path.join(dir,'distribution.key');
  const csrPath=path.join(dir,'distribution.csr');
  execFileSync('openssl',['genrsa','-out',keyPath,'2048'],{stdio:'ignore'});
  execFileSync('openssl',['req','-new','-key',keyPath,'-subj','/CN=Red-XAI CI Distribution','-out',csrPath],{stdio:'ignore'});
  const csr=fs.readFileSync(csrPath,'utf8');
  const cert=await api('POST','/v1/certificates',{data:{type:'certificates',attributes:{certificateType:'IOS_DISTRIBUTION',csrContent:csr}}});
  const certId=cert.data.id;
  const certDer=Buffer.from(cert.data.attributes.certificateContent,'base64');
  const cerPath=path.join(dir,'distribution.cer'); fs.writeFileSync(cerPath,certDer,{mode:0o600});
  const bundleIdentifier=process.env.SELECTED_APP==='database'?'com.redxai.database':'com.redxai.host';
  const bundles=await api('GET',`/v1/bundleIds?filter%5Bidentifier%5D=${encodeURIComponent(bundleIdentifier)}&limit=5`);
  if(!bundles.data?.length)throw new Error('Bundle ID not found in Apple Developer account: '+bundleIdentifier);
  const profileName=`Red-XAI CI ${process.env.SELECTED_APP} ${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT}`;
  const profile=await api('POST','/v1/profiles',{data:{type:'profiles',attributes:{name:profileName,profileType:'IOS_APP_STORE'},relationships:{bundleId:{data:{type:'bundleIds',id:bundles.data[0].id}},certificates:{data:[{type:'certificates',id:certId}]}}}});
  const profileId=profile.data.id;
  const profileDir=path.join(os.homedir(),'Library','MobileDevice','Provisioning Profiles');fs.mkdirSync(profileDir,{recursive:true});
  fs.writeFileSync(path.join(profileDir,profileId+'.mobileprovision'),Buffer.from(profile.data.attributes.profileContent,'base64'),{mode:0o600});
  fs.writeFileSync(statePath,JSON.stringify({certId,profileId,profileName,bundleIdentifier}),{mode:0o600});
  appendEnv('RX_PROFILE_NAME',profileName); appendEnv('RX_BUNDLE_ID',bundleIdentifier);
  console.log('Created ephemeral Apple Distribution certificate and App Store profile for '+bundleIdentifier+'.');
}
async function cleanup(){
  if(!fs.existsSync(statePath))return;
  const s=JSON.parse(fs.readFileSync(statePath,'utf8'));
  for(const [type,id] of [['profiles',s.profileId],['certificates',s.certId]]){
    if(!id)continue;
    try{await api('DELETE',`/v1/${type}/${id}`);console.log('Revoked ephemeral '+type.slice(0,-1)+'.')}catch(e){console.error('Cleanup warning: '+e.message)}
  }
}
try{if(mode==='prepare')await prepare();else if(mode==='cleanup')await cleanup();else throw new Error('Unknown mode');}
catch(e){console.error('::error::'+e.message);process.exitCode=1;}
