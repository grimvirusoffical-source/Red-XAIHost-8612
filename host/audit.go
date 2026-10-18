package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEvent is one line of the JSON-lines audit log. It never contains key
// material; KeyID is the public 8-char lookup id.
type AuditEvent struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	KeyID  string    `json:"key_id,omitempty"`
	DB     string    `json:"db,omitempty"`
	Remote string    `json:"remote,omitempty"`
	Method string    `json:"method,omitempty"`
	Route  string    `json:"route,omitempty"`
	Reason string    `json:"reason,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Auditor appends events to a file (size-rotated).
type Auditor struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	maxSize int64
}

// NewAuditor opens (appending) the log. path=="" discards events.
func NewAuditor(path string) (*Auditor, error) {
	a := &Auditor{path: path, maxSize: 50 << 20}
	if path == "" {
		return a, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := a.open(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Auditor) open() error {
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	if st != nil {
		a.size = st.Size()
	}
	a.f = f
	return nil
}

// Log records an event.
func (a *Auditor) Log(ev AuditEvent) {
	if a == nil || a.f == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	b, _ := json.Marshal(ev)
	b = append(b, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.size+int64(len(b)) > a.maxSize {
		a.f.Close()
		_ = os.Rename(a.path, a.path+".1")
		if err := a.open(); err != nil {
			return
		}
	}
	n, _ := a.f.Write(b)
	a.size += int64(n)
}

// Close closes the file.
func (a *Auditor) Close() error {
	if a == nil || a.f == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}
