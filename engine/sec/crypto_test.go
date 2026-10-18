package sec

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func key(t testing.TB, id string, fill byte) *MasterKey {
	t.Helper()
	b := bytes.Repeat([]byte{fill}, KeySize)
	k, err := NewMasterKey(id, b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func ringOf(t testing.TB, ks ...*MasterKey) *Keyring {
	t.Helper()
	r, err := NewKeyring(ks...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRoundTripSizes(t *testing.T) {
	k := key(t, "k1", 0x11)
	ring := ringOf(t, k)
	const seg = 32
	for _, n := range []int{0, 1, 15, 16, 31, 32, 33, 63, 64, 65, 1000} {
		plain := make([]byte, n)
		rand.Read(plain)
		ct, err := EncryptBytes(plain, k, Options{SegmentSize: seg, Context: []byte("ctx")})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if bytes.Contains(ct, plain) && n > 8 {
			t.Fatalf("n=%d: plaintext visible in ciphertext", n)
		}
		got, err := DecryptBytes(ct, ring, Options{Context: []byte("ctx")})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: mismatch", n)
		}
		// segments: ceil(n/seg) with a minimum of one
		wantSegs := (n + seg - 1) / seg
		if wantSegs == 0 {
			wantSegs = 1
		}
		if got := len(parseSegments(t, ct)); got != wantSegs {
			t.Fatalf("n=%d: %d segments, want %d", n, got, wantSegs)
		}
	}
}

func TestDefaultSegmentAndStreaming(t *testing.T) {
	k := key(t, "k1", 0x22)
	ring := ringOf(t, k)
	plain := make([]byte, 3*DefaultSegmentSize+123)
	rand.Read(plain)
	var ct bytes.Buffer
	if err := Encrypt(&ct, iotest.OneByteReader(bytes.NewReader(plain)), k, Options{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Decrypt(&out, iotest.OneByteReader(bytes.NewReader(ct.Bytes())), ring, Options{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), plain) {
		t.Fatal("mismatch")
	}
	// reader works with small reads and ends with io.EOF
	r, err := NewReader(bytes.NewReader(ct.Bytes()), ring, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := iotest.TestReader(r, plain); err != nil {
		t.Fatal(err)
	}
}

func TestBadOptionsAndKeys(t *testing.T) {
	k := key(t, "k1", 1)
	if _, err := EncryptBytes([]byte("x"), k, Options{SegmentSize: 3}); err == nil {
		t.Fatal("tiny segment size")
	}
	if _, err := EncryptBytes([]byte("x"), k, Options{SegmentSize: MaxSegmentSize + 1}); err == nil {
		t.Fatal("huge segment size")
	}
	if _, err := EncryptBytes([]byte("x"), nil, Options{}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("nil key: %v", err)
	}
	if _, err := NewMasterKey("k", make([]byte, 31)); !errors.Is(err, ErrBadKey) {
		t.Fatal("short key")
	}
	if _, err := NewMasterKey("bad id", make([]byte, 32)); !errors.Is(err, ErrBadKey) {
		t.Fatal("bad id")
	}
	if _, err := NewMasterKey("", make([]byte, 32)); !errors.Is(err, ErrBadKey) {
		t.Fatal("empty id")
	}
	z, _ := NewMasterKey("zero", make([]byte, 32))
	if _, err := EncryptBytes([]byte("x"), z, Options{}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("all-zero key must be refused: %v", err)
	}
	k.Destroy()
	if _, err := EncryptBytes([]byte("x"), k, Options{}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("destroyed key: %v", err)
	}
	r := ringOf(t, key(t, "a", 2))
	if err := r.Add(key(t, "a", 3)); !errors.Is(err, ErrBadKey) {
		t.Fatal("duplicate id must be refused")
	}
	if err := r.Remove("a"); err == nil {
		t.Fatal("cannot remove the current key")
	}
	if err := r.SetCurrent("zzz"); !errors.Is(err, ErrUnknownKey) {
		t.Fatal("set current unknown")
	}
	rk, err := RandomKey()
	if err != nil || len(rk) != KeySize || bytes.Equal(rk, make([]byte, KeySize)) {
		t.Fatal("RandomKey")
	}
}

func TestWrongKey(t *testing.T) {
	k := key(t, "main", 0x33)
	ct, err := EncryptBytes([]byte("secret game state"), k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// same id, different bytes
	if _, err := DecryptBytes(ct, ringOf(t, key(t, "main", 0x34)), Options{}); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key bytes: want ErrAuth, got %v", err)
	}
	// unknown id
	if _, err := DecryptBytes(ct, ringOf(t, key(t, "other", 0x33)), Options{}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown id: want ErrUnknownKey, got %v", err)
	}
	// empty keyring
	if _, err := DecryptBytes(ct, ringOf(t), Options{}); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("empty ring: %v", err)
	}
	// wrong context
	if _, err := DecryptBytes(ct, ringOf(t, k), Options{Context: []byte("other-file")}); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong context: want ErrAuth, got %v", err)
	}
	// errors never carry key bytes
	_, err = DecryptBytes(ct, ringOf(t, key(t, "main", 0x34)), Options{})
	if strings.Contains(err.Error(), hex.EncodeToString(bytes.Repeat([]byte{0x34}, 4))) {
		t.Fatal("error mentions key material")
	}
	// not our format
	if _, err := DecryptBytes([]byte("hello world, definitely not encrypted"), ringOf(t, k), Options{}); !errors.Is(err, ErrFormat) {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := DecryptBytes(nil, ringOf(t, k), Options{}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("empty input: %v", err)
	}
}

// Every single-bit flip in every byte must be rejected.
func TestTamperEveryByte(t *testing.T) {
	k := key(t, "k1", 0x44)
	ring := ringOf(t, k)
	plain := []byte(strings.Repeat("0123456789", 12)) // 120 bytes, 4 segments of 32
	ct, err := EncryptBytes(plain, k, Options{SegmentSize: 32, Context: []byte("f")})
	if err != nil {
		t.Fatal(err)
	}
	for i := range ct {
		for _, bit := range []byte{0x01, 0x80} {
			mod := append([]byte(nil), ct...)
			mod[i] ^= bit
			got, err := DecryptBytes(mod, ring, Options{Context: []byte("f")})
			if err == nil {
				t.Fatalf("flip byte %d bit %#x was not detected", i, bit)
			}
			if got != nil {
				t.Fatalf("flip byte %d: plaintext returned with error", i)
			}
		}
	}
}

func TestTruncationAppendReorder(t *testing.T) {
	k := key(t, "k1", 0x55)
	ring := ringOf(t, k)
	plain := bytes.Repeat([]byte("abcdefgh"), 20) // 160 bytes, 5 segments of 32
	ct, err := EncryptBytes(plain, k, Options{SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	segs := parseSegments(t, ct)
	if len(segs) != 5 {
		t.Fatalf("segments: %d", len(segs))
	}
	hdrLen := len(ct) - totalLen(segs)
	// every prefix is rejected
	for n := 0; n < len(ct); n++ {
		if _, err := DecryptBytes(ct[:n], ring, Options{}); err == nil {
			t.Fatalf("prefix of %d bytes accepted", n)
		}
	}
	// cut exactly at a segment boundary: must say truncated
	cut := hdrLen + totalLen(segs[:3])
	if _, err := DecryptBytes(ct[:cut], ring, Options{}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("cut at boundary: want ErrTruncated, got %v", err)
	}
	// appended bytes and an appended duplicate segment
	if _, err := DecryptBytes(append(append([]byte(nil), ct...), 0), ring, Options{}); err == nil {
		t.Fatal("trailing byte accepted")
	}
	dup := append(append([]byte(nil), ct...), segs[4].raw...)
	if _, err := DecryptBytes(dup, ring, Options{}); err == nil {
		t.Fatal("duplicated final segment accepted")
	}
	// swapped segments
	sw := append([]byte(nil), ct[:hdrLen]...)
	order := []int{1, 0, 2, 3, 4}
	for _, i := range order {
		sw = append(sw, segs[i].raw...)
	}
	if _, err := DecryptBytes(sw, ring, Options{}); !errors.Is(err, ErrAuth) {
		t.Fatalf("swapped segments: %v", err)
	}
	// dropped middle segment
	dr := append([]byte(nil), ct[:hdrLen]...)
	for i, s := range segs {
		if i != 2 {
			dr = append(dr, s.raw...)
		}
	}
	if _, err := DecryptBytes(dr, ring, Options{}); err == nil {
		t.Fatal("dropped segment accepted")
	}
	// segments of another file under the same key cannot be spliced in
	other, _ := EncryptBytes(plain, k, Options{SegmentSize: 32})
	os := parseSegments(t, other)
	sp := append([]byte(nil), ct[:hdrLen]...)
	for i, s := range segs {
		if i == 2 {
			sp = append(sp, os[2].raw...)
		} else {
			sp = append(sp, s.raw...)
		}
	}
	if _, err := DecryptBytes(sp, ring, Options{}); !errors.Is(err, ErrAuth) {
		t.Fatalf("spliced segment: %v", err)
	}
	// a segment that claims an oversized length is rejected without allocating it
	big := append([]byte(nil), ct[:hdrLen]...)
	big = binary.BigEndian.AppendUint32(big, 1<<30)
	if _, err := DecryptBytes(big, ring, Options{}); err == nil {
		t.Fatal("oversize segment accepted")
	}
	// unmodified still decrypts
	if got, err := DecryptBytes(ct, ring, Options{}); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("control: %v", err)
	}
}

// Nonces must not repeat: across encryptions of the same data and across the
// segments of one file, and a repeated wrap nonce or salt would be a bug too.
func TestNonceUniqueness(t *testing.T) {
	k := key(t, "k1", 0x66)
	plain := bytes.Repeat([]byte{7}, 100)
	seen := map[string]string{}
	add := func(kind string, b []byte) {
		s := string(b)
		if prev, dup := seen[s]; dup {
			t.Fatalf("%s %x repeated (first seen as %s)", kind, b, prev)
		}
		seen[s] = kind
	}
	const files = 300
	fileIDs := map[string]bool{}
	for i := 0; i < files; i++ {
		ct, err := EncryptBytes(plain, k, Options{SegmentSize: 16}) // 7 segments each
		if err != nil {
			t.Fatal(err)
		}
		h, err := readHeader(bytes.NewReader(ct))
		if err != nil {
			t.Fatal(err)
		}
		if fileIDs[string(h.fileID[:])] {
			t.Fatal("file id repeated")
		}
		fileIDs[string(h.fileID[:])] = true
		add("wrap nonce", h.nonce[:])
		add("wrap salt", h.salt[:])
		for j, s := range parseSegments(t, ct) {
			add(fmt.Sprintf("file %d segment %d nonce", i, j), s.nonce)
		}
	}
	if len(seen) != files*(2+7) {
		t.Fatalf("saw %d distinct values, want %d", len(seen), files*9)
	}
	// identical plaintext must give different ciphertexts, including wrapped keys
	a, _ := EncryptBytes(plain, k, Options{})
	b, _ := EncryptBytes(plain, k, Options{})
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions are identical")
	}
	ha, _ := readHeader(bytes.NewReader(a))
	hb, _ := readHeader(bytes.NewReader(b))
	if ha.wrapped == hb.wrapped {
		t.Fatal("per-file data keys are not unique")
	}
}

func TestRewrap(t *testing.T) {
	oldK := key(t, "k2025", 0x77)
	newK := key(t, "k2026", 0x78)
	plain := bytes.Repeat([]byte("rotate me "), 50)
	ctx := Options{Context: []byte("players.red-xai.db"), SegmentSize: 64}
	ct, err := EncryptBytes(plain, oldK, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := KeyID(bytes.NewReader(ct)); id != "k2025" {
		t.Fatalf("KeyID %q", id)
	}
	// rewrap needs the old key
	if _, err := RewrapBytes(ct, ringOf(t, newK), newK, ctx); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("rewrap without old key: %v", err)
	}
	// and the right context
	if _, err := RewrapBytes(ct, ringOf(t, oldK), newK, Options{Context: []byte("other")}); !errors.Is(err, ErrAuth) {
		t.Fatalf("rewrap with wrong context: %v", err)
	}
	// and a wrong old key with the right id
	if _, err := RewrapBytes(ct, ringOf(t, key(t, "k2025", 0x01)), newK, ctx); !errors.Is(err, ErrAuth) {
		t.Fatalf("rewrap with wrong old key: %v", err)
	}
	ct2, err := RewrapBytes(ct, ringOf(t, oldK), newK, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := KeyID(bytes.NewReader(ct2)); id != "k2026" {
		t.Fatalf("KeyID after rewrap %q", id)
	}
	// segments are byte-identical: only the header changed
	s1, s2 := parseSegments(t, ct), parseSegments(t, ct2)
	if len(s1) != len(s2) {
		t.Fatal("segment count changed")
	}
	for i := range s1 {
		if !bytes.Equal(s1[i].raw, s2[i].raw) {
			t.Fatalf("segment %d changed by rewrap", i)
		}
	}
	// new key alone reads it; old key alone no longer can
	if got, err := DecryptBytes(ct2, ringOf(t, newK), ctx); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypt with new key: %v", err)
	}
	if _, err := DecryptBytes(ct2, ringOf(t, oldK), ctx); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("old key must not open the rewrapped file: %v", err)
	}
	// during a rotation a ring with both reads both generations
	both := ringOf(t, oldK, newK)
	for _, c := range [][]byte{ct, ct2} {
		if got, err := DecryptBytes(c, both, ctx); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("rotation ring: %v", err)
		}
	}
	// rewrap twice, and tampering after rewrap is still detected
	ct3, err := RewrapBytes(ct2, both, oldK, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecryptBytes(ct3, both, ctx); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("second rewrap: %v", err)
	}
	ct2[len(ct2)-1] ^= 1
	if _, err := DecryptBytes(ct2, both, ctx); err == nil {
		t.Fatal("tamper after rewrap undetected")
	}
	// Rewrap of a header that was tampered is refused
	bad := append([]byte(nil), ct...)
	bad[9] ^= 1 // inside the key id / file id area
	if _, err := RewrapBytes(bad, ringOf(t, oldK), newK, ctx); err == nil {
		t.Fatal("rewrap accepted a tampered header")
	}
	// the keyring current key is what callers pass to Rewrap during rotation
	ring := ringOf(t, oldK)
	if err := ring.Add(newK); err != nil {
		t.Fatal(err)
	}
	if err := ring.SetCurrent("k2026"); err != nil {
		t.Fatal(err)
	}
	cur, _ := ring.Current()
	if cur.ID() != "k2026" || strings.Join(ring.IDs(), ",") != "k2025,k2026" {
		t.Fatal("keyring")
	}
	if err := ring.Remove("k2025"); err != nil {
		t.Fatal(err)
	}
}

func TestKeyMaterialNeverPrinted(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, KeySize)
	k, _ := NewMasterKey("main", raw)
	forms := []string{
		fmt.Sprintf("%v", k), fmt.Sprintf("%+v", k), fmt.Sprintf("%#v", k), fmt.Sprintf("%s", k),
		fmt.Sprintf("%x", k), fmt.Sprintf("%X", k), fmt.Sprintf("%q", k), fmt.Sprintf("%d", k),
		fmt.Sprint(k), fmt.Sprintf("%v", *k), fmt.Sprintf("%+v", []*MasterKey{k}),
		fmt.Sprintf("%v", map[string]*MasterKey{"a": k}), fmt.Sprintf("%v", struct{ K *MasterKey }{k}),
		k.String(), k.GoString(),
	}
	needles := []string{hex.EncodeToString(raw), strings.ToUpper(hex.EncodeToString(raw)), string(raw), "171 171", "0xab", "[171"}
	for _, f := range forms {
		for _, n := range needles {
			if strings.Contains(f, n) {
				t.Fatalf("output %q contains key material %q", f, n)
			}
		}
		if !strings.Contains(f, "REDACTED") && !strings.Contains(f, "MasterKey") {
			t.Fatalf("unexpected form %q", f)
		}
	}
	if _, err := json.Marshal(k); err == nil {
		t.Fatal("json.Marshal of a key must fail")
	}
	if _, err := json.Marshal(map[string]interface{}{"k": k}); err == nil {
		t.Fatal("json.Marshal of a struct holding a key must fail")
	}
	// Keyring prints no keys either, and listing ids leaks nothing
	ring := ringOf(t, k)
	if s := fmt.Sprintf("%v %+v", ring.IDs(), ring.IDs()); strings.Contains(s, "ab") && strings.Contains(s, hex.EncodeToString(raw)) {
		t.Fatal("ids leak")
	}
	// caller-side mutation of the input slice must not change the key
	b := bytes.Repeat([]byte{9}, KeySize)
	k2, _ := NewMasterKey("k2", b)
	ct, _ := EncryptBytes([]byte("x"), k2, Options{})
	for i := range b {
		b[i] = 0
	}
	if got, err := DecryptBytes(ct, ringOf(t, k2), Options{}); err != nil || string(got) != "x" {
		t.Fatalf("key aliases the caller slice: %v", err)
	}
	var nilKey *MasterKey
	_ = fmt.Sprintf("%v", nilKey)
	nilKey.Destroy()
}

// The package must not write to disk or log: it imports neither os, log, net nor io/ioutil.
func TestNoFilesLogsOrNetwork(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{`"os"`, `"log"`, `"net"`, `"net/http"`, `"io/ioutil"`, `"os/exec"`, `"log/slog"`} {
			if bytes.Contains(src, []byte("\t"+banned+"\n")) {
				t.Errorf("%s imports %s", f, banned)
			}
		}
		for _, banned := range []string{"fmt.Print", "println(", "print("} {
			if bytes.Contains(src, []byte(banned)) {
				t.Errorf("%s calls %s", f, banned)
			}
		}
	}
}

type seg struct {
	raw   []byte
	nonce []byte
}

func totalLen(ss []seg) int {
	n := 0
	for _, s := range ss {
		n += len(s.raw)
	}
	return n
}

// parseSegments splits ct after its header (test helper, uses internals).
func parseSegments(t testing.TB, ct []byte) []seg {
	t.Helper()
	r := bytes.NewReader(ct)
	if _, err := readHeader(r); err != nil {
		t.Fatal(err)
	}
	var out []seg
	for r.Len() > 0 {
		var l [4]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			t.Fatal(err)
		}
		n := int(binary.BigEndian.Uint32(l[:]))
		body := make([]byte, gcmNonce+n)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		out = append(out, seg{raw: append(l[:], body...), nonce: body[:gcmNonce]})
	}
	return out
}

func BenchmarkEncrypt1MiB(b *testing.B) {
	k := key(b, "k", 1)
	plain := make([]byte, 1<<20)
	b.SetBytes(int64(len(plain)))
	for i := 0; i < b.N; i++ {
		if err := Encrypt(io.Discard, bytes.NewReader(plain), k, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
