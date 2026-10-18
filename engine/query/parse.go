package query

import (
	"fmt"
	"math"
	"strings"

	"redx/box"
)

// Parse parses one statement with the default limits. It returns the
// statement and the number of ? placeholders in it.
func Parse(src string) (Stmt, int, error) { return parseWith(src, DefaultLimits()) }

var reserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`SELECT FROM WHERE AND OR NOT IN LIKE ORDER BY ASC DESC LIMIT OFFSET
		INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT INNER AS IS TRUE FALSE NELL EXPLAIN BETWEEN MATCH MATCH_ANY`) {
		reserved[w] = true
	}
}

func isReserved(s string) bool { return reserved[strings.ToUpper(s)] }

type parser struct {
	src     string
	toks    []token
	i       int
	nparams int
	depth   int
	lim     Limits
}

func parseWith(src string, lim Limits) (Stmt, int, error) {
	lim = lim.withDefaults()
	if len(src) > lim.MaxQueryBytes {
		return nil, 0, fmt.Errorf("%w: %d bytes (limit %d)", ErrTooLarge, len(src), lim.MaxQueryBytes)
	}
	toks, err := lex(src, lim.MaxTokens)
	if err != nil {
		return nil, 0, err
	}
	p := &parser{src: src, toks: toks, lim: lim}
	st, err := p.statement()
	if err != nil {
		return nil, 0, err
	}
	if p.isPunct(";") {
		p.i++
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, 0, p.errAt(t, "unexpected "+t.describe()+" after the end of the statement")
	}
	return st, p.nparams, nil
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) peekN(n int) token {
	if p.i+n >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.i+n]
}
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}
func (p *parser) prevEnd() int {
	if p.i == 0 {
		return 0
	}
	return p.toks[p.i-1].end
}

func (p *parser) errAt(t token, msg string) error {
	near := ""
	if t.kind != tEOF {
		near = p.src[t.pos.Offset:t.end]
		if len(near) > 40 {
			near = near[:40] + "..."
		}
	}
	return &SyntaxError{Pos: t.pos, Msg: msg, Near: near}
}

func (p *parser) expected(what string) error {
	t := p.peek()
	return p.errAt(t, fmt.Sprintf("expected %s, found %s", what, t.describe()))
}

func (p *parser) isKw(w string) bool {
	t := p.peek()
	return t.kind == tIdent && strings.EqualFold(t.text, w)
}

func (p *parser) isKwN(n int, w string) bool {
	t := p.peekN(n)
	return t.kind == tIdent && strings.EqualFold(t.text, w)
}

func (p *parser) acceptKw(w string) bool {
	if p.isKw(w) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expectKw(w string) (token, error) {
	if !p.isKw(w) {
		return token{}, p.expected(w)
	}
	return p.next(), nil
}

func (p *parser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == tPunct && t.text == s
}

func (p *parser) acceptPunct(s string) bool {
	if p.isPunct(s) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expectPunct(s string) (token, error) {
	if !p.isPunct(s) {
		return token{}, p.expected("'" + s + "'")
	}
	return p.next(), nil
}

func (p *parser) enter(t token) error {
	p.depth++
	if p.depth > p.lim.MaxDepth {
		return p.errAt(t, fmt.Sprintf("expression nested too deeply (limit %d)", p.lim.MaxDepth))
	}
	return nil
}
func (p *parser) leave() { p.depth-- }

// ---- statements ----------------------------------------------------------

func (p *parser) statement() (Stmt, error) {
	t := p.peek()
	if t.kind != tIdent {
		return nil, p.expected("SELECT, INSERT, UPDATE, DELETE or EXPLAIN")
	}
	switch strings.ToUpper(t.text) {
	case "EXPLAIN":
		p.next()
		if p.isKw("EXPLAIN") {
			return nil, p.errAt(p.peek(), "EXPLAIN cannot be nested")
		}
		inner, err := p.statement()
		if err != nil {
			return nil, err
		}
		return &Explain{P: t.pos, Inner: inner}, nil
	case "SELECT":
		return p.selectStmt()
	case "INSERT":
		return p.insertStmt()
	case "UPDATE":
		return p.updateStmt()
	case "DELETE":
		return p.deleteStmt()
	}
	return nil, p.errAt(t, "unknown statement "+t.describe()+"; expected SELECT, INSERT, UPDATE, DELETE or EXPLAIN")
}

func (p *parser) name(what string) (string, token, error) {
	t := p.peek()
	switch t.kind {
	case tQIdent:
		p.i++
		return t.text, t, nil
	case tIdent:
		if isReserved(t.text) {
			return "", t, p.errAt(t, fmt.Sprintf("%s cannot be the reserved word %s; quote it as \"%s\"", what, strings.ToUpper(t.text), t.text))
		}
		p.i++
		return t.text, t, nil
	}
	return "", t, p.expected(what)
}

// source parses file[.Box] [[AS] alias].
func (p *parser) source(allowAlias bool) (Source, error) {
	t := p.peek()
	file, _, err := p.name("a file name")
	if err != nil {
		return Source{}, err
	}
	s := Source{P: t.pos, File: file}
	if p.isPunct(".") {
		p.i++
		b, _, err := p.name("a box name after '.'")
		if err != nil {
			return Source{}, err
		}
		s.Box = b
	}
	if allowAlias {
		if p.acceptKw("AS") {
			a, _, err := p.name("an alias")
			if err != nil {
				return Source{}, err
			}
			s.Alias = a
		} else if t := p.peek(); t.kind == tQIdent || (t.kind == tIdent && !isReserved(t.text)) {
			p.i++
			s.Alias = t.text
		}
	}
	return s, nil
}

func (p *parser) selectStmt() (Stmt, error) {
	st := p.next()
	sel := &Select{P: st.pos}
	for {
		it, err := p.selectItem()
		if err != nil {
			return nil, err
		}
		sel.Items = append(sel.Items, it)
		if !p.acceptPunct(",") {
			break
		}
	}
	if _, err := p.expectKw("FROM"); err != nil {
		return nil, err
	}
	src, err := p.source(true)
	if err != nil {
		return nil, err
	}
	sel.From = src
	for p.isKw("JOIN") || p.isKw("LEFT") || p.isKw("INNER") {
		jt := p.next()
		j := Join{P: jt.pos}
		if strings.EqualFold(jt.text, "LEFT") {
			j.Left = true
			if _, err := p.expectKw("JOIN"); err != nil {
				return nil, err
			}
		} else if strings.EqualFold(jt.text, "INNER") {
			if _, err := p.expectKw("JOIN"); err != nil {
				return nil, err
			}
		}
		f, err := p.fieldPath()
		if err != nil {
			return nil, err
		}
		if len(f.Segs) < 2 {
			return nil, &SyntaxError{Pos: f.P, Msg: "JOIN needs alias.field (the field of an earlier source that holds @> links)", Near: f.Raw}
		}
		j.Via = f
		p.acceptKw("AS")
		a, _, err := p.name("an alias for the joined records")
		if err != nil {
			return nil, err
		}
		j.Alias = a
		sel.Joins = append(sel.Joins, j)
		if len(sel.Joins) > p.lim.MaxJoins {
			return nil, p.errAt(jt, fmt.Sprintf("too many JOINs (limit %d)", p.lim.MaxJoins))
		}
	}
	if p.acceptKw("WHERE") {
		w, err := p.expr()
		if err != nil {
			return nil, err
		}
		sel.Where = w
	}
	if p.isKw("ORDER") {
		p.next()
		if _, err := p.expectKw("BY"); err != nil {
			return nil, err
		}
		for {
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			o := OrderItem{Expr: e}
			if p.acceptKw("DESC") {
				o.Desc = true
			} else {
				p.acceptKw("ASC")
			}
			sel.Order = append(sel.Order, o)
			if !p.acceptPunct(",") {
				break
			}
		}
	}
	// LIMIT and OFFSET in either order, each once.
	for p.isKw("LIMIT") || p.isKw("OFFSET") {
		kw := p.next()
		e, err := p.countExpr(strings.ToUpper(kw.text))
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(kw.text, "LIMIT") {
			if sel.Limit != nil {
				return nil, p.errAt(kw, "LIMIT given twice")
			}
			sel.Limit = e
		} else {
			if sel.Offset != nil {
				return nil, p.errAt(kw, "OFFSET given twice")
			}
			sel.Offset = e
		}
	}
	return sel, nil
}

// countExpr parses a non-negative integer literal or a ? placeholder.
func (p *parser) countExpr(what string) (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.i++
		if t.num != math.Trunc(t.num) || t.num < 0 || t.num > 1<<53 {
			return nil, p.errAt(t, what+" must be a non-negative whole number")
		}
		return &Lit{P: t.pos, V: box.Number(t.num)}, nil
	case tParam:
		p.i++
		n := p.nparams
		p.nparams++
		return &Param{P: t.pos, N: n}, nil
	}
	return nil, p.expected("a number or ? after " + what)
}

func (p *parser) selectItem() (SelectItem, error) {
	t := p.peek()
	it := SelectItem{P: t.pos}
	if p.isPunct("*") {
		p.i++
		it.Star = true
		it.Text = "*"
		return it, nil
	}
	if (t.kind == tIdent || t.kind == tQIdent) && p.peekN(1).kind == tPunct && p.peekN(1).text == "." &&
		p.peekN(2).kind == tPunct && p.peekN(2).text == "*" {
		it.Star = true
		it.StarAlias = t.text
		p.i += 3
		it.Text = t.text + ".*"
		return it, nil
	}
	e, err := p.expr()
	if err != nil {
		return it, err
	}
	it.Expr = e
	it.Text = strings.TrimSpace(p.src[t.pos.Offset:p.prevEnd()])
	if p.acceptKw("AS") {
		a, _, err := p.name("a column alias")
		if err != nil {
			return it, err
		}
		it.As = a
	}
	return it, nil
}

func (p *parser) insertStmt() (Stmt, error) {
	st := p.next()
	if _, err := p.expectKw("INTO"); err != nil {
		return nil, err
	}
	src, err := p.source(false)
	if err != nil {
		return nil, err
	}
	ins := &Insert{P: st.pos, Target: src}
	if _, err := p.expectPunct("("); err != nil {
		return nil, err
	}
	for {
		f, err := p.fieldPath()
		if err != nil {
			return nil, err
		}
		ins.Cols = append(ins.Cols, f)
		if !p.acceptPunct(",") {
			break
		}
	}
	if _, err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	if _, err := p.expectKw("VALUES"); err != nil {
		return nil, err
	}
	for {
		lp, err := p.expectPunct("(")
		if err != nil {
			return nil, err
		}
		var row []Expr
		for {
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			if f := firstField(e); f != nil {
				return nil, &SyntaxError{Pos: f.P, Msg: "field references are not allowed in VALUES", Near: f.Raw}
			}
			row = append(row, e)
			if !p.acceptPunct(",") {
				break
			}
		}
		if _, err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		if len(row) != len(ins.Cols) {
			return nil, &SyntaxError{Pos: lp.pos, Msg: fmt.Sprintf("row has %d values but %d columns were named", len(row), len(ins.Cols))}
		}
		ins.Rows = append(ins.Rows, row)
		if len(ins.Rows) > p.lim.MaxInsertRows {
			return nil, p.errAt(lp, fmt.Sprintf("too many rows in one INSERT (limit %d)", p.lim.MaxInsertRows))
		}
		if !p.acceptPunct(",") {
			break
		}
	}
	return ins, nil
}

func (p *parser) updateStmt() (Stmt, error) {
	st := p.next()
	src, err := p.source(true)
	if err != nil {
		return nil, err
	}
	up := &Update{P: st.pos, Target: src}
	if _, err := p.expectKw("SET"); err != nil {
		return nil, err
	}
	for {
		f, err := p.fieldPath()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectPunct("="); err != nil {
			return nil, err
		}
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		up.Sets = append(up.Sets, Assign{Target: f, Value: e})
		if !p.acceptPunct(",") {
			break
		}
	}
	if !p.isKw("WHERE") {
		return nil, p.errAt(p.peek(), "UPDATE requires a WHERE clause (write WHERE TRUE to change every record); found "+p.peek().describe())
	}
	p.next()
	w, err := p.expr()
	if err != nil {
		return nil, err
	}
	up.Where = w
	return up, nil
}

func (p *parser) deleteStmt() (Stmt, error) {
	st := p.next()
	if _, err := p.expectKw("FROM"); err != nil {
		return nil, err
	}
	src, err := p.source(true)
	if err != nil {
		return nil, err
	}
	d := &Delete{P: st.pos, Target: src}
	if !p.isKw("WHERE") {
		return nil, p.errAt(p.peek(), "DELETE requires a WHERE clause (write WHERE TRUE to delete every record); found "+p.peek().describe())
	}
	p.next()
	w, err := p.expr()
	if err != nil {
		return nil, err
	}
	d.Where = w
	return d, nil
}

// ---- expressions ---------------------------------------------------------

func (p *parser) expr() (Expr, error) { return p.orExpr() }

func (p *parser) orExpr() (Expr, error) {
	l, err := p.andExpr()
	if err != nil {
		return nil, err
	}
	for p.isKw("OR") {
		t := p.next()
		r, err := p.andExpr()
		if err != nil {
			return nil, err
		}
		l = &Binary{P: t.pos, Op: "or", L: l, R: r}
	}
	return l, nil
}

func (p *parser) andExpr() (Expr, error) {
	l, err := p.notExpr()
	if err != nil {
		return nil, err
	}
	for p.isKw("AND") {
		t := p.next()
		r, err := p.notExpr()
		if err != nil {
			return nil, err
		}
		l = &Binary{P: t.pos, Op: "and", L: l, R: r}
	}
	return l, nil
}

func (p *parser) notExpr() (Expr, error) {
	if p.isKw("NOT") {
		t := p.next()
		if err := p.enter(t); err != nil {
			return nil, err
		}
		defer p.leave()
		x, err := p.notExpr()
		if err != nil {
			return nil, err
		}
		return &Not{P: t.pos, X: x}, nil
	}
	return p.predicate()
}

func (p *parser) predicate() (Expr, error) {
	l, err := p.additive()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tPunct {
		switch t.text {
		case "=", "!=", "<", "<=", ">", ">=":
			p.i++
			r, err := p.additive()
			if err != nil {
				return nil, err
			}
			return &Binary{P: t.pos, Op: t.text, L: l, R: r}, nil
		}
		return l, nil
	}
	if t.kind != tIdent {
		return l, nil
	}
	switch strings.ToUpper(t.text) {
	case "IS":
		p.i++
		neg := p.acceptKw("NOT")
		if _, err := p.expectKw("NELL"); err != nil {
			return nil, err
		}
		return &IsNell{P: t.pos, X: l, Neg: neg}, nil
	case "NOT":
		// x NOT IN / NOT LIKE / NOT BETWEEN
		nx := p.peekN(1)
		if nx.kind == tIdent {
			switch strings.ToUpper(nx.text) {
			case "IN", "LIKE", "BETWEEN":
				p.i++
				return p.postfixPred(l, true)
			}
		}
		return l, nil
	case "IN", "LIKE", "BETWEEN":
		return p.postfixPred(l, false)
	}
	return l, nil
}

func (p *parser) postfixPred(l Expr, neg bool) (Expr, error) {
	t := p.next()
	switch strings.ToUpper(t.text) {
	case "IN":
		if _, err := p.expectPunct("("); err != nil {
			return nil, err
		}
		in := &In{P: t.pos, X: l, Neg: neg}
		for {
			e, err := p.additive()
			if err != nil {
				return nil, err
			}
			in.List = append(in.List, e)
			if len(in.List) > p.lim.MaxInList {
				return nil, p.errAt(t, fmt.Sprintf("IN list too long (limit %d)", p.lim.MaxInList))
			}
			if !p.acceptPunct(",") {
				break
			}
		}
		if _, err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return in, nil
	case "LIKE":
		pt := p.peek()
		pat, err := p.additive()
		if err != nil {
			return nil, err
		}
		switch pat.(type) {
		case *Lit, *Param:
		default:
			return nil, p.errAt(pt, "LIKE needs a string literal or ? pattern")
		}
		return &Like{P: t.pos, X: l, Pat: pat, Neg: neg}, nil
	default: // BETWEEN
		lo, err := p.additive()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectKw("AND"); err != nil {
			return nil, err
		}
		hi, err := p.additive()
		if err != nil {
			return nil, err
		}
		return &Between{P: t.pos, X: l, Lo: lo, Hi: hi, Neg: neg}, nil
	}
}

func (p *parser) additive() (Expr, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isPunct("+") || p.isPunct("-") {
		t := p.next()
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = &Binary{P: t.pos, Op: t.text, L: l, R: r}
	}
	return l, nil
}

func (p *parser) term() (Expr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isPunct("*") || p.isPunct("/") {
		t := p.next()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = &Binary{P: t.pos, Op: t.text, L: l, R: r}
	}
	return l, nil
}

func (p *parser) unary() (Expr, error) {
	if p.isPunct("-") {
		t := p.next()
		if err := p.enter(t); err != nil {
			return nil, err
		}
		defer p.leave()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		if l, ok := x.(*Lit); ok && l.V.Kind == box.KindNumber {
			return &Lit{P: t.pos, V: box.Number(-l.V.Num)}, nil
		}
		return &Unary{P: t.pos, Op: "-", X: x}, nil
	}
	return p.primary()
}

func (p *parser) primary() (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.i++
		return &Lit{P: t.pos, V: box.Number(t.num)}, nil
	case tString:
		p.i++
		return &Lit{P: t.pos, V: box.String(t.text)}, nil
	case tParam:
		p.i++
		n := p.nparams
		p.nparams++
		return &Param{P: t.pos, N: n}, nil
	case tPunct:
		if t.text == "(" {
			p.i++
			if err := p.enter(t); err != nil {
				return nil, err
			}
			defer p.leave()
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return e, nil
		}
	case tQIdent:
		return p.fieldPath()
	case tIdent:
		switch strings.ToUpper(t.text) {
		case "TRUE":
			p.i++
			return &Lit{P: t.pos, V: box.Bool(true)}, nil
		case "FALSE":
			p.i++
			return &Lit{P: t.pos, V: box.Bool(false)}, nil
		case "NELL":
			p.i++
			return &Lit{P: t.pos, V: box.Nell()}, nil
		case "MATCH", "MATCH_ANY":
			return p.matchExpr()
		}
		if isReserved(t.text) {
			return nil, p.errAt(t, fmt.Sprintf("unexpected keyword %s; to use it as a field name quote it as \"%s\"", strings.ToUpper(t.text), t.text))
		}
		return p.fieldPath()
	}
	return nil, p.expected("a value, field name, ? or '('")
}

func (p *parser) matchExpr() (Expr, error) {
	t := p.next()
	if _, err := p.expectPunct("("); err != nil {
		return nil, err
	}
	f, err := p.fieldPath()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectPunct(","); err != nil {
		return nil, err
	}
	pt := p.peek()
	terms, err := p.additive()
	if err != nil {
		return nil, err
	}
	switch terms.(type) {
	case *Lit, *Param:
	default:
		return nil, p.errAt(pt, "MATCH needs a string literal or ? for the words")
	}
	if _, err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	return &Match{P: t.pos, Field: f, Terms: terms, Any: strings.EqualFold(t.text, "MATCH_ANY")}, nil
}

// fieldPath parses Name[.Name]* with optional [n] / [ID=n] selectors.
func (p *parser) fieldPath() (*Field, error) {
	first := p.peek()
	f := &Field{P: first.pos}
	for {
		var seg box.Seg
		t := p.peek()
		switch {
		case t.kind == tQIdent:
			seg.Name = t.text
			p.i++
		case t.kind == tIdent && ((len(f.Segs) > 0 && t.pos.Offset == p.prevEnd()) || !isReserved(t.text)):
			seg.Name = t.text
			p.i++
		case t.kind == tIdent:
			return nil, p.errAt(t, fmt.Sprintf("unexpected keyword %s; to use it as a field name quote it as \"%s\"", strings.ToUpper(t.text), t.text))
		default:
			if len(f.Segs) == 0 {
				return nil, p.expected("a field name")
			}
			return nil, p.expected("a field name after '.'")
		}
		if p.isPunct("[") {
			lb := p.next()
			if p.isKw("ID") && p.peekN(1).kind == tPunct && p.peekN(1).text == "=" {
				p.i += 2
			}
			nt := p.peek()
			if nt.kind != tNumber || nt.num != math.Trunc(nt.num) || math.Abs(nt.num) > 1<<53 {
				return nil, p.errAt(nt, "expected a whole number ID in [...]")
			}
			p.i++
			if _, err := p.expectPunct("]"); err != nil {
				return nil, p.errAt(lb, "unclosed '[' in field path")
			}
			seg.ID, seg.HasID = int64(nt.num), true
		}
		f.Segs = append(f.Segs, seg)
		if len(f.Segs) > p.lim.MaxPathDepth {
			return nil, p.errAt(t, fmt.Sprintf("field path too deep (limit %d)", p.lim.MaxPathDepth))
		}
		if p.isPunct(".") && !(p.peekN(1).kind == tPunct && p.peekN(1).text == "*") {
			p.i++
			continue
		}
		break
	}
	f.Raw = strings.TrimSpace(p.src[first.pos.Offset:p.prevEnd()])
	return f, nil
}

func firstField(e Expr) *Field {
	var found *Field
	walk(e, func(x Expr) {
		if f, ok := x.(*Field); ok && found == nil {
			found = f
		}
	})
	return found
}

// walk visits e and all of its sub-expressions (pre-order).
func walk(e Expr, fn func(Expr)) {
	if e == nil {
		return
	}
	fn(e)
	switch x := e.(type) {
	case *Unary:
		walk(x.X, fn)
	case *Not:
		walk(x.X, fn)
	case *Binary:
		walk(x.L, fn)
		walk(x.R, fn)
	case *In:
		walk(x.X, fn)
		for _, v := range x.List {
			walk(v, fn)
		}
	case *Like:
		walk(x.X, fn)
		walk(x.Pat, fn)
	case *Between:
		walk(x.X, fn)
		walk(x.Lo, fn)
		walk(x.Hi, fn)
	case *IsNell:
		walk(x.X, fn)
	case *Match:
		fn(x.Field)
		walk(x.Terms, fn)
	}
}
