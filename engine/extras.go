package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"redx/engine/idx"
	"redx/engine/pack"
)

// Phase-3 wiring: search indexes (.red-xai-db-idx) and file packs
// (.red-xai-pack) living next to the data files of one Database directory.
//
//	<dir>/<name>.red-xai-db-idx        index declarations (Box grammar, see docs/phase3.md)
//	<dir>/<name>.red-xai-db-idx.snap/  sealed index snapshots (written by IndexSet.Save/Close)
//	<dir>/<name>.red-xai-pack/         chunks/ + manifests/ (pack.DirBackend)
//
// The Database never scans these entries as data files: their names do not
// end in a storage extension (or they are directories).

// validAuxName rejects names that could escape the data directory.
func validAuxName(name string) error {
	if name == "" || !identRe.MatchString(name) {
		return fmt.Errorf("%w: %q (use letters, digits, '_' and '-')", ErrBadPath, name)
	}
	return nil
}

// IndexSet is an open .red-xai-db-idx file bound to the real engines.
//
// Store is a link.Store (and idx.Store) over the real engines that keeps the
// indexes current: every Put/Delete made through it (including the ones made
// by LinkEngine/PulseEngine when you hand them Store instead of
// db.Records()) is observed synchronously after it is committed.
//
// Writes made with Database.Set/Delete directly bypass the wrapper and are
// not indexed; call Check to detect that and Rebuild to repair it.
type IndexSet struct {
	Manager *idx.Manager
	Store   *idx.ObservedStore

	db   *Database
	blob idx.DirBlob
	mu   sync.Mutex
	// Loaded reports, per index, how it was restored at open time
	// (loaded / verified / rebuilt + reason).
	Loaded []idx.LoadResult
}

// OpenIndexes opens <dir>/<name>.red-xai-db-idx. Each index is restored from its
// snapshot when the snapshot verifies against the current data and rebuilt
// from a scan of the engine otherwise (never stale; see idx.Manager.Load).
func (db *Database) OpenIndexes(name string) (*IndexSet, error) {
	if err := validAuxName(name); err != nil {
		return nil, err
	}
	if db.closed.Load() {
		return nil, ErrClosed
	}
	src, err := os.ReadFile(filepath.Join(db.dir, name+".red-xai-db-idx"))
	if err != nil {
		return nil, fmt.Errorf("engine: index declarations: %w", err)
	}
	mgr, err := idx.ParseManager(string(src))
	if err != nil {
		return nil, err
	}
	rs := db.Records()
	s := &IndexSet{Manager: mgr, db: db, blob: idx.DirBlob{Dir: filepath.Join(db.dir, name+".red-xai-db-idx.snap")}}
	s.Loaded, err = mgr.Load(s.blob, rs, idx.LoadOptions{})
	if err != nil {
		return nil, err
	}
	s.Store = idx.NewObservedStore(rs, mgr)
	return s, nil
}

// Save writes a sealed snapshot of every index so the next open can skip the rebuild.
func (s *IndexSet) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Manager.Save(s.blob, idx.SaveOptions{})
}

// Check compares the live indexes with a fresh scan of the engine and returns
// the differences per index name (only exact while no writes are in flight).
func (s *IndexSet) Check() (map[string]idx.Diff, error) { return s.Manager.Check(s.db.Records()) }

// Rebuild rescans the engine and replaces every index (queries keep answering meanwhile).
func (s *IndexSet) Rebuild() error { return s.Manager.RebuildAll(s.db.Records()) }

// Close saves a final snapshot.
func (s *IndexSet) Close() error { return s.Save() }

// OpenPack opens (creating on first use) the file pack <dir>/<name>.red-xai-pack.
// A pack must be used by one Database (process) at a time; Database.Close does
// not close packs (they hold no file handles between calls).
func (db *Database) OpenPack(name string, cfg pack.Config) (*pack.Pack, error) {
	if err := validAuxName(name); err != nil {
		return nil, err
	}
	if db.closed.Load() {
		return nil, ErrClosed
	}
	root := filepath.Join(db.dir, name+".red-xai-pack")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	be := pack.DirBackend{Root: root}
	_, _ = be.RemoveTemp() // leftovers of a crashed write
	return pack.New(be, be, cfg)
}
