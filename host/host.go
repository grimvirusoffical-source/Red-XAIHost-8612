package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
)

// Host is the redxd core: databases, keys, auth, audit, cluster.
type Host struct {
	Cfg     Config
	Reg     *Registry
	KS      *Keystore
	Auth    *Authenticator
	Audit   *Auditor
	Cluster *Cluster
	Tracker *TTLTracker
	Logf    func(format string, args ...interface{})
	NodeID  string

	// BootstrapKey is set only when this start created the first admin key.
	BootstrapKey string

	started time.Time
	lock    *flock.Flock
	pause   sync.RWMutex // writes hold RLock; backup/snapshot hold Lock
	opMu    sync.Mutex   // serialises (apply, log-append) on the leader => log order == apply order
	reqs    atomic.Uint64
	authBad atomic.Uint64
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mux     http.Handler
}

// New opens everything under cfg.DataDir.
func New(cfg Config, opener Opener, logf func(string, ...interface{})) (*Host, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	h := &Host{Cfg: cfg, Logf: logf, started: time.Now()}
	h.lock = flock.New(filepath.Join(cfg.DataDir, "redxd.lock"))
	if ok, err := h.lock.TryLock(); err != nil || !ok {
		return nil, fmt.Errorf("data dir %s is in use by another redxd process", cfg.DataDir)
	}
	fail := func(err error) (*Host, error) { h.closeParts(); return nil, err }

	h.NodeID = cfg.NodeID
	if h.NodeID == "" {
		id, err := loadOrCreateNodeID(cfg.DataDir)
		if err != nil {
			return fail(err)
		}
		h.NodeID = id
	}
	var err error
	if h.Audit, err = NewAuditor(cfg.AuditLog); err != nil {
		return fail(err)
	}
	pass := os.Getenv("REDX_KEYSTORE_PASSPHRASE")
	if cfg.KeystoreEnc && pass == "" {
		return fail(errors.New("keystore_encrypt is on but REDX_KEYSTORE_PASSPHRASE is not set"))
	}
	if !cfg.KeystoreEnc {
		// An already-encrypted keystore is still opened if a passphrase is supplied (and stays encrypted).
		if !fileHasMagic(filepath.Join(cfg.DataDir, "keys.red-xai-keys")) {
			pass = ""
		}
	}
	if h.KS, err = OpenKeystore(filepath.Join(cfg.DataDir, "keys.red-xai-keys"),
		KeystoreOptions{Passphrase: pass, ArgonMem: cfg.ArgonMemKiB, ArgonTime: cfg.ArgonTime}); err != nil {
		return fail(err)
	}
	if h.Reg, err = NewRegistry(cfg.DataDir, opener,
		func() bool { return h.Cluster != nil && !h.Cluster.IsLeader() },
		h.onOpen); err != nil {
		return fail(err)
	}
	h.Auth = NewAuthenticator(h.KS, h.Audit, cfg.RateRPS, cfg.RateBurst, cfg.AuditOK)
	h.Tracker = NewTTLTracker(filepath.Join(cfg.DataDir, "cluster", "ttl.json"))
	if h.Cluster, err = newCluster(h); err != nil {
		return fail(err)
	}
	h.KS.SetOnChange(func(r KeyRecord) {
		if h.Cluster.IsLeader() {
			rc := r
			if _, err := h.Cluster.Replicate(LogEntry{Op: "key", Key: &rc}); err != nil {
				h.Logf("replicate key change: %v", err)
			}
		}
	})
	if h.Cluster.IsLeader() {
		// Keys created before the first start (e.g. offline bootstrap by an installer) are not
		// in the log yet; announce them so followers learn them.
		if h.Cluster.Offset() == 0 {
			for _, r := range h.KS.List() {
				rc := r
				if _, err := h.Cluster.Replicate(LogEntry{Op: "key", Key: &rc}); err != nil {
					return fail(err)
				}
			}
		}
		if !h.Reg.Exists(cfg.DefaultDB) {
			if _, err := h.WriteCreateDB(cfg.DefaultDB); err != nil {
				return fail(err)
			}
		}
		if h.KS.Count() == 0 && os.Getenv("REDX_NO_BOOTSTRAP") == "" {
			key, rec, err := h.KS.Create(CreateSpec{DB: AllDBs, Scope: ScopeAdmin, Label: "bootstrap-admin"})
			if err != nil {
				return fail(err)
			}
			h.BootstrapKey = key
			h.Audit.Log(AuditEvent{Event: "bootstrap", KeyID: rec.ID, Detail: "first-run admin key created"})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.mux = h.routes()
	h.Cluster.start(ctx)
	h.wg.Add(1)
	go h.maintenance(ctx)
	return h, nil
}

func fileHasMagic(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, len(encMagic))
	n, _ := f.Read(b)
	return string(b[:n]) == string(encMagic)
}

func loadOrCreateNodeID(dir string) (string, error) {
	p := filepath.Join(dir, "node.id")
	if b, err := os.ReadFile(p); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	id := "node-" + hex.EncodeToString(buf)
	return id, os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// onOpen wires engine-level replication for backends that support it.
func (h *Host) onOpen(name string, b Backend) {
	if rb, ok := b.(ReplBackend); ok {
		rb.SetCommitHook(func(payload []byte) {
			if h.Cluster != nil && h.Cluster.IsLeader() {
				if _, err := h.Cluster.Replicate(LogEntry{DB: name, Op: "engine", Value: string(payload)}); err != nil {
					h.Logf("replicate commit for %s: %v", name, err)
				}
			}
		})
	}
}

// setFollowerMode flips every open engine between leader and follower behaviour.
func (h *Host) setFollowerMode(on bool) {
	h.Reg.Each(func(_ string, b Backend) {
		if rb, ok := b.(ReplBackend); ok {
			rb.SetFollower(on)
		}
	})
}

// checkpointAll flushes engine files (used before file-level copies).
func (h *Host) checkpointAll() {
	h.Reg.Each(func(n string, b Backend) {
		if rb, ok := b.(ReplBackend); ok {
			if err := rb.Checkpoint(); err != nil {
				h.Logf("checkpoint %s: %v", n, err)
			}
		}
	})
}

// Handler returns the HTTP handler.
func (h *Host) Handler() http.Handler { return h.mux }

func (h *Host) maintenance(ctx context.Context) {
	defer h.wg.Done()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if h.Cluster.IsLeader() {
				h.expireDue(now)
			}
			h.KS.Poll()
			h.Cluster.log.MaybeCompact()
			if now.Sub(last) > 15*time.Second {
				last = now
				_ = h.Tracker.Save()
				_ = h.Cluster.log.Sync()
			}
		}
	}
}

// expireDue is the single place where TTL expiry is decided: the leader deletes
// and replicates an explicit del.
func (h *Host) expireDue(now time.Time) {
	for _, e := range h.Tracker.Due(now) {
		h.pause.RLock()
		h.opMu.Lock()
		if b, err := h.Reg.Get(e.DB); err == nil {
			if err := b.Delete(e.Path); err != nil && !errors.Is(err, ErrNotFound) {
				h.Logf("expire %s: %v", e.DB, err)
			}
			_, _ = h.Cluster.Replicate(LogEntry{DB: e.DB, Op: "del", Path: e.Path, Reason: "expire"})
		}
		h.opMu.Unlock()
		h.pause.RUnlock()
	}
}

// ErrReadOnly is returned by write helpers on followers.
var ErrReadOnly = errors.New("this node is a follower; writes go to the leader")

// WriteSet applies a set on the leader and appends it to the replication log.
func (h *Host) WriteSet(db, path, value string, o SetOpts) (uint64, error) {
	if !h.Cluster.IsLeader() {
		return 0, ErrReadOnly
	}
	if err := ValidPath(path); err != nil {
		return 0, err
	}
	b, err := h.Reg.Get(db)
	if err != nil {
		return 0, err
	}
	h.pause.RLock()
	defer h.pause.RUnlock()
	if _, ok := b.(ReplBackend); ok {
		// The engine's commit hook appends to the replication log (including TTL deadlines
		// and expiry deletes); no host-level serialisation or TTL tracking is needed.
		if err := b.Set(path, value, o); err != nil {
			return 0, err
		}
		return h.Cluster.Offset(), nil
	}
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if err := b.Set(path, value, o); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	h.Tracker.Apply(db, path, o, now)
	return h.Cluster.Replicate(LogEntry{DB: db, Op: "set", Path: path, Value: value, Opts: o, At: now})
}

// WriteDel deletes on the leader.
func (h *Host) WriteDel(db, path string) (uint64, error) {
	if !h.Cluster.IsLeader() {
		return 0, ErrReadOnly
	}
	if err := ValidPath(path); err != nil {
		return 0, err
	}
	b, err := h.Reg.Get(db)
	if err != nil {
		return 0, err
	}
	h.pause.RLock()
	defer h.pause.RUnlock()
	if _, ok := b.(ReplBackend); ok {
		if err := b.Delete(path); err != nil {
			return 0, err
		}
		return h.Cluster.Offset(), nil
	}
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if err := b.Delete(path); err != nil {
		return 0, err
	}
	h.Tracker.Remove(db, path)
	return h.Cluster.Replicate(LogEntry{DB: db, Op: "del", Path: path})
}

// WriteCreateDB creates a database on the leader and replicates it.
func (h *Host) WriteCreateDB(name string) (uint64, error) {
	if !h.Cluster.IsLeader() {
		return 0, ErrReadOnly
	}
	h.pause.RLock()
	defer h.pause.RUnlock()
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if err := h.Reg.Create(name); err != nil {
		return 0, err
	}
	return h.Cluster.Replicate(LogEntry{DB: name, Op: "createdb"})
}

// ReadGet reads a value; on the leader a sliding TTL is extended.
func (h *Host) ReadGet(db, path string) (string, error) {
	b, err := h.Reg.Get(db)
	if err != nil {
		return "", err
	}
	v, err := b.Get(path)
	if err == nil && h.Cluster.IsLeader() {
		h.Tracker.Touch(db, path, time.Now())
	}
	return v, err
}

func (h *Host) closeParts() {
	if h.Cluster != nil {
		h.Cluster.stop()
	}
	if h.Reg != nil {
		_ = h.Reg.CloseAll()
	}
	if h.Tracker != nil {
		_ = h.Tracker.Save()
	}
	if h.Audit != nil {
		_ = h.Audit.Close()
	}
	if h.lock != nil {
		_ = h.lock.Unlock()
	}
}

// Close stops background work and closes all resources.
func (h *Host) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	h.wg.Wait()
	h.closeParts()
	return nil
}
