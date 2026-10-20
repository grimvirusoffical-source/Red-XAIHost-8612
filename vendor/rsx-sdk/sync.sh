#!/usr/bin/env bash
# Re-vendor the Red-SecretsXAI JS SDK: sync.sh <red-secretsxai checkout> <commit>
# The checkout must have the single-threaded WebAssembly build (sdk/js/wasm/build.sh) done.
set -euo pipefail
src=$1; rev=$2; here=$(cd "$(dirname "$0")" && pwd)
test "$(git -C "$src" rev-parse HEAD)" = "$(git -C "$src" rev-parse "$rev")" || { echo "checkout is not at $rev" >&2; exit 1; }
js=$src/sdk/js
mkdir -p "$here/wasm/dist"
cp "$js/index.js" "$js/native.js" "$js/wasm.js" "$here/"
cp "$js/wasm/dist/rsx-st.js" "$js/wasm/dist/rsx-st.wasm" "$here/wasm/dist/"
{ echo "red-secretsxai $(git -C "$src" rev-parse "$rev")"; echo "files: index.js native.js wasm.js wasm/dist/rsx-st.js wasm/dist/rsx-st.wasm";
  (cd "$here" && sha256sum index.js native.js wasm.js wasm/dist/rsx-st.js wasm/dist/rsx-st.wasm); } > "$here/VENDOR.txt"
