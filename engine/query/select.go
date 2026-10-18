package query

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"redx/box"
	"redx/engine/link"
	"redx/engine/sec"
)

type selPlan struct {
	sel       *Select
	aliases   []string
	file, box string
	pre, post []Expr
	acc       access
	limit     int // -1 = none
	offset    int
	items     []SelectItem
}

func (r *run) countArg(e Expr, what string) (int, error) {
	if e == nil {
		return -1, nil
	}
	v := r.constEval(e)
	if v.Kind != box.KindNumber || v.Num != math.Trunc(v.Num) || v.Num < 0 || v.Num > 1<<40 {
		return 0, fmt.Errorf("%w: %s must be a non-negative whole number, got %s", ErrParams, what, fmtValue(v))
	}
	return int(v.Num), nil
}

func (r *run) planSelect(sel *Select) (*selPlan, error) {
	pl := &selPlan{sel: sel, items: append([]SelectItem(nil), sel.Items...)}
	pl.aliases = []string{sel.From.alias()}
	for _, j := range sel.Joins {
		for _, a := range pl.aliases {
			if a == j.Alias {
				return nil, synErr(j.P, j.Alias, "alias %q is used twice", j.Alias)
			}
		}
		pl.aliases = append(pl.aliases, j.Alias)
	}
	if len(sel.Joins) > 0 && r.db.cfg.Links == nil {
		return nil, fmt.Errorf("%w: JOIN follows @> links", ErrNoLinks)
	}
	// ORDER BY may name a select alias
	order := make([]OrderItem, len(sel.Order))
	copy(order, sel.Order)
	for i, o := range order {
		if f, ok := o.Expr.(*Field); ok && len(f.Segs) == 1 && !f.Segs[0].HasID {
			for _, it := range pl.items {
				if !it.Star && it.As != "" && it.As == f.Segs[0].Name {
					order[i].Expr = it.Expr
					break
				}
			}
		}
	}
	for i, j := range sel.Joins {
		visible := pl.aliases[:i+1]
		bindField(j.Via, visible)
		if len(j.Via.Path) == len(j.Via.Segs) || j.Via.Pseudo != "" {
			return nil, synErr(j.Via.P, j.Via.Raw, "JOIN path must be alias.field, where alias is an earlier FROM or JOIN alias (%s)", strings.Join(visible, ", "))
		}
	}
	var exprs []Expr
	for _, it := range pl.items {
		if it.Star {
			if it.StarAlias != "" {
				found := false
				for _, a := range pl.aliases {
					found = found || a == it.StarAlias
				}
				if !found {
					return nil, synErr(it.P, it.Text, "unknown alias %q in %s", it.StarAlias, it.Text)
				}
			}
			continue
		}
		exprs = append(exprs, it.Expr)
	}
	exprs = append(exprs, sel.Where)
	for _, o := range order {
		exprs = append(exprs, o.Expr)
	}
	var allFields []*Field
	for _, e := range exprs {
		if e == nil {
			continue
		}
		bindExpr(e, pl.aliases)
		if err := r.validateExpr(e); err != nil {
			return nil, err
		}
		allFields = append(allFields, fieldsOf(e)...)
	}
	sel.Order = order
	var err error
	if pl.limit, err = r.countArg(sel.Limit, "LIMIT"); err != nil {
		return nil, err
	}
	if pl.offset, err = r.countArg(sel.Offset, "OFFSET"); err != nil {
		return nil, err
	}
	if pl.offset < 0 {
		pl.offset = 0
	}
	if pl.limit > r.lim.MaxRows {
		return nil, fmt.Errorf("%w: LIMIT %d exceeds the maximum of %d rows per query", ErrLimit, pl.limit, r.lim.MaxRows)
	}
	pl.box = sel.From.Box
	if pl.file, err = r.resolveFile(sel.From.File, pl.box, sec.ActionRead, false); err != nil {
		return nil, err
	}
	if err := r.staticRead(pl.file, pl.box, allFields); err != nil {
		return nil, err
	}
	for _, c := range flattenAnd(sel.Where, nil) {
		if onlyPrimary(c) {
			pl.pre = append(pl.pre, c)
		} else {
			pl.post = append(pl.post, c)
		}
	}
	pl.acc = access{}
	if len(pl.pre) > 0 {
		if a, ok := r.accessAnd(pl.pre, pl.file, pl.box, r.params); ok {
			pl.acc = a
		}
	}
	return pl, nil
}

func (r *run) explainSelect(pl *selPlan) *Result {
	s := pl.sel
	var lines []string
	lines = append(lines, fmt.Sprintf("SELECT from %s:%s AS %s", pl.file, orStar(pl.box), pl.aliases[0]))
	lines = append(lines, pl.acc.describe("  access: ")...)
	if len(pl.pre) > 0 {
		lines = append(lines, "  filter: "+condString(pl.pre, r.params)+"  (applied to every fetched record, including after an index lookup)")
	}
	for i, j := range s.Joins {
		kind := "INNER"
		if j.Left {
			kind = "LEFT"
		}
		lines = append(lines, fmt.Sprintf("  join: %s JOIN %s AS %s via @> links (one lookup per link)", kind, j.Via.Raw, pl.aliases[i+1]))
	}
	if len(pl.post) > 0 {
		lines = append(lines, "  post-join filter: "+condString(pl.post, r.params))
	}
	if len(s.Order) > 0 {
		parts := make([]string, len(s.Order))
		for i, o := range s.Order {
			parts[i] = exprString(o.Expr, r.params)
			if o.Desc {
				parts[i] += " DESC"
			}
		}
		lines = append(lines, "  sort: "+strings.Join(parts, ", "))
	} else {
		lines = append(lines, "  order: by record address (deterministic)")
	}
	lim := "none"
	if pl.limit >= 0 {
		lim = strconv.Itoa(pl.limit)
	}
	lines = append(lines, fmt.Sprintf("  limit: %s, offset: %d, max rows: %d, max records read: %d", lim, pl.offset, r.lim.MaxRows, r.lim.MaxScan))
	return planResult(lines)
}

func orStar(b string) string {
	if b == "" {
		return "*"
	}
	return b
}

func condString(conds []Expr, params []box.Value) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = exprString(c, params)
	}
	return strings.Join(parts, " AND ")
}

func planResult(lines []string) *Result {
	res := &Result{Kind: "explain", Columns: []string{"plan"}, Plan: lines}
	for _, l := range lines {
		res.Rows = append(res.Rows, []box.Value{box.String(l)})
	}
	return res
}

type sortRow struct {
	t    tuple
	keys []box.Value
}

func (r *run) doSelect(sel *Select, explain bool) (*Result, error) {
	pl, err := r.planSelect(sel)
	if err != nil {
		return nil, err
	}
	if explain {
		return r.explainSelect(pl), nil
	}
	n := len(pl.aliases)
	cands, err := r.candidates(pl.file, pl.box, pl.acc, pl.pre, n)
	if err != nil {
		return nil, err
	}
	ordered := len(sel.Order) > 0
	maxCollect := math.MaxInt32
	if !ordered {
		if pl.limit >= 0 {
			maxCollect = pl.offset + pl.limit
		} else {
			maxCollect = pl.offset + r.lim.MaxRows + 1
		}
	}
	var rows []sortRow
	emit := func(t tuple) (bool, error) {
		sr := sortRow{t: t}
		if ordered {
			sr.keys = make([]box.Value, len(sel.Order))
			for i, o := range sel.Order {
				sr.keys[i] = r.eval(o.Expr, t)
			}
		}
		rows = append(rows, sr)
		if len(rows) > r.lim.MaxScan {
			return false, fmt.Errorf("%w: more than %d candidate rows", ErrLimit, r.lim.MaxScan)
		}
		return len(rows) < maxCollect, nil
	}
	for _, c := range cands {
		t := make(tuple, n)
		t[0] = c
		cont, err := r.expandJoins(pl, t, 0, emit)
		if err != nil {
			return nil, err
		}
		if !cont {
			break
		}
	}
	if ordered {
		sort.SliceStable(rows, func(i, j int) bool {
			for k, o := range sel.Order {
				c := cmpSort(rows[i].keys[k], rows[j].keys[k])
				if c == 0 {
					continue
				}
				if o.Desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
	}
	if pl.offset >= len(rows) {
		rows = nil
	} else {
		rows = rows[pl.offset:]
	}
	if pl.limit >= 0 && len(rows) > pl.limit {
		rows = rows[:pl.limit]
	}
	if pl.limit < 0 && len(rows) > r.lim.MaxRows {
		return nil, fmt.Errorf("%w: the query matches more than %d rows; add a LIMIT (or raise Limits.MaxRows)", ErrLimit, r.lim.MaxRows)
	}
	return r.assemble(pl, rows)
}

func (r *run) expandJoins(pl *selPlan, t tuple, j int, emit func(tuple) (bool, error)) (bool, error) {
	if err := r.tick(); err != nil {
		return false, err
	}
	if j == len(pl.sel.Joins) {
		if !r.all(pl.post, t) {
			return true, nil
		}
		return emit(t)
	}
	jn := pl.sel.Joins[j]
	var targets []*rec
	if src := t[jn.Via.Alias]; src != nil {
		var err error
		if targets, err = r.followLinks(src, jn.Via); err != nil {
			return false, err
		}
	}
	if len(targets) == 0 {
		if !jn.Left {
			return true, nil
		}
		return r.expandJoins(pl, append(tuple(nil), t...), j+1, emit)
	}
	for _, tg := range targets {
		t2 := append(tuple(nil), t...)
		t2[j+1] = tg
		cont, err := r.expandJoins(pl, t2, j+1, emit)
		if err != nil || !cont {
			return cont, err
		}
	}
	return true, nil
}

// linkTargets lists the @> (read) link targets held in a value: a link, or an
// array of links. @!> cascade links are not read links and are not followed.
func linkTargets(v box.Value) []box.Ref {
	var out []box.Ref
	one := func(x box.Value) {
		if x.Kind != box.KindLink || x.Link == nil {
			return
		}
		l := x.Link
		switch {
		case l.To == nil:
			out = append(out, l.From)
		case l.Op == link.OpRead:
			out = append(out, *l.To)
		}
	}
	if v.Kind == box.KindArray {
		for _, x := range v.Arr {
			one(x)
		}
	} else {
		one(v)
	}
	return out
}

func (r *run) followLinks(src *rec, via *Field) ([]*rec, error) {
	val := r.readPath(src, via)
	var out []*rec
	for _, ref := range linkTargets(val) {
		addr, err := link.AddrOf(ref)
		if err != nil {
			continue
		}
		r.scanned++
		if r.scanned > r.lim.MaxScan {
			return nil, fmt.Errorf("%w: statement read more than %d records", ErrLimit, r.lim.MaxScan)
		}
		if r.db.cfg.OwnerOf == nil && !r.az.ok(sec.ActionRead, sec.Resource{File: addr.File, Box: addr.Box}) {
			continue // authorized before the target is read
		}
		v, err := r.db.cfg.Links.Resolve(ref)
		if err != nil {
			if errors.Is(err, link.ErrDangling) {
				continue
			}
			return nil, err
		}
		ent := &box.Entry{Key: addr.Box, Value: v}
		if addr.HasID {
			ent.Attrs = []box.Attr{{Name: "ID", Value: box.Int(addr.ID)}}
		}
		if rc, ok := r.gate(addr, ent, v); ok {
			out = append(out, rc)
		}
	}
	return out, nil
}

type cell struct {
	name string
	v    box.Value
}

func (r *run) starCells(rc *rec, prefix string) []cell {
	if rc == nil {
		return nil
	}
	if rc.val.Kind != box.KindBox || rc.val.Box == nil {
		return []cell{{prefix + "_value", rc.val}}
	}
	var out []cell
	for _, e := range rc.val.Box.Entries() {
		if !r.az.pathAllowed(sec.ActionRead, rc.addr.File, rc.addr.Box, rc.owner, []string{e.Key}) {
			continue
		}
		out = append(out, cell{prefix + e.EKey().String(), r.mask(rc, e.Key, e.Value)})
	}
	return out
}

func (r *run) assemble(pl *selPlan, rows []sortRow) (*Result, error) {
	res := &Result{Kind: "select", Access: pl.acc.label(), Scanned: r.scanned}
	colIdx := map[string]int{}
	size := 0
	multi := len(pl.aliases) > 1
	var all [][]cell
	for _, sr := range rows {
		var cells []cell
		for _, it := range pl.items {
			switch {
			case it.Star && it.StarAlias != "":
				for i, a := range pl.aliases {
					if a == it.StarAlias {
						cells = append(cells, r.starCells(sr.t[i], a+".")...)
					}
				}
			case it.Star:
				for i, a := range pl.aliases {
					p := ""
					if multi {
						p = a + "."
					}
					cells = append(cells, r.starCells(sr.t[i], p)...)
				}
			default:
				name := it.As
				if name == "" {
					name = it.Text
					if f, ok := it.Expr.(*Field); ok {
						name = box.SegsString(f.Segs)
					}
				}
				cells = append(cells, cell{name, r.eval(it.Expr, sr.t)})
			}
		}
		// make names unique inside one row
		seen := map[string]int{}
		for i := range cells {
			seen[cells[i].name]++
			if c := seen[cells[i].name]; c > 1 {
				cells[i].name += "#" + strconv.Itoa(c)
			}
			size += valueSize(cells[i].v) + len(cells[i].name)
			if _, ok := colIdx[cells[i].name]; !ok {
				if len(colIdx) >= r.lim.MaxColumns {
					return nil, fmt.Errorf("%w: result has more than %d columns", ErrLimit, r.lim.MaxColumns)
				}
				colIdx[cells[i].name] = len(res.Columns)
				res.Columns = append(res.Columns, cells[i].name)
			}
		}
		if size > r.lim.MaxResultBytes {
			return nil, fmt.Errorf("%w: result larger than %d bytes", ErrLimit, r.lim.MaxResultBytes)
		}
		all = append(all, cells)
		res.Addrs = append(res.Addrs, sr.t[0].addr)
	}
	for _, cells := range all {
		row := make([]box.Value, len(res.Columns))
		for _, c := range cells {
			row[colIdx[c.name]] = c.v
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}
