package engine

import (
	"fmt"
	"testing"

	"redx/box"
)

// Benchmarks use a 1024-key space so the working set stays small and the
// numbers reflect the engine, not cache misses. Paths are parsed on every
// call (that cost is included).

func benchPaths(file string) []string {
	ps := make([]string, 1024)
	for i := range ps {
		ps[i] = fmt.Sprintf("%s:Store[ID=%d].K%d", file, i%64, i)
	}
	return ps
}

func benchOpen(b *testing.B, cfg Config) *Database {
	b.Helper()
	return open(b, b.TempDir(), cfg)
}

func runSet(b *testing.B, db *Database, file string, opts SetOpts, parallelism int) {
	ps := benchPaths(file)
	for _, p := range ps { // pre-create so we measure overwrite, not growth
		if err := db.Set(p, box.Int(0), opts); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	if parallelism == 0 {
		for i := 0; i < b.N; i++ {
			if err := db.Set(ps[i&1023], box.Int(int64(i)), opts); err != nil {
				b.Fatal(err)
			}
		}
	} else {
		b.SetParallelism(parallelism)
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				i++
				if err := db.Set(ps[i&1023], box.Int(int64(i)), opts); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
}

func runGet(b *testing.B, db *Database, file string, opts SetOpts, parallel bool) {
	ps := benchPaths(file)
	for _, p := range ps {
		if err := db.Set(p, box.Int(1), opts); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	if !parallel {
		for i := 0; i < b.N; i++ {
			if _, err := db.Get(ps[i&1023]); err != nil {
				b.Fatal(err)
			}
		}
	} else {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				i++
				if _, err := db.Get(ps[i&1023]); err != nil {
					b.Error(err)
					return
				}
			}
		})
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/s")
}

const (
	fQS   = "s.red-xai-db-qs"
	fQSLS = "w.red-xai-db-qs-ls"
	fCold = "c.red-xai.db"
)

func BenchmarkQSSet(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runSet(b, db, fQS, SetOpts{TTLSeconds: 3600}, 0)
}
func BenchmarkQSSetSliding(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runSet(b, db, fQS, SetOpts{TTLSeconds: 3600, Sliding: true}, 0)
}
func BenchmarkQSSetParallel(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runSet(b, db, fQS, SetOpts{TTLSeconds: 3600}, 1)
}
func BenchmarkQSGet(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runGet(b, db, fQS, SetOpts{TTLSeconds: 3600}, false)
}
func BenchmarkQSGetParallel(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runGet(b, db, fQS, SetOpts{TTLSeconds: 3600}, true)
}

// qs-ls with the (N)* marker: RAM write + coalesced delta, no journal I/O on the hot path.
func BenchmarkQSLSSetCheckpointed(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runSet(b, db, fQSLS, SetOpts{CheckpointSeconds: 5}, 0)
}
func BenchmarkQSLSSetCheckpointedParallel(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runSet(b, db, fQSLS, SetOpts{CheckpointSeconds: 5}, 1)
}

// qs-ls ordinary ',' write: every write is journaled immediately.
func BenchmarkQSLSSetImmediateGroup(b *testing.B) {
	db := benchOpen(b, Config{Durability: "group"})
	defer db.Close()
	runSet(b, db, fQSLS, SetOpts{}, 0)
}
func BenchmarkQSLSSetImmediateGroupParallel64(b *testing.B) {
	db := benchOpen(b, Config{Durability: "group"})
	defer db.Close()
	runSet(b, db, fQSLS, SetOpts{}, 8) // 8 x GOMAXPROCS goroutines
}
func BenchmarkQSLSSetImmediateAsync(b *testing.B) {
	db := benchOpen(b, Config{Durability: "async"})
	defer db.Close()
	runSet(b, db, fQSLS, SetOpts{}, 0)
}
func BenchmarkQSLSGet(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runGet(b, db, fQSLS, SetOpts{CheckpointSeconds: 5}, false)
}

// cold .db: durable commits. These numbers are dominated by fsync latency of the disk.
func BenchmarkColdSetSync(b *testing.B) {
	db := benchOpen(b, Config{Durability: "sync"})
	defer db.Close()
	runSet(b, db, fCold, SetOpts{}, 0)
}
func BenchmarkColdSetGroupSingleWriter(b *testing.B) {
	db := benchOpen(b, Config{Durability: "group"})
	defer db.Close()
	runSet(b, db, fCold, SetOpts{}, 0)
}
func BenchmarkColdSetGroupParallel64(b *testing.B) {
	db := benchOpen(b, Config{Durability: "group"})
	defer db.Close()
	runSet(b, db, fCold, SetOpts{}, 8)
}
func BenchmarkColdSetAsync(b *testing.B) {
	db := benchOpen(b, Config{Durability: "async"})
	defer db.Close()
	runSet(b, db, fCold, SetOpts{}, 0)
}
func BenchmarkColdSetAsyncParallel(b *testing.B) {
	db := benchOpen(b, Config{Durability: "async"})
	defer db.Close()
	runSet(b, db, fCold, SetOpts{}, 1)
}
func BenchmarkColdGet(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runGet(b, db, fCold, SetOpts{}, false)
}
func BenchmarkColdGetParallel(b *testing.B) {
	db := benchOpen(b, Config{})
	defer db.Close()
	runGet(b, db, fCold, SetOpts{}, true)
}

// Timing wheel in isolation: insert + fire.
func BenchmarkWheelAddAdvance(b *testing.B) {
	var w wheel
	w.start(0)
	b.ReportAllocs()
	var out []*wItem
	items := make([]wItem, 4096)
	for i := 0; i < b.N; i++ {
		it := &items[i&4095]
		it.at = int64(i) + 5000
		w.add(it)
		if i&255 == 0 {
			out = w.advance(int64(i), out[:0])
		}
	}
}
