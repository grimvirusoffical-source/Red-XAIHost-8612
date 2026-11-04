import {expect,test} from 'bun:test';
import {mkdtempSync} from 'node:fs';
import {rmSync,realpathSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,dirname,basename} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';

// Native libSQL transaction handles can outlive Client.close() on Windows.
// Separate processes prove persistence and release all handles before cleanup.
function fixture(run:(directory:string)=>void) {
  const directory=mkdtempSync(join(tmpdir(),'host-realm-metadata-'));
  try {run(directory);} finally {
    expect(dirname(realpathSync(directory))).toBe(realpathSync(tmpdir()));
    expect(basename(directory).startsWith('host-realm-metadata-')).toBe(true);
    rmSync(directory,{recursive:true,maxRetries:5,retryDelay:100});
  }
}
function child(mode:string,directory:string) {
  const result=spawnSync(process.execPath,[fileURLToPath(new URL('./realm-ownership.fixture.ts',import.meta.url)),mode,directory],{timeout:15000,encoding:'utf8',env:{SystemRoot:process.env.SystemRoot,TEMP:process.env.TEMP,TMP:process.env.TMP,PATH:process.env.PATH}});
  expect(result.error).toBeUndefined();expect(result.signal).toBeNull();
  expect(result.stdout).toBe('');expect(result.stderr).toBe('');expect(result.status).toBe(0);
}
test('all resource tables preserve legacy ownership and fresh A/B isolation across a real process restart',()=>fixture(directory=>{
  child('create-and-isolate',directory);child('restart-and-verify',directory);
}));
test('migration refuses unlabelled internal keys atomically rather than adopting or rewriting them',()=>fixture(directory=>child('refuse-unlabelled',directory)));
test('real Host node/project/provider procedures deny cross-realm and cross-user resource use',()=>fixture(directory=>child('router-isolation',directory)),20000);
