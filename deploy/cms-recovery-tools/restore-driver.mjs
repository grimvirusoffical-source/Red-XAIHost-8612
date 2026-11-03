// Runs only inside the generated RootDirectory + PrivateNetwork sandbox.
// No seed, mutation, website cookie, admin key or live-data path is used.
import {promises as fs} from 'node:fs';
import path from 'node:path';
import net from 'node:net';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {pathToFileURL,fileURLToPath} from 'node:url';

const need=ok=>{if(!ok)throw Error('Isolated restore verification refused');};
export const digest=bytes=>createHash('sha256').update(bytes).digest('hex');
const MODE=0o077;
export async function supportInventory(root,{portableFixture=false}={}){
  const rootStat=await fs.lstat(root);need(rootStat.isDirectory()&&!rootStat.isSymbolicLink()&&(portableFixture||(rootStat.mode&MODE)===0));
  const files={},directories=[];
  async function walk(relative=''){
    const entries=(await fs.readdir(path.join(root,relative))).sort();
    for(const name of entries){
      const rel=relative?relative+'/'+name:name,filename=path.join(root,rel),s=await fs.lstat(filename);
      need(rel!=='writer.lock'&&!s.isSymbolicLink()&&(portableFixture||(s.mode&MODE)===0));
      if(s.isDirectory()){directories.push(rel);await walk(rel);}
      else{
        need(s.isFile()&&s.nlink===1);const f=await fs.open(filename,'r');
        try{
          const a=await f.stat(),h=createHash('sha256');let bytes=0;
          for await(const part of f.createReadStream({autoClose:false})){h.update(part);bytes+=part.length;}
          const b=await f.stat();need(s.ino===a.ino&&a.ino===b.ino&&s.size===a.size&&a.size===b.size&&b.size===bytes&&s.mtimeMs===b.mtimeMs);
          files[rel]={sha256:h.digest('hex'),bytes};
        }finally{await f.close();}
      }
    }
  }
  await walk();return {files,directories:directories.sort()};
}
export function compareSupport(actual,expected){
  need(JSON.stringify(actual.directories)===JSON.stringify([...expected.directories].sort()));
  need(JSON.stringify(Object.keys(actual.files).sort())===JSON.stringify(Object.keys(expected.files).sort()));
  for(const [name,record]of Object.entries(expected.files))need(actual.files[name].sha256===record.sha256&&actual.files[name].bytes===record.bytes);
}
export function mappedFetch(origin,fetchImpl=fetch){
  need(/^http:\/\/127\.0\.0\.1:\d+$/u.test(origin));
  return (url,options)=>{
    const parsed=new URL(url);need(parsed.origin==='http://127.0.0.1:17790'&&parsed.username===''&&parsed.password==='');
    need(options?.redirect==='manual'&&(!options.method||options.method==='GET'));
    return fetchImpl(origin+parsed.pathname+parsed.search,{...options,redirect:'manual'});
  };
}
function noListener(){return new Promise((resolve,reject)=>{const s=net.connect({host:'127.0.0.1',port:17791});s.setTimeout(1500);s.once('connect',()=>{s.destroy();reject(Error('Fixture port already in use'));});s.once('timeout',()=>{s.destroy();reject(Error('Fixture port state unknown'));});s.once('error',error=>error.code==='ECONNREFUSED'?resolve():reject(Error('Fixture port state unknown')));});}
async function main(){
  need(process.platform==='linux'&&process.getuid()===986&&process.getgid()===976&&process.argv.length===2&&process.cwd()==='/work');
  need(await fs.realpath('/work')==='/work'&&await fs.realpath('/site')==='/site'&&await fs.realpath('/credential')==='/credential');
  // No /proc mount is provided: /proc/PID/root cannot expose live host files.
  try{need((await fs.readdir('/proc')).length===0);}catch(e){need(e.code==='EACCES'||e.code==='ENOENT');}
  for(const absent of ['/var/lib/red-xai-site','/opt/redx-db-website-ec6e9798','/srv/red-xai']){
    let missing=false;try{await fs.lstat(absent);}catch(e){missing=e.code==='ENOENT'||e.code==='EACCES';}need(missing);
  }
  const expected=JSON.parse(await fs.readFile('/work/expected.json','utf8'));
  need(expected.schema===1&&expected.kind==='cms-native-support-pair'&&expected.nativeRevision==='8512e1704e589d24535e08082c3ab9f832d7655d');
  need(digest(await fs.readFile('/tools/redxd'))===expected.nativeBinarySha256);
  need(digest(await fs.readFile('/tools/probe.mjs'))===expected.probeSha256);
  need(digest(await fs.readFile('/tools/restore-driver.mjs'))===expected.restoreDriverSha256);
  for(const [name,hash]of Object.entries(expected.moduleSha256))need(digest(await fs.readFile('/site/'+name))===hash);
  compareSupport(await supportInventory('/work/support'),expected.support);
  const dbs=(await fs.readdir('/work/native/dbs',{withFileTypes:true}));
  need(dbs.every(x=>x.isDirectory()&&!x.isSymbolicLink())&&JSON.stringify(dbs.map(x=>x.name).sort())===JSON.stringify(expected.databases));
  const key=await fs.lstat('/credential');need(key.isFile()&&!key.isSymbolicLink()&&(key.mode&MODE)===0&&key.uid===986&&key.gid===976&&key.nlink===1);
  await noListener();
  const child=spawn('/tools/redxd',['run','--config','/work/fixture.toml','--no-open'],{cwd:'/work',env:{PATH:'/usr/bin:/bin'},stdio:'ignore'});
  let exited=false,exitCode=null,store=null;
  const stopped=new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',code=>{exited=true;exitCode=code;resolve();});});stopped.catch(()=>{});
  const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
  let verified;
  try{
    let ready=false;
    for(let i=0;i<80&&!exited;i++){
      try{const r=await fetch('http://127.0.0.1:17791/healthz',{redirect:'manual',signal:AbortSignal.timeout(300)});if(r.status===200){const v=await r.json();ready=v.status==='ok'&&v.role==='leader';if(ready)break;}}catch{}
      await pause(100);
    }
    need(ready&&!exited);
    const {RedXCmsStore}=await import('file:///site/redx-cms-store.mjs');
    const {processImage}=await import('file:///site/site-backend.mjs');
    const {verifyProbe,verifyWriterLockReleased}=await import('file:///tools/probe.mjs');
    store=await new RedXCmsStore({directory:'/work/support',database:'website_cms',keyFile:'/credential',durability:'group',singleWriter:true,fetchImpl:mappedFetch('http://127.0.0.1:17791')}).init();
    // init/read enforce all six physical key scope/role assertions on this host.
    verified=await verifyProbe(store,{processImage});
    for(const [name,value]of Object.entries(expected.probe))if(name!=='writerLockReleased')need(verified[name]===value);
    await store.close();store=null;await verifyWriterLockReleased('/work/support');
    compareSupport(await supportInventory('/work/support'),expected.support);
  }finally{
    await store?.close();
    if(!exited)child.kill('SIGTERM');
    const timed=await Promise.race([stopped.then(()=>false),pause(10000).then(()=>true)]);
    if(timed){child.kill('SIGKILL');await stopped;throw Error('Fixture graceful shutdown failed');}
  }
  need(exited&&exitCode===0);
  await noListener();
  compareSupport(await supportInventory('/work/support'),expected.support);
  const result={...verified,backupRestoreVerified:true,fullSupportMatched:true,fullAuditSha256:expected.fullAuditSha256,daemonStopped:true,allDatabasesPreserved:true,databaseCount:expected.databases.length,nativeRevision:expected.nativeRevision,liveDataOpened:false,websiteOwnerApiVerified:false,normalAccountIsolationVerified:false};
  const f=await fs.open('/work/restore-result.json','wx',0o600);try{await f.writeFile(JSON.stringify(result)+'\n');await f.sync();}finally{await f.close();}
  const dir=await fs.open('/work','r');try{await dir.sync();}finally{await dir.close();}
}
if(process.argv[1]&&fileURLToPath(import.meta.url)===path.resolve(process.argv[1]))main().catch(()=>{process.exitCode=1;});
