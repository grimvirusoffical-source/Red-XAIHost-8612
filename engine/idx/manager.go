package idx

import (
	"errors"
	"fmt"
	"sort"

	"redx/box"
)

// Manager owns the indexes of one .red-xai-db-idx file. It implements
// Observer by fanning writes out to the indexes that match.
type Manager struct {
	indexes []*Index
	byName  map[string]*Index
}

// NewManager creates empty indexes for the declarations. Names must be unique.
func NewManager(decls []Decl) (*Manager, error) {
	m := &Manager{byName: map[string]*Index{}}
	for _, d := range decls {
		if m.byName[d.Name] != nil {
			return nil, fmt.Errorf("%w: duplicate index name %q", ErrBadDecl, d.Name)
		}
		ix, err := New(d)
		if err != nil {
			return nil, err
		}
		m.indexes = append(m.indexes, ix)
		m.byName[d.Name] = ix
	}
	return m, nil
}

// ParseManager parses a .red-xai-db-idx document and builds a Manager.
func ParseManager(src string) (*Manager, error) {
	decls, err := ParseDecls(src)
	if err != nil {
		return nil, err
	}
	return NewManager(decls)
}

// Index returns the index with the given name, or nil.
func (m *Manager) Index(name string) *Index { return m.byName[name] }

// Indexes returns the indexes in declaration order.
func (m *Manager) Indexes() []*Index { return append([]*Index(nil), m.indexes...) }

// OnPut implements Observer.
func (m *Manager) OnPut(a Addr, e *box.Entry) {
	for _, ix := range m.indexes {
		ix.OnPut(a, e)
	}
}

// OnDelete implements Observer.
func (m *Manager) OnDelete(a Addr) {
	for _, ix := range m.indexes {
		ix.OnDelete(a)
	}
}

// RebuildAll rebuilds every index from a scan of src.
func (m *Manager) RebuildAll(src Scanner) error {
	for _, ix := range m.indexes {
		if err := ix.Rebuild(src); err != nil {
			return fmt.Errorf("idx: rebuild %s: %w", ix.Name(), err)
		}
	}
	return nil
}

// SaveOptions says which source sequence number a snapshot corresponds to.
type SaveOptions struct {
	SourceSeq    uint64
	HasSourceSeq bool
}

// Save writes one sealed snapshot per index. Use ObservedStore.Locked to take
// the snapshot and read the source sequence number together.
func (m *Manager) Save(b Blob, o SaveOptions) error {
	for _, ix := range m.indexes {
		if err := b.Save(ix.Name(), ix.snapshot(o.SourceSeq, o.HasSourceSeq)); err != nil {
			return fmt.Errorf("idx: save %s: %w", ix.Name(), err)
		}
	}
	return nil
}

// LoadOptions controls Load.
type LoadOptions struct {
	// If HasSourceSeq is set and equals the sequence number recorded in the
	// snapshot, the snapshot is trusted without scanning the source (the
	// seal and the internal count/digest are still verified). The caller
	// guarantees the sequence number is durable and changes with every
	// write to the source (for example a WAL position). Otherwise the
	// source is scanned and its checksum compared.
	SourceSeq    uint64
	HasSourceSeq bool
}

// Load outcomes.
const (
	LoadedTrusted  = "loaded"   // snapshot used; source sequence number matched
	LoadedVerified = "verified" // snapshot used; source scan matched the checksum
	Rebuilt        = "rebuilt"  // snapshot unusable; rebuilt from the source
)

// LoadResult reports what happened to one index.
type LoadResult struct {
	Name   string
	Status string // LoadedTrusted, LoadedVerified or Rebuilt
	Reason string // why it was rebuilt: missing, corrupt, declaration changed, stale
}

// Load restores every index from its snapshot, falling back to a rebuild
// from src when the snapshot is missing, corrupt (bad seal, truncated, bad
// rows), built from another declaration, or stale. It never returns a
// stale index. An error is returned only if the source cannot be scanned or
// the blob store fails for a reason other than a missing snapshot.
func (m *Manager) Load(b Blob, src Scanner, o LoadOptions) ([]LoadResult, error) {
	var res []LoadResult
	for _, ix := range m.indexes {
		r, err := ix.load(b, src, o)
		if err != nil {
			return res, fmt.Errorf("idx: load %s: %w", ix.Name(), err)
		}
		res = append(res, r)
	}
	return res, nil
}

func (ix *Index) load(b Blob, src Scanner, o LoadOptions) (LoadResult, error) {
	r := LoadResult{Name: ix.Name()}
	rebuild := func(reason string) (LoadResult, error) {
		r.Status, r.Reason = Rebuilt, reason
		return r, ix.Rebuild(src)
	}
	data, err := b.Load(ix.Name())
	if errors.Is(err, ErrNotExist) {
		return rebuild("missing")
	}
	if err != nil {
		return r, err
	}
	sd, err := ix.decodeSnapshot(data)
	if err != nil {
		return rebuild("corrupt: " + err.Error())
	}
	if sd.decl != ix.decl.hash() {
		return rebuild("declaration changed")
	}
	if err := ix.beginRebuild(); err != nil {
		return r, err
	}
	if o.HasSourceSeq && sd.hasSrcSeq && o.SourceSeq == sd.srcSeq {
		ix.commitRebuild(sd.st, &sd.seq)
		r.Status = LoadedTrusted
		return r, nil
	}
	fresh, err := ix.scanState(src)
	if err != nil {
		ix.abortRebuild()
		return r, err
	}
	if fresh.digest != sd.st.digest || len(fresh.recs) != len(sd.st.recs) {
		ix.commitRebuild(fresh, nil)
		r.Status, r.Reason = Rebuilt, "stale"
		return r, nil
	}
	ix.commitRebuild(sd.st, &sd.seq)
	r.Status = LoadedVerified
	return r, nil
}

// Check runs Index.Check for every index and returns the stale ones by name.
func (m *Manager) Check(src Scanner) (map[string]Diff, error) {
	out := map[string]Diff{}
	for _, ix := range m.indexes {
		d, err := ix.Check(src)
		if err != nil {
			return nil, err
		}
		if d.Stale() {
			out[ix.Name()] = d
		}
	}
	return out, nil
}

// Names lists the index names, sorted.
func (m *Manager) Names() []string {
	out := make([]string, 0, len(m.byName))
	for n := range m.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
