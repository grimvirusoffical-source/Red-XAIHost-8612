package code

import (
	"errors"
	"fmt"
)

// Errors. Use errors.Is.
var (
	// ErrLimit is wrapped by every resource-limit error below.
	ErrLimit           = errors.New("code: limit exceeded")
	ErrStepLimit       = fmt.Errorf("%w: step budget used up", ErrLimit)
	ErrTimeout         = fmt.Errorf("%w: time limit", ErrLimit)
	ErrAllocLimit      = fmt.Errorf("%w: allocation budget used up", ErrLimit)
	ErrStringLimit     = fmt.Errorf("%w: string too long", ErrLimit)
	ErrArrayLimit      = fmt.Errorf("%w: array too long", ErrLimit)
	ErrDepthLimit      = fmt.Errorf("%w: call depth", ErrLimit)
	ErrHostCallLimit   = fmt.Errorf("%w: too many host calls", ErrLimit)
	ErrValueTooLarge   = fmt.Errorf("%w: value too large to convert", ErrLimit)
	ErrCanceled        = errors.New("code: canceled")
	ErrNotGranted      = errors.New("code: host function not granted")
	ErrBadScript       = errors.New("code: bad script")
	ErrBadFile         = errors.New("code: bad .red-xai-code file")
	ErrBadArguments    = errors.New("code: bad arguments")
	ErrHostFunc        = errors.New("code: host function failed")
	ErrInternal        = errors.New("code: internal error")
	errNumericOverflow = errors.New("numeric overflow (result is not a finite number)")
)

// SyntaxError is a compile-time error with a position (1-based).
type SyntaxError struct {
	Script    string
	Line, Col int
	Msg       string
}

func (e *SyntaxError) Error() string {
	if e.Script != "" {
		return fmt.Sprintf("code: %s:%d:%d: %s", e.Script, e.Line, e.Col, e.Msg)
	}
	return fmt.Sprintf("code: %d:%d: %s", e.Line, e.Col, e.Msg)
}
func (e *SyntaxError) Unwrap() error { return ErrBadScript }

// RuntimeError is an error raised while a script ran. Err, if set, is the
// cause (a limit error, sec.ErrDenied, a host function's error...).
type RuntimeError struct {
	Script    string
	Line, Col int
	Msg       string
	Err       error
}

func (e *RuntimeError) Error() string {
	s := fmt.Sprintf("code: %s:%d:%d: %s", e.Script, e.Line, e.Col, e.Msg)
	if e.Err != nil && e.Msg != e.Err.Error() {
		s += ": " + e.Err.Error()
	}
	return s
}
func (e *RuntimeError) Unwrap() error { return e.Err }
