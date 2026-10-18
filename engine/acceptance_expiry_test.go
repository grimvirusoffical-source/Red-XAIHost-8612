package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"redx/box"
)

// Acceptance cases E1-E18 (see docs/ENGINES.md for the decisions they pin).
// Rule: an entry is expired when now >= expiresAt.

const qsF = "s.red-xai-db-qs:"

func absentOrNell(t *testing.T, db Store, path string) bool {
	t.Helper()
	v, err := db.Get(path)
	return err == nil && v.IsNell()
}

func TestE1FixedBoundary(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].A", num(1), SetOpts{TTLSeconds: 30})
	clk.Advance(29*time.Second + 999*time.Millisecond)
	if mustGetNum(t, db, qsF+"Sess[ID=1].A") != 1 {
		t.Fatal("expired early")
	}
	clk.Advance(time.Millisecond)
	if !absentOrNell(t, db, qsF+"Sess[ID=1].A") {
		t.Fatal("alive at exactly T0+30s")
	}
}

func TestE2SlidingAndE3FixedNotSlid(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].B", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	mustSet(t, db, qsF+"Sess[ID=1].A", num(1), SetOpts{TTLSeconds: 30})
	clk.Advance(6 * time.Second) // T0+6: deadline -> T0+16
	mustGetNum(t, db, qsF+"Sess[ID=1].B")
	clk.Advance(8 * time.Second) // T0+14: still alive, deadline -> T0+24
	mustGetNum(t, db, qsF+"Sess[ID=1].B")
	clk.Advance(9 * time.Second)          // T0+23
	mustGetNum(t, db, qsF+"Sess[ID=1].B") // -> T0+33
	_ = 0
	clk.Advance(10*time.Second + 1) // T0+33.001
	if !absentOrNell(t, db, qsF+"Sess[ID=1].B") {
		t.Fatal("sliding entry should expire 10s after last read")
	}
	// E3: the fixed entry was read at +6, +14, +23 and still died at +30
	if !absentOrNell(t, db, qsF+"Sess[ID=1].A") {
		t.Fatal("fixed TTL must not slide")
	}
}

func TestE2ExactTimeline(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].B", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	clk.Advance(6 * time.Second)
	mustGetNum(t, db, qsF+"Sess[ID=1].B") // deadline T0+16
	clk.Advance(8 * time.Second)          // T0+14
	mustGetNum(t, db, qsF+"Sess[ID=1].B") // deadline T0+24
	clk.Advance(10 * time.Second)         // T0+24
	if !absentOrNell(t, db, qsF+"Sess[ID=1].B") {
		t.Fatal("must be gone at T0+24")
	}
}

func TestE4ExpiredGetNeverSlides(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].B", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	clk.Advance(10 * time.Second)
	for i := 0; i < 3; i++ {
		if !absentOrNell(t, db, qsF+"Sess[ID=1].B") {
			t.Fatalf("revived by read %d", i)
		}
	}
	// a failed read of a sibling must not slide a live sliding ancestor either
	mustSet(t, db, qsF+"Anc[ID=1]", box.BoxValue(box.NewBox()), SetOpts{TTLSeconds: 10, Sliding: true})
	clk.Advance(5 * time.Second)
	if _, err := db.Get(qsF + "Anc[ID=1].Missing"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	if !absentOrNell(t, db, qsF+"Anc[ID=1]") {
		t.Fatal("a failed Get slid the ancestor")
	}
}

func TestE5SubBoxTTL(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	v, err := box.ParseValue(`[ {X[]} = 1, {Nested[ID=2]} = [ {Y[]} = 2 ] ]`)
	if err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, qsF+"Sess[ID=1]", v, SetOpts{TTLSeconds: 30})
	mustSet(t, db, qsF+"Sess[ID=2].Z", num(3), SetOpts{TTLSeconds: 100})
	clk.Advance(30 * time.Second)
	if !absentOrNell(t, db, qsF+"Sess[ID=1].X") || !absentOrNell(t, db, qsF+"Sess[ID=1].Nested[ID=2].Y") {
		t.Fatal("subtree survived its parent's TTL")
	}
	res, _ := db.Query(qsF + "Sess[ID=*]")
	if len(res) != 1 {
		t.Fatalf("query returned %d, want only Sess[ID=2]", len(res))
	}
	res, _ = db.Query(qsF + "Sess[ID=*].X") // expired ancestor hides the child from queries too
	if len(res) != 0 {
		t.Fatalf("query descended into an expired subtree: %v", res)
	}
	if mustGetNum(t, db, qsF+"Sess[ID=2].Z") != 3 {
		t.Fatal("sibling lost")
	}
}

func TestE6ChildShorterThanParent(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	v, _ := box.ParseValue(`[ {P[]} = 1 (10), {Q[]} = 2 ]`)
	mustSet(t, db, qsF+"Sess[ID=1]", v, SetOpts{TTLSeconds: 100})
	clk.Advance(10 * time.Second)
	db.sweepNow()
	if !absentOrNell(t, db, qsF+"Sess[ID=1].P") {
		t.Fatal("P should have expired at its own TTL")
	}
	if mustGetNum(t, db, qsF+"Sess[ID=1].Q") != 2 {
		t.Fatal("Q (inherits the parent's TTL) died early")
	}
	if r, _ := db.Query(qsF + "Sess[ID=1]"); len(r) != 1 {
		t.Fatal("parent died with its short-lived child")
	}
	clk.Advance(90 * time.Second)
	if !absentOrNell(t, db, qsF+"Sess[ID=1].Q") || !absentOrNell(t, db, qsF+"Sess[ID=1]") {
		t.Fatal("everything must be gone at +100s")
	}
}

func TestE7StructuralParentVanishes(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=3].K", num(1), SetOpts{TTLSeconds: 10})
	clk.Advance(10 * time.Second)
	if !absentOrNell(t, db, qsF+"Sess[ID=3].K") {
		t.Fatal("K alive")
	}
	if r, _ := db.Query(qsF + "Sess[ID=3]"); len(r) != 0 {
		t.Fatalf("structural parent lingered: %v", r)
	}
}

func TestE8ExpiredGetConsistentNELL(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].A", num(1), SetOpts{TTLSeconds: 5})
	clk.Advance(5 * time.Second)
	for i := 0; i < 3; i++ { // 1st read purges lazily; later reads must answer the same way
		v, err := db.Get(qsF + "Sess[ID=1].A")
		if err != nil || !v.IsNell() {
			t.Fatalf("read %d: got (%v,%v), want (NELL,nil)", i, v, err)
		}
	}
	// same for qs-ls
	mustSet(t, db, "w.red-xai-db-qs-ls:H.A", num(1), SetOpts{TTLSeconds: 5})
	clk.Advance(5 * time.Second)
	for i := 0; i < 2; i++ {
		if v, err := db.Get("w.red-xai-db-qs-ls:H.A"); err != nil || !v.IsNell() {
			t.Fatalf("qs-ls read %d: (%v,%v)", i, v, err)
		}
		db.sweepNow()
	}
	// cold files keep ErrNotFound for missing paths
	if _, err := db.Get("c.red-xai.db:Nope.X"); err == nil {
		t.Fatal("cold missing path must return ErrNotFound")
	}
}

func TestE9ReSetReplacesDeadlineAndKind(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Sess[ID=1].A", num(1), SetOpts{TTLSeconds: 30})
	clk.Advance(10 * time.Second)
	mustSet(t, db, qsF+"Sess[ID=1].A", num(2), SetOpts{TTLSeconds: 5, Sliding: true})
	e, _ := box.Resolve(db.tables["s.red-xai-db-qs"].root, []box.Seg{{Name: "Sess", ID: 1, HasID: true}, {Name: "A"}})
	if e.TTL.Kind != box.TTLSliding || e.TTL.Seconds != 5 || e.TTL.ExpiresAt() != clk.Now().UnixMilli()+5000 {
		t.Fatalf("kind=%d secs=%d exp-now=%d", e.TTL.Kind, e.TTL.Seconds, e.TTL.ExpiresAt()-clk.Now().UnixMilli())
	}
	clk.Advance(5 * time.Second)
	if !absentOrNell(t, db, qsF+"Sess[ID=1].A") {
		t.Fatal("old 30s deadline still applied")
	}
}

func TestE10NoTTLAndNonPositive(t *testing.T) {
	db := open(t, t.TempDir(), Config{})
	defer db.Close()
	if err := db.Set(qsF+"A.B", num(1), SetOpts{}); err != ErrTTLRequired {
		t.Fatalf("no TTL: %v", err)
	}
	for _, o := range []SetOpts{{TTLSeconds: -1}, {TTLSeconds: -5, Sliding: true}, {Sliding: true}} {
		if err := db.Set(qsF+"A.B", num(1), o); err == nil || !errorsIs(err, ErrBadOpts) {
			t.Fatalf("%+v: want ErrBadOpts, got %v", o, err)
		}
	}
	db2 := open(t, t.TempDir(), Config{DefaultTTLSeconds: 7})
	defer db2.Close()
	if err := db2.Set(qsF+"A.B", num(1), SetOpts{}); err != nil {
		t.Fatalf("default TTL: %v", err)
	}
}

func TestE11OnlySuccessfulGetAndQuerySlide(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	f := db.tables["s.red-xai-db-qs"]
	_ = f
	mustSet(t, db, qsF+"Sess[ID=1].G", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	mustSet(t, db, qsF+"Sess[ID=1].Q", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	mustSet(t, db, qsF+"Sess[ID=1].N", num(1), SetOpts{TTLSeconds: 10, Sliding: true})
	exp := func(k string) int64 {
		e, _ := box.Resolve(db.tables["s.red-xai-db-qs"].root, []box.Seg{{Name: "Sess", ID: 1, HasID: true}, {Name: k}})
		return e.TTL.ExpiresAt()
	}
	base := exp("N")
	clk.Advance(5 * time.Second)
	db.Get(qsF + "Sess[ID=1].G")
	db.Query(qsF + "Sess[ID=1].Q")
	// things that must NOT slide N:
	db.Stats()
	_, cancel := db.Subscribe(qsF + "Sess")
	cancel()
	db.Get(qsF + "Sess[ID=1].Missing")
	db.Get(qsF + "Sess[ID=9].N")
	db.Records().Get(addr("s.red-xai-db-qs", "Sess", 1)) // adapter reads do not slide
	if exp("G") != base+5000 || exp("Q") != base+5000 {
		t.Fatalf("Get/Query must slide: G=%+d Q=%+d", exp("G")-base, exp("Q")-base)
	}
	if exp("N") != base {
		t.Fatalf("N slid by %dms through a non-sliding path", exp("N")-base)
	}
}

func TestE12ClockJumpForwardManyKeys(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	const N = 10000
	for i := 0; i < N; i++ {
		mustSet(t, db, fmt.Sprintf("%sSess[ID=%d].K", qsF, i), num(1), SetOpts{TTLSeconds: 60 + i%50})
	}
	ch, cancel := db.Subscribe("*")
	defer cancel()
	var got atomic.Int64
	go func() {
		for e := range ch {
			if e.Op == "expire" {
				got.Add(1)
			}
		}
	}()
	clk.Advance(time.Hour)
	db.sweepNow() // ONE sweep
	st := db.Stats()
	if st["qs.expired"] != N || st["qs.wheel_items"] != 0 || st["qs.entries_top_level"] != 0 {
		t.Fatalf("one sweep left work behind: %v", st)
	}
	time.Sleep(50 * time.Millisecond)
	if sent, dropped := st["events_sent"], st["events_dropped"]; sent+dropped < N {
		t.Fatalf("expire events unaccounted: sent=%v dropped=%v", sent, dropped)
	}
	if !absentOrNell(t, db, qsF+"Sess[ID=7].K") {
		t.Fatal("key alive")
	}
}

func TestE13ClockJumpBackward(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	mustSet(t, db, qsF+"Gone.K", num(1), SetOpts{TTLSeconds: 10})
	mustSet(t, db, qsF+"Live.K", num(1), SetOpts{TTLSeconds: 100})
	clk.Advance(20 * time.Second)
	db.sweepNow() // Gone is purged for good
	clk.ms.Add(-int64(time.Hour / time.Millisecond))
	db.sweepNow() // must not panic or spin
	if !absentOrNell(t, db, qsF+"Gone.K") {
		t.Fatal("purged key came back")
	}
	if mustGetNum(t, db, qsF+"Live.K") != 1 {
		t.Fatal("unexpired key should live longer when the clock goes back")
	}
	// the wheel must have been rebased: a NEW short TTL fires on time
	mustSet(t, db, qsF+"New.K", num(1), SetOpts{TTLSeconds: 30})
	clk.Advance(31 * time.Second)
	db.sweepNow()
	if db.Stats()["qs.expired"] < 2 {
		t.Fatalf("sweeper stalled after the clock went backwards: expired=%v", db.Stats()["qs.expired"])
	}
	if _, ok := db.tables["s.red-xai-db-qs"].resolveExact([]box.Seg{{Name: "New"}}); ok {
		t.Fatal("new key not swept")
	}
}

func TestE14SweepDoesNotStallReaders(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	const N = 100000
	// 100k keys with one deadline (a box with no TTL of its own holds them)
	for i := 0; i < N; i++ {
		if err := db.Set(fmt.Sprintf("%sMass.K%d", qsF, i), box.Int(1), SetOpts{TTLSeconds: 30}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 1000; i++ {
		mustSet(t, db, fmt.Sprintf("%sKeep.K%d", qsF, i), num(1), SetOpts{TTLSeconds: 100000})
	}
	clk.Advance(31 * time.Second)
	var stop atomic.Bool
	var wg sync.WaitGroup
	lat := make([][]time.Duration, 4)
	for g := range lat {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				t0 := time.Now()
				if _, err := db.Get(fmt.Sprintf("%sKeep.K%d", qsF, i%1000)); err != nil {
					t.Error(err)
					return
				}
				lat[g] = append(lat[g], time.Since(t0))
			}
		}(g)
	}
	time.Sleep(20 * time.Millisecond)
	t0 := time.Now()
	db.sweepNow()
	sweep := time.Since(t0)
	stop.Store(true)
	wg.Wait()
	var all []time.Duration
	for _, l := range lat {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p99 := all[len(all)*99/100]
	t.Logf("swept %d keys in %v; %d concurrent Gets, p99=%v max=%v", N, sweep, len(all), p99, all[len(all)-1])
	if db.Stats()["qs.expired"] != N {
		t.Fatalf("expired %v", db.Stats()["qs.expired"])
	}
	if p99 > 50*time.Millisecond {
		t.Fatalf("p99 Get latency during sweep %v > 50ms", p99)
	}
}

func TestE15ExactlyOneExpireEventUnderRace(t *testing.T) {
	clk := newFakeClock()
	db := open(t, t.TempDir(), Config{Clock: clk.Now})
	defer db.Close()
	const N = 1000
	for i := 0; i < N; i++ {
		mustSet(t, db, fmt.Sprintf("%sR[ID=%d].K", qsF, i), num(1), SetOpts{TTLSeconds: 5})
	}
	ch, cancel := db.Subscribe("*")
	counts := map[string]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			if e.Op == "expire" {
				counts[e.Path]++
			}
		}
	}()
	clk.Advance(6 * time.Second)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			db.Get(fmt.Sprintf("%sR[ID=%d].K", qsF, i)) // lazy purge
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			db.sweepNow()
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	db.sweepNow()
	cancel()
	<-done
	// each lazily purged key emits its expire event; a key removed by the
	// sweeper/lazy pair emits exactly one. (Pruned structural parents emit nothing extra.)
	if db.Stats()["events_dropped"] != 0 {
		t.Skip("subscriber buffer overflowed; counts not meaningful")
	}
	for i := 0; i < N; i++ {
		p := fmt.Sprintf("s.red-xai-db-qs:R[ID=%d].K", i)
		if counts[p] != 1 {
			t.Fatalf("%s: %d expire events, want exactly 1", p, counts[p])
		}
	}
	if st := db.Stats(); st["qs.expired"] != N {
		t.Fatalf("expired=%v", st["qs.expired"])
	}
}

func TestE16QSRestartIsEmptyAndSeedNeverWritten(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now})
	mustSet(t, db, qsF+"Sess[ID=1].K", num(1), SetOpts{TTLSeconds: 60})
	db.Close()
	if st, err := os.Stat(filepath.Join(dir, "s.red-xai-db-qs")); err != nil || st.Size() != 0 {
		t.Fatalf("qs file must stay empty: %v %v", st, err)
	}
	db = open(t, dir, Config{Clock: clk.Now})
	if !absentOrNell(t, db, qsF+"Sess[ID=1].K") {
		t.Fatal("volatile data survived a restart")
	}
	db.Close()
	seed := `{Boot[ID=1]= [ {A[]} = 1 (60), {B[]} = 2 (~60) ]};`
	os.WriteFile(filepath.Join(dir, "seed.red-xai-db-qs"), []byte(seed), 0o644)
	clk.Advance(1000 * time.Second)
	db = open(t, dir, Config{Clock: clk.Now})
	if mustGetNum(t, db, "seed.red-xai-db-qs:Boot[ID=1].A") != 1 {
		t.Fatal("seed not loaded with TTLs counted from reopen time")
	}
	clk.Advance(30 * time.Second)
	mustSet(t, db, "seed.red-xai-db-qs:Boot[ID=1].C", num(3), SetOpts{TTLSeconds: 5})
	db.Close()
	if b, _ := os.ReadFile(filepath.Join(dir, "seed.red-xai-db-qs")); string(b) != seed {
		t.Fatalf("seed file was modified:\n%s", b)
	}
}

func TestE17QSLSAbsoluteDeadlineAcrossCleanClose(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now})
	mustSet(t, db, "w.red-xai-db-qs-ls:B.V", num(1), SetOpts{TTLSeconds: 60})
	db.Close() // clean close: deadline lives in the checkpoint as (60@abs)
	clk.Advance(30 * time.Second)
	db = open(t, dir, Config{Clock: clk.Now})
	if mustGetNum(t, db, "B.V") != 1 {
		t.Fatal("alive at +30s")
	}
	e, _ := box.Resolve(db.tables["w.red-xai-db-qs-ls"].root, []box.Seg{{Name: "B"}, {Name: "V"}})
	if rem := e.TTL.ExpiresAt() - clk.Now().UnixMilli(); rem != 30000 {
		t.Fatalf("remaining %dms, want 30000 (restart must not reset the TTL)", rem)
	}
	db.Close()
	clk.Advance(31 * time.Second) // T0+61
	db = open(t, dir, Config{Clock: clk.Now})
	defer db.Close()
	if !absentOrNell(t, db, "w.red-xai-db-qs-ls:B.V") {
		t.Fatal("alive at +61s")
	}
}

func TestE18CheckpointMarkerBoundsLoss(t *testing.T) {
	run := func(killAfter time.Duration) (box.Value, bool) {
		dir := t.TempDir()
		clk := newFakeClock()
		db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
		p := "w.red-xai-db-qs-ls:S.V"
		mustSet(t, db, p, num(1), SetOpts{CheckpointSeconds: 5})
		clk.Advance(2 * time.Second)
		mustSet(t, db, p, num(2), SetOpts{CheckpointSeconds: 5})
		clk.Advance(killAfter - 2*time.Second)
		db.tables["w.red-xai-db-qs-ls"].flushPending(false) // what the 20ms flusher would have done by now
		db.crash()
		db2 := open(t, dir, Config{Clock: clk.Now})
		defer db2.Close()
		v, err := db2.Get(p)
		return v, err == nil && !v.IsNell()
	}
	if v, ok := run(4900 * time.Millisecond); ok && v.Num != 1 {
		t.Fatalf("kill at +4.9s: v=%v (must be absent or 1, never 2)", v.Num)
	}
	if v, ok := run(5100 * time.Millisecond); !ok || v.Num != 2 {
		t.Fatalf("kill at +5.1s: v=%v ok=%v, want 2", v.Num, ok)
	}
}
