'use strict';
// rsx-sdk entry point.
//   require('rsx-sdk')           native API (synchronous) when the addon and librsx load
//   await require('rsx-sdk').open()        best available backend: native, else WebAssembly
//   await require('rsx-sdk').wasm(opts)    WebAssembly backend ({ threads: 'auto' | true | false })
// RSX_BACKEND=wasm makes open() skip the native backend.
const fs = require('fs');
const { loadNative } = require('./native.js');
const { createWasm } = require('./wasm.js');

let native = null, nativeError = null;
try { native = loadNative(); } catch (e) { nativeError = e; }

/**
 * GetMap setup hook (docs/spec/).
 * @param {string} keymapPath
 * @param {Buffer} passwordBuf
 * @param {() => boolean} [onSetup]  true=accept, false=decline. Decline creates nothing.
 * @returns {object|null} Session, or null if declined
 */
function getMapSession(keymapPath, passwordBuf, onSetup) {
  if (!native) throw nativeError;
  if (!fs.existsSync(keymapPath)) {
    if (!onSetup || !onSetup()) return null;
  }
  return new native.Session(passwordBuf, keymapPath);
}

async function open(opts = {}) {
  if (native && process.env.RSX_BACKEND !== 'wasm' && opts.backend !== 'wasm') return Object.assign({ backend: 'native' }, native);
  return createWasm(opts);
}

module.exports = Object.assign({}, native || {}, {
  backend: native ? 'native' : null, nativeError, getMapSession, open, wasm: createWasm,
});
