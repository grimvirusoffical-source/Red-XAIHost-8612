package idx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"redx/box"
)

// Snapshot format: a Box document with one entry, Index, followed by a Seal
// entry holding the SHA-256 of every byte before it:
//
//	{Index[]} = [
//	    {Version[]} = 1, {Name[]} = "...", {Decl[]} = "<decl hash>",
//	    {Seq[]} = 17, {SourceSeq[]} = 40, {Count[]} = 2, {Digest[]} = "<hex>",
//	    {Rec[ID=1]} = ;["file", "Box", "<id or empty>", "field", "key", ...]:,
//	    ...
//	];
//	{Seal[]} = "<hex>";
//
// A truncated or edited file has no valid seal and is rejected.
const snapVersion = 1

const sealPrefix = "{Seal[]} = \""

func seal(body string) []byte {
	h := sha256.Sum256([]byte(body))
	return []byte(body + sealPrefix + hex.EncodeToString(h[:]) + "\";\n")
}

func unseal(data []byte) (string, error) {
	i := bytes.LastIndex(data, []byte(sealPrefix))
	if i < 0 {
		return "", fmt.Errorf("%w: no seal (truncated?)", ErrCorrupt)
	}
	rest := data[i+len(sealPrefix):]
	if len(rest) != 64+3 || string(rest[64:]) != "\";\n" {
		return "", fmt.Errorf("%w: malformed seal", ErrCorrupt)
	}
	h := sha256.Sum256(data[:i])
	if hex.EncodeToString(h[:]) != string(rest[:64]) {
		return "", fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}
	return string(data[:i]), nil
}

// snapshot serializes the index; srcSeq/hasSeq record the source sequence
// number the caller says this state corresponds to.
func (ix *Index) snapshot(srcSeq uint64, hasSeq bool) []byte {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	b := box.NewBox()
	b.Put(box.NewEntry("Version", nil, box.Int(snapVersion)))
	b.Put(box.NewEntry("Name", nil, box.String(ix.decl.Name)))
	b.Put(box.NewEntry("Decl", nil, box.String(ix.decl.hash())))
	b.Put(box.NewEntry("Seq", nil, box.String(strconv.FormatUint(ix.seq, 10))))
	if hasSeq {
		b.Put(box.NewEntry("SourceSeq", nil, box.String(strconv.FormatUint(srcSeq, 10))))
	}
	b.Put(box.NewEntry("Count", nil, box.Int(int64(len(ix.st.recs)))))
	b.Put(box.NewEntry("Digest", nil, box.String(hex.EncodeToString(ix.st.digest[:]))))
	addrs := make([]Addr, 0, len(ix.st.recs))
	for a := range ix.st.recs {
		addrs = append(addrs, a)
	}
	sortAddrs(addrs)
	for i, a := range addrs {
		id := ""
		if a.HasID {
			id = strconv.FormatInt(a.ID, 10)
		}
		row := []box.Value{box.String(a.File), box.String(a.Box), box.String(id)}
		for _, t := range ix.st.recs[a].terms {
			row = append(row, box.String(t.Field), box.String(t.Key))
		}
		n := int64(i + 1)
		b.Put(box.NewEntry("Rec", &n, box.Array(row...)))
	}
	root := box.NewBox()
	root.Put(box.NewEntry("Index", nil, box.BoxValue(b)))
	return seal(box.Serialize(root))
}

type snapData struct {
	decl      string
	seq       uint64
	srcSeq    uint64
	hasSrcSeq bool
	st        *state
}

// decodeSnapshot verifies the seal and rebuilds the state. Errors wrap
// ErrCorrupt.
func (ix *Index) decodeSnapshot(data []byte) (*snapData, error) {
	body, err := unseal(data)
	if err != nil {
		return nil, err
	}
	bad := func(format string, a ...interface{}) (*snapData, error) {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
	}
	root, err := box.ParseDocument(body)
	if err != nil {
		return bad("%v", err)
	}
	e := root.Get("Index", 0, false)
	if e == nil || e.Value.Kind != box.KindBox || e.Value.Box == nil {
		return bad("missing Index box")
	}
	b := e.Value.Box
	str := func(name string) (string, bool) {
		x := b.Get(name, 0, false)
		if x == nil || x.Value.Kind != box.KindString {
			return "", false
		}
		return x.Value.Str, true
	}
	if v := b.Get("Version", 0, false); v == nil || v.Value.Kind != box.KindNumber || v.Value.Num != snapVersion {
		return bad("unsupported version")
	}
	sd := &snapData{}
	var ok bool
	if sd.decl, ok = str("Decl"); !ok {
		return bad("missing Decl")
	}
	seqS, ok := str("Seq")
	if !ok {
		return bad("missing Seq")
	}
	if sd.seq, err = strconv.ParseUint(seqS, 10, 64); err != nil {
		return bad("bad Seq")
	}
	if s, has := str("SourceSeq"); has {
		if sd.srcSeq, err = strconv.ParseUint(s, 10, 64); err != nil {
			return bad("bad SourceSeq")
		}
		sd.hasSrcSeq = true
	}
	digest, ok := str("Digest")
	if !ok {
		return bad("missing Digest")
	}
	cnt := b.Get("Count", 0, false)
	if cnt == nil || cnt.Value.Kind != box.KindNumber {
		return bad("missing Count")
	}
	st := newState(ix.decl.Kind)
	st.bulk = true
	var recs []*box.Entry
	for _, c := range b.Entries() {
		if c.Key == "Rec" {
			recs = append(recs, c)
		}
	}
	for _, c := range recs {
		row := c.Value.Arr
		if c.Value.Kind != box.KindArray || len(row) < 5 || len(row)%2 != 1 {
			return bad("bad record row %s", c.EKey())
		}
		for _, x := range row {
			if x.Kind != box.KindString {
				return bad("bad record row %s", c.EKey())
			}
		}
		a := Addr{File: row[0].Str, Box: row[1].Str}
		if row[2].Str != "" {
			id, err := strconv.ParseInt(row[2].Str, 10, 64)
			if err != nil {
				return bad("bad id in %s", c.EKey())
			}
			a.ID, a.HasID = id, true
		}
		if !ix.decl.matches(a) {
			return bad("record %s does not belong to the index", a)
		}
		if st.recs[a] != nil {
			return bad("duplicate record %s", a)
		}
		var terms []term
		for i := 3; i < len(row); i += 2 {
			t := term{Field: row[i].Str, Key: row[i+1].Str}
			if _, err := ix.decl.fieldIndex(t.Field); err != nil || t.Field == "" {
				return bad("unknown field %q", t.Field)
			}
			if ix.decl.Kind == KindRange {
				if _, err := strconv.ParseFloat(t.Key, 64); err != nil {
					return bad("bad number %q", t.Key)
				}
			}
			terms = append(terms, t)
		}
		if !sort.SliceIsSorted(terms, func(i, j int) bool {
			if terms[i].Field != terms[j].Field {
				return terms[i].Field < terms[j].Field
			}
			return terms[i].Key < terms[j].Key
		}) {
			return bad("terms of %s are not sorted", a)
		}
		st.put(a, terms)
	}
	st.finish()
	if float64(len(st.recs)) != cnt.Value.Num || hex.EncodeToString(st.digest[:]) != digest {
		return bad("count/digest do not match the records")
	}
	sd.st = st
	return sd, nil
}
