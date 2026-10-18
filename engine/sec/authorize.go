package sec

import "fmt"

func validResource(r Resource) error {
	if r.File == "" {
		return fmt.Errorf("resource has no file")
	}
	if r.Box == "" && r.Field != "" {
		return fmt.Errorf("resource has a field but no box")
	}
	for _, s := range []string{r.File, r.Box, r.Field, r.Owner} {
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] == 0x7f {
				return fmt.Errorf("resource contains a control character")
			}
		}
	}
	return nil
}

func deny(reason string) Decision { return Decision{Allowed: false, Reason: reason} }

// effective computes the roles and scopes a principal holds under p.
func (p *Policy) effective(pr Principal) (roles, scopes map[string]bool) {
	roles = map[string]bool{}
	scopes = map[string]bool{}
	for _, s := range pr.Scopes {
		if p.scopes[s] {
			scopes[s] = true
		}
	}
	for _, rn := range pr.Roles {
		r := p.roles[rn]
		if r == nil {
			continue // unknown roles grant nothing
		}
		for _, c := range r.closure {
			roles[c] = true
			for _, s := range p.roles[c].scopes {
				scopes[s] = true
			}
		}
	}
	return roles, scopes
}

func (g *grant) appliesTo(pr Principal, roles, scopes map[string]bool) bool {
	if g.scope != "" { // scope grant
		return scopes[g.scope]
	}
	for _, w := range g.who {
		switch w.kind {
		case "any":
			return true
		case "role":
			if roles[w.name] {
				return true
			}
		case "scope":
			if scopes[w.name] {
				return true
			}
		case "user":
			if pr.ID != "" && (w.name == "*" || w.name == pr.ID) {
				return true
			}
		}
	}
	return false
}

// Authorize decides whether pr may perform act on res.
//
// The answer is deny unless something allows it. Candidates are the scope
// grants for scopes the principal holds (directly or through roles) and the
// Rule entries whose Who, Actions, Resource (and Owner) all match. Among the
// candidates the winner is, in order:
//
//  1. the highest Priority (scope grants have priority 0);
//  2. the most specific matching resource pattern (more parts named, then
//     more parts without wildcards);
//  3. deny over allow.
//
// Invalid input (unknown action, no file, control characters) is denied.
// A pattern that names a box or field does not match a request for the
// coarser level: authorize each field separately (see FilterFields).
func (p *Policy) Authorize(pr Principal, act Action, res Resource) Decision {
	if p == nil {
		return deny("no policy: default deny")
	}
	if !validAction(act) {
		return deny(fmt.Sprintf("unknown action %q", act))
	}
	if err := validResource(res); err != nil {
		return deny(err.Error())
	}
	roles, scopes := p.effective(pr)
	var best *grant
	var bestPat pattern
	for i := range p.grants {
		g := &p.grants[i]
		if !g.actions[act] && !g.actions[ActionAny] {
			continue
		}
		if !g.appliesTo(pr, roles, scopes) {
			continue
		}
		if g.owner && (pr.ID == "" || res.Owner != pr.ID) {
			continue
		}
		// the most specific matching pattern of this grant
		var pat pattern
		found := false
		for _, cand := range g.patterns {
			if cand.matches(res) && (!found || cand.moreSpecificThan(pat)) {
				pat, found = cand, true
			}
		}
		if !found {
			continue
		}
		if best == nil || beats(g, pat, best, bestPat) {
			best, bestPat = g, pat
		}
	}
	if best == nil {
		return deny("no rule allows this: default deny")
	}
	if best.allow {
		return Decision{Allowed: true, Rule: best.name, Reason: "allowed by " + best.name}
	}
	return Decision{Allowed: false, Rule: best.name, Reason: "denied by " + best.name}
}

// beats reports whether candidate a (with matched pattern ap) outranks b.
func beats(a *grant, ap pattern, b *grant, bp pattern) bool {
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	if ap.segs != bp.segs || ap.literal != bp.literal {
		return ap.moreSpecificThan(bp)
	}
	return !a.allow && b.allow // deny wins ties; otherwise keep the earlier one
}

// Check is Authorize that returns an error wrapping ErrDenied on a deny.
func (p *Policy) Check(pr Principal, act Action, res Resource) error {
	d := p.Authorize(pr, act, res)
	if d.Allowed {
		return nil
	}
	return fmt.Errorf("%w: %s on %s: %s", ErrDenied, act, res, d.Reason)
}

// FilterFields returns the fields of box that pr may act on, in input order.
// Use it before returning or changing a record so a field-level deny is
// honoured even when the box as a whole is allowed.
func (p *Policy) FilterFields(pr Principal, act Action, file, boxName, owner string, fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if p.Authorize(pr, act, Resource{File: file, Box: boxName, Field: f, Owner: owner}).Allowed {
			out = append(out, f)
		}
	}
	return out
}
