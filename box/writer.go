package box

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// FormatOptions controls serialization.
type FormatOptions struct {
	Indent string // default 4 spaces
	// Absolute writes engine expiry stamps as `(N@unixms)`. Used by
	// snapshots so replay never extends a lifetime.
	Absolute bool
	// Compact writes everything on one line.
	Compact bool
}

type writer struct {
	sb       *strings.Builder
	indent   string
	absolute bool
	compact  bool
}

// Serialize renders a document (root box) as RedX text.
func Serialize(root *Box) string { return Format(root, FormatOptions{}) }

// Format renders a document with options.
func Format(root *Box, o FormatOptions) string {
	var sb strings.Builder
	w := &writer{sb: &sb, indent: o.Indent, absolute: o.Absolute, compact: o.Compact}
	if w.indent == "" {
		w.indent = "    "
	}
	for _, e := range root.Entries() {
		w.topItem(e)
		if w.compact {
			w.sb.WriteByte(' ')
		} else {
			w.sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// String renders the box body as `[ ... ]`.
func (b *Box) String() string {
	var sb strings.Builder
	w := &writer{sb: &sb, indent: "    "}
	w.value(BoxValue(b), 0)
	return sb.String()
}

func (w *writer) nl(level int) {
	if w.compact {
		w.sb.WriteByte(' ')
		return
	}
	w.sb.WriteByte('\n')
	for i := 0; i < level; i++ {
		w.sb.WriteString(w.indent)
	}
}

func (w *writer) topItem(e *Entry) {
	if e.Value.Kind == KindBox && e.Value.Box != nil {
		w.sb.WriteString("{" + e.Key)
		w.attrs(e.Attrs)
		w.sb.WriteString("= ")
		w.boxBody(e.Value.Box, 0)
		w.ttl(e)
		b := e.Value.Box
		if b.HasFlag {
			if b.Flag {
				w.sb.WriteString("True")
			} else {
				w.sb.WriteString("False")
			}
		}
		for _, m := range b.Meta {
			w.sb.WriteString(", " + m.Name + " = ")
			w.meta(m.Value)
		}
		w.sb.WriteString("};")
		return
	}
	w.entry(e, 0)
	w.sb.WriteString(";")
}

func (w *writer) entry(e *Entry, level int) {
	w.sb.WriteString("{" + e.Key)
	w.attrs(e.Attrs)
	w.sb.WriteString("} = ")
	w.value(e.Value, level)
	w.ttl(e)
}

func (w *writer) attrs(as []Attr) {
	w.sb.WriteByte('[')
	for i, a := range as {
		if i > 0 {
			w.sb.WriteString(", ")
		}
		w.sb.WriteString(a.Name + "=")
		w.attrValue(a.Value)
	}
	w.sb.WriteByte(']')
}

var wordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*|\[[A-Za-z0-9_=*]+\])*$`)
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func isKeyword(s string) bool {
	switch strings.ToUpper(s) {
	case "TRUE", "FALSE", "NELL":
		return true
	}
	return false
}

func (w *writer) attrValue(v Value) {
	if v.Kind == KindString && wordRe.MatchString(v.Str) && !isKeyword(v.Str) {
		w.sb.WriteString(v.Str)
		return
	}
	w.value(v, 0)
}

func (w *writer) meta(v Value) {
	if v.Kind == KindString && identRe.MatchString(v.Str) && !isKeyword(v.Str) {
		w.sb.WriteString(v.Str)
		return
	}
	w.value(v, 0)
}

func (w *writer) ttl(e *Entry) {
	switch e.TTL.Kind {
	case TTLNone:
		return
	case TTLFixed, TTLSliding, TTLCheckpoint:
		w.sb.WriteString(" (")
		if e.TTL.Kind == TTLSliding {
			w.sb.WriteByte('~')
		}
		w.sb.WriteString(strconv.Itoa(e.TTL.Seconds))
		if exp := e.TTL.ExpiresAt(); w.absolute && exp > 0 {
			w.sb.WriteString("@" + strconv.FormatInt(exp, 10))
		}
		w.sb.WriteByte(')')
		if e.TTL.Kind == TTLCheckpoint {
			w.sb.WriteByte('*')
		}
	}
}

func (w *writer) boxBody(b *Box, level int) {
	es := b.Entries()
	if len(es) == 0 {
		w.sb.WriteString("[]")
		return
	}
	w.sb.WriteByte('[')
	for i, e := range es {
		w.nl(level + 1)
		w.entry(e, level+1)
		if i < len(es)-1 {
			w.sb.WriteByte(',')
		}
	}
	w.nl(level)
	w.sb.WriteByte(']')
}

func (w *writer) value(v Value, level int) {
	switch v.Kind {
	case KindNell:
		w.sb.WriteString("NELL")
	case KindString:
		w.sb.WriteString(quote(v.Str))
	case KindNumber:
		w.sb.WriteString(FormatNumber(v.Num))
	case KindBool:
		if v.Bool {
			w.sb.WriteString("TRUE")
		} else {
			w.sb.WriteString("FALSE")
		}
	case KindArray:
		w.sb.WriteString(";[")
		for i, x := range v.Arr {
			if i > 0 {
				w.sb.WriteString(", ")
			}
			w.value(x, level)
		}
		w.sb.WriteString("]:")
	case KindBox:
		if v.Box == nil {
			w.sb.WriteString("[]")
			return
		}
		w.boxBody(v.Box, level)
	case KindLink:
		if v.Link == nil {
			w.sb.WriteString("NELL")
		} else {
			w.sb.WriteString(v.Link.String())
		}
	}
}

// FormatNumber renders a float in the shortest form the lexer reads back.
func FormatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				sb.WriteString(`\u00`)
				sb.WriteString(strconv.FormatInt(int64(r)+0x100, 16)[1:])
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
