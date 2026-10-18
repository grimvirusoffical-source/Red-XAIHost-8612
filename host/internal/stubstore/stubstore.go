// Package stubstore is an in-memory host.Backend used for tests and as a
// fallback when the real engine is not linked in (build tag redx_stub).
// It keeps data in RAM and rewrites a JSON file in dir on every mutation (O(n),
// test-grade only) so backup, snapshot and restart flows can be exercised.
package stubstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"redx/host"
)

type entry struct {
	Value   string    `json:"v"`
	Expires time.Time `json:"e,omitempty"`
	Sliding int       `json:"s,omitempty"`
}

// Store implements host.Backend.
type Store struct {
	dir  string
	mu   sync.Mutex
	data map[string]*entry
	subs map[int]*sub
	next int
	ops  float64
}

type sub struct {
	prefix string
	ch     chan host.Event
}

// Open loads a previous snapshot from dir if present.
func Open(dir string, follower bool) (host.Backend, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, data: map[string]*entry{}, subs: map[int]*sub{}}
	if b, err := os.ReadFile(filepath.Join(dir, "stub.red-xai-stub.json")); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	return s, nil
}

func (s *Store) live(p string) (*entry, bool) {
	e, ok := s.data[p]
	if !ok {
		return nil, false
	}
	if !e.Expires.IsZero() && time.Now().After(e.Expires) {
		delete(s.data, p)
		return nil, false
	}
	return e, true
}

func (s *Store) Get(path string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	e, ok := s.live(path)
	if !ok {
		return "", host.ErrNotFound
	}
	if e.Sliding > 0 {
		e.Expires = time.Now().Add(time.Duration(e.Sliding) * time.Second)
	}
	return e.Value, nil
}

func (s *Store) Set(path, value string, o host.SetOpts) error {
	if path == "" {
		return fmt.Errorf("%w: empty path", host.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	e := &entry{Value: value}
	if o.TTLSeconds > 0 {
		e.Expires = time.Now().Add(time.Duration(o.TTLSeconds) * time.Second)
		if o.Sliding {
			e.Sliding = o.TTLSeconds
		}
	}
	s.data[path] = e
	s.publish(host.Event{Path: path, Op: "set", Value: value, At: time.Now()})
	return s.flush()
}

func (s *Store) Delete(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	if _, ok := s.live(path); !ok {
		return host.ErrNotFound
	}
	delete(s.data, path)
	s.publish(host.Event{Path: path, Op: "delete", At: time.Now()})
	return s.flush()
}

// Query: stub semantics are "prefix:<p>" (list values whose path starts with p)
// or "*" (list everything). The real engine implements the RXQL/path grammar.
func (s *Store) Query(q string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops++
	q = strings.TrimSpace(q)
	var keys []string
	for k := range s.data {
		if _, ok := s.live(k); !ok {
			continue
		}
		switch {
		case q == "*":
			keys = append(keys, k)
		case strings.HasPrefix(q, "prefix:"):
			if strings.HasPrefix(k, strings.TrimPrefix(q, "prefix:")) {
				keys = append(keys, k)
			}
		default:
			return nil, fmt.Errorf("%w: stub store supports only '*' and 'prefix:<p>'", host.ErrInvalid)
		}
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.data[k].Value)
	}
	return out, nil
}

func (s *Store) publish(ev host.Event) {
	for _, sb := range s.subs {
		if strings.HasPrefix(ev.Path, sb.prefix) {
			select {
			case sb.ch <- ev:
			default: // slow consumer: drop
			}
		}
	}
}

func (s *Store) Subscribe(path string) (<-chan host.Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	sb := &sub{prefix: path, ch: make(chan host.Event, 64)}
	s.subs[id] = sb
	return sb.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(sb.ch)
		}
	}
}

func (s *Store) Stats() map[string]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]float64{"keys": float64(len(s.data)), "ops": s.ops, "stub": 1}
}

// flush rewrites the whole snapshot file (O(n); fine for a stub). Caller holds s.mu.
func (s *Store) flush() error {
	b, _ := json.Marshal(s.data)
	tmp := filepath.Join(s.dir, "stub.red-xai-stub.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "stub.red-xai-stub.json"))
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}
