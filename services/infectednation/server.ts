import { Database } from "bun:sqlite";
import { mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { createHash, randomBytes, timingSafeEqual } from "node:crypto";

const PORT=Number(process.env.PORT||8787);
const HOST=process.env.HOST||"0.0.0.0";
const DATA=resolve(
  process.env.INFECTEDNATION_DATA ||
  (process.env.REDX_PERSIST_ROOT ? process.env.REDX_PERSIST_ROOT + "/infectednation.sqlite" : "./data/infectednation.sqlite"),
);
const TERMS_VERSION="2026-09-23";
const PRIVACY_VERSION="2026-09-23";
const SESSION_TTL=30*24*60*60*1000;
const SECURITY_RETENTION=180*24*60*60*1000;
mkdirSync(dirname(DATA),{recursive:true});
const db=new Database(DATA,{create:true,strict:true});

db.exec(`
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS accounts(
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL COLLATE NOCASE UNIQUE,
 email TEXT NOT NULL COLLATE NOCASE UNIQUE,
 password_hash TEXT NOT NULL,
 dob TEXT NOT NULL,
 first_name TEXT NOT NULL,
 middle_initial TEXT NOT NULL DEFAULT '',
 last_name TEXT NOT NULL,
 use_type TEXT NOT NULL,
 company_name TEXT NOT NULL DEFAULT '',
 terms_version TEXT NOT NULL,
 privacy_version TEXT NOT NULL,
 terms_accepted_at INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'active',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL UNIQUE,
 app_id TEXT NOT NULL,
 method TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 last_seen_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 ip TEXT NOT NULL DEFAULT '',
 user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_account_idx ON sessions(account_id);
CREATE TABLE IF NOT EXISTS security_events(
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 type TEXT NOT NULL,
 at INTEGER NOT NULL,
 ip TEXT NOT NULL DEFAULT '',
 user_agent TEXT NOT NULL DEFAULT '',
 method TEXT NOT NULL DEFAULT '',
 app_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS security_account_at_idx ON security_events(account_id,at);
CREATE TABLE IF NOT EXISTS connect_requests(
 id TEXT PRIMARY KEY,
 secret_hash TEXT NOT NULL,
 app_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 account_id TEXT,
 approved_at INTEGER NOT NULL DEFAULT 0,
 consumed_at INTEGER NOT NULL DEFAULT 0
);
`);

const json=(data:unknown,status=200,headers:Record<string,string>={})=>
 new Response(JSON.stringify(data),{status,headers:{"content-type":"application/json; charset=utf-8","cache-control":"no-store",...headers}});
const sha=(s:string)=>createHash("sha256").update(s).digest("hex");
const id=(prefix:string)=>prefix+"_"+randomBytes(18).toString("base64url");
const normalizeEmail=(v:string)=>v.trim().toLowerCase();
const normalizeUsername=(v:string)=>v.trim();
function safeEqual(a:string,b:string){const x=Buffer.from(a),y=Buffer.from(b);return x.length===y.length&&timingSafeEqual(x,y);}
function validApp(v:unknown){return typeof v==="string"&&/^[a-z0-9][a-z0-9-]{2,63}$/.test(v);}
function usernameError(v:unknown){
 if(typeof v!=="string")return "Username is required.";
 const s=v.trim();
 if(s.length<4||s.length>24)return "Username must be 4–24 characters.";
 if(!/[A-Za-z]/.test(s))return "Username must contain at least one letter.";
 if(!/^[A-Za-z0-9_.-]+$/.test(s))return "Username may use letters, numbers, underscore, period and hyphen.";
 return "";
}
function passwordError(v:unknown){
 if(typeof v!=="string"||v.length<8||v.length>128)return "Password must be 8–128 characters.";
 if(!/[A-Z]/.test(v))return "Password needs an uppercase letter.";
 if(!/[a-z]/.test(v))return "Password needs a lowercase letter.";
 if(!/[0-9]/.test(v))return "Password needs a number.";
 if(!/[^A-Za-z0-9]/.test(v))return "Password needs a special symbol.";
 return "";
}
function dobError(v:unknown){
 if(typeof v!=="string"||!/^\d{4}-\d{2}-\d{2}$/.test(v))return "Enter a valid date of birth.";
 const d=new Date(v+"T00:00:00Z");
 if(!Number.isFinite(d.getTime())||d.toISOString().slice(0,10)!==v)return "Enter a valid date of birth.";
 const n=new Date(),cutoff=new Date(Date.UTC(n.getUTCFullYear()-13,n.getUTCMonth(),n.getUTCDate()));
 if(d>cutoff)return "InfectedNation accounts are currently available to people age 13 and older.";
 return "";
}
function requestIp(req:Request,server:any){
 if(process.env.TRUST_CLOUDFLARE==="true"){
  const cf=req.headers.get("cf-connecting-ip");if(cf&&cf.length<=64)return cf;
 }
 const raw=server.requestIP(req)?.address||"";
 return String(raw).slice(0,64);
}
async function body(req:Request){try{return await req.json() as any;}catch{return {};}}
function accountPublic(row:any){
 return {accountId:row.id,username:row.username,email:row.email,firstName:row.first_name,middleInitial:row.middle_initial,lastName:row.last_name,dob:row.dob,useType:row.use_type,companyName:row.company_name,termsVersion:row.terms_version,privacyVersion:row.privacy_version,createdAt:row.created_at};
}
function logSecurity(accountId:string,type:string,req:Request,server:any,method="",appId=""){
 db.prepare("INSERT INTO security_events(id,account_id,type,at,ip,user_agent,method,app_id) VALUES(?,?,?,?,?,?,?,?)")
  .run(id("sec"),accountId,type,Date.now(),requestIp(req,server),String(req.headers.get("user-agent")||"").slice(0,300),method,appId);
 db.prepare("DELETE FROM security_events WHERE account_id=? AND at<?").run(accountId,Date.now()-SECURITY_RETENTION);
 const rows=db.prepare("SELECT id FROM security_events WHERE account_id=? ORDER BY at DESC LIMIT -1 OFFSET 50").all(accountId) as any[];
 for(const r of rows)db.prepare("DELETE FROM security_events WHERE id=?").run(r.id);
}
function createSession(accountId:string,appId:string,method:string,req:Request,server:any){
 const token="inat_"+accountId+"."+randomBytes(32).toString("base64url"),now=Date.now();
 db.prepare("DELETE FROM sessions WHERE account_id=? AND expires_at<=?").run(accountId,now);
 const old=db.prepare("SELECT id FROM sessions WHERE account_id=? ORDER BY last_seen_at DESC LIMIT -1 OFFSET 20").all(accountId) as any[];
 for(const r of old)db.prepare("DELETE FROM sessions WHERE id=?").run(r.id);
 db.prepare("INSERT INTO sessions(id,account_id,token_hash,app_id,method,created_at,last_seen_at,expires_at,ip,user_agent) VALUES(?,?,?,?,?,?,?,?,?,?)")
  .run(id("ses"),accountId,sha(token),appId,method,now,now,now+SESSION_TTL,requestIp(req,server),String(req.headers.get("user-agent")||"").slice(0,300));
 logSecurity(accountId,"login",req,server,method,appId);
 return token;
}
function nationSession(token:unknown){
 if(typeof token!=="string"||!token.startsWith("inat_"))return null;
 const dot=token.indexOf(".");if(dot<10)return null;
 const accountId=token.slice(5,dot);
 const session=db.prepare("SELECT * FROM sessions WHERE account_id=? AND token_hash=? AND expires_at>?").get(accountId,sha(token),Date.now()) as any;
 if(!session)return null;
 const account=db.prepare("SELECT * FROM accounts WHERE id=? AND status='active'").get(accountId) as any;
 if(!account)return null;
 if(Date.now()-session.last_seen_at>600000)db.prepare("UPDATE sessions SET last_seen_at=? WHERE id=?").run(Date.now(),session.id);
 return {account,session};
}
function cors(req:Request){
 const origin=req.headers.get("origin")||"";
 if(["capacitor://localhost","http://localhost","https://localhost"].includes(origin))return {"access-control-allow-origin":origin,"vary":"origin"};
 return {};
}
async function api(req:Request,server:any,url:URL){
 const path=url.pathname;
 if(req.method==="OPTIONS")return new Response(null,{status:204,headers:{...cors(req),"access-control-allow-methods":"POST, OPTIONS","access-control-allow-headers":"content-type","access-control-max-age":"600"}});
 if(path==="/api/health")return json({ok:true,service:"InfectedNation"});
 if(path==="/api/v1/meta")return json({service:"InfectedNation",termsVersion:TERMS_VERSION,privacyVersion:PRIVACY_VERSION,securityIpRetentionDays:180});
 const b=await body(req);
 if(path==="/api/v1/username/check"){
  const e=usernameError(b.username);if(e)return json({available:false,reason:e});
  const row=db.prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE").get(normalizeUsername(b.username));
  return json({available:!row,reason:row?"That username is already taken.":""});
 }
 if(path==="/api/v1/signup/email"){
  if(!validApp(b.appId))return json({error:"Invalid application identifier."},400);
  const ue=usernameError(b.username),pe=passwordError(b.password),de=dobError(b.dob);
  if(ue||pe||de)return json({error:ue||pe||de},400);
  const email=normalizeEmail(String(b.email||""));
  if(!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))return json({error:"Enter a valid email address."},400);
  if(!String(b.firstName||"").trim()||!String(b.lastName||"").trim())return json({error:"First and last name are required."},400);
  const mi=String(b.middleInitial||"").trim().toUpperCase();if(mi&&!/^[A-Z]$/.test(mi))return json({error:"Middle initial must be one letter."},400);
  if(!["personal","company"].includes(b.useType))return json({error:"Choose personal or company use."},400);
  if(b.useType==="company"&&!String(b.companyName||"").trim())return json({error:"Company name is required."},400);
  if(b.termsAccepted!==true||b.termsScrollCompleted!==true||b.termsVersion!==TERMS_VERSION||b.privacyVersion!==PRIVACY_VERSION)return json({error:"Read and accept the current Terms and Privacy Policy."},400);
  if(db.prepare("SELECT id FROM accounts WHERE username=? COLLATE NOCASE OR email=? COLLATE NOCASE").get(String(b.username).trim(),email))return json({error:"Username or email is already registered."},409);
  const aid=id("nat"),now=Date.now(),hash=await Bun.password.hash(b.password,{algorithm:"argon2id"});
  db.prepare("INSERT INTO accounts(id,username,email,password_hash,dob,first_name,middle_initial,last_name,use_type,company_name,terms_version,privacy_version,terms_accepted_at,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,?)")
   .run(aid,String(b.username).trim(),email,hash,b.dob,String(b.firstName).trim(),mi,String(b.lastName).trim(),b.useType,b.useType==="company"?String(b.companyName).trim():"",TERMS_VERSION,PRIVACY_VERSION,now,now,now);
  const token=createSession(aid,b.appId,"email",req,server);
  return json({token,account:accountPublic(db.prepare("SELECT * FROM accounts WHERE id=?").get(aid))},201);
 }
 if(path==="/api/v1/login/email"){
  if(!validApp(b.appId))return json({error:"Invalid application identifier."},400);
  const row=db.prepare("SELECT * FROM accounts WHERE email=? COLLATE NOCASE AND status='active'").get(normalizeEmail(String(b.email||""))) as any;
  if(!row||!await Bun.password.verify(String(b.password||""),row.password_hash)){if(row)logSecurity(row.id,"password_failure",req,server,"email",String(b.appId||""));return json({error:"Email or password is incorrect."},401);}
  const token=createSession(row.id,b.appId,"email",req,server);return json({token,account:accountPublic(row)});
 }
 if(path==="/api/v1/session/me"){const s=nationSession(b.token);return s?json({account:accountPublic(s.account)}):json({error:"Session expired or invalid."},401);}
 if(path==="/api/v1/session/logout"){const s=nationSession(b.token);if(s){db.prepare("DELETE FROM sessions WHERE token_hash=?").run(sha(b.token));logSecurity(s.account.id,"logout",req,server,s.session.method,s.session.app_id);}return json({loggedOut:true});}
 if(path==="/api/v1/security/history"){const s=nationSession(b.token);if(!s)return json({error:"Session expired or invalid."},401);return json({retentionDays:180,items:db.prepare("SELECT id,type,at,ip,user_agent AS userAgent,method,app_id AS appId FROM security_events WHERE account_id=? AND at>=? ORDER BY at DESC LIMIT 50").all(s.account.id,Date.now()-SECURITY_RETENTION)});}
 if(path==="/api/v1/connect/start"){
  if(!validApp(b.appId)||typeof b.secret!=="string"||!/^[a-f0-9]{64}$/i.test(b.secret))return json({error:"Invalid connection request."},400,cors(req));
  const cid=id("con"),now=Date.now();db.prepare("INSERT INTO connect_requests(id,secret_hash,app_id,created_at,expires_at) VALUES(?,?,?,?,?)").run(cid,sha(b.secret),b.appId,now,now+600000);
  return json({id:cid,expires:now+600000},201,cors(req));
 }
 if(path==="/api/v1/connect/info"){const r=db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id||"")) as any;return r&&r.expires_at>Date.now()&&!r.consumed_at?json({appId:r.app_id,expires:r.expires_at,approved:!!r.account_id}):json({error:"Connection request expired or unavailable."},404);}
 if(path==="/api/v1/connect/approve"){const s=nationSession(b.token);if(!s)return json({error:"Sign in to approve this connection."},401);const r=db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id||"")) as any;if(!r||r.expires_at<=Date.now()||r.consumed_at)return json({error:"Connection request expired or unavailable."},404);db.prepare("UPDATE connect_requests SET account_id=?,approved_at=? WHERE id=?").run(s.account.id,Date.now(),r.id);return json({approved:true,appId:r.app_id});}
 if(path==="/api/v1/connect/session"){const r=db.prepare("SELECT * FROM connect_requests WHERE id=?").get(String(b.id||"")) as any;if(!r||r.expires_at<=Date.now()||r.consumed_at)return json({error:"Connection request expired or unavailable."},404,cors(req));if(!safeEqual(r.secret_hash,sha(String(b.secret||""))))return json({error:"Connection proof is invalid."},403,cors(req));if(!r.account_id)return json({pending:true,expires:r.expires_at},202,cors(req));const account=db.prepare("SELECT * FROM accounts WHERE id=? AND status='active'").get(r.account_id) as any;if(!account)return json({error:"Account is unavailable."},403,cors(req));const token=createSession(account.id,r.app_id,"infectednation-connect",req,server);db.prepare("UPDATE connect_requests SET consumed_at=? WHERE id=?").run(Date.now(),r.id);return json({pending:false,token,account:accountPublic(account)},200,cors(req));}
 return json({error:"Not found."},404);
}

const index=await Bun.file(new URL("./public/index.html",import.meta.url)).text();
const server=Bun.serve({hostname:HOST,port:PORT,fetch(req,server){const u=new URL(req.url);if(u.pathname.startsWith("/api/"))return api(req,server,u);return new Response(index,{headers:{"content-type":"text/html; charset=utf-8"}});}});
console.log(`InfectedNation listening on http://${HOST}:${server.port}`);
