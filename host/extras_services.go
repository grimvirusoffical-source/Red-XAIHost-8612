package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Errors specific to the 0.2.0 services (mapped to HTTP 403, 409, 503 and 415).
var (
	ErrForbidden   = errors.New("forbidden")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
	ErrMediaType   = errors.New("unsupported media type")
)

// Services are the capabilities added in 0.2.0: RXQL (SQL-ish) queries, sealed media
// libraries (.red-xai-media), sandboxed scripts (.red-xai-code) and access policy
// (.red-xai-sec). They are part of Extras; only the real-engine adapter implements them.

// SecPrincipal is the caller as the policy engine sees it: derived from the API key.
type SecPrincipal struct {
	KeyID  string   // key id (never the key)
	Scope  string   // read | write | admin
	Prefix string   // the key's path prefix ("" = none)
	Roles  []string // roles carried by the key
}

// SQLResult is one RXQL result. Rows are aligned with Columns (NELL/null where a row has no such column).
type SQLResult struct {
	Kind     string              `json:"kind"`
	Columns  []string            `json:"columns,omitempty"`
	Rows     [][]json.RawMessage `json:"rows"`            // typed JSON values (NELL = null, boxes = objects)
	RowsBox  [][]string          `json:"rows_box"`        // the same cells as exact Box-grammar text
	Paths    []string            `json:"paths,omitempty"` // SELECT: the FROM record behind each row
	Affected int                 `json:"affected"`
	Cascaded []string            `json:"cascaded,omitempty"`
	Access   string              `json:"access,omitempty"` // scan | pk | index:<name> | union(...)
	Scanned  int                 `json:"scanned"`
	Plan     []string            `json:"plan,omitempty"`
}

// SecInfo describes the loaded access policy.
type SecInfo struct {
	Loaded       bool     `json:"loaded"`
	Files        []string `json:"files,omitempty"`
	Roles        []string `json:"roles,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	MasterKeyIDs []string `json:"master_key_ids,omitempty"`
	Source       string   `json:"source,omitempty"` // only for admins; the API-managed policy file
}

// CodeScript describes one compiled script.
type CodeScript struct {
	File   string   `json:"file"`
	Name   string   `json:"name"`
	Params []string `json:"params"`
	Uses   []string `json:"uses"`
}

// CodeCall is one host call a script attempted.
type CodeCall struct {
	Name    string `json:"name"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// CodeResult is the outcome of a script run.
type CodeResult struct {
	Value     interface{} `json:"value"`
	Steps     int64       `json:"steps"`
	HostCalls int         `json:"host_calls"`
	ElapsedMS float64     `json:"elapsed_ms"`
	Calls     []CodeCall  `json:"calls,omitempty"`
}

// MediaInfo is the manifest of one media item.
type MediaInfo struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	MIME              string `json:"mime"`
	Size              int64  `json:"size"`
	SHA256            string `json:"sha256"`
	Width             int    `json:"width,omitempty"`
	Height            int    `json:"height,omitempty"`
	DurationMS        int64  `json:"duration_ms,omitempty"`
	DurationEstimated bool   `json:"duration_estimated,omitempty"`
}

// MediaFile is an open media item: seekable (for HTTP Range) and closeable.
type MediaFile interface {
	io.ReadSeeker
	io.Closer
	Info() MediaInfo
}

// MediaFsckResult is a media library integrity report.
type MediaFsckResult struct {
	OK       bool     `json:"ok"`
	Items    int      `json:"items"`
	Problems []string `json:"problems"`
	Orphans  []string `json:"orphans"`
}

// Services is the 0.2.0 part of Extras.
type Services interface {
	// SQL runs one RXQL statement as p. Statements that write run on the leader only.
	SQL(ctx context.Context, p SecPrincipal, q string, args []interface{}, explain bool) (SQLResult, error)

	// SecAllow reports whether the database's policy lets p do action (read | write |
	// delete | exec) on file[:box[.field]]. It is true when the database has no
	// .red-xai-sec file. The key's own scope/db/prefix ceiling is checked by the caller.
	SecAllow(p SecPrincipal, action, file, box, field string) bool
	// SecRead applies the policy to a value read at path (Box text): ErrForbidden if the path is not
	// readable, and fields the caller may not read are removed from records. Unchanged without a policy.
	SecRead(p SecPrincipal, path, text string) (string, error)
	// SecWrite checks a write (value text) or delete at path; records are checked field by field.
	SecWrite(p SecPrincipal, path, text string, del bool) error
	// SecActive reports whether the database has a .red-xai-sec policy loaded (cheap).
	SecActive() bool
	SecInfo(includeSource bool) (SecInfo, error)
	SecDefine(src string) error // "" removes the API-managed policy file

	CodeList() ([]CodeScript, error)
	CodeDefine(file, src string) error // "" removes the file
	CodeRun(ctx context.Context, p SecPrincipal, script string, args []interface{}) (CodeResult, error)

	MediaLibs() ([]string, error)
	MediaList(lib string) ([]MediaInfo, error)
	MediaPut(lib, name, declared string, r io.Reader) (MediaInfo, error)
	MediaStat(lib, name string) (MediaInfo, error)
	MediaOpen(lib, name string) (MediaFile, error)
	MediaDelete(lib, name string) error
	MediaFsck(lib string, deep bool) (MediaFsckResult, error)
	MediaSweep(lib string) (int, error)
}
