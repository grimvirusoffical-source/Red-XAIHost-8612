#!/usr/bin/env bash
# Cross-compile redxd + redx for every supported platform (static, CGO off).
# usage: scripts/build.sh [--tags "redx_stub"] [--only linux/amd64,...] [--version X] [--no-archive]
# output: dist/redx-<version>-<os>-<arch>/{redxd,redx}[.exe] + dist/*.tar.gz|zip + dist/SHA256SUMS
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)}"
TAGS="${TAGS:-}"
ONLY=""
ARCHIVE=1
while [ $# -gt 0 ]; do
  case "$1" in
    --tags) TAGS="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;
    --no-archive) ARCHIVE=0; shift;;
    *) echo "unknown arg $1" >&2; exit 2;;
  esac
done

# os/arch[/goarm]  (armv7 = Raspberry Pi 2/3/4 32-bit, Termux on 32-bit Android phones)
TARGETS=(linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
if [ -n "$ONLY" ]; then
  IFS=',' read -r -a TARGETS <<< "$ONLY"
fi

rm -rf dist; mkdir -p dist; FAILED=""
LDFLAGS="-s -w -X redx/host.Version=${VERSION}"
for t in "${TARGETS[@]}"; do
  IFS='/' read -r os arch arm <<< "$t"
  name="${arch}"; [ "$arch" = "arm" ] && name="armv${arm:-7}"
  out="dist/redx-${VERSION}-${os}-${name}"
  ext=""; [ "$os" = "windows" ] && ext=".exe"
  mkdir -p "$out"
  ok=1
  for cmd in redxd redx; do
    echo "build $cmd $os/$name"
    GOOS=$os GOARCH=$arch GOARM=${arm:-} CGO_ENABLED=0 \
      go build -trimpath -tags "$TAGS" -ldflags "$LDFLAGS" -o "$out/$cmd$ext" "./cmd/$cmd" || ok=0
  done
  if [ "$ok" = 0 ]; then echo "FAILED target $os/$name (skipped)" >&2; FAILED="$FAILED $os/$name"; rm -rf "$out"; continue; fi
  cp -r deploy/systemd deploy/launchd deploy/windows "$out/" 2>/dev/null || true
  cp docs/HOSTING.md deploy/install.sh deploy/install.ps1 "$out/" 2>/dev/null || true
  cp deploy/redx.example.toml "$out/redx.example.toml" 2>/dev/null || true
  if [ "$ARCHIVE" = 1 ]; then
    base="$(basename "$out")"
    if [ "$os" = "windows" ] && command -v zip >/dev/null; then
      (cd dist && zip -qr "$base.zip" "$base")
    elif [ "$os" = "windows" ] && command -v python3 >/dev/null; then
      (cd dist && python3 -m zipfile -c "$base.zip" "$base")
    else
      tar -C dist -czf "dist/$base.tar.gz" "$base"
    fi
  fi
done
(cd dist && { ls *.tar.gz *.zip 2>/dev/null | xargs -r sha256sum; } > SHA256SUMS || true)
echo "done: dist/"
if [ -n "${FAILED:-}" ]; then echo "failed targets:$FAILED" >&2; exit 1; fi
