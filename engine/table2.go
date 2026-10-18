package engine

import (
	"fmt"
	"os"
	"strings"
	"time"

	"redx/box"
)

// ---- apply / finish ----------------------------------------------------------

func (c *applyCtx) applyOp(op *Op) error {
	switch op.Kind {
	case "set":
		return c.applySet(op)
	case "delete":
		return c.applyDelete(op, "delete")
	case "expire":
		return c.applyDelete(op, "expire")
	case "touch":
		segs, err := op.parsed()
		if err != nil {
			return err
		}
		e, ok := c.t.resolveExact(segs)
		if !ok || !e.TTL.Expiring() {
			return nil // idempotent: the entry is gone or was rewritten
		}
		oldExp, oldLog := e.TTL.ExpiresAt(), e.TTL.LoggedAt()
		e.TTL.SetExpiresAt(op.ExpiresAtMs)
		e.TTL.SetLoggedAt(op.ExpiresAtMs)
		c.undo = append(c.undo, func() { e.TTL.SetExpiresAt(oldExp); e.TTL.SetLoggedAt(oldLog) })
		return nil
	}
	return fmt.Errorf("engine: unknown op kind %q", op.Kind)
}

// finish runs after the commit point: versions, wheel scheduling, events.
func (c *applyCtx) finish(lsn uint64, atMs int64) {
	t := c.t
	if lsn == 0 {
		lsn = 1
	}
	for _, e := range c.touched {
		e.Ver = lsn
	}
	for _, s := range c.sched {
		s.e.TTL.SetLoggedAt(s.e.TTL.ExpiresAt())
		t.schedule(s)
	}
	if len(c.events) > 0 {
		t.db.publish(t.name, c.events, atMs)
	}
}

// ---- commit ------------------------------------------------------------------

func (t *table) entryFor(lsn uint64, now int64, ops []Op) LogEntry {
	return LogEntry{History: t.db.HistoryID(), LSN: lsn, File: t.name, At: time.UnixMilli(now), Ops: ops}
}

func (t *table) commit(ops []Op, m commitMode) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	if t.failed != nil {
		err := t.failed
		t.mu.Unlock()
		return err
	}
	if m.replica && m.leaderLSN > 0 {
		switch {
		case m.leaderLSN <= t.lsn: // already applied: idempotent no-op
			t.mu.Unlock()
			return nil
		case m.leaderLSN != t.lsn+1:
			have := t.lsn
			t.mu.Unlock()
			return fmt.Errorf("%w: %s has LSN %d, entry is %d", ErrNeedResync, t.name, have, m.leaderLSN)
		}
	}
	if m.pre != nil {
		if err := m.pre(); err != nil {
			t.mu.Unlock()
			return err
		}
	}
	now := t.now()
	if !m.replica && !t.db.isFollower() && t.expiring() {
		ops = t.withReaps(ops, now)
	}
	ctx := &applyCtx{t: t, replay: m.replica}
	for i := range ops {
		if err := ctx.applyOp(&ops[i]); err != nil {
			ctx.rollback()
			t.mu.Unlock()
			return err
		}
	}
	ticket, lsn, err := t.logLocked(ops, now, m)
	if err != nil {
		t.mu.Unlock()
		return err
	}
	ctx.finish(lsn, now)
	if !m.replica {
		t.afterCommitLocked(ops, now)
	}
	var entry LogEntry
	hook := t.db.hook()
	if hook != nil {
		entry = t.entryFor(lsn, now, ops)
		hook.OnCommit(entry)
	}
	t.mu.Unlock()
	if t.p != nil {
		if err := t.p.wait(ticket); err != nil {
			t.fail(err)
			return err
		}
	}
	if hook != nil && !m.replica {
		if err := hook.Apply(entry); err != nil {
			return err
		}
	}
	t.maybeCheckpoint()
	return nil
}

// logLocked assigns the LSN (every commit gets its own; a replica adopts the
// leader's) and appends the journal frame. qs-ls sets that carry the (N)*
// marker are not journaled here: they are coalesced into t.pending and
// written by the flusher (replicas journal everything at once so their LSN
// is always durable).
func (t *table) logLocked(ops []Op, now int64, m commitMode) (ticket, lsn uint64, err error) {
	frameOps := ops
	if t.typ == ftQSLS && !m.replica {
		frameOps = frameOps[:0:0]
		for i := range ops {
			if ops[i].Kind == "set" && ops[i].TTLKind == box.TTLCheckpoint {
				continue
			}
			frameOps = append(frameOps, ops[i])
		}
	}
	if m.replica && m.leaderLSN > 0 {
		t.lsn = m.leaderLSN
	} else {
		t.lsn++
	}
	lsn = t.lsn
	if len(frameOps) > 0 && t.p != nil {
		ticket, err = t.p.append(encodeFrame(lsn, now, frameOps))
		if err != nil {
			t.failed = fmt.Errorf("%w: %v", ErrFailed, err)
			return 0, 0, t.failed
		}
	}
	return ticket, lsn, nil
}

// afterCommitLocked maintains the qs-ls coalescing set.
func (t *table) afterCommitLocked(ops []Op, now int64) {
	if t.typ != ftQSLS {
		return
	}
	for i := range ops {
		op := &ops[i]
		switch {
		case op.Kind == "touch":
		case op.Kind == "set" && op.TTLKind == box.TTLCheckpoint:
			if t.pending == nil {
				t.pending = make(map[string]int64)
			}
			if _, dup := t.pending[op.Path]; dup {
				t.nCoalesced.Add(1)
			} else {
				t.pending[op.Path] = now + int64(op.TTLSeconds)*1000
			}
		case op.Kind == "set":
			delete(t.pending, op.Path) // superseded by an immediate write
		default:
			if len(t.pending) > 0 {
				prefix := op.Path
				for k := range t.pending {
					if k == prefix || strings.HasPrefix(k, prefix+".") || strings.HasPrefix(k, prefix+"[") {
						delete(t.pending, k)
					}
				}
			}
		}
	}
}

// ---- reads ---------------------------------------------------------------------

type touchRec struct {
	segs []box.Seg
	e    *box.Entry
}

// slide pushes out the deadline of every sliding entry in chain (a read hit
// the leaf). Deadline moves of at least 10% of the TTL beyond what was last
// journaled are collected so they can be logged/replicated as "touch" ops.
func slide(chain []*box.Entry, now int64, touches *[]touchRec) {
	for i, e := range chain {
		if e.TTL.Kind != box.TTLSliding {
			continue
		}
		ttlMs := int64(e.TTL.Seconds) * 1000
		nexp := now + ttlMs
		if nexp > e.TTL.ExpiresAt() {
			e.TTL.SetExpiresAt(nexp)
		}
		if nexp-e.TTL.LoggedAt() >= ttlMs/10 {
			segs := make([]box.Seg, i+1)
			for k := 0; k <= i; k++ {
				segs[k] = canonSeg(chain[k])
			}
			*touches = append(*touches, touchRec{segs, e})
		}
	}
}

func (t *table) get(segs []box.Seg) (box.Value, error) {
	t.nGet.Add(1)
	now := t.now()
	follower := t.db.isFollower()
	t.mu.RLock()
	if t.closed {
		t.mu.RUnlock()
		return box.Value{}, ErrClosed
	}
	cur := t.root
	var (
		expiredAt []box.Seg
		expiredE  *box.Entry
		res       box.Value
		found     bool
		chain     []*box.Entry
	)
	for i, s := range segs {
		var e *box.Entry
		if cur != nil {
			e = cur.Lookup(s)
		}
		if e == nil {
			break
		}
		if expired(e, now) {
			expiredAt = make([]box.Seg, 0, i+1)
			for _, c := range chain {
				expiredAt = append(expiredAt, canonSeg(c))
			}
			expiredAt = append(expiredAt, canonSeg(e))
			expiredE = e
			break
		}
		chain = append(chain, e)
		if i == len(segs)-1 {
			res = e.Value.Clone()
			found = true
		} else if e.Value.Kind == box.KindBox {
			cur = e.Value.Box
		} else {
			cur = nil
		}
	}
	var touches []touchRec
	if found && !follower { // only successful reads slide, and never on a replica
		slide(chain, now, &touches)
	}
	t.mu.RUnlock()
	if expiredE != nil {
		t.expireLazy(expiredAt, expiredE)
		return box.Nell(), nil
	}
	if len(touches) > 0 {
		t.logTouches(touches)
	}
	if !found {
		if t.typ != ftCold {
			return box.Nell(), nil // in qs / qs-ls an absent path reads as NELL
		}
		return box.Value{}, fmt.Errorf("%w: %s", ErrNotFound, box.SegsString(segs))
	}
	return res, nil
}

// expireLazy purges an entry noticed expired during a read.
func (t *table) expireLazy(segs []box.Seg, e *box.Entry) {
	if t.db.isFollower() {
		return // the leader's logged delete will remove it
	}
	t.nLazy.Add(1)
	t.expireBatch([]*wItem{{segs: segs, e: e}})
}

func (t *table) query(segs []box.Seg) []box.Value {
	now := t.now()
	follower := t.db.isFollower()
	t.mu.RLock()
	chains := box.MatchChains(t.root, segs, func(e *box.Entry) bool { return expired(e, now) })
	out := make([]box.Value, 0, len(chains))
	var touches []touchRec
	for _, ch := range chains {
		out = append(out, ch[len(ch)-1].Value.Clone())
		if !follower {
			slide(ch, now, &touches)
		}
	}
	t.mu.RUnlock()
	if len(touches) > 0 {
		t.logTouches(touches)
	}
	return out
}

// logTouches journals (and hands to the cluster hook) sliding-deadline moves.
func (t *table) logTouches(rs []touchRec) {
	t.mu.Lock()
	if t.closed || t.failed != nil || t.db.isFollower() {
		t.mu.Unlock()
		return
	}
	now := t.now()
	var ops []Op
	for _, r := range rs {
		cur, ok := t.resolveExact(r.segs)
		if !ok || cur != r.e || expired(cur, now) {
			continue
		}
		exp := cur.TTL.ExpiresAt()
		if exp <= cur.TTL.LoggedAt() {
			continue // another reader already logged this move
		}
		cur.TTL.SetLoggedAt(exp)
		ops = append(ops, Op{Kind: "touch", Path: box.SegsString(r.segs), segs: r.segs, ExpiresAtMs: exp})
	}
	if len(ops) == 0 {
		t.mu.Unlock()
		return
	}
	ticket, lsn, err := t.logLocked(ops, now, commitMode{})
	if err != nil {
		t.mu.Unlock()
		return
	}
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
}

// ---- sweeping ------------------------------------------------------------------

// sweep advances the timing wheel and expires due entries, taking the lock
// in bounded batches so writers and readers are never stalled for long.
func (t *table) sweep() {
	if t.db.isFollower() {
		return
	}
	t.mu.Lock()
	if t.closed || t.wheel.len() == 0 {
		t.mu.Unlock()
		return
	}
	now := t.now()
	if tickOf(now)+100 < t.wheel.cur { // the clock jumped back by more than a second
		t.rearmLocked(now)
	}
	// look one tick ahead so items due "right now" fire; expireBatch
	// re-checks the exact deadline and re-arms anything that is early
	ready := t.wheel.advance(now+wheelTickMs, nil)
	t.mu.Unlock()
	const batch = 512
	for len(ready) > 0 {
		n := batch
		if n > len(ready) {
			n = len(ready)
		}
		t.expireBatch(ready[:n])
		ready = ready[n:]
	}
}

// rearm rebuilds the wheel from the tree (after recovery or promotion).
func (t *table) rearm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rearmLocked(t.now())
}

func (t *table) rearmLocked(now int64) {
	t.wheel = wheel{}
	t.wheel.start(now)
	var walk func(prefix []box.Seg, b *box.Box)
	walk = func(prefix []box.Seg, b *box.Box) {
		for _, e := range b.Entries() {
			segs := append(append(make([]box.Seg, 0, len(prefix)+1), prefix...), canonSeg(e))
			e.TTL.Aux = nil
			if e.TTL.Expiring() && e.TTL.ExpiresAt() > 0 {
				e.TTL.SetLoggedAt(e.TTL.ExpiresAt())
				it := &wItem{at: e.TTL.ExpiresAt(), segs: segs, e: e}
				e.TTL.Aux = it
				t.wheel.add(it)
			}
			if e.Value.Kind == box.KindBox && e.Value.Box != nil {
				walk(segs, e.Value.Box)
			}
		}
	}
	walk(nil, t.root)
}

// ---- qs-ls flusher -----------------------------------------------------------

// flushPending writes every coalesced delta whose interval has elapsed (all
// of them when force) as ONE journal frame holding only the latest value of
// each path. The frame carries the current LSN without advancing it (the
// deltas were already numbered when they were committed in RAM).
func (t *table) flushPending(force bool) {
	if t.typ != ftQSLS || t.p == nil {
		return
	}
	t.mu.Lock()
	if t.closed && !force || t.failed != nil || len(t.pending) == 0 {
		t.mu.Unlock()
		return
	}
	now := t.now()
	var ops []Op
	for path, due := range t.pending {
		if !force && due > now {
			continue
		}
		delete(t.pending, path)
		p, err := box.ParsePath(path)
		if err != nil {
			continue
		}
		e, ok := t.resolveExact(p.Segs)
		if !ok || e.TTL.Kind != box.TTLCheckpoint {
			continue // deleted or replaced by an immediately-journaled write
		}
		ops = append(ops, Op{Kind: "set", Path: path, segs: p.Segs, Value: e.Value.Clone(),
			TTLKind: e.TTL.Kind, TTLSeconds: e.TTL.Seconds, ExpiresAtMs: e.TTL.ExpiresAt()})
	}
	if len(ops) == 0 {
		t.mu.Unlock()
		return
	}
	ticket, err := t.p.append(encodeFrame(t.lsn, now, ops))
	if err != nil {
		t.failed = fmt.Errorf("%w: %v", ErrFailed, err)
		t.mu.Unlock()
		return
	}
	t.nFlushDeltas.Add(int64(len(ops)))
	t.mu.Unlock()
	if err := t.p.wait(ticket); err != nil {
		t.fail(err)
		return
	}
	t.maybeCheckpoint()
}

// ---- recovery --------------------------------------------------------------------

func (t *table) recoverFromDisk(dur durability) error {
	_ = os.Remove(t.path + ".tmp") // an interrupted checkpoint never became visible
	root, meta, _, err := loadSnapshot(t.path)
	if err != nil {
		return err
	}
	t.root = root
	t.lsn = meta.lsn
	snapLSN := meta.lsn

	gens, err := listSegments(t.path)
	if err != nil {
		return err
	}
	var live []uint64
	for _, g := range gens {
		if g < meta.walGen {
			_ = os.Remove(segPath(t.path, g)) // already covered by the checkpoint
		} else {
			live = append(live, g)
		}
	}
	openGen := meta.walGen
	validLen := int64(-1)
	ctx := &applyCtx{t: t, replay: true}
	for i, g := range live {
		data, err := os.ReadFile(segPath(t.path, g))
		if err != nil {
			return err
		}
		_, recs, good, torn, serr := scanSegment(data, t.db.cfg.RepairWAL)
		if serr != nil {
			return fmt.Errorf("%s gen %d: %w", t.name, g, serr)
		}
		last := i == len(live)-1
		if torn && !last {
			if !t.db.cfg.RepairWAL {
				return fmt.Errorf("%w: %s has a damaged record in a non-final WAL segment (gen %d) at byte %d",
					ErrCorrupt, t.name, g, good)
			}
		}
		for _, r := range recs {
			if r.lsn <= snapLSN {
				continue // already in the checkpoint
			}
			for k := range r.ops {
				if err := ctx.applyOp(&r.ops[k]); err != nil {
					return fmt.Errorf("%w: %s replay lsn %d: %v", ErrCorrupt, t.name, r.lsn, err)
				}
			}
			if r.lsn > t.lsn {
				t.lsn = r.lsn
			}
			t.recovered++
		}
		if last {
			openGen = g
		}
		if torn {
			t.truncatedBytes += int64(len(data) - good)
			if len(data) > good && good >= walHeaderLen {
				// keep the dropped bytes for forensics, then cut the log there
				_ = os.WriteFile(fmt.Sprintf("%s.torn-%d", segPath(t.path, g), time.Now().UnixNano()), data[good:], 0o644)
			}
			if last {
				validLen = int64(good)
				if good < walHeaderLen {
					validLen = 0
				}
			}
		}
	}
	fixVersions(t.root)
	f, size, err := openSegmentForAppend(t.path, openGen, validLen)
	if err != nil {
		return err
	}
	t.p = newPersist(t.path, dur, time.Duration(t.db.cfg.AsyncFlushMS)*time.Millisecond, f, openGen, size)
	if t.truncatedBytes > 0 {
		t.db.logf("redx: %s: dropped %d bytes of torn WAL tail", t.name, t.truncatedBytes)
	}
	return nil
}

// ---- snapshots (resync) -----------------------------------------------------------

func (t *table) exportSnapshot() (string, uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return box.Format(t.root, box.FormatOptions{Absolute: true}), t.lsn
}

func (t *table) installSnapshot(root *box.Box, lsn uint64) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrClosed
	}
	if t.failed != nil {
		err := t.failed
		t.mu.Unlock()
		return err
	}
	t.root = root
	fixVersions(root)
	t.lsn = lsn
	t.pending = nil
	t.rearmLocked(t.now())
	t.mu.Unlock()
	if t.p != nil {
		return t.checkpoint()
	}
	return nil
}
