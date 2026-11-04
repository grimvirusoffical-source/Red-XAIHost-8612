import { lstat, readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const forbidden = /@runablehq|runable\.com|runablebadge|agentfeedback|runablemanagedauth|managedauthclient|collector\.onedollarstats\.com|r\.lilstts\.com|(?:^|[/'"\s])(?:analytics-consent|runable)\.js(?:[?'"\s]|$)/iu;
const textFile = /\.(?:js|mjs|cjs|css|html|json)$/iu;

export async function checkHostWebBuild(directory, mount = '/host') {
  if (mount !== '' && (!/^\/[a-z0-9][a-z0-9/_-]*$/iu.test(mount) || mount.endsWith('/'))) throw new Error('Invalid mounted path');
  const root = path.resolve(directory);
  if (!(await lstat(root)).isDirectory()) throw new Error('Build directory is required');
  const files = [];
  async function walk(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const full = path.join(current, entry.name);
      if (entry.isSymbolicLink()) throw new Error('Build must contain regular files, not links');
      if (entry.isDirectory()) await walk(full);
      else if (entry.isFile()) files.push(full);
      else throw new Error('Unexpected build entry');
    }
  }
  await walk(root);
  let scannedTextFiles = 0;
  for (const file of files) {
    if (/^(?:analytics-consent|runable)\.js$/iu.test(path.basename(file))) throw new Error('Legacy vendor asset remains');
    if (!textFile.test(file)) continue;
    scannedTextFiles++;
    if (forbidden.test(await readFile(file, 'utf8'))) throw new Error(`Vendor runtime remains in ${path.relative(root, file)}`);
  }
  const html = await readFile(path.join(root, 'index.html'), 'utf8');
  const scriptSources = [...html.matchAll(/<script\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>/giu)].map(match => match[1]);
  if (!scriptSources.some(source => source.startsWith(`${mount}/assets/`) && source.endsWith('.js'))) throw new Error('Mounted application script is missing');
  if (!scriptSources.includes('/redxai-connect/ux.js')) throw new Error('Existing first-party Quick Login script was lost');
  const references = [...scriptSources, ...[...html.matchAll(/<link\b[^>]*\bhref\s*=\s*["']([^"']+)["'][^>]*>/giu)].map(match => match[1]).filter(value => value.startsWith(`${mount}/assets/`))];
  for (const reference of references) {
    if (reference === '/redxai-connect/ux.js') continue;
    if (!reference.startsWith(`${mount}/assets/`) || /[?#\\]/u.test(reference)) throw new Error('Unexpected application asset reference');
    const file = path.resolve(root, reference.slice(mount.length + 1));
    if (!file.startsWith(root + path.sep) || !files.includes(file)) throw new Error('Application asset reference is not a local build file');
  }
  return { regularFiles: files.length, scannedTextFiles, mountedPath: mount || '/', scriptSources, vendorRuntimeFound: false, quickLoginScriptPreserved: true };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (!process.argv[2]) throw new Error('Usage: node scripts/check-host-web-vendor-surface.mjs <build-directory> [mount]');
    console.log(JSON.stringify(await checkHostWebBuild(process.argv[2], process.argv[3] ?? '/host')));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
