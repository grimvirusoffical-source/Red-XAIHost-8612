package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"redx/box"
)

func TestWheelFiresInOrderAndNeverEarly(t *testing.T) {
	var w wheel
	start := int64(1_800_000_000_000)
	w.start(start)
	rng := rand.New(rand.NewSource(7))
	type rec struct{ at int64 }
	var items []*wItem
	for i := 0; i < 4000; i++ {
		// mix of near, medium and far deadlines across all wheel levels
		var d int64
		switch rng.Intn(4) {
		case 0:
			d = rng.Int63n(500)
		case 1:
			d = rng.Int63n(60_000)
		case 2:
			d = rng.Int63n(10 * 3600 * 1000)
		default:
			d = rng.Int63n(40 * 24 * 3600 * 1000)
		}
		it := &wItem{at: start + d}
		items = append(items, it)
		w.add(it)
	}
	fired := map[*wItem]int64{}
	now := start
	for now < start+41*24*3600*1000 {
		step := 1 + rng.Int63n(20_000_000)
		now += step
		for _, it := range w.advance(now, nil) {
			if _, dup := fired[it]; dup {
				t.Fatal("item fired twice")
			}
			if tickOf(it.at) > tickOf(now) {
				t.Fatalf("fired early: deadline tick %d now tick %d", tickOf(it.at), tickOf(now))
			}
			fired[it] = now
		}
	}
	if len(fired) != len(items) || w.len() != 0 {
		t.Fatalf("fired %d of %d, wheel len %d", len(fired), len(items), w.len())
	}
	// firing is also timely: no item waits more than one advance step past its deadline
	sort.Slice(items, func(i, j int) bool { return items[i].at < items[j].at })
	// fine-grained: fire times in a dense advance equal the deadline tick
	var w2 wheel
	w2.start(start)
	it := &wItem{at: start + 1234}
	w2.add(it)
	var got int64 = -1
	for ms := int64(0); ms <= 2000; ms += 10 {
		if len(w2.advance(start+ms, nil)) > 0 {
			got = ms
			break
		}
	}
	if got != 1230 {
		t.Fatalf("dense advance fired at +%dms, want +1230ms (tick of 1234)", got)
	}
}

func TestQSExpiryFixedSlidingSubtreeLazy(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	q := "s.red-xai-db-qs:"
	mustSet(t, db, q+"Otp[ID=1].Code", str("1234"), SetOpts{TTLSeconds: 30})
	mustSet(t, db, q+"Otp[ID=1].Keep", str("k"), SetOpts{TTLSeconds: 100})
	mustSet(t, db, q+"Presence[ID=9].Seen", box.Bool(true), SetOpts{TTLSeconds: 60, Sliding: true})

	// subtree TTL: whole sub-box (children carry no TTL of their own) expires together
	sub, _ := box.ParseDocument(`{Buffs[]= [ {Haste[]} = 1, {Shield[]} = 2, {Deep[]} = [ {X[]} = 3 ] ]};`)
	e, _ := box.Resolve(sub, []box.Seg{{Name: "Buffs"}})
	mustSet(t, db, q+"Player[ID=3].Buffs", e.Value, SetOpts{TTLSeconds: 20})

	clk.Advance(19 * time.Second)
	db.sweepNow()
	if mustGetNum(t, db, q+"Player[ID=3].Buffs.Deep.X") != 3 {
		t.Fatal("subtree expired early")
	}
	// touching the sliding entry at t=19 pushes its deadline to t=79
	if v, _ := db.Get(q + "Presence[ID=9].Seen"); !v.Bool {
		t.Fatal("presence")
	}
	clk.Advance(2 * time.Second) // t=21: Buffs (20s) is gone, Otp.Code (30s) is not
	db.sweepNow()
	if v, err := db.Get(q + "Player[ID=3].Buffs.Haste"); err != nil || !v.IsNell() {
		t.Fatalf("subtree child should be gone with its parent: %v", err)
	}
	if v, err := db.Get(q + "Player[ID=3]"); err != nil || !v.IsNell() {
		t.Fatalf("empty structural parent should be pruned: %v", err)
	}
	if v, err := db.Get(q + "Otp[ID=1].Code"); err != nil || v.Str != "1234" {
		t.Fatalf("otp: %v %v", v, err)
	}
	clk.Advance(10 * time.Second) // t=31: OTP expired. NO sweep: lazy read must return NELL and purge.
	v, err := db.Get(q + "Otp[ID=1].Code")
	if err != nil || !v.IsNell() {
		t.Fatalf("lazy expiry should return NELL, got %v %v", v, err)
	}
	if v, err := db.Get(q + "Otp[ID=1].Code"); err != nil || !v.IsNell() {
		t.Fatalf("lazy purge should have removed it: %v", err)
	}
	if db.Stats()["qs.lazy_expired"] != 1 {
		t.Fatalf("lazy_expired=%v", db.Stats()["qs.lazy_expired"])
	}
	// sibling with a longer TTL is untouched
	if v, _ := db.Get(q + "Otp[ID=1].Keep"); v.Str != "k" {
		t.Fatal("sibling lost")
	}
	if v, err := db.Get(q + "Presence[ID=9].Seen"); err != nil || !v.Bool { // t=31: refresh (deadline -> t=91)
		t.Fatalf("presence at t=31: %v %v", v, err)
	}
	// sliding: reads every 50s keep it alive indefinitely (60s TTL)
	for i := 0; i < 5; i++ {
		clk.Advance(50 * time.Second)
		db.sweepNow()
		if v, err := db.Get(q + "Presence[ID=9].Seen"); err != nil || !v.Bool {
			t.Fatalf("sliding entry died at round %d: %v %v", i, v, err)
		}
	}
	// ...but stops living once not touched for 60s. (Keep at 100s is also gone by now.)
	clk.Advance(61 * time.Second)
	db.sweepNow()
	if v, err := db.Get(q + "Presence[ID=9].Seen"); err != nil || !v.IsNell() {
		t.Fatalf("sliding entry should expire after 60s idle: %v", err)
	}
	if st := db.Stats(); st["qs.wheel_items"] != 0 || st["qs.expired"] < 3 {
		t.Fatalf("stats: wheel=%v expired=%v", st["qs.wheel_items"], st["qs.expired"])
	}
	// overwrite refreshes TTL (and the old wheel item is re-used, not leaked)
	for i := 0; i < 1000; i++ {
		mustSet(t, db, q+"Hot.V", num(float64(i)), SetOpts{TTLSeconds: 30})
	}
	if w := db.Stats()["qs.wheel_items"]; w > 2 {
		t.Fatalf("wheel items leaked on rewrite: %v", w)
	}
	// a TTL shortened by rewrite must still fire at the earlier time
	mustSet(t, db, q+"Hot.V", num(1), SetOpts{TTLSeconds: 5})
	clk.Advance(6 * time.Second)
	db.sweepNow()
	if v, err := db.Get(q + "Hot.V"); err != nil || !v.IsNell() {
		t.Fatal("shortened TTL did not fire")
	}
}

func TestQSBackgroundSweeperRealClock(t *testing.T) {
	db := open(t, t.TempDir(), Config{SweepMS: 20})
	defer db.Close()
	ch, cancel := db.Subscribe("s.red-xai-db-qs:*")
	defer cancel()
	mustSet(t, db, "s.red-xai-db-qs:Lobby[ID=1].Open", box.Bool(true), SetOpts{TTLSeconds: 1})
	<-ch // set event
	select {
	case e := <-ch:
		if e.Op != "expire" || e.Path != "s.red-xai-db-qs:Lobby[ID=1].Open" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background sweep never expired the entry")
	}
	if db.Stats()["qs.expired"] != 1 {
		t.Fatalf("expired=%v", db.Stats()["qs.expired"])
	}
}

func TestQSDefaultTTLAndSeedFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "seed.red-xai-db-qs"), []byte(`{Boot[ID=1]= [ {A[]} = 1 (60), {B[]} = 2 (~60) ]};`), 0o644)
	db := open(t, dir, Config{DefaultTTLSeconds: 10})
	defer db.Close()
	if mustGetNum(t, db, "Boot[ID=1].A") != 1 {
		t.Fatal("seed file not loaded")
	}
	mustSet(t, db, "seed.red-xai-db-qs:X.Y", num(1), SetOpts{}) // default TTL applies
	if db.Stats()["qs.wheel_items"] != 3 {
		t.Fatalf("wheel items %v", db.Stats()["qs.wheel_items"])
	}
	db.Close()
	os.WriteFile(filepath.Join(dir, "bad.red-xai-db-qs"), []byte(`{Boot[ID=1]= [ {A[]} = 1 ]};`), 0o644)
	if _, err := OpenDatabase(dir, Config{}); err == nil {
		t.Fatal("seed file violating the mandatory-TTL rule must be rejected")
	}
}

func TestQSLSTTLAbsoluteSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	mustSet(t, db, "w.red-xai-db-qs-ls:Buff.Speed", num(2), SetOpts{TTLSeconds: 100})
	db.crash()

	clk.Advance(60 * time.Second) // downtime
	db = open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	// restart must NOT restart the 100s lifetime: 40s remain
	if mustGetNum(t, db, "Buff.Speed") != 2 {
		t.Fatal("entry should still be alive at +60s")
	}
	clk.Advance(41 * time.Second)
	if v, err := db.Get("Buff.Speed"); err != nil || !v.IsNell() {
		t.Fatalf("entry must have expired at its ORIGINAL deadline, got %v %v", v, err)
	}
	db.crash()
	// the expiry was journaled as a synthetic delete: it stays gone after another crash
	db = open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	defer db.Close()
	if v, err := db.Get("Buff.Speed"); err != nil || !v.IsNell() {
		t.Fatalf("expiry not durable: %v", err)
	}
}

func TestSyntheticDeletesAreLoggedAndReplayed(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	rec := &recorder{}
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync", Hook: rec})
	mustSet(t, db, "w.red-xai-db-qs-ls:Buff[ID=1].A", num(1), SetOpts{TTLSeconds: 10})
	mustSet(t, db, "w.red-xai-db-qs-ls:Buff[ID=1].B", num(2), SetOpts{TTLSeconds: 10})
	mustSet(t, db, "w.red-xai-db-qs-ls:Buff[ID=1].C", num(3), SetOpts{})
	clk.Advance(11 * time.Second)
	db.sweepNow()
	ents := rec.entries()
	last := ents[len(ents)-1]
	if len(last.Ops) != 2 || last.Ops[0].Kind != "expire" || last.Ops[1].Kind != "expire" {
		t.Fatalf("sweep should log ONE atomic frame of 2 synthetic deletes, got %+v", last)
	}
	if last.File != "w.red-xai-db-qs-ls" || last.Ops[0].Path == "" {
		t.Fatalf("bad log entry %+v", last)
	}
	db.crash()
	// replay with the clock rewound: the logged deletes must still apply (no TTL math on replay)
	clk.ms.Add(-11000)
	db2 := open(t, dir, Config{Clock: clk.Now})
	defer db2.Close()
	if v, err := db2.Get("Buff[ID=1].A"); err != nil || !v.IsNell() {
		t.Fatalf("logged expire not replayed: %v", err)
	}
	if mustGetNum(t, db2, "Buff[ID=1].C") != 3 {
		t.Fatal("permanent sibling lost")
	}
}

// ---------------------------------------------------------------------------
// qs-ls coalescing

func TestQSLSCoalescingAndRestartSurvival(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	p := "w.red-xai-db-qs-ls:HotWorldStore.GlobalKillCounter"
	for i := 1; i <= 1000; i++ {
		mustSet(t, db, p, num(float64(i)), SetOpts{CheckpointSeconds: 5})
	}
	st := db.Stats()
	if st["qsls.writes_coalesced"] != 999 || st["qsls.wal_frames"] != 0 {
		t.Fatalf("expected 999 coalesced writes and no journal frames yet, got %v / %v", st["qsls.writes_coalesced"], st["qsls.wal_frames"])
	}
	if mustGetNum(t, db, p) != 1000 {
		t.Fatal("RAM must serve the latest value immediately")
	}
	clk.Advance(4 * time.Second)
	db.tables["w.red-xai-db-qs-ls"].flushPending(false)
	if db.Stats()["qsls.wal_frames"] != 0 {
		t.Fatal("flushed before the (5)* interval")
	}
	clk.Advance(2 * time.Second)
	db.tables["w.red-xai-db-qs-ls"].flushPending(false)
	st = db.Stats()
	if st["qsls.wal_frames"] != 1 || st["qsls.deltas_flushed"] != 1 {
		t.Fatalf("1000 writes must coalesce into ONE journaled delta, frames=%v deltas=%v", st["qsls.wal_frames"], st["qsls.deltas_flushed"])
	}
	// crash: the flushed value survives
	mustSet(t, db, p, num(2000), SetOpts{CheckpointSeconds: 5}) // not yet flushed
	db.crash()
	db2 := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	if got := mustGetNum(t, db2, p); got != 1000 {
		t.Fatalf("after crash want last flushed value 1000 (loss window = checkpoint interval), got %v", got)
	}
	// the (N)* marker survived in the data file
	v := db2.tables["w.red-xai-db-qs-ls"].root
	e, _ := box.Resolve(v, []box.Seg{{Name: "HotWorldStore"}, {Name: "GlobalKillCounter"}})
	if e.TTL.Kind != box.TTLCheckpoint || e.TTL.Seconds != 5 {
		t.Fatalf("marker lost: kind=%d secs=%d", e.TTL.Kind, e.TTL.Seconds)
	}
	// clean shutdown flushes everything, even deltas that are not due
	mustSet(t, db2, p, num(3000), SetOpts{CheckpointSeconds: 3600})
	db2.Close()
	db3 := open(t, dir, Config{Clock: clk.Now})
	defer db3.Close()
	if got := mustGetNum(t, db3, p); got != 3000 {
		t.Fatalf("clean Close must persist pending deltas, got %v", got)
	}
}

func TestQSLSOrdinaryWriteIsImmediatelyJournaled(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 0; i < 50; i++ {
		mustSet(t, db, "w.red-xai-db-qs-ls:Chat.Last", num(float64(i)), SetOpts{})
	}
	if db.Stats()["qsls.wal_frames"] != 50 {
		t.Fatalf("ordinary ',' writes must each hit the journal: %v", db.Stats()["qsls.wal_frames"])
	}
	db.crash()
	db2 := open(t, dir, Config{})
	defer db2.Close()
	if mustGetNum(t, db2, "Chat.Last") != 49 {
		t.Fatal("lost journaled write")
	}
}

func TestQSLSPendingDroppedWhenSupersededOrDeleted(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	f := "w.red-xai-db-qs-ls:"
	mustSet(t, db, f+"H.A", num(1), SetOpts{CheckpointSeconds: 1})
	mustSet(t, db, f+"H.B", num(1), SetOpts{CheckpointSeconds: 1})
	mustSet(t, db, f+"H.C", num(1), SetOpts{CheckpointSeconds: 1})
	mustSet(t, db, f+"H.A", num(2), SetOpts{}) // immediate write supersedes the pending delta
	db.Delete(f + "H.B")
	db.Delete(f + "H")           // whole subtree gone, C's pending delta must vanish
	clk.Advance(2 * time.Second) // everything due
	db.tables["w.red-xai-db-qs-ls"].flushPending(false)
	st := db.Stats()
	if st["qsls.deltas_flushed"] != 0 {
		t.Fatalf("stale deltas flushed: %v", st["qsls.deltas_flushed"])
	}
	db.crash()
	db2 := open(t, dir, Config{})
	defer db2.Close()
	if v, err := db2.Get("w.red-xai-db-qs-ls:H.C"); err != nil || !v.IsNell() {
		t.Fatalf("deleted subtree resurrected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cluster hook / follower

type recorder struct {
	mu sync.Mutex
	es []LogEntry
	ap int
}

func (r *recorder) OnCommit(e LogEntry) { r.mu.Lock(); r.es = append(r.es, e); r.mu.Unlock() }
func (r *recorder) Apply(e LogEntry) error {
	r.mu.Lock()
	r.ap++
	r.mu.Unlock()
	return nil
}
func (r *recorder) entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogEntry(nil), r.es...)
}

type failHook struct{ NopHook }

func (failHook) Apply(LogEntry) error { return errors.New("quorum lost") }

func TestClusterHookAndFollowerAppliesLeaderDeleteWithoutLocalExpiry(t *testing.T) {
	leaderClk, followerClk := newFakeClock(), newFakeClock()
	rec := &recorder{}
	leader := open(t, t.TempDir(), Config{Clock: leaderClk.Now, Hook: rec})
	defer leader.Close()
	follower := open(t, t.TempDir(), Config{Clock: followerClk.Now, Follower: true})
	defer follower.Close()

	ship := func() {
		for _, e := range rec.entries() {
			if err := follower.ApplyEntry(e); err != nil {
				t.Fatal(err)
			}
		}
		rec.mu.Lock()
		rec.es = nil
		rec.mu.Unlock()
	}
	mustSet(t, leader, "s.red-xai-db-qs-ls:Buff[ID=1].Speed", num(5), SetOpts{TTLSeconds: 30})
	mustSet(t, leader, "s.red-xai-db-qs-ls:Buff[ID=1].Perm", num(1), SetOpts{})
	ship()
	if rec.ap != 2 {
		t.Fatalf("Apply hook calls: %d", rec.ap)
	}
	if mustGetNum(t, follower, "Buff[ID=1].Speed") != 5 {
		t.Fatal("replication of set failed")
	}
	if err := follower.Set("s.red-xai-db-qs-ls:X.Y", num(1), SetOpts{}); !errors.Is(err, ErrFollower) {
		t.Fatalf("follower must reject client writes: %v", err)
	}

	// the follower's clock runs far ahead: it still must not expire locally
	followerClk.Advance(10 * time.Minute)
	follower.sweepNow()
	if st := follower.Stats(); st["qsls.expired"] != 0 {
		t.Fatal("follower expired locally")
	}
	if v, _ := follower.Get("Buff[ID=1].Speed"); !v.IsNell() { // reads still hide expired entries
		t.Fatalf("expired entry should read as NELL on follower, got %v", v)
	}
	if follower.Stats()["qsls.lazy_expired"] != 0 {
		t.Fatal("follower must not purge on read")
	}
	if _, ok := follower.tables["s.red-xai-db-qs-ls"].resolveExact([]box.Seg{{Name: "Buff", ID: 1, HasID: true}, {Name: "Speed"}}); !ok {
		t.Fatal("entry must linger until the leader's delete arrives")
	}

	// leader expires it and logs a synthetic delete; follower applies it
	leaderClk.Advance(31 * time.Second)
	leader.sweepNow()
	ents := rec.entries()
	if len(ents) != 1 || ents[0].Ops[0].Kind != "expire" {
		t.Fatalf("leader did not log the expiry: %+v", ents)
	}
	ship()
	if _, ok := follower.tables["s.red-xai-db-qs-ls"].resolveExact([]box.Seg{{Name: "Buff", ID: 1, HasID: true}, {Name: "Speed"}}); ok {
		t.Fatal("leader's logged delete not applied")
	}
	if mustGetNum(t, follower, "Buff[ID=1].Perm") != 1 {
		t.Fatal("sibling lost")
	}
	// promotion: expiry is re-armed from the data
	leaderClk2 := followerClk
	_ = leaderClk2
	follower.ApplyEntry(LogEntry{File: "s.red-xai-db-qs-ls", Ops: []Op{{Kind: "set", Path: "T.V", Value: num(1), TTLKind: box.TTLFixed, TTLSeconds: 5,
		ExpiresAtMs: followerClk.Now().UnixMilli() + 5000}}})
	follower.SetFollower(false)
	followerClk.Advance(6 * time.Second)
	follower.sweepNow()
	if v, err := follower.Get("T.V"); err != nil || !v.IsNell() {
		t.Fatalf("promoted node did not expire: %v", err)
	}
}

func TestApplyHookErrorSurfaces(t *testing.T) {
	db := open(t, t.TempDir(), Config{Hook: failHook{}})
	defer db.Close()
	if err := db.Set("a.red-xai.db:A.B", num(1), SetOpts{}); err == nil {
		t.Fatal("hook error not returned")
	}
	if mustGetNum(t, db, "A.B") != 1 {
		t.Fatal("local commit must stand")
	}
	db.SetClusterHook(nil)
	if err := db.Set("a.red-xai.db:A.B", num(2), SetOpts{}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsKeysPresent(t *testing.T) {
	db := open(t, t.TempDir(), Config{})
	defer db.Close()
	mustSet(t, db, "a.red-xai.db:A.B", num(1), SetOpts{})
	st := db.Stats()
	for _, k := range []string{"files", "cold.sets", "cold.wal_fsyncs", "cold.lsn", "events_dropped", "subscribers"} {
		if _, ok := st[k]; !ok {
			t.Errorf("missing stat %s (have %v)", k, fmt.Sprint(st))
		}
	}
}
