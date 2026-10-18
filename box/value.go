// Package box implements the RedX "Box" grammar: lexer, parser, serializer,
// in-memory tree types and a path resolver. All RedX file formats share it.
package box

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// Kind is the dynamic type of a Value.
type Kind uint8

// Value kinds. The zero Value is NELL (null).
const (
	KindNell Kind = iota
	KindString
	KindNumber
	KindBool
	KindArray
	KindBox
	KindLink
)

func (k Kind) String() string {
	switch k {
	case KindNell:
		return "nell"
	case KindString:
		return "string"
	case KindNumber:
		return "number"
	case KindBool:
		return "bool"
	case KindArray:
		return "array"
	case KindBox:
		return "box"
	case KindLink:
		return "link"
	}
	return "unknown"
}

// Value is a Box value: a tagged union. The zero Value is NELL.
type Value struct {
	Kind Kind
	Str  string
	Num  float64
	Bool bool
	Arr  []Value
	Box  *Box
	Link *Link
}

// Constructors.
func Nell() Value             { return Value{} }
func String(s string) Value   { return Value{Kind: KindString, Str: s} }
func Number(f float64) Value  { return Value{Kind: KindNumber, Num: f} }
func Int(i int64) Value       { return Value{Kind: KindNumber, Num: float64(i)} }
func Bool(b bool) Value       { return Value{Kind: KindBool, Bool: b} }
func Array(vs ...Value) Value { return Value{Kind: KindArray, Arr: vs} }
func BoxValue(b *Box) Value   { return Value{Kind: KindBox, Box: b} }
func LinkValue(l *Link) Value { return Value{Kind: KindLink, Link: l} }
func (v Value) IsNell() bool  { return v.Kind == KindNell }

// Link is `"file":Box[ID=1] @> "other":Box[ID=2]`. Op is "@>" (resolve),
// "@!>" (cascade) or "" for a bare reference (To is then nil).
type Link struct {
	From Ref
	Op   string
	To   *Ref
}

// Ref is `"file":Box[ID=1].Key`.
type Ref struct {
	File string
	Segs []Seg
}

func (r Ref) String() string {
	return quote(r.File) + ":" + SegsString(r.Segs)
}

// Attr is a bracket attribute such as ID=1, LOCK=3 or ON=Path.Health, or a
// trailing metadata pair.
type Attr struct {
	Name  string
	Value Value
}

// TTL kinds.
const (
	TTLNone       uint8 = iota
	TTLFixed            // (N)
	TTLSliding          // (~N)
	TTLCheckpoint       // (N)*   (qs-ls checkpoint interval)
)

// TTL is the marker after a value. Seconds is the TTL (or checkpoint
// interval). ExpiresAt is an absolute unix-ms deadline that engines stamp at
// write time (persisted as `(N@ms)`); it is accessed atomically because
// sliding reads refresh it under a read lock.
type TTL struct {
	Kind    uint8
	Seconds int
	expires atomic.Int64 // atomic.Int64 is 64-bit aligned on 32-bit platforms
	logged  atomic.Int64
	// Aux is engine scratch (the scheduled timing-wheel item). Not
	// serialized, not cloned.
	Aux interface{}
}

// ExpiresAt returns the absolute unix-ms expiry, 0 if none.
func (t *TTL) ExpiresAt() int64 { return t.expires.Load() }

// SetExpiresAt stores the absolute unix-ms expiry.
func (t *TTL) SetExpiresAt(ms int64) { t.expires.Store(ms) }

// LoggedAt is engine scratch: the deadline last journaled/replicated.
func (t *TTL) LoggedAt() int64 { return t.logged.Load() }

// SetLoggedAt stores LoggedAt.
func (t *TTL) SetLoggedAt(ms int64) { t.logged.Store(ms) }

// Expiring reports whether the marker implies expiry.
func (t *TTL) Expiring() bool { return t.Kind == TTLFixed || t.Kind == TTLSliding }

// EKey identifies an entry inside its parent box.
type EKey struct {
	Name  string
	ID    int64
	HasID bool
}

func (k EKey) String() string {
	if k.HasID {
		return k.Name + "[ID=" + strconv.FormatInt(k.ID, 10) + "]"
	}
	return k.Name
}

// Entry is `{Key[attrs]} = value ttl`.
type Entry struct {
	Key   string
	Attrs []Attr
	Value Value
	TTL   TTL
	// Ver is engine scratch (commit LSN of the last change, for optimistic
	// concurrency). Not serialized.
	Ver  uint64
	Line int // source position when parsed (1-based), 0 otherwise
	Col  int

	seq int
	ek  EKey
}

// EKey returns the entry's key within its parent.
func (e *Entry) EKey() EKey { return e.ek }

// Seq returns insertion order within the parent.
func (e *Entry) Seq() int { return e.seq }

// NewEntry builds an entry; if id is non-nil an ID attribute is attached.
func NewEntry(key string, id *int64, v Value) *Entry {
	e := &Entry{Key: key, Value: v}
	if id != nil {
		e.Attrs = []Attr{{Name: "ID", Value: Int(*id)}}
	}
	return e
}

func attrID(attrs []Attr) (int64, bool) {
	for _, a := range attrs {
		if strings.EqualFold(a.Name, "id") {
			switch a.Value.Kind {
			case KindNumber:
				if a.Value.Num == math.Trunc(a.Value.Num) {
					return int64(a.Value.Num), true
				}
			case KindString:
				if n, err := strconv.ParseInt(a.Value.Str, 10, 64); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// ID returns the entry's ID attribute if it has one.
func (e *Entry) ID() (int64, bool) { return attrID(e.Attrs) }

// Box is an ordered container of entries (with O(1) keyed lookup/delete).
// Top-level boxes may carry a trailing flag (`]True`) and metadata pairs.
type Box struct {
	HasFlag bool
	Flag    bool
	Meta    []Attr

	m       map[EKey]*Entry
	ids     map[string]map[int64]*Entry
	nextSeq int
}

// NewBox returns an empty box.
func NewBox() *Box { return &Box{} }

// Len is the number of direct entries.
func (b *Box) Len() int {
	if b == nil {
		return 0
	}
	return len(b.m)
}

// Get returns the entry with the exact key (name plus optional id).
func (b *Box) Get(name string, id int64, hasID bool) *Entry {
	if b == nil || b.m == nil {
		return nil
	}
	if hasID {
		return b.m[EKey{name, id, true}]
	}
	if e, ok := b.m[EKey{Name: name}]; ok {
		return e
	}
	// no id given, but entries with ids exist: pick the oldest
	var best *Entry
	for _, e := range b.ids[name] {
		if best == nil || e.seq < best.seq {
			best = e
		}
	}
	return best
}

// Lookup resolves one path segment (non-wildcard).
func (b *Box) Lookup(s Seg) *Entry { return b.Get(s.Name, s.ID, s.HasID) }

// Put inserts e, replacing an existing entry with the same key. It returns
// the replaced entry (nil if none). Replacement keeps the old position.
func (b *Box) Put(e *Entry) *Entry {
	id, has := attrID(e.Attrs)
	e.ek = EKey{e.Key, id, has}
	if b.m == nil {
		b.m = make(map[EKey]*Entry)
	}
	old := b.m[e.ek]
	switch {
	case old != nil:
		e.seq = old.seq
	case e.seq > 0: // restoring an entry that already has a position
		if e.seq > b.nextSeq {
			b.nextSeq = e.seq
		}
	default:
		b.nextSeq++
		e.seq = b.nextSeq
	}
	b.m[e.ek] = e
	if has {
		if b.ids == nil {
			b.ids = make(map[string]map[int64]*Entry)
		}
		mm := b.ids[e.Key]
		if mm == nil {
			mm = make(map[int64]*Entry)
			b.ids[e.Key] = mm
		}
		mm[id] = e
	}
	return old
}

// Remove deletes the entry stored under e's key if it is e itself.
func (b *Box) Remove(e *Entry) bool {
	if b == nil || b.m == nil || b.m[e.ek] != e {
		return false
	}
	delete(b.m, e.ek)
	if e.ek.HasID {
		if mm := b.ids[e.Key]; mm != nil {
			delete(mm, e.ek.ID)
			if len(mm) == 0 {
				delete(b.ids, e.Key)
			}
		}
	}
	return true
}

// Entries returns the entries in insertion order.
func (b *Box) Entries() []*Entry {
	if b == nil {
		return nil
	}
	out := make([]*Entry, 0, len(b.m))
	for _, e := range b.m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// Each visits entries in arbitrary order until fn returns false.
func (b *Box) Each(fn func(*Entry) bool) {
	if b == nil {
		return
	}
	for _, e := range b.m {
		if !fn(e) {
			return
		}
	}
}

// Clone deep-copies the box.
func (b *Box) Clone() *Box {
	if b == nil {
		return nil
	}
	c := &Box{HasFlag: b.HasFlag, Flag: b.Flag, Meta: cloneAttrs(b.Meta)}
	for _, e := range b.Entries() {
		c.Put(e.Clone())
	}
	return c
}

func cloneAttrs(a []Attr) []Attr {
	if a == nil {
		return nil
	}
	out := make([]Attr, len(a))
	for i, x := range a {
		out[i] = Attr{x.Name, x.Value.Clone()}
	}
	return out
}

// Clone deep-copies the entry.
func (e *Entry) Clone() *Entry {
	c := &Entry{Key: e.Key, Attrs: cloneAttrs(e.Attrs), Value: e.Value.Clone(),
		Ver: e.Ver, Line: e.Line, Col: e.Col}
	c.TTL = TTL{Kind: e.TTL.Kind, Seconds: e.TTL.Seconds}
	c.TTL.SetExpiresAt(e.TTL.ExpiresAt())
	c.TTL.SetLoggedAt(e.TTL.LoggedAt())
	c.ek = e.ek
	c.seq = e.seq
	return c
}

// Clone deep-copies the value.
func (v Value) Clone() Value {
	switch v.Kind {
	case KindArray:
		if v.Arr != nil {
			a := make([]Value, len(v.Arr))
			for i, x := range v.Arr {
				a[i] = x.Clone()
			}
			v.Arr = a
		}
	case KindBox:
		v.Box = v.Box.Clone()
	case KindLink:
		if v.Link != nil {
			l := *v.Link
			l.From.Segs = append([]Seg(nil), l.From.Segs...)
			if l.To != nil {
				t := *l.To
				t.Segs = append([]Seg(nil), t.Segs...)
				l.To = &t
			}
			v.Link = &l
		}
	}
	return v
}

// Equal compares values structurally (box entries in order, attrs, TTL
// kind/seconds; not expiry timestamps or engine scratch).
func (v Value) Equal(o Value) bool {
	if v.Kind != o.Kind {
		return false
	}
	switch v.Kind {
	case KindNell:
		return true
	case KindString:
		return v.Str == o.Str
	case KindNumber:
		return v.Num == o.Num
	case KindBool:
		return v.Bool == o.Bool
	case KindArray:
		if len(v.Arr) != len(o.Arr) {
			return false
		}
		for i := range v.Arr {
			if !v.Arr[i].Equal(o.Arr[i]) {
				return false
			}
		}
		return true
	case KindBox:
		return v.Box.Equal(o.Box)
	case KindLink:
		if v.Link == nil || o.Link == nil {
			return v.Link == o.Link
		}
		return v.Link.String() == o.Link.String()
	}
	return false
}

func attrsEqual(a, b []Attr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !a[i].Value.Equal(b[i].Value) {
			return false
		}
	}
	return true
}

// Equal compares boxes structurally.
func (b *Box) Equal(o *Box) bool {
	if b == nil || o == nil {
		return b == o
	}
	if b.HasFlag != o.HasFlag || b.Flag != o.Flag || !attrsEqual(b.Meta, o.Meta) || b.Len() != o.Len() {
		return false
	}
	ea, eb := b.Entries(), o.Entries()
	for i := range ea {
		x, y := ea[i], eb[i]
		if x.Key != y.Key || !attrsEqual(x.Attrs, y.Attrs) || x.TTL.Kind != y.TTL.Kind ||
			x.TTL.Seconds != y.TTL.Seconds || !x.Value.Equal(y.Value) {
			return false
		}
	}
	return true
}

func (l *Link) String() string {
	if l.To == nil || l.Op == "" {
		return l.From.String()
	}
	return l.From.String() + " " + l.Op + " " + l.To.String()
}

// String renders the value in Box grammar.
func (v Value) String() string {
	var sb strings.Builder
	w := &writer{sb: &sb, indent: "    "}
	w.value(v, 0)
	return sb.String()
}

// Float returns the numeric value if v is a number.
func (v Value) Float() (float64, bool) { return v.Num, v.Kind == KindNumber }

// Text returns the string content if v is a string.
func (v Value) Text() (string, bool) { return v.Str, v.Kind == KindString }

// ErrNotFound is returned when a path does not resolve.
var ErrNotFound = errors.New("box: not found")

// ---- JSON ----------------------------------------------------------------

// MarshalJSON renders NELL as null, boxes as objects keyed by `Name` or
// `Name[ID=n]`, links as {"$link": "<grammar text>"}.
func (v Value) MarshalJSON() ([]byte, error) {
	switch v.Kind {
	case KindNell:
		return []byte("null"), nil
	case KindString:
		return json.Marshal(v.Str)
	case KindNumber:
		if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) {
			return []byte("null"), nil
		}
		return json.Marshal(v.Num)
	case KindBool:
		return json.Marshal(v.Bool)
	case KindArray:
		if v.Arr == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(v.Arr)
	case KindLink:
		if v.Link == nil {
			return []byte("null"), nil
		}
		return json.Marshal(map[string]string{"$link": v.Link.String()})
	case KindBox:
		var sb strings.Builder
		sb.WriteByte('{')
		first := true
		if v.Box != nil {
			for _, e := range v.Box.Entries() {
				if !first {
					sb.WriteByte(',')
				}
				first = false
				k, _ := json.Marshal(e.ek.String())
				sb.Write(k)
				sb.WriteByte(':')
				bs, err := e.Value.MarshalJSON()
				if err != nil {
					return nil, err
				}
				sb.Write(bs)
			}
		}
		sb.WriteByte('}')
		return []byte(sb.String()), nil
	}
	return nil, fmt.Errorf("box: cannot marshal kind %d", v.Kind)
}

// UnmarshalJSON is the inverse of MarshalJSON (objects become boxes).
func (v *Value) UnmarshalJSON(data []byte) error {
	var raw interface{}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	out, err := fromAny(raw)
	if err != nil {
		return err
	}
	*v = out
	return nil
}

// FromJSONObject builds a Value from decoded JSON (UseNumber optional).
func FromAny(x interface{}) (Value, error) { return fromAny(x) }

func fromAny(x interface{}) (Value, error) {
	switch t := x.(type) {
	case nil:
		return Nell(), nil
	case string:
		return String(t), nil
	case bool:
		return Bool(t), nil
	case float64:
		return Number(t), nil
	case int:
		return Int(int64(t)), nil
	case int64:
		return Int(t), nil
	case json.Number:
		f, err := t.Float64()
		return Number(f), err
	case []interface{}:
		arr := make([]Value, len(t))
		for i, e := range t {
			x, err := fromAny(e)
			if err != nil {
				return Value{}, err
			}
			arr[i] = x
		}
		return Array(arr...), nil
	case map[string]interface{}:
		if len(t) == 1 {
			if s, ok := t["$link"].(string); ok {
				return ParseValue(s)
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b := NewBox()
		for _, k := range keys {
			p, err := ParsePath(k)
			if err != nil || p.File != "" || len(p.Segs) != 1 || p.Segs[0].Wild || p.Segs[0].AnyID {
				return Value{}, fmt.Errorf("box: bad JSON key %q", k)
			}
			ev, err := fromAny(t[k])
			if err != nil {
				return Value{}, err
			}
			e := &Entry{Key: p.Segs[0].Name, Value: ev}
			if p.Segs[0].HasID {
				e.Attrs = []Attr{{Name: "ID", Value: Int(p.Segs[0].ID)}}
			}
			b.Put(e)
		}
		return BoxValue(b), nil
	}
	return Value{}, fmt.Errorf("box: unsupported JSON value %T", x)
}
