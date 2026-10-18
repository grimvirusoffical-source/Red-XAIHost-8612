package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"redx/box"
)

// fakeClock is a manually advanced clock (unix ms).
type fakeClock struct{ ms atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.ms.Store(1_800_000_000_000)
	return c
}
func (c *fakeClock) Now() time.Time          { return time.UnixMilli(c.ms.Load()) }
func (c *fakeClock) Advance(d time.Duration) { c.ms.Add(d.Milliseconds()) }

func open(t testing.TB, dir string, cfg Config) *Database {
	t.Helper()
	if cfg.SweepMS == 0 {
		cfg.SweepMS = 3_600_000 // tests drive sweeps explicitly unless they want the real loop
	}
	db, err := OpenDatabase(dir, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func mustSet(t testing.TB, s Store, path string, v box.Value, o SetOpts) {
	t.Helper()
	if err := s.Set(path, v, o); err != nil {
		t.Fatalf("set %s: %v", path, err)
	}
}

func mustGetNum(t testing.TB, s Store, path string) float64 {
	t.Helper()
	v, err := s.Get(path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	f, ok := v.Float()
	if !ok {
		t.Fatalf("get %s: not a number: %v", path, v)
	}
	return f
}

func copyDir(t testing.TB, src, dst string) {
	t.Helper()
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() == "LOCK" || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func walFile(t testing.TB, dir, file string) string {
	t.Helper()
	gens, err := listSegments(filepath.Join(dir, file))
	if err != nil || len(gens) == 0 {
		t.Fatalf("no wal segments for %s: %v", file, err)
	}
	return segPath(filepath.Join(dir, file), gens[len(gens)-1])
}

func num(f float64) box.Value { return box.Number(f) }
func str(s string) box.Value  { return box.String(s) }

var _ = fmt.Sprintf

func errorsIs(err, target error) bool { return errors.Is(err, target) }
