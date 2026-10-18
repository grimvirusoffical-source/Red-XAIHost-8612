package code

import (
	"fmt"
	"sort"
)

type pos struct{ line, col int }

type (
	expr interface{}
	stmt interface{}
)

type (
	numLit struct {
		pos
		v float64
	}
	strLit struct {
		pos
		v string
	}
	boolLit struct {
		pos
		v bool
	}
	nilLit struct{ pos }
	varRef struct {
		pos
		name string
	}
	arrayLit struct {
		pos
		elems []expr
	}
	indexExpr struct {
		pos
		x, i expr
	}
	callKind uint8
	callExpr struct {
		pos
		name string
		args []expr
		kind callKind
		fn   *funcDecl
		bi   *builtin
	}
	unaryExpr struct {
		pos
		op string
		x  expr
	}
	binExpr struct {
		pos
		op   string
		l, r expr
	}
	logicExpr struct {
		pos
		and  bool
		l, r expr
	}
)

const (
	callUser callKind = iota + 1
	callBuiltin
	callHost
)

type (
	block struct {
		pos
		stmts []stmt
	}
	letStmt struct {
		pos
		name string
		x    expr
	}
	assignStmt struct {
		pos
		target expr // *varRef or *indexExpr
		x      expr
	}
	ifStmt struct {
		pos
		cond expr
		then *block
		els  stmt // nil, *block or *ifStmt
	}
	whileStmt struct {
		pos
		cond expr
		body *block
	}
	forStmt struct {
		pos
		name     string
		from, to expr // to == nil: iterate over the array in from
		body     *block
	}
	returnStmt struct {
		pos
		x expr
	}
	breakStmt    struct{ pos }
	continueStmt struct{ pos }
	exprStmt     struct {
		pos
		x expr
	}
)

type funcDecl struct {
	pos
	name   string
	params []string
	body   *block
}

type parser struct {
	script   string
	toks     []token
	i        int
	depth    int
	maxDepth int
	loops    int
	inFn     bool
	funcs    map[string]*funcDecl
	order    []*funcDecl
	calls    []*callExpr
}

const maxChain = 1000

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) errAt(t token, format string, a ...interface{}) error {
	return &SyntaxError{Script: p.script, Line: t.line, Col: t.col, Msg: fmt.Sprintf(format, a...)}
}

func (t token) describe() string {
	switch t.kind {
	case tEOF:
		return "end of script"
	case tNum:
		return "a number"
	case tStr:
		return "a string"
	case tIdent:
		return fmt.Sprintf("%q", t.s)
	}
	return fmt.Sprintf("%q", t.s)
}

func (p *parser) isOp(s string) bool { t := p.peek(); return t.kind == tOp && t.s == s }
func (p *parser) isKw(s string) bool { t := p.peek(); return t.kind == tIdent && t.s == s }

func (p *parser) expectOp(s string) (token, error) {
	t := p.peek()
	if t.kind != tOp || t.s != s {
		return t, p.errAt(t, "expected %q, got %s", s, t.describe())
	}
	return p.next(), nil
}

func (p *parser) expectKw(s string) error {
	t := p.peek()
	if t.kind != tIdent || t.s != s {
		return p.errAt(t, "expected %q, got %s", s, t.describe())
	}
	p.next()
	return nil
}

func (p *parser) ident(what string) (token, error) {
	t := p.peek()
	if t.kind != tIdent || keywords[t.s] {
		return t, p.errAt(t, "expected %s, got %s", what, t.describe())
	}
	return p.next(), nil
}

func (p *parser) enter(t token) error {
	p.depth++
	if p.depth > p.maxDepth {
		return p.errAt(t, "nesting is deeper than %d levels", p.maxDepth)
	}
	return nil
}
func (p *parser) leave() { p.depth-- }

func (p *parser) parseProgram() (*block, error) {
	root := &block{pos: pos{1, 1}}
	for p.peek().kind != tEOF {
		if p.isKw("fn") {
			if err := p.parseFn(); err != nil {
				return nil, err
			}
			continue
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		root.stmts = append(root.stmts, s)
	}
	return root, nil
}

func (p *parser) parseFn() error {
	kw := p.next()
	if p.depth != 0 {
		return p.errAt(kw, "functions can only be declared at the top level")
	}
	name, err := p.ident("a function name")
	if err != nil {
		return err
	}
	if p.funcs[name.s] != nil {
		return p.errAt(name, "function %q is declared twice", name.s)
	}
	if _, err := p.expectOp("("); err != nil {
		return err
	}
	f := &funcDecl{pos: pos{kw.line, kw.col}, name: name.s}
	seen := map[string]bool{}
	for !p.isOp(")") {
		a, err := p.ident("a parameter name")
		if err != nil {
			return err
		}
		if seen[a.s] {
			return p.errAt(a, "duplicate parameter %q", a.s)
		}
		seen[a.s] = true
		f.params = append(f.params, a.s)
		if !p.isOp(")") {
			if _, err := p.expectOp(","); err != nil {
				return err
			}
		}
	}
	p.next()
	p.funcs[f.name] = f // visible to its own body (recursion)
	p.order = append(p.order, f)
	savedLoops := p.loops
	p.loops = 0
	p.inFn = true
	f.body, err = p.parseBlock()
	p.inFn = false
	p.loops = savedLoops
	return err
}

func (p *parser) parseBlock() (*block, error) {
	open, err := p.expectOp("{")
	if err != nil {
		return nil, err
	}
	if err := p.enter(open); err != nil {
		return nil, err
	}
	defer p.leave()
	b := &block{pos: pos{open.line, open.col}}
	for !p.isOp("}") {
		if p.peek().kind == tEOF {
			return nil, p.errAt(p.peek(), "missing }")
		}
		if p.isKw("fn") {
			return nil, p.errAt(p.peek(), "functions can only be declared at the top level")
		}
		s, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		b.stmts = append(b.stmts, s)
	}
	p.next()
	return b, nil
}

func (p *parser) parseStmt() (stmt, error) {
	t := p.peek()
	if err := p.enter(t); err != nil {
		return nil, err
	}
	defer p.leave()
	ps := pos{t.line, t.col}
	if t.kind == tIdent {
		switch t.s {
		case "let":
			p.next()
			name, err := p.ident("a variable name")
			if err != nil {
				return nil, err
			}
			if _, err := p.expectOp("="); err != nil {
				return nil, err
			}
			x, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expectOp(";"); err != nil {
				return nil, err
			}
			return &letStmt{ps, name.s, x}, nil
		case "if":
			return p.parseIf()
		case "while":
			p.next()
			c, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			p.loops++
			body, err := p.parseBlock()
			p.loops--
			if err != nil {
				return nil, err
			}
			return &whileStmt{ps, c, body}, nil
		case "for":
			p.next()
			v, err := p.ident("a loop variable")
			if err != nil {
				return nil, err
			}
			if err := p.expectKw("in"); err != nil {
				return nil, err
			}
			from, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			var to expr
			if p.isOp("..") {
				p.next()
				if to, err = p.parseExpr(); err != nil {
					return nil, err
				}
			}
			p.loops++
			body, err := p.parseBlock()
			p.loops--
			if err != nil {
				return nil, err
			}
			return &forStmt{ps, v.s, from, to, body}, nil
		case "return":
			p.next()
			var x expr
			if !p.isOp(";") {
				var err error
				if x, err = p.parseExpr(); err != nil {
					return nil, err
				}
			}
			if _, err := p.expectOp(";"); err != nil {
				return nil, err
			}
			return &returnStmt{ps, x}, nil
		case "break", "continue":
			p.next()
			if p.loops == 0 {
				return nil, p.errAt(t, "%s outside a loop", t.s)
			}
			if _, err := p.expectOp(";"); err != nil {
				return nil, err
			}
			if t.s == "break" {
				return &breakStmt{ps}, nil
			}
			return &continueStmt{ps}, nil
		case "else":
			return nil, p.errAt(t, "else without if")
		}
	}
	if p.isOp("{") {
		return p.parseBlock()
	}
	x, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.isOp("=") {
		eq := p.next()
		switch x.(type) {
		case *varRef, *indexExpr:
		default:
			return nil, p.errAt(eq, "cannot assign to this expression")
		}
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectOp(";"); err != nil {
			return nil, err
		}
		return &assignStmt{ps, x, v}, nil
	}
	if _, err := p.expectOp(";"); err != nil {
		return nil, err
	}
	return &exprStmt{ps, x}, nil
}

func (p *parser) parseIf() (stmt, error) {
	t := p.next() // if
	c, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	then, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	s := &ifStmt{pos: pos{t.line, t.col}, cond: c, then: then}
	if p.isKw("else") {
		p.next()
		if p.isKw("if") {
			if err := p.enter(p.peek()); err != nil {
				return nil, err
			}
			s.els, err = p.parseIf()
			p.leave()
		} else {
			s.els, err = p.parseBlock()
		}
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Expression grammar, lowest precedence first:
// or, and, equality, comparison, additive, multiplicative, unary, postfix, primary.

func (p *parser) parseExpr() (expr, error) {
	t := p.peek()
	if err := p.enter(t); err != nil {
		return nil, err
	}
	defer p.leave()
	return p.parseOr()
}

func (p *parser) opIs(t token, ops ...string) bool {
	if t.kind != tOp && t.kind != tIdent {
		return false
	}
	for _, o := range ops {
		if t.s == o && (t.kind == tOp || keywords[o]) {
			return true
		}
	}
	return false
}

func (p *parser) parseOr() (expr, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	n := 0
	for p.opIs(p.peek(), "or", "||") {
		t := p.next()
		if n++; n > maxChain {
			return nil, p.errAt(t, "expression chain too long")
		}
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &logicExpr{pos{t.line, t.col}, false, l, r}
	}
	return l, nil
}

func (p *parser) parseAnd() (expr, error) {
	l, err := p.parseEq()
	if err != nil {
		return nil, err
	}
	n := 0
	for p.opIs(p.peek(), "and", "&&") {
		t := p.next()
		if n++; n > maxChain {
			return nil, p.errAt(t, "expression chain too long")
		}
		r, err := p.parseEq()
		if err != nil {
			return nil, err
		}
		l = &logicExpr{pos{t.line, t.col}, true, l, r}
	}
	return l, nil
}

func (p *parser) binLevel(ops []string, next func() (expr, error)) (expr, error) {
	l, err := next()
	if err != nil {
		return nil, err
	}
	n := 0
	for p.peek().kind == tOp && p.opIs(p.peek(), ops...) {
		t := p.next()
		if n++; n > maxChain {
			return nil, p.errAt(t, "expression chain too long")
		}
		r, err := next()
		if err != nil {
			return nil, err
		}
		l = &binExpr{pos{t.line, t.col}, t.s, l, r}
	}
	return l, nil
}

func (p *parser) parseEq() (expr, error) { return p.binLevel([]string{"==", "!="}, p.parseCmp) }
func (p *parser) parseCmp() (expr, error) {
	return p.binLevel([]string{"<", "<=", ">", ">="}, p.parseAdd)
}
func (p *parser) parseAdd() (expr, error) { return p.binLevel([]string{"+", "-"}, p.parseMul) }
func (p *parser) parseMul() (expr, error) { return p.binLevel([]string{"*", "/", "%"}, p.parseUnary) }

func (p *parser) parseUnary() (expr, error) {
	t := p.peek()
	if p.opIs(t, "-", "!", "not") {
		if err := p.enter(t); err != nil {
			return nil, err
		}
		defer p.leave()
		p.next()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		op := t.s
		if op == "!" {
			op = "not"
		}
		return &unaryExpr{pos{t.line, t.col}, op, x}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (expr, error) {
	x, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	n := 0
	for p.isOp("[") {
		t := p.next()
		if n++; n > maxChain {
			return nil, p.errAt(t, "index chain too long")
		}
		i, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectOp("]"); err != nil {
			return nil, err
		}
		x = &indexExpr{pos{t.line, t.col}, x, i}
	}
	return x, nil
}

func (p *parser) parsePrimary() (expr, error) {
	t := p.next()
	ps := pos{t.line, t.col}
	switch t.kind {
	case tNum:
		return &numLit{ps, t.n}, nil
	case tStr:
		return &strLit{ps, t.s}, nil
	case tIdent:
		switch t.s {
		case "true":
			return &boolLit{ps, true}, nil
		case "false":
			return &boolLit{ps, false}, nil
		case "nil":
			return &nilLit{ps}, nil
		}
		if keywords[t.s] {
			return nil, p.errAt(t, "unexpected %s", t.describe())
		}
		if p.isOp("(") {
			p.next()
			c := &callExpr{pos: ps, name: t.s}
			for !p.isOp(")") {
				a, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				c.args = append(c.args, a)
				if len(c.args) > 32 {
					return nil, p.errAt(t, "too many arguments")
				}
				if !p.isOp(")") {
					if _, err := p.expectOp(","); err != nil {
						return nil, err
					}
				}
			}
			p.next()
			p.calls = append(p.calls, c)
			return c, nil
		}
		return &varRef{ps, t.s}, nil
	case tOp:
		switch t.s {
		case "(":
			x, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expectOp(")"); err != nil {
				return nil, err
			}
			return x, nil
		case "[":
			a := &arrayLit{pos: ps}
			for !p.isOp("]") {
				e, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				a.elems = append(a.elems, e)
				if !p.isOp("]") {
					if _, err := p.expectOp(","); err != nil {
						return nil, err
					}
				}
			}
			p.next()
			return a, nil
		}
	}
	return nil, p.errAt(t, "unexpected %s", t.describe())
}

// Program is a compiled script. It is immutable and may be run many times,
// concurrently.
type Program struct {
	name   string
	params []string
	uses   []string
	useSet map[string]bool
	funcs  map[string]*funcDecl
	body   *block
	limits Limits // what the script asks for; Effective() clamps it
}

// Name is the script's name.
func (p *Program) Name() string { return p.name }

// Params are the parameter names, in order.
func (p *Program) Params() []string { return append([]string(nil), p.params...) }

// Uses are the host functions the script declared, sorted.
func (p *Program) Uses() []string { return append([]string(nil), p.uses...) }

// Limits returns the limits the script asks for (zero fields: not asked).
func (p *Program) Limits() Limits { return p.limits }

// Compile parses and checks a script. uses lists the host functions it may
// call; calling anything else that is not a built-in or one of the script's
// own functions is a compile error.
func Compile(name, src string, params, uses []string, requested Limits) (*Program, error) {
	return CompileWith(name, src, params, uses, requested, Limits{})
}

// CompileWith is Compile with explicit compile-time limits (MaxSourceBytes,
// MaxNesting) taken from ceiling.
func CompileWith(name, src string, params, uses []string, requested, ceiling Limits) (*Program, error) {
	c := ceiling.WithDefaults()
	bad := func(format string, a ...interface{}) error {
		return &SyntaxError{Script: name, Line: 1, Col: 1, Msg: fmt.Sprintf(format, a...)}
	}
	if len(src) > c.MaxSourceBytes {
		return nil, bad("source is %d bytes, the limit is %d", len(src), c.MaxSourceBytes)
	}
	prog := &Program{name: name, limits: requested, useSet: map[string]bool{}}
	seen := map[string]bool{}
	for _, a := range params {
		if !validIdent(a) || seen[a] {
			return nil, bad("bad or duplicate parameter %q", a)
		}
		seen[a] = true
		prog.params = append(prog.params, a)
	}
	for _, u := range uses {
		if !validIdent(u) || builtins[u] != nil {
			return nil, bad("bad host function name %q in Uses", u)
		}
		if !prog.useSet[u] {
			prog.useSet[u] = true
			prog.uses = append(prog.uses, u)
		}
	}
	sort.Strings(prog.uses)
	toks, err := lex(name, src)
	if err != nil {
		return nil, err
	}
	p := &parser{script: name, toks: toks, maxDepth: c.MaxNesting, funcs: map[string]*funcDecl{}}
	body, err := p.parseProgram()
	if err != nil {
		return nil, err
	}
	for _, f := range p.order {
		if builtins[f.name] != nil || prog.useSet[f.name] {
			return nil, &SyntaxError{Script: name, Line: f.line, Col: f.col,
				Msg: fmt.Sprintf("function %q collides with a built-in or host function name", f.name)}
		}
	}
	for _, call := range p.calls {
		switch {
		case p.funcs[call.name] != nil:
			f := p.funcs[call.name]
			if len(call.args) != len(f.params) {
				return nil, &SyntaxError{Script: name, Line: call.line, Col: call.col,
					Msg: fmt.Sprintf("%s takes %d arguments, got %d", call.name, len(f.params), len(call.args))}
			}
			call.kind, call.fn = callUser, f
		case builtins[call.name] != nil:
			b := builtins[call.name]
			if len(call.args) < b.min || len(call.args) > b.max {
				return nil, &SyntaxError{Script: name, Line: call.line, Col: call.col,
					Msg: fmt.Sprintf("%s takes %s arguments, got %d", call.name, b.arity(), len(call.args))}
			}
			call.kind, call.bi = callBuiltin, b
		case prog.useSet[call.name]:
			call.kind = callHost
		default:
			return nil, &SyntaxError{Script: name, Line: call.line, Col: call.col,
				Msg: fmt.Sprintf("unknown function %q (host functions must be declared in Uses)", call.name)}
		}
	}
	prog.funcs = p.funcs
	prog.body = body
	return prog, nil
}

func validIdent(s string) bool {
	if s == "" || len(s) > 64 || keywords[s] || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentStart(s[i]) && !isDigit(s[i]) {
			return false
		}
	}
	return true
}
