package pack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func newMem(t testing.TB, cs int) (*Pack, *MemBackend) {
	t.Helper()
	b := NewMemBackend()
	p, err := New(b, b, Config{ChunkSize: cs})
	if err != nil {
		t.Fatal(err)
	}
	return p, b
}

func randBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func mustPut(t testing.TB, p *Pack, path string, data []byte) Info {
	t.Helper()
	i, err := p.Put(path, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put %s: %v", path, err)
	}
	return i
}

func mustGet(t testing.TB, p *Pack, path string) []byte {
	t.Helper()
	rc, _, err := p.Get(path)
	if err != nil {
		t.Fatalf("Get %s: %v", path, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// ---- path safety ----------------------------------------------------------

func TestCleanPath(t *testing.T) {
	good := map[string][]string{
		"":                       nil,
		"/":                      nil,
		"a":                      {"a"},
		"a/b/c.txt":              {"a", "b", "c.txt"},
		"dir with space/é日本.txt": {"dir with space", "é日本.txt"},
		"%2e%2e/x":               {"%2e%2e", "x"}, // not decoded, so just a name
		"...":                    nil,             // placeholder, replaced below
		".hidden":                {".hidden"},
		"a..b":                   {"a..b"},
		"console":                {"console"},
		"com10":                  {"com10"},
		"-rf":                    {"-rf"},
	}
	delete(good, "...")
	for in, want := range good {
		got, err := CleanPath(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
	bad := []string{
		"..", "../x", "a/../b", "a/..", "../../etc/passwd", "a/b/../../..", ".", "./a", "a/./b",
		"/etc/passwd", "//x", "a//b", "a/", "a/b/",
		`a\b`, `..\x`, `C:\Windows`, "c:x", "a:b", "file.txt:stream", `\\server\share`,
		"x\x00y", "x\ny", "x\ty", "a\x7fb", "a\u0085b",
		"CON", "con.txt", "a/NUL", "aux.tar.gz", "COM1", "lpt9.log", "Com3.txt", "CONIN$",
		"a.", "a ", "a/b. ", "dir./x",
		"a<b", "a>b", `a"b`, "a|b", "a?b", "a*b",
		"a\u202eb", "\ufeffa", "a\u2066b",
		"bad\xffutf8", strings.Repeat("a", MaxNameBytes+1), strings.Repeat("a/", MaxDepth) + "a",
		strings.Repeat("abcdefgh/", 600),
	}
	for _, in := range bad {
		if _, err := CleanPath(in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%q: expected ErrInvalidPath, got %v", in, err)
		}
	}
	if _, err := CleanPath(strings.Repeat("a", MaxNameBytes)); err != nil {
		t.Errorf("255-byte name should be fine: %v", err)
	}
}

func TestBadPathsNeverTouchBackend(t *testing.T) {
	p, b := newMem(t, 8)
	b.Hook = func(op, id string) error { t.Errorf("backend mutated: %s", op); return nil }
	for _, path := range []string{"../evil", "a/../../evil", "/abs", `a\b`, "a/", "", "/"} {
		if _, err := p.Put(path, strings.NewReader("x")); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Put %q: %v", path, err)
		}
	}
	if err := p.Mkdir("../x"); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if _, _, err := p.Get("../x"); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if err := p.Delete("../x"); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if err := p.RemoveDir("..", true); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if _, err := p.List("a/../.."); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if _, err := p.Stat("a\x00"); !errors.Is(err, ErrInvalidPath) {
		t.Error(err)
	}
	if err := p.RemoveDir("", true); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("root removal: %v", err)
	}
}

func TestBackendRejectsBadIDs(t *testing.T) {
	root := t.TempDir()
	d := DirBackend{Root: filepath.Join(root, "p")}
	for _, id := range []string{"../../x", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64), "", strings.Repeat("a", 63) + "/"} {
		if err := d.PutChunk(id, []byte("x")); err == nil {
			t.Errorf("PutChunk accepted %q", id)
		}
		if _, err := d.GetChunk(id); err == nil || errors.Is(err, ErrChunkNotFound) {
			t.Errorf("GetChunk accepted %q: %v", id, err)
		}
		if err := d.SaveManifest(id, []byte("x")); err == nil {
			t.Errorf("SaveManifest accepted %q", id)
		}
		if err := d.DeleteChunk(id); err == nil {
			t.Errorf("DeleteChunk accepted %q", id)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("escaped the root")
	}
}

// ---- round trips ----------------------------------------------------------

func TestPutGetSizes(t *testing.T) {
	const cs = 16
	for _, n := range []int{0, 1, 15, 16, 17, 31, 32, 33, 160, 163, 1000} {
		p, _ := newMem(t, cs)
		data := randBytes(int64(n), n)
		info := mustPut(t, p, "docs/f.bin", data)
		wantChunks := (n + cs - 1) / cs
		if info.Size != int64(n) || len(info.Chunks) != wantChunks || info.ChunkSize != cs || !ValidID(info.SHA256) {
			t.Fatalf("n=%d info %+v", n, info)
		}
		if info.SHA256 != ChunkID(data) { // same hash function over the whole file
			t.Fatalf("n=%d file hash wrong", n)
		}
		if got := mustGet(t, p, "docs/f.bin"); !bytes.Equal(got, data) {
			t.Fatalf("n=%d content differs", n)
		}
		rc, _, _ := p.Get("docs/f.bin")
		got, err := io.ReadAll(iotest.OneByteReader(rc))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("n=%d one-byte reads: %v", n, err)
		}
		// Put from a reader that returns tiny pieces gives the same result
		info2, err := p.Put("docs/g.bin", iotest.OneByteReader(bytes.NewReader(data)))
		if err != nil || info2.SHA256 != info.SHA256 || !reflect.DeepEqual(info2.Chunks, info.Chunks) {
			t.Fatalf("n=%d chunking depends on reader: %v", n, err)
		}
		if err := p.Verify("docs/f.bin"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStreamingLargeFile(t *testing.T) {
	p, b := newMem(t, 64<<10)
	const total = 5 << 20
	src := io.LimitReader(rand.New(rand.NewSource(7)), total)
	h := &hashW{}
	info, err := p.Put("big/data.bin", io.TeeReader(src, h))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != total || len(info.Chunks) != total/(64<<10) {
		t.Fatalf("%+v", info)
	}
	rc, _, err := p.Get("big/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(h2{h}, rc)
	if err != nil || n != total {
		t.Fatal(n, err)
	}
	if !bytes.Equal(h.buf.Bytes(), h.second.Bytes()) {
		t.Fatal("streamed read differs from what was written")
	}
	if cs, _ := b.ListChunks(); len(cs) != total/(64<<10) {
		t.Fatalf("%d chunks", len(cs))
	}
}

type hashW struct{ buf, second bytes.Buffer }

func (h *hashW) Write(p []byte) (int, error) { return h.buf.Write(p) }

type h2 struct{ h *hashW }

func (w h2) Write(p []byte) (int, error) { return w.h.second.Write(p) }

func TestDedupAndOverwrite(t *testing.T) {
	p, b := newMem(t, 8)
	zeros := make([]byte, 80) // ten identical chunks
	i := mustPut(t, p, "z", zeros)
	if len(i.Chunks) != 10 {
		t.Fatal(len(i.Chunks))
	}
	if cs, _ := b.ListChunks(); len(cs) != 1 {
		t.Fatalf("identical chunks should be stored once, got %d", len(cs))
	}
	data := randBytes(1, 40)
	mustPut(t, p, "a/one", data)
	mustPut(t, p, "b/two", data)
	cs, _ := b.ListChunks()
	if len(cs) != 1+5 {
		t.Fatalf("files with the same content should share chunks: %d", len(cs))
	}
	// overwrite replaces content
	mustPut(t, p, "a/one", []byte("short"))
	if got := mustGet(t, p, "a/one"); string(got) != "short" {
		t.Fatalf("%q", got)
	}
	if got := mustGet(t, p, "b/two"); !bytes.Equal(got, data) {
		t.Fatal("other file affected")
	}
	// GC drops chunks only a/one used; b/two's survive
	r, err := p.GC()
	if err != nil || r.ChunksRemoved != 0 {
		t.Fatalf("shared chunks must survive: %+v %v", r, err)
	}
	mustPut(t, p, "b/two", []byte("x"))
	r, err = p.GC()
	if err != nil || r.ChunksRemoved != 5 {
		t.Fatalf("%+v %v", r, err)
	}
	if rep, _ := p.Fsck(true); !rep.OK() || len(rep.OrphanChunks) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestFoldersListStatDelete(t *testing.T) {
	p, _ := newMem(t, 8)
	mustPut(t, p, "docs/b.txt", []byte("bbb"))
	mustPut(t, p, "docs/a.txt", []byte("a"))
	mustPut(t, p, "docs/img/x.png", []byte("png"))
	mustPut(t, p, "top.txt", []byte("t"))
	if err := p.Mkdir("empty/deeper"); err != nil {
		t.Fatal(err)
	}
	if err := p.Mkdir("docs/img"); err != nil { // existing: fine
		t.Fatal(err)
	}

	l, err := p.List("docs")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range l {
		names = append(names, i.Name)
		if i.Chunks != nil {
			t.Fatal("List must not return chunk lists")
		}
	}
	if !reflect.DeepEqual(names, []string{"a.txt", "b.txt", "img"}) || !l[2].IsDir || l[1].Size != 3 {
		t.Fatalf("%+v", l)
	}
	root, _ := p.List("")
	if len(root) != 3 || root[0].Name != "docs" || root[1].Name != "empty" || root[2].Name != "top.txt" {
		t.Fatalf("%+v", root)
	}
	if l, err := p.List("empty/deeper"); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	st, err := p.Stat("docs/b.txt")
	if err != nil || st.Size != 3 || len(st.Chunks) != 1 || st.Path != "docs/b.txt" || st.IsDir {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err := p.Stat("docs"); err != nil || !st.IsDir {
		t.Fatal(st, err)
	}
	if st, err := p.Stat(""); err != nil || !st.IsDir {
		t.Fatal(st, err)
	}
	var walked []string
	if err := p.Walk("", func(i Info) error { walked = append(walked, i.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"docs", "docs/a.txt", "docs/b.txt", "docs/img", "docs/img/x.png", "empty", "empty/deeper", "top.txt"}
	if !reflect.DeepEqual(walked, want) {
		t.Fatalf("%v", walked)
	}
	stop := errors.New("stop")
	if err := p.Walk("", func(Info) error { return stop }); err != stop {
		t.Fatal(err)
	}

	// errors
	if _, err := p.List("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List("top.txt"); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
	if _, err := p.List("top.txt/x"); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
	if _, _, err := p.Get("docs"); !errors.Is(err, ErrIsDir) {
		t.Fatal(err)
	}
	if _, _, err := p.Get("docs/zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, _, err := p.Get("nodir/zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.Stat("docs/zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.Put("docs", strings.NewReader("x")); !errors.Is(err, ErrIsDir) {
		t.Fatalf("file over folder: %v", err)
	}
	if _, err := p.Put("top.txt/inside", strings.NewReader("x")); !errors.Is(err, ErrNotDir) {
		t.Fatalf("folder under file: %v", err)
	}
	if err := p.Mkdir("top.txt/inside"); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
	if err := p.Delete("docs"); !errors.Is(err, ErrIsDir) {
		t.Fatal(err)
	}
	if err := p.Delete("docs/zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := p.RemoveDir("docs", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatal(err)
	}
	if err := p.RemoveDir("top.txt", true); !errors.Is(err, ErrNotDir) {
		t.Fatal(err)
	}
	if err := p.RemoveDir("ghost", true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	// deletes
	if err := p.Delete("docs/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Get("docs/a.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := p.RemoveDir("empty", false); err == nil { // has a sub-folder, not empty
		t.Fatal("empty/ contains deeper/")
	}
	if err := p.RemoveDir("empty/deeper", false); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveDir("docs", true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat("docs/img/x.png"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// the name can be reused as a file
	mustPut(t, p, "docs", []byte("now a file"))
	if string(mustGet(t, p, "docs")) != "now a file" {
		t.Fatal()
	}
	res, err := p.GC()
	if err != nil || res.ChunksRemoved == 0 || res.ManifestsRemoved != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if rep, _ := p.Fsck(true); !rep.OK() || len(rep.OrphanChunks)+len(rep.OrphanManifests) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestLimitsAndConfig(t *testing.T) {
	b := NewMemBackend()
	for _, c := range []Config{{ChunkSize: -1}, {ChunkSize: MaxChunkSize + 1}, {MaxFileSize: -5}, {MaxFileSize: 1 << 60}} {
		if _, err := New(b, b, c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := New(nil, b, Config{}); err == nil {
		t.Error("nil store accepted")
	}
	p, err := New(b, b, Config{ChunkSize: 4, MaxFileSize: 10, MaxFolderEntries: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Put("ok", bytes.NewReader(make([]byte, 10))); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Put("big", bytes.NewReader(make([]byte, 11))); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	if _, _, err := p.Get("big"); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected file must not appear")
	}
	mustPut(t, p, "f2", nil)
	mustPut(t, p, "f3", nil)
	if _, err := p.Put("f4", strings.NewReader("")); !errors.Is(err, ErrFolderFull) {
		t.Fatal(err)
	}
	if err := p.Mkdir("d"); !errors.Is(err, ErrFolderFull) {
		t.Fatal(err)
	}
	mustPut(t, p, "f2", []byte("overwrite is fine when full"[:9])) // 9 < 10
	// read error from the source aborts the Put
	if _, err := p.Put("bad", iotest.ErrReader(errors.New("boom"))); err == nil {
		t.Fatal("expected error")
	}
}

// ---- corruption -----------------------------------------------------------

func setup(t *testing.T) (*Pack, *MemBackend, Info, []byte) {
	t.Helper()
	p, b := newMem(t, 10)
	data := randBytes(3, 95) // 10 chunks, last has 5 bytes
	info := mustPut(t, p, "d/file", data)
	if len(info.Chunks) != 10 {
		t.Fatal(len(info.Chunks))
	}
	return p, b, info, data
}

func readAllErr(t *testing.T, p *Pack, path string) ([]byte, error) {
	t.Helper()
	rc, _, err := p.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func TestTruncatedChunk(t *testing.T) {
	for _, idx := range []int{0, 4, 9} {
		p, b, info, data := setup(t)
		id := info.Chunks[idx]
		c, _ := b.GetChunk(id)
		b.SetRawChunk(id, c[:len(c)-1]) // torn write: one byte short
		got, err := readAllErr(t, p, "d/file")
		var ce *ChunkError
		if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) || ce.Index != idx || ce.ID != id {
			t.Fatalf("idx %d: %v", idx, err)
		}
		if !strings.Contains(ce.Why, "truncated") {
			t.Fatalf("%v", ce)
		}
		// everything before the bad chunk was delivered, nothing of it
		if !bytes.Equal(got, data[:idx*10]) {
			t.Fatalf("idx %d: delivered %d bytes, want %d", idx, len(got), idx*10)
		}
		if err := p.Verify("d/file"); !errors.Is(err, ErrCorrupt) {
			t.Fatal(err)
		}
		rep, err := p.Fsck(true)
		if err != nil || rep.OK() || len(rep.Problems) != 1 || rep.Problems[0].Chunk != idx || !errors.Is(rep.Problems[0].Err, ErrCorrupt) {
			t.Fatalf("%+v %v", rep, err)
		}
		// shallow fsck cannot see a size problem; it only checks existence
		if rep, _ := p.Fsck(false); !rep.OK() {
			t.Fatalf("shallow fsck should not read chunks: %+v", rep)
		}
	}
}

func TestTruncatedChunkToZeroAndExtended(t *testing.T) {
	p, b, info, _ := setup(t)
	b.SetRawChunk(info.Chunks[2], nil)
	if _, err := readAllErr(t, p, "d/file"); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	p, b, info, _ = setup(t)
	c, _ := b.GetChunk(info.Chunks[9])
	b.SetRawChunk(info.Chunks[9], append(c, 'x')) // extended last chunk
	if _, err := readAllErr(t, p, "d/file"); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestBitFlipAndSwappedChunk(t *testing.T) {
	p, b, info, data := setup(t)
	c, _ := b.GetChunk(info.Chunks[3])
	c[0] ^= 0x80
	b.SetRawChunk(info.Chunks[3], c)
	got, err := readAllErr(t, p, "d/file")
	var ce *ChunkError
	if !errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) || ce.Index != 3 || !strings.Contains(ce.Why, "hash") {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[:30]) {
		t.Fatal("bytes of the corrupt chunk leaked")
	}
	// the error is sticky
	rc, _, _ := p.Get("d/file")
	buf := make([]byte, 1000)
	var first, second error
	for first == nil {
		_, first = rc.Read(buf)
	}
	_, second = rc.Read(buf)
	if first != second {
		t.Fatalf("not sticky: %v vs %v", first, second)
	}
	// a valid chunk stored under another chunk's id is caught by the hash
	p, b, info, _ = setup(t)
	other, _ := b.GetChunk(info.Chunks[1])
	b.SetRawChunk(info.Chunks[0], other)
	if _, err := readAllErr(t, p, "d/file"); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestMissingChunk(t *testing.T) {
	p, b, info, data := setup(t)
	b.DeleteChunk(info.Chunks[5])
	got, err := readAllErr(t, p, "d/file")
	var ce *ChunkError
	if !errors.Is(err, ErrMissing) || errors.Is(err, ErrCorrupt) || !errors.As(err, &ce) || ce.Index != 5 {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data[:50]) {
		t.Fatal("wrong bytes before the missing chunk")
	}
	for _, deep := range []bool{false, true} {
		rep, err := p.Fsck(deep)
		if err != nil || len(rep.Problems) != 1 || rep.Problems[0].Chunk != 5 || !errors.Is(rep.Problems[0].Err, ErrMissing) {
			t.Fatalf("deep=%v %+v %v", deep, rep, err)
		}
	}
}

func TestWholeFileHashMismatch(t *testing.T) {
	// every chunk is individually valid, but the manifest's chunk order
	// does not give the recorded file hash
	p, b := newMem(t, 10)
	data := randBytes(5, 30)
	info := mustPut(t, p, "f", data)
	m, _ := p.load("")
	m.files["f"].Chunks[0], m.files["f"].Chunks[1] = info.Chunks[1], info.Chunks[0]
	if err := p.save(m); err != nil {
		t.Fatal(err)
	}
	_ = b
	if _, err := readAllErr(t, p, "f"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	rep, _ := p.Fsck(true)
	if rep.OK() || rep.Problems[0].Chunk != -1 {
		t.Fatalf("%+v", rep)
	}
}

func TestManifestCorruption(t *testing.T) {
	good := func() (*Pack, *MemBackend, []byte) {
		p, b := newMem(t, 10)
		mustPut(t, p, "d/f", randBytes(1, 25))
		mustPut(t, p, "g", randBytes(2, 5))
		d, _ := b.LoadManifest(FolderKey("d"))
		return p, b, d
	}
	cases := map[string]func([]byte) []byte{
		"truncated half": func(d []byte) []byte { return d[:len(d)/2] },
		"truncated tail": func(d []byte) []byte { return d[:len(d)-3] },
		"empty":          func([]byte) []byte { return nil },
		"flipped":        func(d []byte) []byte { d = append([]byte(nil), d...); d[len(d)/2] ^= 1; return d },
		"junk":           func([]byte) []byte { return []byte("not a manifest") },
		"seal removed": func(d []byte) []byte {
			return d[:bytes.LastIndex(d, []byte(sealPrefix))]
		},
	}
	for name, mut := range cases {
		p, b, d := good()
		b.SetRawManifest(FolderKey("d"), mut(d))
		for _, op := range []func() error{
			func() error { _, err := p.List("d"); return err },
			func() error { _, _, err := p.Get("d/f"); return err },
			func() error { _, err := p.Put("d/new", strings.NewReader("x")); return err },
			func() error { return p.Delete("d/f") },
		} {
			if err := op(); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s: %v", name, err)
			}
		}
		// the root and unrelated folders keep working
		if got := mustGet(t, p, "g"); len(got) != 5 {
			t.Errorf("%s: unrelated file broken", name)
		}
		rep, err := p.Fsck(false)
		if err != nil || rep.OK() || rep.Problems[0].Path != "d" || !errors.Is(rep.Problems[0].Err, ErrCorrupt) || !rep.OrphansSkipped {
			t.Errorf("%s: %+v %v", name, rep, err)
		}
		// GC refuses to delete anything on an incomplete view
		nc, _ := b.ListChunks()
		if _, err := p.GC(); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: gc %v", name, err)
		}
		if after, _ := b.ListChunks(); len(after) != len(nc) {
			t.Errorf("%s: gc deleted chunks despite corrupt manifest", name)
		}
	}
	// a valid manifest stored under another folder's key is rejected
	p, b, d := good()
	b.SetRawManifest(FolderKey("other"), d)
	if _, err := p.load("other"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("misplaced manifest accepted: %v", err)
	}
}

func TestManifestSemanticTamper(t *testing.T) {
	// A manifest that is correctly sealed but inconsistent (someone
	// re-sealed an edited file): the decoder still checks the content.
	p, b := newMem(t, 10)
	mustPut(t, p, "f", randBytes(1, 25))
	data, _ := b.LoadManifest(FolderKey(""))
	body, err := unseal(data)
	if err != nil {
		t.Fatal(err)
	}
	reseal := func(s string) []byte {
		return []byte(s + sealPrefix + ChunkID([]byte(s)) + "\";\n")
	}
	tamper := map[string]string{
		"size changed":   strings.Replace(body, "{Size[]} = 25", "{Size[]} = 35", 1),
		"size negative":  strings.Replace(body, "{Size[]} = 25", "{Size[]} = -1", 1),
		"name traversal": strings.Replace(body, `{Name[]} = "f"`, `{Name[]} = "../f"`, 1),
		"wrong folder":   strings.Replace(body, `{Folder[]} = ""`, `{Folder[]} = "x"`, 1),
		"bad version":    strings.Replace(body, "{Version[]} = 1", "{Version[]} = 2", 1),
		"chunksize zero": strings.Replace(body, "{ChunkSize[]} = 10", "{ChunkSize[]} = 0", 1),
		"unknown entry":  strings.Replace(body, "{File[ID=1]}", "{Weird[ID=1]}", 1),
	}
	for name, s := range tamper {
		if s == body {
			t.Fatalf("%s: replacement did not apply to:\n%s", name, body)
		}
		b.SetRawManifest(FolderKey(""), reseal(s))
		if _, err := p.List(""); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a size edit that keeps the chunk count passes the manifest checks but
	// is caught when the data is read
	b.SetRawManifest(FolderKey(""), reseal(strings.Replace(body, "{Size[]} = 25", "{Size[]} = 26", 1)))
	if _, err := readAllErr(t, p, "f"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("size edit not caught on read: %v", err)
	}
	// sanity: the untouched body re-sealed is accepted
	b.SetRawManifest(FolderKey(""), reseal(body))
	if _, err := p.List(""); err != nil {
		t.Fatal(err)
	}
}

// ---- crashes --------------------------------------------------------------

type crasher struct {
	mu    sync.Mutex
	n     int // fail the n-th mutating call (1-based); 0 = never
	count int
	log   []string
}

func (c *crasher) hook(op, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	c.log = append(c.log, op)
	if c.n != 0 && c.count == c.n {
		return fmt.Errorf("crash before %s #%d", op, c.count)
	}
	return nil
}

func TestCrashAtEveryStepOfPut(t *testing.T) {
	old := randBytes(1, 33)
	newData := randBytes(2, 57)
	// first count the mutating calls of a nested Put over an existing file
	probe := func() int {
		p, b := newMem(t, 8)
		mustPut(t, p, "keep/other", randBytes(9, 20))
		mustPut(t, p, "a/b/c/f", old)
		c := &crasher{}
		b.Hook = c.hook
		mustPut(t, p, "a/b/c/f", newData)
		mustPut(t, p, "x/y/z/new", newData)
		return c.count
	}()
	if probe < 8 {
		t.Fatalf("suspiciously few steps: %d", probe)
	}
	for k := 1; k <= probe; k++ {
		p, b := newMem(t, 8)
		otherData := randBytes(9, 20)
		mustPut(t, p, "keep/other", otherData)
		mustPut(t, p, "a/b/c/f", old)
		c := &crasher{n: k}
		b.Hook = c.hook
		_, err1 := p.Put("a/b/c/f", bytes.NewReader(newData))
		_, err2 := p.Put("x/y/z/new", bytes.NewReader(newData))
		b.Hook = nil

		// after any crash the store is consistent
		rep, err := p.Fsck(true)
		if err != nil || !rep.OK() {
			t.Fatalf("k=%d: fsck: %+v %v", k, rep, err)
		}
		if got := mustGet(t, p, "keep/other"); !bytes.Equal(got, otherData) {
			t.Fatalf("k=%d: unrelated file damaged", k)
		}
		// the overwritten file is entirely old or entirely new, never mixed
		got := mustGet(t, p, "a/b/c/f")
		switch {
		case bytes.Equal(got, old):
			if err1 == nil {
				t.Fatalf("k=%d: Put reported success but old data is still there", k)
			}
		case bytes.Equal(got, newData):
		default:
			t.Fatalf("k=%d: file is neither old nor new", k)
		}
		if err1 == nil && !bytes.Equal(got, newData) {
			t.Fatalf("k=%d: success without new data", k)
		}
		// the new file either exists completely or not at all
		if _, _, err := p.Get("x/y/z/new"); err == nil {
			if !bytes.Equal(mustGet(t, p, "x/y/z/new"), newData) {
				t.Fatalf("k=%d: partial new file", k)
			}
			if err2 != nil {
				t.Fatalf("k=%d: Put failed but file exists", k)
			}
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("k=%d: %v", k, err)
		}
		// no folder is unreachable: every stored manifest is reachable (no orphan manifests)
		if len(rep.OrphanManifests) != 0 {
			t.Fatalf("k=%d: orphan manifests %v", k, rep.OrphanManifests)
		}
		// retrying works, then GC leaves a clean store
		mustPut(t, p, "a/b/c/f", newData)
		mustPut(t, p, "x/y/z/new", newData)
		if _, err := p.GC(); err != nil {
			t.Fatal(err)
		}
		rep, _ = p.Fsck(true)
		if !rep.OK() || len(rep.OrphanChunks) != 0 || len(rep.OrphanManifests) != 0 {
			t.Fatalf("k=%d: after retry+gc %+v", k, rep)
		}
		_ = err2
	}
}

func TestCrashDuringRemoveDirLeavesOrphansForGC(t *testing.T) {
	p, b := newMem(t, 8)
	mustPut(t, p, "a/b/f", randBytes(1, 30))
	mustPut(t, p, "keep", randBytes(2, 9))
	calls := 0
	b.Hook = func(op, id string) error {
		if op == "delete-manifest" {
			calls++
			if calls == 2 {
				return errors.New("crash")
			}
		}
		return nil
	}
	if err := p.RemoveDir("a", true); err == nil {
		t.Fatal("expected crash error")
	}
	b.Hook = nil
	// the folder is already gone for readers
	if _, err := p.Stat("a"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	rep, _ := p.Fsck(true)
	if !rep.OK() || len(rep.OrphanManifests) == 0 || len(rep.OrphanChunks) == 0 {
		t.Fatalf("%+v", rep)
	}
	res, err := p.GC()
	if err != nil || res.ManifestsRemoved == 0 || res.ChunksRemoved == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	rep, _ = p.Fsck(true)
	if !rep.OK() || len(rep.OrphanChunks)+len(rep.OrphanManifests) != 0 {
		t.Fatalf("%+v", rep)
	}
	if len(mustGet(t, p, "keep")) != 9 {
		t.Fatal()
	}
	// the name can be created again
	mustPut(t, p, "a/b/f", []byte("again"))
}

// ---- directory backend ----------------------------------------------------

func TestDirBackendPersistsAndSurvivesDamage(t *testing.T) {
	root := t.TempDir()
	be := DirBackend{Root: root}
	p, err := New(be, be, Config{ChunkSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"readme.md":            []byte("hello"),
		"data/a.bin":           randBytes(1, 5000),
		"data/sub/b.bin":       randBytes(2, 1024),
		"data/sub/empty":       nil,
		"data/sub/dup.bin":     randBytes(1, 5000), // same as a.bin
		"spaces é/日本 file.txt": []byte("unicode"),
	}
	for n, d := range files {
		mustPut(t, p, n, d)
	}
	// a crash left temp files behind: ignored by listings
	os.WriteFile(filepath.Join(root, "chunks", ".tmp-1234"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "chunks", "ab"), 0o755)
	os.WriteFile(filepath.Join(root, "chunks", "ab", ".tmp-9"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "manifests", ".tmp-77"), []byte("x"), 0o644)

	// reopen from disk with a fresh Pack
	p2, _ := New(be, be, Config{ChunkSize: 4096}) // different chunk size: old files keep theirs
	for n, d := range files {
		if got := mustGet(t, p2, n); !bytes.Equal(got, d) {
			t.Fatalf("%s differs after reopen", n)
		}
	}
	rep, err := p2.Fsck(true)
	if err != nil || !rep.OK() || len(rep.OrphanChunks) != 0 || len(rep.OrphanManifests) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if n, err := be.RemoveTemp(); err != nil || n != 3 {
		t.Fatalf("RemoveTemp %d %v", n, err)
	}
	cs, _ := be.ListChunks()
	// 5000 bytes = 5 chunks (1024*4+904), shared by a.bin and dup.bin; plus b.bin's chunk (1024); plus readme and unicode
	st, _ := p2.Stat("data/a.bin")
	if len(st.Chunks) != 5 {
		t.Fatal(len(st.Chunks))
	}
	if len(cs) > 5+1+2 {
		t.Fatalf("dedupe not effective on disk: %d chunks", len(cs))
	}

	// truncate a chunk file on disk
	id := st.Chunks[2]
	path := filepath.Join(root, "chunks", id[:2], id)
	if err := os.Truncate(path, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := readAllErr(t, p2, "data/a.bin"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("%v", err)
	}
	// dup.bin shares the damaged chunk, b.bin does not
	if _, err := readAllErr(t, p2, "data/sub/dup.bin"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("%v", err)
	}
	if got := mustGet(t, p2, "data/sub/b.bin"); !bytes.Equal(got, files["data/sub/b.bin"]) {
		t.Fatal("unrelated file")
	}
	rep, _ = p2.Fsck(true)
	if len(rep.Problems) != 2 {
		t.Fatalf("both files sharing the chunk should be reported: %+v", rep.Problems)
	}
	// re-uploading heals the chunk (dedupe verifies the stored chunk before trusting it)
	mustPut(t, p, "data/a.bin", files["data/a.bin"]) // p uses the same chunk size as the original upload
	if got := mustGet(t, p2, "data/sub/dup.bin"); !bytes.Equal(got, files["data/a.bin"]) {
		t.Fatal("not healed")
	}
	// delete a chunk file
	os.Remove(path)
	if _, err := readAllErr(t, p2, "data/a.bin"); !errors.Is(err, ErrMissing) {
		t.Fatalf("%v", err)
	}
	// truncated manifest on disk
	mp := filepath.Join(root, "manifests", FolderKey("data")+".manifest")
	st2, _ := os.Stat(mp)
	os.Truncate(mp, st2.Size()/2)
	if _, err := p2.List("data"); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

// ---- reader details -------------------------------------------------------

func TestReaderClose(t *testing.T) {
	p, _ := newMem(t, 4)
	mustPut(t, p, "f", []byte("0123456789"))
	rc, _, _ := p.Get("f")
	buf := make([]byte, 3)
	if n, err := rc.Read(buf); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := rc.Read(nil); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	rc.Close()
	if _, err := rc.Read(buf); err == nil || err == io.EOF {
		t.Fatalf("read after close: %v", err)
	}
	// reading a file to a clean EOF
	rc, _, _ = p.Get("f")
	b, err := io.ReadAll(rc)
	if err != nil || string(b) != "0123456789" {
		t.Fatal(string(b), err)
	}
	if n, err := rc.Read(buf); n != 0 || err != io.EOF {
		t.Fatal(n, err)
	}
	if err := iotest.TestReader(mustReader(t, p, "f"), []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
}

func mustReader(t *testing.T, p *Pack, path string) io.Reader {
	rc, _, err := p.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

// ---- concurrency ----------------------------------------------------------

func TestConcurrentPutGetListDeleteGC(t *testing.T) {
	p, b := newMem(t, 16)
	const workers = 8
	const perWorker = 25
	contents := func(w, i int) []byte { return randBytes(int64(w*1000+i), 20+(w*7+i*3)%90) }
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// same folder for everyone, plus a private folder
				path := fmt.Sprintf("shared/w%d-%d", w, i)
				if _, err := p.Put(path, bytes.NewReader(contents(w, i))); err != nil {
					t.Errorf("put %s: %v", path, err)
					return
				}
				if _, err := p.Put(fmt.Sprintf("priv%d/f%d", w, i%3), bytes.NewReader(contents(w, i))); err != nil {
					t.Errorf("put priv: %v", err)
					return
				}
				if i%5 == 4 {
					if err := p.Delete(fmt.Sprintf("shared/w%d-%d", w, i-1)); err != nil {
						t.Errorf("delete: %v", err)
						return
					}
				}
			}
		}(w)
	}
	var rwg sync.WaitGroup
	for r := 0; r < 3; r++ {
		rwg.Add(1)
		go func(r int) {
			defer rwg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				time.Sleep(3 * time.Millisecond)
				l, err := p.List("shared")
				if err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("list: %v", err)
					return
				}
				for _, e := range l {
					rc, _, err := p.Get("shared/" + e.Name)
					if errors.Is(err, ErrNotFound) {
						continue // deleted meanwhile
					}
					if err != nil {
						t.Errorf("get: %v", err)
						return
					}
					if _, err := io.Copy(io.Discard, rc); err != nil && !errors.Is(err, ErrMissing) {
						t.Errorf("read: %v", err) // missing is legal only if the file was deleted and GC ran
						return
					} else if errors.Is(err, ErrMissing) {
						if _, serr := p.Stat("shared/" + e.Name); serr == nil {
							t.Errorf("chunk of a live file went missing")
							return
						}
					}
				}
				if r == 0 {
					if _, err := p.GC(); err != nil {
						t.Errorf("gc: %v", err)
						return
					}
				}
			}
		}(r)
	}
	wg.Wait()
	close(stop)
	rwg.Wait()

	l, err := p.List("shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != workers*perWorker-workers*(perWorker/5) {
		t.Fatalf("%d files in shared", len(l))
	}
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			if i%5 == 3 {
				continue // deleted
			}
			if got := mustGet(t, p, fmt.Sprintf("shared/w%d-%d", w, i)); !bytes.Equal(got, contents(w, i)) {
				t.Fatalf("w%d-%d differs", w, i)
			}
		}
	}
	if _, err := p.GC(); err != nil {
		t.Fatal(err)
	}
	rep, err := p.Fsck(true)
	if err != nil || !rep.OK() || len(rep.OrphanChunks)+len(rep.OrphanManifests) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	_ = b
}

func TestConcurrentOverwriteSameFile(t *testing.T) {
	p, _ := newMem(t, 8)
	versions := make([][]byte, 6)
	for i := range versions {
		versions[i] = randBytes(int64(i), 50+i)
	}
	var wg sync.WaitGroup
	for i := range versions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for k := 0; k < 20; k++ {
				mustPut(t, p, "f", versions[i])
				got := mustGet(t, p, "f")
				ok := false
				for _, v := range versions {
					ok = ok || bytes.Equal(got, v)
				}
				if !ok {
					t.Error("read a mix of versions")
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if err := p.Verify("f"); err != nil {
		t.Fatal(err)
	}
}
