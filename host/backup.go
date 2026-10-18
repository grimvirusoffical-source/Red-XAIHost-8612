package host

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// Snapshot layout (tar.gz): dbs/<name>/** (database files), keys.json (key
// records: hashes only), ttl.json (TTL deadlines). Used for `redx backup`,
// the online admin backup endpoint and follower full-resync.
//
// Consistency: the snapshot is taken with all host-level writes paused, so it
// is a write-quiescent file copy. It relies on the engine's own WAL recovery
// for any background checkpoint that happens concurrently (the engine
// interface has no explicit flush hook). For a guaranteed-clean copy use the
// offline backup with the daemon stopped.

func addFile(tw *tar.Writer, name string, path string, fi os.FileInfo) error {
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func addBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// WriteSnapshot writes the tar.gz to w. dbsRoot is <data>/dbs.
func WriteSnapshot(w io.Writer, dbsRoot string, keys []KeyRecord, ttlJSON []byte) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dbsRoot, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dbsRoot, p)
		if rel == "." {
			return nil
		}
		name := "dbs/" + filepath.ToSlash(rel)
		if fi.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o700, ModTime: fi.ModTime()})
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		return addFile(tw, name, p, fi)
	})
	if err != nil {
		return err
	}
	kb, _ := json.Marshal(keystoreFile{Version: 1, Keys: ptrs(keys)})
	if err := addBytes(tw, "keys.json", kb); err != nil {
		return err
	}
	if ttlJSON == nil {
		ttlJSON = []byte("[]")
	}
	if err := addBytes(tw, "ttl.json", ttlJSON); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func ptrs(k []KeyRecord) []*KeyRecord {
	out := make([]*KeyRecord, len(k))
	for i := range k {
		out[i] = &k[i]
	}
	return out
}

// SnapshotContents is what ExtractSnapshot found.
type SnapshotContents struct {
	Keys []KeyRecord
	TTL  []byte
}

// ExtractSnapshot unpacks a snapshot: db files go under destDBs; keys/ttl are returned.
// Rejects absolute paths, "..", links and anything outside the known layout.
func ExtractSnapshot(r io.Reader, destDBs string) (*SnapshotContents, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	sc := &SnapshotContents{}
	if err := os.MkdirAll(destDBs, 0o700); err != nil {
		return nil, err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		switch {
		case name == "keys.json":
			b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
			if err != nil {
				return nil, err
			}
			var f keystoreFile
			if err := json.Unmarshal(b, &f); err != nil {
				return nil, err
			}
			for _, k := range f.Keys {
				sc.Keys = append(sc.Keys, *k)
			}
		case name == "ttl.json":
			if sc.TTL, err = io.ReadAll(io.LimitReader(tr, 256<<20)); err != nil {
				return nil, err
			}
		case strings.HasPrefix(name, "dbs/"):
			rel := strings.TrimPrefix(name, "dbs/")
			if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") || strings.Contains(rel, `\`) || filepath.IsAbs(rel) {
				return nil, fmt.Errorf("snapshot: unsafe path %q", hdr.Name)
			}
			target := filepath.Join(destDBs, filepath.FromSlash(rel))
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(target, 0o700); err != nil {
					return nil, err
				}
			case tar.TypeReg:
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					return nil, err
				}
				f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
				if err != nil {
					return nil, err
				}
				if _, err := io.Copy(f, tr); err != nil {
					f.Close()
					return nil, err
				}
				if err := f.Close(); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("snapshot: unsupported entry type for %q", hdr.Name)
			}
		default:
			return nil, fmt.Errorf("snapshot: unexpected entry %q", hdr.Name)
		}
	}
	return sc, nil
}

// buildSnapshot pauses writes, records the log offset and writes a snapshot to a temp file.
func (h *Host) buildSnapshot() (path string, offset uint64, err error) {
	f, err := os.CreateTemp(h.Cfg.DataDir, ".snapshot-*.tgz")
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h.pause.Lock()
	defer h.pause.Unlock()
	h.checkpointAll()
	offset = h.Cluster.Offset() // offset BEFORE exporting keys: see note in docs
	_ = h.Tracker.Save()
	ttl, _ := os.ReadFile(filepath.Join(h.Cfg.DataDir, "cluster", "ttl.json"))
	if err := WriteSnapshot(f, h.Reg.Root(), h.KS.List(), ttl); err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), offset, nil
}

func (h *Host) serveFile(w http.ResponseWriter, path string, offset uint64, name string) {
	defer os.Remove(path)
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, "snapshot failed")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Redx-Snapshot-Offset", strconv.FormatUint(offset, 10))
	h.Cluster.mu.Lock()
	rid := h.Cluster.meta.ReplID
	h.Cluster.mu.Unlock()
	w.Header().Set("X-Redx-Replid", rid)
	_, _ = io.Copy(w, f)
}

func (c *Cluster) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.nodeAuth(w, r); !ok {
		return
	}
	if !c.IsLeader() {
		writeErr(w, http.StatusMisdirectedRequest, "not the leader")
		return
	}
	p, off, err := c.h.buildSnapshot()
	if err != nil {
		writeErr(w, 500, "snapshot failed")
		c.h.Logf("snapshot: %v", err)
		return
	}
	c.h.serveFile(w, p, off, "redx-snapshot.tgz")
}

// installSnapshot downloads and installs a full snapshot from the leader.
func (c *Cluster) installSnapshot(ctx context.Context, leader string) error {
	h := c.h
	req, _ := http.NewRequestWithContext(ctx, "GET", leader+"/v1/cluster/snapshot", nil)
	c.nodeHeaders(req)
	cl := &http.Client{Transport: c.client.Transport} // no overall timeout: large transfers
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("snapshot refused (%d)", resp.StatusCode)
	}
	off, err := strconv.ParseUint(resp.Header.Get("X-Redx-Snapshot-Offset"), 10, 64)
	if err != nil {
		return errors.New("snapshot: missing offset header")
	}
	rid := resp.Header.Get("X-Redx-Replid")
	tmp, err := os.MkdirTemp(h.Cfg.DataDir, ".snap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	sc, err := ExtractSnapshot(resp.Body, filepath.Join(tmp, "dbs"))
	if err != nil {
		return err
	}
	h.pause.Lock()
	defer h.pause.Unlock()
	if err := h.Reg.CloseAll(); err != nil {
		h.Logf("snapshot: closing dbs: %v", err)
	}
	old := h.Reg.Root() + ".old-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.Rename(h.Reg.Root(), old); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, "dbs"), h.Reg.Root()); err != nil {
		_ = os.Rename(old, h.Reg.Root())
		return err
	}
	_ = os.RemoveAll(old)
	if err := h.KS.ReplaceAll(sc.Keys); err != nil {
		return err
	}
	if sc.TTL != nil {
		_ = os.WriteFile(filepath.Join(c.dir, "ttl.json"), sc.TTL, 0o600)
	}
	h.Tracker.Reload()
	if err := c.log.Reset(off); err != nil {
		return err
	}
	c.mu.Lock()
	c.meta.ReplID = rid
	c.snaps++
	_ = c.saveMeta()
	c.mu.Unlock()
	h.Logf("replication: full snapshot installed, resuming at offset %d", off+1)
	return nil
}

// handleBackup streams an online snapshot (host admin only).
func (h *Host) handleBackup(w http.ResponseWriter, r *http.Request, p *Principal) {
	path, off, err := h.buildSnapshot()
	if err != nil {
		writeErr(w, 500, "backup failed")
		h.Logf("backup: %v", err)
		return
	}
	h.Audit.Log(AuditEvent{Event: "admin.backup", KeyID: p.Key.ID, Remote: remoteIP(r)})
	h.serveFile(w, path, off, "redx-backup-"+time.Now().UTC().Format("20060102-150405")+".tgz")
}

// ---- offline backup / restore (used by the CLI) ----

// OfflineBackup snapshots dataDir into w. The daemon must not be running unless force is set.
func OfflineBackup(dataDir string, w io.Writer, pass string, force bool) error {
	l := flock.New(filepath.Join(dataDir, "redxd.lock"))
	if ok, _ := l.TryLock(); !ok {
		if !force {
			return errors.New("redxd is running on this data dir: use the online backup (--addr/--key) or stop it")
		}
	} else {
		defer l.Unlock()
	}
	var keys []KeyRecord
	if _, err := os.Stat(filepath.Join(dataDir, "keys.red-xai-keys")); err == nil {
		ks, err := OpenKeystore(filepath.Join(dataDir, "keys.red-xai-keys"), KeystoreOptions{Passphrase: pass})
		if err != nil {
			return err
		}
		keys = ks.List()
	}
	ttl, _ := os.ReadFile(filepath.Join(dataDir, "cluster", "ttl.json"))
	return WriteSnapshot(w, filepath.Join(dataDir, "dbs"), keys, ttl)
}

// OfflineRestore restores a snapshot into dataDir. Refuses if redxd is running
// or the data dir already has databases (unless force, which moves them aside).
func OfflineRestore(dataDir string, r io.Reader, pass string, force bool) (movedTo string, err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	l := flock.New(filepath.Join(dataDir, "redxd.lock"))
	if ok, _ := l.TryLock(); !ok {
		return "", errors.New("redxd is running on this data dir; stop it first")
	}
	defer l.Unlock()
	dbs := filepath.Join(dataDir, "dbs")
	if ents, _ := os.ReadDir(dbs); len(ents) > 0 && !force {
		return "", errors.New("data dir already contains databases; use --force (they are moved aside, not deleted)")
	}
	tmp, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	sc, err := ExtractSnapshot(r, filepath.Join(tmp, "dbs"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dbs); err == nil {
		movedTo = dbs + ".pre-restore-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(dbs, movedTo); err != nil {
			return "", err
		}
	}
	if err := os.Rename(filepath.Join(tmp, "dbs"), dbs); err != nil {
		return movedTo, err
	}
	ks, err := OpenKeystore(filepath.Join(dataDir, "keys.red-xai-keys"), KeystoreOptions{Passphrase: pass})
	if err != nil {
		return movedTo, err
	}
	if err := ks.ReplaceAll(sc.Keys); err != nil {
		return movedTo, err
	}
	// New history: drop the replication log; a fresh replid is generated on next start.
	cdir := filepath.Join(dataDir, "cluster")
	_ = os.Remove(filepath.Join(cdir, "oplog.jsonl"))
	_ = os.Remove(filepath.Join(cdir, "repl.json"))
	if sc.TTL != nil {
		_ = os.MkdirAll(cdir, 0o700)
		_ = os.WriteFile(filepath.Join(cdir, "ttl.json"), sc.TTL, 0o600)
	}
	return movedTo, nil
}
