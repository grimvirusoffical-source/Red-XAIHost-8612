package idx

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"redx/box"
)

// Limits.
const (
	// MaxTermsPerRecord caps the distinct terms one record contributes to one
	// index. Extra terms are dropped (counted in Stats.Truncated).
	MaxTermsPerRecord = 4096
	// MaxTermBytes is the longest key or token that is indexed; longer ones
	// are skipped.
	MaxTermBytes = 256
)

// term is one (field, key) pair a record contributes to an index.
type term struct {
	Field string
	Key   string // typed key (exact), folded string (prefix), token (text), canonical number (range)
}

func fmtNum(f float64) string {
	if f == 0 {
		f = 0 // normalise -0
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func okString(s string) bool { return utf8.ValidString(s) && len(s) <= MaxTermBytes }

// extract returns the sorted, de-duplicated terms of a record for d, and
// whether the cap truncated them.
func (d *Decl) extract(e *box.Entry) ([]term, bool) {
	if e == nil || e.Value.Kind != box.KindBox || e.Value.Box == nil {
		return nil, false
	}
	var out []term
	for i, segs := range d.paths {
		le, err := box.Resolve(e.Value.Box, segs)
		if err != nil {
			continue
		}
		field := d.Fields[i]
		walkScalars(le.Value, func(v box.Value) {
			d.addTerms(&out, field, v)
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Field != out[j].Field {
			return out[i].Field < out[j].Field
		}
		return out[i].Key < out[j].Key
	})
	w := 0
	for i, t := range out {
		if i == 0 || t != out[w-1] {
			out[w] = t
			w++
		}
	}
	out = out[:w]
	truncated := false
	if len(out) > MaxTermsPerRecord {
		out = out[:MaxTermsPerRecord]
		truncated = true
	}
	return out, truncated
}

func walkScalars(v box.Value, fn func(box.Value)) {
	switch v.Kind {
	case box.KindString, box.KindNumber, box.KindBool:
		fn(v)
	case box.KindArray:
		for _, x := range v.Arr {
			walkScalars(x, fn)
		}
	}
}

func (d *Decl) addTerms(out *[]term, field string, v box.Value) {
	switch d.Kind {
	case KindExact:
		switch v.Kind {
		case box.KindString:
			if okString(v.Str) {
				*out = append(*out, term{field, exactKeyString(v.Str, d.Fold)})
			}
		case box.KindNumber:
			if !math.IsNaN(v.Num) && !math.IsInf(v.Num, 0) {
				*out = append(*out, term{field, "n:" + fmtNum(v.Num)})
			}
		case box.KindBool:
			k := "b:0"
			if v.Bool {
				k = "b:1"
			}
			*out = append(*out, term{field, k})
		}
	case KindPrefix:
		if v.Kind == box.KindString && okString(v.Str) {
			s := v.Str
			if d.Fold {
				s = strings.ToLower(s)
			}
			*out = append(*out, term{field, s})
		}
	case KindRange:
		if v.Kind == box.KindNumber && !math.IsNaN(v.Num) && !math.IsInf(v.Num, 0) {
			*out = append(*out, term{field, fmtNum(v.Num)})
		}
	case KindText:
		if v.Kind == box.KindString && utf8.ValidString(v.Str) {
			for _, tok := range Tokenize(v.Str) {
				*out = append(*out, term{field, tok})
			}
		}
	}
}

func exactKeyString(s string, fold bool) string {
	if fold {
		s = strings.ToLower(s)
	}
	return "s:" + s
}

// exactKey is the typed key of a query value.
func exactKey(v box.Value, fold bool) (string, bool) {
	switch v.Kind {
	case box.KindString:
		if !okString(v.Str) {
			return "", false
		}
		return exactKeyString(v.Str, fold), true
	case box.KindNumber:
		if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) {
			return "", false
		}
		return "n:" + fmtNum(v.Num), true
	case box.KindBool:
		if v.Bool {
			return "b:1", true
		}
		return "b:0", true
	}
	return "", false
}

// Tokenize splits text into lower-cased word tokens (maximal runs of Unicode
// letters and digits). Tokens longer than MaxTermBytes are dropped.
// Duplicates are kept; callers de-duplicate.
func Tokenize(s string) []string {
	var out []string
	f := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	for _, w := range strings.FieldsFunc(s, f) {
		w = strings.ToLower(w)
		if len(w) <= MaxTermBytes {
			out = append(out, w)
		}
	}
	return out
}

// recDigest checksums one record's contribution (address and terms).
func recDigest(a Addr, terms []term) [32]byte {
	h := sha256.New()
	h.Write([]byte(a.String()))
	h.Write([]byte{'\n'})
	for _, t := range terms {
		var n [8]byte
		binary.BigEndian.PutUint32(n[:4], uint32(len(t.Field)))
		binary.BigEndian.PutUint32(n[4:], uint32(len(t.Key)))
		h.Write(n[:])
		h.Write([]byte(t.Field))
		h.Write([]byte(t.Key))
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}
