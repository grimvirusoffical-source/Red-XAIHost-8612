package code

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tNum
	tStr
	tIdent
	tOp // punctuation and operators, text in tok.s
)

type token struct {
	kind      tokKind
	s         string // identifier, operator or decoded string
	n         float64
	line, col int
}

var keywords = map[string]bool{
	"let": true, "fn": true, "if": true, "else": true, "while": true, "for": true, "in": true,
	"return": true, "break": true, "continue": true, "true": true, "false": true, "nil": true,
	"and": true, "or": true, "not": true,
}

type lexer struct {
	src       string
	pos       int
	line, col int
	script    string
}

func (lx *lexer) errf(line, col int, format string, a ...interface{}) error {
	return &SyntaxError{Script: lx.script, Line: line, Col: col, Msg: fmt.Sprintf(format, a...)}
}

func (lx *lexer) advance(n int) {
	for i := 0; i < n; i++ {
		if lx.src[lx.pos] == '\n' {
			lx.line++
			lx.col = 1
		} else if lx.src[lx.pos]&0xC0 != 0x80 {
			lx.col++
		}
		lx.pos++
	}
}

func isIdentStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }

// lex splits src into tokens. Strings are decoded. Only ASCII identifiers
// are accepted; the source must be valid UTF-8.
func lex(script, src string) ([]token, error) {
	lx := &lexer{src: src, line: 1, col: 1, script: script}
	if !utf8.ValidString(src) {
		return nil, lx.errf(1, 1, "source is not valid UTF-8")
	}
	var toks []token
	for lx.pos < len(src) {
		c := src[lx.pos]
		line, col := lx.line, lx.col
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			lx.advance(1)
		case c == '/' && lx.pos+1 < len(src) && src[lx.pos+1] == '/':
			for lx.pos < len(src) && src[lx.pos] != '\n' {
				lx.advance(1)
			}
		case isDigit(c):
			start := lx.pos
			for lx.pos < len(src) && isDigit(src[lx.pos]) {
				lx.advance(1)
			}
			if lx.pos+1 < len(src) && src[lx.pos] == '.' && isDigit(src[lx.pos+1]) {
				lx.advance(1)
				for lx.pos < len(src) && isDigit(src[lx.pos]) {
					lx.advance(1)
				}
			}
			if lx.pos < len(src) && (src[lx.pos] == 'e' || src[lx.pos] == 'E') {
				j := lx.pos + 1
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				if j < len(src) && isDigit(src[j]) {
					lx.advance(j - lx.pos)
					for lx.pos < len(src) && isDigit(src[lx.pos]) {
						lx.advance(1)
					}
				}
			}
			if lx.pos < len(src) && (isIdentStart(src[lx.pos]) || isDigit(src[lx.pos])) {
				return nil, lx.errf(line, col, "malformed number")
			}
			f, err := strconv.ParseFloat(src[start:lx.pos], 64)
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, lx.errf(line, col, "number out of range")
			}
			toks = append(toks, token{kind: tNum, n: f, line: line, col: col})
		case isIdentStart(c):
			start := lx.pos
			for lx.pos < len(src) && (isIdentStart(src[lx.pos]) || isDigit(src[lx.pos])) {
				lx.advance(1)
			}
			toks = append(toks, token{kind: tIdent, s: src[start:lx.pos], line: line, col: col})
		case c == '"':
			lx.advance(1)
			var sb strings.Builder
			closed := false
			for lx.pos < len(src) {
				ch := src[lx.pos]
				if ch == '"' {
					lx.advance(1)
					closed = true
					break
				}
				if ch == '\n' {
					break
				}
				if ch == '\\' {
					if lx.pos+1 >= len(src) {
						break
					}
					switch src[lx.pos+1] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					case 'r':
						sb.WriteByte('\r')
					case '"':
						sb.WriteByte('"')
					case '\\':
						sb.WriteByte('\\')
					case '0':
						sb.WriteByte(0)
					default:
						return nil, lx.errf(lx.line, lx.col, "unknown escape \\%c", src[lx.pos+1])
					}
					lx.advance(2)
					continue
				}
				sb.WriteByte(ch)
				lx.advance(1)
			}
			if !closed {
				return nil, lx.errf(line, col, "unterminated string")
			}
			toks = append(toks, token{kind: tStr, s: sb.String(), line: line, col: col})
		default:
			two := ""
			if lx.pos+1 < len(src) {
				two = src[lx.pos : lx.pos+2]
			}
			switch two {
			case "==", "!=", "<=", ">=", "&&", "||", "..":
				lx.advance(2)
				toks = append(toks, token{kind: tOp, s: two, line: line, col: col})
				continue
			}
			if strings.IndexByte("+-*/%<>=!(){}[],;", c) >= 0 {
				lx.advance(1)
				toks = append(toks, token{kind: tOp, s: string(c), line: line, col: col})
				continue
			}
			return nil, lx.errf(line, col, "unexpected character %q", string(rune(c)))
		}
	}
	toks = append(toks, token{kind: tEOF, line: lx.line, col: lx.col})
	return toks, nil
}
