package query

import (
	"errors"
	"fmt"

	"redx/box"
	"redx/engine/link"
	"redx/engine/sec"
)

// put writes a record through the link engine when there is one (so it is
// serialized with cascades), else straight to the store.
func (r *run) put(a link.Addr, e *box.Entry) error {
	if r.db.cfg.Links != nil {
		return r.db.cfg.Links.Put(a, e, link.PutOptions{})
	}
	return r.db.cfg.Store.Put(a, e)
}

// setPath sets the value at segs inside the record, creating intermediate
// boxes. With replace=false an existing final entry is an error.
func setPath(ent *box.Entry, segs []box.Seg, v box.Value, replace bool) error {
	if ent.Value.Kind == box.KindNell && ent.Value.Box == nil {
		ent.Value = box.BoxValue(box.NewBox())
	}
	if ent.Value.Kind != box.KindBox || ent.Value.Box == nil {
		return fmt.Errorf("%w: record %s is not a box, cannot set a field in it", ErrType, ent.Key)
	}
	b := ent.Value.Box
	for i, s := range segs {
		cur := b.Get(s.Name, s.ID, s.HasID)
		last := i == len(segs)-1
		if last {
			if cur != nil {
				if !replace {
					return fmt.Errorf("%w: field %s is set twice", ErrType, box.SegsString(segs))
				}
				cur.Value = v
				return nil
			}
			var idp *int64
			if s.HasID {
				id := s.ID
				idp = &id
			}
			b.Put(box.NewEntry(s.Name, idp, v))
			return nil
		}
		if cur == nil {
			var idp *int64
			if s.HasID {
				id := s.ID
				idp = &id
			}
			cur = box.NewEntry(s.Name, idp, box.BoxValue(box.NewBox()))
			b.Put(cur)
		}
		if cur.Value.Kind != box.KindBox || cur.Value.Box == nil {
			return fmt.Errorf("%w: %s is not a box (in %s)", ErrType, s.Name, box.SegsString(segs))
		}
		b = cur.Value.Box
	}
	return nil
}

func checkNames(f *Field) error {
	for _, s := range f.Path {
		if !validIdentName(s.Name) {
			return synErr(f.P, f.Raw, "field name %q cannot be stored (letters, digits, _ and - only; not TRUE, FALSE, NELL or ID)", s.Name)
		}
	}
	return nil
}

func (r *run) checkWritable(f *Field) error {
	if f.Pseudo != "" {
		return synErr(f.P, f.Raw, "%s is read-only", f.Pseudo)
	}
	if len(f.Path) == 0 {
		return synErr(f.P, f.Raw, "empty field path")
	}
	return checkNames(f)
}

// ---- INSERT --------------------------------------------------------------

func (r *run) doInsert(ins *Insert, explain bool) (*Result, error) {
	if ins.Target.Box == "" {
		return nil, synErr(ins.Target.P, ins.Target.File, "INSERT needs a box: INSERT INTO file.Box (...)")
	}
	if !validIdentName(ins.Target.Box) {
		return nil, synErr(ins.Target.P, ins.Target.Box, "box name %q cannot be stored", ins.Target.Box)
	}
	idCol := -1
	seen := map[string]bool{}
	for i, f := range ins.Cols {
		bindField(f, nil)
		if f.Pseudo == "_id" {
			idCol = i
		} else if err := r.checkWritable(f); err != nil {
			return nil, err
		}
		key := box.SegsString(f.Path)
		if seen[key] {
			return nil, synErr(f.P, f.Raw, "column %s is listed twice", f.Raw)
		}
		seen[key] = true
	}
	for _, row := range ins.Rows {
		for _, e := range row {
			if err := r.validateExpr(e); err != nil {
				return nil, err
			}
		}
	}
	file, err := r.resolveFile(ins.Target.File, ins.Target.Box, sec.ActionWrite, true)
	if err != nil {
		return nil, err
	}
	if len(ins.Rows) > r.lim.MaxWrites {
		return nil, fmt.Errorf("%w: INSERT of %d rows exceeds the maximum of %d writes", ErrLimit, len(ins.Rows), r.lim.MaxWrites)
	}
	if r.db.cfg.OwnerOf == nil {
		if err := r.az.check(sec.ActionWrite, sec.Resource{File: file, Box: ins.Target.Box}); err != nil {
			return nil, err
		}
		for _, f := range ins.Cols {
			if f.Pseudo == "" && !r.az.pathAllowed(sec.ActionWrite, file, ins.Target.Box, "", f.Names) {
				return nil, fmt.Errorf("%w: write on %s:%s.%s", sec.ErrDenied, file, ins.Target.Box, f.Names[len(f.Names)-1])
			}
		}
	}
	if explain {
		return planResult([]string{
			fmt.Sprintf("INSERT %d row(s) into %s:%s", len(ins.Rows), file, ins.Target.Box),
			"  each row: authorize write, fail if the record already exists, then store",
			fmt.Sprintf("  max writes: %d", r.lim.MaxWrites),
		}), nil
	}
	type pend struct {
		addr link.Addr
		ent  *box.Entry
	}
	var pending []pend
	dup := map[link.Addr]bool{}
	for _, row := range ins.Rows {
		var id *int64
		ent := box.NewEntry(ins.Target.Box, nil, box.BoxValue(box.NewBox()))
		vals := make([]box.Value, len(row))
		for i, e := range row {
			vals[i] = r.constEval(e)
		}
		for i, f := range ins.Cols {
			if i == idCol {
				n, ok := wholeID(vals[i])
				if !ok {
					return nil, fmt.Errorf("%w: _id must be a whole number, got %s", ErrParams, fmtValue(vals[i]))
				}
				id = &n
				continue
			}
			if err := setPath(ent, f.Path, vals[i].Clone(), false); err != nil {
				return nil, err
			}
		}
		if id != nil {
			ent.Attrs = []box.Attr{{Name: "ID", Value: box.Int(*id)}}
		}
		addr := link.EntryAddr(file, ent)
		if dup[addr] {
			return nil, fmt.Errorf("%w: %s appears twice in the statement", ErrExists, addr)
		}
		dup[addr] = true
		owner := r.owner(addr, ent)
		if r.db.cfg.OwnerOf != nil {
			if err := r.az.check(sec.ActionWrite, sec.Resource{File: file, Box: addr.Box, Owner: owner}); err != nil {
				return nil, err
			}
			for _, f := range ins.Cols {
				if f.Pseudo == "" && !r.az.pathAllowed(sec.ActionWrite, file, addr.Box, owner, f.Names) {
					return nil, fmt.Errorf("%w: write on %s:%s.%s", sec.ErrDenied, file, addr.Box, f.Names[len(f.Names)-1])
				}
			}
		}
		pending = append(pending, pend{addr, ent})
	}
	r.db.wmu.Lock()
	defer r.db.wmu.Unlock()
	for _, p := range pending {
		if _, ok, err := r.db.cfg.Store.Get(p.addr); err != nil {
			return nil, err
		} else if ok {
			return nil, fmt.Errorf("%w: %s", ErrExists, p.addr)
		}
	}
	res := &Result{Kind: "insert"}
	for _, p := range pending {
		if err := r.ctx.Err(); err != nil {
			return res, err
		}
		if err := r.put(p.addr, p.ent); err != nil {
			return res, err
		}
		res.Affected++
	}
	return res, nil
}

// ---- UPDATE --------------------------------------------------------------

func (r *run) planWriteTarget(src Source, where Expr, act sec.Action) (file string, pre []Expr, acc access, err error) {
	file, err = r.resolveFile(src.File, src.Box, act, false)
	if err != nil {
		return
	}
	pre = flattenAnd(where, nil)
	if a, ok := r.accessAnd(pre, file, src.Box, r.params); ok {
		acc = a
	}
	return
}

func (r *run) doUpdate(up *Update, explain bool) (*Result, error) {
	aliases := []string{up.Target.alias()}
	var fields []*Field
	seen := map[string]bool{}
	for _, s := range up.Sets {
		bindField(s.Target, aliases)
		if err := r.checkWritable(s.Target); err != nil {
			return nil, err
		}
		k := box.SegsString(s.Target.Path)
		if seen[k] {
			return nil, synErr(s.Target.P, s.Target.Raw, "field %s is assigned twice", s.Target.Raw)
		}
		seen[k] = true
		bindExpr(s.Value, aliases)
		if err := r.validateExpr(s.Value); err != nil {
			return nil, err
		}
		fields = append(fields, fieldsOf(s.Value)...)
	}
	bindExpr(up.Where, aliases)
	if err := r.validateExpr(up.Where); err != nil {
		return nil, err
	}
	fields = append(fields, fieldsOf(up.Where)...)
	file, pre, acc, err := r.planWriteTarget(up.Target, up.Where, sec.ActionWrite)
	if err != nil {
		return nil, err
	}
	if err := r.staticRead(file, up.Target.Box, fields); err != nil {
		return nil, err
	}
	if up.Target.Box != "" && r.db.cfg.OwnerOf == nil {
		for _, s := range up.Sets {
			if !r.az.pathAllowed(sec.ActionWrite, file, up.Target.Box, "", s.Target.Names) {
				return nil, fmt.Errorf("%w: write on %s:%s.%s", sec.ErrDenied, file, up.Target.Box, s.Target.Names[len(s.Target.Names)-1])
			}
		}
	}
	if explain {
		lines := []string{fmt.Sprintf("UPDATE %s:%s", file, orStar(up.Target.Box))}
		lines = append(lines, acc.describe("  access: ")...)
		lines = append(lines, "  filter: "+condString(pre, r.params))
		for _, s := range up.Sets {
			lines = append(lines, fmt.Sprintf("  set: %s = %s", s.Target.Raw, exprString(s.Value, r.params)))
		}
		lines = append(lines, fmt.Sprintf("  max writes: %d; every changed field is authorized for write before the first record is stored", r.lim.MaxWrites))
		return planResult(lines), nil
	}
	r.db.wmu.Lock()
	defer r.db.wmu.Unlock()
	cands, err := r.candidates(file, up.Target.Box, acc, pre, 1)
	if err != nil {
		return nil, err
	}
	if len(cands) > r.lim.MaxWrites {
		return nil, fmt.Errorf("%w: UPDATE matches %d records, more than the maximum of %d writes", ErrLimit, len(cands), r.lim.MaxWrites)
	}
	type pend struct {
		addr link.Addr
		ent  *box.Entry
	}
	var pending []pend
	for _, c := range cands {
		t := tuple{c}
		vals := make([]box.Value, len(up.Sets))
		for i, s := range up.Sets {
			vals[i] = r.eval(s.Value, t)
		}
		ne := c.ent.Clone()
		for i, s := range up.Sets {
			if !r.az.pathAllowed(sec.ActionWrite, c.addr.File, c.addr.Box, c.owner, s.Target.Names) {
				return nil, fmt.Errorf("%w: write on %s:%s.%s", sec.ErrDenied, c.addr.File, c.addr.Box, s.Target.Names[len(s.Target.Names)-1])
			}
			if err := setPath(ne, s.Target.Path, vals[i].Clone(), true); err != nil {
				return nil, err
			}
		}
		if ne.Value.Equal(c.ent.Value) {
			continue
		}
		pending = append(pending, pend{c.addr, ne})
	}
	res := &Result{Kind: "update", Access: acc.label(), Scanned: r.scanned}
	for _, p := range pending {
		if err := r.ctx.Err(); err != nil {
			return res, err
		}
		if err := r.put(p.addr, p.ent); err != nil {
			return res, err
		}
		res.Affected++
	}
	return res, nil
}

// ---- DELETE --------------------------------------------------------------

func (r *run) doDelete(d *Delete, explain bool) (*Result, error) {
	aliases := []string{d.Target.alias()}
	bindExpr(d.Where, aliases)
	if err := r.validateExpr(d.Where); err != nil {
		return nil, err
	}
	file, pre, acc, err := r.planWriteTarget(d.Target, d.Where, sec.ActionDelete)
	if err != nil {
		return nil, err
	}
	if err := r.staticRead(file, d.Target.Box, fieldsOf(d.Where)); err != nil {
		return nil, err
	}
	if d.Target.Box != "" && r.db.cfg.OwnerOf == nil {
		if err := r.az.check(sec.ActionDelete, sec.Resource{File: file, Box: d.Target.Box}); err != nil {
			return nil, err
		}
	}
	if explain {
		lines := []string{fmt.Sprintf("DELETE from %s:%s", file, orStar(d.Target.Box))}
		lines = append(lines, acc.describe("  access: ")...)
		lines = append(lines, "  filter: "+condString(pre, r.params))
		if r.db.cfg.Links != nil {
			lines = append(lines, "  cascade: follows @!> links (every cascaded record is authorized for delete first)")
		} else {
			lines = append(lines, "  cascade: no link engine; records with embedded @!> links are refused")
		}
		lines = append(lines, fmt.Sprintf("  max writes (cascades included): %d", r.lim.MaxWrites))
		return planResult(lines), nil
	}
	r.db.wmu.Lock()
	defer r.db.wmu.Unlock()
	cands, err := r.candidates(file, d.Target.Box, acc, pre, 1)
	if err != nil {
		return nil, err
	}
	if len(cands) > r.lim.MaxWrites {
		return nil, fmt.Errorf("%w: DELETE matches %d records, more than the maximum of %d writes", ErrLimit, len(cands), r.lim.MaxWrites)
	}
	// Pass 1: work out everything that would go (cascades included) and
	// authorize every record before the first delete.
	planned := map[link.Addr]bool{}
	total := 0
	for _, c := range cands {
		if err := r.az.check(sec.ActionDelete, sec.Resource{File: c.addr.File, Box: c.addr.Box, Owner: c.owner}); err != nil {
			return nil, err
		}
		if r.db.cfg.Links == nil {
			for _, e := range link.EmbeddedEdges(c.addr, c.ent) {
				if e.Op == link.OpCascade {
					return nil, fmt.Errorf("%w: %s has a cascade link (%s) but the query engine has no link engine to follow it", ErrNoLinks, c.addr, e.Via)
				}
			}
			total++
			continue
		}
		plan, err := r.db.cfg.Links.PlanDelete(c.addr, link.DeleteOptions{MaxRecords: r.lim.MaxWrites})
		if errors.Is(err, link.ErrTooManyRecords) {
			return nil, fmt.Errorf("%w: DELETE would remove more than %d records including cascades: %w", ErrLimit, r.lim.MaxWrites, err)
		}
		if err != nil {
			return nil, err
		}
		for _, a := range plan.Planned {
			if planned[a] {
				continue
			}
			planned[a] = true
			total++
			if a == c.addr {
				continue
			}
			ent, ok, err := r.db.cfg.Store.Get(a)
			if err != nil {
				return nil, err
			}
			owner := ""
			if ok {
				owner = r.owner(a, ent)
			}
			if err := r.az.check(sec.ActionDelete, sec.Resource{File: a.File, Box: a.Box, Owner: owner}); err != nil {
				return nil, fmt.Errorf("%w (reached by cascade from %s)", err, c.addr)
			}
		}
		if total > r.lim.MaxWrites {
			return nil, fmt.Errorf("%w: DELETE would remove more than %d records including cascades", ErrLimit, r.lim.MaxWrites)
		}
	}
	// Pass 2: delete.
	res := &Result{Kind: "delete", Access: acc.label(), Scanned: r.scanned}
	primary := map[link.Addr]bool{}
	for _, c := range cands {
		primary[c.addr] = true
	}
	for _, c := range cands {
		if err := r.ctx.Err(); err != nil {
			return res, err
		}
		if r.db.cfg.Links == nil {
			ok, err := r.db.cfg.Store.Delete(c.addr)
			if err != nil {
				return res, err
			}
			if ok {
				res.Affected++
			}
			continue
		}
		dr, err := r.db.cfg.Links.Delete(c.addr, link.DeleteOptions{MaxRecords: r.lim.MaxWrites})
		for _, a := range dr.Deleted {
			if a == c.addr || primary[a] {
				res.Affected++
			} else {
				res.Cascaded = append(res.Cascaded, a)
			}
		}
		if err != nil && !errors.Is(err, link.ErrDangling) {
			return res, err
		}
	}
	return res, nil
}
