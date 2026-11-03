// Disposable actual native daemon-IPC backup, fresh restore and same-key check.
// Synthetic child bootstrap output stays in private pipes and is never logged.
import assert from 'node:assert/strict';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn,execFileSync} from 'node:child_process';
import {pathToFileURL,fileURLToPath} from 'node:url';
import {createHash} from 'node:crypto';
const here=path.dirname(fileURLToPath(import.meta.url)),repository=path.resolve(here,'../..');
// Source paths only; all state, credentials and listeners remain disposable.
assert.ok(process.argv[2]&&process.argv.length>=3&&process.argv.length<=4,'Usage: node native-ipc-check.mjs ABS_NATIVE_SOURCE [ABS_SITE_SOURCE_WITH_DEPENDENCIES]');
const source=path.resolve(process.argv[2]),site=path.resolve(process.argv[3]||path.join(repository,'website/red-xai.com'));
assert.equal(await fs.realpath(source),source,'Unlinked explicit native source required');
assert.equal(await fs.realpath(site),site,'Unlinked site source required');
const revision='8512e1704e589d24535e08082c3ab9f832d7655d';
const hash=b=>createHash('sha256').update(b).digest('hex');
const env=Object.fromEntries(Object.entries(process.env).filter(([k])=>['PATH','SYSTEMROOT','WINDIR','TEMP','TMP','LANG'].includes(k.toUpperCase())));
const build={...env,CGO_ENABLED:'0',GOFLAGS:'',GOWORK:'off',GOENV:'off',GOTOOLCHAIN:'local',GOPROXY:'off'};
for(const [k,v]of Object.entries(process.env))if(['HOME','USERPROFILE','APPDATA','LOCALAPPDATA','GOPATH','GOMODCACHE','GOROOT'].includes(k.toUpperCase()))build[k]=v;
const root=await fs.mkdtemp(path.join(os.tmpdir(),'redx-native-cms-ipc-'));
await fs.chmod(root,0o700);await fs.writeFile(path.join(root,'fixture-marker'),'isolated-native-cms-v1\n',{mode:0o600,flag:'wx'});
const suffix=process.platform==='win32'?'.exe':'',hostBin=path.join(root,'native-host'+suffix),cli=path.join(root,'redx'+suffix);
const live=new Set();let store,phase='source';
function launch(data){
  const child=spawn(hostBin,[],{cwd:root,env,stdio:['pipe','pipe','pipe'],windowsHide:true});live.add(child);
  const exited=new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',code=>{live.delete(child);code===0?resolve():reject(Error('Private native fixture failed'));});});exited.catch(()=>{});
  let buffer='';
  const ready=new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>reject(Error('Fixture readiness timeout')),15000);
    child.stdout.on('data',part=>{
      buffer+=part;
      if(buffer.length>8192){clearTimeout(timer);reject(Error('Private output limit'));return;}
      const n=buffer.indexOf('\n');
      if(n>=0){clearTimeout(timer);try{resolve(JSON.parse(buffer.slice(0,n)));}catch{reject(Error('Invalid readiness'));}}
    });
    exited.catch(reject);
  });
  child.stderr.resume();child.stdin.write(JSON.stringify({Root:root,Mode:'serve',Data:data,Durability:'group-default'})+'\n');
  return {ready,exited,stop:async()=>{child.stdin.end('stop\n');await Promise.race([exited,new Promise((_,reject)=>{const timer=setTimeout(()=>reject(Error('Stop timeout')),10000);timer.unref();})]);}};
}
async function admin(origin,key,method,route,body){const r=await fetch(origin+route,{method,headers:{authorization:'Bearer '+key,'content-type':'application/json',...(route==='/v1/keys'?{'x-redx-confirm':'createKey'}:{})},body:body===undefined?undefined:JSON.stringify(body),redirect:'manual'});assert.ok(r.status===200||r.status===201);return r.json();}
try{
  assert.equal(execFileSync('git',['rev-parse','HEAD'],{cwd:source,encoding:'utf8'}).trim(),revision);
  assert.equal(execFileSync('git',['status','--porcelain'],{cwd:source,encoding:'utf8'}).trim(),'');
  execFileSync('go',['build','-trimpath','-o',hostBin,path.join(site,'fixtures/native-cms-host.go')],{cwd:source,env:build,stdio:'pipe',windowsHide:true,timeout:120000});
  execFileSync('go',['build','-trimpath','-o',cli,'./cmd/redx'],{cwd:source,env:build,stdio:'pipe',windowsHide:true,timeout:120000});
  const {RedXCmsStore}=await import(pathToFileURL(path.join(site,'redx-cms-store.mjs')));
  const {processImage}=await import(pathToFileURL(path.join(site,'site-backend.mjs')));
  const {seedProbe,verifyProbe,verifyWriterLockReleased}=await import(pathToFileURL(path.join(site,'cms-maintenance-probe.mjs')));
  const {supportInventory,compareSupport,mappedFetch}=await import('./restore-driver.mjs');
  phase='seed';const first=launch('native'),ready=await first.ready;
  for(const name of ['website_cms','fl_studio_db','fixture_a','fixture_b'])await admin(ready.url,ready.key,'POST','/v1/dbs',{name});
  const key=await admin(ready.url,ready.key,'POST','/v1/keys',{scope:'write',db:'website_cms',prefix:'site-cms.red-xai.db'});
  const keyFile=path.join(root,'cms.key'),support=path.join(root,'support');await fs.writeFile(keyFile,key.key,{mode:0o600,flag:'wx'});
  const open=(origin,directory)=>new RedXCmsStore({directory,database:'website_cms',keyFile,durability:'group',singleWriter:true,testMode:true,fetchImpl:mappedFetch(origin)}).init();
  store=await new RedXCmsStore({directory:support,database:'website_cms',keyFile,durability:'group',singleWriter:true,testMode:true,fetchImpl:(url,options)=>{const p=new URL(url);assert.equal(p.origin,'http://127.0.0.1:17790');return fetch(ready.url+p.pathname+p.search,options);}}).init();
  const expected=await seedProbe(store,{processImage});await store.close();store=null;await verifyWriterLockReleased(support);
  const supportBefore=await supportInventory(support,{portableFixture:process.platform==='win32'}),auditBefore=await fs.readFile(path.join(support,'audit.jsonl'));
  phase='live-daemon-ipc-backup';const archive=path.join(root,'native.tgz');
  execFileSync(cli,['backup','--offline','--data-dir',path.join(root,'native'),'--out',archive],{cwd:root,env,stdio:'pipe',windowsHide:true,timeout:45000});
  // The original daemon remains alive and readable after its coordinated backup.
  store=await open(ready.url,support);assert.deepEqual(await verifyProbe(store,{processImage}),expected);await store.close();store=null;
  compareSupport(await supportInventory(support,{portableFixture:process.platform==='win32'}),supportBefore);
  const restoredSupport=path.join(root,'restored-support');await fs.cp(support,restoredSupport,{recursive:true,force:false,errorOnExist:true});await fs.chmod(restoredSupport,0o700);
  phase='offline-fresh-restore';
  execFileSync(cli,['restore',archive,'--data-dir',path.join(root,'restored')],{cwd:root,env,stdio:'pipe',windowsHide:true,timeout:45000});
  assert.equal((await fs.readdir(path.join(root,'restored','dbs'))).length,5);
  for(const name of ['website_cms','fl_studio_db'])assert.ok((await fs.stat(path.join(root,'restored','dbs',name))).isDirectory());
  const restored=launch('restored'),restoredReady=await restored.ready;
  phase='same-scoped-key-restored-readback';store=await open(restoredReady.url,restoredSupport);
  assert.deepEqual(await verifyProbe(store,{processImage}),expected);await store.close();store=null;
  compareSupport(await supportInventory(restoredSupport,{portableFixture:process.platform==='win32'}),supportBefore);
  assert.deepEqual(await fs.readFile(path.join(restoredSupport,'audit.jsonl')),auditBefore);
  await restored.stop();await first.stop();
  assert.equal(execFileSync('git',['status','--porcelain'],{cwd:source,encoding:'utf8'}).trim(),'');
  console.log(JSON.stringify({ok:true,nativeRevision:revision,liveDaemonIpcBackup:true,nativeCliSha256:hash(await fs.readFile(cli)),archiveSha256:hash(await fs.readFile(archive)),sameScopedKeyRestored:true,sixScopeAssertions:true,allFiveDatabasesPreserved:true,flStudioPreserved:true,fullAuditAndSupportMatched:true,sourceUnchanged:true,productionAccess:false,systemdSandboxTested:false}));
}catch{process.exitCode=1;console.error(JSON.stringify({ok:false,phase,privateOutputWithheld:true,productionAccess:false}));}
finally{
  await store?.close().catch(()=>{});
  for(const child of live)child.kill('SIGKILL');
  if(live.size)await new Promise(resolve=>setTimeout(resolve,1000));
  const actual=await fs.realpath(root),parent=await fs.realpath(os.tmpdir());
  if(live.size===0&&path.dirname(actual)===parent&&path.basename(actual).startsWith('redx-native-cms-ipc-')&&await fs.readFile(path.join(actual,'fixture-marker'),'utf8')==='isolated-native-cms-v1\n')await fs.rm(actual,{recursive:true,force:false,maxRetries:5,retryDelay:100});
  else{process.exitCode=1;console.error('Private fixture retained: process exit or path bounds not verified.');}
}
