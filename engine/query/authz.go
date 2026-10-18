package query

import (
	"fmt"

	"redx/engine/sec"
)

// Authorizer decides whether a principal may act on a resource. *sec.Policy
// implements it (and denies everything when nil).
type Authorizer interface {
	Authorize(pr sec.Principal, act sec.Action, res sec.Resource) sec.Decision
}

var _ Authorizer = (*sec.Policy)(nil)

// AllowAll is an Authorizer that allows everything. It is meant for tests
// and for embedding the engine in a process that already trusts its caller;
// never hand it to code that serves other principals.
type AllowAll struct{}

// Authorize implements Authorizer.
func (AllowAll) Authorize(sec.Principal, sec.Action, sec.Resource) sec.Decision {
	return sec.Decision{Allowed: true, Rule: "AllowAll", Reason: "allowed"}
}

type authKey struct {
	act sec.Action
	res sec.Resource
}

// authz is a per-statement wrapper that caches decisions (the policy is
// pure, so a decision cannot change while one statement runs).
type authz struct {
	a      Authorizer
	pr     sec.Principal
	cache  map[authKey]sec.Decision
	checks int // Authorize calls made (cache misses)
}

func newAuthz(a Authorizer, pr sec.Principal) *authz {
	return &authz{a: a, pr: pr, cache: map[authKey]sec.Decision{}}
}

func (z *authz) decide(act sec.Action, res sec.Resource) sec.Decision {
	k := authKey{act, res}
	if d, ok := z.cache[k]; ok {
		return d
	}
	d := z.a.Authorize(z.pr, act, res)
	z.checks++
	if len(z.cache) >= 100000 {
		z.cache = map[authKey]sec.Decision{}
	}
	z.cache[k] = d
	return d
}

func (z *authz) ok(act sec.Action, res sec.Resource) bool { return z.decide(act, res).Allowed }

func (z *authz) check(act sec.Action, res sec.Resource) error {
	d := z.decide(act, res)
	if d.Allowed {
		return nil
	}
	return fmt.Errorf("%w: %s on %s: %s", sec.ErrDenied, act, res, d.Reason)
}

// pathAllowed reports whether act on a field path is allowed. names are the
// cumulative dotted names ("Stats", "Stats.Level"). The full path must be
// allowed, and no shorter prefix may be explicitly denied by a rule (so a
// deny on "Secret" also covers "Secret.Inner"). A prefix that is merely not
// allowed does not block, so a grant on "Stats.*" works.
func (z *authz) pathAllowed(act sec.Action, file, boxName, owner string, names []string) bool {
	for i, n := range names {
		d := z.decide(act, sec.Resource{File: file, Box: boxName, Field: n, Owner: owner})
		if i == len(names)-1 {
			return d.Allowed
		}
		if !d.Allowed && d.Rule != "" {
			return false
		}
	}
	return true
}
