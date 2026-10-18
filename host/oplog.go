package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogEntry is one replicated operation. Seq is the replication offset.
type LogEntry struct {
	Seq    uint64     `json:"seq"`
	DB     string     `json:"db,omitempty"`
	Op     string     `json:"op"` // set | del | createdb | key | engine | aux (index / pulse definitions)
	Path   string     `json:"path,omitempty"`
	Value  string     `json:"value,omitempty"`
	Opts   SetOpts    `json:"opts,omitempty"`
	At     time.Time  `json:"at"`
	Reason string     `json:"reason,omitempty"` // "expire" for leader-driven TTL deletes
	Key    *KeyRecord `json:"key,omitempty"`
}

// ErrLogGap means the requested offset is not available (truncated or ahead).
var ErrLogGap = errors.New("replication offset not available in log")

// OpLog is the append-only replication log: a JSON-lines file with a header
// {"base":N} (the offset preceding the first retained entry). Offsets are kept
// in memory for random access. Appends are buffered by the OS (async durability);
// Sync() is called periodically and on Close.
type OpLog struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	base    uint64
	offs    []int64 // file offset of each retained entry
	size    int64
	max     int // retain at most this many entries (compaction at 2x)
	waiters chan struct{}
}

// OpenOpLog opens or creates the log. max<=0 selects 1,000,000.
func OpenOpLog(path string, max int) (*OpLog, error) {
	if max <= 0 {
		max = 1_000_000
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &OpLog{path: path, max: max, waiters: make(chan struct{})}
	if err := l.load(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *OpLog) load() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	var pos int64
	l.offs = nil
	l.base = 0
	first := true
	good := int64(0)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if first {
				first = false
				var h struct {
					Base *uint64 `json:"base"`
				}
				if json.Unmarshal(line, &h) == nil && h.Base != nil {
					l.base = *h.Base
					pos += int64(len(line))
					good = pos
					continue
				}
			}
			var e LogEntry
			if json.Unmarshal(line, &e) != nil || e.Seq != l.base+uint64(len(l.offs))+1 {
				break // torn or out-of-order tail: truncate below
			}
			l.offs = append(l.offs, pos)
			pos += int64(len(line))
			good = pos
		}
		if err != nil {
			break
		}
	}
	if good == 0 && first {
		// new file: write header
		h := []byte(`{"base":0}` + "\n")
		if _, err := f.WriteAt(h, 0); err != nil {
			f.Close()
			return err
		}
		good = int64(len(h))
	}
	if err := f.Truncate(good); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, good
	return nil
}

// First returns the lowest available offset (base+1); Last the highest (== base when empty).
func (l *OpLog) First() uint64 { l.mu.Lock(); defer l.mu.Unlock(); return l.base + 1 }

// Last returns the highest offset in the log (or base when empty).
func (l *OpLog) Last() uint64 { l.mu.Lock(); defer l.mu.Unlock(); return l.base + uint64(len(l.offs)) }

func (l *OpLog) notifyLocked() {
	close(l.waiters)
	l.waiters = make(chan struct{})
}

// Append assigns the next offset to e and writes it (leader path).
func (l *OpLog) Append(e LogEntry) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq = l.base + uint64(len(l.offs)) + 1
	return l.appendLocked(e)
}

// AppendAt appends an entry received from the leader; Seq must be next.
func (l *OpLog) AppendAt(e LogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if want := l.base + uint64(len(l.offs)) + 1; e.Seq != want {
		return fmt.Errorf("%w: got seq %d want %d", ErrLogGap, e.Seq, want)
	}
	_, err := l.appendLocked(e)
	return err
}

func (l *OpLog) appendLocked(e LogEntry) (uint64, error) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		return 0, err
	}
	l.offs = append(l.offs, l.size)
	l.size += int64(len(b))
	l.notifyLocked()
	return e.Seq, nil
}

// MaybeCompact drops old entries once the log holds 2x its retention (called
// periodically by the daemon, never on the append path, so engine commit hooks
// are not delayed by a rewrite).
func (l *OpLog) MaybeCompact() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.offs) >= 2*l.max {
		_ = l.compactLocked()
	}
}

// compactLocked drops the oldest entries so only max remain.
func (l *OpLog) compactLocked() error {
	drop := len(l.offs) - l.max
	if drop <= 0 {
		return nil
	}
	startOff := l.offs[drop]
	rd := make([]byte, l.size-startOff)
	if _, err := l.f.ReadAt(rd, startOff); err != nil {
		return err
	}
	newBase := l.base + uint64(drop)
	hdr := []byte(fmt.Sprintf("{\"base\":%d}\n", newBase))
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, append(hdr, rd...), 0o600); err != nil {
		return err
	}
	l.f.Close()
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	return l.load()
}

// Read returns up to limit entries with Seq >= from. ErrLogGap if from is
// older than the retained range or beyond last+1.
func (l *OpLog) Read(from uint64, limit int) ([]LogEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	last := l.base + uint64(len(l.offs))
	if from < l.base+1 || from > last+1 {
		return nil, ErrLogGap
	}
	if from == last+1 {
		return nil, nil
	}
	i := int(from - l.base - 1)
	j := i + limit
	if limit <= 0 || j > len(l.offs) {
		j = len(l.offs)
	}
	endOff := l.size
	if j < len(l.offs) {
		endOff = l.offs[j]
	}
	buf := make([]byte, endOff-l.offs[i])
	if _, err := l.f.ReadAt(buf, l.offs[i]); err != nil {
		return nil, err
	}
	var out []LogEntry
	for _, ln := range bytes.Split(bytes.TrimRight(buf, "\n"), []byte("\n")) {
		var e LogEntry
		if err := json.Unmarshal(ln, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Wait returns a channel closed on the next append.
func (l *OpLog) Wait() <-chan struct{} { l.mu.Lock(); defer l.mu.Unlock(); return l.waiters }

// Reset discards everything and restarts the log after offset base
// (used after installing a full snapshot).
func (l *OpLog) Reset(base uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Close()
	if err := os.WriteFile(l.path, []byte(fmt.Sprintf("{\"base\":%d}\n", base)), 0o600); err != nil {
		return err
	}
	if err := l.load(); err != nil {
		return err
	}
	l.notifyLocked()
	return nil
}

// Sync flushes to disk.
func (l *OpLog) Sync() error { l.mu.Lock(); defer l.mu.Unlock(); return l.f.Sync() }

// Close syncs and closes.
func (l *OpLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.f.Sync()
	return l.f.Close()
}
