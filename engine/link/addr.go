package link

import (
	"errors"
	"fmt"
	"strconv"

	"redx/box"
)

// Link operators, as stored in box.Link.Op.
const (
	OpRead    = "@>"
	OpCascade = "@!>"
)

// ErrInvalidRef is returned for references that cannot address a record
// (empty, wildcard, or an ID wildcard).
var ErrInvalidRef = errors.New("link: invalid reference")

// Addr addresses one top-level record: a box name plus optional ID inside a
// file, e.g. "players.red-xai.db":Player[ID=1].
type Addr struct {
	File  string
	Box   string
	ID    int64
	HasID bool
}

func (a Addr) String() string {
	s := strconv.Quote(a.File) + ":" + a.Box
	if a.HasID {
		s += "[ID=" + strconv.FormatInt(a.ID, 10) + "]"
	}
	return s
}

// Ref returns the reference that addresses exactly this record.
func (a Addr) Ref() box.Ref {
	return box.Ref{File: a.File, Segs: []box.Seg{{Name: a.Box, ID: a.ID, HasID: a.HasID}}}
}

// AddrOf returns the record address of a reference (its first segment).
func AddrOf(r box.Ref) (Addr, error) {
	if r.File == "" || len(r.Segs) == 0 {
		return Addr{}, fmt.Errorf("%w: %s", ErrInvalidRef, r.String())
	}
	s := r.Segs[0]
	if s.Wild || s.AnyID || s.Name == "" {
		return Addr{}, fmt.Errorf("%w: wildcard in %s", ErrInvalidRef, r.String())
	}
	return Addr{File: r.File, Box: s.Name, ID: s.ID, HasID: s.HasID}, nil
}

// EntryAddr returns the address of a top-level entry stored in file.
func EntryAddr(file string, e *box.Entry) Addr {
	id, has := e.ID()
	return Addr{File: file, Box: e.Key, ID: id, HasID: has}
}

// ParseLink parses one link written in Box grammar.
func ParseLink(s string) (*box.Link, error) {
	v, err := box.ParseValue(s)
	if err != nil {
		return nil, err
	}
	if v.Kind != box.KindLink || v.Link == nil {
		return nil, fmt.Errorf("link: %q is not a link", s)
	}
	return v.Link, nil
}

// Edge is one outgoing reference of a record.
type Edge struct {
	Owner  Addr
	Via    string  // "field:<path>" for embedded links, "link:<name>" for standalone links
	Op     string  // OpRead or OpCascade
	Target box.Ref // what the edge points at
}

func (e Edge) String() string {
	return fmt.Sprintf("%s (%s) %s %s", e.Owner, e.Via, e.Op, e.Target)
}

// linkEdge converts a link value into (op, target). A bare reference
// (no To) points at its own From and is a read link.
func linkTarget(l *box.Link) (string, box.Ref) {
	if l.To == nil {
		return OpRead, l.From
	}
	op := l.Op
	if op != OpCascade {
		op = OpRead
	}
	return op, *l.To
}

// EmbeddedEdges lists the links embedded in the record, in field order.
func EmbeddedEdges(owner Addr, e *box.Entry) []Edge {
	var out []Edge
	walkLinks(e.Value, "", func(path string, l *box.Link) {
		op, tgt := linkTarget(l)
		out = append(out, Edge{Owner: owner, Via: "field:" + path, Op: op, Target: tgt})
	})
	return out
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func walkLinks(v box.Value, path string, fn func(path string, l *box.Link)) {
	switch v.Kind {
	case box.KindLink:
		if v.Link != nil {
			fn(path, v.Link)
		}
	case box.KindBox:
		if v.Box == nil {
			return
		}
		for _, c := range v.Box.Entries() {
			walkLinks(c.Value, joinPath(path, c.EKey().String()), fn)
		}
	case box.KindArray:
		for i, c := range v.Arr {
			walkLinks(c, path+"["+strconv.Itoa(i)+"]", fn)
		}
	}
}
