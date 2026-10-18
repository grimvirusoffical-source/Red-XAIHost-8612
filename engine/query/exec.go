package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"redx/box"
	"redx/engine/idx"
	"redx/engine/link"
	"redx/engine/sec"
)

// Config configures a DB.
type Config struct {
	// Store is the record storage (required). To keep indexes current, pass
	// the store the idx.Manager observes (idx.ObservedStore, or
	// engine.IndexSet.Store), not the bare one.
	Store link.Store
	// Auth is asked before every resource is touched (required; nil is an
	// error, use AllowAll explicitly if you really want no checks).
	Auth Authorizer
	// Indexes, if set, lets the planner use matching indexes.
	Indexes *idx.Manager
	// Links is needed for JOIN and for cascade deletes (@!>). It must wrap
	// the same store as Store. Without it JOIN is refused, and DELETE
	// refuses records that carry embedded @!> links.
	Links *link.Engine
	// OwnerOf reports the owner principal id of a record, for rules with
	// Owner = TRUE. Optional; without it Resource.Owner is empty.
	OwnerOf func(link.Addr, *box.Entry) string
	// Limits bound the cost of one statement (zero fields = defaults).
	Limits Limits
}

// DB executes queries. It is safe for concurrent use; writes made through
// one DB are serialized with each other.
type DB struct {
	cfg Config
	lim Limits
	wmu sync.Mutex
}

// New creates a DB.
func New(cfg Config) (*DB, error) {
	if cfg.Store == nil {
		return nil, errors.New("query: Config.Store is required")
	}
	if cfg.Auth == nil {
		return nil, ErrNoAuth
	}
	return &DB{cfg: cfg, lim: cfg.Limits.withDefaults()}, nil
}

// Result is what a statement returns.
type Result struct {
	Kind     string // select, insert, update, delete, explain
	Columns  []string
	Rows     [][]box.Value // aligned with Columns; NELL where a row has no such column
	Addrs    []link.Addr   // SELECT: the FROM record behind each row
	Affected int           // INSERT/UPDATE/DELETE: records changed (cascades excluded)
	Cascaded []link.Addr   // DELETE: extra records removed by @!> cascades
	Access   string        // how the FROM records were found: scan, pk, index:<name>, union(...)
	Scanned  int           // records read from storage
	Plan     []string      // EXPLAIN: the plan, one line per entry (also in Rows)
}

// Explain parses q and returns its plan without running it.
func (db *DB) Explain(ctx context.Context, pr sec.Principal, q string, args ...interface{}) (*Result, error) {
	return db.exec(ctx, pr, q, true, args)
}

// Exec parses and runs one statement. Values for ? placeholders go in args
// (nil, bool, string, any int/uint/float type, json.Number, []interface{} or
// box.Value); never build query text from user input.
func (db *DB) Exec(ctx context.Context, pr sec.Principal, q string, args ...interface{}) (*Result, error) {
	return db.exec(ctx, pr, q, false, args)
}

type run struct {
	db      *DB
	ctx     context.Context
	az      *authz
	lim     Limits
	params  []box.Value
	scanned int
	ticks   int
}

func (db *DB) exec(ctx context.Context, pr sec.Principal, q string, explain bool, args []interface{}) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	st, n, err := parseWith(q, db.lim)
	if err != nil {
		return nil, err
	}
	if n != len(args) {
		return nil, fmt.Errorf("%w: query has %d placeholder(s) but %d argument(s) were given", ErrParams, n, len(args))
	}
	r := &run{db: db, ctx: ctx, az: newAuthz(db.cfg.Auth, pr), lim: db.lim}
	for i, a := range args {
		v, err := toValue(a, db.lim.MaxParamBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: argument %d: %v", ErrParams, i+1, err)
		}
		r.params = append(r.params, v)
	}
	if ex, ok := st.(*Explain); ok {
		st, explain = ex.Inner, true
	}
	switch s := st.(type) {
	case *Select:
		return r.doSelect(s, explain)
	case *Insert:
		return r.doInsert(s, explain)
	case *Update:
		return r.doUpdate(s, explain)
	case *Delete:
		return r.doDelete(s, explain)
	}
	return nil, errors.New("query: unsupported statement")
}

func (r *run) tick() error {
	r.ticks++
	if r.ticks&255 == 0 {
		return r.ctx.Err()
	}
	return nil
}

// ---- records and field access -------------------------------------------

type rec struct {
	addr  link.Addr
	ent   *box.Entry // the stored record (primary records only)
	val   box.Value
	owner string
}

type tuple []*rec

func (r *run) owner(a link.Addr, e *box.Entry) string {
	if r.db.cfg.OwnerOf == nil {
		return ""
	}
	return r.db.cfg.OwnerOf(a, e)
}

// gate admits a record only if the principal may read its box.
func (r *run) gate(a link.Addr, e *box.Entry, val box.Value) (*rec, bool) {
	owner := r.owner(a, e)
	if !r.az.ok(sec.ActionRead, sec.Resource{File: a.File, Box: a.Box, Owner: owner}) {
		return nil, false
	}
	return &rec{addr: a, ent: e, val: val, owner: owner}, true
}

func (r *run) field(t tuple, f *Field) box.Value {
	if f.Alias >= len(t) || t[f.Alias] == nil {
		return box.Nell()
	}
	rc := t[f.Alias]
	switch f.Pseudo {
	case "_id":
		if rc.addr.HasID {
			return box.Int(rc.addr.ID)
		}
		return box.Nell()
	case "_box":
		return box.String(rc.addr.Box)
	case "_file":
		return box.String(rc.addr.File)
	}
	return r.readPath(rc, f)
}

func (r *run) readPath(rc *rec, f *Field) box.Value {
	if rc.val.Kind != box.KindBox || rc.val.Box == nil || len(f.Path) == 0 {
		return box.Nell()
	}
	if !r.az.pathAllowed(sec.ActionRead, rc.addr.File, rc.addr.Box, rc.owner, f.Names) {
		return box.Nell()
	}
	ent, err := box.Resolve(rc.val.Box, f.Path)
	if err != nil || ent == nil {
		return box.Nell()
	}
	return r.mask(rc, f.Names[len(f.Names)-1], ent.Value)
}

// mask removes the parts of a box value (or array of boxes) the principal
// may not read. It returns v itself when nothing was removed.
func (r *run) mask(rc *rec, prefix string, v box.Value) box.Value {
	out, _ := r.maskV(rc, prefix, v)
	return out
}

func (r *run) maskV(rc *rec, prefix string, v box.Value) (box.Value, bool) {
	switch v.Kind {
	case box.KindBox:
		if v.Box == nil {
			return v, false
		}
		ents := v.Box.Entries()
		changed := false
		kept := make([]*box.Entry, 0, len(ents))
		for _, e := range ents {
			cp := prefix + "." + e.Key
			if !r.az.pathAllowed(sec.ActionRead, rc.addr.File, rc.addr.Box, rc.owner, []string{prefix, cp}) {
				changed = true
				continue
			}
			nv, ch := r.maskV(rc, cp, e.Value)
			if ch {
				changed = true
				c := e.Clone()
				c.Value = nv
				kept = append(kept, c)
			} else {
				kept = append(kept, e)
			}
		}
		if !changed {
			return v, false
		}
		nb := box.NewBox()
		nb.HasFlag, nb.Flag, nb.Meta = v.Box.HasFlag, v.Box.Flag, v.Box.Meta
		for _, e := range kept {
			nb.Put(e.Clone())
		}
		return box.BoxValue(nb), true
	case box.KindArray:
		changed := false
		out := make([]box.Value, len(v.Arr))
		for i, x := range v.Arr {
			nv, ch := r.maskV(rc, prefix, x)
			out[i] = nv
			changed = changed || ch
		}
		if !changed {
			return v, false
		}
		return box.Array(out...), true
	}
	return v, false
}

// eval evaluates an expression for a tuple. It never fails: validation
// happened before the first record was read.
func (r *run) eval(e Expr, t tuple) box.Value {
	switch x := e.(type) {
	case *Lit:
		return x.V
	case *Param:
		return r.params[x.N]
	case *Field:
		return r.field(t, x)
	case *Unary:
		v := r.eval(x.X, t)
		if v.Kind == box.KindNumber {
			return box.Number(-v.Num)
		}
		return box.Nell()
	case *Not:
		return box.Bool(!truthy(r.eval(x.X, t)))
	case *Binary:
		switch x.Op {
		case "and":
			return box.Bool(truthy(r.eval(x.L, t)) && truthy(r.eval(x.R, t)))
		case "or":
			return box.Bool(truthy(r.eval(x.L, t)) || truthy(r.eval(x.R, t)))
		}
		a, b := r.eval(x.L, t), r.eval(x.R, t)
		switch x.Op {
		case "=":
			return box.Bool(valEq(a, b))
		case "!=":
			return box.Bool(!valEq(a, b))
		case "<", "<=", ">", ">=":
			c, ok := cmpOrder(a, b)
			if !ok {
				return box.Bool(false)
			}
			switch x.Op {
			case "<":
				return box.Bool(c < 0)
			case "<=":
				return box.Bool(c <= 0)
			case ">":
				return box.Bool(c > 0)
			}
			return box.Bool(c >= 0)
		}
		return arith(x.Op, a, b)
	case *In:
		v := r.eval(x.X, t)
		found := false
		for _, it := range x.List {
			if valEq(v, r.eval(it, t)) {
				found = true
				break
			}
		}
		return box.Bool(found != x.Neg)
	case *Like:
		v := r.eval(x.X, t)
		pat := r.eval(x.Pat, t)
		prefix, exact, _ := likePrefix(pat.Str)
		return box.Bool(likeMatch(v, prefix, exact) != x.Neg)
	case *Between:
		v := r.eval(x.X, t)
		lo, ok1 := cmpOrder(v, r.eval(x.Lo, t))
		hi, ok2 := cmpOrder(v, r.eval(x.Hi, t))
		in := ok1 && ok2 && lo >= 0 && hi <= 0
		return box.Bool(in != x.Neg)
	case *IsNell:
		return box.Bool(r.eval(x.X, t).IsNell() != x.Neg)
	case *Match:
		return box.Bool(matchValue(r.field(t, x.Field), termsOf(r.eval(x.Terms, t)), x.Any))
	}
	return box.Nell()
}

func (r *run) all(conds []Expr, t tuple) bool {
	for _, c := range conds {
		if !truthy(r.eval(c, t)) {
			return false
		}
	}
	return true
}

// ---- file resolution -----------------------------------------------------

// resolveFile maps the file name of a query to a stored file. A name without
// its type extension ("players") matches the one file that starts with
// "players.". When the file cannot be resolved the caller is first asked
// whether it may touch it at all, so that an unauthorized principal cannot
// tell a missing file from a forbidden one.
func (r *run) resolveFile(name, boxName string, act sec.Action, forWrite bool) (string, error) {
	files := r.db.cfg.Store.Files()
	var match []string
	for _, f := range files {
		if f == name {
			match = []string{f}
			break
		}
		if strings.HasPrefix(f, name+".") {
			match = append(match, f)
		}
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(match) > 1:
		sort.Strings(match)
		return "", fmt.Errorf("%w: %q matches %s", ErrAmbiguous, name, strings.Join(match, ", "))
	}
	if err := r.az.check(act, sec.Resource{File: name, Box: boxName}); err != nil {
		return "", err
	}
	if forWrite && strings.Contains(name, ".red-xai") {
		return name, nil // a new file named in full: the store decides whether it can be created
	}
	return "", fmt.Errorf("%w: %q", ErrNoFile, name)
}

// staticRead checks, before any record is read, what can be checked without
// one: read on the box and on every field named explicitly. It is skipped
// when the box is not fixed or ownership rules apply (those are decided per
// record at run time, which is always enforced as well).
func (r *run) staticRead(file, boxName string, fields []*Field) error {
	if boxName == "" || r.db.cfg.OwnerOf != nil {
		return nil
	}
	if err := r.az.check(sec.ActionRead, sec.Resource{File: file, Box: boxName}); err != nil {
		return err
	}
	for _, f := range fields {
		if f.Alias != 0 || f.Pseudo != "" || len(f.Names) == 0 {
			continue
		}
		if !r.az.pathAllowed(sec.ActionRead, file, boxName, "", f.Names) {
			return fmt.Errorf("%w: read on %s:%s.%s", sec.ErrDenied, file, boxName, f.Names[len(f.Names)-1])
		}
	}
	return nil
}

// ---- candidates ----------------------------------------------------------

// candidates reads the FROM records that pass the box filter, the read gate
// and the pre-filter conditions, sorted by address.
func (r *run) candidates(file, boxName string, acc access, pre []Expr, nAlias int) ([]*rec, error) {
	var out []*rec
	var stop error
	keep := func(a link.Addr, e *box.Entry) bool {
		if err := r.tick(); err != nil {
			stop = err
			return false
		}
		r.scanned++
		if r.scanned > r.lim.MaxScan {
			stop = fmt.Errorf("%w: statement read more than %d records (add an index, a narrower WHERE or raise Limits.MaxScan)", ErrLimit, r.lim.MaxScan)
			return false
		}
		if a.File != file || (boxName != "" && a.Box != boxName) {
			return true
		}
		rc, ok := r.gate(a, e, e.Value)
		if !ok {
			return true
		}
		if len(pre) > 0 {
			t := make(tuple, nAlias)
			t[0] = rc
			if !r.all(pre, t) {
				return true
			}
		}
		out = append(out, rc)
		return true
	}
	if acc.kind == accScan {
		if err := r.db.cfg.Store.Scan(file, keep); err != nil {
			return nil, err
		}
		if stop != nil {
			return nil, stop
		}
	} else {
		addrs, err := r.addrsOf(acc, file, boxName)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			e, ok, err := r.db.cfg.Store.Get(a)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue // index entry for a record that is gone
			}
			if !keep(a, e) {
				return nil, stop
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return lessAddr(out[i].addr, out[j].addr) })
	return out, nil
}

func (r *run) addrsOf(a access, file, boxName string) ([]link.Addr, error) {
	seen := map[link.Addr]bool{}
	var out []link.Addr
	add := func(x link.Addr) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	var gather func(a access) error
	gather = func(a access) error {
		switch a.kind {
		case accPK:
			for _, id := range a.ids {
				add(link.Addr{File: file, Box: boxName, ID: id, HasID: true})
			}
		case accUnion:
			for _, s := range a.subs {
				if err := gather(s); err != nil {
					return err
				}
			}
		case accIndex:
			lim := r.lim.MaxScan + 1
			var hits []idx.Hit
			var err error
			switch a.mode {
			case "exact":
				hits, err = a.ix.Exact(a.field, a.vals[0], lim)
			case "in":
				for _, v := range a.vals {
					var h []idx.Hit
					if h, err = a.ix.Exact(a.field, v, lim); err != nil {
						break
					}
					hits = append(hits, h...)
				}
			case "prefix":
				hits, err = a.ix.Prefix(a.field, a.pre, lim)
			case "range":
				hits, err = a.ix.Range(a.field, a.lo, a.hi, lim)
			case "text-all":
				hits, err = a.ix.Text(a.field, idx.And, a.terms, lim)
			case "text-any":
				hits, err = a.ix.Text(a.field, idx.Or, a.terms, lim)
			}
			if err != nil {
				return fmt.Errorf("query: index %s: %w", a.ix.Name(), err)
			}
			if len(hits) > r.lim.MaxScan {
				return fmt.Errorf("%w: index %s returned more than %d records", ErrLimit, a.ix.Name(), r.lim.MaxScan)
			}
			for _, h := range hits {
				add(h.Addr)
			}
		}
		return nil
	}
	if err := gather(a); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return lessAddr(out[i], out[j]) })
	return out, nil
}
