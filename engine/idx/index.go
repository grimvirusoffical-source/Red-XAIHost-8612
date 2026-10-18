package idx

import (
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"redx/box"
	"redx/engine/link"
)

// Addr addresses one top-level record (same type as link.Addr).
type Addr = link.Addr

// Scanner is the read side of a store: it visits every record of a file.
// link.Store satisfies it.
type Scanner interface {
	Scan(file string, fn func(Addr, *box.Entry) bool) error
}

func lessAddr(a, b Addr) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Box != b.Box {
		return a.Box < b.Box
	}
	if a.HasID != b.HasID {
		return !a.HasID
	}
	return a.ID < b.ID
}

// item is one row of a sorted (prefix or range) index.
type item struct {
	field string
	key   string
	num   float64
	addr  Addr
}

func cmpItem(kind Kind, x, y item) int {
	if x.field != y.field {
		if x.field < y.field {
			return -1
		}
		return 1
	}
	if kind == KindRange {
		if x.num != y.num {
			if x.num < y.num {
				return -1
			}
			return 1
		}
	} else if x.key != y.key {
		if x.key < y.key {
			return -1
		}
		return 1
	}
	switch {
	case lessAddr(x.addr, y.addr):
		return -1
	case lessAddr(y.addr, x.addr):
		return 1
	}
	return 0
}

type recEntry struct {
	terms []term
	dig   [32]byte
}

// state is the data of one index. It is replaced as a whole on rebuild.
type state struct {
	kind   Kind
	recs   map[Addr]*recEntry
	post   map[string]map[Addr]struct{} // exact and text: field\x00key -> records
	sorted []item                       // prefix and range
	digest [32]byte                     // XOR of record digests
	bulk   bool                         // sorted is unsorted until finish
	trunc  int
}

func newState(kind Kind) *state {
	s := &state{kind: kind, recs: map[Addr]*recEntry{}}
	if kind == KindExact || kind == KindText {
		s.post = map[string]map[Addr]struct{}{}
	}
	return s
}

func postKey(field, key string) string { return field + "\x00" + key }

func (s *state) mkItem(a Addr, t term) item {
	it := item{field: t.Field, key: t.Key, addr: a}
	if s.kind == KindRange {
		it.num, _ = strconv.ParseFloat(t.Key, 64)
	}
	return it
}

func (s *state) remove(a Addr) {
	old := s.recs[a]
	if old == nil {
		return
	}
	for i := range s.digest {
		s.digest[i] ^= old.dig[i]
	}
	for _, t := range old.terms {
		if s.post != nil {
			k := postKey(t.Field, t.Key)
			if m := s.post[k]; m != nil {
				delete(m, a)
				if len(m) == 0 {
					delete(s.post, k)
				}
			}
		} else {
			it := s.mkItem(a, t)
			i := sort.Search(len(s.sorted), func(i int) bool { return cmpItem(s.kind, s.sorted[i], it) >= 0 })
			if i < len(s.sorted) && cmpItem(s.kind, s.sorted[i], it) == 0 {
				s.sorted = append(s.sorted[:i], s.sorted[i+1:]...)
			}
		}
	}
	delete(s.recs, a)
}

// put replaces the terms of a. Empty terms remove the record.
func (s *state) put(a Addr, terms []term) {
	if s.recs[a] != nil {
		s.remove(a)
	}
	if len(terms) == 0 {
		return
	}
	r := &recEntry{terms: terms, dig: recDigest(a, terms)}
	s.recs[a] = r
	for i := range s.digest {
		s.digest[i] ^= r.dig[i]
	}
	for _, t := range terms {
		if s.post != nil {
			k := postKey(t.Field, t.Key)
			m := s.post[k]
			if m == nil {
				m = map[Addr]struct{}{}
				s.post[k] = m
			}
			m[a] = struct{}{}
			continue
		}
		it := s.mkItem(a, t)
		if s.bulk {
			s.sorted = append(s.sorted, it)
			continue
		}
		i := sort.Search(len(s.sorted), func(i int) bool { return cmpItem(s.kind, s.sorted[i], it) >= 0 })
		s.sorted = append(s.sorted, item{})
		copy(s.sorted[i+1:], s.sorted[i:])
		s.sorted[i] = it
	}
}

func (s *state) finish() {
	if s.bulk {
		sort.Slice(s.sorted, func(i, j int) bool { return cmpItem(s.kind, s.sorted[i], s.sorted[j]) < 0 })
		s.bulk = false
	}
}

func (s *state) termCount() int {
	n := 0
	for _, r := range s.recs {
		n += len(r.terms)
	}
	return n
}

type event struct {
	a     Addr
	terms []term // nil means delete
}

// Index is one live index. All methods are safe for concurrent use.
type Index struct {
	decl Decl

	mu         sync.RWMutex
	st         *state
	seq        uint64
	rebuilding bool
	pending    []event
	truncated  uint64
}

// New creates an empty index for a declaration.
func New(d Decl) (*Index, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &Index{decl: d, st: newState(d.Kind)}, nil
}

// Decl returns the declaration.
func (ix *Index) Decl() Decl { return ix.decl }

// Name returns the index name.
func (ix *Index) Name() string { return ix.decl.Name }

// Observer is told about every successful write to the source. Index and
// Manager implement it. The entry must not be retained by the observer.
type Observer interface {
	OnPut(a Addr, e *box.Entry)
	OnDelete(a Addr)
}

// OnPut applies a put (insert or replace) of record a. Records outside the
// declaration (other file or box) are ignored. It is idempotent.
func (ix *Index) OnPut(a Addr, e *box.Entry) {
	if !ix.decl.matches(a) {
		return
	}
	terms, trunc := ix.decl.extract(e)
	ix.apply(event{a: a, terms: terms}, trunc)
}

// OnDelete applies a delete of record a.
func (ix *Index) OnDelete(a Addr) {
	if !ix.decl.matches(a) {
		return
	}
	ix.apply(event{a: a}, false)
}

func (ix *Index) apply(ev event, trunc bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if trunc {
		ix.truncated++
	}
	ix.st.put(ev.a, ev.terms)
	ix.seq++
	if ix.rebuilding {
		ix.pending = append(ix.pending, ev)
	}
}

// Rebuild replaces the index contents with a fresh scan of the source.
// Writes that arrive while the scan runs are buffered and replayed, so
// queries keep working (on the old data) during the rebuild and no write is
// lost.
func (ix *Index) Rebuild(src Scanner) error {
	if err := ix.beginRebuild(); err != nil {
		return err
	}
	ns, err := ix.scanState(src)
	if err != nil {
		ix.abortRebuild()
		return err
	}
	ix.commitRebuild(ns, nil)
	return nil
}

func (ix *Index) beginRebuild() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.rebuilding {
		return ErrRebuilding
	}
	ix.rebuilding = true
	ix.pending = nil
	return nil
}

func (ix *Index) abortRebuild() {
	ix.mu.Lock()
	ix.rebuilding = false
	ix.pending = nil
	ix.mu.Unlock()
}

// commitRebuild installs ns (replaying buffered writes onto it). If seq is
// non-nil the index sequence number is set to *seq (snapshot restore) before
// the replayed events are counted.
func (ix *Index) commitRebuild(ns *state, seq *uint64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, ev := range ix.pending {
		ns.put(ev.a, ev.terms)
	}
	ix.pending = nil
	ix.rebuilding = false
	ix.st = ns
	if seq != nil {
		ix.seq = *seq
	}
}

func (ix *Index) scanState(src Scanner) (*state, error) {
	ns := newState(ix.decl.Kind)
	ns.bulk = true
	var trunc int
	err := src.Scan(ix.decl.File, func(a Addr, e *box.Entry) bool {
		if ix.decl.matches(a) {
			terms, t := ix.decl.extract(e)
			if t {
				trunc++
			}
			ns.put(a, terms)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	ns.finish()
	ns.trunc = trunc
	return ns, nil
}

// Stats describes an index.
type Stats struct {
	Records   int    // indexed records (records with at least one term)
	Terms     int    // total (record, term) pairs
	Seq       uint64 // number of writes applied (restored from a snapshot on Load)
	Truncated uint64 // records whose terms were capped at MaxTermsPerRecord
	Digest    string // hex of the XOR of per-record checksums
}

// Stats returns a consistent summary.
func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return Stats{
		Records: len(ix.st.recs), Terms: ix.st.termCount(), Seq: ix.seq,
		Truncated: ix.truncated + uint64(ix.st.trunc),
		Digest:    hex.EncodeToString(ix.st.digest[:]),
	}
}

// Diff is the result of Check: records where the index and the source
// disagree.
type Diff struct {
	Missing []Addr // in the source (with terms) but not in the index
	Extra   []Addr // in the index but not (or no longer) in the source
	Changed []Addr // in both but with different terms
}

// Stale reports whether the index differs from the source.
func (d Diff) Stale() bool { return len(d.Missing)+len(d.Extra)+len(d.Changed) > 0 }

// Check compares the live index with a fresh scan of the source. It is O(n)
// and only meaningful while no writes are in flight.
func (ix *Index) Check(src Scanner) (Diff, error) {
	ns, err := ix.scanState(src)
	if err != nil {
		return Diff{}, err
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var d Diff
	for a, r := range ns.recs {
		o := ix.st.recs[a]
		switch {
		case o == nil:
			d.Missing = append(d.Missing, a)
		case o.dig != r.dig:
			d.Changed = append(d.Changed, a)
		}
	}
	for a := range ix.st.recs {
		if ns.recs[a] == nil {
			d.Extra = append(d.Extra, a)
		}
	}
	for _, s := range [][]Addr{d.Missing, d.Extra, d.Changed} {
		sort.Slice(s, func(i, j int) bool { return lessAddr(s[i], s[j]) })
	}
	return d, nil
}

// ---- queries -------------------------------------------------------------

// Hit is one query result.
type Hit struct {
	Addr  Addr
	Field string
	// Value is the indexed value that matched: the queried value for exact,
	// the (folded, if Fold) string for prefix, the number for range, and
	// NELL for text.
	Value box.Value
}

// Op combines the terms of a text query.
type Op string

// Text query operators.
const (
	And Op = "and"
	Or  Op = "or"
)

func (ix *Index) need(k Kind) error {
	if ix.decl.Kind != k {
		return wrongKind(ix.decl, k)
	}
	return nil
}

// Exact returns the records whose field equals v (same type and value; a
// number never equals a string). Results are ordered by record address.
// limit <= 0 means no limit.
func (ix *Index) Exact(field string, v box.Value, limit int) ([]Hit, error) {
	if err := ix.need(KindExact); err != nil {
		return nil, err
	}
	f, err := ix.decl.fieldIndex(field)
	if err != nil {
		return nil, err
	}
	key, ok := exactKey(v, ix.decl.Fold)
	if !ok {
		return nil, badQuery("value %v cannot be indexed", v.Kind)
	}
	ix.mu.RLock()
	addrs := make([]Addr, 0, len(ix.st.post[postKey(f, key)]))
	for a := range ix.st.post[postKey(f, key)] {
		addrs = append(addrs, a)
	}
	ix.mu.RUnlock()
	sortAddrs(addrs)
	return hits(addrs, f, v, limit), nil
}

func hits(addrs []Addr, field string, v box.Value, limit int) []Hit {
	if limit > 0 && len(addrs) > limit {
		addrs = addrs[:limit]
	}
	out := make([]Hit, len(addrs))
	for i, a := range addrs {
		out[i] = Hit{Addr: a, Field: field, Value: v}
	}
	return out
}

func sortAddrs(a []Addr) { sort.Slice(a, func(i, j int) bool { return lessAddr(a[i], a[j]) }) }

// Prefix returns the records with a string value starting with prefix
// (case-insensitive if the index folds). Results are ordered by the matched
// value, then address; a record with several matching values appears once.
func (ix *Index) Prefix(field, prefix string, limit int) ([]Hit, error) {
	if err := ix.need(KindPrefix); err != nil {
		return nil, err
	}
	f, err := ix.decl.fieldIndex(field)
	if err != nil {
		return nil, err
	}
	if ix.decl.Fold {
		prefix = strings.ToLower(prefix)
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := ix.st.sorted
	i := sort.Search(len(s), func(i int) bool {
		if s[i].field != f {
			return s[i].field > f
		}
		return s[i].key >= prefix
	})
	seen := map[Addr]bool{}
	var out []Hit
	for ; i < len(s) && s[i].field == f && strings.HasPrefix(s[i].key, prefix); i++ {
		if seen[s[i].addr] {
			continue
		}
		seen[s[i].addr] = true
		out = append(out, Hit{Addr: s[i].addr, Field: f, Value: box.String(s[i].key)})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Bound is one end of a numeric range.
type Bound struct {
	Value     float64
	Exclusive bool
}

// Incl and Excl build bounds.
func Incl(v float64) *Bound { return &Bound{Value: v} }
func Excl(v float64) *Bound { return &Bound{Value: v, Exclusive: true} }

// Range returns the records with a number between lo and hi (nil means
// unbounded). Results are ordered by number, then address; a record with
// several values in range appears once, at its smallest.
func (ix *Index) Range(field string, lo, hi *Bound, limit int) ([]Hit, error) {
	if err := ix.need(KindRange); err != nil {
		return nil, err
	}
	f, err := ix.decl.fieldIndex(field)
	if err != nil {
		return nil, err
	}
	for _, b := range []*Bound{lo, hi} {
		if b != nil && (math.IsNaN(b.Value) || math.IsInf(b.Value, 0)) {
			return nil, badQuery("range bound must be a finite number")
		}
	}
	start := math.Inf(-1)
	if lo != nil {
		start = lo.Value
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := ix.st.sorted
	i := sort.Search(len(s), func(i int) bool {
		if s[i].field != f {
			return s[i].field > f
		}
		return s[i].num >= start
	})
	seen := map[Addr]bool{}
	var out []Hit
	for ; i < len(s) && s[i].field == f; i++ {
		n := s[i].num
		if lo != nil && lo.Exclusive && n == lo.Value {
			continue
		}
		if hi != nil && (n > hi.Value || (hi.Exclusive && n == hi.Value)) {
			break
		}
		if seen[s[i].addr] {
			continue
		}
		seen[s[i].addr] = true
		out = append(out, Hit{Addr: s[i].addr, Field: f, Value: box.Number(n)})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Text searches word tokens. Every string in terms is tokenized (so "Red
// dragon" is two tokens) and the tokens are combined with op: And needs all
// of them, Or needs any. Results are ordered by record address.
func (ix *Index) Text(field string, op Op, terms []string, limit int) ([]Hit, error) {
	if err := ix.need(KindText); err != nil {
		return nil, err
	}
	f, err := ix.decl.fieldIndex(field)
	if err != nil {
		return nil, err
	}
	if op != And && op != Or {
		return nil, badQuery("operator must be and/or, got %q", string(op))
	}
	seen := map[string]bool{}
	var toks []string
	for _, t := range terms {
		for _, tok := range Tokenize(t) {
			if !seen[tok] {
				seen[tok] = true
				toks = append(toks, tok)
			}
		}
	}
	if len(toks) == 0 {
		return nil, badQuery("no searchable tokens in query")
	}
	ix.mu.RLock()
	sets := make([]map[Addr]struct{}, len(toks))
	for i, t := range toks {
		sets[i] = ix.st.post[postKey(f, t)]
	}
	var addrs []Addr
	if op == Or {
		u := map[Addr]struct{}{}
		for _, s := range sets {
			for a := range s {
				u[a] = struct{}{}
			}
		}
		for a := range u {
			addrs = append(addrs, a)
		}
	} else {
		sort.Slice(sets, func(i, j int) bool { return len(sets[i]) < len(sets[j]) })
		for a := range sets[0] {
			all := true
			for _, s := range sets[1:] {
				if _, ok := s[a]; !ok {
					all = false
					break
				}
			}
			if all {
				addrs = append(addrs, a)
			}
		}
	}
	ix.mu.RUnlock()
	sortAddrs(addrs)
	return hits(addrs, f, box.Nell(), limit), nil
}
