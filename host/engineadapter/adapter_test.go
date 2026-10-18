package engineadapter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"redx/box"
	"redx/host"
	"redx/host/engineadapter"
)

type node struct {
	h   *host.Host
	srv *httptest.Server
	url string
	key string
}

func start(t *testing.T, mod func(*host.Config, string)) *node {
	t.Helper()
	n := &node{srv: httptest.NewUnstartedServer(nil)}
	n.url = "http://" + n.srv.Listener.Addr().String()
	cfg := host.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.ArgonMemKiB, cfg.ArgonTime = 64, 1
	cfg.Heartbeat = 1
	cfg.Advertise = n.url
	cfg.ClusterKey = "engine-test-cluster-secret"
	if mod != nil {
		mod(&cfg, n.url)
	}
	h, err := host.New(cfg, engineadapter.Opener(engineadapter.Config{SweepMS: 50}), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	n.h, n.key = h, h.BootstrapKey
	n.srv.Config.Handler = h.Handler()
	n.srv.Start()
	t.Cleanup(func() { n.srv.Close(); n.h.Close() })
	return n
}

func do(t *testing.T, method, url, key, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func eventually(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestHostOnRealEngineCRUDAndErrors(t *testing.T) {
	n := start(t, nil)
	p := n.url + "/v1/path/"
	if st, b := do(t, "PUT", p+"accounts.red-xai.db:AccountVault[ID=1].Name", n.key, `"Ann"`); st != 200 {
		t.Fatalf("put %d %s", st, b)
	}
	st, b := do(t, "GET", p+"accounts.red-xai.db:AccountVault[ID=1].Name", n.key, "")
	var m map[string]string
	json.Unmarshal([]byte(b), &m)
	if st != 200 || m["value"] != `"Ann"` {
		t.Fatalf("get %d %s", st, b)
	}
	// JSON object -> box
	if st, b := do(t, "PUT", p+"accounts.red-xai.db:AccountVault[ID=2].Stats", n.key, ""); st != 400 {
		t.Fatalf("empty value should be 400, got %d %s", st, b)
	}
	if st, b := do(t, "PUT", n.url+"/v1/path/accounts.red-xai.db:AccountVault[ID=2].Stats", n.key, `[ {hp[]} = 10, {tags[]} = ;["a", "b"]: ]`); st != 200 {
		t.Fatalf("box literal put %d %s", st, b)
	}
	// engine validation surfaces as 400, not 500
	if st, _ := do(t, "PUT", p+"sessions.red-xai-db-qs:Q.B", n.key, `1`); st != 400 {
		t.Fatalf("qs write without TTL must be 400, got %d", st)
	}
	if st, _ := do(t, "PUT", p+"weird.txt:A.B", n.key, `1`); st != 400 {
		t.Fatalf("bad path must be 400, got %d", st)
	}
	if st, _ := do(t, "PUT", p+"accounts.red-xai.db:A.B", n.key, `{{{`); st != 400 {
		t.Fatalf("bad box grammar must be 400, got %d", st)
	}
	if st, _ := do(t, "GET", p+"accounts.red-xai.db:AccountVault[ID=9].Nope", n.key, ""); st != 404 {
		t.Fatalf("missing must be 404, got %d", st)
	}
	st, b = do(t, "POST", n.url+"/v1/query", n.key, `accounts.red-xai.db:AccountVault[ID=*].Name`)
	if st != 200 || !strings.Contains(b, "Ann") {
		t.Fatalf("query %d %s", st, b)
	}
	if st, _ := do(t, "DELETE", p+"accounts.red-xai.db:AccountVault[ID=1].Name", n.key, ""); st != 200 {
		t.Fatal("delete")
	}
	if st, b := do(t, "GET", n.url+"/v1/stats", n.key, ""); st != 200 || !strings.Contains(b, "dbs") {
		t.Fatalf("stats %d %s", st, b)
	}
}

func TestJSONToBoxParsesAsBox(t *testing.T) {
	txt, err := host.JSONToBox(json.RawMessage(`{"hp":10,"name":"x y","ok":true,"n":null,"tags":["a",1,{"z":2}],"nested":{"a":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.ParseValue(txt); err != nil {
		t.Fatalf("JSONToBox output is not valid Box grammar: %v\n%s", err, txt)
	}
}

func TestEngineReplicationLeaderToFollowers(t *testing.T) {
	leader := start(t, nil)
	f := start(t, func(c *host.Config, _ string) {
		c.Role = "follower"
		c.Peers = []string{leader.url}
		c.NodeID = "eng-f1"
	})
	f.key = leader.key
	P := "/v1/path/"
	do(t, "PUT", leader.url+P+"accounts.red-xai.db:AccountVault[ID=1].Name", leader.key, `"Zed"`)
	do(t, "PUT", leader.url+P+"world.red-xai-db-qs-ls:Hot.Counter", leader.key, `41`)
	do(t, "PUT", leader.url+P+"sessions.red-xai-db-qs:Q.Otp?ttl=1", leader.key, `"1234"`)
	for _, path := range []string{"accounts.red-xai.db:AccountVault[ID=1].Name", "world.red-xai-db-qs-ls:Hot.Counter", "sessions.red-xai-db-qs:Q.Otp"} {
		path := path
		eventually(t, "replicated "+path, 6*time.Second, func() bool {
			st, _ := do(t, "GET", f.url+P+path, leader.key, "")
			return st == 200
		})
	}
	// follower engine rejects direct client writes at the engine level (host forwards instead)
	if st, _ := do(t, "PUT", f.url+P+"accounts.red-xai.db:AccountVault[ID=1].Name", leader.key, `"Direct"`); st != 200 {
		t.Fatalf("write via follower is forwarded to the leader, got %d", st)
	}
	eventually(t, "forwarded write visible on leader", 3*time.Second, func() bool {
		_, b := do(t, "GET", leader.url+P+"accounts.red-xai.db:AccountVault[ID=1].Name", leader.key, "")
		return strings.Contains(b, "Direct")
	})
	// TTL: the LEADER's engine expires the otp and replicates an explicit expire delete
	eventually(t, "otp expired on leader", 5*time.Second, func() bool {
		_, b := do(t, "GET", leader.url+P+"sessions.red-xai-db-qs:Q.Otp", leader.key, "")
		return !strings.Contains(b, "1234")
	})
	eventually(t, "expiry replicated to follower", 5*time.Second, func() bool {
		_, b := do(t, "GET", f.url+P+"sessions.red-xai-db-qs:Q.Otp", leader.key, "")
		return !strings.Contains(b, "1234")
	})
	// replicated value on follower matches
	_, b := do(t, "GET", f.url+P+"world.red-xai-db-qs-ls:Hot.Counter", leader.key, "")
	if !strings.Contains(b, "41") {
		t.Fatalf("follower value: %s", b)
	}
}

func TestEngineSnapshotFallbackAndPromotion(t *testing.T) {
	leader := start(t, func(c *host.Config, _ string) { c.ReplLogMax = 3 })
	P := "/v1/path/"
	for i := 0; i < 20; i++ {
		do(t, "PUT", leader.url+P+"d.red-xai.db:B.K"+string(rune('a'+i)), leader.key, `1`)
	}
	time.Sleep(700 * time.Millisecond) // let the log compact so the late follower needs a snapshot
	f := start(t, func(c *host.Config, _ string) {
		c.Role = "follower"
		c.Peers = []string{leader.url}
		c.NodeID = "eng-late"
	})
	f.key = leader.key
	eventually(t, "snapshot-installed follower has data", 8*time.Second, func() bool {
		st, _ := do(t, "GET", f.url+P+"d.red-xai.db:B.Ka", leader.key, "")
		return st == 200
	})
	if _, b := do(t, "GET", f.url+"/v1/cluster/status", leader.key, ""); !strings.Contains(b, `"snapshots_installed":1`) {
		t.Fatalf("expected a full snapshot install, status: %s", b)
	}
	do(t, "PUT", leader.url+P+"d.red-xai.db:B.After", leader.key, `2`)
	eventually(t, "tail after snapshot", 5*time.Second, func() bool {
		st, _ := do(t, "GET", f.url+P+"d.red-xai.db:B.After", leader.key, "")
		return st == 200
	})
	// promotion: follower becomes leader and accepts engine writes
	if st, b := do(t, "POST", f.url+"/v1/cluster/promote", leader.key, ""); st != 200 {
		t.Fatalf("promote %d %s", st, b)
	}
	if st, b := do(t, "PUT", f.url+P+"d.red-xai.db:B.NewLeader", leader.key, `3`); st != 200 {
		t.Fatalf("write on promoted node %d %s", st, b)
	}
}
