import test from 'node:test';
import assert from 'node:assert/strict';
import { fork } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createManagedSiteServer, installShutdownSignals } from './server-lifecycle.mjs';

const HERE = path.dirname(fileURLToPath(import.meta.url));
function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
async function child(t, directory) {
  const process = fork(path.join(HERE, 'fixtures', 'lifecycle-child.mjs'), [directory], { stdio: ['ignore', 'pipe', 'pipe', 'ipc'] });
  let diagnostics = '';
  process.stderr.on('data', chunk => { diagnostics += chunk; });
  const messages = [], waiters = [];
  process.on('message', message => {
    const index = waiters.findIndex(v => v.event === message.event);
    if (index >= 0) waiters.splice(index, 1)[0].resolve(message); else messages.push(message);
  });
  const stopped = new Promise((resolve, reject) => {
    process.once('error', reject);
    process.once('exit', (code, signal) => code === 0 ? resolve() : reject(new Error(`Fixture failed (${code}/${signal}): ${diagnostics}`)));
  });
  stopped.catch(() => {});
  t.after(async () => {
    if (process.exitCode === null && process.signalCode === null) process.kill();
    await stopped.catch(() => {});
  });
  const message = event => {
    const index = messages.findIndex(v => v.event === event);
    if (index >= 0) return Promise.resolve(messages.splice(index, 1)[0]);
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error(`Missing ${event}: ${diagnostics}`)), 10000);
      waiters.push({ event, resolve: value => { clearTimeout(timeout); resolve(value); } });
      stopped.catch(error => { clearTimeout(timeout); reject(error); });
    });
  };
  return { process, message, stopped };
}

test('shutdown waits for asynchronous request cleanup before closing CMS; calls are idempotent', async t => {
  const handlerEntered = deferred(), cleanup = deferred(), closed = deferred();
  const store = { queue: Promise.resolve(), close: () => { closed.resolve(); } };
  const server = createManagedSiteServer({ store, config: { publicOrigin: 'http://127.0.0.1' }, handler: async (_req, res) => {
    res.end('response complete'); handlerEntered.resolve(); await cleanup.promise;
  } }, { graceMs: 1000 });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.shutdown());
  const response = await fetch('http://127.0.0.1:' + server.address().port);
  assert.equal(await response.text(), 'response complete'); await handlerEntered.promise;
  const stop = server.shutdown(); assert.equal(server.shutdown(), stop);
  let lockReleased = false; closed.promise.then(() => { lockReleased = true; });
  await new Promise(resolve => setImmediate(resolve)); assert.equal(lockReleased, false);
  cleanup.resolve(); await stop; assert.equal(lockReleased, true);
});

test('real process restart preserves a queued native CMS mutation and releases its writer lock', { timeout: 30000 }, async t => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'redx-site-lifecycle-'));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  await fs.writeFile(path.join(directory, 'fixture.key'), 'rx_live_' + 'a'.repeat(43), { mode: 0o600 });
  const first = await child(t, directory), ready = await first.message('ready');
  const write = fetch('http://127.0.0.1:' + ready.port + '/write');
  await first.message('writing');
  if (process.platform === 'win32') first.process.send('shutdown-event'); else first.process.kill('SIGTERM');
  // The API write is held by the fixture; shutdown must retain the lock.
  await new Promise(resolve => setTimeout(resolve, 100));
  assert.equal((await fs.lstat(path.join(directory, 'writer.lock'))).isFile(), true);
  first.process.send('release');
  const written = await (await write).json(); assert.equal(written.preferences.fixture.theme, 'dark');
  await first.message('stopped'); await first.stopped;
  await assert.rejects(fs.lstat(path.join(directory, 'writer.lock')), { code: 'ENOENT' });
  const second = await child(t, directory), restarted = await second.message('ready');
  assert.equal(restarted.state.preferences.fixture.theme, 'dark');
  if (process.platform === 'win32') second.process.send('shutdown-event'); else second.process.kill('SIGTERM');
  await second.message('stopped'); await second.stopped;
  await assert.rejects(fs.lstat(path.join(directory, 'writer.lock')), { code: 'ENOENT' });
});

test('signal hooks are removed after HTTP shutdown and do not convert a failure to success', async () => {
  const target = new EventEmitter(); target.exitCode = 2;
  const server = createManagedSiteServer({ handler: (_req, res) => res.end(), config: {}, store: null });
  installShutdownSignals(server, target);
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  target.emit('SIGTERM'); await server.shutdown();
  assert.equal(target.listenerCount('SIGTERM'), 0); assert.equal(target.listenerCount('SIGINT'), 0);
  assert.equal(target.exitCode, 2);
});
