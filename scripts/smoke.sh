#!/usr/bin/env bash
# Smoke test: start redxd on 127.0.0.1 (random port), exercise keys + data with curl.
# usage: scripts/smoke.sh [bindir]     (default ./bin ; needs redxd, redx, curl)
set -uo pipefail
BIN="${1:-bin}"; BIN="$(cd "$BIN" && pwd)"
D=$(mktemp -d); trap 'kill $PID 2>/dev/null; wait $PID 2>/dev/null; rm -rf "$D"' EXIT
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
URL="http://127.0.0.1:$PORT"
fail=0; ok() { echo "PASS  $*"; }; bad() { echo "FAIL  $*"; fail=1; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

"$BIN/redxd" keygen --data-dir "$D" --id smoke-key >/dev/null && ok "redxd keygen wrote master.key (mode $(stat -c %a "$D/master.key"))" || bad "keygen"
[ "$(stat -c %a "$D/master.key")" = 600 ] || bad "master.key is not mode 600"
"$BIN/redxd" run --data-dir "$D" --code-allowed-boxes 'players.red-xai.db:Player' --listen "127.0.0.1:$PORT" > "$D/out.txt" 2>&1 & PID=$!
for i in $(seq 1 50); do curl -sf "$URL/healthz" >/dev/null && break; sleep 0.1; done
curl -sf "$URL/healthz" >/dev/null && ok "healthz" || { bad "daemon did not start"; cat "$D/out.txt"; exit 1; }

ADMIN=$(grep -o 'rx_live_[0-9A-Za-z]*' "$D/out.txt" | head -1)
[ -n "$ADMIN" ] && ok "bootstrap admin key printed once on console" || bad "no bootstrap key"
[ "$(grep -c "$ADMIN" "$D/out.txt")" = 1 ] && ok "bootstrap key appears exactly once in output" || bad "bootstrap key repeated"
grep -rq "$ADMIN" "$D" --exclude=out.txt && bad "admin key found on disk in clear text" || ok "admin key not stored on disk in clear text"

export REDX_ADDR="$URL" REDX_KEY="$ADMIN"
WKEY=$("$BIN/redx" key create --db default --scope write --ttl 30d --label smoke --json | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["key"])')
[[ "$WKEY" == rx_live_* ]] && ok "redx key create -> $(echo "$WKEY" | cut -c1-12)…" || bad "key create"

P="$URL/v1/path/players.red-xai.db:Box%5BID=1%5D.Name"
[ "$(code -X PUT -H "Authorization: Bearer $WKEY" --data '"Ann"' "$P")" = 200 ] && ok "PUT 200" || bad "PUT"
curl -s -H "Authorization: Bearer $WKEY" "$P" | grep -q 'Ann' && ok "GET returns value" || bad "GET"
[ "$(code -X DELETE -H "Authorization: Bearer $WKEY" "$P")" = 200 ] && ok "DELETE 200" || bad "DELETE"
[ "$(code -H "Authorization: Bearer $WKEY" "$P")" = 404 ] && ok "GET after delete 404" || bad "GET after delete"
[ "$(code -H "Authorization: Bearer rx_live_$(printf 'a%.0s' $(seq 43))" "$P")" = 401 ] && ok "bad key 401" || bad "bad key"
[ "$(code "$P")" = 401 ] && ok "no key 401" || bad "no key"
ID=$("$BIN/redx" key list --json | python3 -c 'import sys,json;print([k["id"] for k in json.load(sys.stdin) if k["label"]=="smoke"][0])')
"$BIN/redx" key revoke "$ID" >/dev/null && [ "$(code -H "Authorization: Bearer $WKEY" "$P")" = 401 ] && ok "revoked key 401" || bad "revoked key"
"$BIN/redx" doctor >/dev/null 2>&1; ok "redx doctor ran (exit $?)"
grep -q "$WKEY" "$D/audit.log" && bad "audit log contains a key" || ok "audit log has no key material"
grep -q auth.fail "$D/audit.log" && ok "audit log recorded auth failures" || bad "audit missing auth.fail"
# ---- version flags ----
"$BIN/redxd" --version | grep -q '^redxd ' && "$BIN/redx" --version | grep -q '^redx ' && ok "--version on redxd and redx ($("$BIN/redx" --version))" || bad "--version"

# ---- set/get, links, indexes ----
RW=$("$BIN/redx" key create --db default --scope write --label smoke2 --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["key"])')
AUTH=(-H "Authorization: Bearer $RW")
"$BIN/redx" set 'guilds.red-xai.db:Guild[ID=1]' '[ {Name[]} = "Dragons" ]' >/dev/null
"$BIN/redx" set 'players.red-xai.db:Player[ID=1]' '[ {Name[]} = "Ann", {Level[]} = 7, {Guild[]} = "players.red-xai.db":Player[ID=1] @> "guilds.red-xai.db":Guild[ID=1] ]' >/dev/null
"$BIN/redx" set 'players.red-xai.db:Player[ID=2]' '[ {Name[]} = "Bob", {Level[]} = 30 ]' >/dev/null
"$BIN/redx" get 'players.red-xai.db:Player[ID=1].Name' | grep -q Ann && ok "redx set/get" || bad "redx set/get"
"$BIN/redx" get 'players.red-xai.db:Player[ID=1].Guild' --resolve | grep -q Dragons && ok "get --resolve follows the link" || bad "resolve"
cat > "$D/idx.decl" <<'EOF'
{ByName[]}  = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Name"]:,  {Kind[]} = "exact" ];
{ByLevel[]} = [ {File[]} = "players.red-xai.db", {Box[]} = "Player", {Fields[]} = ;["Level"]:, {Kind[]} = "range" ];
EOF
"$BIN/redx" idx define players "$D/idx.decl" >/dev/null && ok "idx define (admin)" || bad "idx define"
"$BIN/redx" idx query players ByName --field Name --value Ann 2>/dev/null | grep -q 'Player\[ID=1\]' && ok "idx query exact" || bad "idx exact"
"$BIN/redx" idx query players ByLevel --field Level --lo 10 --hi 50 2>/dev/null | grep -q 'Player\[ID=2\]' && ok "idx query range" || bad "idx range"
Q="$URL/v1/db/default/idx/players/ByName/query?mode=exact&field=Name&value=Ann"
curl -s "${AUTH[@]}" "$Q" | grep -q 'Player\[ID=1\]' && ok "idx query over REST (curl)" || bad "idx REST"
[ "$(code "${AUTH[@]}" -X PUT --data-binary @"$D/idx.decl" "$URL/v1/db/default/idx/x")" = 403 ] && ok "write key cannot define indexes (403)" || bad "idx define scope"
[ "$(code -H "Authorization: Bearer rx_live_$(printf 'a%.0s' $(seq 43))" "$Q")" = 401 ] && ok "idx query bad key 401" || bad "idx bad key"
[ "$(code "$Q")" = 401 ] && ok "idx query no key 401" || bad "idx no key"

# ---- packs: put/get/range ----
python3 -c 'import os,sys;open(sys.argv[1],"wb").write(os.urandom(2*1024*1024+4321))' "$D/blob.bin"
"$BIN/redx" pack put media "$D/blob.bin" dir/blob.bin >/dev/null && ok "pack put (sha256 verified by server)" || bad "pack put"
"$BIN/redx" pack get media dir/blob.bin --out "$D/got.bin" 2>/dev/null && cmp -s "$D/blob.bin" "$D/got.bin" && ok "pack get round trip is byte-identical" || bad "pack get"
PF="$URL/v1/db/default/pack/media/file/dir/blob.bin"
curl -s "${AUTH[@]}" -H 'Range: bytes=1048570-1048590' "$PF" -o "$D/r.bin" -D "$D/r.hdr"
head -c 1048591 "$D/blob.bin" | tail -c 21 > "$D/r.exp"
grep -q '^HTTP/1.1 206' "$D/r.hdr" && cmp -s "$D/r.bin" "$D/r.exp" && ok "Range read across a chunk boundary: 206, exact bytes" || bad "range read"
grep -qi "^content-range: bytes 1048570-1048590/$(stat -c %s "$D/blob.bin")" "$D/r.hdr" && ok "Content-Range header correct" || bad "content-range"
"$BIN/redx" pack get media dir/blob.bin --out "$D/p.bin" --range=-100 2>/dev/null && tail -c 100 "$D/blob.bin" | cmp -s - "$D/p.bin" && ok "redx pack get --range -100 (suffix)" || bad "cli range"
[ "$(code "${AUTH[@]}" -H 'Range: bytes=999999999-' "$PF")" = 416 ] && ok "unsatisfiable range 416" || bad "416"
"$BIN/redx" pack ls media dir 2>/dev/null | grep -q blob.bin && ok "pack ls" || bad "pack ls"
[ "$(code -H "Authorization: Bearer rx_live_$(printf 'a%.0s' $(seq 43))" "$PF")" = 401 ] && ok "pack get bad key 401" || bad "pack bad key"
[ "$(code "$PF")" = 401 ] && ok "pack get no key 401" || bad "pack no key"
[ "$(code --path-as-is "${AUTH[@]}" -X PUT --data x "$URL/v1/db/default/pack/media/file/../evil")" = 400 ] && ok "path traversal in pack path rejected" || bad "traversal"
"$BIN/redx" pack fsck media --deep 2>/dev/null | grep -q '"ok": true' && ok "pack fsck --deep ok" || bad "fsck"
# ---- 0.2.0: SQL, media (Range), sec policy, code, 404/subscribe quirks ----
ADM=(-H "Authorization: Bearer $ADMIN")
sqlq() { curl -s "${ADM[@]}" -H 'Content-Type: application/json' --data "$1" "$URL/v1/sql"; }
sqlq '{"q":"SELECT Name FROM players.Player WHERE Level > ? ORDER BY Name","params":[10]}' | grep -q '"Bob"' && ok "POST /v1/sql SELECT with a parameter" || bad "sql select"
"$BIN/redx" sql 'SELECT Name FROM players.Player WHERE Level > ?' --param 10 2>/dev/null | grep -q Bob && ok "redx sql" || bad "redx sql"
RO=$("$BIN/redx" key create --db default --scope read --label smoke-ro --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["key"])')
[ "$(code -H "Authorization: Bearer $RO" -X POST --data 'DELETE FROM players.Player WHERE _id = 1' "$URL/v1/sql")" = 403 ] && ok "read key cannot run SQL writes (403)" || bad "sql read key"
[ "$(code -X POST --data 'SELECT 1' "$URL/v1/sql")" = 401 ] && ok "sql without key 401" || bad "sql no key"
[ "$(code "${ADM[@]}" -X POST --data 'SELEKT' "$URL/v1/sql")" = 400 ] && ok "sql syntax error 400" || bad "sql 400"
python3 - "$D/a.png" <<'PY'
import struct,zlib,sys
w=h=16
raw=b"".join(b"\x00"+b"".join(bytes([(x*16)%256,(y*16)%256,128]) for x in range(w)) for y in range(h))
def ch(t,d):
    c=struct.pack(">I",len(d))+t+d
    return c+struct.pack(">I",zlib.crc32(t+d)&0xffffffff)
open(sys.argv[1],"wb").write(b"\x89PNG\r\n\x1a\n"+ch(b"IHDR",struct.pack(">IIBBBBB",w,h,8,2,0,0,0))+ch(b"IDAT",zlib.compress(raw))+ch(b"IEND",b""))
PY
"$BIN/redx" media put art "$D/a.png" a.png --type image/png >/dev/null && ok "media put (type verified from the bytes)" || bad "media put"
MF="$URL/v1/db/default/media/art/file/a.png"
curl -s "${AUTH[@]}" -H 'Range: bytes=8-23' "$MF" -o "$D/m.bin" -D "$D/m.hdr"
head -c 24 "$D/a.png" | tail -c 16 | cmp -s - "$D/m.bin" && grep -q '^HTTP/1.1 206' "$D/m.hdr" && ok "media Range: 206, exact bytes" || bad "media range"
grep -qi "^content-range: bytes 8-23/$(stat -c %s "$D/a.png")" "$D/m.hdr" && ok "media Content-Range correct" || bad "media content-range"
[ "$(code "${AUTH[@]}" -X PUT --data 'plain text' "$URL/v1/db/default/media/art/file/t.png")" = 415 ] && ok "non-media upload refused (415)" || bad "media 415"
[ "$(code "$MF")" = 401 ] && ok "media without key 401" || bad "media no key"
# GET of an absent path is always 404; bare-file subscribe is an error
[ "$(code "${AUTH[@]}" "$URL/v1/path/nofile.red-xai.db:Box%5BID=9%5D.X")" = 404 ] && ok "absent .db path 404" || bad "absent db 404"
[ "$(code "${AUTH[@]}" "$URL/v1/path/nosess.red-xai-db-qs:Q%5BID=9%5D.X")" = 404 ] && ok "absent qs path 404 (not 200 NELL)" || bad "absent qs 404"
[ "$(code "${AUTH[@]}" -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' "$URL/v1/subscribe?path=players")" = 400 ] && ok "subscribe to a bare file name is 400" || bad "subscribe bare"
# sec policy: role "guest" may not read Level; encrypt packs named vault*
cat > "$D/pol.red-xai-sec" <<'EOF'
{Role[1]} = [ {Name[]} = "guest" ];
{Rule[1]} = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*", {Resource[]} = "players.red-xai.db:Player.Level" ];
{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "smoke-key" ];
EOF
"$BIN/redx" sec put "$D/pol.red-xai-sec" >/dev/null && ok "sec put (admin)" || bad "sec put"
GK=$("$BIN/redx" key create --db default --scope read --roles guest --label smoke-guest --json | python3 -c 'import sys,json;print(json.load(sys.stdin)["key"])')
curl -s -H "Authorization: Bearer $GK" "$URL/v1/path/players.red-xai.db:Player%5BID=1%5D" | grep -q 'Level' && bad "guest sees the denied field" || ok "guest record read is redacted"
[ "$(code -H "Authorization: Bearer $GK" -X POST --data 'SELECT Level FROM players.Player' "$URL/v1/sql")" = 403 ] && ok "guest SQL on the denied field 403" || bad "guest sql"
curl -s "${ADM[@]}" "$URL/v1/path/players.red-xai.db:Player%5BID=1%5D" | grep -q 'Level' && ok "admin still sees the field" || bad "admin read"
# encrypted pack: plaintext not on disk, round trip works
python3 -c 'open("'"$D"'/sec.txt","w").write("SMOKE-PLAINTEXT-MARKER "*200)'
"$BIN/redx" pack put vault "$D/sec.txt" s.txt >/dev/null && ok "encrypted pack put" || bad "enc pack put"
"$BIN/redx" pack get vault s.txt --out "$D/sec.out" 2>/dev/null && cmp -s "$D/sec.txt" "$D/sec.out" && ok "encrypted pack round trip" || bad "enc pack get"
grep -rl 'SMOKE-PLAINTEXT-MARKER' "$D/dbs/default/vault.red-xai-pack" 2>/dev/null | grep -q . && bad "plaintext at rest" || ok "no plaintext at rest in the encrypted pack"
# code (sandboxed; AllowedBoxes passed to the daemon with --code-allowed-boxes)
cat > "$D/game.red-xai-code" <<'EOF'
{Script[1]} = [ {Name[]} = "heal", {Params[]} = ;["target", "amount"]:, {Uses[]} = ;["db_get", "db_set"]:, {Source[]} = "let hp = db_get(target, \"Level\"); db_set(target, \"Level\", hp + amount); return hp + amount;" ];
EOF
"$BIN/redx" code define game "$D/game.red-xai-code" >/dev/null && ok "code define" || bad "code define"
"$BIN/redx" code run heal 'players.red-xai.db:Player[ID=1]' 3 2>/dev/null | grep -q 10 && ok "code run (sandboxed, AllowedBoxes)" || bad "code run"
exit $fail
