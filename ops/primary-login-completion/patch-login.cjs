'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const EXPECTED_INPUT_SHA256 = 'aeeeebb35bec7118573bc13c12c2da31cbb2bfab24a00e854923e599b405e4a2';
const ORIGINAL_HEADER = 'function pN({setView:n}){const[i,o]=C.useState(""),[c,d]=C.useState(""),[f,p]=C.useState(!1),[y,m]=C.useState("");async function g(j){';
const PATCHED_HEADER = 'function pN({setView:n}){const[i,o]=C.useState(""),[c,d]=C.useState(""),[f,p]=C.useState(!1),[y,m]=C.useState(""),S=tg();async function g(j){';
const ORIGINAL_RESULT = 'k.json.next==="2fa"?n({k:"challenge",token:k.json.token,methods:k.json.methods}):k.json.next==="2fa_setup"&&n({k:"setup",token:k.json.token,methods:k.json.methods,username:k.json.username})';
const PATCHED_RESULT = 'if(k.json.next==="2fa")return n({k:"challenge",token:k.json.token,methods:k.json.methods});if(k.json.next==="2fa_setup")return n({k:"setup",token:k.json.token,methods:k.json.methods,username:k.json.username});if((k.json.next===void 0||k.json.next==="")&&k.json.token===void 0&&k.json.challenge===void 0&&k.json.error===void 0&&(k.json.pending===void 0||Array.isArray(k.json.pending)&&k.json.pending.length===0))return S();m("Sign-in did not complete. Please try again.")';

function sha256(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function count(text, needle) {
  return text.split(needle).length - 1;
}

function extractFunction(source, name, nextName) {
  const startMarker = `function ${name}(`;
  const endMarker = `function ${nextName}(`;
  if (count(source, startMarker) !== 1 || count(source, endMarker) !== 1) {
    throw new Error(`Expected exactly one ${name} and ${nextName} function marker`);
  }
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start + startMarker.length);
  if (end <= start) throw new Error(`Could not isolate ${name}`);
  return { start, end, source: source.slice(start, end) };
}

function patchBundle(input) {
  if (!Buffer.isBuffer(input)) throw new TypeError('Input must be a Buffer');
  const inputSha256 = sha256(input);
  if (inputSha256 !== EXPECTED_INPUT_SHA256) {
    throw new Error(`Input SHA-256 mismatch: expected ${EXPECTED_INPUT_SHA256}, received ${inputSha256}`);
  }
  const source = input.toString('utf8');
  if (!Buffer.from(source, 'utf8').equals(input)) throw new Error('Input must be valid UTF-8');
  const login = extractFunction(source, 'pN', 'Up');
  for (const needle of [ORIGINAL_HEADER, ORIGINAL_RESULT]) {
    if (count(source, needle) !== 1 || count(login.source, needle) !== 1) {
      throw new Error('Reviewed login patch location did not match exactly once');
    }
  }
  const patchedLogin = login.source.replace(ORIGINAL_HEADER, PATCHED_HEADER).replace(ORIGINAL_RESULT, PATCHED_RESULT);
  const patchedSource = source.slice(0, login.start) + patchedLogin + source.slice(login.end);
  const output = Buffer.from(patchedSource, 'utf8');
  if (source.slice(0, login.start) !== patchedSource.slice(0, login.start) || !patchedSource.endsWith(source.slice(login.end))) {
    throw new Error('Patch affected bytes outside the sign-in component');
  }
  return {
    output,
    report: {
      inputSha256,
      outputSha256: sha256(output),
      inputBytes: input.length,
      outputBytes: output.length,
      byteDelta: output.length - input.length,
      replacements: 2,
      changedFunctions: ['pN'],
      originalComponentBytes: Buffer.byteLength(login.source),
      patchedComponentBytes: Buffer.byteLength(patchedLogin),
      unchangedPrefixBytes: Buffer.byteLength(source.slice(0, login.start)),
      unchangedSuffixBytes: Buffer.byteLength(source.slice(login.end)),
    },
  };
}

function main(args) {
  if (args.length !== 2) throw new Error('Usage: node patch-login.cjs <reviewed-original-bundle.js> <new-output.js>');
  const [inputPath, outputPath] = args.map(p => path.resolve(p));
  if (inputPath === outputPath) throw new Error('Input and output must differ');
  const { output, report } = patchBundle(fs.readFileSync(inputPath));
  // Exclusive create: never overwrite an original, prior result or deployment asset.
  fs.writeFileSync(outputPath, output, { flag: 'wx' });
  process.stdout.write(JSON.stringify(report, null, 2) + '\n');
}

if (require.main === module) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    process.stderr.write(error.message + '\n');
    process.exitCode = 1;
  }
}

module.exports = { EXPECTED_INPUT_SHA256, ORIGINAL_HEADER, ORIGINAL_RESULT, PATCHED_HEADER, PATCHED_RESULT, extractFunction, patchBundle, sha256 };
