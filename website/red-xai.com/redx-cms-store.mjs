import { promises as fs } from 'node:fs';
import path from 'node:path';
import { PrivateStore, boundedJSON, fail, plainObject, validatePost } from './site-backend.mjs';
const ORIGIN='http://127.0.0.1:17790';
const EMPTY=()=>({schema:1,posts:[],media:{},preferences:{}});
function validateState(value,now) {
  if(!plainObject(value)||value.schema!==1||!Array.isArray(value.posts)||value.posts.length>5000||!plainObject(value.media)||!plainObject(value.preferences))fail(503,'cms_storage_invalid');
  const state=structuredClone(value);
  for(const p of state.posts) {
    if(!plainObject(p)||!/^p_[a-f0-9]{32}$/u.test(p.id)||!['draft','published','deleted'].includes(p.status))fail(503,'cms_storage_invalid');
    try{p.body=validatePost({...Object.fromEntries(['title','category','body','startsAt','endsAt'].filter(k=>p[k]!==undefined).map(k=>[k,p[k]])),status:p.status==='deleted'?'draft':p.status},state.media,p,now).body;}catch{fail(503,'cms_storage_invalid');}
  }
  return state;
}
/** Native path API and one serialized gateway writer; not HA/CAS.
 * durability is an operator-verified deployment prerequisite, not proof supplied
 * by the path API. A sequence acknowledgement alone does not prove fsync.
 */
export class RedXCmsStore extends PrivateStore {
  constructor({directory,database,keyFile,durability,singleWriter=false,fetchImpl=fetch,now=Date.now,testMode=false}) {
    super(directory,{testMode,now});
    if(!/^[a-z0-9][a-z0-9_-]{0,62}$/u.test(database||'')||['default','admin','redx_accounts','redxai_accounts'].includes(database)||!path.isAbsolute(keyFile||'')||!['sync','group'].includes(durability)||singleWriter!==true)throw new Error('Reviewed native CMS storage configuration required');
    this.database=database;this.keyFile=keyFile;this.durability=durability;this.fetchImpl=fetchImpl;
    this.prefix='site-cms.red-xai.db';this.valuePath=this.prefix+':SiteState[ID=1].Payload';
    this.url=ORIGIN+'/v1/db/'+database+'/path/'+encodeURIComponent(this.valuePath);
    this.key=null;this.envelope=null;this.lock=null;
  }
  async init() {
    await fs.mkdir(this.directory,{recursive:true,mode:0o700});
    const st=await fs.lstat(this.directory);
    if(!st.isDirectory()||st.isSymbolicLink()||(!this.testMode&&process.platform!=='win32'&&(st.mode&0o077)))throw new Error('Private CMS support directory required');
    await fs.mkdir(path.join(this.directory,'media'),{mode:0o700}).catch(e=>{if(e.code!=='EEXIST')throw e;});
    const m=await fs.lstat(path.join(this.directory,'media'));if(!m.isDirectory()||m.isSymbolicLink())throw new Error('Invalid CMS media directory');
    const ks=await fs.lstat(this.keyFile);
    if(!ks.isFile()||ks.isSymbolicLink()||ks.size>1024||(!this.testMode&&process.platform!=='win32'&&(ks.mode&0o077)))throw new Error('Private scoped CMS key file required');
    this.key=(await fs.readFile(this.keyFile,'utf8')).trim();
    if(!/^rx_live_[A-Za-z0-9]{43}$/u.test(this.key))throw new Error('Invalid scoped CMS key file');
    this.lock=await fs.open(path.join(this.directory,'writer.lock'),'wx',0o600);
    try {
      await this.lock.writeFile(JSON.stringify({pid:process.pid,startedAt:new Date(this.now()).toISOString()})+'\n');await this.lock.sync();
      await this.validateKey();const loaded=await this.load();this.envelope=loaded;this.state=loaded.state;
      return this;
    }catch(e){await this.close();throw e;}
  }
  async close() {
    if(this.lock){await this.lock.close();this.lock=null;await fs.unlink(path.join(this.directory,'writer.lock')).catch(()=>{});}
    this.key=null;
  }
  async call(url,init={}) {
    let r;try{r=await this.fetchImpl(url,{...init,headers:{accept:'application/json',authorization:'Bearer '+this.key,...init.headers},redirect:'manual',cache:'no-store',signal:AbortSignal.timeout(10000)});}catch{fail(503,'cms_storage_unavailable');}
    return r;
  }
  async validateKey() {
    const r=await this.call(ORIGIN+'/v1/ui/whoami');if(r.status!==200)fail(503,'cms_storage_unavailable');
    const key=await boundedJSON(r);
    if(key.scope!=='write'||key.db!==this.database||key.prefix!==this.prefix||key.host_admin!==false||key.owner!==false||key.role!=='leader')fail(503,'cms_scope_mismatch');
  }
  async load(waitSeq=undefined) {
    const r=await this.call(this.url,{headers:waitSeq===undefined?{}:{'x-redx-wait-seq':String(waitSeq)}});
    if(r.status===404){
      // An empty, newly provisioned namespace is valid only before its first
      // write. Losing an existing CMS record must never silently reset it.
      if(waitSeq!==undefined||this.envelope?.revision>0)fail(503,'cms_storage_missing');
      return {schema:1,revision:0,state:EMPTY()};
    }
    if(r.status!==200)fail(503,'cms_storage_unavailable');
    const data=await boundedJSON(r,32*1024*1024);
    let result;try{if(data.path!==this.valuePath||typeof data.value!=='string')throw Error();result=JSON.parse(JSON.parse(data.value));}catch{fail(503,'cms_storage_invalid');}
    if(!plainObject(result)||result.schema!==1||!Number.isSafeInteger(result.revision)||result.revision<1)fail(503,'cms_storage_invalid');
    return {schema:1,revision:result.revision,state:validateState(result.state,this.now())};
  }
  async read() {
    const task=this.queue.then(async()=>{await this.validateKey();const loaded=await this.load();this.envelope=loaded;this.state=loaded.state;return this.snapshot();});
    this.queue=task.catch(()=>{});return task;
  }
  async mutate(event,identity,target,change) {
    const task=this.queue.then(async()=>{
      await this.validateKey();const current=await this.load();
      if(JSON.stringify(current)!==JSON.stringify(this.envelope))fail(409,'cms_writer_conflict');
      await this.audit(event+'.attempt',identity,target);
      const next=structuredClone(current.state),result=await change(next);
      if(current.revision>=Number.MAX_SAFE_INTEGER)fail(503,'cms_revision_limit');
      const envelope={schema:1,revision:current.revision+1,state:validateState(next,this.now())},body=JSON.stringify(JSON.stringify(envelope));
      if(Buffer.byteLength(body)>32*1024*1024)fail(413,'cms_storage_limit');
      const response=await this.call(this.url,{method:'PUT',headers:{'content-type':'text/x-redx-box'},body});
      if(response.status!==200)fail(503,'cms_storage_unavailable');
      const ack=await boundedJSON(response);
      if(ack.ok!==true||!Number.isSafeInteger(ack.seq)||ack.seq<1)fail(503,'cms_write_unverified');
      const readback=await this.load(ack.seq);
      if(JSON.stringify(readback)!==JSON.stringify(envelope))fail(503,'cms_write_unverified');
      this.envelope=readback;this.state=readback.state;
      await this.audit(event+'.committed',identity,target);
      return result;
    });
    this.queue=task.catch(()=>{});return task;
  }
}
