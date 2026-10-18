package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"redx/box"
)

func TestRoutingAndCRUD(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{})
	defer db.Close()

	mustSet(t, db, "accounts.red-xai.db:AccountVault[ID=1].Name", str("Josh"), SetOpts{})
	mustSet(t, db, "accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10].Sword", str("steel"), SetOpts{})
	mustSet(t, db, "accounts.red-xai.db:AccountVault[ID=1].Inventory[id=11].Sword", str("iron"), SetOpts{})
	mustSet(t, db, "sessions.red-xai-db-qs:QuickSessionStore[ID=1].Otp", str("123456"), SetOpts{TTLSeconds: 30})
	mustSet(t, db, "world.red-xai-db-qs-ls:HotWorldStore.GlobalKillCounter", num(7), SetOpts{})

	for name, typ := range map[string]string{"accounts.red-xai.db": "cold", "sessions.red-xai-db-qs": "qs", "world.red-xai-db-qs-ls": "qsls"} {
		if db.Files()[name] != typ {
			t.Fatalf("file %s routed to %q, want %q", name, db.Files()[name], typ)
		}
	}
	// file prefix optional when the box name is unique
	if mustGetNum(t, db, "HotWorldStore.GlobalKillCounter") != 7 {
		t.Fatal("unprefixed get")
	}
	v, err := db.Get("AccountVault[ID=1].Inventory[id=10]")
	if err != nil || v.Kind != box.KindBox || v.Box.Len() != 1 {
		t.Fatalf("box get: %v %v", v, err)
	}
	// basename without extension
	if v, _ := db.Get("accounts:AccountVault[ID=1].Name"); v.Str != "Josh" {
		t.Fatalf("basename route: %v", v)
	}
	// query with wildcards across files
	res, err := db.Query("AccountVault[ID=1].Inventory[ID=*].Sword")
	if err != nil || len(res) != 2 {
		t.Fatalf("query: %v %v", res, err)
	}
	res, _ = db.Query("accounts.red-xai.db:AccountVault[ID=1].*")
	if len(res) != 3 { // Name, Inventory[10], Inventory[11]
		t.Fatalf("wildcard: %d", len(res))
	}
	// errors
	if _, err := db.Get("Nope.X"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
	if err := db.Set("NoFileBox.X", num(1), SetOpts{}); !errors.Is(err, ErrNoFile) {
		t.Fatalf("want ErrNoFile: %v", err)
	}
	if err := db.Set("weird.txt:A.B", num(1), SetOpts{}); !errors.Is(err, ErrBadPath) {
		t.Fatalf("want ErrBadPath: %v", err)
	}
	if err := db.Set("accounts.red-xai.db:AccountVault[ID=1].Name.Deep", num(1), SetOpts{}); !errors.Is(err, ErrNotBox) {
		t.Fatalf("want ErrNotBox: %v", err)
	}
	// failed set did not leave junk behind
	if v, _ := db.Get("accounts.red-xai.db:AccountVault[ID=1].Name"); v.Str != "Josh" {
		t.Fatal("rollback failed")
	}
	if err := db.Set("accounts.red-xai.db:A.B", num(1), SetOpts{TTLSeconds: 5}); !errors.Is(err, ErrBadOpts) {
		t.Fatalf("cold TTL must be rejected: %v", err)
	}
	if err := db.Set("sessions.red-xai-db-qs:Q.B", num(1), SetOpts{}); !errors.Is(err, ErrTTLRequired) {
		t.Fatalf("qs w/o TTL: %v", err)
	}
	if err := db.Set("sessions.red-xai-db-qs:Q.B", num(1), SetOpts{TTLSeconds: 5, CheckpointSeconds: 5}); !errors.Is(err, ErrBadOpts) {
		t.Fatalf("both: %v", err)
	}
	if err := db.Set("sessions.red-xai-db-qs:Q.B", num(1), SetOpts{CheckpointSeconds: 5}); !errors.Is(err, ErrBadOpts) {
		t.Fatalf("qs ckpt: %v", err)
	}
	if err := db.Set("accounts.red-xai.db:A.B", box.Number(nan()), SetOpts{}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("nan: %v", err)
	}
	// delete
	if err := db.Delete("accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10]"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10].Sword"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := db.Delete("accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10]"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
	// storing a parsed Box value
	doc, err := box.ParseDocument(`{P[ID=5]= [ {Hp[]} = 10, {Tags[]} = ;["a","b"]:, {Sub[]} = [ {X[]} = 1 ] ]True};`)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := box.Resolve(doc, []box.Seg{{Name: "P", ID: 5, HasID: true}})
	mustSet(t, db, "accounts.red-xai.db:Players[ID=1].P", e.Value, SetOpts{})
	if got := mustGetNum(t, db, "Players[ID=1].P.Sub.X"); got != 1 {
		t.Fatalf("nested box: %v", got)
	}
	// Get returns a copy: mutating it must not affect the store
	v, _ = db.Get("Players[ID=1].P")
	v.Box.Put(&box.Entry{Key: "Hacked", Value: box.Bool(true)})
	if _, err := db.Get("Players[ID=1].P.Hacked"); !errors.Is(err, ErrNotFound) {
		t.Fatal("Get aliased internal state")
	}
}

func nan() float64 { var z float64; return z / z }

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{})
	for i := 0; i < 100; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:Acc[ID=%d].Bal", i), num(float64(i)), SetOpts{})
		mustSet(t, db, fmt.Sprintf("w.red-xai-db-qs-ls:Hot.C%d", i), num(float64(i)), SetOpts{})
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// the data file is plain, checksummed Box grammar
	txt, _ := os.ReadFile(filepath.Join(dir, "a.red-xai.db"))
	if !strings.Contains(string(txt), "{Acc[ID=7]= [") || !strings.Contains(string(txt), "<< REDX-CKPT") {
		t.Fatalf("unexpected data file:\n%.300s", txt)
	}
	db = open(t, dir, Config{})
	defer db.Close()
	for i := 0; i < 100; i++ {
		if mustGetNum(t, db, fmt.Sprintf("Acc[ID=%d].Bal", i)) != float64(i) || mustGetNum(t, db, fmt.Sprintf("Hot.C%d", i)) != float64(i) {
			t.Fatalf("lost data at %d", i)
		}
	}
	if db.Stats()["cold.recovered_records"] != 0 {
		t.Fatal("clean close should leave a checkpoint, nothing to replay")
	}
}

// ---------------------------------------------------------------------------
// WAL crash recovery

func TestCrashRecoverySyncCommitsSurvive(t *testing.T) {
	for _, mode := range []string{"sync", "group", "async-after-flush"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{Durability: mode}
			if mode == "async-after-flush" {
				cfg.Durability = "async"
				cfg.AsyncFlushMS = 5
			}
			db := open(t, dir, cfg)
			for i := 0; i < 200; i++ {
				mustSet(t, db, fmt.Sprintf("a.red-xai.db:Acc[ID=1].K%d", i), num(float64(i)), SetOpts{})
			}
			if mode == "async-after-flush" {
				time.Sleep(60 * time.Millisecond) // let the background flusher run
			}
			db.crash() // kill -9: no final flush, no checkpoint
			db2 := open(t, dir, Config{})
			defer db2.Close()
			for i := 0; i < 200; i++ {
				if mustGetNum(t, db2, fmt.Sprintf("Acc[ID=1].K%d", i)) != float64(i) {
					t.Fatalf("lost %d", i)
				}
			}
			if r := db2.Stats()["cold.recovered_records"]; r != 200 {
				t.Fatalf("recovered %v records, want 200", r)
			}
		})
	}
}

// Every commit writes x and y in one transaction; recovery from a log cut at
// ANY byte must yield a state where x == y == k for some k (atomic prefix).
func TestTornTailEveryByteOffset(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	const N = 25
	for i := 1; i <= N; i++ {
		tx := db.Begin()
		if err := tx.Set("a.red-xai.db:Pair.X", num(float64(i)), SetOpts{}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Set("a.red-xai.db:Pair.Y", num(float64(i)), SetOpts{}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Set("a.red-xai.db:Pair.Pad", str(strings.Repeat("p", i)), SetOpts{}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	db.crash()
	wal := walFile(t, dir, "a.red-xai.db")
	full, _ := os.ReadFile(wal)
	seen := map[int]bool{}
	for cut := walHeaderLen; cut <= len(full); cut++ {
		d2 := filepath.Join(t.TempDir(), "db")
		copyDir(t, dir, d2)
		w2 := filepath.Join(d2, filepath.Base(wal))
		if err := os.WriteFile(w2, full[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		db2 := open(t, d2, Config{})
		x, errx := db2.Get("Pair.X")
		y, erry := db2.Get("Pair.Y")
		if errx != nil || erry != nil {
			if errx == nil || erry == nil {
				t.Fatalf("cut %d: partial transaction visible (x err=%v y err=%v)", cut, errx, erry)
			}
			seen[0] = true
		} else {
			if x.Num != y.Num {
				t.Fatalf("cut %d: torn transaction x=%v y=%v", cut, x.Num, y.Num)
			}
			seen[int(x.Num)] = true
		}
		if cut == len(full) && db2.Stats()["cold.wal_torn_bytes_dropped"] != 0 {
			t.Fatal("whole log reported torn")
		}
		// the recovered db must accept new writes and survive another restart
		mustSet(t, db2, "a.red-xai.db:After.Z", num(1), SetOpts{})
		db2.crash()
		db3 := open(t, d2, Config{})
		if mustGetNum(t, db3, "After.Z") != 1 {
			t.Fatalf("cut %d: write after torn-tail recovery lost", cut)
		}
		db3.Close()
	}
	if len(seen) != N+1 {
		t.Fatalf("expected every prefix 0..%d to be observed across cuts, saw %d distinct", N, len(seen))
	}
}

func TestTornTailGarbageAndBitFlip(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 1; i <= 10; i++ {
		mustSet(t, db, "a.red-xai.db:Box.V", num(float64(i)), SetOpts{})
	}
	db.crash()
	wal := walFile(t, dir, "a.red-xai.db")
	full, _ := os.ReadFile(wal)

	// 1) random garbage appended after a good log: all 10 commits kept
	d1 := filepath.Join(t.TempDir(), "d")
	copyDir(t, dir, d1)
	g := append(append([]byte{}, full...), []byte("\x07garbage-bytes-not-a-frame!!")...)
	os.WriteFile(filepath.Join(d1, filepath.Base(wal)), g, 0o644)
	db1 := open(t, d1, Config{})
	if mustGetNum(t, db1, "Box.V") != 10 {
		t.Fatal("garbage tail lost good commits")
	}
	if db1.Stats()["cold.wal_torn_bytes_dropped"] == 0 {
		t.Fatal("garbage not reported")
	}
	db1.Close()
	// forensic copy of the dropped tail was kept
	m, _ := filepath.Glob(filepath.Join(d1, "*.torn-*"))
	if len(m) != 1 {
		t.Fatalf("expected a .torn-* forensic file, got %v", m)
	}

	// 2) bit flip inside the LAST frame: that commit (only) is dropped
	d2 := filepath.Join(t.TempDir(), "d")
	copyDir(t, dir, d2)
	b := append([]byte{}, full...)
	b[len(b)-3] ^= 0xff
	os.WriteFile(filepath.Join(d2, filepath.Base(wal)), b, 0o644)
	db2 := open(t, d2, Config{})
	if got := mustGetNum(t, db2, "Box.V"); got != 9 {
		t.Fatalf("bit-flipped last frame: V=%v want 9", got)
	}
	db2.Close()
}

func TestCheckpointRecoveryPaths(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 0; i < 50; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:A[ID=%d].V", i), num(float64(i)), SetOpts{})
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	for i := 50; i < 80; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:A[ID=%d].V", i), num(float64(i)), SetOpts{})
	}
	mustSet(t, db, "a.red-xai.db:A[ID=3].V", num(333), SetOpts{})
	_ = db.Delete("a.red-xai.db:A[ID=4]")
	// a half-written checkpoint from a crashed process must be ignored
	os.WriteFile(filepath.Join(dir, "a.red-xai.db.tmp"), []byte("{Half[ID=1]= [ {X"), 0o644)
	db.crash()

	db2 := open(t, dir, Config{})
	if _, err := os.Stat(filepath.Join(dir, "a.red-xai.db.tmp")); !os.IsNotExist(err) {
		t.Fatal("stale tmp checkpoint not removed")
	}
	if mustGetNum(t, db2, "A[ID=79].V") != 79 || mustGetNum(t, db2, "A[ID=3].V") != 333 || mustGetNum(t, db2, "A[ID=10].V") != 10 {
		t.Fatal("snapshot+wal replay wrong")
	}
	if _, err := db2.Get("A[ID=4].V"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete after checkpoint lost")
	}
	if r := db2.Stats()["cold.recovered_records"]; r != 32 {
		t.Fatalf("replayed %v records after checkpoint, want 32 (30 sets + 1 overwrite + 1 delete)", r)
	}
	db2.Close()
}

// Crash after the WAL was rotated but before the new checkpoint became
// visible: the old checkpoint plus BOTH segments must give the full state.
func TestCrashBetweenRotateAndCheckpointRename(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 0; i < 20; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:A.K%d", i), num(float64(i)), SetOpts{})
	}
	tb := db.tables["a.red-xai.db"]
	tb.mu.Lock()
	if _, err := tb.p.rotate(); err != nil {
		t.Fatal(err)
	}
	tb.mu.Unlock()
	for i := 20; i < 30; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:A.K%d", i), num(float64(i)), SetOpts{})
	}
	db.crash() // never wrote the new snapshot
	db2 := open(t, dir, Config{})
	defer db2.Close()
	for i := 0; i < 30; i++ {
		if mustGetNum(t, db2, fmt.Sprintf("A.K%d", i)) != float64(i) {
			t.Fatalf("lost K%d", i)
		}
	}
}

func TestCorruptCheckpointDetected(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{})
	mustSet(t, db, "a.red-xai.db:A.B", str("hello world"), SetOpts{})
	db.Close()
	p := filepath.Join(dir, "a.red-xai.db")
	b, _ := os.ReadFile(p)
	i := strings.Index(string(b), "hello")
	b[i] = 'J'
	os.WriteFile(p, b, 0o644)
	if _, err := OpenDatabase(dir, Config{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	// truncated file (no footer)
	os.WriteFile(p, b[:i], 0o644)
	if _, err := OpenDatabase(dir, Config{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt for truncated checkpoint, got %v", err)
	}
}

func TestAutoCheckpointTruncatesWAL(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "async", CheckpointBytes: 20_000})
	for i := 0; i < 2000; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:A.K%d", i%50), str(strings.Repeat("x", 100)), SetOpts{})
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats()["cold.checkpoints"] < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if db.Stats()["cold.checkpoints"] < 1 {
		t.Fatal("no automatic checkpoint")
	}
	db.Close()
	gens, _ := listSegments(filepath.Join(dir, "a.red-xai.db"))
	if len(gens) > 2 {
		t.Fatalf("old WAL segments not removed: %v", gens)
	}
}

func TestGroupCommitBatchesFsyncs(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "group"})
	defer db.Close()
	var wg sync.WaitGroup
	const G, N = 32, 100
	for g := 0; g < G; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				if err := db.Set(fmt.Sprintf("a.red-xai.db:G[ID=%d].V", g), num(float64(i)), SetOpts{}); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	st := db.Stats()
	if st["cold.wal_frames"] != G*N {
		t.Fatalf("frames %v", st["cold.wal_frames"])
	}
	if !(st["cold.wal_fsyncs"] < st["cold.wal_frames"]) {
		t.Fatalf("group commit did not batch: %v fsyncs for %v commits", st["cold.wal_fsyncs"], st["cold.wal_frames"])
	}
	t.Logf("group commit: %v commits / %v fsyncs", st["cold.wal_frames"], st["cold.wal_fsyncs"])
}

// ---------------------------------------------------------------------------
// transactions

func TestTxnAtomicityAndConflict(t *testing.T) {
	db := open(t, t.TempDir(), Config{})
	defer db.Close()
	mustSet(t, db, "a.red-xai.db:Acct[ID=1].Bal", num(100), SetOpts{})
	mustSet(t, db, "a.red-xai.db:Acct[ID=2].Bal", num(0), SetOpts{})

	t1 := db.Begin()
	v, _ := t1.Get("Acct[ID=1].Bal")
	// concurrent writer sneaks in
	mustSet(t, db, "a.red-xai.db:Acct[ID=1].Bal", num(50), SetOpts{})
	t1.Set("Acct[ID=1].Bal", num(v.Num-30), SetOpts{})
	t1.Set("Acct[ID=2].Bal", num(30), SetOpts{})
	if err := t1.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if mustGetNum(t, db, "Acct[ID=2].Bal") != 0 || mustGetNum(t, db, "Acct[ID=1].Bal") != 50 {
		t.Fatal("conflicting txn left partial writes")
	}
	// write to a descendant invalidates a read of the ancestor
	t2 := db.Begin()
	if _, err := t2.Get("Acct[ID=2]"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, "a.red-xai.db:Acct[ID=2].Extra", num(1), SetOpts{})
	t2.Set("Acct[ID=1].Bal", num(1), SetOpts{})
	if err := t2.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("ancestor read not validated: %v", err)
	}
	// absent read then creation by someone else
	t3 := db.Begin()
	if _, err := t3.Get("a.red-xai.db:Ghost.X"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	mustSet(t, db, "a.red-xai.db:Ghost.X", num(1), SetOpts{})
	t3.Set("Acct[ID=1].Bal", num(2), SetOpts{})
	if err := t3.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("phantom not detected: %v", err)
	}
	// a failing op mid-transaction rolls back every earlier op
	t4 := db.Begin()
	t4.Set("Acct[ID=1].Bal", num(999), SetOpts{})
	t4.Set("Acct[ID=1].Bal.Deeper", num(1), SetOpts{}) // Bal is a number now: descend must fail
	if err := t4.Commit(); !errors.Is(err, ErrNotBox) {
		t.Fatalf("want ErrNotBox, got %v", err)
	}
	if mustGetNum(t, db, "Acct[ID=1].Bal") != 50 {
		t.Fatal("partial apply not rolled back")
	}
	// read-your-writes + success
	t5 := db.Begin()
	t5.Set("Acct[ID=1].Bal", num(10), SetOpts{})
	if v, _ := t5.Get("Acct[ID=1].Bal"); v.Num != 10 {
		t.Fatal("read-your-writes")
	}
	t5.Delete("Acct[ID=2]")
	if err := t5.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Get("Acct[ID=2].Bal"); !errors.Is(err, ErrNotFound) {
		t.Fatal("txn delete")
	}
	if err := t5.Commit(); !errors.Is(err, ErrTxnDone) {
		t.Fatal("double commit")
	}
	// unsupported / cross-file
	mustSet(t, db, "b.red-xai.db:Other.X", num(1), SetOpts{})
	t6 := db.Begin()
	t6.Set("a.red-xai.db:A.B", num(1), SetOpts{})
	if err := t6.Set("b.red-xai.db:A.B", num(1), SetOpts{}); !errors.Is(err, ErrCrossFile) {
		t.Fatalf("cross-file: %v", err)
	}
	t7 := db.Begin()
	if err := t7.Set("q.red-xai-db-qs:A.B", num(1), SetOpts{TTLSeconds: 5}); !errors.Is(err, ErrTxnUnsupport) {
		t.Fatalf("qs txn: %v", err)
	}
}

func TestTxnConcurrentTransfersConserveMoney(t *testing.T) {
	db := open(t, t.TempDir(), Config{Durability: "group"})
	defer db.Close()
	const accts = 8
	for i := 0; i < accts; i++ {
		mustSet(t, db, fmt.Sprintf("bank.red-xai.db:Acct[ID=%d].Bal", i), num(1000), SetOpts{})
	}
	var wg sync.WaitGroup
	var conflicts, commits atomic.Int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g)))
			for n := 0; n < 150; n++ {
				a, b := rng.Intn(accts), rng.Intn(accts)
				if a == b {
					continue
				}
				for {
					tx := db.Begin()
					va, err1 := tx.Get(fmt.Sprintf("Acct[ID=%d].Bal", a))
					vb, err2 := tx.Get(fmt.Sprintf("Acct[ID=%d].Bal", b))
					if err1 != nil || err2 != nil {
						t.Error(err1, err2)
						return
					}
					tx.Set(fmt.Sprintf("Acct[ID=%d].Bal", a), num(va.Num-1), SetOpts{})
					tx.Set(fmt.Sprintf("Acct[ID=%d].Bal", b), num(vb.Num+1), SetOpts{})
					err := tx.Commit()
					if err == nil {
						commits.Add(1)
						break
					}
					if !errors.Is(err, ErrConflict) {
						t.Error(err)
						return
					}
					conflicts.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	sum := 0.0
	for i := 0; i < accts; i++ {
		sum += mustGetNum(t, db, fmt.Sprintf("Acct[ID=%d].Bal", i))
	}
	if sum != accts*1000 {
		t.Fatalf("money not conserved: %v (commits=%d conflicts=%d)", sum, commits.Load(), conflicts.Load())
	}
	t.Logf("transfers committed=%d conflicts-retried=%d", commits.Load(), conflicts.Load())
}

// ---------------------------------------------------------------------------
// subscriptions

func TestSubscribe(t *testing.T) {
	db := open(t, t.TempDir(), Config{})
	defer db.Close()
	mustSet(t, db, "a.red-xai.db:Player[ID=1].Health", num(100), SetOpts{})
	exact, cancel1 := db.Subscribe("Player[ID=1].Health")
	parent, cancel2 := db.Subscribe("a.red-xai.db:Player")
	other, cancel3 := db.Subscribe("Player[ID=2]")
	all, cancel4 := db.Subscribe("*")
	defer cancel1()
	defer cancel2()
	defer cancel3()
	defer cancel4()

	mustSet(t, db, "a.red-xai.db:Player[ID=1].Health", num(0), SetOpts{})
	db.Delete("Player[ID=1].Health")
	get := func(ch <-chan Event) Event {
		select {
		case e := <-ch:
			return e
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
		return Event{}
	}
	e := get(exact)
	if e.Op != "set" || e.Value.Num != 0 || e.Path != "a.red-xai.db:Player[ID=1].Health" || e.At.IsZero() {
		t.Fatalf("event: %+v", e)
	}
	if e := get(exact); e.Op != "delete" {
		t.Fatalf("event: %+v", e)
	}
	get(parent)
	get(parent)
	get(all)
	get(all)
	select {
	case e := <-other:
		t.Fatalf("unrelated subscriber got %+v", e)
	default:
	}
	// event on an ancestor reaches a subscriber of the descendant
	mustSet(t, db, "a.red-xai.db:Player[ID=1]", box.BoxValue(box.NewBox()), SetOpts{})
	if e := get(exact); e.Op != "set" || e.Path != "a.red-xai.db:Player[ID=1]" {
		t.Fatalf("ancestor event: %+v", e)
	}
	cancel1()
	if _, ok := <-exact; ok {
		t.Fatal("channel not closed after cancel")
	}
	cancel1() // idempotent
	if db.Stats()["subscribers"] != 3 {
		t.Fatalf("subscribers=%v", db.Stats()["subscribers"])
	}
}

func TestSubscribeSlowConsumerDropsNotBlocks(t *testing.T) {
	db := open(t, t.TempDir(), Config{})
	defer db.Close()
	_, cancel := db.Subscribe("*")
	defer cancel()
	for i := 0; i < subBuffer+500; i++ {
		mustSet(t, db, "w.red-xai-db-qs-ls:H.C", num(float64(i)), SetOpts{})
	}
	if db.Stats()["events_dropped"] < 400 {
		t.Fatalf("expected drops, stats=%v", db.Stats()["events_dropped"])
	}
}

// ---------------------------------------------------------------------------
// concurrency (run with -race)

func TestConcurrencyAllEngines(t *testing.T) {
	db := open(t, t.TempDir(), Config{Durability: "group", SweepMS: 5})
	defer db.Close()
	files := []struct {
		name string
		opts SetOpts
	}{
		{"c.red-xai.db", SetOpts{}},
		{"q.red-xai-db-qs", SetOpts{TTLSeconds: 1, Sliding: true}},
		{"l.red-xai-db-qs-ls", SetOpts{}},
		{"l2.red-xai-db-qs-ls", SetOpts{CheckpointSeconds: 1}},
	}
	ch, cancel := db.Subscribe("*")
	go func() {
		for range ch {
		}
	}()
	defer cancel()
	var wg, ckwg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g)))
			f := files[g%len(files)]
			for n := 0; n < 600; n++ {
				path := fmt.Sprintf("%s:Box[ID=%d].Sub.K%d", f.name, rng.Intn(4), rng.Intn(10))
				switch rng.Intn(6) {
				case 0, 1, 2:
					if err := db.Set(path, num(float64(n)), f.opts); err != nil {
						t.Errorf("set: %v", err)
						return
					}
				case 3:
					if _, err := db.Get(path); err != nil && !errors.Is(err, ErrNotFound) {
						t.Errorf("get: %v", err)
						return
					}
				case 4:
					if err := db.Delete(path); err != nil && !errors.Is(err, ErrNotFound) {
						t.Errorf("del: %v", err)
						return
					}
				case 5:
					if _, err := db.Query(fmt.Sprintf("%s:Box[ID=*].Sub.*", f.name)); err != nil {
						t.Errorf("query: %v", err)
						return
					}
					_ = db.Stats()
				}
			}
		}(g)
	}
	ckwg.Add(1)
	go func() { // checkpoints racing the writers
		defer ckwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				db.Checkpoint()
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	wg.Wait()
	close(stop)
	ckwg.Wait()
}

// ---------------------------------------------------------------------------
// misc

func TestDoubleOpenRejectedAndClosedErrors(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{})
	if _, err := OpenDatabase(dir, Config{}); err == nil {
		t.Fatal("second open of same dir must fail")
	}
	db.Close()
	if err := db.Set("a.red-xai.db:A.B", num(1), SetOpts{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("set after close: %v", err)
	}
	if _, err := db.Get("A.B"); !errors.Is(err, ErrClosed) {
		t.Fatalf("get after close: %v", err)
	}
	db2 := open(t, dir, Config{}) // reopen works
	db2.Close()
}

func TestInterfaceSatisfied(t *testing.T) {
	var s Store
	s, err := Open(t.TempDir(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.(Replicable); !ok {
		t.Fatal("Open must return a Replicable")
	}
	if _, ok := s.(interface{ Begin() *Txn }); !ok {
		t.Fatal("Open must return a transactional store")
	}
}
