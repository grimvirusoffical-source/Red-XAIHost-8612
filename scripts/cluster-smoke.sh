#!/usr/bin/env bash
# 3-node cluster smoke test on loopback (leader + 2 followers joined with `redx node join`).
# usage: scripts/cluster-smoke.sh [bindir]
set -uo pipefail
BIN="${1:-bin}"; BIN="$(cd "$BIN" && pwd)"
D=${KEEP:-$(mktemp -d)}; PIDS=""; trap 'kill $PIDS 2>/dev/null; [ -n "${KEEP:-}" ] || rm -rf "$D"' EXIT
fail=0; ok() { echo "PASS  $*"; }; bad() { echo "FAIL  $*"; fail=1; }
free() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
P0=$(free); P1=$(free); P2=$(free)
start() { "$BIN/redxd" run --data-dir "$D/$1" --listen "127.0.0.1:$2" > "$D/$1.out" 2>&1 & PIDS="$PIDS $!"; for i in $(seq 1 50); do curl -sf "http://127.0.0.1:$2/healthz" >/dev/null && return; sleep 0.1; done; echo "start $1 failed"; cat "$D/$1.out"; exit 1; }
start leader $P0
export REDX_KEY=$(grep -o 'rx_live_[0-9A-Za-z]*' "$D/leader.out" | head -1)
export REDX_ADDR="http://127.0.0.1:$P0"
for n in 1 2; do
  eval P=\$P$n
  TOK=$("$BIN/redx" node token | head -1)
  REDX_KEY= "$BIN/redx" node join "http://127.0.0.1:$P0" --token "$TOK" --data-dir "$D/f$n" --node-id "f$n" --advertise "http://127.0.0.1:$P" >/dev/null && ok "node f$n joined" || bad "join f$n"
  start f$n $P
done
curl -s -o /dev/null -w '' -X PUT -H "Authorization: Bearer $REDX_KEY" --data '"hello"' "http://127.0.0.1:$P0/v1/path/c.red-xai.db:B.K"
for n in 1 2; do
  eval P=\$P$n
  for i in $(seq 1 50); do curl -s -H "Authorization: Bearer $REDX_KEY" "http://127.0.0.1:$P/v1/path/c.red-xai.db:B.K" | grep -q hello && break; sleep 0.1; done
  curl -s -H "Authorization: Bearer $REDX_KEY" "http://127.0.0.1:$P/v1/path/c.red-xai.db:B.K" | grep -q hello && ok "leader write replicated to f$n" || bad "replication to f$n"
done
c=$(curl -s -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $REDX_KEY" --data '"via-follower"' "http://127.0.0.1:$P1/v1/path/c.red-xai.db:B.K2")
[ "$c" = 200 ] && ok "write through follower forwarded to leader (200)" || bad "forwarded write ($c)"
curl -s -H "Authorization: Bearer $REDX_KEY" "http://127.0.0.1:$P0/v1/path/c.red-xai.db:B.K2" | grep -q via-follower && ok "forwarded write landed on leader" || bad "forwarded write missing on leader"
sleep 1
"$BIN/redx" node status
"$BIN/redx" node status --json | python3 -c 'import sys,json;d=json.load(sys.stdin);assert len(d["members"])==3 and all(m["healthy"] for m in d["members"])' && ok "3 healthy members" || bad "membership"
exit $fail
