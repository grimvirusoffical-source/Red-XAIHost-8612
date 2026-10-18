package media

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"redx/engine/pack"
)

// Reader gives random access to one item. It implements io.Reader,
// io.Seeker, io.ReaderAt and io.Closer. Every chunk it fetches is checked
// against its length and its SHA-256 id before any of its bytes are
// returned, so a damaged or missing chunk is an error (ErrCorrupt or
// pack.ErrMissing), never wrong data. A ranged read cannot check the
// whole-file hash; Store.Verify does that.
//
// ReadAt may be called from several goroutines at once; Read and Seek keep
// one position and must not be shared. Reading a few bytes costs one chunk
// fetch (1 MiB by default), but the last chunk is cached, so sequential
// reads fetch each chunk once.
type Reader struct {
	s    *Store
	meta Meta
	info pack.Info

	pos int64

	mu       sync.Mutex
	cacheIdx int
	cache    []byte
}

// Meta returns the item's manifest.
func (r *Reader) Meta() Meta { return r.meta }

// Size is the item's size in bytes.
func (r *Reader) Size() int64 { return r.info.Size }

// Close releases the cache. Further reads fail.
func (r *Reader) Close() error {
	r.mu.Lock()
	r.cache = nil
	r.cacheIdx = -2
	r.mu.Unlock()
	return nil
}

var errClosed = errors.New("media: read from closed reader")

func (r *Reader) chunk(i int) ([]byte, error) {
	r.mu.Lock()
	if r.cacheIdx == -2 {
		r.mu.Unlock()
		return nil, errClosed
	}
	if r.cache != nil && r.cacheIdx == i {
		c := r.cache
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()
	id := r.info.Chunks[i]
	data, err := r.s.chunks.GetChunk(id)
	if errors.Is(err, pack.ErrChunkNotFound) {
		return nil, fmt.Errorf("%w: %q chunk %d is not in the store", pack.ErrMissing, r.meta.Name, i)
	}
	if err != nil {
		return nil, err
	}
	want := expectedChunkLen(r.info, i)
	if len(data) != want {
		return nil, corruptf("%q chunk %d is %d bytes, expected %d", r.meta.Name, i, len(data), want)
	}
	if pack.ChunkID(data) != id {
		return nil, corruptf("%q chunk %d does not match its hash", r.meta.Name, i)
	}
	r.mu.Lock()
	if r.cacheIdx != -2 {
		r.cache, r.cacheIdx = data, i
	}
	r.mu.Unlock()
	return data, nil
}

func expectedChunkLen(f pack.Info, i int) int {
	if i < len(f.Chunks)-1 {
		return f.ChunkSize
	}
	return int(f.Size - int64(len(f.Chunks)-1)*int64(f.ChunkSize))
}

// ReadAt implements io.ReaderAt.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("media: negative offset")
	}
	size := r.info.Size
	if off >= size {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, io.EOF
	}
	cs := int64(r.info.ChunkSize)
	n := 0
	for n < len(p) && off < size {
		i := int(off / cs)
		c, err := r.chunk(i)
		if err != nil {
			return n, err
		}
		in := int(off - int64(i)*cs)
		m := copy(p[n:], c[in:])
		n += m
		off += int64(m)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// Seek implements io.Seeker.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		base = r.info.Size
	default:
		return 0, errors.New("media: bad seek whence")
	}
	np := base + offset
	if np < 0 || (offset > 0 && np < base) {
		return 0, errors.New("media: bad seek offset")
	}
	r.pos = np
	return np, nil
}

// ParseRange parses an HTTP Range header value against a file of the given
// size and returns the offset and length to serve.
//
//	bytes=0-99   the first 100 bytes     bytes=500-    from 500 to the end
//	bytes=-200   the last 200 bytes      bytes=0-9999  clipped to the file
//
// ErrBadRange means the header is malformed (HTTP says to ignore it and
// serve the whole file), ErrMultiRange that several ranges were asked for
// (serve the whole file or ask again), ErrUnsatisfiable that the range is
// outside the file (HTTP 416, with "Content-Range: bytes */size").
func ParseRange(header string, size int64) (off, length int64, err error) {
	h := strings.TrimSpace(header)
	i := strings.IndexByte(h, '=')
	if i < 0 || !strings.EqualFold(strings.TrimSpace(h[:i]), "bytes") {
		return 0, 0, ErrBadRange
	}
	spec := strings.TrimSpace(h[i+1:])
	if strings.Contains(spec, ",") {
		parts := strings.Split(spec, ",")
		n := 0
		for _, p := range parts {
			if strings.TrimSpace(p) != "" {
				n++
			}
		}
		if n > 1 {
			return 0, 0, ErrMultiRange
		}
		spec = strings.TrimSpace(strings.Join(parts, ""))
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, ErrBadRange
	}
	a, b := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	if a == "" && b == "" {
		return 0, 0, ErrBadRange
	}
	if a == "" { // suffix
		n, err := parseUint(b)
		if err != nil {
			return 0, 0, ErrBadRange
		}
		if n == 0 || size == 0 {
			return 0, 0, ErrUnsatisfiable
		}
		if n > size {
			n = size
		}
		return size - n, n, nil
	}
	start, err := parseUint(a)
	if err != nil {
		return 0, 0, ErrBadRange
	}
	end := size - 1
	if b != "" {
		e, err := parseUint(b)
		if err != nil || e < start {
			return 0, 0, ErrBadRange
		}
		if e < end {
			end = e
		}
	}
	if start >= size {
		return 0, 0, ErrUnsatisfiable
	}
	return start, end - start + 1, nil
}

func parseUint(s string) (int64, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrBadRange
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// ContentRange formats the Content-Range header value of a served range.
func ContentRange(off, length, size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, size)
}
