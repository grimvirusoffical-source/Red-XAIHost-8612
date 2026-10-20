'use strict';
// Native backend: the N-API addon plus a librsx found at run time.
// librsx search order: RSX_LIB, then prebuilds/<platform>-<arch>/ next to this package.
const path = require('path');
const fs = require('fs');

const LIBNAME = process.platform === 'win32' ? 'librsx.dll' : process.platform === 'darwin' ? 'librsx.dylib' : 'librsx.so';

function libCandidates() {
  const out = [];
  if (process.env.RSX_LIB) out.push(process.env.RSX_LIB);
  out.push(path.join(__dirname, 'prebuilds', `${process.platform}-${process.arch}`, LIBNAME));
  return out;
}

function findLib() {
  for (const p of libCandidates()) if (fs.existsSync(p)) return p;
  return null;
}

function loadNative() {
  const addon = path.join(__dirname, 'build', 'Release', 'rsx.node');
  if (!fs.existsSync(addon)) throw new Error('native addon not built (' + addon + ')');
  const lib = findLib();
  if (!lib) throw new Error('librsx not found; tried: ' + libCandidates().join(', '));
  const binding = require(addon);
  if (binding.loadedPath() === null) binding.load(lib);
  return binding;
}

module.exports = { loadNative, findLib, libCandidates, LIBNAME };
