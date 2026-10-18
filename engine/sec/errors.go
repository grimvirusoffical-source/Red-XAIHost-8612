package sec

import "errors"

// Errors. Use errors.Is.
var (
	// ErrBadPolicy: the .red-xai-sec document is malformed or inconsistent.
	ErrBadPolicy = errors.New("sec: bad policy")
	// ErrDenied is returned by Policy.Check when Authorize says no.
	ErrDenied = errors.New("sec: access denied")

	// ErrBadKey: a master key is not 32 bytes or has an invalid id.
	ErrBadKey = errors.New("sec: bad key")
	// ErrUnknownKey: the ciphertext names a master key id the key source does not have.
	ErrUnknownKey = errors.New("sec: unknown master key id")
	// ErrAuth: authentication failed. Wrong master key, wrong context, or the
	// ciphertext was modified, reordered or spliced.
	ErrAuth = errors.New("sec: authentication failed (wrong key, wrong context or tampered data)")
	// ErrTruncated: the ciphertext ends early or has bytes after its end.
	ErrTruncated = errors.New("sec: truncated ciphertext")
	// ErrFormat: not a RedX encrypted file or an unsupported version.
	ErrFormat = errors.New("sec: bad encrypted-file format")
)
