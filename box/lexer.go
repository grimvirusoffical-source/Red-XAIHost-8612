package box

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Error is a syntax error with a source position.
type Error struct {
	Line, Col int
	Msg       string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d, col %d: %s", e.Line, e.Col, e.Msg) }

// Context renders the offending source line with a caret under the column.
func (e *Error) Context(src string) string {
	lines := strings.Split(src, "\n")
	if e.Line < 1 || e.Line > len(lines) {
		return e.Error()
	}
	l := strings.TrimRight(lines[e.Line-1], "\r")
	return fmt.Sprintf("%s\n  %s\n  %s^", e.Error(), l, strings.Repeat(" ", max(e.Col-1, 0)))
}

type tokKind int

const (
	tEOF tokKind = iota
	tLBrace
	tRBrace
	tLBrack
	tRBrack
	tLParen
	tRParen
	tEq
	tComma
	tSemi
	tColon
	tDot
	tStar
	tTilde
	tAt
	tArrow
	tCascade
	tIdent
	tString
	tNumber
)

func (k tokKind) String() string {
	switch k {
	case tEOF:
		return "end of input"
	case tLBrace:
		return "'{'"
	case tRBrace:
		return "'}'"
	case tLBrack:
		return "'['"
	case tRBrack:
		return "']'"
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tEq:
		return "'='"
	case tComma:
		return "','"
	case tSemi:
		return "';'"
	case tColon:
		return "':'"
	case tDot:
		return "'.'"
	case tStar:
		return "'*'"
	case tTilde:
		return "'~'"
	case tAt:
		return "'@'"
	case tArrow:
		return "'@>'"
	case tCascade:
		return "'@!>'"
	case tIdent:
		return "identifier"
	case tString:
		return "string"
	case tNumber:
		return "number"
	}
	return "token"
}

type token struct {
	kind      tokKind
	text      string
	num       float64
	line, col int
}

func (t token) describe() string {
	switch t.kind {
	case tIdent:
		return "identifier " + strconv.Quote(t.text)
	case tString:
		return "string " + strconv.Quote(t.text)
	case tNumber:
		return "number " + t.text
	}
	return t.kind.String()
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool {
	return r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func lex(src string) ([]token, error) {
	var toks []token
	line, col := 1, 1
	i := 0
	adv := func(n int) {
		for k := 0; k < n; k++ {
			if i >= len(src) {
				return
			}
			r, sz := utf8.DecodeRuneInString(src[i:])
			i += sz
			if r == '\n' {
				line++
				col = 1
			} else {
				col++
			}
		}
	}
	for i < len(src) {
		r, _ := utf8.DecodeRuneInString(src[i:])
		switch {
		case r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == '\uFEFF':
			adv(1)
			continue
		case strings.HasPrefix(src[i:], "<<"):
			sl, sc := line, col
			end := strings.Index(src[i+2:], ">>")
			if end < 0 {
				return nil, &Error{sl, sc, "unterminated comment (missing '>>')"}
			}
			adv(2 + end + 2)
			continue
		}
		t := token{line: line, col: col}
		single := map[rune]tokKind{'{': tLBrace, '}': tRBrace, '[': tLBrack, ']': tRBrack, '(': tLParen,
			')': tRParen, '=': tEq, ',': tComma, ';': tSemi, ':': tColon, '.': tDot, '*': tStar, '~': tTilde}
		if k, ok := single[r]; ok {
			t.kind = k
			toks = append(toks, t)
			adv(1)
			continue
		}
		switch {
		case r == '@':
			switch {
			case strings.HasPrefix(src[i:], "@!>"):
				t.kind = tCascade
				adv(3)
			case strings.HasPrefix(src[i:], "@>"):
				t.kind = tArrow
				adv(2)
			default:
				t.kind = tAt
				adv(1)
			}
			toks = append(toks, t)
		case r == '"':
			s, n, err := lexString(src[i:])
			if err != nil {
				// compute position of the error inside the string
				l, c := line, col
				for _, ch := range src[i : i+err.off] {
					if ch == '\n' {
						l++
						c = 1
					} else {
						c++
					}
				}
				return nil, &Error{l, c, err.msg}
			}
			t.kind, t.text = tString, s
			toks = append(toks, t)
			adv(utf8.RuneCountInString(src[i : i+n]))
		case r == '-' || (r >= '0' && r <= '9'):
			j := i
			if src[j] == '-' {
				j++
			}
			ds := j
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			if j == ds {
				return nil, &Error{line, col, "'-' must be followed by a digit"}
			}
			if j+1 < len(src) && src[j] == '.' && src[j+1] >= '0' && src[j+1] <= '9' {
				j++
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
				k := j + 1
				if k < len(src) && (src[k] == '+' || src[k] == '-') {
					k++
				}
				if k < len(src) && src[k] >= '0' && src[k] <= '9' {
					for k < len(src) && src[k] >= '0' && src[k] <= '9' {
						k++
					}
					j = k
				}
			}
			f, err := strconv.ParseFloat(src[i:j], 64)
			if err != nil {
				return nil, &Error{line, col, "invalid number " + src[i:j]}
			}
			t.kind, t.text, t.num = tNumber, src[i:j], f
			toks = append(toks, t)
			adv(j - i)
		case isIdentStart(r):
			j := i
			for j < len(src) {
				rr, s := utf8.DecodeRuneInString(src[j:])
				if !isIdentPart(rr) {
					break
				}
				j += s
			}
			t.kind, t.text = tIdent, src[i:j]
			toks = append(toks, t)
			adv(utf8.RuneCountInString(src[i:j]))
		default:
			return nil, &Error{line, col, fmt.Sprintf("unexpected character %q", r)}
		}
	}
	toks = append(toks, token{kind: tEOF, line: line, col: col})
	return toks, nil
}

type strErr struct {
	off int
	msg string
}

// lexString scans a quoted string at s[0]=='"'; returns decoded text and the
// number of bytes consumed.
func lexString(s string) (string, int, *strErr) {
	var sb strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		switch c {
		case '"':
			return sb.String(), i + 1, nil
		case '\\':
			if i+1 >= len(s) {
				return "", 0, &strErr{i, "unterminated string"}
			}
			e := s[i+1]
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
				i += 2
			case 'n':
				sb.WriteByte('\n')
				i += 2
			case 'r':
				sb.WriteByte('\r')
				i += 2
			case 't':
				sb.WriteByte('\t')
				i += 2
			case 'b':
				sb.WriteByte('\b')
				i += 2
			case 'f':
				sb.WriteByte('\f')
				i += 2
			case 'u':
				if i+6 > len(s) {
					return "", 0, &strErr{i, "truncated \\u escape"}
				}
				n, err := strconv.ParseUint(s[i+2:i+6], 16, 32)
				if err != nil {
					return "", 0, &strErr{i, "invalid \\u escape"}
				}
				r := rune(n)
				i += 6
				if utf16.IsSurrogate(r) {
					if i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
						if n2, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
							if d := utf16.DecodeRune(r, rune(n2)); d != utf8.RuneError {
								r = d
								i += 6
							}
						}
					}
					if utf16.IsSurrogate(r) {
						r = utf8.RuneError
					}
				}
				sb.WriteRune(r)
			default:
				return "", 0, &strErr{i, fmt.Sprintf("invalid escape '\\%c'", e)}
			}
		default:
			if c >= 0x80 {
				r, sz := utf8.DecodeRuneInString(s[i:])
				sb.WriteRune(r) // invalid bytes become U+FFFD
				i += sz
				continue
			}
			sb.WriteByte(c)
			i++
		}
	}
	return "", 0, &strErr{0, "unterminated string (missing closing '\"')"}
}
