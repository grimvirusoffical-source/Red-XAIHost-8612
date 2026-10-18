package engine

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"redx/box"
)

type fileType uint8

const (
	ftCold fileType = iota
	ftQS
	ftQSLS
)

func (f fileType) String() string { return [...]string{"cold", "qs", "qsls"}[f] }

// typeOf routes by extension (longest suffix first).
func typeOf(name string) (fileType, bool) {
	switch {
	case strings.HasSuffix(name, ".red-xai-db-qs-ls"):
		return ftQSLS, true
	case strings.HasSuffix(name, ".red-xai-db-qs"):
		return ftQS, true
	case strings.HasSuffix(name, ".red-xai.db"), strings.HasSuffix(name, ".red-xai-db"):
		return ftCold, true
	}
	return 0, false
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// table is one open .red-xai* file: an in-memory Box tree guarded by an
// RWMutex, a timing wheel for expiry, and (cold / qs-ls) a persist.
type table struct {
	db   *Database
	name string
	path string
	typ  fileType

	mu     sync.RWMutex
	root   *box.Box
	wheel  wheel
	lsn    uint64
	closed bool
	failed error
	p      *persist

	// qs-ls: coalesced deltas awaiting their (N)* checkpoint interval.
	pending map[string]int64 // canonical path -> due unix-ms

	ckptMu     sync.Mutex
	ckptQueued atomic.Bool

	// counters
	nGet, nSet, nDel, nExpired, nLazy, nCoalesced, nCkpt, nTxn, nConflict atomic.Int64
	nFlushDeltas                                                          atomic.Int64
	recovered, truncatedBytes                                             int64
}

func (t *table) now() int64 { return t.db.nowMs() }

// ---- ops ---------------------------------------------------------------

func (op *Op) parsed() ([]box.Seg, error) {
	if op.segs != nil {
		return op.segs, nil
	}
	p, err := box.ParsePath(op.Path)
	if err != nil || p.File != "" || p.HasWild() {
		return nil, fmt.Errorf("%w: %q", ErrBadPath, op.Path)
	}
	op.segs = p.Segs
	return p.Segs, nil
}

type pendEvent struct {
	op    string
	segs  []box.Seg
	value box.Value
}

type schedRec struct {
	segs []box.Seg
	e    *box.Entry
	old  *box.Entry
}

// applyCtx accumulates side effects of one commit so a failing op can be
// rolled back and so versions/wheel/events happen only after success.
type applyCtx struct {
	t       *table
	undo    []func()
	touched []*box.Entry
	sched   []schedRec
	events  []pendEvent
	replay  bool
}

func (c *applyCtx) rollback() {
	for i := len(c.undo) - 1; i >= 0; i-- {
		c.undo[i]()
	}
	c.undo = nil
}

func canonSeg(e *box.Entry) box.Seg {
	k := e.EKey()
	return box.Seg{Name: k.Name, ID: k.ID, HasID: k.HasID}
}

func (c *applyCtx) applySet(op *Op) error {
	segs, err := op.parsed()
	if err != nil {
		return err
	}
	cur := c.t.root
	canon := make([]box.Seg, 0, len(segs))
	var chain []*box.Entry
	for i, s := range segs {
		e := cur.Lookup(s)
		if i == len(segs)-1 {
			ne := &box.Entry{Key: s.Name, Value: op.Value}
			if e != nil {
				ne.Attrs = e.Attrs
			} else if s.HasID {
				ne.Attrs = []box.Attr{{Name: "ID", Value: box.Int(s.ID)}}
			}
			ne.TTL.Kind, ne.TTL.Seconds = op.TTLKind, op.TTLSeconds
			ne.TTL.SetExpiresAt(op.ExpiresAtMs)
			if e != nil {
				ne.Ver = e.Ver
			}
			old := cur.Put(ne)
			parent := cur
			c.undo = append(c.undo, func() {
				if old != nil {
					parent.Put(old)
				} else {
					parent.Remove(ne)
				}
			})
			chain = append(chain, ne)
			canon = append(canon, canonSeg(ne))
			c.touched = append(c.touched, chain...)
			c.sched = append(c.sched, schedRec{segs: canon, e: ne, old: old})
			c.events = append(c.events, pendEvent{op: "set", segs: canon, value: ne.Value})
			if ne.Value.Kind == box.KindBox {
				c.collectTree(canon, ne.Value.Box)
			}
			return nil
		}
		if e == nil {
			ne := &box.Entry{Key: s.Name, Value: box.BoxValue(box.NewBox())}
			if s.HasID {
				ne.Attrs = []box.Attr{{Name: "ID", Value: box.Int(s.ID)}}
			}
			cur.Put(ne)
			parent := cur
			c.undo = append(c.undo, func() { parent.Remove(ne) })
			e = ne
		} else if e.Value.Kind != box.KindBox {
			return fmt.Errorf("%w: %s is a %s", ErrNotBox, box.SegsString(append(canon, canonSeg(e))), e.Value.Kind)
		} else if e.Value.Box == nil {
			e.Value.Box = box.NewBox()
		}
		chain = append(chain, e)
		canon = append(canon, canonSeg(e))
		cur = e.Value.Box
	}
	return fmt.Errorf("%w: empty path", ErrBadPath)
}

// collectTree schedules expiring entries nested in a freshly written value.
func (c *applyCtx) collectTree(prefix []box.Seg, b *box.Box) {
	if b == nil {
		return
	}
	for _, e := range b.Entries() {
		segs := append(append(make([]box.Seg, 0, len(prefix)+1), prefix...), canonSeg(e))
		if e.TTL.ExpiresAt() > 0 {
			c.sched = append(c.sched, schedRec{segs: segs, e: e})
		}
		if e.Value.Kind == box.KindBox {
			c.collectTree(segs, e.Value.Box)
		}
	}
}

func (c *applyCtx) applyDelete(op *Op, kind string) error {
	segs, err := op.parsed()
	if err != nil {
		return err
	}
	cur := c.t.root
	var chain []*box.Entry
	var parents []*box.Box
	canon := make([]box.Seg, 0, len(segs))
	for _, s := range segs {
		if cur == nil {
			return fmt.Errorf("%w: %s", ErrNotFound, op.Path)
		}
		e := cur.Lookup(s)
		if e == nil {
			if kind == "expire" || c.replay {
				return nil // idempotent
			}
			return fmt.Errorf("%w: %s", ErrNotFound, op.Path)
		}
		parents = append(parents, cur)
		chain = append(chain, e)
		canon = append(canon, canonSeg(e))
		if e.Value.Kind == box.KindBox {
			cur = e.Value.Box
		} else {
			cur = nil
		}
	}
	last := len(chain) - 1
	rm := func(i int) {
		e, p := chain[i], parents[i]
		p.Remove(e)
		c.undo = append(c.undo, func() { p.Put(e) })
	}
	rm(last)
	c.events = append(c.events, pendEvent{op: kind, segs: canon})
	// qs: structural parents (no TTL of their own) vanish with their last child
	if c.t.typ == ftQS {
		for i := last - 1; i >= 0; i-- {
			e := chain[i]
			if e.Value.Kind == box.KindBox && e.Value.Box.Len() == 0 && e.TTL.Kind == box.TTLNone {
				rm(i)
				continue
			}
			break
		}
	}
	c.touched = append(c.touched, chain[:last]...)
	return nil
}

// schedule arms the wheel for e (re-using the old entry's item when the
// existing deadline is not later than the new one).
func (t *table) schedule(s schedRec) {
	e := s.e
	exp := e.TTL.ExpiresAt()
	if exp <= 0 || !e.TTL.Expiring() {
		return
	}
	if s.old != nil {
		if it, ok := s.old.TTL.Aux.(*wItem); ok && it != nil && it.e == s.old && it.at <= exp {
			it.e = e
			e.TTL.Aux = it
			s.old.TTL.Aux = nil
			return
		}
	}
	if t.db.isFollower() && !t.db.cfg.Follower {
		// still arm: promotion rescans anyway; keep it cheap
	}
	it := &wItem{at: exp, segs: s.segs, e: e}
	e.TTL.Aux = it
	t.wheel.add(it)
}

// ---- commit ------------------------------------------------------------

func (t *table) expiring() bool { return t.wheel.len() > 0 }

// withReaps prepends "expire" ops for any expired entry on a path about to
// be written, so the log carries the same deletion the memory tree makes.
func (t *table) withReaps(ops []Op, now int64) []Op {
	var out []Op
	for i := range ops {
		segs, err := ops[i].parsed()
		if err != nil {
			continue
		}
		if pre := t.expiredPrefix(segs, now); pre != nil {
			out = append(out, Op{Kind: "expire", Path: box.SegsString(pre), segs: pre})
		}
	}
	if out == nil {
		return ops
	}
	return append(out, ops...)
}

// expiredPrefix returns the canonical path of the first expired entry along
// segs, or nil.
func (t *table) expiredPrefix(segs []box.Seg, now int64) []box.Seg {
	cur := t.root
	canon := make([]box.Seg, 0, len(segs))
	for _, s := range segs {
		if cur == nil {
			return nil
		}
		e := cur.Lookup(s)
		if e == nil {
			return nil
		}
		canon = append(canon, canonSeg(e))
		if exp := e.TTL.ExpiresAt(); exp > 0 && e.TTL.Expiring() && exp <= now {
			return canon
		}
		if e.Value.Kind == box.KindBox {
			cur = e.Value.Box
		} else {
			cur = nil
		}
	}
	return nil
}

type commitMode struct {
	replica   bool
	leaderLSN uint64
	pre       func() error
}

func (t *table) fail(err error) {
	t.mu.Lock()
	if t.failed == nil {
		t.failed = fmt.Errorf("%w: %v", ErrFailed, err)
	}
	t.mu.Unlock()
}

// ---- reads ---------------------------------------------------------------

// ---- expiry ----------------------------------------------------------------

// expireBatch deletes the given entries if (and only if) they are still the
// live entry at their path and still past their deadline, logging one
// atomic frame of synthetic "expire" ops.
func (t *table) expireBatch(items []*wItem) {
	t.mu.Lock()
	if t.closed || t.failed != nil || t.db.isFollower() {
		t.mu.Unlock()
		return
	}
	now := t.now()
	ctx := &applyCtx{t: t}
	var ops []Op
	for _, it := range items {
		e, ok := t.resolveExact(it.segs)
		if !ok || e != it.e {
			continue // replaced or already deleted
		}
		exp := e.TTL.ExpiresAt()
		if exp > now {
			// sliding refresh or rewrite pushed the deadline out: re-arm
			e.TTL.Aux = nil
			t.schedule(schedRec{segs: it.segs, e: e})
			continue
		}
		op := Op{Kind: "expire", Path: box.SegsString(it.segs), segs: it.segs}
		if err := ctx.applyOp(&op); err != nil {
			ctx.rollback()
			t.mu.Unlock()
			return
		}
		ops = append(ops, op)
	}
	if len(ops) == 0 {
		t.mu.Unlock()
		return
	}
	t.nExpired.Add(int64(len(ops)))
	ticket, lsn, err := t.logLocked(ops, now, commitMode{})
	if err != nil {
		t.mu.Unlock()
		return
	}
	ctx.finish(lsn, now)
	t.afterCommitLocked(ops, now)
	hook := t.db.hook()
	var entry LogEntry
	if hook != nil {
		entry = t.entryFor(lsn, now, ops)
		hook.OnCommit(entry)
	}
	t.mu.Unlock()
	if t.p != nil {
		if err := t.p.wait(ticket); err != nil {
			t.fail(err)
			return
		}
	}
	if hook != nil {
		_ = hook.Apply(entry)
	}
	t.maybeCheckpoint()
}

// resolveExact walks canonical segs; caller holds the lock.
func (t *table) resolveExact(segs []box.Seg) (*box.Entry, bool) {
	cur := t.root
	var e *box.Entry
	for _, s := range segs {
		if cur == nil {
			return nil, false
		}
		e = cur.Lookup(s)
		if e == nil {
			return nil, false
		}
		if e.Value.Kind == box.KindBox {
			cur = e.Value.Box
		} else {
			cur = nil
		}
	}
	return e, e != nil
}

// ---- qs-ls flusher -----------------------------------------------------------

// ---- checkpoint / recovery ------------------------------------------------------

func (t *table) maybeCheckpoint() {
	lim := t.db.cfg.CheckpointBytes
	if t.p == nil || lim < 0 || t.p.walSize() < lim {
		return
	}
	if t.ckptQueued.CompareAndSwap(false, true) {
		select {
		case t.db.ckptCh <- t:
		default:
			t.ckptQueued.Store(false)
		}
	}
}

// checkpoint snapshots the tree. The WAL is rotated under the write lock so
// the snapshot and "start replaying at segment N" are consistent; the slow
// file write then happens without blocking writers.
func (t *table) checkpoint() error {
	if t.p == nil {
		return nil
	}
	t.ckptMu.Lock()
	defer t.ckptMu.Unlock()
	t.mu.Lock()
	if t.closed || t.failed != nil {
		err := t.failed
		t.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	}
	gen, err := t.p.rotate()
	if err != nil {
		t.failed = fmt.Errorf("%w: %v", ErrFailed, err)
		t.mu.Unlock()
		return err
	}
	lsn := t.lsn
	body := box.Format(t.root, box.FormatOptions{Absolute: true})
	t.mu.Unlock()
	if err := writeSnapshotBody(t.path, body, ckptMeta{lsn, gen}); err != nil {
		return err // WAL segments are all still there: nothing lost
	}
	t.p.removeBefore(gen)
	t.nCkpt.Add(1)
	return nil
}

// ---- close -----------------------------------------------------------------

// fixVersions gives every recovered entry a non-zero version (0 means
// "absent" to the optimistic-concurrency check).
func fixVersions(b *box.Box) {
	b.Each(func(e *box.Entry) bool {
		if e.Ver == 0 {
			e.Ver = 1
		}
		if e.Value.Kind == box.KindBox && e.Value.Box != nil {
			fixVersions(e.Value.Box)
		}
		return true
	})
}

func (t *table) close(crash bool) error {
	if crash {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		if t.p != nil {
			t.p.abandon()
		}
		return nil
	}
	t.flushPending(true)
	var firstErr error
	if t.p != nil && t.failed == nil {
		// leave a fresh checkpoint so the next start does not replay a WAL
		if t.p.walSize() > walHeaderLen {
			if err := t.checkpoint(); err != nil {
				firstErr = err
			}
		}
	}
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	if t.p != nil {
		if err := t.p.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ---- value validation / stamping -----------------------------------------------

func (t *table) prepareOp(segs []box.Seg, v box.Value, opts SetOpts, now int64) (Op, error) {
	if opts.TTLSeconds < 0 || opts.CheckpointSeconds < 0 || (opts.Sliding && opts.TTLSeconds == 0) ||
		(opts.TTLSeconds > 0 && opts.CheckpointSeconds > 0) || int64(opts.TTLSeconds) > 1<<31 || int64(opts.CheckpointSeconds) > 1<<31 {
		return Op{}, fmt.Errorf("%w: %+v", ErrBadOpts, opts)
	}
	op := Op{Kind: "set", Path: box.SegsString(segs), segs: segs}
	switch t.typ {
	case ftCold:
		if opts.TTLSeconds > 0 || opts.CheckpointSeconds > 0 {
			return Op{}, fmt.Errorf("%w: .red-xai.db entries are permanent (no TTL/checkpoint markers)", ErrBadOpts)
		}
	case ftQS:
		if opts.CheckpointSeconds > 0 {
			return Op{}, fmt.Errorf("%w: (N)* checkpoints belong to .red-xai-db-qs-ls", ErrBadOpts)
		}
		if opts.TTLSeconds == 0 {
			if d := t.db.cfg.DefaultTTLSeconds; d > 0 {
				opts.TTLSeconds = d
			} else {
				return Op{}, ErrTTLRequired
			}
		}
	}
	switch {
	case opts.CheckpointSeconds > 0:
		op.TTLKind, op.TTLSeconds = box.TTLCheckpoint, opts.CheckpointSeconds
	case opts.TTLSeconds > 0 && opts.Sliding:
		op.TTLKind, op.TTLSeconds = box.TTLSliding, opts.TTLSeconds
		op.ExpiresAtMs = now + int64(opts.TTLSeconds)*1000
	case opts.TTLSeconds > 0:
		op.TTLKind, op.TTLSeconds = box.TTLFixed, opts.TTLSeconds
		op.ExpiresAtMs = now + int64(opts.TTLSeconds)*1000
	}
	nv, err := t.sanitize(v, now)
	if err != nil {
		return Op{}, err
	}
	op.Value = nv
	return op, nil
}

// sanitize deep-copies v (the store never aliases caller memory), rejects
// values that could not survive a checkpoint round-trip, and stamps absolute
// expiries on nested markers.
func (t *table) sanitize(v box.Value, now int64) (box.Value, error) {
	switch v.Kind {
	case box.KindNell, box.KindBool:
		return v, nil
	case box.KindString:
		if !utf8.ValidString(v.Str) {
			return v, fmt.Errorf("%w: string is not valid UTF-8", ErrInvalidValue)
		}
		return v, nil
	case box.KindNumber:
		if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) {
			return v, fmt.Errorf("%w: NaN/Inf numbers are not representable", ErrInvalidValue)
		}
		return v, nil
	case box.KindArray:
		out := make([]box.Value, len(v.Arr))
		for i, x := range v.Arr {
			nx, err := t.sanitize(x, now)
			if err != nil {
				return v, err
			}
			out[i] = nx
		}
		return box.Array(out...), nil
	case box.KindLink:
		return v.Clone(), nil
	case box.KindBox:
		nb := box.NewBox()
		if v.Box == nil {
			return box.BoxValue(nb), nil
		}
		nb.HasFlag, nb.Flag = v.Box.HasFlag, v.Box.Flag
		for _, a := range v.Box.Meta {
			if !identRe.MatchString(a.Name) {
				return v, fmt.Errorf("%w: bad metadata name %q", ErrInvalidValue, a.Name)
			}
			nb.Meta = append(nb.Meta, box.Attr{Name: a.Name, Value: a.Value.Clone()})
		}
		for _, e := range v.Box.Entries() {
			if !identRe.MatchString(e.Key) {
				return v, fmt.Errorf("%w: bad key %q", ErrInvalidValue, e.Key)
			}
			ne := &box.Entry{Key: e.Key, Attrs: make([]box.Attr, 0, len(e.Attrs))}
			for _, a := range e.Attrs {
				if !identRe.MatchString(a.Name) {
					return v, fmt.Errorf("%w: bad attribute name %q", ErrInvalidValue, a.Name)
				}
				av, err := t.sanitize(a.Value, now)
				if err != nil {
					return v, err
				}
				ne.Attrs = append(ne.Attrs, box.Attr{Name: a.Name, Value: av})
			}
			if len(ne.Attrs) == 0 {
				ne.Attrs = nil
			}
			nv, err := t.sanitize(e.Value, now)
			if err != nil {
				return v, err
			}
			ne.Value = nv
			k, s := e.TTL.Kind, e.TTL.Seconds
			switch {
			case k == box.TTLNone:
			case t.typ == ftCold:
				return v, fmt.Errorf("%w: TTL marker on %s inside a .red-xai.db value", ErrBadOpts, e.Key)
			case k == box.TTLCheckpoint && t.typ == ftQS:
				return v, fmt.Errorf("%w: (N)* marker on %s inside a .red-xai-db-qs value", ErrBadOpts, e.Key)
			case k == box.TTLCheckpoint:
				ne.TTL.Kind, ne.TTL.Seconds = k, s
			default:
				ne.TTL.Kind, ne.TTL.Seconds = k, s
				if exp := e.TTL.ExpiresAt(); exp > 0 {
					ne.TTL.SetExpiresAt(exp)
				} else {
					ne.TTL.SetExpiresAt(now + int64(s)*1000)
				}
			}
			nb.Put(ne)
		}
		return box.BoxValue(nb), nil
	}
	return v, fmt.Errorf("%w: unknown kind", ErrInvalidValue)
}

func baseName(p string) string { return filepath.Base(p) }
