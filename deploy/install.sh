#!/bin/sh
# RedX host installer for Linux and macOS. Installs from a LOCAL build directory (nothing is downloaded).
#
#   sudo ./install.sh --from dist/redx-<ver>-linux-amd64      # system service (systemd / launchd)
#   ./install.sh --from dist --user                           # no root: ~/.local/bin + ~/.redx, no service
#   ./install.sh --from dist --listen 0.0.0.0:7777 --tls auto # reachable from other devices, with TLS
#
# --from may be the directory holding redxd+redx, or the dist/ root (the right redx-*-OS-ARCH folder is picked).
set -eu

FROM=""; PREFIX=/usr/local; LISTEN="127.0.0.1:7777"; TLS="off"; USERMODE=0; NOSVC=0; ROLE=leader; PEERS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --from) FROM="$2"; shift 2;;
    --prefix) PREFIX="$2"; shift 2;;
    --listen) LISTEN="$2"; shift 2;;
    --tls) TLS="$2"; shift 2;;
    --role) ROLE="$2"; shift 2;;
    --peers) PEERS="$2"; shift 2;;
    --user) USERMODE=1; NOSVC=1; shift;;
    --no-service) NOSVC=1; shift;;
    -h|--help) sed -n '2,10p' "$0"; exit 0;;
    *) echo "unknown option: $1" >&2; exit 2;;
  esac
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]'); M=$(uname -m)
case "$M" in
  x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; armv7*|armv6*|armhf) ARCH=armv7;;
  *) echo "unsupported CPU: $M" >&2; exit 1;;
esac
case "$OS" in linux|darwin) ;; *) echo "unsupported OS: $OS (use install.ps1 on Windows)" >&2; exit 1;; esac

[ -n "$FROM" ] || { HERE=$(cd "$(dirname "$0")" && pwd); FROM="$HERE"; [ -x "$FROM/redxd" ] || FROM="$HERE/../dist"; }
if [ ! -x "$FROM/redxd" ]; then
  CAND=$(ls -d "$FROM"/redx-*-"$OS"-"$ARCH" 2>/dev/null | head -n1 || true)
  [ -n "$CAND" ] && FROM="$CAND"
fi
[ -x "$FROM/redxd" ] && [ -x "$FROM/redx" ] || { echo "redxd/redx not found in $FROM (build first: make dist)" >&2; exit 1; }
echo "installing $OS/$ARCH binaries from $FROM"

HERE_DEPLOY=$(cd "$(dirname "$0")" && pwd)
EXAMPLE="$HERE_DEPLOY/redx.example.toml"; [ -f "$EXAMPLE" ] || EXAMPLE="$FROM/redx.example.toml"

write_conf() { # $1=path $2=data_dir
  mkdir -p "$(dirname "$1")"
  if [ -e "$1" ]; then echo "keeping existing config $1"; return; fi
  {
    echo "data_dir = \"$2\""
    echo "listen = \"$LISTEN\""
    echo "tls = \"$TLS\""
    echo "role = \"$ROLE\""
    if [ -n "$PEERS" ]; then echo "peers = [\"$PEERS\"]"; fi
  } > "$1"
  chmod 640 "$1" 2>/dev/null || true
}

make_admin_key() { # $1=data dir ; remaining: command prefix (e.g. runuser -u redx --)
  D="$1"; shift
  if [ "$ROLE" = follower ]; then echo "follower node: keys come from the leader; skipping admin key"; return; fi
  if [ -e "$D/keys.red-xai-keys" ]; then echo "keystore already exists; not creating another admin key"; return; fi
  echo
  echo "==== Creating the first ADMIN API key (shown once, never stored in clear) ===="
  "$@" "$BIN/redx" key create --offline --data-dir "$D" --scope admin --db '*' --label bootstrap-admin
  echo "================================================================================"
}

if [ "$USERMODE" = 1 ]; then
  BIN="${HOME}/.local/bin"; DATA="${HOME}/.redx/data"; CONF="${HOME}/.redx/redx.toml"
  mkdir -p "$BIN" "$DATA"; chmod 700 "$DATA"
  cp "$FROM/redxd" "$FROM/redx" "$BIN/"
  write_conf "$CONF" "$DATA"
  make_admin_key "$DATA"
  echo "installed to $BIN. Start the host with:"
  echo "   $BIN/redxd run --config $CONF"
  case ":$PATH:" in *":$BIN:"*) ;; *) echo "(add $BIN to your PATH)";; esac
  exit 0
fi

[ "$(id -u)" = 0 ] || { echo "run as root (sudo), or use --user" >&2; exit 1; }
BIN="$PREFIX/bin"
install -d "$BIN"
install -m 0755 "$FROM/redxd" "$FROM/redx" "$BIN/"

if [ "$OS" = linux ]; then
  DATA=/var/lib/redx; CONF=/etc/redx/redx.toml
  id redx >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin redx 2>/dev/null || adduser --system --home "$DATA" --shell /usr/sbin/nologin redx
  install -d -m 0700 -o redx -g redx "$DATA"
  write_conf "$CONF" "$DATA"; chgrp redx "$CONF" 2>/dev/null || true
  if command -v runuser >/dev/null; then RUN="runuser -u redx --"; elif command -v sudo >/dev/null; then RUN="sudo -u redx"; else RUN=""; fi
  make_admin_key "$DATA" $RUN
  [ -z "$RUN" ] || true
  chown -R redx:redx "$DATA"
  if [ "$NOSVC" = 0 ] && command -v systemctl >/dev/null; then
    install -m 0644 "$HERE_DEPLOY/systemd/redxd.service" /etc/systemd/system/redxd.service 2>/dev/null || install -m 0644 "$FROM/systemd/redxd.service" /etc/systemd/system/redxd.service
    sed -i "s#/usr/local/bin#$BIN#g" /etc/systemd/system/redxd.service
    systemctl daemon-reload
    if [ "$ROLE" = follower ]; then
      systemctl enable redxd
      echo "follower: service installed but NOT started. Join first, then start:"
      echo "   redx node join <leader-url> --token <rxj_...> --pin <fingerprint> --advertise <this-url> --data-dir $DATA"
      echo "   chown -R redx:redx $DATA && systemctl start redxd"
    else
      systemctl enable --now redxd
      sleep 1; systemctl --no-pager --lines=5 status redxd || true
    fi
  else
    echo "no systemd (or --no-service): run manually:  sudo -u redx $BIN/redxd run --config $CONF"
  fi
else
  DATA=/usr/local/var/redx; CONF=/usr/local/etc/redx/redx.toml
  install -d -m 0700 "$DATA" /usr/local/var/log
  write_conf "$CONF" "$DATA"
  make_admin_key "$DATA"
  if [ "$NOSVC" = 0 ]; then
    P=/Library/LaunchDaemons/com.redx.redxd.plist
    install -m 0644 "$HERE_DEPLOY/launchd/com.redx.redxd.plist" "$P" 2>/dev/null || install -m 0644 "$FROM/launchd/com.redx.redxd.plist" "$P"
    sed -i '' "s#/usr/local/bin#$BIN#g" "$P"
    launchctl unload "$P" 2>/dev/null || true
    if [ "$ROLE" = follower ]; then
      echo "follower: launchd plist installed but not loaded. Run 'redx node join ...' then: sudo launchctl load -w $P"
    else
      launchctl load -w "$P"
      echo "launchd service com.redx.redxd loaded (runs as root; log: /usr/local/var/log/redxd.log)"
    fi
  fi
fi
echo
echo "Done. Check:  REDX_KEY=<your admin key> redx doctor     and     curl http://127.0.0.1:7777/healthz"
