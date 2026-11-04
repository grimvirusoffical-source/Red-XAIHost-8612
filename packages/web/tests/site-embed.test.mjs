import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHostEmbedMode, HOST_WORKSPACE_ROUTES, siteEmbedRequested } from '../src/web/lib/site-embed.ts';
import { hostSidebarLink } from '../src/web/lib/site-navigation.ts';

const web=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const read=name=>fs.readFileSync(path.join(web,name),'utf8');

test('website mode contains every Host workspace tool and no second global navigation',()=>{
  assert.deepEqual(HOST_WORKSPACE_ROUTES.map(route=>route.key),[
    'overview','nodes','projects','domains','usage','builds','activity','settings',
  ]);
  assert.ok(HOST_WORKSPACE_ROUTES.every(route=>!['Hub','DB','Vortex','Cloud PC'].includes(route.label)));
});

test('embedded navigation persists the flag across project links, queries and fragments',()=>{
  const mode=createHostEmbedMode('?embed=site');
  assert.equal(mode.site,true);
  assert.equal(mode.href('/projects/project-id?view=logs#latest'),'/projects/project-id?view=logs&embed=site#latest');
  assert.equal(mode.href('/nodes'),'/nodes?embed=site');
  assert.equal(mode.href('/nodes?embed=1'),'/nodes?embed=site');
  assert.equal(siteEmbedRequested(new URL(mode.href('/projects'), 'https://example.invalid').search),true);
  assert.throws(()=>mode.href('//untrusted.invalid/path'),/local path/);
});

test('standalone and existing legacy embed URLs keep their existing navigation contract',()=>{
  for(const search of ['', '?embed=1', '?embed=other']) {
    const mode=createHostEmbedMode(search);
    assert.equal(mode.site,false);
    assert.equal(mode.href('/projects?view=logs#latest'),'/projects?view=logs#latest');
  }
});

test('mounted Host leaves its router for Hub and DB without prefixing their workspace paths',()=>{
  for(const href of ['/','/db'])assert.deepEqual(hostSidebarLink(href,'/host'),{href,browserNavigation:true});
  assert.deepEqual(hostSidebarLink('/host','/host'),{href:'/',browserNavigation:false});
  for(const href of ['/nodes','/projects','/projects/owned-id?view=logs#latest','/domains','/usage','/builds','/activity','/settings'])assert.deepEqual(hostSidebarLink(href,'/host'),{href,browserNavigation:false});
});

test('mounted Host opens existing Vortex and Cloud PC services without forwarding session parameters',()=>{
  for(const [path,href]of [['/vortex','https://45.77.105.187/vortex/'],['/cloud-pc','https://45.77.105.187/vortex/cloudpc/']]){
    const destination=hostSidebarLink(path,'/host');
    assert.deepEqual(destination,{href,browserNavigation:true,target:'_blank',rel:'noopener noreferrer'});
    assert.equal(new URL(destination.href).search,'');assert.equal(new URL(destination.href).hash,'');
  }
});

test('unmounted local Host keeps all existing local navigation contracts',()=>{
  for(const href of ['/','/host','/db','/vortex','/nodes','/projects','/domains','/usage','/builds','/activity','/settings'])assert.deepEqual(hostSidebarLink(href,''),{href,browserNavigation:false});
});

test('deployed web runtime no longer depends on the removed platform broker or tracking injection',()=>{
  const pkg=JSON.parse(read('package.json'));
  assert.ok(Object.keys(pkg.dependencies).every(name=>!name.startsWith('@runablehq/')));
  assert.doesNotMatch(read('src/web/app.tsx'),/@runablehq|AgentFeedback|RunableBadge/);
  assert.doesNotMatch(read('src/api/auth.ts'),/@runablehq|managedEnabled|runableManagedAuth/);
  assert.doesNotMatch(read('src/web/lib/auth.ts'),/@runablehq|managedAuth|MANAGED_AUTH_ENABLED/);
  assert.doesNotMatch(read('src/web/lib/api.ts'),/managedAuth|Bearer/);
  assert.match(read('src/web/lib/api.ts'),/credentials: "include"/);
  assert.match(read('src/api/auth.ts'),/betterAuth\(/);
  assert.match(read('src/api/middleware/auth.ts'),/hostWebsiteIdentity\(headers\)/);
  assert.match(read('src/api/lib/host-website-identity.ts'),/websiteIdentity\(headers\)/);
  for(const name of ['public/runable.js','public/analytics-consent.js','src/web/types/__analytics.d.ts','vite/__plugins/runable-analytics-plugin.ts','website.config.json']) {
    assert.equal(fs.existsSync(path.join(web,name)),false);
  }
});
