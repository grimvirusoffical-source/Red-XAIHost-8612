package pulse

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"redx/box"
)

// ---- tokens ---------------------------------------------------------------

type tokKind int

const (
	tkEOF tokKind = iota
	tkNum
	tkStr
	tkPath // dotted field path, e.g. Health or Stats.Hp or Inventory[id=10].Count
	tkRef  // "file":Box[ID=1].Field
	tkKw   // and or not true false nell null
	tkOp   // == = != < <= > >= + - * / ( )
)

type token struct {
	kind tokKind
	text string
	num  float64
	str  string
	path box.Path
	pos  int
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool  { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func lexExpr(src string) ([]token, error) {
	var toks []token
	rs := []rune(src)
	i := 0
	for i < len(rs) {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case unicode.IsDigit(r) || (r == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				k := j + 1
				if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
					k++
				}
				if k < len(rs) && unicode.IsDigit(rs[k]) {
					for k < len(rs) && unicode.IsDigit(rs[k]) {
						k++
					}
					j = k
				}
			}
			f, err := strconv.ParseFloat(string(rs[i:j]), 64)
			if err != nil {
				return nil, fmt.Errorf("bad number %q at %d", string(rs[i:j]), i)
			}
			toks = append(toks, token{kind: tkNum, num: f, pos: i})
			i = j
		case r == '"':
			s, j, err := lexQuoted(rs, i)
			if err != nil {
				return nil, err
			}
			// "file":Box... is a reference
			if j+1 < len(rs) && rs[j] == ':' && isIdentStart(rs[j+1]) {
				k := j + 1
				for k < len(rs) && (isIdentPart(rs[k]) || strings.ContainsRune(".[]=*-", rs[k])) {
					k++
				}
				p, err := box.ParsePath(string(rs[i:k]))
				if err != nil {
					return nil, fmt.Errorf("bad reference at %d: %v", i, err)
				}
				toks = append(toks, token{kind: tkRef, path: p, pos: i})
				i = k
				continue
			}
			toks = append(toks, token{kind: tkStr, str: s, pos: i})
			i = j
		case isIdentStart(r):
			j := i
			for {
				for j < len(rs) && isIdentPart(rs[j]) {
					j++
				}
				if j < len(rs) && rs[j] == '[' {
					for j < len(rs) && rs[j] != ']' {
						j++
					}
					if j >= len(rs) {
						return nil, fmt.Errorf("missing ']' at %d", i)
					}
					j++
				}
				if j+1 < len(rs) && rs[j] == '.' && isIdentStart(rs[j+1]) {
					j++
					continue
				}
				break
			}
			text := string(rs[i:j])
			switch strings.ToLower(text) {
			case "and", "or", "not", "true", "false", "nell", "null":
				toks = append(toks, token{kind: tkKw, text: strings.ToLower(text), pos: i})
			default:
				p, err := box.ParsePath(text)
				if err != nil {
					return nil, fmt.Errorf("bad field path %q: %v", text, err)
				}
				for _, s := range p.Segs {
					if s.Wild || s.AnyID {
						return nil, fmt.Errorf("wildcards are not allowed in %q", text)
					}
				}
				toks = append(toks, token{kind: tkPath, path: p, pos: i})
			}
			i = j
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			switch two {
			case "==", "!=", "<=", ">=", "<>":
				op := two
				if op == "<>" {
					op = "!="
				}
				toks = append(toks, token{kind: tkOp, text: op, pos: i})
				i += 2
				continue
			}
			if strings.ContainsRune("=<>+-*/()", r) {
				toks = append(toks, token{kind: tkOp, text: string(r), pos: i})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected %q at %d", string(r), i)
		}
	}
	toks = append(toks, token{kind: tkEOF, pos: len(rs)})
	return toks, nil
}

func lexQuoted(rs []rune, i int) (string, int, error) {
	var sb strings.Builder
	j := i + 1
	for j < len(rs) {
		switch rs[j] {
		case '"':
			return sb.String(), j + 1, nil
		case '\\':
			j++
			if j >= len(rs) {
				return "", 0, fmt.Errorf("unterminated string at %d", i)
			}
			switch rs[j] {
			case 'n':
				sb.WriteRune('\n')
			case 't':
				sb.WriteRune('\t')
			case '"', '\\':
				sb.WriteRune(rs[j])
			default:
				return "", 0, fmt.Errorf("unknown escape \\%c at %d", rs[j], j)
			}
		default:
			sb.WriteRune(rs[j])
		}
		j++
	}
	return "", 0, fmt.Errorf("unterminated string at %d", i)
}

// ---- AST ------------------------------------------------------------------

// env is what expressions evaluate against.
type env struct {
	rec  *box.Entry                       // the record being evaluated (may be nil)
	read func(box.Ref) (box.Value, error) // cross-record reads
}

// Expr is a compiled expression.
type Expr interface {
	eval(*env) (box.Value, error)
	// locals appends the local field paths the expression reads.
	locals(*[][]box.Seg)
}

type litExpr struct{ v box.Value }
type pathExpr struct{ segs []box.Seg }
type refExpr struct{ ref box.Ref }
type unaryMinus struct{ x Expr }
type notExpr struct{ x Expr }
type binExpr struct {
	op   string
	l, r Expr
}

func (e litExpr) eval(*env) (box.Value, error) { return e.v, nil }
func (e litExpr) locals(*[][]box.Seg)          {}
func (e pathExpr) locals(out *[][]box.Seg)     { *out = append(*out, e.segs) }
func (e refExpr) locals(*[][]box.Seg)          {}
func (e unaryMinus) locals(out *[][]box.Seg)   { e.x.locals(out) }
func (e notExpr) locals(out *[][]box.Seg)      { e.x.locals(out) }
func (e binExpr) locals(out *[][]box.Seg)      { e.l.locals(out); e.r.locals(out) }

// lookup follows segs inside a record. A missing field is (nell, false).
func lookup(rec *box.Entry, segs []box.Seg) (box.Value, bool) {
	if rec == nil {
		return box.Value{}, false
	}
	if len(segs) == 0 {
		return rec.Value, true
	}
	if rec.Value.Kind != box.KindBox || rec.Value.Box == nil {
		return box.Value{}, false
	}
	ent, err := box.Resolve(rec.Value.Box, segs)
	if err != nil {
		return box.Value{}, false
	}
	return ent.Value, true
}

func (e pathExpr) eval(en *env) (box.Value, error) {
	v, _ := lookup(en.rec, e.segs)
	return v, nil
}

func (e refExpr) eval(en *env) (box.Value, error) {
	if en.read == nil {
		return box.Value{}, nil
	}
	return en.read(e.ref)
}

func (e unaryMinus) eval(en *env) (box.Value, error) {
	v, err := e.x.eval(en)
	if err != nil {
		return v, err
	}
	if v.Kind != box.KindNumber {
		return box.Value{}, fmt.Errorf("cannot negate %s", v.Kind)
	}
	return box.Number(-v.Num), nil
}

func truthy(v box.Value) bool {
	switch v.Kind {
	case box.KindBool:
		return v.Bool
	case box.KindNumber:
		return v.Num != 0
	case box.KindString:
		return v.Str != ""
	case box.KindArray:
		return len(v.Arr) > 0
	case box.KindBox:
		return v.Box != nil && v.Box.Len() > 0
	case box.KindLink:
		return v.Link != nil
	}
	return false
}

func (e notExpr) eval(en *env) (box.Value, error) {
	v, err := e.x.eval(en)
	if err != nil {
		return v, err
	}
	return box.Bool(!truthy(v)), nil
}

func (e binExpr) eval(en *env) (box.Value, error) {
	switch e.op {
	case "and", "or":
		l, err := e.l.eval(en)
		if err != nil {
			return l, err
		}
		if e.op == "and" && !truthy(l) {
			return box.Bool(false), nil
		}
		if e.op == "or" && truthy(l) {
			return box.Bool(true), nil
		}
		r, err := e.r.eval(en)
		if err != nil {
			return r, err
		}
		return box.Bool(truthy(r)), nil
	}
	l, err := e.l.eval(en)
	if err != nil {
		return l, err
	}
	r, err := e.r.eval(en)
	if err != nil {
		return r, err
	}
	switch e.op {
	case "==":
		return box.Bool(l.Equal(r)), nil
	case "!=":
		return box.Bool(!l.Equal(r)), nil
	case "<", "<=", ">", ">=":
		return box.Bool(order(e.op, l, r)), nil
	case "+":
		if l.Kind == box.KindString && r.Kind == box.KindString {
			return box.String(l.Str + r.Str), nil
		}
		fallthrough
	case "-", "*", "/":
		if l.Kind != box.KindNumber || r.Kind != box.KindNumber {
			return box.Value{}, fmt.Errorf("cannot apply %q to %s and %s", e.op, l.Kind, r.Kind)
		}
		var f float64
		switch e.op {
		case "+":
			f = l.Num + r.Num
		case "-":
			f = l.Num - r.Num
		case "*":
			f = l.Num * r.Num
		case "/":
			if r.Num == 0 {
				return box.Value{}, fmt.Errorf("division by zero")
			}
			f = l.Num / r.Num
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return box.Value{}, fmt.Errorf("arithmetic overflow")
		}
		return box.Number(f), nil
	}
	return box.Value{}, fmt.Errorf("unknown operator %q", e.op)
}

// order compares numbers with numbers and strings with strings; any other
// combination (including nell) is simply false.
func order(op string, l, r box.Value) bool {
	var c int
	switch {
	case l.Kind == box.KindNumber && r.Kind == box.KindNumber:
		switch {
		case l.Num < r.Num:
			c = -1
		case l.Num > r.Num:
			c = 1
		}
	case l.Kind == box.KindString && r.Kind == box.KindString:
		c = strings.Compare(l.Str, r.Str)
	default:
		return false
	}
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

// ---- parser ---------------------------------------------------------------

type exprParser struct {
	toks     []token
	pos      int
	boxName  string // leading path segment equal to this names the record itself
	implicit Expr   // left operand for shorthand conditions like "<= 0"
}

func (p *exprParser) peek() token { return p.toks[p.pos] }
func (p *exprParser) next() token { t := p.toks[p.pos]; p.pos++; return t }
func (p *exprParser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tkOp && t.text == s
}
func (p *exprParser) isKw(s string) bool {
	t := p.peek()
	return t.kind == tkKw && t.text == s
}

func (p *exprParser) errf(format string, a ...interface{}) error {
	return fmt.Errorf("at %d: %s", p.peek().pos, fmt.Sprintf(format, a...))
}

// parseCondition compiles a condition. If implicit is non-nil the condition
// may start with a comparison operator, which then applies to implicit.
func parseCondition(src, boxName string, implicit Expr) (Expr, error) {
	toks, err := lexExpr(src)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, boxName: boxName, implicit: implicit}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		return nil, p.errf("unexpected %s", describe(p.peek()))
	}
	return e, nil
}

func describe(t token) string {
	switch t.kind {
	case tkEOF:
		return "end of expression"
	case tkOp, tkKw:
		return strconv.Quote(t.text)
	case tkNum:
		return "number"
	case tkStr:
		return "string"
	}
	return "field"
}

func (p *exprParser) or() (Expr, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isKw("or") {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = binExpr{"or", l, r}
	}
	return l, nil
}

func (p *exprParser) and() (Expr, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.isKw("and") {
		p.next()
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		l = binExpr{"and", l, r}
	}
	return l, nil
}

func (p *exprParser) not() (Expr, error) {
	if p.isKw("not") {
		p.next()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return notExpr{x}, nil
	}
	return p.cmp()
}

func isCmpOp(t token) bool {
	if t.kind != tkOp {
		return false
	}
	switch t.text {
	case "==", "=", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

func (p *exprParser) cmp() (Expr, error) {
	if p.peek().kind == tkOp && p.peek().text == "(" {
		// "(a + 1) > 2" is an arithmetic group, "(a > 1 and b)" a condition group.
		save, saveImplicit := p.pos, p.implicit
		e, err := p.cmpPlain()
		if err == nil {
			return e, nil
		}
		p.pos, p.implicit = save, saveImplicit
		p.next()
		c, err2 := p.or()
		if err2 != nil {
			return nil, err2
		}
		if !p.isOp(")") {
			return nil, p.errf("expected ')'")
		}
		p.next()
		return c, nil
	}
	return p.cmpPlain()
}

func (p *exprParser) cmpPlain() (Expr, error) {
	var l Expr
	if p.implicit != nil && isCmpOp(p.peek()) {
		l, p.implicit = p.implicit, nil
	} else {
		var err error
		l, err = p.arith()
		if err != nil {
			return nil, err
		}
	}
	if isCmpOp(p.peek()) {
		op := p.next().text
		if op == "=" {
			op = "=="
		}
		r, err := p.arith()
		if err != nil {
			return nil, err
		}
		return binExpr{op, l, r}, nil
	}
	return l, nil
}

func (p *exprParser) arith() (Expr, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = binExpr{op, l, r}
	}
	return l, nil
}

func (p *exprParser) term() (Expr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") {
		op := p.next().text
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = binExpr{op, l, r}
	}
	return l, nil
}

func (p *exprParser) unary() (Expr, error) {
	if p.isOp("-") {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return unaryMinus{x}, nil
	}
	return p.primary()
}

// localSegs strips a leading segment naming the rule's own box.
func localSegs(segs []box.Seg, boxName string) []box.Seg {
	if len(segs) > 1 && boxName != "" && segs[0].Name == boxName {
		return segs[1:]
	}
	return segs
}

func (p *exprParser) primary() (Expr, error) {
	t := p.next()
	switch t.kind {
	case tkNum:
		return litExpr{box.Number(t.num)}, nil
	case tkStr:
		return litExpr{box.String(t.str)}, nil
	case tkPath:
		return pathExpr{localSegs(t.path.Segs, p.boxName)}, nil
	case tkRef:
		return refExpr{box.Ref{File: t.path.File, Segs: t.path.Segs}}, nil
	case tkKw:
		switch t.text {
		case "true":
			return litExpr{box.Bool(true)}, nil
		case "false":
			return litExpr{box.Bool(false)}, nil
		case "nell", "null":
			return litExpr{box.Nell()}, nil
		}
	case tkOp:
		if t.text == "(" {
			e, err := p.arith()
			if err != nil {
				return nil, err
			}
			if !p.isOp(")") {
				return nil, p.errf("expected ')'")
			}
			p.next()
			return e, nil
		}
	}
	p.pos--
	return nil, p.errf("unexpected %s, expected a value", describe(t))
}
