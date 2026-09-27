import test from 'node:test';
import assert from 'node:assert/strict';
import {generateKeyPairSync,verify} from 'node:crypto';
import {parsePrivateKey,makeJWT,makeClient,audit} from './apple_metadata.mjs';
const {privateKey,publicKey}=generateKeyPairSync('ec',{namedCurve:'prime256v1'});
const pem=privateKey.export({format:'pem',type:'pkcs8'});
const credential={key:privateKey,keyID:'SYNTHETIC',individual:false};
for (const [name,value] of Object.entries({pem,escaped:pem.replaceAll('\n','\\n'),json:JSON.stringify({private_key:pem}),jsonString:JSON.stringify(pem),base64:Buffer.from(pem).toString('base64'),der:privateKey.export({format:'der',type:'pkcs8'}).toString('base64')})) {
  test(`parses ${name}`,()=>assert.ok(parsePrivateKey(value)));
}
test('rejects filename, empty, huge or public data',()=>{
  for(const value of ['file.p8','','x'.repeat(65537),publicKey.export({format:'pem',type:'spki'})]) assert.equal(parsePrivateKey(value),null);
});
test('rejects wrong curve',()=>assert.equal(parsePrivateKey(generateKeyPairSync('ec',{namedCurve:'secp384r1'}).privateKey.export({format:'pem',type:'pkcs8'})),null));
test('team token is signed, short-lived and read-scoped',()=>{
  const jwt=makeJWT(privateKey,'SYNTHETIC',false,'/v1/apps');
  const [head,body,sig]=jwt.split('.');const p=JSON.parse(Buffer.from(body,'base64url'));
  assert.ok(p.iss);assert.equal(p.sub,undefined);assert.deepEqual(p.scope,['GET /v1/apps']);assert.ok(p.exp-p.iat<=120);
  assert.equal(Buffer.from(sig,'base64url').length,64);
  assert.ok(verify('sha256',Buffer.from(`${head}.${body}`),{key:publicKey,dsaEncoding:'ieee-p1363'},Buffer.from(sig,'base64url')));
});
test('individual token uses sub user and no issuer',()=>{
  const jwt=makeJWT(privateKey,'SYNTHETIC',true,'/v1/apps');const p=JSON.parse(Buffer.from(jwt.split('.')[1],'base64url'));
  assert.equal(p.sub,'user');assert.equal(p.iss,undefined);
});
test('client is constrained to Apple and disables redirects',async()=>{
  const request=makeClient(credential,async(url,options)=>{
    assert.equal(url.origin,'https://api.appstoreconnect.apple.com');assert.equal(options.redirect,'error');
    assert.ok(options.headers.Authorization.startsWith('Bearer '));
    return {ok:true,status:200,json:async()=>({data:[]})};
  });
  assert.equal((await request('/v1/apps')).status,200);
  await assert.rejects(()=>request('https://example.org/'));
  await assert.rejects(()=>request('/v1/../apps'));
  await assert.rejects(()=>request('/v1/apps',{},'DELETE'));
});
test('invalid credentials do not make API calls or leak',async()=>{
  const report=await audit({RX_ASC_PRIVATE:'file.p8'},()=>{throw new Error('Must not call');});
  assert.ok(report.errors.includes('NO_VALID_P256_PRIVATE_KEY_IN_CONFIGURED_SECRETS'));
  assert.ok(!JSON.stringify(report).includes('file.p8'));
});
test('authentication failure reports safe status without sensitive response detail',async()=>{
  const report=await audit({RX_ASC_PRIVATE:pem},async()=>({ok:false,status:401,json:async()=>({errors:[{code:'NOT_AUTHORIZED',detail:pem}]})}));
  assert.equal(report.authentication.length,4);
  assert.ok(!JSON.stringify(report).includes('BEGIN PRIVATE'));
  assert.ok(report.errors.includes('APPLE_AUTHENTICATION_NOT_ESTABLISHED'));
});
test('does not retry rate limits with other identities',async()=>{
  let calls=0;await audit({RX_ASC_PRIVATE:pem},async()=>{calls++;return {ok:false,status:429,json:async()=>({errors:[]})};});assert.equal(calls,1);
});
test('scopes app discovery and reports missing expected apps',async()=>{
  const report=await audit({RX_ASC_PRIVATE:pem},async(url)=>{
    assert.equal(url.searchParams.get('filter[bundleId]'),'com.redxai.database,com.redxai.host');
    return {ok:true,status:200,json:async()=>({data:[]})};
  });
  assert.deepEqual(report.apps.map(a=>a.found),[false,false]);
});
