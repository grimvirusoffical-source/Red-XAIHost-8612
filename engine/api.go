// Package engine implements the RedX Phase 1 storage engines behind one
// Store interface: cold durable (.red-xai.db), volatile quick storage
// (.red-xai-db-qs) and hot persistent (.red-xai-db-qs-ls).
//
// A "Database" is a directory of .red-xai* files. Paths are routed by file:
//
//	accounts.red-xai.db:AccountVault[ID=1].Inventory[id=10]
//	HotWorldStore.GlobalKillCounter          (file omitted if the box name is unique)
package engine

import (
	"errors"
	"time"

	"redx/box"
)

// SetOpts are per-write options.
//
//	.red-xai.db        none allowed (TTL/checkpoint rejected)
//	.red-xai-db-qs     TTLSeconds required (or Config.DefaultTTLSeconds); Sliding optional
//	.red-xai-db-qs-ls  none (immediate journal flush), TTLSeconds[+Sliding], or
//	                   CheckpointSeconds=N (the `(N)*` marker: coalesced, flushed every N s)
type SetOpts struct {
	TTLSeconds        int
	Sliding           bool
	CheckpointSeconds int
}

// Event is delivered to subscribers. Op is "set", "delete" or "expire".
type Event struct {
	Path  string
	Op    string
	Value box.Value
	At    time.Time
}

// Store is the interface the host codes against.
type Store interface {
	// Get returns the value at path. An entry whose TTL has elapsed returns
	// (NELL, nil) and is purged (lazy expiry). A path that never existed
	// returns ErrNotFound.
	Get(path string) (box.Value, error)
	Set(path string, v box.Value, opts SetOpts) error
	Delete(path string) error
	// Query evaluates a path query; wildcards `*` (any key) and `[ID=*]`
	// (any id) are supported and the file prefix may be omitted to search
	// every file.
	Query(q string) ([]box.Value, error)
	// Subscribe delivers events for changes at, under, or above path. The
	// channel is buffered; slow consumers drop events (counted in Stats).
	Subscribe(path string) (<-chan Event, func())
	Stats() map[string]float64
	Close() error
}

// Config configures a Database.
type Config struct {
	// Durability for .red-xai.db and the journal of .red-xai-db-qs-ls:
	// "sync" (fsync every commit), "group" (default; group commit: one
	// fsync covers all concurrent commits) or "async" (acknowledge before
	// fsync; a background flush runs every AsyncFlushMS).
	Durability string
	// SweepMS is the expiry sweep period (default 100).
	SweepMS int
	// AsyncFlushMS is the background flush period for async durability
	// (default 10).
	AsyncFlushMS int
	// CheckpointBytes triggers an automatic checkpoint when a file's WAL
	// exceeds this size (default 32 MiB; <0 disables).
	CheckpointBytes int64
	// DefaultTTLSeconds is applied to .red-xai-db-qs writes that carry no
	// TTL (0 = such writes are rejected with ErrTTLRequired).
	DefaultTTLSeconds int
	// Follower starts the database as a replication follower: no local
	// expiry, client writes rejected; only ApplyEntry mutates.
	Follower bool
	// RepairWAL makes recovery drop everything from a damaged WAL record
	// on even when valid records follow it (mid-log corruption). Default:
	// Open fails with ErrCorrupt so acknowledged commits are never lost
	// silently. The dropped bytes are kept in a .torn-* file.
	RepairWAL bool
	// Hook receives commits for replication (stub in Phase 1).
	Hook ClusterHook
	// Clock overrides time.Now (tests).
	Clock func() time.Time
	// Logf receives engine log lines (recovery, synthetic deletes at debug
	// volume are not logged individually). Default: discard.
	Logf func(format string, args ...interface{})
}

// Errors.
var (
	ErrNotFound     = box.ErrNotFound
	ErrClosed       = errors.New("engine: closed")
	ErrTTLRequired  = errors.New("engine: .red-xai-db-qs entries must carry a TTL (SetOpts.TTLSeconds)")
	ErrBadOpts      = errors.New("engine: invalid SetOpts for this file type")
	ErrBadPath      = errors.New("engine: invalid path")
	ErrNoFile       = errors.New("engine: path must name a file, e.g. file.red-xai.db:Box[ID=1].Key")
	ErrAmbiguous    = errors.New("engine: box name exists in several files; prefix the path with the file name")
	ErrConflict     = errors.New("engine: transaction conflict")
	ErrFollower     = errors.New("engine: database is a read-only replication follower")
	ErrCorrupt      = errors.New("engine: data file corrupt")
	ErrCrossFile    = errors.New("engine: a transaction may only touch one file")
	ErrTxnUnsupport = errors.New("engine: transactions are only supported on .red-xai.db files")
	ErrNotBox       = errors.New("engine: cannot descend into a non-box value")
	ErrInvalidValue = errors.New("engine: invalid value")
	ErrFailed       = errors.New("engine: file is in a failed state after an I/O error; reopen the database")
	ErrTxnDone      = errors.New("engine: transaction already finished")
	ErrNeedResync   = errors.New("engine: replication gap or history change; install a snapshot (InstallSnapshot) and resume")
)

// Op is one logged mutation. Kind is "set", "delete" or "expire" (a
// synthetic delete produced by TTL expiry; followers apply it as a plain
// delete without consulting their own clock). Set ops carry the absolute
// expiry (unix ms) stamped by the leader, never a relative TTL.
type Op struct {
	// Kind is "set", "delete", "expire" (synthetic delete) or "touch": a
	// sliding-TTL deadline move (Path + ExpiresAtMs only), emitted when a
	// read pushes the deadline of a (~N) entry out by at least 10% of its TTL.
	Kind        string
	Path        string // canonical, relative to the file: Box[ID=1].Key
	Value       box.Value
	TTLKind     uint8 // box.TTLNone/Fixed/Sliding/Checkpoint
	TTLSeconds  int
	ExpiresAtMs int64

	segs []box.Seg
}

// LogEntry is one atomic commit (all ops apply or none).
type LogEntry struct {
	// History identifies the replication history (a new one is minted when
	// a follower is promoted). Followers refuse entries of a different
	// history with ErrNeedResync.
	History string
	LSN     uint64
	File    string
	At      time.Time
	Ops     []Op
}

// ClusterHook is the replication extension point (Phase 5; stub today).
//
// OnCommit is invoked, in LSN order per file and under the file lock, after
// a commit (including synthetic expiry deletes) has been applied and handed
// to the journal; it must not block or call back into the Store.
//
// Apply is invoked on the committing goroutine after local durability and
// before the client is acknowledged, so a synchronous replicator can ship
// the entry and veto the acknowledgement by returning an error (the local
// commit is not rolled back). Order across goroutines is not guaranteed.
type ClusterHook interface {
	Apply(entry LogEntry) error
	OnCommit(entry LogEntry)
}

// NopHook is the default hook.
type NopHook struct{}

func (NopHook) Apply(LogEntry) error { return nil }
func (NopHook) OnCommit(LogEntry)    {}

// Replicable is implemented by *Database for cluster layers: a follower
// applies a leader's logged entries (including "expire" deletes) without
// any local expiry decisions.
type Replicable interface {
	Store
	// ApplyEntry applies a leader's entry to the named file atomically,
	// journaling it locally under the leader's LSN. Expire ops are applied
	// as plain deletes. An entry whose LSN was already applied is a no-op;
	// an LSN beyond last+1 (or any first entry other than LSN 1 on a
	// fresh file) returns ErrNeedResync and changes nothing. Entries with
	// LSN 0 are applied unsequenced.
	ApplyEntry(e LogEntry) error
	// ExportSnapshot returns the file's full contents as Box text with
	// absolute expiries and the LSN it corresponds to (leader side of a
	// resync).
	ExportSnapshot(file string) (doc string, lsn uint64, err error)
	// InstallSnapshot replaces the file's contents (follower side of a
	// resync): entries not in doc disappear. The follower's LSN becomes lsn.
	InstallSnapshot(file, doc string, lsn uint64, history string) error
	// HistoryID is this node's current replication history.
	HistoryID() string
	// SetFollower toggles follower mode. Leaving follower mode (promotion)
	// re-arms expiry for every entry that carries a deadline.
	SetFollower(on bool)
	SetClusterHook(h ClusterHook)
	// Checkpoint forces a checkpoint of every persistent file.
	Checkpoint() error
}
