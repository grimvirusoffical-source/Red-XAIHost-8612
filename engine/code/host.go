package code

import (
	"context"
	"fmt"
	"sort"

	"redx/engine/sec"
)

// Authorizer decides whether a principal may do something. *sec.Policy
// implements it. A nil Authorizer denies every host call.
type Authorizer interface {
	Authorize(pr sec.Principal, act sec.Action, res sec.Resource) sec.Decision
}

// HostFunc is a capability the embedding program offers to scripts. It is
// ordinary Go code, trusted, and is the only way a script can touch anything
// outside the interpreter.
type HostFunc struct {
	// Name is how scripts call it; a plain identifier that is not a built-in.
	Name string
	// Action is what the call does for authorization purposes: sec.ActionRead,
	// ActionWrite, ActionDelete or ActionExec (the default).
	Action sec.Action
	// Resource names what the call touches, given the converted arguments (nil,
	// bool, float64, string, []any). Nil means {File: "host", Box: Name}.
	// Returning an error refuses the call.
	Resource func(args []interface{}) (sec.Resource, error)
	// Fn does the work. It receives a context that expires with the run's
	// time limit and must honour it. Arguments are copies; it may return nil,
	// bool, numbers, string, or slices of those.
	Fn func(ctx context.Context, args []interface{}) (interface{}, error)
}

// Host is a set of host functions. It is immutable once built and safe for
// concurrent use.
type Host struct{ fns map[string]*HostFunc }

// NewHost validates and collects host functions.
func NewHost(fns ...HostFunc) (*Host, error) {
	h := &Host{fns: map[string]*HostFunc{}}
	for i := range fns {
		f := fns[i]
		if !validIdent(f.Name) || builtins[f.Name] != nil {
			return nil, fmt.Errorf("code: bad host function name %q", f.Name)
		}
		if _, dup := h.fns[f.Name]; dup {
			return nil, fmt.Errorf("code: duplicate host function %q", f.Name)
		}
		if f.Fn == nil {
			return nil, fmt.Errorf("code: host function %q has no Fn", f.Name)
		}
		if f.Action == "" {
			f.Action = sec.ActionExec
		}
		switch f.Action {
		case sec.ActionRead, sec.ActionWrite, sec.ActionDelete, sec.ActionExec:
		default:
			return nil, fmt.Errorf("code: host function %q has bad action %q", f.Name, f.Action)
		}
		h.fns[f.Name] = &f
	}
	return h, nil
}

// Names lists the registered functions, sorted.
func (h *Host) Names() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0, len(h.fns))
	for n := range h.fns {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FixedResource returns a Resource function that always names the same resource.
func FixedResource(file, boxName, field string) func([]interface{}) (sec.Resource, error) {
	r := sec.Resource{File: file, Box: boxName, Field: field}
	return func([]interface{}) (sec.Resource, error) { return r, nil }
}

// CallRecord is one host call a script attempted, for auditing.
type CallRecord struct {
	Name    string
	Allowed bool
	Reason  string
}

const maxCallRecords = 256
