import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { checkHostWebBuild } from './check-host-web-vendor-surface.mjs';

async function fixture(t) {
  const directory = await mkdtemp(path.join(tmpdir(), 'redx-vendor-build-'));
  t.after(async () => {
    const resolved = await realpath(directory);
    assert.equal(path.dirname(resolved), await realpath(tmpdir()));
    assert.ok(path.basename(resolved).startsWith('redx-vendor-build-'));
    await rm(resolved, { recursive: true });
  });
  await mkdir(path.join(directory, 'assets'));
  await writeFile(path.join(directory, 'assets/app.js'), 'console.log("Red-XAI fixture");');
  await writeFile(path.join(directory, 'index.html'), '<link rel="stylesheet" href="/host/assets/style.css"><script defer src="/redxai-connect/ux.js"></script><script type="module" src="/host/assets/app.js"></script>');
  await writeFile(path.join(directory, 'assets/style.css'), 'body{color:white}');
  return directory;
}

test('accepts a mounted build with its first-party Quick Login reference', async t => {
  const result = await checkHostWebBuild(await fixture(t));
  assert.equal(result.regularFiles, 3);
  assert.equal(result.vendorRuntimeFound, false);
  assert.equal(result.quickLoginScriptPreserved, true);
});

test('rejects a collector hidden in a compiled chunk', async t => {
  const directory = await fixture(t);
  await writeFile(path.join(directory, 'assets/app.js'), 'fetch("https://collector.onedollarstats.com/events");');
  await assert.rejects(checkHostWebBuild(directory), /Vendor runtime remains/u);
});

test('rejects copied legacy assets even if HTML no longer references them', async t => {
  const directory = await fixture(t);
  await writeFile(path.join(directory, 'analytics-consent.js'), '/* obsolete */');
  await assert.rejects(checkHostWebBuild(directory), /Legacy vendor asset remains/u);
});

test('rejects lost Quick Login integration or an incorrect mounted bundle path', async t => {
  const directory = await fixture(t);
  await writeFile(path.join(directory, 'index.html'), '<script type="module" src="/host/assets/app.js"></script>');
  await assert.rejects(checkHostWebBuild(directory), /Quick Login script was lost/u);
  await writeFile(path.join(directory, 'index.html'), '<script defer src="/redxai-connect/ux.js"></script><script type="module" src="/assets/app.js"></script>');
  await assert.rejects(checkHostWebBuild(directory), /Mounted application script is missing/u);
});
