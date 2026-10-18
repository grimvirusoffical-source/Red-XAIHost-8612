package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"

	"redx/box"
)

const manifestVersion = 1

const sealPrefix = "{Seal[]} = \""

// Info describes a file or folder.
type Info struct {
	Path      string // canonical path, "" for the root
	Name      string
	IsDir     bool
	Size      int64    // files only
	SHA256    string   // hex SHA-256 of the whole file; files only
	ChunkSize int      // files only
	Chunks    []string // chunk ids in order; files only (nil in List results)
}

// manifest is the decoded form of one folder's manifest.
type manifest struct {
	folder string
	files  map[string]*fileRec
	dirs   map[string]int64 // name -> entry id
	nextID int64
}

type fileRec struct {
	id int64
	Info
}

func newManifest(folder string) *manifest {
	return &manifest{folder: folder, files: map[string]*fileRec{}, dirs: map[string]int64{}, nextID: 1}
}

func (m *manifest) clone() *manifest {
	c := &manifest{folder: m.folder, files: make(map[string]*fileRec, len(m.files)),
		dirs: make(map[string]int64, len(m.dirs)), nextID: m.nextID}
	for n, f := range m.files {
		g := *f
		g.Chunks = append([]string(nil), f.Chunks...)
		c.files[n] = &g
	}
	for n, id := range m.dirs {
		c.dirs[n] = id
	}
	return c
}

func (m *manifest) alloc() int64 {
	id := m.nextID
	m.nextID++
	return id
}

func (m *manifest) count() int { return len(m.files) + len(m.dirs) }

func (m *manifest) encode() []byte {
	b := box.NewBox()
	b.Put(box.NewEntry("Version", nil, box.Int(manifestVersion)))
	b.Put(box.NewEntry("Folder", nil, box.String(m.folder)))
	type ent struct {
		id    int64
		isDir bool
		name  string
	}
	var ents []ent
	for n, f := range m.files {
		ents = append(ents, ent{f.id, false, n})
	}
	for n, id := range m.dirs {
		ents = append(ents, ent{id, true, n})
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].id < ents[j].id })
	for _, e := range ents {
		id := e.id
		body := box.NewBox()
		body.Put(box.NewEntry("Name", nil, box.String(e.name)))
		if e.isDir {
			b.Put(box.NewEntry("Dir", &id, box.BoxValue(body)))
			continue
		}
		f := m.files[e.name]
		body.Put(box.NewEntry("Size", nil, box.Int(f.Size)))
		body.Put(box.NewEntry("SHA256", nil, box.String(f.SHA256)))
		body.Put(box.NewEntry("ChunkSize", nil, box.Int(int64(f.ChunkSize))))
		cs := make([]box.Value, len(f.Chunks))
		for i, c := range f.Chunks {
			cs[i] = box.String(c)
		}
		body.Put(box.NewEntry("Chunks", nil, box.Array(cs...)))
		b.Put(box.NewEntry("File", &id, box.BoxValue(body)))
	}
	root := box.NewBox()
	root.Put(box.NewEntry("Manifest", nil, box.BoxValue(b)))
	text := box.Serialize(root)
	h := sha256.Sum256([]byte(text))
	return []byte(text + sealPrefix + hex.EncodeToString(h[:]) + "\";\n")
}

func unseal(data []byte) (string, error) {
	i := bytes.LastIndex(data, []byte(sealPrefix))
	if i < 0 {
		return "", corruptf("manifest has no seal (truncated?)")
	}
	rest := data[i+len(sealPrefix):]
	if len(rest) != 64+3 || string(rest[64:]) != "\";\n" {
		return "", corruptf("manifest seal is malformed")
	}
	h := sha256.Sum256(data[:i])
	if hex.EncodeToString(h[:]) != string(rest[:64]) {
		return "", corruptf("manifest checksum mismatch")
	}
	return string(data[:i]), nil
}

func intField(b *box.Box, name string, max float64) (int64, error) {
	e := b.Get(name, 0, false)
	if e == nil || e.Value.Kind != box.KindNumber {
		return 0, corruptf("missing number %s", name)
	}
	f := e.Value.Num
	if f < 0 || f != math.Trunc(f) || f > max {
		return 0, corruptf("bad %s", name)
	}
	return int64(f), nil
}

// decodeManifest verifies the seal and every field. folder is the folder the
// caller expects (a manifest stored under the wrong key is corrupt).
func decodeManifest(data []byte, folder string) (*manifest, error) {
	body, err := unseal(data)
	if err != nil {
		return nil, err
	}
	root, err := box.ParseDocument(body)
	if err != nil {
		return nil, corruptf("manifest: %v", err)
	}
	e := root.Get("Manifest", 0, false)
	if e == nil || e.Value.Kind != box.KindBox || e.Value.Box == nil || root.Len() != 1 {
		return nil, corruptf("manifest: missing Manifest box")
	}
	b := e.Value.Box
	if v, err := intField(b, "Version", 1<<20); err != nil || v != manifestVersion {
		return nil, corruptf("manifest: unsupported version")
	}
	if f := b.Get("Folder", 0, false); f == nil || f.Value.Kind != box.KindString || f.Value.Str != folder {
		return nil, corruptf("manifest does not belong to folder %q", folder)
	}
	m := newManifest(folder)
	for _, c := range b.Entries() {
		if c.Key == "Version" || c.Key == "Folder" {
			continue
		}
		id, hasID := c.ID()
		if !hasID || id < 1 || (c.Key != "File" && c.Key != "Dir") || c.Value.Kind != box.KindBox || c.Value.Box == nil {
			return nil, corruptf("manifest: unexpected entry %s", c.EKey())
		}
		body := c.Value.Box
		ne := body.Get("Name", 0, false)
		if ne == nil || ne.Value.Kind != box.KindString || checkName(ne.Value.Str) != nil {
			return nil, corruptf("manifest: bad name in %s", c.EKey())
		}
		name := ne.Value.Str
		if _, dup := m.files[name]; dup {
			return nil, corruptf("manifest: duplicate name %q", name)
		}
		if _, dup := m.dirs[name]; dup {
			return nil, corruptf("manifest: duplicate name %q", name)
		}
		if id >= m.nextID {
			m.nextID = id + 1
		}
		if c.Key == "Dir" {
			m.dirs[name] = id
			continue
		}
		f := &fileRec{id: id}
		f.Name, f.Path = name, joinPath(append(splitFolder(folder), name))
		if f.Size, err = intField(body, "Size", 1<<53); err != nil {
			return nil, err
		}
		cs, err := intField(body, "ChunkSize", MaxChunkSize)
		if err != nil || cs < 1 {
			return nil, corruptf("manifest: bad ChunkSize for %q", name)
		}
		f.ChunkSize = int(cs)
		sh := body.Get("SHA256", 0, false)
		if sh == nil || sh.Value.Kind != box.KindString || !ValidID(sh.Value.Str) {
			return nil, corruptf("manifest: bad SHA256 for %q", name)
		}
		f.SHA256 = sh.Value.Str
		ce := body.Get("Chunks", 0, false)
		if ce == nil || ce.Value.Kind != box.KindArray {
			return nil, corruptf("manifest: missing Chunks for %q", name)
		}
		for _, x := range ce.Value.Arr {
			if x.Kind != box.KindString || !ValidID(x.Str) {
				return nil, corruptf("manifest: bad chunk id for %q", name)
			}
			f.Chunks = append(f.Chunks, x.Str)
		}
		want := (f.Size + int64(f.ChunkSize) - 1) / int64(f.ChunkSize)
		if int64(len(f.Chunks)) != want {
			return nil, corruptf("manifest: %q has %d chunks, size %d needs %d", name, len(f.Chunks), f.Size, want)
		}
		m.files[name] = f
	}
	return m, nil
}

func splitFolder(folder string) []string {
	if folder == "" {
		return nil
	}
	comps, _ := CleanPath(folder)
	return comps
}

// expectedChunkLen is the byte length chunk i of f must have.
func (f *Info) expectedChunkLen(i int) int {
	if i < len(f.Chunks)-1 {
		return f.ChunkSize
	}
	return int(f.Size - int64(len(f.Chunks)-1)*int64(f.ChunkSize))
}

func (m *manifest) info(name string) (Info, bool) {
	if f, ok := m.files[name]; ok {
		i := f.Info
		i.Chunks = append([]string(nil), i.Chunks...)
		return i, true
	}
	if _, ok := m.dirs[name]; ok {
		return Info{Path: joinPath(append(splitFolder(m.folder), name)), Name: name, IsDir: true}, true
	}
	return Info{}, false
}

func (m *manifest) names() []string {
	out := make([]string, 0, m.count())
	for n := range m.files {
		out = append(out, n)
	}
	for n := range m.dirs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
