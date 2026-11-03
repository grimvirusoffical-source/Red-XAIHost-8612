import test from 'node:test';
import assert from 'node:assert/strict';
import {FRESH_ISSUER,LEGACY_ISSUER,realmIdentity,legacyNativeIdentityAllowed,envelopeIdentity,realmCookies,FRESH_COOKIE} from './identity-realm.mjs';

test('same raw subject is disjoint across authorities; legacy ownership stays exact',()=>{
  const legacy=realmIdentity(LEGACY_ISSUER,'account-a'),fresh=realmIdentity(FRESH_ISSUER,'account-a');
  assert.equal(legacy.id,'account-a');assert.notEqual(fresh.id,legacy.id);
  assert.match(fresh.id,/^rxf1_[a-f0-9]{64}$/u);
  assert.equal(fresh.id,realmIdentity(FRESH_ISSUER,'account-a').id);
  assert.notEqual(fresh.id,realmIdentity(FRESH_ISSUER,'account-b').id);
  assert.throws(()=>realmIdentity(LEGACY_ISSUER,fresh.id));
  assert.equal(realmIdentity(FRESH_ISSUER,'same-subject').id,'rxf1_f7fb084c3229d8dcc7bcf980bbb75b59afa1ff9224066c7bf4786145ec99015f');
  assert.equal(realmIdentity(FRESH_ISSUER,'normal-ü-😀').id,'rxf1_76fe6aa0ebd69ef522f99f5aa1806aabc00119cbf04010504b641e2e9740d2d4');
});
test('unknown authorities and malformed subjects fail closed',()=>{
  for(const issuer of ['',null,'legacy','urn:attacker'])assert.throws(()=>realmIdentity(issuer,'account-a'));
  for(const subject of ['',null,'has space','control\u0000','line\u0085','x'.repeat(201),'ü'.repeat(101),'\ud800'])
    assert.throws(()=>realmIdentity(FRESH_ISSUER,subject));
});
test('fresh identities cannot enter unchanged native Host email attachment',()=>{
  assert.equal(legacyNativeIdentityAllowed({id:'account-a'}),true);
  assert.equal(legacyNativeIdentityAllowed({id:'account-a',issuer:LEGACY_ISSUER}),true);
  assert.equal(legacyNativeIdentityAllowed({id:'account-a',issuer:FRESH_ISSUER}),false);
  assert.equal(legacyNativeIdentityAllowed({id:realmIdentity(FRESH_ISSUER,'account-a').id,issuer:FRESH_ISSUER}),false);
});
test('realm envelope binds version, issuer, raw subject and canonical account ID',()=>{
  const p=realmIdentity(FRESH_ISSUER,'account-a');
  const j={identityRealm:{version:1,issuer:p.issuer,subject:p.subject,canonicalId:p.id},account:{id:p.id,roles:['user'],emailVerified:true,active:true},active:true,amr:['pwd']};
  assert.equal(envelopeIdentity(j).id,p.id);
  for(const change of [{identityRealm:{...j.identityRealm,canonicalId:'account-a'}},{account:{...j.account,id:'account-a'}},{account:{...j.account,roles:['owner']}},{account:{...j.account,roles:['user','admin']}},{account:{...j.account,emailVerified:false}}])assert.throws(()=>envelopeIdentity({...j,...change}));
  assert.equal(realmCookies(new Headers({cookie:`ignored=secret; redx_session=a; ${FRESH_COOKIE}=b`})) ,`redx_session=a; ${FRESH_COOKIE}=b`);
  assert.throws(()=>realmCookies(new Headers({cookie:`${FRESH_COOKIE}=a; ${FRESH_COOKIE}=b`})));
});
