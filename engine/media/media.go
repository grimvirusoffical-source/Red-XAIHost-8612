package media

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"redx/engine/pack"
)

// Files is the part of *pack.Pack the media store uses.
type Files interface {
	Put(path string, r io.Reader) (pack.Info, error)
	Get(path string) (io.ReadCloser, pack.Info, error)
	Stat(path string) (pack.Info, error)
	List(folder string) ([]pack.Info, error)
	Delete(path string) error
	RemoveDir(path string, recursive bool) error
	Verify(path string) error
}

// Chunks reads single chunks by id; every pack.ChunkStore does.
type Chunks interface {
	GetChunk(id string) ([]byte, error)
}

// Defaults.
const (
	DefaultMaxSize      = 4 << 30  // 4 GiB per item
	DefaultMaxRangeRead = 64 << 20 // largest single ReadRange
	maxName             = 200
)

// Config configures a Store. Zero values mean the defaults.
type Config struct {
	// Root is the pack folder the store lives in ("" = the pack root). The
	// store uses Root/meta and Root/data.
	Root string
	// MaxSize bounds one item in bytes.
	MaxSize int64
	// MaxRangeRead bounds Store.ReadRange.
	MaxRangeRead int64
}

// Store keeps media items in a pack. It is safe for concurrent use within
// one process.
type Store struct {
	files  Files
	chunks Chunks
	cfg    Config
	meta   string // folder of manifests
	data   string // folder of data files

	gate sync.RWMutex // Put/Delete hold R; Fsck/Sweep hold W
	mu   sync.Mutex   // serialises the manifest swap of a Put and Delete
}

// New creates a Store over a pack (any Files) and its chunk store.
func New(files Files, chunks Chunks, cfg Config) (*Store, error) {
	if files == nil || chunks == nil {
		return nil, errors.New("media: nil store")
	}
	if cfg.MaxSize == 0 {
		cfg.MaxSize = DefaultMaxSize
	}
	if cfg.MaxSize < 0 || cfg.MaxSize > 1<<53 {
		return nil, errors.New("media: bad MaxSize")
	}
	if cfg.MaxRangeRead == 0 {
		cfg.MaxRangeRead = DefaultMaxRangeRead
	}
	if cfg.MaxRangeRead < 0 {
		return nil, errors.New("media: bad MaxRangeRead")
	}
	comps, err := pack.CleanPath(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("media: bad Root: %w", err)
	}
	root := strings.Join(comps, "/")
	join := func(a, b string) string {
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	cfg.Root = root
	return &Store{files: files, chunks: chunks, cfg: cfg, meta: join(root, "meta"), data: join(root, "data")}, nil
}

func checkName(name string) error {
	comps, err := pack.CleanPath(name)
	if err != nil || len(comps) != 1 || comps[0] != name || len(name) > maxName {
		return fmt.Errorf("%w: %q (one valid file name, at most %d bytes, no '/')", ErrInvalidName, name, maxName)
	}
	return nil
}

func (s *Store) metaPath(name string) string { return s.meta + "/" + name + manifestExt }
func (s *Store) dataDir(name string) string  { return s.data + "/" + name }

// PutOptions control Put.
type PutOptions struct {
	// Declared is the type the caller says the bytes are: a MIME type
	// ("image/png", parameters and common aliases are accepted) or just
	// "image", "audio" or "video". Empty accepts whatever is sniffed. A
	// declaration that does not match the content is rejected with
	// ErrTypeMismatch before anything is stored.
	Declared string
}

type limitReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.max {
		return n, fmt.Errorf("%w: limit is %d bytes", ErrTooLarge, l.max)
	}
	return n, err
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func checkDeclared(declared string, sn Sniffed) (mime string, err error) {
	if strings.TrimSpace(declared) == "" {
		return sn.MIME, nil
	}
	d := strings.ToLower(strings.TrimSpace(declared))
	if d == "image" || d == "audio" || d == "video" {
		if Type(d) != sn.Type {
			return "", fmt.Errorf("%w: declared %q but the content is %s (%s)", ErrTypeMismatch, declared, sn.Type, sn.MIME)
		}
		return sn.MIME, nil
	}
	n := NormalizeMIME(d)
	if n == "" {
		return "", fmt.Errorf("%w: declared type %q is not a MIME type", ErrTypeMismatch, declared)
	}
	if !sn.Accepts(n) {
		return "", fmt.Errorf("%w: declared %q but the content is %s", ErrTypeMismatch, declared, sn.MIME)
	}
	return n, nil
}

// Put stores r as the item called name, replacing an existing item. The
// first bytes are sniffed and checked against o.Declared before anything is
// written. The data is written first and the manifest second, and the manifest
// is what makes the item exist, so a crash leaves either the old item or the
// new one, plus possibly unreferenced data that Sweep removes.
func (s *Store) Put(name string, r io.Reader, o PutOptions) (Meta, error) {
	if err := checkName(name); err != nil {
		return Meta{}, err
	}
	s.gate.RLock()
	defer s.gate.RUnlock()

	head := make([]byte, SniffLen)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Meta{}, err
	}
	head = head[:n]
	if n == 0 {
		return Meta{}, ErrEmpty
	}
	sn, err := Sniff(head)
	if err != nil {
		return Meta{}, err
	}
	mime, err := checkDeclared(o.Declared, sn)
	if err != nil {
		return Meta{}, err
	}
	tok, err := newToken()
	if err != nil {
		return Meta{}, fmt.Errorf("media: random: %w", err)
	}
	dpath := s.dataDir(name) + "/" + tok
	lr := &limitReader{r: io.MultiReader(bytes.NewReader(head), r), max: s.cfg.MaxSize}
	info, err := s.files.Put(dpath, lr)
	if err != nil {
		return Meta{}, err
	}
	m := Meta{Name: name, MIME: mime, Size: info.Size, SHA256: info.SHA256, Path: dpath}
	m.Type = Type(mime[:strings.IndexByte(mime, '/')])
	rd := &Reader{s: s, meta: m, info: info}
	pi := Probe(sn, rd, info.Size)
	m.Width, m.Height, m.DurationMs, m.DurationEstimated = pi.Width, pi.Height, pi.DurationMs, pi.DurationEstimated

	s.mu.Lock()
	defer s.mu.Unlock()
	var old *Meta
	if prev, err := s.loadMeta(name); err == nil {
		old = &prev
	}
	if err := s.saveMeta(m); err != nil {
		s.files.Delete(dpath) // best effort; Sweep would remove it anyway
		return Meta{}, err
	}
	if old != nil && old.Path != dpath {
		s.files.Delete(old.Path) // best effort; Sweep removes leftovers
	}
	return m, nil
}

func (s *Store) saveMeta(m Meta) error {
	_, err := s.files.Put(s.metaPath(m.Name), bytes.NewReader(m.encode()))
	return err
}

// loadMeta reads and verifies the manifest of name.
func (s *Store) loadMeta(name string) (Meta, error) {
	rc, info, err := s.files.Get(s.metaPath(name))
	if err != nil {
		if errors.Is(err, pack.ErrNotFound) || errors.Is(err, pack.ErrNotDir) {
			return Meta{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return Meta{}, wrapPack(err)
	}
	defer rc.Close()
	if info.Size > maxManifest {
		return Meta{}, corruptf("manifest of %q is %d bytes", name, info.Size)
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxManifest+1))
	if err != nil {
		return Meta{}, wrapPack(err)
	}
	m, err := decodeMeta(data, name, s.dataDir(name)+"/")
	if err != nil {
		return Meta{}, fmt.Errorf("%q: %w", name, err)
	}
	return m, nil
}

// wrapPack maps pack integrity errors to ErrCorrupt (keeping the cause).
func wrapPack(err error) error {
	if errors.Is(err, pack.ErrCorrupt) || errors.Is(err, pack.ErrMissing) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}

// Stat returns the manifest of an item.
func (s *Store) Stat(name string) (Meta, error) {
	if err := checkName(name); err != nil {
		return Meta{}, err
	}
	return s.loadMeta(name)
}

// Open opens an item for reading. The data file must still match the
// manifest's size and SHA-256 (as recorded by the pack), otherwise ErrCorrupt.
func (s *Store) Open(name string) (*Reader, error) {
	m, err := s.Stat(name)
	if err != nil {
		return nil, err
	}
	info, err := s.files.Stat(m.Path)
	if err != nil {
		if errors.Is(err, pack.ErrNotFound) || errors.Is(err, pack.ErrNotDir) {
			return nil, corruptf("%q: data file is missing", name)
		}
		return nil, wrapPack(err)
	}
	if info.IsDir || info.Size != m.Size || info.SHA256 != m.SHA256 {
		return nil, corruptf("%q: data file does not match its manifest", name)
	}
	return &Reader{s: s, meta: m, info: info}, nil
}

// ReadRange returns length bytes starting at off (clipped at the end of the
// file). An off at or past the end is ErrUnsatisfiable. At most
// Config.MaxRangeRead bytes can be asked for in one call; use Open for more.
func (s *Store) ReadRange(name string, off, length int64) ([]byte, Meta, error) {
	if off < 0 || length < 0 {
		return nil, Meta{}, ErrBadRange
	}
	rd, err := s.Open(name)
	if err != nil {
		return nil, Meta{}, err
	}
	if off >= rd.Size() {
		return nil, rd.Meta(), ErrUnsatisfiable
	}
	if length > rd.Size()-off {
		length = rd.Size() - off
	}
	if length > s.cfg.MaxRangeRead {
		return nil, rd.Meta(), fmt.Errorf("%w: a range read is limited to %d bytes", ErrTooLarge, s.cfg.MaxRangeRead)
	}
	buf := make([]byte, length)
	n, err := rd.ReadAt(buf, off)
	if err != nil && !(err == io.EOF && int64(n) == length) {
		return nil, rd.Meta(), err
	}
	return buf, rd.Meta(), nil
}

// Verify reads the whole item and checks every chunk, the file hash and the
// manifest against each other.
func (s *Store) Verify(name string) error {
	rd, err := s.Open(name)
	if err != nil {
		return err
	}
	return wrapPack(s.files.Verify(rd.meta.Path))
}

// Delete removes an item. It also works on an item whose manifest is
// damaged. The manifest goes first (the item disappears), then the data;
// chunks stay in the chunk store until the pack's GC.
func (s *Store) Delete(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.files.Delete(s.metaPath(name)); err != nil {
		if errors.Is(err, pack.ErrNotFound) || errors.Is(err, pack.ErrNotDir) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return err
	}
	if err := s.files.RemoveDir(s.dataDir(name), true); err != nil && !errors.Is(err, pack.ErrNotFound) {
		return fmt.Errorf("media: %q removed but its data was not cleaned up (Sweep will retry): %w", name, err)
	}
	return nil
}

// Entry is one item in a listing: its manifest, or the reason it could not be read.
type Entry struct {
	Meta Meta // Meta.Name is always set
	Err  error
}

// List returns every item sorted by name. Items whose manifest is damaged are
// listed with Err set.
func (s *Store) List() ([]Entry, error) {
	infos, err := s.files.List(s.meta)
	if err != nil {
		if errors.Is(err, pack.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, in := range infos {
		if in.IsDir || !strings.HasSuffix(in.Name, manifestExt) {
			continue
		}
		name := strings.TrimSuffix(in.Name, manifestExt)
		m, err := s.loadMeta(name)
		if err != nil {
			m = Meta{Name: name}
		}
		out = append(out, Entry{Meta: m, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.Name < out[j].Meta.Name })
	return out, nil
}

// Problem is one thing Fsck found wrong.
type Problem struct {
	Name string
	Err  error
}

// Report is the result of Fsck.
type Report struct {
	Items    int
	Problems []Problem
	// Orphans are data files in the pack that no manifest refers to
	// (leftovers of a crash or an interrupted replace). Sweep removes them.
	Orphans []string
}

// OK reports whether nothing was found.
func (r Report) OK() bool { return len(r.Problems) == 0 && len(r.Orphans) == 0 }

// Fsck checks every manifest and its data file (size and hash as recorded by
// the pack) and lists orphan data. With deep it also reads every chunk. It
// changes nothing and blocks Put and Delete while it runs.
func (s *Store) Fsck(deep bool) (Report, error) {
	s.gate.Lock()
	defer s.gate.Unlock()
	var rep Report
	entries, err := s.List()
	if err != nil {
		return rep, err
	}
	live := map[string]string{} // name -> data path
	for _, e := range entries {
		rep.Items++
		if e.Err != nil {
			rep.Problems = append(rep.Problems, Problem{e.Meta.Name, e.Err})
			live[e.Meta.Name] = "" // its data is not an orphan, it is just unreachable
			continue
		}
		live[e.Meta.Name] = e.Meta.Path
		if _, err := s.openMeta(e.Meta); err != nil {
			rep.Problems = append(rep.Problems, Problem{e.Meta.Name, err})
			continue
		}
		if deep {
			if err := s.files.Verify(e.Meta.Path); err != nil {
				rep.Problems = append(rep.Problems, Problem{e.Meta.Name, wrapPack(err)})
			}
		}
	}
	orphans, err := s.orphans(live)
	if err != nil {
		return rep, err
	}
	rep.Orphans = orphans
	return rep, nil
}

func (s *Store) openMeta(m Meta) (pack.Info, error) {
	info, err := s.files.Stat(m.Path)
	if err != nil {
		if errors.Is(err, pack.ErrNotFound) || errors.Is(err, pack.ErrNotDir) {
			return info, corruptf("data file is missing")
		}
		return info, wrapPack(err)
	}
	if info.IsDir || info.Size != m.Size || info.SHA256 != m.SHA256 {
		return info, corruptf("data file does not match its manifest")
	}
	return info, nil
}

// orphans lists data paths not referenced by live. A data folder whose item
// has no manifest at all is reported as the folder path.
func (s *Store) orphans(live map[string]string) ([]string, error) {
	dirs, err := s.files.List(s.data)
	if err != nil {
		if errors.Is(err, pack.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		if !d.IsDir {
			out = append(out, d.Path)
			continue
		}
		keep, ok := live[d.Name]
		if !ok {
			out = append(out, d.Path)
			continue
		}
		if keep == "" { // damaged manifest: leave its data alone
			continue
		}
		files, err := s.files.List(d.Path)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.Path != keep {
				out = append(out, f.Path)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Sweep deletes the orphans Fsck reports and returns how many it removed. It
// never touches an item that has a manifest; if a manifest is damaged, that
// item's data is left alone too (it may be recoverable). Chunks stay in the
// chunk store until the pack's GC.
func (s *Store) Sweep() (int, error) {
	s.gate.Lock()
	defer s.gate.Unlock()
	entries, err := s.List()
	if err != nil {
		return 0, err
	}
	live := map[string]string{}
	for _, e := range entries {
		if e.Err != nil {
			// A damaged manifest: keep its data folder, it may be recoverable.
			live[e.Meta.Name] = ""
			continue
		}
		live[e.Meta.Name] = e.Meta.Path
	}
	orph, err := s.orphans(live)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range orph {
		info, err := s.files.Stat(p)
		if err != nil {
			continue
		}
		if info.IsDir {
			err = s.files.RemoveDir(p, true)
		} else {
			err = s.files.Delete(p)
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
