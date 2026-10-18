package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ChunkStore stores immutable chunks under their SHA-256 id (64 lower-case
// hex characters). PutChunk must be atomic and may overwrite. Backends must
// reject ids that are not 64 lower-case hex characters (use ValidID).
type ChunkStore interface {
	PutChunk(id string, data []byte) error
	// GetChunk returns ErrChunkNotFound (errors.Is) if absent.
	GetChunk(id string) ([]byte, error)
	HasChunk(id string) (bool, error)
	// DeleteChunk is a no-op if the chunk is absent.
	DeleteChunk(id string) error
	ListChunks() ([]string, error)
}

// ManifestStore stores one manifest blob per folder, under a key that is
// 64 lower-case hex characters (see FolderKey). SaveManifest must be atomic:
// after a crash a load returns the old or the new bytes, never a mix.
type ManifestStore interface {
	// LoadManifest returns ErrManifestNotFound (errors.Is) if absent.
	LoadManifest(key string) ([]byte, error)
	SaveManifest(key string, data []byte) error
	DeleteManifest(key string) error
	ListManifests() ([]string, error)
}

// ValidID reports whether s is 64 lower-case hex characters.
func ValidID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ChunkID returns the id of a chunk.
func ChunkID(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// FolderKey is the manifest key of a folder path ("" is the root).
func FolderKey(folder string) string {
	h := sha256.Sum256([]byte("redx-pack-folder\x00" + folder))
	return hex.EncodeToString(h[:])
}

// ---- in-memory fake -------------------------------------------------------

// MemBackend implements ChunkStore and ManifestStore in memory. It is the
// test fake; its hooks and raw accessors let tests inject failures and
// damage.
type MemBackend struct {
	mu     sync.Mutex
	chunks map[string][]byte
	mans   map[string][]byte
	// Hook, if set, is called before every mutating call with op
	// "put-chunk", "delete-chunk", "save-manifest" or "delete-manifest" and
	// the id or key. A non-nil error fails the call without changing
	// anything (simulating a crash at that point).
	Hook func(op, id string) error
}

func NewMemBackend() *MemBackend {
	return &MemBackend{chunks: map[string][]byte{}, mans: map[string][]byte{}}
}

func (m *MemBackend) hook(op, id string) error {
	if m.Hook != nil {
		return m.Hook(op, id)
	}
	return nil
}

func (m *MemBackend) PutChunk(id string, data []byte) error {
	if !ValidID(id) {
		return fmt.Errorf("pack: bad chunk id %q", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hook("put-chunk", id); err != nil {
		return err
	}
	m.chunks[id] = append([]byte(nil), data...)
	return nil
}

func (m *MemBackend) GetChunk(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.chunks[id]
	if !ok {
		return nil, ErrChunkNotFound
	}
	return append([]byte(nil), d...), nil
}

func (m *MemBackend) HasChunk(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.chunks[id]
	return ok, nil
}

func (m *MemBackend) DeleteChunk(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hook("delete-chunk", id); err != nil {
		return err
	}
	delete(m.chunks, id)
	return nil
}

func (m *MemBackend) ListChunks() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedKeys(m.chunks), nil
}

func (m *MemBackend) LoadManifest(key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.mans[key]
	if !ok {
		return nil, ErrManifestNotFound
	}
	return append([]byte(nil), d...), nil
}

func (m *MemBackend) SaveManifest(key string, data []byte) error {
	if !ValidID(key) {
		return fmt.Errorf("pack: bad manifest key %q", key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hook("save-manifest", key); err != nil {
		return err
	}
	m.mans[key] = append([]byte(nil), data...)
	return nil
}

func (m *MemBackend) DeleteManifest(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.hook("delete-manifest", key); err != nil {
		return err
	}
	delete(m.mans, key)
	return nil
}

func (m *MemBackend) ListManifests() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortedKeys(m.mans), nil
}

// SetRawChunk replaces a chunk without validation (test damage).
func (m *MemBackend) SetRawChunk(id string, data []byte) {
	m.mu.Lock()
	m.chunks[id] = append([]byte(nil), data...)
	m.mu.Unlock()
}

// SetRawManifest replaces a manifest without validation (test damage).
func (m *MemBackend) SetRawManifest(key string, data []byte) {
	m.mu.Lock()
	m.mans[key] = append([]byte(nil), data...)
	m.mu.Unlock()
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- directory backend ----------------------------------------------------

// DirBackend keeps chunks in Root/chunks/<first two hex>/<id> and manifests
// in Root/manifests/<key>.manifest. Writes go to a ".tmp-" file in the target
// directory, are fsynced, then renamed into place; leftovers of a crash are
// ignored by the listings (RemoveTemp deletes them). Ids and keys are
// validated, so no caller input can escape Root.
type DirBackend struct{ Root string }

func (d DirBackend) chunkPath(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("pack: bad chunk id %q", id)
	}
	return filepath.Join(d.Root, "chunks", id[:2], id), nil
}

func (d DirBackend) manPath(key string) (string, error) {
	if !ValidID(key) {
		return "", fmt.Errorf("pack: bad manifest key %q", key)
	}
	return filepath.Join(d.Root, "manifests", key+".manifest"), nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if df, err := os.Open(dir); err == nil { // best effort; not supported on every OS
		df.Sync()
		df.Close()
	}
	return nil
}

func (d DirBackend) PutChunk(id string, data []byte) error {
	p, err := d.chunkPath(id)
	if err != nil {
		return err
	}
	return atomicWrite(p, data)
}

func (d DirBackend) GetChunk(id string) ([]byte, error) {
	p, err := d.chunkPath(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrChunkNotFound
	}
	return b, err
}

func (d DirBackend) HasChunk(id string) (bool, error) {
	p, err := d.chunkPath(id)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil && st.Mode().IsRegular(), err
}

func (d DirBackend) DeleteChunk(id string) error {
	p, err := d.chunkPath(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (d DirBackend) ListChunks() ([]string, error) {
	var out []string
	subs, err := os.ReadDir(filepath.Join(d.Root, "chunks"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, s := range subs {
		if !s.IsDir() {
			continue
		}
		fs, err := os.ReadDir(filepath.Join(d.Root, "chunks", s.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			if ValidID(f.Name()) && strings.HasPrefix(f.Name(), s.Name()) {
				out = append(out, f.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d DirBackend) LoadManifest(key string) ([]byte, error) {
	p, err := d.manPath(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrManifestNotFound
	}
	return b, err
}

func (d DirBackend) SaveManifest(key string, data []byte) error {
	p, err := d.manPath(key)
	if err != nil {
		return err
	}
	return atomicWrite(p, data)
}

func (d DirBackend) DeleteManifest(key string) error {
	p, err := d.manPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (d DirBackend) ListManifests() ([]string, error) {
	fs, err := os.ReadDir(filepath.Join(d.Root, "manifests"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fs {
		if k := strings.TrimSuffix(f.Name(), ".manifest"); k != f.Name() && ValidID(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RemoveTemp deletes ".tmp-" files left behind by a crash. Call it at start
// up, before any writer is active.
func (d DirBackend) RemoveTemp() (int, error) {
	n := 0
	err := filepath.WalkDir(d.Root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !e.IsDir() && strings.HasPrefix(e.Name(), ".tmp-") {
			if os.Remove(p) == nil {
				n++
			}
		}
		return nil
	})
	return n, err
}
