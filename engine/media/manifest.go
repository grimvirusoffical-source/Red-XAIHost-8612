package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strings"

	"redx/box"
)

const (
	manifestVersion = 1
	manifestExt     = ".red-xai-media"
	sealPrefix      = "{Seal[]} = \""
	maxManifest     = 64 << 10
)

// Meta is the manifest of one media item.
//
//	{Media[]= [
//	    {Version[]} = 1, {Name[]} = "cat.png", {Type[]} = "image", {MIME[]} = "image/png",
//	    {Size[]} = 1234, {SHA256[]} = "<hex>", {Path[]} = "data/cat.png/<token>",
//	    {Width[]} = 64, {Height[]} = 48
//	];
//	{Seal[]} = "<sha256 of every byte above>";
//
// Width/Height/DurationMs/DurationEstimated are omitted when unknown.
type Meta struct {
	Name              string
	Type              Type
	MIME              string
	Size              int64
	SHA256            string // hex SHA-256 of the whole file
	Width, Height     int
	DurationMs        int64
	DurationEstimated bool
	// Path is where the data file lives inside the pack. It changes every
	// time the item is replaced.
	Path string
}

var tokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func hex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

func (m Meta) encode() []byte {
	b := box.NewBox()
	b.Put(box.NewEntry("Version", nil, box.Int(manifestVersion)))
	b.Put(box.NewEntry("Name", nil, box.String(m.Name)))
	b.Put(box.NewEntry("Type", nil, box.String(string(m.Type))))
	b.Put(box.NewEntry("MIME", nil, box.String(m.MIME)))
	b.Put(box.NewEntry("Size", nil, box.Int(m.Size)))
	b.Put(box.NewEntry("SHA256", nil, box.String(m.SHA256)))
	b.Put(box.NewEntry("Path", nil, box.String(m.Path)))
	if m.Width > 0 && m.Height > 0 {
		b.Put(box.NewEntry("Width", nil, box.Int(int64(m.Width))))
		b.Put(box.NewEntry("Height", nil, box.Int(int64(m.Height))))
	}
	if m.DurationMs > 0 {
		b.Put(box.NewEntry("DurationMs", nil, box.Int(m.DurationMs)))
		if m.DurationEstimated {
			b.Put(box.NewEntry("DurationEstimated", nil, box.Bool(true)))
		}
	}
	root := box.NewBox()
	root.Put(box.NewEntry("Media", nil, box.BoxValue(b)))
	text := box.Serialize(root)
	h := sha256.Sum256([]byte(text))
	return []byte(text + sealPrefix + hex.EncodeToString(h[:]) + "\";\n")
}

func num(b *box.Box, name string, max float64, required bool) (int64, bool, error) {
	e := b.Get(name, 0, false)
	if e == nil {
		if required {
			return 0, false, corruptf("manifest: missing %s", name)
		}
		return 0, false, nil
	}
	f := e.Value.Num
	if e.Value.Kind != box.KindNumber || f < 0 || f != math.Trunc(f) || f > max {
		return 0, false, corruptf("manifest: bad %s", name)
	}
	return int64(f), true, nil
}

func str(b *box.Box, name string) (string, error) {
	e := b.Get(name, 0, false)
	if e == nil || e.Value.Kind != box.KindString {
		return "", corruptf("manifest: missing string %s", name)
	}
	return e.Value.Str, nil
}

// decodeMeta verifies the seal and every field. dataPrefix is the pack
// folder the item's data must live under ("<root>/data/<name>/").
func decodeMeta(data []byte, name, dataPrefix string) (Meta, error) {
	var m Meta
	i := bytes.LastIndex(data, []byte(sealPrefix))
	if i < 0 {
		return m, corruptf("manifest has no seal (truncated?)")
	}
	rest := data[i+len(sealPrefix):]
	if len(rest) != 64+3 || string(rest[64:]) != "\";\n" {
		return m, corruptf("manifest seal is malformed")
	}
	h := sha256.Sum256(data[:i])
	if hex.EncodeToString(h[:]) != string(rest[:64]) {
		return m, corruptf("manifest checksum mismatch")
	}
	root, err := box.ParseDocument(string(data[:i]))
	if err != nil {
		return m, corruptf("manifest: %v", err)
	}
	e := root.Get("Media", 0, false)
	if e == nil || e.Value.Kind != box.KindBox || e.Value.Box == nil || root.Len() != 1 {
		return m, corruptf("manifest: missing Media box")
	}
	b := e.Value.Box
	if v, _, err := num(b, "Version", 1<<20, true); err != nil || v != manifestVersion {
		return m, corruptf("manifest: unsupported version")
	}
	if m.Name, err = str(b, "Name"); err != nil || m.Name != name {
		return m, corruptf("manifest does not belong to %q", name)
	}
	t, err := str(b, "Type")
	if err != nil {
		return m, err
	}
	m.Type = Type(t)
	if m.MIME, err = str(b, "MIME"); err != nil {
		return m, err
	}
	if (m.Type != TypeImage && m.Type != TypeAudio && m.Type != TypeVideo) ||
		NormalizeMIME(m.MIME) != m.MIME || !strings.HasPrefix(m.MIME, t+"/") {
		return m, corruptf("manifest: bad type or MIME")
	}
	if m.Size, _, err = num(b, "Size", 1<<53, true); err != nil {
		return m, err
	}
	if m.Size < 1 {
		return m, corruptf("manifest: bad Size")
	}
	if m.SHA256, err = str(b, "SHA256"); err != nil || !hex64(m.SHA256) {
		return m, corruptf("manifest: bad SHA256")
	}
	if m.Path, err = str(b, "Path"); err != nil {
		return m, err
	}
	if !strings.HasPrefix(m.Path, dataPrefix) || !tokenRE.MatchString(m.Path[len(dataPrefix):]) {
		return m, corruptf("manifest: bad Path")
	}
	w, wok, err := num(b, "Width", maxDimension, false)
	if err != nil {
		return m, err
	}
	hh, hok, err := num(b, "Height", maxDimension, false)
	if err != nil {
		return m, err
	}
	if wok != hok || (wok && (w < 1 || hh < 1)) {
		return m, corruptf("manifest: bad dimensions")
	}
	m.Width, m.Height = int(w), int(hh)
	if m.DurationMs, _, err = num(b, "DurationMs", 1<<50, false); err != nil {
		return m, err
	}
	if de := b.Get("DurationEstimated", 0, false); de != nil {
		if de.Value.Kind != box.KindBool || m.DurationMs == 0 {
			return m, corruptf("manifest: bad DurationEstimated")
		}
		m.DurationEstimated = de.Value.Bool
	}
	return m, nil
}
