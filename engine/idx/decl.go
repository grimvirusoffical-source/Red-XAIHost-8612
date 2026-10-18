package idx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"redx/box"
)

// Kind is the kind of an index.
type Kind string

// Index kinds.
const (
	KindExact  Kind = "exact"  // equality on a string, number or bool
	KindPrefix Kind = "prefix" // string prefix
	KindRange  Kind = "range"  // numeric range
	KindText   Kind = "text"   // word tokens, AND/OR
)

// ErrBadDecl is wrapped by every declaration error.
var ErrBadDecl = errors.New("idx: bad declaration")

// Decl declares one index.
type Decl struct {
	Name   string   // entry key as written, e.g. PlayerByName or ByName[ID=2]
	File   string   // the file whose records are indexed
	Box    string   // record box name; "" indexes every record of File
	Fields []string // field paths inside the record, e.g. Name, Stats.Level, Inventory[ID=3].Count
	Kind   Kind
	// Fold makes exact and prefix indexes case-insensitive (keys and queries
	// are lower-cased). Text indexes always fold. Not allowed for range.
	Fold bool

	paths [][]box.Seg
}

// Validate checks the declaration and compiles the field paths.
func (d *Decl) Validate() error {
	bad := func(format string, a ...interface{}) error {
		return fmt.Errorf("%w: %s: %s", ErrBadDecl, d.Name, fmt.Sprintf(format, a...))
	}
	if d.Name == "" {
		return fmt.Errorf("%w: empty name", ErrBadDecl)
	}
	if d.File == "" {
		return bad("missing File")
	}
	switch d.Kind {
	case KindExact, KindPrefix, KindRange, KindText:
	case "":
		return bad("missing Kind (exact, prefix, range or text)")
	default:
		return bad("unknown Kind %q (exact, prefix, range or text)", string(d.Kind))
	}
	if d.Fold && d.Kind == KindRange {
		return bad("Fold is not allowed on a range index")
	}
	if len(d.Fields) == 0 {
		return bad("missing Fields")
	}
	d.paths = d.paths[:0]
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if seen[f] {
			return bad("duplicate field %q", f)
		}
		seen[f] = true
		p, err := box.ParsePath(f)
		if err != nil {
			return bad("field %q: %v", f, err)
		}
		if p.File != "" || len(p.Segs) == 0 || p.HasWild() {
			return bad("field %q must be a plain path like Stats.Level (no file, no wildcards)", f)
		}
		d.paths = append(d.paths, p.Segs)
	}
	return nil
}

// hash identifies the declaration; a snapshot built from another
// declaration is never reused.
func (d *Decl) hash() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%q|%q|%q|%s|%t|%q", d.Name, d.File, d.Box, d.Kind, d.Fold, d.Fields)))
	return hex.EncodeToString(h[:])
}

func (d *Decl) matches(a Addr) bool {
	return a.File == d.File && (d.Box == "" || d.Box == a.Box)
}

// fieldIndex resolves a query field to its position. "" is allowed only when
// there is a single field.
func (d *Decl) fieldIndex(field string) (string, error) {
	if field == "" {
		if len(d.Fields) == 1 {
			return d.Fields[0], nil
		}
		return "", fmt.Errorf("%w: index %s has several fields (%s)", ErrFieldRequired, d.Name, strings.Join(d.Fields, ", "))
	}
	for _, f := range d.Fields {
		if f == field {
			return f, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not a field of index %s", ErrUnknownField, field, d.Name)
}

// ParseDecls parses the text of a .red-xai-db-idx file: one top-level box
// per index. Entries are File, Box, Fields (a string or an array of
// strings), Kind and Fold. Unknown entries are an error.
func ParseDecls(src string) ([]Decl, error) {
	root, err := box.ParseDocument(src)
	if err != nil {
		return nil, err
	}
	var out []Decl
	for _, e := range root.Entries() {
		d, err := declFromEntry(e)
		if err != nil {
			if errors.Is(err, ErrBadDecl) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %s (line %d): %v", ErrBadDecl, e.EKey(), e.Line, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func declFromEntry(e *box.Entry) (Decl, error) {
	d := Decl{Name: e.EKey().String()}
	if e.Value.Kind != box.KindBox || e.Value.Box == nil {
		return d, fmt.Errorf("an index must be a box: {Name[]} = [ {File[]} = ..., ... ]")
	}
	for _, c := range e.Value.Box.Entries() {
		v := c.Value
		str := func() (string, error) {
			if v.Kind != box.KindString {
				return "", fmt.Errorf("%s must be a string", c.Key)
			}
			return v.Str, nil
		}
		var err error
		switch strings.ToLower(c.Key) {
		case "file":
			d.File, err = str()
		case "box":
			d.Box, err = str()
		case "kind":
			var s string
			s, err = str()
			d.Kind = Kind(strings.ToLower(s))
		case "fold":
			if v.Kind != box.KindBool {
				err = fmt.Errorf("Fold must be TRUE or FALSE")
			}
			d.Fold = v.Bool
		case "fields":
			switch v.Kind {
			case box.KindString:
				d.Fields = []string{v.Str}
			case box.KindArray:
				for _, x := range v.Arr {
					if x.Kind != box.KindString {
						err = fmt.Errorf("Fields must be strings")
						break
					}
					d.Fields = append(d.Fields, x.Str)
				}
			default:
				err = fmt.Errorf("Fields must be a string or an array of strings")
			}
		default:
			err = fmt.Errorf("unknown entry %q (expected File, Box, Fields, Kind, Fold)", c.Key)
		}
		if err != nil {
			return d, err
		}
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	return d, nil
}

// FormatDecls renders declarations as a .red-xai-db-idx document.
func FormatDecls(decls []Decl) string {
	root := box.NewBox()
	for _, d := range decls {
		b := box.NewBox()
		b.Put(box.NewEntry("File", nil, box.String(d.File)))
		if d.Box != "" {
			b.Put(box.NewEntry("Box", nil, box.String(d.Box)))
		}
		fs := make([]box.Value, len(d.Fields))
		for i, f := range d.Fields {
			fs[i] = box.String(f)
		}
		b.Put(box.NewEntry("Fields", nil, box.Array(fs...)))
		b.Put(box.NewEntry("Kind", nil, box.String(string(d.Kind))))
		if d.Fold {
			b.Put(box.NewEntry("Fold", nil, box.Bool(true)))
		}
		key, id := splitName(d.Name)
		root.Put(box.NewEntry(key, id, box.BoxValue(b)))
	}
	return box.Serialize(root)
}

// splitName undoes EKey.String for names such as "ByName[ID=2]".
func splitName(n string) (string, *int64) {
	if p, err := box.ParsePath(n); err == nil && len(p.Segs) == 1 && p.Segs[0].HasID {
		id := p.Segs[0].ID
		return p.Segs[0].Name, &id
	}
	return n, nil
}
