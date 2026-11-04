import assert from 'node:assert/strict';
import {createClient} from '@libsql/client';
import {join} from 'node:path';
import {FRESH_COOKIE,FRESH_ISSUER,LEGACY_ISSUER,REALM_AUTHORITY,realmIdentity} from '../../../../../website/red-xai.com/identity-realm.mjs';

const directory=process.argv[2];
assert.equal(process.argv.length,3);assert.ok(directory);
Object.assign(process.env,{
  DATABASE_URL:`file:${join(directory!,'fixture.db')}`,
  REDX_DATA_DIR:directory,
  BETTER_AUTH_SECRET:'00000000000000000000000000000000',
  CREDENTIAL_SECRET:'11111111111111111111111111111111',
  OWNER_EMAIL:'owner@example.invalid',
  REDX_WEBSITE_SESSION:'true',REDX_WEBSITE_SESSION_REALMS:'prototype-v1',
  REDX_WEBSITE_OWNER_ACCOUNT_IDS:'verified-owner',
  REDX_PUBLIC_URL:'https://red-xai.test/host',WEBSITE_URL:'https://red-xai.test/host',NODE_ENV:'test',
  AI_GATEWAY_BASE_URL:'https://gateway.example.invalid/v1',AI_GATEWAY_API_KEY:'synthetic-owner-gateway',
  GITHUB_REPO:'synthetic-owner/deployment-repo',
});
const client=createClient({url:process.env.DATABASE_URL!});
try {
  const schema=await import('../database/schema');
  const {generateSQLiteDrizzleJson,generateSQLiteMigration}=await import('drizzle-kit/api');
  const empty=await generateSQLiteDrizzleJson({}),full=await generateSQLiteDrizzleJson(schema);
  for(const statement of await generateSQLiteMigration(empty,full))await client.execute(statement);
  const {call}=await import('@orpc/server');
  const {projects}=await import('../routes/projects');
  const {builds}=await import('../routes/builds');
  const {hostRealmOwnership}=await import('./host-website-identity');
  const {withTenantContext}=await import('./tenant-context');
  const {saveCredential}=await import('./credentials');
  await hostRealmOwnership.initialize();
  const identities={
    owner:{...realmIdentity(LEGACY_ISSUER,'verified-owner'),owner:true},
    legacy:{...realmIdentity(LEGACY_ISSUER,'normal-legacy'),owner:false},
    fresh:{...realmIdentity(FRESH_ISSUER,'normal-fresh'),owner:false},
  };
  type Who=keyof typeof identities;
  const headers=(who:Who)=>new Headers({cookie:`${FRESH_COOKIE}=${who}`});
  const providerCalls:{url:string;authorization:string|null}[]=[];
  globalThis.fetch=(async(url,init)=>{
    const target=String(url),requestHeaders=new Headers(init?.headers);
    if(target===REALM_AUTHORITY) {
      const who=requestHeaders.get('cookie')?.split('=')[1] as Who;
      const p=identities[who];assert.ok(p,'only known fixture identities are accepted');
      return Response.json({identityRealm:{version:1,issuer:p.issuer,subject:p.subject,canonicalId:p.id},
        account:{id:p.id,email:'same@example.invalid',roles:[p.owner?'owner':'user'],emailVerified:true,active:true},
        active:true,amr:p.owner?['pwd','totp']:['pwd'],expiresMs:Date.now()+60000});
    }
    providerCalls.push({url:target,authorization:requestHeaders.get('authorization')});
    if(target.endsWith('/chat/completions'))return Response.json({choices:[{message:{content:JSON.stringify({runtime:'static',port:8080,dockerfile:'FROM scratch',notes:'synthetic plan'})}}]});
    assert.ok(target.startsWith('https://api.github.com/repos/'),'no external fetch is allowed');
    assert.ok(target.endsWith('/dispatches'),'only synthetic workflow dispatches are allowed');
    return new Response(null,{status:204});
  }) as typeof fetch;
  const projectIds=new Map<Who,string>();
  const save=async(who:Who,provider:'github'|'openai',values:Record<string,string>)=>{
    const p=identities[who],tenantId=await hostRealmOwnership.resolve(p);
    await withTenantContext({tenantId,isOwner:p.owner,userId:p.id},()=>saveCredential(provider,values));
  };
  for(const who of Object.keys(identities) as Who[]) {
    const p=await call(projects.create,{name:'Synthetic '+who},{context:{headers:headers(who)}});
    projectIds.set(who,p.id);
    await save(who,'github',{token:'synthetic-'+who});
  }
  const plan=(who:Who)=>call(projects.aiPlan,{id:projectIds.get(who)!},{context:{headers:headers(who)}});
  for(const who of ['legacy','fresh'] as const) {
    const before=providerCalls.length,result=await plan(who);
    assert.equal(result.ok,false,'normal tenants without their own key cannot use the deployment gateway');
    assert.equal(providerCalls.length,before,'refused normal-tenant planning must not contact a provider');
    const target=await call(builds.targets,undefined,{context:{headers:headers(who)}});
    assert.equal(target.repoConnected,false);assert.equal(target.repo,null);
    const dispatched=await call(builds.dispatch,{platform:'linux',projectId:projectIds.get(who)!},{context:{headers:headers(who)}});
    assert.equal(dispatched.ok,false,'normal tenants must configure their own repository');
    assert.equal(providerCalls.length,before,'missing tenant repository must not contact GitHub');
  }
  assert.equal((await plan('owner')).ok,true,'verified owner retains the deployment gateway fallback');
  assert.ok(providerCalls.at(-1)?.url.startsWith('https://gateway.example.invalid/'));
  assert.ok(providerCalls.at(-1)?.authorization==='Bearer synthetic-owner-gateway','owner gateway credential remains scoped');
  const ownerTarget=await call(builds.targets,undefined,{context:{headers:headers('owner')}});
  assert.equal(ownerTarget.repo,'synthetic-owner/deployment-repo');assert.equal(ownerTarget.repoConnected,true);
  assert.equal((await call(builds.dispatch,{platform:'linux'},{context:{headers:headers('owner')}})).ok,true);
  assert.ok(providerCalls.at(-1)?.url.includes('/synthetic-owner/deployment-repo/'));
  assert.ok(providerCalls.at(-1)?.authorization==='Bearer synthetic-owner','owner GitHub credential remains scoped');
  for(const who of ['legacy','fresh'] as const) {
    await save(who,'openai',{apiKey:'synthetic-openai-'+who});
    assert.equal((await plan(who)).ok,true,'normal tenant may use its own AI key');
    assert.equal(providerCalls.at(-1)?.url,'https://api.openai.com/v1/chat/completions');
    assert.ok(providerCalls.at(-1)?.authorization==='Bearer synthetic-openai-'+who,'AI credential must match the owning tenant');
    await save(who,'github',{token:'synthetic-'+who,repo:'synthetic-'+who+'/own-repo'});
    const target=await call(builds.targets,undefined,{context:{headers:headers(who)}});
    assert.equal(target.repo,'synthetic-'+who+'/own-repo');assert.equal(target.repoConnected,true);
    assert.equal((await call(builds.dispatch,{platform:'linux'},{context:{headers:headers(who)}})).ok,true);
    assert.ok(providerCalls.at(-1)?.url.includes('/synthetic-'+who+'/own-repo/'));
    assert.ok(providerCalls.at(-1)?.authorization==='Bearer synthetic-'+who,'GitHub credential must match the owning tenant');
  }
} finally {client.close();}
