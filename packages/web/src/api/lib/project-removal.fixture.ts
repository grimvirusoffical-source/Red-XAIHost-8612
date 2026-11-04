import assert from 'node:assert/strict';
import {createClient} from '@libsql/client';
import {join} from 'node:path';
import {readFileSync,writeFileSync} from 'node:fs';
const AUTHORITY='http://127.0.0.1:4200/api/accounts/me';
const COOKIE='redx_session';

const mode=process.argv[2],directory=process.argv[3];
assert.equal(process.argv.length,4);assert.ok(directory);assert.ok(['start','restart'].includes(mode!));
Object.assign(process.env,{DATABASE_URL:`file:${join(directory!,'fixture.db')}`,REDX_DATA_DIR:directory,
  BETTER_AUTH_SECRET:'00000000000000000000000000000000',CREDENTIAL_SECRET:'11111111111111111111111111111111',
  OWNER_EMAIL:'owner@example.invalid',REDX_WEBSITE_SESSION:'true',
  REDX_WEBSITE_OWNER_ACCOUNT_IDS:'verified-owner',REDX_PUBLIC_URL:'https://red-xai.test/host',WEBSITE_URL:'https://red-xai.test/host',NODE_ENV:'test'});
const client=createClient({url:process.env.DATABASE_URL!});
const a={id:'fixture-a',owner:false};
const b={id:'fixture-b',owner:false};
const owner={id:'verified-owner',owner:true};
const identities={a,b,owner};type Who=keyof typeof identities;
const headers=(who:Who)=>new Headers({cookie:`${COOKIE}=${who}`});
const nodeTokens={a1:'synthetic-node-a1',a2:'synthetic-node-a2',b1:'synthetic-node-b1',owner1:'synthetic-node-owner1'};
try {
  if(mode==='start') {
    const schema=await import('../database/schema');
    const {generateSQLiteDrizzleJson,generateSQLiteMigration}=await import('drizzle-kit/api');
    for(const statement of await generateSQLiteMigration(await generateSQLiteDrizzleJson({}),await generateSQLiteDrizzleJson(schema)))await client.execute(statement);
  }
  const {call}=await import('@orpc/server');
  const {Hono}=await import('hono');
  const {projects}=await import('../routes/projects');
  const {registerAgentRoutes}=await import('../agent');
  const {hashToken}=await import('./crypto');
  const {REMOVAL_CLAIM_SECONDS}=await import('./project-removal');
  const keys={a:a.id,b:b.id,owner:'owner'};
  globalThis.fetch=(async(url,init)=>{
    assert.equal(String(url),AUTHORITY,'no external provider or node request is allowed');
    const who=new Headers(init?.headers).get('cookie')?.split('=')[1] as Who,p=identities[who];assert.ok(p);
    return Response.json({account:{id:p.id,email:'shared@example.invalid',roles:[p.owner?'owner':'user'],emailVerified:true,active:true},active:true,
      amr:p.owner?['pwd','totp']:['pwd'],expiresMs:Date.now()+60000});
  }) as typeof fetch;
  const app=new Hono();app.onError(()=>Response.json({error:'fixture request failed'},{status:500}));registerAgentRoutes(app);
  const rpc=<T>(procedure:any,input:T,who:Who='a')=>call(procedure,input,{context:{headers:headers(who)}}) as Promise<any>;
  const agent=(node:keyof typeof nodeTokens,path:string,body:unknown={})=>app.request('/api/agent/'+path,{method:'POST',headers:{authorization:'Bearer '+nodeTokens[node],'content-type':'application/json'},body:JSON.stringify(body)});
  const row=async(sql:string,args:(string|number)[]=[])=>{const r=await client.execute({sql,args});return r.rows[0];};
  const removals=async(id:string)=>(await client.execute({sql:"SELECT id,node_id,status FROM deployments WHERE tenant_id=? AND project_id=? AND action='remove' AND trigger='delete'",args:[keys.a,id]})).rows;
  const poll=async(node:keyof typeof nodeTokens,body:unknown={})=>{const r=await agent(node,'heartbeat',body);assert.equal(r.status,200);return (await r.json() as any).jobs as any[];};
  const report=async(node:keyof typeof nodeTokens,id:string,status:string)=>{const r=await agent(node,'job',{deploymentId:id,status});assert.equal(r.status,200);};
  if(mode==='start') {
    const now=Math.floor(Date.now()/1000);
    for(const [id,who,status,last] of [['a1','a','online',now],['a2','a','offline',now-600],['b1','b','online',now]] as const)
      await client.execute({sql:'INSERT INTO nodes(id,tenant_id,name,token_hash,token_preview,status,last_seen_at) VALUES (?,?,?,?,?,?,?)',args:[id,keys[who],'Synthetic '+id,hashToken(nodeTokens[id]),'synthetic',status,last]});
    const p=await rpc(projects.create,{name:'Removal target',nodeId:'a1'}),foreign=await rpc(projects.create,{name:'Other tenant',nodeId:'b1'},'b');
    const owned=await rpc(projects.create,{name:'Owner project'},'owner');
    await client.execute({sql:"UPDATE projects SET status='running',env_vars=?,bundle_key=?,dockerfile=? WHERE id=?",args:[JSON.stringify({PRIVATE_TEST:'synthetic-not-for-removal'}),'bundles/fixture/private.zip','synthetic-dockerfile',p.id]});
    for(const n of ['a1','a2'])await client.execute({sql:"INSERT INTO project_replicas(id,tenant_id,project_id,node_id,status) VALUES (?,?,?,?,'running')",args:['rep-'+n,keys.a,p.id,n]});
    await client.execute({sql:"INSERT INTO domains(id,tenant_id,hostname,project_id,tunnel_name) VALUES (?,?,?,?,'synthetic-private-tunnel')",args:['domain-a',keys.a,'synthetic.invalid',p.id]});
    await client.execute({sql:"INSERT INTO deployments(id,tenant_id,project_id,node_id,status,action) VALUES ('old-deploy',?,?,?,'building','deploy')",args:[keys.a,p.id,'a1']});
    await rpc(projects.remove,{id:foreign.id});await rpc(projects.remove,{id:p.id},'b');await rpc(projects.remove,{id:p.id},'owner');
    assert.ok(await row('SELECT id FROM projects WHERE id=?',[p.id]));assert.ok(await row('SELECT id FROM projects WHERE id=?',[foreign.id]));
    await rpc(projects.remove,{id:p.id});
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[p.id]))?.status,'removing','the project payload must survive the initial remove request');
    assert.equal((await row('SELECT auto_deploy FROM projects WHERE id=?',[p.id]))?.auto_deploy,0);
    assert.equal((await row("SELECT status FROM deployments WHERE id='old-deploy'"))?.status,'cancelled');
    assert.equal((await removals(p.id)).length,2,'all owned replicas, including the offline node, get teardown jobs');
    await rpc(projects.remove,{id:p.id});assert.equal((await removals(p.id)).length,2,'a repeated remove must not duplicate active jobs');
    for(const action of ['deploy','restart','stop'])await assert.rejects(()=>rpc(projects.deploy,{id:p.id,action}),{code:'CONFLICT'});
    await assert.rejects(()=>rpc(projects.update,{id:p.id,name:'resurrection'}),{code:'CONFLICT'});
    await assert.rejects(()=>rpc(projects.aiPlan,{id:p.id}),{code:'CONFLICT'});
    await report('a1','old-deploy','succeeded');
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[p.id]))?.status,'removing','a late old deployment callback cannot revive the project');
    assert.equal((await row("SELECT status FROM deployments WHERE id='old-deploy'"))?.status,'cancelled');
    assert.deepEqual(await poll('b1'),[]);
    const first=(await removals(p.id)).find(x=>x.node_id==='a1')!;
    assert.equal((await agent('b1','job',{deploymentId:first.id,status:'succeeded'})).status,404);
    assert.equal((await agent('a1','job',{deploymentId:first.id,status:'invented-status'})).status,400);
    assert.deepEqual(await poll('a1',{busy:true}),[]);
    assert.equal((await row('SELECT status FROM deployments WHERE id=?',[String(first.id)]))?.status,'queued');
    const delivered=await poll('a1');assert.equal(delivered.length,1);assert.equal(delivered[0].action,'remove');
    assert.equal(delivered[0].project.id,p.id);assert.equal(delivered[0].project.slug,p.slug);
    assert.deepEqual(delivered[0].project.envVars,{});assert.deepEqual(delivered[0].tunnels,[]);
    for(const field of ['gitUrl','gitBranch','bundleUrl','bundleName','dockerfile','installCommand','buildCommand','startCommand'])assert.equal(delivered[0].project[field],null);
    assert.ok(!JSON.stringify(delivered).includes('synthetic-not-for-removal'));
    assert.deepEqual(await poll('a1'),[],'a current claim is delivered only once');
    await client.execute({sql:'UPDATE deployments SET started_at=? WHERE id=?',args:[now-REMOVAL_CLAIM_SECONDS-1,String(first.id)]});
    assert.deepEqual(await poll('a1',{busy:true}),[],'a busy worker keeps its current removal lease');
    assert.equal((await poll('a1',{busy:false}))[0]?.deploymentId,first.id,'an expired lost response is retried without another job');
    await report('a1',String(first.id),'failed');assert.ok(await row('SELECT id FROM projects WHERE id=?',[p.id]));
    await rpc(projects.remove,{id:p.id});
    const next=(await removals(p.id)).find(x=>x.node_id==='a1'&&x.status==='queued')!;
    assert.notEqual(next.id,first.id,'failed attempts retain immutable terminal history');
    await report('a1',String(first.id),'succeeded');
    assert.equal((await row('SELECT status FROM deployments WHERE id=?',[String(first.id)]))?.status,'failed');
    assert.equal((await poll('a1'))[0]?.deploymentId,next.id);
    await report('a1',String(next.id),'succeeded');await report('a1',String(next.id),'failed');
    assert.equal((await row('SELECT status FROM deployments WHERE id=?',[String(next.id)]))?.status,'succeeded');
    assert.ok(await row('SELECT id FROM projects WHERE id=?',[p.id]),'offline replica still prevents final purge');
    writeFileSync(join(directory!,'state.json'),JSON.stringify({p,foreign,owned,firstId:first.id,nextId:next.id}));
  } else {
    const {p,foreign,owned,firstId,nextId}=JSON.parse(readFileSync(join(directory!,'state.json'),'utf8'));
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[p.id]))?.status,'removing','removal intent survives a process restart');
    assert.equal((await agent('a2','hello',{agentVersion:'synthetic'})).status,200);
    const jobs=await poll('a2');assert.equal(jobs.length,1);const last=jobs[0].deploymentId;
    await report('a2',last,'building');
    await client.execute({sql:'UPDATE nodes SET last_seen_at=? WHERE id=?',args:[Math.floor(Date.now()/1000)-600,'a2']});
    assert.deepEqual(await poll('a2',{busy:true}),[]);
    assert.equal((await poll('a2',{busy:false}))[0]?.deploymentId,last,'offline reconnect recovers in-flight teardown');
    await report('a2',last,'building');
    assert.equal((await agent('a2','hello',{})).status,200);assert.equal((await agent('a2','hello',{})).status,200);
    const redelivered=await Promise.all([poll('a2'),poll('a2')]);
    assert.equal(redelivered.flat().length,1,'repeated reconnect/concurrent polling cannot duplicate a claimed job');
    await report('a2',last,'succeeded');
    assert.equal(await row('SELECT id FROM projects WHERE id=?',[p.id]),undefined);
    assert.equal((await row('SELECT count(*) AS n FROM project_replicas WHERE tenant_id=? AND project_id=?',[keys.a,p.id]))?.n,0);
    assert.equal((await row("SELECT project_id FROM domains WHERE id='domain-a'"))?.project_id,null);
    const tombstone=await row("SELECT meta FROM activity WHERE tenant_id=? AND ref_id=? AND scope='project_removal'",[keys.a,p.id]);
    assert.deepEqual(JSON.parse(String(tombstone?.meta)),{version:1,projectId:p.id,slug:p.slug});
    const replacement=await rpc(projects.create,{name:'Removal target',nodeId:'a1'});
    assert.notEqual(replacement.id,p.id);assert.equal(replacement.slug,p.slug+'-2','completed slugs remain reserved for delayed legacy workers');
    for(const [node,id] of [['a1',firstId],['a1',nextId],['a2',last]] as const)for(const status of ['building','succeeded','failed'])await report(node,id,status);
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[replacement.id]))?.status,'draft');
    assert.deepEqual(await poll('a2'),[],'completed teardown jobs cannot be delivered against a replacement');
    assert.ok(await row('SELECT id FROM projects WHERE id=?',[foreign.id]));assert.ok(await row('SELECT id FROM projects WHERE id=?',[owned.id]));
    const invalid=await rpc(projects.create,{name:'Invalid relationship',nodeId:'a1'});
    for(const nodeId of ['b1','missing-node']) {
      await client.execute({sql:'UPDATE projects SET node_id=? WHERE id=?',args:[nodeId,invalid.id]});
      await assert.rejects(()=>rpc(projects.remove,{id:invalid.id}),{code:'CONFLICT'});
      assert.equal((await row('SELECT status FROM projects WHERE id=?',[invalid.id]))?.status,'draft');
      assert.equal((await removals(invalid.id)).length,0);
    }
    const rollback=await rpc(projects.create,{name:'Atomic rollback',nodeId:'a1'});
    await client.execute({sql:"INSERT INTO deployments(id,tenant_id,project_id,node_id,status,action) VALUES ('rollback-deploy',?,?,?,'queued','deploy')",args:[keys.a,rollback.id,'a1']});
    await client.execute(`CREATE TRIGGER fixture_abort_removal BEFORE INSERT ON deployments WHEN NEW.project_id='${rollback.id}' AND NEW.action='remove' BEGIN SELECT RAISE(ABORT,'synthetic rollback'); END`);
    await assert.rejects(()=>rpc(projects.remove,{id:rollback.id}));
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[rollback.id]))?.status,'draft');
    assert.equal((await row("SELECT status FROM deployments WHERE id='rollback-deploy'"))?.status,'queued');
    await client.execute('DROP TRIGGER fixture_abort_removal');
    const purgeRetry=await rpc(projects.create,{name:'Purge retry',nodeId:'a1'});
    await rpc(projects.remove,{id:purgeRetry.id});
    const purgeJob=(await poll('a1')).find(j=>j.project.id===purgeRetry.id)!;assert.ok(purgeJob);
    await client.execute(`CREATE TRIGGER fixture_abort_purge BEFORE DELETE ON projects WHEN OLD.id='${purgeRetry.id}' BEGIN SELECT RAISE(ABORT,'synthetic purge rollback'); END`);
    assert.equal((await agent('a1','job',{deploymentId:purgeJob.deploymentId,status:'succeeded'})).status,500);
    assert.equal((await row('SELECT status FROM projects WHERE id=?',[purgeRetry.id]))?.status,'removing');
    assert.equal(await row("SELECT id FROM activity WHERE scope='project_removal' AND ref_id=?",[purgeRetry.id]),undefined,'a purge failure rolls back its reservation too');
    await client.execute('DROP TRIGGER fixture_abort_purge');
    await report('a1',purgeJob.deploymentId,'succeeded');
    assert.equal(await row('SELECT id FROM projects WHERE id=?',[purgeRetry.id]),undefined,'a repeated success can finish an interrupted finalization');
    const draft=await rpc(projects.create,{name:'Never deployed'});await rpc(projects.remove,{id:draft.id});
    assert.equal(await row('SELECT id FROM projects WHERE id=?',[draft.id]),undefined);
    const successor=await rpc(projects.create,{name:'Never deployed'});assert.equal(successor.slug,draft.slug+'-2');
    const {ensureInfectedNationProject}=await import('./local-worker');
    const noPortProbe=async()=>{throw new Error('bootstrap must not probe/start a removed project');};
    await client.execute({sql:"INSERT INTO nodes(id,tenant_id,name,token_hash,token_preview,status) VALUES ('owner1',?,'Synthetic owner',?,'synthetic','offline')",args:[keys.owner,hashToken(nodeTokens.owner1)]});
    const nation=await rpc(projects.create,{name:'InfectedNation',nodeId:'owner1'},'owner');
    await rpc(projects.remove,{id:nation.id},'owner');
    await ensureInfectedNationProject('unexpected-bootstrap-node',noPortProbe);
    assert.equal((await row('SELECT node_id FROM projects WHERE id=?',[nation.id]))?.node_id,'owner1');
    const nationJob=(await poll('owner1'))[0];assert.ok(nationJob);await report('owner1',nationJob.deploymentId,'succeeded');
    const originalLog=console.log;
    try {console.log=()=>{};await ensureInfectedNationProject('owner1',noPortProbe);}finally{console.log=originalLog;}
    assert.equal(await row("SELECT id FROM projects WHERE slug='infectednation'"),undefined,'bootstrap cannot resurrect a completed slug');
  }
} finally {client.close();}
