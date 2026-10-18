package engine

import (
	"errors"
	"sort"
	"sync"

	"redx/box"
	"redx/engine/link"
	"redx/engine/pulse"
)

// RecordStore adapts a Database to link.Store (and therefore also to
// pulse.Store, which is a subset). A record is a top-level entry of a file,
// addressed by box name + ID (link.Addr).
//
// Notes:
//   - Put writes through the normal Set path, so WAL/durability, TTL
//     markers carried by the entry (`(N)`, `(~N)`, `(N)*`) and subscriber
//     events all behave exactly as for Store.Set. Extra attributes on the
//     entry other than ID are not stored.
//   - Get/Scan hide records whose TTL has elapsed (they are purged by the
//     sweeper, not by these reads).
//   - Each call is atomic on its own; a sequence of calls is not (link's
//     cascade delete is documented as non-atomic).
type RecordStore struct{ db *Database }

var (
	_ link.Store  = (*RecordStore)(nil)
	_ pulse.Store = (*RecordStore)(nil)
)

// Records returns the link.Store/pulse.Store view of the database.
func (db *Database) Records() *RecordStore { return &RecordStore{db: db} }

func (r *RecordStore) table(file string) *table {
	r.db.mu.RLock()
	defer r.db.mu.RUnlock()
	return r.db.tables[file]
}

func addrSeg(a link.Addr) box.Seg {
	return box.Seg{Name: a.Box, ID: a.ID, HasID: a.HasID}
}

func (r *RecordStore) Get(a link.Addr) (*box.Entry, bool, error) {
	if r.db.closed.Load() {
		return nil, false, ErrClosed
	}
	t := r.table(a.File)
	if t == nil {
		return nil, false, nil
	}
	now := t.now()
	t.mu.RLock()
	defer t.mu.RUnlock()
	e := t.root.Lookup(addrSeg(a))
	if e == nil || expired(e, now) {
		return nil, false, nil
	}
	return e.Clone(), true, nil
}

func expired(e *box.Entry, now int64) bool {
	exp := e.TTL.ExpiresAt()
	return exp > 0 && e.TTL.Expiring() && exp <= now
}

func (r *RecordStore) Put(a link.Addr, e *box.Entry) error {
	if r.db.closed.Load() {
		return ErrClosed
	}
	if r.db.isFollower() {
		return ErrFollower
	}
	t, err := r.db.route(box.Path{File: a.File, Segs: []box.Seg{addrSeg(a)}}, true)
	if err != nil {
		return err
	}
	var opts SetOpts
	switch e.TTL.Kind {
	case box.TTLFixed:
		opts.TTLSeconds = e.TTL.Seconds
	case box.TTLSliding:
		opts.TTLSeconds, opts.Sliding = e.TTL.Seconds, true
	case box.TTLCheckpoint:
		opts.CheckpointSeconds = e.TTL.Seconds
	}
	t.nSet.Add(1)
	op, err := t.prepareOp([]box.Seg{addrSeg(a)}, e.Value, opts, t.now())
	if err != nil {
		return err
	}
	return t.commit([]Op{op}, commitMode{})
}

func (r *RecordStore) Delete(a link.Addr) (bool, error) {
	if r.db.closed.Load() {
		return false, ErrClosed
	}
	if r.db.isFollower() {
		return false, ErrFollower
	}
	t := r.table(a.File)
	if t == nil {
		return false, nil
	}
	t.nDel.Add(1)
	seg := addrSeg(a)
	err := t.commit([]Op{{Kind: "delete", Path: box.SegsString([]box.Seg{seg}), segs: []box.Seg{seg}}}, commitMode{})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Scan visits a snapshot of the file's records in document order. fn runs
// without any engine lock held.
func (r *RecordStore) Scan(file string, fn func(link.Addr, *box.Entry) bool) error {
	if r.db.closed.Load() {
		return ErrClosed
	}
	t := r.table(file)
	if t == nil {
		return nil
	}
	now := t.now()
	t.mu.RLock()
	es := t.root.Entries()
	snap := make([]*box.Entry, 0, len(es))
	for _, e := range es {
		if !expired(e, now) {
			snap = append(snap, e.Clone())
		}
	}
	t.mu.RUnlock()
	for _, e := range snap {
		if !fn(link.EntryAddr(file, e), e) {
			break
		}
	}
	return nil
}

// Files lists the open files (sorted).
func (r *RecordStore) Files() []string {
	fs := r.db.Files()
	out := make([]string, 0, len(fs))
	for n := range fs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------

// NewLinkEngine returns a link engine (.red-xai-db-link semantics) over the
// real storage engines.
func (db *Database) NewLinkEngine(opt link.Options) *link.Engine {
	return link.New(db.Records(), opt)
}

// NewPulseEngine returns a pulse rule engine whose records live in the
// database. Rules run for writes made through the returned engine's
// Put/Set (that is where the before/after state is known); direct
// Database.Set calls do not run rules.
func (db *Database) NewPulseEngine(cfg pulse.Config) *pulse.Engine {
	return pulse.New(db.Records(), cfg)
}

// BridgeChanges forwards every Database.Subscribe event for path ("*" for
// all) to a pulse bus as a KindChange event (topic "change", or "change.delete" / "change.expire"), so live clients
// that subscribe to the bus see raw storage writes (including TTL expiry,
// op "expire") next to rule events. Only changes to top-level records map to
// an Addr; Data is a snapshot of the record after the change (nil after a
// delete/expire). Events carry Seq 0 (Seq is assigned by the pulse engine
// for its own events). Do not combine with pulse.Config.PublishChanges
// unless duplicates for writes made through pulse are acceptable.
// The returned stop func ends the bridge.
func (db *Database) BridgeChanges(path string, bus *pulse.Bus) (stop func()) {
	ch, cancel := db.Subscribe(path)
	done := make(chan struct{})
	var once sync.Once
	rs := db.Records()
	go func() {
		defer close(done)
		for ev := range ch {
			p, err := box.ParsePath(ev.Path)
			if err != nil || len(p.Segs) == 0 {
				continue
			}
			s := p.Segs[0]
			a := link.Addr{File: p.File, Box: s.Name, ID: s.ID, HasID: s.HasID}
			var data *box.Entry
			if ev.Op == "set" {
				if rec, ok, _ := rs.Get(a); ok {
					data = rec
				}
			}
			topic := pulse.TopicChange
			if ev.Op != "set" {
				topic += "." + ev.Op // change.delete, change.expire
			}
			bus.Publish(pulse.Event{Time: ev.At, Kind: pulse.KindChange, Topic: topic, Addr: a, Data: data})
		}
	}()
	return func() { once.Do(func() { cancel(); <-done }) }
}
