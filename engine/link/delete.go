package link

import (
	"fmt"

	"redx/box"
)

// CascadePolicy says what Delete does with `@!>` links.
type CascadePolicy int

const (
	// CascadeFollow (default) deletes everything reachable over @!> links.
	CascadeFollow CascadePolicy = iota
	// CascadeReject refuses the delete (ErrCascadeRejected) if it would
	// delete any record other than the one asked for.
	CascadeReject
	// CascadeNone deletes only the named record and ignores @!> links.
	CascadeNone
)

// InboundPolicy says what Delete does about links from OTHER records that
// point at records being deleted (they would become dangling).
type InboundPolicy int

const (
	// InboundIgnore (default) does not look: no scan is performed.
	InboundIgnore InboundPolicy = iota
	// InboundReport scans and lists such links in DeleteResult.Inbound.
	InboundReport
	// InboundRestrict scans and refuses the delete (ErrRestricted) if any exist.
	InboundRestrict
)

// DeleteOptions controls Engine.Delete.
type DeleteOptions struct {
	Cascade CascadePolicy
	Inbound InboundPolicy
	// DryRun computes the plan and checks every policy but deletes nothing.
	DryRun bool
	// PruneLinks removes standalone links that touch a deleted record
	// (as source or target) after a successful delete.
	PruneLinks bool
	// MaxRecords overrides Options.MaxDeleteRecords when > 0.
	MaxRecords int
}

// DeleteResult describes what Delete planned and did.
type DeleteResult struct {
	Root        Addr
	RootMissing bool // the record did not exist; nothing was done
	DryRun      bool
	Planned     []Addr     // deletion order: leaves first, root last
	Deleted     []Addr     // records actually removed (subset of Planned on partial failure)
	Cycles      []Edge     // cascade edges that close a cycle (skipped, no infinite loop)
	Dangling    []Dangling // cascade links whose target did not exist (skipped)
	Inbound     []Edge     // links from outside the delete set that point into it (InboundReport/Restrict)
	PrunedLinks []string   // standalone links removed by PruneLinks
}

// RestrictError is returned for InboundRestrict.
type RestrictError struct {
	Addr     Addr
	Blockers []Edge
}

func (e *RestrictError) Error() string {
	return fmt.Sprintf("%s: %s (%d blocking link(s), first: %s)", ErrRestricted, e.Addr, len(e.Blockers), e.Blockers[0])
}
func (e *RestrictError) Is(t error) bool { return t == ErrRestricted }

// CascadeError is returned for CascadeReject.
type CascadeError struct {
	Addr  Addr
	Edges []Edge
}

func (e *CascadeError) Error() string {
	return fmt.Sprintf("%s: %s would cascade via %s", ErrCascadeRejected, e.Addr, e.Edges[0])
}
func (e *CascadeError) Is(t error) bool { return t == ErrCascadeRejected }

// PlanDelete is Delete with DryRun forced on.
func (e *Engine) PlanDelete(a Addr, o DeleteOptions) (DeleteResult, error) {
	o.DryRun = true
	return e.Delete(a, o)
}

// Delete removes record a. By default it follows @!> links transitively
// (each record once; cycles are detected and skipped) and deletes leaves
// first so that a crash or failure part-way leaves the owners in place and
// the same call can simply be repeated. The delete is NOT atomic across the
// store: on a store error the returned DeleteResult.Deleted lists what was
// already removed.
func (e *Engine) Delete(a Addr, o DeleteOptions) (DeleteResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	res := DeleteResult{Root: a, DryRun: o.DryRun}
	max := o.MaxRecords
	if max <= 0 {
		max = e.opt.MaxDeleteRecords
	}
	if _, ok, err := e.store.Get(a); err != nil {
		return res, err
	} else if !ok {
		res.RootMissing = true
		return res, nil
	}

	// 1. Plan: breadth-first over cascade links.
	order := []Addr{a}
	set := map[Addr]bool{a: true}
	adj := map[Addr][]Addr{} // cascade adjacency inside the set
	var revisits []Edge      // cascade edges to already-planned records
	for i := 0; i < len(order) && o.Cascade != CascadeNone; i++ {
		cur := order[i]
		edges, err := e.outgoing(cur)
		if err != nil {
			return res, err
		}
		var bad []Edge
		for _, ed := range edges {
			if ed.Op != OpCascade {
				continue
			}
			ta, aerr := AddrOf(ed.Target)
			if aerr != nil {
				res.Dangling = append(res.Dangling, Dangling{ed.Owner, ed.Via, ed.Target, ReasonInvalidRef})
				continue
			}
			if len(ed.Target.Segs) > 1 {
				return res, fmt.Errorf("%w: %s", ErrBadCascadeTarget, ed)
			}
			if set[ta] {
				adj[cur] = append(adj[cur], ta)
				revisits = append(revisits, ed)
				continue
			}
			_, ok, err := e.store.Get(ta)
			if err != nil {
				return res, err
			}
			if !ok {
				res.Dangling = append(res.Dangling, Dangling{ed.Owner, ed.Via, ed.Target, ReasonMissingRecord})
				continue
			}
			if o.Cascade == CascadeReject {
				bad = append(bad, ed)
				continue
			}
			set[ta] = true
			order = append(order, ta)
			adj[cur] = append(adj[cur], ta)
			if len(order) > max {
				return res, fmt.Errorf("%w: more than %d records from %s", ErrTooManyRecords, max, a)
			}
		}
		if len(bad) > 0 {
			return res, &CascadeError{Addr: a, Edges: bad}
		}
	}
	for _, ed := range revisits {
		ta, _ := AddrOf(ed.Target)
		if reaches(adj, ta, ed.Owner) {
			res.Cycles = append(res.Cycles, ed)
		}
	}

	// 2. Inbound references from outside the set.
	if o.Inbound != InboundIgnore {
		in, err := e.inbound(set)
		if err != nil {
			return res, err
		}
		res.Inbound = in
		if o.Inbound == InboundRestrict && len(in) > 0 {
			return res, &RestrictError{Addr: a, Blockers: in}
		}
	}

	for i := len(order) - 1; i >= 0; i-- {
		res.Planned = append(res.Planned, order[i])
	}
	if o.DryRun {
		return res, nil
	}

	// 3. Execute, leaves first.
	for _, x := range res.Planned {
		if _, err := e.store.Delete(x); err != nil {
			return res, fmt.Errorf("link: deleting %s: %w", x, err)
		}
		res.Deleted = append(res.Deleted, x)
	}
	if o.PruneLinks {
		res.PrunedLinks = e.links.RemoveWhere(func(_ string, l *box.Link) bool {
			f, _ := AddrOf(l.From)
			t, terr := AddrOf(*l.To)
			return set[f] || (terr == nil && set[t])
		})
	}
	return res, nil
}

// reaches reports whether to is reachable from from (including from == to).
func reaches(adj map[Addr][]Addr, from, to Addr) bool {
	seen := map[Addr]bool{from: true}
	stack := []Addr{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		for _, m := range adj[n] {
			if !seen[m] {
				seen[m] = true
				stack = append(stack, m)
			}
		}
	}
	return false
}

// inbound finds links owned by records outside set that point at a record in
// set. It scans every store file, plus the standalone links.
func (e *Engine) inbound(set map[Addr]bool) ([]Edge, error) {
	recs, err := e.scanFiles(e.store.Files())
	if err != nil {
		return nil, err
	}
	var out []Edge
	add := func(ed Edge) {
		if set[ed.Owner] {
			return
		}
		if ta, err := AddrOf(ed.Target); err == nil && set[ta] {
			out = append(out, ed)
		}
	}
	for _, r := range recs {
		for _, ed := range EmbeddedEdges(r.a, r.rec) {
			add(ed)
		}
	}
	for _, nl := range e.links.All() {
		owner, err := AddrOf(nl.Link.From)
		if err != nil {
			continue
		}
		op, tgt := linkTarget(nl.Link)
		add(Edge{Owner: owner, Via: "link:" + nl.Name, Op: op, Target: tgt})
	}
	return out, nil
}
