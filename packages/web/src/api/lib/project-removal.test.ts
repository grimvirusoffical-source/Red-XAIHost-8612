import {expect,test} from 'bun:test';
import {mkdtempSync,rmSync,realpathSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,dirname,basename} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';

test('actual project RPC and node HTTP teardown retain payloads, isolate tenants and recover across restart',()=>{
  const directory=mkdtempSync(join(tmpdir(),'host-project-removal-'));
  try {
    for(const mode of ['start','restart']) {
      const result=spawnSync(process.execPath,[fileURLToPath(new URL('./project-removal.fixture.ts',import.meta.url)),mode,directory],{timeout:25000,encoding:'utf8',env:{SystemRoot:process.env.SystemRoot,TEMP:process.env.TEMP,TMP:process.env.TMP,PATH:process.env.PATH}});
      expect(result.error).toBeUndefined();expect(result.signal).toBeNull();
      expect(result.stderr).toBe('');expect(result.stdout).toBe('');expect(result.status).toBe(0);
    }
  } finally {
    expect(dirname(realpathSync(directory))).toBe(realpathSync(tmpdir()));
    expect(basename(directory).startsWith('host-project-removal-')).toBe(true);
    rmSync(directory,{recursive:true,maxRetries:5,retryDelay:100});
  }
},55000);
