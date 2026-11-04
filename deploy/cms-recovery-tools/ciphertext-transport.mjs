// One-off encrypted recovery transport through the existing native path API.
// Requires an operator-reviewed exclusive writer interval. GET/PUT is not CAS.
import {promises as fs} from 'node:fs';
import {createHash} from 'node:crypto';
import net from 'node:net';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
const ORIGIN='http://127.0.0.1:17790',PREFIX='site-cms.red-xai.db';
const STATE=PREFIX+':SiteState[ID=1].Payload';
export const CAPSULE_PATH=PREFIX+':RecoveryTransportV1[ID=1].Payload';
const endpoint=p=>ORIGIN+'/v1/db/website_cms/path/'+encodeURIComponent(p);
const hash=b=>createHash('sha256').update(b).digest('hex');
const need=ok=>{if(!ok)throw Error('Encrypted recovery transport refused');};
export function validateCapsule(value){
  need(value?.kind==='cms-pair-age-ciphertext-v1'&&value.exclusiveWriterReviewed===true);
  need(/^[a-f0-9]{64}$/u.test(value.sha256)&&/^[a-f0-9]{64}$/u.test(value.pairSha256));
  need(typeof value.data==='string'&&value.data.length<1400000);
  const bytes=Buffer.from(value.data,'base64');
  need(bytes.length>100&&bytes.length<=1024*1024&&bytes.toString('base64')===value.data);
  need(bytes.subarray(0,22).toString()==='age-encryption.org/v1\n'&&hash(bytes)===value.sha256);
  return {schema:1,kind:value.kind,sha256:value.sha256,bytes:bytes.length,pairSha256:value.pairSha256,data:value.data};
}
export async function withMaintenanceLock(directory,operation){
  const filename=path.join(directory,'writer.lock'),file=await fs.open(filename,'wx',0o600),created=await file.stat();
  let complete=false;
  try{
    await file.writeFile(JSON.stringify({pid:process.pid,actor:'maintenance-service:ciphertext-transport-v1'})+'\n');await file.sync();
    const result=await operation();complete=true;return result;
  }finally{
    await file.close();
    if(complete){const current=await fs.lstat(filename);need(current.isFile()&&!current.isSymbolicLink()&&current.dev===created.dev&&current.ino===created.ino&&current.nlink===1);await fs.unlink(filename);const d=await fs.open(directory,'r');try{if(process.platform!=='win32')await d.sync();}finally{await d.close();}}
    // An incomplete write/readback retains its lock for operator inspection.
  }
}
export async function stageOnce(input,{key,fetchImpl=fetch}={}){
  const capsule=validateCapsule(input);need(/^rx_live_[A-Za-z0-9]{43}$/u.test(key));
  const call=async(url,options={})=>fetchImpl(url,{...options,headers:{accept:'application/json',authorization:'Bearer '+key,...options.headers},redirect:'manual',cache:'no-store',signal:AbortSignal.timeout(10000)});
  const scope=async()=>{
    const r=await call(ORIGIN+'/v1/ui/whoami');need(r.status===200);
    const v=await r.json();need(v.scope==='write'&&v.db==='website_cms'&&v.prefix===PREFIX&&v.host_admin===false&&v.owner===false&&v.role==='leader');
  };
  await scope();
  // A fixed, permanent one-off record refuses repeated/recursive transports.
  const absent=await call(endpoint(CAPSULE_PATH));need(absent.status===404);
  const before=await call(endpoint(STATE));need(before.status===200);
  const stateBefore=await before.text();need(stateBefore.length<32*1024*1024);
  const body=JSON.stringify(JSON.stringify(capsule));need(Buffer.byteLength(body)<2*1024*1024);
  const response=await call(endpoint(CAPSULE_PATH),{method:'PUT',headers:{'content-type':'text/x-redx-box'},body});need(response.status===200);
  const ack=await response.json();need(ack.ok===true&&Number.isSafeInteger(ack.seq)&&ack.seq>0);
  await scope();
  const readback=await call(endpoint(CAPSULE_PATH),{headers:{'x-redx-wait-seq':String(ack.seq)}});need(readback.status===200);
  const value=await readback.json();need(value.path===CAPSULE_PATH&&typeof value.value==='string');
  const actual=JSON.parse(JSON.parse(value.value));need(JSON.stringify(actual)===JSON.stringify(capsule));
  const after=await call(endpoint(STATE));need(after.status===200&&await after.text()===stateBefore);
  return {ok:true,kind:capsule.kind,ciphertextSha256:capsule.sha256,ciphertextBytes:capsule.bytes,pairSha256:capsule.pairSha256,readPath:'/v1/db/website_cms/path/'+encodeURIComponent(CAPSULE_PATH),cmsStateUnchanged:true,oneOffRecordRetained:true,offServerVerified:false};
}
async function main(){
  need(process.platform==='linux'&&process.getuid()===986&&process.getgid()===976&&process.argv.length===3&&process.argv[2]==='--stage-once');
  const noListener=await new Promise((resolve,reject)=>{const s=net.connect({host:'127.0.0.1',port:14210});s.setTimeout(1500);s.once('connect',()=>{s.destroy();resolve(false);});s.once('error',e=>resolve(e.code==='ECONNREFUSED'));s.once('timeout',()=>{s.destroy();reject(Error('Transport refused'));});});need(noListener);
  try{await fs.lstat('/var/lib/red-xai-site/writer.lock');throw Error('Transport refused');}catch(e){need(e.code==='ENOENT');}
  const keyFile='/etc/red-xai/site-cms.key',st=await fs.lstat(keyFile);
  need(st.isFile()&&!st.isSymbolicLink()&&st.nlink===1&&st.uid===986&&st.gid===976&&(st.mode&0o777)===0o600&&st.size<1024);
  let length=0;const parts=[];for await(const part of process.stdin){length+=part.length;need(length<1500000);parts.push(part);}
  const input=JSON.parse(Buffer.concat(parts).toString('utf8'));validateCapsule(input);
  const result=await withMaintenanceLock('/var/lib/red-xai-site',async()=>{
    const key=(await fs.readFile(keyFile,'utf8')).trim();return stageOnce(input,{key});
  });
  process.stdout.write(JSON.stringify(result)+'\n');
}
if(process.argv[1]&&fileURLToPath(import.meta.url)===path.resolve(process.argv[1]))main().catch(()=>{process.stderr.write('{"ok":false,"privateOutputWithheld":true,"partialEvidenceRetained":true}\n');process.exitCode=1;});
