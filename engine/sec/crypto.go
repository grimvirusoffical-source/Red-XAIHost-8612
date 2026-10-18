package sec

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Encrypted-file layout (all integers big-endian):
//
//	header:
//	  "RXSEC" | version=1 | flags=0 | keyIDLen(1) | keyID |
//	  fileID(16) | segSize(4) | salt(16) | wrapNonce(12) | wrappedKey(48)
//	then one or more segments, each:
//	  ctLen(4) | nonce(12) | ciphertext+tag (ctLen bytes, 16..segSize+16)
//
// wrappedKey is AES-256-GCM(wrapKey, wrapNonce, dataKey) where
// wrapKey = HMAC-SHA256-based HKDF(masterKey, salt, "redx-sec wrap v1") is
// unique per wrap (random salt). The wrap's additional data is the header up
// to and including segSize, plus the caller's context, so the key id, file
// id and segment size are authenticated. Each segment is AES-256-GCM under
// the file's data key with a fresh random 96-bit nonce; its additional data
// is the file id, segment size, segment index, a final-segment flag and the
// context. That makes reordering, dropping, duplicating or appending
// segments, and moving data to another file or context, fail authentication.
//
// Rewrap replaces only the header (new salt, nonce, wrapped key, key id), so
// segments stay valid: the segment additional data does not mention the key id.
const (
	magic              = "RXSEC"
	formatVersion      = 1
	DefaultSegmentSize = 64 << 10
	MinSegmentSize     = 16
	MaxSegmentSize     = 1 << 20
	maxReadSegmentSize = 1 << 24
	gcmTag             = 16
	gcmNonce           = 12
	fileIDLen          = 16
	saltLen            = 16
	wrappedLen         = KeySize + gcmTag
)

// Options for Encrypt and friends.
type Options struct {
	// Context is additional authenticated data that is not stored: typically
	// the file name or record address. Decrypt must be given the same bytes.
	// It stops ciphertext being moved to another file or record.
	Context []byte
	// SegmentSize is the plaintext bytes per segment (default 64 KiB).
	SegmentSize int
}

type header struct {
	keyID   string
	fileID  [fileIDLen]byte
	segSize uint32
	salt    [saltLen]byte
	nonce   [gcmNonce]byte
	wrapped [wrappedLen]byte
}

func (h *header) aadPrefix() []byte {
	b := make([]byte, 0, 8+len(h.keyID)+fileIDLen+4)
	b = append(b, magic...)
	b = append(b, formatVersion, 0, byte(len(h.keyID)))
	b = append(b, h.keyID...)
	b = append(b, h.fileID[:]...)
	b = binary.BigEndian.AppendUint32(b, h.segSize)
	return b
}

func (h *header) marshal() []byte {
	b := h.aadPrefix()
	b = append(b, h.salt[:]...)
	b = append(b, h.nonce[:]...)
	b = append(b, h.wrapped[:]...)
	return b
}

func withContext(prefix, ctx []byte) []byte {
	out := make([]byte, 0, len(prefix)+4+len(ctx))
	out = append(out, prefix...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(ctx)))
	return append(out, ctx...)
}

func readHeader(r io.Reader) (*header, error) {
	var fixed [8]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return nil, headerErr(err)
	}
	if string(fixed[:5]) != magic {
		return nil, fmt.Errorf("%w: bad magic", ErrFormat)
	}
	if fixed[5] != formatVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrFormat, fixed[5])
	}
	if fixed[6] != 0 {
		return nil, fmt.Errorf("%w: unknown flags", ErrFormat)
	}
	n := int(fixed[7])
	if n == 0 || n > maxKeyIDLen {
		return nil, fmt.Errorf("%w: bad key id length", ErrFormat)
	}
	rest := make([]byte, n+fileIDLen+4+saltLen+gcmNonce+wrappedLen)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, headerErr(err)
	}
	h := &header{keyID: string(rest[:n])}
	if !validKeyID(h.keyID) {
		return nil, fmt.Errorf("%w: bad key id", ErrFormat)
	}
	p := n
	p += copy(h.fileID[:], rest[p:])
	h.segSize = binary.BigEndian.Uint32(rest[p:])
	p += 4
	p += copy(h.salt[:], rest[p:])
	p += copy(h.nonce[:], rest[p:])
	copy(h.wrapped[:], rest[p:])
	if h.segSize < MinSegmentSize || h.segSize > maxReadSegmentSize {
		return nil, fmt.Errorf("%w: bad segment size", ErrFormat)
	}
	return h, nil
}

func headerErr(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return fmt.Errorf("%w: header", ErrTruncated)
	}
	return err
}

// deriveWrapKey is HKDF (RFC 5869) with HMAC-SHA256, one output block.
func deriveWrapKey(master *[KeySize]byte, salt []byte) [32]byte {
	ext := hmac.New(sha256.New, salt)
	ext.Write(master[:])
	prk := ext.Sum(nil)
	exp := hmac.New(sha256.New, prk)
	exp.Write([]byte("redx-sec wrap v1"))
	exp.Write([]byte{1})
	var out [32]byte
	copy(out[:], exp.Sum(nil))
	for i := range prk {
		prk[i] = 0
	}
	return out
}

func newGCM(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// wrapDataKey fills h.salt, h.nonce and h.wrapped for dataKey under master.
func (h *header) wrap(master *MasterKey, dataKey []byte, ctx []byte) error {
	h.keyID = master.id
	if _, err := io.ReadFull(rand.Reader, h.salt[:]); err != nil {
		return fmt.Errorf("sec: random: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, h.nonce[:]); err != nil {
		return fmt.Errorf("sec: random: %w", err)
	}
	wk := deriveWrapKey(master.key, h.salt[:])
	defer wipe(wk[:])
	aead, err := newGCM(wk[:])
	if err != nil {
		return err
	}
	out := aead.Seal(nil, h.nonce[:], dataKey, withContext(h.aadPrefix(), ctx))
	copy(h.wrapped[:], out)
	return nil
}

// unwrap returns the data key. The caller wipes it.
func (h *header) unwrap(master *MasterKey, ctx []byte) ([]byte, error) {
	wk := deriveWrapKey(master.key, h.salt[:])
	defer wipe(wk[:])
	aead, err := newGCM(wk[:])
	if err != nil {
		return nil, err
	}
	dk, err := aead.Open(nil, h.nonce[:], h.wrapped[:], withContext(h.aadPrefix(), ctx))
	if err != nil {
		return nil, ErrAuth
	}
	return dk, nil
}

func segAAD(h *header, idx uint64, final bool, ctx []byte) []byte {
	b := make([]byte, 0, 9+fileIDLen+4+8+1+4+len(ctx))
	b = append(b, "RXSEC-seg"...)
	b = append(b, h.fileID[:]...)
	b = binary.BigEndian.AppendUint32(b, h.segSize)
	b = binary.BigEndian.AppendUint64(b, idx)
	if final {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(ctx)))
	return append(b, ctx...)
}

// Encrypt reads all of src and writes the encrypted form to dst, using a new
// random data key wrapped by master. Memory use is two segments. If it
// returns an error, dst holds a partial result that must be discarded.
func Encrypt(dst io.Writer, src io.Reader, master *MasterKey, o Options) error {
	if !master.usable() {
		return fmt.Errorf("%w: nil, destroyed or zero master key", ErrBadKey)
	}
	seg := o.SegmentSize
	if seg == 0 {
		seg = DefaultSegmentSize
	}
	if seg < MinSegmentSize || seg > MaxSegmentSize {
		return fmt.Errorf("sec: SegmentSize must be %d..%d", MinSegmentSize, MaxSegmentSize)
	}
	h := &header{segSize: uint32(seg)}
	if _, err := io.ReadFull(rand.Reader, h.fileID[:]); err != nil {
		return fmt.Errorf("sec: random: %w", err)
	}
	dataKey := make([]byte, KeySize)
	defer wipe(dataKey)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return fmt.Errorf("sec: random: %w", err)
	}
	if err := h.wrap(master, dataKey, o.Context); err != nil {
		return err
	}
	aead, err := newGCM(dataKey)
	if err != nil {
		return err
	}
	if _, err := dst.Write(h.marshal()); err != nil {
		return err
	}
	cur := make([]byte, seg)
	nxt := make([]byte, seg)
	defer wipe(cur)
	defer wipe(nxt)
	var idx uint64
	frame := make([]byte, 0, 4+gcmNonce+seg+gcmTag)
	emit := func(plain []byte, final bool) error {
		frame = frame[:0]
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(plain)+gcmTag))
		frame = frame[:4+gcmNonce]
		if _, err := io.ReadFull(rand.Reader, frame[4:]); err != nil {
			return fmt.Errorf("sec: random: %w", err)
		}
		nonce := append([]byte(nil), frame[4:]...)
		frame = aead.Seal(frame, nonce, plain, segAAD(h, idx, final, o.Context))
		idx++
		_, err := dst.Write(frame)
		return err
	}
	n, rerr := io.ReadFull(src, cur)
	for {
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return emit(cur[:n], true)
		}
		if rerr != nil {
			return rerr
		}
		m, rerr2 := io.ReadFull(src, nxt)
		if rerr2 == io.EOF {
			return emit(cur[:n], true)
		}
		if rerr2 != nil && rerr2 != io.ErrUnexpectedEOF {
			return rerr2
		}
		if err := emit(cur[:n], false); err != nil {
			return err
		}
		cur, nxt = nxt, cur
		n, rerr = m, rerr2
	}
}

// Reader decrypts a stream. It releases plaintext only after the segment it
// belongs to has been authenticated; at the end, io.EOF means the whole file
// was present and authentic.
type Reader struct {
	br    *bufio.Reader
	aead  cipher.AEAD
	h     *header
	ctx   []byte
	idx   uint64
	buf   []byte
	plain []byte
	err   error
}

// NewReader reads the header from src, finds the master key in keys and
// unwraps the data key. A wrong key or context, or a changed header, gives
// ErrAuth; an id that keys does not know gives ErrUnknownKey.
func NewReader(src io.Reader, keys KeySource, o Options) (*Reader, error) {
	br := bufio.NewReaderSize(src, 1<<16)
	h, err := readHeader(br)
	if err != nil {
		return nil, err
	}
	master, ok := keys.Key(h.keyID)
	if !ok || !master.usable() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKey, h.keyID)
	}
	dk, err := h.unwrap(master, o.Context)
	if err != nil {
		return nil, err
	}
	defer wipe(dk)
	aead, err := newGCM(dk)
	if err != nil {
		return nil, err
	}
	return &Reader{br: br, aead: aead, h: h, ctx: append([]byte(nil), o.Context...)}, nil
}

func (r *Reader) nextSegment() error {
	var lenb [4]byte
	if _, err := io.ReadFull(r.br, lenb[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return fmt.Errorf("%w: segment %d", ErrTruncated, r.idx)
		}
		return err
	}
	ctLen := binary.BigEndian.Uint32(lenb[:])
	if ctLen < gcmTag || uint64(ctLen) > uint64(r.h.segSize)+gcmTag {
		return fmt.Errorf("%w: bad segment length", ErrAuth)
	}
	need := gcmNonce + int(ctLen)
	if cap(r.buf) < need {
		r.buf = make([]byte, need)
	}
	buf := r.buf[:need]
	if _, err := io.ReadFull(r.br, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return fmt.Errorf("%w: segment %d", ErrTruncated, r.idx)
		}
		return err
	}
	_, perr := r.br.Peek(1)
	final := perr == io.EOF
	if perr != nil && perr != io.EOF {
		return perr
	}
	nonce, ct := buf[:gcmNonce], buf[gcmNonce:]
	pt, err := r.aead.Open(r.plain[:0], nonce, ct, segAAD(r.h, r.idx, final, r.ctx))
	if err != nil {
		// Work out whether it was cut short or has extra data: retry with the other flag.
		if _, err2 := r.aead.Open(nil, nonce, ct, segAAD(r.h, r.idx, !final, r.ctx)); err2 == nil {
			if final {
				return fmt.Errorf("%w: stream ends before its final segment", ErrTruncated)
			}
			return fmt.Errorf("%w: unexpected data after the final segment", ErrAuth)
		}
		return ErrAuth
	}
	if len(pt) == 0 && r.idx > 0 {
		return fmt.Errorf("%w: empty non-first segment", ErrFormat)
	}
	r.plain = pt
	r.idx++
	if final {
		r.err = io.EOF // delivered after the buffered plaintext
	}
	return nil
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if err := r.nextSegment(); err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

// Decrypt writes the plaintext of src to dst. On any error, bytes already
// written passed authentication but the file as a whole did not (it may be
// truncated); the caller should discard them.
func Decrypt(dst io.Writer, src io.Reader, keys KeySource, o Options) error {
	r, err := NewReader(src, keys, o)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, r)
	return err
}

// EncryptBytes is Encrypt for in-memory data.
func EncryptBytes(plain []byte, master *MasterKey, o Options) ([]byte, error) {
	var out bytes.Buffer
	if err := Encrypt(&out, bytes.NewReader(plain), master, o); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DecryptBytes is Decrypt for in-memory data. It returns no plaintext on error.
func DecryptBytes(ct []byte, keys KeySource, o Options) ([]byte, error) {
	var out bytes.Buffer
	if err := Decrypt(&out, bytes.NewReader(ct), keys, o); err != nil {
		wipe(out.Bytes())
		return nil, err
	}
	return out.Bytes(), nil
}

// KeyID reads the master key id a ciphertext names, without decrypting. The
// header is unauthenticated at this point; use it to choose a key or to find
// files still on an old key during a rotation.
func KeyID(src io.Reader) (string, error) {
	h, err := readHeader(src)
	if err != nil {
		return "", err
	}
	return h.keyID, nil
}

// Rewrap re-encrypts only the data key: it unwraps it with the key named in
// src's header (found in from) and wraps it under to, then copies the segments
// unchanged. Cost is independent of the file size. The data key itself is not
// changed, so this protects against loss of the old master key only if the
// old wrapped copies are gone; to change the data key as well, Decrypt and
// Encrypt again. The segments are not read or checked here: use Decrypt to
// verify them. The same context as for the original encryption is required.
func Rewrap(dst io.Writer, src io.Reader, from KeySource, to *MasterKey, o Options) error {
	if !to.usable() {
		return fmt.Errorf("%w: nil, destroyed or zero target key", ErrBadKey)
	}
	h, err := readHeader(src)
	if err != nil {
		return err
	}
	old, ok := from.Key(h.keyID)
	if !ok || !old.usable() {
		return fmt.Errorf("%w: %q", ErrUnknownKey, h.keyID)
	}
	dk, err := h.unwrap(old, o.Context)
	if err != nil {
		return err
	}
	defer wipe(dk)
	nh := *h
	if err := nh.wrap(to, dk, o.Context); err != nil {
		return err
	}
	if _, err := dst.Write(nh.marshal()); err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}

// RewrapBytes is Rewrap for in-memory data.
func RewrapBytes(ct []byte, from KeySource, to *MasterKey, o Options) ([]byte, error) {
	var out bytes.Buffer
	if err := Rewrap(&out, bytes.NewReader(ct), from, to, o); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var _ = errors.Is
