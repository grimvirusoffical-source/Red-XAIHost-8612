package code

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"redx/engine/sec"
)

// RunOptions are the host's side of a run.
type RunOptions struct {
	// Principal is who the script runs as; every host call is authorized for it.
	Principal sec.Principal
	// Auth authorizes host calls. Nil denies them all.
	Auth Authorizer
	// Host provides the host functions. Nil means none.
	Host *Host
	// Limits is the ceiling. The script's own limits can only lower it.
	Limits Limits
}

// Result is the outcome of a run. It is returned with an error too, so the
// resource use and the host calls attempted can be audited.
type Result struct {
	// Value is the script's return value: nil, bool, float64, string or []any.
	Value     interface{}
	Steps     int64
	Alloc     int64
	HostCalls int
	Elapsed   time.Duration
	Calls     []CallRecord
}

type scope struct {
	vars   map[string]value
	parent *scope
}

func newScope(parent *scope) *scope { return &scope{vars: map[string]value{}, parent: parent} }

func (s *scope) lookup(name string) (value, bool) {
	for c := s; c != nil; c = c.parent {
		if v, ok := c.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (s *scope) assign(name string, v value) bool {
	for c := s; c != nil; c = c.parent {
		if _, ok := c.vars[name]; ok {
			c.vars[name] = v
			return true
		}
	}
	return false
}

type machine struct {
	prog     *Program
	lim      Limits
	ctx      context.Context
	deadline time.Time
	opts     RunOptions
	steps    int64
	alloc    int64
	depth    int
	hcalls   int
	calls    []CallRecord
}

func (m *machine) fail(p pos, format string, a ...interface{}) error {
	return &RuntimeError{Script: m.prog.name, Line: p.line, Col: p.col, Msg: fmt.Sprintf(format, a...)}
}

func (m *machine) failErr(p pos, msg string, err error) error {
	return &RuntimeError{Script: m.prog.name, Line: p.line, Col: p.col, Msg: msg, Err: err}
}

func (m *machine) checkTime(p pos) error {
	if err := m.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return m.failErr(p, "stopped", ErrTimeout)
		}
		return m.failErr(p, "stopped", ErrCanceled)
	}
	if !time.Now().Before(m.deadline) {
		return m.failErr(p, "stopped", ErrTimeout)
	}
	return nil
}

// charge spends n steps.
func (m *machine) charge(n int64, p pos) error {
	if n <= 0 {
		return nil
	}
	old := m.steps
	m.steps += n
	if m.steps > m.lim.MaxSteps || m.steps < old {
		return m.failErr(p, "stopped", ErrStepLimit)
	}
	if old>>6 != m.steps>>6 {
		return m.checkTime(p)
	}
	return nil
}

func (m *machine) step(p pos) error { return m.charge(1, p) }

func (m *machine) spendAlloc(n int64, p pos) error {
	m.alloc += n
	if m.alloc > m.lim.MaxAlloc || m.alloc < 0 {
		return m.failErr(p, "stopped", ErrAllocLimit)
	}
	return nil
}

func (m *machine) allocString(n int, p pos) error {
	if n > m.lim.MaxStringLen {
		return m.failErr(p, "string would be longer than the limit", ErrStringLimit)
	}
	return m.spendAlloc(int64(n)+1, p)
}

func (m *machine) allocCells(n int, p pos) error {
	return m.spendAlloc(int64(n)*elemCost, p)
}

type ctlKind uint8

const (
	ctlNone ctlKind = iota
	ctlBreak
	ctlContinue
	ctlReturn
)

// Run executes the script. args are matched to the declared parameters in
// order. See RunOptions and Limits for what is enforced.
func (p *Program) Run(ctx context.Context, args []interface{}, o RunOptions) (res Result, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) != len(p.params) {
		return res, fmt.Errorf("%w: %s takes %d arguments, got %d", ErrBadArguments, p.name, len(p.params), len(args))
	}
	lim := Effective(o.Limits, p.limits)
	start := time.Now()
	dctx, cancel := context.WithDeadline(ctx, start.Add(lim.MaxDuration))
	defer cancel()
	m := &machine{prog: p, lim: lim, ctx: dctx, deadline: start.Add(lim.MaxDuration), opts: o}
	defer func() {
		if r := recover(); r != nil {
			err = &RuntimeError{Script: p.name, Msg: fmt.Sprintf("internal error: %v", r), Err: ErrInternal}
		}
		res.Steps, res.Alloc, res.HostCalls, res.Calls = m.steps, m.alloc, m.hcalls, m.calls
		res.Elapsed = time.Since(start)
	}()

	root := newScope(nil)
	nodes := 0
	for i, a := range args {
		v, cerr := fromGo(a, lim, 0, &nodes)
		if cerr != nil {
			return res, fmt.Errorf("%w: argument %d (%s): %v", ErrBadArguments, i+1, p.params[i], cerr)
		}
		root.vars[p.params[i]] = v
	}
	if err := m.spendAlloc(int64(nodes)*elemCost, pos{1, 1}); err != nil {
		return res, err
	}
	ctl, rv, rerr := m.execStmts(p.body.stmts, root)
	if rerr != nil {
		return res, rerr
	}
	if ctl != ctlReturn {
		rv = nil
	}
	c := &conv{}
	out, cerr := c.toGo(rv, 0)
	if cerr != nil {
		return res, m.failErr(pos{1, 1}, "the returned value cannot be converted", cerr)
	}
	res.Value = out
	return res, nil
}

func (m *machine) execStmts(stmts []stmt, sc *scope) (ctlKind, value, error) {
	for _, s := range stmts {
		ctl, v, err := m.exec(s, sc)
		if err != nil || ctl != ctlNone {
			return ctl, v, err
		}
	}
	return ctlNone, nil, nil
}

func (m *machine) execBlock(b *block, sc *scope) (ctlKind, value, error) {
	if err := m.step(b.pos); err != nil {
		return ctlNone, nil, err
	}
	return m.execStmts(b.stmts, newScope(sc))
}

func (m *machine) cond(e expr, sc *scope, p pos) (bool, error) {
	v, err := m.eval(e, sc)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, m.fail(p, "a condition must be true or false, got %s", typeName(v))
	}
	return b, nil
}

func (m *machine) exec(s stmt, sc *scope) (ctlKind, value, error) {
	switch s := s.(type) {
	case *letStmt:
		if err := m.step(s.pos); err != nil {
			return 0, nil, err
		}
		v, err := m.eval(s.x, sc)
		if err != nil {
			return 0, nil, err
		}
		sc.vars[s.name] = v
	case *assignStmt:
		if err := m.step(s.pos); err != nil {
			return 0, nil, err
		}
		v, err := m.eval(s.x, sc)
		if err != nil {
			return 0, nil, err
		}
		switch t := s.target.(type) {
		case *varRef:
			if !sc.assign(t.name, v) {
				return 0, nil, m.fail(t.pos, "assignment to undeclared variable %q (declare it with let)", t.name)
			}
		case *indexExpr:
			arr, idx, err := m.indexTarget(t, sc)
			if err != nil {
				return 0, nil, err
			}
			arr.elems[idx] = v
		}
	case *ifStmt:
		if err := m.step(s.pos); err != nil {
			return 0, nil, err
		}
		c, err := m.cond(s.cond, sc, s.pos)
		if err != nil {
			return 0, nil, err
		}
		if c {
			return m.execBlock(s.then, sc)
		}
		switch e := s.els.(type) {
		case *block:
			return m.execBlock(e, sc)
		case *ifStmt:
			return m.exec(e, sc)
		}
	case *whileStmt:
		for {
			if err := m.step(s.pos); err != nil {
				return 0, nil, err
			}
			c, err := m.cond(s.cond, sc, s.pos)
			if err != nil {
				return 0, nil, err
			}
			if !c {
				break
			}
			ctl, v, err := m.execBlock(s.body, sc)
			if err != nil {
				return 0, nil, err
			}
			if ctl == ctlBreak {
				break
			}
			if ctl == ctlReturn {
				return ctl, v, nil
			}
		}
	case *forStmt:
		return m.execFor(s, sc)
	case *returnStmt:
		if err := m.step(s.pos); err != nil {
			return 0, nil, err
		}
		var v value
		if s.x != nil {
			var err error
			if v, err = m.eval(s.x, sc); err != nil {
				return 0, nil, err
			}
		}
		return ctlReturn, v, nil
	case *breakStmt:
		return ctlBreak, nil, m.step(s.pos)
	case *continueStmt:
		return ctlContinue, nil, m.step(s.pos)
	case *exprStmt:
		if err := m.step(s.pos); err != nil {
			return 0, nil, err
		}
		if _, err := m.eval(s.x, sc); err != nil {
			return 0, nil, err
		}
	case *block:
		return m.execBlock(s, sc)
	default:
		return 0, nil, m.fail(pos{}, "internal: unknown statement")
	}
	return ctlNone, nil, nil
}

func (m *machine) execFor(s *forStmt, sc *scope) (ctlKind, value, error) {
	if err := m.step(s.pos); err != nil {
		return 0, nil, err
	}
	from, err := m.eval(s.from, sc)
	if err != nil {
		return 0, nil, err
	}
	body := func(v value) (ctlKind, value, bool, error) {
		if err := m.step(s.pos); err != nil {
			return 0, nil, true, err
		}
		inner := newScope(sc)
		inner.vars[s.name] = v
		ctl, rv, err := m.execStmts(s.body.stmts, inner)
		if err != nil {
			return 0, nil, true, err
		}
		switch ctl {
		case ctlBreak:
			return ctlNone, nil, true, nil
		case ctlReturn:
			return ctl, rv, true, nil
		}
		return ctlNone, nil, false, nil
	}
	if s.to != nil {
		toV, err := m.eval(s.to, sc)
		if err != nil {
			return 0, nil, err
		}
		a, ok1 := from.(float64)
		b, ok2 := toV.(float64)
		if !ok1 || !ok2 || a != math.Trunc(a) || b != math.Trunc(b) {
			return 0, nil, m.fail(s.pos, "a range needs whole numbers, got %s..%s", typeName(from), typeName(toV))
		}
		for i := a; i < b; i++ {
			ctl, rv, stop, err := body(i)
			if err != nil || stop {
				return ctl, rv, err
			}
		}
		return ctlNone, nil, nil
	}
	arr, ok := from.(*array)
	if !ok {
		return 0, nil, m.fail(s.pos, "for ... in needs an array or a range, got %s", typeName(from))
	}
	n := len(arr.elems) // the length at the start; elements are read live
	for i := 0; i < n && i < len(arr.elems); i++ {
		ctl, rv, stop, err := body(arr.elems[i])
		if err != nil || stop {
			return ctl, rv, err
		}
	}
	return ctlNone, nil, nil
}

func (m *machine) indexTarget(t *indexExpr, sc *scope) (*array, int, error) {
	xv, err := m.eval(t.x, sc)
	if err != nil {
		return nil, 0, err
	}
	iv, err := m.eval(t.i, sc)
	if err != nil {
		return nil, 0, err
	}
	arr, ok := xv.(*array)
	if !ok {
		return nil, 0, m.fail(t.pos, "only arrays can be indexed, got %s", typeName(xv))
	}
	idx, err := m.index(t.pos, arr, iv)
	return arr, idx, err
}

func (m *machine) index(p pos, arr *array, iv value) (int, error) {
	f, ok := iv.(float64)
	if !ok || f != math.Trunc(f) {
		return 0, m.fail(p, "an array index must be a whole number, got %s", typeName(iv))
	}
	if f < 0 || f >= float64(len(arr.elems)) {
		return 0, m.fail(p, "index %s is outside the array (length %d)", fmtNumber(f), len(arr.elems))
	}
	return int(f), nil
}

func equal(a, b value) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case *array:
		y, ok := b.(*array)
		return ok && x == y // arrays are equal only if they are the same array
	}
	return false
}

func (m *machine) eval(e expr, sc *scope) (value, error) {
	switch e := e.(type) {
	case *numLit:
		return e.v, m.step(e.pos)
	case *strLit:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		if len(e.v) > m.lim.MaxStringLen {
			return nil, m.failErr(e.pos, "string literal is longer than the limit", ErrStringLimit)
		}
		return e.v, nil
	case *boolLit:
		return e.v, m.step(e.pos)
	case *nilLit:
		return nil, m.step(e.pos)
	case *varRef:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		v, ok := sc.lookup(e.name)
		if !ok {
			return nil, m.fail(e.pos, "undefined variable %q", e.name)
		}
		return v, nil
	case *arrayLit:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		if len(e.elems) > m.lim.MaxArrayLen {
			return nil, m.failErr(e.pos, "array literal is longer than the limit", ErrArrayLimit)
		}
		if err := m.allocCells(len(e.elems), e.pos); err != nil {
			return nil, err
		}
		a := &array{elems: make([]value, 0, len(e.elems))}
		for _, x := range e.elems {
			v, err := m.eval(x, sc)
			if err != nil {
				return nil, err
			}
			a.elems = append(a.elems, v)
		}
		return a, nil
	case *indexExpr:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		xv, err := m.eval(e.x, sc)
		if err != nil {
			return nil, err
		}
		iv, err := m.eval(e.i, sc)
		if err != nil {
			return nil, err
		}
		arr, ok := xv.(*array)
		if !ok {
			return nil, m.fail(e.pos, "only arrays can be indexed, got %s", typeName(xv))
		}
		i, err := m.index(e.pos, arr, iv)
		if err != nil {
			return nil, err
		}
		return arr.elems[i], nil
	case *unaryExpr:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		x, err := m.eval(e.x, sc)
		if err != nil {
			return nil, err
		}
		if e.op == "-" {
			f, ok := x.(float64)
			if !ok {
				return nil, m.fail(e.pos, "cannot negate %s", typeName(x))
			}
			return -f, nil
		}
		b, ok := x.(bool)
		if !ok {
			return nil, m.fail(e.pos, "not needs true or false, got %s", typeName(x))
		}
		return !b, nil
	case *logicExpr:
		if err := m.step(e.pos); err != nil {
			return nil, err
		}
		l, err := m.cond(e.l, sc, e.pos)
		if err != nil {
			return nil, err
		}
		if e.and && !l {
			return false, nil
		}
		if !e.and && l {
			return true, nil
		}
		return m.cond(e.r, sc, e.pos)
	case *binExpr:
		return m.evalBin(e, sc)
	case *callExpr:
		return m.evalCall(e, sc)
	}
	return nil, m.fail(pos{}, "internal: unknown expression")
}

func (m *machine) evalBin(e *binExpr, sc *scope) (value, error) {
	if err := m.step(e.pos); err != nil {
		return nil, err
	}
	l, err := m.eval(e.l, sc)
	if err != nil {
		return nil, err
	}
	r, err := m.eval(e.r, sc)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "+":
		if ls, ok := l.(string); ok {
			rs, ok := r.(string)
			if !ok {
				return nil, m.fail(e.pos, "cannot add %s to a string (use str())", typeName(r))
			}
			if len(ls)+len(rs) > m.lim.MaxStringLen {
				return nil, m.failErr(e.pos, "string would be longer than the limit", ErrStringLimit)
			}
			if err := m.charge(int64((len(ls)+len(rs))/256), e.pos); err != nil {
				return nil, err
			}
			if err := m.allocString(len(ls)+len(rs), e.pos); err != nil {
				return nil, err
			}
			return ls + rs, nil
		}
	case "<", "<=", ">", ">=":
		if ls, ok := l.(string); ok {
			rs, ok := r.(string)
			if !ok {
				return nil, m.fail(e.pos, "cannot compare string with %s", typeName(r))
			}
			c := strings.Compare(ls, rs)
			return cmpOp(e.op, c), nil
		}
	}
	lf, ok1 := l.(float64)
	rf, ok2 := r.(float64)
	if !ok1 || !ok2 {
		return nil, m.fail(e.pos, "cannot apply %q to %s and %s", e.op, typeName(l), typeName(r))
	}
	var out float64
	switch e.op {
	case "+":
		out = lf + rf
	case "-":
		out = lf - rf
	case "*":
		out = lf * rf
	case "/":
		if rf == 0 {
			return nil, m.fail(e.pos, "division by zero")
		}
		out = lf / rf
	case "%":
		if rf == 0 {
			return nil, m.fail(e.pos, "remainder of division by zero")
		}
		out = math.Mod(lf, rf)
	case "<", "<=", ">", ">=":
		c := 0
		if lf < rf {
			c = -1
		} else if lf > rf {
			c = 1
		}
		return cmpOp(e.op, c), nil
	default:
		return nil, m.fail(e.pos, "internal: unknown operator %q", e.op)
	}
	if math.IsNaN(out) || math.IsInf(out, 0) {
		return nil, m.failErr(e.pos, "arithmetic", errNumericOverflow)
	}
	return out, nil
}

func cmpOp(op string, c int) bool {
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	}
	return c >= 0
}

func (m *machine) evalCall(e *callExpr, sc *scope) (value, error) {
	if err := m.step(e.pos); err != nil {
		return nil, err
	}
	args := make([]value, len(e.args))
	for i, a := range e.args {
		v, err := m.eval(a, sc)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	switch e.kind {
	case callBuiltin:
		return e.bi.fn(m, e, args)
	case callUser:
		return m.callUser(e, args)
	case callHost:
		return m.callHost(e, args)
	}
	return nil, m.fail(e.pos, "internal: unresolved call")
}

func (m *machine) callUser(e *callExpr, args []value) (value, error) {
	if m.depth >= m.lim.MaxDepth {
		return nil, m.failErr(e.pos, "calls are nested too deeply", ErrDepthLimit)
	}
	m.depth++
	defer func() { m.depth-- }()
	f := e.fn
	sc := newScope(nil) // functions see only their own parameters: no globals, no caller variables
	for i, p := range f.params {
		sc.vars[p] = args[i]
	}
	ctl, v, err := m.execStmts(f.body.stmts, sc)
	if err != nil {
		return nil, err
	}
	if ctl == ctlReturn {
		return v, nil
	}
	return nil, nil
}

func (m *machine) record(name string, allowed bool, reason string) {
	if len(m.calls) < maxCallRecords {
		m.calls = append(m.calls, CallRecord{Name: name, Allowed: allowed, Reason: reason})
	}
}

func (m *machine) callHost(e *callExpr, args []value) (value, error) {
	name := e.name
	// Defence in depth: the compiler already refuses undeclared names.
	if !m.prog.useSet[name] {
		m.record(name, false, "not declared in Uses")
		return nil, m.failErr(e.pos, name, fmt.Errorf("%w: %s is not declared in the script's Uses", ErrNotGranted, name))
	}
	var hf *HostFunc
	if m.opts.Host != nil {
		hf = m.opts.Host.fns[name]
	}
	if hf == nil {
		m.record(name, false, "not provided by the host")
		return nil, m.failErr(e.pos, name, fmt.Errorf("%w: %s is not provided by the host", ErrNotGranted, name))
	}
	if m.hcalls >= m.lim.MaxHostCalls {
		m.record(name, false, "host call limit")
		return nil, m.failErr(e.pos, name, ErrHostCallLimit)
	}
	m.hcalls++
	if err := m.checkTime(e.pos); err != nil {
		return nil, err
	}
	c := &conv{}
	goArgs := make([]interface{}, len(args))
	for i, a := range args {
		x, err := c.toGo(a, 0)
		if err != nil {
			m.record(name, false, "argument cannot be converted")
			return nil, m.failErr(e.pos, name+": argument "+fmt.Sprint(i+1), err)
		}
		goArgs[i] = x
	}
	if err := m.charge(int64(c.nodes), e.pos); err != nil {
		return nil, err
	}
	res := sec.Resource{File: "host", Box: name}
	if hf.Resource != nil {
		r, err := hf.Resource(goArgs)
		if err != nil {
			m.record(name, false, "resource refused: "+err.Error())
			return nil, m.failErr(e.pos, name, fmt.Errorf("%w: %v", sec.ErrDenied, err))
		}
		res = r
	}
	if m.opts.Auth == nil {
		m.record(name, false, "no authorizer: default deny")
		return nil, m.failErr(e.pos, name, fmt.Errorf("%w: no authorizer configured", sec.ErrDenied))
	}
	d := m.opts.Auth.Authorize(m.opts.Principal, hf.Action, res)
	if !d.Allowed {
		m.record(name, false, d.Reason)
		return nil, m.failErr(e.pos, name, fmt.Errorf("%w: %s %s: %s", sec.ErrDenied, hf.Action, res, d.Reason))
	}
	m.record(name, true, d.Reason)

	out, err := m.safeCall(hf, goArgs)
	if err != nil {
		return nil, m.failErr(e.pos, name, fmt.Errorf("%w: %w", ErrHostFunc, err))
	}
	if err := m.checkTime(e.pos); err != nil {
		return nil, err
	}
	nodes := 0
	v, err := fromGo(out, m.lim, 0, &nodes)
	if err != nil {
		return nil, m.failErr(e.pos, name+": result", err)
	}
	if err := m.allocCells(nodes, e.pos); err != nil {
		return nil, err
	}
	return v, nil
}

func (m *machine) safeCall(hf *HostFunc, args []interface{}) (out interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("host function panicked: %v", r)
		}
	}()
	return hf.Fn(m.ctx, args)
}
