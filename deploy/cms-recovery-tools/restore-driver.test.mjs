import test from 'node:test';
import assert from 'node:assert/strict';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {digest,supportInventory,compareSupport,mappedFetch,createStatusRecorder} from './restore-driver.mjs';

test('phase diagnostics allow only fixed words and refuse existing paths',async t=>{
  const root=await fs.mkdtemp(path.join(os.tmpdir(),'cms-restore-driver-test-'));
  t.after(async()=>{assert.equal(path.dirname(await fs.realpath(root)),await fs.realpath(os.tmpdir()));await fs.rm(root,{recursive:true,force:false});});
  const recorder=await createStatusRecorder(root);
  await recorder.record('preflight','entered');await recorder.record('store-open','failed');
  await assert.rejects(recorder.record('private exception text','failed'));
  await assert.rejects(recorder.record('preflight','private exception text'));
  await recorder.close();
  assert.deepEqual((await fs.readFile(path.join(root,'restore-status.jsonl'),'utf8')).trim().split('\n').map(x=>JSON.parse(x)),[{phase:'preflight',status:'entered'},{phase:'store-open',status:'failed'}]);
  await assert.rejects(createStatusRecorder(root));
});

test('read-only remap accepts only the fixed native origin and refuses writes/redirect relaxation',async()=>{
  let observed;
  const call=mappedFetch('http://127.0.0.1:17791',async(...args)=>{observed=args;return 'fixture';});
  assert.equal(await call('http://127.0.0.1:17790/v1/ui/whoami',{redirect:'manual',headers:{}}),'fixture');
  assert.equal(observed[0],'http://127.0.0.1:17791/v1/ui/whoami');
  for(const [url,options]of [['http://example.com/v1/ui/whoami',{redirect:'manual'}],['http://127.0.0.1:17790/v1/path',{redirect:'manual',method:'PUT'}],['http://127.0.0.1:17790/v1/path',{redirect:'follow'}]])assert.throws(()=>call(url,options));
});

test('full support comparison detects an audit suffix changed after the probe prefix',async t=>{
  const root=await fs.mkdtemp(path.join(os.tmpdir(),'cms-restore-driver-test-'));
  t.after(async()=>{assert.equal(path.dirname(await fs.realpath(root)),await fs.realpath(os.tmpdir()));assert.ok(path.basename(root).startsWith('cms-restore-driver-test-'));await fs.rm(root,{recursive:true,force:false});});
  await fs.mkdir(path.join(root,'media'),{mode:0o700});
  await fs.writeFile(path.join(root,'audit.jsonl'),'prefix\nsuffix\n',{mode:0o600});
  await fs.writeFile(path.join(root,'media','fixture.webp'),'fixture',{mode:0o600});
  const expected=await supportInventory(root,{portableFixture:process.platform==='win32'});
  assert.doesNotThrow(()=>compareSupport(expected,expected));
  await fs.appendFile(path.join(root,'audit.jsonl'),'new suffix\n');
  const changed=await supportInventory(root,{portableFixture:process.platform==='win32'});
  assert.throws(()=>compareSupport(changed,expected));
  await fs.writeFile(path.join(root,'writer.lock'),'fixture',{mode:0o600});
  await assert.rejects(supportInventory(root,{portableFixture:true}));
});

test('support hardlinks are refused without reading their contents',async t=>{
  const root=await fs.mkdtemp(path.join(os.tmpdir(),'cms-restore-driver-test-'));
  t.after(async()=>{assert.equal(path.dirname(await fs.realpath(root)),await fs.realpath(os.tmpdir()));await fs.rm(root,{recursive:true,force:false});});
  await fs.writeFile(path.join(root,'first'),'fixture',{mode:0o600});
  await fs.link(path.join(root,'first'),path.join(root,'second'));
  await assert.rejects(supportInventory(root,{portableFixture:true}));
});
