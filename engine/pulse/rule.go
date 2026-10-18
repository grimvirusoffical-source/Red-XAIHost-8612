package pulse

import (
	"fmt"
	"math"
	"strings"
	"time"

	"redx/box"
	"redx/engine/link"
)

// Action is one step a rule performs. Concrete types: SetAction, EmitAction,
// TimerAction, CancelAction.
type Action interface{ actionText() string }

// SetAction assigns a value to a field of the triggering record
// (Target.File == "") or of another record (Target is a full reference).
type SetAction struct {
	Local  []box.Seg // field path inside the triggering record
	Remote *box.Ref  // or: "file":Box[ID=n].Field.Path
	Value  Expr
	text   string
}

// EmitAction publishes an event with the given topic and a snapshot of the record.
type EmitAction struct{ Topic string }

// TimerAction starts (or restarts) a named timer for the triggering record;
// when it fires, Actions run.
type TimerAction struct {
	Name     string
	Duration time.Duration
	Actions  []Action
}

// CancelAction stops a named timer of the triggering record.
type CancelAction struct{ Name string }

func (a SetAction) actionText() string    { return "set " + a.text }
func (a EmitAction) actionText() string   { return "emit " + a.Topic }
func (a TimerAction) actionText() string  { return "timer " + a.Name }
func (a CancelAction) actionText() string { return "cancel " + a.Name }

// Rule is one compiled pulse rule.
type Rule struct {
	Name string // box key as written, e.g. PlayerDeath or Tick[ID=2]
	File string // "" matches any file
	Box  string // box name the rule watches
	// ID pins the rule to one record when HasID is set.
	ID    int64
	HasID bool
	// OnField is the field named by ON=, the implicit left side of shorthand conditions.
	OnField []box.Seg
	// Level selects level triggering (fire on every matching write whose
	// watched fields changed) instead of edge triggering (default: fire when
	// the condition turns from false to true).
	Level   bool
	Cond    Expr
	Actions []Action
	watch   [][]box.Seg
	Line    int
}

func (r *Rule) matches(a link.Addr) bool {
	if r.File != "" && r.File != a.File {
		return false
	}
	if r.Box != a.Box {
		return false
	}
	if r.HasID && !(a.HasID && a.ID == r.ID) {
		return false
	}
	return true
}

// ParseRules parses the text of a .red-xai-db-pulse file.
func ParseRules(src string) ([]*Rule, error) {
	root, err := box.ParseDocument(src)
	if err != nil {
		return nil, err
	}
	var rules []*Rule
	for _, e := range root.Entries() {
		r, err := compileRule(e)
		if err != nil {
			return nil, fmt.Errorf("pulse: rule %s (line %d): %w", e.EKey(), e.Line, err)
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func attr(e *box.Entry, name string) (box.Value, bool) {
	for _, a := range e.Attrs {
		if strings.EqualFold(a.Name, name) {
			return a.Value, true
		}
	}
	return box.Value{}, false
}

func compileRule(e *box.Entry) (*Rule, error) {
	if e.Value.Kind != box.KindBox || e.Value.Box == nil {
		return nil, fmt.Errorf("a rule must be a box: {Name[ON=...]} = [ ... ]")
	}
	on, ok := attr(e, "ON")
	if !ok || on.Kind != box.KindString || on.Str == "" {
		return nil, fmt.Errorf(`missing ON attribute, e.g. [ON="players.red-xai.db:Player.Health"]`)
	}
	p, err := box.ParsePath(on.Str)
	if err != nil {
		return nil, fmt.Errorf("ON: %v", err)
	}
	if len(p.Segs) == 0 || p.Segs[0].Wild || p.Segs[0].Name == "" {
		return nil, fmt.Errorf("ON must start with a box name")
	}
	for _, s := range p.Segs[1:] {
		if s.Wild || s.AnyID {
			return nil, fmt.Errorf("ON: wildcards are only allowed on the box ID")
		}
	}
	r := &Rule{Name: e.EKey().String(), File: p.File, Box: p.Segs[0].Name, Line: e.Line,
		OnField: p.Segs[1:]}
	if s := p.Segs[0]; s.HasID {
		r.ID, r.HasID = s.ID, true
	}
	var implicit Expr
	if len(r.OnField) > 0 {
		implicit = pathExpr{r.OnField}
	}
	var when *string
	for _, c := range e.Value.Box.Entries() {
		if strings.EqualFold(c.Key, "when") {
			if c.Value.Kind != box.KindString {
				return nil, fmt.Errorf("When must be a string")
			}
			s := c.Value.Str
			when = &s
		}
	}
	if when == nil {
		return nil, fmt.Errorf("missing When condition")
	}
	cond, err := parseCondition(*when, r.Box, implicit)
	if err != nil {
		return nil, fmt.Errorf("When %q: %v", *when, err)
	}
	r.Cond = cond
	var paths [][]box.Seg
	cond.locals(&paths)
	r.watch = paths
	for _, c := range e.Value.Box.Entries() {
		if strings.EqualFold(c.Key, "trigger") || strings.EqualFold(c.Key, "mode") {
			if c.Value.Kind != box.KindString {
				return nil, fmt.Errorf("%s must be \"edge\" or \"level\"", c.Key)
			}
			switch strings.ToLower(c.Value.Str) {
			case "edge":
			case "level":
				r.Level = true
			default:
				return nil, fmt.Errorf("%s must be \"edge\" or \"level\", got %q", c.Key, c.Value.Str)
			}
		}
	}
	acts, err := compileActions(e.Value.Box, r.Box, true)
	if err != nil {
		return nil, err
	}
	if len(acts) == 0 {
		return nil, fmt.Errorf("rule has no actions (SetTarget, Do, Emit, Timer, CancelTimer)")
	}
	r.Actions = acts
	return r, nil
}

// compileActions turns the action entries of a rule or timer box into
// actions, in the order they are written. In a rule, the keys When and
// Trigger/Mode are skipped; in a timer, Name and Seconds are.
func compileActions(b *box.Box, boxName string, isRule bool) ([]Action, error) {
	var out []Action
	for _, c := range b.Entries() {
		key := strings.ToLower(c.Key)
		switch key {
		case "when", "trigger", "mode":
			if !isRule {
				return nil, fmt.Errorf("%s is not allowed inside a Timer", c.Key)
			}
			continue
		case "name", "seconds":
			if isRule {
				return nil, fmt.Errorf("unknown key %s", c.Key)
			}
			continue
		}
		strs, err := stringsOf(c)
		switch key {
		case "settarget", "set":
			if err != nil {
				return nil, fmt.Errorf("%s: %v", c.Key, err)
			}
			for _, s := range strs {
				a, err := parseSet(s, boxName)
				if err != nil {
					return nil, fmt.Errorf("%s %q: %v", c.Key, s, err)
				}
				out = append(out, a)
			}
		case "emit":
			if err != nil {
				return nil, fmt.Errorf("%s: %v", c.Key, err)
			}
			for _, s := range strs {
				s = strings.TrimSpace(s)
				if s == "" {
					return nil, fmt.Errorf("Emit: empty topic")
				}
				out = append(out, EmitAction{Topic: s})
			}
		case "canceltimer":
			if err != nil {
				return nil, fmt.Errorf("%s: %v", c.Key, err)
			}
			for _, s := range strs {
				if strings.TrimSpace(s) == "" {
					return nil, fmt.Errorf("CancelTimer: empty timer name")
				}
				out = append(out, CancelAction{Name: strings.TrimSpace(s)})
			}
		case "do":
			if err != nil {
				return nil, fmt.Errorf("%s: %v", c.Key, err)
			}
			for _, s := range strs {
				a, err := parseActionString(s, boxName)
				if err != nil {
					return nil, fmt.Errorf("Do %q: %v", s, err)
				}
				out = append(out, a)
			}
		case "timer":
			t, err := compileTimer(c, boxName)
			if err != nil {
				return nil, fmt.Errorf("Timer: %w", err)
			}
			out = append(out, t)
		default:
			return nil, fmt.Errorf("unknown key %s (expected SetTarget, Do, Emit, Timer, CancelTimer%s)",
				c.Key, map[bool]string{true: ", When, Trigger", false: ""}[isRule])
		}
	}
	return out, nil
}

func stringsOf(c *box.Entry) ([]string, error) {
	switch c.Value.Kind {
	case box.KindString:
		return []string{c.Value.Str}, nil
	case box.KindArray:
		out := make([]string, 0, len(c.Value.Arr))
		for _, v := range c.Value.Arr {
			if v.Kind != box.KindString {
				return nil, fmt.Errorf("array elements must be strings")
			}
			out = append(out, v.Str)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a string or an array of strings")
}

const maxTimerSeconds = 366 * 24 * 3600

func compileTimer(c *box.Entry, boxName string) (Action, error) {
	if c.Value.Kind != box.KindBox || c.Value.Box == nil {
		return nil, fmt.Errorf("must be a box with Name, Seconds and actions")
	}
	var name string
	var secs float64
	var haveSecs bool
	for _, f := range c.Value.Box.Entries() {
		switch strings.ToLower(f.Key) {
		case "name":
			if f.Value.Kind != box.KindString || strings.TrimSpace(f.Value.Str) == "" {
				return nil, fmt.Errorf("Name must be a non-empty string")
			}
			name = strings.TrimSpace(f.Value.Str)
		case "seconds":
			if f.Value.Kind != box.KindNumber {
				return nil, fmt.Errorf("Seconds must be a number")
			}
			secs, haveSecs = f.Value.Num, true
		}
	}
	if name == "" {
		return nil, fmt.Errorf("missing Name")
	}
	if !haveSecs || !(secs > 0) || secs > maxTimerSeconds || math.IsNaN(secs) {
		return nil, fmt.Errorf("Seconds must be > 0 and at most %d", maxTimerSeconds)
	}
	acts, err := compileActions(c.Value.Box, boxName, false)
	if err != nil {
		return nil, err
	}
	if len(acts) == 0 {
		return nil, fmt.Errorf("timer %s has no actions", name)
	}
	return TimerAction{Name: name, Duration: time.Duration(secs * float64(time.Second)), Actions: acts}, nil
}

// parseActionString parses `set <target> = <expr>`, `emit <topic>`,
// or `cancel <timer>`.
func parseActionString(s, boxName string) (Action, error) {
	s = strings.TrimSpace(s)
	verb, rest := s, ""
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		verb, rest = s[:i], strings.TrimSpace(s[i+1:])
	}
	switch strings.ToLower(verb) {
	case "set":
		return parseSet(rest, boxName)
	case "emit":
		topic := rest
		if strings.HasPrefix(topic, `"`) {
			rs := []rune(topic)
			u, n, err := lexQuoted(rs, 0)
			if err != nil || n != len(rs) {
				return nil, fmt.Errorf("bad quoted topic")
			}
			topic = u
		}
		if topic == "" || strings.ContainsAny(topic, " \t") {
			return nil, fmt.Errorf("emit needs one topic word, e.g. emit player.died")
		}
		return EmitAction{Topic: topic}, nil
	case "cancel":
		if rest == "" || strings.ContainsAny(rest, " \t") {
			return nil, fmt.Errorf("cancel needs one timer name")
		}
		return CancelAction{Name: rest}, nil
	}
	return nil, fmt.Errorf("unknown action %q (expected set, emit or cancel)", verb)
}

// parseSet parses `<target> = <expr>`.
func parseSet(s, boxName string) (SetAction, error) {
	toks, err := lexExpr(s)
	if err != nil {
		return SetAction{}, err
	}
	p := &exprParser{toks: toks, boxName: boxName}
	t := p.next()
	var a SetAction
	a.text = strings.TrimSpace(s)
	switch t.kind {
	case tkPath:
		a.Local = localSegs(t.path.Segs, boxName)
	case tkRef:
		r := box.Ref{File: t.path.File, Segs: t.path.Segs}
		ad, err := link.AddrOf(r)
		if err != nil {
			return a, err
		}
		_ = ad
		if len(r.Segs) < 2 {
			return a, fmt.Errorf("target %s must name a field, not a whole record", r)
		}
		for _, sg := range r.Segs {
			if sg.Wild || sg.AnyID {
				return a, fmt.Errorf("wildcards are not allowed in a set target")
			}
		}
		a.Remote = &r
	default:
		return a, fmt.Errorf("expected a field to set, got %s", describe(t))
	}
	if !p.isOp("=") {
		return a, p.errf("expected '=' after the target")
	}
	p.next()
	v, err := p.or()
	if err != nil {
		return a, err
	}
	if p.peek().kind != tkEOF {
		return a, p.errf("unexpected %s", describe(p.peek()))
	}
	a.Value = v
	return a, nil
}
