package sec

import (
	"crypto/rand"
	"fmt"
	"io"
	"sort"
	"sync"
)

// KeySize is the master key size in bytes (AES-256).
const KeySize = 32

const maxKeyIDLen = 64

func validKeyID(id string) bool {
	if id == "" || len(id) > maxKeyIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// MasterKey is a 32-byte key wrapping per-file data keys, plus a public id
// that is stored in every ciphertext so the right key can be found. The
// caller supplies the bytes; this package never stores them anywhere but
// memory. Every fmt verb, JSON and text marshalling print only the id.
//
// The key bytes sit behind a pointer on purpose: reflection-based printers
// (fmt's %v of a struct that contains a MasterKey by value, spew, ...) then
// show an address instead of the bytes.
type MasterKey struct {
	id  string
	key *[KeySize]byte
}

// NewMasterKey copies key (exactly 32 bytes) under the given id (1 to 64 of
// letters, digits, '_', '-', '.'). The caller should wipe its own copy.
func NewMasterKey(id string, key []byte) (*MasterKey, error) {
	if !validKeyID(id) {
		return nil, fmt.Errorf("%w: key id must be 1-%d characters of letters, digits, '_', '-', '.'", ErrBadKey, maxKeyIDLen)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: key must be %d bytes, got %d", ErrBadKey, KeySize, len(key))
	}
	k := &MasterKey{id: id, key: new([KeySize]byte)}
	copy(k.key[:], key)
	return k, nil
}

// RandomKey returns KeySize random bytes from the system CSPRNG, for callers
// that need a new master key. Nothing is stored.
func RandomKey() ([]byte, error) {
	b := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("sec: random: %w", err)
	}
	return b, nil
}

// ID returns the key id.
func (k *MasterKey) ID() string {
	if k == nil {
		return ""
	}
	return k.id
}

// Destroy overwrites the key bytes. The key is unusable afterwards.
func (k *MasterKey) Destroy() {
	if k == nil {
		return
	}
	if k.key != nil {
		for i := range k.key {
			k.key[i] = 0
		}
	}
	k.id = ""
}

func (k *MasterKey) usable() bool {
	if k == nil || k.id == "" || k.key == nil {
		return false
	}
	var z [KeySize]byte
	return *k.key != z
}

// Redaction: nothing prints the key.

func (k MasterKey) redacted() string {
	return "MasterKey(id=" + k.id + ", key=REDACTED)"
}

// String implements fmt.Stringer.
func (k MasterKey) String() string { return k.redacted() }

// GoString implements fmt.GoStringer.
func (k MasterKey) GoString() string { return k.redacted() }

// Format implements fmt.Formatter so that %v, %+v, %#v, %s, %x, %q and the
// rest all print the redacted form.
func (k MasterKey) Format(f fmt.State, verb rune) { io.WriteString(f, k.redacted()) }

// MarshalText refuses: a key must not end up in JSON, logs or config dumps.
func (k MasterKey) MarshalText() ([]byte, error) {
	return nil, fmt.Errorf("%w: master keys cannot be marshalled", ErrBadKey)
}

// MarshalJSON refuses, see MarshalText.
func (k MasterKey) MarshalJSON() ([]byte, error) { return k.MarshalText() }

// KeySource finds a master key by id.
type KeySource interface {
	Key(id string) (*MasterKey, bool)
}

// Keyring holds master keys by id and names the one used for new
// encryptions. During a rotation it holds the old key (still needed to
// read) and the new one (current). Safe for concurrent use.
type Keyring struct {
	mu      sync.RWMutex
	keys    map[string]*MasterKey
	current string
}

// NewKeyring creates a keyring. The first key added becomes current.
func NewKeyring(keys ...*MasterKey) (*Keyring, error) {
	r := &Keyring{keys: map[string]*MasterKey{}}
	for _, k := range keys {
		if err := r.Add(k); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add adds a key. A duplicate id is an error (it could silently swap keys).
func (r *Keyring) Add(k *MasterKey) error {
	if !k.usable() {
		return fmt.Errorf("%w: nil, destroyed or zero key", ErrBadKey)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.keys[k.id]; dup {
		return fmt.Errorf("%w: duplicate key id %q", ErrBadKey, k.id)
	}
	r.keys[k.id] = k
	if r.current == "" {
		r.current = k.id
	}
	return nil
}

// SetCurrent chooses the key used for new encryptions and rewraps.
func (r *Keyring) SetCurrent(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.keys[id]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKey, id)
	}
	r.current = id
	return nil
}

// Remove forgets a key (for the end of a rotation). The current key cannot be removed.
func (r *Keyring) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == r.current {
		return fmt.Errorf("%w: cannot remove the current key", ErrBadKey)
	}
	delete(r.keys, id)
	return nil
}

// Key implements KeySource.
func (r *Keyring) Key(id string) (*MasterKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[id]
	return k, ok
}

// Current returns the key used for new encryptions.
func (r *Keyring) Current() (*MasterKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k, ok := r.keys[r.current]
	if !ok {
		return nil, fmt.Errorf("%w: keyring is empty", ErrUnknownKey)
	}
	return k, nil
}

// IDs lists the key ids, sorted (never the keys).
func (r *Keyring) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.keys))
	for id := range r.keys {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
