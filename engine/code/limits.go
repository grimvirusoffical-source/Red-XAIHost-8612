package code

import "time"

// Limits bound one script run (and, for the two compile-time fields, one
// compilation). A zero field means "use the default". When a script declares
// limits next to the host's ceiling, each field is the smaller of the two.
type Limits struct {
	// MaxSteps is the evaluation budget: every expression node, statement and
	// loop iteration costs one step, and built-ins that walk data cost extra.
	MaxSteps int64
	// MaxDuration is the wall-clock limit. It is checked every 64 steps and at
	// each host call; the context deadline applies as well.
	MaxDuration time.Duration
	// MaxStringLen is the longest string a script can create, in bytes.
	MaxStringLen int
	// MaxArrayLen is the longest array a script can create, in elements.
	MaxArrayLen int
	// MaxAlloc is a cumulative allocation budget (not live memory): creating
	// a string costs its length in bytes, an array 16 per element.
	MaxAlloc int64
	// MaxDepth is the maximum nesting of user-function calls.
	MaxDepth int
	// MaxHostCalls is the number of host-function calls allowed per run.
	MaxHostCalls int
	// MaxSourceBytes and MaxNesting apply when compiling.
	MaxSourceBytes int
	MaxNesting     int
}

// Defaults.
const (
	DefaultMaxSteps       = 100_000
	DefaultMaxDuration    = time.Second
	DefaultMaxStringLen   = 64 << 10
	DefaultMaxArrayLen    = 10_000
	DefaultMaxAlloc       = 8 << 20
	DefaultMaxDepth       = 32
	DefaultMaxHostCalls   = 100
	DefaultMaxSourceBytes = 64 << 10
	DefaultMaxNesting     = 64
	elemCost              = 16
)

// WithDefaults fills the zero fields with the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxSteps <= 0 {
		l.MaxSteps = DefaultMaxSteps
	}
	if l.MaxDuration <= 0 {
		l.MaxDuration = DefaultMaxDuration
	}
	if l.MaxStringLen <= 0 {
		l.MaxStringLen = DefaultMaxStringLen
	}
	if l.MaxArrayLen <= 0 {
		l.MaxArrayLen = DefaultMaxArrayLen
	}
	if l.MaxAlloc <= 0 {
		l.MaxAlloc = DefaultMaxAlloc
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxHostCalls <= 0 {
		l.MaxHostCalls = DefaultMaxHostCalls
	}
	if l.MaxSourceBytes <= 0 {
		l.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if l.MaxNesting <= 0 {
		l.MaxNesting = DefaultMaxNesting
	}
	return l
}

// Effective combines the host's ceiling with a script's own request: zero
// script fields inherit the ceiling, and no field can exceed it.
func Effective(ceiling, script Limits) Limits {
	c := ceiling.WithDefaults()
	s := script
	minI64 := func(a, b int64) int64 {
		if a <= 0 || a > b {
			return b
		}
		return a
	}
	minInt := func(a, b int) int {
		if a <= 0 || a > b {
			return b
		}
		return a
	}
	out := c
	out.MaxSteps = minI64(s.MaxSteps, c.MaxSteps)
	if s.MaxDuration > 0 && s.MaxDuration < c.MaxDuration {
		out.MaxDuration = s.MaxDuration
	}
	out.MaxStringLen = minInt(s.MaxStringLen, c.MaxStringLen)
	out.MaxArrayLen = minInt(s.MaxArrayLen, c.MaxArrayLen)
	out.MaxAlloc = minI64(s.MaxAlloc, c.MaxAlloc)
	out.MaxDepth = minInt(s.MaxDepth, c.MaxDepth)
	out.MaxHostCalls = minInt(s.MaxHostCalls, c.MaxHostCalls)
	return out
}
