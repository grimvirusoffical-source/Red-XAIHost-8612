package link

import (
	"sort"
	"sync"

	"redx/box"
)

// Store is the storage the link engine needs. A record is a top-level
// entry (`{Player[ID=1]} = [ ... ]`) of a file, addressed by box name + ID.
//
// Implementations must be safe for concurrent use. Get must return data the
// caller may keep (the Engine clones where it mutates, but a Store must not
// let callers alias internal state). Scan must not hold a lock that Get,
// Put or Delete need while it calls fn, or must hand fn a snapshot; the
// Engine never calls the Store from inside fn, but keep this in mind.
type Store interface {
	Get(a Addr) (*box.Entry, bool, error)
	Put(a Addr, e *box.Entry) error
	// Delete removes the record and reports whether it existed.
	Delete(a Addr) (bool, error)
	// Scan visits every record of file until fn returns false.
	Scan(file string, fn func(Addr, *box.Entry) bool) error
	// Files lists the files that can be scanned (for inbound-reference checks).
	Files() []string
}

// MemStore is an in-memory Store used for tests and until the real engines
// are plugged in. It deep-copies on Put and Get.
type MemStore struct {
	mu    sync.Mutex
	files map[string]map[Addr]*box.Entry

	// DeleteErr, if set, is consulted before each Delete (test hook).
	DeleteErr func(Addr) error
	// Deletes records the order of successful deletions.
	deletes []Addr
}

func NewMemStore() *MemStore { return &MemStore{files: map[string]map[Addr]*box.Entry{}} }

func (m *MemStore) Get(a Addr) (*box.Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.files[a.File][a]
	if !ok {
		return nil, false, nil
	}
	return e.Clone(), true, nil
}

func (m *MemStore) Put(a Addr, e *box.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.files[a.File]
	if f == nil {
		f = map[Addr]*box.Entry{}
		m.files[a.File] = f
	}
	f[a] = e.Clone()
	return nil
}

func (m *MemStore) Delete(a Addr) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteErr != nil {
		if err := m.DeleteErr(a); err != nil {
			return false, err
		}
	}
	f := m.files[a.File]
	if _, ok := f[a]; !ok {
		return false, nil
	}
	delete(f, a)
	m.deletes = append(m.deletes, a)
	return true, nil
}

func (m *MemStore) Scan(file string, fn func(Addr, *box.Entry) bool) error {
	m.mu.Lock()
	type rec struct {
		a Addr
		e *box.Entry
	}
	var snap []rec
	for a, e := range m.files[file] {
		snap = append(snap, rec{a, e.Clone()})
	}
	m.mu.Unlock()
	sort.Slice(snap, func(i, j int) bool { return snap[i].a.String() < snap[j].a.String() })
	for _, r := range snap {
		if !fn(r.a, r.e) {
			break
		}
	}
	return nil
}

func (m *MemStore) Files() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for f := range m.files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of records in file.
func (m *MemStore) Len(file string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.files[file])
}

// DeleteOrder returns the successful deletions in order.
func (m *MemStore) DeleteOrder() []Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Addr(nil), m.deletes...)
}

// PutDoc parses a Box document and stores each top-level entry in file.
// It is a convenience for tests and tools.
func PutDoc(s Store, file, src string) error {
	root, err := box.ParseDocument(src)
	if err != nil {
		return err
	}
	for _, e := range root.Entries() {
		if err := s.Put(EntryAddr(file, e), e); err != nil {
			return err
		}
	}
	return nil
}
