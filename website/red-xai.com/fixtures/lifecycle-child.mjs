// Local disposable protocol fixture. It never contacts a native or account service.
import { promises as fs } from 'node:fs';
import path from 'node:path';
import { RedXCmsStore } from '../redx-cms-store.mjs';
import { createManagedSiteServer, installShutdownSignals } from '../server-lifecycle.mjs';

const directory = process.argv[2], keyFile = path.join(directory, 'fixture.key');
const scalarFile = path.join(directory, 'native-fixture-scalar');
const valuePath = 'site-cms.red-xai.db:SiteState[ID=1].Payload';
let release;
const held = new Promise(resolve => { release = resolve; });
process.on('message', message => {
  if (message === 'release') release();
  // Windows TerminateProcess does not deliver POSIX SIGTERM. The Linux CI
  // fixture uses the real OS signal; Windows tests the registered event path.
  if (message === 'shutdown-event') process.emit('SIGTERM');
});
const fetchImpl = async (url, init = {}) => {
  if (url === 'http://127.0.0.1:17790/v1/ui/whoami') return Response.json({
    scope: 'write', db: 'website_cms_fixture', prefix: 'site-cms.red-xai.db',
    host_admin: false, owner: false, role: 'leader',
  });
  if (url !== 'http://127.0.0.1:17790/v1/db/website_cms_fixture/path/' + encodeURIComponent(valuePath)) throw new Error('Unexpected fixture request');
  if (init.method === 'PUT') {
    process.send({ event: 'writing' });
    await held;
    await fs.writeFile(scalarFile, init.body, { mode: 0o600 });
    return Response.json({ ok: true, seq: 1 });
  }
  let value;
  try { value = await fs.readFile(scalarFile, 'utf8'); }
  catch (error) { if (error.code !== 'ENOENT') throw error; return Response.json({}, { status: 404 }); }
  return Response.json({ path: valuePath, value });
};
const store = await new RedXCmsStore({ directory, keyFile, database: 'website_cms_fixture',
  durability: 'sync', singleWriter: true, testMode: true, fetchImpl }).init();
const site = { store, config: { publicOrigin: 'http://127.0.0.1' }, handler: async (req, res) => {
  if (req.url === '/write') await store.mutate('fixture.update', { id: 'fixture' }, 'self', state => {
    state.preferences.fixture = { theme: 'dark' };
  });
  res.end(JSON.stringify(store.snapshot()));
} };
const server = createManagedSiteServer(site, { graceMs: 1000 });
installShutdownSignals(server);
// Wait for the writer lock to be released, not merely for the HTTP close event.
server.once('close', () => server.shutdown().then(() => {
  process.send({ event: 'stopped' }, () => process.disconnect());
}));
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
process.send({ event: 'ready', port: server.address().port, state: store.snapshot() });
