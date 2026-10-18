package host_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"redx/host"
)

func joinToken(t *testing.T, leader *node) string {
	t.Helper()
	st, b, _ := do(t, "POST", leader.URL+"/v1/cluster/join-token", leader.Key, `{"ttl_seconds":60}`)
	if st != 200 {
		t.Fatalf("join-token %d %s", st, b)
	}
	return jsonOf(t, b)["token"].(string)
}

func joinOffline(t *testing.T, leader *node, dir string, nodeID string, advertise string) {
	t.Helper()
	tok := joinToken(t, leader)
	jr, err := host.JoinLeader(context.Background(), leader.Srv.Client(), leader.URL, tok, nodeID, advertise)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.WriteJoinedState(dir, leader.URL, jr.NodeSecret, "", advertise); err != nil {
		t.Fatal(err)
	}
}

func newFollower(t *testing.T, leader *node, id string) *node {
	t.Helper()
	dir := t.TempDir()
	joinOffline(t, leader, dir, id, "")
	return startNode(t, dir, func(c *host.Config, url string) { c.NodeID = id; c.Role = "follower" })
}

func status(t *testing.T, n *node) host.ClusterStatus {
	t.Helper()
	st, b, _ := do(t, "GET", n.URL+"/v1/cluster/status", n.Key, "")
	if st != 200 {
		t.Fatalf("status %d %s", st, b)
	}
	var cs host.ClusterStatus
	json.Unmarshal([]byte(b), &cs)
	return cs
}

func TestThreeNodeCluster(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	f1 := newFollower(t, leader, "f1")
	f2 := newFollower(t, leader, "f2")
	// the bootstrap admin key replicates (keys are in the log) so followers authenticate it
	for _, f := range []*node{f1, f2} {
		f.Key = leader.Key
	}
	eventually(t, "followers authenticate leader's key", 5*time.Second, func() bool {
		a, _, _ := do(t, "GET", f1.URL+"/healthz", "", "")
		s1, _, _ := do(t, "GET", f1.URL+"/v1/stats", leader.Key, "")
		s2, _, _ := do(t, "GET", f2.URL+"/v1/stats", leader.Key, "")
		return a == 200 && s1 == 200 && s2 == 200
	})

	// write on leader => visible on followers
	st, b, h := do(t, "PUT", leader.URL+P+"acc.red-xai.db:Box[ID=1].Name", leader.Key, `"Zed"`)
	if st != 200 {
		t.Fatalf("leader put %d %s", st, b)
	}
	seq := h.Get("X-Redx-Seq")
	for _, f := range []*node{f1, f2} {
		eventually(t, "replicated to "+f.URL, 5*time.Second, func() bool {
			st, b, _ := do(t, "GET", f.URL+P+"acc.red-xai.db:Box[ID=1].Name", leader.Key, "")
			return st == 200 && strings.Contains(b, "Zed")
		})
	}
	// write via follower is forwarded to the leader; read-your-writes via X-Redx-Wait-Seq
	st, b, h = do(t, "PUT", f1.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, "77")
	if st != 200 {
		t.Fatalf("forwarded put %d %s", st, b)
	}
	if h.Get("X-Redx-Seq") == "" {
		t.Fatal("expected seq from leader")
	}
	st, b, _ = do(t, "GET", f2.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, "", "X-Redx-Wait-Seq", h.Get("X-Redx-Seq"))
	if st != 200 || !strings.Contains(b, "77") {
		t.Fatalf("wait-seq read %d %s", st, b)
	}
	// the write really landed on the leader first
	if st, b, _ := do(t, "GET", leader.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, ""); st != 200 || !strings.Contains(b, "77") {
		t.Fatalf("leader missing forwarded write: %d %s", st, b)
	}
	_ = seq
	// delete via follower
	if st, _, _ := do(t, "DELETE", f2.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, ""); st != 200 {
		t.Fatal("forwarded delete")
	}
	eventually(t, "delete replicated", 5*time.Second, func() bool {
		a, _, _ := do(t, "GET", f1.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, "")
		b2, _, _ := do(t, "GET", leader.URL+P+"acc.red-xai.db:Box[ID=1].Hp", leader.Key, "")
		return a == 404 && b2 == 404
	})
	// new database replicates
	do(t, "POST", f1.URL+"/v1/dbs", leader.Key, `{"name":"extra"}`)
	eventually(t, "db replicated", 5*time.Second, func() bool {
		st, _, _ := do(t, "PUT", f2.URL+"/v1/db/extra/path/a:b", leader.Key, "1")
		return st == 200
	})

	// key lifecycle replicates: create on leader (via follower), use on follower, revoke => 401 on follower
	k, id := mkKeyVia(t, f1, leader.Key, map[string]interface{}{"scope": "read", "db": "default"})
	eventually(t, "new key valid on f2", 5*time.Second, func() bool {
		st, _, _ := do(t, "GET", f2.URL+P+"acc.red-xai.db:Box[ID=1].Name", k, "")
		return st == 200
	})
	if st, _, _ := do(t, "DELETE", leader.URL+"/v1/keys/"+id, leader.Key, ""); st != 200 {
		t.Fatal("revoke")
	}
	eventually(t, "revocation reaches follower", 5*time.Second, func() bool {
		st, _, _ := do(t, "GET", f2.URL+P+"acc.red-xai.db:Box[ID=1].Name", k, "")
		return st == 401
	})

	// membership visible on leader with ack/lag
	eventually(t, "members healthy and caught up", 5*time.Second, func() bool {
		cs := status(t, leader)
		if cs.Role != "leader" || len(cs.Members) != 3 {
			return false
		}
		for _, m := range cs.Members {
			if !m.Healthy || m.Lag > 1 {
				return false
			}
		}
		return true
	})
	if cs := status(t, f1); cs.Role != "follower" || cs.LeaderURL != leader.URL {
		t.Fatalf("follower status %+v", cs)
	}
	// sync-ish ack: wait for 2 replicas
	_, _, h = do(t, "PUT", leader.URL+P+"acc.red-xai.db:Box[ID=2].X", leader.Key, "1", "X-Redx-Wait-Replicas", "2")
	if h.Get("X-Redx-Replicas-Acked") != "2" {
		t.Fatalf("wanted 2 replicas acked, header=%q", h.Get("X-Redx-Replicas-Acked"))
	}
}

func mkKeyVia(t *testing.T, via *node, adminKey string, req map[string]interface{}) (string, string) {
	t.Helper()
	b, _ := json.Marshal(req)
	st, body, _ := do(t, "POST", via.URL+"/v1/keys", adminKey, string(b), "Content-Type", "application/json")
	if st != 201 {
		t.Fatalf("create key via %s: %d %s", via.URL, st, body)
	}
	m := jsonOf(t, body)
	return m["key"].(string), m["id"].(string)
}

func TestJoinTokenSingleUseAndExpiry(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	tok := joinToken(t, leader)
	if _, err := host.JoinLeader(context.Background(), leader.Srv.Client(), leader.URL, tok, "a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := host.JoinLeader(context.Background(), leader.Srv.Client(), leader.URL, tok, "b", ""); err == nil {
		t.Fatal("join token must be single use")
	}
	if _, err := host.JoinLeader(context.Background(), leader.Srv.Client(), leader.URL, "rxj_nope", "c", ""); err == nil {
		t.Fatal("bogus token")
	}
	// non-admin cannot mint tokens
	rd, _ := mkKey(t, leader, map[string]interface{}{"scope": "write", "db": "*"})
	if st, _, _ := do(t, "POST", leader.URL+"/v1/cluster/join-token", rd, ""); st != 403 {
		t.Fatalf("join-token needs host admin, got %d", st)
	}
	// expiry (1 second TTL)
	_, b, _ := do(t, "POST", leader.URL+"/v1/cluster/join-token", leader.Key, `{"ttl_seconds":1}`)
	short := jsonOf(t, b)["token"].(string)
	time.Sleep(1200 * time.Millisecond)
	if _, err := host.JoinLeader(context.Background(), leader.Srv.Client(), leader.URL, short, "d", ""); err == nil {
		t.Fatal("expired token must be refused")
	}
	// node endpoints reject API keys and anonymous callers
	for _, p := range []string{"/v1/cluster/log?from=1", "/v1/cluster/snapshot"} {
		if st, _, _ := do(t, "GET", leader.URL+p, leader.Key, "", "X-Redx-Node", "x"); st != 401 {
			t.Fatalf("%s with API key must be 401, got %d", p, st)
		}
		if st, _, _ := do(t, "GET", leader.URL+p, "", ""); st != 401 {
			t.Fatalf("%s anonymous must be 401", p)
		}
	}
}

func TestTTLExpiryIsLeaderDriven(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	f := newFollower(t, leader, "f1")
	do(t, "PUT", leader.URL+P+"s:o.otp?ttl=1", leader.Key, `"1234"`)
	eventually(t, "replicated", 5*time.Second, func() bool {
		return len(f.opts()) > 0
	})
	for _, o := range f.opts() {
		if o.TTLSeconds != 0 || o.Sliding {
			t.Fatalf("follower must store replicated keys WITHOUT a local TTL, got %+v", o)
		}
	}
	// leader's explicit delete (reason=expire) is replicated and removes the key on the follower
	eventually(t, "leader expiry replicated to follower", 6*time.Second, func() bool {
		st, _, _ := do(t, "GET", f.URL+P+"s:o.otp", leader.Key, "")
		return st == 404
	})
	cs := status(t, leader)
	if cs.TTLTracked != 0 {
		t.Fatalf("tracker should be empty, %d", cs.TTLTracked)
	}
	// the delete is in the log with reason=expire
	raw, _ := os.ReadFile(filepath.Join(leader.Dir, "cluster", "oplog.jsonl"))
	if !strings.Contains(string(raw), `"reason":"expire"`) {
		t.Fatal("expiry delete not logged")
	}
}

func TestSnapshotFallbackWhenLogTruncated(t *testing.T) {
	leader := startNode(t, t.TempDir(), func(c *host.Config, _ string) { c.ReplLogMax = 3 })
	for i := 0; i < 20; i++ { // compaction at 2*max entries => early history is gone
		do(t, "PUT", leader.URL+P+fmt.Sprintf("d:B.K%d", i), leader.Key, fmt.Sprint(i))
	}
	eventually(t, "log compaction", 3*time.Second, func() bool { return status(t, leader).FirstOffset > 1 })
	f := newFollower(t, leader, "late")
	f.Key = leader.Key
	eventually(t, "snapshot installed and caught up", 8*time.Second, func() bool {
		st, b, _ := do(t, "GET", f.URL+P+"d:B.K0", leader.Key, "")
		if st != 200 || !strings.Contains(b, "0") {
			return false
		}
		st, b, _ = do(t, "GET", f.URL+"/v1/cluster/status", leader.Key, "")
		return st == 200 && strings.Contains(b, `"snapshots_installed":1`)
	})
	// after the snapshot, tailing resumes from the offset (no further snapshots)
	do(t, "PUT", leader.URL+P+"d:B.After", leader.Key, "9")
	eventually(t, "incremental after snapshot", 5*time.Second, func() bool {
		st, _, _ := do(t, "GET", f.URL+P+"d:B.After", leader.Key, "")
		return st == 200
	})
	if cs := status(t, f); cs.Snapshots != 1 || cs.ReplID != status(t, leader).ReplID {
		t.Fatalf("follower status %+v", cs)
	}
}

func TestFollowerRestartResumesByOffset(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	dir := t.TempDir()
	joinOffline(t, leader, dir, "f1", "")
	start := func() *node {
		return startNode(t, dir, func(c *host.Config, u string) { c.NodeID = "f1"; c.Role = "follower" })
	}
	f := start()
	f.Key = leader.Key
	do(t, "PUT", leader.URL+P+"r:B.A", leader.Key, "1")
	eventually(t, "A", 5*time.Second, func() bool { st, _, _ := do(t, "GET", f.URL+P+"r:B.A", leader.Key, ""); return st == 200 })
	off := status(t, f).Offset
	f.Stop()
	do(t, "PUT", leader.URL+P+"r:B.B", leader.Key, "2") // while follower is down
	f = start()
	f.Key = leader.Key
	eventually(t, "B after restart", 5*time.Second, func() bool { st, _, _ := do(t, "GET", f.URL+P+"r:B.B", leader.Key, ""); return st == 200 })
	cs := status(t, f)
	if cs.Snapshots != 0 || cs.Offset <= off {
		t.Fatalf("restart must resume by offset, not snapshot: %+v (was %d)", cs, off)
	}
}

func TestPromoteFailoverPartialResyncAndLostWrites(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	f1 := newFollower(t, leader, "f1")
	f2 := newFollower(t, leader, "f2")
	f1.Key, f2.Key = leader.Key, leader.Key
	do(t, "PUT", leader.URL+P+"p:B.A", leader.Key, "1")
	for _, f := range []*node{f1, f2} {
		f := f
		eventually(t, "A on follower", 5*time.Second, func() bool { st, _, _ := do(t, "GET", f.URL+P+"p:B.A", leader.Key, ""); return st == 200 })
	}
	oldID := status(t, leader).ReplID
	// leader dies
	leader.Stop()

	// writes to a follower fail while there's no leader
	if st, _, _ := do(t, "PUT", f1.URL+P+"p:B.X", f1.Key, "1"); st != 502 {
		t.Fatalf("forwarded write to dead leader should be 502, got %d", st)
	}
	// promote f1
	st, b, _ := do(t, "POST", f1.URL+"/v1/cluster/promote", f1.Key, "")
	if st != 200 {
		t.Fatalf("promote %d %s", st, b)
	}
	cs := status(t, f1)
	if cs.Role != "leader" || cs.ReplID == oldID || cs.ParentID != oldID {
		t.Fatalf("after promote: %+v", cs)
	}
	if st, _, _ := do(t, "PUT", f1.URL+P+"p:B.New", f1.Key, "2"); st != 200 {
		t.Fatal("new leader accepts writes")
	}
	// repoint f2 using a join token from the new leader
	tok := joinToken(t, f1)
	body := fmt.Sprintf(`{"url":%q,"join_token":%q}`, f1.URL, tok)
	if st, b, _ := do(t, "POST", f2.URL+"/v1/cluster/leader", f1.Key, body); st != 200 {
		t.Fatalf("repoint %d %s", st, b)
	}
	eventually(t, "f2 follows new leader (partial resync)", 8*time.Second, func() bool {
		st, _, _ := do(t, "GET", f2.URL+P+"p:B.New", f1.Key, "")
		return st == 200
	})
	if cs := status(t, f2); cs.Snapshots != 0 || cs.ReplID != status(t, f1).ReplID {
		t.Fatalf("f2 should have resumed by offset under the new replid: %+v", cs)
	}
}

func TestDivergedOldLeaderGetsSnapshotAndLosesUnreplicatedWrites(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	f1 := newFollower(t, leader, "f1")
	f1.Key = leader.Key
	do(t, "PUT", leader.URL+P+"v:B.Shared", leader.Key, "1")
	eventually(t, "shared on f1", 5*time.Second, func() bool { st, _, _ := do(t, "GET", f1.URL+P+"v:B.Shared", leader.Key, ""); return st == 200 })
	// Partition-ish: f1 is promoted while the old leader is still alive and accepts an "acked" write
	// that f1 can never see.
	if st, _, _ := do(t, "POST", f1.URL+"/v1/cluster/promote", f1.Key, ""); st != 200 {
		t.Fatal("promote")
	}
	f1.H.Cluster.IsLeader() // keep import used
	lostSt, _, _ := do(t, "PUT", leader.URL+P+"v:B.Lost", leader.Key, "x")
	if lostSt != 200 {
		t.Fatal("old leader still acks writes (split brain) -- this is the documented hazard")
	}
	tok := joinToken(t, f1)
	body := fmt.Sprintf(`{"url":%q,"join_token":%q}`, f1.URL, tok)
	if st, b, _ := do(t, "POST", leader.URL+"/v1/cluster/leader", leader.Key, body); st != 200 {
		t.Fatalf("demote old leader %d %s", st, b)
	}
	eventually(t, "old leader resynced via snapshot, acked write discarded", 8*time.Second, func() bool {
		st, _, _ := do(t, "GET", leader.URL+P+"v:B.Lost", f1.Key, "")
		cs := status(t, leader)
		return st == 404 && cs.Snapshots == 1 && cs.Role == "follower"
	})
	if st, _, _ := do(t, "GET", leader.URL+P+"v:B.Shared", f1.Key, ""); st != 200 {
		t.Fatal("shared data survives")
	}
}

func TestStaticClusterSecretFollower(t *testing.T) {
	leader := startNode(t, t.TempDir(), func(c *host.Config, _ string) { c.ClusterKey = "s3cret-shared-value-for-tests" })
	f := startNode(t, t.TempDir(), func(c *host.Config, _ string) {
		c.ClusterKey = "s3cret-shared-value-for-tests"
		c.Role = "follower"
		c.Peers = []string{leader.URL}
		c.NodeID = "static1"
	})
	do(t, "PUT", leader.URL+P+"z:B.K", leader.Key, "1")
	eventually(t, "static-secret follower replicates", 6*time.Second, func() bool {
		st, _, _ := do(t, "GET", f.URL+P+"z:B.K", leader.Key, "")
		return st == 200
	})
	// wrong secret is refused
	g := startNode(t, t.TempDir(), func(c *host.Config, _ string) {
		c.ClusterKey = "wrong"
		c.Role = "follower"
		c.Peers = []string{leader.URL}
		c.NodeID = "evil"
	})
	time.Sleep(1500 * time.Millisecond)
	if st, _, _ := do(t, "GET", g.URL+P+"z:B.K", leader.Key, ""); st == 200 {
		t.Fatal("follower with wrong secret must not replicate")
	}
}

func TestOfflineBackupRestore(t *testing.T) {
	dir := t.TempDir()
	n := startNode(t, dir, nil)
	do(t, "PUT", n.URL+P+"b:B.K", n.Key, `"keep"`)
	key, _ := mkKey(t, n, map[string]interface{}{"scope": "read", "db": "default"})
	st, _, _ := do(t, "GET", n.URL+"/v1/admin/backup", n.Key, "")
	if st != 200 {
		t.Fatalf("online backup %d", st)
	}
	var buf strings.Builder
	_ = buf
	// offline path requires the daemon to be stopped (or force)
	f, _ := os.CreateTemp(t.TempDir(), "bk")
	if err := host.OfflineBackup(dir, f, "", false); err == nil {
		t.Fatal("offline backup must refuse while daemon runs")
	}
	n.Stop()
	f.Seek(0, 0)
	if err := host.OfflineBackup(dir, f, "", false); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)
	dir2 := t.TempDir()
	if _, err := host.OfflineRestore(dir2, f, "", false); err != nil {
		t.Fatal(err)
	}
	n2 := startNode(t, dir2, nil)
	if n2.Key != "" {
		t.Fatal("restored keystore must not bootstrap a new admin key")
	}
	if st, b, _ := do(t, "GET", n2.URL+P+"b:B.K", key, ""); st != 200 || !strings.Contains(b, "keep") {
		t.Fatalf("restored data/key: %d %s", st, b)
	}
}

func TestOfflineKeyCreatedWhileRunningReplicates(t *testing.T) {
	leader := startNode(t, t.TempDir(), nil)
	f := newFollower(t, leader, "f1")
	// what `redx key create --offline` does: a separate process opens the keystore file directly
	ks, err := host.OpenKeystore(filepath.Join(leader.Dir, "keys.red-xai-keys"), host.KeystoreOptions{ArgonMem: 64, ArgonTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := ks.Create(host.CreateSpec{DB: "*", Scope: "read"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "leader notices offline key (periodic poll)", 4*time.Second, func() bool { return leader.H.KS.Count() == 2 })
	if st, _, _ := do(t, "GET", leader.URL+P+"nothing:here", key, ""); st != 404 {
		t.Fatalf("leader should accept the offline-created key, got %d", st)
	}
	eventually(t, "offline key replicated to follower", 5*time.Second, func() bool {
		st, _, _ := do(t, "GET", f.URL+P+"nothing:here", key, "")
		time.Sleep(100 * time.Millisecond)
		return st == 404 // authenticated (404 = not 401)
	})
}

func TestOfflineBootstrapKeyBeforeFirstStartIsReplicated(t *testing.T) {
	dir := t.TempDir()
	ks, _ := host.OpenKeystore(filepath.Join(dir, "keys.red-xai-keys"), host.KeystoreOptions{ArgonMem: 64, ArgonTime: 1})
	key, _, _ := ks.Create(host.CreateSpec{DB: "*", Scope: "admin", Label: "installer"})
	leader := startNode(t, dir, nil)
	if leader.Key != "" {
		t.Fatal("must not bootstrap a second admin key")
	}
	leader.Key = key
	f := newFollower(t, leader, "f1")
	eventually(t, "installer key valid on follower", 5*time.Second, func() bool {
		st, _, _ := do(t, "GET", f.URL+P+"nothing:here", key, "")
		return st == 404
	})
}
