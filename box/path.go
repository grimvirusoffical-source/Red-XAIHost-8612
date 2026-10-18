package box

import (
	"fmt"
	"strconv"
	"strings"
)

// Seg is one path segment: `Name`, `Name[ID=1]`, `Name[id=*]`, `*`.
type Seg struct {
	Name  string
	ID    int64
	HasID bool
	AnyID bool // [*] or [ID=*]
	Wild  bool // Name == "*"
}

func (s Seg) String() string {
	var b strings.Builder
	b.WriteString(s.Name)
	switch {
	case s.AnyID:
		b.WriteString("[ID=*]")
	case s.HasID:
		b.WriteString("[ID=" + strconv.FormatInt(s.ID, 10) + "]")
	}
	return b.String()
}

// SegsString joins segments with dots.
func SegsString(segs []Seg) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.String()
	}
	return strings.Join(parts, ".")
}

// Path is `[file:]Seg(.Seg)*`, e.g. `accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10]`.
type Path struct {
	File string
	Segs []Seg
}

func (p Path) String() string {
	s := SegsString(p.Segs)
	if p.File != "" {
		return p.File + ":" + s
	}
	return s
}

// HasWild reports whether the path contains wildcards.
func (p Path) HasWild() bool {
	for _, s := range p.Segs {
		if s.Wild || s.AnyID {
			return true
		}
	}
	return false
}

// ParsePath parses a path string. The file prefix (before the first ':'
// outside brackets) may be quoted.
func ParsePath(s string) (Path, error) {
	var p Path
	s = strings.TrimSpace(s)
	if s == "" {
		return p, fmt.Errorf("box: empty path")
	}
	rest := s
	if s[0] == '"' {
		str, n, err := lexString(s)
		if err != nil {
			return p, fmt.Errorf("box: bad path %q: %s", s, err.msg)
		}
		if n >= len(s) || s[n] != ':' {
			return p, fmt.Errorf("box: bad path %q: expected ':' after quoted file", s)
		}
		p.File = str
		rest = s[n+1:]
	} else {
		depth := 0
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '[':
				depth++
			case ']':
				depth--
			case ':':
				if depth == 0 {
					p.File = s[:i]
					rest = s[i+1:]
					i = len(s)
				}
			}
		}
	}
	if rest == "" {
		return p, fmt.Errorf("box: path %q has no box name", s)
	}
	i := 0
	for {
		seg, n, err := parseSeg(rest[i:])
		if err != nil {
			return p, fmt.Errorf("box: bad path %q at offset %d: %v", s, i, err)
		}
		p.Segs = append(p.Segs, seg)
		i += n
		if i >= len(rest) {
			break
		}
		if rest[i] != '.' {
			return p, fmt.Errorf("box: bad path %q: unexpected %q at offset %d", s, rest[i], i)
		}
		i++
		if i >= len(rest) {
			return p, fmt.Errorf("box: bad path %q: trailing '.'", s)
		}
	}
	return p, nil
}

func parseSeg(s string) (Seg, int, error) {
	var seg Seg
	i := 0
	if len(s) > 0 && s[0] == '*' {
		seg.Name, seg.Wild = "*", true
		i = 1
	} else {
		for i < len(s) {
			r := rune(s[i])
			if i == 0 && !(r == '_' || isASCIILetter(r) || r >= 0x80) {
				break
			}
			if i > 0 && !(r == '_' || r == '-' || isASCIILetter(r) || (r >= '0' && r <= '9') || r >= 0x80) {
				break
			}
			i++
		}
		if i == 0 {
			return seg, 0, fmt.Errorf("expected a name")
		}
		seg.Name = s[:i]
	}
	if i < len(s) && s[i] == '[' {
		end := strings.IndexByte(s[i:], ']')
		if end < 0 {
			return seg, 0, fmt.Errorf("missing ']'")
		}
		inner := strings.TrimSpace(s[i+1 : i+end])
		i += end + 1
		if inner != "" {
			if k := strings.IndexByte(inner, '='); k >= 0 {
				if !strings.EqualFold(strings.TrimSpace(inner[:k]), "id") {
					return seg, 0, fmt.Errorf("only ID selectors are supported in paths, got %q", inner)
				}
				inner = strings.TrimSpace(inner[k+1:])
			}
			if inner == "*" {
				seg.AnyID = true
			} else {
				n, err := strconv.ParseInt(inner, 10, 64)
				if err != nil {
					return seg, 0, fmt.Errorf("bad ID %q", inner)
				}
				seg.ID, seg.HasID = n, true
			}
		}
	}
	return seg, i, nil
}

func isASCIILetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// Resolve walks segs from root (no wildcards) and returns the entry.
func Resolve(root *Box, segs []Seg) (*Entry, error) {
	cur := root
	var e *Entry
	for i, s := range segs {
		if cur == nil {
			return nil, fmt.Errorf("%w: %s is not a box", ErrNotFound, SegsString(segs[:i]))
		}
		e = cur.Lookup(s)
		if e == nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, SegsString(segs[:i+1]))
		}
		if e.Value.Kind == KindBox {
			cur = e.Value.Box
		} else {
			cur = nil
		}
	}
	if e == nil {
		return nil, fmt.Errorf("%w: empty path", ErrNotFound)
	}
	return e, nil
}

// Match resolves a path that may contain wildcards, returning all matching
// entries in document order.
func Match(root *Box, segs []Seg) []*Entry {
	level := []*Box{root}
	var out []*Entry
	for i, s := range segs {
		var next []*Box
		out = out[:0]
		for _, b := range level {
			if b == nil {
				continue
			}
			var hits []*Entry
			switch {
			case s.Wild:
				hits = b.Entries()
			case s.AnyID:
				for _, e := range b.Entries() {
					if e.Key == s.Name {
						hits = append(hits, e)
					}
				}
			default:
				if e := b.Lookup(s); e != nil {
					hits = []*Entry{e}
				}
			}
			for _, e := range hits {
				out = append(out, e)
				if i < len(segs)-1 && e.Value.Kind == KindBox {
					next = append(next, e.Value.Box)
				}
			}
		}
		level = next
	}
	return append([]*Entry(nil), out...)
}

// MatchChains is Match with ancestry: it returns, for every entry matching
// segs, the chain of entries from the top level down to the match. Subtrees
// for which skip(entry) is true are not entered and never match (the engine
// uses it to hide expired entries together with everything below them).
func MatchChains(root *Box, segs []Seg, skip func(*Entry) bool) [][]*Entry {
	type node struct {
		b     *Box
		chain []*Entry
	}
	level := []node{{b: root}}
	var out [][]*Entry
	for i, s := range segs {
		var next []node
		out = out[:0]
		for _, n := range level {
			if n.b == nil {
				continue
			}
			var hits []*Entry
			switch {
			case s.Wild:
				hits = n.b.Entries()
			case s.AnyID:
				for _, e := range n.b.Entries() {
					if e.Key == s.Name {
						hits = append(hits, e)
					}
				}
			default:
				if e := n.b.Lookup(s); e != nil {
					hits = []*Entry{e}
				}
			}
			for _, e := range hits {
				if skip != nil && skip(e) {
					continue
				}
				ch := make([]*Entry, len(n.chain)+1)
				copy(ch, n.chain)
				ch[len(n.chain)] = e
				out = append(out, ch)
				if i < len(segs)-1 && e.Value.Kind == KindBox {
					next = append(next, node{e.Value.Box, ch})
				}
			}
		}
		level = next
	}
	return append([][]*Entry(nil), out...)
}
