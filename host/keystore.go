package host

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/argon2"
)

// API key format: rx_live_<43 base62 chars> (32 random bytes ≈ 256 bits).
// The first 8 chars of the body are a non-secret lookup id (they reveal ~47 of
// 256 bits). Only an argon2id hash with a per-key random salt is stored; the
// full key is shown once at creation and never persisted or logged.
const (
	KeyPrefix  = "rx_live_"
	keyBodyLen = 43
	keyIDLen   = 8
)

// Scopes.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
	AllDBs     = "*"
)

var scopeRank = map[string]int{ScopeRead: 1, ScopeWrite: 2, ScopeAdmin: 3}

var dbNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidDBName reports whether name is a legal database name.
func ValidDBName(n string) bool { return dbNameRe.MatchString(n) }

// Keystore errors. HTTP maps all verify failures to a generic 401.
var (
	ErrKeyMalformed = errors.New("malformed key")
	ErrKeyUnknown   = errors.New("unknown key")
	ErrKeyBad       = errors.New("key mismatch")
	ErrKeyRevoked   = errors.New("key revoked")
	ErrKeyExpired   = errors.New("key expired")
	ErrKeyNotFound  = errors.New("key id not found")
)

// KeyRecord is the persisted description of a key (never the key itself).
type KeyRecord struct {
	ID          string     `json:"id"`
	Hash        string     `json:"hash"` // base64 argon2id output
	Salt        string     `json:"salt"` // base64
	Mem         uint32     `json:"m"`
	Time        uint32     `json:"t"`
	Threads     uint8      `json:"p"`
	Scope       string     `json:"scope"`
	DB          string     `json:"db"`
	Prefix      string     `json:"prefix,omitempty"`
	Label       string     `json:"label,omitempty"`
	Roles       []string   `json:"roles,omitempty"` // role names for the db's .red-xai-sec policy
	Created     time.Time  `json:"created"`
	Expires     *time.Time `json:"expires,omitempty"`
	Revoked     *time.Time `json:"revoked,omitempty"`
	RateRPS     float64    `json:"rate_rps,omitempty"`
	RateBurst   int        `json:"rate_burst,omitempty"`
	RotatedFrom string     `json:"rotated_from,omitempty"`
	Kind        string     `json:"kind,omitempty"` // "" = api key, "node" reserved
}

// Active reports whether the record is usable at now.
func (r *KeyRecord) Active(now time.Time) error {
	if r.Revoked != nil {
		return ErrKeyRevoked
	}
	if r.Expires != nil && !now.Before(*r.Expires) {
		return ErrKeyExpired
	}
	return nil
}

// IsHostAdmin: admin on all databases.
func (r *KeyRecord) IsHostAdmin() bool { return r.Scope == ScopeAdmin && r.DB == AllDBs }

// Allows checks scope, database and path-prefix ACL.
func (r *KeyRecord) Allows(need, db, path string) bool {
	if scopeRank[r.Scope] < scopeRank[need] {
		return false
	}
	if r.DB != AllDBs && r.DB != db {
		return false
	}
	if r.Prefix != "" && !PathHasPrefix(path, r.Prefix) {
		return false
	}
	return true
}

// PathHasPrefix is a boundary-aware prefix match for engine paths.
func PathHasPrefix(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	if strings.ContainsRune(".[:/]", rune(prefix[len(prefix)-1])) && prefix[len(prefix)-1] != ']' {
		return true
	}
	return strings.ContainsRune(".[:/", rune(path[len(prefix)]))
}

// CreateSpec describes a key to create.
type CreateSpec struct {
	DB        string
	Scope     string
	Prefix    string
	Label     string
	Roles     []string      // names of roles declared in the db's .red-xai-sec (unknown names grant nothing)
	TTL       time.Duration // 0 = no expiry
	RateRPS   float64
	RateBurst int
}

// KeystoreOptions configure Open.
type KeystoreOptions struct {
	Passphrase string // non-empty => encrypt at rest (AES-256-GCM, argon2id-derived key)
	ArgonMem   uint32 // KiB, default 19456
	ArgonTime  uint32 // default 2
	Now        func() time.Time
}

// Keystore is the host-owned key database (one file, atomic rewrites, flock'd).
type Keystore struct {
	path string
	opts KeystoreOptions
	lock *flock.Flock

	mu    sync.RWMutex
	recs  map[string]*KeyRecord
	mtime time.Time
	size  int64
	chk   time.Time

	cacheKey [32]byte
	cmu      sync.Mutex
	cache    map[[32]byte]cacheEnt

	onChange func(KeyRecord) // replication hook (called with the new record state)
	loaded   bool
	pending  []KeyRecord // changes made by OTHER processes, to be announced via onChange
}

type cacheEnt struct {
	id   string
	hash string
	at   time.Time
}

const verifyCacheTTL = 5 * time.Minute

type keystoreFile struct {
	Version int          `json:"version"`
	Keys    []*KeyRecord `json:"keys"`
}

var encMagic = []byte("REDXKS1\n")

// OpenKeystore opens or creates the keystore at path.
func OpenKeystore(path string, o KeystoreOptions) (*Keystore, error) {
	if o.ArgonMem == 0 {
		o.ArgonMem = 19456
	}
	if o.ArgonTime == 0 {
		o.ArgonTime = 2
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ks := &Keystore{path: path, opts: o, lock: flock.New(path + ".lock"), recs: map[string]*KeyRecord{}, cache: map[[32]byte]cacheEnt{}}
	_, _ = rand.Read(ks.cacheKey[:])
	if err := ks.reloadLocked(true); err != nil {
		return nil, err
	}
	return ks, nil
}

// SetOnChange registers a callback invoked after every local mutation.
func (ks *Keystore) SetOnChange(f func(KeyRecord)) { ks.onChange = f }

func (ks *Keystore) readFile() ([]*KeyRecord, error) {
	b, err := os.ReadFile(ks.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if bytes.HasPrefix(b, encMagic) {
		if ks.opts.Passphrase == "" {
			return nil, errors.New("keystore is encrypted: set REDX_KEYSTORE_PASSPHRASE")
		}
		b = b[len(encMagic):]
		if len(b) < 16+12+16 {
			return nil, errors.New("keystore: truncated encrypted file")
		}
		salt, nonce, ct := b[:16], b[16:28], b[28:]
		gcm, err := ks.gcm(salt)
		if err != nil {
			return nil, err
		}
		pt, err := gcm.Open(nil, nonce, ct, encMagic)
		if err != nil {
			return nil, errors.New("keystore: decryption failed (wrong passphrase or corrupted file)")
		}
		b = pt
	}
	var f keystoreFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("keystore: %w", err)
	}
	return f.Keys, nil
}

func (ks *Keystore) gcm(salt []byte) (cipher.AEAD, error) {
	k := argon2.IDKey([]byte(ks.opts.Passphrase), salt, 2, 19456, 1, 32)
	blk, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func (ks *Keystore) writeFile() error {
	keys := make([]*KeyRecord, 0, len(ks.recs))
	for _, r := range ks.recs {
		keys = append(keys, r)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Created.Before(keys[j].Created) || (keys[i].Created.Equal(keys[j].Created) && keys[i].ID < keys[j].ID)
	})
	b, err := json.MarshalIndent(keystoreFile{Version: 1, Keys: keys}, "", " ")
	if err != nil {
		return err
	}
	if ks.opts.Passphrase != "" {
		salt := make([]byte, 16)
		nonce := make([]byte, 12)
		_, _ = rand.Read(salt)
		_, _ = rand.Read(nonce)
		gcm, err := ks.gcm(salt)
		if err != nil {
			return err
		}
		out := append([]byte{}, encMagic...)
		out = append(out, salt...)
		out = append(out, nonce...)
		out = gcm.Seal(out, nonce, b, encMagic)
		b = out
	}
	tmp := ks.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, ks.path); err != nil {
		return err
	}
	if st, err := os.Stat(ks.path); err == nil {
		ks.mtime, ks.size = st.ModTime(), st.Size()
	}
	return nil
}

// reloadLocked re-reads the file (caller holds ks.mu for write unless init).
func (ks *Keystore) reloadLocked(force bool) error {
	st, err := os.Stat(ks.path)
	if err != nil {
		if os.IsNotExist(err) {
			ks.recs = map[string]*KeyRecord{}
			ks.loaded = true
			return nil
		}
		return err
	}
	if !force && st.ModTime().Equal(ks.mtime) && st.Size() == ks.size {
		return nil
	}
	list, err := ks.readFile()
	if err != nil {
		return err
	}
	m := make(map[string]*KeyRecord, len(list))
	for _, r := range list {
		m[r.ID] = r
		if ks.loaded {
			if old, ok := ks.recs[r.ID]; !ok || !sameRecord(old, r) {
				ks.pending = append(ks.pending, *r)
			}
		}
	}
	ks.loaded = true
	ks.recs = m
	ks.mtime, ks.size = st.ModTime(), st.Size()
	return nil
}

func sameRecord(a, b *KeyRecord) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// flushPending announces changes made by other processes (offline CLI) so a
// leader can replicate them.
func (ks *Keystore) flushPending() {
	ks.mu.Lock()
	p := ks.pending
	ks.pending = nil
	ks.mu.Unlock()
	if ks.onChange != nil {
		for _, r := range p {
			ks.onChange(r)
		}
	}
}

// Poll checks for changes made by other processes (called periodically by the daemon).
func (ks *Keystore) Poll() { ks.maybeReload() }

// maybeReload picks up changes made by another process (e.g. offline `redx key`).
func (ks *Keystore) maybeReload() {
	ks.mu.RLock()
	recent := ks.opts.Now().Sub(ks.chk) < time.Second
	ks.mu.RUnlock()
	if recent {
		return
	}
	ks.mu.Lock()
	ks.chk = ks.opts.Now()
	_ = ks.reloadLocked(false)
	ks.mu.Unlock()
	ks.flushPending()
}

// mutate runs fn under the inter-process lock on a fresh copy of the file.
func (ks *Keystore) mutate(fn func() (*KeyRecord, error)) (*KeyRecord, error) {
	if err := ks.lock.Lock(); err != nil {
		return nil, err
	}
	defer ks.lock.Unlock()
	rec, err := func() (*KeyRecord, error) {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if err := ks.reloadLocked(true); err != nil {
			return nil, err
		}
		rec, err := fn()
		if err != nil {
			return nil, err
		}
		if err := ks.writeFile(); err != nil {
			return nil, err
		}
		return rec, nil
	}()
	ks.flushPending()
	return rec, err
}

func randBase62(n int) string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	s := new(big.Int).SetBytes(buf).Text(62)
	// big.Int base62 alphabet is 0-9a-zA-Z.
	for len(s) < n {
		s = "0" + s
	}
	return s[len(s)-n:]
}

func hashKey(key string, salt []byte, mem, t uint32, p uint8) []byte {
	return argon2.IDKey([]byte(key), salt, t, mem, p, 32)
}

// ParseTTL accepts Go durations plus d (days) and w (weeks) suffixes; "" or "0" or "never" = 0.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" || s == "never" || s == "none" {
		return 0, nil
	}
	mult := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour}
	if m, ok := mult[s[len(s)-1]]; ok {
		n, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad ttl %q", s)
		}
		return time.Duration(n * float64(m)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad ttl %q", s)
	}
	return d, nil
}

// Create makes a new key. The returned key string is the only copy.
func (ks *Keystore) Create(sp CreateSpec) (string, KeyRecord, error) {
	return ks.create(sp, "")
}

func (ks *Keystore) create(sp CreateSpec, rotatedFrom string) (string, KeyRecord, error) {
	if sp.DB == "" {
		sp.DB = AllDBs
	}
	if sp.DB != AllDBs && !ValidDBName(sp.DB) {
		return "", KeyRecord{}, fmt.Errorf("invalid db %q", sp.DB)
	}
	if scopeRank[sp.Scope] == 0 {
		return "", KeyRecord{}, fmt.Errorf("scope must be read|write|admin")
	}
	if len(sp.Label) > 128 || len(sp.Prefix) > 1024 {
		return "", KeyRecord{}, errors.New("label/prefix too long")
	}
	if err := validRoles(sp.Roles); err != nil {
		return "", KeyRecord{}, err
	}
	var key string
	rec, err := ks.mutate(func() (*KeyRecord, error) {
		for i := 0; i < 8; i++ {
			body := randBase62(keyBodyLen)
			if _, clash := ks.recs[body[:keyIDLen]]; clash {
				continue
			}
			key = KeyPrefix + body
			salt := make([]byte, 16)
			_, _ = rand.Read(salt)
			now := ks.opts.Now().UTC().Truncate(time.Second)
			r := &KeyRecord{ID: body[:keyIDLen], Salt: base64.RawStdEncoding.EncodeToString(salt),
				Mem: ks.opts.ArgonMem, Time: ks.opts.ArgonTime, Threads: 1,
				Scope: sp.Scope, DB: sp.DB, Prefix: sp.Prefix, Label: sp.Label, Roles: append([]string(nil), sp.Roles...), Created: now,
				RateRPS: sp.RateRPS, RateBurst: sp.RateBurst, RotatedFrom: rotatedFrom}
			r.Hash = base64.RawStdEncoding.EncodeToString(hashKey(key, salt, r.Mem, r.Time, r.Threads))
			if sp.TTL > 0 {
				e := now.Add(sp.TTL)
				r.Expires = &e
			}
			ks.recs[r.ID] = r
			return r, nil
		}
		return nil, errors.New("could not allocate key id")
	})
	if err != nil {
		return "", KeyRecord{}, err
	}
	if ks.onChange != nil {
		ks.onChange(*rec)
	}
	return key, *rec, nil
}

func parseKey(key string) (id string, err error) {
	if !strings.HasPrefix(key, KeyPrefix) {
		return "", ErrKeyMalformed
	}
	body := key[len(KeyPrefix):]
	if len(body) != keyBodyLen {
		return "", ErrKeyMalformed
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", ErrKeyMalformed
		}
	}
	return body[:keyIDLen], nil
}

var dummySalt = bytes.Repeat([]byte{7}, 16)

// Verify authenticates a presented key and returns a copy of its record.
// Failure reasons are for the audit log only; callers must answer generically.
func (ks *Keystore) Verify(key string) (*KeyRecord, error) {
	id, err := parseKey(key)
	if err != nil {
		return nil, err
	}
	ks.maybeReload()
	now := ks.opts.Now()
	ks.mu.RLock()
	rec := ks.recs[id]
	var cp KeyRecord
	if rec != nil {
		cp = *rec
	}
	ks.mu.RUnlock()

	mac := hmac.New(sha256.New, ks.cacheKey[:])
	mac.Write([]byte(key))
	var ck [32]byte
	copy(ck[:], mac.Sum(nil))

	if rec == nil {
		// Burn comparable CPU so unknown ids are not trivially distinguishable by timing.
		_ = hashKey(key, dummySalt, ks.opts.ArgonMem, ks.opts.ArgonTime, 1)
		return nil, ErrKeyUnknown
	}
	ok := false
	ks.cmu.Lock()
	if ce, hit := ks.cache[ck]; hit && now.Sub(ce.at) < verifyCacheTTL && ce.id == id && ce.hash == cp.Hash {
		ok = true
	}
	ks.cmu.Unlock()
	if !ok {
		salt, e1 := base64.RawStdEncoding.DecodeString(cp.Salt)
		want, e2 := base64.RawStdEncoding.DecodeString(cp.Hash)
		if e1 != nil || e2 != nil {
			return nil, ErrKeyBad
		}
		got := hashKey(key, salt, cp.Mem, cp.Time, cp.Threads)
		if subtle.ConstantTimeCompare(got, want) != 1 {
			return nil, ErrKeyBad
		}
		ks.cmu.Lock()
		if len(ks.cache) > 4096 {
			ks.cache = map[[32]byte]cacheEnt{}
		}
		ks.cache[ck] = cacheEnt{id: id, hash: cp.Hash, at: now}
		ks.cmu.Unlock()
	}
	if err := cp.Active(now); err != nil {
		return &cp, err
	}
	return &cp, nil
}

// Get returns a copy of a record.
func (ks *Keystore) Get(id string) (KeyRecord, bool) {
	ks.maybeReload()
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	r, ok := ks.recs[id]
	if !ok {
		return KeyRecord{}, false
	}
	return *r, true
}

// List returns all records sorted by creation time.
func (ks *Keystore) List() []KeyRecord {
	ks.maybeReload()
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	out := make([]KeyRecord, 0, len(ks.recs))
	for _, r := range ks.recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.Before(out[j].Created) || (out[i].Created.Equal(out[j].Created) && out[i].ID < out[j].ID)
	})
	return out
}

// Revoke marks a key revoked (idempotent). Takes effect immediately in this process.
func (ks *Keystore) Revoke(id string) (KeyRecord, error) {
	rec, err := ks.mutate(func() (*KeyRecord, error) {
		r, ok := ks.recs[id]
		if !ok {
			return nil, ErrKeyNotFound
		}
		if r.Revoked == nil {
			t := ks.opts.Now().UTC().Truncate(time.Second)
			r.Revoked = &t
		}
		return r, nil
	})
	if err != nil {
		return KeyRecord{}, err
	}
	if ks.onChange != nil {
		ks.onChange(*rec)
	}
	return *rec, nil
}

// Rotate issues a replacement with identical attributes. The old key is revoked
// after grace (0 = immediately) by setting its expiry.
func (ks *Keystore) Rotate(id string, grace time.Duration) (string, KeyRecord, error) {
	old, ok := ks.Get(id)
	if !ok {
		return "", KeyRecord{}, ErrKeyNotFound
	}
	if err := old.Active(ks.opts.Now()); err != nil {
		return "", KeyRecord{}, fmt.Errorf("cannot rotate: %w", err)
	}
	sp := CreateSpec{DB: old.DB, Scope: old.Scope, Prefix: old.Prefix, Label: old.Label, Roles: old.Roles, RateRPS: old.RateRPS, RateBurst: old.RateBurst}
	if old.Expires != nil {
		if rem := old.Expires.Sub(old.Created); rem > 0 {
			sp.TTL = rem
		}
	}
	key, nr, err := ks.create(sp, old.ID)
	if err != nil {
		return "", KeyRecord{}, err
	}
	if grace <= 0 {
		if _, err := ks.Revoke(id); err != nil {
			return "", KeyRecord{}, err
		}
	} else {
		rec, err := ks.mutate(func() (*KeyRecord, error) {
			r, ok := ks.recs[id]
			if !ok {
				return nil, ErrKeyNotFound
			}
			e := ks.opts.Now().UTC().Add(grace).Truncate(time.Second)
			if r.Expires == nil || e.Before(*r.Expires) {
				r.Expires = &e
			}
			return r, nil
		})
		if err != nil {
			return "", KeyRecord{}, err
		}
		if ks.onChange != nil {
			ks.onChange(*rec)
		}
	}
	return key, nr, nil
}

// Upsert installs a record received through replication (no onChange callback).
func (ks *Keystore) Upsert(r KeyRecord) error {
	_, err := ks.mutate(func() (*KeyRecord, error) {
		cp := r
		ks.recs[r.ID] = &cp
		return &cp, nil
	})
	return err
}

// Count returns the number of records.
func (ks *Keystore) Count() int {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return len(ks.recs)
}

// Path returns the keystore file path.
func (ks *Keystore) Path() string { return ks.path }

// ReplaceAll replaces every record (snapshot install / restore).
func (ks *Keystore) ReplaceAll(recs []KeyRecord) error {
	_, err := ks.mutate(func() (*KeyRecord, error) {
		ks.recs = make(map[string]*KeyRecord, len(recs))
		for i := range recs {
			r := recs[i]
			ks.recs[r.ID] = &r
		}
		return nil, nil
	})
	ks.cmu.Lock()
	ks.cache = map[[32]byte]cacheEnt{}
	ks.cmu.Unlock()
	return err
}

var roleNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func validRoles(roles []string) error {
	if len(roles) > 16 {
		return errors.New("at most 16 roles per key")
	}
	for _, r := range roles {
		if !roleNameRe.MatchString(r) {
			return fmt.Errorf("invalid role name %q (letters, digits, _ . : -; max 64)", r)
		}
	}
	return nil
}
