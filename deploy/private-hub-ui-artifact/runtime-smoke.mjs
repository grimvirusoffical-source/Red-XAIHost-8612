import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {promises as fs} from 'node:fs';
import {createServer} from 'node:http';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

const [directory, provenancePath] = process.argv.slice(2);
assert.ok(directory && provenancePath, 'Explicit isolated fixture paths required');
const provenance = JSON.parse(await fs.readFile(provenancePath, 'utf8'));
assert.equal(provenance.kind, 'red-xai-private-hub-ui-source-candidate');
assert.equal(provenance.runtimeArchive.sha256, '56496195081d28be14b9be6d2fd79f77272de8280fcf67347f155544b94056c3');
assert.equal(provenance.runtimeFileCount, 22);
const root = await fs.realpath(directory);
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
for (const [name, metadata] of Object.entries(provenance.runtimeFiles)) {
  assert.ok(!name.includes('\\') && !name.startsWith('/') && !name.split('/').includes('..'));
  const file = path.join(root, name), info = await fs.lstat(file);
  assert.ok(info.isFile() && !info.isSymbolicLink());
  const bytes = await fs.readFile(file);
  assert.equal(bytes.length, metadata.size);
  assert.equal(digest(bytes), metadata.sha256);
}

// Python verifies the complete archive against pinned committed blobs before
// materializing this fixture. This import cannot initialize a CMS or account
// authority: the production startServer entry point is never called.
const {createSiteHandler, SITE_FILES} = await import(pathToFileURL(path.join(root, 'server.mjs')).href);
let externalCalls = 0;
const site = await createSiteHandler({
  staticDirectory: root,
  testMode: true,
  development: true,
  publicOrigin: 'http://127.0.0.1',
  fetchImpl: async () => { externalCalls++; throw Error('External fixture requests are forbidden'); },
});
assert.equal(site.store, null);
const server = createServer(site.handler);
await new Promise((resolve, reject) => {
  server.once('error', reject);
  server.listen(0, '127.0.0.1', resolve);
});
let assetRequests = 0, hiddenRequests = 0;
try {
  const origin = 'http://127.0.0.1:' + server.address().port;
  const get = route => fetch(origin + route, {redirect:'error', signal:AbortSignal.timeout(5000)});
  const revision = provenance.browserAssets.candidateRevision;
  let rootHtml;
  for (const route of ['/', '/products/cli/redxai-cli', '/db', '/host']) {
    const response = await get(route);
    assert.equal(response.status, 200);
    const html = await response.text();
    if (route === '/') rootHtml = html;
    assert.ok(html.includes('/site-assets/' + revision + '/app.js'));
    assert.ok(html.includes('/site-assets/' + revision + '/styles.css'));
  }
  for (const name of SITE_FILES) {
    for (const route of ['/' + name, '/site-assets/' + revision + '/' + name]) {
      const response = await get(route);
      assert.equal(response.status, 200, route);
      const bytes = Buffer.from(await response.arrayBuffer());
      assert.equal(digest(bytes), provenance.runtimeFiles[name].sha256, route);
      assetRequests++;
    }
  }
  for (const route of ['/server.mjs', '/package.json', '/package-lock.json',
    '/redx-cms-store.mjs', '/environment.txt', '/site-assets/' + revision + '/server.mjs',
    '/site-assets/' + '0'.repeat(64) + '/app.js']) {
    const response = await get(route);
    assert.equal(response.status, 404, route);
    await response.arrayBuffer();
    hiddenRequests++;
  }
  // The preserved c443 SPA treats a leading-dot basename as a page fallback.
  // Verify that this returns only the exact Hub document (or a future404),
  // rather than assuming a status code proves that file contents are hidden.
  const dotfileResponse = await get('/.env');
  assert.ok([200, 404].includes(dotfileResponse.status));
  if (dotfileResponse.status === 200) {
    assert.ok(dotfileResponse.headers.get('content-type').startsWith('text/html'));
    assert.equal(await dotfileResponse.text(), rootHtml);
  } else await dotfileResponse.arrayBuffer();
  const healthResponse = await get('/api/site/health');
  assert.equal(healthResponse.status, 200);
  const health = await healthResponse.json();
  assert.equal(health.ok, true);
  assert.equal(health.cmsConfigured, false);
  assert.equal(externalCalls, 0);
  console.log(JSON.stringify({ok:true, fixtureOnly:true, browserRevision:revision,
    renderedPages:4, assetRequests, hiddenRequests, dotfileSourceExposed:false,
    externalRequests:externalCalls,
    cmsStarted:false, accountAuthorityStarted:false, productionAccess:false}));
} finally {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}
