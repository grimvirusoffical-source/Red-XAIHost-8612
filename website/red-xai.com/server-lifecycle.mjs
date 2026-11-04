import http from 'node:http';
import { attachUpgrades } from './gateway-proxy.mjs';

/** Drain requests before releasing the single native CMS writer lock.
 * A forced process kill deliberately leaves its lock for operator review.
 */
export function createManagedSiteServer(site, { graceMs = 30000 } = {}) {
  if (!Number.isSafeInteger(graceMs) || graceMs < 1) throw new Error('Invalid shutdown grace period');
  const requests = new Set(), upgrades = new Set();
  let stopping = null;
  const server = http.createServer((req, res) => {
    const task = Promise.resolve().then(() => site.handler(req, res));
    requests.add(task);
    task.catch(() => res.destroy()).finally(() => requests.delete(task));
  });
  attachUpgrades(server, site.config);
  server.on('upgrade', (_req, socket) => {
    upgrades.add(socket);
    socket.once('close', () => upgrades.delete(socket));
  });
  let resolveClosed;
  const closed = new Promise(resolve => { resolveClosed = resolve; });
  server.once('close', resolveClosed);
  server.shutdown = () => {
    if (stopping) return stopping;
    stopping = (async () => {
      const deadline = setTimeout(() => {
        server.closeAllConnections();
        for (const socket of upgrades) socket.destroy();
      }, graceMs);
      deadline.unref();
      try {
        // Upgraded connections do not participate in http.Server.close().
        // Their upstream closes when the client socket closes.
        for (const socket of upgrades) socket.destroy();
        if (server.listening) server.close();
        else resolveClosed();
        await closed;
        // Response completion can precede asynchronous handler cleanup. Never
        // unlock while a handler can still enqueue a write or audit event.
        await Promise.allSettled([...requests]);
        await site.store?.queue;
        await site.store?.close?.();
      } finally { clearTimeout(deadline); }
    })();
    return stopping;
  };
  return server;
}

export function installShutdownSignals(server, signalTarget = process) {
  const stop = () => {
    server.shutdown().catch(() => {
      signalTarget.stderr?.write('Website gateway shutdown failed; retain the writer lock for review.\n');
      signalTarget.exitCode = 1;
    });
  };
  signalTarget.on('SIGTERM', stop);
  signalTarget.on('SIGINT', stop);
  const remove = () => {
    signalTarget.removeListener('SIGTERM', stop);
    signalTarget.removeListener('SIGINT', stop);
  };
  server.once('close', remove);
  return remove;
}
