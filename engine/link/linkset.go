package link

import (
	"fmt"
	"sync"

	"redx/box"
)

// NamedLink is one standalone link of a .red-xai-db-link file.
type NamedLink struct {
	Name string // key as written, e.g. UserGuildLink[ID=1]
	Link *box.Link
}

// LinkSet is the content of a .red-xai-db-link file: standalone links that
// live outside the records they connect. Safe for concurrent use.
//
// File syntax (a normal Box document, one link per top-level entry):
//
//	{UserGuildLink[1]} = "accounts.red-xai.db":AccountVault[ID=1] @> "guilds.red-xai.db":GuildBox[ID=4];
//	{CleanupInv[2]}    = "accounts.red-xai.db":AccountVault[ID=1] @!> "inventories.red-xai.db":Inv[ID=9];
type LinkSet struct {
	mu    sync.RWMutex
	order []string
	byKey map[string]*box.Link
}

func NewLinkSet() *LinkSet { return &LinkSet{byKey: map[string]*box.Link{}} }

// ParseLinkFile parses the text of a .red-xai-db-link file. Every top-level
// entry must be a link with both sides (`A @> B` or `A @!> B`).
func ParseLinkFile(src string) (*LinkSet, error) {
	root, err := box.ParseDocument(src)
	if err != nil {
		return nil, err
	}
	ls := NewLinkSet()
	for _, e := range root.Entries() {
		if e.Value.Kind != box.KindLink || e.Value.Link == nil || e.Value.Link.To == nil {
			return nil, fmt.Errorf("link file: entry %s (line %d) is not a link of the form A @> B or A @!> B", e.EKey(), e.Line)
		}
		if err := ls.Add(e.EKey().String(), e.Value.Link); err != nil {
			return nil, fmt.Errorf("link file: entry %s (line %d): %w", e.EKey(), e.Line, err)
		}
	}
	return ls, nil
}

func validateStandalone(l *box.Link) error {
	if l == nil || l.To == nil {
		return fmt.Errorf("link has no target")
	}
	if l.Op != OpRead && l.Op != OpCascade {
		return fmt.Errorf("link operator must be @> or @!>, got %q", l.Op)
	}
	if _, err := AddrOf(l.From); err != nil {
		return err
	}
	if _, err := AddrOf(*l.To); err != nil {
		return err
	}
	return nil
}

// Add registers a link under a unique name.
func (s *LinkSet) Add(name string, l *box.Link) error {
	if name == "" {
		return fmt.Errorf("link name is empty")
	}
	if err := validateStandalone(l); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byKey[name]; dup {
		return fmt.Errorf("duplicate link name %s", name)
	}
	c := box.LinkValue(l).Clone().Link
	s.byKey[name] = c
	s.order = append(s.order, name)
	return nil
}

// Remove deletes a link by name.
func (s *LinkSet) Remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[name]; !ok {
		return false
	}
	s.removeLocked(name)
	return true
}

func (s *LinkSet) removeLocked(name string) {
	delete(s.byKey, name)
	for i, n := range s.order {
		if n == name {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// Len is the number of links.
func (s *LinkSet) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.order)
}

// All returns every link in insertion order.
func (s *LinkSet) All() []NamedLink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NamedLink, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, NamedLink{n, s.byKey[n]})
	}
	return out
}

// Outgoing returns the links whose source record is a.
func (s *LinkSet) Outgoing(a Addr) []Edge {
	var out []Edge
	for _, nl := range s.All() {
		from, err := AddrOf(nl.Link.From)
		if err != nil || from != a {
			continue
		}
		op, tgt := linkTarget(nl.Link)
		out = append(out, Edge{Owner: a, Via: "link:" + nl.Name, Op: op, Target: tgt})
	}
	return out
}

// RemoveWhere removes every link for which pred returns true and returns
// the removed names.
func (s *LinkSet) RemoveWhere(pred func(name string, l *box.Link) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	for _, n := range append([]string(nil), s.order...) {
		if pred(n, s.byKey[n]) {
			s.removeLocked(n)
			gone = append(gone, n)
		}
	}
	return gone
}

// String renders the set as a .red-xai-db-link document.
func (s *LinkSet) String() string {
	root := box.NewBox()
	for _, nl := range s.All() {
		p, err := box.ParsePath(nl.Name)
		if err != nil || len(p.Segs) != 1 {
			continue
		}
		seg := p.Segs[0]
		var id *int64
		if seg.HasID {
			id = &seg.ID
		}
		root.Put(box.NewEntry(seg.Name, id, box.LinkValue(nl.Link)))
	}
	return box.Serialize(root)
}
