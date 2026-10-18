package pack

import (
	"errors"
	"fmt"
)

// Errors. Use errors.Is.
var (
	ErrInvalidPath = errors.New("pack: invalid path")
	ErrNotFound    = errors.New("pack: not found")
	ErrIsDir       = errors.New("pack: is a folder")
	ErrNotDir      = errors.New("pack: not a folder")
	ErrNotEmpty    = errors.New("pack: folder not empty")
	ErrTooLarge    = errors.New("pack: file too large")
	ErrFolderFull  = errors.New("pack: folder has too many entries")
	// ErrCorrupt: a chunk, file or manifest does not match its checksum or size.
	ErrCorrupt = errors.New("pack: corrupt data")
	// ErrMissing: a chunk the manifest refers to is not in the chunk store.
	ErrMissing = errors.New("pack: missing chunk")

	// ErrChunkNotFound and ErrManifestNotFound are what backends return.
	ErrChunkNotFound    = errors.New("pack: chunk not in store")
	ErrManifestNotFound = errors.New("pack: manifest not in store")
)

// ChunkError says which chunk of which file failed. It wraps ErrCorrupt or
// ErrMissing.
type ChunkError struct {
	Path  string
	Index int    // chunk number in the file, -1 for whole-file checks
	ID    string // chunk id
	Err   error
	Why   string
}

func (e *ChunkError) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("pack: %s: %v: %s", e.Path, e.Err, e.Why)
	}
	return fmt.Sprintf("pack: %s: chunk %d (%.12s): %v: %s", e.Path, e.Index, e.ID, e.Err, e.Why)
}
func (e *ChunkError) Unwrap() error { return e.Err }

func corruptf(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}
