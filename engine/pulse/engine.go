package pulse

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"redx/box"
	"redx/engine/link"
)

// Store is the storage the pulse engine needs. It is a subset of link.Store,
// so any link.Store (including link.MemStore) works.
type Store interface {
	Get(a link.Addr) (*box.Entry, bool, error)
	Put(a link.Addr, e *box.Entry) error
	Delete(a link.Addr) (bool, error)
}

// Errors.
var (
	ErrEntryMismatch = errors.New("pulse: entry does not match its address")
	ErrRuleExists    = errors.New("pulse: duplicate rule name")
	ErrNoSubscriber  = errors.New("pulse: publisher does not support Subscribe")
)

// Config configures an Engine. The zero value is usable.
type Config struct {
	Clock     Clock     // default RealClock
	Publisher Publisher // default: a new Bus
	// MaxChainDepth bounds how many writes deep rule actions may cascade
	// within one chain (default 16).
	MaxChainDepth int
	// MaxTimerGenerations bounds how many timers may start from the actions of
	// timers (timer -> rule -> timer -> ...) before the chain is cut (default 32).
	MaxTimerGenerations int
	// PublishChanges also publishes a KindChange event for every write.
	PublishChanges bool
}

// Firing records that a rule fired.
type Firing struct {
	Rule  string
	Addr  link.Addr
	Depth int // 0 for rules triggered by the caller's write
}

// Blocked records that loop protection stopped a rule.
type Blocked struct {
	Rule   string
	Addr   link.Addr
	Reason string // "loop" (same rule+record already fired in this chain), "depth", "timer-generation"
}

// Result reports what happened while processing a write. Fired lists rules in
// the order their actions started. Errors holds failed conditions/actions
// (they are also published as KindError events). The write itself succeeded
// if Put returned a nil error.
type Result struct {
	Fired   []Firing
	Blocked []Blocked
	Errors  []error
}

// TimerInfo describes a pending timer.
type TimerInfo struct {
	Rule string
	Addr link.Addr
	Name string
	Due  time.Time
}

// timerKey identifies a timer: names are per record, shared by all rules, so
// one rule can cancel or restart a timer another rule started.
type timerKey struct {
	addr link.Addr
	name string
}

type timerEntry struct {
	stop    Stopper
	due     time.Time
	rule    *Rule
	addr    link.Addr
	actions []Action
	gen     int
}

type firedKey struct {
	rule string
	addr link.Addr
}

// run is the state of one chain of rule firings.
type run struct {
	res   *Result
	fired map[firedKey]bool
	gen   int // timer generation
}

// Engine evaluates pulse rules on writes. All processing is serialized by one
// lock, so rules observe a consistent view and chains never interleave.
// Writes made directly to the Store bypass the rules.
type Engine struct {
	mu     sync.Mutex
	store  Store
	cfg    Config
	rules  []*Rule
	timers map[timerKey]*timerEntry
	seq    uint64
	closed bool
	bus    *Bus
}

// New creates an Engine.
func New(store Store, cfg Config) *Engine {
	if cfg.Clock == nil {
		cfg.Clock = RealClock{}
	}
	if cfg.MaxChainDepth <= 0 {
		cfg.MaxChainDepth = 16
	}
	if cfg.MaxTimerGenerations <= 0 {
		cfg.MaxTimerGenerations = 32
	}
	e := &Engine{store: store, cfg: cfg, timers: map[timerKey]*timerEntry{}}
	if cfg.Publisher == nil {
		e.bus = NewBus()
		e.cfg.Publisher = e.bus
	} else if b, ok := cfg.Publisher.(*Bus); ok {
		e.bus = b
	}
	return e
}

// Bus returns the built-in bus (nil if a custom Publisher was configured
// that is not a *Bus).
func (e *Engine) Bus() *Bus { return e.bus }

// Subscribe registers a live client for events matching pattern (see
// Subscriber). It requires the configured Publisher to also be a Subscriber.
func (e *Engine) Subscribe(pattern string, buffer int) (<-chan Event, func(), error) {
	s, ok := e.cfg.Publisher.(Subscriber)
	if !ok {
		return nil, nil, ErrNoSubscriber
	}
	ch, cancel := s.Subscribe(pattern, buffer)
	return ch, cancel, nil
}

// LoadRules parses a .red-xai-db-pulse document and adds its rules. It is
// all-or-nothing: on any error no rule is added.
func (e *Engine) LoadRules(src string) error {
	rules, err := ParseRules(src)
	if err != nil {
		return err
	}
	return e.AddRules(rules...)
}

// AddRules adds compiled rules; names must be unique.
func (e *Engine) AddRules(rules ...*Rule) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range e.rules {
		seen[r.Name] = true
	}
	for _, r := range rules {
		if seen[r.Name] {
			return fmt.Errorf("%w: %s", ErrRuleExists, r.Name)
		}
		seen[r.Name] = true
	}
	e.rules = append(e.rules, rules...)
	return nil
}

// RemoveRule removes a rule and cancels its pending timers.
func (e *Engine) RemoveRule(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	found := false
	for i, r := range e.rules {
		if r.Name == name {
			e.rules = append(e.rules[:i:i], e.rules[i+1:]...)
			found = true
			break
		}
	}
	for k, t := range e.timers {
		if t.rule.Name == name {
			t.stop.Stop()
			delete(e.timers, k)
		}
	}
	return found
}

// Rules lists the loaded rule names in evaluation order.
func (e *Engine) Rules() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.rules))
	for i, r := range e.rules {
		out[i] = r.Name
	}
	return out
}

// Timers lists pending timers sorted by due time.
func (e *Engine) Timers() []TimerInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TimerInfo, 0, len(e.timers))
	for k, t := range e.timers {
		out = append(out, TimerInfo{Rule: t.rule.Name, Addr: k.addr, Name: k.name, Due: t.due})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Due.Equal(out[j].Due) {
			return out[i].Due.Before(out[j].Due)
		}
		return out[i].Rule+out[i].Name < out[j].Rule+out[j].Name
	})
	return out
}

// Close cancels all timers. Later writes still evaluate rules, but timer
// actions are ignored.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for k, t := range e.timers {
		t.stop.Stop()
		delete(e.timers, k)
	}
}

func checkEntry(a link.Addr, ent *box.Entry) error {
	if ent == nil || ent.Key != a.Box {
		return fmt.Errorf("%w: key does not match %s", ErrEntryMismatch, a)
	}
	id, has := ent.ID()
	if has != a.HasID || (has && id != a.ID) {
		return fmt.Errorf("%w: ID does not match %s", ErrEntryMismatch, a)
	}
	return nil
}

func newRun() *run { return &run{res: &Result{}, fired: map[firedKey]bool{}} }

// Put stores the record and runs every rule it triggers. The returned error
// is only about storing the record itself; rule failures are in Result.Errors.
func (e *Engine) Put(a link.Addr, ent *box.Entry) (Result, error) {
	if err := checkEntry(a, ent); err != nil {
		return Result{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old, _, err := e.store.Get(a)
	if err != nil {
		return Result{}, err
	}
	r := newRun()
	if err := e.putLocked(r, a, ent.Clone(), old, 0); err != nil {
		return *r.res, err
	}
	return *r.res, nil
}

// Set writes one field of an existing record (read-modify-write under the
// engine lock) and runs the rules it triggers. path is a field path such as
// "Health" or "Stats.Hp".
func (e *Engine) Set(a link.Addr, path string, v box.Value) (Result, error) {
	p, err := box.ParsePath(path)
	if err != nil {
		return Result{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur, ok, err := e.store.Get(a)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("pulse: record %s does not exist", a)
	}
	next := cur.Clone()
	if err := setField(next, p.Segs, v); err != nil {
		return Result{}, err
	}
	r := newRun()
	if err := e.putLocked(r, a, next, cur, 0); err != nil {
		return *r.res, err
	}
	return *r.res, nil
}

// Delete removes a record and cancels its pending timers. Rules do not run
// on deletes.
func (e *Engine) Delete(a link.Addr) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ok, err := e.store.Delete(a)
	if err != nil {
		return ok, err
	}
	for k, t := range e.timers {
		if k.addr == a {
			t.stop.Stop()
			delete(e.timers, k)
			e.emitTimer(TopicTimerCancelled, t.rule.Name, k, nil)
		}
	}
	return ok, nil
}

// setField sets a (possibly nested) field in rec, creating boxes as needed.
func setField(rec *box.Entry, segs []box.Seg, v box.Value) error {
	if len(segs) == 0 {
		return fmt.Errorf("cannot replace a whole record, name a field")
	}
	for _, s := range segs {
		if s.Wild || s.AnyID {
			return fmt.Errorf("wildcards are not allowed in a set target")
		}
	}
	if rec.Value.Kind == box.KindNell {
		rec.Value = box.BoxValue(box.NewBox())
	}
	if rec.Value.Kind != box.KindBox {
		return fmt.Errorf("record %s is a %s, not a box", rec.Key, rec.Value.Kind)
	}
	if rec.Value.Box == nil {
		rec.Value.Box = box.NewBox()
	}
	b := rec.Value.Box
	mk := func(s box.Seg, val box.Value) *box.Entry {
		var id *int64
		if s.HasID {
			x := s.ID
			id = &x
		}
		return box.NewEntry(s.Name, id, val)
	}
	for i, s := range segs {
		ent := b.Lookup(s)
		if i == len(segs)-1 {
			if ent != nil {
				ent.Value = v
			} else {
				b.Put(mk(s, v))
			}
			return nil
		}
		if ent == nil {
			nb := box.NewBox()
			b.Put(mk(s, box.BoxValue(nb)))
			b = nb
			continue
		}
		if ent.Value.Kind != box.KindBox {
			return fmt.Errorf("%s is a %s, not a box", s.Name, ent.Value.Kind)
		}
		if ent.Value.Box == nil {
			ent.Value.Box = box.NewBox()
		}
		b = ent.Value.Box
	}
	return nil
}

// ---- event helpers (callers hold e.mu) --------------------------------------

func (e *Engine) publish(ev Event) {
	e.seq++
	ev.Seq = e.seq
	ev.Time = e.cfg.Clock.Now()
	e.cfg.Publisher.Publish(ev)
}

func (e *Engine) snapshot(a link.Addr) *box.Entry {
	rec, ok, err := e.store.Get(a)
	if err != nil || !ok {
		return nil
	}
	return rec
}

func (e *Engine) emitTimer(topic, rule string, k timerKey, snap *box.Entry) {
	e.publish(Event{Kind: KindTimer, Topic: topic, Rule: rule, Addr: k.addr, Timer: k.name, Data: snap})
}

func (e *Engine) fail(r *run, rule *Rule, a link.Addr, err error) {
	wrapped := fmt.Errorf("rule %s on %s: %w", rule.Name, a, err)
	r.res.Errors = append(r.res.Errors, wrapped)
	e.publish(Event{Kind: KindError, Topic: TopicError, Rule: rule.Name, Addr: a, Err: wrapped.Error()})
}

func (e *Engine) block(r *run, rule *Rule, a link.Addr, reason string) {
	r.res.Blocked = append(r.res.Blocked, Blocked{Rule: rule.Name, Addr: a, Reason: reason})
	e.publish(Event{Kind: KindLoop, Topic: TopicLoop, Rule: rule.Name, Addr: a,
		Err: fmt.Sprintf("rule %s stopped by loop protection (%s) on %s", rule.Name, reason, a)})
}

func (e *Engine) envFor(rec *box.Entry) *env {
	return &env{rec: rec, read: func(ref box.Ref) (box.Value, error) {
		a, err := link.AddrOf(ref)
		if err != nil {
			return box.Value{}, err
		}
		other, ok, err := e.store.Get(a)
		if err != nil {
			return box.Value{}, err
		}
		if !ok {
			return box.Value{}, nil // missing record reads as NELL
		}
		v, _ := lookup(other, ref.Segs[1:])
		return v, nil
	}}
}

// ---- rule evaluation ----------------------------------------------------------

func (r *Rule) shouldFire(e *Engine, now, old *box.Entry) (bool, error) {
	nv, err := r.Cond.eval(e.envFor(now))
	if err != nil {
		return false, err
	}
	if !truthy(nv) {
		return false, nil
	}
	if r.Level {
		if old == nil || len(r.watch) == 0 {
			return true, nil
		}
		for _, w := range r.watch {
			a, _ := lookup(now, w)
			b, _ := lookup(old, w)
			if !a.Equal(b) {
				return true, nil
			}
		}
		return false, nil
	}
	// edge: the condition must have been false (or the record absent) before
	if old == nil {
		return true, nil
	}
	ov, err := r.Cond.eval(e.envFor(old))
	if err != nil {
		return true, nil // an old state we cannot evaluate counts as "was false"
	}
	return !truthy(ov), nil
}

// putLocked stores ent and then runs the rules triggered by the change.
// All firing rules are determined from this write first (in definition
// order) and only then are their actions run.
func (e *Engine) putLocked(r *run, a link.Addr, ent, old *box.Entry, depth int) error {
	if err := e.store.Put(a, ent); err != nil {
		return err
	}
	if e.cfg.PublishChanges {
		e.publish(Event{Kind: KindChange, Topic: TopicChange, Addr: a, Data: ent.Clone()})
	}
	var firing []*Rule
	for _, rule := range e.rules {
		if !rule.matches(a) {
			continue
		}
		fire, err := rule.shouldFire(e, ent, old)
		if err != nil {
			e.fail(r, rule, a, fmt.Errorf("condition: %w", err))
			continue
		}
		if fire {
			firing = append(firing, rule)
		}
	}
	for _, rule := range firing {
		key := firedKey{rule.Name, a}
		switch {
		case r.fired[key]:
			e.block(r, rule, a, "loop")
			continue
		case depth >= e.cfg.MaxChainDepth:
			e.block(r, rule, a, "depth")
			continue
		}
		r.fired[key] = true
		r.res.Fired = append(r.res.Fired, Firing{Rule: rule.Name, Addr: a, Depth: depth})
		e.runActions(r, rule, a, rule.Actions, depth)
	}
	return nil
}

// runActions executes actions in order; the first failing action stops the
// rest of this firing (earlier actions are not rolled back).
func (e *Engine) runActions(r *run, rule *Rule, a link.Addr, acts []Action, depth int) {
	for _, act := range acts {
		if err := e.runAction(r, rule, a, act, depth); err != nil {
			e.fail(r, rule, a, fmt.Errorf("action %q: %w", act.actionText(), err))
			return
		}
	}
}

func (e *Engine) runAction(r *run, rule *Rule, a link.Addr, act Action, depth int) error {
	switch x := act.(type) {
	case SetAction:
		cur, _, err := e.store.Get(a) // nil if the record is gone
		if err != nil {
			return err
		}
		ta, segs := a, x.Local
		target := cur
		if x.Remote != nil {
			if ta, err = link.AddrOf(*x.Remote); err != nil {
				return err
			}
			segs = x.Remote.Segs[1:]
			if ta != a {
				if target, _, err = e.store.Get(ta); err != nil {
					return err
				}
			}
		}
		if target == nil {
			return fmt.Errorf("record %s does not exist", ta)
		}
		val, err := x.Value.eval(e.envFor(cur))
		if err != nil {
			return err
		}
		if existing, has := lookup(target, segs); has && existing.Equal(val) {
			return nil // no change: do not write, do not retrigger
		}
		next := target.Clone()
		if err := setField(next, segs, val); err != nil {
			return err
		}
		return e.putLocked(r, ta, next, target, depth+1)
	case EmitAction:
		e.publish(Event{Kind: KindEmit, Topic: x.Topic, Rule: rule.Name, Addr: a, Data: e.snapshot(a)})
	case CancelAction:
		k := timerKey{a, x.Name}
		if t, ok := e.timers[k]; ok {
			t.stop.Stop()
			delete(e.timers, k)
			e.emitTimer(TopicTimerCancelled, rule.Name, k, nil)
		}
	case TimerAction:
		if e.closed {
			return nil
		}
		k := timerKey{a, x.Name}
		if old, ok := e.timers[k]; ok {
			old.stop.Stop() // restarting replaces the pending timer
		}
		t := &timerEntry{rule: rule, addr: a, actions: x.Actions, gen: r.gen,
			due: e.cfg.Clock.Now().Add(x.Duration)}
		t.stop = e.cfg.Clock.AfterFunc(x.Duration, func() { e.fireTimer(k, t) })
		e.timers[k] = t
		e.emitTimer(TopicTimerStarted, rule.Name, k, nil)
	default:
		return fmt.Errorf("unknown action %T", act)
	}
	return nil
}

func (e *Engine) fireTimer(k timerKey, t *timerEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.timers[k] != t {
		return // cancelled, replaced or engine closed
	}
	delete(e.timers, k)
	snap := e.snapshot(t.addr)
	e.emitTimer(TopicTimerFired, t.rule.Name, k, snap)
	r := newRun()
	r.gen = t.gen + 1
	if t.gen >= e.cfg.MaxTimerGenerations {
		e.block(r, t.rule, t.addr, "timer-generation")
		return
	}
	if snap == nil {
		e.fail(r, t.rule, t.addr, fmt.Errorf("timer %s fired but the record no longer exists", k.name))
		return
	}
	e.runActions(r, t.rule, t.addr, t.actions, 0)
}
