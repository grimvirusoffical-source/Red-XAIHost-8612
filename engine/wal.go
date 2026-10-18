package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"redx/box"
)

// WAL segment layout:
//
//	header: "REDXWAL1" | gen u64le                         (16 bytes)
//	frame:  len u32le | crc32c(payload) u32le | payload
//	payload: lsn u64le | atMs varint | nops uvarint | op...
//	op:     kind u8 (1 set, 2 delete, 3 expire) | path str |
//	        [set: ttlKind u8 | ttlSeconds uvarint | expiresAt varint | value]
//
// A frame is one atomic commit. On load, the first frame that is short or
// fails its checksum ends the log: everything from there on is a torn tail
// (the process died mid-write) and is dropped.
const (
	walMagic     = "REDXWAL1"
	walHeaderLen = 16
	maxFrame     = 1 << 30
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

const (
	opSet    = 1
	opDelete = 2
	opExpire = 3
	opTouch  = 4
)

func opKindByte(k string) byte {
	switch k {
	case "set":
		return opSet
	case "delete":
		return opDelete
	case "touch":
		return opTouch
	}
	return opExpire
}

func opKindName(b byte) string {
	switch b {
	case opSet:
		return "set"
	case opDelete:
		return "delete"
	case opTouch:
		return "touch"
	}
	return "expire"
}

func encodeFrame(lsn uint64, atMs int64, ops []Op) []byte {
	b := make([]byte, 8, 64+len(ops)*48)
	b = binary.LittleEndian.AppendUint64(b, lsn)
	b = binary.AppendVarint(b, atMs)
	b = binary.AppendUvarint(b, uint64(len(ops)))
	for i := range ops {
		op := &ops[i]
		b = append(b, opKindByte(op.Kind))
		b = binary.AppendUvarint(b, uint64(len(op.Path)))
		b = append(b, op.Path...)
		if op.Kind == "set" {
			b = append(b, op.TTLKind)
			b = binary.AppendUvarint(b, uint64(op.TTLSeconds))
			b = binary.AppendVarint(b, op.ExpiresAtMs)
			b = box.AppendValue(b, op.Value)
		} else if op.Kind == "touch" {
			b = binary.AppendVarint(b, op.ExpiresAtMs)
		}
	}
	payload := b[8:]
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(b[4:8], crc32.Checksum(payload, crcTable))
	return b
}

func decodePayload(p []byte) (lsn uint64, atMs int64, ops []Op, err error) {
	if len(p) < 8 {
		return 0, 0, nil, io.ErrUnexpectedEOF
	}
	lsn = binary.LittleEndian.Uint64(p)
	p = p[8:]
	at, n := binary.Varint(p)
	if n <= 0 {
		return 0, 0, nil, io.ErrUnexpectedEOF
	}
	p = p[n:]
	atMs = at
	nops, n := binary.Uvarint(p)
	if n <= 0 || nops > uint64(len(p)) {
		return 0, 0, nil, io.ErrUnexpectedEOF
	}
	p = p[n:]
	for i := uint64(0); i < nops; i++ {
		if len(p) < 1 {
			return 0, 0, nil, io.ErrUnexpectedEOF
		}
		op := Op{Kind: opKindName(p[0])}
		if p[0] < opSet || p[0] > opTouch {
			return 0, 0, nil, fmt.Errorf("bad op kind %d", p[0])
		}
		kb := p[0]
		p = p[1:]
		l, n := binary.Uvarint(p)
		if n <= 0 || uint64(len(p)-n) < l {
			return 0, 0, nil, io.ErrUnexpectedEOF
		}
		op.Path = string(p[n : n+int(l)])
		p = p[n+int(l):]
		if kb == opSet {
			if len(p) < 1 {
				return 0, 0, nil, io.ErrUnexpectedEOF
			}
			op.TTLKind = p[0]
			p = p[1:]
			s, n := binary.Uvarint(p)
			if n <= 0 {
				return 0, 0, nil, io.ErrUnexpectedEOF
			}
			op.TTLSeconds = int(s)
			p = p[n:]
			ex, n := binary.Varint(p)
			if n <= 0 {
				return 0, 0, nil, io.ErrUnexpectedEOF
			}
			op.ExpiresAtMs = ex
			p = p[n:]
			v, rest, err := box.DecodeValue(p)
			if err != nil {
				return 0, 0, nil, err
			}
			op.Value = v
			p = rest
		} else if kb == opTouch {
			ex, n := binary.Varint(p)
			if n <= 0 {
				return 0, 0, nil, io.ErrUnexpectedEOF
			}
			op.ExpiresAtMs = ex
			p = p[n:]
		}
		ops = append(ops, op)
	}
	if len(p) != 0 {
		return 0, 0, nil, errors.New("trailing bytes in frame")
	}
	return lsn, atMs, ops, nil
}

// scanSegment parses a segment image. It returns the decoded records, the
// byte length of the valid prefix, and whether trailing garbage was found.
//
// A bad frame is only a torn tail if nothing valid follows it. If a valid
// frame is found after the damage, the log is corrupt in the middle and
// silently dropping the rest would lose acknowledged commits, so that is
// reported as ErrCorrupt (with the offset) unless repair is set.
type walRecord struct {
	lsn uint64
	at  int64
	ops []Op
}

func tryFrame(data []byte, off int) (rec walRecord, size int, ok bool) {
	if len(data)-off < 8 {
		return rec, 0, false
	}
	l := binary.LittleEndian.Uint32(data[off:])
	sum := binary.LittleEndian.Uint32(data[off+4:])
	if l > maxFrame || len(data)-off-8 < int(l) {
		return rec, 0, false
	}
	payload := data[off+8 : off+8+int(l)]
	if crc32.Checksum(payload, crcTable) != sum {
		return rec, 0, false
	}
	lsn, at, ops, err := decodePayload(payload)
	if err != nil {
		return rec, 0, false
	}
	return walRecord{lsn, at, ops}, 8 + int(l), true
}

func validFrameAfter(data []byte, from int) (int, bool) {
	for pos := from; pos+8 <= len(data); pos++ {
		if _, _, ok := tryFrame(data, pos); ok {
			return pos, true
		}
	}
	return 0, false
}

func scanSegment(data []byte, repair bool) (gen uint64, recs []walRecord, good int, torn bool, err error) {
	if len(data) < walHeaderLen {
		return 0, nil, 0, true, nil // header itself torn: an empty log
	}
	if string(data[:8]) != walMagic {
		return 0, nil, 0, false, fmt.Errorf("%w: WAL segment has a bad header", ErrCorrupt)
	}
	gen = binary.LittleEndian.Uint64(data[8:16])
	off := walHeaderLen
	for off < len(data) {
		rec, size, ok := tryFrame(data, off)
		if !ok {
			if pos, found := validFrameAfter(data, off+1); found && !repair {
				return gen, recs, off, false, fmt.Errorf("%w: damaged WAL record at byte %d with valid records after it at byte %d (not a torn tail; set Config.RepairWAL to drop everything from the damage on)", ErrCorrupt, off, pos)
			}
			return gen, recs, off, true, nil
		}
		recs = append(recs, rec)
		off += size
	}
	return gen, recs, off, false, nil
}

// ---------------------------------------------------------------------------

type durability int

const (
	durSync durability = iota
	durGroup
	durAsync
)

func parseDurability(s string) (durability, error) {
	switch strings.ToLower(s) {
	case "", "group", "group-commit", "groupcommit":
		return durGroup, nil
	case "sync":
		return durSync, nil
	case "async":
		return durAsync, nil
	}
	return 0, fmt.Errorf("engine: unknown durability %q (want sync, group or async)", s)
}

// persist owns the WAL segments of one file and implements the three
// durability modes. append() is called under the table lock (so frame order
// equals commit order); wait() is called after the lock is released.
type persist struct {
	base string
	dur  durability

	mu       sync.Mutex
	cond     *sync.Cond
	f        *os.File
	gen      uint64
	size     int64 // bytes durably written to the current segment (incl. header)
	buf      []byte
	seq      uint64 // frames appended
	flushed  uint64 // frames made durable
	flushing bool
	err      error

	fsyncs  atomic.Int64
	bytes   atomic.Int64
	batches atomic.Int64 // group-commit batches written
	frames  atomic.Int64

	// ioHook (tests) runs before every write/fsync; it may write part of
	// data itself and return an error to simulate EIO.
	ioHook func(op string, f *os.File, data []byte) error

	stop chan struct{}
	done chan struct{}
}

func segPath(base string, gen uint64) string { return fmt.Sprintf("%s.wal.%08d", base, gen) }

func listSegments(base string) ([]uint64, error) {
	dir := filepath.Dir(base)
	prefix := filepath.Base(base) + ".wal."
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var gens []uint64
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, prefix) {
			if g, err := strconv.ParseUint(n[len(prefix):], 10, 64); err == nil {
				gens = append(gens, g)
			}
		}
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i] < gens[j] })
	return gens, nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync() // not supported on every platform (Windows); best effort
	f.Close()
}

// openSegmentForAppend opens (creating if needed) segment gen, truncating it
// to validLen first when validLen >= 0.
func openSegmentForAppend(base string, gen uint64, validLen int64) (*os.File, int64, error) {
	path := segPath(base, gen)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	size := st.Size()
	if validLen >= 0 && validLen < size {
		if err := f.Truncate(validLen); err != nil {
			f.Close()
			return nil, 0, err
		}
		size = validLen
	}
	if size < walHeaderLen {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, 0, err
		}
		hdr := make([]byte, walHeaderLen)
		copy(hdr, walMagic)
		binary.LittleEndian.PutUint64(hdr[8:], gen)
		if _, err := f.WriteAt(hdr, 0); err != nil {
			f.Close()
			return nil, 0, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, 0, err
		}
		syncDir(filepath.Dir(base))
		size = walHeaderLen
	}
	if _, err := f.Seek(size, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, size, nil
}

func newPersist(base string, dur durability, asyncEvery time.Duration, f *os.File, gen uint64, size int64) *persist {
	p := &persist{base: base, dur: dur, f: f, gen: gen, size: size,
		stop: make(chan struct{}), done: make(chan struct{})}
	p.cond = sync.NewCond(&p.mu)
	if dur == durAsync {
		go p.asyncLoop(asyncEvery)
	} else {
		close(p.done)
	}
	return p
}

func (p *persist) asyncLoop(every time.Duration) {
	defer close(p.done)
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tk.C:
			p.mu.Lock()
			if !p.flushing && len(p.buf) > 0 {
				p.flushOnceLocked()
			}
			p.mu.Unlock()
		}
	}
}

// flushOnceLocked writes+fsyncs the buffered frames. mu held, !flushing.
// mu is released during I/O so other committers can keep buffering (this is
// what makes group commit batch).
func (p *persist) flushOnceLocked() {
	if len(p.buf) == 0 {
		return
	}
	data := p.buf
	p.buf = nil
	upTo := p.seq
	n := p.seq - p.flushed
	p.flushing = true
	f := p.f
	p.mu.Unlock()
	var err error
	if p.ioHook != nil {
		err = p.ioHook("write", f, data)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil && p.ioHook != nil {
		err = p.ioHook("sync", f, nil)
	}
	if err == nil {
		err = f.Sync()
		p.fsyncs.Add(1)
	}
	p.mu.Lock()
	p.flushing = false
	if err != nil {
		p.err = err
	} else {
		p.flushed = upTo
		p.size += int64(len(data))
		p.bytes.Add(int64(len(data)))
		p.batches.Add(1)
		p.frames.Add(int64(n))
	}
	p.cond.Broadcast()
}

// append buffers a frame and returns its ticket. In sync mode the frame is
// durable on return.
func (p *persist) append(frame []byte) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return 0, p.err
	}
	p.buf = append(p.buf, frame...)
	p.seq++
	t := p.seq
	if p.dur == durSync {
		for p.flushing {
			p.cond.Wait()
		}
		p.flushOnceLocked()
		return t, p.err
	}
	return t, nil
}

// wait blocks until the ticket is durable (group), or returns at once
// (async/sync-already-done).
func (p *persist) wait(ticket uint64) error {
	if ticket == 0 || p.dur == durAsync {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.flushed < ticket && p.err == nil {
		if !p.flushing {
			p.flushOnceLocked() // become the group-commit leader
		} else {
			p.cond.Wait()
		}
	}
	if p.flushed >= ticket {
		return nil
	}
	return p.err
}

// flushAll makes everything buffered durable.
func (p *persist) flushAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		for p.flushing {
			p.cond.Wait()
		}
		if len(p.buf) == 0 || p.err != nil {
			return p.err
		}
		p.flushOnceLocked()
	}
}

// walSize is the current segment size including buffered frames.
func (p *persist) walSize() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size + int64(len(p.buf))
}

// rotate flushes and switches to a new segment; returns its generation.
// Caller must prevent concurrent append (it holds the table lock).
func (p *persist) rotate() (uint64, error) {
	if err := p.flushAll(); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.flushing {
		p.cond.Wait()
	}
	ng := p.gen + 1
	f, size, err := openSegmentForAppend(p.base, ng, 0)
	if err != nil {
		return 0, err
	}
	old := p.f
	p.f, p.gen, p.size = f, ng, size
	old.Close()
	return ng, nil
}

func (p *persist) removeBefore(gen uint64) {
	gens, _ := listSegments(p.base)
	for _, g := range gens {
		if g < gen {
			_ = os.Remove(segPath(p.base, g))
		}
	}
}

func (p *persist) close() error {
	close(p.stop)
	<-p.done
	err := p.flushAll()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f != nil {
		if cerr := p.f.Close(); err == nil {
			err = cerr
		}
		p.f = nil
	}
	return err
}

// abandon closes the file without flushing buffered frames (crash simulation).
func (p *persist) abandon() {
	close(p.stop)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f != nil {
		p.f.Close()
		p.f = nil
	}
}
