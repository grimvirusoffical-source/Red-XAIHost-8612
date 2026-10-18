package idx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Blob stores named byte blobs (index snapshots). Save must be atomic: after
// a crash Load returns either the previous or the new contents, never a mix.
type Blob interface {
	// Load returns ErrNotExist (errors.Is) if there is no blob.
	Load(name string) ([]byte, error)
	Save(name string, data []byte) error
}

// MemBlob is an in-memory Blob for tests.
type MemBlob struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemBlob() *MemBlob { return &MemBlob{m: map[string][]byte{}} }

func (b *MemBlob) Load(name string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.m[name]
	if !ok {
		return nil, ErrNotExist
	}
	return append([]byte(nil), d...), nil
}

func (b *MemBlob) Save(name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[name] = append([]byte(nil), data...)
	return nil
}

// DirBlob keeps each blob in a file of Dir, written to a temporary file,
// fsynced and renamed into place. Leftover temporary files from a crash are
// ignored.
type DirBlob struct{ Dir string }

// fileName maps an index name to a safe file name (no separators, no "..").
func (d DirBlob) fileName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		}
		if sb.Len() >= 40 {
			break
		}
	}
	h := sha256.Sum256([]byte(name))
	return sb.String() + "-" + hex.EncodeToString(h[:4]) + ".idxsnap"
}

func (d DirBlob) Load(name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(d.Dir, d.fileName(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotExist
	}
	return data, err
}

func (d DirBlob) Save(name string, data []byte) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(d.Dir, ".tmp-*")
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
	if err := os.Rename(tmp, filepath.Join(d.Dir, d.fileName(name))); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, err := os.Open(d.Dir); err == nil { // best effort: not supported everywhere
		dir.Sync()
		dir.Close()
	}
	return nil
}
