package query

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"redx/box"
	"redx/engine/idx"
	"redx/engine/link"
)

// ---- binding -------------------------------------------------------------

var pseudoFields = map[string]bool{"_id": true, "_box": true, "_file": true}

func (s Source) alias() string {
	switch {
	case s.Alias != "":
		return s.Alias
	case s.Box != "":
		return s.Box
	}
	return s.File
}

func synErr(p Pos, near, format string, a ...interface{}) error {
	return &SyntaxError{Pos: p, Msg: fmt.Sprintf(format, a...), Near: near}
}

// bindField resolves f against the aliases visible to it. A first segment
// that names an alias (and is followed by more segments) is a qualifier.
func bindField(f *Field, aliases []string) error {
	segs := f.Segs
	f.Alias = 0
	if len(segs) > 1 && !segs[0].HasID {
		for i, a := range aliases {
			if segs[0].Name == a {
				f.Alias = i
				segs = segs[1:]
				break
			}
		}
	}
	f.Path = segs
	f.Pseudo = ""
	if len(segs) == 1 && !segs[0].HasID && pseudoFields[segs[0].Name] {
		f.Pseudo = segs[0].Name
	}
	f.Names = make([]string, len(segs))
	var sb strings.Builder
	for i, s := range segs {
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(s.Name)
		f.Names[i] = sb.String()
	}
	return nil
}

func bindExpr(e Expr, aliases []string) {
	walk(e, func(x Expr) {
		if f, ok := x.(*Field); ok {
			_ = bindField(f, aliases)
		}
	})
}

func fieldsOf(e Expr) []*Field {
	var out []*Field
	walk(e, func(x Expr) {
		if f, ok := x.(*Field); ok {
			out = append(out, f)
		}
	})
	return out
}

func flattenAnd(e Expr, out []Expr) []Expr {
	if e == nil {
		return out
	}
	if b, ok := e.(*Binary); ok && b.Op == "and" {
		out = flattenAnd(b.L, out)
		return flattenAnd(b.R, out)
	}
	return append(out, e)
}

func flattenOr(e Expr, out []Expr) []Expr {
	if b, ok := e.(*Binary); ok && b.Op == "or" {
		out = flattenOr(b.L, out)
		return flattenOr(b.R, out)
	}
	return append(out, e)
}

func onlyPrimary(e Expr) bool {
	for _, f := range fieldsOf(e) {
		if f.Alias != 0 {
			return false
		}
	}
	return true
}

// validateExprs checks everything that does not depend on a record, so that
// evaluation itself can never fail: LIKE patterns and MATCH words must be
// strings, and a LIKE pattern may only have a trailing %.
func (r *run) validateExpr(e Expr) error {
	var err error
	walk(e, func(x Expr) {
		if err != nil {
			return
		}
		switch n := x.(type) {
		case *Like:
			v := r.constEval(n.Pat)
			if v.Kind != box.KindString {
				err = synErr(n.Pat.exprPos(), "", "LIKE pattern must be a string, got %s", v.Kind)
				return
			}
			if _, _, e2 := likePrefix(v.Str); e2 != nil {
				err = synErr(n.Pat.exprPos(), "", "%v", e2)
			}
		case *Match:
			v := r.constEval(n.Terms)
			if v.Kind != box.KindString {
				err = synErr(n.Terms.exprPos(), "", "MATCH words must be a string, got %s", v.Kind)
			}
		}
	})
	return err
}

// ---- access paths --------------------------------------------------------

type accKind int

const (
	accScan accKind = iota
	accPK
	accIndex
	accUnion
)

type access struct {
	kind  accKind
	ids   []int64 // accPK
	ix    *idx.Index
	field string
	mode  string // exact, in, prefix, range, text-all, text-any
	vals  []box.Value
	pre   string
	lo    *idx.Bound
	hi    *idx.Bound
	terms []string
	subs  []access
	text  string // the condition this access answers, for EXPLAIN
}

func (a access) rank() int {
	switch a.kind {
	case accPK:
		return 0
	case accIndex:
		switch a.mode {
		case "exact":
			return 1
		case "in":
			return 2
		case "text-all", "text-any":
			return 3
		case "prefix":
			return 4
		case "range":
			return 5
		}
	case accUnion:
		return 6
	}
	return 99
}

func (a access) label() string {
	switch a.kind {
	case accPK:
		return "pk"
	case accIndex:
		return "index:" + a.ix.Name()
	case accUnion:
		parts := make([]string, len(a.subs))
		for i, s := range a.subs {
			parts[i] = s.label()
		}
		return "union(" + strings.Join(parts, ",") + ")"
	}
	return "scan"
}

func (a access) describe(prefix string) []string {
	switch a.kind {
	case accPK:
		return []string{prefix + fmt.Sprintf("PRIMARY KEY LOOKUP %s (%d record(s))", a.text, len(a.ids))}
	case accIndex:
		return []string{prefix + fmt.Sprintf("INDEX %s (%s) on %s: %s", a.ix.Name(), a.mode, a.field, a.text)}
	case accUnion:
		out := []string{prefix + fmt.Sprintf("UNION of %d lookups (duplicates removed):", len(a.subs))}
		for _, s := range a.subs {
			out = append(out, s.describe(prefix+"  - ")...)
		}
		return out
	}
	return []string{prefix + "FULL SCAN of every record in the file (no usable index)"}
}

// constEval evaluates an expression that contains no field references.
func (r *run) constEval(e Expr) box.Value { return r.eval(e, nil) }

func isConst(e Expr) bool {
	c := true
	walk(e, func(x Expr) {
		switch x.(type) {
		case *Field, *Match:
			c = false
		}
	})
	return c
}

// indexFor finds a usable index of the wanted kind over (file, box, field).
func (r *run) indexFor(kind idx.Kind, file, boxName, fieldKey string) *idx.Index {
	if r.db.cfg.Indexes == nil {
		return nil
	}
	for _, ix := range r.db.cfg.Indexes.Indexes() {
		d := ix.Decl()
		if d.File != file || d.Kind != kind {
			continue
		}
		// an index restricted to one box does not cover a whole-file query
		if d.Box != "" && d.Box != boxName {
			continue
		}
		for _, f := range d.Fields {
			if f == fieldKey {
				return ix
			}
		}
	}
	return nil
}

func fieldKeyOf(f *Field) (string, bool) {
	if f.Pseudo != "" || f.Alias != 0 || len(f.Path) == 0 {
		return "", false
	}
	return box.SegsString(f.Path), true
}

func indexable(v box.Value) bool {
	switch v.Kind {
	case box.KindString:
		return utf8.ValidString(v.Str) && len(v.Str) <= idx.MaxTermBytes
	case box.KindNumber:
		return !math.IsNaN(v.Num) && !math.IsInf(v.Num, 0)
	case box.KindBool:
		return true
	}
	return false
}

// splitCmp returns (field, const value, op normalised so the field is on the left).
func (r *run) splitCmp(b *Binary) (*Field, box.Value, string, bool) {
	flip := map[string]string{"=": "=", "<": ">", "<=": ">=", ">": "<", ">=": "<="}
	if f, ok := b.L.(*Field); ok && isConst(b.R) {
		return f, r.constEval(b.R), b.Op, true
	}
	if f, ok := b.R.(*Field); ok && isConst(b.L) {
		if op, ok := flip[b.Op]; ok {
			return f, r.constEval(b.L), op, true
		}
	}
	return nil, box.Value{}, "", false
}

func wholeID(v box.Value) (int64, bool) {
	if v.Kind != box.KindNumber || v.Num != math.Trunc(v.Num) || math.Abs(v.Num) > 1<<53 {
		return 0, false
	}
	return int64(v.Num), true
}

type rangeCand struct {
	field string
	ix    *idx.Index
	lo    *idx.Bound
	hi    *idx.Bound
	texts []string
}

func tighterLo(a, b *idx.Bound) *idx.Bound {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Value > a.Value || (b.Value == a.Value && b.Exclusive) {
		return b
	}
	return a
}

func tighterHi(a, b *idx.Bound) *idx.Bound {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.Value < a.Value || (b.Value == a.Value && b.Exclusive) {
		return b
	}
	return a
}

// leaf returns an access path for one condition, if an index can answer it.
// The answer is always a superset of the matches; the full condition is
// re-checked on every fetched record.
func (r *run) leaf(e Expr, file, boxName string, params []box.Value) (access, *rangeCand) {
	none := access{}
	str := func(x Expr) string { return exprString(x, params) }
	switch n := e.(type) {
	case *Binary:
		f, v, op, ok := r.splitCmp(n)
		if !ok {
			return none, nil
		}
		switch op {
		case "=":
			if f.Pseudo == "_id" && f.Alias == 0 && boxName != "" {
				if id, ok := wholeID(v); ok {
					return access{kind: accPK, ids: []int64{id}, text: str(e)}, nil
				}
				return none, nil
			}
			key, ok := fieldKeyOf(f)
			if !ok || !indexable(v) {
				return none, nil
			}
			if ix := r.indexFor(idx.KindExact, file, boxName, key); ix != nil {
				return access{kind: accIndex, ix: ix, field: key, mode: "exact", vals: []box.Value{v}, text: str(e)}, nil
			}
		case "<", "<=", ">", ">=":
			key, ok := fieldKeyOf(f)
			if !ok || v.Kind != box.KindNumber || !indexable(v) {
				return none, nil
			}
			ix := r.indexFor(idx.KindRange, file, boxName, key)
			if ix == nil {
				return none, nil
			}
			rc := &rangeCand{field: key, ix: ix, texts: []string{str(e)}}
			switch op {
			case "<":
				rc.hi = idx.Excl(v.Num)
			case "<=":
				rc.hi = idx.Incl(v.Num)
			case ">":
				rc.lo = idx.Excl(v.Num)
			case ">=":
				rc.lo = idx.Incl(v.Num)
			}
			return none, rc
		}
	case *Between:
		if n.Neg {
			return none, nil
		}
		f, ok := n.X.(*Field)
		if !ok || !isConst(n.Lo) || !isConst(n.Hi) {
			return none, nil
		}
		key, ok := fieldKeyOf(f)
		lo, hi := r.constEval(n.Lo), r.constEval(n.Hi)
		if !ok || lo.Kind != box.KindNumber || hi.Kind != box.KindNumber || !indexable(lo) || !indexable(hi) {
			return none, nil
		}
		if ix := r.indexFor(idx.KindRange, file, boxName, key); ix != nil {
			return none, &rangeCand{field: key, ix: ix, lo: idx.Incl(lo.Num), hi: idx.Incl(hi.Num), texts: []string{str(e)}}
		}
	case *In:
		if n.Neg {
			return none, nil
		}
		f, ok := n.X.(*Field)
		if !ok {
			return none, nil
		}
		var vals []box.Value
		for _, it := range n.List {
			if !isConst(it) {
				return none, nil
			}
			vals = append(vals, r.constEval(it))
		}
		if f.Pseudo == "_id" && f.Alias == 0 && boxName != "" {
			var ids []int64
			for _, v := range vals {
				if id, ok := wholeID(v); ok {
					ids = append(ids, id)
				}
			}
			return access{kind: accPK, ids: ids, text: str(e)}, nil
		}
		key, ok := fieldKeyOf(f)
		if !ok {
			return none, nil
		}
		for _, v := range vals {
			if !indexable(v) {
				if v.Kind == box.KindNell || v.Kind == box.KindArray || v.Kind == box.KindBox {
					return none, nil // a non-indexable member may match by scan semantics
				}
				return none, nil
			}
		}
		if ix := r.indexFor(idx.KindExact, file, boxName, key); ix != nil {
			return access{kind: accIndex, ix: ix, field: key, mode: "in", vals: vals, text: str(e)}, nil
		}
	case *Like:
		if n.Neg {
			return none, nil
		}
		f, ok := n.X.(*Field)
		key, ok2 := "", false
		if ok {
			key, ok2 = fieldKeyOf(f)
		}
		pv := r.constEval(n.Pat)
		if !ok2 || pv.Kind != box.KindString {
			return none, nil
		}
		prefix, exact, err := likePrefix(pv.Str)
		if err != nil || !indexable(box.String(prefix)) {
			return none, nil
		}
		if exact {
			if ix := r.indexFor(idx.KindExact, file, boxName, key); ix != nil {
				return access{kind: accIndex, ix: ix, field: key, mode: "exact", vals: []box.Value{box.String(prefix)}, text: str(e)}, nil
			}
			return none, nil
		}
		if prefix == "" {
			return none, nil
		}
		if ix := r.indexFor(idx.KindPrefix, file, boxName, key); ix != nil {
			return access{kind: accIndex, ix: ix, field: key, mode: "prefix", pre: prefix, text: str(e)}, nil
		}
	case *Match:
		key, ok := fieldKeyOf(n.Field)
		tv := r.constEval(n.Terms)
		if !ok || tv.Kind != box.KindString {
			return none, nil
		}
		terms := termsOf(tv)
		if len(terms) == 0 {
			return none, nil
		}
		if ix := r.indexFor(idx.KindText, file, boxName, key); ix != nil {
			mode := "text-all"
			if n.Any {
				mode = "text-any"
			}
			return access{kind: accIndex, ix: ix, field: key, mode: mode, terms: terms, text: str(e)}, nil
		}
	}
	return none, nil
}

// accessOf chooses an access path for one condition (possibly an AND or an
// OR). It returns (zero, false) when only a scan is possible.
func (r *run) accessOf(e Expr, file, boxName string, params []box.Value) (access, bool) {
	switch n := e.(type) {
	case *Binary:
		switch n.Op {
		case "and":
			return r.accessAnd(flattenAnd(e, nil), file, boxName, params)
		case "or":
			parts := flattenOr(e, nil)
			var subs []access
			for _, p := range parts {
				a, ok := r.accessOf(p, file, boxName, params)
				if !ok {
					return access{}, false
				}
				if a.kind == accUnion {
					subs = append(subs, a.subs...)
				} else {
					subs = append(subs, a)
				}
			}
			return access{kind: accUnion, subs: subs, text: exprString(e, params)}, true
		}
	}
	a, rc := r.leaf(e, file, boxName, params)
	if rc != nil {
		return rc.access(params), true
	}
	return a, a.kind != accScan
}

func (rc *rangeCand) access(params []box.Value) access {
	return access{kind: accIndex, ix: rc.ix, field: rc.field, mode: "range", lo: rc.lo, hi: rc.hi, text: strings.Join(rc.texts, " AND ")}
}

// accessAnd picks the best access path among AND-ed conditions; range
// bounds on the same indexed field are merged into one range lookup.
func (r *run) accessAnd(conj []Expr, file, boxName string, params []box.Value) (access, bool) {
	var best *access
	ranges := map[string]*rangeCand{}
	var order []string
	for _, c := range conj {
		var a access
		var ok bool
		var rc *rangeCand
		if b, isB := c.(*Binary); isB && b.Op == "or" {
			a, ok = r.accessOf(c, file, boxName, params)
		} else {
			a, rc = r.leaf(c, file, boxName, params)
			ok = a.kind != accScan
		}
		if rc != nil {
			cur := ranges[rc.field+"\x00"+rc.ix.Name()]
			if cur == nil {
				ranges[rc.field+"\x00"+rc.ix.Name()] = rc
				order = append(order, rc.field+"\x00"+rc.ix.Name())
			} else {
				cur.lo = tighterLo(cur.lo, rc.lo)
				cur.hi = tighterHi(cur.hi, rc.hi)
				cur.texts = append(cur.texts, rc.texts...)
			}
			continue
		}
		if ok && (best == nil || a.rank() < best.rank()) {
			cp := a
			best = &cp
		}
	}
	for _, k := range order {
		a := ranges[k].access(params)
		if best == nil || a.rank() < best.rank() {
			cp := a
			best = &cp
		}
	}
	if best == nil {
		return access{}, false
	}
	return *best, true
}

// ---- helpers shared with exec -------------------------------------------

func validIdentName(s string) bool {
	if s == "" || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	switch strings.ToUpper(s) {
	case "TRUE", "FALSE", "NELL", "ID":
		return false
	}
	for i, c := range s {
		if c == '_' || unicode.IsLetter(c) {
			continue
		}
		if i > 0 && (unicode.IsDigit(c) || c == '-') {
			continue
		}
		return false
	}
	return true
}

func lessAddr(a, b link.Addr) bool {
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
