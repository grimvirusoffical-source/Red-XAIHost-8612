import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createServer,isIP} from 'node:net';
import {join} from 'node:path';
import vm from 'node:vm';

// Evaluate only the exported string constant, then the selected served
// functions. Never run the worker's imports, environment handling or main().
const moduleSource=await readFile(new URL('./agent-assets.ts',import.meta.url),'utf8');
const boundary=moduleSource.indexOf('\nexport const STATIC_SERVER_SOURCE');
assert.ok(boundary>0);
const literal=moduleSource.slice(0,boundary).replace('export const AGENT_SOURCE','const AGENT_SOURCE');
const source=new vm.Script(literal+'\nAGENT_SOURCE').runInNewContext();
const section=(first,last)=>{
  const start=source.indexOf(first),end=source.indexOf(last,start+first.length);
  assert.ok(start>0&&end>start);return source.slice(start,end);
};
const portSource=section('function assertValidWorkloadPort(','\n/** The no-Docker static');
const deploySource=section('async function deployProject(','\nasync function ensureCloudflared(');
const nativeSource=section('async function deployNative(','\nfunction staticDockerfile(');
const stopSource=section('async function stopProject(','\nasync function handleJob(');
const strictReaderSource=section('function readTrackedForRemoval(','\nasync function writeTracked(');
const pidAliveSource=section('function pidAlive(','\n/** Kills a tracked native');
const trackedStopSource=section('async function stopTracked(','\nasync function removeDirWithRetry(');
const reporter={progress:async()=>{},write:()=>{}};
const fixtureInterfaces=()=>({lo:[{address:'127.0.0.1',family:'IPv4'},{address:'::1',family:'IPv6',scopeid:0}]});

function fakePorts(events,{errors={},closeError}={}){
  return onConnection=>{
    let errorHandler,listeningOptions;
    return {
      once(name,handler){assert.equal(name,'error');errorHandler=handler;return this;},
      listen(options,ready){
        listeningOptions=options;
        events.push(['probe',options]);
        queueMicrotask(()=>{const code=typeof errors==='function'?errors(options):errors[options.host];if(code)errorHandler({code});else ready();});
        return this;
      },
      close(done){events.push(['probe-close']);done(typeof closeError==='function'?closeError(listeningOptions):closeError);},
      fixtureConnection(){const socket={destroy(){events.push(['probe-socket-destroy']);}};onConnection(socket);},
    };
  };
}

function portCheck(options={}){
  const events=[],context=vm.createContext({createServer:fakePorts(events,options),isIP,networkInterfaces:options.networkInterfaces||fixtureInterfaces});
  const methods=new vm.Script(portSource+'\n({assertValidWorkloadPort,nativePortProbeHosts,assertNativePortAvailable})').runInContext(context);
  return {...methods,events};
}

function deployment(options={}){
  const events=[];let reads=0,occupied=options.occupied===true;
  const context=vm.createContext({
    WORK:'/owned-fixture/projects',join,
    networkInterfaces:fixtureInterfaces,isIP,
    createServer:fakePorts(events,{errors:()=>occupied?'EADDRINUSE':undefined}),
    readTrackedForRemoval:()=>{
      events.push(['read-tracked']);
      if(options.invalidTracked)throw Error('Tracked state could not be verified.');
      return reads++===0?(options.tracked?{pid:999}:null):(options.stillAlive?{pid:999}:null);
    },
    stopTracked:async slug=>{events.push(['stop-tracked',slug]);return !options.stillAlive;},
    pidAlive:()=>options.stillAlive===true,
    has:async command=>{events.push(['has',command]);return command==='docker'?options.docker===true:true;},
    removeDirWithRetry:async dir=>{events.push(['remove-checkout',dir]);},
    mkdir:async dir=>{events.push(['mkdir',dir]);},
    streamCommand:async(command,args)=>{events.push(['command',command,...args]);return true;},
    deployNative:async()=>{events.push(['start-native']);},
    writeFile:async file=>{events.push(['write-file',file]);},
    run:async(command,args)=>{
      events.push(['run',command,...args]);
      if(command==='docker'&&args[0]==='container')return {ok:true,stdout:occupied?'rxh-fixture\n':''};
      if(command==='docker'&&args[0]==='rm')occupied=false;
      return {ok:true,stdout:''};
    },
    waitForPort:async()=>true,
    startTunnel:async()=>{events.push(['start-tunnel']);},
  });
  const methods=new vm.Script(portSource+'\n'+deploySource+'\n'+stopSource+'\n({deployProject,stopProject})').runInContext(context);
  const job=patch=>({action:'deploy',project:{id:'fixture-project',slug:'fixture',name:'Fixture',runtime:'static',port:43187,sourceKind:'git',gitUrl:'https://example.invalid/fixture.git',...patch},tunnels:[]});
  return {events,job,deploy:patch=>methods.deployProject(job(patch),reporter),stop:()=>methods.stopProject(job(),reporter,false)};
}

test('served worker checks local and wildcard addresses exclusively and closes its own probes',async()=>{
  const f=portCheck();await f.assertNativePortAvailable(43187);
  assert.deepEqual(f.events.map(x=>x[0]),['probe','probe-close','probe','probe-close','probe','probe-close','probe','probe-close']);
  assert.deepEqual(JSON.parse(JSON.stringify(f.events.filter(x=>x[0]==='probe').map(x=>x[1]))),[
    {host:'127.0.0.1',port:43187,exclusive:true},
    {host:'0.0.0.0',port:43187,exclusive:true},
    {host:'::1',port:43187,exclusive:true,ipv6Only:true},
    {host:'::',port:43187,exclusive:true,ipv6Only:true},
  ]);
});

test('interface probes include specific local addresses and scoped IPv6 without guessing absent IPv6 support',async()=>{
  const f=portCheck({networkInterfaces:()=>({nic:[{address:'192.0.2.42',family:'IPv4'},{address:'fe80::1',family:'IPv6',scopeid:7}]})});
  assert.deepEqual(Array.from(f.nativePortProbeHosts()),['127.0.0.1','0.0.0.0','192.0.2.42','fe80::1%7','::1','::']);
  await f.assertNativePortAvailable(43187);
  const scoped=f.events.find(x=>x[0]==='probe'&&x[1].host==='fe80::1%7');assert.equal(scoped[1].ipv6Only,true);
  const ipv4=portCheck({networkInterfaces:()=>({lo:[{address:'127.0.0.1',family:4}]})});
  assert.deepEqual(Array.from(ipv4.nativePortProbeHosts()),['127.0.0.1','0.0.0.0']);
});

test('interface lookup failure, malformed addresses and disappearing addresses refuse without logging inventories',async()=>{
  for(const networkInterfaces of [()=>{throw Error('private-inventory-must-not-escape');},()=>({nic:[{address:'not-a-local-ip',family:'IPv4'}]}),()=>({nic:[{address:'192.0.2.42',family:'unknown'}]})]){
    const f=portCheck({networkInterfaces});
    await assert.rejects(()=>f.assertNativePortAvailable(43187),error=>/Local network addresses could not be verified/.test(error.message)&&!error.message.includes('private-inventory'));
    assert.deepEqual(f.events,[]);
  }
  const disappeared=portCheck({networkInterfaces:()=>({nic:[{address:'fe80::1',family:'IPv6',scopeid:7}]}),errors:{'fe80::1%7':'EADDRNOTAVAIL'}});
  await assert.rejects(()=>disappeared.assertNativePortAvailable(43187),/could not be verified/);
});

test('occupied, permission-denied and uncertain ports refuse; only an unsupported IPv6 family is optional',async()=>{
  for(const host of ['0.0.0.0','::']){
    const f=portCheck({errors:{[host]:'EADDRINUSE'}});
    await assert.rejects(()=>f.assertNativePortAvailable(43187),/already occupied/);
  }
  for(const code of ['EACCES','EADDRNOTAVAIL','UNKNOWN']){
    const f=portCheck({errors:{'::':code}});
    await assert.rejects(()=>f.assertNativePortAvailable(43187),/could not be verified/);
  }
  for(const code of ['EAFNOSUPPORT','EPROTONOSUPPORT']){
    await portCheck({errors:{'::':code}}).assertNativePortAvailable(43187);
    await assert.rejects(()=>portCheck({errors:{'0.0.0.0':code}}).assertNativePortAvailable(43187),/could not be verified/);
  }
  await assert.rejects(()=>portCheck({closeError:{code:'EIO'}}).assertNativePortAvailable(43187),/could not be verified/);
});

test('invalid requested ports fail before any tracked stop or checkout operation',async()=>{
  for(const port of [0,-1,65536,1.5,NaN,'not-a-port']){
    const f=deployment();await assert.rejects(()=>f.deploy({port}),/Invalid workload port/);
    assert.deepEqual(f.events,[]);
  }
  const f=portCheck();assert.throws(()=>f.assertValidWorkloadPort(Infinity),/Invalid workload port/);assert.deepEqual(f.events,[]);
});

test('an IPv6 probe close error is not treated as an unsupported address family',async()=>{
  for(const code of ['EAFNOSUPPORT','EPROTONOSUPPORT']){
    const f=portCheck({closeError:options=>options.host==='::'?{code}:undefined});
    await assert.rejects(()=>f.assertNativePortAvailable(43187),/could not be verified/);
  }
});

test('an occupied native port leaves its checkout intact without commands or startup side effects',async()=>{
  const f=deployment({occupied:true});await assert.rejects(()=>f.deploy(),/already occupied/);
  assert.deepEqual(f.events.filter(x=>['remove-checkout','mkdir','command','run','write-file','start-native','start-tunnel'].includes(x[0])),[]);
  assert.equal(f.events.filter(x=>x[0]==='stop-tracked').length,1);
});

test('unreadable tracked state and a failed tracked stop refuse before checking or replacing files',async()=>{
  const unreadable=deployment({invalidTracked:true});await assert.rejects(()=>unreadable.deploy(),/could not be verified/);
  assert.equal(unreadable.events.some(x=>x[0]==='stop-tracked'),false);
  const alive=deployment({tracked:true,stillAlive:true});await assert.rejects(()=>alive.deploy(),/still running/);
  for(const f of [unreadable,alive])assert.equal(f.events.some(x=>['probe','remove-checkout','mkdir','command','run','start-native'].includes(x[0])),false);
});

test('managed native redeployment stops once and verifies the port before checkout replacement',async()=>{
  const f=deployment({tracked:true});await f.deploy();
  assert.equal(f.events.filter(x=>x[0]==='stop-tracked').length,1);
  const order=f.events.map(x=>x[0]);
  assert.ok(order.indexOf('stop-tracked')<order.indexOf('probe'));
  assert.ok(order.lastIndexOf('probe-close')<order.indexOf('remove-checkout'));
  assert.ok(order.indexOf('remove-checkout')<order.indexOf('command'));
  assert.equal(order.at(-1),'start-native');
});

test('reserved, self and out-of-range tracked PIDs retain state and never reach signal or command adapters',async()=>{
  for(const pid of [0,1,-1,-42,2147483648,Number.MAX_SAFE_INTEGER,1.5,500000,null,'123']){
    for(const os of ['linux','win32']){
      const signals=[],commands=[],removed=[];
      const context=vm.createContext({
        process:{pid:500000,kill:(...args)=>{signals.push(args);}},
        platform:()=>os,
        stateFile:()=>'/synthetic-tracked-state',
        readFileSync:()=>JSON.stringify({pid}),
        readTracked:()=>({pid}),
        run:async(...args)=>{commands.push(args);return {ok:true};},
        rm:async(...args)=>{removed.push(args);},
        setTimeout:()=>{throw Error('Invalid state reached a stop timer');},
      });
      const stop=new vm.Script(strictReaderSource+'\n'+pidAliveSource+'\n'+trackedStopSource+'\nstopTracked').runInContext(context);
      await assert.rejects(()=>stop('fixture',reporter),/could not be verified/);
      assert.deepEqual(signals,[]);assert.deepEqual(commands,[]);assert.deepEqual(removed,[]);
    }
  }
});

test('tracked PID validation accepts only ordinary positive nonself signed-32-bit values',()=>{
  const read=pid=>new vm.Script(strictReaderSource+'\nreadTrackedForRemoval').runInNewContext({process:{pid:500000},stateFile:()=>'/synthetic-only',readFileSync:()=>JSON.stringify({pid})})('fixture');
  assert.equal(read(2).pid,2);assert.equal(read(2147483647).pid,2147483647);
});

test('native startup does not stop the workload a second time after checkout replacement',async()=>{
  const events=[],context=vm.createContext({
    process:{execPath:'/fixture/node'},
    stopTracked:async()=>{throw Error('Unexpected second stop');},
    ensureStaticServer:async()=>'/fixture/static-server.mjs',
    spawn:()=>({pid:999,unref(){events.push('unref');}}),
    childEnv:()=>({PORT:'43187'}),
    writeTracked:async(_slug,state)=>{events.push(state);},
  });
  const deployNative=new vm.Script(nativeSource+'\ndeployNative').runInContext(context);
  await deployNative({project:{slug:'fixture',runtime:'static',port:43187}},reporter,'/fixture/checkout');
  assert.equal(events[0],'unref');assert.equal(events[1].pid,999);
});

test('Docker redeploy and native-to-Docker retain their named-container lifecycle without native bind probes',async()=>{
  for(const tracked of [false,true]){
    const f=deployment({docker:true,occupied:true,tracked});await f.deploy({runtime:'docker',dockerfile:'FROM scratch'});
    assert.equal(f.events.some(x=>x[0]==='probe'),false);
    assert.equal(f.events.filter(x=>x[0]==='stop-tracked').length,1);
    assert.deepEqual(f.events.filter(x=>x[0]==='run'),[['run','docker','rm','-f','rxh-fixture']]);
    const commands=f.events.filter(x=>x[0]==='command');
    assert.equal(commands[1][2],'build');assert.equal(commands[2][2],'run');
  }
});

test('Docker-to-native refuses an occupied port until the existing managed Stop operation succeeds',async()=>{
  const f=deployment({docker:true,occupied:true});
  await assert.rejects(()=>f.deploy(),/already occupied/);
  assert.equal(f.events.some(x=>x[0]==='remove-checkout'),false);
  assert.equal(await f.stop(),true);
  await f.deploy();
  assert.deepEqual(f.events.filter(x=>x[0]==='run'),[
    ['run','docker','container','ls','--all','--filter','name=^/rxh-fixture$','--format','{{.Names}}'],
    ['run','docker','rm','-f','rxh-fixture'],
  ]);
  assert.equal(f.events.filter(x=>x[0]==='start-native').length,1);
});

test('the actual availability check refuses an owned loopback listener without closing it',async t=>{
  const held=createServer(socket=>socket.destroy());
  t.after(()=>new Promise(resolve=>held.close(()=>resolve())));
  await new Promise((resolve,reject)=>{held.once('error',reject);held.listen({host:'127.0.0.1',port:0,exclusive:true},resolve);});
  const port=held.address().port;
  const check=new vm.Script(portSource+'\nassertNativePortAvailable').runInNewContext({createServer,isIP,networkInterfaces:fixtureInterfaces});
  await assert.rejects(()=>check(port),/already occupied/);
  assert.equal(held.listening,true);
});

test('worker version, removal confirmation and busy reporting remain present without listener-by-port termination',()=>{
  assert.match(source,/const AGENT_VERSION = "1\.3\.2"/u);
  assert.match(source,/api\("\/api\/agent\/heartbeat", \{ \.\.\.info, busy \}\)/u);
  assert.match(source,/async function stopProject\(/u);
  for(const marker of ['killPortListener','lsof','fuser','netstat','stopped orphan listener'])assert.equal(source.includes(marker),false);
});
