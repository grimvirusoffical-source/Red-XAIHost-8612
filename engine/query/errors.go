package query

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors (use errors.Is). Authorization failures wrap sec.ErrDenied
// (see authz.go); syntax errors are *SyntaxError.
var (
	ErrSyntax       = errors.New("query: syntax error")
	ErrTooLarge     = errors.New("query: query text too large")
	ErrParams       = errors.New("query: wrong number or type of ? parameters")
	ErrLimit        = errors.New("query: limit exceeded")
	ErrNoFile       = errors.New("query: unknown file")
	ErrAmbiguous    = errors.New("query: ambiguous file name")
	ErrBadStatement = errors.New("query: statement refused")
	ErrExists       = errors.New("query: record already exists")
	ErrType         = errors.New("query: type conflict")
	ErrNoLinks      = errors.New("query: no link engine configured")
	ErrNoAuth       = errors.New("query: no authorizer configured")
)

// Pos is a position in the query text. Line and Col are 1-based; Col counts
// runes. Offset is the byte offset.
type Pos struct {
	Offset, Line, Col int
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// SyntaxError is a lexing, parsing or binding error with a position.
type SyntaxError struct {
	Pos  Pos
	Msg  string
	Near string // the offending text, may be empty
}

func (e *SyntaxError) Error() string {
	if e.Near != "" {
		return fmt.Sprintf("query: syntax error at %s: %s (near %q)", e.Pos, e.Msg, e.Near)
	}
	return fmt.Sprintf("query: syntax error at %s: %s", e.Pos, e.Msg)
}

// Is makes errors.Is(err, ErrSyntax) true.
func (e *SyntaxError) Is(t error) bool { return t == ErrSyntax }

// Context renders the offending line of src with a caret under the error.
func (e *SyntaxError) Context(src string) string {
	lines := strings.Split(src, "\n")
	if e.Pos.Line < 1 || e.Pos.Line > len(lines) {
		return ""
	}
	line := strings.TrimRight(lines[e.Pos.Line-1], "\r")
	col := e.Pos.Col - 1
	if col < 0 {
		col = 0
	}
	return line + "\n" + strings.Repeat(" ", col) + "^"
}
