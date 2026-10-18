package host

import (
	"container/heap"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// TTLTracker records the deadline of every key written with a TTL through the
// host, so that ONLY THE LEADER decides expiry: it deletes the key from its own
// store and replicates an explicit "del" (reason=expire). Followers store
// replicated keys without a local TTL and never expire anything on their own.
// The tracker is kept on followers too (passively) so a promoted follower can
// take over expiry.
type TTLTracker struct {
	mu   sync.Mutex
	m    map[string]*ttlEnt
	h    ttlHeap
	ver  uint64
	path string
}

type ttlEnt struct {
	DB      string    `json:"db"`
	Path    string    `json:"path"`
	Expires time.Time `json:"exp"`
	Sliding int       `json:"sliding,omitempty"`
	ver     uint64
}

type ttlItem struct {
	exp time.Time
	key string
	ver uint64
}
type ttlHeap []ttlItem

func (h ttlHeap) Len() int            { return len(h) }
func (h ttlHeap) Less(i, j int) bool  { return h[i].exp.Before(h[j].exp) }
func (h ttlHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *ttlHeap) Push(x interface{}) { *h = append(*h, x.(ttlItem)) }
func (h *ttlHeap) Pop() interface{} {
	o := *h
	n := len(o)
	x := o[n-1]
	*h = o[:n-1]
	return x
}

// NewTTLTracker creates a tracker persisted at path ("" = memory only).
func NewTTLTracker(path string) *TTLTracker {
	t := &TTLTracker{m: map[string]*ttlEnt{}, path: path}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var list []*ttlEnt
			if json.Unmarshal(b, &list) == nil {
				for _, e := range list {
					t.put(e)
				}
			}
		}
	}
	return t
}

func tkey(db, path string) string { return db + "\x00" + path }

func (t *TTLTracker) put(e *ttlEnt) {
	t.ver++
	e.ver = t.ver
	k := tkey(e.DB, e.Path)
	t.m[k] = e
	heap.Push(&t.h, ttlItem{exp: e.Expires, key: k, ver: e.ver})
}

// Apply updates the tracker for a set (opts.TTLSeconds>0 adds a deadline, otherwise clears it).
func (t *TTLTracker) Apply(db, path string, o SetOpts, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if o.TTLSeconds <= 0 {
		delete(t.m, tkey(db, path))
		return
	}
	e := &ttlEnt{DB: db, Path: path, Expires: at.Add(time.Duration(o.TTLSeconds) * time.Second)}
	if o.Sliding {
		e.Sliding = o.TTLSeconds
	}
	t.put(e)
}

// Remove forgets a key.
func (t *TTLTracker) Remove(db, path string) {
	t.mu.Lock()
	delete(t.m, tkey(db, path))
	t.mu.Unlock()
}

// Touch extends a sliding deadline (leader read path).
func (t *TTLTracker) Touch(db, path string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.m[tkey(db, path)]; e != nil && e.Sliding > 0 {
		ne := *e
		ne.Expires = now.Add(time.Duration(e.Sliding) * time.Second)
		t.put(&ne)
	}
}

// ResetSliding gives every sliding key a fresh deadline (used on promotion).
func (t *TTLTracker) ResetSliding(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.m {
		if e.Sliding > 0 {
			ne := *e
			ne.Expires = now.Add(time.Duration(e.Sliding) * time.Second)
			t.put(&ne)
		}
	}
}

// Due pops keys whose deadline is <= now.
func (t *TTLTracker) Due(now time.Time) []ttlEnt {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []ttlEnt
	for t.h.Len() > 0 && !t.h[0].exp.After(now) {
		it := heap.Pop(&t.h).(ttlItem)
		if e := t.m[it.key]; e != nil && e.ver == it.ver {
			out = append(out, *e)
			delete(t.m, it.key)
		}
	}
	return out
}

// Len returns the number of tracked keys.
func (t *TTLTracker) Len() int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.m) }

// Save persists the tracker.
func (t *TTLTracker) Save() error {
	if t.path == "" {
		return nil
	}
	t.mu.Lock()
	list := make([]*ttlEnt, 0, len(t.m))
	for _, e := range t.m {
		list = append(list, e)
	}
	t.mu.Unlock()
	b, _ := json.Marshal(list)
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

// Reload replaces contents from disk (after a snapshot install).
func (t *TTLTracker) Reload() {
	t.mu.Lock()
	t.m = map[string]*ttlEnt{}
	t.h = nil
	t.mu.Unlock()
	if t.path == "" {
		return
	}
	if b, err := os.ReadFile(t.path); err == nil {
		var list []*ttlEnt
		if json.Unmarshal(b, &list) == nil {
			t.mu.Lock()
			for _, e := range list {
				t.put(e)
			}
			t.mu.Unlock()
		}
	}
}
