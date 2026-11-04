import {expect,test} from 'bun:test';
import {mkdtempSync,rmSync,realpathSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,dirname,basename} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';

test('real Host AI/build procedures keep deployment-wide provider fallbacks owner-only',()=>{
  const directory=mkdtempSync(join(tmpdir(),'host-provider-tenant-'));
  try {
    const result=spawnSync(process.execPath,[fileURLToPath(new URL('./provider-tenant.fixture.ts',import.meta.url)),directory],{
      timeout:20000,encoding:'utf8',
      env:{SystemRoot:process.env.SystemRoot,TEMP:process.env.TEMP,TMP:process.env.TMP,PATH:process.env.PATH},
    });
    expect(result.error).toBeUndefined();expect(result.signal).toBeNull();
    expect(result.stderr).toBe('');expect(result.stdout).toBe('');expect(result.status).toBe(0);
  } finally {
    expect(dirname(realpathSync(directory))).toBe(realpathSync(tmpdir()));
    expect(basename(directory).startsWith('host-provider-tenant-')).toBe(true);
    rmSync(directory,{recursive:true,maxRetries:5,retryDelay:100});
  }
},25000);
