// Private, single-writer maintenance evidence; never an account/API bypass.
import {promises as fs} from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import {createHash} from 'node:crypto';
import {fileURLToPath,pathToFileURL} from 'node:url';

export const SERVICE_ACTOR=Object.freeze({id:'maintenance-service:cms-persistence-probe:v1'});
export const SYNTHETIC_PREFS=Object.freeze({maintenance_fixture_alpha_v1:{theme:'dark',locale:'en-US'},maintenance_fixture_beta_v1:{theme:'light',locale:'en-GB'}});
const POST_ID='p_'+createHash('sha256').update(SERVICE_ACTOR.id).digest('hex').slice(0,32);
const RECEIPT='maintenance-probe-v1.json';
const PREFIX='maintenance.cms_probe.';
const AUDIT_EVENTS=[PREFIX+'service.begin','media.create.attempt','media.create.committed',PREFIX+'draft.attempt',PREFIX+'draft.committed',PREFIX+'preferences.attempt',PREFIX+'preferences.committed',PREFIX+'preferences.attempt',PREFIX+'preferences.committed',PREFIX+'service.complete'];
const PNG=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=','base64');
const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
const check=(ok)=>{if(!ok)throw Error('Maintenance probe prerequisite or readback failed');};
const plain=value=>value!==null&&typeof value==='object'&&!Array.isArray(value);
async function privateRegular(file,max){const s=await fs.lstat(file);check(s.isFile()&&!s.isSymbolicLink()&&s.size<=max&&(process.platform==='win32'||(s.mode&0o077)===0));return fs.readFile(file);}
async function auditBytes(store){try{return await privateRegular(path.join(store.directory,'audit.jsonl'),8*1024*1024);}catch(e){if(e.code==='ENOENT')return Buffer.alloc(0);throw e;}}
function actorEvents(bytes){return bytes.toString('utf8').split('\n').filter(Boolean).map(line=>JSON.parse(line)).filter(v=>v.actor===hash(SERVICE_ACTOR.id));}
async function writeReceipt(store,value){
  const fd=await fs.open(path.join(store.directory,RECEIPT),'wx',0o600);
  try{await fd.writeFile(JSON.stringify(value)+'\n');await fd.sync();}finally{await fd.close();}
  if(process.platform!=='win32'){const dir=await fs.open(store.directory,'r');try{await dir.sync();}finally{await dir.close();}}
}
async function readReceipt(store){const bytes=await privateRegular(path.join(store.directory,RECEIPT),16384);const value=JSON.parse(bytes);check(value.schema===1&&value.kind==='synthetic-service-maintenance'&&value.actor===SERVICE_ACTOR.id&&value.postId===POST_ID&&value.revision===4&&Number.isSafeInteger(value.auditBytes)&&value.auditBytes>0&&/^[a-f0-9]{32}$/u.test(value.mediaId)&&['stateSha256','mediaSha256','auditPrefixSha256'].every(k=>/^[a-f0-9]{64}$/u.test(value[k])));return {value,bytes};}

export async function seedProbe(store,{processImage,now=Date.now}={}){
  check(typeof processImage==='function');
  const state=await store.read();
  check(store.envelope?.revision===0&&state.schema===1&&Array.isArray(state.posts)&&state.posts.length===0&&plain(state.media)&&Object.keys(state.media).length===0&&plain(state.preferences)&&Object.keys(state.preferences).length===0);
  // Refuse a prior/partial maintenance attempt. Preserve all evidence and locks;
  // this tool has no cleanup, overwrite, account roles or publication operation.
  try{await fs.lstat(path.join(store.directory,RECEIPT));check(false);}catch(e){if(e.code!=='ENOENT')throw e;}
  check((await fs.readdir(path.join(store.directory,'media'))).length===0&&actorEvents(await auditBytes(store)).length===0);
  const image=await processImage(PNG,'image/png');check(Buffer.isBuffer(image.bytes)&&image.width===1&&image.height===1&&image.bytes.subarray(0,4).toString()==='RIFF'&&image.bytes.subarray(8,12).toString()==='WEBP');
  const at=new Date(now()).toISOString();
  await store.audit(PREFIX+'service.begin',SERVICE_ACTOR,'synthetic-service-fixture-v1');
  const media=await store.addMedia(image.bytes,image,SERVICE_ACTOR);
  await store.mutate(PREFIX+'draft',SERVICE_ACTOR,POST_ID,next=>{next.posts.push({id:POST_ID,title:'Synthetic CMS maintenance draft',category:'announcements',status:'draft',createdAt:at,updatedAt:at,createdBy:SERVICE_ACTOR.id,body:{type:'doc',children:[{type:'paragraph',children:[{type:'text',text:'Synthetic service fixture. No account or publication authority.'}]},{type:'image',mediaId:media.id,alt:'Synthetic one pixel maintenance image'}]}});});
  for(const [key,value]of Object.entries(SYNTHETIC_PREFS))await store.mutate(PREFIX+'preferences',SERVICE_ACTOR,key,next=>{next.preferences[key]=structuredClone(value);});
  const saved=await store.read();check(store.envelope.revision===4);
  await store.audit(PREFIX+'service.complete',SERVICE_ACTOR,'synthetic-service-fixture-v1');
  const audit=await auditBytes(store);check(JSON.stringify(actorEvents(audit).map(v=>v.event))===JSON.stringify(AUDIT_EVENTS));
  const receipt={schema:1,kind:'synthetic-service-maintenance',actor:SERVICE_ACTOR.id,createdAt:at,postId:POST_ID,mediaId:media.id,revision:4,stateSha256:hash(JSON.stringify(saved)),mediaSha256:hash(image.bytes),auditBytes:audit.length,auditPrefixSha256:hash(audit)};
  await writeReceipt(store,receipt);
  return verifyProbe(store,{processImage});
}

export async function verifyProbe(store,{processImage}={}){
  check(typeof processImage==='function');
  const {value:receipt,bytes:receiptBytes}=await readReceipt(store),state=await store.read();
  check(store.envelope.revision===receipt.revision&&hash(JSON.stringify(state))===receipt.stateSha256);
  check(state.posts.length===1&&state.posts[0].id===POST_ID&&state.posts[0].status==='draft'&&state.posts[0].createdBy===SERVICE_ACTOR.id);
  check(JSON.stringify(state.preferences)===JSON.stringify(SYNTHETIC_PREFS)&&Object.keys(state.media).length===1&&plain(state.media[receipt.mediaId]));
  const media=await store.mediaBytes(receipt.mediaId);check(hash(media)===receipt.mediaSha256);
  const decoded=await processImage(media,'image/webp');check(decoded.width===1&&decoded.height===1);
  const audit=await auditBytes(store);check(audit.length>=receipt.auditBytes&&hash(audit.subarray(0,receipt.auditBytes))===receipt.auditPrefixSha256&&JSON.stringify(actorEvents(audit.subarray(0,receipt.auditBytes)).map(v=>v.event))===JSON.stringify(AUDIT_EVENTS));
  return {ok:true,evidence:'synthetic-service-maintenance',receiptSha256:hash(receiptBytes),stateSha256:receipt.stateSha256,mediaSha256:receipt.mediaSha256,auditPrefixSha256:receipt.auditPrefixSha256,cmsRevisionReadback:receipt.revision,unpublishedDraft:true,syntheticPreferenceKeys:2,realWebpDecoded:true,auditServiceEvents:10,websiteOwnerApiVerified:false,normalAccountIsolationVerified:false,backupRestoreVerified:false};
}

export async function verifyWriterLockReleased(directory){try{await fs.lstat(path.join(directory,'writer.lock'));check(false);}catch(e){if(e.code!=='ENOENT')throw e;}}

async function noGateway(){await new Promise((resolve,reject)=>{const s=net.connect({host:'127.0.0.1',port:14210});s.setTimeout(3000);s.once('connect',()=>{s.destroy();reject(Error('Private gateway must be stopped'));});s.once('timeout',()=>{s.destroy();reject(Error('Private gateway state unknown'));});s.once('error',e=>e.code==='ECONNREFUSED'?resolve():reject(Error('Private gateway state unknown')));});}
async function run(){
  check(process.platform==='linux'&&process.getuid()===986&&process.getgid()===976);
  const [mode,confirmation,...extra]=process.argv.slice(2);check(['seed','verify'].includes(mode)&&confirmation==='--private-gateway-stopped'&&extra.length===0);
  await noGateway();
  const site='/opt/redx-site-c443ce7/website/red-xai.com';
  for(const [name,expected]of Object.entries({'redx-cms-store.mjs':'4c5b9187010fee5a9b384722c0dd7ab2e5bf825e2ec1811f6fac5a936d031d5d','site-backend.mjs':'6095d8cb2047dba867444ebbea8a44a5d4919f447baa871301020a3095249437'}))check(hash(await fs.readFile(path.join(site,name)))===expected);
  const directory='/var/lib/red-xai-site',keyFile='/etc/red-xai/site-cms.key';
  check(await fs.realpath(directory)===directory&&await fs.realpath(keyFile)===keyFile);
  const {RedXCmsStore}=await import(pathToFileURL(path.join(site,'redx-cms-store.mjs'))),{processImage}=await import(pathToFileURL(path.join(site,'site-backend.mjs')));
  let store;
  try{store=await new RedXCmsStore({directory,database:'website_cms',keyFile,durability:'group',singleWriter:true}).init();const result=await(mode==='seed'?seedProbe(store,{processImage}):verifyProbe(store,{processImage}));await store.close();await verifyWriterLockReleased(directory);store=null;console.log(JSON.stringify({...result,writerLockReleased:true}));}
  finally{await store?.close();}
}
if(process.argv[1]&&fileURLToPath(import.meta.url)===path.resolve(process.argv[1]))run().catch(()=>{console.error(JSON.stringify({ok:false,error:'maintenance_probe_refused_or_failed',privateOutputWithheld:true,partialEvidencePreserved:true}));process.exitCode=1;});
