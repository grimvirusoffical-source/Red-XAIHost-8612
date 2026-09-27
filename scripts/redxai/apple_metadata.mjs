import { createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const API = 'https://api.appstoreconnect.apple.com';
export const BUNDLES = ['com.redxai.database', 'com.redxai.host'];
const ISSUER = 'af0c0450-10f2-4027-b34d-5356dfc53f5c';
const TEAM_IDS = ['2184BZOB7XYB', '2NB8LG4FTJ', '7S63D5S479'];
const INDIVIDUAL_ID = 'DBR5N3BTFG23';
const SOURCES = ['RX_ASC_PRIVATE', 'RX_APPLE_TEAM_KEY', 'RX_ASC_PRIVATE_P8'];

// Parse supported representations locally. Never log input or OpenSSL errors.
export function parsePrivateKey(raw) {
  if (typeof raw !== 'string' || !raw.trim() || raw.length > 65536) return null;
  const queue = [{value: raw.replace(/^\uFEFF/, '').trim(), depth: 0}];
  const seen = new Set();
  while (queue.length) {
    const {value, depth} = queue.shift();
    if (typeof value !== 'string' || !value || seen.has(value) || depth > 3) continue;
    seen.add(value);
    const normalized = value.replace(/\\r\\n/g, '\n').replace(/\\n/g, '\n').replace(/\r\n/g, '\n').trim();
    try {
      const key = createPrivateKey(normalized);
      if (key.type === 'private' && key.asymmetricKeyType === 'ec' &&
          key.asymmetricKeyDetails?.namedCurve === 'prime256v1') return key;
    } catch { /* Never include credential material in logs. */ }
    try {
      const data = JSON.parse(value);
      if (typeof data === 'string') queue.push({value:data, depth:depth+1});
      if (data && typeof data === 'object') {
        for (const field of ['private_key','key','key_content','privateKey']) {
          if (typeof data[field] === 'string') queue.push({value:data[field],depth:depth+1});
        }
      }
    } catch { /* Not JSON. */ }
    const compact = normalized.replace(/\s/g, '');
    if (compact.length >= 80 && /^[A-Za-z0-9+/]+={0,2}$/.test(compact)) {
      const decoded = Buffer.from(compact, 'base64');
      if (decoded.toString('base64').replace(/=+$/, '') !== compact.replace(/=+$/, '')) continue;
      try {
        const key = createPrivateKey({key:decoded, format:'der', type:'pkcs8'});
        if (key.asymmetricKeyType === 'ec' && key.asymmetricKeyDetails?.namedCurve === 'prime256v1') return key;
      } catch { /* Not DER. */ }
      queue.push({value:decoded.toString('utf8'),depth:depth+1});
    }
  }
  return null;
}

export function makeJWT(key, keyID, individual, path, method = 'GET') {
  const now = Math.floor(Date.now()/1000);
  const payload = {iat:now-5, exp:now+115, aud:'appstoreconnect-v1'};
  if (individual) payload.sub = 'user'; else payload.iss = ISSUER;
  if (method === 'GET') payload.scope = [`GET ${path}`];
  const enc = x => Buffer.from(JSON.stringify(x)).toString('base64url');
  const unsigned = `${enc({alg:'ES256',kid:keyID,typ:'JWT'})}.${enc(payload)}`;
  const signature = sign('sha256',Buffer.from(unsigned),{key,dsaEncoding:'ieee-p1363'}).toString('base64url');
  return `${unsigned}.${signature}`;
}

function credentialsFromEnv(env, report) {
  const keys = [], publicKeys = new Set();
  const preferred = env.RX_APPLE_TEAM_KEY?.trim();
  const teamIDs = [...new Set([...(preferred && /^[A-Z0-9]{8,16}$/.test(preferred) ? [preferred] : []), ...TEAM_IDS])];
  for (const name of SOURCES) {
    const raw = env[name];
    const key = parsePrivateKey(raw);
    report.credentials.push({source:name.replace(/^RX_/,''),present:Boolean(raw?.trim()),validP256PrivateKey:Boolean(key)});
    if (!key) continue;
    const identity = createPublicKey(key).export({type:'spki',format:'der'}).toString('base64');
    if (publicKeys.has(identity)) continue;
    publicKeys.add(identity);
    keys.push({key,source:name.replace(/^RX_/, '')});
  }
  // Only user-supplied IDs are tested, never generated IDs.
  return keys.flatMap(({key,source}) => [
    ...teamIDs.map(keyID => ({key,source,keyID,individual:false})),
    {key,source,keyID:INDIVIDUAL_ID,individual:true}
  ]);
}

export function makeClient(credential, fetcher = fetch) {
  return async function request(path, params = {}, method = 'GET', attributes = null) {
    if (!/^\/v1\/[A-Za-z][A-Za-z0-9/\-]*$/.test(path)) throw new Error('INVALID_API_PATH');
    if (!['GET','PATCH','POST'].includes(method)) throw new Error('METHOD_NOT_ALLOWED');
    const url = new URL(path,API);
    for (const [key,value] of Object.entries(params)) url.searchParams.set(key,String(value));
    const token = makeJWT(credential.key,credential.keyID,credential.individual,path,method);
    const headers = {Authorization:`Bearer ${token}`,Accept:'application/json'};
    if (attributes) headers['Content-Type']='application/json';
    let response;
    try {
      response = await fetcher(url,{method,headers,body:attributes?JSON.stringify(attributes):undefined,
        redirect:'error',signal:AbortSignal.timeout(25000)});
    } catch { return {ok:false,status:0,codes:['NETWORK_ERROR']}; }
    let body;
    try { body = await response.json(); } catch { body = {}; }
    const codes = (body.errors || []).map(e=>String(e.code || 'UNSPECIFIED').replace(/[^A-Z0-9_.-]/g,'').slice(0,120));
    return {ok:response.ok,status:response.status,codes,body};
  };
}

export async function audit(env = process.env, fetcher = fetch) {
  const report = {operation:'read-only-app-store-audit',at:new Date().toISOString(),credentials:[],authentication:[],apps:[],errors:[]};
  const credentials = credentialsFromEnv(env,report);
  let request;
  let apps;
  for (const candidate of credentials) {
    const client = makeClient(candidate,fetcher);
    const result = await client('/v1/apps',{'filter[bundleId]':BUNDLES.join(','),'fields[apps]':'name,bundleId,sku,primaryLocale',limit:10});
    report.authentication.push({source:candidate.source,keyID:candidate.keyID,kind:candidate.individual?'individual':'team',status:result.status,codes:result.codes});
    if (result.ok) {request=client;apps=result.body.data;break;}
    if ([0,429].includes(result.status) || result.status >= 500) break;
  }
  if (!request) {
    report.errors.push(credentials.length?'APPLE_AUTHENTICATION_NOT_ESTABLISHED':'NO_VALID_P256_PRIVATE_KEY_IN_CONFIGURED_SECRETS');
    return report;
  }
  if (!Array.isArray(apps)) {report.errors.push('UNEXPECTED_APPS_RESPONSE');return report;}
  for (const bundle of BUNDLES) {
    const matches=apps.filter(app=>app.attributes?.bundleId===bundle);
    if(matches.length!==1){report.apps.push({bundleId:bundle,found:false,matchCount:matches.length});continue;}
    const app=matches[0];
    const item={id:app.id,...app.attributes,found:true,infos:[],versions:[],betaLocalizations:[],builds:[],errors:[]};
    report.apps.push(item);
    async function get(path,params={}) {
      const response=await request(path,params);
      if (!response.ok) {item.errors.push({path,status:response.status,codes:response.codes});return null;}
      return response.body;
    }
    const infos=await get(`/v1/apps/${app.id}/appInfos`,{limit:10});
    for(const info of infos?.data||[]) {
      const localizations=await get(`/v1/appInfos/${info.id}/appInfoLocalizations`,{limit:20});
      item.infos.push({id:info.id,...info.attributes,
        primaryCategory:info.relationships?.primaryCategory?.data?.id||null,
        localizations:(localizations?.data||[]).map(l=>({id:l.id,...l.attributes}))});
    }
    const versions=await get(`/v1/apps/${app.id}/appStoreVersions`,{'filter[platform]':'IOS',limit:10});
    for(const version of versions?.data||[]) {
      const localizations=await get(`/v1/appStoreVersions/${version.id}/appStoreVersionLocalizations`,{limit:20});
      const review=await get(`/v1/appStoreVersions/${version.id}/appStoreReviewDetail`,{'fields[appStoreReviewDetails]':'contactFirstName,contactLastName,contactPhone,contactEmail,demoAccountRequired,notes'});
      item.versions.push({id:version.id,...version.attributes,
        localizations:(localizations?.data||[]).map(l=>({id:l.id,...l.attributes})),
        review:review?.data?{id:review.data.id,fieldsPresent:Object.entries(review.data.attributes||{}).filter(([k,v])=>v!==null&&v!=='').map(([k])=>k)}:null});
    }
    const beta=await get(`/v1/apps/${app.id}/betaAppLocalizations`,{limit:20});
    item.betaLocalizations=(beta?.data||[]).map(l=>({id:l.id,...l.attributes}));
    const builds=await get(`/v1/apps/${app.id}/builds`,{'fields[builds]':'version,processingState,uploadedDate,expired',limit:10});
    item.builds=(builds?.data||[]).map(b=>({id:b.id,...b.attributes}));
  }
  return report;
}

async function main() {
  let report;
  try {report=await audit();} catch {report={errors:['AUDIT_INTERNAL_ERROR']};}
  const text=JSON.stringify(report,null,2);
  // Public metadata and field-presence checks only; never credentials or tokens.
  const destination=process.env.RX_REPORT_PATH;
  if(destination) writeFileSync(destination,text+'\n',{mode:0o600});
  console.log(text);
  if(report.errors?.length || report.apps?.some(a=>!a.found||a.errors?.length)) process.exitCode=1;
}
if(process.argv[1] && fileURLToPath(import.meta.url)===process.argv[1]) await main();
