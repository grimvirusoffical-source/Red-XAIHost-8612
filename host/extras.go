package host

import (
	"errors"
	"io"
	"time"
)

// Optional database capabilities beyond the plain Backend: search indexes
// (.red-xai-db-idx), file packs (.red-xai-pack), link resolution
// (.red-xai-db-link semantics) and live rules (.red-xai-db-pulse). A Backend
// opts in by also implementing Extras (the real-engine adapter does; the
// in-memory stub does not, and the API answers 501 for it).

// Errors specific to the extras.
var (
	ErrUnsupported = errors.New("this backend does not support that feature")
	ErrTooLarge    = errors.New("too large")
	ErrCorrupt     = errors.New("stored data failed its integrity check")
)

// IndexInfo describes one declared index.
type IndexInfo struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Box     string   `json:"box,omitempty"`
	Kind    string   `json:"kind"`
	Fields  []string `json:"fields"`
	Fold    bool     `json:"fold,omitempty"`
	Records int      `json:"records"`
	Terms   int      `json:"terms"`
}

// IndexSetInfo is one .red-xai-db-idx file.
type IndexSetInfo struct {
	Set     string      `json:"set"`
	Indexes []IndexInfo `json:"indexes"`
	Source  string      `json:"source,omitempty"`
	// Loaded says how each index was restored when the set was opened (loaded/verified/rebuilt ...).
	Loaded []string `json:"loaded,omitempty"`
}

// IndexQuery is one lookup. Mode is exact | prefix | range | text; "fuzzy" is accepted
// by the HTTP layer as an alias of text with op=or (any word matches; no typo tolerance).
type IndexQuery struct {
	Mode   string   `json:"mode"`
	Field  string   `json:"field"`
	Value  string   `json:"value,omitempty"`   // exact / prefix
	VType  string   `json:"vtype,omitempty"`   // exact: auto | string | number | bool
	Lo     *float64 `json:"lo,omitempty"`      // range
	Hi     *float64 `json:"hi,omitempty"`      // range
	LoExcl bool     `json:"lo_excl,omitempty"` // range
	HiExcl bool     `json:"hi_excl,omitempty"` // range
	Terms  []string `json:"terms,omitempty"`   // text
	Op     string   `json:"op,omitempty"`      // text: and | or
	Limit  int      `json:"limit,omitempty"`   // 0 = default (100)
	Record bool     `json:"record,omitempty"`  // include each record as Box text
}

// IndexHit is one matching record. Path is "file:Box[ID=n]".
type IndexHit struct {
	Path   string `json:"path"`
	Field  string `json:"field"`
	Value  string `json:"value"`
	Record string `json:"record,omitempty"`
}

// IndexDiff is the result of comparing an index with a fresh scan.
type IndexDiff struct {
	Missing int  `json:"missing"`
	Extra   int  `json:"extra"`
	Changed int  `json:"changed"`
	Stale   bool `json:"stale"`
}

// PackFile describes a pack entry.
type PackFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	ChunkSize int    `json:"chunk_size,omitempty"`
	// Encrypted: the pack is under an Encrypt rule of the database's .red-xai-sec, so Size and
	// SHA256 describe the stored ciphertext, downloads are decrypted on the fly and are not seekable.
	Encrypted bool `json:"encrypted,omitempty"`
}

// PackGCResult is what a GC removed.
type PackGCResult struct {
	ChunksRemoved    int `json:"chunks_removed"`
	ManifestsRemoved int `json:"manifests_removed"`
}

// PackFsckResult is an integrity report.
type PackFsckResult struct {
	OK              bool     `json:"ok"`
	Folders         int      `json:"folders"`
	Files           int      `json:"files"`
	Chunks          int      `json:"chunks"`
	Problems        []string `json:"problems"`
	OrphanChunks    int      `json:"orphan_chunks"`
	OrphanManifests int      `json:"orphan_manifests"`
}

// Resolved is a read with embedded links followed.
type Resolved struct {
	Value    string   `json:"value"`
	Dangling []string `json:"dangling,omitempty"`
	Cycles   []string `json:"cycles,omitempty"`
}

// PulseState is the rule set of a database.
type PulseState struct {
	Source string       `json:"source"`
	Rules  []string     `json:"rules"`
	Timers []PulseTimer `json:"timers"`
	Active bool         `json:"active"` // false on followers: rules run only on the leader
}

// PulseTimer is a pending timer.
type PulseTimer struct {
	Rule string    `json:"rule"`
	Name string    `json:"name"`
	Path string    `json:"path"`
	At   time.Time `json:"fires_at"`
}

// PulseEvent is one event fired by a rule, a timer or (optionally) a storage change.
type PulseEvent struct {
	Seq   uint64    `json:"seq"`
	Time  time.Time `json:"time"`
	Kind  string    `json:"kind"`
	Topic string    `json:"topic"`
	Rule  string    `json:"rule,omitempty"`
	Path  string    `json:"path,omitempty"`
	Data  string    `json:"data,omitempty"`
	Error string    `json:"error,omitempty"`
	Timer string    `json:"timer,omitempty"`
}

// Extras is implemented by backends with indexes, packs, links and rules.
// allow callbacks implement the caller's key path-prefix ACL: they take a record
// path ("file:Box[ID=n]") and report whether the key may read it.
type Extras interface {
	Services

	IndexList() ([]IndexSetInfo, error)
	IndexDefine(set, decl string) error // validates, stores, (re)opens
	IndexDrop(set string) error
	IndexRebuild(set string) error
	IndexCheck(set string) (map[string]IndexDiff, error)
	IndexQuery(set, index string, q IndexQuery, allow func(path string) bool) ([]IndexHit, error)

	PackList() ([]string, error)
	PackPut(pack, path string, r io.Reader) (PackFile, error)
	// PackOpen streams file bytes [off, off+n) (n < 0 = to the end). Whole-file reads
	// verify every chunk and the whole-file hash; ranged reads verify each chunk they touch.
	PackOpen(pack, path string, off, n int64) (io.ReadCloser, PackFile, error)
	PackStat(pack, path string) (PackFile, error)
	PackLs(pack, folder string) ([]PackFile, error)
	PackDelete(pack, path string) error
	// PackGC deletes chunks and manifests nothing references (space left by deletes/replacements).
	PackGC(pack string) (PackGCResult, error)
	// PackFsck checks the pack (deep = read and hash every chunk).
	PackFsck(pack string, deep bool) (PackFsckResult, error)

	// Resolve reads path with embedded links followed up to depth (<=0: default).
	Resolve(path string, depth int, allow func(path string) bool) (Resolved, error)

	// PulseDefine replaces the whole rule set ("" removes it). On a follower the
	// definition is stored but not run until the node is promoted.
	PulseDefine(src string) error
	PulseGet() (PulseState, error)
	// PulseSubscribe delivers events whose record passes allow (events without a
	// record are delivered only if allow == nil). changes also bridges raw storage changes.
	PulseSubscribe(topic string, changes bool, allow func(path string) bool) (<-chan PulseEvent, func(), error)
}
