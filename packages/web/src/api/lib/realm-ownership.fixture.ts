import assert from 'node:assert/strict';
import {createClient} from '@libsql/client';
import {join} from 'node:path';
import {HostRealmOwnership,HOST_TENANT_TABLES} from './realm-ownership';
import {FRESH_COOKIE,FRESH_ISSUER,LEGACY_ISSUER,REALM_AUTHORITY,realmIdentity} from '../../../../../website/red-xai.com/identity-realm.mjs';

const mode=process.argv[2],directory=process.argv[3];
assert.equal(process.argv.length,4);assert.ok(directory);
const client=createClient({url:`file:${join(directory!,'fixture.db')}`});
const owners=new HostRealmOwnership(client);
const principal=(issuer:string,subject:string)=>({...realmIdentity(issuer,subject),owner:false});
const a=principal(FRESH_ISSUER,'same-subject'),b=principal(FRESH_ISSUER,'other-subject');
const legacy=principal(LEGACY_ISSUER,a.id);
try {
  if(mode==='router-isolation') {
    Object.assign(process.env,{DATABASE_URL:`file:${join(directory!,'fixture.db')}`,BETTER_AUTH_SECRET:'00000000000000000000000000000000',CREDENTIAL_SECRET:'11111111111111111111111111111111',OWNER_EMAIL:'owner@example.invalid',REDX_WEBSITE_SESSION:'true',REDX_WEBSITE_SESSION_REALMS:'prototype-v1',REDX_WEBSITE_OWNER_ACCOUNT_IDS:'owner-id',REDX_PUBLIC_URL:'https://red-xai.test/host',WEBSITE_URL:'https://red-xai.test/host',NODE_ENV:'test'});
    const schema=await import('../database/schema');
    const {generateSQLiteDrizzleJson,generateSQLiteMigration}=await import('drizzle-kit/api');
    const empty=await generateSQLiteDrizzleJson({}),full=await generateSQLiteDrizzleJson(schema);
    for(const statement of await generateSQLiteMigration(empty,full))await client.execute(statement);
    const {call}=await import('@orpc/server');
    const {nodes}=await import('../routes/nodes');
    const {projects}=await import('../routes/projects');
    const {settings}=await import('../routes/settings');
    const {hostRealmOwnership}=await import('./host-website-identity');
    await hostRealmOwnership.initialize();
    const header=(who:string)=>new Headers({cookie:who==='legacy'?'redx_session=collision':`${FRESH_COOKIE}=${who}`,'x-redx-account-id':legacy.id,'x-redx-role':'owner','x-redx-realm':LEGACY_ISSUER});
    globalThis.fetch=(async(url,init)=>{
      assert.equal(String(url),REALM_AUTHORITY);
      const cookie=new Headers(init?.headers).get('cookie'),p=cookie?.startsWith('redx_session=')?legacy:cookie?.endsWith('=a')?a:b;
      return Response.json({identityRealm:{version:1,issuer:p.issuer,subject:p.subject,canonicalId:p.id},account:{id:p.id,email:'shared@example.invalid',roles:['user'],emailVerified:true,active:true},active:true,amr:['pwd'],expiresMs:Date.now()+60000});
    }) as typeof fetch;
    const resources=new Map<string,{nodeId:string;projectId:string}>();
    for(const who of ['legacy','a','b']) {
      const context={headers:header(who)};
      const n=await call(nodes.create,{name:'Synthetic '+who,kind:'vps'},{context});
      const p=await call(projects.create,{name:'Synthetic '+who,nodeId:n.id},{context});
      resources.set(who,{nodeId:n.id,projectId:p.id});
      const key=who==='legacy'?legacy.id:who==='a'?a.ownershipKey:b.ownershipKey;
      await client.execute({sql:"INSERT INTO credentials(id,tenant_id,label,secret_enc,hint) VALUES (?,?,'Synthetic','fixture-sealed',?)",args:[`${key}:openai`,key,who]});
    }
    for(const who of ['legacy','a','b']) {
      const context={headers:header(who)},own=resources.get(who)!;
      assert.deepEqual((await call(nodes.list,undefined,{context})).map(x=>x.id),[own.nodeId]);
      assert.deepEqual((await call(projects.list,undefined,{context})).map(x=>x.id),[own.projectId]);
      const providers=await call(settings.providers,undefined,{context});assert.equal(providers.find(x=>x.id==='openai')?.hint,who);
      for(const other of ['legacy','a','b'].filter(x=>x!==who)) {
        const foreign=resources.get(other)!;
        await assert.rejects(()=>call(nodes.rotateToken,{id:foreign.nodeId},{context}),{code:'NOT_FOUND'});
        await assert.rejects(()=>call(projects.get,{id:foreign.projectId},{context}),{code:'NOT_FOUND'});
        await assert.rejects(()=>call(projects.create,{name:'Cross tenant refused',nodeId:foreign.nodeId},{context}),{code:'BAD_REQUEST'});
      }
    }
    assert.equal((await client.execute('SELECT count(*) AS n FROM projects')).rows[0]!.n,3);
  } else if(mode!=='restart-and-verify')for(const table of HOST_TENANT_TABLES)await client.execute(`CREATE TABLE ${table}(id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,payload TEXT)`);
  if(mode==='create-and-isolate') {
    for(const table of HOST_TENANT_TABLES)await client.execute({sql:`INSERT INTO ${table} VALUES (?,?,?)`,args:['legacy-row',legacy.id,'legacy-only']});
    assert.equal(await owners.resolve(legacy),legacy.id);
    await assert.rejects(()=>owners.resolve(a),/metadata refused/);
    await owners.initialize();await owners.initialize();
    const [aKey,bKey]=await Promise.all([owners.resolve(a),owners.resolve(b)]);
    assert.notEqual(aKey,legacy.id);assert.notEqual(aKey,bKey);assert.equal(await owners.resolve(legacy),legacy.id);
    for(const table of HOST_TENANT_TABLES) {
      await client.execute({sql:`INSERT INTO ${table} VALUES (?,?,?)`,args:['fresh-a',aKey,'fresh-a-only']});
      await client.execute({sql:`INSERT INTO ${table} VALUES (?,?,?)`,args:['fresh-b',bKey,'fresh-b-only']});
      assert.deepEqual((await client.execute({sql:`SELECT payload FROM ${table} WHERE tenant_id=?`,args:[legacy.id]})).rows.map(x=>x.payload),['legacy-only']);
      assert.deepEqual((await client.execute({sql:`SELECT payload FROM ${table} WHERE tenant_id=?`,args:[aKey]})).rows.map(x=>x.payload),['fresh-a-only']);
      await assert.rejects(()=>client.execute({sql:`UPDATE ${table} SET tenant_id=? WHERE id='legacy-row'`,args:[aKey]}));
      await assert.rejects(()=>client.execute({sql:`INSERT OR REPLACE INTO ${table} VALUES (?,?,?)`,args:['legacy-row',aKey,'stolen']}));
      await assert.rejects(()=>client.execute({sql:`INSERT INTO ${table} VALUES (?,?,?)`,args:['unbound','future-legacy','wrong']}));
    }
    await owners.resolve(principal(LEGACY_ISSUER,'future-legacy'));
    await client.execute("INSERT INTO nodes VALUES ('future','future-legacy','correctly-bound')");
    await assert.rejects(()=>client.execute("UPDATE host_realm_bindings SET issuer='urn:attacker'"));
    await assert.rejects(()=>client.execute('DELETE FROM host_realm_bindings'));
    await assert.rejects(()=>client.execute({sql:"INSERT OR REPLACE INTO host_realm_bindings(storage_key,issuer,subject,canonical_id,kind) VALUES (?,?,?,?,'fresh')",args:[aKey,FRESH_ISSUER,'changed-subject',a.id]}));
    await assert.rejects(()=>owners.resolve({issuer:LEGACY_ISSUER,subject:aKey,id:aKey,owner:false}));
    await assert.rejects(()=>owners.resolve({...a,owner:true}));
  } else if(mode==='restart-and-verify') {
    assert.equal(await owners.resolve(a),a.ownershipKey);assert.equal(await owners.resolve(legacy),legacy.id);
    const metadata=await client.execute({sql:'SELECT issuer,subject FROM host_realm_bindings WHERE storage_key=?',args:[a.ownershipKey]});
    assert.equal(metadata.rows[0]!.issuer,FRESH_ISSUER);assert.equal(metadata.rows[0]!.subject,'same-subject');
    for(const table of HOST_TENANT_TABLES)for(const [p,payload]of [[a,'fresh-a-only'],[b,'fresh-b-only'],[legacy,'legacy-only']] as const) {
      const key=await owners.resolve(p);
      assert.deepEqual((await client.execute({sql:`SELECT payload FROM ${table} WHERE tenant_id=?`,args:[key]})).rows.map(x=>x.payload),[payload]);
    }
    await client.execute('DROP TRIGGER host_realm_nodes_insert');
    await assert.rejects(()=>owners.resolve(a),/metadata refused/);
    await assert.rejects(()=>owners.resolve(legacy),/metadata refused/);
  } else if(mode==='refuse-unlabelled') {
    await client.execute({sql:'INSERT INTO nodes VALUES (?,?,?)',args:['unlabelled',a.ownershipKey,'preserve-me']});
    await assert.rejects(()=>owners.initialize(),/metadata refused/);
    assert.equal((await client.execute('SELECT payload FROM nodes')).rows[0]!.payload,'preserve-me');
    assert.equal((await client.execute("SELECT name FROM sqlite_master WHERE name='host_realm_bindings'")).rows.length,0);
  } else if(mode!=='router-isolation')throw Error('Unknown fixture mode');
} finally {client.close();}
