import { createHash } from 'node:crypto';

// Source-only prototype contract. This module authenticates no request. Its
// arguments must come only from a freshly revalidated trusted authority.
export const LEGACY_ISSUER = 'urn:redxai:legacy-site';
export const FRESH_ISSUER = 'urn:redxai:fresh-normal-prototype:v1';
export const FRESH_ID_PREFIX = 'rxf1_';
export const REALM_AUTHORITY = 'http://127.0.0.1:14330/__prototype/identity';
export const FRESH_COOKIE = 'redx_fresh_normal_prototype_session';

export function realmIdentity(issuer, subject) {
  if (![LEGACY_ISSUER,FRESH_ISSUER].includes(issuer)
      || typeof subject!=='string' || !subject || Buffer.byteLength(subject,'utf8')>200
      || /[\p{White_Space}\p{Cc}\p{Surrogate}]/u.test(subject)) throw Error('Invalid authority principal');
  if (issuer===LEGACY_ISSUER) {
    // Source prototype only: activation remains blocked until persistent
    // namespace enforcement or issuer-aware storage can prove disjointness.
    if (subject.startsWith(FRESH_ID_PREFIX)) throw Error('Reserved authority namespace');
    return Object.freeze({issuer,subject,id:subject,legacy:true});
  }
  const id=FRESH_ID_PREFIX+createHash('sha256').update(issuer+'\0'+subject,'utf8').digest('hex');
  return Object.freeze({issuer,subject,id,legacy:false});
}

export function envelopeIdentity(json) {
  const r=json?.identityRealm,a=json?.account;
  if (!r || r.version!==1 || typeof r.canonicalId!=='string') throw Error('Realm envelope required');
  const identity=realmIdentity(r.issuer,r.subject);
  if (r.canonicalId!==identity.id || a?.id!==identity.id) throw Error('Authority identity mismatch');
  if (!Array.isArray(json.amr)||!json.amr.length||json.amr.some(v=>typeof v!=='string'||!v||v.length>40)) throw Error('Authentication methods required');
  if (!identity.legacy && (json.active!==true || a.active!==true || a.emailVerified!==true
      || !Array.isArray(a.roles) || a.roles.length!==1 || a.roles[0]!=='user')) throw Error('Fresh normal proof required');
  return identity;
}

export function realmCookies(headers) {
  const value=headers.get?headers.get('cookie'):headers.cookie;
  if (value && (typeof value!=='string'||value.length>16384)) throw Error('Invalid cookies');
  const selected=[];
  for (const name of ['redx_session',FRESH_COOKIE]) {
    const cookies=(value||'').split(';').map(v=>v.trim()).filter(v=>v.startsWith(name+'='));
    if (cookies.length>1 || cookies.some(v=>v.length<=name.length+1||v.length>8192||/[\u0000-\u0020\u007f]/u.test(v))) throw Error('Ambiguous cookies');
    selected.push(...cookies);
  }
  // Both are sent to the fixed resolver, which must return 409. No priority,
  // user-supplied realm header, cached identity or legacy fallback is allowed.
  return selected.join('; ');
}

export function legacyNativeIdentityAllowed(identity) {
  // Unchanged native clients remain legacy-only. No fresh identity can enter
  // Host's historical verified-email attachment/session conversion path.
  return identity && (identity.issuer===undefined || identity.issuer===LEGACY_ISSUER)
    && typeof identity.id==='string';
}
