package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// GCResult says what GC removed.
type GCResult struct {
	ChunksRemoved    int
	ManifestsRemoved int
}

// GC deletes chunks that no manifest refers to (left by deleted or replaced
// files and by interrupted Puts) and manifests of folders that are no longer
// reachable from the root (left by an interrupted RemoveDir). It blocks all
// other operations while it runs. If any reachable manifest cannot be read
// or is corrupt, GC changes nothing and returns the error: it never deletes
// on the basis of an incomplete view.
func (p *Pack) GC() (GCResult, error) {
	p.gc.Lock()
	defer p.gc.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()

	refs := map[string]bool{}
	keys := map[string]bool{}
	var mark func(folder string) error
	mark = func(folder string) error {
		m, err := p.loadRO(folder)
		if err != nil {
			return err
		}
		keys[FolderKey(folder)] = true
		for _, f := range m.files {
			for _, c := range f.Chunks {
				refs[c] = true
			}
		}
		for _, n := range sortedNames(m.dirs) {
			sub := n
			if folder != "" {
				sub = folder + "/" + n
			}
			if err := mark(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := mark(""); err != nil {
		return GCResult{}, fmt.Errorf("pack: gc aborted, nothing deleted: %w", err)
	}
	var res GCResult
	chunks, err := p.chunks.ListChunks()
	if err != nil {
		return res, err
	}
	for _, c := range chunks {
		if !refs[c] {
			if err := p.chunks.DeleteChunk(c); err != nil {
				return res, err
			}
			res.ChunksRemoved++
		}
	}
	mans, err := p.mans.ListManifests()
	if err != nil {
		return res, err
	}
	for _, k := range mans {
		if !keys[k] {
			if err := p.mans.DeleteManifest(k); err != nil {
				return res, err
			}
			res.ManifestsRemoved++
		}
	}
	return res, nil
}

// Problem is one integrity failure found by Fsck.
type Problem struct {
	Path  string // file or folder
	Chunk int    // chunk number, -1 if the problem is not about one chunk
	ID    string
	Err   error // wraps ErrCorrupt or ErrMissing
}

func (p Problem) String() string {
	return fmt.Sprintf("%s (chunk %d): %v", p.Path, p.Chunk, p.Err)
}

// Report is the result of Fsck.
type Report struct {
	Folders  int
	Files    int
	Chunks   int // distinct chunks referenced by readable manifests
	Problems []Problem
	// OrphanChunks and OrphanManifests are stored but unreferenced (GC
	// removes them). They are left empty and OrphansSkipped is set when a
	// manifest was unreadable, because the view is then incomplete.
	OrphanChunks    []string
	OrphanManifests []string
	OrphansSkipped  bool
}

// OK reports whether no problems were found.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

// Fsck checks the whole pack. Every manifest is decoded and sealed-checked.
// Shallow mode (deep=false) checks that each referenced chunk exists;
// deep mode reads every chunk, checks its size and hash, and checks each
// file's whole-file hash. Problems are reported, never fixed.
func (p *Pack) Fsck(deep bool) (*Report, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rep := &Report{}
	refs := map[string]bool{}
	keys := map[string]bool{}
	var visit func(folder string) error
	visit = func(folder string) error {
		m, err := p.loadRO(folder)
		if err != nil {
			if errors.Is(err, ErrCorrupt) {
				rep.Problems = append(rep.Problems, Problem{Path: folder, Chunk: -1, Err: err})
				rep.OrphansSkipped = true
				return nil
			}
			return err
		}
		rep.Folders++
		keys[FolderKey(folder)] = true
		for _, n := range m.names() {
			if f, ok := m.files[n]; ok {
				rep.Files++
				if err := p.checkFile(rep, &f.Info, refs, deep); err != nil {
					return err
				}
				continue
			}
			sub := n
			if folder != "" {
				sub = folder + "/" + n
			}
			if err := visit(sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(""); err != nil {
		return nil, err
	}
	rep.Chunks = len(refs)
	if !rep.OrphansSkipped {
		chunks, err := p.chunks.ListChunks()
		if err != nil {
			return nil, err
		}
		for _, c := range chunks {
			if !refs[c] {
				rep.OrphanChunks = append(rep.OrphanChunks, c)
			}
		}
		mans, err := p.mans.ListManifests()
		if err != nil {
			return nil, err
		}
		for _, k := range mans {
			if !keys[k] {
				rep.OrphanManifests = append(rep.OrphanManifests, k)
			}
		}
		sort.Strings(rep.OrphanChunks)
		sort.Strings(rep.OrphanManifests)
	}
	return rep, nil
}

func (p *Pack) checkFile(rep *Report, f *Info, refs map[string]bool, deep bool) error {
	problem := func(i int, kind error, why string) {
		id := ""
		if i >= 0 {
			id = f.Chunks[i]
		}
		rep.Problems = append(rep.Problems, Problem{Path: f.Path, Chunk: i, ID: id,
			Err: &ChunkError{Path: f.Path, Index: i, ID: id, Err: kind, Why: why}})
	}
	whole := sha256.New()
	clean := true
	for i, id := range f.Chunks {
		refs[id] = true
		if !deep {
			ok, err := p.chunks.HasChunk(id)
			if err != nil {
				return err
			}
			if !ok {
				problem(i, ErrMissing, "chunk is not in the store")
			}
			continue
		}
		data, err := p.chunks.GetChunk(id)
		if errors.Is(err, ErrChunkNotFound) {
			problem(i, ErrMissing, "chunk is not in the store")
			clean = false
			continue
		}
		if err != nil {
			return err
		}
		if want := f.expectedChunkLen(i); len(data) != want {
			problem(i, ErrCorrupt, fmt.Sprintf("chunk is %d bytes, expected %d (truncated or extended)", len(data), want))
			clean = false
			continue
		}
		if ChunkID(data) != id {
			problem(i, ErrCorrupt, "chunk hash does not match its id")
			clean = false
			continue
		}
		whole.Write(data)
	}
	if deep && clean && hex.EncodeToString(whole.Sum(nil)) != f.SHA256 {
		problem(-1, ErrCorrupt, "file SHA-256 does not match manifest")
	}
	return nil
}
