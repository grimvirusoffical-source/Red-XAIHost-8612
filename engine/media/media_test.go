package media

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"redx/engine/pack"
)

// countingChunks counts chunk fetches.
type countingChunks struct {
	pack.ChunkStore
	gets atomic.Int64
	ids  sync.Map
}

func (c *countingChunks) GetChunk(id string) ([]byte, error) {
	c.gets.Add(1)
	c.ids.Store(id, true)
	return c.ChunkStore.GetChunk(id)
}

type env struct {
	be *pack.MemBackend
	cc *countingChunks
	p  *pack.Pack
	s  *Store
}

func newEnv(t testing.TB, chunk int, cfg Config) *env {
	t.Helper()
	be := pack.NewMemBackend()
	p, err := pack.New(be, be, pack.Config{ChunkSize: chunk})
	if err != nil {
		t.Fatal(err)
	}
	cc := &countingChunks{ChunkStore: be}
	s, err := New(p, cc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &env{be, cc, p, s}
}

func put(t testing.TB, e *env, name string, data []byte, declared string) Meta {
	t.Helper()
	m, err := e.s.Put(name, bytes.NewReader(data), PutOptions{Declared: declared})
	if err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
	return m
}

func TestPutStatOpenAllFormats(t *testing.T) {
	e := newEnv(t, 256, Config{})
	cases := []struct {
		name string
		data []byte
		mime string
		typ  Type
		w, h int
		ms   int64
	}{
		{"a.png", makePNG(640, 480, 1000), "image/png", TypeImage, 640, 480, 0},
		{"b.jpg", makeJPEG(800, 600, 2000), "image/jpeg", TypeImage, 800, 600, 0},
		{"c.gif", makeGIF(10, 20), "image/gif", TypeImage, 10, 20, 0},
		{"d.webp", makeWebPLossy(30, 40), "image/webp", TypeImage, 30, 40, 0},
		{"e.mp3", makeMP3(50, 100, 0), "audio/mpeg", TypeAudio, 0, 0, int64(50*417) * 8 * 1000 / 128000},
		{"f.wav", makeWAV(1000, 3000, 3000), "audio/wav", TypeAudio, 0, 0, 3000},
		{"g.ogg", makeOggVorbis(44100, 44100), "audio/ogg", TypeAudio, 0, 0, 1000},
		{"h.mp4", makeMP4("isom", 1280, 720, 1000, 9000, true, 5000), "video/mp4", TypeVideo, 1280, 720, 9000},
	}
	for _, c := range cases {
		m := put(t, e, c.name, c.data, "")
		if m.MIME != c.mime || m.Type != c.typ || m.Width != c.w || m.Height != c.h || m.DurationMs != c.ms || m.Size != int64(len(c.data)) {
			t.Errorf("%s: put returned %+v", c.name, m)
		}
		st, err := e.s.Stat(c.name)
		if err != nil || st != m {
			t.Errorf("%s: stat %+v %v, want %+v", c.name, st, err, m)
		}
		rd, err := e.s.Open(c.name)
		if err != nil {
			t.Fatalf("%s: open: %v", c.name, err)
		}
		got, err := io.ReadAll(rd)
		if err != nil || !bytes.Equal(got, c.data) {
			t.Errorf("%s: read back differs (%v)", c.name, err)
		}
		if err := e.s.Verify(c.name); err != nil {
			t.Errorf("%s: verify: %v", c.name, err)
		}
	}
	list, err := e.s.List()
	if err != nil || len(list) != len(cases) {
		t.Fatalf("list: %d %v", len(list), err)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Meta.Name >= list[i].Meta.Name || list[i].Err != nil {
			t.Fatalf("list order or error: %+v", list)
		}
	}
	if rep, err := e.s.Fsck(true); err != nil || !rep.OK() || rep.Items != len(cases) {
		t.Fatalf("fsck: %+v %v", rep, err)
	}
}

func TestManifestIsABoxDocument(t *testing.T) {
	e := newEnv(t, 256, Config{Root: "lib/media"})
	m := put(t, e, "x.png", makePNG(5, 6, 10), "")
	rc, _, err := e.p.Get("lib/media/meta/x.png.red-xai-media")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	txt := string(b)
	for _, want := range []string{"{Media[]= [", `{MIME[]} = "image/png"`, `{Type[]} = "image"`, "{Width[]} = 5", "{Height[]} = 6", `{SHA256[]} = "` + m.SHA256, "{Seal[]}"} {
		if !strings.Contains(txt, want) {
			t.Errorf("manifest lacks %q:\n%s", want, txt)
		}
	}
	if !strings.HasPrefix(m.Path, "lib/media/data/x.png/") {
		t.Fatalf("path %q", m.Path)
	}
}

func TestDeclaredType(t *testing.T) {
	e := newEnv(t, 256, Config{})
	png := makePNG(2, 2, 10)
	mp4 := makeMP4("isom", 0, 0, 1000, 1000, false, 10)
	ok := []struct {
		data []byte
		decl string
		mime string
	}{
		{png, "", "image/png"}, {png, "image/png", "image/png"}, {png, " IMAGE/PNG; charset=binary ", "image/png"},
		{png, "image", "image/png"}, {makeJPEG(2, 2, 10), "image/jpg", "image/jpeg"},
		{makeMP3(3, -1, 0), "audio/mp3", "audio/mpeg"}, {makeWAV(1000, 10, 10), "audio/x-wav", "audio/wav"},
		{mp4, "video/mp4", "video/mp4"}, {mp4, "audio/mp4", "audio/mp4"}, {mp4, "video", "video/mp4"},
	}
	for i, c := range ok {
		m, err := e.s.Put(fmt.Sprintf("ok%d", i), bytes.NewReader(c.data), PutOptions{Declared: c.decl})
		if err != nil || m.MIME != c.mime {
			t.Errorf("declared %q: %+v %v, want %s", c.decl, m, err, c.mime)
		}
	}
	nOK := len(ok)
	chunksBefore, _ := e.be.ListChunks()
	bad := []struct {
		data []byte
		decl string
	}{
		{png, "image/jpeg"}, {png, "audio/mpeg"}, {png, "audio"}, {png, "video/mp4"}, {png, "text/html"},
		{png, "application/octet-stream"}, {png, "png"}, {png, "image/png/x"}, {png, "image/gif"},
		{makeMP3(3, -1, 0), "image/png"}, {makeMP4("M4A ", 0, 0, 1, 1, false, 1), "video/mp4"},
		{makeOggVorbis(44100, 1), "video/ogg"}, {makeWAV(1000, 10, 10), "audio/mpeg"},
	}
	for i, c := range bad {
		if _, err := e.s.Put(fmt.Sprintf("bad%d", i), bytes.NewReader(c.data), PutOptions{Declared: c.decl}); !errors.Is(err, ErrTypeMismatch) {
			t.Errorf("declared %q: want ErrTypeMismatch, got %v", c.decl, err)
		}
	}
	// nothing was stored for any rejected upload
	chunksAfter, _ := e.be.ListChunks()
	if len(chunksAfter) != len(chunksBefore) {
		t.Fatal("a rejected upload stored chunks")
	}
	list, _ := e.s.List()
	if len(list) != nOK {
		t.Fatalf("list has %d items, want %d", len(list), nOK)
	}
	// a declared type that matches does not rescue unrecognised bytes
	if _, err := e.s.Put("x", strings.NewReader("just text, not media"), PutOptions{Declared: "image/png"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("text declared as png: %v", err)
	}
	if _, err := e.s.Put("x", bytes.NewReader(nil), PutOptions{}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := e.s.Stat("x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("x must not exist: %v", err)
	}
	// replacing an item with a mismatching upload leaves the old one intact
	old := put(t, e, "keep", png, "")
	if _, err := e.s.Put("keep", bytes.NewReader(makeMP3(3, -1, 0)), PutOptions{Declared: "image/png"}); !errors.Is(err, ErrTypeMismatch) {
		t.Fatal("expected mismatch")
	}
	if st, err := e.s.Stat("keep"); err != nil || st != old {
		t.Fatalf("old item changed: %+v %v", st, err)
	}
}

func TestNamesAndLimits(t *testing.T) {
	e := newEnv(t, 256, Config{MaxSize: 1000})
	png := makePNG(2, 2, 10)
	for _, n := range []string{"", "a/b", "../x", "..", ".", "a\\b", "con", "x:y", "a\x00b", strings.Repeat("n", 201), "/abs", "trail."} {
		if _, err := e.s.Put(n, bytes.NewReader(png), PutOptions{}); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Put(%q): want ErrInvalidName, got %v", n, err)
		}
		if _, err := e.s.Stat(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Stat(%q): %v", n, err)
		}
		if err := e.s.Delete(n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Delete(%q): %v", n, err)
		}
	}
	put(t, e, strings.Repeat("n", 200), png, "") // longest allowed
	// size limit (the check is on bytes read, not on a header)
	big := makePNG(2, 2, 2000)
	if _, err := e.s.Put("big", bytes.NewReader(big), PutOptions{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if _, err := e.s.Stat("big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversize item must not exist: %v", err)
	}
	if _, err := New(nil, nil, Config{}); err == nil {
		t.Fatal("nil stores")
	}
	if _, err := New(e.p, e.cc, Config{Root: "../x"}); err == nil {
		t.Fatal("bad root")
	}
	if _, _, err := e.s.ReadRange("nope", 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
}

func bigPNG(t testing.TB, n int) []byte {
	t.Helper()
	b := makePNG(100, 100, n)
	rand.Read(b[40 : len(b)-20]) // random payload after the header
	return b
}

func TestRangedReads(t *testing.T) {
	const chunk = 1000
	e := newEnv(t, chunk, Config{})
	data := bigPNG(t, 10*chunk+317)
	put(t, e, "pic", data, "")
	rd, err := e.s.Open("pic")
	if err != nil {
		t.Fatal(err)
	}
	if rd.Size() != int64(len(data)) || rd.Meta().Name != "pic" {
		t.Fatal("size/meta")
	}
	nChunks := (len(data) + chunk - 1) / chunk

	// ReadAt at, around and across chunk boundaries and at the very end
	for _, c := range []struct{ off, n int }{
		{0, 1}, {0, 100}, {chunk - 1, 2}, {chunk, chunk}, {chunk - 5, chunk + 10}, {3*chunk + 17, 4 * chunk},
		{len(data) - 1, 1}, {len(data) - 100, 100}, {0, len(data)}, {5, 0},
	} {
		buf := make([]byte, c.n)
		n, err := rd.ReadAt(buf, int64(c.off))
		if n != c.n || (err != nil && err != io.EOF) || !bytes.Equal(buf, data[c.off:c.off+c.n]) {
			t.Errorf("ReadAt(%d,%d) = %d %v", c.off, c.n, n, err)
		}
	}
	// reading past the end returns the available bytes and io.EOF
	buf := make([]byte, 500)
	n, err := rd.ReadAt(buf, int64(len(data)-200))
	if n != 200 || err != io.EOF || !bytes.Equal(buf[:200], data[len(data)-200:]) {
		t.Fatalf("short ReadAt: %d %v", n, err)
	}
	if n, err := rd.ReadAt(buf, int64(len(data))); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt at end: %d %v", n, err)
	}
	if n, err := rd.ReadAt(buf, int64(len(data))+10); n != 0 || err != io.EOF {
		t.Fatalf("ReadAt past end: %d %v", n, err)
	}
	if _, err := rd.ReadAt(buf, -1); err == nil {
		t.Fatal("negative offset")
	}

	// Seek + Read (the streaming/seeking case)
	for _, w := range []struct {
		off    int64
		whence int
		want   int64
	}{{4321, io.SeekStart, 4321}, {10, io.SeekCurrent, 4381}, {-100, io.SeekEnd, int64(len(data)) - 100}} {
		got, err := rd.Seek(w.off, w.whence)
		if err != nil || got != w.want {
			t.Fatalf("seek %+v: %d %v", w, got, err)
		}
		b := make([]byte, 50)
		if _, err := io.ReadFull(rd, b); err != nil && err != io.ErrUnexpectedEOF {
			t.Fatal(err)
		}
	}
	if _, err := rd.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek")
	}
	if _, err := rd.Seek(0, 99); err == nil {
		t.Fatal("bad whence")
	}
	rd.Seek(int64(len(data))-3, io.SeekStart)
	tail, err := io.ReadAll(rd)
	if err != nil || !bytes.Equal(tail, data[len(data)-3:]) {
		t.Fatalf("tail: %v", err)
	}
	if n, err := rd.Read(make([]byte, 4)); n != 0 || err != io.EOF {
		t.Fatalf("read at EOF: %d %v", n, err)
	}

	// only the chunks a range touches are fetched
	e.cc.gets.Store(0)
	rd2, _ := e.s.Open("pic")
	got := make([]byte, 100)
	if _, err := rd2.ReadAt(got, 5*chunk+10); err != nil || !bytes.Equal(got, data[5*chunk+10:5*chunk+110]) {
		t.Fatal(err)
	}
	if g := e.cc.gets.Load(); g != 1 {
		t.Fatalf("a 100-byte range inside one chunk fetched %d chunks", g)
	}
	// sequential small reads fetch each chunk once
	e.cc.gets.Store(0)
	rd3, _ := e.s.Open("pic")
	small := make([]byte, 64)
	for {
		if _, err := rd3.Read(small); err != nil {
			break
		}
	}
	if g := e.cc.gets.Load(); g != int64(nChunks) {
		t.Fatalf("sequential read fetched %d chunks, want %d", g, nChunks)
	}
	// a range spanning two chunks fetches two
	e.cc.gets.Store(0)
	rd4, _ := e.s.Open("pic")
	rd4.ReadAt(make([]byte, 20), 2*chunk-10)
	if g := e.cc.gets.Load(); g != 2 {
		t.Fatalf("spanning range fetched %d chunks", g)
	}

	// Store.ReadRange
	b, m, err := e.s.ReadRange("pic", 2500, 100)
	if err != nil || !bytes.Equal(b, data[2500:2600]) || m.Name != "pic" {
		t.Fatalf("ReadRange: %v", err)
	}
	b, _, err = e.s.ReadRange("pic", int64(len(data))-10, 1000) // clipped
	if err != nil || !bytes.Equal(b, data[len(data)-10:]) {
		t.Fatalf("clipped ReadRange: %v", err)
	}
	if _, _, err := e.s.ReadRange("pic", int64(len(data)), 1); !errors.Is(err, ErrUnsatisfiable) {
		t.Fatalf("ReadRange at end: %v", err)
	}
	if _, _, err := e.s.ReadRange("pic", -1, 1); !errors.Is(err, ErrBadRange) {
		t.Fatalf("negative: %v", err)
	}
	small2 := newEnv(t, 1000, Config{MaxRangeRead: 100})
	put(t, small2, "pic", data, "")
	if _, _, err := small2.s.ReadRange("pic", 0, 101); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("range read limit: %v", err)
	}
	// closed reader
	rd.Close()
	if _, err := rd.ReadAt(buf, 0); err == nil {
		t.Fatal("read after close")
	}
}

func TestConcurrentReadAt(t *testing.T) {
	e := newEnv(t, 512, Config{})
	data := bigPNG(t, 20000)
	put(t, e, "pic", data, "")
	rd, _ := e.s.Open("pic")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			buf := make([]byte, 700)
			for i := 0; i < 200; i++ {
				off := (g*7919 + i*3571) % (len(data) - 700)
				n, err := rd.ReadAt(buf, int64(off))
				if n != 700 || (err != nil && err != io.EOF) || !bytes.Equal(buf, data[off:off+700]) {
					t.Errorf("goroutine %d: bad read at %d: %v", g, off, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestHTTPServeContent(t *testing.T) {
	e := newEnv(t, 700, Config{})
	data := bigPNG(t, 5000)
	m := put(t, e, "pic", data, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rd, err := e.s.Open("pic")
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		defer rd.Close()
		w.Header().Set("Content-Type", rd.Meta().MIME)
		http.ServeContent(w, r, "", time.Time{}, rd)
	}))
	defer srv.Close()
	get := func(rng string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	resp, b := get("")
	if resp.StatusCode != 200 || !bytes.Equal(b, data) || resp.Header.Get("Content-Type") != m.MIME || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("full: %d %v", resp.StatusCode, resp.Header)
	}
	resp, b = get("bytes=1000-1999")
	if resp.StatusCode != 206 || !bytes.Equal(b, data[1000:2000]) || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 1000-1999/%d", len(data)) {
		t.Fatalf("range: %d %v", resp.StatusCode, resp.Header)
	}
	resp, b = get("bytes=-50")
	if resp.StatusCode != 206 || !bytes.Equal(b, data[len(data)-50:]) {
		t.Fatalf("suffix: %d", resp.StatusCode)
	}
	resp, _ = get(fmt.Sprintf("bytes=%d-", len(data)+5))
	if resp.StatusCode != 416 {
		t.Fatalf("unsatisfiable: %d", resp.StatusCode)
	}
}

func TestParseRange(t *testing.T) {
	type r struct{ off, n int64 }
	ok := map[string]r{
		"bytes=0-99": {0, 100}, "bytes=0-0": {0, 1}, "bytes=500-": {500, 500}, "bytes=-200": {800, 200},
		"bytes=0-9999": {0, 1000}, "bytes=999-999": {999, 1}, "bytes=-5000": {0, 1000}, " Bytes = 10 - 19 ": {10, 10},
		"bytes=0-": {0, 1000}, "bytes=999-": {999, 1}, "bytes=10-19,": {10, 10},
	}
	for h, want := range ok {
		off, n, err := ParseRange(h, 1000)
		if err != nil || off != want.off || n != want.n {
			t.Errorf("%q: %d,%d %v, want %+v", h, off, n, err, want)
		}
	}
	errs := map[string]error{
		"bytes=1000-": ErrUnsatisfiable, "bytes=2000-3000": ErrUnsatisfiable, "bytes=-0": ErrUnsatisfiable,
		"bytes=0-1,5-9": ErrMultiRange, "bytes=0-1, 5-9, 20-30": ErrMultiRange,
		"": ErrBadRange, "bytes": ErrBadRange, "items=0-1": ErrBadRange, "bytes=": ErrBadRange, "bytes=-": ErrBadRange,
		"bytes=a-b": ErrBadRange, "bytes=5-2": ErrBadRange, "bytes=1-2-3": ErrBadRange, "bytes=-x": ErrBadRange,
		"bytes=+1-2": ErrBadRange, "bytes=99999999999999999999-": ErrBadRange, "bytes=0": ErrBadRange,
	}
	for h, want := range errs {
		if _, _, err := ParseRange(h, 1000); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", h, err, want)
		}
	}
	if _, _, err := ParseRange("bytes=0-1", 0); !errors.Is(err, ErrUnsatisfiable) {
		t.Errorf("empty file: %v", err)
	}
	if _, _, err := ParseRange("bytes=-1", 0); !errors.Is(err, ErrUnsatisfiable) {
		t.Errorf("empty file suffix: %v", err)
	}
	if got := ContentRange(10, 20, 100); got != "bytes 10-29/100" {
		t.Errorf("ContentRange %q", got)
	}
}

func TestDamageDetectedOnRead(t *testing.T) {
	const chunk = 500
	e := newEnv(t, chunk, Config{})
	data := bigPNG(t, 6*chunk)
	m := put(t, e, "pic", data, "")
	info, _ := e.p.Stat(m.Path)
	flip := info.Chunks[2]
	trunc := info.Chunks[4]
	gone := info.Chunks[5]
	c, _ := e.be.GetChunk(flip)
	c[10] ^= 1
	e.be.SetRawChunk(flip, c)
	c, _ = e.be.GetChunk(trunc)
	e.be.SetRawChunk(trunc, c[:len(c)-1])
	e.be.DeleteChunk(gone)

	rd, err := e.s.Open("pic")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	// good ranges still work
	if _, err := rd.ReadAt(buf, 0); err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	if n, err := rd.ReadAt(buf, 3*chunk); err != nil || n != 100 {
		t.Fatalf("chunk 3: %v", err)
	}
	// damaged ranges fail with the right error and release no bytes of the bad chunk
	for _, c := range []struct {
		off  int
		want error
	}{{2 * chunk, ErrCorrupt}, {4*chunk + 1, ErrCorrupt}, {5 * chunk, pack.ErrMissing}} {
		b := make([]byte, 10)
		n, err := rd.ReadAt(b, int64(c.off))
		if !errors.Is(err, c.want) || n != 0 {
			t.Errorf("read at %d: n=%d err=%v, want %v", c.off, n, err, c.want)
		}
	}
	// a read that starts good and runs into the bad chunk returns the good part and an error
	b := make([]byte, 2*chunk)
	n, err := rd.ReadAt(b, chunk)
	if !errors.Is(err, ErrCorrupt) || n != chunk || !bytes.Equal(b[:n], data[chunk:2*chunk]) {
		t.Fatalf("spanning read: n=%d err=%v", n, err)
	}
	if err := e.s.Verify("pic"); err == nil {
		t.Fatal("Verify must fail")
	}
	rep, err := e.s.Fsck(true)
	if err != nil || len(rep.Problems) != 1 || rep.Problems[0].Name != "pic" {
		t.Fatalf("deep fsck: %+v %v", rep, err)
	}
}

func TestManifestDamage(t *testing.T) {
	e := newEnv(t, 256, Config{})
	put(t, e, "a", makePNG(2, 2, 10), "")
	put(t, e, "b", makePNG(3, 3, 10), "")
	mp := e.s.metaPath("a")
	rc, _, _ := e.p.Get(mp)
	orig, _ := io.ReadAll(rc)
	replace := func(data []byte) {
		if _, err := e.p.Put(mp, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	mod := func(f func([]byte) []byte) []byte { return f(append([]byte(nil), orig...)) }
	bad := map[string][]byte{
		"truncated":   orig[:len(orig)-10],
		"no seal":     orig[:bytes.Index(orig, []byte("{Seal"))],
		"bit flip":    mod(func(b []byte) []byte { b[20] ^= 1; return b }),
		"garbage":     []byte("not a manifest at all"),
		"empty":       {},
		"junk after":  append(append([]byte(nil), orig...), 'x'),
		"edited size": bytes.Replace(orig, []byte("{Size[]} = "), []byte("{Size[]} = 9"), 1),
	}
	for name, data := range bad {
		replace(data)
		if _, err := e.s.Stat("a"); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: Stat: %v", name, err)
		}
		if _, err := e.s.Open("a"); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: Open: %v", name, err)
		}
	}
	// another item's manifest copied over this one is refused (it names a different item)
	rcb, _, _ := e.p.Get(e.s.metaPath("b"))
	bm, _ := io.ReadAll(rcb)
	replace(bm)
	if _, err := e.s.Stat("a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("manifest of b stored as a: %v", err)
	}
	// a manifest whose Path points outside the item's data folder is refused (re-sealed so only the path is wrong)
	m := Meta{Name: "a", Type: TypeImage, MIME: "image/png", Size: 5, SHA256: strings.Repeat("a", 64), Path: "data/b/" + strings.Repeat("0", 32)}
	replace(m.encode())
	if _, err := e.s.Stat("a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("foreign path: %v", err)
	}
	m.Path = "data/a/../b/" + strings.Repeat("0", 32)
	replace(m.encode())
	if _, err := e.s.Stat("a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("path traversal: %v", err)
	}
	m.Path, m.MIME, m.Type = "data/a/"+strings.Repeat("0", 32), "audio/mpeg", TypeImage
	replace(m.encode())
	if _, err := e.s.Stat("a"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("type/MIME disagree: %v", err)
	}
	// list shows the damaged item and keeps going
	list, err := e.s.List()
	if err != nil || len(list) != 2 || list[0].Err == nil || list[1].Err != nil || list[0].Meta.Name != "a" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// the data of "a" is not reported as an orphan, and Sweep leaves it alone
	rep, _ := e.s.Fsck(false)
	if len(rep.Problems) != 1 || len(rep.Orphans) != 0 {
		t.Fatalf("fsck: %+v", rep)
	}
	if n, err := e.s.Sweep(); err != nil || n != 0 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	// a damaged item can still be deleted
	if err := e.s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Stat("a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a should be gone")
	}
	if rep, _ := e.s.Fsck(true); !rep.OK() || rep.Items != 1 {
		t.Fatalf("fsck after delete: %+v", rep)
	}
	// data file replaced behind the manifest's back
	e.p.Put(e.s.dataDir("b")+"/"+path.Base(mustMeta(t, e, "b").Path), strings.NewReader("different content"))
	if _, err := e.s.Open("b"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("data replaced behind manifest: %v", err)
	}
}

func mustMeta(t testing.TB, e *env, name string) Meta {
	t.Helper()
	m, err := e.s.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReplaceDeleteAndSweep(t *testing.T) {
	e := newEnv(t, 256, Config{})
	v1 := put(t, e, "pic", makePNG(1, 1, 500), "")
	v2 := put(t, e, "pic", makePNG(2, 2, 700), "")
	if v1.Path == v2.Path || v2.Width != 2 {
		t.Fatalf("replace: %+v %+v", v1, v2)
	}
	if _, err := e.p.Stat(v1.Path); err == nil {
		t.Fatal("old data file should be gone after a replace")
	}
	rd, _ := e.s.Open("pic")
	if rd.Meta().Width != 2 {
		t.Fatal("reads see the new version")
	}
	// orphan data (what a crash between the data write and the manifest write leaves)
	e.p.Put(e.s.dataDir("pic")+"/"+strings.Repeat("e", 32), strings.NewReader("orphan"))
	e.p.Put(e.s.dataDir("ghost")+"/"+strings.Repeat("f", 32), strings.NewReader("orphan 2"))
	rep, err := e.s.Fsck(false)
	if err != nil || len(rep.Orphans) != 2 || len(rep.Problems) != 0 || rep.OK() {
		t.Fatalf("fsck: %+v %v", rep, err)
	}
	n, err := e.s.Sweep()
	if err != nil || n != 2 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if rep, _ := e.s.Fsck(true); !rep.OK() {
		t.Fatalf("after sweep: %+v", rep)
	}
	if err := e.s.Verify("pic"); err != nil {
		t.Fatal(err)
	}
	// delete
	if err := e.s.Delete("pic"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Delete("pic"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := e.s.Open("pic"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open deleted: %v", err)
	}
	if list, _ := e.s.List(); len(list) != 0 {
		t.Fatal("list not empty")
	}
	// pack GC reclaims the chunks
	if _, err := e.p.GC(); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.be.ListChunks(); len(ids) != 0 {
		t.Fatalf("%d chunks left after delete + GC", len(ids))
	}
	// an empty store lists and checks cleanly
	fresh := newEnv(t, 256, Config{})
	if l, err := fresh.s.List(); err != nil || len(l) != 0 {
		t.Fatalf("fresh list: %v %v", l, err)
	}
	if rep, err := fresh.s.Fsck(true); err != nil || !rep.OK() {
		t.Fatalf("fresh fsck: %+v %v", rep, err)
	}
}

// A crash before any single mutating backend call of a Put (new item or replacement)
// leaves a store where the item is intact (old or new), fsck finds no problem,
// Sweep + GC clean up, and a retry succeeds.
func TestCrashDuringPut(t *testing.T) {
	oldData := makePNG(1, 1, 900)
	newData := makePNG(7, 7, 1300)
	for _, replace := range []bool{false, true} {
		// count the mutating calls of a clean run
		count := func() int {
			e := newEnv(t, 256, Config{})
			if replace {
				put(t, e, "pic", oldData, "")
			}
			n := 0
			e.be.Hook = func(op, id string) error { n++; return nil }
			put(t, e, "pic", newData, "")
			return n
		}()
		if count < 3 {
			t.Fatalf("suspiciously few backend calls: %d", count)
		}
		for k := 1; k <= count; k++ {
			e := newEnv(t, 256, Config{})
			var oldMeta Meta
			if replace {
				oldMeta = put(t, e, "pic", oldData, "")
			}
			n := 0
			e.be.Hook = func(op, id string) error {
				n++
				if n == k {
					return errors.New("crash")
				}
				return nil
			}
			_, perr := e.s.Put("pic", bytes.NewReader(newData), PutOptions{})
			e.be.Hook = nil
			name := fmt.Sprintf("replace=%v crash before call %d/%d", replace, k, count)
			// A crash in the final best-effort cleanup (deleting the old data
			// file) does not fail the Put: the new item is already in place.
			m, serr := e.s.Stat("pic")
			switch {
			case serr == nil:
				rd, err := e.s.Open("pic")
				if err != nil {
					t.Fatalf("%s: open: %v", name, err)
				}
				got, err := io.ReadAll(rd)
				if err != nil || (!bytes.Equal(got, oldData) && !bytes.Equal(got, newData)) {
					t.Fatalf("%s: item content is neither old nor new (%v)", name, err)
				}
				if replace && m.Path != oldMeta.Path && !bytes.Equal(got, newData) {
					t.Fatalf("%s: manifest and data disagree", name)
				}
			case errors.Is(serr, ErrNotFound) && !replace:
			default:
				t.Fatalf("%s: item unreadable: %v", name, serr)
			}
			if perr == nil {
				if got, _ := io.ReadAll(mustOpen(t, e, "pic")); !bytes.Equal(got, newData) {
					t.Fatalf("%s: Put succeeded but the item is not the new one", name)
				}
			}
			rep, err := e.s.Fsck(true)
			if err != nil || len(rep.Problems) != 0 {
				t.Fatalf("%s: fsck: %+v %v", name, rep, err)
			}
			if _, err := e.s.Sweep(); err != nil {
				t.Fatalf("%s: sweep: %v", name, err)
			}
			if rep, _ := e.s.Fsck(true); !rep.OK() {
				t.Fatalf("%s: after sweep: %+v", name, rep)
			}
			if _, err := e.p.GC(); err != nil {
				t.Fatalf("%s: gc: %v", name, err)
			}
			if _, err := e.s.Put("pic", bytes.NewReader(newData), PutOptions{}); err != nil {
				t.Fatalf("%s: retry: %v", name, err)
			}
			if err := e.s.Verify("pic"); err != nil {
				t.Fatalf("%s: verify after retry: %v", name, err)
			}
		}
	}
}

func TestConcurrentPuts(t *testing.T) {
	e := newEnv(t, 300, Config{})
	var wg sync.WaitGroup
	datas := make([][]byte, 6)
	for i := range datas {
		datas[i] = bigPNG(t, 1000+i*100)
	}
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				// distinct names, and one name everybody fights over
				if _, err := e.s.Put(fmt.Sprintf("own%d", g), bytes.NewReader(datas[g]), PutOptions{}); err != nil {
					t.Errorf("put own: %v", err)
				}
				if _, err := e.s.Put("shared", bytes.NewReader(datas[g]), PutOptions{}); err != nil {
					t.Errorf("put shared: %v", err)
				}
				if rd, err := e.s.Open("shared"); err == nil {
					if b, err := io.ReadAll(rd); err != nil && !errors.Is(err, pack.ErrMissing) {
						t.Errorf("read shared: %v", err)
					} else if err == nil {
						ok := false
						for _, d := range datas {
							ok = ok || bytes.Equal(b, d)
						}
						if !ok {
							t.Error("shared item holds mixed content")
						}
					}
				}
			}
		}(g)
	}
	wg.Wait()
	rep, err := e.s.Fsck(true)
	if err != nil || !rep.OK() || rep.Items != 7 {
		t.Fatalf("fsck: %+v %v", rep, err)
	}
}

func TestDirBackendReopen(t *testing.T) {
	dir := t.TempDir()
	open := func() *Store {
		be := pack.DirBackend{Root: dir}
		p, err := pack.New(be, be, pack.Config{ChunkSize: 4096})
		if err != nil {
			t.Fatal(err)
		}
		s, err := New(p, be, Config{Root: "library"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	data := makeMP4("isom", 1920, 1080, 1000, 61000, true, 50000)
	s := open()
	if _, err := s.Put("clip.mp4", bytes.NewReader(data), PutOptions{Declared: "video/mp4"}); err != nil {
		t.Fatal(err)
	}
	s2 := open() // a fresh process
	m, err := s2.Stat("clip.mp4")
	if err != nil || m.Width != 1920 || m.DurationMs != 61000 {
		t.Fatalf("%+v %v", m, err)
	}
	b, _, err := s2.ReadRange("clip.mp4", 4000, 9000)
	if err != nil || !bytes.Equal(b, data[4000:13000]) {
		t.Fatalf("range after reopen: %v", err)
	}
	if rep, err := s2.Fsck(true); err != nil || !rep.OK() {
		t.Fatalf("fsck: %+v %v", rep, err)
	}
}

func mustOpen(t testing.TB, e *env, name string) *Reader {
	t.Helper()
	rd, err := e.s.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	return rd
}
