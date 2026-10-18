package engine

import (
	"fmt"
	"sync"

	"redx/box"
)

// Txn is an optimistic multi-Box transaction on ONE .red-xai.db file.
// Reads record the version of the entry they saw; Commit validates under the
// file lock that none changed (every write bumps the version of the written
// entry and all its ancestors to the commit LSN) and then applies all writes
// as a single WAL frame: all-or-nothing, also across crashes.
// Read-your-writes only holds for exactly the same path string. Once a
// transaction has touched a file, unprefixed paths resolve inside that file;
// name the file explicitly on the first access of a path that may not exist yet.
type Txn struct {
	mu      sync.Mutex
	db      *Database
	t       *table
	reads   map[string]txnRead
	writes  []Op
	overlay map[string]*Op
	done    bool
}

type txnRead struct {
	segs []box.Seg
	ver  uint64
}

// Begin starts a transaction.
func (db *Database) Begin() *Txn {
	return &Txn{db: db, reads: map[string]txnRead{}, overlay: map[string]*Op{}}
}

func (x *Txn) bind(path string, create bool) (box.Path, error) {
	p, err := box.ParsePath(path)
	if err != nil || p.HasWild() {
		return p, fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	var t *table
	if x.t != nil && p.File == "" {
		t = x.t // a bound transaction resolves unprefixed paths inside its file
	} else {
		var err error
		if t, err = x.db.route(p, create); err != nil {
			return p, err
		}
	}
	if t.typ != ftCold {
		return p, ErrTxnUnsupport
	}
	if x.t != nil && x.t != t {
		return p, ErrCrossFile
	}
	x.t = t
	return p, nil
}

func (x *Txn) Get(path string) (box.Value, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done {
		return box.Value{}, ErrTxnDone
	}
	p, err := x.bind(path, false)
	if err != nil {
		return box.Value{}, err
	}
	key := box.SegsString(p.Segs)
	if op, ok := x.overlay[key]; ok {
		if op.Kind == "delete" {
			return box.Value{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return op.Value.Clone(), nil
	}
	t := x.t
	t.mu.RLock()
	e, ok := t.resolveExact(p.Segs)
	var v box.Value
	var ver uint64
	if ok {
		v, ver = e.Value.Clone(), e.Ver
	}
	t.mu.RUnlock()
	if _, seen := x.reads[key]; !seen {
		x.reads[key] = txnRead{p.Segs, ver}
	}
	if !ok {
		return box.Value{}, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return v, nil
}

func (x *Txn) Set(path string, v box.Value, opts SetOpts) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done {
		return ErrTxnDone
	}
	p, err := x.bind(path, true)
	if err != nil {
		return err
	}
	op, err := x.t.prepareOp(p.Segs, v, opts, x.t.now())
	if err != nil {
		return err
	}
	x.writes = append(x.writes, op)
	x.overlay[op.Path] = &x.writes[len(x.writes)-1]
	return nil
}

func (x *Txn) Delete(path string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done {
		return ErrTxnDone
	}
	p, err := x.bind(path, false)
	if err != nil {
		return err
	}
	op := Op{Kind: "delete", Path: box.SegsString(p.Segs), segs: p.Segs}
	x.writes = append(x.writes, op)
	x.overlay[op.Path] = &x.writes[len(x.writes)-1]
	return nil
}

// Rollback discards the transaction.
func (x *Txn) Rollback() {
	x.mu.Lock()
	x.done = true
	x.mu.Unlock()
}

// Commit validates the read set and applies the write set atomically.
// ErrConflict means another commit changed something this transaction read;
// retry the whole transaction.
func (x *Txn) Commit() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done {
		return ErrTxnDone
	}
	x.done = true
	if x.t == nil {
		return nil
	}
	t := x.t
	if x.db.isFollower() && len(x.writes) > 0 {
		return ErrFollower
	}
	pre := func() error {
		for key, r := range x.reads {
			var cur uint64
			if e, ok := t.resolveExact(r.segs); ok {
				cur = e.Ver
			}
			if cur != r.ver {
				t.nConflict.Add(1)
				return fmt.Errorf("%w: %s changed (version %d -> %d)", ErrConflict, key, r.ver, cur)
			}
		}
		return nil
	}
	if len(x.writes) == 0 {
		t.mu.RLock()
		defer t.mu.RUnlock()
		return pre()
	}
	t.nTxn.Add(1)
	return t.commit(x.writes, commitMode{pre: pre})
}
