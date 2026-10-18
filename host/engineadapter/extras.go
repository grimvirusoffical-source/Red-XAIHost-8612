package engineadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"redx/box"
	"redx/engine"
	"redx/engine/code"
	"redx/engine/idx"
	"redx/engine/link"
	"redx/engine/media"
	"redx/engine/pack"
	"redx/engine/pulse"
	"redx/engine/query"
	"redx/engine/sec"
	"redx/host"
)

// extras holds the optional per-database engines (indexes, packs, rules). It is embedded in store.
type extras struct {
	dir string

	mu       sync.Mutex
	sets     map[string]*engine.IndexSet
	packs    map[string]*pack.Pack
	pulseEng *pulse.Engine
	bridge   func()

	pulseP   atomic.Pointer[pulse.Engine]
	hasRules atomic.Bool
	nSets    atomic.Int32
	opening  atomic.Int32 // sets being built: writes must queue behind the build so none is missed
	imu      sync.Mutex   // serialises "read the record, then update the indexes"
	follower atomic.Bool

	cfg Config

	// access policy (.red-xai-sec), scripts (.red-xai-code), standalone links, media libraries
	secP         atomic.Pointer[secState]
	secMu        sync.Mutex
	codeP        atomic.Pointer[codeState]
	codeMu       sync.Mutex
	codeHostOnce sync.Once
	codeHostV    *code.Host
	codeHostErr  error
	links        atomic.Pointer[link.LinkSet]
	media        map[string]*media.Store

	// RXQL: one cached query engine and one planner index manager over every index set
	qmu      sync.Mutex
	qdb      *query.DB
	qdbMgr   *idx.Manager
	sqlMgr   *idx.Manager // guarded by mu
	sqlTried bool         // guarded by mu
	sqlOn    atomic.Bool
}

var _ host.Extras = (*store)(nil)

const (
	idxExt    = ".red-xai-db-idx"
	packExt   = ".red-xai-pack"
	pulseFile = "rules.red-xai-db-pulse"
)

// initExtras loads every .red-xai-* file of the database folder. A .red-xai-sec file that does
// not parse is an error (the database must not open with weaker access rules than intended);
// problems in the other definition files are logged and that feature stays off.
func (s *store) initExtras(dir string, follower bool) error {
	s.dir = dir
	s.follower.Store(follower)
	s.secP.Store(newSecState(nil, "", nil))
	if err := s.loadSec(); err != nil {
		return fmt.Errorf("access policy: %w", err)
	}
	s.loadLinks()
	s.loadCode()
	if !follower {
		s.pulseLoadFile()
		s.openAllSets()
	}
	return nil
}

// openAllSets opens every .red-xai-db-idx set so writes keep them current from the start.
func (s *store) openAllSets() {
	names, err := globFiles(s.dir, idxExt)
	if err != nil {
		return
	}
	for _, n := range names {
		set := strings.TrimSuffix(n, idxExt)
		if validSet(set) != nil {
			continue
		}
		if _, err := s.openSet(set); err != nil {
			s.warnf("idx: %s did not open: %v", n, err)
		}
	}
}

func (s *store) closeExtras() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopPulseLocked()
	for _, set := range s.sets {
		_ = set.Close()
	}
	s.sets, s.packs = nil, nil
	s.nSets.Store(0)
}

func recPath(a link.Addr) string {
	p := a.File + ":" + a.Box
	if a.HasID {
		p += "[ID=" + strconv.FormatInt(a.ID, 10) + "]"
	}
	return p
}

func extErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, host.ErrNotFound), errors.Is(err, host.ErrInvalid), errors.Is(err, host.ErrReadOnly):
		return err
	case errors.Is(err, pack.ErrNotFound):
		return host.ErrNotFound
	case errors.Is(err, pack.ErrInvalidPath), errors.Is(err, pack.ErrIsDir), errors.Is(err, pack.ErrNotDir),
		errors.Is(err, pack.ErrNotEmpty), errors.Is(err, pack.ErrFolderFull):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	case errors.Is(err, pack.ErrTooLarge):
		return fmt.Errorf("%w: %v", host.ErrTooLarge, err)
	case errors.Is(err, pack.ErrCorrupt), errors.Is(err, pack.ErrMissing),
		errors.Is(err, sec.ErrAuth), errors.Is(err, sec.ErrTruncated), errors.Is(err, sec.ErrFormat):
		return fmt.Errorf("%w: %v", host.ErrCorrupt, err)
	case errors.Is(err, sec.ErrUnknownKey), errors.Is(err, sec.ErrBadKey), errors.Is(err, host.ErrUnavailable):
		return fmt.Errorf("%w: %v", host.ErrUnavailable, err)
	case errors.Is(err, host.ErrUnsupported), errors.Is(err, host.ErrTooLarge), errors.Is(err, host.ErrMediaType):
		return err
	case errors.Is(err, idx.ErrBadQuery), errors.Is(err, idx.ErrWrongKind), errors.Is(err, idx.ErrUnknownField),
		errors.Is(err, idx.ErrFieldRequired), errors.Is(err, idx.ErrBadDecl), errors.Is(err, idx.ErrRebuilding):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	case errors.Is(err, engine.ErrBadPath):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	case errors.Is(err, engine.ErrFollower):
		return host.ErrReadOnly
	}
	var be *box.Error
	if errors.As(err, &be) {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	return err
}

// ---- index maintenance on writes ----

func pathAddr(p string) (link.Addr, bool, bool) { // addr, ok, wild
	bp, err := box.ParsePath(p)
	if err != nil || bp.File == "" || len(bp.Segs) == 0 {
		return link.Addr{}, false, false
	}
	s := bp.Segs[0]
	if s.Wild || s.AnyID {
		return link.Addr{}, false, true
	}
	return link.Addr{File: bp.File, Box: s.Name, ID: s.ID, HasID: s.HasID}, true, false
}

// observePath updates open indexes after a host write to path.
func (s *store) observePath(p string) {
	if s.nSets.Load() == 0 && s.opening.Load() == 0 && !s.sqlOn.Load() {
		return
	}
	a, ok, wild := pathAddr(p)
	switch {
	case ok:
		s.observeAddr(a)
	case wild:
		s.mu.Lock()
		sets := s.setList()
		s.mu.Unlock()
		for _, set := range sets {
			_ = set.Rebuild()
		}
		s.invalidateSQL()
	}
}

func (s *store) setList() []*engine.IndexSet {
	out := make([]*engine.IndexSet, 0, len(s.sets))
	for _, st := range s.sets {
		out = append(out, st)
	}
	return out
}

func (s *store) observeAddr(a link.Addr) {
	if s.nSets.Load() == 0 && s.opening.Load() == 0 && !s.sqlOn.Load() {
		return
	}
	s.mu.Lock()
	sets := s.setList()
	sqlMgr := s.sqlMgr
	s.mu.Unlock()
	s.imu.Lock()
	defer s.imu.Unlock()
	rec, ok, err := s.db.Records().Get(a)
	if err != nil {
		return
	}
	mgrs := make([]*idx.Manager, 0, len(sets)+1)
	for _, set := range sets {
		mgrs = append(mgrs, set.Manager)
	}
	if sqlMgr != nil {
		mgrs = append(mgrs, sqlMgr)
	}
	for _, m := range mgrs {
		if ok {
			m.OnPut(a, rec)
		} else {
			m.OnDelete(a)
		}
	}
}

// obsStore is the link.Store handed to the pulse engine: writes (including those made by rule
// actions) update the open indexes.
type obsStore struct {
	link.Store
	s *store
}

func (o obsStore) Put(a link.Addr, e *box.Entry) error {
	err := o.Store.Put(a, e)
	if err == nil {
		o.s.observeAddr(a)
	}
	return err
}

func (o obsStore) Delete(a link.Addr) (bool, error) {
	ok, err := o.Store.Delete(a)
	if err == nil && ok {
		o.s.observeAddr(a)
	}
	return ok, err
}

// ---- indexes ----

func validSet(name string) error {
	if name == "" || len(name) > 64 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return fmt.Errorf("%w: bad set name", host.ErrInvalid)
	}
	return nil
}

func (s *store) idxPath(set string) string { return filepath.Join(s.dir, set+idxExt) }

func writeFileAtomic(p string, b []byte) error {
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// openSet returns the open set, opening it (restore or rebuild) on first use. Leader only.
func (s *store) openSet(name string) (*engine.IndexSet, error) {
	if err := validSet(name); err != nil {
		return nil, err
	}
	s.opening.Add(1)
	defer s.opening.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.sets[name]; set != nil {
		return set, nil
	}
	if _, err := os.Stat(s.idxPath(name)); err != nil {
		return nil, host.ErrNotFound
	}
	if s.follower.Load() {
		return nil, host.ErrReadOnly
	}
	s.imu.Lock()
	set, err := s.db.OpenIndexes(name)
	s.imu.Unlock()
	if err != nil {
		return nil, extErr(err)
	}
	if s.sets == nil {
		s.sets = map[string]*engine.IndexSet{}
	}
	s.sets[name] = set
	s.nSets.Store(int32(len(s.sets)))
	return set, nil
}

func (s *store) IndexDefine(name, decl string) error {
	if err := validSet(name); err != nil {
		return err
	}
	if _, err := idx.ParseManager(decl); err != nil {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	if !strings.HasSuffix(decl, "\n") {
		decl += "\n"
	}
	p := s.idxPath(name)
	old, oldErr := os.ReadFile(p)
	if err := writeFileAtomic(p, []byte(decl)); err != nil {
		return err
	}
	s.invalidateSQL()
	if s.follower.Load() {
		return nil // stored; opened when this node becomes leader
	}
	s.mu.Lock()
	if cur := s.sets[name]; cur != nil {
		delete(s.sets, name)
		s.nSets.Store(int32(len(s.sets)))
		_ = cur.Close()
	}
	s.mu.Unlock()
	if _, err := s.openSet(name); err != nil {
		if oldErr == nil {
			_ = writeFileAtomic(p, old)
		} else {
			_ = os.Remove(p)
		}
		return err
	}
	return nil
}

func (s *store) IndexDrop(name string) error {
	if err := validSet(name); err != nil {
		return err
	}
	p := s.idxPath(name)
	if _, err := os.Stat(p); err != nil {
		return host.ErrNotFound
	}
	s.mu.Lock()
	delete(s.sets, name)
	s.nSets.Store(int32(len(s.sets)))
	s.mu.Unlock()
	if err := os.Remove(p); err != nil {
		return err
	}
	_ = os.RemoveAll(p + ".snap")
	s.invalidateSQL()
	return nil
}

func (s *store) IndexList() ([]host.IndexSetInfo, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []host.IndexSetInfo
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, idxExt) {
			continue
		}
		name := strings.TrimSuffix(n, idxExt)
		src, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		info := host.IndexSetInfo{Set: name, Source: string(src)}
		s.mu.Lock()
		set := s.sets[name]
		s.mu.Unlock()
		var mgr *idx.Manager
		if set != nil {
			mgr = set.Manager
			for _, l := range set.Loaded {
				info.Loaded = append(info.Loaded, l.Name+": "+l.Status)
			}
		} else if m, err := idx.ParseManager(string(src)); err == nil {
			mgr = m
		}
		if mgr != nil {
			for _, ix := range mgr.Indexes() {
				d := ix.Decl()
				st := ix.Stats()
				info.Indexes = append(info.Indexes, host.IndexInfo{Name: d.Name, File: d.File, Box: d.Box, Kind: string(d.Kind),
					Fields: d.Fields, Fold: d.Fold, Records: st.Records, Terms: st.Terms})
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Set < out[j].Set })
	return out, nil
}

func (s *store) IndexRebuild(name string) error {
	set, err := s.openSet(name)
	if err != nil {
		return err
	}
	s.imu.Lock()
	err = set.Rebuild()
	s.imu.Unlock()
	s.invalidateSQL()
	return extErr(err)
}

func (s *store) IndexCheck(name string) (map[string]host.IndexDiff, error) {
	set, err := s.openSet(name)
	if err != nil {
		return nil, err
	}
	s.imu.Lock()
	defer s.imu.Unlock()
	d, err := set.Check()
	if err != nil {
		return nil, extErr(err)
	}
	out := map[string]host.IndexDiff{}
	for k, v := range d {
		out[k] = host.IndexDiff{Missing: len(v.Missing), Extra: len(v.Extra), Changed: len(v.Changed), Stale: v.Stale()}
	}
	return out, nil
}

func queryValue(v, vtype string) (box.Value, error) {
	switch strings.ToLower(vtype) {
	case "string":
		return box.String(v), nil
	case "number":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return box.Value{}, fmt.Errorf("%w: value %q is not a number", host.ErrInvalid, v)
		}
		return box.Number(f), nil
	case "bool":
		switch strings.ToLower(v) {
		case "true":
			return box.Bool(true), nil
		case "false":
			return box.Bool(false), nil
		}
		return box.Value{}, fmt.Errorf("%w: value %q is not a bool", host.ErrInvalid, v)
	case "", "auto":
		switch strings.ToLower(v) {
		case "true":
			return box.Bool(true), nil
		case "false":
			return box.Bool(false), nil
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return box.Number(f), nil
		}
		return box.String(v), nil
	}
	return box.Value{}, fmt.Errorf("%w: vtype must be auto, string, number or bool", host.ErrInvalid)
}

func (s *store) IndexQuery(setName, index string, q host.IndexQuery, allow func(string) bool) ([]host.IndexHit, error) {
	set, err := s.openSet(setName)
	if err != nil {
		return nil, err
	}
	ix := set.Manager.Index(index)
	if ix == nil {
		return nil, host.ErrNotFound
	}
	want := q.Limit
	if want <= 0 {
		want = 100
	}
	run := func(limit int) ([]idx.Hit, error) {
		switch q.Mode {
		case "exact":
			v, err := queryValue(q.Value, q.VType)
			if err != nil {
				return nil, err
			}
			return ix.Exact(q.Field, v, limit)
		case "prefix":
			return ix.Prefix(q.Field, q.Value, limit)
		case "range":
			var lo, hi *idx.Bound
			if q.Lo != nil {
				lo = &idx.Bound{Value: *q.Lo, Exclusive: q.LoExcl}
			}
			if q.Hi != nil {
				hi = &idx.Bound{Value: *q.Hi, Exclusive: q.HiExcl}
			}
			return ix.Range(q.Field, lo, hi, limit)
		case "text":
			op := idx.And
			switch strings.ToLower(q.Op) {
			case "", "and":
			case "or":
				op = idx.Or
			default:
				return nil, fmt.Errorf("%w: op must be and or or", host.ErrInvalid)
			}
			return ix.Text(q.Field, op, q.Terms, limit)
		}
		return nil, fmt.Errorf("%w: mode must be exact, prefix, range or text", host.ErrInvalid)
	}
	rs := s.db.Records()
	fetch := want
	for round := 0; ; round++ {
		hits, err := run(fetch)
		if err != nil {
			return nil, extErr(err)
		}
		var out []host.IndexHit
		for _, h := range hits {
			p := recPath(h.Addr)
			if allow != nil && !allow(p) {
				continue
			}
			rec, ok, err := rs.Get(h.Addr) // hides expired / vanished records
			if err != nil {
				return nil, err
			}
			if !ok {
				s.imu.Lock()
				set.Manager.OnDelete(h.Addr)
				s.imu.Unlock()
				continue
			}
			hit := host.IndexHit{Path: p, Field: h.Field, Value: h.Value.String()}
			if q.Record {
				hit.Record = rec.Value.String()
			}
			out = append(out, hit)
			if len(out) >= want {
				return out, nil
			}
		}
		if len(hits) < fetch || fetch >= 200000 || round >= 6 {
			return out, nil
		}
		fetch *= 4
	}
}

// ---- packs ----

func (s *store) packRoot(name string) string { return filepath.Join(s.dir, name+packExt) }

func (s *store) packFor(name string, create bool) (*pack.Pack, error) {
	if err := validSet(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.packs[name]; p != nil {
		return p, nil
	}
	if !create {
		if st, err := os.Stat(s.packRoot(name)); err != nil || !st.IsDir() {
			return nil, host.ErrNotFound
		}
	}
	p, err := s.db.OpenPack(name, pack.Config{})
	if err != nil {
		return nil, extErr(err)
	}
	if s.packs == nil {
		s.packs = map[string]*pack.Pack{}
	}
	s.packs[name] = p
	return p, nil
}

func packInfo(i pack.Info) host.PackFile {
	return host.PackFile{Path: i.Path, Name: i.Name, IsDir: i.IsDir, Size: i.Size, SHA256: i.SHA256, ChunkSize: i.ChunkSize}
}

// packInfoFor marks files of packs that are under an Encrypt rule.
func (s *store) packInfoFor(name string, i pack.Info) host.PackFile {
	pf := packInfo(i)
	if _, req := s.encryptionFor(name + packExt); req && !i.IsDir {
		pf.Encrypted = true
	}
	return pf
}

func encContext(pack, path string) []byte { return []byte("redx-pack:" + pack + "/" + path) }

// encKey finds the master key an Encrypt rule names.
func (s *store) encKey(keyID string) (*sec.MasterKey, error) {
	if s.cfg.Keyring == nil {
		return nil, fmt.Errorf("%w: %v (start redxd with a master key file)", host.ErrUnavailable, errNoKey)
	}
	k, ok := s.cfg.Keyring.Key(keyID)
	if !ok {
		return nil, fmt.Errorf("%w: %v: key id %q is not in the master key file", host.ErrUnavailable, errNoKey, keyID)
	}
	return k, nil
}

func (s *store) PackList() ([]string, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && strings.HasSuffix(e.Name(), packExt) {
			out = append(out, strings.TrimSuffix(e.Name(), packExt))
		}
	}
	return out, nil
}

func (s *store) PackPut(name, path string, r io.Reader) (host.PackFile, error) {
	if s.follower.Load() {
		return host.PackFile{}, host.ErrReadOnly
	}
	var key *sec.MasterKey
	keyID, required := s.encryptionFor(name + packExt)
	if required {
		var err error
		if key, err = s.encKey(keyID); err != nil {
			return host.PackFile{}, err
		}
	}
	p, err := s.packFor(name, true)
	if err != nil {
		return host.PackFile{}, err
	}
	if required { // encrypt on the fly: what reaches the pack is ciphertext
		pr, pw := io.Pipe()
		defer pr.Close()
		src := r
		go func() { pw.CloseWithError(sec.Encrypt(pw, src, key, sec.Options{Context: encContext(name, path)})) }()
		r = pr
	}
	info, err := p.Put(path, r)
	if err != nil {
		return host.PackFile{}, extErr(err)
	}
	return s.packInfoFor(name, info), nil
}

func (s *store) PackStat(name, path string) (host.PackFile, error) {
	p, err := s.packFor(name, false)
	if err != nil {
		return host.PackFile{}, err
	}
	info, err := p.Stat(path)
	if err != nil {
		return host.PackFile{}, extErr(err)
	}
	return s.packInfoFor(name, info), nil
}

func (s *store) PackLs(name, folder string) ([]host.PackFile, error) {
	p, err := s.packFor(name, false)
	if err != nil {
		return nil, err
	}
	infos, err := p.List(folder)
	if err != nil {
		return nil, extErr(err)
	}
	out := make([]host.PackFile, len(infos))
	for i, in := range infos {
		out[i] = s.packInfoFor(name, in)
	}
	return out, nil
}

func (s *store) PackDelete(name, path string) error {
	if s.follower.Load() {
		return host.ErrReadOnly
	}
	p, err := s.packFor(name, false)
	if err != nil {
		return err
	}
	return extErr(p.Delete(path))
}

func (s *store) PackGC(name string) (host.PackGCResult, error) {
	if s.follower.Load() {
		return host.PackGCResult{}, host.ErrReadOnly
	}
	p, err := s.packFor(name, false)
	if err != nil {
		return host.PackGCResult{}, err
	}
	r, err := p.GC()
	if err != nil {
		return host.PackGCResult{}, extErr(err)
	}
	return host.PackGCResult{ChunksRemoved: r.ChunksRemoved, ManifestsRemoved: r.ManifestsRemoved}, nil
}

func (s *store) PackFsck(name string, deep bool) (host.PackFsckResult, error) {
	p, err := s.packFor(name, false)
	if err != nil {
		return host.PackFsckResult{}, err
	}
	rep, err := p.Fsck(deep)
	if err != nil {
		return host.PackFsckResult{}, extErr(err)
	}
	out := host.PackFsckResult{OK: rep.OK(), Folders: rep.Folders, Files: rep.Files, Chunks: rep.Chunks,
		Problems: []string{}, OrphanChunks: len(rep.OrphanChunks), OrphanManifests: len(rep.OrphanManifests)}
	for _, pr := range rep.Problems {
		out.Problems = append(out.Problems, pr.String())
	}
	return out, nil
}

func (s *store) PackOpen(name, path string, off, n int64) (io.ReadCloser, host.PackFile, error) {
	p, err := s.packFor(name, false)
	if err != nil {
		return nil, host.PackFile{}, err
	}
	info, err := p.Stat(path)
	if err != nil {
		return nil, host.PackFile{}, extErr(err)
	}
	if info.IsDir {
		return nil, host.PackFile{}, fmt.Errorf("%w: that is a folder", host.ErrInvalid)
	}
	pf := s.packInfoFor(name, info)
	if pf.Encrypted { // whole file only: decrypted as a stream (ranges are not seekable in ciphertext)
		keyID, _ := s.encryptionFor(name + packExt)
		if _, err := s.encKey(keyID); err != nil {
			return nil, pf, err
		}
		rc, _, err := p.Get(path)
		if err != nil {
			return nil, pf, extErr(err)
		}
		dr, err := sec.NewReader(rc, s.cfg.Keyring, sec.Options{Context: encContext(name, path)})
		if err != nil {
			rc.Close()
			return nil, pf, extErr(err)
		}
		return &decReader{r: dr, c: rc}, pf, nil
	}
	if off <= 0 && (n < 0 || n >= info.Size) {
		rc, _, err := p.Get(path)
		if err != nil {
			return nil, pf, extErr(err)
		}
		return &mapReader{rc: rc}, pf, nil
	}
	if off < 0 || off > info.Size || n < 0 || off+n > info.Size {
		return nil, pf, fmt.Errorf("%w: range outside the file", host.ErrInvalid)
	}
	cs := int64(info.ChunkSize)
	if cs <= 0 || int64(len(info.Chunks)) != (info.Size+cs-1)/cs {
		return nil, pf, fmt.Errorf("%w: inconsistent manifest", host.ErrCorrupt)
	}
	return &rangeReader{be: pack.DirBackend{Root: s.packRoot(name)}, info: info, pos: off, end: off + n}, pf, nil
}

// decReader streams decrypted bytes and maps cipher/pack errors onto host errors.
type decReader struct {
	r io.Reader
	c io.Closer
}

func (d *decReader) Read(b []byte) (int, error) {
	n, err := d.r.Read(b)
	if err != nil && err != io.EOF {
		err = extErr(err)
	}
	return n, err
}
func (d *decReader) Close() error { return d.c.Close() }

// mapReader maps pack read errors onto host errors.
type mapReader struct{ rc io.ReadCloser }

func (m *mapReader) Read(b []byte) (int, error) {
	n, err := m.rc.Read(b)
	if err != nil && err != io.EOF {
		err = extErr(err)
	}
	return n, err
}
func (m *mapReader) Close() error { return m.rc.Close() }

// rangeReader reads bytes [pos,end) straight from the chunk store, checking the length and SHA-256 of
// every chunk it touches. It cannot check the whole-file hash (it does not read the whole file).
type rangeReader struct {
	be   pack.DirBackend
	info pack.Info
	pos  int64
	end  int64
	buf  []byte
	err  error
}

func (r *rangeReader) Read(b []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(r.buf) == 0 {
		if r.pos >= r.end {
			return 0, io.EOF
		}
		cs := int64(r.info.ChunkSize)
		ci := r.pos / cs
		id := r.info.Chunks[ci]
		data, err := r.be.GetChunk(id)
		if err != nil {
			r.err = fmt.Errorf("%w: chunk %d: %v", host.ErrCorrupt, ci, err)
			return 0, r.err
		}
		want := cs
		if ci == int64(len(r.info.Chunks))-1 {
			want = r.info.Size - ci*cs
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != want || hex.EncodeToString(sum[:]) != id {
			r.err = fmt.Errorf("%w: chunk %d failed its size/hash check", host.ErrCorrupt, ci)
			return 0, r.err
		}
		start := r.pos - ci*cs
		stop := int64(len(data))
		if rem := r.end - ci*cs; rem < stop {
			stop = rem
		}
		r.buf = data[start:stop]
		r.pos += int64(len(r.buf))
	}
	n := copy(b, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *rangeReader) Close() error { return nil }

// ---- links ----

type filterStore struct {
	link.Store
	src   link.Addr
	allow func(string) bool
}

func (f filterStore) Get(a link.Addr) (*box.Entry, bool, error) {
	if a != f.src && !f.allow(recPath(a)) {
		return nil, false, nil // looks exactly like a missing record
	}
	return f.Store.Get(a)
}

func (f filterStore) Scan(file string, fn func(link.Addr, *box.Entry) bool) error {
	return f.Store.Scan(file, func(a link.Addr, e *box.Entry) bool {
		if a != f.src && !f.allow(recPath(a)) {
			return true
		}
		return fn(a, e)
	})
}

func (s *store) Resolve(p string, depth int, allow func(string) bool) (host.Resolved, error) {
	bp, err := box.ParsePath(p)
	if err != nil {
		return host.Resolved{}, fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	a, ok, _ := pathAddr(p)
	if !ok || bp.HasWild() {
		return host.Resolved{}, fmt.Errorf("%w: resolve needs a concrete path with a file, e.g. players.red-xai.db:Player[ID=1].Guild", host.ErrInvalid)
	}
	var st link.Store = s.db.Records()
	if allow != nil {
		st = filterStore{Store: st, src: a, allow: allow}
	}
	x, err := link.New(st, link.Options{Links: s.linkSet()}).Expand(a, depth)
	if err != nil {
		if errors.Is(err, link.ErrDangling) {
			return host.Resolved{}, host.ErrNotFound
		}
		return host.Resolved{}, extErr(err)
	}
	v := x.Entry.Value
	if len(bp.Segs) > 1 {
		if v.Kind != box.KindBox || v.Box == nil {
			return host.Resolved{}, host.ErrNotFound
		}
		e, err := box.Resolve(v.Box, bp.Segs[1:])
		if err != nil {
			return host.Resolved{}, host.ErrNotFound
		}
		v = e.Value
	}
	out := host.Resolved{Value: v.String()}
	for _, d := range x.Dangling {
		out.Dangling = append(out.Dangling, d.String())
	}
	for _, c := range x.Cycles {
		out.Cycles = append(out.Cycles, c.String())
	}
	return out, nil
}

// ---- pulse ----

func (s *store) pulsePath() string { return filepath.Join(s.dir, pulseFile) }

const pulseExt = ".red-xai-db-pulse"

// pulseSources joins every .red-xai-db-pulse file of the folder (hand-placed ones and the
// API-managed rules.red-xai-db-pulse). skipManaged leaves the managed file out.
func (s *store) pulseSources(skipManaged bool) string {
	names, err := globFiles(s.dir, pulseExt)
	if err != nil {
		return ""
	}
	var parts []string
	for _, n := range names {
		if skipManaged && n == pulseFile {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(s.dir, n)); err == nil && strings.TrimSpace(string(b)) != "" {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

// pulseLoadFile starts the rule engine from every stored definition file (leader only).
func (s *store) pulseLoadFile() {
	txt := s.pulseSources(false)
	if strings.TrimSpace(txt) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadRulesLocked(txt); err != nil && s.logf != nil {
		s.logf("pulse: stored rules did not load: %v", err)
	}
}

func (s *store) ensurePulseLocked() *pulse.Engine {
	if s.pulseEng == nil {
		s.pulseEng = pulse.New(obsStore{Store: s.db.Records(), s: s}, pulse.Config{})
		s.pulseP.Store(s.pulseEng)
	}
	return s.pulseEng
}

func (s *store) loadRulesLocked(src string) error {
	rules, err := pulse.ParseRules(src)
	if err != nil {
		return err
	}
	eng := s.ensurePulseLocked()
	for _, n := range eng.Rules() {
		eng.RemoveRule(n)
	}
	if err := eng.AddRules(rules...); err != nil {
		return err
	}
	s.hasRules.Store(len(rules) > 0)
	return nil
}

func (s *store) stopPulseLocked() {
	if s.bridge != nil {
		s.bridge()
		s.bridge = nil
	}
	if s.pulseEng != nil {
		s.pulseEng.Close()
		s.pulseEng = nil
		s.pulseP.Store(nil)
	}
	s.hasRules.Store(false)
}

func (s *store) PulseDefine(src string) error {
	others := s.pulseSources(true)
	combined := others
	if strings.TrimSpace(src) != "" {
		if _, err := pulse.ParseRules(src); err != nil {
			return fmt.Errorf("%w: %v", host.ErrInvalid, err)
		}
		if combined != "" {
			combined += "\n"
		}
		combined += src
		if _, err := pulse.ParseRules(combined); err != nil {
			return fmt.Errorf("%w: %v (rules in other .red-xai-db-pulse files in this database count too)", host.ErrInvalid, err)
		}
	}
	p := s.pulsePath()
	if strings.TrimSpace(src) == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := writeFileAtomic(p, []byte(src)); err != nil {
		return err
	}
	if s.follower.Load() {
		return nil // stored; runs once this node is promoted
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(combined) == "" {
		if s.pulseEng != nil {
			for _, n := range s.pulseEng.Rules() {
				s.pulseEng.RemoveRule(n)
			}
		}
		s.hasRules.Store(false)
		return nil
	}
	if err := s.loadRulesLocked(combined); err != nil {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	return nil
}

func (s *store) PulseGet() (host.PulseState, error) {
	st := host.PulseState{Rules: []string{}, Timers: []host.PulseTimer{}, Active: !s.follower.Load()}
	if b, err := os.ReadFile(s.pulsePath()); err == nil {
		st.Source = string(b) // the API-managed file; hand-placed files add rules but are not shown here
	}
	if rules, err := pulse.ParseRules(s.pulseSources(false)); err == nil {
		for _, r := range rules {
			st.Rules = append(st.Rules, r.Name)
		}
	}
	if eng := s.pulseP.Load(); eng != nil {
		for _, t := range eng.Timers() {
			st.Timers = append(st.Timers, host.PulseTimer{Rule: t.Rule, Name: t.Name, Path: recPath(t.Addr), At: t.Due})
		}
	}
	return st, nil
}

// setViaPulse routes a host write through the rule engine when rules exist, so rules see the
// before/after state. It reports handled=false for writes the rule engine cannot express
// (TTL on a field, wildcard paths, a field of a record that does not exist yet): those are
// written directly and do not run rules.
func (s *store) setViaPulse(path string, v box.Value, o host.SetOpts) (handled bool, err error) {
	eng := s.pulseP.Load()
	if eng == nil || !s.hasRules.Load() || s.follower.Load() {
		return false, nil
	}
	a, ok, _ := pathAddr(path)
	bp, perr := box.ParsePath(path)
	if !ok || perr != nil || bp.HasWild() {
		return false, nil
	}
	if len(bp.Segs) == 1 {
		var id *int64
		if a.HasID {
			i := a.ID
			id = &i
		}
		e := box.NewEntry(a.Box, id, v)
		switch {
		case o.CheckpointSeconds > 0:
			e.TTL.Kind, e.TTL.Seconds = box.TTLCheckpoint, o.CheckpointSeconds
		case o.TTLSeconds > 0 && o.Sliding:
			e.TTL.Kind, e.TTL.Seconds = box.TTLSliding, o.TTLSeconds
		case o.TTLSeconds > 0:
			e.TTL.Kind, e.TTL.Seconds = box.TTLFixed, o.TTLSeconds
		}
		_, err := eng.Put(a, e)
		return true, err
	}
	if o.TTLSeconds > 0 || o.CheckpointSeconds > 0 || o.Sliding {
		return false, nil
	}
	if _, exists, _ := s.db.Records().Get(a); !exists {
		return false, nil
	}
	_, err = eng.Set(a, box.SegsString(bp.Segs[1:]), v)
	return true, err
}

func (s *store) PulseSubscribe(topic string, changes bool, allow func(string) bool) (<-chan host.PulseEvent, func(), error) {
	if s.follower.Load() {
		return nil, nil, host.ErrReadOnly
	}
	s.mu.Lock()
	eng := s.ensurePulseLocked()
	if changes && s.bridge == nil {
		s.bridge = s.db.BridgeChanges("*", eng.Bus())
	}
	s.mu.Unlock()
	in, cancelIn, err := eng.Subscribe(topic, 256)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan host.PulseEvent, 64)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-in:
				if !ok {
					return
				}
				if ev.Kind == pulse.KindChange && !changes {
					continue
				}
				pe := host.PulseEvent{Seq: ev.Seq, Time: ev.Time, Kind: string(ev.Kind), Topic: ev.Topic, Rule: ev.Rule, Error: ev.Err, Timer: ev.Timer}
				if ev.Addr.File != "" {
					pe.Path = recPath(ev.Addr)
					if allow != nil && !allow(pe.Path) {
						continue
					}
				} else if allow != nil {
					continue // no record to check the key's prefix against
				}
				if ev.Data != nil {
					pe.Data = ev.Data.Value.String()
				}
				select {
				case out <- pe:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	var once sync.Once
	return out, func() { once.Do(func() { cancel(); cancelIn() }) }, nil
}

var _ = bytes.NewReader
var _ = time.Now
