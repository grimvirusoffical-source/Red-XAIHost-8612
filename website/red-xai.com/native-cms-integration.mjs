// Actual native RedX + CMS-driver integration, using disposable loopback hosts.
// No production ports, accounts, cookies, keys, engine files or configuration.
import assert from 'node:assert/strict';
import {spawn,execFileSync} from 'node:child_process';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
import {RedXCmsStore} from './redx-cms-store.mjs';
import {processImage} from './site-backend.mjs';
import {seedProbe,verifyProbe} from './cms-maintenance-probe.mjs';

const HERE=path.dirname(fileURLToPath(import.meta.url));
const expectedCommit='8512e1704e589d24535e08082c3ab9f832d7655d';
const requestedDurability=process.argv[3]||'sync';
assert.ok(['sync','group-default'].includes(requestedDurability),'supported disposable fixture durability');
const effectiveDurability=requestedDurability==='group-default'?'group':'sync';
const rawDurability=requestedDurability==='group-default'?'':'sync';
const checkout=path.resolve(process.argv[2]||'');
if(!process.argv[2]||await fs.realpath(checkout)!==checkout)throw Error('Explicit pinned RedX checkout required');
assert.equal(execFileSync('git',['rev-parse','HEAD'],{cwd:checkout,encoding:'utf8'}).trim(),expectedCommit,'pinned native source revision');
assert.equal(execFileSync('git',['status','--porcelain'],{cwd:checkout,encoding:'utf8'}).trim(),'','unchanged native source checkout');
const runtimeEnv=Object.fromEntries(Object.entries(process.env).filter(([k])=>['PATH','SYSTEMROOT','WINDIR','TEMP','TMP','LANG'].includes(k.toUpperCase())));
const buildPaths=Object.fromEntries(Object.entries(process.env).filter(([key])=>['HOME','USERPROFILE','APPDATA','LOCALAPPDATA','GOPATH','GOMODCACHE','PROGRAMFILES','PROGRAMFILES(X86)'].includes(key.toUpperCase())));
const buildEnv={...runtimeEnv,...buildPaths,CGO_ENABLED:'0',GOFLAGS:'',GOWORK:'off',GOENV:'off',GOTOOLCHAIN:'local'};
const root=await fs.mkdtemp(path.join(os.tmpdir(),'redx-native-cms-'));
await fs.chmod(root,0o700);
await fs.writeFile(path.join(root,'fixture-marker'),'isolated-native-cms-v1\n',{mode:0o600,flag:'wx'});
for(const name of ['go-cache','go-tmp'])await fs.mkdir(path.join(root,name),{mode:0o700});
buildEnv.GOCACHE=path.join(root,'go-cache');buildEnv.GOTMPDIR=path.join(root,'go-tmp');
const binary=path.join(root,process.platform==='win32'?'native-cms-fixture.exe':'native-cms-fixture');
const live=new Map();let cms,maintenance,phase='build';
async function bounded(promise,ms){
  let timer;try{return await Promise.race([promise,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('Fixture shutdown deadline')),ms);})]);}finally{clearTimeout(timer);}
}
function launch(mode,data){
  const child=spawn(binary,[],{cwd:root,env:runtimeEnv,stdio:['pipe','pipe','pipe'],windowsHide:true});
  let buffer='',settled=false;
  const exited=new Promise((resolve,reject)=>{child.once('error',()=>reject(Error('Native fixture process failed')));child.once('exit',code=>{live.delete(child);code===0?resolve():reject(Error('Native fixture process failed'));});});exited.catch(()=>{});
  live.set(child,exited);
  const ready=new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(Error('Native fixture readiness timeout')),15000);
    child.stdout.on('data',chunk=>{buffer+=chunk;if(buffer.length>8192){clearTimeout(timer);reject(Error('Native fixture output too large'));child.kill();return;}const line=buffer.indexOf('\n');if(line>=0&&!settled){settled=true;clearTimeout(timer);try{resolve(JSON.parse(buffer.slice(0,line)));}catch{reject(Error('Native fixture readiness invalid'));}}});
    exited.then(()=>{if(!settled){clearTimeout(timer);reject(Error('Native fixture exited before readiness'));}},error=>{clearTimeout(timer);reject(error);});
  });
  // Consume errors without writing private child output into build logs.
  child.stderr.resume();
  child.stdin.write(JSON.stringify({Root:root,Mode:mode,Data:data,Durability:requestedDurability})+'\n');
  return {child,ready,exited,stop:async()=>{child.stdin.end('stop\n');try{await bounded(exited,10000);}catch(error){child.kill('SIGKILL');await bounded(exited.catch(()=>{}),3000);throw error;}}};
}
async function admin(origin,key,method,route,body){
  const response=await fetch(origin+route,{method,headers:{authorization:'Bearer '+key,'content-type':'application/json',...(route==='/v1/keys'?{'x-redx-confirm':'createKey'}:{})},body:JSON.stringify(body),redirect:'error',signal:AbortSignal.timeout(10000)});
  assert.ok(response.status===200||response.status===201,'native fixture administration failed');
  return response.json();
}
function openStore(origin,keyFile,directory,database='website_cms_fixture'){
  return new RedXCmsStore({directory,database,keyFile,durability:effectiveDurability,singleWriter:true,testMode:true,
    fetchImpl:(url,options)=>{const parsed=new URL(url);assert.equal(parsed.origin,'http://127.0.0.1:17790');return fetch(origin+parsed.pathname+parsed.search,options);}}).init();
}
try{
  execFileSync('go',['build','-trimpath','-o',binary,path.join(HERE,'fixtures','native-cms-host.go')],{cwd:checkout,env:buildEnv,stdio:['ignore','pipe','pipe'],timeout:120000,windowsHide:true});
  phase='start';
  const first=launch('serve','native'),ready=await first.ready;
  assert.equal(ready.nativeEngine,true);assert.equal(ready.durability,effectiveDurability);assert.equal(ready.rawDurability,rawDurability);assert.match(ready.url,/^http:\/\/127\.0\.0\.1:\d+$/u);
  const observedConfig=await admin(ready.url,ready.key,'GET','/v1/admin/config');
  assert.equal(observedConfig.config.engine_durability,rawDurability,'real native config keeps its raw default value');
  phase='create-database';await admin(ready.url,ready.key,'POST','/v1/dbs',{name:'website_cms_fixture'});
  const scoped=await admin(ready.url,ready.key,'POST','/v1/keys',{scope:'write',db:'website_cms_fixture',prefix:'site-cms.red-xai.db'});
  assert.match(scoped.key,/^rx_live_[A-Za-z0-9]{43}$/u);
  const keyFile=path.join(root,'cms.key'),support=path.join(root,'support');
  await fs.writeFile(keyFile,scoped.key,{mode:0o600,flag:'wx'});
  phase='initialize-cms';cms=await openStore(ready.url,keyFile,support);
  phase='maintenance-service-seed';
  const maintenanceDb='website_cms_probe_fixture',maintenanceKeyFile=path.join(root,'maintenance.key'),maintenanceSupport=path.join(root,'maintenance-support');
  await admin(ready.url,ready.key,'POST','/v1/dbs',{name:maintenanceDb});
  const maintenanceKey=await admin(ready.url,ready.key,'POST','/v1/keys',{scope:'write',db:maintenanceDb,prefix:'site-cms.red-xai.db'});
  await fs.writeFile(maintenanceKeyFile,maintenanceKey.key,{mode:0o600,flag:'wx'});
  maintenance=await openStore(ready.url,maintenanceKeyFile,maintenanceSupport,maintenanceDb);
  const maintenanceEvidence=await seedProbe(maintenance,{processImage});
  assert.equal(maintenanceEvidence.websiteOwnerApiVerified,false);assert.equal(maintenanceEvidence.normalAccountIsolationVerified,false);
  await maintenance.close();maintenance=null;
  const identity={id:'isolated-cms-fixture-owner'};
  const at=new Date().toISOString();
  phase='write-post';await cms.mutate('post.create',identity,'p_'+'a'.repeat(32),state=>{state.posts.push({id:'p_'+'a'.repeat(32),title:'Native fixture draft',category:'announcements',status:'draft',createdAt:at,updatedAt:at,body:{type:'doc',children:[{type:'paragraph',children:[{type:'text',text:'Preserve this native Box payload.'}]}]}});});
  await Promise.all(['alpha','beta'].map((id,i)=>cms.mutate('preferences.update',{id},'self',state=>{state.preferences[id]={theme:i?'light':'dark'};})));
  phase='write-media';
  const sharp=(await import('sharp')).default;
  const png=await sharp({create:{width:2,height:2,channels:3,background:'#dc143c'}}).png().toBuffer();
  const image=await processImage(png,'image/png'),media=await cms.addMedia(image.bytes,image,identity);
  await cms.mutate('post.update',identity,'p_'+'a'.repeat(32),state=>{state.posts[0].body.children.push({type:'image',mediaId:media.id,alt:'Native fixture image'});});
  const expected=await cms.read();assert.equal(expected.preferences.alpha.theme,'dark');assert.equal(expected.preferences.beta.theme,'light');
  await cms.close();cms=null;await first.stop();
  phase='restart';const second=launch('serve','native'),secondReady=await second.ready;
  maintenance=await openStore(secondReady.url,maintenanceKeyFile,maintenanceSupport,maintenanceDb);
  assert.deepEqual(await verifyProbe(maintenance,{processImage}),maintenanceEvidence);await maintenance.close();maintenance=null;
  cms=await openStore(secondReady.url,keyFile,support);assert.deepEqual(await cms.read(),expected);
  await cms.close();cms=null;await second.stop();
  const restoredSupport=path.join(root,'restored-support');
  assert.deepEqual((await fs.readdir(support)).sort(),['audit.jsonl','media']);
  assert.deepEqual(await fs.readdir(path.join(support,'media')),[media.id+'.webp']);
  await fs.cp(support,restoredSupport,{recursive:true,errorOnExist:true,force:false});
  const restoredMaintenanceSupport=path.join(root,'restored-maintenance-support');
  await fs.cp(maintenanceSupport,restoredMaintenanceSupport,{recursive:true,errorOnExist:true,force:false});
  const supportAudit=await fs.readFile(path.join(support,'audit.jsonl'));
  assert.deepEqual(await fs.readFile(path.join(restoredSupport,'audit.jsonl')),supportAudit);
  phase='offline-restore';const restore=launch('backup-restore','');assert.equal((await restore.ready).backupRestored,true);await restore.exited;
  phase='restore-readback';const restored=launch('serve','restored'),restoredReady=await restored.ready;
  maintenance=await openStore(restoredReady.url,maintenanceKeyFile,restoredMaintenanceSupport,maintenanceDb);
  assert.deepEqual(await verifyProbe(maintenance,{processImage}),maintenanceEvidence);await maintenance.close();maintenance=null;
  cms=await openStore(restoredReady.url,keyFile,restoredSupport);assert.deepEqual(await cms.read(),expected);
  assert.deepEqual(await cms.mediaBytes(media.id),image.bytes);
  await cms.close();cms=null;await restored.stop();
  const archive=await fs.readFile(path.join(root,'native-backup.tgz'));
  assert.equal(execFileSync('git',['status','--porcelain'],{cwd:checkout,encoding:'utf8'}).trim(),'','native source remained unchanged');
  console.log(JSON.stringify({ok:true,nativeRevision:expectedCommit,nativeEngine:true,engineCodeChanged:false,durability:effectiveDurability,rawDurability,configObservationMatched:true,draftPostRoundTrip:true,queuedPreferenceWrites:2,realWebpMediaRoundTrip:true,supportAuditCopyMatched:true,daemonRestartReadback:true,offlineBackupRestoreReadback:true,maintenanceServiceProbeNativeRestart:true,maintenanceServiceProbeOfflineRestore:true,websiteOwnerApiVerified:false,normalAccountIsolationVerified:false,backupSha256:createHash('sha256').update(archive).digest('hex'),productionAccess:false}));
}catch(error){
  process.exitCode=1;console.error(JSON.stringify({error:'Native CMS integration failed',phase,code:error?.code||'fixture_error',privateOutputWithheld:true}));
}finally{
  await cms?.close().catch(()=>{});
  await maintenance?.close().catch(()=>{});
  const stopping=[...live.entries()];for(const [child]of stopping)child.kill('SIGKILL');
  const stopped=await Promise.allSettled(stopping.map(([,exited])=>bounded(exited.catch(()=>{}),3000)));
  // Never recursively remove a computed directory without resolving its bounds.
  const real=await fs.realpath(root),parent=await fs.realpath(os.tmpdir());
  if(stopped.every(result=>result.status==='fulfilled')&&real===root&&path.dirname(real)===parent&&path.basename(real).startsWith('redx-native-cms-')&&await fs.readFile(path.join(real,'fixture-marker'),'utf8')==='isolated-native-cms-v1\n')await fs.rm(real,{recursive:true,force:false,maxRetries:5,retryDelay:100});
  else {process.exitCode=1;console.error('Fixture cleanup refused: a child did not stop or its directory bounds changed.');}
}
