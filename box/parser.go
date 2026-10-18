package box

import (
	"fmt"
	"math"
	"strings"
)

const maxDepth = 200

type parser struct {
	toks  []token
	pos   int
	depth int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) peekN(n int) token {
	if p.pos+n >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.pos+n]
}
func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}

func (p *parser) errAt(t token, format string, a ...interface{}) error {
	return &Error{t.line, t.col, fmt.Sprintf(format, a...)}
}

func (p *parser) expect(k tokKind, ctx string) (token, error) {
	t := p.peek()
	if t.kind != k {
		return t, p.errAt(t, "expected %s %s, got %s", k, ctx, t.describe())
	}
	return p.next(), nil
}

func isKw(t token, kw string) bool { return t.kind == tIdent && strings.EqualFold(t.text, kw) }

// ParseDocument parses a whole RedX file. The result is the root box whose
// entries are the top-level boxes/entries, in order.
func ParseDocument(src string) (*Box, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	root := NewBox()
	for p.peek().kind != tEOF {
		e, err := p.topItem()
		if err != nil {
			return nil, err
		}
		if err := putUnique(root, e); err != nil {
			return nil, err
		}
		if k := p.peek().kind; k == tSemi || k == tComma {
			p.next()
		} else if k != tEOF && k != tLBrace {
			t := p.peek()
			return nil, p.errAt(t, "expected ';' or end of input after top-level item, got %s", t.describe())
		}
	}
	return root, nil
}

// ParseValue parses a single value (no TTL).
func ParseValue(src string) (Value, error) {
	toks, err := lex(src)
	if err != nil {
		return Value{}, err
	}
	p := &parser{toks: toks}
	v, err := p.value()
	if err != nil {
		return Value{}, err
	}
	if t := p.peek(); t.kind != tEOF {
		return Value{}, p.errAt(t, "unexpected %s after value", t.describe())
	}
	return v, nil
}

func putUnique(b *Box, e *Entry) error {
	id, has := attrID(e.Attrs)
	if b.m != nil {
		if _, dup := b.m[EKey{e.Key, id, has}]; dup {
			return &Error{e.Line, e.Col, fmt.Sprintf("duplicate key %s", EKey{e.Key, id, has})}
		}
	}
	b.Put(e)
	return nil
}

// topItem: `{Name[attrs]= [ ... ] ttl? flag? (, K = V)* }` or an entry.
func (p *parser) topItem() (*Entry, error) {
	open, err := p.expect(tLBrace, "to start a top-level item")
	if err != nil {
		return nil, err
	}
	e, err := p.keyAndAttrs(open)
	if err != nil {
		return nil, err
	}
	switch t := p.peek(); t.kind {
	case tRBrace: // entry form
		p.next()
		return e, p.entryTail(e)
	case tEq: // declaration form
		p.next()
		lb, err := p.expect(tLBrack, "to open the box body")
		if err != nil {
			return nil, err
		}
		b, err := p.boxBody(lb)
		if err != nil {
			return nil, err
		}
		e.Value = BoxValue(b)
		if err := p.ttl(e); err != nil {
			return nil, err
		}
		if t := p.peek(); t.kind == tIdent && (isKw(t, "true") || isKw(t, "false")) {
			p.next()
			b.HasFlag, b.Flag = true, isKw(t, "true")
		}
		for p.peek().kind == tComma {
			p.next()
			k, err := p.expect(tIdent, "(metadata name)")
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tEq, "after metadata name "+k.text); err != nil {
				return nil, err
			}
			v, err := p.metaValue()
			if err != nil {
				return nil, err
			}
			b.Meta = append(b.Meta, Attr{k.text, v})
		}
		if _, err := p.expect(tRBrace, "to close the box declaration (after ']' [flag] [, Name = Value]...)"); err != nil {
			return nil, err
		}
		return e, nil
	default:
		return nil, p.errAt(t, "expected '}' (entry form) or '=' (box declaration) after %s, got %s", e.Key, t.describe())
	}
}

func (p *parser) entryTail(e *Entry) error {
	if _, err := p.expect(tEq, "after '}' of key "+e.Key); err != nil {
		return err
	}
	v, err := p.value()
	if err != nil {
		return err
	}
	e.Value = v
	return p.ttl(e)
}

// keyAndAttrs parses `Ident [ '[' attrs ']' ]` after the opening brace.
func (p *parser) keyAndAttrs(open token) (*Entry, error) {
	k, err := p.expect(tIdent, "(entry key) after '{'")
	if err != nil {
		return nil, err
	}
	e := &Entry{Key: k.text, Line: open.line, Col: open.col}
	if p.peek().kind == tLBrack {
		p.next()
		for p.peek().kind != tRBrack {
			t := p.peek()
			switch t.kind {
			case tNumber:
				p.next()
				e.Attrs = append(e.Attrs, Attr{"ID", Number(t.num)})
			case tIdent:
				p.next()
				if p.peek().kind == tEq {
					p.next()
					v, err := p.attrValue()
					if err != nil {
						return nil, err
					}
					e.Attrs = append(e.Attrs, Attr{t.text, v})
				} else {
					e.Attrs = append(e.Attrs, Attr{t.text, Bool(true)})
				}
			default:
				return nil, p.errAt(t, "expected attribute (e.g. ID=1) or ']', got %s", t.describe())
			}
			if p.peek().kind == tComma {
				p.next()
			} else if p.peek().kind != tRBrack {
				t := p.peek()
				return nil, p.errAt(t, "expected ',' or ']' in attribute list, got %s", t.describe())
			}
		}
		p.next()
	}
	return e, nil
}

// attrValue: literal, or a raw dotted path word like PlayerStatsBox.Health.
func (p *parser) attrValue() (Value, error) {
	t := p.peek()
	switch t.kind {
	case tString:
		p.next()
		return String(t.text), nil
	case tNumber:
		if n := p.peekN(1); n.kind != tDot {
			p.next()
			return Number(t.num), nil
		}
	case tIdent:
		if n := p.peekN(1); n.kind != tDot && n.kind != tLBrack {
			p.next()
			return kwOrString(t), nil
		}
	}
	// raw word: idents, numbers, dots and balanced brackets
	var sb strings.Builder
	depth := 0
	for {
		t := p.peek()
		switch t.kind {
		case tIdent, tNumber:
			sb.WriteString(t.text)
		case tDot:
			sb.WriteByte('.')
		case tEq:
			if depth == 0 {
				goto done
			}
			sb.WriteByte('=')
		case tStar:
			sb.WriteByte('*')
		case tLBrack:
			depth++
			sb.WriteByte('[')
		case tRBrack:
			if depth == 0 {
				goto done
			}
			depth--
			sb.WriteByte(']')
		default:
			goto done
		}
		p.next()
	}
done:
	if sb.Len() == 0 {
		return Value{}, p.errAt(p.peek(), "expected attribute value, got %s", p.peek().describe())
	}
	return String(sb.String()), nil
}

func kwOrString(t token) Value {
	switch strings.ToUpper(t.text) {
	case "TRUE":
		return Bool(true)
	case "FALSE":
		return Bool(false)
	case "NELL":
		return Nell()
	}
	return String(t.text)
}

// metaValue: a normal value, or a bare identifier (string).
func (p *parser) metaValue() (Value, error) {
	t := p.peek()
	if t.kind == tIdent {
		if n := p.peekN(1); n.kind != tDot && n.kind != tLBrack {
			p.next()
			return kwOrString(t), nil
		}
	}
	return p.value()
}

func (p *parser) boxBody(open token) (*Box, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		return nil, p.errAt(open, "boxes nested deeper than %d levels", maxDepth)
	}
	b := NewBox()
	for {
		t := p.peek()
		if t.kind == tRBrack {
			p.next()
			return b, nil
		}
		if t.kind != tLBrace {
			if t.kind == tEOF {
				return nil, p.errAt(open, "box opened here is never closed (missing ']')")
			}
			return nil, p.errAt(t, "expected '{' to start an entry or ']' to close the box, got %s", t.describe())
		}
		p.next()
		e, err := p.keyAndAttrs(t)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRBrace, fmt.Sprintf("to close the key of %s", e.Key)); err != nil {
			return nil, err
		}
		if err := p.entryTail(e); err != nil {
			return nil, err
		}
		if err := putUnique(b, e); err != nil {
			return nil, err
		}
		switch n := p.peek(); n.kind {
		case tComma:
			p.next()
		case tRBrack:
		default:
			return nil, p.errAt(n, "expected ',' or ']' after entry %s, got %s", e.Key, n.describe())
		}
	}
}

func (p *parser) ttl(e *Entry) error {
	if p.peek().kind != tLParen {
		return nil
	}
	open := p.next()
	sliding := false
	if p.peek().kind == tTilde {
		p.next()
		sliding = true
	}
	n, err := p.expect(tNumber, "(TTL seconds) in '(N)', '(~N)' or '(N)*'")
	if err != nil {
		return err
	}
	if n.num < 0 || n.num != math.Trunc(n.num) || n.num > math.MaxInt32 {
		return p.errAt(n, "TTL must be a non-negative whole number of seconds, got %s", n.text)
	}
	e.TTL.Seconds = int(n.num)
	if p.peek().kind == tAt {
		p.next()
		a, err := p.expect(tNumber, "(absolute unix-ms) after '@'")
		if err != nil {
			return err
		}
		e.TTL.SetExpiresAt(int64(a.num))
	}
	if _, err := p.expect(tRParen, "to close the TTL marker"); err != nil {
		return err
	}
	if p.peek().kind == tStar {
		if sliding {
			return p.errAt(open, "a checkpoint marker '(N)*' cannot be sliding")
		}
		p.next()
		e.TTL.Kind = TTLCheckpoint
		return nil
	}
	if sliding {
		e.TTL.Kind = TTLSliding
	} else {
		e.TTL.Kind = TTLFixed
	}
	return nil
}

func (p *parser) value() (Value, error) {
	t := p.peek()
	switch t.kind {
	case tString:
		if p.peekN(1).kind == tColon && p.peekN(2).kind == tIdent {
			return p.link()
		}
		p.next()
		return String(t.text), nil
	case tNumber:
		p.next()
		return Number(t.num), nil
	case tIdent:
		switch {
		case isKw(t, "true"):
			p.next()
			return Bool(true), nil
		case isKw(t, "false"):
			p.next()
			return Bool(false), nil
		case isKw(t, "nell"):
			p.next()
			return Nell(), nil
		}
		return Value{}, p.errAt(t, "unexpected identifier %q in value position (strings must be quoted; keywords are TRUE, FALSE, NELL)", t.text)
	case tSemi:
		if p.peekN(1).kind == tLBrack {
			p.next()
			open := p.next()
			return p.array(open)
		}
		return Value{}, p.errAt(t, "unexpected ';' in value position (arrays are written ;[1, 2]: )")
	case tLBrack:
		p.next()
		b, err := p.boxBody(t)
		if err != nil {
			return Value{}, err
		}
		return BoxValue(b), nil
	}
	return Value{}, p.errAt(t, "expected a value (string, number, TRUE, FALSE, NELL, ;[array]:, [box] or link), got %s", t.describe())
}

func (p *parser) array(open token) (Value, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		return Value{}, p.errAt(open, "arrays nested deeper than %d levels", maxDepth)
	}
	arr := []Value{}
	for {
		if p.peek().kind == tRBrack {
			p.next()
			break
		}
		if p.peek().kind == tEOF {
			return Value{}, p.errAt(open, "array opened here is never closed (missing ']:')")
		}
		v, err := p.value()
		if err != nil {
			return Value{}, err
		}
		arr = append(arr, v)
		switch n := p.peek(); n.kind {
		case tComma:
			p.next()
		case tRBrack:
		default:
			return Value{}, p.errAt(n, "expected ',' or ']:' in array, got %s", n.describe())
		}
	}
	if _, err := p.expect(tColon, "after ']' (arrays end with ']:')"); err != nil {
		return Value{}, err
	}
	return Array(arr...), nil
}

func (p *parser) ref() (Ref, error) {
	f, err := p.expect(tString, "(file name) to start a reference")
	if err != nil {
		return Ref{}, err
	}
	if _, err := p.expect(tColon, "after file name in reference"); err != nil {
		return Ref{}, err
	}
	var r Ref
	r.File = f.text
	for {
		k, err := p.expect(tIdent, "(box/key name) in reference")
		if err != nil {
			return r, err
		}
		seg := Seg{Name: k.text}
		if p.peek().kind == tLBrack {
			p.next()
			for p.peek().kind != tRBrack {
				t := p.next()
				switch {
				case t.kind == tNumber:
					seg.ID, seg.HasID = int64(t.num), true
				case t.kind == tIdent && strings.EqualFold(t.text, "id"):
					if _, err := p.expect(tEq, "after ID"); err != nil {
						return r, err
					}
					switch n := p.next(); n.kind {
					case tNumber:
						seg.ID, seg.HasID = int64(n.num), true
					case tStar:
						seg.AnyID = true
					default:
						return r, p.errAt(n, "expected a number after ID=, got %s", n.describe())
					}
				case t.kind == tComma:
				default:
					return r, p.errAt(t, "unsupported selector in reference: %s", t.describe())
				}
			}
			p.next()
		}
		r.Segs = append(r.Segs, seg)
		if p.peek().kind == tDot {
			p.next()
			continue
		}
		return r, nil
	}
}

func (p *parser) link() (Value, error) {
	from, err := p.ref()
	if err != nil {
		return Value{}, err
	}
	l := &Link{From: from}
	switch t := p.peek(); t.kind {
	case tArrow, tCascade:
		p.next()
		to, err := p.ref()
		if err != nil {
			return Value{}, err
		}
		l.To = &to
		if t.kind == tArrow {
			l.Op = "@>"
		} else {
			l.Op = "@!>"
		}
	}
	return LinkValue(l), nil
}
