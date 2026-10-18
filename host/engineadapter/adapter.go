// Package engineadapter connects the host to the real RedX storage engine
// (package redx/engine). It converts between Box-grammar text (what the host
// speaks) and box.Value, maps engine errors onto host errors, and exposes the
// engine's replication entry points (commit hook, ApplyEntry, follower mode,
// checkpoint) through host.ReplBackend.
package engineadapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"redx/box"
	"redx/engine"
	"redx/engine/sec"
	"redx/host"
)

// Config are the engine settings the host exposes.
type Config struct {
	Durability        string // "sync" | "group" (default) | "async"
	DefaultTTLSeconds int    // applied to .red-xai-db-qs writes without a TTL (0 = reject)
	SweepMS           int
	Logf              func(format string, args ...interface{})

	// Keyring holds the .red-xai-sec master keys (nil = none loaded: packs under an Encrypt
	// rule then refuse reads and writes).
	Keyring *sec.Keyring
	// CodeAllowedBoxes: "file:Box" globs .red-xai-code scripts may touch (empty = none).
	CodeAllowedBoxes []string
	// MaxMediaBytes bounds one media item (0 = the media engine's default).
	MaxMediaBytes int64
}

// Opener returns a host.Opener that opens each database directory with the engine.
func Opener(c Config) host.Opener {
	return func(dir string, follower bool) (host.Backend, error) {
		db, err := engine.OpenDatabase(dir, engine.Config{
			Durability: c.Durability, DefaultTTLSeconds: c.DefaultTTLSeconds, SweepMS: c.SweepMS,
			Follower: follower, Logf: c.Logf,
		})
		if err != nil {
			return nil, err
		}
		st := &store{db: db, logf: c.Logf}
		st.cfg = c
		if err := st.initExtras(dir, follower); err != nil {
			st.closeExtras()
			_ = db.Close()
			return nil, err
		}
		return st, nil
	}
}

type store struct {
	db   *engine.Database
	logf func(format string, args ...interface{})
	extras
}

var _ host.ReplBackend = (*store)(nil)

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, engine.ErrNotFound):
		return host.ErrNotFound
	case errors.Is(err, engine.ErrFollower):
		return host.ErrReadOnly
	case errors.Is(err, engine.ErrTTLRequired), errors.Is(err, engine.ErrBadOpts), errors.Is(err, engine.ErrBadPath),
		errors.Is(err, engine.ErrNoFile), errors.Is(err, engine.ErrAmbiguous), errors.Is(err, engine.ErrInvalidValue),
		errors.Is(err, engine.ErrNotBox), errors.Is(err, engine.ErrCrossFile):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	var be *box.Error
	if errors.As(err, &be) {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	return err
}

func (s *store) Get(path string) (string, error) {
	v, err := s.db.Get(path)
	if err != nil {
		return "", mapErr(err)
	}
	if v.IsNell() {
		// The engine reads an absent or expired path in .red-xai-db-qs / -qs-ls files as NELL. The
		// host contract is: absent = 404, and 200 NELL only for a NELL that is really stored.
		if vs, qerr := s.db.Query(path); qerr == nil && len(vs) == 0 {
			return "", host.ErrNotFound
		}
	}
	return v.String(), nil
}

func (s *store) Set(path, value string, o host.SetOpts) error {
	v, err := box.ParseValue(value)
	if err != nil {
		return fmt.Errorf("%w: value is not valid Box grammar: %v", host.ErrInvalid, err)
	}
	if handled, err := s.setViaPulse(path, v, o); handled {
		return mapErr(err)
	}
	if err := s.db.Set(path, v, engine.SetOpts{TTLSeconds: o.TTLSeconds, Sliding: o.Sliding, CheckpointSeconds: o.CheckpointSeconds}); err != nil {
		return mapErr(err)
	}
	s.observePath(path)
	return nil
}

func (s *store) Delete(path string) error {
	if err := s.db.Delete(path); err != nil {
		return mapErr(err)
	}
	s.observePath(path)
	return nil
}

func (s *store) Query(q string) ([]string, error) {
	vs, err := s.db.Query(q)
	if err != nil {
		e := mapErr(err)
		if e == err && !errors.Is(err, engine.ErrClosed) && !errors.Is(err, engine.ErrFailed) && !errors.Is(err, engine.ErrCorrupt) {
			e = fmt.Errorf("%w: %v", host.ErrInvalid, err) // treat query errors as bad requests
		}
		return nil, e
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}
	return out, nil
}

func (s *store) Subscribe(path string) (<-chan host.Event, func()) {
	if strings.HasSuffix(path, ":") {
		path += "*" // "file:" means every box of the file (the engine would end the stream at once)
	}
	ch, cancel := s.db.Subscribe(path)
	out := make(chan host.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- host.Event{Path: ev.Path, Op: ev.Op, Value: ev.Value.String(), At: ev.At}:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			close(done)
			cancel()
		})
	}
}

func (s *store) Stats() map[string]float64 { return s.db.Stats() }
func (s *store) Close() error {
	s.closeExtras()
	return s.db.Close()
}

// ---- replication (host.ReplBackend) ----

type wireOp struct {
	Kind        string `json:"k"`
	Path        string `json:"p"`
	Value       string `json:"v,omitempty"` // Box grammar
	TTLKind     uint8  `json:"tk,omitempty"`
	TTLSeconds  int    `json:"ts,omitempty"`
	ExpiresAtMs int64  `json:"x,omitempty"`
}

type wireEntry struct {
	LSN  uint64   `json:"lsn"`
	File string   `json:"file"`
	At   int64    `json:"at"`
	Ops  []wireOp `json:"ops"`
}

type hook struct{ fn func([]byte) }

func (hook) Apply(engine.LogEntry) error { return nil }
func (h hook) OnCommit(e engine.LogEntry) {
	w := wireEntry{LSN: e.LSN, File: e.File, At: e.At.UnixNano(), Ops: make([]wireOp, len(e.Ops))}
	for i, op := range e.Ops {
		w.Ops[i] = wireOp{Kind: op.Kind, Path: op.Path, TTLKind: op.TTLKind, TTLSeconds: op.TTLSeconds, ExpiresAtMs: op.ExpiresAtMs}
		if op.Kind == "set" {
			w.Ops[i].Value = op.Value.String()
		}
	}
	b, err := json.Marshal(w)
	if err == nil {
		h.fn(b)
	}
}

func (s *store) SetCommitHook(fn func([]byte)) { s.db.SetClusterHook(hook{fn}) }

func (s *store) ApplyReplicated(payload []byte) error {
	var w wireEntry
	if err := json.Unmarshal(payload, &w); err != nil {
		return err
	}
	e := engine.LogEntry{LSN: w.LSN, File: w.File, At: time.Unix(0, w.At), Ops: make([]engine.Op, len(w.Ops))}
	for i, op := range w.Ops {
		v := box.Value{}
		if op.Kind == "set" {
			var err error
			if v, err = box.ParseValue(op.Value); err != nil {
				return fmt.Errorf("replicated value: %w", err)
			}
		}
		e.Ops[i] = engine.Op{Kind: op.Kind, Path: op.Path, Value: v, TTLKind: op.TTLKind, TTLSeconds: op.TTLSeconds, ExpiresAtMs: op.ExpiresAtMs}
	}
	if err := s.db.ApplyEntry(e); err != nil {
		if errors.Is(err, engine.ErrNeedResync) {
			return fmt.Errorf("%w: %v", host.ErrNeedResync, err)
		}
		return err
	}
	return nil
}

func (s *store) SetFollower(on bool) {
	s.db.SetFollower(on)
	s.follower.Store(on)
	if on { // demoted: indexes may be stale and rules must not run here
		s.mu.Lock()
		s.stopPulseLocked()
		for _, set := range s.sets {
			_ = set.Close()
		}
		s.sets = nil
		s.nSets.Store(0)
		s.mu.Unlock()
		s.invalidateSQL()
		return
	}
	s.pulseLoadFile() // promoted: start the stored rules (timers are not carried over)
	s.openAllSets()
}
func (s *store) Checkpoint() error { return s.db.Checkpoint() }
