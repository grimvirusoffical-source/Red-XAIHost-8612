import {expect,test} from 'bun:test';
import vm from 'node:vm';
import {AGENT_SOURCE} from './agent-assets';

// Execute the actual served function with inert process/Docker adapters, never
// the agent's imports/main loop, real processes, containers or network calls.
function removal(options:{tracked?:boolean;docker?:boolean;responses?:{ok:boolean;stdout:string}[]}={}) {
  const start=AGENT_SOURCE.indexOf('async function stopProject('),end=AGENT_SOURCE.indexOf('\nasync function handleJob(',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  const commands:string[][]=[],responses=[...(options.responses??[])];
  const context=vm.createContext({
    stopTracked:async()=>false,
    readTrackedForRemoval:()=>options.tracked?{pid:123}:null,
    pidAlive:()=>options.tracked===true,
    has:async()=>options.docker!==false,
    run:async(cmd:string,args:string[])=>{commands.push([cmd,...args]);return responses.shift()??{ok:true,stdout:''};},
  });
  const execute=new vm.Script(AGENT_SOURCE.slice(start,end)+'\nstopProject').runInContext(context) as (job:unknown,reporter:unknown,removeImage:boolean)=>Promise<boolean>;
  const invoke=(runtime='static',slug='fixture-only')=>execute({project:{slug,runtime}},{progress:async()=>{},write:()=>{}},true);
  return {invoke,commands};
}

test('removal does not report success while its tracked native process is alive',async()=>{
  const check=removal({tracked:true});await expect(check.invoke()).rejects.toThrow('still running');expect(check.commands).toEqual([]);
});
test('worker refuses unsafe workload paths before any process or Docker action',async()=>{
  const check=removal();await expect(check.invoke('static','../other-project')).rejects.toThrow('Unsafe workload identifier');expect(check.commands).toEqual([]);
});
test('missing native state is distinct from unreadable or invalid state',()=>{
  const start=AGENT_SOURCE.indexOf('function readTrackedForRemoval('),end=AGENT_SOURCE.indexOf('\nasync function writeTracked(',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  const make=(read:()=>string)=>new vm.Script(AGENT_SOURCE.slice(start,end)+'\nreadTrackedForRemoval').runInNewContext({stateFile:()=>'/synthetic-only',readFileSync:read}) as (slug:string)=>unknown;
  expect(make(()=>{throw Object.assign(new Error('missing'),{code:'ENOENT'});})('fixture')).toBeNull();
  expect(()=>make(()=>{throw Object.assign(new Error('denied'),{code:'EACCES'});})('fixture')).toThrow('could not be verified');
  expect(()=>make(()=>'{broken')('fixture')).toThrow('could not be verified');
  expect(()=>make(()=>'{}')('fixture')).toThrow('could not be verified');
  expect(make(()=>' {"pid":123} ')('fixture')).toEqual({pid:123});
});
test('Docker unavailability/daemon/permission failures are not successful absence',async()=>{
  await expect(removal({docker:false}).invoke('docker')).rejects.toThrow('unavailable');
  const daemon=removal({responses:[{ok:false,stdout:''}]});await expect(daemon.invoke()).rejects.toThrow('could not be verified');
  expect(daemon.commands.some(c=>c[1]==='rm'||c[1]==='rmi')).toBe(false);
  const imageLookup=removal({responses:[{ok:true,stdout:''},{ok:false,stdout:''}]});await expect(imageLookup.invoke()).rejects.toThrow('images could not be verified');
});
test('proven container/image absence makes a removal retry idempotent',async()=>{
  const check=removal();expect(await check.invoke()).toBe(true);
  expect(check.commands.some(c=>c[1]==='rm'||c[1]==='rmi')).toBe(false);
});
test('worker removes only the exact owned workload and checks command success',async()=>{
  const check=removal({responses:[{ok:true,stdout:'rxh-fixture-only\n'},{ok:true,stdout:''},{ok:true,stdout:'rxh-fixture-only:latest\n'},{ok:true,stdout:''}]});
  expect(await check.invoke()).toBe(true);
  expect(check.commands.filter(c=>c[1]==='rm'||c[1]==='rmi')).toEqual([
    ['docker','rm','-f','rxh-fixture-only'],['docker','rmi','-f','rxh-fixture-only:latest'],
  ]);
  const failed=removal({responses:[{ok:true,stdout:'rxh-fixture-only\n'},{ok:false,stdout:''}]});await expect(failed.invoke()).rejects.toThrow('could not be removed');
  const failedImage=removal({responses:[{ok:true,stdout:''},{ok:true,stdout:'rxh-fixture-only:latest\n'},{ok:false,stdout:''}]});await expect(failedImage.invoke()).rejects.toThrow('image could not be removed');
});
test('served worker keeps heartbeats alive but advertises busy while executing a job',async()=>{
  const start=AGENT_SOURCE.indexOf('let busy = false;'),end=AGENT_SOURCE.indexOf('\nasync function main()',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  const observed:boolean[]=[];let release!:()=>void,started!:()=>void;
  const entered=new Promise<void>(resolve=>{started=resolve;});const blocked=new Promise<void>(resolve=>{release=resolve;});
  const context=vm.createContext({machineInfo:async()=>({agentVersion:'synthetic'}),log:()=>{},
    api:async(_path:string,body:{busy:boolean})=>{observed.push(body.busy);return {jobs:observed.length===1?[{deploymentId:'synthetic'}]:[]};},
    handleJob:async()=>{started();await blocked;},
  });
  const tick=new vm.Script(AGENT_SOURCE.slice(start,end)+'\ntick').runInContext(context) as ()=>Promise<void>;
  const work=tick();await entered;await tick();release();await work;await tick();
  expect(observed).toEqual([false,true,false]);
});
