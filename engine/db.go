package engine

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"redx/box"
)

// Database is a directory of .red-xai* files behind one Store.
type Database struct {
	dir string
	cfg Config
	dur durability

	mu     sync.RWMutex
	tables map[string]*table

	subMu sync.RWMutex
	subs  map[*sub]struct{}
	nsubs atomic.Int32

	hookV    atomic.Value // holds hookBox
	follower atomic.Bool
	closed   atomic.Bool

	eventsSent, eventsDropped atomic.Int64

	histMu     sync.Mutex
	history    string // this node's replication history id
	leaderHist string // last history id seen from a leader (follower side)

	ckptCh   chan *table
	stop     chan struct{}
	wg       sync.WaitGroup
	unlockFn func()
}

type hookBox struct{ h ClusterHook }

var _ Replicable = (*Database)(nil)

var (
	openMu   sync.Mutex
	openDirs = map[string]bool{}
)

var fileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}$`)

// Open opens (creating if needed) the database directory dir.
func Open(dir string, cfg Config) (Store, error) {
	db, err := OpenDatabase(dir, cfg)
	if err != nil {
		return nil, err
	}
	return db, nil
}

// OpenDatabase is Open returning the concrete type (transactions,
// checkpoint, replication entry points).
func OpenDatabase(dir string, cfg Config) (*Database, error) {
	dur, err := parseDurability(cfg.Durability)
	if err != nil {
		return nil, err
	}
	if cfg.SweepMS <= 0 {
		cfg.SweepMS = 100
	}
	if cfg.AsyncFlushMS <= 0 {
		cfg.AsyncFlushMS = 10
	}
	if cfg.CheckpointBytes == 0 {
		cfg.CheckpointBytes = 32 << 20
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	openMu.Lock()
	if openDirs[abs] {
		openMu.Unlock()
		return nil, fmt.Errorf("engine: %s is already open in this process", abs)
	}
	openDirs[abs] = true
	openMu.Unlock()
	release := func() { openMu.Lock(); delete(openDirs, abs); openMu.Unlock() }
	unlock, err := lockDir(abs)
	if err != nil {
		release()
		return nil, err
	}
	db := &Database{dir: abs, cfg: cfg, dur: dur, tables: map[string]*table{}, subs: map[*sub]struct{}{},
		ckptCh: make(chan *table, 64), stop: make(chan struct{})}
	db.unlockFn = func() { unlock(); release() }
	db.follower.Store(cfg.Follower)
	db.loadHistory()
	if cfg.Hook != nil {
		db.hookV.Store(hookBox{cfg.Hook})
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		db.unlockFn()
		return nil, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".tmp") { // interrupted checkpoint of any generation
			if _, ok := typeOf(strings.TrimSuffix(e.Name(), ".tmp")); ok {
				_ = os.Remove(filepath.Join(abs, e.Name()))
			}
			continue
		}
		if _, ok := typeOf(e.Name()); !ok || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := db.openTable(e.Name()); err != nil {
			db.abort()
			return nil, err
		}
	}
	db.wg.Add(3)
	go db.sweepLoop()
	go db.flushLoop()
	go db.ckptLoop()
	return db, nil
}

func (db *Database) abort() {
	for _, t := range db.tables {
		_ = t.close(true)
	}
	db.unlockFn()
}

func (db *Database) logf(format string, a ...interface{}) {
	if db.cfg.Logf != nil {
		db.cfg.Logf(format, a...)
	}
}

func (db *Database) nowMs() int64 {
	if db.cfg.Clock != nil {
		return db.cfg.Clock().UnixMilli()
	}
	return time.Now().UnixMilli()
}

func (db *Database) isFollower() bool { return db.follower.Load() }

func (db *Database) hook() ClusterHook {
	if v, ok := db.hookV.Load().(hookBox); ok {
		return v.h
	}
	return nil
}

// SetClusterHook installs (or with nil removes) the replication hook.
func (db *Database) SetClusterHook(h ClusterHook) { db.hookV.Store(hookBox{h}) }

// SetFollower toggles follower mode; promotion re-arms every expiry.
func (db *Database) SetFollower(on bool) {
	was := db.follower.Swap(on)
	if was && !on {
		db.newHistory() // promotion starts a new replication history
		db.mu.RLock()
		ts := make([]*table, 0, len(db.tables))
		for _, t := range db.tables {
			ts = append(ts, t)
		}
		db.mu.RUnlock()
		for _, t := range ts {
			t.rearm()
		}
	}
}

// openTable loads (or creates) the named file. Caller must not hold db.mu.
func (db *Database) openTable(name string) (*table, error) {
	typ, ok := typeOf(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q has no .red-xai extension (.red-xai.db, .red-xai-db-qs, .red-xai-db-qs-ls)", ErrBadPath, name)
	}
	if !fileNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: bad file name %q", ErrBadPath, name)
	}
	t := &table{db: db, name: name, path: filepath.Join(db.dir, name), typ: typ, root: box.NewBox()}
	t.wheel.start(db.nowMs())
	switch typ {
	case ftQS:
		if data, err := os.ReadFile(t.path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			// seed file: parsed once, TTLs counted from now; never written back
			root, err := box.ParseDocument(string(data))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if errs := box.Validate(root, box.ModeQS); len(errs) > 0 {
				return nil, fmt.Errorf("%s: %w", name, errs[0])
			}
			t.root = root
			now := db.nowMs()
			var stamp func(b *box.Box, inherited bool)
			stamp = func(b *box.Box, _ bool) {
				b.Each(func(e *box.Entry) bool {
					if e.TTL.Expiring() && e.TTL.ExpiresAt() == 0 {
						e.TTL.SetExpiresAt(now + int64(e.TTL.Seconds)*1000)
					}
					if e.Ver == 0 {
						e.Ver = 1
					}
					if e.Value.Kind == box.KindBox && e.Value.Box != nil {
						stamp(e.Value.Box, false)
					}
					return true
				})
			}
			stamp(root, false)
			t.rearm()
		} else if os.IsNotExist(err) {
			if f, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				f.Close()
			}
		}
	default:
		_, statErr := os.Stat(t.path)
		fresh := os.IsNotExist(statErr)
		if err := t.recoverFromDisk(db.dur); err != nil {
			return nil, err
		}
		t.rearm()
		if fresh {
			if err := t.checkpoint(); err != nil {
				return nil, err
			}
		}
		db.logf("redx: opened %s (%s): %d WAL records replayed", name, typ, t.recovered)
	}
	db.mu.Lock()
	db.tables[name] = t
	db.mu.Unlock()
	return t, nil
}

// route finds the table for a parsed path. With create, an unknown file
// name carrying a RedX extension is created on the fly.
func (db *Database) route(p box.Path, create bool) (*table, error) {
	if len(p.Segs) == 0 {
		return nil, ErrBadPath
	}
	db.mu.RLock()
	if p.File != "" {
		if t, ok := db.tables[p.File]; ok {
			db.mu.RUnlock()
			return t, nil
		}
		var found []*table
		for n, t := range db.tables {
			if strings.HasPrefix(n, p.File+".") {
				found = append(found, t)
			}
		}
		db.mu.RUnlock()
		switch {
		case len(found) == 1:
			return found[0], nil
		case len(found) > 1:
			return nil, fmt.Errorf("%w: %q matches %d files; use the full file name", ErrAmbiguous, p.File, len(found))
		case create:
			if db.closed.Load() {
				return nil, ErrClosed
			}
			return db.createTable(p.File)
		}
		return nil, fmt.Errorf("%w: file %s", ErrNotFound, p.File)
	}
	// no file: find the unique file holding a top-level entry with this name
	var found []*table
	first := p.Segs[0]
	for _, t := range db.tables {
		t.mu.RLock()
		hit := first.Wild || t.root.Lookup(first) != nil
		t.mu.RUnlock()
		if hit {
			found = append(found, t)
		}
	}
	db.mu.RUnlock()
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) == 0:
		if create {
			return nil, ErrNoFile
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p.String())
	}
	return nil, ErrAmbiguous
}

func (db *Database) createTable(name string) (*table, error) {
	// serialize creation so two writers do not open the same files twice
	db.mu.Lock()
	if t, ok := db.tables[name]; ok {
		db.mu.Unlock()
		return t, nil
	}
	db.mu.Unlock()
	createMu.Lock()
	defer createMu.Unlock()
	db.mu.RLock()
	t, ok := db.tables[name]
	db.mu.RUnlock()
	if ok {
		return t, nil
	}
	return db.openTable(name)
}

var createMu sync.Mutex

// tablesFor lists the tables a query path applies to.
func (db *Database) tablesFor(p box.Path) ([]*table, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	var out []*table
	for n, t := range db.tables {
		if p.File == "" || n == p.File || strings.HasPrefix(n, p.File+".") {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// ---- Store ---------------------------------------------------------------------

func (db *Database) Get(path string) (box.Value, error) {
	if db.closed.Load() {
		return box.Value{}, ErrClosed
	}
	p, err := box.ParsePath(path)
	if err != nil || p.HasWild() {
		return box.Value{}, fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	t, err := db.route(p, false)
	if err != nil {
		return box.Value{}, err
	}
	return t.get(p.Segs)
}

func (db *Database) Set(path string, v box.Value, opts SetOpts) error {
	if db.closed.Load() {
		return ErrClosed
	}
	if db.isFollower() {
		return ErrFollower
	}
	p, err := box.ParsePath(path)
	if err != nil || p.HasWild() {
		return fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	t, err := db.route(p, true)
	if err != nil {
		return err
	}
	t.nSet.Add(1)
	op, err := t.prepareOp(p.Segs, v, opts, t.now())
	if err != nil {
		return err
	}
	return t.commit([]Op{op}, commitMode{})
}

func (db *Database) Delete(path string) error {
	if db.closed.Load() {
		return ErrClosed
	}
	if db.isFollower() {
		return ErrFollower
	}
	p, err := box.ParsePath(path)
	if err != nil || p.HasWild() {
		return fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	t, err := db.route(p, false)
	if err != nil {
		return err
	}
	t.nDel.Add(1)
	return t.commit([]Op{{Kind: "delete", Path: box.SegsString(p.Segs), segs: p.Segs}}, commitMode{})
}

func (db *Database) Query(q string) ([]box.Value, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	p, err := box.ParsePath(q)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPath, err)
	}
	ts, err := db.tablesFor(p)
	if err != nil {
		return nil, err
	}
	out := []box.Value{}
	for _, t := range ts {
		out = append(out, t.query(p.Segs)...)
	}
	return out, nil
}

// ---- subscriptions (Pulse-lite) -------------------------------------------------

type sub struct {
	file string
	segs []box.Seg
	ch   chan Event
}

const subBuffer = 1024

func (db *Database) Subscribe(path string) (<-chan Event, func()) {
	s := &sub{ch: make(chan Event, subBuffer)}
	p, err := box.ParsePath(path)
	if err != nil {
		close(s.ch)
		return s.ch, func() {}
	}
	s.file, s.segs = p.File, p.Segs
	db.subMu.Lock()
	db.subs[s] = struct{}{}
	db.nsubs.Add(1)
	db.subMu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			db.subMu.Lock()
			if _, ok := db.subs[s]; ok {
				delete(db.subs, s)
				db.nsubs.Add(-1)
				close(s.ch)
			}
			db.subMu.Unlock()
		})
	}
}

func segMatch(sub, ev box.Seg) bool {
	if sub.Wild {
		return true
	}
	if sub.Name != ev.Name {
		return false
	}
	if sub.AnyID || !sub.HasID {
		return true
	}
	return ev.HasID && ev.ID == sub.ID
}

func (s *sub) matches(file string, segs []box.Seg) bool {
	if s.file != "" && s.file != file && !strings.HasPrefix(file, s.file+".") {
		return false
	}
	n := len(s.segs)
	if len(segs) < n {
		n = len(segs)
	}
	for i := 0; i < n; i++ {
		if !segMatch(s.segs[i], segs[i]) {
			return false
		}
	}
	return true // sub is at/above the event (descendant changed) or the event is above sub
}

// publish is called under the table lock so per-file event order equals
// commit order. Sends never block.
func (db *Database) publish(file string, evs []pendEvent, atMs int64) {
	if db.nsubs.Load() == 0 {
		return
	}
	at := time.UnixMilli(atMs)
	db.subMu.RLock()
	defer db.subMu.RUnlock()
	for i := range evs {
		ev := &evs[i]
		var out *Event
		for s := range db.subs {
			if !s.matches(file, ev.segs) {
				continue
			}
			if out == nil {
				out = &Event{Path: file + ":" + box.SegsString(ev.segs), Op: ev.op, Value: ev.value.Clone(), At: at}
			}
			select {
			case s.ch <- *out:
				db.eventsSent.Add(1)
			default:
				db.eventsDropped.Add(1)
			}
		}
	}
}

// ---- stats ---------------------------------------------------------------------

func (db *Database) Stats() map[string]float64 {
	st := map[string]float64{}
	add := func(k string, v float64) { st[k] += v }
	db.mu.RLock()
	defer db.mu.RUnlock()
	add("files", float64(len(db.tables)))
	for _, t := range db.tables {
		pre := t.typ.String() + "."
		add(pre+"files", 1)
		add(pre+"gets", float64(t.nGet.Load()))
		add(pre+"sets", float64(t.nSet.Load()))
		add(pre+"deletes", float64(t.nDel.Load()))
		add(pre+"expired", float64(t.nExpired.Load()))
		add(pre+"lazy_expired", float64(t.nLazy.Load()))
		add(pre+"checkpoints", float64(t.nCkpt.Load()))
		add(pre+"txn_commits", float64(t.nTxn.Load()))
		add(pre+"txn_conflicts", float64(t.nConflict.Load()))
		add(pre+"writes_coalesced", float64(t.nCoalesced.Load()))
		add(pre+"deltas_flushed", float64(t.nFlushDeltas.Load()))
		add(pre+"recovered_records", float64(t.recovered))
		add(pre+"wal_torn_bytes_dropped", float64(t.truncatedBytes))
		t.mu.RLock()
		st["file."+t.name+".lsn"] = float64(t.lsn)
		add(pre+"entries_top_level", float64(t.root.Len()))
		add(pre+"wheel_items", float64(t.wheel.len()))
		add(pre+"pending_deltas", float64(len(t.pending)))
		add(pre+"lsn", float64(t.lsn))
		t.mu.RUnlock()
		if t.p != nil {
			add(pre+"wal_bytes_written", float64(t.p.bytes.Load()))
			add(pre+"wal_fsyncs", float64(t.p.fsyncs.Load()))
			add(pre+"wal_batches", float64(t.p.batches.Load()))
			add(pre+"wal_frames", float64(t.p.frames.Load()))
			add(pre+"wal_size", float64(t.p.walSize()))
		}
	}
	st["subscribers"] = float64(db.nsubs.Load())
	st["events_sent"] = float64(db.eventsSent.Load())
	st["events_dropped"] = float64(db.eventsDropped.Load())
	return st
}

// Files lists the open files with their engine type.
func (db *Database) Files() map[string]string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := map[string]string{}
	for n, t := range db.tables {
		out[n] = t.typ.String()
	}
	return out
}

// ---- background work ---------------------------------------------------------

func (db *Database) tableList() []*table {
	db.mu.RLock()
	defer db.mu.RUnlock()
	ts := make([]*table, 0, len(db.tables))
	for _, t := range db.tables {
		ts = append(ts, t)
	}
	return ts
}

func (db *Database) sweepLoop() {
	defer db.wg.Done()
	tk := time.NewTicker(time.Duration(db.cfg.SweepMS) * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-db.stop:
			return
		case <-tk.C:
			db.sweepNow()
		}
	}
}

// sweepNow runs one expiry pass (also used directly by tests).
func (db *Database) sweepNow() {
	for _, t := range db.tableList() {
		t.sweep()
	}
}

func (db *Database) flushLoop() {
	defer db.wg.Done()
	tk := time.NewTicker(20 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-db.stop:
			return
		case <-tk.C:
			for _, t := range db.tableList() {
				t.flushPending(false)
			}
		}
	}
}

func (db *Database) ckptLoop() {
	defer db.wg.Done()
	for {
		select {
		case <-db.stop:
			return
		case t := <-db.ckptCh:
			if err := t.checkpoint(); err != nil {
				db.logf("redx: checkpoint %s: %v", t.name, err)
			}
			t.ckptQueued.Store(false)
		}
	}
}

// Checkpoint forces a checkpoint of every persistent file.
func (db *Database) Checkpoint() error {
	var first error
	for _, t := range db.tableList() {
		t.flushPending(true)
		if err := t.checkpoint(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ApplyEntry applies a leader's logged entry (see Replicable).
func (db *Database) ApplyEntry(e LogEntry) error {
	if db.closed.Load() {
		return ErrClosed
	}
	if e.History != "" {
		db.histMu.Lock()
		if db.leaderHist != "" && db.leaderHist != e.History {
			h := db.leaderHist
			db.histMu.Unlock()
			return fmt.Errorf("%w: history changed (%s -> %s)", ErrNeedResync, h, e.History)
		}
		db.histMu.Unlock()
	}
	t, err := db.route(box.Path{File: e.File, Segs: []box.Seg{{Name: "x"}}}, true)
	if err != nil {
		return err
	}
	ops := make([]Op, len(e.Ops))
	for i, op := range e.Ops {
		op.segs = nil
		op.Value = op.Value.Clone()
		if _, err := op.parsed(); err != nil {
			return err
		}
		ops[i] = op
	}
	if err := t.commit(ops, commitMode{replica: true, leaderLSN: e.LSN}); err != nil {
		return err
	}
	if e.History != "" {
		db.histMu.Lock()
		db.leaderHist = e.History
		db.histMu.Unlock()
	}
	return nil
}

// ExportSnapshot: see Replicable.
func (db *Database) ExportSnapshot(file string) (string, uint64, error) {
	t := func() *table { db.mu.RLock(); defer db.mu.RUnlock(); return db.tables[file] }()
	if t == nil {
		return "", 0, fmt.Errorf("%w: file %s", ErrNotFound, file)
	}
	doc, lsn := t.exportSnapshot()
	return doc, lsn, nil
}

// InstallSnapshot: see Replicable. Subscribers are not notified of the
// individual changes a snapshot implies.
func (db *Database) InstallSnapshot(file, doc string, lsn uint64, history string) error {
	if db.closed.Load() {
		return ErrClosed
	}
	root, err := box.ParseDocument(doc)
	if err != nil {
		return fmt.Errorf("engine: snapshot: %w", err)
	}
	t, err := db.route(box.Path{File: file, Segs: []box.Seg{{Name: "x"}}}, true)
	if err != nil {
		return err
	}
	if err := t.installSnapshot(root, lsn); err != nil {
		return err
	}
	if history != "" {
		db.histMu.Lock()
		db.leaderHist = history
		db.histMu.Unlock()
	}
	return nil
}

// HistoryID is this node's replication history id (persisted in <dir>/REPLID;
// regenerated when a follower is promoted).
func (db *Database) HistoryID() string {
	db.histMu.Lock()
	defer db.histMu.Unlock()
	return db.history
}

func (db *Database) loadHistory() {
	if b, err := os.ReadFile(filepath.Join(db.dir, "REPLID")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		db.history = strings.TrimSpace(string(b))
		return
	}
	db.newHistory()
}

func (db *Database) newHistory() {
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	db.histMu.Lock()
	db.history = id
	db.histMu.Unlock()
	_ = os.WriteFile(filepath.Join(db.dir, "REPLID"), []byte(id+"\n"), 0o644)
}

// Close flushes everything, leaves fresh checkpoints and releases the directory.
func (db *Database) Close() error { return db.closeWith(false) }

func (db *Database) closeWith(crash bool) error {
	if !db.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}
	close(db.stop)
	db.wg.Wait()
	var first error
	db.subMu.Lock()
	for s := range db.subs {
		delete(db.subs, s)
		close(s.ch)
	}
	db.nsubs.Store(0)
	db.subMu.Unlock()
	for _, t := range db.tableList() {
		if err := t.close(crash); err != nil && first == nil {
			first = err
		}
	}
	db.unlockFn()
	return first
}

// crash simulates kill -9: nothing is flushed or checkpointed.
func (db *Database) crash() { _ = db.closeWith(true) }
