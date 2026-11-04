import {expect,test} from 'bun:test';
import {spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';

test('current Host UI waits for removal acceptance, retains errors and supports pending retry',()=>{
  const web=fileURLToPath(new URL('..',import.meta.url));
  const result=spawnSync(process.execPath,[fileURLToPath(new URL('./project-removal-ui.fixture.ts',import.meta.url))],{
    cwd:web,timeout:15000,encoding:'utf8',
    env:Object.fromEntries(['SystemRoot','WINDIR','TEMP','TMP','PATH'].filter(key=>process.env[key]).map(key=>[key,process.env[key]])),
  });
  expect(result.error).toBeUndefined();expect(result.signal).toBeNull();
  expect(result.stdout).toBe('');expect(result.stderr).toBe('');expect(result.status).toBe(0);
},20000);
