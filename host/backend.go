package host

import (
	"errors"
	"time"
)

// The host talks to storage through Backend, a thin text-oriented mirror of
// engine.Store. Values travel as canonical Box-grammar text; the adapter in
// host/engineadapter converts to/from box.Value. This keeps the host buildable
// and testable independently of the engine package.

// Sentinel errors a Backend should return (wrapped is fine).
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
)

// SetOpts mirrors engine.SetOpts.
type SetOpts struct {
	TTLSeconds        int  `json:"ttl_seconds,omitempty"`
	Sliding           bool `json:"sliding,omitempty"`
	CheckpointSeconds int  `json:"checkpoint_seconds,omitempty"`
}

// Event mirrors engine.Event (Value is Box text).
type Event struct {
	Path  string    `json:"path"`
	Op    string    `json:"op"`
	Value string    `json:"value,omitempty"`
	At    time.Time `json:"at"`
}

// Backend is one database (a directory of .red-xai* files).
type Backend interface {
	Get(path string) (string, error)
	Set(path, value string, opts SetOpts) error
	Delete(path string) error
	Query(q string) ([]string, error)
	Subscribe(path string) (<-chan Event, func())
	Stats() map[string]float64
	Close() error
}

// Opener opens (creating if necessary) a database in dir. follower=true means
// the node is currently a replication follower: a Backend that implements
// ReplBackend must start in follower mode (no local expiry decisions).
type Opener func(dir string, follower bool) (Backend, error)

// ReplBackend is implemented by engines that provide their own replication
// stream (the real RedX engine). The host then ships the engine's commit
// entries (sets with absolute expiry, leader-generated "expire" deletes) instead
// of API-level operations, and followers apply them with ApplyReplicated.
type ReplBackend interface {
	Backend
	// SetCommitHook registers fn, called synchronously (and in per-file commit
	// order) for every local commit, including expiry deletes. fn must not block
	// or call back into the backend.
	SetCommitHook(fn func(payload []byte))
	// ApplyReplicated applies one payload produced by a leader's commit hook.
	ApplyReplicated(payload []byte) error
	// SetFollower toggles follower mode; leaving it (promotion) re-arms expiry.
	SetFollower(on bool)
	// Checkpoint flushes every persistent file so a file copy is consistent.
	Checkpoint() error
}

// ErrNeedResync is returned by a ReplBackend when a replicated entry cannot be
// applied because of a gap or history change; the follower then falls back to a
// full snapshot.
var ErrNeedResync = errors.New("host: backend needs a full resync")
