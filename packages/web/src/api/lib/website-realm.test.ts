import {expect,test} from 'bun:test';
import {websiteIdentity} from './website-session';
import {accountIdentity} from './accounts-client';
import {FRESH_COOKIE,FRESH_ISSUER,LEGACY_ISSUER,REALM_AUTHORITY,realmIdentity} from '../../../../../website/red-xai.com/identity-realm.mjs';
const now=1900000000000;
const env={REDX_WEBSITE_SESSION:'true',REDX_WEBSITE_SESSION_REALMS:'prototype-v1',REDX_WEBSITE_OWNER_ACCOUNT_IDS:'shared'};
function profile(issuer:string,subject='shared') {
  const p=realmIdentity(issuer,subject);
  return {identityRealm:{version:1,issuer,subject,canonicalId:p.id},account:{id:p.id,email:'same@example.invalid',username:'same',roles:['user'],emailVerified:true,active:true},active:true,expiresMs:now+60000,amr:['pwd']};
}
test('unchanged native account attachment adapter remains legacy-only',()=>{
  expect(accountIdentity(profile(FRESH_ISSUER))).toBeNull();
  expect(accountIdentity({account:{id:'shared',email:'same@example.invalid'}})?.id).toBe('shared');
});
test('Host realm adapter accepts only fixed resolver output and forwards no identity headers',async()=>{
  const h=new Headers({cookie:`ignored=private; ${FRESH_COOKIE}=a`,authorization:'Bearer forged','x-redx-realm':LEGACY_ISSUER,'x-redx-account-id':'shared'});
  const result=await websiteIdentity(h,{env,now,fetch:async(url,init)=>{
    expect(url).toBe(REALM_AUTHORITY);expect([...new Headers(init.headers).keys()].sort()).toEqual(['accept','cookie']);
    expect(new Headers(init.headers).get('cookie')).toBe(`${FRESH_COOKIE}=a`);
    return Response.json(profile(FRESH_ISSUER));
  }});
  expect(result?.id).toBe(realmIdentity(FRESH_ISSUER,'shared').id);expect(result?.owner).toBe(false);
  expect(result?.issuer).toBe(FRESH_ISSUER);expect(result?.subject).toBe('shared');
});
test('same email/raw subject and forged owner proof cannot inherit legacy Host tenant',async()=>{
  const legacy=profile(LEGACY_ISSUER);legacy.account.roles=['owner'];legacy.amr=['pwd','call'];
  const fresh=profile(FRESH_ISSUER);const freshId=fresh.account.id;
  const options={...env,REDX_WEBSITE_OWNER_ACCOUNT_IDS:`shared,${freshId}`};
  const get=(p:unknown)=>websiteIdentity(new Headers({cookie:`${FRESH_COOKIE}=a`}),{env:options,now,fetch:async()=>Response.json(p)});
  expect((await get(legacy))?.id).toBe('shared');expect((await get(legacy))?.owner).toBe(true);
  expect((await get(fresh))?.owner).toBe(false);
  expect((await get(fresh))?.id).not.toBe('shared');
  for(const p of [{...fresh,account:{...fresh.account,id:'shared'}},{...fresh,account:{...fresh.account,roles:['owner']}},{...fresh,account:{...fresh.account,roles:['user','admin']}},{...fresh,identityRealm:{...fresh.identityRealm,issuer:'urn:attacker'}},{...fresh,expiresMs:now}])expect(await get(p)).toBeNull();
});
test('fresh A/B differ; revocation, ambiguity, outage and configuration errors fail closed without legacy fallback',async()=>{
  let revoked=false,calls=0;
  const request=async(_url:string,init:RequestInit)=>{calls++;const c=new Headers(init.headers).get('cookie')!;if(c.includes(';'))return new Response(null,{status:409});if(revoked)return new Response(null,{status:401});return Response.json(profile(FRESH_ISSUER,c.endsWith('=a')?'shared':'other'));};
  const get=(value:string)=>websiteIdentity(new Headers({cookie:value}),{env,now,fetch:request});
  const [a,b]=await Promise.all(['a','b'].map(v=>get(`${FRESH_COOKIE}=${v}`)));expect(a?.id).not.toBe(b?.id);
  expect(await get(`redx_session=old; ${FRESH_COOKIE}=a`)).toBeNull();
  revoked=true;expect(await get(`${FRESH_COOKIE}=a`)).toBeNull();expect(calls).toBe(4);
  expect(await websiteIdentity(new Headers({cookie:`${FRESH_COOKIE}=a`}),{env:{...env,REDX_WEBSITE_SESSION_REALMS:'typo'},now,fetch:request})).toBeNull();expect(calls).toBe(4);
});
