package engine

import (
	"bufio"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"redx/box"
)

// Acceptance cases C1-C12. C1 (multi-op commit atomic at every byte) is
// TestTornTailEveryByteOffset; C4 (half-written checkpoint) is
// TestCheckpointRecoveryPaths; C7's base case is TestCorruptCheckpointDetected.

func cold(int) string { return "a.red-xai.db:Box.V" }

// C2: a crash while creating a fresh WAL segment leaves 0..headerLen-1 bytes.
func TestC2TruncatedSegmentHeader(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 1; i <= 5; i++ {
		mustSet(t, db, cold(0), num(float64(i)), SetOpts{})
	}
	if err := db.Checkpoint(); err != nil { // creates a fresh, empty segment
		t.Fatal(err)
	}
	db.crash()
	wal := walFile(t, dir, "a.red-xai.db")
	for cut := 0; cut < walHeaderLen; cut++ {
		d2 := filepath.Join(t.TempDir(), "d")
		copyDir(t, dir, d2)
		if err := os.Truncate(filepath.Join(d2, filepath.Base(wal)), int64(cut)); err != nil {
			t.Fatal(err)
		}
		db2, err := OpenDatabase(d2, Config{})
		if err != nil {
			t.Fatalf("header cut at %d: %v", cut, err)
		}
		if mustGetNum(t, db2, "Box.V") != 5 {
			t.Fatalf("cut %d: lost checkpointed data", cut)
		}
		mustSet(t, db2, cold(0), num(6), SetOpts{})
		db2.crash()
		db3 := open(t, d2, Config{})
		if mustGetNum(t, db3, "Box.V") != 6 {
			t.Fatalf("cut %d: write after header repair lost", cut)
		}
		db3.Close()
	}
	// a full-length but wrong header is corruption, not a torn tail
	d2 := filepath.Join(t.TempDir(), "d")
	copyDir(t, dir, d2)
	b, _ := os.ReadFile(filepath.Join(d2, filepath.Base(wal)))
	b[0] ^= 0xff
	os.WriteFile(filepath.Join(d2, filepath.Base(wal)), b, 0o644)
	if _, err := OpenDatabase(d2, Config{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bad magic: want ErrCorrupt, got %v", err)
	}
}

// C3: damage in the MIDDLE of the log (valid records follow) is not a torn tail.
func TestC3MidLogCorruption(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	wal := ""
	var offs []int64
	for i := 1; i <= 8; i++ {
		mustSet(t, db, cold(0), num(float64(i)), SetOpts{})
		if wal == "" {
			wal = walFile(t, dir, "a.red-xai.db")
		}
		st, _ := os.Stat(wal)
		offs = append(offs, st.Size())
	}
	db.crash()
	// flip one bit inside the 5th record (between offs[3] and offs[4])
	mid := (offs[3] + offs[4]) / 2
	b, _ := os.ReadFile(wal)
	b[mid] ^= 0x10
	os.WriteFile(wal, b, 0o644)
	if _, err := OpenDatabase(dir, Config{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("mid-log damage must be ErrCorrupt (never silently drop acked commits), got %v", err)
	}
	// explicit repair keeps the valid prefix (records 1-4) and preserves the dropped bytes
	db2, err := OpenDatabase(dir, Config{RepairWAL: true})
	if err != nil {
		t.Fatal(err)
	}
	if mustGetNum(t, db2, "Box.V") != 4 {
		t.Fatalf("repair kept %v, want the prefix ending at 4", mustGetNum(t, db2, "Box.V"))
	}
	db2.Close()
	m, _ := filepath.Glob(filepath.Join(dir, "*.torn-*"))
	if len(m) == 0 {
		t.Fatal("dropped bytes were not preserved")
	}
	// and the repaired db opens normally afterwards
	db3 := open(t, dir, Config{})
	if mustGetNum(t, db3, "Box.V") != 4 {
		t.Fatal("state changed after repair")
	}
	db3.Close()
}

// C5: a COMPLETE but never-renamed checkpoint temp must be ignored.
func TestC5CompleteTmpCheckpointIgnored(t *testing.T) {
	other := t.TempDir()
	o := open(t, other, Config{})
	mustSet(t, o, "a.red-xai.db:Bogus.X", num(1), SetOpts{})
	o.Close()
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	mustSet(t, db, "a.red-xai.db:Real.X", num(1), SetOpts{})
	db.crash()
	b, _ := os.ReadFile(filepath.Join(other, "a.red-xai.db")) // valid footer, other contents
	os.WriteFile(filepath.Join(dir, "a.red-xai.db.tmp"), b, 0o644)
	db2 := open(t, dir, Config{})
	defer db2.Close()
	if mustGetNum(t, db2, "Real.X") != 1 {
		t.Fatal("real data lost")
	}
	if v, err := db2.Get("Bogus.X"); err == nil && !v.IsNell() {
		t.Fatal("a never-renamed checkpoint was adopted")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.red-xai.db.tmp")); !os.IsNotExist(err) {
		t.Fatal("tmp not removed")
	}
}

// C6: a stale WAL segment (older than the checkpoint) must never be replayed
// over the newer snapshot; LSNs continue without reuse.
func TestC6StaleWALNotReplayed(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	mustSet(t, db, "a.red-xai.db:X.V", num(1), SetOpts{})
	if err := db.Delete("a.red-xai.db:X"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, "a.red-xai.db:Y.V", num(1), SetOpts{})
	oldPath := walFile(t, dir, "a.red-xai.db")
	old, _ := os.ReadFile(oldPath)
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	mustSet(t, db, "a.red-xai.db:Z.V", num(1), SetOpts{})
	db.crash()
	os.WriteFile(oldPath, old, 0o644) // the old segment "reappears" (removal not yet durable)
	rec := &recorder{}
	db2 := open(t, dir, Config{Hook: rec})
	defer db2.Close()
	if v, err := db2.Get("X.V"); err == nil && !v.IsNell() {
		t.Fatal("stale segment resurrected a deleted key")
	}
	if mustGetNum(t, db2, "Y.V") != 1 || mustGetNum(t, db2, "Z.V") != 1 {
		t.Fatal("state wrong")
	}
	mustSet(t, db2, "a.red-xai.db:W.V", num(1), SetOpts{})
	if es := rec.entries(); len(es) != 1 || es[0].LSN != 5 {
		t.Fatalf("LSN after recovery = %+v, want 5 (4 earlier commits, none reused)", es)
	}
}

// C7: damaged checkpoint with a missing/short log is ErrCorrupt, never "empty".
func TestC7CorruptCheckpointWithTruncatedWAL(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	mustSet(t, db, cold(0), str("hello world"), SetOpts{})
	db.Close()
	p := filepath.Join(dir, "a.red-xai.db")
	b, _ := os.ReadFile(p)
	b[strings.Index(string(b), "hello")] = 'J'
	os.WriteFile(p, b, 0o644)
	os.Truncate(walFile(t, dir, "a.red-xai.db"), int64(walHeaderLen))
	if _, err := OpenDatabase(dir, Config{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	if _, err := OpenDatabase(dir, Config{RepairWAL: true}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("RepairWAL must not paper over a bad checkpoint: %v", err)
	}
}

// C8: kill -9 a real child process mid-write, many times, in each durability mode.
func TestChildWriter(t *testing.T) {
	dir := os.Getenv("REDX_CHILD_DIR")
	if dir == "" {
		t.Skip("helper process for TestC8KillNine")
	}
	db, err := OpenDatabase(dir, Config{Durability: os.Getenv("REDX_CHILD_DUR"), SweepMS: 3_600_000})
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(2)
	}
	for i := 0; i < 100000; i++ {
		if err := db.Set(fmt.Sprintf("a.red-xai.db:K[ID=%d].V", i), num(float64(i)), SetOpts{}); err != nil {
			fmt.Println("ERR", err)
			os.Exit(2)
		}
		fmt.Println(i) // acknowledged
	}
}

func TestC8KillNine(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	rng := rand.New(rand.NewSource(8))
	for _, dur := range []string{"sync", "group"} {
		t.Run(dur, func(t *testing.T) {
			dir := t.TempDir()
			next := 0 // children continue where the last acknowledged key left off? no: each key i is unique per run
			for round := 0; round < 6; round++ {
				rd := filepath.Join(dir, "r") // one dir; each round starts a fresh namespace
				os.RemoveAll(rd)
				os.MkdirAll(rd, 0o755)
				cmd := exec.Command(os.Args[0], "-test.run=^TestChildWriter$", "-test.count=1")
				cmd.Env = append(os.Environ(), "REDX_CHILD_DIR="+rd, "REDX_CHILD_DUR="+dur)
				out, _ := cmd.StdoutPipe()
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				sc := bufio.NewScanner(out)
				target := 20 + rng.Intn(400)
				acked := -1
				for sc.Scan() {
					line := sc.Text()
					n, err := strconv.Atoi(line)
					if err != nil {
						continue // go test chatter ("PASS", "ERR ...")
					}
					acked = n
					if n >= target {
						break
					}
				}
				cmd.Process.Kill()
				cmd.Wait()
				if acked < 0 {
					t.Fatalf("child produced nothing")
				}
				db, err := OpenDatabase(rd, Config{})
				if err != nil {
					t.Fatalf("round %d: reopen after kill -9: %v", round, err)
				}
				for i := 0; i <= acked; i++ {
					v, err := db.Get(fmt.Sprintf("a.red-xai.db:K[ID=%d].V", i))
					if err != nil || v.Num != float64(i) {
						t.Fatalf("round %d: acknowledged commit %d lost (acked up to %d): %v", round, i, acked, err)
					}
				}
				// at most the commits that were in flight may exist beyond the last ack, and no holes
				extra := 0
				for i := acked + 1; ; i++ {
					if _, err := db.Get(fmt.Sprintf("a.red-xai.db:K[ID=%d].V", i)); err != nil {
						break
					}
					extra++
				}
				if extra > 1000 {
					t.Fatalf("implausible extra commits %d", extra)
				}
				for i := acked + extra + 2; i < acked+extra+20; i++ {
					if _, err := db.Get(fmt.Sprintf("a.red-xai.db:K[ID=%d].V", i)); err == nil {
						t.Fatalf("hole in recovered log at %d", i)
					}
				}
				db.Close()
				next += acked
			}
			t.Logf("%s: 6 kill -9 rounds, %d acknowledged commits verified", dur, next)
		})
	}
}

// C9: recovering the same crashed directory repeatedly is idempotent.
func TestC9RecoveryIdempotent(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	for i := 1; i <= 20; i++ {
		mustSet(t, db, fmt.Sprintf("a.red-xai.db:K[ID=%d].V", i), num(float64(i)), SetOpts{})
	}
	db.crash()
	wal := walFile(t, dir, "a.red-xai.db")
	b, _ := os.ReadFile(wal)
	os.WriteFile(wal, append(b, 1, 2, 3, 4, 5, 6, 7), 0o644) // torn tail
	snapshot := func(db *Database) string {
		res, _ := db.Query("a.red-xai.db:K[ID=*].V")
		s := make([]string, 0, len(res))
		for _, r := range res {
			s = append(s, fmt.Sprint(r.Num))
		}
		return strings.Join(s, ";")
	}
	var first string
	for i := 0; i < 4; i++ {
		db := open(t, dir, Config{})
		if i == 0 {
			first = snapshot(db)
			if len(first) == 0 {
				t.Fatal("empty")
			}
		} else if snapshot(db) != first {
			t.Fatalf("recovery %d changed the state", i)
		}
		if i%2 == 0 {
			db.crash() // crash again right after recovery
		} else {
			db.Close()
		}
	}
}

// C10: I/O errors during the journal write or fsync.
func TestC10IOErrors(t *testing.T) {
	for _, tc := range []struct {
		name, op string
		partial  bool
	}{{"torn-write", "write", true}, {"enospc-write", "write", false}, {"fsync-fail", "sync", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db := open(t, dir, Config{Durability: "sync"})
			tb := db.tables["a.red-xai.db"]
			if tb == nil {
				mustSet(t, db, cold(0), num(0), SetOpts{})
				tb = db.tables["a.red-xai.db"]
			}
			var n int
			tb.p.ioHook = func(op string, f *os.File, data []byte) error {
				if op != tc.op {
					return nil
				}
				n++
				if n < 3 {
					return nil
				}
				if tc.partial {
					f.Write(data[:len(data)/2])
				}
				return errors.New("injected EIO")
			}
			mustSet(t, db, cold(0), num(1), SetOpts{})
			mustSet(t, db, cold(0), num(2), SetOpts{})
			err := db.Set(cold(0), num(3), SetOpts{})
			if err == nil {
				t.Fatal("failed I/O was acknowledged")
			}
			if err2 := db.Set(cold(0), num(4), SetOpts{}); !errors.Is(err2, ErrFailed) {
				t.Fatalf("file must latch into the failed state, got %v", err2)
			}
			db.crash()
			db2 := open(t, dir, Config{})
			defer db2.Close()
			got := mustGetNum(t, db2, "Box.V")
			if got < 2 || got > 3 || (tc.partial && got != 2) {
				t.Fatalf("recovered V=%v: acked commit lost or a half commit became visible", got)
			}
			mustSet(t, db2, cold(0), num(9), SetOpts{}) // and the reopened db works
		})
	}
	t.Run("group-commit-concurrent", func(t *testing.T) {
		dir := t.TempDir()
		db := open(t, dir, Config{Durability: "group"})
		mustSet(t, db, "a.red-xai.db:Init.V", num(0), SetOpts{})
		tb := db.tables["a.red-xai.db"]
		var mu sync.Mutex
		n := 0
		tb.p.ioHook = func(op string, f *os.File, data []byte) error {
			if op != "write" {
				return nil
			}
			mu.Lock()
			n++
			k := n
			mu.Unlock()
			if k == 5 {
				f.Write(data[:len(data)/3])
				return errors.New("injected EIO")
			}
			return nil
		}
		const W = 8
		acked := make([]int, W)
		var wg sync.WaitGroup
		for w := 0; w < W; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 1; i < 200; i++ {
					if db.Set(fmt.Sprintf("a.red-xai.db:W%d.V", w), num(float64(i)), SetOpts{}) != nil {
						return
					}
					acked[w] = i
				}
			}(w)
		}
		wg.Wait()
		db.crash()
		db2 := open(t, dir, Config{})
		defer db2.Close()
		for w := 0; w < W; w++ {
			v, err := db2.Get(fmt.Sprintf("a.red-xai.db:W%d.V", w))
			if acked[w] > 0 && (err != nil || int(v.Num) < acked[w]) {
				t.Fatalf("writer %d: acked %d but recovered %v (%v)", w, acked[w], v.Num, err)
			}
		}
	})
}

// C11: crash between a deadline passing and the sweeper journaling the delete.
func TestC11CrashAroundExpiry(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	db := open(t, dir, Config{Clock: clk.Now, Durability: "sync"})
	p := "w.red-xai-db-qs-ls:S.V"
	mustSet(t, db, p, num(1), SetOpts{TTLSeconds: 10})
	mustSet(t, db, "w.red-xai-db-qs-ls:S.Keep", num(1), SetOpts{})
	clk.Advance(20 * time.Second) // deadline passed, sweeper never ran
	db.crash()
	rec := &recorder{}
	db2 := open(t, dir, Config{Clock: clk.Now, Hook: rec})
	if db2.Stats()["qsls.expired"] != 0 {
		t.Fatal("recovery itself purged (it should only hide until the sweeper/read journals the delete)")
	}
	if _, ok := db2.tables["w.red-xai-db-qs-ls"].resolveExact([]box.Seg{{Name: "S"}, {Name: "V"}}); !ok {
		t.Fatal("entry should still be stored until its delete is journaled")
	}
	db2.sweepNow() // recovery re-armed it: the delete is journaled now
	es := rec.entries()
	if len(es) != 1 || es[0].Ops[0].Kind != "expire" {
		t.Fatalf("expected one synthetic delete, got %+v", es)
	}
	if !absentOrNell(t, db2, p) {
		t.Fatal("expired-before-crash key readable after recovery")
	}
	db2.crash()
	db3 := open(t, dir, Config{Clock: clk.Now})
	defer db3.Close()
	if !absentOrNell(t, db3, p) || mustGetNum(t, db3, "S.Keep") != 1 {
		t.Fatal("state wrong after the journaled expiry was replayed")
	}
	// a clock that went BACKWARD across the crash: the absolute deadline still wins
	dir2 := t.TempDir()
	db4 := open(t, dir2, Config{Clock: clk.Now, Durability: "sync"})
	mustSet(t, db4, p, num(1), SetOpts{TTLSeconds: 10})
	db4.crash()
	clk.Advance(-3 * time.Second)
	db5 := open(t, dir2, Config{Clock: clk.Now})
	defer db5.Close()
	if mustGetNum(t, db5, "S.V") != 1 {
		t.Fatal("alive key lost")
	}
}

// C12: unrelated and stray files in the data directory.
func TestC12StrayFiles(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir, Config{Durability: "sync"})
	mustSet(t, db, cold(0), num(1), SetOpts{})
	db.Close()
	for name, body := range map[string]string{"notes.txt": "hi", ".DS_Store": "x", "other.red-xai.db.tmp": "{partial", "empty.red-xai.db": "", "empty.red-xai-db-qs-ls": ""} {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	db2, err := OpenDatabase(dir, Config{})
	if err != nil {
		t.Fatalf("stray files broke Open: %v", err)
	}
	defer db2.Close()
	if mustGetNum(t, db2, "Box.V") != 1 {
		t.Fatal("data lost")
	}
	mustSet(t, db2, "empty.red-xai.db:E.V", num(1), SetOpts{})
	for _, n := range []string{"notes.txt", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("engine deleted a file it does not own: %s", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.red-xai.db.tmp")); !os.IsNotExist(err) {
		t.Fatal("stale tmp of an engine file not cleaned")
	}
}
