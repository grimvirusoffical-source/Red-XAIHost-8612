package link

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"redx/box"
)

// Reasons a reference can be dangling.
const (
	ReasonMissingRecord = "missing-record" // target record does not exist
	ReasonMissingField  = "missing-field"  // record exists but the .Path inside it does not
	ReasonInvalidRef    = "invalid-ref"    // wildcard / empty reference
	ReasonMissingSource = "missing-source" // standalone link whose source record is gone
)

// Sentinel errors (use errors.Is).
var (
	ErrDangling         = errors.New("link: dangling reference")
	ErrRestricted       = errors.New("link: delete restricted by inbound references")
	ErrCascadeRejected  = errors.New("link: delete would cascade and cascades are rejected")
	ErrTooManyRecords   = errors.New("link: delete would affect too many records")
	ErrBadCascadeTarget = errors.New("link: cascade target must be a whole record")
	ErrEntryMismatch    = errors.New("link: entry does not match its address")
)

// Dangling describes one reference whose target (or source) is missing.
type Dangling struct {
	Owner  Addr    // record holding the reference (zero for a bare Resolve)
	Via    string  // "field:<path>" or "link:<name>" ("" for a bare Resolve)
	Target box.Ref // the reference that does not resolve
	Reason string
}

func (d Dangling) String() string {
	if d.Via == "" {
		return fmt.Sprintf("%s: %s", d.Target, d.Reason)
	}
	return fmt.Sprintf("%s (%s) -> %s: %s", d.Owner, d.Via, d.Target, d.Reason)
}

// DanglingError is returned when one or more references do not resolve.
type DanglingError struct{ Items []Dangling }

func (e *DanglingError) Error() string {
	parts := make([]string, len(e.Items))
	for i, d := range e.Items {
		parts[i] = d.String()
	}
	return ErrDangling.Error() + ": " + strings.Join(parts, "; ")
}
func (e *DanglingError) Is(t error) bool { return t == ErrDangling }

// Options configures an Engine.
type Options struct {
	// Links are the standalone links (.red-xai-db-link). Nil means an empty set.
	Links *LinkSet
	// MaxExpandDepth bounds Expand recursion (default 8).
	MaxExpandDepth int
	// MaxDeleteRecords bounds how many records one delete may touch (default 100000).
	MaxDeleteRecords int
}

// Engine resolves and maintains links on top of a Store. All link-aware
// reads take a shared lock and all link-aware writes (Put, Delete) take an
// exclusive one, so a cascade is never interleaved with another Put/Delete
// that goes through the same Engine. Writing to the Store directly bypasses
// that guarantee.
type Engine struct {
	mu    sync.RWMutex
	store Store
	links *LinkSet
	opt   Options
}

// New creates an Engine.
func New(store Store, opt Options) *Engine {
	if opt.Links == nil {
		opt.Links = NewLinkSet()
	}
	if opt.MaxExpandDepth <= 0 {
		opt.MaxExpandDepth = 8
	}
	if opt.MaxDeleteRecords <= 0 {
		opt.MaxDeleteRecords = 100000
	}
	return &Engine{store: store, links: opt.Links, opt: opt}
}

// Links returns the standalone link set.
func (e *Engine) Links() *LinkSet { return e.links }

// Get returns a copy of the record.
func (e *Engine) Get(a Addr) (*box.Entry, bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.store.Get(a)
}

// PutOptions controls Engine.Put.
type PutOptions struct {
	// RequireTargets rejects a write whose embedded links point at records
	// or fields that do not exist (returns *DanglingError, writes nothing).
	RequireTargets bool
}

// Put stores a record. The entry's key and ID must match the address.
func (e *Engine) Put(a Addr, ent *box.Entry, o PutOptions) error {
	if ent == nil || ent.Key != a.Box {
		return fmt.Errorf("%w: key does not match %s", ErrEntryMismatch, a)
	}
	id, has := ent.ID()
	if has != a.HasID || (has && id != a.ID) {
		return fmt.Errorf("%w: ID does not match %s", ErrEntryMismatch, a)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if o.RequireTargets {
		var bad []Dangling
		for _, ed := range EmbeddedEdges(a, ent) {
			if _, r, err := e.resolveRef(ed.Target); err != nil {
				return err
			} else if r != "" {
				bad = append(bad, Dangling{Owner: a, Via: ed.Via, Target: ed.Target, Reason: r})
			}
		}
		if len(bad) > 0 {
			return &DanglingError{Items: bad}
		}
	}
	return e.store.Put(a, ent)
}

// fieldValue follows segs (after the record segment) inside a record.
func fieldValue(rec *box.Entry, segs []box.Seg) (box.Value, bool) {
	if len(segs) == 0 {
		return rec.Value, true
	}
	if rec.Value.Kind != box.KindBox || rec.Value.Box == nil {
		return box.Value{}, false
	}
	for _, s := range segs {
		if s.Wild || s.AnyID {
			return box.Value{}, false
		}
	}
	ent, err := box.Resolve(rec.Value.Box, segs)
	if err != nil {
		return box.Value{}, false
	}
	return ent.Value, true
}

// resolveRef reads the value a reference points at. It returns a non-empty
// reason (and no error) when the reference is dangling; err is a store error.
func (e *Engine) resolveRef(ref box.Ref) (box.Value, string, error) {
	a, aerr := AddrOf(ref)
	if aerr != nil {
		return box.Value{}, ReasonInvalidRef, nil
	}
	rec, ok, err := e.store.Get(a)
	if err != nil {
		return box.Value{}, "", err
	}
	if !ok {
		return box.Value{}, ReasonMissingRecord, nil
	}
	v, ok := fieldValue(rec, ref.Segs[1:])
	if !ok {
		return box.Value{}, ReasonMissingField, nil
	}
	return v, "", nil
}

// Resolve reads across files: it returns the value ref points at (the whole
// record value for `"f":Box[ID=1]`, a field for `"f":Box[ID=1].Key.Sub`).
// A missing target yields a *DanglingError (errors.Is(err, ErrDangling)).
func (e *Engine) Resolve(ref box.Ref) (box.Value, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v, reason, err := e.resolveRef(ref)
	if err != nil {
		return box.Value{}, err
	}
	if reason != "" {
		return box.Value{}, &DanglingError{Items: []Dangling{{Target: ref, Reason: reason}}}
	}
	return v, nil
}

// ResolveLink follows a link's target (`@>`); for a bare reference it reads
// the reference itself.
func (e *Engine) ResolveLink(l *box.Link) (box.Value, error) {
	_, tgt := linkTarget(l)
	return e.Resolve(tgt)
}

// Expansion is the result of Expand.
type Expansion struct {
	Entry    *box.Entry // copy of the record with link values replaced by their targets
	Dangling []Dangling // links that could not be followed (left in place)
	Cycles   []box.Ref  // links that point back into the chain being expanded (left in place)
}

// Expand returns a copy of the record in which every embedded link value is
// replaced by what it points at, recursively up to depth (<=0 uses the
// engine default). Dangling links and links that would close a cycle are
// left as links and reported. Standalone links are not expanded here; use
// Outgoing for those.
func (e *Engine) Expand(a Addr, depth int) (Expansion, error) {
	if depth <= 0 {
		depth = e.opt.MaxExpandDepth
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	rec, ok, err := e.store.Get(a)
	if err != nil {
		return Expansion{}, err
	}
	if !ok {
		return Expansion{}, &DanglingError{Items: []Dangling{{Target: a.Ref(), Reason: ReasonMissingRecord}}}
	}
	var out Expansion
	x := &expander{e: e, out: &out, visiting: map[Addr]bool{a: true}, maxDepth: depth}
	res := rec.Clone()
	res.Value = x.value(a, rec.Value, "", 0)
	if x.err != nil {
		return Expansion{}, x.err
	}
	out.Entry = res
	return out, nil
}

type expander struct {
	e        *Engine
	out      *Expansion
	visiting map[Addr]bool
	maxDepth int
	err      error
}

func (x *expander) value(owner Addr, v box.Value, path string, depth int) box.Value {
	switch v.Kind {
	case box.KindLink:
		if v.Link == nil {
			return v
		}
		return x.follow(owner, v, path, depth)
	case box.KindBox:
		if v.Box == nil {
			return v
		}
		nb := &box.Box{HasFlag: v.Box.HasFlag, Flag: v.Box.Flag, Meta: v.Box.Meta}
		for _, c := range v.Box.Entries() {
			cc := c.Clone()
			cc.Value = x.value(owner, c.Value, joinPath(path, c.EKey().String()), depth)
			nb.Put(cc)
		}
		return box.BoxValue(nb)
	case box.KindArray:
		arr := make([]box.Value, len(v.Arr))
		for i, c := range v.Arr {
			arr[i] = x.value(owner, c, fmt.Sprintf("%s[%d]", path, i), depth)
		}
		return box.Array(arr...)
	}
	return v
}

func (x *expander) follow(owner Addr, v box.Value, path string, depth int) box.Value {
	if x.err != nil {
		return v
	}
	_, tgt := linkTarget(v.Link)
	via := "field:" + path
	ta, aerr := AddrOf(tgt)
	if aerr != nil {
		x.out.Dangling = append(x.out.Dangling, Dangling{owner, via, tgt, ReasonInvalidRef})
		return v
	}
	if depth >= x.maxDepth {
		return v
	}
	if x.visiting[ta] {
		x.out.Cycles = append(x.out.Cycles, tgt)
		return v
	}
	rec, ok, err := x.e.store.Get(ta)
	if err != nil {
		x.err = err
		return v
	}
	if !ok {
		x.out.Dangling = append(x.out.Dangling, Dangling{owner, via, tgt, ReasonMissingRecord})
		return v
	}
	if len(tgt.Segs) > 1 {
		fv, ok := fieldValue(rec, tgt.Segs[1:])
		if !ok {
			x.out.Dangling = append(x.out.Dangling, Dangling{owner, via, tgt, ReasonMissingField})
			return v
		}
		return fv.Clone()
	}
	x.visiting[ta] = true
	nv := x.value(ta, rec.Value, "", depth+1)
	delete(x.visiting, ta)
	return nv
}

// Outgoing lists every edge (embedded and standalone) that leaves record a.
func (e *Engine) Outgoing(a Addr) ([]Edge, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.outgoing(a)
}

func (e *Engine) outgoing(a Addr) ([]Edge, error) {
	rec, ok, err := e.store.Get(a)
	if err != nil {
		return nil, err
	}
	var out []Edge
	if ok {
		out = append(out, EmbeddedEdges(a, rec)...)
	}
	out = append(out, e.links.Outgoing(a)...)
	return out, nil
}

type scanned struct {
	a   Addr
	rec *box.Entry
}

func (e *Engine) scanFiles(files []string) ([]scanned, error) {
	var out []scanned
	for _, f := range files {
		serr := e.store.Scan(f, func(a Addr, r *box.Entry) bool {
			out = append(out, scanned{a, r})
			return true
		})
		if serr != nil {
			return nil, serr
		}
	}
	return out, nil
}

// CheckDangling scans files (all store files if none are given) and reports
// every embedded link whose target is missing, and every standalone link
// whose source or target is missing (for a missing source, Target is the
// source reference). With file arguments, only records and links whose source
// lives in those files are checked. Results are sorted for stable output.
func (e *Engine) CheckDangling(files ...string) ([]Dangling, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	scoped := len(files) > 0
	if !scoped {
		files = e.store.Files()
	}
	inScope := map[string]bool{}
	for _, f := range files {
		inScope[f] = true
	}
	recs, err := e.scanFiles(files)
	if err != nil {
		return nil, err
	}
	var out []Dangling
	check := func(ed Edge) error {
		_, reason, err := e.resolveRef(ed.Target)
		if err != nil {
			return err
		}
		if reason != "" {
			out = append(out, Dangling{ed.Owner, ed.Via, ed.Target, reason})
		}
		return nil
	}
	for _, r := range recs {
		for _, ed := range EmbeddedEdges(r.a, r.rec) {
			if err := check(ed); err != nil {
				return nil, err
			}
		}
	}
	for _, nl := range e.links.All() {
		op, tgt := linkTarget(nl.Link)
		owner, _ := AddrOf(nl.Link.From)
		if scoped && !inScope[owner.File] {
			continue // standalone links are scoped by their source file
		}
		via := "link:" + nl.Name
		if _, ok, err := e.store.Get(owner); err != nil {
			return nil, err
		} else if !ok {
			out = append(out, Dangling{owner, via, nl.Link.From, ReasonMissingSource})
		}
		if err := check(Edge{Owner: owner, Via: via, Op: op, Target: tgt}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
