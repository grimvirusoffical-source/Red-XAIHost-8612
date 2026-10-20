#!/usr/bin/env bash
# Starts the real panel server on a random loopback port and checks which
# address the auth rate limiter buckets on. usage: packages/web/tests/rate-limit-e2e.sh
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"; cd "$ROOT/packages/web"
fail=0; ok() { echo "PASS  $*"; }; bad() { echo "FAIL  $*"; fail=1; }
port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
D=$(mktemp -d)
start() { # $1 = extra env assignments
  P=$(port)
  env REDX_DATA_DIR="$D" REDX_AUTO_LOCAL_NODE=false REDX_BOOTSTRAP_INFECTEDNATION=false PORT=$P REDX_BIND_HOST=127.0.0.1 $1 \
    bun --env-file=../../.env src/__server.ts > "$D/log.$P" 2>&1 & PID=$!
  for i in $(seq 1 100); do curl -s -o /dev/null "http://127.0.0.1:$P/api/health" && return 0; sleep 0.1; done
  cat "$D/log.$P"; return 1
}
attempts() { # sends 15 sign-in attempts, each with a fresh cf-connecting-ip; prints how many hit our limiter
  n=0
  for i in $(seq 1 15); do
    b=$(curl -s -X POST -H 'content-type: application/json' -H "cf-connecting-ip: 198.51.100.$i" \
      --data '{"email":"nobody@example.invalid","password":"wrong-password"}' "http://127.0.0.1:$P/api/auth/sign-in/email")
    echo "$b" | grep -q '"rate_limited"' && n=$((n+1))
  done
  echo $n
}
stop() { kill $PID 2>/dev/null; wait $PID 2>/dev/null; }
trap 'stop; rm -rf "$D"' EXIT

start "" || { bad "server did not start (default config)"; exit 1; }
n=$(attempts); stop
[ "$n" -ge 5 ] && ok "default config: spoofed cf-connecting-ip ignored, $n/15 attempts limited by peer address" || bad "default config: only $n/15 limited (header was honoured)"

start "REDX_TRUST_CLOUDFLARE=true" || { bad "server did not start (flag on)"; exit 1; }
n=$(attempts); stop
[ "$n" -ge 5 ] && ok "flag on, loopback peer not trusted: header ignored, $n/15 limited" || bad "flag on, untrusted peer: only $n/15 limited"

start "REDX_TRUST_CLOUDFLARE=true REDX_TRUSTED_PROXIES=127.0.0.1" || { bad "server did not start (trusted)"; exit 1; }
n=$(attempts); stop
[ "$n" = 0 ] && ok "flag on, trusted peer: header honoured, each address gets its own bucket (0/15 limited)" || bad "flag on, trusted peer: $n/15 limited"
exit $fail
