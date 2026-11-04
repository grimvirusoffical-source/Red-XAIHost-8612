import type {Client,Transaction,InStatement,ResultSet} from '@libsql/client';
import {LEGACY_ISSUER,FRESH_ISSUER,realmIdentity,legacyNativeIdentityAllowed} from '../../../../../website/red-xai.com/identity-realm.mjs';

export const HOST_TENANT_TABLES = ['nodes','projects','deployments','project_replicas','domains','credentials','usage_events','app_builds','activity'] as const;
type SQL = Pick<Client,'execute'> | Pick<Transaction,'execute'>;
type Principal = {issuer:string;subject:string;id:string;owner:boolean};
const META='host_realm_metadata',BIND='host_realm_bindings';
const TABLES=[
  `CREATE TABLE ${META}(name TEXT PRIMARY KEY,value TEXT NOT NULL)`,
  `CREATE TABLE ${BIND}(storage_key TEXT PRIMARY KEY,issuer TEXT NOT NULL,subject TEXT NOT NULL,canonical_id TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('legacy','fresh','owner')),UNIQUE(issuer,subject,kind),CHECK((kind IN ('legacy','owner') AND issuer='${LEGACY_ISSUER}' AND storage_key=subject AND substr(storage_key,1,1)<>' ') OR (kind='fresh' AND issuer='${FRESH_ISSUER}' AND substr(storage_key,1,6)=' rxf1:')))`,
];
const IMMUTABLE = [
  `CREATE TRIGGER host_realm_binding_immutable_insert BEFORE INSERT ON ${BIND} WHEN EXISTS (SELECT 1 FROM ${BIND} WHERE (storage_key=NEW.storage_key OR (issuer=NEW.issuer AND subject=NEW.subject AND kind=NEW.kind)) AND NOT (storage_key=NEW.storage_key AND issuer=NEW.issuer AND subject=NEW.subject AND canonical_id=NEW.canonical_id AND kind=NEW.kind)) BEGIN SELECT RAISE(ABORT,'immutable realm binding'); END`,
  `CREATE TRIGGER host_realm_binding_immutable_update BEFORE UPDATE ON ${BIND} BEGIN SELECT RAISE(ABORT,'immutable realm binding'); END`,
  `CREATE TRIGGER host_realm_binding_immutable_delete BEFORE DELETE ON ${BIND} BEGIN SELECT RAISE(ABORT,'immutable realm binding'); END`,
];
const TRIGGERS=HOST_TENANT_TABLES.flatMap(table=>[
  `CREATE TRIGGER host_realm_${table}_insert BEFORE INSERT ON ${table} WHEN NOT EXISTS (SELECT 1 FROM ${BIND} WHERE storage_key=NEW.tenant_id) OR EXISTS (SELECT 1 FROM ${table} WHERE id=NEW.id AND tenant_id IS NOT NEW.tenant_id) BEGIN SELECT RAISE(ABORT,'immutable or unbound tenant identity'); END`,
  `CREATE TRIGGER host_realm_${table}_update BEFORE UPDATE OF tenant_id ON ${table} WHEN NEW.tenant_id IS NOT OLD.tenant_id OR NOT EXISTS (SELECT 1 FROM ${BIND} WHERE storage_key=NEW.tenant_id) BEGIN SELECT RAISE(ABORT,'immutable or unbound tenant identity'); END`,
]);
const EXPECTED=[...IMMUTABLE,...TRIGGERS];
const normalize=(s:string)=>s.replace(/\s+/g,' ').trim().toLowerCase();
const refused=()=>{throw new Error('Host realm ownership metadata refused');};

async function ready(sql:SQL):Promise<boolean> {
  const r=await sql.execute("SELECT type,name,sql FROM sqlite_master WHERE name IN ('host_realm_metadata','host_realm_bindings') OR name LIKE 'host_realm_%'");
  if(!r.rows.length)return false;
  if(!r.rows.some(x=>x.name===META&&x.type==='table')||!r.rows.some(x=>x.name===BIND&&x.type==='table'))return refused();
  const tables=new Set(r.rows.filter(x=>x.type==='table').map(x=>normalize(String(x.sql))));
  if(TABLES.some(x=>!tables.has(normalize(x))))return refused();
  const version=await sql.execute(`SELECT value FROM ${META} WHERE name='schema'`);
  if(version.rows.length!==1||version.rows[0].value!=='1')return refused();
  const observed=new Set(r.rows.filter(x=>x.type==='trigger').map(x=>normalize(String(x.sql))));
  if(EXPECTED.some(x=>!observed.has(normalize(x))))return refused();
  return true;
}

function claim(p:Principal) {
  if(p.owner) {
    if(p.issuer!==LEGACY_ISSUER)return refused();
    return {key:'owner',issuer:LEGACY_ISSUER,subject:'owner',canonical:'owner',kind:'owner'};
  }
  if(p.issuer===LEGACY_ISSUER) {
    if(!legacyNativeIdentityAllowed({issuer:p.issuer,id:p.subject})||p.id!==p.subject||p.subject==='__unassigned__')return refused();
    return {key:p.subject,issuer:p.issuer,subject:p.subject,canonical:p.subject,kind:'legacy'};
  }
  const v=realmIdentity(p.issuer,p.subject);
  if(p.issuer!==FRESH_ISSUER||v.id!==p.id)return refused();
  return {key:v.ownershipKey,issuer:v.issuer,subject:v.subject,canonical:v.id,kind:'fresh'};
}

function statements(c:ReturnType<typeof claim>):InStatement[] {
  return [
    {sql:`INSERT OR IGNORE INTO ${BIND}(storage_key,issuer,subject,canonical_id,kind) VALUES (?,?,?,?,?)`,args:[c.key,c.issuer,c.subject,c.canonical,c.kind]},
    {sql:`SELECT storage_key,issuer,subject,canonical_id,kind FROM ${BIND} WHERE storage_key=?`,args:[c.key]},
    {sql:`SELECT storage_key FROM ${BIND} WHERE issuer=? AND subject=? AND kind=?`,args:[c.issuer,c.subject,c.kind]},
  ];
}
function verifyBinding(c:ReturnType<typeof claim>,r:ResultSet,tuple:ResultSet) {
  const v=r.rows[0];
  if(r.rows.length!==1||v.storage_key!==c.key||v.issuer!==c.issuer||v.subject!==c.subject||v.canonical_id!==c.canonical||v.kind!==c.kind)return refused();
  if(tuple.rows.length!==1||tuple.rows[0].storage_key!==c.key)return refused();
}
async function bind(sql:SQL,c:ReturnType<typeof claim>) {
  const [insert,select,tuple]=statements(c);
  await sql.execute(insert!);verifyBinding(c,await sql.execute(select!),await sql.execute(tuple!));
}

export class HostRealmOwnership {
  constructor(private readonly client:Client) {}

  /** Explicit maintenance migration. No login invokes this. Unlabelled rows
   * are legacy only; existing identifiers/resources are never rewritten. */
  async initialize():Promise<void> {
    const tx=await this.client.transaction('write');
    try {
      if(await ready(tx)){await tx.commit();return;}
      const found=await tx.execute("SELECT name FROM sqlite_master WHERE type='table'");
      if(HOST_TENANT_TABLES.some(name=>!found.rows.some(x=>x.name===name)))return refused();
      const legacy=new Set<string>(['owner']);
      for(const table of HOST_TENANT_TABLES) {
        const rows=await tx.execute(`SELECT DISTINCT tenant_id FROM ${table}`);
        for(const r of rows.rows) {
          if(typeof r.tenant_id!=='string'||!legacyNativeIdentityAllowed({id:r.tenant_id})||r.tenant_id==='__unassigned__')return refused();
          legacy.add(r.tenant_id);
        }
      }
      if(found.rows.some(x=>x.name==='user')) {
        const users=await tx.execute('SELECT redx_account_id FROM user WHERE redx_account_id IS NOT NULL');
        for(const r of users.rows) {
          if(typeof r.redx_account_id!=='string'||!legacyNativeIdentityAllowed({id:r.redx_account_id}))return refused();
          legacy.add(r.redx_account_id);
        }
      }
      for(const table of TABLES)await tx.execute(table);
      for(const id of legacy)await bind(tx,claim({issuer:LEGACY_ISSUER,subject:id,id,owner:id==='owner'}));
      for(const trigger of EXPECTED)await tx.execute(trigger);
      await tx.execute(`INSERT INTO ${META}(name,value) VALUES ('schema','1')`);
      if(!await ready(tx))return refused();
      await tx.commit();
    } catch(e) {await tx.rollback().catch(()=>{});throw e;} finally {tx.close();}
  }

  /** Called only after live authority validation, never from client headers. */
  async resolve(p:Principal):Promise<string> {
    const c=claim(p);
    if(!await ready(this.client)) {
      if(c.kind==='fresh')return refused();
      return c.key; // Legacy-only installations never auto-migrate at login.
    }
    const binding=statements(c);
    let [row,tuple]=await this.client.batch(binding.slice(1),'read');
    // Bound accounts do not take a writer lock on every protected request.
    // Binding rows and their tuple mapping are immutable once registered.
    if(row!.rows.length===0&&tuple!.rows.length===0) {
      // A single atomic batch avoids retaining a transaction across JS yields.
      // Concurrent local SQLite requests must not synchronously block the writer
      // whose continuation is waiting on that same event loop.
      [,row,tuple]=await this.client.batch(binding,'write');
    }
    verifyBinding(c,row!,tuple!);
    if(!await ready(this.client))return refused();
    return c.key;
  }
}
