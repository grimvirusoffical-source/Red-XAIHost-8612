package sec

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"redx/box"
)

// Action is something a principal wants to do to a resource.
type Action string

// Actions. ActionAny ("*") is only meaningful inside rules.
const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionDelete Action = "delete"
	ActionExec   Action = "exec" // call a capability (for example a host function)
	ActionAny    Action = "*"
)

func validAction(a Action) bool {
	return a == ActionRead || a == ActionWrite || a == ActionDelete || a == ActionExec
}

// Principal is who is asking. ID is empty for an anonymous caller. Roles are
// role names declared in the policy (unknown names are ignored). Scopes are
// scope names held directly, typically copied from an API key.
type Principal struct {
	ID     string
	Roles  []string
	Scopes []string
}

// Resource is what is being touched: a file, optionally a box in it,
// optionally a field path in that box. Owner is the principal id that owns
// the record (empty if unknown); it is only used by rules with Owner = TRUE.
type Resource struct {
	File  string
	Box   string
	Field string
	Owner string
}

func (r Resource) String() string {
	s := r.File
	if r.Box != "" {
		s += ":" + r.Box
		if r.Field != "" {
			s += "." + r.Field
		}
	}
	return s
}

// Decision is the answer of Authorize.
type Decision struct {
	Allowed bool
	// Rule names the deciding rule or scope grant ("Rule[ID=3]",
	// "Scope[ID=1]:players.read"), or "" for the default deny.
	Rule   string
	Reason string
}

// Limits on a policy document.
const (
	MaxPolicyBytes   = 4 << 20
	MaxPolicyEntries = 10000
	maxPatternLen    = 256
	maxNameLen       = 64
	MaxPriority      = 1000
)

type pattern struct {
	text  string
	file  string
	box   string
	field string
	// specificity: more segments first, then more literal segments.
	segs, literal int
}

func parsePattern(s string) (pattern, error) {
	if s == "" || len(s) > maxPatternLen {
		return pattern{}, fmt.Errorf("resource pattern %q: empty or longer than %d bytes", s, maxPatternLen)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return pattern{}, fmt.Errorf("resource pattern %q: control character", s)
		}
	}
	p := pattern{text: s}
	rest := s
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		p.file, rest = rest[:i], rest[i+1:]
		if rest == "" {
			return pattern{}, fmt.Errorf("resource pattern %q: empty box part", s)
		}
		if j := strings.IndexByte(rest, '.'); j >= 0 {
			p.box, p.field = rest[:j], rest[j+1:]
			if p.field == "" {
				return pattern{}, fmt.Errorf("resource pattern %q: empty field part", s)
			}
		} else {
			p.box = rest
		}
	} else {
		p.file = rest
	}
	if p.file == "" || (strings.Contains(s, ":") && p.box == "") {
		return pattern{}, fmt.Errorf("resource pattern %q: empty segment", s)
	}
	p.segs = 1
	if !hasWild(p.file) {
		p.literal++
	}
	if p.box != "" {
		p.segs++
		if !hasWild(p.box) {
			p.literal++
		}
	}
	if p.field != "" {
		p.segs++
		if !hasWild(p.field) {
			p.literal++
		}
	}
	return p, nil
}

// matches reports whether the pattern covers r. A pattern naming fewer parts
// than the resource covers everything below that level; a pattern naming
// more parts than the resource does not match it.
func (p pattern) matches(r Resource) bool {
	if !glob(p.file, r.File) {
		return false
	}
	if p.box != "" && (r.Box == "" || !glob(p.box, r.Box)) {
		return false
	}
	if p.field != "" && (r.Field == "" || !glob(p.field, r.Field)) {
		return false
	}
	return true
}

func (p pattern) moreSpecificThan(o pattern) bool {
	if p.segs != o.segs {
		return p.segs > o.segs
	}
	return p.literal > o.literal
}

type subject struct {
	kind string // "any", "role", "scope", "user"
	name string
}

type grant struct {
	name     string // for Decision.Rule
	allow    bool
	priority int
	who      []subject // nil for scope grants
	scope    string    // scope grants: the scope name
	actions  map[Action]bool
	patterns []pattern
	owner    bool
}

type role struct {
	name     string
	scopes   []string
	inherits []string
	// closure, filled after validation: this role plus all inherited roles
	closure []string
}

type encRule struct {
	patterns []string
	keyID    string
}

// Policy is a parsed .red-xai-sec document. It is immutable and safe for
// concurrent use. A nil *Policy denies everything.
type Policy struct {
	roles  map[string]*role
	scopes map[string]bool
	grants []grant // scope grants first (in document order), then rules
	enc    []encRule
}

func validName(s string) bool {
	if s == "" || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.' || c == ':'
		if !ok {
			return false
		}
	}
	return true
}

func badf(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrBadPolicy, fmt.Sprintf(format, a...))
}

func entryName(e *box.Entry, n int) string {
	if id, ok := e.ID(); ok {
		return fmt.Sprintf("%s[ID=%d]", e.Key, id)
	}
	return fmt.Sprintf("%s#%d", e.Key, n)
}

// field helpers over a declaration box. known tracks the keys seen so that
// unknown keys (typos) can be rejected: a security file must fail closed.
type decl struct {
	b     *box.Box
	where string
	seen  map[string]bool
}

func newDecl(e *box.Entry, where string) (*decl, error) {
	if e.Value.Kind != box.KindBox || e.Value.Box == nil {
		return nil, badf("%s: must be a box", where)
	}
	return &decl{b: e.Value.Box, where: where, seen: map[string]bool{}}, nil
}

func (d *decl) get(name string) (*box.Entry, error) {
	d.seen[name] = true
	var found *box.Entry
	for _, e := range d.b.Entries() {
		if e.Key == name {
			if found != nil {
				return nil, badf("%s: duplicate %s", d.where, name)
			}
			found = e
		}
	}
	return found, nil
}

func (d *decl) str(name string, required bool) (string, bool, error) {
	e, err := d.get(name)
	if err != nil {
		return "", false, err
	}
	if e == nil {
		if required {
			return "", false, badf("%s: missing %s", d.where, name)
		}
		return "", false, nil
	}
	if e.Value.Kind != box.KindString {
		return "", false, badf("%s: %s must be a string", d.where, name)
	}
	return e.Value.Str, true, nil
}

func (d *decl) strs(name string, required bool) ([]string, error) {
	e, err := d.get(name)
	if err != nil {
		return nil, err
	}
	if e == nil {
		if required {
			return nil, badf("%s: missing %s", d.where, name)
		}
		return nil, nil
	}
	var out []string
	switch e.Value.Kind {
	case box.KindString:
		out = []string{e.Value.Str}
	case box.KindArray:
		for _, x := range e.Value.Arr {
			if x.Kind != box.KindString {
				return nil, badf("%s: %s must contain only strings", d.where, name)
			}
			out = append(out, x.Str)
		}
	default:
		return nil, badf("%s: %s must be a string or an array of strings", d.where, name)
	}
	if required && len(out) == 0 {
		return nil, badf("%s: %s is empty", d.where, name)
	}
	return out, nil
}

func (d *decl) boolean(name string) (bool, error) {
	e, err := d.get(name)
	if err != nil || e == nil {
		return false, err
	}
	if e.Value.Kind != box.KindBool {
		return false, badf("%s: %s must be TRUE or FALSE", d.where, name)
	}
	return e.Value.Bool, nil
}

func (d *decl) integer(name string, lo, hi int) (int, error) {
	e, err := d.get(name)
	if err != nil || e == nil {
		return 0, err
	}
	f := e.Value.Num
	if e.Value.Kind != box.KindNumber || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		return 0, badf("%s: %s must be an integer in %d..%d", d.where, name, lo, hi)
	}
	return int(f), nil
}

// done rejects keys that were never asked for.
func (d *decl) done() error {
	for _, e := range d.b.Entries() {
		if !d.seen[e.Key] {
			return badf("%s: unknown entry %q", d.where, e.Key)
		}
	}
	return nil
}

// Parse parses a .red-xai-sec document.
//
// Top-level entries (names are case-sensitive; any other entry is an error,
// as is any unknown key inside one):
//
//	{Role[1]}    = [ {Name[]} = "mod", {Scopes[]} = ;["chat.write"]:, {Inherits[]} = ;["player"]: ];
//	{Scope[1]}   = [ {Name[]} = "chat.write", {Actions[]} = ;["read","write"]:, {Resource[]} = "chat.red-xai-db-qs" ];
//	{Rule[1]}    = [ {Effect[]} = "deny", {Who[]} = ;["role:guest"]:, {Actions[]} = "*",
//	                 {Resource[]} = "players.red-xai.db:Player.Email", {Priority[]} = 0, {Owner[]} = FALSE ];
//	{Encrypt[1]} = [ {File[]} = "vault*", {Key[]} = "main-2026" ];
func Parse(src string) (*Policy, error) {
	if len(src) > MaxPolicyBytes {
		return nil, badf("document larger than %d bytes", MaxPolicyBytes)
	}
	root, err := box.ParseDocument(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPolicy, err)
	}
	return FromBox(root)
}

// FromBox builds a Policy from a parsed document.
func FromBox(root *box.Box) (*Policy, error) {
	p := &Policy{roles: map[string]*role{}, scopes: map[string]bool{}}
	ents := root.Entries()
	if len(ents) > MaxPolicyEntries {
		return nil, badf("more than %d entries", MaxPolicyEntries)
	}
	var scopeGrants, rules []grant
	var roleOrder []string
	for n, e := range ents {
		where := entryName(e, n)
		switch e.Key {
		case "Role":
			d, err := newDecl(e, where)
			if err != nil {
				return nil, err
			}
			name, _, err := d.str("Name", true)
			if err != nil {
				return nil, err
			}
			if !validName(name) {
				return nil, badf("%s: invalid role name %q", where, name)
			}
			if p.roles[name] != nil {
				return nil, badf("%s: duplicate role %q", where, name)
			}
			scopes, err := d.strs("Scopes", false)
			if err != nil {
				return nil, err
			}
			inh, err := d.strs("Inherits", false)
			if err != nil {
				return nil, err
			}
			if err := d.done(); err != nil {
				return nil, err
			}
			p.roles[name] = &role{name: name, scopes: scopes, inherits: inh}
			roleOrder = append(roleOrder, name)
		case "Scope":
			d, err := newDecl(e, where)
			if err != nil {
				return nil, err
			}
			name, _, err := d.str("Name", true)
			if err != nil {
				return nil, err
			}
			if !validName(name) {
				return nil, badf("%s: invalid scope name %q", where, name)
			}
			if p.scopes[name] {
				return nil, badf("%s: duplicate scope %q", where, name)
			}
			p.scopes[name] = true
			acts, err := parseActions(d, where)
			if err != nil {
				return nil, err
			}
			pats, err := parsePatterns(d, where)
			if err != nil {
				return nil, err
			}
			if err := d.done(); err != nil {
				return nil, err
			}
			scopeGrants = append(scopeGrants, grant{name: where + ":" + name, allow: true, scope: name,
				actions: acts, patterns: pats})
		case "Rule":
			d, err := newDecl(e, where)
			if err != nil {
				return nil, err
			}
			eff, _, err := d.str("Effect", true)
			if err != nil {
				return nil, err
			}
			if eff != "allow" && eff != "deny" {
				return nil, badf("%s: Effect must be \"allow\" or \"deny\"", where)
			}
			whoS, err := d.strs("Who", true)
			if err != nil {
				return nil, err
			}
			var who []subject
			for _, w := range whoS {
				s, err := parseSubject(w)
				if err != nil {
					return nil, badf("%s: %v", where, err)
				}
				who = append(who, s)
			}
			acts, err := parseActions(d, where)
			if err != nil {
				return nil, err
			}
			pats, err := parsePatterns(d, where)
			if err != nil {
				return nil, err
			}
			prio, err := d.integer("Priority", -MaxPriority, MaxPriority)
			if err != nil {
				return nil, err
			}
			owner, err := d.boolean("Owner")
			if err != nil {
				return nil, err
			}
			if err := d.done(); err != nil {
				return nil, err
			}
			rules = append(rules, grant{name: where, allow: eff == "allow", priority: prio, who: who,
				actions: acts, patterns: pats, owner: owner})
		case "Encrypt":
			d, err := newDecl(e, where)
			if err != nil {
				return nil, err
			}
			files, err := d.strs("File", true)
			if err != nil {
				return nil, err
			}
			for _, f := range files {
				if f == "" || len(f) > maxPatternLen {
					return nil, badf("%s: bad File pattern", where)
				}
			}
			key, _, err := d.str("Key", true)
			if err != nil {
				return nil, err
			}
			if !validKeyID(key) {
				return nil, badf("%s: invalid key id %q", where, key)
			}
			if err := d.done(); err != nil {
				return nil, err
			}
			p.enc = append(p.enc, encRule{patterns: files, keyID: key})
		default:
			return nil, badf("unknown top-level entry %q", e.Key)
		}
	}
	// scope names granted by roles and rules must exist; roles must exist; no cycles
	for _, rn := range roleOrder {
		r := p.roles[rn]
		for _, s := range r.scopes {
			if !p.scopes[s] {
				return nil, badf("role %q grants undeclared scope %q", rn, s)
			}
		}
		for _, in := range r.inherits {
			if p.roles[in] == nil {
				return nil, badf("role %q inherits undeclared role %q", rn, in)
			}
		}
	}
	for _, rn := range roleOrder {
		cl, err := p.closure(rn)
		if err != nil {
			return nil, err
		}
		p.roles[rn].closure = cl
	}
	for _, g := range rules {
		for _, w := range g.who {
			switch w.kind {
			case "role":
				if p.roles[w.name] == nil {
					return nil, badf("%s: undeclared role %q", g.name, w.name)
				}
			case "scope":
				if !p.scopes[w.name] {
					return nil, badf("%s: undeclared scope %q", g.name, w.name)
				}
			}
		}
	}
	p.grants = append(append(p.grants, scopeGrants...), rules...)
	return p, nil
}

// closure returns role plus everything it inherits, detecting cycles.
func (p *Policy) closure(start string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var visit func(name string, stack []string) error
	visit = func(name string, stack []string) error {
		for _, s := range stack {
			if s == name {
				return badf("role inheritance cycle: %s -> %s", strings.Join(stack, " -> "), name)
			}
		}
		if seen[name] {
			return nil
		}
		seen[name] = true
		out = append(out, name)
		for _, in := range p.roles[name].inherits {
			if err := visit(in, append(stack, name)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(start, nil); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func parseSubject(s string) (subject, error) {
	if s == "*" {
		return subject{kind: "any"}, nil
	}
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return subject{}, fmt.Errorf("bad Who %q (want *, role:NAME, scope:NAME or user:ID)", s)
	}
	kind, name := s[:i], s[i+1:]
	switch kind {
	case "role", "scope":
		if !validName(name) {
			return subject{}, fmt.Errorf("bad name in Who %q", s)
		}
	case "user":
		if len(name) > 128 {
			return subject{}, fmt.Errorf("user id too long in Who")
		}
	default:
		return subject{}, fmt.Errorf("bad Who %q (want *, role:NAME, scope:NAME or user:ID)", s)
	}
	return subject{kind: kind, name: name}, nil
}

func parseActions(d *decl, where string) (map[Action]bool, error) {
	list, err := d.strs("Actions", true)
	if err != nil {
		return nil, err
	}
	out := map[Action]bool{}
	for _, a := range list {
		act := Action(a)
		if act != ActionAny && !validAction(act) {
			return nil, badf("%s: unknown action %q", where, a)
		}
		out[act] = true
	}
	return out, nil
}

func parsePatterns(d *decl, where string) ([]pattern, error) {
	list, err := d.strs("Resource", true)
	if err != nil {
		return nil, err
	}
	var out []pattern
	for _, s := range list {
		pt, err := parsePattern(s)
		if err != nil {
			return nil, badf("%s: %v", where, err)
		}
		out = append(out, pt)
	}
	return out, nil
}

// Roles returns the declared role names, sorted.
func (p *Policy) Roles() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.roles))
	for n := range p.roles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Scopes returns the declared scope names, sorted.
func (p *Policy) Scopes() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.scopes))
	for n := range p.scopes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// EncryptionFor reports whether the policy requires files named file to be
// encrypted, and with which master key id. The first matching Encrypt entry
// in document order wins. The policy only names key ids; it never holds keys.
func (p *Policy) EncryptionFor(file string) (keyID string, required bool) {
	if p == nil {
		return "", false
	}
	for _, er := range p.enc {
		for _, pat := range er.patterns {
			if glob(pat, file) {
				return er.keyID, true
			}
		}
	}
	return "", false
}
