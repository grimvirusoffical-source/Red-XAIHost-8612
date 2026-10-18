package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"sort"
	"sync"
)

// Defaults and limits.
const (
	DefaultChunkSize        = 1 << 20  // 1 MiB
	MaxChunkSize            = 64 << 20 // 64 MiB
	DefaultMaxFileSize      = 64 << 30 // 64 GiB
	DefaultMaxFolderEntries = 50000
)

// Config configures a Pack. Zero values mean the defaults.
type Config struct {
	// ChunkSize is the chunk size for new files (1 to MaxChunkSize bytes).
	// Each file records its own chunk size, so it can change later.
	ChunkSize int
	// MaxFileSize bounds one Put. The whole file's chunk list lives in its
	// folder's manifest, so keep ChunkSize*65536 >= MaxFileSize.
	MaxFileSize int64
	// MaxFolderEntries bounds files plus sub-folders in one folder: the
	// manifest is rewritten in full on every change.
	MaxFolderEntries int
}

// Pack is a .red-xai-pack store. It is safe for concurrent use within one
// process; two processes must not share one backend.
type Pack struct {
	chunks ChunkStore
	mans   ManifestStore
	cfg    Config

	mu sync.RWMutex // manifests: read-modify-write
	gc sync.RWMutex // Put holds R for its whole run; GC holds W

	cacheMu sync.Mutex
	cache   map[string]cacheEnt
}

// cacheEnt is a decoded manifest together with the exact bytes it was decoded
// from. Manifests are always re-read from the store and only the decoding
// is skipped when the bytes are identical, so damage to a stored manifest is
// never hidden by the cache.
type cacheEnt struct {
	raw []byte
	m   *manifest
}

const maxCacheEntries = 512

// New creates a Pack over the given stores.
func New(chunks ChunkStore, mans ManifestStore, cfg Config) (*Pack, error) {
	if chunks == nil || mans == nil {
		return nil, errors.New("pack: nil store")
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.ChunkSize < 1 || cfg.ChunkSize > MaxChunkSize {
		return nil, fmt.Errorf("pack: ChunkSize must be 1..%d", MaxChunkSize)
	}
	if cfg.MaxFileSize == 0 {
		cfg.MaxFileSize = DefaultMaxFileSize
	}
	if cfg.MaxFileSize < 0 || cfg.MaxFileSize > 1<<53 {
		return nil, errors.New("pack: bad MaxFileSize")
	}
	if cfg.MaxFolderEntries == 0 {
		cfg.MaxFolderEntries = DefaultMaxFolderEntries
	}
	return &Pack{chunks: chunks, mans: mans, cfg: cfg}, nil
}

// ---- manifests ------------------------------------------------------------

// loadRO returns the manifest of a folder for reading; the result is shared
// and must not be modified. A folder with no stored manifest is empty (it was
// created but never written to, or a crash hit between the parent update and
// the first write).
func (p *Pack) loadRO(folder string) (*manifest, error) {
	key := FolderKey(folder)
	data, err := p.mans.LoadManifest(key)
	if errors.Is(err, ErrManifestNotFound) {
		return newManifest(folder), nil
	}
	if err != nil {
		return nil, err
	}
	p.cacheMu.Lock()
	ent, ok := p.cache[key]
	p.cacheMu.Unlock()
	if ok && bytes.Equal(ent.raw, data) {
		return ent.m, nil
	}
	m, err := decodeManifest(data, folder)
	if err != nil {
		return nil, fmt.Errorf("folder %q: %w", folder, err)
	}
	p.remember(key, data, m)
	return m, nil
}

func (p *Pack) remember(key string, raw []byte, m *manifest) {
	p.cacheMu.Lock()
	if p.cache == nil || len(p.cache) >= maxCacheEntries {
		p.cache = map[string]cacheEnt{}
	}
	p.cache[key] = cacheEnt{raw: raw, m: m}
	p.cacheMu.Unlock()
}

// load returns a private copy of the manifest that the caller may modify.
func (p *Pack) load(folder string) (*manifest, error) {
	m, err := p.loadRO(folder)
	if err != nil {
		return nil, err
	}
	return m.clone(), nil
}

func (p *Pack) save(m *manifest) error {
	data := m.encode()
	if err := p.mans.SaveManifest(FolderKey(m.folder), data); err != nil {
		return err
	}
	p.remember(FolderKey(m.folder), data, m.clone())
	return nil
}

// walkTo checks that every folder on comps exists and returns the manifest
// of the last one (the root for no components). The result is read-only.
func (p *Pack) walkTo(comps []string) (*manifest, error) {
	m, err := p.loadRO("")
	if err != nil {
		return nil, err
	}
	for i, c := range comps {
		if _, ok := m.files[c]; ok {
			return nil, fmt.Errorf("%w: %s", ErrNotDir, joinPath(comps[:i+1]))
		}
		if _, ok := m.dirs[c]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, joinPath(comps[:i+1]))
		}
		if m, err = p.loadRO(joinPath(comps[:i+1])); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// ensureDirs creates every folder on comps (parent first). It checks all
// conflicts before writing anything. Caller holds p.mu for writing.
func (p *Pack) ensureDirs(comps []string) (*manifest, error) {
	ms := make([]*manifest, 0, len(comps)+1)
	m, err := p.load("")
	if err != nil {
		return nil, err
	}
	ms = append(ms, m)
	created := make([]bool, len(comps))
	for i, c := range comps {
		if _, ok := m.files[c]; ok {
			return nil, fmt.Errorf("%w: %s is a file", ErrNotDir, joinPath(comps[:i+1]))
		}
		if _, ok := m.dirs[c]; ok {
			if m, err = p.load(joinPath(comps[:i+1])); err != nil {
				return nil, err
			}
		} else {
			created[i] = true
			m = newManifest(joinPath(comps[:i+1]))
		}
		ms = append(ms, m)
	}
	for i, c := range comps {
		if !created[i] {
			continue
		}
		if ms[i].count() >= p.cfg.MaxFolderEntries {
			return nil, fmt.Errorf("%w: %q", ErrFolderFull, ms[i].folder)
		}
		ms[i].dirs[c] = ms[i].alloc()
		// parent first: a crash before the child is written leaves an
		// empty folder, never an unreachable one.
		if err := p.save(ms[i]); err != nil {
			return nil, err
		}
	}
	return ms[len(ms)-1], nil
}

// ---- operations -----------------------------------------------------------

// Mkdir creates a folder and any missing parents. Existing folders are fine.
func (p *Pack) Mkdir(path string) error {
	comps, err := CleanPath(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = p.ensureDirs(comps)
	return err
}

func (p *Pack) ensureChunk(id string, data []byte) error {
	if ok, err := p.chunks.HasChunk(id); err != nil {
		return err
	} else if ok {
		// Dedupe only against a chunk that reads back intact; otherwise
		// heal it by rewriting.
		if old, err := p.chunks.GetChunk(id); err == nil && bytes.Equal(old, data) {
			return nil
		}
	}
	return p.chunks.PutChunk(id, data)
}

// Put stores the contents of r as the file at path, creating folders as
// needed and replacing an existing file. The data is streamed: one chunk is
// held in memory at a time. The file only becomes visible when its manifest
// is saved; a failure or crash before that leaves unreferenced chunks, which
// GC removes, and the old file (if any) untouched.
func (p *Pack) Put(path string, r io.Reader) (Info, error) {
	comps, err := CleanPath(path)
	if err != nil {
		return Info{}, err
	}
	if len(comps) == 0 {
		return Info{}, badPath(path, "a file needs a name")
	}
	name := comps[len(comps)-1]
	p.gc.RLock()
	defer p.gc.RUnlock()

	cs := p.cfg.ChunkSize
	buf := make([]byte, cs)
	whole := sha256.New()
	var size int64
	var ids []string
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			size += int64(n)
			if size > p.cfg.MaxFileSize {
				return Info{}, fmt.Errorf("%w: limit is %d bytes", ErrTooLarge, p.cfg.MaxFileSize)
			}
			whole.Write(buf[:n])
			id := ChunkID(buf[:n])
			if err := p.ensureChunk(id, buf[:n]); err != nil {
				return Info{}, err
			}
			ids = append(ids, id)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return Info{}, rerr
		}
	}
	fr := &fileRec{}
	fr.Name, fr.Path = name, joinPath(comps)
	fr.Size, fr.ChunkSize, fr.Chunks = size, cs, ids
	fr.SHA256 = hex.EncodeToString(whole.Sum(nil))

	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.ensureDirs(comps[:len(comps)-1])
	if err != nil {
		return Info{}, err
	}
	if _, isDir := m.dirs[name]; isDir {
		return Info{}, fmt.Errorf("%w: %s", ErrIsDir, fr.Path)
	}
	if old, ok := m.files[name]; ok {
		fr.id = old.id
	} else {
		if m.count() >= p.cfg.MaxFolderEntries {
			return Info{}, fmt.Errorf("%w: %q", ErrFolderFull, m.folder)
		}
		fr.id = m.alloc()
	}
	m.files[name] = fr
	if err := p.save(m); err != nil {
		return Info{}, err
	}
	info, _ := m.info(name)
	return info, nil
}

func (p *Pack) lookup(path string) (*manifest, string, error) {
	comps, err := CleanPath(path)
	if err != nil {
		return nil, "", err
	}
	if len(comps) == 0 {
		return nil, "", badPath(path, "a file needs a name")
	}
	m, err := p.walkTo(comps[:len(comps)-1])
	if err != nil {
		return nil, "", err
	}
	return m, comps[len(comps)-1], nil
}

// Stat describes a file or folder ("" is the root).
func (p *Pack) Stat(path string) (Info, error) {
	comps, err := CleanPath(path)
	if err != nil {
		return Info{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(comps) == 0 {
		return Info{IsDir: true}, nil
	}
	m, err := p.walkTo(comps[:len(comps)-1])
	if err != nil {
		return Info{}, err
	}
	info, ok := m.info(comps[len(comps)-1])
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, joinPath(comps))
	}
	return info, nil
}

// List returns the entries of a folder sorted by name ("" is the root).
// Chunk lists are not included; use Stat for them.
func (p *Pack) List(folder string) ([]Info, error) {
	comps, err := CleanPath(folder)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	m, err := p.walkTo(comps)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, m.count())
	for _, n := range m.names() {
		i, _ := m.info(n)
		i.Chunks = nil
		out = append(out, i)
	}
	return out, nil
}

// Get opens the file at path for streaming. The returned reader fetches one
// chunk at a time and verifies it before returning any of its bytes: a chunk
// that is missing fails with an error wrapping ErrMissing, a chunk that is
// the wrong size (truncated) or whose SHA-256 does not match its id fails with
// ErrCorrupt (both as *ChunkError). When the last chunk has been read the
// total size and the whole-file SHA-256 are checked before io.EOF is
// returned, so a clean EOF means the file is intact. Errors are sticky.
func (p *Pack) Get(path string) (io.ReadCloser, Info, error) {
	p.mu.RLock()
	m, name, err := p.lookup(path)
	if err != nil {
		p.mu.RUnlock()
		return nil, Info{}, err
	}
	info, ok := m.info(name)
	p.mu.RUnlock()
	if !ok {
		return nil, Info{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if info.IsDir {
		return nil, Info{}, fmt.Errorf("%w: %s", ErrIsDir, info.Path)
	}
	return &reader{p: p, info: info, whole: sha256.New()}, info, nil
}

type reader struct {
	p     *Pack
	info  Info
	next  int
	buf   []byte
	whole hash.Hash
	read  int64
	err   error
}

func (r *reader) fail(idx int, kind error, why string) error {
	id := ""
	if idx >= 0 && idx < len(r.info.Chunks) {
		id = r.info.Chunks[idx]
	}
	r.err = &ChunkError{Path: r.info.Path, Index: idx, ID: id, Err: kind, Why: why}
	return r.err
}

func (r *reader) Read(b []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(b) == 0 {
		return 0, nil
	}
	for len(r.buf) == 0 {
		if r.next >= len(r.info.Chunks) {
			if r.read != r.info.Size {
				return 0, r.fail(-1, ErrCorrupt, fmt.Sprintf("read %d bytes, manifest says %d", r.read, r.info.Size))
			}
			if hex.EncodeToString(r.whole.Sum(nil)) != r.info.SHA256 {
				return 0, r.fail(-1, ErrCorrupt, "file SHA-256 does not match manifest")
			}
			r.err = io.EOF
			return 0, io.EOF
		}
		i := r.next
		id := r.info.Chunks[i]
		data, err := r.p.chunks.GetChunk(id)
		if errors.Is(err, ErrChunkNotFound) {
			return 0, r.fail(i, ErrMissing, "chunk is not in the store")
		}
		if err != nil {
			r.err = err
			return 0, err
		}
		if want := r.info.expectedChunkLen(i); len(data) != want {
			return 0, r.fail(i, ErrCorrupt, fmt.Sprintf("chunk is %d bytes, expected %d (truncated or extended)", len(data), want))
		}
		if ChunkID(data) != id {
			return 0, r.fail(i, ErrCorrupt, "chunk hash does not match its id")
		}
		r.whole.Write(data)
		r.read += int64(len(data))
		r.buf = data
		r.next++
	}
	n := copy(b, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *reader) Close() error {
	if r.err == nil {
		r.err = errors.New("pack: read from closed file")
	}
	r.buf = nil
	return nil
}

// Verify reads the whole file and checks every chunk and the file hash.
func (p *Pack) Verify(path string) error {
	rc, _, err := p.Get(path)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// Delete removes a file. Its chunks stay in the chunk store until GC (they
// may be shared with other files).
func (p *Pack) Delete(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, name, err := p.lookup(path)
	if err != nil {
		return err
	}
	if _, ok := m.dirs[name]; ok {
		return fmt.Errorf("%w: %s (use RemoveDir)", ErrIsDir, path)
	}
	if _, ok := m.files[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	m = m.clone()
	delete(m.files, name)
	return p.save(m)
}

// RemoveDir removes a folder. A non-empty folder needs recursive. The
// folder disappears when its parent manifest is saved; the sub-folder
// manifests are deleted afterwards (a crash in between leaves orphan
// manifests, which GC removes). Chunks are left for GC.
func (p *Pack) RemoveDir(path string, recursive bool) error {
	comps, err := CleanPath(path)
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return badPath(path, "the root cannot be removed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	parent, err := p.walkTo(comps[:len(comps)-1])
	if err != nil {
		return err
	}
	name := comps[len(comps)-1]
	if _, ok := parent.files[name]; ok {
		return fmt.Errorf("%w: %s", ErrNotDir, path)
	}
	if _, ok := parent.dirs[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	var keys []string
	if err := p.collectFolders(joinPath(comps), &keys, func(m *manifest) error {
		if !recursive && m.count() > 0 {
			return fmt.Errorf("%w: %s", ErrNotEmpty, path)
		}
		return nil
	}); err != nil {
		return err
	}
	parent = parent.clone()
	delete(parent.dirs, name)
	if err := p.save(parent); err != nil {
		return err
	}
	for _, k := range keys {
		if err := p.mans.DeleteManifest(k); err != nil {
			return err
		}
	}
	return nil
}

// collectFolders visits folder and all folders below it, calling check on
// each manifest and appending their manifest keys.
func (p *Pack) collectFolders(folder string, keys *[]string, check func(*manifest) error) error {
	m, err := p.loadRO(folder)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(m); err != nil {
			return err
		}
	}
	*keys = append(*keys, FolderKey(folder))
	for _, n := range sortedNames(m.dirs) {
		sub := n
		if folder != "" {
			sub = folder + "/" + n
		}
		if err := p.collectFolders(sub, keys, check); err != nil {
			return err
		}
	}
	return nil
}

func sortedNames(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Walk calls fn for every file and folder below folder in sorted,
// depth-first order (folders before their contents), not including folder
// itself. A non-nil error from fn stops the walk and is returned.
func (p *Pack) Walk(folder string, fn func(Info) error) error {
	comps, err := CleanPath(folder)
	if err != nil {
		return err
	}
	p.mu.RLock()
	m, err := p.walkTo(comps)
	p.mu.RUnlock()
	if err != nil {
		return err
	}
	return p.walk(m, fn)
}

func (p *Pack) walk(m *manifest, fn func(Info) error) error {
	for _, n := range m.names() {
		i, _ := m.info(n)
		if err := fn(i); err != nil {
			return err
		}
		if i.IsDir {
			p.mu.RLock()
			sub, err := p.loadRO(i.Path)
			p.mu.RUnlock()
			if err != nil {
				return err
			}
			if err := p.walk(sub, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
