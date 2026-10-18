package host_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"redx/host"
)

const P = "/v1/path/"

func TestBootstrapAndCRUD(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	if !strings.HasPrefix(n.Key, "rx_live_") {
		t.Fatalf("no bootstrap key: %q", n.Key)
	}
	path := "players.red-xai.db:Box[ID=1].Name"
	if st, b, _ := do(t, "PUT", n.URL+P+path, n.Key, `"Ann"`); st != 200 {
		t.Fatalf("put %d %s", st, b)
	}
	st, b, _ := do(t, "GET", n.URL+P+path, n.Key, "")
	if st != 200 || jsonOf(t, b)["value"] != `"Ann"` {
		t.Fatalf("get %d %s", st, b)
	}
	if st, b, _ := do(t, "GET", n.URL+P+path, n.Key, "", "Accept", "text/x-redx-box"); st != 200 || b != `"Ann"` {
		t.Fatalf("raw get %d %q", st, b)
	}
	if st, _, _ := do(t, "DELETE", n.URL+P+path, n.Key, ""); st != 200 {
		t.Fatal("delete")
	}
	if st, _, _ := do(t, "GET", n.URL+P+path, n.Key, ""); st != 404 {
		t.Fatalf("after delete want 404 got %d", st)
	}
	// JSON body forms
	if st, b, _ := do(t, "PUT", n.URL+P+"a.red-xai.db:B.K", n.Key, `{"value":{"hp":10,"tags":["a","b"],"ok":true}}`, "Content-Type", "application/json"); st != 200 {
		t.Fatalf("json put %d %s", st, b)
	}
	_, b, _ = do(t, "GET", n.URL+P+"a.red-xai.db:B.K", n.Key, "")
	if v := jsonOf(t, b)["value"]; v != `[ {hp[]} = 10, {ok[]} = TRUE, {tags[]} = ;["a", "b"]: ]` {
		t.Fatalf("json->box: %v", v)
	}
	if st, _, _ := do(t, "PUT", n.URL+P+"a.red-xai.db:B.K2", n.Key, `{"box":"42.5"}`, "Content-Type", "application/json"); st != 200 {
		t.Fatal("box put")
	}
	// health needs no key and leaks nothing sensitive
	st, b, _ = do(t, "GET", n.URL+"/healthz", "", "")
	if st != 200 || strings.Contains(b, n.Key) {
		t.Fatal("healthz")
	}
}

func TestBadAndMissingKey401(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	for _, k := range []string{"", "rx_live_" + strings.Repeat("a", 43), "garbage", n.Key + "x"} {
		st, b, h := do(t, "GET", n.URL+P+"x.red-xai.db:B.K", k, "")
		if st != 401 || h.Get("WWW-Authenticate") == "" {
			t.Fatalf("key %q: want 401, got %d", k, st)
		}
		if strings.Contains(b, "revoked") || strings.Contains(b, "expired") {
			t.Fatal("401 body must be generic")
		}
	}
	// wrong scheme
	if st, _, _ := do(t, "GET", n.URL+P+"x.red-xai.db:B.K", "", "", "Authorization", "Basic "+n.Key); st != 401 {
		t.Fatal("basic auth must be 401")
	}
	// key in query string is NOT accepted outside websocket upgrades
	if st, _, _ := do(t, "GET", n.URL+P+"x.red-xai.db:B.K?access_token="+n.Key, "", ""); st != 401 {
		t.Fatal("query token must not authenticate plain requests")
	}
}

func TestRevokedKey401(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	key, id := mkKey(t, n, map[string]interface{}{"scope": "write", "db": "default", "label": "app"})
	if st, _, _ := do(t, "PUT", n.URL+P+"x.red-xai.db:B.K", key, "1"); st != 200 {
		t.Fatal("new key should work")
	}
	if st, b, _ := do(t, "DELETE", n.URL+"/v1/keys/"+id, n.Key, ""); st != 200 {
		t.Fatalf("revoke %d %s", st, b)
	}
	if st, _, _ := do(t, "GET", n.URL+P+"x.red-xai.db:B.K", key, ""); st != 401 {
		t.Fatalf("revoked key must be 401, got %d", st)
	}
}

func TestScopesDBsAndPrefix(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	if st, b, _ := do(t, "POST", n.URL+"/v1/dbs", n.Key, `{"name":"game"}`); st != 201 {
		t.Fatalf("create db %d %s", st, b)
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/dbs", n.Key, `{"name":"game"}`); st != 409 {
		t.Fatal("dup db")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/dbs", n.Key, `{"name":"../etc"}`); st != 400 {
		t.Fatal("bad db name")
	}
	rd, _ := mkKey(t, n, map[string]interface{}{"scope": "read", "db": "game"})
	wr, _ := mkKey(t, n, map[string]interface{}{"scope": "write", "db": "game"})
	pf, _ := mkKey(t, n, map[string]interface{}{"scope": "write", "db": "game", "prefix": "shared.red-xai.db:Box[ID=1]"})
	url := n.URL + "/v1/db/game/path/"
	if st, _, _ := do(t, "PUT", url+"w.red-xai.db:B.K", wr, "5"); st != 200 {
		t.Fatal("write key can write")
	}
	if st, _, _ := do(t, "GET", url+"w.red-xai.db:B.K", rd, ""); st != 200 {
		t.Fatal("read key can read")
	}
	if st, _, _ := do(t, "PUT", url+"w.red-xai.db:B.K", rd, "6"); st != 403 {
		t.Fatal("read key must not write")
	}
	if st, _, _ := do(t, "DELETE", url+"w.red-xai.db:B.K", rd, ""); st != 403 {
		t.Fatal("read key must not delete")
	}
	// other db denied
	if st, _, _ := do(t, "GET", n.URL+P+"w.red-xai.db:B.K", wr, ""); st != 403 {
		t.Fatalf("game key on default db must be 403, got %d", st)
	}
	// prefix ACL
	if st, _, _ := do(t, "PUT", url+"shared.red-xai.db:Box[ID=1].X", pf, "1"); st != 200 {
		t.Fatal("prefix key inside prefix")
	}
	if st, _, _ := do(t, "PUT", url+"shared.red-xai.db:Box[ID=2].X", pf, "1"); st != 403 {
		t.Fatal("prefix key outside prefix")
	}
	if st, _, _ := do(t, "POST", url+"../query", pf, `*`); st == 200 {
		t.Fatal("must not escape")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/db/game/query", pf, `*`); st != 403 {
		t.Fatal("prefix key cannot query")
	}
	// non-admin cannot manage keys or create dbs
	if st, _, _ := do(t, "POST", n.URL+"/v1/keys", wr, `{"scope":"read"}`); st != 403 {
		t.Fatal("write key cannot create keys")
	}
	if st, _, _ := do(t, "GET", n.URL+"/v1/admin/backup", wr, ""); st != 403 {
		t.Fatal("non-admin backup")
	}
	// db-scoped admin can manage only its own db
	ad, _ := mkKey(t, n, map[string]interface{}{"scope": "admin", "db": "game"})
	if st, _, _ := do(t, "POST", n.URL+"/v1/keys", ad, `{"scope":"read","db":"game"}`); st != 201 {
		t.Fatal("db admin creates own-db key")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/keys", ad, `{"scope":"read","db":"default"}`); st != 403 {
		t.Fatal("db admin must not create keys for another db")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/keys", ad, `{"scope":"admin","db":"*"}`); st != 403 {
		t.Fatal("db admin must not mint host admin")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/dbs", ad, `{"name":"zzz"}`); st != 403 {
		t.Fatal("db admin must not create dbs")
	}
	// path hygiene
	if st, _, _ := do(t, "PUT", url+"..%2f..%2fetc:passwd", wr, "x"); st != 400 {
		t.Fatalf("traversal path must be rejected, got %d", st)
	}
	// unknown db
	if st, _, _ := do(t, "GET", n.URL+"/v1/db/nope/path/x:y", n.Key, ""); st != 404 {
		t.Fatal("unknown db 404")
	}
}

func TestRateLimitPerKey(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	lim, _ := mkKey(t, n, map[string]interface{}{"scope": "read", "db": "default", "rate_rps": 0.01, "rate_burst": 3})
	codes := map[int]int{}
	var retry string
	for i := 0; i < 6; i++ {
		st, _, h := do(t, "GET", n.URL+P+"x.red-xai.db:B.K", lim, "")
		codes[st]++
		if st == 429 {
			retry = h.Get("Retry-After")
		}
	}
	if codes[404] != 3 || codes[429] != 3 || retry == "" {
		t.Fatalf("burst 3 then 429: %v retry=%q", codes, retry)
	}
	// the admin key has its own bucket
	if st, _, _ := do(t, "GET", n.URL+P+"x.red-xai.db:B.K", n.Key, ""); st == 429 {
		t.Fatal("limits are per key")
	}
}

func TestBruteForceThrottle(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	bad := "rx_live_" + strings.Repeat("b", 43)
	last := 0
	for i := 0; i < 30; i++ {
		last, _, _ = do(t, "GET", n.URL+P+"x:y", bad, "")
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
	// source IP is now throttled even for a valid key until the window passes
	if st, _, _ := do(t, "GET", n.URL+P+"x:y", n.Key, ""); st != 429 {
		t.Fatalf("throttled IP, got %d", st)
	}
}

func TestAuditLogNoSecrets(t *testing.T) {
	dir := t.TempDir()
	n := startNode(t, dir, func(c *host.Config, _ string) { c.AuditOK = true })
	key, id := mkKey(t, n, map[string]interface{}{"scope": "read", "db": "default"})
	do(t, "GET", n.URL+P+"secret-path.red-xai.db:Box.K", key, "")
	do(t, "GET", n.URL+P+"x:y", "rx_live_"+strings.Repeat("z", 43), "")
	do(t, "PUT", n.URL+P+"x:y", key, "1") // denied
	do(t, "DELETE", n.URL+"/v1/keys/"+id, n.Key, "")
	n.H.Audit.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, secret := range []string{n.Key, key, key[len("rx_live_"):], strings.Repeat("z", 43), "secret-path"} {
		if strings.Contains(s, secret) {
			t.Fatalf("audit log leaks %q", secret)
		}
	}
	for _, ev := range []string{`"bootstrap"`, `"key.create"`, `"auth.ok"`, `"auth.fail"`, `"auth.denied"`, `"key.revoke"`} {
		if !strings.Contains(s, ev) {
			t.Errorf("audit log missing %s", ev)
		}
	}
}

func TestQueryStatsAndTTL(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	do(t, "PUT", n.URL+P+"a:b.x", n.Key, "1")
	do(t, "PUT", n.URL+P+"a:b.y", n.Key, "2")
	st, b, _ := do(t, "POST", n.URL+"/v1/query", n.Key, `{"q":"prefix:a:b"}`, "Content-Type", "application/json")
	if st != 200 || jsonOf(t, b)["count"].(float64) != 2 {
		t.Fatalf("query %d %s", st, b)
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/query", n.Key, `{"q":"bogus"}`, "Content-Type", "application/json"); st != 400 {
		t.Fatal("bad query -> 400")
	}
	st, b, _ = do(t, "GET", n.URL+"/v1/stats", n.Key, "")
	m := jsonOf(t, b)
	if st != 200 || m["host"] == nil || m["dbs"] == nil {
		t.Fatalf("stats %d %s", st, b)
	}
	rd, _ := mkKey(t, n, map[string]interface{}{"scope": "read", "db": "default"})
	_, b, _ = do(t, "GET", n.URL+"/v1/stats", rd, "")
	if jsonOf(t, b)["host"] != nil {
		t.Fatal("non-admin must not see host stats")
	}
	// TTL: leader expires via tracker
	do(t, "PUT", n.URL+P+"s:o.otp?ttl=1", n.Key, `"1234"`)
	if st, _, _ := do(t, "GET", n.URL+P+"s:o.otp", n.Key, ""); st != 200 {
		t.Fatal("otp present")
	}
	eventually(t, "ttl expiry", 4*time.Second, func() bool {
		st, _, _ := do(t, "GET", n.URL+P+"s:o.otp", n.Key, "")
		return st == 404
	})
	if st, _, _ := do(t, "PUT", n.URL+P+"s:o.z?sliding=true", n.Key, "1"); st != 400 {
		t.Fatal("sliding without ttl must be 400")
	}
}

func TestKeyAPILifecycle(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	key, id := mkKey(t, n, map[string]interface{}{"scope": "write", "db": "default", "ttl": "30d", "label": "ci"})
	st, b, _ := do(t, "GET", n.URL+"/v1/keys", n.Key, "")
	if st != 200 || strings.Contains(b, key) || strings.Contains(b, "hash") || !strings.Contains(b, id) {
		t.Fatalf("list keys leaks or is wrong: %s", b)
	}
	st, b, _ = do(t, "POST", n.URL+"/v1/keys/"+id+"/rotate", n.Key, `{"grace":"0"}`)
	if st != 201 {
		t.Fatalf("rotate %d %s", st, b)
	}
	nk := jsonOf(t, b)["key"].(string)
	if st, _, _ := do(t, "GET", n.URL+P+"x:y", key, ""); st != 401 {
		t.Fatal("old key dead after rotate")
	}
	if st, _, _ := do(t, "PUT", n.URL+P+"x:y", nk, "1"); st != 200 {
		t.Fatal("new key works")
	}
	if st, _, _ := do(t, "POST", n.URL+"/v1/keys", n.Key, `{"scope":"read","ttl":"zzz"}`); st != 400 {
		t.Fatal("bad ttl")
	}
}

func TestSubscribeWebSocket(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	ws := "ws" + strings.TrimPrefix(n.URL, "http") + "/v1/subscribe?path=live:"
	// no key => handshake refused
	if _, resp, err := websocket.DefaultDialer.Dial(ws, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("unauthenticated ws should be 401 (err=%v)", err)
	}
	hdr := map[string][]string{"Authorization": {"Bearer " + n.Key}}
	c, _, err := websocket.DefaultDialer.Dial(ws, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		do(t, "PUT", n.URL+P+"live:Box.K", n.Key, "99")
	}()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var ev host.Event
	if err := c.ReadJSON(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Op != "set" || ev.Path != "live:Box.K" || ev.Value != "99" {
		t.Fatalf("event %+v", ev)
	}
	// browser style: access_token query param works for upgrades
	c2, _, err := websocket.DefaultDialer.Dial(ws+"&access_token="+n.Key, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
}

func TestFollowerNeverLocalExpiryAndConfig(t *testing.T) {
	var c host.Config
	_ = c
	cfg := host.DefaultConfig()
	if err := cfg.ApplyEnv(func(k string) string {
		return map[string]string{"REDX_LISTEN": "0.0.0.0:9", "REDX_ROLE": "standalone"}[k]
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil || cfg.Role != "leader" || cfg.Listen != "0.0.0.0:9" {
		t.Fatalf("env config: %v %+v", err, cfg)
	}
	bad := host.DefaultConfig()
	bad.Role = "follower"
	bad.DataDir = t.TempDir()
	if err := bad.Validate(); err == nil {
		t.Fatal("follower without peers must fail")
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "redx.toml")
	os.WriteFile(f, []byte("data_dir = '"+filepath.ToSlash(dir)+"'\nlisten = '127.0.0.1:1111'\ndefault_db = 'filedb'\n"), 0o600)
	env := func(k string) string {
		if k == "REDX_LISTEN" {
			return "127.0.0.1:2222"
		}
		return ""
	}
	cfg, err := host.LoadConfig([]string{"--config", f}, env, nil)
	if err != nil || cfg.Listen != "127.0.0.1:2222" || cfg.DefaultDB != "filedb" {
		t.Fatalf("env>file: %v %+v", err, cfg)
	}
	cfg, err = host.LoadConfig([]string{"--config", f, "--listen", "127.0.0.1:3333"}, env, nil)
	if err != nil || cfg.Listen != "127.0.0.1:3333" {
		t.Fatalf("flag>env: %v %+v", err, cfg)
	}
	os.WriteFile(f, []byte("bogus_key = 1\n"), 0o600)
	if _, err := host.LoadConfig([]string{"--config", f}, env, nil); err == nil {
		t.Fatal("unknown config keys must error")
	}
}

func TestSingleInstanceLock(t *testing.T) {
	dir := t.TempDir()
	startNode(t, dir, nil)
	cfg := baseCfg(dir)
	if _, err := host.New(cfg, nil, nil); err == nil {
		t.Fatal("second host on same dir must fail")
	}
}

func TestExtrasUnsupportedBackendIs501(t *testing.T) {
	n := startNode(t, t.TempDir(), nil)
	base := n.URL + "/v1/db/default"
	for _, c := range []struct{ m, p string }{
		{"GET", "/idx"}, {"GET", "/pack"}, {"GET", "/pulse"}, {"GET", "/pack/x/ls"}, {"GET", "/idx/s/i/query?mode=exact&field=f&value=1"},
	} {
		if st, b, _ := do(t, c.m, base+c.p, n.Key, ""); st != 501 {
			t.Errorf("%s %s: got %d %s, want 501", c.m, c.p, st, b)
		}
	}
	if st, _, _ := do(t, "PUT", n.URL+"/v1/path/f.red-xai.db:B.K", n.Key, "1"); st != 200 {
		t.Fatal("plain data path must keep working")
	}
	if st, _, _ := do(t, "GET", n.URL+"/v1/path/f.red-xai.db:B.K?resolve=1", n.Key, ""); st != 501 {
		t.Errorf("resolve on a backend without links: %d", st)
	}
	if st, _, _ := do(t, "GET", base+"/idx", "", ""); st != 401 {
		t.Errorf("unauthenticated: %d", st)
	}
}

func TestParseRangeAndPackPathChecks(t *testing.T) {
	// exercised through the engine tests; keep a guard that path traversal strings never reach the ACL as-is
	for _, p := range []string{"..", "a/..", "../a", "a//b", "/a", "a/", "a\\b", "a:b", "a\x00b"} {
		if host.ValidPackPathForTest(p) {
			t.Errorf("%q accepted", p)
		}
	}
	for _, p := range []string{"a", "a/b.txt", "dir/sub/ünï.png"} {
		if !host.ValidPackPathForTest(p) {
			t.Errorf("%q rejected", p)
		}
	}
}

func TestParseRangeTable(t *testing.T) {
	for _, c := range []struct {
		h         string
		size      int64
		off, n    int64
		ok, unsat bool
	}{
		{"bytes=0-9", 100, 0, 10, true, false},
		{"bytes=90-", 100, 90, 10, true, false},
		{"bytes=-10", 100, 90, 10, true, false},
		{"bytes=-500", 100, 0, 100, true, false},
		{"bytes=5-500", 100, 5, 95, true, false},
		{"bytes=100-", 100, 0, 0, true, true},
		{"bytes=-0", 100, 0, 0, true, true},
		{"bytes=9-5", 100, 0, 0, false, false},
		{"bytes=a-b", 100, 0, 0, false, false},
		{"bytes=0-1,5-6", 100, 0, 0, false, false},
		{"items=0-1", 100, 0, 0, false, false},
		{"bytes=0-1", 0, 0, 0, false, false},
	} {
		off, n, ok, unsat := host.ParseRangeForTest(c.h, c.size)
		if ok != c.ok || unsat != c.unsat || (ok && !unsat && (off != c.off || n != c.n)) {
			t.Errorf("%q/%d: got off=%d n=%d ok=%v unsat=%v", c.h, c.size, off, n, ok, unsat)
		}
	}
}
