package query

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tQIdent // "quoted identifier"
	tString // 'string literal'
	tNumber
	tParam // ?
	tPunct // ( ) , . * = != <> < <= > >= + - / [ ] ;
)

type token struct {
	kind tokKind
	text string // ident/qident/string content, punct text, number source text
	num  float64
	pos  Pos
	end  int // byte offset after the token
}

func (t token) describe() string {
	switch t.kind {
	case tEOF:
		return "end of query"
	case tIdent:
		return "'" + t.text + "'"
	case tQIdent:
		return "\"" + t.text + "\""
	case tString:
		return "string literal"
	case tNumber:
		return "number " + t.text
	case tParam:
		return "'?'"
	}
	return "'" + t.text + "'"
}

type lexer struct {
	src  string
	i    int
	line int
	col  int
	max  int
}

func (l *lexer) pos() Pos { return Pos{Offset: l.i, Line: l.line, Col: l.col} }

func (l *lexer) advance(n int) {
	for k := 0; k < n && l.i < len(l.src); {
		r, sz := utf8.DecodeRuneInString(l.src[l.i:])
		l.i += sz
		k += sz
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool  { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// lex splits src into tokens. It enforces maxTokens.
func lex(src string, maxTokens int) ([]token, error) {
	if !utf8.ValidString(src) {
		bad := 0
		for i := 0; i < len(src); {
			r, sz := utf8.DecodeRuneInString(src[i:])
			if r == utf8.RuneError && sz == 1 {
				bad = i
				break
			}
			i += sz
		}
		l := lexer{src: src, line: 1, col: 1}
		l.advance(bad)
		return nil, &SyntaxError{Pos: l.pos(), Msg: "invalid UTF-8"}
	}
	l := &lexer{src: src, line: 1, col: 1, max: maxTokens}
	var toks []token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.kind == tEOF {
			return toks, nil
		}
		if maxTokens > 0 && len(toks) > maxTokens {
			return nil, &SyntaxError{Pos: t.pos, Msg: "query has too many tokens (limit " + strconv.Itoa(maxTokens) + ")"}
		}
	}
}

func (l *lexer) next() (token, error) {
	for l.i < len(l.src) {
		c := l.src[l.i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			l.advance(1)
			continue
		}
		break
	}
	start := l.pos()
	mk := func(k tokKind, text string, n int) token {
		l.advance(n)
		return token{kind: k, text: text, pos: start, end: l.i}
	}
	if l.i >= len(l.src) {
		return token{kind: tEOF, pos: start, end: l.i}, nil
	}
	c := l.src[l.i]
	rest := l.src[l.i:]
	switch {
	case c == '\'':
		return l.str(start)
	case c == '"':
		return l.qident(start)
	case c >= '0' && c <= '9':
		return l.number(start)
	case c == '?':
		return mk(tParam, "?", 1), nil
	}
	r, _ := utf8.DecodeRuneInString(rest)
	if isIdentStart(r) {
		j := 0
		for j < len(rest) {
			r, sz := utf8.DecodeRuneInString(rest[j:])
			if !isIdentPart(r) {
				break
			}
			j += sz
		}
		if j > 256 {
			return token{}, &SyntaxError{Pos: start, Msg: "identifier longer than 256 bytes"}
		}
		return mk(tIdent, rest[:j], j), nil
	}
	switch c {
	case '(', ')', ',', '.', '*', '+', '-', '/', '[', ']', ';', '=':
		return mk(tPunct, string(c), 1), nil
	case '<':
		if strings.HasPrefix(rest, "<=") {
			return mk(tPunct, "<=", 2), nil
		}
		if strings.HasPrefix(rest, "<>") {
			return mk(tPunct, "!=", 2), nil
		}
		return mk(tPunct, "<", 1), nil
	case '>':
		if strings.HasPrefix(rest, ">=") {
			return mk(tPunct, ">=", 2), nil
		}
		return mk(tPunct, ">", 1), nil
	case '!':
		if strings.HasPrefix(rest, "!=") {
			return mk(tPunct, "!=", 2), nil
		}
	}
	msg := "unexpected character"
	if c < 0x20 || c == 0x7f {
		msg = "unexpected control character"
		return token{}, &SyntaxError{Pos: start, Msg: msg}
	}
	return token{}, &SyntaxError{Pos: start, Msg: msg, Near: string(r)}
}

func (l *lexer) str(start Pos) (token, error) {
	var sb strings.Builder
	j := l.i + 1
	for j < len(l.src) {
		c := l.src[j]
		if c == '\'' {
			if j+1 < len(l.src) && l.src[j+1] == '\'' {
				sb.WriteByte('\'')
				j += 2
				continue
			}
			l.advance(j + 1 - l.i)
			return token{kind: tString, text: sb.String(), pos: start, end: l.i}, nil
		}
		if c == 0 {
			p := l.pos()
			l.advance(j - l.i)
			p = l.pos()
			return token{}, &SyntaxError{Pos: p, Msg: "NUL byte in string literal"}
		}
		sb.WriteByte(c)
		j++
	}
	return token{}, &SyntaxError{Pos: start, Msg: "unterminated string literal (missing closing ')"}
}

func (l *lexer) qident(start Pos) (token, error) {
	var sb strings.Builder
	j := l.i + 1
	for j < len(l.src) {
		c := l.src[j]
		if c == '"' {
			if j+1 < len(l.src) && l.src[j+1] == '"' {
				sb.WriteByte('"')
				j += 2
				continue
			}
			if sb.Len() == 0 {
				return token{}, &SyntaxError{Pos: start, Msg: "empty quoted identifier"}
			}
			l.advance(j + 1 - l.i)
			return token{kind: tQIdent, text: sb.String(), pos: start, end: l.i}, nil
		}
		if c < 0x20 || c == 0x7f {
			l.advance(j - l.i)
			return token{}, &SyntaxError{Pos: l.pos(), Msg: "control character in quoted identifier"}
		}
		sb.WriteByte(c)
		j++
	}
	return token{}, &SyntaxError{Pos: start, Msg: "unterminated quoted identifier (missing closing \")"}
}

func (l *lexer) number(start Pos) (token, error) {
	s := l.src[l.i:]
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	// fraction: only when a digit follows the dot (so `1.x` is not a number)
	if j+1 < len(s) && s[j] == '.' && s[j+1] >= '0' && s[j+1] <= '9' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		if k < len(s) && s[k] >= '0' && s[k] <= '9' {
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
			}
			j = k
		}
	}
	if j < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[j:]); isIdentStart(r) {
			return token{}, &SyntaxError{Pos: start, Msg: "malformed number", Near: s[:j+1]}
		}
	}
	text := s[:j]
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return token{}, &SyntaxError{Pos: start, Msg: "number out of range", Near: text}
	}
	l.advance(j)
	return token{kind: tNumber, text: text, num: f, pos: start, end: l.i}, nil
}
