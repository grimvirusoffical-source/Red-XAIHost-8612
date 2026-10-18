package engine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"redx/box"
)

// Acceptance cases R1-R13: leader/follower behaviour through ClusterHook +
// ApplyEntry with independent fake clocks.

const qsls = "s.red-xai-db-qs-ls"

type pair struct {
	t      *testing.T
	L, F   *Database
	LC, FC *fakeClock
	rec    *recorder
	fdir   string
}

func newPair(t *testing.T) *pair {
	p := &pair{t: t, LC: newFakeClock(), FC: newFakeClock(), rec: &recorder{}, fdir: t.TempDir()}
	p.L = open(t, t.TempDir(), Config{Clock: p.LC.Now, Hook: p.rec})
	p.F = open(t, p.fdir, Config{Clock: p.FC.Now, Follower: true})
	t.Cleanup(func() { p.L.Close(); p.F.Close() })
	return p
}

// ship applies every entry the leader committed since the last call.
func (p *pair) ship() []LogEntry {
	p.t.Helper()
	es := p.rec.entries()
	p.rec.mu.Lock()
	p.rec.es = nil
	p.rec.mu.Unlock()
	for _, e := range es {
		if err := p.F.ApplyEntry(e); err != nil {
			p.t.Fatalf("apply lsn %d: %v", e.LSN, err)
		}
	}
	return es
}

func (p *pair) fEntry(file string, segs ...box.Seg) *box.Entry {
	e, _ := p.F.tables[file].resolveExact(segs)
	return e
}

func seg(name string, id ...int64) box.Seg {
	if len(id) > 0 {
		return box.Seg{Name: name, ID: id[0], HasID: true}
	}
	return box.Seg{Name: name}
}

// R1: the follower never decides expiry; R4 (clock behind) is the same path.
func TestR1FollowerNeverExpires(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:B.V", num(1), SetOpts{TTLSeconds: 30})
	mustSet(t, p.L, "q.red-xai-db-qs:B.V", num(1), SetOpts{TTLSeconds: 30})
	p.ship()
	p.FC.Advance(time.Hour)
	for i := 0; i < 10; i++ {
		p.F.sweepNow()
	}
	st := p.F.Stats()
	if st["qsls.expired"] != 0 || st["qs.expired"] != 0 {
		t.Fatalf("follower expired locally: %v", st)
	}
	if p.fEntry(qsls, seg("B"), seg("V")) == nil || p.fEntry("q.red-xai-db-qs", seg("B"), seg("V")) == nil {
		t.Fatal("entries must be retained until the leader's delete arrives")
	}
}

// R2: an idle follower writes nothing: LSN and journal size stay put.
func TestR2IdleFollowerWritesNothing(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:B.V", num(1), SetOpts{TTLSeconds: 30})
	p.ship()
	before := p.F.Stats()
	p.FC.Advance(time.Hour)
	for i := 0; i < 10; i++ {
		p.F.sweepNow()
		p.F.Get("B.V")
		p.F.Get("Nope.X")
	}
	after := p.F.Stats()
	for _, k := range []string{"qsls.wal_frames", "qsls.wal_size", "qsls.lazy_expired"} {
		if before[k] != after[k] {
			t.Fatalf("%s changed on an idle follower: %v -> %v", k, before[k], after[k])
		}
	}
}

// R3: exactly one expire event on the follower, when the leader's delete arrives.
func TestR3FollowerEventExactlyOnce(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:B.V", num(1), SetOpts{TTLSeconds: 30})
	p.ship()
	ch, cancel := p.F.Subscribe("*")
	defer cancel()
	p.FC.Advance(time.Hour)
	p.F.sweepNow()
	p.F.Get("B.V") // reads of an expired entry on a follower neither purge nor emit
	select {
	case e := <-ch:
		t.Fatalf("follower emitted %+v before the leader's delete", e)
	case <-time.After(30 * time.Millisecond):
	}
	p.LC.Advance(31 * time.Second)
	p.L.sweepNow()
	es := p.ship()
	for _, e := range es { // a duplicate delivery is a no-op and silent
		if err := p.F.ApplyEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	deadline := time.After(100 * time.Millisecond)
loop:
	for {
		select {
		case e := <-ch:
			if e.Op == "expire" || e.Op == "delete" {
				n++
			}
		case <-deadline:
			break loop
		}
	}
	if n != 1 {
		t.Fatalf("%d delete/expire events on the follower, want exactly 1", n)
	}
}

// R5: a follower whose clock is ahead hides data early (documented, harmless);
// when its clock is behind it simply keeps showing until the leader's delete.
func TestR5ClockSkew(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:B.V", num(1), SetOpts{TTLSeconds: 60})
	p.ship()
	p.FC.Advance(45 * time.Second)      // follower is 45s ahead of the leader's clock
	if mustGetNum(t, p.F, "B.V") != 1 { // follower +45s < 60s: still visible
		t.Fatal("hidden too early")
	}
	p.FC.Advance(20 * time.Second) // follower +65s, leader still at +0
	if !absentOrNell(t, p.F, "B.V") {
		t.Fatal("ahead-clock follower should hide at the absolute deadline")
	}
	if mustGetNum(t, p.L, "B.V") != 1 {
		t.Fatal("leader must still serve it")
	}
	// behind: leader expires; the follower (clock behind) applies the delete at once
	p.LC.Advance(61 * time.Second)
	p.L.sweepNow()
	p.ship()
	if p.fEntry(qsls, seg("B"), seg("V")) != nil {
		t.Fatal("delete not applied")
	}
}

// R6: sliding refreshes replicate as touch ops; follower reads never slide.
func TestR6SlidingReplicatesAsTouch(t *testing.T) {
	p := newPair(t)
	path := "q.red-xai-db-qs:Sess[ID=1].B"
	mustSet(t, p.L, path, num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	p.ship()
	p.LC.Advance(6 * time.Second)
	p.FC.Advance(6 * time.Second)
	mustGetNum(t, p.L, path) // leader read slides to T0+16
	es := p.ship()
	if len(es) != 1 || es[0].Ops[0].Kind != "touch" {
		t.Fatalf("sliding move did not replicate as a touch op: %+v", es)
	}
	fe := p.fEntry("q.red-xai-db-qs", seg("Sess", 1), seg("B"))
	le, _ := p.L.tables["q.red-xai-db-qs"].resolveExact([]box.Seg{seg("Sess", 1), seg("B")})
	if fe.TTL.ExpiresAt() != le.TTL.ExpiresAt() {
		t.Fatalf("follower deadline %d != leader %d", fe.TTL.ExpiresAt(), le.TTL.ExpiresAt())
	}
	before := fe.TTL.ExpiresAt()
	for i := 0; i < 5; i++ { // follower reads must not slide
		p.FC.Advance(time.Second)
		p.F.Get("Sess[ID=1].B")
		p.F.Query("Sess[ID=1].B")
	}
	if fe.TTL.ExpiresAt() != before {
		t.Fatal("a follower read slid the deadline")
	}
	// promotion keeps the replicated deadline (T0+16 on the shared timeline)
	p.FC.ms.Store(p.LC.ms.Load())
	p.F.SetFollower(false)
	p.FC.Advance(9 * time.Second) // T0+15
	p.F.sweepNow()
	if p.fEntry("q.red-xai-db-qs", seg("Sess", 1), seg("B")) == nil { // (a Get here would slide: it is the leader now)
		t.Fatal("promoted node expired early (lost the slid deadline)")
	}
	p.FC.Advance(2 * time.Second) // T0+17
	if !absentOrNell(t, p.F, "q.red-xai-db-qs:Sess[ID=1].B") {
		t.Fatal("promoted node did not expire at the replicated deadline")
	}
}

// R7: TTL kind, seconds and the absolute deadline are replicated verbatim.
func TestR7TTLReplicatedVerbatim(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K.Fixed", num(1), SetOpts{TTLSeconds: 30})
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K.Slide", num(1), SetOpts{TTLSeconds: 40, Sliding: true})
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K.Ckpt", num(1), SetOpts{CheckpointSeconds: 5})
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K.None", num(1), SetOpts{})
	p.ship()
	for _, k := range []string{"Fixed", "Slide", "Ckpt", "None"} {
		fe := p.fEntry(qsls, seg("K"), seg(k))
		le, _ := p.L.tables[qsls].resolveExact([]box.Seg{seg("K"), seg(k)})
		if fe == nil || fe.TTL.Kind != le.TTL.Kind || fe.TTL.Seconds != le.TTL.Seconds || fe.TTL.ExpiresAt() != le.TTL.ExpiresAt() {
			t.Fatalf("%s: follower %+v leader %+v", k, fe, le)
		}
	}
}

// R8: a sub-box TTL replicates as one delete of the whole subtree.
func TestR8SubtreeExpiryReplicates(t *testing.T) {
	p := newPair(t)
	v, _ := box.ParseValue(`[ {X[]} = 1, {Nested[ID=2]} = [ {Y[]} = 2 ] ]`)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:Sess[ID=1]", v, SetOpts{TTLSeconds: 30})
	p.ship()
	if p.fEntry(qsls, seg("Sess", 1), seg("Nested", 2), seg("Y")) == nil {
		t.Fatal("subtree not replicated")
	}
	p.LC.Advance(31 * time.Second)
	p.L.sweepNow()
	es := p.ship()
	if len(es) != 1 || len(es[0].Ops) != 1 || es[0].Ops[0].Kind != "expire" {
		t.Fatalf("subtree expiry should be one expire op, got %+v", es)
	}
	if p.fEntry(qsls, seg("Sess", 1)) != nil {
		t.Fatal("subtree survived on the follower")
	}
}

// R9: duplicate and gap handling.
func TestR9DuplicateAndGap(t *testing.T) {
	p := newPair(t)
	for i := 1; i <= 3; i++ {
		mustSet(t, p.L, fmt.Sprintf("a.red-xai.db:K.V%d", i), num(float64(i)), SetOpts{})
	}
	es := p.rec.entries()
	if err := p.F.ApplyEntry(es[0]); err != nil {
		t.Fatal(err)
	}
	if err := p.F.ApplyEntry(es[0]); err != nil { // duplicate: no-op
		t.Fatalf("duplicate must be a no-op, got %v", err)
	}
	if err := p.F.ApplyEntry(es[2]); !errors.Is(err, ErrNeedResync) { // gap
		t.Fatalf("gap: want ErrNeedResync, got %v", err)
	}
	if _, err := p.F.Get("a.red-xai.db:K.V3"); err == nil {
		t.Fatal("gap entry was applied")
	}
	if err := p.F.ApplyEntry(es[1]); err != nil {
		t.Fatal(err)
	}
	if err := p.F.ApplyEntry(es[2]); err != nil {
		t.Fatal(err)
	}
	if mustGetNum(t, p.F, "a.red-xai.db:K.V3") != 3 {
		t.Fatal("not applied")
	}
	// a different history is refused
	bad := es[2]
	bad.LSN = 4
	bad.History = "someone-else"
	if err := p.F.ApplyEntry(bad); !errors.Is(err, ErrNeedResync) {
		t.Fatalf("history mismatch: %v", err)
	}
}

// R10: delete/expire then re-set of the same key converges.
func TestR10ExpireThenResetConverges(t *testing.T) {
	p := newPair(t)
	path := "s.red-xai-db-qs-ls:K.V"
	mustSet(t, p.L, path, num(1), SetOpts{TTLSeconds: 30})
	p.LC.Advance(30 * time.Second)
	p.L.sweepNow()
	p.LC.Advance(time.Second)
	mustSet(t, p.L, path, num(2), SetOpts{TTLSeconds: 30})
	if es := p.ship(); len(es) != 3 {
		t.Fatalf("want set, expire, set; got %d entries", len(es))
	}
	fe := p.fEntry(qsls, seg("K"), seg("V"))
	le, _ := p.L.tables[qsls].resolveExact([]box.Seg{seg("K"), seg("V")})
	if fe == nil || fe.Value.Num != 2 || fe.TTL.ExpiresAt() != le.TTL.ExpiresAt() {
		t.Fatalf("diverged: %+v vs %+v", fe, le)
	}
}

// R11: promotion arms expiry from the data, mints a new history, and keeps
// the LSN sequence going.
func TestR11Promotion(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K.V", num(1), SetOpts{TTLSeconds: 30})
	p.ship()
	hOld := p.F.HistoryID()
	p.FC.ms.Store(p.LC.ms.Load())
	rec2 := &recorder{}
	p.F.SetClusterHook(rec2)
	p.F.SetFollower(false)
	if p.F.HistoryID() == hOld {
		t.Fatal("promotion must start a new replication history")
	}
	if err := p.F.Set("s.red-xai-db-qs-ls:K.W", num(1), SetOpts{}); err != nil {
		t.Fatalf("promoted node must accept writes: %v", err)
	}
	p.FC.Advance(31 * time.Second)
	p.F.sweepNow()
	es := rec2.entries()
	if len(es) != 2 || es[0].LSN != 2 || es[1].LSN != 3 || es[1].Ops[0].Kind != "expire" || es[0].History != p.F.HistoryID() {
		t.Fatalf("promoted journal wrong: %+v", es)
	}
	// an old-history entry from the deposed leader is refused
	old := LogEntry{History: hOld, LSN: 4, File: qsls, Ops: []Op{{Kind: "set", Path: "Z.V", Value: num(1)}}}
	if err := p.F.ApplyEntry(old); err == nil {
		t.Fatal("promoted node accepted a deposed leader's entry")
	}
}

// R12: snapshot resync replaces stale state.
func TestR12SnapshotResync(t *testing.T) {
	p := newPair(t)
	mustSet(t, p.L, "s.red-xai-db-qs-ls:A.V", num(1), SetOpts{TTLSeconds: 600})
	mustSet(t, p.L, "s.red-xai-db-qs-ls:B.V", num(2), SetOpts{})
	p.rec.mu.Lock()
	p.rec.es = nil
	p.rec.mu.Unlock()
	// follower holds stale data from some other history
	p.F.SetFollower(false)
	mustSet(t, p.F, "s.red-xai-db-qs-ls:Stale.V", num(9), SetOpts{})
	p.F.SetFollower(true)
	doc, lsn, err := p.L.ExportSnapshot(qsls)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.F.InstallSnapshot(qsls, doc, lsn, p.L.HistoryID()); err != nil {
		t.Fatal(err)
	}
	if p.fEntry(qsls, seg("Stale")) != nil {
		t.Fatal("stale entry survived the snapshot")
	}
	fe := p.fEntry(qsls, seg("A"), seg("V"))
	le, _ := p.L.tables[qsls].resolveExact([]box.Seg{seg("A"), seg("V")})
	if fe == nil || fe.TTL.ExpiresAt() != le.TTL.ExpiresAt() {
		t.Fatalf("absolute deadline lost in the snapshot: %+v", fe)
	}
	mustSet(t, p.L, "s.red-xai-db-qs-ls:C.V", num(3), SetOpts{})
	p.ship() // streaming continues at lsn+1
	if mustGetNum(t, p.F, "C.V") != 3 {
		t.Fatal("stream after snapshot not applied")
	}
}

// R13: a follower restart keeps its LSN and its (hidden but retained) data.
func TestR13FollowerRestart(t *testing.T) {
	p := newPair(t)
	for i := 1; i <= 3; i++ {
		mustSet(t, p.L, fmt.Sprintf("s.red-xai-db-qs-ls:K[ID=%d].V", i), num(float64(i)), SetOpts{TTLSeconds: 30})
	}
	p.ship()
	p.F.Close()
	p.FC.Advance(time.Hour)
	f2 := open(t, p.fdir, Config{Clock: p.FC.Now, Follower: true})
	defer f2.Close()
	if _, ok := f2.tables[qsls].resolveExact([]box.Seg{seg("K", 1), seg("V")}); !ok {
		t.Fatal("expired-but-unconfirmed entry must be retained across a follower restart")
	}
	if !absentOrNell(t, f2, "K[ID=1].V") {
		t.Fatal("it must still read as absent")
	}
	f2.sweepNow()
	if f2.Stats()["qsls.expired"] != 0 {
		t.Fatal("restarted follower expired locally")
	}
	mustSet(t, p.L, "s.red-xai-db-qs-ls:K[ID=4].V", num(4), SetOpts{})
	es := p.rec.entries()
	if err := f2.ApplyEntry(es[0]); err != nil {
		t.Fatalf("next LSN after restart rejected: %v", err)
	}
	if err := f2.ApplyEntry(LogEntry{History: es[0].History, LSN: 99, File: qsls, Ops: es[0].Ops}); !errors.Is(err, ErrNeedResync) {
		t.Fatalf("gap after restart: %v", err)
	}
	p.rec.mu.Lock()
	p.rec.es = nil
	p.rec.mu.Unlock()
}
