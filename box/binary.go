package box

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Compact binary encoding of values (used by the engine WAL). It is not a
// public wire format; it only has to round-trip.

var errShort = errors.New("box: truncated binary value")

func putUvarint(b []byte, x uint64) []byte { return binary.AppendUvarint(b, x) }
func putStr(b []byte, s string) []byte     { b = putUvarint(b, uint64(len(s))); return append(b, s...) }

// AppendValue appends the binary form of v.
func AppendValue(b []byte, v Value) []byte {
	b = append(b, byte(v.Kind))
	switch v.Kind {
	case KindString:
		b = putStr(b, v.Str)
	case KindNumber:
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v.Num))
	case KindBool:
		if v.Bool {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	case KindArray:
		b = putUvarint(b, uint64(len(v.Arr)))
		for _, x := range v.Arr {
			b = AppendValue(b, x)
		}
	case KindBox:
		b = AppendBox(b, v.Box)
	case KindLink:
		l := v.Link
		if l == nil {
			l = &Link{}
		}
		b = appendRef(b, l.From)
		b = putStr(b, l.Op)
		if l.To != nil {
			b = append(b, 1)
			b = appendRef(b, *l.To)
		} else {
			b = append(b, 0)
		}
	}
	return b
}

func appendRef(b []byte, r Ref) []byte {
	b = putStr(b, r.File)
	b = putUvarint(b, uint64(len(r.Segs)))
	for _, s := range r.Segs {
		b = putStr(b, s.Name)
		var f byte
		if s.HasID {
			f |= 1
		}
		if s.AnyID {
			f |= 2
		}
		if s.Wild {
			f |= 4
		}
		b = append(b, f)
		b = binary.AppendVarint(b, s.ID)
	}
	return b
}

// AppendBox appends a box (flag, meta, entries with attrs and TTL).
func AppendBox(b []byte, bx *Box) []byte {
	if bx == nil {
		bx = NewBox()
	}
	var f byte
	if bx.HasFlag {
		f |= 1
	}
	if bx.Flag {
		f |= 2
	}
	b = append(b, f)
	b = appendAttrs(b, bx.Meta)
	es := bx.Entries()
	b = putUvarint(b, uint64(len(es)))
	for _, e := range es {
		b = AppendEntry(b, e)
	}
	return b
}

func appendAttrs(b []byte, as []Attr) []byte {
	b = putUvarint(b, uint64(len(as)))
	for _, a := range as {
		b = putStr(b, a.Name)
		b = AppendValue(b, a.Value)
	}
	return b
}

// AppendEntry appends one entry.
func AppendEntry(b []byte, e *Entry) []byte {
	b = putStr(b, e.Key)
	b = appendAttrs(b, e.Attrs)
	b = append(b, e.TTL.Kind)
	b = putUvarint(b, uint64(e.TTL.Seconds))
	b = binary.AppendVarint(b, e.TTL.ExpiresAt())
	return AppendValue(b, e.Value)
}

type dec struct {
	b   []byte
	err error
}

func (d *dec) byte() byte {
	if len(d.b) < 1 {
		d.err = errShort
		return 0
	}
	c := d.b[0]
	d.b = d.b[1:]
	return c
}
func (d *dec) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	x, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = errShort
		return 0
	}
	d.b = d.b[n:]
	return x
}
func (d *dec) varint() int64 {
	if d.err != nil {
		return 0
	}
	x, n := binary.Varint(d.b)
	if n <= 0 {
		d.err = errShort
		return 0
	}
	d.b = d.b[n:]
	return x
}
func (d *dec) str() string {
	n := d.uvarint()
	if d.err != nil {
		return ""
	}
	if uint64(len(d.b)) < n {
		d.err = errShort
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

// DecodeValue reads a value and returns the remaining bytes.
func DecodeValue(b []byte) (Value, []byte, error) {
	d := &dec{b: b}
	v := d.value(0)
	return v, d.b, d.err
}

// DecodeEntry reads one entry and returns the remaining bytes.
func DecodeEntry(b []byte) (*Entry, []byte, error) {
	d := &dec{b: b}
	e := d.entry(0)
	return e, d.b, d.err
}

func (d *dec) value(depth int) Value {
	if d.err != nil {
		return Value{}
	}
	if depth > maxDepth*2 {
		d.err = errors.New("box: binary value nested too deeply")
		return Value{}
	}
	k := Kind(d.byte())
	switch k {
	case KindNell:
		return Value{}
	case KindString:
		return String(d.str())
	case KindNumber:
		if len(d.b) < 8 {
			d.err = errShort
			return Value{}
		}
		f := math.Float64frombits(binary.LittleEndian.Uint64(d.b))
		d.b = d.b[8:]
		return Number(f)
	case KindBool:
		return Bool(d.byte() == 1)
	case KindArray:
		n := d.uvarint()
		if d.err != nil || n > uint64(len(d.b)) {
			d.err = errShort
			return Value{}
		}
		arr := make([]Value, 0, n)
		for i := uint64(0); i < n && d.err == nil; i++ {
			arr = append(arr, d.value(depth+1))
		}
		return Array(arr...)
	case KindBox:
		return BoxValue(d.box(depth + 1))
	case KindLink:
		l := &Link{}
		l.From = d.ref()
		l.Op = d.str()
		if d.byte() == 1 {
			r := d.ref()
			l.To = &r
		}
		return LinkValue(l)
	}
	if d.err == nil {
		d.err = fmt.Errorf("box: bad binary kind %d", k)
	}
	return Value{}
}

func (d *dec) ref() Ref {
	r := Ref{File: d.str()}
	n := d.uvarint()
	if d.err != nil || n > uint64(len(d.b)) {
		d.err = errShort
		return r
	}
	for i := uint64(0); i < n && d.err == nil; i++ {
		s := Seg{Name: d.str()}
		f := d.byte()
		s.HasID, s.AnyID, s.Wild = f&1 != 0, f&2 != 0, f&4 != 0
		s.ID = d.varint()
		r.Segs = append(r.Segs, s)
	}
	return r
}

func (d *dec) attrs() []Attr {
	n := d.uvarint()
	if d.err != nil || n > uint64(len(d.b)) {
		d.err = errShort
		return nil
	}
	var as []Attr
	for i := uint64(0); i < n && d.err == nil; i++ {
		name := d.str()
		as = append(as, Attr{name, d.value(1)})
	}
	return as
}

func (d *dec) box(depth int) *Box {
	bx := NewBox()
	f := d.byte()
	bx.HasFlag, bx.Flag = f&1 != 0, f&2 != 0
	bx.Meta = d.attrs()
	n := d.uvarint()
	if d.err != nil || n > uint64(len(d.b)) {
		d.err = errShort
		return bx
	}
	for i := uint64(0); i < n && d.err == nil; i++ {
		bx.Put(d.entry(depth))
	}
	return bx
}

func (d *dec) entry(depth int) *Entry {
	e := &Entry{Key: d.str()}
	e.Attrs = d.attrs()
	e.TTL.Kind = d.byte()
	e.TTL.Seconds = int(d.uvarint())
	e.TTL.SetExpiresAt(d.varint())
	e.Value = d.value(depth)
	return e
}
